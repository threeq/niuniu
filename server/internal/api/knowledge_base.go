// Knowledge-base REST endpoints (Epic #496 · #498). This is the UI/REST layer
// over #497's service.KBService (entities + storage + FTS). KBService covers
// create/get/list/delete/search/ingest; the lifecycle + binding metadata the
// management UI needs (status enable/disable, async ingest status/progress,
// project bindings, document browse, presets) is layered here via raw SQL on
// the additive knowledge_bases columns (added in migrate.go) and the
// kb_bindings / kb_documents tables — no sqlc regen required.
//
// Routes: /api/me/knowledge-bases (owner CRUD + browse/search) +
// /api/projects/:id/knowledge-bases (project binding). Owner is the caller's
// personal owner (mirrors data sources). Source-kind mapping to #497: the UI's
// "upload" is a local KB whose dataset dir we fill via the files endpoint.
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

type KnowledgeBaseHandler struct {
	kb      *service.KBService
	db      *store.DB
	dataDir string
	Authz   *service.Authz

	// ingesting guards against concurrent ingests for the same KB. Two runs (e.g.
	// a double-clicked Retry, or Upload-while-downloading) would race on the
	// shared staging / ".old" dataset dirs in DownloadURLSource and corrupt the
	// result, so a second start while one is in flight is a no-op.
	ingesting sync.Map // kb id (int64) -> struct{}
}

func NewKnowledgeBaseHandler(kb *service.KBService, rawDB *sql.DB, dataDir string) *KnowledgeBaseHandler {
	return &KnowledgeBaseHandler{kb: kb, db: store.Wrap(rawDB), dataDir: dataDir}
}

func (h *KnowledgeBaseHandler) caller(c *gin.Context) (int64, service.OwnerRef, bool) {
	uid := c.GetInt64("auth_user_id")
	if uid <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return 0, service.OwnerRef{}, false
	}
	return uid, service.OwnerRef{Type: "user", ID: uid}, true
}

func kbIDParam(c *gin.Context, key string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(key), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad knowledge base id"})
		return 0, false
	}
	return id, true
}

// kbExtra holds the management-UI columns layered on top of #497's base row.
type kbExtra struct {
	Status        string
	IngestStatus  string
	IngestProg    int64
	IngestError   string
	DocCount      int64
	ChunkCount    int64
	LastIndexedAt sql.NullTime
}

func (h *KnowledgeBaseHandler) readExtra(ctx context.Context, id int64) kbExtra {
	var e kbExtra
	_ = h.db.QueryRowContext(ctx,
		`SELECT status, ingest_status, ingest_progress, ingest_error, doc_count, chunk_count, last_indexed_at
		   FROM knowledge_bases WHERE id = ?`, id).
		Scan(&e.Status, &e.IngestStatus, &e.IngestProg, &e.IngestError, &e.DocCount, &e.ChunkCount, &e.LastIndexedAt)
	if e.Status == "" {
		e.Status = "enabled"
	}
	if e.IngestStatus == "" {
		e.IngestStatus = "ready"
	}
	return e
}

func (h *KnowledgeBaseHandler) loadBindings(ctx context.Context, id int64) []gin.H {
	out := []gin.H{}
	rows, err := h.db.QueryContext(ctx,
		`SELECT target_type, target_id FROM kb_bindings WHERE kb_id = ? ORDER BY target_type, target_id`, id)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var tt string
		var ti int64
		if rows.Scan(&tt, &ti) == nil {
			out = append(out, gin.H{"target_type": tt, "target_id": ti})
		}
	}
	return out
}

func (h *KnowledgeBaseHandler) kbToJSON(ctx context.Context, kb store.KnowledgeBase) gin.H {
	return kbToJSONFrom(kb, h.readExtra(ctx, kb.ID), h.loadBindings(ctx, kb.ID))
}

