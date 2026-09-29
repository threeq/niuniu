package service_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	testutil "github.com/niuniu-dev/niuniu/internal/testing"
	"github.com/stretchr/testify/require"
)

// fakeVideoChangeDispatcher records the request-changes calls the dispatch
// action makes (the same service path EpicExecutionHandler.RequestChanges
// calls) so tests never touch the real epic engine.
type fakeVideoChangeDispatcher struct {
	calls  []service.RequestChangesInput
	result service.RequestChangesResult
	err    error
}

func (f *fakeVideoChangeDispatcher) RequestChanges(_ context.Context, in service.RequestChangesInput) (service.RequestChangesResult, error) {
	f.calls = append(f.calls, in)
	if f.err != nil {
		return service.RequestChangesResult{}, f.err
	}
	res := f.result
	if res.IssueID == 0 {
		res.IssueID = in.IssueID
	}
	return res, nil
}

// videoProjectEnv is a temp-dir workspace backed by an in-memory DB: project /
// column / root issue / (optionally) the four discipline child issues + the
// workspace row. Paths resolve through OwnerRef exactly like production.
type videoProjectEnv struct {
	t        *testing.T
	ctx      context.Context
	q        *store.Queries
	svc      *service.VideoProjectService
	dispat   *fakeVideoChangeDispatcher
	dataDir  string
	columnID int64
	wsID     int64
	wsDir    string
	issueID  map[string]int64 // "root", "screenwriter", "characters", "scenes", "storyboard"
}

func setupVideoProjectEnv(t *testing.T, withIssueTree bool) *videoProjectEnv {
	t.Helper()
	prev := store.Driver
	store.Driver = "sqlite"
	t.Cleanup(func() { store.Driver = prev })
	db := testutil.SetupTestDB(t)
	store.Migrate(db)
	q := store.New(db)
	ctx := context.Background()

	proj, err := q.CreateProject(ctx, store.CreateProjectParams{Name: "video-proj", OwnerType: "user", OwnerID: 1})
	require.NoError(t, err)
	col, err := q.CreateColumn(ctx, store.CreateColumnParams{ProjectID: proj.ID, Name: "Backlog", Position: 0})
	require.NoError(t, err)
	root, err := q.CreateIssue(ctx, store.CreateIssueParams{ColumnID: col.ID, Title: "视频创作（主 issue）"})
	require.NoError(t, err)

	env := &videoProjectEnv{t: t, ctx: ctx, q: q, columnID: col.ID, issueID: map[string]int64{"root": root.ID}}
	if withIssueTree {
		for _, c := range []struct{ key, title string }{
			{"screenwriter", "wave1 编剧"},
			{"characters", "wave2 角色设计"},
			{"scenes", "wave2 场景设定"},
			{"storyboard", "wave3 分镜"},
		} {
			child, cerr := q.CreateIssue(ctx, store.CreateIssueParams{
				ColumnID:      col.ID,
				Title:         c.title,
				ParentIssueID: sql.NullInt64{Int64: root.ID, Valid: true},
			})
			require.NoError(t, cerr)
			env.issueID[c.key] = child.ID
		}
	}

	ws, err := q.CreateWorkspace(ctx, store.CreateWorkspaceParams{
		IssueID:   sql.NullInt64{Int64: root.ID, Valid: true},
		Name:      "video-ws",
		Path:      "/tmp/ws-video",
		Status:    "active",
		OwnerType: "user",
		OwnerID:   1,
	})
	require.NoError(t, err)
	env.wsID = ws.ID
	env.dataDir = t.TempDir()
	owner := service.OwnerRef{Type: "user", ID: 1}
	env.wsDir = owner.WorkspacePath(env.dataDir, ws.ID)
	require.NoError(t, os.MkdirAll(env.wsDir, 0o755))

	env.svc = service.NewVideoProjectService(q, env.dataDir)
	env.dispat = &fakeVideoChangeDispatcher{}
	env.svc.SetChangeDispatcher(env.dispat)
	return env
}

func (e *videoProjectEnv) projectDir() string { return filepath.Join(e.wsDir, "video-project") }

