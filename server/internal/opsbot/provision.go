package opsbot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// IssueCreator creates the control Epic that backs an Ops Bot's Kanban home.
// server.go adapts *service.KanbanService to this shape so the provisioning
// logic stays free of the kanban service's many create parameters.
type IssueCreator interface {
	CreateEpic(ctx context.Context, columnID int64, title, description string, byUserID int64) (int64, error)
}

// IssueDeleter rolls back a control Epic when a later provisioning step fails.
// Optional: when absent, a failed provisioning returns an error asking the
// caller to retry (a retry sees epic_issue_id still unset and recreates).
type IssueDeleter interface {
	DeleteIssue(ctx context.Context, issueID int64) error
}

// WorkspaceCreator creates the control workspace bound to an Epic.
type WorkspaceCreator interface {
	CreateControl(ctx context.Context, issueID int64, ownerType string, ownerID, createdBy int64) (int64, error)
}

// WorkspaceDeleter rolls back a control workspace when write-back fails.
type WorkspaceDeleter interface {
	DeleteWorkspace(ctx context.Context, workspaceID int64) error
}

// ColumnResolver resolves the board column new control Epics land in.
type ColumnResolver interface {
	FirstColumn(ctx context.Context, projectID int64) (int64, error)
}

// BotStore is the subset of *store.Queries provisioning needs. It is an
// interface so tests stay database-free.
type BotStore interface {
	GetOpsBot(ctx context.Context, id int64) (store.OpsBot, error)
	ListOpsBotsByOwner(ctx context.Context, arg store.ListOpsBotsByOwnerParams) ([]store.OpsBot, error)
	SetOpsBotProvision(ctx context.Context, arg store.SetOpsBotProvisionParams) (store.OpsBot, error)
}

var _ BotStore = (*store.Queries)(nil)

// Provisioner gives every account/director Bot a control Epic plus a control
// workspace, and records the pair on the Bot row. All methods are idempotent:
// re-running fills only the missing half of an existing provisioning.
type Provisioner struct {
	Issues     IssueCreator
	Workspaces WorkspaceCreator
	Columns    ColumnResolver
	Bots       BotStore

	DeleteIssues     IssueDeleter     // optional
	DeleteWorkspaces WorkspaceDeleter // optional
}

// ProvisionReport lists the Bot ids provisioned (or completed) and skipped
// because they were already fully provisioned.
type ProvisionReport struct {
	Provisioned []int64
	Skipped     []int64
}

const (
	accountEpicPrefix  = "运营 Bot："
	directorEpicPrefix = "运营总监："
)

// ProvisionAccount provisions an account Bot's control Epic and workspace.
func (p *Provisioner) ProvisionAccount(ctx context.Context, ownerType string, ownerID, projectID, botID int64, displayName string, byUserID int64) (int64, int64, error) {
	return p.provision(ctx, ownerType, ownerID, projectID, botID, displayName, byUserID, accountEpicPrefix, "账号")
}

// ProvisionDirector provisions a director Bot's control Epic and workspace.
func (p *Provisioner) ProvisionDirector(ctx context.Context, ownerType string, ownerID, projectID, botID int64, displayName string, byUserID int64) (int64, int64, error) {
	return p.provision(ctx, ownerType, ownerID, projectID, botID, displayName, byUserID, directorEpicPrefix, "总监")
}

// EnsureAll provisions every Bot of one owner that is missing either half of
// its provisioning. Fully provisioned Bots are skipped, so repeated calls are
// safe. Bots are processed in id order for deterministic side effects.
func (p *Provisioner) EnsureAll(ctx context.Context, ownerType string, ownerID, projectID, byUserID int64) (ProvisionReport, error) {
	if err := p.validate(); err != nil {
		return ProvisionReport{}, err
	}
	bots, err := p.Bots.ListOpsBotsByOwner(ctx, store.ListOpsBotsByOwnerParams{OwnerType: ownerType, OwnerID: ownerID})
	if err != nil {
		return ProvisionReport{}, fmt.Errorf("opsbot: list bots for %s/%d: %w", ownerType, ownerID, err)
	}
	sort.Slice(bots, func(i, j int) bool { return bots[i].ID < bots[j].ID })

	var report ProvisionReport
	for _, bot := range bots {
		if bot.EpicIssueID.Valid && bot.ControlWorkspaceID.Valid {
			report.Skipped = append(report.Skipped, bot.ID)
			continue
		}
		var err error
		if bot.Role == "director" {
			_, _, err = p.ProvisionDirector(ctx, ownerType, ownerID, projectID, bot.ID, bot.DisplayName, byUserID)
		} else {
			_, _, err = p.ProvisionAccount(ctx, ownerType, ownerID, projectID, bot.ID, bot.DisplayName, byUserID)
		}
		if err != nil {
			return report, err
		}
		report.Provisioned = append(report.Provisioned, bot.ID)
	}
	return report, nil
}

