import {
  makeStyles,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableFooter,
  TableHead,
  TablePagination,
  TableRow,
} from "@material-ui/core";
import { FC, memo, useCallback, useState } from "react";
import * as React from "react";
import { useDispatch, useSelector } from "react-redux";
import { AppState } from "../../modules";
import {
  Application,
  enableApplication,
  fetchApplications,
  selectAll,
} from "../../modules/applications";
import { setDeletingAppId } from "../../modules/delete-application";
import { setUpdateTargetId } from "../../modules/update-application";
import { AppDispatch } from "../../store";
import { ApplicationListItem } from "../application-list-item";
import { DeleteApplicationDialog } from "../delete-application-dialog";
import { DisableApplicationDialog } from "../disable-application-dialog";
import { SealedSecretDialog } from "../sealed-secret-dialog";

const useStyles = makeStyles(() => ({
  container: {
    flex: 1,
  },
}));

const PAGER_ROWS_PER_PAGE = [20, 50, { label: "All", value: -1 }];

export interface ApplicationListProps {
  currentPage: number;
  onPageChange: (page: number) => void;
}

export const ApplicationList: FC<ApplicationListProps> = memo(
  function ApplicationList({ currentPage, onPageChange }) {
    const classes = useStyles();
    const dispatch = useDispatch<AppDispatch>();
    const [actionTarget, setActionTarget] = useState<string | null>(null);
    const [dialogState, setDialogState] = useState({
      disabling: false,
      generateSecret: false,
    });
    const [rowsPerPage, setRowsPerPage] = React.useState(20);
    const page = currentPage - 1;

    const applications = useSelector<AppState, Application.AsObject[]>(
      (state) => selectAll(state.applications)
    );

    const closeMenu = useCallback(() => {
      setActionTarget(null);
    }, []);

    const handleOnCloseGenerateDialog = (): void => {
      closeMenu();
      setDialogState({
        ...dialogState,
        generateSecret: false,
      });
    };

    const handleCloseDialog = (): void => {
      closeMenu();
      setDialogState({
        ...dialogState,
        disabling: false,
      });
      dispatch(fetchApplications());
    };

    // Menu item event handler

    const handleEditClick = useCallback(
      (id: string) => {
        closeMenu();
        dispatch(setUpdateTargetId(id));
      },
      [dispatch, closeMenu]
    );

    const handleDisableClick = useCallback(
      (id: string) => {
        setActionTarget(id);
        setDialogState({
          ...dialogState,
          disabling: true,
        });
      },
      [dialogState]
    );

    const handleEnableClick = useCallback(
      (id: string) => {
        dispatch(enableApplication({ applicationId: id })).then(() => {
          dispatch(fetchApplications());
        });
        closeMenu();
      },
      [dispatch, closeMenu]
    );

    const handleDeleteClick = useCallback(
      (id: string) => {
        dispatch(setDeletingAppId(id));
        closeMenu();
      },
      [dispatch, closeMenu]
    );

    const handleEncryptSecretClick = useCallback(
      (id: string) => {
        setActionTarget(id);
        setDialogState({
          ...dialogState,
          generateSecret: true,
        });
      },
      [dialogState]
    );

    return (
      <>
        <TableContainer component={Paper} className={classes.container} square>
          <Table stickyHeader>
            <TableHead>
              <TableRow>
                <TableCell>Status</TableCell>
                <TableCell>Name</TableCell>
                <TableCell>Kind</TableCell>
                <TableCell>Environment</TableCell>
                <TableCell>Running Version</TableCell>
                <TableCell>Running Commit</TableCell>
                <TableCell>Deployed By</TableCell>
                <TableCell>Deployed At</TableCell>
                <TableCell />
              </TableRow>
            </TableHead>
            <TableBody>
              {(rowsPerPage > 0
                ? applications.slice(
                    page * rowsPerPage,
                    page * rowsPerPage + rowsPerPage
                  )
                : applications
              ).map((app) => (
                <ApplicationListItem
                  key={`app-${app.id}`}
                  applicationId={app.id}
                  onEdit={handleEditClick}
                  onDisable={handleDisableClick}
                  onEnable={handleEnableClick}
                  onDelete={handleDeleteClick}
                  onEncryptSecret={handleEncryptSecretClick}
                />
              ))}
            </TableBody>
            <TableFooter>
              <TableRow>
                <TablePagination
                  rowsPerPageOptions={PAGER_ROWS_PER_PAGE}
                  count={applications.length}
                  rowsPerPage={rowsPerPage}
                  page={page}
                  colSpan={9}
                  onChangePage={(_, newPage) => {
                    onPageChange(newPage + 1);
                  }}
                  onChangeRowsPerPage={(e) => {
                    setRowsPerPage(parseInt(e.target.value, 10));
                    onPageChange(1);
                  }}
                />
              </TableRow>
            </TableFooter>
          </Table>
        </TableContainer>

        <DisableApplicationDialog
          open={dialogState.disabling}
          applicationId={actionTarget}
          onDisable={handleCloseDialog}
          onCancel={handleCloseDialog}
        />

        <SealedSecretDialog
          open={Boolean(actionTarget) && dialogState.generateSecret}
          applicationId={actionTarget}
          onClose={handleOnCloseGenerateDialog}
        />

        <DeleteApplicationDialog />
      </>
    );
  }
);
