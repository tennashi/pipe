import {
  Box,
  IconButton,
  Link,
  makeStyles,
  Menu,
  MenuItem,
  TableCell,
  TableRow,
} from "@material-ui/core";
import MenuIcon from "@material-ui/icons/MoreVert";
import clsx from "clsx";
import dayjs from "dayjs";
import { FC, memo, useState } from "react";
import { useSelector } from "react-redux";
import { Link as RouterLink } from "react-router-dom";
import { APPLICATION_KIND_TEXT } from "../../constants/application-kind";
import { PAGE_PATH_APPLICATIONS } from "../../constants/path";
import { UI_TEXT_NOT_AVAILABLE_TEXT } from "../../constants/ui-text";
import { AppState } from "../../modules";
import { Application, selectById } from "../../modules/applications";
import { selectEnvById } from "../../modules/environments";
import { AppSyncStatus } from "../app-sync-status";

const useStyles = makeStyles((theme) => ({
  root: {
    padding: theme.spacing(2),
    flex: 1,
    overflow: "auto",
  },
  disabled: {
    background: theme.palette.grey[200],
  },
}));

const EmptyDeploymentData: FC = () => (
  <>
    <TableCell>{UI_TEXT_NOT_AVAILABLE_TEXT}</TableCell>
    <TableCell>{UI_TEXT_NOT_AVAILABLE_TEXT}</TableCell>
    <TableCell>{UI_TEXT_NOT_AVAILABLE_TEXT}</TableCell>
    <TableCell>{UI_TEXT_NOT_AVAILABLE_TEXT}</TableCell>
  </>
);

export interface ApplicationListItemProps {
  applicationId: string;
  onEdit: (id: string) => void;
  onEnable: (id: string) => void;
  onDisable: (id: string) => void;
  onDelete: (id: string) => void;
  onEncryptSecret: (id: string) => void;
}

export const ApplicationListItem: FC<ApplicationListItemProps> = memo(
  function ApplicationListItem({
    applicationId,
    onDisable,
    onEdit,
    onEnable,
    onDelete,
    onEncryptSecret,
  }) {
    const classes = useStyles();
    const [anchorEl, setAnchorEl] = useState<HTMLButtonElement | null>(null);
    const app = useSelector<AppState, Application.AsObject | undefined>(
      (state) => selectById(state.applications, applicationId)
    );
    const env = useSelector(selectEnvById(app?.envId));

    const handleEdit = (): void => {
      setAnchorEl(null);
      onEdit(applicationId);
    };

    const handleDisable = (): void => {
      setAnchorEl(null);
      onDisable(applicationId);
    };

    const handleEnable = (): void => {
      setAnchorEl(null);
      onEnable(applicationId);
    };

    const handleDelete = (): void => {
      setAnchorEl(null);
      onDelete(applicationId);
    };

    const handleGenerateSecret = (): void => {
      setAnchorEl(null);
      onEncryptSecret(applicationId);
    };

    if (!app) {
      return null;
    }

    const recentlyDeployment = app.mostRecentlySuccessfulDeployment;

    return (
      <>
        <TableRow className={clsx({ [classes.disabled]: app.disabled })}>
          <TableCell>
            <Box display="flex" alignItems="center">
              <AppSyncStatus
                syncState={app.syncState}
                deploying={app.deploying}
              />
            </Box>
          </TableCell>
          <TableCell>
            <Link
              component={RouterLink}
              to={`${PAGE_PATH_APPLICATIONS}/${app.id}`}
            >
              {app.name}
            </Link>
          </TableCell>
          <TableCell>{APPLICATION_KIND_TEXT[app.kind]}</TableCell>
          <TableCell>{env?.name}</TableCell>
          {recentlyDeployment ? (
            <>
              <TableCell>{recentlyDeployment.version}</TableCell>
              <TableCell>
                {recentlyDeployment.trigger?.commit?.hash.slice(0, 8) ??
                  UI_TEXT_NOT_AVAILABLE_TEXT}
              </TableCell>
              <TableCell>
                {recentlyDeployment.trigger?.commander ||
                  recentlyDeployment.trigger?.commit?.author ||
                  UI_TEXT_NOT_AVAILABLE_TEXT}
              </TableCell>
              <TableCell>
                {dayjs(recentlyDeployment.startedAt * 1000).fromNow()}
              </TableCell>
            </>
          ) : (
            <EmptyDeploymentData />
          )}
          <TableCell align="right">
            <IconButton
              aria-label="Open menu"
              onClick={(e) => {
                setAnchorEl(e.currentTarget);
              }}
            >
              <MenuIcon />
            </IconButton>
          </TableCell>
        </TableRow>

        <Menu
          id="application-menu"
          anchorEl={anchorEl}
          keepMounted
          open={Boolean(anchorEl)}
          onClose={() => setAnchorEl(null)}
          PaperProps={{
            style: {
              width: "20ch",
            },
          }}
        >
          <MenuItem onClick={handleEdit}>Edit</MenuItem>
          <MenuItem onClick={handleGenerateSecret}>Encrypt Secret</MenuItem>
          {app && app.disabled ? (
            <MenuItem onClick={handleEnable}>Enable</MenuItem>
          ) : (
            <MenuItem onClick={handleDisable}>Disable</MenuItem>
          )}
          <MenuItem onClick={handleDelete}>Delete</MenuItem>
        </Menu>
      </>
    );
  }
);
