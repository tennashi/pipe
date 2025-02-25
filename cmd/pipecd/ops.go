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

package main

import (
	"context"
	"net/http"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/pipe-cd/pipe/pkg/admin"
	"github.com/pipe-cd/pipe/pkg/app/ops/firestoreindexensurer"
	"github.com/pipe-cd/pipe/pkg/app/ops/handler"
	"github.com/pipe-cd/pipe/pkg/app/ops/insightcollector"
	"github.com/pipe-cd/pipe/pkg/app/ops/mysqlensurer"
	"github.com/pipe-cd/pipe/pkg/app/ops/orphancommandcleaner"
	"github.com/pipe-cd/pipe/pkg/backoff"
	"github.com/pipe-cd/pipe/pkg/cli"
	"github.com/pipe-cd/pipe/pkg/config"
	"github.com/pipe-cd/pipe/pkg/datastore"
	"github.com/pipe-cd/pipe/pkg/model"
	"github.com/pipe-cd/pipe/pkg/version"
)

type ops struct {
	httpPort               int
	adminPort              int
	gracePeriod            time.Duration
	enableInsightCollector bool
	configFile             string
	gcloudPath             string
}

func NewOpsCommand() *cobra.Command {
	s := &ops{
		httpPort:    9082,
		adminPort:   9085,
		gracePeriod: 15 * time.Second,
	}
	cmd := &cobra.Command{
		Use:   "ops",
		Short: "Start running ops server.",
		RunE:  cli.WithContext(s.run),
	}
	cmd.Flags().IntVar(&s.httpPort, "http-port", s.httpPort, "The port number used to run http server.")
	cmd.Flags().IntVar(&s.adminPort, "admin-port", s.adminPort, "The port number used to run a HTTP server for admin tasks such as metrics, healthz.")
	cmd.Flags().DurationVar(&s.gracePeriod, "grace-period", s.gracePeriod, "How long to wait for graceful shutdown.")
	cmd.Flags().BoolVar(&s.enableInsightCollector, "enableInsightCollector-insight-collector", s.enableInsightCollector, "Enable insight collector.")
	cmd.Flags().StringVar(&s.configFile, "config-file", s.configFile, "The path to the configuration file.")
	cmd.Flags().StringVar(&s.gcloudPath, "gcloud-path", s.gcloudPath, "The path to the gcloud command executable.")
	return cmd
}

