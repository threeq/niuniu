package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// VideoProjectService is the P3 "video product" thin API (plan
// 2026-09-27-video-creation-implementation.md §4): read/write the
// agent-produced artifact family under <ws>/video-project/.
//
// No new database tables — the files on disk are the source of truth. The
// service only aggregates reads, version-stamps saves (top-level revision +
// updated_at), records change requests, and routes a change back to the
// owning/child issue through the existing kanban request-changes path.
//
// All paths are built through OwnerRef.WorkspacePath (the only legal way to
// construct per-owner paths, CLAUDE.md hard rule).
type VideoProjectService struct {
	q       *store.Queries
	dataDir string
	// dispatch is the issue-level request-changes path (the same service the
	// POST /api/issues/:id/request-changes handler calls). Optional; when nil
	// the dispatch endpoint reports a wiring error instead of panicking.
	dispatch ChangeDispatcher
}

// ChangeDispatcher is the slice of EpicExecutionService the video-project
// dispatch reuses. Kept as an interface so tests can substitute a fake and the
// service does not depend on the whole execution engine.
type ChangeDispatcher interface {
	RequestChanges(ctx context.Context, in RequestChangesInput) (RequestChangesResult, error)
}

// NewVideoProjectService constructs the service. dataDir is <~/.niuniu> (the
// owner-dir root); workspace paths resolve through OwnerRef.WorkspacePath.
func NewVideoProjectService(q *store.Queries, dataDir string) *VideoProjectService {
	return &VideoProjectService{q: q, dataDir: dataDir}
}

// SetChangeDispatcher wires the request-changes service path used by the
// "送回重生成" dispatch action.
func (s *VideoProjectService) SetChangeDispatcher(d ChangeDispatcher) { s.dispatch = d }

// VideoProjectError is a contract-level failure carrying the HTTP status and a
// user-facing Chinese message, so the thin API layer maps service failures to
// §4.2's 400/404/409 without re-deriving them. Errors of any other type are
// treated as internal errors by the API layer.
type VideoProjectError struct {
	Status  int
	Message string
}

func (e *VideoProjectError) Error() string { return e.Message }

func videoErr(status int, format string, args ...any) *VideoProjectError {
	return &VideoProjectError{Status: status, Message: fmt.Sprintf(format, args...)}
}

// --- Product key ↔ file mapping (plan §4.1, whitelist — unknown key → 400) ---

const videoProjectDirName = "video-project"

// videoProjectListLimit caps each output source (output/, shots/) and the
// change list, mirroring the contract's "各自上限 200" for outputs.
const videoProjectListLimit = 200

type videoProductSpec struct {
	key       string
	file      string // relative to <ws>, forward slashes
	kind      string // "json" | "markdown"
	versioned bool   // json products carry top-level revision + review_status
}

var videoProductSpecs = []videoProductSpec{
	{key: "brief", file: "video-project/directorial-brief.md", kind: "markdown"},
	{key: "script", file: "video-project/script.json", kind: "json", versioned: true},
	{key: "storyline", file: "video-project/storyline.json", kind: "json", versioned: true},
	{key: "characters", file: "video-project/characters.json", kind: "json", versioned: true},
	{key: "scenes", file: "video-project/scenes.json", kind: "json", versioned: true},
	{key: "storyboard", file: "video-project/storyboard.json", kind: "json", versioned: true},
}

func videoProductSpecByKey(key string) (videoProductSpec, bool) {
	for _, spec := range videoProductSpecs {
		if spec.key == key {
			return spec, true
		}
	}
	return videoProductSpec{}, false
}

// videoChangeRouteKeywords maps a target's product prefix to the substring that
// identifies the child issue responsible for that product (plan §4.2).
var videoChangeRouteKeywords = map[string]string{
	"storyline":  "编剧",
	"script":     "编剧",
	"characters": "角色",
	"scenes":     "场景",
	"storyboard": "分镜",
}

// --- Wire shapes (plan §4.2) ---

// VideoProduct is one product entry of the aggregate read / a product save.
type VideoProduct struct {
	Key      string `json:"key"`
	File     string `json:"file"`
	Kind     string `json:"kind"`
	Present  bool   `json:"present"`
	Revision int64  `json:"revision"`
	// ReviewStatus is only meaningful for json products ("" for markdown).
	ReviewStatus string `json:"review_status"`
	// Data carries the parsed document: the JSON object for json products, the
	// raw text as a JSON string for markdown. Omitted when the file is absent
	// or failed to parse (see Error).
	Data json.RawMessage `json:"data,omitempty"`
	// Error is a Chinese explanation when the file exists but could not be
	// read/parsed; it never fails the whole aggregate read.
	Error string `json:"error"`
}

