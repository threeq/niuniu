package agentproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// newTodoWriteTestSession builds a minimal WorkspaceSession backed by an
// in-memory SQLite store, just enough to exercise handleTodoWrite end-to-end
// (parse → DB upsert → broadcast). Hub broadcast is best-effort; we don't
// subscribe.
func newTodoWriteTestSession(t *testing.T) *WorkspaceSession {
	t.Helper()
	q := setupDispatchDB(t)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name:      "todowrite-test-ws",
		Path:      t.TempDir(),
		Status:    "created",
		OwnerType: "user",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	hub := NewSessionHub()
	t.Cleanup(hub.Stop)
	return &WorkspaceSession{
		workspaceID:   ws.ID,
		q:             q,
		hub:           hub,
		taskBatchId:   "batch-test",
		toolUseNames:  map[string]string{},
		toolUseIds:    map[int]string{},
		toolInputBufs: map[int]string{},
		textBlockBufs: map[int]string{},
	}
}

// listVisibleTaskSubjects returns subjects of all non-deleted tasks for the
// session's batch, in created_at order. Used to assert the visible state of
// the task list as the UI would see it.
func listVisibleTaskSubjects(t *testing.T, s *WorkspaceSession) []string {
	t.Helper()
	rows, err := s.q.ListWorkspaceTasksByBatch(context.Background(), store.ListWorkspaceTasksByBatchParams{
		WorkspaceID: s.workspaceID,
		BatchID:     s.taskBatchId,
	})
	if err != nil {
		t.Fatalf("ListWorkspaceTasksByBatch: %v", err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Subject)
	}
	return out
}

// todosJSON builds a TodoWrite array-format input. Each pair is
// (content, status).
func todosJSON(pairs ...[2]string) string {
	type todo struct {
		Content    string `json:"content"`
		Status     string `json:"status"`
		ActiveForm string `json:"activeForm"`
	}
	items := make([]todo, len(pairs))
	for i, p := range pairs {
		items[i] = todo{Content: p[0], Status: p[1], ActiveForm: p[0] + "ing"}
	}
	body, _ := json.Marshal(struct {
		Todos []todo `json:"todos"`
	}{items})
	return string(body)
}

// TestTodoWrite_StableContentIdentity verifies that calling TodoWrite N times
// with the SAME content produces exactly N tasks in DB (no duplicates).
//
// Bug being prevented: the old positional logic would `INSERT` a fresh row for
// any position whose old agent_task_id was generated from a prior toolUseId.
// With stable content-derived ids, identical content collapses via UPSERT.
func TestTodoWrite_StableContentIdentity(t *testing.T) {
	s := newTodoWriteTestSession(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		input := todosJSON([2]string{"全量回归 + 部署前 vet + go test", "pending"})
		s.handleTodoWrite(ctx, fmt.Sprintf("toolu_%d", i), input, fmt.Sprintf("msg%d", i))
	}

	subjects := listVisibleTaskSubjects(t, s)
	if len(subjects) != 1 {
		t.Errorf("after 5 identical TodoWrite calls, expected 1 task, got %d: %v", len(subjects), subjects)
	}
}

// TestTodoWrite_ShrinkGrowDoesNotCreateOrphans reproduces the screenshot bug:
// the todo list expands, contracts, then expands again with the SAME later
// content. The old code created fresh agent_task_ids each time the list
// re-expanded, leaving the previously-created tasks as visible orphans.
func TestTodoWrite_ShrinkGrowDoesNotCreateOrphans(t *testing.T) {
	s := newTodoWriteTestSession(t)
	ctx := context.Background()

	// Simulate the cycle 5 times: [A,B] → [A] → [A,B] → [A] → [A,B] ...
	for i := 0; i < 5; i++ {
		s.handleTodoWrite(ctx, fmt.Sprintf("toolu_grow_%d", i),
			todosJSON([2]string{"删除孤儿弹框", "pending"}, [2]string{"全量回归", "pending"}),
			"msg")
		s.handleTodoWrite(ctx, fmt.Sprintf("toolu_shrink_%d", i),
			todosJSON([2]string{"删除孤儿弹框", "pending"}),
			"msg")
	}

	// End state after the loop: last call was a shrink, so visible state should
	// be exactly ["删除孤儿弹框"]. The "全量回归" task should be marked deleted.
	subjects := listVisibleTaskSubjects(t, s)
	if len(subjects) != 1 || subjects[0] != "删除孤儿弹框" {
		t.Errorf("after shrink-grow cycles, expected exactly [\"删除孤儿弹框\"], got %v", subjects)
	}
}

