package adapter

// CursorAdapter is the marker adapter for Cursor CLI workspaces. Like OmpAdapter
// and GooseAdapter it does NOT use the parse-the-stdout model the Claude / Codex
// / Qwen adapters serve: the agentproxy session layer routes TypeCursor
// workspaces to agentbackend/cursor (ACP over `agent acp`) before the standard
// ProcessMode dispatch (see agentproxy.Send).
//
// This adapter exists so adapter.For("cursor") and cli_type==="cursor" stay
// symmetric with the other engines and the marker Type is visible to the session
// layer.
//
// History: this was briefly a full stream-json parsing adapter driving
// `cursor-agent -p --output-format stream-json`. That was replaced by the ACP
// backend because the headless print path cannot express a permission gate,
// multi-turn continuity, or cooperative cancellation, and has a reported defect
// where MCP servers are not injected into the agent's toolset at all — which
// would leave a cursor workspace unable to call niuniu-mcp (i.e. unable to move
// its own kanban card).
type CursorAdapter struct{}

// Type returns TypeCursor.
func (CursorAdapter) Type() Type { return TypeCursor }

// ProcessMode is declared long-running for symmetry with the other
// protocol-driven backends; the Send bridge intercepts TypeCursor before this is
// consulted.
func (CursorAdapter) ProcessMode() ProcessMode { return ProcessLongRunning }

// DisplayName returns the engine name. `agent` is Cursor's current binary name
// (with `cursor-agent` as a back-compat alias), but "agent" alone is a uselessly
// generic label in the UI, so an unset command falls back to the engine name
// rather than the binary name.
func (CursorAdapter) DisplayName(command string) string {
	if command == "" {
		return "cursor"
	}
	return cliBaseName(command, "cursor")
}

// ParseLine is unused for cursor (ACP frame handling lives in
// agentbackend/cursor).
func (CursorAdapter) ParseLine(line string) ([]ParsedEvent, error) {
	return nil, nil
}

// BuildSpawn returns the ACP invocation. The backend builds the real argv
// (including the root-level --model flag, which must precede the subcommand), so
// this exists only to keep the interface total.
func (CursorAdapter) BuildSpawn(opts SpawnOptions) (string, []string) {
	command := opts.Command
	if command == "" {
		command = "agent"
	}
	args := append([]string{"acp"}, opts.ExtraArgs...)
	return command, args
}

// InjectEnv passes the base env through; cursor's credentials come from its own
// login (~/.local/share/cursor-agent) or CURSOR_API_KEY / CURSOR_AUTH_TOKEN,
// and agentbackend/cursor seeds the child env.
func (CursorAdapter) InjectEnv(base []string, _ EnvOptions) []string {
	return base
}

// PermissionArgs returns nil: cursor's permission surface is the ACP
// session/request_permission sub-protocol, not CLI flags.
func (CursorAdapter) PermissionArgs(PermissionOptions) []string {
	return nil
}
