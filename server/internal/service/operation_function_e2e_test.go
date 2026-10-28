package service

// End-to-end for the sandbox operation function runtime (#739), over the real
// integration stack: a provider is created by publishing a draft, the draft
// carries an AI-authored function, and the agent then calls it — through the
// fence, through the proxy egress, against a local HTTP server standing in for
// the upstream.
//
// What this covers that the unit tests cannot: that the pieces are wired to
// each other. The draft pipeline must carry the function into the registry, the
// registry must reach the provider's contract at egress time, the credential
// must be injected by the proxy rather than by the function, and the audit row
// must record the whole thing.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/sandbox"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// allowVerifier stands in for the post-publish probe. The probe's own
// behaviour is covered by the #734 tests; here it only has to not require a
// reachable upstream.
type allowVerifier struct{}

func (allowVerifier) VerifyProvider(context.Context, string, int64) (*DraftVerifyResult, error) {
	return &DraftVerifyResult{OK: true, Detail: "test verifier"}, nil
}

func (allowVerifier) VerifyDataSource(context.Context, int64, int64, []int64) (*DraftVerifyResult, error) {
	return &DraftVerifyResult{OK: true, Detail: "test verifier"}, nil
}

type fnE2E struct {
	db        *sql.DB
	q         *store.Queries
	creds     *ExternalCredentialService
	providers *ExternalProviderService
	proxy     *ExternalProxyService
	functions *OperationFunctionService
	drafts    *IntegrationDraftService
	upstream  *httptest.Server
}

// newFnTestDB opens an in-memory SQLite database with the real schema, so the
// E2E exercises the same DDL the product does.
func newFnTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store.Driver = "sqlite"
	if err := store.ApplySchema(db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	store.Migrate(db)
	return db
}

// The helper is built ONCE for the whole test binary. Building it per test
// into t.TempDir() would delete it when the first test finishes, and every
// later test would fail for a reason that has nothing to do with what it tests.
var (
	e2eFuncboxOnce sync.Once
	funcboxDir     string
	funcboxErr     error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if funcboxDir != "" {
		_ = os.RemoveAll(funcboxDir)
	}
	os.Exit(code)
}

func buildFuncbox(t *testing.T) {
	t.Helper()
	e2eFuncboxOnce.Do(func() {
		dir, err := os.MkdirTemp("", "funcbox-e2e")
		if err != nil {
			funcboxErr = err
			return
		}
		funcboxDir = dir
		name := "niuniu-funcbox"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		out := filepath.Join(dir, name)
		cmd := exec.Command("go", "build", "-o", out, "./cmd/niuniu-funcbox")
		cmd.Dir = filepath.Join("..", "..")
		if b, err := cmd.CombinedOutput(); err != nil {
			funcboxErr = fmt.Errorf("%v (%s)", err, b)
			return
		}
		sandbox.SetHelperPath(out)
	})
	if funcboxErr != nil {
		t.Fatalf("build funcbox: %v", funcboxErr)
	}
}

func newFnE2E(t *testing.T) *fnE2E {
	t.Helper()
	buildFuncbox(t)
	db := newFnTestDB(t)
	q := store.NewQueries(db)
	// ONE credential service, built on ONE keyring: the proxy decrypts what this
	// seals, so a second instance with its own temp keyring would fail at
	// egress time for a reason that has nothing to do with the test.
	credSvc := NewExternalCredentialService(q, db, testKeyring(t), integration.NewRegistry())
	providers := NewExternalProviderService(q, db)
	// nil policy: this E2E uses a USER-owned credential, for which the org
	// usage-policy gate (#742) does not apply.
	proxy := NewExternalProxyService(q, db, providers, credSvc, nil, nil)
	functions := NewOperationFunctionService(q, db, providers, proxy)
	// The REAL health service, not a nil stand-in: publishing leaves the target
	// disabled and enables it through this path, so a nil here would leave the
	// provider off and every call would be refused for the wrong reason.
	health := NewIntegrationHealthService(q, db, providers, proxy, nil, IntegrationHealthConfig{})
	drafts := NewIntegrationDraftService(q, providers, nil, proxy, health)
	drafts.SetFunctionService(functions)
	drafts.SetVerifier(allowVerifier{})

	// The upstream the function will reach — through the proxy, never directly.
	// httptest listens on loopback, which the proxy's SSRF guard refuses by
	// design; this is the product's own seam for tests and local sources.
	prevAllowLocal := KBAllowLocalSources()
	SetKBAllowLocalSources(true)
	t.Cleanup(func() { SetKBAllowLocalSources(prevAllowLocal) })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer e2e-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"missing injected credential"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth/token" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["grant"] != "grant-1" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"the caller's input did not reach step 1"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"tok-123"}`))
			return
		}
		// Echo the token the caller threaded through, so the test can prove the
		// value from step 1 actually reached step 2.
		_, _ = w.Write([]byte(`{"orders":[{"id":"42"}],"path":"` + r.URL.Path +
			`","seen_token":"` + r.URL.Query().Get("token") + `"}`))
	}))
	t.Cleanup(upstream.Close)

	return &fnE2E{db: db, q: q, creds: credSvc, providers: providers, proxy: proxy,
		functions: functions, drafts: drafts, upstream: upstream}
}