// TestTodoWrite_RemovedItemsMarkedDeleted verifies that when a todo
// disappears from a TodoWrite array, its task is marked deleted (so the UI
// stops showing it) rather than silently lingering.
func TestTodoWrite_RemovedItemsMarkedDeleted(t *testing.T) {
	s := newTodoWriteTestSession(t)
	ctx := context.Background()

	s.handleTodoWrite(ctx, "toolu_1",
		todosJSON([2]string{"A", "pending"}, [2]string{"B", "pending"}, [2]string{"C", "pending"}),
		"msg1")
	if got := listVisibleTaskSubjects(t, s); len(got) != 3 {
		t.Fatalf("after first call expected 3 tasks, got %v", got)
	}

	// Drop B from the middle.
	s.handleTodoWrite(ctx, "toolu_2",
		todosJSON([2]string{"A", "pending"}, [2]string{"C", "pending"}),
		"msg2")

	got := listVisibleTaskSubjects(t, s)
	if len(got) != 2 {
		t.Fatalf("after dropping B, expected 2 visible tasks, got %v", got)
	}
	for _, subj := range got {
		if subj == "B" {
			t.Errorf("task B should be marked deleted but is still visible: %v", got)
		}
	}
}

// TestTodoWrite_RenameMarksOldHashDeleted verifies the rename invariant:
// when a todo's content text changes between calls, the old content's row
// (looked up by its content-hash id) is marked deleted and a new row is
// created for the new content. This is mechanically the same as
// "remove + add" since identity flows from content, but it's worth a
// dedicated assertion so a future identity-scheme change can't silently
// break the rename behavior.
func TestTodoWrite_RenameMarksOldHashDeleted(t *testing.T) {
	s := newTodoWriteTestSession(t)
	ctx := context.Background()

	s.handleTodoWrite(ctx, "toolu_1", todosJSON([2]string{"A", "pending"}), "msg1")
	s.handleTodoWrite(ctx, "toolu_2", todosJSON([2]string{"A revised", "pending"}), "msg2")

	got := listVisibleTaskSubjects(t, s)
	if len(got) != 1 || got[0] != "A revised" {
		t.Errorf("after rename, expected exactly [\"A revised\"], got %v", got)
	}
}

// TestTodoWrite_ExplicitIDRoundTrip verifies the explicit-todo.id branch:
// when Claude provides a stable id, two calls with the same id but different
// content update the same row (rather than creating a new one). Also
// verifies taskIdMap gets populated so a subsequent TaskUpdate against that
// claude-side id can find the agent_task_id.
func TestTodoWrite_ExplicitIDRoundTrip(t *testing.T) {
	s := newTodoWriteTestSession(t)
	ctx := context.Background()

	withID := `{"todos":[{"id":"claude-side-7","content":"first content","status":"pending","activeForm":"doing first"}]}`
	s.handleTodoWrite(ctx, "toolu_1", withID, "msg1")

	if mapped, ok := s.taskIdMap["claude-side-7"]; !ok || mapped != "todo-id-claude-side-7" {
		t.Errorf("taskIdMap[claude-side-7]=%q, want todo-id-claude-side-7 (ok=%v)", mapped, ok)
	}

	// Same explicit id, new content — must update the existing row, not create a new one.
	withIDRenamed := `{"todos":[{"id":"claude-side-7","content":"second content","status":"in_progress","activeForm":"doing second"}]}`
	s.handleTodoWrite(ctx, "toolu_2", withIDRenamed, "msg2")

	got := listVisibleTaskSubjects(t, s)
	if len(got) != 1 || got[0] != "second content" {
		t.Errorf("after explicit-id rename, expected [\"second content\"], got %v", got)
	}
}

