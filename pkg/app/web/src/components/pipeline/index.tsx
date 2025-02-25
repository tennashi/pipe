import { FC, memo, useCallback, useEffect, useState } from "react";
import {
  makeStyles,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  DialogContentText,
  Button,
  Box,
} from "@material-ui/core";
import { PipelineStage } from "../pipeline-stage";
import { useSelector, useDispatch } from "react-redux";
import { AppState } from "../../modules";
import {
  selectById,
  Deployment,
  Stage,
  approveStage,
  isDeploymentRunning,
} from "../../modules/deployments";
import { fetchStageLog } from "../../modules/stage-logs";
import { updateActiveStage, ActiveStage } from "../../modules/active-stage";
import { ApprovalStage } from "../approval-stage";
import clsx from "clsx";
import { StageStatus } from "pipe/pkg/app/web/model/deployment_pb";
import { METADATA_APPROVED_BY } from "../../constants/metadata-keys";

const WAIT_APPROVAL_NAME = "WAIT_APPROVAL";
const STAGE_HEIGHT = 56;
const APPROVED_STAGE_HEIGHT = 66;

// Find stage that is running or latest
const findDefaultActiveStage = (stages: Stage[]): Stage => {
  const runningStage = stages.find(
    (stage) => stage.status === StageStatus.STAGE_RUNNING
  );

  if (runningStage) {
    return runningStage;
  }

  return stages[stages.length - 1];
};

const useDeploymentStage = (
  deploymentId: string
): [boolean, Stage[][], Stage | null] => {
  const stages: Stage[][] = [];
  const deployment = useSelector<AppState, Deployment.AsObject | undefined>(
    (state) => selectById(state.deployments, deploymentId)
  );

  if (!deployment) {
    return [false, stages, null];
  }

  const visibleStages = deployment.stagesList.filter((stage) => stage.visible);
  const isRunning = isDeploymentRunning(deployment.status);

  stages[0] = visibleStages.filter((stage) => stage.requiresList.length === 0);

  let index = 0;
  while (stages[index].length > 0) {
    const previousIds = stages[index].map((stage) => stage.id);
    index++;
    stages[index] = visibleStages.filter((stage) =>
      stage.requiresList.some((id) => previousIds.includes(id))
    );
  }
  return [isRunning, stages, findDefaultActiveStage(visibleStages)];
};

const LARGE_STAGE_NAMES = ["WAIT_APPROVAL", "K8S_TRAFFIC_ROUTING"];

const useStyles = makeStyles((theme) => ({
  showScrollbar: {
    "&::-webkit-scrollbar": {
      height: "7px",
    },
    "&::-webkit-scrollbar-thumb": {
      borderRadius: 8,
      backgroundColor: "rgba(0,0,0,0.3)",
    },
  },
  pipelineColumn: {
    display: "flex",
    flexDirection: "column",
  },
  stage: {
    display: "flex",
    padding: theme.spacing(2),
  },
  requireLine: {
    position: "relative",
    "&::before": {
      content: '""',
      position: "absolute",
      top: "48%",
      left: -theme.spacing(2),
      borderTop: `2px solid ${theme.palette.divider}`,
      width: theme.spacing(4),
      height: 1,
    },
  },
  requireCurvedLine: {
    position: "relative",
    "&::before": {
      content: '""',
      position: "absolute",
      bottom: "50%",
      left: 0,
      borderLeft: `2px solid ${theme.palette.divider}`,
      borderBottom: `2px solid ${theme.palette.divider}`,
      width: theme.spacing(2),
      height: STAGE_HEIGHT + theme.spacing(4),
    },
  },
  extendRequireLine: {
    "&::before": {
      height: APPROVED_STAGE_HEIGHT + theme.spacing(4),
    },
  },
  approveDialog: {
    display: "flex",
  },
}));

export interface PipelineProps {
  deploymentId: string;
}

const findApprover = (
  metadata: Array<[string, string]>
): string | undefined => {
  const res = metadata.find(([key]) => key === METADATA_APPROVED_BY);

  if (res) {
    return res[1];
  }

  return undefined;
};

