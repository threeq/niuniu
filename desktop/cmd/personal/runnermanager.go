package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu-desktop/internal/localrunner"
	"github.com/niuniu-dev/niuniu-desktop/internal/runnerstore"
)

// runnermanager.go owns the LIVE side of the local Runner (#526·子D): for every
// started binding it holds a localrunner.Client maintaining the reverse channel
// to the remote node, plus the per-connection auth material harvested from the
// remote webview. It is the bridge between the desktop registry (runnerstore,
// what the manager UI shows) and the execution engine (internal/localrunner).
//
// Auth model: the desktop↔remote connection is a webview logged in by the user;
// the JWT lives in that page's localStorage. A small harvester injected into the
// remote webview posts it to Go (RawMessageHandler → App.SetLocalRunnerToken),
// keyed by connection. A runner can only come online once BOTH its base URL
// (known when the connection window opens) and a token are present.

// errAwaitingToken signals start() could not proceed because the connection's
// auth token has not been harvested yet — not a hard failure; the runner is
// (re)started automatically once SetLocalRunnerToken arrives.
var errAwaitingToken = errors.New("awaiting connection auth token")

type runnerManager struct {
	store      *runnerstore.Store
	approver   localrunner.Approver
	auditDir   string
	httpClient *http.Client

	// onAuthExpired is called with a connKey when that connection's reverse
	// channel is rejected with 401 — the App wires it to ExecJS the connection
	// webview's __niuniuRunnerRefresh__ hook so a fresh token is minted + posted.
	// Set once at startup; read without the lock.
	onAuthExpired func(connKey string)

	mu       sync.Mutex
	clients  map[string]*localrunner.Client // runner id -> live client
	tokens   map[string]string              // connKey -> bearer JWT
	baseURLs map[string]string              // connKey -> http(s)://host:port
}

func newRunnerManager(store *runnerstore.Store, approver localrunner.Approver, auditDir string) *runnerManager {
	return &runnerManager{
		store:      store,
		approver:   approver,
		auditDir:   auditDir,
		httpClient: &http.Client{Timeout: 15 * time.Second},
		clients:    make(map[string]*localrunner.Client),
		tokens:     make(map[string]string),
		baseURLs:   make(map[string]string),
	}
}

// setBaseURL records the base URL for a connection (called when its window opens).
func (m *runnerManager) setBaseURL(connKey, baseURL string) {
	m.mu.Lock()
	m.baseURLs[connKey] = baseURL
	m.mu.Unlock()
}

