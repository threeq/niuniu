// External tests for IntakeSourceService: the ci/email/webhook binding lifecycle
// plus the isolation guarantees that keep these bindings out of the generic
// external-systems surface.
package service_test

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/niuniu-dev/niuniu/internal/intake"
	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

type intakeSourceFixture struct {
	env    *niutest.IsolationEnv
	svc    *service.IntakeSourceService
	creds  *service.ExternalCredentialService
	extSvc *service.ExternalSourceService
	authz  *service.Authz
}

func newIntakeSourceFixture(t *testing.T) *intakeSourceFixture {
	t.Helper()
	env := niutest.NewIsolationEnv(t)
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	require.NoError(t, err)
	reg := integration.NewRegistry()
	creds := service.NewExternalCredentialService(env.Queries(), env.DB, kr, reg)
	authz := service.NewAuthz(env.Queries(), env.DB)
	svc := service.NewIntakeSourceService(env.DB, env.Queries(), creds, authz, nil)
	extSvc := service.NewExternalSourceService(env.Queries(), env.DB, creds, reg, authz, nil)
	return &intakeSourceFixture{env: env, svc: svc, creds: creds, extSvc: extSvc, authz: authz}
}

func (f *intakeSourceFixture) createCI(t *testing.T, projectID int64) (*service.IntakeSourceDTO, string) {
	t.Helper()
	dto, secret, err := f.svc.Create(context.Background(), service.IntakeSourceInput{
		ProjectID:    projectID,
		SourceKind:   "ci",
		Name:         "gitlab-main",
		Mode:         "push",
		Provider:     "gitlab",
		Config:       map[string]any{"events": []any{"ci_failed"}},
		CallerUserID: f.env.UserA,
	})
	require.NoError(t, err)
	return dto, secret
}

func TestIntakeSource_CreateListRotateDelete(t *testing.T) {
	f := newIntakeSourceFixture(t)
	ctx := context.Background()
	project := f.env.NewProject(t, f.env.UserA, "intake-lifecycle")

	dto, secret := f.createCI(t, project.ID)
	require.NotEmpty(t, secret)
	require.Equal(t, "ci", dto.SourceKind)
	require.Equal(t, "gitlab", dto.Provider)
	require.Equal(t, "push", dto.Mode)
	require.True(t, dto.HasSecret)
	require.Equal(t, "/api/integrations/ci/"+strconv.FormatInt(dto.ID, 10), dto.InboundPath)

	list, err := f.svc.List(ctx, f.env.UserA, project.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, dto.ID, list[0].ID)

	// Duplicate create is rejected rather than silently rewriting the binding
	// (which would orphan the first credential).
	_, _, err = f.svc.Create(ctx, service.IntakeSourceInput{
		ProjectID: project.ID, SourceKind: "ci", Name: "gitlab-main", Mode: "push", CallerUserID: f.env.UserA,
	})
	require.ErrorIs(t, err, service.ErrIntakeSourceExists)

	rotated, err := f.svc.RotateSecret(ctx, f.env.UserA, project.ID, dto.ID)
	require.NoError(t, err)
	require.NotEqual(t, secret, rotated)

	rows, err := f.env.Queries().ListProjectExternalSources(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].CredentialID.Valid)
	cred, err := f.creds.GetBoundByID(ctx, rows[0].CredentialID.Int64)
	require.NoError(t, err)
	require.Equal(t, rotated, cred.Config[webhookSecretKeyForTest])

	require.NoError(t, f.svc.Delete(ctx, f.env.UserA, project.ID, dto.ID))
	list, err = f.svc.List(ctx, f.env.UserA, project.ID)
	require.NoError(t, err)
	require.Empty(t, list)

	// The generated credential is cleaned up with the binding.
	creds, err := f.creds.ListByProvider(ctx, "user", f.env.UserA, f.env.UserA, integration.ProviderName("ci"))
	require.NoError(t, err)
	require.Empty(t, creds)
}

func TestIntakeSource_RejectsBadKindAndMode(t *testing.T) {
	f := newIntakeSourceFixture(t)
	project := f.env.NewProject(t, f.env.UserA, "intake-validation")

	_, _, err := f.svc.Create(context.Background(), service.IntakeSourceInput{
		ProjectID: project.ID, SourceKind: "github", Name: "x", CallerUserID: f.env.UserA,
	})
	require.ErrorIs(t, err, service.ErrIntakeSourceInvalid)

	_, _, err = f.svc.Create(context.Background(), service.IntakeSourceInput{
		ProjectID: project.ID, SourceKind: "ci", Name: "x", Mode: "imap", CallerUserID: f.env.UserA,
	})
	require.ErrorIs(t, err, service.ErrIntakeSourceInvalid)
}