// kbToJSONFrom renders a KB row from already-loaded extra columns + bindings, so
// List can format N KBs without a per-KB query (see readExtraBatch /
// loadBindingsBatch). A nil bindings slice renders as an empty JSON array.
func kbToJSONFrom(kb store.KnowledgeBase, e kbExtra, bindings []gin.H) gin.H {
	var ingestErr any
	if e.IngestError != "" {
		ingestErr = e.IngestError
	}
	var lastIndexed any
	if e.LastIndexedAt.Valid {
		lastIndexed = e.LastIndexedAt.Time.Format(time.RFC3339)
	}
	if bindings == nil {
		bindings = []gin.H{}
	}
	return gin.H{
		"id":              kb.ID,
		"name":            kb.Name,
		"description":     kb.Description,
		"source_kind":     kb.SourceKind,
		"source_location": kb.SourceAddr,
		"status":          e.Status,
		"ingest_status":   e.IngestStatus,
		"ingest_progress": e.IngestProg,
		"ingest_error":    ingestErr,
		"doc_count":       e.DocCount,
		"chunk_count":     e.ChunkCount,
		"last_indexed_at": lastIndexed,
		"bindings":        bindings,
		"created_at":      kb.CreatedAt.Format(time.RFC3339),
	}
}

// readExtraBatch loads the management-UI extra columns for ALL of owner's KBs in
// one query (keyed by id), so List avoids a per-KB readExtra round-trip.
func (h *KnowledgeBaseHandler) readExtraBatch(ctx context.Context, owner service.OwnerRef) map[int64]kbExtra {
	out := map[int64]kbExtra{}
	rows, err := h.db.QueryContext(ctx,
		`SELECT id, status, ingest_status, ingest_progress, ingest_error, doc_count, chunk_count, last_indexed_at
		   FROM knowledge_bases WHERE owner_type = ? AND owner_id = ?`, owner.Type, owner.ID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var e kbExtra
		if rows.Scan(&id, &e.Status, &e.IngestStatus, &e.IngestProg, &e.IngestError, &e.DocCount, &e.ChunkCount, &e.LastIndexedAt) != nil {
			continue
		}
		if e.Status == "" {
			e.Status = "enabled"
		}
		if e.IngestStatus == "" {
			e.IngestStatus = "ready"
		}
		out[id] = e
	}
	return out
}

// loadBindingsBatch loads bindings for ALL of owner's KBs in one query, grouped
// by kb_id, so List avoids a per-KB loadBindings round-trip.
func (h *KnowledgeBaseHandler) loadBindingsBatch(ctx context.Context, owner service.OwnerRef) map[int64][]gin.H {
	out := map[int64][]gin.H{}
	rows, err := h.db.QueryContext(ctx,
		`SELECT b.kb_id, b.target_type, b.target_id
		   FROM kb_bindings b JOIN knowledge_bases kb ON kb.id = b.kb_id
		  WHERE kb.owner_type = ? AND kb.owner_id = ?
		  ORDER BY b.target_type, b.target_id`, owner.Type, owner.ID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var kbid, ti int64
		var tt string
		if rows.Scan(&kbid, &tt, &ti) == nil {
			out[kbid] = append(out[kbid], gin.H{"target_type": tt, "target_id": ti})
		}
	}
	return out
}

func (h *KnowledgeBaseHandler) mapErr(c *gin.Context, err error) {
	// KBService surfaces owner-miss / missing rows as a generic error; treat any
	// lookup failure as not-found so the SPA shows the right state.
	c.JSON(http.StatusNotFound, gin.H{"error": "knowledge base not found", "detail": err.Error()})
}

// ---- async ingest ----------------------------------------------------------

