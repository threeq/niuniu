package api

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/imageopt"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// AttachmentHandler handles file attachment upload and deletion for workspaces.
type AttachmentHandler struct {
	queries      *store.Queries
	workspaceSvc *service.WorkspaceService
	Authz        *service.Authz

	optEnabled bool
	optOptions imageopt.Options
}

// NewAttachmentHandler creates a new AttachmentHandler.
func NewAttachmentHandler(queries *store.Queries, workspaceSvc *service.WorkspaceService) *AttachmentHandler {
	return &AttachmentHandler{
		queries:      queries,
		workspaceSvc: workspaceSvc,
		optEnabled:   false, // default off until SetImageOpts is called
		optOptions:   imageopt.DefaultOptions(),
	}
}

// SetImageOpts wires server-side image optimization. Called by the DI
// layer at startup based on cfg.ImageOptimization. Pass enabled=false to
// disable optimization entirely.
func (h *AttachmentHandler) SetImageOpts(enabled bool, opts imageopt.Options) {
	h.optEnabled = enabled
	h.optOptions = opts
}

// Windows reserved device names that cannot be used as filenames.
var windowsReservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// sanitizeFilename returns a safe filename: basename only, spaces replaced with underscores.
// Rejects Windows reserved device names.
func sanitizeFilename(name string) string {
	base := filepath.Base(name)
	base = strings.ReplaceAll(base, " ", "_")

	// Check Windows reserved names (without extension)
	nameWithoutExt := strings.TrimSuffix(base, filepath.Ext(base))
	if windowsReservedNames[strings.ToUpper(nameWithoutExt)] {
		base = "_" + base
	}

	return base
}

// Upload handles POST /workspaces/:id/attachments.
//
// Accepts a multipart form file under the "file" key, saves it to
// <workspace_path>/.attachments/, optionally runs imageopt.Optimize on
// it, and returns metadata.
//
// Response always includes the three image-optimization fields:
//   - "optimized" (bool): true iff imageopt actually shrunk the file
//   - "originalSize" (int): pre-optimization bytes; equals "size" when
//     optimization was disabled or skipped (already_small, unsupported, …)
//   - "optimizationSkipReason" (string): empty when optimized=true OR
//     when optimization is disabled; non-empty when imageopt attempted
//     but skipped (e.g. "already_small", "animated", "decode_failed")
//
// Image-optimization is fail-soft: any imageopt error falls through to
// the original file untouched.
func (h *AttachmentHandler) Upload(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), workspaceID); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	// Enforce 10 MB limit before parsing the body.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 10<<20)

	ctx := c.Request.Context()
	ws, err := h.queries.GetWorkspace(ctx, workspaceID)
	if err != nil {
		InternalError(c, fmt.Errorf("workspace not found: %w", err))
		return
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		BadRequest(c, "file is required")
		return
	}
	defer file.Close()

	attachmentsDir := filepath.Join(ws.Path, ".attachments")
	if err := os.MkdirAll(attachmentsDir, 0755); err != nil {
		InternalError(c, fmt.Errorf("failed to create attachments directory: %w", err))
		return
	}

	filename := sanitizeFilename(header.Filename)
	destPath := filepath.Join(attachmentsDir, filename)

	// Handle filename collision by appending a timestamp suffix.
	if _, err := os.Stat(destPath); err == nil {
		ext := filepath.Ext(filename)
		base := strings.TrimSuffix(filename, ext)
		ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
		filename = base + "_" + ts + ext
		destPath = filepath.Join(attachmentsDir, filename)
	}

	dst, err := os.Create(destPath)
	if err != nil {
		InternalError(c, fmt.Errorf("failed to create file: %w", err))
		return
	}
	defer dst.Close()

	size, err := io.Copy(dst, file)
	if err != nil {
		InternalError(c, fmt.Errorf("failed to write file: %w", err))
		return
	}

	// Close the file before optimizing — imageopt opens it for read.
	if err := dst.Close(); err != nil {
		InternalError(c, fmt.Errorf("failed to close file: %w", err))
		return
	}

	// Image optimization (optional, fail-soft). Skipped entirely when disabled.
	optRes := imageopt.Result{OrigSize: size, NewSize: size}
	if h.optEnabled {
		optRes, _ = imageopt.Optimize(c.Request.Context(), destPath, h.optOptions)
		if optRes.Optimized {
			filename = optRes.NewName
			destPath = optRes.NewPath
			size = optRes.NewSize
		}
	}

	// MIME prefers final extension (optimization may have changed it).
	// Falls back to client Content-Type for non-image uploads (.md/.txt/.log etc.)
	// so we don't regress to application/octet-stream for those.
	// (Review fix I2.)
	mimeType := mime.TypeByExtension(filepath.Ext(filename))
	if mimeType == "" {
		mimeType = header.Header.Get("Content-Type")
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	c.JSON(http.StatusOK, gin.H{
		"name":                   filename,
		"path":                   ".attachments/" + filename,
		"size":                   size,
		"mimeType":               mimeType,
		"originalSize":           optRes.OrigSize,
		"optimized":              optRes.Optimized,
		"optimizationSkipReason": string(optRes.SkipReason),
	})
}

