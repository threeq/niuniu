// niuniu-agent workspace projection: the host-side half of the capability
// injection contract. For cli_type=niuniu workspaces niuniu writes
//
//	<workDir>/.niuniu-agent/inject.md — capability instructions the agent
//	                                   loads into its system prompt (the
//	                                   agent-side contract is in
//	                                   agent/internal/prompt.LoadInject)
//	<workDir>/.mcp.json               — the same generated MCP config the
//	                                   claude engine gets (niuniu-mcp), so
//	                                   the agent's own MCP client mounts
//	                                   the niuniu tool family
//
// Both writes are best-effort: any failure logs and the session proceeds
// without the injection (the agent runs fine standalone).
package agentproxy

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/niuniu-dev/niuniu/internal/config"
)

// niuniuInject is projected verbatim into <workDir>/.niuniu-agent/inject.md.
// Memory interop: prefer the niuniu-mcp memory tools (server-side storage,
// shared across engines/sessions); the agent's native local memory is the
// fallback for standalone runs.
func renderNiuniuInject() string {
	return `# niuniu 工作空间能力注入（由 niuniu host 自动生成）

## niuniu-mcp 工具族
本工作空间已通过 .mcp.json 挂载 niuniu-mcp（工具名前缀 mcp__niuniu__）：
- 看板：list_issues / get_issue_detail / advance_issue / update_issue / request_changes / approve_review —— 参与看板流程用
- 黑板：blackboard_read / blackboard_write —— 跨 agent 共享的计划、代码、结论
- 收件箱：inbox_read / inbox_send —— 与团队成员互通
- 记忆：memory_search / memory_generate 等 —— 服务端存储，跨会话、跨引擎共享

记忆互通约定：优先用 niuniu-mcp 的记忆工具（服务端存储）；本 agent 的本地记忆（MemorySave/MemorySearch）仅在脱离 niuniu 独立运行时作为回退。

## AUTOHOST 收尾约定
自动托管会话中，当整体目标真正全部达成、且代码已提交并合并到目标分支、看板已收尾后，在回复最末尾【单独一行】输出：
[AUTOHOST_DONE]
只要有任何一项未满足，切勿输出该标记。

## 看板纪律
- 只调整当前 issue 的状态；不为已完成的工作新建或修改其它 issue
- 移入完成列前：全部代码改动已提交并合并到目标分支
- 工程底线纪律（构建/测试全绿、不绕过审查）不可绕过
`
}

// writeNiuniuInject writes the injection file (idempotent, overwrites so
// content stays fresh across sessions).
func writeNiuniuInject(workDir string) error {
	dir := filepath.Join(workDir, ".niuniu-agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "inject.md"), []byte(renderNiuniuInject()), 0o644)
}

// projectNiuniuAgentFiles writes inject.md and regenerates .mcp.json (with
// niuniu-mcp) for a cli_type=niuniu workspace. gen may be nil (MCP config
// unavailable — injection still lands). Returns joined errors; callers log
// and proceed.
func projectNiuniuAgentFiles(workDir, sessionToken string, gen MCPConfigWriter, projectID, workspaceID int64, inboxDir string) error {
	var errs []error
	if err := writeNiuniuInject(workDir); err != nil {
		errs = append(errs, fmt.Errorf("write inject.md: %w", err))
	}
	if gen != nil {
		opts := config.MCPGenerateOptions{
			ProjectID:    projectID,
			WorkspaceID:  workspaceID,
			InboxDir:     inboxDir,
			SessionToken: sessionToken,
		}
		if _, err := gen.Generate(workDir, opts, nil, ""); err != nil {
			errs = append(errs, fmt.Errorf("generate .mcp.json: %w", err))
		}
	}
	switch len(errs) {
	case 0:
		return nil
	case 1:
		return errs[0]
	default:
		return fmt.Errorf("%v; %v", errs[0], errs[1])
	}
}