// write creates parent dirs and writes a file relative to the workspace dir.
func (e *videoProjectEnv) write(rel, body string) string {
	e.t.Helper()
	abs := filepath.Join(e.wsDir, filepath.FromSlash(rel))
	require.NoError(e.t, os.MkdirAll(filepath.Dir(abs), 0o755))
	require.NoError(e.t, os.WriteFile(abs, []byte(body), 0o644))
	return abs
}

func (e *videoProjectEnv) read(rel string) string {
	e.t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.wsDir, filepath.FromSlash(rel)))
	require.NoError(e.t, err)
	return string(raw)
}

func videoProjectProductsByKey(t *testing.T, agg service.VideoProjectAggregate) map[string]service.VideoProduct {
	t.Helper()
	out := make(map[string]service.VideoProduct, len(agg.Products))
	for _, p := range agg.Products {
		out[p.Key] = p
	}
	return out
}

// findVideoTmpFiles returns any atomic-write temp residue under root.
func findVideoTmpFiles(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), ".tmp-") {
			found = append(found, path)
		}
		return nil
	})
	require.NoError(t, err)
	return found
}

// TestVideoProjectAggregateRead covers the aggregate read: six whitelisted
// products, the corrupt-JSON error path (per-entry, not fatal), changes, the
// outputs list (both sources, newest first) and the shard counts.
func TestVideoProjectAggregateRead(t *testing.T) {
	env := setupVideoProjectEnv(t, true)
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	env.write("video-project/directorial-brief.md", "导演阐述：一支 30 秒的咖啡广告。")
	env.write("video-project/storyline.json", `{"revision": 5, "review_status": "in-review", "beats": ["开场", "冲突"]}`)
	env.write("video-project/script.json", `{"revision": 2, `) // 损坏的 JSON
	env.write("video-project/changes/chg-20260927-001.json",
		`{"id":"chg-20260927-001","target":"storyboard.json#shots[2]","kind":"annotation","content":"第二个镜头再慢一点","status":"pending","dispatched_to_issue":0,"created_at":"2026-09-27T10:00:00Z"}`)
	env.write("video-project/changes/chg-20260927-002.json",
		`{"id":"chg-20260927-002","target":"characters.json#c-hero","kind":"edit","content":"外套改成风衣","status":"dispatched","dispatched_to_issue":42,"created_at":"2026-09-27T11:00:00Z"}`)
	env.write("video-project/quotes/q1.json", "{}")
	env.write("video-project/quotes/q2.json", "{}")
	env.write("video-project/qc/q1.json", "{}")
	env.write("video-project/output/final.mp4", "final")
	env.write("video-project/output/draft.mp4", "draft")
	env.write("video-project/shots/01-a.mp4", "shot-a")
	// Deterministic mtimes: shots/01-a.mp4 newest → draft.mp4 → final.mp4.
	proj := env.projectDir()
	require.NoError(t, os.Chtimes(filepath.Join(proj, "output", "final.mp4"), base.Add(time.Minute), base.Add(time.Minute)))
	require.NoError(t, os.Chtimes(filepath.Join(proj, "output", "draft.mp4"), base.Add(2*time.Minute), base.Add(2*time.Minute)))
	require.NoError(t, os.Chtimes(filepath.Join(proj, "shots", "01-a.mp4"), base.Add(3*time.Minute), base.Add(3*time.Minute)))

	agg, err := env.svc.GetAggregate(env.ctx, env.wsID)
	require.NoError(t, err)
	require.True(t, agg.Exists)
	require.Len(t, agg.Products, 6)
	byKey := videoProjectProductsByKey(t, agg)

	brief := byKey["brief"]
	require.True(t, brief.Present)
	require.Equal(t, "markdown", brief.Kind)
	require.Equal(t, "video-project/directorial-brief.md", brief.File)
	require.Zero(t, brief.Revision)
	require.Empty(t, brief.ReviewStatus)
	require.Empty(t, brief.Error)
	var briefText string
	require.NoError(t, json.Unmarshal(brief.Data, &briefText))
	require.Equal(t, "导演阐述：一支 30 秒的咖啡广告。", briefText)

	storyline := byKey["storyline"]
	require.True(t, storyline.Present)
	require.EqualValues(t, 5, storyline.Revision)
	require.Equal(t, "in-review", storyline.ReviewStatus)
	require.Empty(t, storyline.Error)
	require.NotEmpty(t, storyline.Data)

	// Corrupt JSON: per-entry Chinese error, no data, the rest of the read is fine.
	script := byKey["script"]
	require.True(t, script.Present)
	require.Contains(t, script.Error, "JSON 解析失败")
	require.Empty(t, script.Data)

	require.False(t, byKey["characters"].Present)
	require.False(t, byKey["scenes"].Present)
	require.False(t, byKey["storyboard"].Present)

	require.Len(t, agg.Changes, 2, "newest first")
	require.Equal(t, "chg-20260927-002", agg.Changes[0].ID)
	require.Equal(t, "dispatched", agg.Changes[0].Status)
	require.EqualValues(t, 42, agg.Changes[0].DispatchedToIssue)
	require.Equal(t, "edit", agg.Changes[0].Kind)
	require.Equal(t, "chg-20260927-001", agg.Changes[1].ID)
	require.Equal(t, "annotation", agg.Changes[1].Kind)
	require.Equal(t, "pending", agg.Changes[1].Status)

	require.Len(t, agg.Outputs, 3)
	require.Equal(t, "video-project/shots/01-a.mp4", agg.Outputs[0].Path)
	require.Equal(t, "video-project/output/draft.mp4", agg.Outputs[1].Path)
	require.Equal(t, "video-project/output/final.mp4", agg.Outputs[2].Path)
	require.EqualValues(t, int64(len("shot-a")), agg.Outputs[0].Size)
	_, terr := time.Parse(time.RFC3339, agg.Outputs[0].ModifiedAt)
	require.NoError(t, terr)

	require.Equal(t, 2, agg.Shards.Quotes)
	require.Equal(t, 1, agg.Shards.QC)
}

