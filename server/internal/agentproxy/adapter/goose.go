package adapter

// GooseAdapter is the marker adapter for Block's Goose agent workspaces. goose
// does not use the parse-the-stdout model the other adapters serve: the
// agentproxy session layer routes TypeGoose workspaces to the bidirectional
// agentbackend.goose.Backend (ACP over `goose acp`) before the standard
// ProcessMode dispatch. This adapter exists so adapter.For("goose") and
// cli_type==="goose" stay symmetric with the other engines and the marker Type
// is visible to the session layer.
type GooseAdapter struct{}

// Type returns TypeGoose.
func (GooseAdapter) Type() Type { return TypeGoose }

// ProcessMode is declared long-running for symmetry; the Send bridge intercepts
// TypeGoose before this is consulted.
func (GooseAdapter) ProcessMode() ProcessMode { return ProcessLongRunning }

// DisplayName returns the CLI base name, defaulting to "goose".
func (GooseAdapter) DisplayName(command string) string {
	return cliBaseName(command, "goose")
}

// ParseLine is unused for goose (ACP frame handling lives in agentbackend.goose).
func (GooseAdapter) ParseLine(line string) ([]ParsedEvent, error) {
	return nil, nil
}

// BuildSpawn returns the goose ACP invocation. niuniu injects the trimmed env
// and model selection via the backend's Options, not here.
func (GooseAdapter) BuildSpawn(opts SpawnOptions) (string, []string) {
	command := opts.Command
	if command == "" {
		command = "goose"
	}
	args := append([]string{"acp"}, opts.ExtraArgs...)
	return command, args
}

// InjectEnv passes the base env through; goose's env surface (GOOSE_PROVIDER /
// GOOSE_MODEL) is handled by agentbackend.goose.
func (GooseAdapter) InjectEnv(base []string, _ EnvOptions) []string {
	return base
}

// PermissionArgs returns nil: goose's permission surface is the ACP
// session/request_permission sub-protocol, not CLI flags.
func (GooseAdapter) PermissionArgs(PermissionOptions) []string {
	return nil
}