// seedProviderAndFunction drafts a write-enabled provider whose contract has a
// token-exchange write and a read-only list, carrying a function that calls both,
// then confirms and publishes.
func (e *fnE2E) seedProviderAndFunction(t *testing.T) FunctionView {
	t.Helper()
	ctx := context.Background()
	const owner, user = "user", int64(7)

	view, err := e.drafts.CreateDraft(ctx, CreateDraftInput{
		OwnerType: owner, OwnerID: user, CreatedBy: user,
		Kind:       DraftKindProvider,
		TargetName: "e2e_orders",
		Provider: &ProviderDraftPayload{
			Label:   "E2E Orders",
			BaseURL: e.upstream.URL,
			// exchangeToken is a side-effecting POST (it mints a token), so it is a
			// genuine write, not read_only: a non-GET/HEAD method cannot be proven
			// side-effect-free and read_only would let it bypass the write gate. The
			// provider therefore asks for the write gate, which is what makes the
			// whole two-step (write then read) flow runnable.
			WriteEnabled: true,
			Whitelist:    `[{"method":"GET","path":"*"},{"method":"POST","path":"/oauth/token"}]`,
			Contract: &ProviderContract{
				Provider: "E2E Orders",
				BaseURL:  e.upstream.URL,
				Operations: []ContractOperation{
					{Name: "exchangeToken", Method: "POST", Path: "/oauth/token", ReadOnly: false,
						ResponseShape: `{"token":"tok-123"}`},
					{Name: "listOrders", Method: "GET", Path: "/orders", ReadOnly: true,
						// Declaring the response shape is what lets the rehearsal
						// hand the function a realistic body instead of a null the
						// code then trips over.
						ResponseShape: `{"orders":[{"id":"42"}],"path":"/orders"}`},
				},
				VerifyOperation: "listOrders",
				Auth:            &ContractAuth{Strategy: "bearer", Header: "Authorization", Prefix: "Bearer"},
			},
			CredentialFields: []DraftCredential{{Name: "token", Location: "header", Required: true}},
			Functions: []FunctionDraftPayload{{
				Name:        "fetchOrders",
				Description: "Fetch the order list",
				Idempotent:  true,
				Enabled:     true,
				// The scenario the whole feature exists for: two calls where the
				// second needs a value the first produced.
				Code: `function main(input) {
					const tok = http_request({operation: "exchangeToken", body: {grant: input.grant}});
					if (!tok.ok) { return {ok: false, step: "token", error: tok.error}; }
					const orders = http_request({operation: "listOrders", query: {token: tok.body.token}});
					if (!orders.ok) { return {ok: false, step: "orders", error: orders.error}; }
					return {ok: true, count: orders.body.orders.length, path: orders.body.path,
					        token: orders.body.seen_token, steps: 2};
				}`,
				Egress: []sandbox.EgressOp{
					{Operation: "exchangeToken", ReadOnly: false},
					{Operation: "listOrders", ReadOnly: true},
				},
			}},
		},
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}

	// The function must be visible on the card — that is the whole point of
	// "人审确认的是行为", and it must be the SERVER's rehearsal, not the draft's.
	if view.Card == nil || len(view.Card.Functions) != 1 {
		t.Fatalf("the confirmation card does not carry the function: %+v", view.Card)
	}
	fnCard := view.Card.Functions[0]
	if fnCard.Rehearsal == nil {
		t.Fatal("the card carries no behaviour rehearsal")
	}
	if !fnCard.Rehearsal.MockOnly {
		t.Fatal("the rehearsal did not declare itself mock-only")
	}
	if !strings.Contains(fnCard.Rehearsal.Manifest, "GET /orders") {
		t.Fatalf("the rehearsal manifest does not name the call it will make:\n%s", fnCard.Rehearsal.Manifest)
	}
	if !fnCard.Rehearsal.Consistent || len(fnCard.Rehearsal.UndeclaredOps) != 0 {
		t.Fatalf("the rehearsal is not consistent: %+v", fnCard.Rehearsal)
	}

	if view.Status == DraftStatusLintFailed {
		for _, is := range view.Issues {
			t.Logf("lint: [%s] %s %s", is.Severity, is.Path, is.Message)
		}
		t.Fatalf("the draft carries blocking lint errors (status=%s)", view.Status)
	}

	if _, err := e.drafts.ConfirmDraft(ctx, ConfirmImpactInput{
		DraftID: view.ID, OwnerType: owner, OwnerID: user,
		AckKeys: view.Card.AckKeys(),
		// Turning on the write gate is a widening change: it needs the separate,
		// explicit second confirmation as well as the per-item acks.
		SecondConfirmation: true,
	}); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	pub, err := e.drafts.PublishDraft(ctx, PublishDraftInput{
		DraftID: view.ID, OwnerType: owner, OwnerID: user, ByUser: user,
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if !pub.Published {
		t.Fatalf("publish did not go live: %+v", pub)
	}

	// The credential is created AFTER the provider exists, and is what the
	// proxy injects — the function never sees it.
	if _, err := e.creds.Create(ctx, ExternalCredentialUpsertInput{
		OwnerType: owner, OwnerID: user, UserID: user,
		Provider: integration.ProviderName("e2e_orders"),
		Alias:    "default",
		RawConfig: map[string]any{
			"token": "e2e-token", "auth_mode": "bearer",
		},
	}); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	list, err := e.functions.List(ctx, owner, user)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected the published function to be registered, got %d", len(list))
	}
	return list[0]
}

func TestOperationFunctionEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real sandbox process")
	}
	e := newFnE2E(t)
	fn := e.seedProviderAndFunction(t)

	if !fn.Enabled {
		t.Fatal("a published function is not enabled")
	}
	if fn.Status != FuncObserving {
		t.Fatalf("a new function must start under observation, got %q", fn.Status)
	}
	if fn.ToolName != "ext__e2e_orders__fetchorders" {
		t.Fatalf("unexpected MCP tool name %q", fn.ToolName)
	}
	if fn.CodeHash == "" {
		t.Fatal("the function was published without a code hash")
	}

	ctx := context.Background()
	res, err := e.functions.Invoke(ctx, FunctionInvokeInput{
		OwnerType: "user", OwnerID: 7, UserID: 7,
		FunctionID: fn.ID, Caller: CallerAgent,
		Input:          map[string]any{"grant": "grant-1"},
		IdempotencyKey: "e2e-1",
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if !res.OK {
		t.Fatalf("the function did not run successfully: %s (%s)", res.Error, res.ErrorCode)
	}
	out, _ := res.Output.(map[string]any)
	if out["count"] != float64(1) {
		t.Fatalf("unexpected output — the proxy response did not reach the function: %#v", res.Output)
	}
	if out["path"] != "/orders" {
		t.Fatalf("the host did not resolve the operation to its path: %#v", res.Output)
	}
	// Both steps, in order — the trail is what an operator reads during the
	// observation window, so its ORDER is part of the record.
	if res.EgressCalls != 2 || len(res.EgressOps) != 2 ||
		res.EgressOps[0] != "exchangeToken" || res.EgressOps[1] != "listOrders" {
		t.Fatalf("the egress trail is wrong: %#v", res.EgressOps)
	}
	// The value produced by step 1 reached step 2: this is the whole point of
	// the feature, and a single-call test cannot observe it.
	if out["token"] != "tok-123" {
		t.Fatalf("the token from the first call did not reach the second: %#v", res.Output)
	}
	if out["steps"] != float64(2) {
		t.Fatalf("the function did not complete both steps: %#v", res.Output)
	}
	if !res.Observation {
		t.Fatal("the first call was not recorded as part of the observation window")
	}
	if res.AuditID == 0 {
		t.Fatal("the call produced no audit row")
	}

	// The audit row is the operator's record: the credential must not appear in
	// it, but the digest and the operation trail must.
	records, err := e.functions.Audit(ctx, "user", 7, 10)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("no audit rows")
	}
	rec := records[0]
	if rec.Status != FuncStatusOK || rec.InputSha256 == "" || rec.CodeHash != fn.CodeHash {
		t.Fatalf("the audit row is incomplete: %#v", rec)
	}
	if rec.Observation != true {
		t.Fatal("the audit row does not mark the observation window")
	}

	// A repeated key on an idempotent function returns the recorded result
	// instead of calling the upstream again.
	replay, err := e.functions.Invoke(ctx, FunctionInvokeInput{
		OwnerType: "user", OwnerID: 7, UserID: 7,
		FunctionID: fn.ID, Caller: CallerAgent, IdempotencyKey: "e2e-1",
		Input: map[string]any{"grant": "grant-1"},
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.OK || !replay.Replayed || replay.EgressCalls != 0 {
		t.Fatalf("the replay did not come from the ledger: %#v", replay)
	}

	// The unified integration activity feed must show the function call. A
	// sandbox function reaches the outside world through the SAME proxy egress
	// (same credential, same scope, same audit) as a direct call, so a feed that
	// stopped at the api/data tables would hide credential-bearing calls from
	// the one surface that exists to show them — and the team edition's
	// cross-user audit view (#743) reuses exactly this reader.
	auditSvc := NewIntegrationAuditService(e.q)
	feed, err := auditSvc.ListActivity(ctx, IntegrationActivityInput{UserID: 7, Kind: ActivityKindFunction})
	if err != nil {
		t.Fatalf("activity feed: %v", err)
	}
	if len(feed.Items) == 0 {
		t.Fatal("the sandbox function call is missing from the unified activity feed")
	}
	var runs, replays int
	for _, it := range feed.Items {
		if it.Kind != ActivityKindFunction || it.FunctionID == nil || *it.FunctionID != fn.ID {
			continue
		}
		// The row must name the PROVIDER it ran against (not just the function),
		// otherwise a provider-scoped view cannot attribute it.
		if it.ProviderID == nil || *it.ProviderID != fn.ProviderID {
			t.Fatalf("the feed row does not name its provider: %+v", it)
		}
		if it.FunctionName != fn.Name || it.Caller != CallerAgent {
			t.Fatalf("the feed row lost the function name/caller: %+v", it)
		}
		if it.CodeHash != fn.CodeHash {
			t.Fatalf("the feed row lost the code hash: %+v", it)
		}
		if it.Replayed {
			// A ledger replay made no outbound call, so a trail on it would be a
			// lie about what egressed.
			replays++
			if it.EgressCalls == nil || *it.EgressCalls != 0 || len(it.EgressOps) != 0 {
				t.Fatalf("a replayed row claims outbound calls: %+v", it)
			}
			continue
		}
		runs++
		if it.EgressCalls == nil || *it.EgressCalls != 2 || len(it.EgressOps) != 2 {
			t.Fatalf("the real run should carry both operations: calls=%v ops=%v",
				it.EgressCalls, it.EgressOps)
		}
	}
	if runs != 1 || replays != 1 {
		t.Fatalf("expected one real run and one replay in the feed, got runs=%d replays=%d",
			runs, replays)
	}

	// Filtering to the provider keeps the function's rows: a provider filter
	// that dropped them would hide precisely the egress a reviewer looks for.
	byProvider, err := auditSvc.ListActivity(ctx, IntegrationActivityInput{
		UserID: 7, ProviderID: fn.ProviderID,
	})
	if err != nil {
		t.Fatalf("provider-filtered feed: %v", err)
	}
	sawFunction := false
	for _, it := range byProvider.Items {
		if it.Kind == ActivityKindFunction {
			sawFunction = true
		}
	}
	if !sawFunction {
		t.Fatal("a provider filter dropped that provider's function calls from the feed")
	}

	// The observation trail is readable and shows the window's request log.
	trail, err := e.functions.ObservationTrail(ctx, fn.ID, 10)
	if err != nil {
		t.Fatalf("observation trail: %v", err)
	}
	if len(trail) == 0 {
		t.Fatal("the observation trail is empty")
	}

	// Disabling the function stops it being callable — "函数随源启用/停用".
	if err := e.functions.SetEnabled(ctx, "user", 7, fn.ID, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := e.functions.Invoke(ctx, FunctionInvokeInput{
		OwnerType: "user", OwnerID: 7, UserID: 7, FunctionID: fn.ID, Caller: CallerAgent,
	}); err == nil {
		t.Fatal("a disabled function was still callable")
	}

	// And deleting the SOURCE takes its functions with it.
	prov, err := e.providers.GetByName(ctx, "e2e_orders")
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if err := e.functions.DeleteForProvider(ctx, prov.ID); err != nil {
		t.Fatalf("delete for provider: %v", err)
	}
	if remaining, err := e.functions.ListForProvider(ctx, prov.ID); err != nil || len(remaining) != 0 {
		t.Fatalf("functions survived their source: %v / %d", err, len(remaining))
	}
}

// The fail-closed path: a host whose OS fence is unavailable must REFUSE to run
// a function. It is the acceptance item "禁止静默降级", and a passing host can
// never reach it on its own — so the verdict is forced.
func TestOperationFunctionRefusesToRunWithoutAFence(t *testing.T) {
	e := newFnE2E(t)
	fn := e.seedProviderAndFunction(t)

	// Force the fence unavailable, and make sure it comes back either way so a
	// failure here cannot poison the rest of the package's runs.
	sandbox.SetAvailabilityOverride(&sandbox.Availability{
		Platform: "test", Backend: "none", Available: false,
		Reason: "required fence component unavailable: seccomp",
		Components: []sandbox.ComponentStatus{
			{Name: "seccomp", Required: true, OK: false, Detail: "disabled by the host policy"},
		},
	})
	t.Cleanup(func() { sandbox.SetAvailabilityOverride(nil) })

	ctx := context.Background()
	_, err := e.functions.Invoke(ctx, FunctionInvokeInput{
		OwnerType: "user", OwnerID: 7, UserID: 7, FunctionID: fn.ID, Caller: CallerAgent,
	})
	if err == nil {
		t.Fatal("a function ran on a host whose OS fence is unavailable")
	}
	if !errors.Is(err, ErrFenceUnavailable) {
		t.Fatalf("the refusal is not reported as a fence problem: %v", err)
	}

	// The refusal itself is audited: "we declined to run this" is exactly the
	// record an operator needs, and an unrecorded refusal is a silent downgrade.
	records, aerr := e.functions.Audit(ctx, "user", 7, 10)
	if aerr != nil {
		t.Fatalf("audit: %v", aerr)
	}
	found := false
	for _, rec := range records {
		if rec.Status == FuncStatusDenied && rec.ErrorCode == sandbox.ErrFenceUnavailable {
			found = true
		}
	}
	if !found {
		t.Fatalf("the refusal left no audit row: %#v", records)
	}

	// The UI surface reports the same verdict, so the degradation is visible.
	if avail := sandbox.SelfCheck(false); avail.Available {
		t.Fatal("the self-check reported the fence as available while it was forced down")
	}

	// Restoring the fence restores the capability.
	sandbox.SetAvailabilityOverride(nil)
	if res, ierr := e.functions.Invoke(ctx, FunctionInvokeInput{
		OwnerType: "user", OwnerID: 7, UserID: 7, FunctionID: fn.ID, Caller: CallerUser,
	}); ierr != nil || !res.OK {
		t.Fatalf("the function did not run again after the fence came back: %v / %+v", ierr, res)
	}
}