// TestTodoWrite_ReorderDoesNotMisrenameTasks verifies that reordering a todo
// array does NOT cause subjects to be overwritten on the wrong rows. The old
// positional matcher would update tA with B's content when the array was
// reordered.
func TestTodoWrite_ReorderDoesNotMisrenameTasks(t *testing.T) {
	s := newTodoWriteTestSession(t)
	ctx := context.Background()

	s.handleTodoWrite(ctx, "toolu_1",
		todosJSON([2]string{"A", "pending"}, [2]string{"B", "pending"}, [2]string{"C", "pending"}),
		"msg1")

	// Reverse the order.
	s.handleTodoWrite(ctx, "toolu_2",
		todosJSON([2]string{"C", "pending"}, [2]string{"B", "pending"}, [2]string{"A", "pending"}),
		"msg2")

	rows, err := s.q.ListWorkspaceTasksByBatch(context.Background(), store.ListWorkspaceTasksByBatchParams{
		WorkspaceID: s.workspaceID,
		BatchID:     s.taskBatchId,
	})
	if err != nil {
		t.Fatalf("ListWorkspaceTasksByBatch: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 visible tasks after reorder, got %d", len(rows))
	}
	// Each subject must appear exactly once — no duplicates, no missing items.
	count := map[string]int{}
	for _, r := range rows {
		count[r.Subject]++
	}
	for _, want := range []string{"A", "B", "C"} {
		if count[want] != 1 {
			t.Errorf("subject %q count=%d, want 1; full counts: %v", want, count[want], count)
		}
	}
}

// TestTodoWrite_StatusUpdatesPersist verifies that flipping a todo's status
// across calls (pending → in_progress → completed) updates the same row
// rather than creating new ones.
func TestTodoWrite_StatusUpdatesPersist(t *testing.T) {
	s := newTodoWriteTestSession(t)
	ctx := context.Background()

	s.handleTodoWrite(ctx, "toolu_1", todosJSON([2]string{"X", "pending"}), "msg1")
	s.handleTodoWrite(ctx, "toolu_2", todosJSON([2]string{"X", "in_progress"}), "msg2")
	s.handleTodoWrite(ctx, "toolu_3", todosJSON([2]string{"X", "completed"}), "msg3")

	rows, err := s.q.ListWorkspaceTasksByBatch(context.Background(), store.ListWorkspaceTasksByBatchParams{
		WorkspaceID: s.workspaceID,
		BatchID:     s.taskBatchId,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row after status transitions, got %d (subjects=%v)", len(rows), func() []string {
			out := []string{}
			for _, r := range rows {
				out = append(out, r.Subject)
			}
			return out
		}())
	}
	if rows[0].Status != "completed" {
		t.Errorf("status=%q, want completed", rows[0].Status)
	}
}

// TestTodoWrite_DuplicateContentInSameArray verifies the edge case where
// Claude emits two todos with identical content in the SAME array. They
// should produce two distinct rows (not collapse to one).
func TestTodoWrite_DuplicateContentInSameArray(t *testing.T) {
	s := newTodoWriteTestSession(t)
	ctx := context.Background()

	s.handleTodoWrite(ctx, "toolu_1",
		todosJSON([2]string{"dup", "pending"}, [2]string{"dup", "in_progress"}),
		"msg1")

	rows, err := s.q.ListWorkspaceTasksByBatch(context.Background(), store.ListWorkspaceTasksByBatchParams{
		WorkspaceID: s.workspaceID,
		BatchID:     s.taskBatchId,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("expected 2 rows for duplicate-content array, got %d", len(rows))
	}
}

// TestTodoWrite_SingleSubjectFormatStillWorks verifies the legacy single-task
// format is delegated to handleTaskCreate and still creates a task.
func TestTodoWrite_SingleSubjectFormatStillWorks(t *testing.T) {
	s := newTodoWriteTestSession(t)
	ctx := context.Background()

	input := `{"subject":"single task","description":"d","activeForm":"doing it"}`
	s.handleTodoWrite(ctx, "toolu_single", input, "msg1")

	got := listVisibleTaskSubjects(t, s)
	if len(got) != 1 || got[0] != "single task" {
		t.Errorf("single-subject format: got %v, want [\"single task\"]", got)
	}
}
