package rsi

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"fmt"

	"github.com/niuniu-dev/niuniu/agent/internal/memory"
	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// threeRoleModel 按调用序回放脚本：
// call1 = broad curriculum，call2 = actor A（成功写文件），call3 = distill A，
// call4 = actor B（不执行 → file-exists 失败），call5 = deep curriculum，
// call6 = actor C（成功），call7 = distill C。
type threeRoleModel struct {
	calls int
	reqs  []model.Request
}

const broadCurriculum = `---
name: rsi-a
description: write out
---
## Task
write out.txt = good
## Fixtures
- in.txt: seed
## Checks
- file-exists: out.txt
====
---
name: rsi-b
description: will fail
---
## Task
do the impossible thing
## Fixtures
- in.txt: seed
## Checks
- file-exists: must-not-exist.txt`

const deepCurriculum = `---
name: rsi-c
description: write ok
---
## Task
write ok.txt = fine
## Fixtures
- in.txt: seed
## Checks
- file-exists: ok.txt`

func (m *threeRoleModel) Complete(_ context.Context, req model.Request) (*model.Response, error) {
	idx := m.calls
	m.calls++
	m.reqs = append(m.reqs, req)
	text := ""
	switch idx {
	case 0, 4: // curriculum（broad / deep）
		text = broadCurriculum
		if idx == 4 {
			text = deepCurriculum
		}
	case 1, 5: // actor：按 prompt 子命令写文件
		for _, blk := range req.Messages[0].Blocks {
			if blk.Type != model.BlockText {
				continue
			}
			for _, line := range strings.Split(blk.Text, "\n") {
				if rest, ok := strings.CutPrefix(line, "write "); ok {
					if file, content, ok2 := strings.Cut(rest, " = "); ok2 {
						_ = os.WriteFile(file, []byte(content), 0o644)
					}
				}
			}
		}
		text = "done"
	case 2, 6: // distill（title 随调用序不同，避免同题去重）
		text = fmt.Sprintf("TITLE: test-lesson-%d\nTYPE: pattern\n---\nA verified lesson %d.", idx, idx)
	default: // actor B：不写 must-not-exist
		text = "I cannot do that."
	}
	return &model.Response{Message: model.Message{Role: model.RoleAssistant, Blocks: []model.Block{
		{Type: model.BlockText, Text: text}}}}, nil
}

func newRunner(t *testing.T, m model.Model) (*Runner, *memory.Store) {
	t.Helper()
	dir := t.TempDir()
	store := memory.NewStore(dir)
	reg := tools.NewRegistry(&probe{}, &memSaveProbe{})
	return &Runner{
		Model: m,
		Reg:   reg,
		Store: store,
		Cwd:   dir,
		Opts:  Options{Broad: 2, Deep: 1, MemoryFreeze: true, PerTaskTimeout: time.Minute},
	}, store
}

type probe struct{}

func (probe) Def() model.ToolDef {
	return model.ToolDef{Name: "LS", Description: "list", InputSchema: jsonSchemaStub()}
}
func (probe) Execute(_ context.Context, _ json.RawMessage) (string, error) { return "", nil }

type memSaveProbe struct{}

func (memSaveProbe) Def() model.ToolDef {
	return model.ToolDef{Name: "MemorySave", Description: "save", InputSchema: jsonSchemaStub()}
}
func (memSaveProbe) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "saved", nil
}

func jsonSchemaStub() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }

func TestThreeRolesFlow(t *testing.T) {
	m := &threeRoleModel{}
	r, store := newRunner(t, m)

	report, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Broad) != 2 || len(report.Deep) != 1 {
		t.Fatalf("broad=%d deep=%d", len(report.Broad), len(report.Deep))
	}
	if !report.Broad[0].Pass || report.Broad[1].Pass || !report.Deep[0].Pass {
		t.Fatalf("attempts = %+v / %+v / %+v", report.Broad[0], report.Broad[1], report.Deep[0])
	}

	// 失败尝试不沉淀；仅两个 verified pass 各沉淀一条经验。
	if len(storeEntries(store)) != 2 {
		t.Fatalf("memory entries = %+v, want 2 (only verified passes distill)", storeEntries(store))
	}
	for _, e := range storeEntries(store) {
		if !strings.HasPrefix(e.Title, "rsi-") || len(e.Tags) != 1 || e.Tags[0] != "rsi" {
			t.Errorf("lesson entry = %+v, want rsi- title + rsi tag", e)
		}
	}
	if len(report.Lessons) != 2 {
		t.Errorf("lessons = %v", report.Lessons)
	}
}

// 防火墙：Verifier 不调模型（调用次数 == curriculum+actor+distill 的确定
// 序列），且 Actor 注册表在冻结下不可见 MemorySave。
func TestVerifierFirewallAndMemoryFreeze(t *testing.T) {
	m := &threeRoleModel{}
	r, _ := newRunner(t, m)
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	// 固定序列：broad-curriculum, actor, distill, actor, deep-curriculum,
	// actor, distill = 7 次模型调用；Verifier（每任务一次规则判定）零调用。
	if m.calls != 7 {
		t.Errorf("model calls = %d, want 7 (verifier must not call the model)", m.calls)
	}
	// 冻结生效：两次 actor 请求的工具列表里没有 MemorySave。
	for i, req := range m.reqs {
		if i == 1 || i == 3 || i == 5 { // actor 调用
			for _, td := range req.Tools {
				if td.Name == "MemorySave" || td.Name == "MemoryConsolidate" {
					t.Errorf("actor round %d still sees memory tool %q", i, td.Name)
				}
			}
		}
	}
}

func storeEntries(s *memory.Store) []memory.Entry {
	// Search("") 返回全部条目。
	entries, err := s.Search("")
	if err != nil {
		return nil
	}
	return entries
}
