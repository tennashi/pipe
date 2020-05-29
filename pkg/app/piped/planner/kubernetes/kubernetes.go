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

package kubernetes

import (
	"context"
	"fmt"
	"strings"
	"time"

	provider "github.com/kapetaniosci/pipe/pkg/app/piped/cloudprovider/kubernetes"
	"github.com/kapetaniosci/pipe/pkg/app/piped/planner"
	"github.com/kapetaniosci/pipe/pkg/model"
)

// Planner plans the deployment pipeline for kubernetes application.
type Planner struct {
}

type registerer interface {
	Register(k model.ApplicationKind, p planner.Planner) error
}

// Register registers this planner into the given registerer.
func Register(r registerer) {
	r.Register(model.ApplicationKind_KUBERNETES, &Planner{})
}

// Plan decides which pipeline should be used for the given input.
func (p *Planner) Plan(ctx context.Context, in planner.Input) (out planner.Output, err error) {
	cfg := in.DeploymentConfig.KubernetesDeploymentSpec
	if cfg == nil {
		err = fmt.Errorf("malfored deployment configuration: missing KubernetesDeploymentSpec")
		return
	}

	// DEBUG
	// Image changed
	//in.MostRecentSuccessfulCommitHash = "626ad85b9c6c02c6409b9aa79ee433fb9b5507d7"
	// Just a scale
	//in.MostRecentSuccessfulCommitHash = "09add0800bffbf61bdedf8fb3ef439d7f1fad100"

	// This is the first time to deploy this application
	// or it was unable to retrieve that value.
	// We just apply all manifests.
	if in.MostRecentSuccessfulCommitHash == "" {
		out.Stages = buildPipeline(time.Now())
		out.Description = fmt.Sprintf("Apply all manifests because it was unable to find the most recent successful commit.")
		return
	}

	// If the commit is a revert one. Let's apply primary to rollback.
	// TODO: Find a better way to determine the revert commit.
	if strings.Contains(in.Deployment.Trigger.Commit.Message, "/pipecd rollback ") {
		out.Stages = buildPipeline(time.Now())
		out.Description = fmt.Sprintf("Rollback from commit %s.", in.MostRecentSuccessfulCommitHash)
		return
	}

	// Load previous deployed manifests and new manifests to compare.
	pv := provider.NewProvider(in.RepoDir, in.AppDir, cfg.Input, in.Logger)
	if err = pv.Init(ctx); err != nil {
		return
	}

	// Load manifests of the new commit.
	newManifests, err := pv.LoadManifests(ctx)
	if err != nil {
		err = fmt.Errorf("failed to load new manifests: %v", err)
		return
	}

	// Checkout to the most recent successful commit to load its manifests.
	err = in.Repo.Checkout(ctx, in.MostRecentSuccessfulCommitHash)
	if err != nil {
		err = fmt.Errorf("failed to checkout to commit %s: %v", in.MostRecentSuccessfulCommitHash, err)
		return
	}

	// Load manifests of the previously applied commit.
	oldManifests, err := pv.LoadManifests(ctx)
	if err != nil {
		err = fmt.Errorf("failed to load previously deployed manifests: %v", err)
		return
	}

	progressive, desc := decideStrategy(oldManifests, newManifests)
	out.Description = desc

	if progressive {
		out.Stages = buildProgressivePipeline(cfg.Pipeline, time.Now())
		return
	}

	out.Stages = buildPipeline(time.Now())
	return
}

func decideStrategy(olds, news []provider.Manifest) (progressive bool, desc string) {
	oldWorkload, ok := findWorkload(olds)
	if !ok {
		desc = "Apply all manifests because it was unable to find the currently running workloads."
		return
	}

	newWorkload, ok := findWorkload(news)
	if !ok {
		desc = "Apply all manifests because it was unable to find workloads in the new manifests."
		return
	}

	// If the workload's pod template was touched
	// do progressive deployment with the specified pipeline.
	var (
		workloadDiffs = provider.Diff(oldWorkload, newWorkload, provider.WithPathPrefix("spec"))
		templateDiffs = workloadDiffs.FindByPrefix("spec.template")
	)
	if len(templateDiffs) > 0 {
		progressive = true

		if msg, changed := checkImageChange(templateDiffs); changed {
			desc = msg
			return
		}

		desc = fmt.Sprintf("Progressive deployment because pod template of workload %s was changed.", newWorkload.Key.Name)
		return
	}

	// If the config/secret was touched
	// we also need to do progressive deployment to check run with the new config/secret content.
	// desc = fmt.Sprintf("Do progressive deployment because configmap %s was updated", "config")

	// Check if this is a scaling commit.
	if msg, changed := checkReplicasChange(workloadDiffs); changed {
		desc = msg
		return
	}

	desc = "Apply all manifests"
	return
}

func findWorkload(manifests []provider.Manifest) (provider.Manifest, bool) {
	for _, m := range manifests {
		if !m.Key.IsDeployment() {
			continue
		}
		return m, true
	}
	return provider.Manifest{}, false
}

func findConfig(manifests []provider.Manifest) []provider.Manifest {
	configs := make([]provider.Manifest, 0)
	for _, m := range manifests {
		if !m.Key.IsConfigMap() && !m.Key.IsSecret() {
			continue
		}
		configs = append(configs, m)
	}
	return configs
}

func checkImageChange(diffList provider.DiffResultList) (string, bool) {
	const containerImageQuery = `^spec.template.spec.containers.\[\d+\].image$`
	imageDiffs := diffList.FindAll(containerImageQuery)

	if len(imageDiffs) == 0 {
		return "", false
	}

	images := make([]string, 0, len(imageDiffs))
	for _, d := range imageDiffs {
		beforeName, beforeTag := parseContainerImage(d.Before)
		afterName, afterTag := parseContainerImage(d.After)

		if beforeName == afterName {
			images = append(images, fmt.Sprintf("image %s from %s to %s", beforeName, beforeTag, afterTag))
		} else {
			images = append(images, fmt.Sprintf("image %s:%s to %s:%s", beforeName, beforeTag, afterName, afterTag))
		}
	}
	desc := fmt.Sprintf("Progressive deployment because of updating %s.", strings.Join(images, ", "))
	return desc, true
}

func checkReplicasChange(diffList provider.DiffResultList) (string, bool) {
	const replicasQuery = `^spec.replicas$`
	diff, found, _ := diffList.Find(replicasQuery)
	if !found {
		return "", false
	}

	desc := fmt.Sprintf("Scale workload from %s to %s.", diff.Before, diff.After)
	return desc, true
}

func parseContainerImage(image string) (name, tag string) {
	parts := strings.Split(image, ":")
	if len(parts) == 2 {
		tag = parts[1]
	}
	paths := strings.Split(parts[0], "/")
	name = paths[len(paths)-1]
	return
}
