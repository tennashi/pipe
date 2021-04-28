// Copyright 2020 The PipeCD Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package piped

import (
	"context"
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/credentials"

	"github.com/pipe-cd/pipe/pkg/admin"
	"github.com/pipe-cd/pipe/pkg/app/api/service/pipedservice"
	"github.com/pipe-cd/pipe/pkg/app/api/service/pipedservice/pipedclientfake"
	"github.com/pipe-cd/pipe/pkg/app/piped/apistore/applicationstore"
	"github.com/pipe-cd/pipe/pkg/app/piped/apistore/commandstore"
	"github.com/pipe-cd/pipe/pkg/app/piped/apistore/deploymentstore"
	"github.com/pipe-cd/pipe/pkg/app/piped/apistore/environmentstore"
	"github.com/pipe-cd/pipe/pkg/app/piped/apistore/eventstore"
	"github.com/pipe-cd/pipe/pkg/app/piped/chartrepo"
	"github.com/pipe-cd/pipe/pkg/app/piped/controller"
	"github.com/pipe-cd/pipe/pkg/app/piped/driftdetector"
	"github.com/pipe-cd/pipe/pkg/app/piped/eventwatcher"
	"github.com/pipe-cd/pipe/pkg/app/piped/livestatereporter"
	"github.com/pipe-cd/pipe/pkg/app/piped/livestatestore"
	"github.com/pipe-cd/pipe/pkg/app/piped/notifier"
	"github.com/pipe-cd/pipe/pkg/app/piped/statsreporter"
	"github.com/pipe-cd/pipe/pkg/app/piped/toolregistry"
	"github.com/pipe-cd/pipe/pkg/app/piped/trigger"
	"github.com/pipe-cd/pipe/pkg/cache/memorycache"
	"github.com/pipe-cd/pipe/pkg/cli"
	"github.com/pipe-cd/pipe/pkg/config"
	"github.com/pipe-cd/pipe/pkg/crypto"
	"github.com/pipe-cd/pipe/pkg/git"
	"github.com/pipe-cd/pipe/pkg/model"
	"github.com/pipe-cd/pipe/pkg/rpc/rpcauth"
	"github.com/pipe-cd/pipe/pkg/rpc/rpcclient"
	"github.com/pipe-cd/pipe/pkg/version"

	// Import to preload all built-in executors to the default registry.
	_ "github.com/pipe-cd/pipe/pkg/app/piped/executor/registry"
	// Import to preload all planners to the default registry.
	_ "github.com/pipe-cd/pipe/pkg/app/piped/planner/registry"
)

type piped struct {
	configFile                           string
	insecure                             bool
	certFile                             string
	adminPort                            int
	toolsDir                             string
	enableDefaultKubernetesCloudProvider bool
	useFakeAPIClient                     bool
	gracePeriod                          time.Duration
	addLoginUserToPasswd                 bool
}

func NewCommand() *cobra.Command {
	home, err := os.UserHomeDir()
	if err != nil {
		panic(fmt.Sprintf("failed to detect the current user's home directory: %v", err))
	}
	p := &piped{
		adminPort:   9085,
		toolsDir:    path.Join(home, ".piped", "tools"),
		gracePeriod: 30 * time.Second,
	}
	cmd := &cobra.Command{
		Use:   "piped",
		Short: "Start running piped.",
		RunE:  cli.WithContext(p.run),
	}

	cmd.Flags().StringVar(&p.configFile, "config-file", p.configFile, "The path to the configuration file.")

	cmd.Flags().BoolVar(&p.insecure, "insecure", p.insecure, "Whether disabling transport security while connecting to control-plane.")
	cmd.Flags().StringVar(&p.certFile, "cert-file", p.certFile, "The path to the TLS certificate file.")
	cmd.Flags().IntVar(&p.adminPort, "admin-port", p.adminPort, "The port number used to run a HTTP server for admin tasks such as metrics, healthz.")

	cmd.Flags().StringVar(&p.toolsDir, "tools-dir", p.toolsDir, "The path to directory where to install needed tools such as kubectl, helm, kustomize.")
	cmd.Flags().BoolVar(&p.useFakeAPIClient, "use-fake-api-client", p.useFakeAPIClient, "Whether the fake api client should be used instead of the real one or not.")
	cmd.Flags().BoolVar(&p.enableDefaultKubernetesCloudProvider, "enable-default-kubernetes-cloud-provider", p.enableDefaultKubernetesCloudProvider, "Whether the default kubernetes provider is enabled or not.")
	cmd.Flags().BoolVar(&p.addLoginUserToPasswd, "add-login-user-to-passwd", p.addLoginUserToPasswd, "Whether to add login user to /etc/passwd")
	cmd.Flags().DurationVar(&p.gracePeriod, "grace-period", p.gracePeriod, "How long to wait for graceful shutdown.")

	cmd.MarkFlagRequired("config-file")

	return cmd
}