// Delete handles DELETE /workspaces/:id/attachments/:name
// Removes the named attachment from <workspace_path>/.attachments/.
func (h *AttachmentHandler) Delete(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), workspaceID); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	// Use filepath.Base to prevent path traversal (including Windows backslash bypass).
	name := filepath.Base(c.Param("name"))
	if name == "" || name == "." || name == string(filepath.Separator) {
		BadRequest(c, "invalid attachment name")
		return
	}

	ctx := c.Request.Context()
	ws, err := h.queries.GetWorkspace(ctx, workspaceID)
	if err != nil {
		InternalError(c, fmt.Errorf("workspace not found: %w", err))
		return
	}

	filePath := filepath.Join(ws.Path, ".attachments", name)

	if err := os.Remove(filePath); err != nil {
		if os.IsNotExist(err) {
			c.Status(http.StatusNotFound)
			return
		}
		InternalError(c, fmt.Errorf("failed to delete attachment: %w", err))
		return
	}

	c.Status(http.StatusNoContent)
}

// FileContent handles GET /workspaces/:id/file-content?path=...&mode=raw|preview
func (h *AttachmentHandler) FileContent(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	relPath := c.Query("path")
	if relPath == "" {
		BadRequest(c, "path parameter is required")
		return
	}

	// Security: prevent path traversal
	cleaned := filepath.Clean(relPath)
	if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) {
		BadRequest(c, "invalid path")
		return
	}

	ctx := c.Request.Context()
	ws, err := h.queries.GetWorkspace(ctx, workspaceID)
	if err != nil {
		InternalError(c, fmt.Errorf("workspace not found: %w", err))
		return
	}

	fullPath := filepath.Join(ws.Path, cleaned)

	// Resolve symlinks and verify the file is still within the workspace directory
	resolved, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	wsResolved, _ := filepath.EvalSymlinks(ws.Path)
	if !strings.HasPrefix(resolved, wsResolved+string(filepath.Separator)) && resolved != wsResolved {
		BadRequest(c, "invalid path")
		return
	}

	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() {
		c.Status(http.StatusNotFound)
		return
	}
	fullPath = resolved

	mode := c.Query("mode")

	if mode == "raw" {
		// Serve raw file (for images in <img> tags)
		contentType := detectContentType(fullPath)
		c.Header("Content-Type", contentType)
		c.Header("Cache-Control", "private, max-age=3600")
		// Agent/user-authored HTML must never execute on the app origin — a page
		// loaded there (e.g. by navigating directly to this URL) could read the
		// SPA's stored token. Sandbox it to a unique opaque origin (scripts may
		// run for preview fidelity but can't reach our origin's storage/APIs) and
		// disable content-type sniffing. The in-app preview iframe already
		// sandboxes; this also covers direct navigation.
		if ext := strings.ToLower(filepath.Ext(fullPath)); ext == ".html" || ext == ".htm" {
			c.Header("Content-Security-Policy", "sandbox allow-scripts")
			c.Header("X-Content-Type-Options", "nosniff")
		}
		c.File(fullPath)
		return
	}

	// Preview mode: return first N lines as JSON
	content, truncated, err := readPreview(fullPath, 20)
	if err != nil {
		InternalError(c, fmt.Errorf("failed to read file: %w", err))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"content":   content,
		"truncated": truncated,
		"size":      info.Size(),
	})
}

// AddArtifactRequest promotes a workspace file to a user-facing deliverable.
// Title is the user-readable name shown in the 产物预览面板 (defaults to the
// file's basename when omitted).
type AddArtifactRequest struct {
	Path  string `json:"path" binding:"required"`
	Title string `json:"title,omitempty"`
}

