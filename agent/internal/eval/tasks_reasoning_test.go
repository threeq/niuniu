package eval

import (
	"testing"
)

// The private reasoning task set must stay parseable and every task must
// carry deterministic checks — a malformed task here silently shrinks the
// reasoning benchmark's coverage.
func TestReasoningTaskSetParses(t *testing.T) {
	tasks, err := LoadTasks("../../eval/tasks-reasoning")
	if err != nil {
		t.Fatalf("LoadTasks: %v", err)
	}
	if len(tasks) < 6 {
		t.Fatalf("reasoning set has %d tasks, want >= 6", len(tasks))
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
		for _, chk := range task.Checks {
			switch chk.Kind {
			case "contains", "not-contains", "file-exists", "command-exit-0", "output-contains":
			default:
				t.Errorf("task %s: unknown check kind %q", task.Name, chk.Kind)
			}
		}
	}
}
