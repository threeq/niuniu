// credential_tools.go — registers the generic one-time credential-link MCP
// tools (issue #727), generalizing imbot_request_credential_link to provider
// credentials and data sources.
//
// Tool surface:
//   request_credential_link — mint a one-time link the user opens to submit a
//                             secret; the agent never sees the value
//   credential_status       — ask whether a target already holds a credential
//                             (boolean only, never the value)
//
// Both are project-scoped: the pid closure from main.go is captured at
// registration time; no project_id argument is exposed to the agent.
package main

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/niuniu-dev/niuniu/internal/credpolicy"
)

func registerCredentialTools(s *server.MCPServer, api *apiClient, pid string) {
	// request_credential_link — mint the link BEFORE asking the user for anything.
	// The tool accepts only NON-SECRET metadata (which kind of target, a title,
	// the field NAMES to render, and where the values get written); values are
	// typed by the user in the browser, never in the conversation.
	s.AddTool(
		mcp.NewTool("request_credential_link",
			mcp.WithDescription("Mint a one-time secure link for the user to submit a credential (password / API key / token). "+
				credpolicy.Discipline+" "+
				"The link expires in 15 minutes and works once. Give the returned link_markdown to the user verbatim; "+
				"never ask them to paste the secret into the chat. After the user submits, call credential_status to confirm "+
				"a credential landed (it answers only has_credential — you can never read the value). "+
				"USE THIS INSTEAD OF collecting secrets yourself for: (a) kind=provider_credential when adding/updating an "+
				"external API provider credential (target: {provider, alias, auth_mode?, credential_id?}); (b) kind=data_source "+
				"when creating/updating a data source (target: {name, kind, source_id?, scope_config?, default_access_mode?, require_confirm?}). "+
				"For a create you must pass the non-secret connection identity in `target` too (e.g. provider_credential: provider+alias; "+
				"data_source: name+kind) — but never a secret. `fields` lists the field NAMES the page should render, e.g. "+
				"[\"host\",\"port\",\"user\",\"password\",\"database\"] or [\"token\"] or [\"user\",\"password\"]."),
			mcp.WithString("kind", mcp.Description("credential kind: provider_credential | data_source"), mcp.Required()),
			mcp.WithString("title", mcp.Description("Human-readable target description shown to the user, e.g. 'MySQL 生产库凭证'"), mcp.Required()),
			mcp.WithArray("fields",
				mcp.Description("Field NAMES the submission page renders (never values). Known names: host, port, user, username, password, database, token, api_key, secret, app_id, app_secret, bot_token, client_id, client_secret, corp_id, agent_id, aes_key, base_path, list_path, scheme, headers, imap_host, imap_port, smtp_host, smtp_port, security, ssh_host, ssh_port, ssh_user, ssh_password, private_key. Unknown names render as secret fields."),
				mcp.Items(map[string]any{"type": "string"}),
				mcp.Required(),
			),
			mcp.WithObject("target", mcp.Description("NON-SECRET target identity. provider_credential: {provider, alias, auth_mode?, credential_id?}. data_source: {name, kind, source_id?, scope_config?, default_access_mode?, require_confirm?}. source_id>0 / credential_id>0 means update an existing row (blank fields keep the stored secret).")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			kind, errRes := requireString(args, "kind")
			if errRes != nil {
				return errRes, nil
			}
			title, errRes := requireString(args, "title")
			if errRes != nil {
				return errRes, nil
			}
			fields := stringSliceArg(args["fields"])
			if len(fields) == 0 {
				return mcp.NewToolResultError("fields is required: a non-empty array of field names"), nil
			}
			payload := map[string]any{"kind": kind, "title": title, "fields": fields}
			if t, ok := args["target"].(map[string]any); ok {
				payload["target"] = t
			}
			data, err := api.post("/mcp/projects/"+pid+"/credential-links", payload)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	// credential_status — the only thing an agent may learn about a stored
	// secret. Returns {has_credential: bool}.
	s.AddTool(
		mcp.NewTool("credential_status",
			mcp.WithDescription("Check whether a credential has already been stored for a target. "+
				"Returns ONLY {has_credential: true|false} — the secret value is never returned, logged, or exposed to you. "+
				"Call this after sending the user a request_credential_link, to confirm they submitted it. "+
				"kind + target must identify the same target you passed to request_credential_link."),
			mcp.WithString("kind", mcp.Description("credential kind: provider_credential | data_source"), mcp.Required()),
			mcp.WithObject("target", mcp.Description("NON-SECRET target identity, same shape as request_credential_link: provider_credential {provider, alias, credential_id?}; data_source {source_id} or {name} (use `name` right after a create link — that is how you confirm the submission landed)."), mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := req.GetArguments()
			kind, errRes := requireString(args, "kind")
			if errRes != nil {
				return errRes, nil
			}
			target, _ := args["target"].(map[string]any)
			data, err := api.post("/mcp/projects/"+pid+"/credential-links/status", map[string]any{
				"kind":   kind,
				"target": target,
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		},
	)
}

// stringSliceArg coerces a JSON array of strings from the tool arguments,
// dropping non-string and empty entries.
func stringSliceArg(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}