// AddArtifact handles POST /workspaces/:id/artifacts.
//
// It registers the given workspace file in the deliverable manifest
// (.niuniu/artifacts.json) so it shows up in the 产物预览面板 — the "提交为产物"
// action of the file detail dialog. The manifest is otherwise agent-maintained;
// this is the user-driven promotion path. Idempotent: re-submitting a file just
// refreshes its title.
func (h *AttachmentHandler) AddArtifact(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), workspaceID); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	var req AddArtifactRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, err.Error())
		return
	}

	// Security: reject path traversal / absolute paths before touching disk.
	cleaned := filepath.Clean(req.Path)
	if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) {
		BadRequest(c, "invalid path")
		return
	}

	ctx := c.Request.Context()
	ws, err := h.queries.GetWorkspace(ctx, workspaceID)
	if err != nil {
		InternalError(c, fmt.Errorf("workspace not found: %w", err))
		return
	}

	// Verify the file exists and is inside the workspace (same symlink-escape
	// guard as FileContent) — you can only register a real deliverable.
	fullPath := filepath.Join(ws.Path, cleaned)
	resolved, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	wsResolved, _ := filepath.EvalSymlinks(ws.Path)
	if !strings.HasPrefix(resolved, wsResolved+string(filepath.Separator)) && resolved != wsResolved {
		BadRequest(c, "invalid path")
		return
	}
	if info, err := os.Stat(resolved); err != nil || info.IsDir() {
		c.Status(http.StatusNotFound)
		return
	}

	// Store the caller-supplied relative path (forward-slash), matching how the
	// agent and the preview panel key entries — not the OS-cleaned form.
	relPath := filepath.ToSlash(cleaned)
	entries, err := service.AddArtifactToManifest(ws.Path, relPath, req.Title)
	if err != nil {
		InternalError(c, fmt.Errorf("failed to update artifact manifest: %w", err))
		return
	}

	c.JSON(http.StatusOK, gin.H{"artifacts": entries})
}

// RemoveArtifact handles DELETE /workspaces/:id/artifacts?path=...
//
// It unregisters the file from the deliverable manifest (.niuniu/artifacts.json)
// — the "移除" action of the 产物预览面板. Unlike AddArtifact it does NOT require
// the file to still exist on disk (a deleted deliverable can still be removed
// from the list). Idempotent: removing an unregistered path returns the current
// manifest unchanged.
func (h *AttachmentHandler) RemoveArtifact(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	workspaceID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid workspace ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, err := h.Authz.CanAccessWorkspace(c.Request.Context(), userID, workspaceID); err != nil {
			writeAuthzError(c, err)
			return
		}
	}

	if err := h.workspaceSvc.CheckNotArchived(c.Request.Context(), workspaceID); err != nil {
		if errors.Is(err, service.ErrWorkspaceArchived) {
			RespondErrorWithDetails(c, http.StatusForbidden, "WORKSPACE_ARCHIVED", "归档工作空间不允许此操作", nil)
			return
		}
		InternalError(c, err)
		return
	}

	relPath := c.Query("path")
	if relPath == "" {
		BadRequest(c, "path parameter is required")
		return
	}
	// Security: reject path traversal / absolute paths (the manifest only ever
	// holds workspace-relative paths).
	cleaned := filepath.Clean(relPath)
	if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) {
		BadRequest(c, "invalid path")
		return
	}

	ctx := c.Request.Context()
	ws, err := h.queries.GetWorkspace(ctx, workspaceID)
	if err != nil {
		InternalError(c, fmt.Errorf("workspace not found: %w", err))
		return
	}

	entries, err := service.RemoveArtifactFromManifest(ws.Path, filepath.ToSlash(cleaned))
	if err != nil {
		InternalError(c, fmt.Errorf("failed to update artifact manifest: %w", err))
		return
	}

	c.JSON(http.StatusOK, gin.H{"artifacts": entries})
}

func detectContentType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".bmp":
		return "image/bmp"
	case ".avif":
		return "image/avif"
	case ".pdf":
		return "application/pdf"
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".ogv":
		return "video/ogg"
	case ".mov":
		return "video/quicktime"
	case ".m3u8":
		return "application/vnd.apple.mpegurl"
	case ".flv":
		return "video/x-flv"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".ogg", ".oga":
		return "audio/ogg"
	case ".m4a":
		return "audio/mp4"
	case ".aac":
		return "audio/aac"
	case ".flac":
		return "audio/flac"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".xls":
		return "application/vnd.ms-excel"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

func readPreview(path string, maxLines int) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) >= maxLines {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return "", false, err
	}
	// File is truncated if we hit the line limit and there's more data
	truncated := len(lines) >= maxLines && scanner.Scan()
	return strings.Join(lines, "\n"), truncated, nil
}
