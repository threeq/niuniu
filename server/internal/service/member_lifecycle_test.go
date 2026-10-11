package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/pgtest"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRevoker records the revoke call so we can assert OrgService.RemoveMember
// invokes the integration reclaim hook.
type fakeRevoker struct {
	called bool
	orgID  int64
	userID int64
}

func (f *fakeRevoker) RevokeMemberIntegrationArtifacts(_ context.Context, orgID, userID int64) error {
	f.called = true
	f.orgID = orgID
	f.userID = userID
	return nil
}

func TestRemoveMemberInvokesIntegrationRevoker(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		f := newApprovalFixture(t, drv)
		rev := &fakeRevoker{}
		f.orgSvc.SetMemberIntegrationRevoker(rev)

		require.NoError(t, f.orgSvc.RemoveMember(context.Background(), f.ownerID, "ap-owner", f.orgID, f.memberID))

		assert.True(t, rev.called, "RemoveMember must call the integration revoker")
		assert.Equal(t, f.orgID, rev.orgID)
		assert.Equal(t, f.memberID, rev.userID)
	})
}

func TestIntegrationMemberRevokerReclaimsArtifacts(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		f := newApprovalFixture(t, drv)
		ctx := context.Background()

		kr, err := crypto.LoadOrCreate(filepath.Join(t.TempDir(), "integration_secret"))
		require.NoError(t, err)
		credSvc := NewExternalCredentialService(f.q, f.db, kr, integration.NewRegistry())
		imbotSvc := NewIMBotService(f.q, f.db, kr, NewAuthz(f.q, f.db), nil)

		revoker := &IntegrationMemberRevoker{
			Approvals:   f.svc,
			Projects:    f.projSvc,
			Credentials: credSvc,
			Onboarding:  imbotSvc,
		}
		f.orgSvc.SetMemberIntegrationRevoker(revoker)

		// 1. a pending approval raised by the member
		req := f.submit(t, ApprovalScopeRelax, f.memberID, 0)

		// 2. a project-admin grant for the member
		projID := newApprovalTestProject(t, f.db, f.orgID, "proj-lifecycle")
		require.NoError(t, f.projSvc.Grant(ctx, f.ownerID, projID, f.memberID))

		// 3. an org credential created by the member, bound to a project source
		var credID int64
		require.NoError(t, store.Wrap(f.db).QueryRowContext(ctx,
			`INSERT INTO external_provider_credentials (owner_type, owner_id, user_id, provider, alias, config)
			 VALUES ('org', ?, ?, 'github', 'member-pat', '{}') RETURNING id`,
			f.orgID, f.memberID).Scan(&credID))
		_, err = store.Wrap(f.db).ExecContext(ctx,
			`INSERT INTO project_external_sources (project_id, provider, source_key, credential_id)
			 VALUES (?, 'github', 'k', ?)`, projID, credID)
		require.NoError(t, err)

		// 4. an unused credential link minted by the member
		var tokenID int64
		require.NoError(t, store.Wrap(f.db).QueryRowContext(ctx,
			`INSERT INTO im_bot_onboarding_tokens (token_hash, project_id, platform, channel_name, connection_mode, issued_by_user_id, expires_at)
			 VALUES ('hash-member', ?, 'lark', 'x', 'stream', ?, ?) RETURNING id`,
			projID, f.memberID, time.Now().Add(10*time.Minute)).Scan(&tokenID))

		// Act: remove the member.
		require.NoError(t, f.orgSvc.RemoveMember(ctx, f.ownerID, "ap-owner", f.orgID, f.memberID))

		// Assert approval invalidated
		got, err := f.svc.Get(ctx, f.adminID, req.ID)
		require.NoError(t, err)
		assert.Equal(t, "invalidated", got.Status, "pending approval must be invalidated")

		// Assert project-admin grant gone
		var pmCount int
		require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM project_members WHERE project_id = ? AND user_id = ?`,
			projID, f.memberID).Scan(&pmCount))
		assert.Equal(t, 0, pmCount)

		// Assert credential + its source binding gone
		var credCount int
		require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM external_provider_credentials WHERE id = ?`, credID).Scan(&credCount))
		assert.Equal(t, 0, credCount)
		var bindCount int
		require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM project_external_sources WHERE credential_id = ?`, credID).Scan(&bindCount))
		assert.Equal(t, 0, bindCount, "binding to the revoked credential must be cleaned up")

		// Assert unused credential link gone
		var tokCount int
		require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM im_bot_onboarding_tokens WHERE id = ?`, tokenID).Scan(&tokCount))
		assert.Equal(t, 0, tokCount)
	})
}

