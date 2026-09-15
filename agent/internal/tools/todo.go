package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
)

// TodoWrite maintains the agent's task list, persisted to
// <cwd>/.niuniu-agent/todos.json so it survives across turns and sessions.
type TodoWrite struct{}

// todoItem is one list entry.
type todoItem struct {
	Content    string `json:"content"`
	Status     string `json:"status"` // pending | in_progress | completed
	ActiveForm string `json:"active_form,omitempty"`
}

// todoInput.
type todoInput struct {
	Todos []todoItem `json:"todos"`
}

func (TodoWrite) Def() model.ToolDef {
	return model.ToolDef{
		Name: "TodoWrite",
		Description: "Replaces the task list. Use for multi-step work: one item per step, mark items " +
			"in_progress before starting and completed right after finishing. Pass the FULL list every time.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"todos":{"type":"array","items":{"type":"object","properties":{` +
			`"content":{"type":"string","description":"Imperative step description"},` +
			`"status":{"type":"string","enum":["pending","in_progress","completed"]},` +
			`"active_form":{"type":"string","description":"Present-continuous form shown while in progress"}},` +
			`"required":["content","status"]}}},` +
			`"required":["todos"]}`),
	}
}

func (TodoWrite) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in todoInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	for _, t := range in.Todos {
		switch t.Status {
		case "pending", "in_progress", "completed":
		default:
			return "", fmt.Errorf("invalid status %q (want pending | in_progress | completed)", t.Status)
		}
	}

	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(wd, ".niuniu-agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(in.Todos, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "todos.json"), data, 0o644); err != nil {
		return "", err
	}
	return renderTodos(in.Todos), nil
}

func renderTodos(todos []todoItem) string {
	if len(todos) == 0 {
		return "(task list cleared)"
	}
	var b strings.Builder
	for _, t := range todos {
		switch t.Status {
		case "completed":
			b.WriteString("[x] ")
		case "in_progress":
			if t.ActiveForm != "" {
				b.WriteString("[~] " + t.ActiveForm)
				b.WriteString("\n")
				continue
			}
			b.WriteString("[~] ")
		default:
			b.WriteString("[ ] ")
		}
		b.WriteString(t.Content)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