func (h *KnowledgeBaseHandler) setIngest(ctx context.Context, id int64, status string, prog int64, errMsg string) {
	_, _ = h.db.ExecContext(ctx,
		`UPDATE knowledge_bases SET ingest_status = ?, ingest_progress = ?, ingest_error = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, prog, errMsg, id)
}

// runIngest performs an ingest in the background, advancing ingest_status /
// progress so the SPA's polling renders a live loading bar. For url sources it
// first downloads the corpus (primary + mirror fallback) into the per-owner
// dataset dir, then indexes it; local/upload sources are already on disk.
func (h *KnowledgeBaseHandler) runIngest(owner service.OwnerRef, id int64, sourceKind string) {
	ctx := context.Background()
	if sourceKind == service.KBSourceURL {
		h.setIngest(ctx, id, "downloading", 10, "")
		kb, err := h.kb.GetKB(ctx, owner, id)
		if err != nil {
			h.setIngest(ctx, id, "failed", 0, err.Error())
			return
		}
		if err := h.kb.DownloadURLSource(ctx, owner, kb, func(stage string, pct int) {
			h.setIngest(ctx, id, stage, int64(pct), "")
		}); err != nil {
			h.setIngest(ctx, id, "failed", 0, err.Error())
			return
		}
	}
	h.setIngest(ctx, id, "indexing", 60, "")
	res, err := h.kb.Ingest(ctx, owner, id, service.IngestOptions{Force: true})
	if err != nil {
		h.setIngest(ctx, id, "failed", 0, err.Error())
		return
	}
	_, _ = h.db.ExecContext(ctx,
		`UPDATE knowledge_bases
		    SET ingest_status = 'ready', ingest_progress = 100, ingest_error = '',
		        doc_count = ?, chunk_count = ?, last_indexed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		  WHERE id = ?`, res.FilesIngested, res.ChunksWritten, id)
}

// startIngest marks the KB pending and launches runIngest in the background,
// unless an ingest for this KB is already in flight (in which case it is a
// no-op: the running ingest owns the staging dir and will report final status).
func (h *KnowledgeBaseHandler) startIngest(ctx context.Context, owner service.OwnerRef, id int64, sourceKind string) {
	if _, busy := h.ingesting.LoadOrStore(id, struct{}{}); busy {
		return
	}
	h.setIngest(ctx, id, "pending", 5, "")
	go func() {
		defer h.ingesting.Delete(id)
		h.runIngest(owner, id, sourceKind)
	}()
}

// ---- CRUD ------------------------------------------------------------------

func (h *KnowledgeBaseHandler) List(c *gin.Context) {
	_, owner, ok := h.caller(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	kbs, err := h.kb.ListKBs(ctx, owner)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Batch the extra columns + bindings (2 queries total) instead of 2 queries
	// per KB, so the panel polling every couple seconds while a corpus ingests
	// stays at O(1) round-trips rather than O(N).
	extras := h.readExtraBatch(ctx, owner)
	bindings := h.loadBindingsBatch(ctx, owner)
	items := make([]gin.H, len(kbs))
	for i, kb := range kbs {
		e, hit := extras[kb.ID]
		if !hit {
			e = kbExtra{Status: "enabled", IngestStatus: "ready"}
		}
		items[i] = kbToJSONFrom(kb, e, bindings[kb.ID])
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

type kbBindingBody struct {
	TargetType string `json:"target_type"`
	TargetID   int64  `json:"target_id"`
}

type createKBBody struct {
	Name           string          `json:"name" binding:"required"`
	Description    string          `json:"description"`
	SourceKind     string          `json:"source_kind" binding:"required"`
	SourceLocation string          `json:"source_location"`
	MirrorURLs     []string        `json:"mirror_urls"`
	PresetID       string          `json:"preset_id"`
	Bindings       []kbBindingBody `json:"bindings"`
}

func (h *KnowledgeBaseHandler) Create(c *gin.Context) {
	_, owner, ok := h.caller(c)
	if !ok {
		return
	}
	var b createKBBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx := c.Request.Context()

	// Map the UI's three source kinds onto #497's local/url. "upload" is a local
	// KB whose dataset dir is filled later via the files endpoint.
	kind := service.KBSourceLocal
	addr := b.SourceLocation
	isUpload := b.SourceKind == "upload"
	switch b.SourceKind {
	case "url":
		kind = service.KBSourceURL
	case "local", "upload":
		kind = service.KBSourceLocal
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported source kind"})
		return
	}
	// Fail fast on a disallowed local source before persisting the KB row. Upload
	// (kind=local, addr filled below with the owner's datasets dir) is always
	// allowed; only a caller-supplied local path is gated. resolveSourceRoot
	// re-checks at ingest time as the real choke point — this is UX, not the
	// boundary.
	if kind == service.KBSourceLocal && !isUpload && addr != "" {
		if err := h.kb.EnsureLocalSourceAllowed(owner, addr); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
	}

	cfg := map[string]any{}
	if len(b.MirrorURLs) > 0 {
		cfg["mirror_urls"] = b.MirrorURLs
	}
	if b.PresetID != "" {
		cfg["preset_id"] = b.PresetID
	}
	cfgJSON, _ := json.Marshal(cfg)

	kb, err := h.kb.CreateKB(ctx, owner, service.CreateKBParams{
		Name: b.Name, Description: b.Description,
		SourceKind: kind, SourceAddr: addr, SourceConfig: string(cfgJSON),
	})
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}

	if isUpload {
		// Point the local source at this KB's dataset dir; files land there next.
		dir := owner.DatasetsPath(h.dataDir, kb.ID)
		_ = os.MkdirAll(dir, 0o755)
		_, _ = h.db.ExecContext(ctx, `UPDATE knowledge_bases SET source_addr = ? WHERE id = ?`, dir, kb.ID)
		kb.SourceAddr = dir
	}

	if len(b.Bindings) > 0 {
		_ = h.replaceBindings(ctx, kb.ID, b.Bindings)
	}

	// Upload waits for files; local/url ingest right away.
	if isUpload {
		h.setIngest(ctx, kb.ID, "pending", 0, "")
	} else {
		h.startIngest(ctx, owner, kb.ID, kind)
	}
	c.JSON(http.StatusCreated, h.kbToJSON(ctx, kb))
}

func (h *KnowledgeBaseHandler) Get(c *gin.Context) {
	_, owner, ok := h.caller(c)
	if !ok {
		return
	}
	id, ok := kbIDParam(c, "id")
	if !ok {
		return
	}
	kb, err := h.kb.GetKB(c.Request.Context(), owner, id)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	c.JSON(http.StatusOK, h.kbToJSON(c.Request.Context(), kb))
}

type updateKBBody struct {
	Name        *string          `json:"name"`
	Description *string          `json:"description"`
	Status      *string          `json:"status"`
	Bindings    *[]kbBindingBody `json:"bindings"`
}

func (h *KnowledgeBaseHandler) Update(c *gin.Context) {
	_, owner, ok := h.caller(c)
	if !ok {
		return
	}
	id, ok := kbIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	kb, err := h.kb.GetKB(ctx, owner, id) // ownership gate
	if err != nil {
		h.mapErr(c, err)
		return
	}
	var b updateKBBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if b.Name != nil {
		if _, err := h.db.ExecContext(ctx, `UPDATE knowledge_bases SET name = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, *b.Name, id); err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
	}
	if b.Description != nil {
		_, _ = h.db.ExecContext(ctx, `UPDATE knowledge_bases SET description = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, *b.Description, id)
	}
	if b.Status != nil && (*b.Status == "enabled" || *b.Status == "disabled") {
		_, _ = h.db.ExecContext(ctx, `UPDATE knowledge_bases SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, *b.Status, id)
	}
	if b.Bindings != nil {
		_ = h.replaceBindings(ctx, id, *b.Bindings)
	}
	kb, _ = h.kb.GetKB(ctx, owner, id)
	c.JSON(http.StatusOK, h.kbToJSON(ctx, kb))
}

