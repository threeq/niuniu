package main

import (
	"path/filepath"
	"testing"

	"github.com/niuniu-dev/niuniu-desktop/internal/config"
	"github.com/niuniu-dev/niuniu-desktop/internal/runnerstore"
)

func newRunnerTestApp(t *testing.T) *App {
	t.Helper()
	store := runnerstore.New(filepath.Join(t.TempDir(), "runners.json"))
	return &App{
		lang:        "zh",
		runners:     store,
		runnerMgr:   newRunnerManager(store, newNativeApprover("zh"), t.TempDir()),
		connWindows: make(map[string]*ConnWindow),
	}
}

func findView(views []RunnerView, id string) (RunnerView, bool) {
	for _, v := range views {
		if v.ID == id {
			return v, true
		}
	}
	return RunnerView{}, false
}

func TestRegisterAndListEnrichesConnActive(t *testing.T) {
	a := newRunnerTestApp(t)
	// One runner on an OPEN connection, one on a closed connection.
	idOpen := a.RegisterLocalRunner("10.0.0.5:3000", "team", "1", "ws-open", "/tmp/open")
	idClosed := a.RegisterLocalRunner("10.0.0.9:3000", "other", "2", "ws-closed", "/tmp/closed")
	a.connWindows["10.0.0.5:3000"] = &ConnWindow{}

	views := a.ListLocalRunners()
	if len(views) != 2 {
		t.Fatalf("expected 2 runners, got %d", len(views))
	}
	vOpen, ok := findView(views, idOpen)
	if !ok || !vOpen.ConnActive {
		t.Fatalf("expected open runner conn_active=true, got %+v", vOpen)
	}
	vClosed, ok := findView(views, idClosed)
	if !ok || vClosed.ConnActive {
		t.Fatalf("expected closed runner conn_active=false, got %+v", vClosed)
	}
	if vOpen.WorkspaceName != "ws-open" || vOpen.LocalDir != "/tmp/open" {
		t.Fatalf("runner fields not surfaced: %+v", vOpen)
	}
}

func TestStartStopDeleteBindings(t *testing.T) {
	a := newRunnerTestApp(t)
	id := a.RegisterLocalRunner("h:1", "n", "1", "ws", "/d")

	if !a.StopLocalRunner(id) {
		t.Fatal("StopLocalRunner should return true")
	}
	if v, _ := findView(a.ListLocalRunners(), id); v.Status != runnerstore.StatusStopped {
		t.Fatalf("expected stopped, got %q", v.Status)
	}
	if !a.StartLocalRunner(id) {
		t.Fatal("StartLocalRunner should return true")
	}
	// With no connection token harvested yet (no webview / no server in a unit
	// test), Start puts the runner into "connecting" and waits for the token —
	// it must NOT claim to be active without a live reverse channel.
	if v, _ := findView(a.ListLocalRunners(), id); v.Status != runnerstore.StatusConnecting {
		t.Fatalf("expected connecting (awaiting token), got %q", v.Status)
	}
	// A start/stop leaves a system log line.
	if logs := a.GetLocalRunnerLogs(id); len(logs) == 0 {
		t.Fatal("expected system log entries after start/stop")
	}

	if !a.DeleteLocalRunner(id) {
		t.Fatal("DeleteLocalRunner should return true")
	}
	if len(a.ListLocalRunners()) != 0 {
		t.Fatal("expected no runners after delete")
	}
	if a.StopLocalRunner("unknown") || a.StartLocalRunner("unknown") || a.DeleteLocalRunner("unknown") {
		t.Fatal("actions on unknown id should return false")
	}
}

func TestOpenLocalRunnerDir(t *testing.T) {
	a := newRunnerTestApp(t)
	id := a.RegisterLocalRunner("h:1", "n", "1", "ws", "/tmp/target")

	var opened string
	orig := openInFileManager
	openInFileManager = func(dir string) error { opened = dir; return nil }
	defer func() { openInFileManager = orig }()

	if !a.OpenLocalRunnerDir(id) {
		t.Fatal("OpenLocalRunnerDir should return true for a bound dir")
	}
	if opened != "/tmp/target" {
		t.Fatalf("expected to open /tmp/target, got %q", opened)
	}
	if a.OpenLocalRunnerDir("unknown") {
		t.Fatal("OpenLocalRunnerDir should return false for unknown id")
	}
}