// TestVideoProjectAggregateMissing: no video-project dir → exists=false with
// empty collections, never a 404.
func TestVideoProjectAggregateMissing(t *testing.T) {
	env := setupVideoProjectEnv(t, false)
	agg, err := env.svc.GetAggregate(env.ctx, env.wsID)
	require.NoError(t, err)
	require.False(t, agg.Exists)
	require.Empty(t, agg.Products)
	require.Empty(t, agg.Changes)
	require.Empty(t, agg.Outputs)
	require.Equal(t, 0, agg.Shards.Quotes)
	require.Equal(t, 0, agg.Shards.QC)
}

// TestVideoProjectSaveProduct covers the happy path (revision bump, client
// revision/updated_at ignored), the 409 concurrency guard, the 400 validation
// paths, markdown saves, and the atomic write (no tmp residue).
func TestVideoProjectSaveProduct(t *testing.T) {
	env := setupVideoProjectEnv(t, false)
	zero, one := int64(0), int64(1)
	var vpErr *service.VideoProjectError

	// Create with expected_revision=0; client-supplied revision/updated_at ignored.
	saved, err := env.svc.SaveProduct(env.ctx, env.wsID, "storyboard",
		`{"revision": 99, "updated_at": "2000-01-01T00:00:00Z", "review_status": "draft", "shots": [{"id": 1}]}`, &zero)
	require.NoError(t, err)
	require.True(t, saved.Present)
	require.EqualValues(t, 1, saved.Revision)
	require.Equal(t, "draft", saved.ReviewStatus)
	require.Empty(t, saved.Error)

	var onDisk struct {
		Revision     int64  `json:"revision"`
		UpdatedAt    string `json:"updated_at"`
		ReviewStatus string `json:"review_status"`
		Shots        []struct {
			ID int `json:"id"`
		} `json:"shots"`
	}
	require.NoError(t, json.Unmarshal([]byte(env.read("video-project/storyboard.json")), &onDisk))
	require.EqualValues(t, 1, onDisk.Revision)
	require.Equal(t, "draft", onDisk.ReviewStatus)
	require.Len(t, onDisk.Shots, 1)
	require.NotEmpty(t, onDisk.UpdatedAt)
	_, terr := time.Parse(time.RFC3339, onDisk.UpdatedAt)
	require.NoError(t, terr)

	// Bump again with the current revision.
	saved, err = env.svc.SaveProduct(env.ctx, env.wsID, "storyboard", env.read("video-project/storyboard.json"), &one)
	require.NoError(t, err)
	require.EqualValues(t, 2, saved.Revision)

	// Stale expected_revision → 409, file untouched.
	before := env.read("video-project/storyboard.json")
	_, err = env.svc.SaveProduct(env.ctx, env.wsID, "storyboard", `{"revision": 0}`, &one)
	require.ErrorAs(t, err, &vpErr)
	require.Equal(t, http.StatusConflict, vpErr.Status)
	require.Contains(t, vpErr.Message, "revision")
	require.Equal(t, before, env.read("video-project/storyboard.json"), "冲突时不得落盘")

	// Invalid JSON → 400 Chinese.
	_, err = env.svc.SaveProduct(env.ctx, env.wsID, "storyboard", `{"revision": `, &one)
	require.ErrorAs(t, err, &vpErr)
	require.Equal(t, http.StatusBadRequest, vpErr.Status)
	require.Contains(t, vpErr.Message, "JSON")

	// Non-object JSON → 400.
	_, err = env.svc.SaveProduct(env.ctx, env.wsID, "scenes", `[1, 2]`, &zero)
	require.ErrorAs(t, err, &vpErr)
	require.Equal(t, http.StatusBadRequest, vpErr.Status)

	// json product without expected_revision → 400.
	_, err = env.svc.SaveProduct(env.ctx, env.wsID, "scenes", `{"revision": 0}`, nil)
	require.ErrorAs(t, err, &vpErr)
	require.Equal(t, http.StatusBadRequest, vpErr.Status)
	require.Contains(t, vpErr.Message, "expected_revision")

	// Unknown key → 400 mentioning the whitelist.
	_, err = env.svc.SaveProduct(env.ctx, env.wsID, "nope", `{}`, &zero)
	require.ErrorAs(t, err, &vpErr)
	require.Equal(t, http.StatusBadRequest, vpErr.Status)
	require.Contains(t, vpErr.Message, "nope")

	// Markdown: no expected_revision required, raw text round-trips.
	md, err := env.svc.SaveProduct(env.ctx, env.wsID, "brief", "# 导演阐述\n基调：轻快", nil)
	require.NoError(t, err)
	require.True(t, md.Present)
	require.Equal(t, "markdown", md.Kind)
	var mdText string
	require.NoError(t, json.Unmarshal(md.Data, &mdText))
	require.Equal(t, "# 导演阐述\n基调：轻快", mdText)
	require.Equal(t, "# 导演阐述\n基调：轻快", env.read("video-project/directorial-brief.md"))

	require.Empty(t, findVideoTmpFiles(t, env.projectDir()), "原子写不得残留 tmp 文件")
}