export const Pipeline: FC<PipelineProps> = memo(function Pipeline({
  deploymentId,
}) {
  const classes = useStyles();
  const dispatch = useDispatch();
  const [approveTargetId, setApproveTargetId] = useState<string | null>(null);
  const isOpenApproveDialog = Boolean(approveTargetId);
  const [isRunning, stages, defaultActiveStage] = useDeploymentStage(
    deploymentId
  );
  const activeStage = useSelector<AppState, ActiveStage>(
    (state) => state.activeStage
  );

  useEffect(() => {
    if (defaultActiveStage) {
      dispatch(
        updateActiveStage({
          deploymentId,
          stageId: defaultActiveStage.id,
          name: defaultActiveStage.name,
        })
      );
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dispatch, deploymentId, defaultActiveStage === null]);

  useEffect(() => {
    if (activeStage) {
      dispatch(
        fetchStageLog({
          deploymentId,
          stageId: activeStage.stageId,
          offsetIndex: 0,
          retriedCount: 0,
        })
      );
    }
  }, [dispatch, deploymentId, activeStage]);

  const handleOnClickStage = useCallback(
    (stageId: string, stageName: string) => {
      dispatch(updateActiveStage({ deploymentId, stageId, name: stageName }));
    },
    [dispatch, deploymentId]
  );

  const handleApprove = (): void => {
    if (approveTargetId) {
      dispatch(approveStage({ deploymentId, stageId: approveTargetId }));
      setApproveTargetId(null);
    }
  };

  return (
    <Box textAlign="center" overflow="scroll" className={classes.showScrollbar}>
      <Box display="inline-flex">
        {stages.map((stageColumn, columnIndex) => {
          let isPrevStageLarge = false;
          return (
            <div
              className={classes.pipelineColumn}
              key={`pipeline-${columnIndex}`}
            >
              {stageColumn.map((stage, stageIndex) => {
                const approver = findApprover(stage.metadataMap);
                const isActive = activeStage
                  ? activeStage.deploymentId === deploymentId &&
                    activeStage.stageId === stage.id
                  : false;
                const stageComp = (
                  <div
                    key={stage.id}
                    className={clsx(
                      classes.stage,
                      columnIndex > 0
                        ? stageIndex > 0
                          ? clsx(classes.requireCurvedLine, {
                              [classes.extendRequireLine]:
                                Boolean(approver) || isPrevStageLarge,
                            })
                          : classes.requireLine
                        : undefined
                    )}
                  >
                    {stage.name === WAIT_APPROVAL_NAME &&
                    stage.status === StageStatus.STAGE_RUNNING ? (
                      <ApprovalStage
                        id={stage.id}
                        name={stage.name}
                        onClick={() => {
                          setApproveTargetId(stage.id);
                        }}
                        active={isActive}
                      />
                    ) : (
                      <PipelineStage
                        id={stage.id}
                        name={stage.name}
                        status={stage.status}
                        metadata={stage.metadataMap}
                        onClick={handleOnClickStage}
                        active={isActive}
                        approver={approver}
                        isDeploymentRunning={isRunning}
                      />
                    )}
                  </div>
                );
                isPrevStageLarge = LARGE_STAGE_NAMES.includes(stage.name);
                return stageComp;
              })}
            </div>
          );
        })}

        <Dialog
          open={isOpenApproveDialog}
          onClose={() => setApproveTargetId(null)}
        >
          <DialogTitle>Approve stage</DialogTitle>
          <DialogContent>
            <DialogContentText>
              {`To continue deploying, click "APPROVE".`}
            </DialogContentText>
          </DialogContent>
          <DialogActions>
            <Button onClick={() => setApproveTargetId(null)}>CANCEL</Button>
            <Button color="primary" onClick={handleApprove}>
              APPROVE
            </Button>
          </DialogActions>
        </Dialog>
      </Box>
    </Box>
  );
});
