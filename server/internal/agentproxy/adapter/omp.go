package adapter

// OmpAdapter is the marker adapter for oh-my-pi (omp) workspaces. omp does not
// use the parse-the-stdout model the other adapters serve: the agentproxy
// session layer routes TypeOmp workspaces to the bidirectional
// agentbackend.omp.Backend (RPC over `omp --mode rpc`) before the standard
// ProcessMode dispatch. This adapter exists so adapter.For("omp") and
// cli_type==="omp" stay symmetric with the other engines and the marker Type is
// visible to the session layer.
type OmpAdapter struct{}

// Type returns TypeOmp.
func (OmpAdapter) Type() Type { return TypeOmp }

// ProcessMode is declared long-running for symmetry; the Send bridge intercepts
// TypeOmp before this is consulted.
func (OmpAdapter) ProcessMode() ProcessMode { return ProcessLongRunning }

// DisplayName returns the CLI base name, defaulting to "omp".
func (OmpAdapter) DisplayName(command string) string {
	return cliBaseName(command, "omp")
}

// ParseLine is unused for omp (frame handling lives in agentbackend.omp).
func (OmpAdapter) ParseLine(line string) ([]ParsedEvent, error) {
	return nil, nil
}

// BuildSpawn returns the omp RPC invocation. niuniu injects the trimmed env and
// model selection via the backend's Options, not here.
func (OmpAdapter) BuildSpawn(opts SpawnOptions) (string, []string) {
	command := opts.Command
	if command == "" {
		command = "omp"
	}
	args := append([]string{"--mode", "rpc"}, opts.ExtraArgs...)
	return command, args
}

// InjectEnv passes the base env through; omp's env surface is handled by
// agentbackend.omp (model/provider selection, capability trimming).
func (OmpAdapter) InjectEnv(base []string, _ EnvOptions) []string {
	return base
}

// PermissionArgs returns nil: omp's permission surface is the RPC
// extension_ui_request sub-protocol, not CLI flags.
func (OmpAdapter) PermissionArgs(PermissionOptions) []string {
	return nil
}