package checkers_test

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/harness"
	"github.com/niuniu-dev/niuniu/internal/harness/checkers"
	"github.com/stretchr/testify/assert"
)

func TestCommitLint_Name(t *testing.T) {
	c := checkers.NewCommitLint()
	assert.Equal(t, "commit-lint", c.Name())
}

func TestCommitLint_EmptyMessage_Skip(t *testing.T) {
	c := checkers.NewCommitLint()
	result := c.Check(context.Background(), harness.CheckOpts{
		CommitMessage: "",
	})
	assert.Equal(t, "skip", result.Status)
}

func TestCommitLint_ValidMessages_Pass(t *testing.T) {
	c := checkers.NewCommitLint()
	validMessages := []string{
		"feat: add new feature",
		"fix: correct a bug",
		"refactor: improve code structure",
		"docs: update README",
		"test: add unit tests",
		"chore: update dependencies",
		"perf: improve performance",
		"ci: update pipeline",
		"feat(scope): add scoped feature",
		"fix(auth): fix login bug",
	}
	for _, msg := range validMessages {
		t.Run(msg, func(t *testing.T) {
			result := c.Check(context.Background(), harness.CheckOpts{
				CommitMessage: msg,
			})
			assert.Equal(t, "pass", result.Status, "expected pass for: %s", msg)
		})
	}
}

func TestCommitLint_InvalidMessages_Fail(t *testing.T) {
	c := checkers.NewCommitLint()
	invalidMessages := []string{
		"Add stuff",
		"WIP",
		"fixed things",
		"update: not a valid type",
		"feat : space before colon",
	}
	for _, msg := range invalidMessages {
		t.Run(msg, func(t *testing.T) {
			result := c.Check(context.Background(), harness.CheckOpts{
				CommitMessage: msg,
			})
			assert.Equal(t, "fail", result.Status, "expected fail for: %s", msg)
			assert.Contains(t, result.Details, msg)
		})
	}
}

func TestCommitLint_CustomPattern_Pass(t *testing.T) {
	c := checkers.NewCommitLint()
	result := c.Check(context.Background(), harness.CheckOpts{
		CommitMessage: "TICKET-123: do something",
		SpecConfig:    `{"pattern": "^TICKET-\\d+: .+"}`,
	})
	assert.Equal(t, "pass", result.Status)
}

func TestCommitLint_CustomPattern_Fail(t *testing.T) {
	c := checkers.NewCommitLint()
	result := c.Check(context.Background(), harness.CheckOpts{
		CommitMessage: "feat: normal commit",
		SpecConfig:    `{"pattern": "^TICKET-\\d+: .+"}`,
	})
	assert.Equal(t, "fail", result.Status)
}

func TestCommitLint_InvalidRegex_Error(t *testing.T) {
	c := checkers.NewCommitLint()
	result := c.Check(context.Background(), harness.CheckOpts{
		CommitMessage: "feat: something",
		SpecConfig:    `{"pattern": "[invalid(regex"}`,
	})
	assert.Equal(t, "error", result.Status)
}
