package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/perm"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// Regression: the registered HistorySearch tool must read the exact dir
// compact archives to (tools.HistoryDir — the private state dir under
// ~/.niuniu-agent/projects/<escaped-cwd>/history). It used to point at
// <cwd>/.niuniu-agent/history while the archive went to the state dir, so
// archived history was unfindable on any machine with a normal HOME.
func TestHistorySearchWiredToArchiveDir(t *testing.T) {
	cwd := t.TempDir()
	reg, closer := sessionRegistry(cwd, nil, perm.NewPolicy(false), nil)
	defer closer.Close()

	msgs := []model.Message{{
		Role: model.RoleUser,
		Blocks: []model.Block{{
			Type: model.BlockText,
			Text: "Investigated E42 disk full on the build agent; fixed by pruning the docker cache.",
		}},
	}}
	if err := tools.SaveHistoryArchive(tools.HistoryDir(cwd), msgs); err != nil {
		t.Fatalf("archive: %v", err)
	}

	tool, ok := reg.Lookup("HistorySearch")
	if !ok {
		t.Fatal("HistorySearch not registered")
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"E42 disk full","top":5}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "E42 disk full") {
		t.Fatalf("archived history not retrievable via the registered tool, out = %q", out)
	}
}
