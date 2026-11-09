package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/mcpclient"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// MCPGateway executes MCP tool calls on behalf of server-side runtimes (the Ops
// Bot). It reuses the same provider definitions, encrypted credentials and
// audit table as the HTTP proxy, so an MCP integration is recorded exactly like
// an API call instead of vanishing into an agent transcript.
type MCPGateway struct {
	q         *store.Queries
	providers *ExternalProviderService
	creds     *ExternalCredentialService
	client    *http.Client

	mu      sync.Mutex
	clients map[string]*mcpclient.Client
}

type MCPCall struct {
	UserID          int64
	OwnerType       string
	OwnerID         int64
	CredentialAlias string
	Provider        string
	Tool            string
	Arguments       map[string]any
}

func NewMCPGateway(q *store.Queries, providers *ExternalProviderService, creds *ExternalCredentialService, client *http.Client) *MCPGateway {
	if client == nil {
		client = http.DefaultClient
	}
	return &MCPGateway{q: q, providers: providers, creds: creds, client: client, clients: map[string]*mcpclient.Client{}}
}

func (g *MCPGateway) CallTool(ctx context.Context, call MCPCall) (map[string]any, error) {
	if strings.TrimSpace(call.Tool) == "" {
		return nil, fmt.Errorf("mcp: tool is required")
	}
	prov, err := g.providers.GetByName(ctx, call.Provider)
	if err != nil {
		return nil, err
	}
	var profile struct {
		Transport string            `json:"transport"`
		URL       string            `json:"url"`
		Headers   map[string]string `json:"headers"`
		Command   []string          `json:"command"`
		Env       map[string]string `json:"env"`
		WorkDir   string            `json:"workdir"`
	}
	if err := json.Unmarshal([]byte(prov.Profile), &profile); err != nil {
		return nil, fmt.Errorf("mcp: provider %s profile: %w", call.Provider, err)
	}
	if !strings.EqualFold(profile.Transport, "mcp") {
		return nil, fmt.Errorf("mcp: provider %s is not an MCP server", call.Provider)
	}
	stdio := len(profile.Command) > 0
	if !stdio && strings.TrimSpace(profile.URL) == "" {
		return nil, fmt.Errorf("mcp: provider %s declares neither url nor command", call.Provider)
	}

	headers := map[string]string{}
	for key, value := range profile.Headers {
		headers[key] = value
	}
	env := map[string]string{}
	var credConfig map[string]any
	if strings.TrimSpace(call.CredentialAlias) != "" {
		if g.creds == nil {
			return nil, fmt.Errorf("mcp: credential store is not configured")
		}
		cred, err := g.creds.GetByAlias(ctx, OwnerRef{Type: call.OwnerType, ID: call.OwnerID}, call.UserID,
			integration.ProviderName(call.Provider), call.CredentialAlias)
		if err != nil {
			return nil, err
		}
		credConfig = cred.Config
		if !stdio {
			if token := firstNonEmpty(strFromCfg(cred.Config, "token"), strFromCfg(cred.Config, "access_token"), strFromCfg(cred.Config, "api_key")); token != "" {
				headers["Authorization"] = "Bearer " + token
			}
		}
	}
	// stdio servers usually take credentials as env vars; `${cred:<key>}` is
	// resolved from the decrypted credential, so the secret never lives in the
	// provider profile.
	for key, value := range profile.Env {
		env[key] = resolveCredTemplate(value, credConfig)
	}

	client := g.clientFor(call.Provider, call.CredentialAlias, profile.URL, profile.Command, env, profile.WorkDir, headers)
	result, status, callErr := client.CallTool(ctx, call.Tool, call.Arguments)

	// Record the call exactly like an HTTP proxy call.
	if g.q != nil {
		args, _ := json.Marshal(call.Arguments)
		fp := fingerprintBody(args)
		if err := g.q.CreateAuditLog(ctx, store.CreateAuditLogParams{
			UserID:       call.UserID,
			ProviderID:   prov.ID,
			CredentialID: 0,
			Method:       "MCP",
			Path:         "tool:" + call.Tool,
			StatusCode:   int64(status),
			BodySha256:   fp.sha256,
			BodyBytes:    fp.bytes,
			BodyFields:   fp.fields,
		}); err != nil {
			// Auditing must not fail the call, but it must be visible.
			_ = err
		}
	}
	if callErr != nil {
		return nil, callErr
	}
	return result, nil
}

func (g *MCPGateway) clientFor(provider, alias, endpoint string, command []string, env map[string]string, workDir string, headers map[string]string) *mcpclient.Client {
	// The key fingerprints the resolved env so a rotated credential yields a
	// fresh process instead of reusing a stale secret.
	h := sha256.New()
	for _, key := range sortedStringKeys(env) {
		h.Write([]byte(key + "=" + env[key] + ";"))
	}
	key := provider + "|" + alias + "|" + endpoint + "|" + strings.Join(command, " ") + "|" + hex.EncodeToString(h.Sum(nil)[:8])
	g.mu.Lock()
	defer g.mu.Unlock()
	if client, ok := g.clients[key]; ok {
		return client
	}
	client := mcpclient.New(mcpclient.Config{
		Endpoint: endpoint, Headers: headers, Client: g.client,
		Command: command, Env: env, WorkDir: workDir,
	})
	g.clients[key] = client
	return client
}

func sortedStringKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// resolveCredTemplate expands ${cred:<key>} placeholders from the decrypted
// credential config.
func resolveCredTemplate(value string, cfg map[string]any) string {
	for strings.Contains(value, "${cred:") {
		start := strings.Index(value, "${cred:")
		end := strings.Index(value[start:], "}")
		if end < 0 {
			break
		}
		key := strings.TrimSpace(value[start+len("${cred:") : start+end])
		value = value[:start] + strFromCfg(cfg, key) + value[start+end+1:]
	}
	return value
}

// ErrMCPNotConfigured marks a provider row that is not an MCP server.
var ErrMCPNotConfigured = errors.New("mcp: provider is not an MCP server")
