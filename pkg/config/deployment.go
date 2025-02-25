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

package config

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/pipe-cd/pipe/pkg/model"
)

const (
	defaultWaitApprovalTimeout  = Duration(6 * time.Hour)
	defaultAnalysisQueryTimeout = Duration(30 * time.Second)
)

type GenericDeploymentSpec struct {
	// Forcibly use QuickSync or Pipeline when commit message matched the specified pattern.
	CommitMatcher DeploymentCommitMatcher `json:"commitMatcher"`
	// Pipeline for deploying progressively.
	Pipeline *DeploymentPipeline `json:"pipeline"`
	// The list of sealed secrets that should be decrypted.
	SealedSecrets []SealedSecretMapping `json:"sealedSecrets"`
	// List of directories or files where their changes will trigger the deployment.
	// Regular expression can be used.
	TriggerPaths []string `json:"triggerPaths,omitempty"`
	// The maximum length of time to execute deployment before giving up.
	// Default is 6h.
	Timeout Duration `json:"timeout,omitempty"`
}

func (s *GenericDeploymentSpec) Validate() error {
	if s.Timeout == 0 {
		s.Timeout = Duration(6 * time.Hour)
	}
	if s.Pipeline != nil {
		for _, stage := range s.Pipeline.Stages {
			if stage.AnalysisStageOptions != nil {
				if err := stage.AnalysisStageOptions.Validate(); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s GenericDeploymentSpec) GetStage(index int32) (PipelineStage, bool) {
	if s.Pipeline == nil {
		return PipelineStage{}, false
	}
	if int(index) >= len(s.Pipeline.Stages) {
		return PipelineStage{}, false
	}
	return s.Pipeline.Stages[index], true
}

// HasStage checks if the given stage is included in the pipeline.
func (s GenericDeploymentSpec) HasStage(stage model.Stage) bool {
	if s.Pipeline == nil {
		return false
	}
	for _, s := range s.Pipeline.Stages {
		if s.Name == stage {
			return true
		}
	}
	return false
}

// DeploymentCommitMatcher provides a way to decide how to deploy.
type DeploymentCommitMatcher struct {
	// It makes sure to perform syncing if the commit message matches this regular expression.
	QuickSync string `json:"quickSync"`
	// It makes sure to perform pipeline if the commit message matches this regular expression.
	Pipeline string `json:"pipeline"`
}

// DeploymentPipeline represents the way to deploy the application.
// The pipeline is triggered by changes in any of the following objects:
// - Target PodSpec (Target can be Deployment, DaemonSet, StatefulSet)
// - ConfigMaps, Secrets that are mounted as volumes or envs in the deployment.
type DeploymentPipeline struct {
	Stages []PipelineStage `json:"stages"`
}

// PipelineStage represents a single stage of a pipeline.
// This is used as a generic struct for all stage type.
type PipelineStage struct {
	Id      string
	Name    model.Stage
	Desc    string
	Timeout Duration

	WaitStageOptions         *WaitStageOptions
	WaitApprovalStageOptions *WaitApprovalStageOptions
	AnalysisStageOptions     *AnalysisStageOptions

	K8sPrimaryRolloutStageOptions  *K8sPrimaryRolloutStageOptions
	K8sCanaryRolloutStageOptions   *K8sCanaryRolloutStageOptions
	K8sCanaryCleanStageOptions     *K8sCanaryCleanStageOptions
	K8sBaselineRolloutStageOptions *K8sBaselineRolloutStageOptions
	K8sBaselineCleanStageOptions   *K8sBaselineCleanStageOptions
	K8sTrafficRoutingStageOptions  *K8sTrafficRoutingStageOptions

	TerraformSyncStageOptions  *TerraformSyncStageOptions
	TerraformPlanStageOptions  *TerraformPlanStageOptions
	TerraformApplyStageOptions *TerraformApplyStageOptions

	CloudRunSyncStageOptions    *CloudRunSyncStageOptions
	CloudRunPromoteStageOptions *CloudRunPromoteStageOptions

	LambdaSyncStageOptions          *LambdaSyncStageOptions
	LambdaCanaryRolloutStageOptions *LambdaCanaryRolloutStageOptions
	LambdaPromoteStageOptions       *LambdaPromoteStageOptions
}

type genericPipelineStage struct {
	Id      string          `json:"id"`
	Name    model.Stage     `json:"name"`
	Desc    string          `json:"desc,omitempty"`
	Timeout Duration        `json:"timeout"`
	With    json.RawMessage `json:"with"`
}

func (s *PipelineStage) UnmarshalJSON(data []byte) error {
	var err error
	gs := genericPipelineStage{}
	if err = json.Unmarshal(data, &gs); err != nil {
		return err
	}
	s.Id = gs.Id
	s.Name = gs.Name
	s.Desc = gs.Desc
	s.Timeout = gs.Timeout

	switch s.Name {
	case model.StageWait:
		s.WaitStageOptions = &WaitStageOptions{}
		if len(gs.With) > 0 {
			err = json.Unmarshal(gs.With, s.WaitStageOptions)
		}
	case model.StageWaitApproval:
		s.WaitApprovalStageOptions = &WaitApprovalStageOptions{}
		if len(gs.With) > 0 {
			err = json.Unmarshal(gs.With, s.WaitApprovalStageOptions)
		}
		if s.WaitApprovalStageOptions.Timeout <= 0 {
			s.WaitApprovalStageOptions.Timeout = defaultWaitApprovalTimeout
		}
	case model.StageAnalysis:
		s.AnalysisStageOptions = &AnalysisStageOptions{}
		if len(gs.With) > 0 {
			err = json.Unmarshal(gs.With, s.AnalysisStageOptions)
		}
		for i := 0; i < len(s.AnalysisStageOptions.Metrics); i++ {
			if s.AnalysisStageOptions.Metrics[i].Timeout <= 0 {
				s.AnalysisStageOptions.Metrics[i].Timeout = defaultAnalysisQueryTimeout
			}
		}
	case model.StageK8sPrimaryRollout:
		s.K8sPrimaryRolloutStageOptions = &K8sPrimaryRolloutStageOptions{}
		if len(gs.With) > 0 {
			err = json.Unmarshal(gs.With, s.K8sPrimaryRolloutStageOptions)
		}
	case model.StageK8sCanaryRollout:
		s.K8sCanaryRolloutStageOptions = &K8sCanaryRolloutStageOptions{}
		if len(gs.With) > 0 {
			err = json.Unmarshal(gs.With, s.K8sCanaryRolloutStageOptions)
		}
	case model.StageK8sCanaryClean:
		s.K8sCanaryCleanStageOptions = &K8sCanaryCleanStageOptions{}
		if len(gs.With) > 0 {
			err = json.Unmarshal(gs.With, s.K8sCanaryCleanStageOptions)
		}
	case model.StageK8sBaselineRollout:
		s.K8sBaselineRolloutStageOptions = &K8sBaselineRolloutStageOptions{}
		if len(gs.With) > 0 {
			err = json.Unmarshal(gs.With, s.K8sBaselineRolloutStageOptions)
		}
	case model.StageK8sBaselineClean:
		s.K8sBaselineCleanStageOptions = &K8sBaselineCleanStageOptions{}
		if len(gs.With) > 0 {
			err = json.Unmarshal(gs.With, s.K8sBaselineCleanStageOptions)
		}
	case model.StageK8sTrafficRouting:
		s.K8sTrafficRoutingStageOptions = &K8sTrafficRoutingStageOptions{}
		if len(gs.With) > 0 {
			err = json.Unmarshal(gs.With, s.K8sTrafficRoutingStageOptions)
		}

	case model.StageTerraformSync:
		s.TerraformSyncStageOptions = &TerraformSyncStageOptions{}
		if len(gs.With) > 0 {
			err = json.Unmarshal(gs.With, s.TerraformSyncStageOptions)
		}
	case model.StageTerraformPlan:
		s.TerraformPlanStageOptions = &TerraformPlanStageOptions{}
		if len(gs.With) > 0 {
			err = json.Unmarshal(gs.With, s.TerraformPlanStageOptions)
		}
	case model.StageTerraformApply:
		s.TerraformApplyStageOptions = &TerraformApplyStageOptions{}
		if len(gs.With) > 0 {
			err = json.Unmarshal(gs.With, s.TerraformApplyStageOptions)
		}

	case model.StageCloudRunSync:
		s.CloudRunSyncStageOptions = &CloudRunSyncStageOptions{}
		if len(gs.With) > 0 {
			err = json.Unmarshal(gs.With, s.CloudRunSyncStageOptions)
		}
	case model.StageCloudRunPromote:
		s.CloudRunPromoteStageOptions = &CloudRunPromoteStageOptions{}
		if len(gs.With) > 0 {
			err = json.Unmarshal(gs.With, s.CloudRunPromoteStageOptions)
		}

	case model.StageLambdaSync:
		s.LambdaSyncStageOptions = &LambdaSyncStageOptions{}
		if len(gs.With) > 0 {
			err = json.Unmarshal(gs.With, s.LambdaSyncStageOptions)
		}
	case model.StageLambdaPromote:
		s.LambdaPromoteStageOptions = &LambdaPromoteStageOptions{}
		if len(gs.With) > 0 {
			err = json.Unmarshal(gs.With, s.LambdaPromoteStageOptions)
		}
	case model.StageLambdaCanaryRollout:
		s.LambdaCanaryRolloutStageOptions = &LambdaCanaryRolloutStageOptions{}
		if len(gs.With) > 0 {
			err = json.Unmarshal(gs.With, s.LambdaCanaryRolloutStageOptions)
		}

	default:
		err = fmt.Errorf("unsupported stage name: %s", s.Name)
	}
	return err
}

// WaitStageOptions contains all configurable values for a WAIT stage.
type WaitStageOptions struct {
	Duration Duration `json:"duration"`
}

// WaitStageOptions contains all configurable values for a WAIT_APPROVAL stage.
type WaitApprovalStageOptions struct {
	// The maximum length of time to wait before giving up.
	// Defaults to 6h.
	Timeout   Duration `json:"timeout"`
	Approvers []string `json:"approvers"`
}

// AnalysisStageOptions contains all configurable values for a K8S_ANALYSIS stage.
type AnalysisStageOptions struct {
	// How long the analysis process should be executed.
	Duration Duration `json:"duration"`
	// TODO: Consider about how to handle a pod restart
	// possible count of pod restarting
	RestartThreshold int                          `json:"restartThreshold"`
	Metrics          []TemplatableAnalysisMetrics `json:"metrics"`
	Logs             []TemplatableAnalysisLog     `json:"logs"`
	Https            []TemplatableAnalysisHTTP    `json:"https"`
	Dynamic          AnalysisDynamic              `json:"dynamic"`
}

func (a *AnalysisStageOptions) Validate() error {
	if a.Duration == 0 {
		return fmt.Errorf("the ANALYSIS stage requires duration field")
	}
	return nil
}

type AnalysisTemplateRef struct {
	Name string            `json:"name"`
	Args map[string]string `json:"args"`
}

// TemplatableAnalysisMetrics wraps AnalysisMetrics to allow specify template to use.
type TemplatableAnalysisMetrics struct {
	AnalysisMetrics
	Template AnalysisTemplateRef `json:"template"`
}

// TemplatableAnalysisLog wraps AnalysisLog to allow specify template to use.
type TemplatableAnalysisLog struct {
	AnalysisLog
	Template AnalysisTemplateRef `json:"template"`
}

// TemplatableAnalysisHTTP wraps AnalysisHTTP to allow specify template to use.
type TemplatableAnalysisHTTP struct {
	AnalysisHTTP
	Template AnalysisTemplateRef `json:"template"`
}

type SealedSecretMapping struct {
	// Relative path from the application directory to sealed secret file.
	Path string `json:"path"`
	// The filename for the decrypted secret.
	// Empty means the same name with the sealed secret file.
	OutFilename string `json:"outFilename"`
	// The directory name where to put the decrypted secret.
	// Empty means the same directory with the sealed secret file.
	OutDir string `json:"outDir"`
}