func (s *ops) run(ctx context.Context, t cli.Telemetry) error {
	group, ctx := errgroup.WithContext(ctx)

	// Load control plane configuration from the specified file.
	cfg, err := loadConfig(s.configFile)
	if err != nil {
		t.Logger.Error("failed to load control-plane configuration",
			zap.String("config-file", s.configFile),
			zap.Error(err),
		)
		return err
	}

	// Prepare sql database.
	if cfg.Datastore.Type == model.DataStoreMySQL {
		if err := ensureSQLDatabase(ctx, cfg, t.Logger); err != nil {
			t.Logger.Error("failed to ensure prepare SQL database", zap.Error(err))
			return err
		}
	}

	if cfg.Datastore.Type == model.DataStoreFirestore {
		// Create needed composite indexes for Firestore.
		ensurer := firestoreindexensurer.NewIndexEnsurer(
			s.gcloudPath,
			cfg.Datastore.FirestoreConfig.Project,
			cfg.Datastore.FirestoreConfig.CredentialsFile,
			cfg.Datastore.FirestoreConfig.CollectionNamePrefix,
			t.Logger,
		)
		group.Go(func() error {
			return ensurer.CreateIndexes(ctx)
		})
	}

	// Connect to the data store.
	ds, err := createDatastore(ctx, cfg, t.Logger)
	if err != nil {
		t.Logger.Error("failed to create datastore", zap.Error(err))
		return err
	}
	defer func() {
		if err := ds.Close(); err != nil {
			t.Logger.Error("failed to close datastore client", zap.Error(err))
		}
	}()

	// Connect to the file store.
	fs, err := createFilestore(ctx, cfg, t.Logger)
	if err != nil {
		t.Logger.Error("failed to create filestore", zap.Error(err))
		return err
	}
	defer func() {
		if err := fs.Close(); err != nil {
			t.Logger.Error("failed to close filestore client", zap.Error(err))
		}
	}()

	// Starting orphan commands cleaner
	cleaner := orphancommandcleaner.NewOrphanCommandCleaner(ds, t.Logger)
	group.Go(func() error {
		return cleaner.Run(ctx)
	})

	// Starting a cron job for insight collector.
	if s.enableInsightCollector {
		insightCfg := cfg.InsightCollector
		mode := loadCollectorMode(insightCfg)
		collector := insightcollector.NewInsightCollector(ds, fs, mode, t.Logger)

		c := cron.New(cron.WithLocation(time.UTC))
		_, err := c.AddFunc(insightCfg.Schedule, func() {
			s.runDeploymentCollector(ctx, collector, insightCfg, t.Logger)
		})
		if err != nil {
			t.Logger.Error("failed to configure cron job for collecting insight data about deployment", zap.Error(err))
		}
		c.Start()
	}

	// Start running HTTP server.
	{
		handler := handler.NewHandler(s.httpPort, datastore.NewProjectStore(ds), cfg.SharedSSOConfigs, s.gracePeriod, t.Logger)
		group.Go(func() error {
			return handler.Run(ctx)
		})
	}

	// Start running admin server.
	{
		var (
			ver   = []byte(version.Get().Version)
			admin = admin.NewAdmin(s.adminPort, s.gracePeriod, t.Logger)
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

	// Wait until all components have finished.
	// A terminating signal or a finish of any components
	// could trigger the finish of server.
	// This ensures that all components are good or no one.
	if err := group.Wait(); err != nil {
		t.Logger.Error("failed while running", zap.Error(err))
		return err
	}
	return nil
}

func (s *ops) runDeploymentCollector(ctx context.Context, col *insightcollector.InsightCollector, cfg config.ControlPlaneInsightCollector, logger *zap.Logger) {
	var doneNewlyCompleted, doneNewlyCreated bool
	retry := backoff.NewRetry(
		cfg.RetryTime,
		backoff.NewConstant(time.Duration(cfg.RetryIntervalHour)*time.Hour),
	)

	for retry.WaitNext(ctx) {
		if !doneNewlyCompleted {
			start := time.Now()
			if err := col.ProcessNewlyCompletedDeployments(ctx); err != nil {
				logger.Error("failed to process the newly completed deployments while accumulating insight data", zap.Error(err))
			} else {
				logger.Info("successfully processed the newly completed deployments while accumulating insight data", zap.Duration("duration", time.Since(start)))
				doneNewlyCompleted = true
			}
		}

		if !doneNewlyCreated {
			start := time.Now()
			if err := col.ProcessNewlyCreatedDeployments(ctx); err != nil {
				logger.Error("failed to process the newly created deployments while accumulating insight data", zap.Error(err))
			} else {
				logger.Info("successfully processed the newly created deployments while accumulating insight data", zap.Duration("duration", time.Since(start)))
				doneNewlyCreated = true
			}
		}

		if doneNewlyCompleted && doneNewlyCreated {
			return
		}
		logger.Info("will do another try to collect insight data")
	}
}

func loadCollectorMode(cfg config.ControlPlaneInsightCollector) insightcollector.CollectorMetrics {
	metrics := insightcollector.NewCollectorMetrics()
	if !cfg.DisabledMetrics.DeploymentFrequency {
		metrics.Enable(insightcollector.DevelopmentFrequency)
	}
	if !cfg.DisabledMetrics.ChangeFailureRate {
		metrics.Enable(insightcollector.ChangeFailureRate)
	}
	return metrics
}

func ensureSQLDatabase(ctx context.Context, cfg *config.ControlPlaneSpec, logger *zap.Logger) error {
	mysqlEnsurer, err := mysqlensurer.NewMySQLEnsurer(
		cfg.Datastore.MySQLConfig.URL,
		cfg.Datastore.MySQLConfig.Database,
		cfg.Datastore.MySQLConfig.UsernameFile,
		cfg.Datastore.MySQLConfig.PasswordFile,
		logger,
	)
	if err != nil {
		logger.Error("failed to create SQL ensurer instance", zap.Error(err))
		return err
	}
	defer func() {
		// Close connection held by the client.
		if err := mysqlEnsurer.Close(); err != nil {
			logger.Error("failed to close database ensurer connection", zap.Error(err))
		}
	}()

	if err = mysqlEnsurer.Run(ctx); err != nil {
		logger.Error("failed to ensure SQL schema and indexes", zap.Error(err))
		return err
	}

	logger.Info("prepare SQL schema and indexes successfully")
	return nil
}