func (p *Provisioner) provision(ctx context.Context, ownerType string, ownerID, projectID, botID int64, displayName string, byUserID int64, titlePrefix, roleLabel string) (int64, int64, error) {
	if err := p.validate(); err != nil {
		return 0, 0, err
	}
	bot, err := p.Bots.GetOpsBot(ctx, botID)
	if err != nil {
		return 0, 0, fmt.Errorf("opsbot: load bot %d: %w", botID, err)
	}
	if bot.OwnerType != ownerType || bot.OwnerID != ownerID {
		return 0, 0, fmt.Errorf("opsbot: bot %d belongs to %s/%d, not %s/%d", botID, bot.OwnerType, bot.OwnerID, ownerType, ownerID)
	}

	epicID := bot.EpicIssueID.Int64
	wsID := bot.ControlWorkspaceID.Int64
	if bot.EpicIssueID.Valid && bot.ControlWorkspaceID.Valid {
		return epicID, wsID, nil
	}

	var createdEpicID, createdWorkspaceID int64
	fail := func(cause error) (int64, int64, error) {
		if cleanupErr := p.rollback(ctx, createdEpicID, createdWorkspaceID); cleanupErr != nil {
			return 0, 0, errors.Join(cause, fmt.Errorf("rollback failed: %w", cleanupErr))
		}
		return 0, 0, cause
	}

	if !bot.EpicIssueID.Valid {
		columnID, err := p.Columns.FirstColumn(ctx, projectID)
		if err != nil {
			return 0, 0, fmt.Errorf("opsbot: resolve first column for project %d: %w", projectID, err)
		}
		description := fmt.Sprintf("%s Bot #%d（%s）。由 opsbot provisioning 自动创建，重复执行不会重复创建。",
			roleLabel, botID, displayName)
		epicID, err = p.Issues.CreateEpic(ctx, columnID, titlePrefix+displayName, description, byUserID)
		if err != nil {
			return 0, 0, fmt.Errorf("opsbot: create control epic for bot %d: %w", botID, err)
		}
		createdEpicID = epicID
	}

	if !bot.ControlWorkspaceID.Valid {
		wsID, err = p.Workspaces.CreateControl(ctx, epicID, ownerType, ownerID, byUserID)
		if err != nil {
			return fail(fmt.Errorf("opsbot: create control workspace for bot %d: %w", botID, err))
		}
		createdWorkspaceID = wsID
	}

	if _, err := p.Bots.SetOpsBotProvision(ctx, store.SetOpsBotProvisionParams{
		EpicIssueID:        sql.NullInt64{Int64: epicID, Valid: true},
		ControlWorkspaceID: sql.NullInt64{Int64: wsID, Valid: true},
		ID:                 botID,
	}); err != nil {
		return fail(fmt.Errorf("opsbot: record provisioning for bot %d: %w", botID, err))
	}
	return epicID, wsID, nil
}

// rollback deletes the resources created by this call, newest first. It only
// touches ids created in the same call, never pre-existing provisioning.
func (p *Provisioner) rollback(ctx context.Context, createdEpicID, createdWorkspaceID int64) error {
	var errs []error
	if createdWorkspaceID != 0 {
		if p.DeleteWorkspaces == nil {
			errs = append(errs, fmt.Errorf("no workspace deleter configured for workspace %d", createdWorkspaceID))
		} else if err := p.DeleteWorkspaces.DeleteWorkspace(ctx, createdWorkspaceID); err != nil {
			errs = append(errs, fmt.Errorf("delete workspace %d: %w", createdWorkspaceID, err))
		}
	}
	if createdEpicID != 0 {
		if p.DeleteIssues == nil {
			errs = append(errs, fmt.Errorf("no issue deleter configured for epic %d", createdEpicID))
		} else if err := p.DeleteIssues.DeleteIssue(ctx, createdEpicID); err != nil {
			errs = append(errs, fmt.Errorf("delete epic %d: %w", createdEpicID, err))
		}
	}
	return errors.Join(errs...)
}

func (p *Provisioner) validate() error {
	switch {
	case p.Issues == nil:
		return errors.New("opsbot: provisioner has no issue creator")
	case p.Workspaces == nil:
		return errors.New("opsbot: provisioner has no workspace creator")
	case p.Columns == nil:
		return errors.New("opsbot: provisioner has no column resolver")
	case p.Bots == nil:
		return errors.New("opsbot: provisioner has no bot store")
	}
	return nil
}
