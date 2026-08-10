package checkers_test

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/harness"
	"github.com/niuniu-dev/niuniu/internal/harness/checkers"
	"github.com/stretchr/testify/assert"
)

func TestBranchName_Name(t *testing.T) {
	c := checkers.NewBranchName()
	assert.Equal(t, "branch-name", c.Name())
}

func TestBranchName_EmptyBranch_Skip(t *testing.T) {
	c := checkers.NewBranchName()
	result := c.Check(context.Background(), harness.CheckOpts{
		BranchName: "",
	})
	assert.Equal(t, "skip", result.Status)
}

func TestBranchName_ProtectedBranches_Pass(t *testing.T) {
	c := checkers.NewBranchName()
	protectedBranches := []string{
		"main",
		"master",
		"develop",
		"dev",
		"staging",
		"release",
	}
	for _, branch := range protectedBranches {
		t.Run(branch, func(t *testing.T) {
			result := c.Check(context.Background(), harness.CheckOpts{
				BranchName: branch,
			})
			assert.Equal(t, "pass", result.Status, "expected pass for protected branch: %s", branch)
		})
	}
}

func TestBranchName_ValidNames_Pass(t *testing.T) {
	c := checkers.NewBranchName()
	validNames := []string{
		"feat/add-login",
		"fix/correct-bug",
		"refactor/improve-code",
		"chore/update-deps",
		"docs/update-readme",
		"test/add-unit-tests",
		"perf/improve-perf",
		"ci/update-pipeline",
		"ws-50/main",
		"ws-123/some-feature",
		"feat/my-feature-123",
	}
	for _, name := range validNames {
		t.Run(name, func(t *testing.T) {
			result := c.Check(context.Background(), harness.CheckOpts{
				BranchName: name,
			})
			assert.Equal(t, "pass", result.Status, "expected pass for: %s", name)
		})
	}
}

func TestBranchName_InvalidNames_Fail(t *testing.T) {
	c := checkers.NewBranchName()
	invalidNames := []string{
		"Add-Feature",
		"my feature",
		"FEAT/add-thing",
		"random-branch",
		"feat/",
		"feat/UPPERCASE",
	}
	for _, name := range invalidNames {
		t.Run(name, func(t *testing.T) {
			result := c.Check(context.Background(), harness.CheckOpts{
				BranchName: name,
			})
			assert.Equal(t, "fail", result.Status, "expected fail for: %s", name)
		})
	}
}

func TestBranchName_CustomPattern_Pass(t *testing.T) {
	c := checkers.NewBranchName()
	result := c.Check(context.Background(), harness.CheckOpts{
		BranchName: "JIRA-123-do-something",
		SpecConfig: `{"pattern": "^JIRA-\\d+-[a-z0-9-]+$"}`,
	})
	assert.Equal(t, "pass", result.Status)
}

func TestBranchName_CustomPattern_Fail(t *testing.T) {
	c := checkers.NewBranchName()
	result := c.Check(context.Background(), harness.CheckOpts{
		BranchName: "feat/my-branch",
		SpecConfig: `{"pattern": "^JIRA-\\d+-[a-z0-9-]+$"}`,
	})
	assert.Equal(t, "fail", result.Status)
}
