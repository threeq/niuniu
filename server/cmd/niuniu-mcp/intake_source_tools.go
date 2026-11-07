// intake_source_tools.go — registers the seven intake-binding MCP tools
// (design §10.3). All are project-scoped: the pid closure from main.go is
// captured at registration time, so no project_id argument is exposed.
//
// Tool surface:
//
//	intake_source_list            — list ci/email/webhook bindings
//	intake_source_create          — create a binding; returns {source, secret_link}
//	intake_source_test            — normalize a synthetic sample, no task created
//	intake_source_rotate          — rotate the secret; returns a new secret_link
//	intake_source_delete          — remove a binding
//	intake_source_poll            — poll an IMAP binding immediately
//	intake_source_credential_link — one-time IMAP credential submission link
//
// Secrets never enter the conversation: create and rotate return a one-time
// link (15 minutes, single use) rather than the plaintext secret, and the
// credential-link tool asks the user to type the password in the browser.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// imapCredentialFields is the field set the one-time page renders for an IMAP
// mailbox credential. Only field NAMES travel through the tool call.
var imapCredentialFields = []string{
	"imap_host", "imap_port", "user", "password", "security", "smtp_host", "smtp_port",
}

func registerIntakeSourceTools(s *server.MCPServer, api *apiClient, pid string) {
	sourcesPath := "/mcp/projects/" + pid + "/intake/sources"

	s.AddTool(
		mcp.NewTool("intake_source_list",
			mcp.WithDescription("List this project's intake bindings (ci/email/webhook). Each binding carries its "+
				"source_kind, mode, provider, inbound_path, has_secret, health status, 24h event count and config. "+
				"The plaintext secret is never returned."),
			mcp.WithString("source_kind", mcp.Description("Optional filter: ci | email | webhook (omit for all)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			path := sourcesPath
			if kind, ok := req.GetArguments()["source_kind"].(string); ok && kind != "" {
				path += "?source_kind=" + url.QueryEscape(kind)
			}
			data, err := api.get(path)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("intake_source_create",
			mcp.WithDescription("Create an intake binding for ci, email or webhook. Returns {source, secret_link?}. "+
				"The plaintext secret is NEVER returned: secret_link is a single-use page (15 minutes) the user opens "+
				"to read the secret once — give it to the user verbatim. Email + mode=imap REQUIRES an existing "+
				"imap credential: when the user has none, call intake_source_credential_link first, wait for the "+
				"submission, then retry with credential_alias (the alias you gave the link) or credential_id."),
			mcp.WithString("source_kind", mcp.Description("ci | email | webhook"), mcp.Required()),
			mcp.WithString("name", mcp.Description("Binding name (1-64 chars, unique among this project's bindings)"), mcp.Required()),
			mcp.WithString("mode", mcp.Description("Transport: push (ci/webhook/email inbound) | imap (email polling). Defaults per source_kind.")),
			mcp.WithString("provider", mcp.Description("Preset provider, e.g. jenkins, gitlab, github, generic for ci; inbound provider for email")),
			mcp.WithObject("config", mcp.Description("Binding config: ci events/local_only; email folder/interval_minutes; webhook event filters, max_bytes, replay_window")),
			mcp.WithNumber("credential_id", mcp.Description("Existing imap credential ID (required for email + mode=imap unless credential_alias is given)")),
			mcp.WithString("credential_alias", mcp.Description("Alias of the imap credential created via intake_source_credential_link; resolved to its ID. Use this when no credential_id has been observed.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			kind, errRes := requireString(args, "source_kind")
			if errRes != nil {
				return errRes, nil
			}
			name, errRes := requireString(args, "name")
			if errRes != nil {
				return errRes, nil
			}
			payload := map[string]any{"source_kind": kind, "name": name}
			if v, ok := args["mode"].(string); ok && v != "" {
				payload["mode"] = v
			}
			if v, ok := args["provider"].(string); ok && v != "" {
				payload["provider"] = v
			}
			if v, ok := args["config"].(map[string]any); ok {
				payload["config"] = v
			}
			if v, ok := args["credential_id"].(float64); ok && v > 0 {
				payload["credential_id"] = int64(v)
			} else if alias, ok := args["credential_alias"].(string); ok && alias != "" {
				id, err := api.intakeCredentialIDByAlias(alias)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				payload["credential_id"] = id
			}
			data, err := api.post(sourcesPath, payload)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("intake_source_test",
			mcp.WithDescription("Dry-run a synthetic delivery through a binding: normalizes the sample and evaluates "+
				"the policy, but records nothing and creates no task. Use it to show the user what a real delivery "+
				"would do before they wire up CI or email."),
			mcp.WithNumber("source_id", mcp.Description("Binding ID from intake_source_list"), mcp.Required()),
			mcp.WithObject("sample", mcp.Description("Optional provider-shaped sample payload; omit to synthesize one")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			id, errRes := requireNumber(args, "source_id")
			if errRes != nil {
				return errRes, nil
			}
			payload := map[string]any{}
			if sample, ok := args["sample"]; ok && sample != nil {
				payload["sample"] = sample
			}
			data, err := api.post(sourcesPath+"/"+int64Arg(int64(id))+"/test", payload)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("intake_source_rotate",
			mcp.WithDescription("Rotate a binding's secret. Returns {source, secret_link} — the new plaintext secret is "+
				"NEVER returned; secret_link is a single-use page (15 minutes) the user opens once. The old secret "+
				"stops working immediately."),
			mcp.WithNumber("source_id", mcp.Description("Binding ID from intake_source_list"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			id, errRes := requireNumber(args, "source_id")
			if errRes != nil {
				return errRes, nil
			}
			data, err := api.post(sourcesPath+"/"+int64Arg(int64(id))+"/rotate", nil)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("intake_source_delete",
			mcp.WithDescription("Delete an intake binding. Confirm with the user first: the binding stops receiving "+
				"deliveries immediately and its credential binding is released."),
			mcp.WithNumber("source_id", mcp.Description("Binding ID from intake_source_list"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			id, errRes := requireNumber(args, "source_id")
			if errRes != nil {
				return errRes, nil
			}
			data, err := api.delete(sourcesPath + "/" + int64Arg(int64(id)))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("intake_source_poll",
			mcp.WithDescription("Poll an email IMAP binding immediately instead of waiting for its interval. "+
				"Only IMAP bindings can be polled; push bindings return an error."),
			mcp.WithNumber("source_id", mcp.Description("Binding ID from intake_source_list"), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			id, errRes := requireNumber(args, "source_id")
			if errRes != nil {
				return errRes, nil
			}
			data, err := api.post(sourcesPath+"/"+int64Arg(int64(id))+"/poll", nil)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	// intake_source_credential_link — for email + IMAP when the user has no
	// credential yet. The password is typed by the user in the browser; the
	// agent only ever sees the link and, later, a boolean credential_status.
	s.AddTool(
		mcp.NewTool("intake_source_credential_link",
			mcp.WithDescription("Mint a one-time secure link (15 minutes) for the user to enter a mailbox password. "+
				"Use it when setting up email + mode=imap and no imap credential exists yet: give the returned "+
				"link_markdown to the user verbatim, NEVER ask for the password in chat. After they submit, retry "+
				"intake_source_create with credential_alias set to the alias returned here (or with credential_id)."),
			mcp.WithNumber("source_id", mcp.Description("Optional: an existing email/imap binding to replace the credential for")),
			mcp.WithString("name", mcp.Description("Mailbox label used as the credential alias (max 40 chars), e.g. 'support@example.com'")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			alias := "imap"
			if name, ok := args["name"].(string); ok && name != "" {
				alias = name
			}
			// When a source is named, resolve its display name so the submission
			// page is labelled for the binding the user is configuring.
			if sid, ok := args["source_id"].(float64); ok && sid > 0 {
				data, err := api.get(sourcesPath)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				name, err := intakeSourceName(data, int64(sid))
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				alias = name
			}
			if len([]rune(alias)) > 40 {
				return mcp.NewToolResultError("the credential alias must be 40 characters or fewer — pass a shorter name"), nil
			}
			data, err := api.post("/mcp/projects/"+pid+"/credential-links", map[string]any{
				"kind":   "provider_credential",
				"title":  "IMAP 邮箱凭据 · " + alias,
				"fields": imapCredentialFields,
				"target": map[string]any{"provider": "imap", "alias": alias},
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)
}

func int64Arg(v int64) string { return strconv.FormatInt(v, 10) }

// intakeCredentialIDByAlias resolves the credential ID created through a
// one-time link. The submission page reports the ID to the user's browser, not
// to the agent, so the agent identifies the credential by the alias it chose.
func (c *apiClient) intakeCredentialIDByAlias(alias string) (int64, error) {
	data, err := c.get("/mcp/external-credentials?provider=imap")
	if err != nil {
		return 0, err
	}
	var body struct {
		Items []struct {
			ID       int64  `json:"id"`
			Provider string `json:"provider"`
			Alias    string `json:"alias"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return 0, fmt.Errorf("could not read the credential list: %w", err)
	}
	for _, it := range body.Items {
		if it.Alias == alias {
			return it.ID, nil
		}
	}
	return 0, fmt.Errorf("no imap credential with alias %q — has the user opened the link and submitted it?", alias)
}

// intakeSourceName finds a binding's display name in an intake_source_list
// response. It also rejects non-email bindings, since an IMAP credential link
// is only meaningful for email.
func intakeSourceName(listJSON []byte, id int64) (string, error) {
	var body struct {
		Sources []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			SourceKind string `json:"source_kind"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(listJSON, &body); err != nil {
		return "", fmt.Errorf("could not read the source list: %w", err)
	}
	for _, s := range body.Sources {
		if s.ID != id {
			continue
		}
		if s.SourceKind != "email" {
			return "", fmt.Errorf("intake source %d is %q, not email — an IMAP credential link only applies to email", id, s.SourceKind)
		}
		return s.Name, nil
	}
	return "", fmt.Errorf("intake source %d not found in this project", id)
}