// VideoChange is one modification request (changes/<id>.json).
type VideoChange struct {
	ID                string `json:"id"`
	Target            string `json:"target"`
	Kind              string `json:"kind"`
	Content           string `json:"content"`
	Status            string `json:"status"`
	DispatchedToIssue int64  `json:"dispatched_to_issue"`
	CreatedAt         string `json:"created_at"`
}

// VideoOutput is one synthesized artifact (output/* or shots/*).
type VideoOutput struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modified_at"`
}

// VideoShards carries counts only — detail stays on disk (plan §4.2).
type VideoShards struct {
	Quotes int `json:"quotes"`
	QC     int `json:"qc"`
}

// VideoProjectAggregate is the GET /api/workspaces/:id/video-project payload.
type VideoProjectAggregate struct {
	Exists   bool          `json:"exists"`
	Products []VideoProduct `json:"products"`
	Changes  []VideoChange  `json:"changes"`
	Outputs  []VideoOutput  `json:"outputs"`
	Shards   VideoShards    `json:"shards"`
}

// VideoDispatchResult is the dispatch response: the updated change plus the
// issue it was routed to.
type VideoDispatchResult struct {
	Change     VideoChange `json:"change"`
	IssueID    int64       `json:"issue_id"`
	IssueTitle string      `json:"issue_title"`
}

// workspaceContext loads the workspace row and resolves its on-disk directory
// via the OwnerRef (the only legal per-owner path construction).
func (s *VideoProjectService) workspaceContext(ctx context.Context, workspaceID int64) (store.Workspace, string, error) {
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return store.Workspace{}, "", videoErr(http.StatusNotFound, "工作空间不存在")
	}
	wsDir := OwnerRef{Type: ws.OwnerType, ID: ws.OwnerID}.WorkspacePath(s.dataDir, ws.ID)
	return ws, wsDir, nil
}

// GetAggregate reads the whole video-project area. A missing directory yields
// exists=false with empty collections — never a 404 (plan §4.2).
func (s *VideoProjectService) GetAggregate(ctx context.Context, workspaceID int64) (VideoProjectAggregate, error) {
	_, wsDir, err := s.workspaceContext(ctx, workspaceID)
	if err != nil {
		return VideoProjectAggregate{}, err
	}
	projectDir := filepath.Join(wsDir, videoProjectDirName)
	if info, statErr := os.Stat(projectDir); statErr != nil || !info.IsDir() {
		return VideoProjectAggregate{
			Products: []VideoProduct{},
			Changes:  []VideoChange{},
			Outputs:  []VideoOutput{},
		}, nil
	}
	agg := VideoProjectAggregate{
		Exists:   true,
		Products: make([]VideoProduct, 0, len(videoProductSpecs)),
	}
	for _, spec := range videoProductSpecs {
		agg.Products = append(agg.Products, readVideoProduct(wsDir, spec))
	}
	agg.Changes = readVideoChanges(projectDir)
	agg.Outputs = readVideoOutputs(projectDir)
	agg.Shards = VideoShards{
		Quotes: countVideoProjectFiles(filepath.Join(projectDir, "quotes")),
		QC:     countVideoProjectFiles(filepath.Join(projectDir, "qc")),
	}
	return agg, nil
}

// readVideoProduct reads one whitelisted product. A parse failure marks only
// this entry's Error (Chinese) and omits Data — it never fails the read.
func readVideoProduct(wsDir string, spec videoProductSpec) VideoProduct {
	out := VideoProduct{Key: spec.key, File: spec.file, Kind: spec.kind}
	abs := filepath.Join(wsDir, filepath.FromSlash(spec.file))
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		return out
	}
	out.Present = true
	raw, err := os.ReadFile(abs)
	if err != nil {
		out.Error = fmt.Sprintf("读取失败：%v", err)
		return out
	}
	if spec.kind == "markdown" {
		if enc, merr := json.Marshal(string(raw)); merr == nil {
			out.Data = enc
		}
		return out
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		out.Error = fmt.Sprintf("JSON 解析失败：%v", err)
		return out
	}
	out.Data = json.RawMessage(raw)
	if v, ok := top["revision"]; ok {
		var rev int64
		if json.Unmarshal(v, &rev) == nil {
			out.Revision = rev
		}
	}
	if v, ok := top["review_status"]; ok {
		var status string
		if json.Unmarshal(v, &status) == nil {
			out.ReviewStatus = status
		}
	}
	return out
}