// TestVideoProjectCreateChange covers the chg-<yyyymmdd>-<seq3> id sequence
// (per-day max + 1), the persisted pending change, and the 400 validation
// paths.
func TestVideoProjectCreateChange(t *testing.T) {
	env := setupVideoProjectEnv(t, false)
	day := time.Now().Format("20060102")
	var vpErr *service.VideoProjectError

	env.write(fmt.Sprintf("video-project/changes/chg-%s-002.json", day),
		fmt.Sprintf(`{"id":"chg-%s-002","target":"x","kind":"edit","content":"y","status":"pending"}`, day))

	chg, err := env.svc.CreateChange(env.ctx, env.wsID, "storyboard.json#shots[0].visual.prompt", "annotation", "镜头再慢一点")
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("chg-%s-003", day), chg.ID)
	require.Equal(t, "pending", chg.Status)
	require.Zero(t, chg.DispatchedToIssue)
	require.Equal(t, "annotation", chg.Kind)
	_, perr := time.Parse(time.RFC3339, chg.CreatedAt)
	require.NoError(t, perr)

	var parsed service.VideoChange
	require.NoError(t, json.Unmarshal([]byte(env.read("video-project/changes/"+chg.ID+".json")), &parsed))
	require.Equal(t, chg, parsed)

	next, err := env.svc.CreateChange(env.ctx, env.wsID, "script.json", "edit", "换一个开场")
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("chg-%s-004", day), next.ID, "seq 当天递增")

	agg, err := env.svc.GetAggregate(env.ctx, env.wsID)
	require.NoError(t, err)
	require.Len(t, agg.Changes, 3)

	// Validation: kind whitelist, empty target, empty content.
	_, err = env.svc.CreateChange(env.ctx, env.wsID, "storyboard.json", "note", "x")
	require.ErrorAs(t, err, &vpErr)
	require.Equal(t, http.StatusBadRequest, vpErr.Status)
	require.Contains(t, vpErr.Message, "annotation")

	_, err = env.svc.CreateChange(env.ctx, env.wsID, "   ", "edit", "x")
	require.ErrorAs(t, err, &vpErr)
	require.Equal(t, http.StatusBadRequest, vpErr.Status)

	_, err = env.svc.CreateChange(env.ctx, env.wsID, "storyboard.json", "edit", "  \n ")
	require.ErrorAs(t, err, &vpErr)
	require.Equal(t, http.StatusBadRequest, vpErr.Status)

	require.Empty(t, findVideoTmpFiles(t, env.projectDir()), "原子写不得残留 tmp 文件")
}