// Removing a member from ONE org must not strip their integration state in
// another org they still belong to.
func TestIntegrationMemberRevokerIsOrgScoped(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		f := newApprovalFixture(t, drv)
		ctx := context.Background()

		kr, err := crypto.LoadOrCreate(filepath.Join(t.TempDir(), "integration_secret"))
		require.NoError(t, err)
		imbotSvc := NewIMBotService(f.q, f.db, kr, NewAuthz(f.q, f.db), nil)
		f.orgSvc.SetMemberIntegrationRevoker(&IntegrationMemberRevoker{
			Approvals: f.svc, Projects: f.projSvc, Onboarding: imbotSvc,
		})

		// Second org the member also belongs to.
		orgB, err := f.orgSvc.Create(ctx, f.ownerID, "ap-owner", "Beta", "beta-"+drv, "")
		require.NoError(t, err)
		require.NoError(t, f.orgSvc.AddMember(ctx, f.ownerID, "ap-owner", orgB.ID, f.memberID, "member"))

		projB := newApprovalTestProject(t, f.db, orgB.ID, "beta-proj")
		require.NoError(t, f.projSvc.Grant(ctx, f.ownerID, projB, f.memberID))
		var tokB int64
		require.NoError(t, store.Wrap(f.db).QueryRowContext(ctx,
			`INSERT INTO im_bot_onboarding_tokens (token_hash, project_id, platform, channel_name, connection_mode, issued_by_user_id, expires_at)
			 VALUES ('h-orgb', ?, 'lark', 'x', 'stream', ?, ?) RETURNING id`,
			projB, f.memberID, time.Now().Add(10*time.Minute)).Scan(&tokB))

		// Remove the member from org A only.
		require.NoError(t, f.orgSvc.RemoveMember(ctx, f.ownerID, "ap-owner", f.orgID, f.memberID))

		var pmCount int
		require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM project_members WHERE project_id = ? AND user_id = ?`,
			projB, f.memberID).Scan(&pmCount))
		assert.Equal(t, 1, pmCount, "org B project grant must survive removal from org A")

		var tokCount int
		require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM im_bot_onboarding_tokens WHERE id = ?`, tokB).Scan(&tokCount))
		assert.Equal(t, 1, tokCount, "org B credential link must survive removal from org A")
	})
}

func TestIntegrationMemberRevokerKeepsUsedOnboardingTokens(t *testing.T) {
	pgtest.ForEachDriver(t, func(t *testing.T, drv string) {
		f := newApprovalFixture(t, drv)
		ctx := context.Background()
		projID := newApprovalTestProject(t, f.db, f.orgID, "proj-used")

		var usedID, unusedID int64
		require.NoError(t, store.Wrap(f.db).QueryRowContext(ctx,
			`INSERT INTO im_bot_onboarding_tokens (token_hash, project_id, platform, channel_name, connection_mode, issued_by_user_id, expires_at, used_at)
			 VALUES ('h-used', ?, 'lark', 'x', 'stream', ?, ?, ?) RETURNING id`,
			projID, f.memberID, time.Now().Add(10*time.Minute), time.Now()).Scan(&usedID))
		require.NoError(t, store.Wrap(f.db).QueryRowContext(ctx,
			`INSERT INTO im_bot_onboarding_tokens (token_hash, project_id, platform, channel_name, connection_mode, issued_by_user_id, expires_at)
			 VALUES ('h-unused', ?, 'lark', 'x', 'stream', ?, ?) RETURNING id`,
			projID, f.memberID, time.Now().Add(10*time.Minute)).Scan(&unusedID))

		imbotSvc := NewIMBotService(f.q, f.db, nil, NewAuthz(f.q, f.db), nil)
		n, err := imbotSvc.RevokeUnusedOnboardingTokensByIssuerInOrg(ctx, f.orgID, f.memberID)
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		var usedCount int
		require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM im_bot_onboarding_tokens WHERE id = ?`, usedID).Scan(&usedCount))
		assert.Equal(t, 1, usedCount, "an already-redeemed link is history, not a live link")
		var unusedCount int
		require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM im_bot_onboarding_tokens WHERE id = ?`, unusedID).Scan(&unusedCount))
		assert.Equal(t, 0, unusedCount)
	})
}