func TestIntakeSource_IMAPLoginFailureRejectsCreate(t *testing.T) {
	f := newIntakeSourceFixture(t)
	ctx := context.Background()
	project := f.env.NewProject(t, f.env.UserA, "intake-imap")

	// A credential with no host cannot authenticate; the bind-time probe must
	// fail before any binding/poll-state row is written.
	cred, err := f.creds.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: f.env.UserA, UserID: f.env.UserA,
		Provider:  integration.ProviderName("imap"),
		Alias:     "broken-mailbox",
		RawConfig: map[string]any{"username": "alice", "password": "secret"},
	})
	require.NoError(t, err)

	_, _, err = f.svc.Create(ctx, service.IntakeSourceInput{
		ProjectID:    project.ID,
		SourceKind:   "email",
		Name:         "support",
		Mode:         "imap",
		CredentialID: cred.ID,
		CallerUserID: f.env.UserA,
	})
	var authErr *service.ErrIMAPAuth
	require.ErrorAs(t, err, &authErr, "missing host must fail the login probe")

	list, err := f.svc.List(ctx, f.env.UserA, project.ID)
	require.NoError(t, err)
	require.Empty(t, list)

	pollStates, err := f.env.Queries().ListEmailPollStatesByProject(ctx, project.ID)
	require.NoError(t, err)
	require.Empty(t, pollStates)
}

func TestIntakeSource_IMAPRejectsNonImapCredential(t *testing.T) {
	f := newIntakeSourceFixture(t)
	ctx := context.Background()
	project := f.env.NewProject(t, f.env.UserA, "intake-imap-provider")

	cred, err := f.creds.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: f.env.UserA, UserID: f.env.UserA,
		Provider:  integration.ProviderGitHub,
		Alias:     "github-not-imap",
		RawConfig: map[string]any{"token": "x"},
	})
	require.NoError(t, err)

	_, _, err = f.svc.Create(ctx, service.IntakeSourceInput{
		ProjectID:    project.ID,
		SourceKind:   "email",
		Name:         "support",
		Mode:         "imap",
		CredentialID: cred.ID,
		CallerUserID: f.env.UserA,
	})
	require.ErrorIs(t, err, service.ErrCredentialProviderMismatch)
}

func TestIntakeSource_TestDeliveryDoesNotPersist(t *testing.T) {
	f := newIntakeSourceFixture(t)
	ctx := context.Background()
	project := f.env.NewProject(t, f.env.UserA, "intake-test-delivery")
	dto, _ := f.createCI(t, project.ID)

	res, err := f.svc.TestDelivery(ctx, f.env.UserA, project.ID, dto.ID, nil)
	require.NoError(t, err)
	require.Equal(t, "ci", res.SourceKind)
	require.NotEmpty(t, res.Text)
	require.NotEmpty(t, res.Mode)

	events, err := f.env.Queries().ListIntakeEventsByProject(ctx, store.ListIntakeEventsByProjectParams{
		ProjectID: project.ID,
		Limit:     10,
	})
	require.NoError(t, err)
	require.Empty(t, events, "a test delivery must not record an intake event")
}

func TestIntakeSource_PollNowRequiresImapBinding(t *testing.T) {
	f := newIntakeSourceFixture(t)
	project := f.env.NewProject(t, f.env.UserA, "intake-poll")
	dto, _ := f.createCI(t, project.ID)
	require.ErrorIs(t, f.svc.PollNow(context.Background(), f.env.UserA, project.ID, dto.ID), service.ErrIntakeSourceInvalid)
}

func TestIntakeSource_HealthForPushBinding(t *testing.T) {
	f := newIntakeSourceFixture(t)
	project := f.env.NewProject(t, f.env.UserA, "intake-health")
	dto, _ := f.createCI(t, project.ID)

	health, err := f.svc.Health(context.Background(), f.env.UserA, project.ID, dto.ID)
	require.NoError(t, err)
	// No delivery attempt has been observed yet, so the binding is `never`, not a
	// healthy-looking `ok` (design §12).
	require.Equal(t, service.IntakeStatusNever, health.Status)
	require.Zero(t, health.Events24h)
}