// TestVideoProjectDispatchAutoMatch covers target-prefix → child issue
// auto-matching (编剧/角色/场景/分镜), the fallback to the workspace's own
// issue, and the dispatched state written back to the change file.
func TestVideoProjectDispatchAutoMatch(t *testing.T) {
	env := setupVideoProjectEnv(t, true)

	cases := []struct{ target, wantKey string }{
		{"storyboard.json#shots[2].visual.prompt", "storyboard"},
		{"script.json#scenes[0].dialogue", "screenwriter"},
		{"storyline.json#acts", "screenwriter"},
		{"characters.json#c-hero.appearance_prompt", "characters"},
		{"scenes.json#s-cafe", "scenes"},
		{"directorial-brief.md#基调", "root"}, // 无匹配 → 回退工作空间自身 issue
	}
	for i, tc := range cases {
		chg, err := env.svc.CreateChange(env.ctx, env.wsID, tc.target, "annotation", "意见-"+tc.target)
		require.NoError(t, err)
		res, err := env.svc.DispatchChange(env.ctx, env.wsID, chg.ID, 0, 0)
		require.NoError(t, err, "target %s", tc.target)

		wantIssue := env.issueID[tc.wantKey]
		require.EqualValues(t, wantIssue, res.IssueID, "target %s 应路由到 %s", tc.target, tc.wantKey)
		require.NotEmpty(t, res.IssueTitle)
		require.Equal(t, "dispatched", res.Change.Status)
		require.EqualValues(t, wantIssue, res.Change.DispatchedToIssue)

		require.Len(t, env.dispat.calls, i+1)
		call := env.dispat.calls[i]
		require.EqualValues(t, wantIssue, call.IssueID)
		require.Contains(t, call.Comment, "意见-"+tc.target, "修改意见原文进入 request-changes")
		require.Contains(t, call.Comment, tc.target, "目标作为路由上下文")
		require.NotEmpty(t, call.Author)

		// Dispatched state is persisted to the change file.
		var onDisk service.VideoChange
		require.NoError(t, json.Unmarshal([]byte(env.read("video-project/changes/"+chg.ID+".json")), &onDisk))
		require.Equal(t, "dispatched", onDisk.Status)
		require.EqualValues(t, wantIssue, onDisk.DispatchedToIssue)
	}
	require.Len(t, env.dispat.calls, len(cases))
}

