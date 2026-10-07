package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

func TestUnsetBudgetRunsPastOldDefault(t *testing.T) {
	const rounds = 18 // old DefaultMaxTurns was 16
	var script []*model.Response
	for i := 0; i < rounds; i++ {
		script = append(script, toolUseResp(fmt.Sprintf("tu_%d", i), "Echo", json.RawMessage(`{}`)))
	}
	script = append(script, textResp("finally done"))
	fm := &fakeModel{script: script}
	reg := tools.NewRegistry(&stubTool{})

	res, err := Run(context.Background(), fm, reg, "", "go", Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Text != "finally done" {
		t.Errorf("answer = %q", res.Text)
	}
	if res.Rounds != rounds+1 {
		t.Errorf("rounds = %d, want %d", res.Rounds, rounds+1)
	}
}

func TestEnvMaxTurnsCapsUnsetOption(t *testing.T) {
	t.Setenv("NIUNIU_AGENT_MAX_TURNS", "2")
	fm := &fakeModel{script: []*model.Response{
		toolUseResp("tu_1", "Echo", nil),
		toolUseResp("tu_2", "Echo", nil),
	}}
	reg := tools.NewRegistry(&stubTool{})

	_, err := Run(context.Background(), fm, reg, "", "go", Options{})
	if err == nil || !strings.Contains(err.Error(), "turn budget exhausted after 2 rounds") {
		t.Fatalf("err = %v, want budget exhaustion at 2 rounds", err)
	}
}