func (p *piped) run(ctx context.Context, t cli.Telemetry) (runErr error) {
	group, ctx := errgroup.WithContext(ctx)
	if p.addLoginUserToPasswd {
		if err := p.insertLoginUserToPasswd(ctx); err != nil {
			return err
		}
	}

	// Load piped configuration from specified file.
	cfg, err := p.loadConfig()
	if err != nil {
		t.Logger.Error("failed to load piped configuration", zap.Error(err))
		return err
	}

	// Initialize notifier and add piped events.
	notifier, err := notifier.NewNotifier(cfg, t.Logger)
	if err != nil {
		t.Logger.Error("failed to initialize notifier", zap.Error(err))
		return err
	}
	group.Go(func() error {
		return notifier.Run(ctx)
	})

	// Configure SSH config if needed.
	if cfg.Git.ShouldConfigureSSHConfig() {
		if err := git.AddSSHConfig(cfg.Git); err != nil {
			t.Logger.Error("failed to configure ssh-config", zap.Error(err))
			return err
		}
		t.Logger.Info("successfully configured ssh-config")
	}

	// Initialize default tool registry.
	if err := toolregistry.InitDefaultRegistry(p.toolsDir, t.Logger); err != nil {
		t.Logger.Error("failed to initialize default tool registry", zap.Error(err))
		return err
	}

	// Add configured Helm chart repositories.
	if len(cfg.ChartRepositories) > 0 {
		reg := toolregistry.DefaultRegistry()
		if err := chartrepo.Add(ctx, cfg.ChartRepositories, reg, t.Logger); err != nil {
			t.Logger.Error("failed to add configured chart repositories", zap.Error(err))
			return err
		}
		if len(cfg.ChartRepositories) > 0 {
			if err := chartrepo.Update(ctx, reg, t.Logger); err != nil {
				t.Logger.Error("failed to update Helm chart repositories", zap.Error(err))
				return err
			}
		}
	}

	// Make gRPC client and connect to the API.
	apiClient, err := p.createAPIClient(ctx, cfg.APIAddress, cfg.ProjectID, cfg.PipedID, cfg.PipedKeyFile, t.Logger)
	if err != nil {
		t.Logger.Error("failed to create gRPC client to control plane", zap.Error(err))
		return err
	}

	// Send the newest piped meta to the control-plane.
	if err := p.sendPipedMeta(ctx, apiClient, cfg, t.Logger); err != nil {
		t.Logger.Error("failed to report piped meta to control-plane", zap.Error(err))
		return err
	}

	// Start running admin server.
	{
		var (
			ver   = []byte(version.Get().Version)
			admin = admin.NewAdmin(p.adminPort, p.gracePeriod, t.Logger)
		)

		admin.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
			w.Write(ver)
		})
		admin.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("ok"))
		})
		admin.Handle("/metrics", t.PrometheusMetricsHandler())

		group.Go(func() error {
			return admin.Run(ctx)
		})
	}

	// Start running stats reporter.
	{
		url := fmt.Sprintf("http://localhost:%d/metrics", p.adminPort)
		r := statsreporter.NewReporter(url, apiClient, t.Logger)
		group.Go(func() error {
			return r.Run(ctx)
		})
	}

	// Initialize git client.
	gitClient, err := git.NewClient(cfg.Git.Username, cfg.Git.Email, t.Logger)
	if err != nil {
		t.Logger.Error("failed to initialize git client", zap.Error(err))
		return err
	}
	defer func() {
		if err := gitClient.Clean(); err != nil {
			t.Logger.Error("had an error while cleaning gitClient", zap.Error(err))
		} else {
			t.Logger.Info("successfully cleaned gitClient")
		}
	}()

	// Initialize environment store.
	environmentStore := environmentstore.NewStore(
		apiClient,
		memorycache.NewTTLCache(ctx, 10*time.Minute, time.Minute),
		t.Logger,
	)

	// Start running application store.
	var applicationLister applicationstore.Lister
	{
		store := applicationstore.NewStore(apiClient, p.gracePeriod, t.Logger)
		group.Go(func() error {
			return store.Run(ctx)
		})
		applicationLister = store.Lister()
	}

	// Start running deployment store.
	var deploymentLister deploymentstore.Lister
	{
		store := deploymentstore.NewStore(apiClient, p.gracePeriod, t.Logger)
		group.Go(func() error {
			return store.Run(ctx)
		})
		deploymentLister = store.Lister()
	}

	// Start running command store.
	var commandLister commandstore.Lister
	{
		store := commandstore.NewStore(apiClient, p.gracePeriod, t.Logger)
		group.Go(func() error {
			return store.Run(ctx)
		})
		commandLister = store.Lister()
	}

	// Start running event store.
	var eventGetter eventstore.Getter
	{
		store := eventstore.NewStore(apiClient, p.gracePeriod, t.Logger)
		group.Go(func() error {
			return store.Run(ctx)
		})
		eventGetter = store.Getter()
	}

	// Create memory caches.
	appManifestsCache := memorycache.NewTTLCache(ctx, time.Hour, time.Minute)

	var liveStateGetter livestatestore.Getter
	// Start running application live state store.
	{
		s := livestatestore.NewStore(cfg, applicationLister, p.gracePeriod, t.Logger)
		group.Go(func() error {
			return s.Run(ctx)
		})
		liveStateGetter = s.Getter()
	}

	// Start running application live state reporter.
	{
		r := livestatereporter.NewReporter(applicationLister, liveStateGetter, apiClient, cfg, t.Logger)
		group.Go(func() error {
			return r.Run(ctx)
		})
	}

	decrypter, err := p.initializeSealedSecretDecrypter(cfg)
	if err != nil {
		t.Logger.Error("failed to initialize sealed secret decrypter", zap.Error(err))
		return err
	}

	// Start running application application drift detector.
	{
		d := driftdetector.NewDetector(
			applicationLister,
			gitClient,
			liveStateGetter,
			apiClient,
			appManifestsCache,
			cfg,
			decrypter,
			t.Logger,
		)
		group.Go(func() error {
			return d.Run(ctx)
		})
	}

	// Start running deployment controller.
	{
		c := controller.NewController(
			apiClient,
			gitClient,
			deploymentLister,
			commandLister,
			applicationLister,
			environmentStore,
			livestatestore.LiveResourceLister{Getter: liveStateGetter},
			notifier,
			decrypter,
			cfg,
			appManifestsCache,
			p.gracePeriod,
			t.Logger,
		)

		group.Go(func() error {
			return c.Run(ctx)
		})
	}

	// Start running deployment trigger.
	{
		t := trigger.NewTrigger(
			apiClient,
			gitClient,
			applicationLister,
			commandLister,
			environmentStore,
			notifier,
			cfg,
			p.gracePeriod,
			t.Logger,
		)
		group.Go(func() error {
			return t.Run(ctx)
		})
	}

	{
		// Start running event watcher.
		t := eventwatcher.NewWatcher(
			cfg,
			eventGetter,
			gitClient,
			t.Logger,
		)
		group.Go(func() error {
			return t.Run(ctx)
		})
	}

	// Wait until all piped components have finished.
	// A terminating signal or a finish of any components
	// could trigger the finish of piped.
	// This ensures that all components are good or no one.
	if err := group.Wait(); err != nil {
		t.Logger.Error("failed while running", zap.Error(err))
		return err
	}
	return nil
}

