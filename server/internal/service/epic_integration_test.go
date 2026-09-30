package service

// Epic 集成收尾 (spec docs/superpowers/specs/2026-09-28-epic-unified-branch-design.md
// §4, 2026-09-30 修订：删除服务端合并流水线)。新流程：epic 编排工作空间完成 →
// terminateEpic(..., "done") 向同一控制工作空间发送集成提示词——AI 在自己的工作树内
// （统一分支后检出 epic/<id>）把 main 合入当前分支、就地解冲突、验证构建/测试，
// 未经用户确认不得推送 origin。本地 main 不再被系统推进（② epic→main 快进与
// ③ 父工作空间同步随服务端流水线一并删除）。
//
// Real-git tests reusing the unifiedBranchEnv helpers from
// epic_unified_branch_test.go (真实 git 仓库 + 真实工作空间创建).

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
)

// integrationDelivery records one delivered message and its target workspace.
type integrationDelivery struct {
	wsID    int64
	content string
}

// integrationFakeProxy is the package-internal agent proxy fake: it records
// every delivered message so tests can assert the integration prompt and its
// target workspace. Mutex-guarded because the workspace-created hook delivers
// from its own goroutine (go s.onCreated) while the test reads.
type integrationFakeProxy struct {
	mu         sync.Mutex
	deliveries []integrationDelivery
}

func (p *integrationFakeProxy) GetOrStartSession(context.Context, int64, int64) (AgentSession, error) {
	return nil, nil
}
func (p *integrationFakeProxy) GetSession(int64) AgentSession  { return nil }
func (p *integrationFakeProxy) PrepareUserSend(context.Context, int64) {}

func (p *integrationFakeProxy) Deliver(_ context.Context, wsID int64, _, content, _ string) (bool, int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deliveries = append(p.deliveries, integrationDelivery{wsID: wsID, content: content})
	return false, 0, nil
}

// snapshot returns a copy of the recorded deliveries, safe to read while the
// hook goroutine keeps delivering.
func (p *integrationFakeProxy) snapshot() []integrationDelivery {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]integrationDelivery, len(p.deliveries))
	copy(out, p.deliveries)
	return out
}

// integrationEnv wires the real unified-branch env with the fake proxy capturing
// every delivered prompt. The workspace-created hook is wired exactly like
// production (server.go: SetWorkspaceCreatedHook(OnWorkspaceCreated)), so a
// created epic workspace gets the real orchestration kickoff through the proxy.
func integrationEnv(t *testing.T) (*unifiedBranchEnv, *integrationFakeProxy) {
	t.Helper()
	e := setupUnifiedBranchEnv(t)
	proxy := &integrationFakeProxy{}
	e.epicSvc.SetAgentProxy(proxy)
	e.wsSvc.SetWorkspaceCreatedHook(e.epicSvc.OnWorkspaceCreated)
	return e, proxy
}

// awaitDeliveries polls until the proxy has recorded at least n messages. The
// OnWorkspaceCreated hook runs asynchronously (go s.onCreated), so the
// orchestration kickoff may land a beat after StartWorkspaceForIssue returns.
func awaitDeliveries(t *testing.T, proxy *integrationFakeProxy, n int) {
	t.Helper()
	require.Eventually(t, func() bool { return len(proxy.snapshot()) >= n },
		5*time.Second, 10*time.Millisecond, "expected >= %d deliveries, got %d", n, len(proxy.snapshot()))
}

// TestEpicTerminate_DoneSendsIntegrationPrompt pins the new §4 flow end to end:
// the epic orchestration workspace completes → terminateEpic writes exec_status
// "done" → the integration prompt is delivered to the SAME control workspace and
// tells the agent to git-merge main into its current branch (epic/<id>), resolve
// conflicts in place, verify build/tests, and get user confirmation before any
// push to origin.
func TestEpicTerminate_DoneSendsIntegrationPrompt(t *testing.T) {
	e, proxy := integrationEnv(t)
	epicID := e.makeEpic(t, "集成收尾 epic")
	out, err := e.epicSvc.StartWorkspaceForIssue(e.ctx, epicID)
	require.NoError(t, err)
	awaitDeliveries(t, proxy, 1) // the OnWorkspaceCreated orchestration kickoff
	before := len(proxy.snapshot())

	e.epicSvc.OnWorkspaceCompleted(e.ctx, epicID, true)

	// The epic's terminal status is done and exactly one new prompt went out.
	iss, err := e.q.GetIssue(e.ctx, epicID)
	require.NoError(t, err)
	require.Equal(t, "done", iss.ExecStatus)
	awaitDeliveries(t, proxy, before+1)
	deliveries := proxy.snapshot()
	require.Len(t, deliveries, before+1, "done must deliver exactly the integration prompt")

	// Delivered to the SAME control workspace that ran the orchestration.
	last := deliveries[len(deliveries)-1]
	require.Equal(t, out.WorkspaceID, last.wsID, "the prompt must target the epic's control workspace")

	// Prompt content: the three-step wind-down (merge main in place → verify →
	// push only after user confirmation), naming the epic/<id> branch.
	content := last.content
	require.Contains(t, content, "集成收尾")
	require.Contains(t, content, epicBranchName(epicID), "the prompt must name epic/<id> as the current branch")
	require.Contains(t, content, "git merge main")
	require.Contains(t, content, "就地解决", "conflicts are resolved in place on the epic branch")
	require.Contains(t, content, "验证构建/测试")
	require.Contains(t, content, "推送 origin")
	require.Contains(t, content, "未经用户确认不得推送")
	require.NotContains(t, content, "服务端合并", "no server-side merge wording may remain")
}

