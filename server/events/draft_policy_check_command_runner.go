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
	silenceVCSStatusNoProjects bool,
) *DraftPolicyCheckCommandRunner {
	return &DraftPolicyCheckCommandRunner{
		commitStatusUpdater:        commitStatusUpdater,
		prjCmdBuilder:              prjCommandBuilder,
		prjCmdRunner:               prjCommandRunner,
		pullUpdater:                pullUpdater,
		silenceVCSStatusNoProjects: silenceVCSStatusNoProjects,
	}
}

// DraftPolicyCheckCommandRunner handles the manually-triggered "atlantis
// draft_policy_check" comment command.
// Runs policy checks against the current .draftplan file.
// Does not affected whether a plan can be applied or not.
//
// Unlike other command runners, this one deliberately does not persist its
// results to the database: the DB-backed PullStatus is keyed only by
// workspace/dir/project, not by which command produced the result, and
// ValidateApplyProject's "policies_passed" apply requirement reads that same
// PolicyStatus to gate real applies. Writing draft results there would let a
// failing draft check silently block a real apply.
type DraftPolicyCheckCommandRunner struct {
	commitStatusUpdater        CommitStatusUpdater
	pullUpdater                *PullUpdater
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

	d.updateCommitStatus(ctx, result.ProjectResults)
}

func (d *DraftPolicyCheckCommandRunner) updateCommitStatus(ctx *command.Context, results []command.ProjectResult) {
	numSuccess := 0
	numErrored := 0
	for _, r := range results {
		if r.Error != nil || r.Failure != "" {
			numErrored++
		} else {
			numSuccess++
		}
	}

	status := models.SuccessCommitStatus
	if numErrored > 0 {
		status = models.FailedCommitStatus
	}

	if err := d.commitStatusUpdater.UpdateCombinedCount(ctx.Log, ctx.Pull.BaseRepo, ctx.Pull, status, command.DraftPolicyCheck, numSuccess, len(results)); err != nil {
		ctx.Log.Warn("unable to update commit status: %s", err)
	}
}