// readVideoChanges lists changes/*.json, newest first. A corrupt file is
// skipped (logged) rather than failing the aggregate.
func readVideoChanges(projectDir string) []VideoChange {
	out := []VideoChange{}
	dir := filepath.Join(projectDir, "changes")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		abs := filepath.Join(dir, e.Name())
		raw, rerr := os.ReadFile(abs)
		if rerr != nil {
			slog.Warn("video-project: read change", "file", abs, "error", rerr)
			continue
		}
		var chg VideoChange
		if uerr := json.Unmarshal(raw, &chg); uerr != nil {
			slog.Warn("video-project: skip unparseable change", "file", abs, "error", uerr)
			continue
		}
		if chg.ID == "" {
			chg.ID = strings.TrimSuffix(e.Name(), ".json")
		}
		if chg.Status == "" {
			chg.Status = "pending"
		}
		if chg.CreatedAt == "" {
			if info, ierr := e.Info(); ierr == nil {
				chg.CreatedAt = info.ModTime().UTC().Format(time.RFC3339)
			}
		}
		out = append(out, chg)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > videoProjectListLimit {
		out = out[:videoProjectListLimit]
	}
	return out
}

// readVideoOutputs lists output/* and shots/* files (each capped at the list
// limit), newest first.
func readVideoOutputs(projectDir string) []VideoOutput {
	out := []VideoOutput{}
	for _, rel := range []string{"output", "shots"} {
		dir := filepath.Join(projectDir, rel)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		per := make([]VideoOutput, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			info, ierr := e.Info()
			if ierr != nil {
				continue
			}
			per = append(per, VideoOutput{
				Path:       path.Join(videoProjectDirName, rel, e.Name()),
				Size:       info.Size(),
				ModifiedAt: info.ModTime().UTC().Format(time.RFC3339),
			})
		}
		sortVideoOutputs(per)
		if len(per) > videoProjectListLimit {
			per = per[:videoProjectListLimit]
		}
		out = append(out, per...)
	}
	sortVideoOutputs(out)
	return out
}

func sortVideoOutputs(list []VideoOutput) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].ModifiedAt != list[j].ModifiedAt {
			return list[i].ModifiedAt > list[j].ModifiedAt
		}
		return list[i].Path < list[j].Path
	})
}

// countVideoProjectFiles counts regular files directly under dir (0 when the
// dir does not exist).
func countVideoProjectFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			n++
		}
	}
	return n
}

// SaveProduct writes one product (plan §4.2 #2).
//
//   - json products must parse as a JSON object, else 400 (Chinese);
//   - the server owns the top level: revision is bumped by +1 and updated_at
//     refreshed (client values of both are ignored);
//   - expected_revision must match the current on-disk revision, else 409
//     (concurrency guard: the agent may have regenerated meanwhile);
//   - the write is atomic (tmp + rename).
func (s *VideoProjectService) SaveProduct(ctx context.Context, workspaceID int64, key, content string, expectedRevision *int64) (VideoProduct, error) {
	spec, ok := videoProductSpecByKey(key)
	if !ok {
		return VideoProduct{}, videoErr(http.StatusBadRequest,
			"未知产物 key %q（可用：brief、script、storyline、characters、scenes、storyboard）", key)
	}
	_, wsDir, err := s.workspaceContext(ctx, workspaceID)
	if err != nil {
		return VideoProduct{}, err
	}
	abs := filepath.Join(wsDir, filepath.FromSlash(spec.file))

	var body []byte
	if !spec.versioned {
		// Markdown products carry no revision/review_status — raw text only.
		body = []byte(content)
	} else {
		var top map[string]json.RawMessage
		if err := json.Unmarshal([]byte(content), &top); err != nil {
			return VideoProduct{}, videoErr(http.StatusBadRequest, "content 必须是合法的 JSON 对象：%v", err)
		}
		if expectedRevision == nil {
			return VideoProduct{}, videoErr(http.StatusBadRequest, "json 类产物必须携带 expected_revision")
		}
		current := readProductRevision(abs)
		if *expectedRevision != current {
			return VideoProduct{}, videoErr(http.StatusConflict,
				"revision 不匹配：当前 %d、期望 %d（产物可能已被重新生成，请刷新后重试）", current, *expectedRevision)
		}
		revRaw, _ := json.Marshal(current + 1)
		top["revision"] = revRaw
		tsRaw, _ := json.Marshal(time.Now().UTC().Format(time.RFC3339))
		top["updated_at"] = tsRaw
		body, err = marshalVideoJSON(top)
		if err != nil {
			return VideoProduct{}, err
		}
	}
	if err := writeFileAtomic(abs, body); err != nil {
		return VideoProduct{}, fmt.Errorf("写入产物失败：%w", err)
	}
	return readVideoProduct(wsDir, spec), nil
}