// TestVideoProjectDispatchExplicitIssue: an explicit issue_id wins over target
// matching; an unknown explicit issue is a 400.
func TestVideoProjectDispatchExplicitIssue(t *testing.T) {
	env := setupVideoProjectEnv(t, true)
	var vpErr *service.VideoProjectError

	other, err := env.q.CreateIssue(env.ctx, store.CreateIssueParams{ColumnID: env.columnID, Title: "独立 issue"})
	require.NoError(t, err)

	chg, err := env.svc.CreateChange(env.ctx, env.wsID, "storyboard.json", "edit", "换分镜")
	require.NoError(t, err)
	res, err := env.svc.DispatchChange(env.ctx, env.wsID, chg.ID, other.ID, 7)
	require.NoError(t, err)
	require.EqualValues(t, other.ID, res.IssueID, "显式 issue_id 优先于目标匹配")
	require.Equal(t, "独立 issue", res.IssueTitle)
	require.EqualValues(t, other.ID, res.Change.DispatchedToIssue)
	require.Len(t, env.dispat.calls, 1)
	require.EqualValues(t, 7, env.dispat.calls[0].CallerUserID)

	// Unknown explicit issue → 400 Chinese.
	_, err = env.svc.DispatchChange(env.ctx, env.wsID, chg.ID, 999999, 0)
	require.ErrorAs(t, err, &vpErr)
	require.Equal(t, http.StatusBadRequest, vpErr.Status)
	require.Contains(t, vpErr.Message, "999999")
}

// TestVideoProjectDispatchNoIssue: no explicit issue, no child match, and a
// workspace with no linked issue → 400 with a Chinese explanation; the change
// stays pending. A missing change id is a 404.
func TestVideoProjectDispatchNoIssue(t *testing.T) {
	env := setupVideoProjectEnv(t, false)
	var vpErr *service.VideoProjectError

	ws2, err := env.q.CreateWorkspace(env.ctx, store.CreateWorkspaceParams{
		Name: "no-issue-ws", Path: "/tmp/ws-no-issue", Status: "active", OwnerType: "user", OwnerID: 1,
	})
	require.NoError(t, err)
	owner := service.OwnerRef{Type: "user", ID: 1}
	ws2Dir := owner.WorkspacePath(env.dataDir, ws2.ID)
	require.NoError(t, os.MkdirAll(filepath.Join(ws2Dir, "video-project", "changes"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ws2Dir, "video-project", "changes", "chg-20260927-001.json"),
		[]byte(`{"id":"chg-20260927-001","target":"storyboard.json#x","kind":"edit","content":"y","status":"pending"}`), 0o644))

	_, err = env.svc.DispatchChange(env.ctx, ws2.ID, "chg-20260927-001", 0, 0)
	require.ErrorAs(t, err, &vpErr)
	require.Equal(t, http.StatusBadRequest, vpErr.Status)
	require.Contains(t, vpErr.Message, "issue")
	require.Empty(t, env.dispat.calls, "无可用 issue 不得调用 request-changes")

	// Missing change → 404 (also on the main workspace).
	_, err = env.svc.DispatchChange(env.ctx, env.wsID, "chg-20260927-099", 0, 0)
	require.ErrorAs(t, err, &vpErr)
	require.Equal(t, http.StatusNotFound, vpErr.Status)

	// Path traversal in the change id is rejected.
	_, err = env.svc.DispatchChange(env.ctx, env.wsID, "../secret", 0, 0)
	require.ErrorAs(t, err, &vpErr)
	require.Equal(t, http.StatusBadRequest, vpErr.Status)
}

// TestVideoProjectDispatchFailureKeepsPending: a failed or blocked
// request-changes bounce must not mark the change dispatched.
func TestVideoProjectDispatchFailureKeepsPending(t *testing.T) {
	env := setupVideoProjectEnv(t, true)
	var vpErr *service.VideoProjectError

	chg, err := env.svc.CreateChange(env.ctx, env.wsID, "storyboard.json", "edit", "换分镜")
	require.NoError(t, err)

	env.dispat.err = fmt.Errorf("no implement (instruct) column found in this project")
	_, err = env.svc.DispatchChange(env.ctx, env.wsID, chg.ID, 0, 0)
	require.ErrorAs(t, err, &vpErr)
	require.Equal(t, http.StatusBadRequest, vpErr.Status)

	env.dispat.err = nil
	env.dispat.result = service.RequestChangesResult{Blocked: true, BlockedReason: "livelock cap"}
	_, err = env.svc.DispatchChange(env.ctx, env.wsID, chg.ID, 0, 0)
	require.ErrorAs(t, err, &vpErr)
	require.Equal(t, http.StatusBadRequest, vpErr.Status)
	require.Contains(t, vpErr.Message, "livelock cap")

	var onDisk service.VideoChange
	require.NoError(t, json.Unmarshal([]byte(env.read("video-project/changes/"+chg.ID+".json")), &onDisk))
	require.Equal(t, "pending", onDisk.Status, "失败的 dispatch 不得改状态")
	require.Zero(t, onDisk.DispatchedToIssue)
}