// setToken records a harvested token and returns the ids of non-stopped runners
// on that connection that are not already live — the caller (re)starts them.
func (m *runnerManager) setToken(connKey, token string) []string {
	if token == "" {
		return nil
	}
	m.mu.Lock()
	changed := m.tokens[connKey] != token
	m.tokens[connKey] = token
	m.mu.Unlock()
	if !changed {
		return nil
	}
	var ids []string
	for _, r := range m.store.List() {
		if r.ConnectionKey != connKey || r.Status == runnerstore.StatusStopped {
			continue
		}
		m.mu.Lock()
		cl, live := m.clients[r.ID]
		m.mu.Unlock()
		if live {
			// Push the refreshed token into the running client so its NEXT
			// reconnect authenticates with the current JWT (the SPA rotates the
			// access token; a stale one makes every reconnect 401).
			cl.SetToken(token)
		} else {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// running reports whether a live client exists for id.
func (m *runnerManager) running(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.clients[id]
	return ok
}

// start builds and launches the reverse-channel client for a runner binding. It
// returns errAwaitingToken when the connection auth token isn't harvested yet.
func (m *runnerManager) start(r runnerstore.Runner) error {
	if r.LocalDir == "" {
		return errors.New("runner has no bound directory")
	}
	m.mu.Lock()
	if _, ok := m.clients[r.ID]; ok {
		m.mu.Unlock()
		return nil // already running
	}
	baseURL := m.baseURLs[r.ConnectionKey]
	token := m.tokens[r.ConnectionKey]
	m.mu.Unlock()

	if token == "" || baseURL == "" {
		return errAwaitingToken
	}

	// Pull the authoritative whitelist + persistence flag from the server config
	// (source of truth). On failure fall back to an EMPTY whitelist — fail-safe:
	// every command then goes through the approval prompt rather than silently
	// running.
	cfg := m.fetchConfig(baseURL, r.WorkspaceID, token)

	auditor := localrunner.NewFileAuditor(filepath.Join(m.auditDir, "runner-"+r.ID+".jsonl"))
	gw := localrunner.NewGateway(localrunner.GatewayConfig{
		Dir:                r.LocalDir,
		Allowed:            cfg.AllowedCommands,
		AlwaysAllowPersist: cfg.AlwaysAllowPersist,
		Approver:           m.approver,
		Persist:            m.persistFunc(baseURL, r.WorkspaceID, token),
		Audit:              auditor,
	})

	provider := localrunner.NewHTTPRemoteState(m.httpClient, baseURL, r.WorkspaceID, token)

	// Seed-clone (#478): if the bound directory is empty, populate it from the
	// workspace's registered repos before the reverse channel comes online, so
	// the AI has a working tree to operate on. Best-effort: a failure (no clone
	// URL, auth, offline server) is logged but never blocks the runner — an
	// unseeded directory is still an operable directory.
	id := r.ID
	if summary, serr := localrunner.NewSeeder(r.LocalDir, provider).Seed(context.Background()); serr != nil {
		slog.Warn("runner seed-clone failed", "id", id, "error", serr)
		_, _ = m.store.AppendLog(id, runnerstore.LogEntry{Level: "system", Text: "种子仓库 clone 失败：" + serr.Error()})
	} else if summary != "" {
		_, _ = m.store.AppendLog(id, runnerstore.LogEntry{Level: "system", Text: summary})
	}

	syncer := localrunner.NewGitSyncer(r.LocalDir, provider)


	connKey := r.ConnectionKey
	client := localrunner.New(localrunner.Config{
		BaseURL:     baseURL,
		WorkspaceID: r.WorkspaceID,
		Token:       token,
		Dir:         r.LocalDir,
		Gateway:     gw,
		Syncer:      syncer,
		OnAuthExpired: func() {
			if m.onAuthExpired != nil {
				m.onAuthExpired(connKey)
			}
		},
		OnStatus: func(s localrunner.Status, note string) {
			if _, err := m.store.SetStatus(id, string(s)); err != nil {
				slog.Warn("runner set status persist failed", "id", id, "error", err)
			}
			_, _ = m.store.AppendLog(id, runnerstore.LogEntry{Level: "system", Text: note})
		},
		OnLog: func(level, text string) {
			_, _ = m.store.AppendLog(id, runnerstore.LogEntry{Level: level, Text: text})
		},
	})

	m.mu.Lock()
	m.clients[id] = client
	m.mu.Unlock()
	client.Start()
	return nil
}

// stop tears down the live client for id (if any). The registry record stays;
// this is "停长连", distinct from Remove ("解绑").
func (m *runnerManager) stop(id string) {
	m.mu.Lock()
	client := m.clients[id]
	delete(m.clients, id)
	m.mu.Unlock()
	if client != nil {
		client.Stop()
	}
}

// stopAll tears down every live client (used at shutdown).
func (m *runnerManager) stopAll() {
	m.mu.Lock()
	clients := make([]*localrunner.Client, 0, len(m.clients))
	for id, c := range m.clients {
		clients = append(clients, c)
		delete(m.clients, id)
	}
	m.mu.Unlock()
	for _, c := range clients {
		c.Stop()
	}
}

// --- server config REST (whitelist source of truth + always-allow persistence) --

type runnerConfigDTO struct {
	LocalDir           string   `json:"local_dir"`
	PromptSnippet      string   `json:"prompt_snippet"`
	AllowedCommands    []string `json:"allowed_commands"`
	AlwaysAllowPersist bool     `json:"always_allow_persist"`
}

type runnerStateDTO struct {
	Status string           `json:"status"`
	Config *runnerConfigDTO `json:"config"`
}

func (m *runnerManager) configURL(baseURL, workspaceID string) string {
	return fmt.Sprintf("%s/api/workspaces/%s/local-runner", strings.TrimRight(baseURL, "/"), workspaceID)
}

// fetchConfig reads the server-side runner config. Errors yield a zero config
// (empty whitelist) so the gateway defaults to prompting — never to allowing.
func (m *runnerManager) fetchConfig(baseURL, workspaceID, token string) runnerConfigDTO {
	req, err := http.NewRequest(http.MethodGet, m.configURL(baseURL, workspaceID), nil)
	if err != nil {
		return runnerConfigDTO{}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := m.httpClient.Do(req)
	if err != nil {
		slog.Warn("runner config fetch failed; defaulting to prompt-all", "error", err)
		return runnerConfigDTO{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return runnerConfigDTO{}
	}
	var state runnerStateDTO
	if json.NewDecoder(resp.Body).Decode(&state) != nil || state.Config == nil {
		return runnerConfigDTO{}
	}
	return *state.Config
}

// persistFunc returns a PersistFunc that appends an approved command to the
// server whitelist so future runs skip the prompt (honors "始终允许").
func (m *runnerManager) persistFunc(baseURL, workspaceID, token string) localrunner.PersistFunc {
	return func(entry string) error {
		cur := m.fetchConfig(baseURL, workspaceID, token)
		for _, c := range cur.AllowedCommands {
			if c == entry {
				return nil // already listed
			}
		}
		cur.AllowedCommands = append(cur.AllowedCommands, entry)
		body, err := json.Marshal(cur)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, m.configURL(baseURL, workspaceID), strings.NewReader(string(body)))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := m.httpClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("persist whitelist: status %d", resp.StatusCode)
		}
		return nil
	}
}