// readProductRevision returns the top-level revision of an existing product
// file (0 when absent or unreadable).
func readProductRevision(abs string) int64 {
	raw, err := os.ReadFile(abs)
	if err != nil {
		return 0
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return 0
	}
	if v, ok := top["revision"]; ok {
		var rev int64
		if json.Unmarshal(v, &rev) == nil {
			return rev
		}
	}
	return 0
}

// CreateChange records a new modification request as
// changes/chg-<yyyymmdd>-<seq3>.json with status=pending. The sequence is the
// per-day max over the existing files + 1 (plan §4.2 #3).
func (s *VideoProjectService) CreateChange(ctx context.Context, workspaceID int64, target, kind, content string) (VideoChange, error) {
	target = strings.TrimSpace(target)
	kind = strings.TrimSpace(kind)
	if target == "" {
		return VideoChange{}, videoErr(http.StatusBadRequest, "target 不能为空")
	}
	if kind != "annotation" && kind != "edit" {
		return VideoChange{}, videoErr(http.StatusBadRequest, `kind 必须是 "annotation" 或 "edit"`)
	}
	if strings.TrimSpace(content) == "" {
		return VideoChange{}, videoErr(http.StatusBadRequest, "content 不能为空")
	}
	_, wsDir, err := s.workspaceContext(ctx, workspaceID)
	if err != nil {
		return VideoChange{}, err
	}
	changesDir := filepath.Join(wsDir, videoProjectDirName, "changes")
	chg := VideoChange{
		ID:        nextVideoChangeID(changesDir, time.Now()),
		Target:    target,
		Kind:      kind,
		Content:   content,
		Status:    "pending",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	body, err := marshalVideoJSON(chg)
	if err != nil {
		return VideoChange{}, err
	}
	if err := writeFileAtomic(filepath.Join(changesDir, chg.ID+".json"), body); err != nil {
		return VideoChange{}, fmt.Errorf("写入修改请求失败：%w", err)
	}
	return chg, nil
}

// nextVideoChangeID scans the changes directory for chg-<yyyymmdd>-NNN.json and
// returns the next id for the given day.
func nextVideoChangeID(changesDir string, now time.Time) string {
	prefix := "chg-" + now.Format("20060102") + "-"
	max := 0
	entries, err := os.ReadDir(changesDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".json") {
				continue
			}
			num := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".json")
			if n, aerr := strconv.Atoi(num); aerr == nil && n > max {
				max = n
			}
		}
	}
	return fmt.Sprintf("%s%03d", prefix, max+1)
}

