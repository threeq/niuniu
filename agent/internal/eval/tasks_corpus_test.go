package eval

import (
	"testing"
)

// The public task corpus must stay parseable and every task must carry
// deterministic checks — a malformed task here silently shrinks eval
// coverage (this bit us once via CRLF line endings on Windows checkouts).
func TestRepoTaskCorpusParses(t *testing.T) {
	tasks, err := LoadTasks("../../eval/tasks")
	if err != nil {
		t.Fatalf("LoadTasks: %v", err)
	}
	if len(tasks) < 20 {
		t.Fatalf("corpus has %d tasks, want >= 20", len(tasks))
	}
	for _, task := range tasks {
		if task.Name == "" {
			t.Errorf("task with empty name (prompt head: %.40q)", task.Prompt)
		}
		if task.Prompt == "" {
			t.Errorf("task %s: empty prompt", task.Name)
		}
		if len(task.Checks) == 0 {
			t.Errorf("task %s: no checks", task.Name)
		}
	}
}
