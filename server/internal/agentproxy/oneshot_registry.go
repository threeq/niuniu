package agentproxy

import (
	"context"
	"fmt"

	"github.com/niuniu-dev/niuniu/internal/agentproxy/adapter"
)

// --- one-shot exec builder registry ---
//
// A one-shot engine turns a workspace row + config + resolved env into the
// (command, argv, env) triple that runOneShotTurn spawns for a single turn.
// Each engine differs in what it must resolve first: Codex reads a per-workspace
// sandbox/approval row and writes .codex/config.toml; Qwen and Cursor need none
// of that.
//
// Why this is a registry in agentproxy rather than a BuildExec method on
// adapter.Adapter: the adapter package deliberately has ZERO dependencies on
// niuniu's internal packages (verify with
// `grep -r niuniu/internal internal/agentproxy/adapter/`). It is a pure
// parse/argv layer, which is what makes it trivially testable. A BuildExec
// method would have to receive store queries, *config.Config and sceneenv —
// dragging all of them into that layer and destroying the property. So the
// dispatch table lives here, on the side that already owns those dependencies,
// and the adapter stays pure.
//
// To add a one-shot engine: implement the builder as a method on
// *WorkspaceSession (see qwen_exec.go / cursor_exec.go for the trimmed shape)
// and register it below. No `if` chain to edit.

// oneShotExecBuilder resolves everything a single one-shot turn needs to spawn:
// the executable, its argv, and the full environment.
type oneShotExecBuilder func(s *WorkspaceSession, ctx context.Context, workDir string) (string, []string, []string, error)

// oneShotExecBuilders maps a CLI type to its exec builder. Engines absent from
// this table are not one-shot (they are long-running or protocol-driven) and
// never reach buildOneShotExec.
//
// Codex is the default rather than a table entry: buildOneShotExec falls back to
// it for a nil adapter, preserving the legacy "no cli_type means codex here"
// behavior that codex_appserver.go also depends on.
var oneShotExecBuilders = map[adapter.Type]oneShotExecBuilder{
	adapter.TypeQwen:   (*WorkspaceSession).buildQwenOneShotExec,
	adapter.TypeCodex:  (*WorkspaceSession).buildCodexOneShotExec,
}

// buildOneShotExec resolves the (command, argv, env) triple for one one-shot
// turn by dispatching through oneShotExecBuilders.
//
// A nil adapter falls back to the Codex builder: that is the legacy
// "no cli_type here means codex" behavior, which buildCodexAppServerRuntime in
// codex_appserver.go relies on when it calls this for command+env only.
func (s *WorkspaceSession) buildOneShotExec(ctx context.Context, workDir string) (string, []string, []string, error) {
	oneShotAdapter := s.cliAdapter
	if oneShotAdapter == nil {
		return s.buildCodexOneShotExec(ctx, workDir)
	}
	if build, ok := oneShotExecBuilders[oneShotAdapter.Type()]; ok {
		return build(s, ctx, workDir)
	}
	// A long-running engine reached the one-shot runner: that is a dispatch bug
	// in Send (proxy.go), not user input. Fail loudly rather than silently
	// spawning some other engine's binary.
	return "", nil, nil, fmt.Errorf("no one-shot exec builder registered for cli_type %q", oneShotAdapter.Type())
}