// DispatchChange routes a recorded change back to a regeneration issue (plan
// §4.2 #4): the target issue is the explicit issue_id when given, else the
// child issue whose title matches the target's product prefix, else the
// workspace's own issue. The action reuses the existing request-changes service
// path; on success the change file flips to status=dispatched with
// dispatched_to_issue recorded.
func (s *VideoProjectService) DispatchChange(ctx context.Context, workspaceID int64, changeID string, explicitIssueID, callerUserID int64) (VideoDispatchResult, error) {
	ws, wsDir, err := s.workspaceContext(ctx, workspaceID)
	if err != nil {
		return VideoDispatchResult{}, err
	}
	changeID = strings.TrimSpace(changeID)
	if changeID == "" || strings.ContainsAny(changeID, `/\`) {
		return VideoDispatchResult{}, videoErr(http.StatusBadRequest, "修改请求 id 非法")
	}
	changePath := filepath.Join(wsDir, videoProjectDirName, "changes", changeID+".json")
	raw, rerr := os.ReadFile(changePath)
	if rerr != nil {
		return VideoDispatchResult{}, videoErr(http.StatusNotFound, "修改请求 %s 不存在", changeID)
	}
	var chg VideoChange
	if uerr := json.Unmarshal(raw, &chg); uerr != nil {
		return VideoDispatchResult{}, videoErr(http.StatusInternalServerError, "修改请求文件损坏：%v", uerr)
	}
	chg.ID = changeID

	issue, err := s.resolveDispatchIssue(ctx, ws, chg.Target, explicitIssueID)
	if err != nil {
		return VideoDispatchResult{}, err
	}
	if s.dispatch == nil {
		return VideoDispatchResult{}, videoErr(http.StatusInternalServerError, "送回重生成通道未接线")
	}
	res, derr := s.dispatch.RequestChanges(ctx, RequestChangesInput{
		IssueID:      issue.ID,
		Comment:      videoChangeDispatchComment(chg),
		Author:       "视频产物",
		CallerUserID: callerUserID,
	})
	if derr != nil {
		return VideoDispatchResult{}, videoErr(http.StatusBadRequest, "送回重生成失败：%v", derr)
	}
	if res.Blocked {
		reason := strings.TrimSpace(res.BlockedReason)
		if reason == "" {
			reason = "路由被阻塞，需要人工处理"
		}
		return VideoDispatchResult{}, videoErr(http.StatusBadRequest, "送回重生成被阻塞：%s", reason)
	}

	chg.Status = "dispatched"
	chg.DispatchedToIssue = issue.ID
	body, merr := marshalVideoJSON(chg)
	if merr != nil {
		return VideoDispatchResult{}, merr
	}
	if werr := writeFileAtomic(changePath, body); werr != nil {
		return VideoDispatchResult{}, videoErr(http.StatusInternalServerError, "修改请求已送出，但状态保存失败：%v", werr)
	}
	return VideoDispatchResult{Change: chg, IssueID: issue.ID, IssueTitle: issue.Title}, nil
}

// videoChangeDispatchComment renders the review comment handed to the routed
// issue: the target is kept as routing context, the change content verbatim as
// the modification opinion.
func videoChangeDispatchComment(chg VideoChange) string {
	if strings.TrimSpace(chg.Target) == "" {
		return chg.Content
	}
	return fmt.Sprintf("【视频产物修改请求】目标：%s\n\n%s", chg.Target, chg.Content)
}

// resolveDispatchIssue picks the issue a change is routed to (plan §4.2):
// explicit issue_id > child issue matching the target prefix > the workspace's
// own issue. No usable issue → 400 with a Chinese explanation.
func (s *VideoProjectService) resolveDispatchIssue(ctx context.Context, ws store.Workspace, target string, explicitIssueID int64) (store.Issue, error) {
	if explicitIssueID > 0 {
		issue, err := s.q.GetIssue(ctx, explicitIssueID)
		if err != nil {
			return store.Issue{}, videoErr(http.StatusBadRequest, "指定的 issue %d 不存在", explicitIssueID)
		}
		return issue, nil
	}
	// Auto-match: storyline|script → 编剧, characters → 角色, scenes → 场景,
	// storyboard → 分镜, among the workspace issue's CHILDREN.
	if keyword := videoChangeRouteKeywords[videoTargetProductKey(target)]; keyword != "" && ws.IssueID.Valid {
		children, cerr := s.q.ListIssuesByParent(ctx, sql.NullInt64{Int64: ws.IssueID.Int64, Valid: true})
		if cerr == nil {
			for _, child := range children {
				if strings.Contains(child.Title, keyword) {
					return child, nil
				}
			}
		}
	}
	// Fallback: the workspace's own issue.
	if ws.IssueID.Valid {
		if issue, err := s.q.GetIssue(ctx, ws.IssueID.Int64); err == nil {
			return issue, nil
		}
	}
	return store.Issue{}, videoErr(http.StatusBadRequest, "该工作空间未关联看板 issue，无法送回重生成（可显式传入 issue_id）")
}

// videoTargetProductKey extracts the product key a target starts with:
// "storyboard.json#shots[2].visual.prompt" → "storyboard".
func videoTargetProductKey(target string) string {
	t := strings.TrimSpace(target)
	t = strings.TrimPrefix(t, videoProjectDirName+"/")
	if i := strings.IndexAny(t, ".#[/\\"); i >= 0 {
		t = t[:i]
	}
	return t
}

// --- File helpers ---

// marshalVideoJSON encodes without HTML escaping (prompt/narration text is
// routinely full of <, > and &) and with 2-space indentation for reviewable
// on-disk diffs.
func marshalVideoJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeFileAtomic writes data to absPath via a temp file + rename, so a reader
// never observes a half-written product.
func writeFileAtomic(absPath string, data []byte) error {
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(absPath)+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmpName)
		if werr != nil {
			return werr
		}
		return cerr
	}
	_ = os.Chmod(tmpName, 0o644)
	if err := os.Rename(tmpName, absPath); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