func TestOpenLocalRunnerDir_EmptyDir(t *testing.T) {
	a := newRunnerTestApp(t)
	id := a.RegisterLocalRunner("h:1", "n", "1", "ws", "")
	called := false
	orig := openInFileManager
	openInFileManager = func(string) error { called = true; return nil }
	defer func() { openInFileManager = orig }()
	if a.OpenLocalRunnerDir(id) {
		t.Fatal("OpenLocalRunnerDir should return false for an empty dir")
	}
	if called {
		t.Fatal("file manager should not be invoked for an empty dir")
	}
}

func TestHandleRawWebviewMessageConfigBinds(t *testing.T) {
	a := newRunnerTestApp(t)
	// A remote window is open so the bridge can derive the connection name.
	a.connWindows["h:1"] = &ConnWindow{conn: config.Connection{Name: "team-node"}}

	msg := `{"type":"niuniu-runner-config","connKey":"h:1","workspaceId":"7","localDir":"/tmp/ws7"}`
	a.HandleRawWebviewMessage(msg)

	views := a.ListLocalRunners()
	if len(views) != 1 {
		t.Fatalf("expected 1 runner after config harvest, got %d", len(views))
	}
	r := views[0]
	if r.ConnectionKey != "h:1" || r.WorkspaceID != "7" || r.LocalDir != "/tmp/ws7" {
		t.Fatalf("binding fields wrong: %+v", r)
	}
	if r.ConnectionName != "team-node" {
		t.Fatalf("expected conn name derived from window, got %q", r.ConnectionName)
	}
	// No token harvested in a unit test → the runner is awaiting auth (connecting),
	// never falsely "active".
	if r.Status != runnerstore.StatusConnecting {
		t.Fatalf("expected connecting (awaiting token), got %q", r.Status)
	}

	// Idempotent: a re-harvest of the same config must not create a second binding
	// or a second connection.
	a.HandleRawWebviewMessage(msg)
	if got := a.ListLocalRunners(); len(got) != 1 {
		t.Fatalf("expected still 1 runner after duplicate harvest, got %d", len(got))
	}
}

func TestHandleRawWebviewMessageUnbindRemoves(t *testing.T) {
	a := newRunnerTestApp(t)
	a.HandleRawWebviewMessage(`{"type":"niuniu-runner-config","connKey":"h:1","workspaceId":"7","localDir":"/tmp/ws7"}`)
	if len(a.ListLocalRunners()) != 1 {
		t.Fatal("setup: expected 1 runner")
	}

	a.HandleRawWebviewMessage(`{"type":"niuniu-runner-unbind","connKey":"h:1","workspaceId":"7"}`)
	if got := a.ListLocalRunners(); len(got) != 0 {
		t.Fatalf("expected runner removed after unbind, got %d", len(got))
	}

	// Unbind of an unknown binding is a safe no-op.
	a.HandleRawWebviewMessage(`{"type":"niuniu-runner-unbind","connKey":"h:1","workspaceId":"nope"}`)
}

func TestHandleRawWebviewMessageIgnoresUnrelated(t *testing.T) {
	a := newRunnerTestApp(t)
	a.HandleRawWebviewMessage(`not json at all`)
	a.HandleRawWebviewMessage(`{"type":"something-else"}`)
	// Well-formed config messages missing required fields are dropped, not bound.
	a.HandleRawWebviewMessage(`{"type":"niuniu-runner-config","connKey":"","workspaceId":"7","localDir":"/d"}`)
	a.HandleRawWebviewMessage(`{"type":"niuniu-runner-config","connKey":"h:1","workspaceId":"7","localDir":""}`)
	if got := a.ListLocalRunners(); len(got) != 0 {
		t.Fatalf("expected no runners from unrelated/invalid messages, got %d", len(got))
	}
}

func TestGetUILang(t *testing.T) {
	a := newRunnerTestApp(t)
	if a.GetUILang() != "zh" {
		t.Fatalf("expected zh, got %q", a.GetUILang())
	}
}

func TestBindingsNilRunnersAreSafe(t *testing.T) {
	a := &App{connWindows: make(map[string]*ConnWindow)} // runners == nil
	if a.ListLocalRunners() != nil {
		t.Fatal("ListLocalRunners should be nil with no store")
	}
	if a.RegisterLocalRunner("h", "n", "1", "w", "/d") != "" {
		t.Fatal("RegisterLocalRunner should return empty with no store")
	}
	if a.StartLocalRunner("x") || a.StopLocalRunner("x") || a.DeleteLocalRunner("x") || a.OpenLocalRunnerDir("x") {
		t.Fatal("mutations should return false with no store")
	}
	if a.GetLocalRunnerLogs("x") != nil {
		t.Fatal("GetLocalRunnerLogs should be nil with no store")
	}
}