func TestExternalSource_IsolatesIntakeProviders(t *testing.T) {
	f := newIntakeSourceFixture(t)
	ctx := context.Background()
	project := f.env.NewProject(t, f.env.UserA, "intake-isolation")
	dto, _ := f.createCI(t, project.ID)

	listed, err := f.extSvc.List(ctx, project.ID)
	require.NoError(t, err)
	require.Empty(t, listed, "ci bindings must not appear in the external-systems panel")

	_, err = f.extSvc.Add(ctx, project.ID, integration.ProviderName("ci"), "sneaky", 0, nil, f.env.UserA)
	require.ErrorIs(t, err, service.ErrIntakeSourceReserved)

	require.ErrorIs(t, f.extSvc.Delete(ctx, dto.ID), service.ErrIntakeSourceReserved)

	// The binding survives the rejected delete.
	list, err := f.svc.List(ctx, f.env.UserA, project.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
}

func TestIntakeSource_RejectsForeignProjectAccess(t *testing.T) {
	f := newIntakeSourceFixture(t)
	ctx := context.Background()
	project := f.env.NewProject(t, f.env.UserA, "intake-foreign")
	dto, _ := f.createCI(t, project.ID)

	// UserB neither owns nor belongs to UserA's project; the binding id alone
	// must not be enough for any read or mutation.
	_, err := f.svc.List(ctx, f.env.UserB, project.ID)
	require.ErrorIs(t, err, service.ErrForbidden)
	_, err = f.svc.RotateSecret(ctx, f.env.UserB, project.ID, dto.ID)
	require.ErrorIs(t, err, service.ErrForbidden)
	_, err = f.svc.TestDelivery(ctx, f.env.UserB, project.ID, dto.ID, nil)
	require.ErrorIs(t, err, service.ErrForbidden)
	require.ErrorIs(t, f.svc.PollNow(ctx, f.env.UserB, project.ID, dto.ID), service.ErrForbidden)
	_, err = f.svc.Health(ctx, f.env.UserB, project.ID, dto.ID)
	require.ErrorIs(t, err, service.ErrForbidden)
	require.ErrorIs(t, f.svc.Delete(ctx, f.env.UserB, project.ID, dto.ID), service.ErrForbidden)

	// The rejected delete did not remove the binding.
	list, err := f.svc.List(ctx, f.env.UserA, project.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
}

func TestIntakeSource_CICreateSeedsFailureRule(t *testing.T) {
	f := newIntakeSourceFixture(t)
	ctx := context.Background()
	project := f.env.NewProject(t, f.env.UserA, "intake-ci-rule")

	dto, _ := f.createCI(t, project.ID)
	require.Equal(t, "gitlab", dto.Provider)
	require.Equal(t, []any{"ci_failed"}, dto.Config["events"], "a CI binding subscribes to failures only by default")

	rules, err := f.env.Queries().ListIntakeRulesByProject(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	rule := rules[0]
	require.Equal(t, intake.SourceKindCI, rule.SourceKind)
	require.Equal(t, "CI 失败自动修复", rule.Name)
	require.Equal(t, int64(100), rule.Priority)
	require.Equal(t, int64(1), rule.Enabled)
	var cond intake.RuleConditions
	require.NoError(t, json.Unmarshal([]byte(rule.ConditionsJson), &cond))
	require.Equal(t, intake.EventKindCIFailed, cond.EventKind)

	versions, err := f.env.Queries().ListIntakePolicyVersions(ctx, store.ListIntakePolicyVersionsParams{
		ProjectID: project.ID, SourceKind: intake.SourceKindCI,
	})
	require.NoError(t, err)
	require.NotEmpty(t, versions, "seeding a rule records a policy version")

	// A non-CI binding seeds nothing.
	_, _, err = f.svc.Create(ctx, service.IntakeSourceInput{
		ProjectID: project.ID, SourceKind: "webhook", Name: "plain-hooks", CallerUserID: f.env.UserA,
	})
	require.NoError(t, err)
	rules, err = f.env.Queries().ListIntakeRulesByProject(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, rules, 1)

	// Recreating the binding does not duplicate the rule.
	require.NoError(t, f.svc.Delete(ctx, f.env.UserA, project.ID, dto.ID))
	_, _ = f.createCI(t, project.ID)
	rules, err = f.env.Queries().ListIntakeRulesByProject(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, rules, 1)
}

func TestIntakeSource_CreateRejectsUnknownCIProvider(t *testing.T) {
	f := newIntakeSourceFixture(t)
	project := f.env.NewProject(t, f.env.UserA, "intake-ci-provider")

	_, _, err := f.svc.Create(context.Background(), service.IntakeSourceInput{
		ProjectID: project.ID, SourceKind: "ci", Name: "travis", Mode: "push",
		Provider: "travis", CallerUserID: f.env.UserA,
	})
	require.ErrorIs(t, err, service.ErrIntakeSourceInvalid)
}

const webhookSecretKeyForTest = "webhook_secret"
