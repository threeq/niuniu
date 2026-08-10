package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/niuniu-dev/niuniu-desktop/internal/runnerstore"
)

func newTestManager(t *testing.T) *runnerManager {
	t.Helper()
	store := runnerstore.New(filepath.Join(t.TempDir(), "runners.json"))
	return newRunnerManager(store, newNativeApprover("en"), t.TempDir())
}

func TestManager_StartAwaitsToken(t *testing.T) {
	m := newTestManager(t)
	r, _ := m.store.Upsert(runnerstore.Runner{ConnectionKey: "h:1", WorkspaceID: "1", LocalDir: t.TempDir()})
	// No token / base URL yet.
	if err := m.start(r); !errors.Is(err, errAwaitingToken) {
		t.Fatalf("expected errAwaitingToken, got %v", err)
	}
}

func TestManager_StartRejectsEmptyDir(t *testing.T) {
	m := newTestManager(t)
	r, _ := m.store.Upsert(runnerstore.Runner{ConnectionKey: "h:1", WorkspaceID: "1"}) // no LocalDir
	if err := m.start(r); err == nil {
		t.Fatal("expected error for runner with no bound directory")
	}
}

func TestManager_SetTokenReturnsWaitingRunners(t *testing.T) {
	m := newTestManager(t)
	active, _ := m.store.Upsert(runnerstore.Runner{ConnectionKey: "h:1", WorkspaceID: "1", LocalDir: "/d", Status: runnerstore.StatusConnecting})
	stopped, _ := m.store.Upsert(runnerstore.Runner{ConnectionKey: "h:1", WorkspaceID: "2", LocalDir: "/d", Status: runnerstore.StatusStopped})
	_, _ = m.store.Upsert(runnerstore.Runner{ConnectionKey: "other:1", WorkspaceID: "3", LocalDir: "/d", Status: runnerstore.StatusConnecting})

	ids := m.setToken("h:1", "tok")
	if len(ids) != 1 || ids[0] != active.ID {
		t.Fatalf("expected only the non-stopped runner on h:1, got %v (stopped=%s)", ids, stopped.ID)
	}
	// Idempotent: same token again returns nothing new.
	if ids := m.setToken("h:1", "tok"); ids != nil {
		t.Fatalf("re-setting the same token should be a no-op, got %v", ids)
	}
}

const configJSON = `{"status":"active","config":{"local_dir":"/d","prompt_snippet":"","allowed_commands":["go","npm"],"always_allow_persist":true}}`

func TestManager_FetchConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing bearer: %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(configJSON))
	}))
	defer srv.Close()

	cfg := newTestManager(t).fetchConfig(srv.URL, "7", "tok")
	if len(cfg.AllowedCommands) != 2 || !cfg.AlwaysAllowPersist {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestManager_FetchConfigFailsSafe(t *testing.T) {
	// A 500 (or any non-200) must yield an EMPTY whitelist — the gateway then
	// prompts for everything rather than silently allowing.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := newTestManager(t).fetchConfig(srv.URL, "7", "tok")
	if len(cfg.AllowedCommands) != 0 {
		t.Fatalf("failed fetch must default to empty whitelist, got %v", cfg.AllowedCommands)
	}
}

func TestManager_PersistAppends(t *testing.T) {
	var (
		mu  sync.Mutex
		put []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			var dto runnerConfigDTO
			_ = json.NewDecoder(r.Body).Decode(&dto)
			mu.Lock()
			put = dto.AllowedCommands
			mu.Unlock()
		}
		_, _ = w.Write([]byte(configJSON))
	}))
	defer srv.Close()

	if err := newTestManager(t).persistFunc(srv.URL, "7", "tok")("deno"); err != nil {
		t.Fatalf("persist: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(put) != 3 || put[2] != "deno" {
		t.Fatalf("expected PUT appending 'deno' to [go,npm], got %v", put)
	}
}

func TestManager_PersistSkipsExisting(t *testing.T) {
	var putCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			putCalled = true
		}
		_, _ = w.Write([]byte(configJSON))
	}))
	defer srv.Close()

	// "go" is already whitelisted → persist must short-circuit (GET only, no PUT).
	if err := newTestManager(t).persistFunc(srv.URL, "7", "tok")("go"); err != nil {
		t.Fatalf("persist existing: %v", err)
	}
	if putCalled {
		t.Fatal("persisting an already-listed entry must not PUT")
	}
}
