package adapter

// NiuniuAgentAdapter is the marker adapter for niuniu's own agent (the
// agent/ Go module). Like goose and omp, it does not use the parse-the-stdout
// model: the agentproxy session layer routes TypeNiuniuAgent workspaces to
// the bidirectional agentbackend.niuniuagent.Backend (ACP over
// `niuniu-agent acp`) before the standard ProcessMode dispatch. This adapter
// exists so adapter.For("niuniu") and cli_type==="niuniu" stay symmetric with
// the other engines.
type NiuniuAgentAdapter struct{}

// Type returns TypeNiuniuAgent.
func (NiuniuAgentAdapter) Type() Type { return TypeNiuniuAgent }

// ProcessMode is declared long-running for symmetry; the Send bridge
// intercepts TypeNiuniuAgent before this is consulted.
func (NiuniuAgentAdapter) ProcessMode() ProcessMode { return ProcessLongRunning }

// DisplayName returns the CLI base name, defaulting to "niuniu-agent".
func (NiuniuAgentAdapter) DisplayName(command string) string {
	return cliBaseName(command, "niuniu-agent")
}

// ParseLine is unused for niuniu-agent (ACP frame handling lives in
// agentbackend.niuniuagent).
func (NiuniuAgentAdapter) ParseLine(line string) ([]ParsedEvent, error) {
	return nil, nil
}

// BuildSpawn returns the ACP invocation (`niuniu-agent acp`). niuniu injects
// env and model selection via the backend's Options, not here.
func (NiuniuAgentAdapter) BuildSpawn(opts SpawnOptions) (string, []string) {
	command := opts.Command
	if command == "" {
		command = "niuniu-agent"
	}
	args := append([]string{"acp"}, opts.ExtraArgs...)
	return command, args
}

// InjectEnv passes the base env through; the agent reads
// ANTHROPIC_*/OPENAI_*/NIUNIU_AGENT_PROVIDER natively (both protocol
// families), handled by agentbackend.niuniuagent.
func (NiuniuAgentAdapter) InjectEnv(base []string, _ EnvOptions) []string {
	return base
}

// PermissionArgs returns nil: the permission surface is the ACP
// session/request_permission sub-protocol, not CLI flags.
func (NiuniuAgentAdapter) PermissionArgs(PermissionOptions) []string {
	return nil
}
