package service

import (
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
)

func TestConvertEnvVarsToSliceFromStore(t *testing.T) {
	envVars := []struct {
		Key   string
		Value string
	}{
		{Key: "KEY1", Value: "value1"},
		{Key: "KEY2", Value: "value2"},
	}

	// Convert to store.WorkspaceEnv format
	storeEnvs := make([]store.WorkspaceEnv, len(envVars))
	for i, e := range envVars {
		storeEnvs[i] = store.WorkspaceEnv{Key: e.Key, Value: e.Value}
	}

	result := convertEnvVarsToSliceFromStore(storeEnvs)

	if len(result) != 2 {
		t.Errorf("expected 2 env vars, got %d", len(result))
	}

	found := map[string]bool{"KEY1=value1": false, "KEY2=value2": false}
	for _, item := range result {
		found[item] = true
	}

	for k, v := range found {
		if !v {
			t.Errorf("missing env var: %s", k)
		}
	}
}

func TestAgentManager_StartWithEnvVars(t *testing.T) {
	// This test verifies that AgentManager.Start passes env vars to PTY
	// Currently AgentManager doesn't have access to workspace env vars
	// After implementation, it should fetch and pass them
	t.Skip("Requires implementation - AgentManager.Start needs to fetch workspace env vars")
}

func TestAgentManager_StartFetchesWorkspaceEnv(t *testing.T) {
	// After implementation, AgentManager.Start should:
	// 1. Fetch workspace env vars from database
	// 2. Convert to []string format
	// 3. Pass to NewPTYProcess
	t.Skip("Requires implementation - workspace env vars not yet injected into PTY")
}