// TestEpicTerminate_DeliversWhenStatusPreWrittenDone pins production parity:
// the floor-gate pass (floor_gate.go onFloorGateDone) and the finalize path
// (workspace_ops.go MarkWorkspaceDone) write exec_status='done' BEFORE the
// completion event reaches terminateEpic — so the FIRST legit delivery already
// observes a done row. The prompt must still go out; exec_status alone can
// never serve as the dedup signal.
func TestEpicTerminate_DeliversWhenStatusPreWrittenDone(t *testing.T) {
	e, proxy := integrationEnv(t)
	epicID := e.makeEpic(t, "预写 done epic")
	_, err := e.epicSvc.StartWorkspaceForIssue(e.ctx, epicID)
	require.NoError(t, err)
	awaitDeliveries(t, proxy, 1) // orchestration kickoff has landed
	before := len(proxy.snapshot())

	// Simulate the finalize/floor-gate pre-write that production performs
	// before publishing the completion event.
	require.NoError(t, e.q.SetIssueExecStatus(e.ctx, store.SetIssueExecStatusParams{ExecStatus: "done", ID: epicID}))

	e.epicSvc.OnWorkspaceCompleted(e.ctx, epicID, true)

	awaitDeliveries(t, proxy, before+1)
	deliveries := proxy.snapshot()
	require.Len(t, deliveries, before+1, "the pre-written done must not suppress the first integration prompt")
	require.Contains(t, deliveries[before].content, "git merge main")
}

// TestEpicTerminate_DonePromptDeliveredOnce pins the idempotency guard: the
// completion event can fire twice for the same epic (double finalize, replayed
// workspace done) — only the FIRST done delivers the integration prompt; a
// repeated done keeps the agent's inbox at exactly one wind-down message.
func TestEpicTerminate_DonePromptDeliveredOnce(t *testing.T) {
	e, proxy := integrationEnv(t)
	epicID := e.makeEpic(t, "幂等 epic")
	_, err := e.epicSvc.StartWorkspaceForIssue(e.ctx, epicID)
	require.NoError(t, err)
	awaitDeliveries(t, proxy, 1) // orchestration kickoff has landed
	before := len(proxy.snapshot())

	e.epicSvc.OnWorkspaceCompleted(e.ctx, epicID, true)
	awaitDeliveries(t, proxy, before+1)
	require.Len(t, proxy.snapshot(), before+1, "the first done delivers exactly the integration prompt")

	// Repeat done (duplicated completion event) — no second prompt.
	e.epicSvc.OnWorkspaceCompleted(e.ctx, epicID, true)
	require.Len(t, proxy.snapshot(), before+1, "a repeated done must not re-deliver the integration prompt")

	iss, err := e.q.GetIssue(e.ctx, epicID)
	require.NoError(t, err)
	require.Equal(t, "done", iss.ExecStatus)
}

// TestEpicTerminate_FailedSendsNoPrompt pins the gate half: a failed completion
// writes exec_status "failed" and sends NO integration prompt — the wind-down is
// a done-only bonus, never a failure follow-up.
func TestEpicTerminate_FailedSendsNoPrompt(t *testing.T) {
	e, proxy := integrationEnv(t)
	epicID := e.makeEpic(t, "失败 epic")
	_, err := e.epicSvc.StartWorkspaceForIssue(e.ctx, epicID)
	require.NoError(t, err)
	awaitDeliveries(t, proxy, 1) // orchestration kickoff has landed
	before := len(proxy.snapshot())

	e.epicSvc.OnWorkspaceCompleted(e.ctx, epicID, false)

	iss, err := e.q.GetIssue(e.ctx, epicID)
	require.NoError(t, err)
	require.Equal(t, "failed", iss.ExecStatus)
	require.Len(t, proxy.snapshot(), before, "a failed epic must not receive the integration prompt")
}

// TestEpicTerminate_NoActiveWorkspaceSkipsPrompt covers the headless epic: done
// without an active control workspace persists the status and silently skips the
// best-effort prompt (no panic, no delivery).
func TestEpicTerminate_NoActiveWorkspaceSkipsPrompt(t *testing.T) {
	e, proxy := integrationEnv(t)
	epicID := e.makeEpic(t, "无工作空间 epic")

	require.NotPanics(t, func() { e.epicSvc.OnWorkspaceCompleted(e.ctx, epicID, true) })

	iss, err := e.q.GetIssue(e.ctx, epicID)
	require.NoError(t, err)
	require.Equal(t, "done", iss.ExecStatus)
	require.Empty(t, proxy.snapshot(), "no workspace means no prompt")
}