// createAPIClient makes a gRPC client to connect to the API.
func (p *piped) createAPIClient(ctx context.Context, address, projectID, pipedID, pipedKeyFile string, logger *zap.Logger) (pipedservice.Client, error) {
	if p.useFakeAPIClient {
		return pipedclientfake.NewClient(logger), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	pipedKey, err := ioutil.ReadFile(pipedKeyFile)
	if err != nil {
		logger.Error("failed to read piped key file", zap.Error(err))
		return nil, err
	}

	var (
		token   = rpcauth.MakePipedToken(projectID, pipedID, string(pipedKey))
		creds   = rpcclient.NewPerRPCCredentials(token, rpcauth.PipedTokenCredentials, !p.insecure)
		options = []rpcclient.DialOption{
			rpcclient.WithBlock(),
			rpcclient.WithPerRPCCredentials(creds),
		}
	)

	if !p.insecure {
		if p.certFile != "" {
			options = append(options, rpcclient.WithTLS(p.certFile))
		} else {
			config := &tls.Config{}
			options = append(options, rpcclient.WithTransportCredentials(credentials.NewTLS(config)))
		}
	} else {
		options = append(options, rpcclient.WithInsecure())
	}

	client, err := pipedservice.NewClient(ctx, address, options...)
	if err != nil {
		logger.Error("failed to create api client", zap.Error(err))
		return nil, err
	}
	return client, nil
}

// loadConfig reads the Piped configuration data from the specified file.
func (p *piped) loadConfig() (*config.PipedSpec, error) {
	cfg, err := config.LoadFromYAML(p.configFile)
	if err != nil {
		return nil, err
	}
	if cfg.Kind != config.KindPiped {
		return nil, fmt.Errorf("wrong configuration kind for piped: %v", cfg.Kind)
	}
	if p.enableDefaultKubernetesCloudProvider {
		cfg.PipedSpec.EnableDefaultKubernetesCloudProvider()
	}
	return cfg.PipedSpec, nil
}

func (p *piped) initializeSealedSecretDecrypter(cfg *config.PipedSpec) (crypto.Decrypter, error) {
	ssm := cfg.SealedSecretManagement
	if ssm == nil {
		return nil, nil
	}

	switch ssm.Type {
	case model.SealedSecretManagementNone:
		return nil, nil

	case model.SealedSecretManagementSealingKey:
		if ssm.SealingKeyConfig.PrivateKeyFile == "" {
			return nil, fmt.Errorf("sealedSecretManagement.privateKeyFile must be set")
		}
		decrypter, err := crypto.NewHybridDecrypter(ssm.SealingKeyConfig.PrivateKeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize decrypter (%w)", err)
		}
		return decrypter, nil
	case model.SealedSecretManagementGCPKMS:
		return nil, fmt.Errorf("type %q is not implemented yet", ssm.Type.String())

	case model.SealedSecretManagementAWSKMS:
		return nil, fmt.Errorf("type %q is not implemented yet", ssm.Type.String())

	default:
		return nil, fmt.Errorf("unsupported sealed secret management type: %s", ssm.Type.String())
	}
}

func (p *piped) sendPipedMeta(ctx context.Context, client pipedservice.Client, cfg *config.PipedSpec, logger *zap.Logger) error {
	repos := make([]*model.ApplicationGitRepository, 0, len(cfg.Repositories))
	for _, r := range cfg.Repositories {
		repos = append(repos, &model.ApplicationGitRepository{
			Id:     r.RepoID,
			Remote: r.Remote,
			Branch: r.Branch,
		})
	}

	var (
		req = &pipedservice.ReportPipedMetaRequest{
			Version:        version.Get().Version,
			Repositories:   repos,
			CloudProviders: make([]*model.Piped_CloudProvider, 0, len(cfg.CloudProviders)),
		}
		retry = pipedservice.NewRetry(5)
		err   error
	)

	// Configure the list of specified cloud providers.
	for _, cp := range cfg.CloudProviders {
		req.CloudProviders = append(req.CloudProviders, &model.Piped_CloudProvider{
			Name: cp.Name,
			Type: cp.Type.String(),
		})
	}

	// Configure sealed secret management.
	if sm := cfg.SealedSecretManagement; sm != nil {
		switch sm.Type {
		case model.SealedSecretManagementSealingKey:
			publicKey, err := ioutil.ReadFile(sm.SealingKeyConfig.PublicKeyFile)
			if err != nil {
				return fmt.Errorf("failed to read public key for sealed secret management (%w)", err)
			}
			req.SealedSecretEncryption = &model.Piped_SealedSecretEncryption{
				Type:      sm.Type.String(),
				PublicKey: string(publicKey),
			}
		}
	}
	if req.SealedSecretEncryption == nil {
		req.SealedSecretEncryption = &model.Piped_SealedSecretEncryption{
			Type: model.SealedSecretManagementNone.String(),
		}
	}

	for retry.WaitNext(ctx) {
		if _, err = client.ReportPipedMeta(ctx, req); err == nil {
			return nil
		}
		logger.Warn("failed to report piped meta to control-plane, wait to the next retry",
			zap.Int("calls", retry.Calls()),
			zap.Error(err),
		)
	}

	return err
}

func (p *piped) insertLoginUserToPasswd(ctx context.Context) error {
	// FIXME: Run with Go code
	const addLoginUserToPasswdscript = `
# Add login user to $HOME/passwd
export USER_ID=$(id -u)
export GROUP_ID=$(id -g)
grep -v -e ^default -e ^$USER_ID /etc/passwd > "$HOME/passwd"
echo "default:x:${USER_ID}:${GROUP_ID}:Piped User:${HOME}:/sbin/nologin" >> "$HOME/passwd"
export NSS_WRAPPER_PASSWD=${HOME}/passwd
export NSS_WRAPPER_GROUP=/etc/group
`
	return nil
}