func (h *KnowledgeBaseHandler) Delete(c *gin.Context) {
	_, owner, ok := h.caller(c)
	if !ok {
		return
	}
	id, ok := kbIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.kb.DeleteKB(c.Request.Context(), owner, id); err != nil {
		h.mapErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

type retryKBBody struct {
	MirrorURL string `json:"mirror_url"`
}

func (h *KnowledgeBaseHandler) Retry(c *gin.Context) {
	_, owner, ok := h.caller(c)
	if !ok {
		return
	}
	id, ok := kbIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	kb, err := h.kb.GetKB(ctx, owner, id)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	var b retryKBBody
	_ = c.ShouldBindJSON(&b)
	if b.MirrorURL != "" {
		_, _ = h.db.ExecContext(ctx, `UPDATE knowledge_bases SET source_addr = ? WHERE id = ?`, b.MirrorURL, id)
		kb.SourceAddr = b.MirrorURL
	}
	h.startIngest(ctx, owner, id, kb.SourceKind)
	kb, _ = h.kb.GetKB(ctx, owner, id)
	c.JSON(http.StatusOK, h.kbToJSON(ctx, kb))
}

const kbMaxUploadBytes = 8 << 20

func (h *KnowledgeBaseHandler) Upload(c *gin.Context) {
	_, owner, ok := h.caller(c)
	if !ok {
		return
	}
	id, ok := kbIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	kb, err := h.kb.GetKB(ctx, owner, id)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	form, err := c.MultipartForm()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	headers := form.File["files"]
	if len(headers) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no files"})
		return
	}
	dir := kb.SourceAddr
	if dir == "" {
		dir = owner.DatasetsPath(h.dataDir, id)
		_, _ = h.db.ExecContext(ctx, `UPDATE knowledge_bases SET source_addr = ? WHERE id = ?`, dir, id)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for _, fh := range headers {
		f, err := fh.Open()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		data, rerr := io.ReadAll(io.LimitReader(f, kbMaxUploadBytes+1))
		f.Close()
		if rerr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": rerr.Error()})
			return
		}
		// Reject (don't silently truncate) a file over the cap: the +1 sentinel
		// means we read one byte past the limit only to detect the overflow.
		if int64(len(data)) > kbMaxUploadBytes {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file exceeds max upload size"})
			return
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(fh.Filename)), data, 0o644); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	h.startIngest(ctx, owner, id, kb.SourceKind)
	kb, _ = h.kb.GetKB(ctx, owner, id)
	c.JSON(http.StatusOK, h.kbToJSON(ctx, kb))
}

// ---- browse + search -------------------------------------------------------

func (h *KnowledgeBaseHandler) ListDocuments(c *gin.Context) {
	_, owner, ok := h.caller(c)
	if !ok {
		return
	}
	id, ok := kbIDParam(c, "id")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if _, err := h.kb.GetKB(ctx, owner, id); err != nil {
		h.mapErr(c, err)
		return
	}
	rows, err := h.db.QueryContext(ctx,
		`SELECT id, kb_id, rel_path, byte_size, chunk_count FROM kb_documents WHERE kb_id = ? ORDER BY rel_path`, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := []gin.H{}
	for rows.Next() {
		var did, kbid, size, chunks int64
		var rel string
		if rows.Scan(&did, &kbid, &rel, &size, &chunks) == nil {
			items = append(items, gin.H{
				"id": did, "kb_id": kbid, "path": rel,
				"title": filepath.Base(rel), "size": size, "chunk_count": chunks,
			})
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *KnowledgeBaseHandler) Search(c *gin.Context) {
	_, owner, ok := h.caller(c)
	if !ok {
		return
	}
	id, ok := kbIDParam(c, "id")
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	if limit <= 0 {
		limit = 50
	}
	hits, err := h.kb.Search(c.Request.Context(), owner, id, c.Query("q"), limit)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	items := make([]gin.H, len(hits))
	for i, hit := range hits {
		items[i] = gin.H{
			"document_id":   hit.DocumentID,
			"document_path": hit.RelPath,
			"chunk_index":   hit.ChunkIndex,
			"snippet":       hit.Snippet,
			"score":         hit.Score,
		}
	}
	c.JSON(http.StatusOK, gin.H{"hits": items})
}

// kbPreset is the wire shape of a built-in network-dataset shortcut (#500).
type kbPreset struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	URLs        []string `json:"urls"`
	License     string   `json:"license,omitempty"`
	Homepage    string   `json:"homepage,omitempty"`
}

var builtinKBPresets = []kbPreset{
	{
		ID:          "chinese-poetry",
		Name:        "chinese-poetry 中文古诗词",
		Description: "最全中文诗歌古典文集数据库（MIT，纯 JSON，约 38 万首）。",
		// Ordered fallback (按序兜底): GitHub archive first, then domestic mirrors.
		// The gitee .git clone is the most reliable path inside China when the
		// GitHub/archive endpoints are blocked or throttled.
		URLs: []string{
			"https://github.com/chinese-poetry/chinese-poetry/archive/refs/heads/master.tar.gz",
			"https://gitee.com/mirrors/chinese-poetry/repository/archive/master.tar.gz",
			"https://gitee.com/mirrors/chinese-poetry.git",
		},
		License:  "MIT",
		Homepage: "https://github.com/chinese-poetry/chinese-poetry",
	},
}

func (h *KnowledgeBaseHandler) Presets(c *gin.Context) {
	if _, _, ok := h.caller(c); !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": builtinKBPresets})
}

// ---- bindings --------------------------------------------------------------

// replaceBindings replaces a KB's visibility bindings wholesale (project only).
func (h *KnowledgeBaseHandler) replaceBindings(ctx context.Context, id int64, bindings []kbBindingBody) error {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM kb_bindings WHERE kb_id = ?`, id); err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, b := range bindings {
		if b.TargetType != "project" || seen[b.TargetID] {
			continue
		}
		seen[b.TargetID] = true
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO kb_bindings (kb_id, target_type, target_id) VALUES (?, 'project', ?)`, id, b.TargetID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (h *KnowledgeBaseHandler) ListProjectKBs(c *gin.Context) {
	uid, _, ok := h.caller(c)
	if !ok {
		return
	}
	pid, ok := kbIDParam(c, "id")
	if !ok {
		return
	}
	if _, err := h.Authz.CanAccessProject(c.Request.Context(), uid, pid); err != nil {
		writeAuthzError(c, err)
		return
	}
	rows, err := h.db.QueryContext(c.Request.Context(),
		`SELECT kb.id, kb.name, kb.source_kind, kb.status
		   FROM kb_bindings b JOIN knowledge_bases kb ON kb.id = b.kb_id
		  WHERE b.target_type = 'project' AND b.target_id = ? ORDER BY kb.name`, pid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()
	items := []gin.H{}
	for rows.Next() {
		var kid int64
		var name, kind, status string
		if rows.Scan(&kid, &name, &kind, &status) == nil {
			items = append(items, gin.H{"id": kid, "name": name, "source_kind": kind, "status": status})
		}
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

type projectKBBody struct {
	KBID int64 `json:"kb_id" binding:"required"`
}

func (h *KnowledgeBaseHandler) AddProjectKB(c *gin.Context) {
	uid, owner, ok := h.caller(c)
	if !ok {
		return
	}
	pid, ok := kbIDParam(c, "id")
	if !ok {
		return
	}
	pOwner, err := h.Authz.CanAccessProject(c.Request.Context(), uid, pid)
	if err != nil {
		writeAuthzError(c, err)
		return
	}
	if err := h.Authz.EnsureOwnerWritable(c.Request.Context(), uid, pOwner); err != nil {
		writeAuthzError(c, err)
		return
	}
	var b projectKBBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Ownership gate on the KB.
	if _, err := h.kb.GetKB(c.Request.Context(), owner, b.KBID); err != nil {
		h.mapErr(c, err)
		return
	}
	if _, err := h.db.ExecContext(c.Request.Context(),
		`INSERT INTO kb_bindings (kb_id, target_type, target_id) VALUES (?, 'project', ?)
		 ON CONFLICT(kb_id, target_type, target_id) DO NOTHING`, b.KBID, pid); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusCreated)
}

func (h *KnowledgeBaseHandler) RemoveProjectKB(c *gin.Context) {
	uid, _, ok := h.caller(c)
	if !ok {
		return
	}
	pid, ok := kbIDParam(c, "id")
	if !ok {
		return
	}
	pOwner, err := h.Authz.CanAccessProject(c.Request.Context(), uid, pid)
	if err != nil {
		writeAuthzError(c, err)
		return
	}
	if err := h.Authz.EnsureOwnerWritable(c.Request.Context(), uid, pOwner); err != nil {
		writeAuthzError(c, err)
		return
	}
	kbid, ok := kbIDParam(c, "kbid")
	if !ok {
		return
	}
	if _, err := h.db.ExecContext(c.Request.Context(),
		`DELETE FROM kb_bindings WHERE kb_id = ? AND target_type = 'project' AND target_id = ?`, kbid, pid); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}