// TestVideoProjectAPIContract drives the four REST endpoints (plan §4.2) over
// gin: exists=false aggregate (200, empty collections — not 404), a product
// save returning a single product object, 409 on a stale expected_revision,
// change creation, and dispatch returning {change, issue_id, issue_title}.
func TestVideoProjectAPIContract(t *testing.T) {
	env := setupVideoProjectEnv(t, true)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := api.NewVideoProjectHandler(env.svc)
	router.GET("/api/workspaces/:id/video-project", h.Get)
	router.PUT("/api/workspaces/:id/video-project/products/:key", h.PutProduct)
	router.POST("/api/workspaces/:id/video-project/changes", h.CreateChange)
	router.POST("/api/workspaces/:id/video-project/changes/:changeId/dispatch", h.DispatchChange)

	base := func(p string) string {
		return fmt.Sprintf("/api/workspaces/%d/video-project%s", env.wsID, p)
	}
	do := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	// exists=false → 200 with empty collections.
	w := do(http.MethodGet, base(""), "")
	require.Equal(t, http.StatusOK, w.Code)
	var empty service.VideoProjectAggregate
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &empty))
	require.False(t, empty.Exists)
	require.Empty(t, empty.Products)
	require.Empty(t, empty.Changes)
	require.Empty(t, empty.Outputs)

	// Product save → 200, single product object, server-owned revision bump.
	w = do(http.MethodPut, base("/products/storyboard"),
		`{"content": "{\"review_status\": \"draft\", \"shots\": []}", "expected_revision": 0}`)
	require.Equal(t, http.StatusOK, w.Code)
	var saved service.VideoProduct
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &saved))
	require.Equal(t, "storyboard", saved.Key)
	require.True(t, saved.Present)
	require.EqualValues(t, 1, saved.Revision)
	require.Equal(t, "draft", saved.ReviewStatus)

	// Stale revision → 409 CONFLICT.
	w = do(http.MethodPut, base("/products/storyboard"), `{"content": "{}", "expected_revision": 0}`)
	require.Equal(t, http.StatusConflict, w.Code)
	var apiErr struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &apiErr))
	require.Equal(t, "CONFLICT", apiErr.Error.Code)
	require.NotEmpty(t, apiErr.Error.Message)

	// Invalid JSON content → 400.
	w = do(http.MethodPut, base("/products/storyboard"), `{"content": "{", "expected_revision": 1}`)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// Change creation → 201 + the change object.
	w = do(http.MethodPost, base("/changes"),
		`{"target":"storyboard.json#shots[0].visual.prompt","kind":"annotation","content":"镜头慢一点"}`)
	require.Equal(t, http.StatusCreated, w.Code)
	var chg service.VideoChange
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &chg))
	require.True(t, strings.HasPrefix(chg.ID, "chg-"), "got id %q", chg.ID)
	require.Equal(t, "pending", chg.Status)

	// Dispatch without a body → auto-match the 分镜 child issue.
	w = do(http.MethodPost, base("/changes/"+chg.ID+"/dispatch"), "")
	require.Equal(t, http.StatusOK, w.Code)
	var res service.VideoDispatchResult
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
	require.EqualValues(t, env.issueID["storyboard"], res.IssueID)
	require.Equal(t, "wave3 分镜", res.IssueTitle)
	require.Equal(t, "dispatched", res.Change.Status)
	require.EqualValues(t, env.issueID["storyboard"], res.Change.DispatchedToIssue)
}
