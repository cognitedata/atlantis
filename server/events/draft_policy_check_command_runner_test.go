// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events_test

import (
	"errors"
	"testing"

	. "github.com/petergtz/pegomock/v4"
	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/events/models/testdata"
	"github.com/runatlantis/atlantis/server/logging"
	"github.com/runatlantis/atlantis/server/metrics/metricstest"
	. "github.com/runatlantis/atlantis/testing"
)

// TestDraftPolicyCheckCommandRunner_Run covers two things regressions were
// just introduced and fixed for:
//  1. ProjectResult.PlanStatus() must not panic for command.DraftPolicyCheck
//     (exercised indirectly here via commit status counting).
//  2. Running draft_policy_check must never write to the database, since the
//     DB-backed PullStatus is what ValidateApplyProject's "policies_passed"
//     requirement reads to gate real applies. A failing draft check must not
//     leave any trace there.
func TestDraftPolicyCheckCommandRunner_Run(t *testing.T) {
	logger := logging.NewNoopLogger(t)
	RegisterMockTestingT(t)

	cases := []struct {
		Description     string
		ProjectContexts []command.ProjectContext
		Outputs         []command.ProjectCommandOutput
		ExpStatus       models.CommitStatus
		ExpSucc         int
		ExpTotal        int
	}{
		{
			Description: "all projects pass",
			ProjectContexts: []command.ProjectContext{
				{ProjectName: "First", CommandName: command.DraftPolicyCheck},
				{ProjectName: "Second", CommandName: command.DraftPolicyCheck},
			},
			Outputs: []command.ProjectCommandOutput{
				{PolicyCheckResults: &models.PolicyCheckResults{}},
				{PolicyCheckResults: &models.PolicyCheckResults{}},
			},
			ExpStatus: models.SuccessCommitStatus,
			ExpSucc:   2,
			ExpTotal:  2,
		},
		{
			Description: "one project fails policy check",
			ProjectContexts: []command.ProjectContext{
				{ProjectName: "First", CommandName: command.DraftPolicyCheck},
				{ProjectName: "Second", CommandName: command.DraftPolicyCheck},
			},
			Outputs: []command.ProjectCommandOutput{
				{Failure: "some policy sets did not pass"},
				{PolicyCheckResults: &models.PolicyCheckResults{}},
			},
			ExpStatus: models.FailedCommitStatus,
			ExpSucc:   1,
			ExpTotal:  2,
		},
	}

	for _, c := range cases {
		t.Run(c.Description, func(t *testing.T) {
			setup(t)

			scopeNull := metricstest.NewLoggingScope(t, logger, "atlantis")

			modelPull := models.PullRequest{BaseRepo: testdata.GithubRepo, State: models.OpenPullState, Num: testdata.Pull.Num}
			cmd := &events.CommentCommand{Name: command.DraftPolicyCheck}

			ctx := &command.Context{
				User:     testdata.User,
				Log:      logging.NewNoopLogger(t),
				Scope:    scopeNull,
				Pull:     modelPull,
				HeadRepo: testdata.GithubRepo,
				Trigger:  command.CommentTrigger,
			}

			When(projectCommandBuilder.BuildPolicyCheckCommands(ctx, cmd)).ThenReturn(c.ProjectContexts, nil)
			for i := range c.ProjectContexts {
				When(projectCommandRunner.PolicyCheck(c.ProjectContexts[i])).ThenReturn(c.Outputs[i])
			}

			draftPolicyCheckCommandRunner.Run(ctx, cmd)

			commitUpdater.VerifyWasCalledOnce().UpdateCombinedCount(
				Any[logging.SimpleLogging](),
				Any[models.Repo](),
				Any[models.PullRequest](),
				Eq[models.CommitStatus](c.ExpStatus),
				Eq[command.Name](command.DraftPolicyCheck),
				Eq(c.ExpSucc),
				Eq(c.ExpTotal),
			)

			// Draft results must never be persisted: real applies gate on the
			// DB-backed PullStatus, and draft output is explicitly not final.
			pullStatus, err := dbUpdater.Database.GetPullStatus(modelPull)
			Ok(t, err)
			Assert(t, pullStatus == nil, "draft_policy_check must not write to the database, got %v", pullStatus)
		})
	}
}

func TestDraftPolicyCheckCommandRunner_Run_NoProjects(t *testing.T) {
	logger := logging.NewNoopLogger(t)
	RegisterMockTestingT(t)

	setup(t, func(tc *TestConfig) {
		tc.silenceVCSStatusNoProjects = false
	})

	scopeNull := metricstest.NewLoggingScope(t, logger, "atlantis")
	modelPull := models.PullRequest{BaseRepo: testdata.GithubRepo, State: models.OpenPullState, Num: testdata.Pull.Num}
	cmd := &events.CommentCommand{Name: command.DraftPolicyCheck}

	ctx := &command.Context{
		User:     testdata.User,
		Log:      logging.NewNoopLogger(t),
		Scope:    scopeNull,
		Pull:     modelPull,
		HeadRepo: testdata.GithubRepo,
		Trigger:  command.CommentTrigger,
	}

	When(projectCommandBuilder.BuildPolicyCheckCommands(ctx, cmd)).ThenReturn(nil, nil)

	draftPolicyCheckCommandRunner.Run(ctx, cmd)

	commitUpdater.VerifyWasCalledOnce().UpdateCombinedCount(
		Any[logging.SimpleLogging](),
		Any[models.Repo](),
		Any[models.PullRequest](),
		Eq[models.CommitStatus](models.SuccessCommitStatus),
		Eq[command.Name](command.DraftPolicyCheck),
		Eq(0),
		Eq(0),
	)
}

func TestDraftPolicyCheckCommandRunner_Run_BuildError(t *testing.T) {
	logger := logging.NewNoopLogger(t)
	RegisterMockTestingT(t)

	vcsClient := setup(t)

	scopeNull := metricstest.NewLoggingScope(t, logger, "atlantis")
	modelPull := models.PullRequest{BaseRepo: testdata.GithubRepo, State: models.OpenPullState, Num: testdata.Pull.Num}
	cmd := &events.CommentCommand{Name: command.DraftPolicyCheck}

	ctx := &command.Context{
		User:     testdata.User,
		Log:      logging.NewNoopLogger(t),
		Scope:    scopeNull,
		Pull:     modelPull,
		HeadRepo: testdata.GithubRepo,
		Trigger:  command.CommentTrigger,
	}

	When(projectCommandBuilder.BuildPolicyCheckCommands(ctx, cmd)).ThenReturn(nil, errors.New("no draft plan found for workspace \"default\", dir \".\" – run 'atlantis draftplan' first"))

	draftPolicyCheckCommandRunner.Run(ctx, cmd)

	commitUpdater.VerifyWasCalledOnce().UpdateCombined(
		Any[logging.SimpleLogging](),
		Any[models.Repo](),
		Any[models.PullRequest](),
		Eq[models.CommitStatus](models.FailedCommitStatus),
		Eq[command.Name](command.DraftPolicyCheck),
	)

	vcsClient.VerifyWasCalledOnce().CreateComment(
		Any[logging.SimpleLogging](), Any[models.Repo](), Any[int](), Any[string](), Any[string]())

	pullStatus, err := dbUpdater.Database.GetPullStatus(modelPull)
	Ok(t, err)
	Assert(t, pullStatus == nil, "draft_policy_check must not write to the database, got %v", pullStatus)
}
