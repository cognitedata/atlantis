// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
)

// NewDraftPolicyCheckCommandRunner constructs a DraftPolicyCheckCommandRunner.
func NewDraftPolicyCheckCommandRunner(
	commitStatusUpdater CommitStatusUpdater,
	prjCommandBuilder ProjectPolicyCheckCommandBuilder,
	prjCommandRunner ProjectPolicyCheckCommandRunner,
	pullUpdater *PullUpdater,
	dbUpdater *DBUpdater,
	silenceVCSStatusNoProjects bool,
) *DraftPolicyCheckCommandRunner {
	return &DraftPolicyCheckCommandRunner{
		commitStatusUpdater:        commitStatusUpdater,
		prjCmdBuilder:              prjCommandBuilder,
		prjCmdRunner:               prjCommandRunner,
		pullUpdater:                pullUpdater,
		dbUpdater:                  dbUpdater,
		silenceVCSStatusNoProjects: silenceVCSStatusNoProjects,
	}
}

// DraftPolicyCheckCommandRunner handles the manually-triggered "atlantis
// draft_policy_check" comment command.
// Runs policy checks against the current .draftplan file.
// Does not affected whether a plan can be applied or not.
type DraftPolicyCheckCommandRunner struct {
	commitStatusUpdater        CommitStatusUpdater
	pullUpdater                *PullUpdater
	dbUpdater                  *DBUpdater
	prjCmdBuilder              ProjectPolicyCheckCommandBuilder
	prjCmdRunner               ProjectPolicyCheckCommandRunner
	silenceVCSStatusNoProjects bool
}

func (d *DraftPolicyCheckCommandRunner) Run(ctx *command.Context, cmd *CommentCommand) {
	baseRepo := ctx.Pull.BaseRepo
	pull := ctx.Pull

	if err := d.commitStatusUpdater.UpdateCombined(ctx.Log, baseRepo, pull, models.PendingCommitStatus, command.DraftPolicyCheck); err != nil {
		ctx.Log.Warn("unable to update commit status: %s", err)
	}

	projectCmds, err := d.prjCmdBuilder.BuildPolicyCheckCommands(ctx, cmd)
	if err != nil {
		if statusErr := d.commitStatusUpdater.UpdateCombined(ctx.Log, baseRepo, pull, models.FailedCommitStatus, command.DraftPolicyCheck); statusErr != nil {
			ctx.Log.Warn("unable to update commit status: %s", statusErr)
		}
		d.pullUpdater.updatePull(ctx, cmd, command.Result{Error: err})
		return
	}

	if len(projectCmds) == 0 {
		ctx.Log.Info("determined there was no project to run draft_policy_check in")
		if !d.silenceVCSStatusNoProjects {
			ctx.Log.Debug("setting VCS status to success with no projects found")
			if err := d.commitStatusUpdater.UpdateCombinedCount(ctx.Log, baseRepo, pull, models.SuccessCommitStatus, command.DraftPolicyCheck, 0, 0); err != nil {
				ctx.Log.Warn("unable to update commit status: %s", err)
			}
		}
		return
	}

	result := runProjectCmds(projectCmds, d.prjCmdRunner.PolicyCheck)

	d.pullUpdater.updatePull(ctx, cmd, result)

	pullStatus, err := d.dbUpdater.updateDB(ctx, pull, result.ProjectResults)
	if err != nil {
		ctx.Log.Err("writing results: %s", err)
		return
	}

	d.updateCommitStatus(ctx, pullStatus)
}

func (d *DraftPolicyCheckCommandRunner) updateCommitStatus(ctx *command.Context, pullStatus models.PullStatus) {
	numSuccess := pullStatus.StatusCount(models.PassedPolicyCheckStatus)
	numErrored := pullStatus.StatusCount(models.ErroredPolicyCheckStatus)

	status := models.SuccessCommitStatus
	if numErrored > 0 {
		status = models.FailedCommitStatus
	}

	if err := d.commitStatusUpdater.UpdateCombinedCount(ctx.Log, ctx.Pull.BaseRepo, ctx.Pull, status, command.DraftPolicyCheck, numSuccess, len(pullStatus.Projects)); err != nil {
		ctx.Log.Warn("unable to update commit status: %s", err)
	}
}
