package main

// Workspace artifact paths — the frozen product-tree contract (design §4.3 /
// plan §1.5):
//
//	<ws>/video-project/
//	  storyboard.json          # 唯一生成事实源（媒体合成硬前置：review_status=approved）
//	  assets/                  # TTS 音频、参考图、生成图等素材
//	  shots/                   # 逐镜 mp4 与视频任务记录（<group_id>.task.json）
//	  quotes/                  # 报价单 <quote_id>.json 与调用留痕 <quote_id>.calls.json
//	  qc/                      # 质量检查记录（G5 技术 QC）
//	  output/                  # final.mp4 / 字幕 ass / concat 清单 / 滤镜脚本
//
// All tool-generated files land here; nothing is written outside the workspace.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const projectDirName = "video-project"

// projectDir returns <ws>/video-project.
func projectDir(wsDir string) string { return filepath.Join(wsDir, projectDirName) }

// projectPath joins parts onto <ws>/video-project.
func projectPath(wsDir string, parts ...string) string {
	all := append([]string{wsDir, projectDirName}, parts...)
	return filepath.Join(all...)
}

// ensureDirs creates the standard sub-directories (idempotent).
func ensureDirs(wsDir string) error {
	for _, sub := range []string{"", "assets", "shots", "quotes", "qc", "output"} {
		if err := os.MkdirAll(projectPath(wsDir, sub), 0o755); err != nil {
			return fmt.Errorf("创建产物目录 %s 失败: %w", projectPath(wsDir, sub), err)
		}
	}
	return nil
}

// withinDir reports whether p lies inside dir (both cleaned; Windows
// separators/case handled by filepath.Rel).
func withinDir(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// resolveWSAsset turns an artifact reference into an absolute path **inside the
// workspace**. References may be absolute, workspace-relative
// ("video-project/assets/x.png" or "assets/x.png"), or project-relative.
//
// Boundary rule (security): storyboard fields and tool parameters are
// untrusted, so a reference that resolves outside wsDir — "../..", an absolute
// path elsewhere on disk, a "..\.." Windows variant — is rejected by returning
// "". Callers must treat "" as 路径越界 and fail with an explicit Chinese error
// instead of touching the filesystem. L1 assets may live anywhere inside the
// workspace (e.g. <ws>/raw/hero.mp4), so the boundary is wsDir, not the project
// dir. Containment is lexical: a symlink *created inside* the workspace can
// still point outside (the module creates none, and the workspace is the
// agent's own sandbox).
//
// Existence is checked by the caller so the error message can name the calling
// parameter.
func resolveWSAsset(wsDir, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	var candidate string
	if filepath.IsAbs(ref) {
		candidate = filepath.Clean(ref)
	} else {
		clean := filepath.Clean(filepath.FromSlash(ref))
		if strings.HasPrefix(clean, projectDirName+string(filepath.Separator)) {
			candidate = filepath.Join(wsDir, clean)
		} else {
			candidate = projectPath(wsDir, clean)
		}
	}
	if !withinDir(filepath.Clean(wsDir), candidate) {
		return ""
	}
	return candidate
}

// resolveCheckedAsset resolves a reference and reports a boundary violation
// with an actionable Chinese message (label names the tool parameter, e.g.
// "reference_image"). An empty reference yields ("", nil) — callers decide
// whether it is optional.
func resolveCheckedAsset(wsDir, ref, label string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", nil
	}
	p := resolveWSAsset(wsDir, ref)
	if p == "" {
		return "", fmt.Errorf("%s 路径越界：%q 解析后不在工作空间 %s 之内，已拒绝（素材必须放在工作空间内，路径不能用 ../ 逃逸）",
			label, ref, wsDir)
	}
	return p, nil
}

// outsideWSErr renders the boundary rejection for the storyboard-driven paths
// (stage-1 assets, BGM, output).
func outsideWSErr(label, ref string) error {
	return fmt.Errorf("%s 路径越界：%q 不在工作空间之内，已拒绝（路径不能用 ../ 逃逸）", label, ref)
}

// relToWS renders a path relative to the workspace (forward slashes) for tool
// output; absolute paths outside the workspace are returned as-is.
func relToWS(wsDir, abs string) string {
	rel, err := filepath.Rel(wsDir, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return abs
	}
	return filepath.ToSlash(rel)
}

// sanitizeShotID makes a shot id safe for a file name. Numeric ids become
// their decimal form; anything else has path-hostile characters replaced.
func sanitizeShotID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "shot"
	}
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "shot"
	}
	return out
}

// nextAssetIndex scans dir for files matching "<prefix><n><suffix...>" and
// returns max(n)+1 (1 when none exist). Used for the tts-<n>.mp3 and
// img-<n>-<k>.png naming schemes.
func nextAssetIndex(dir, prefix string) int {
	max := 0
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 1
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := name[len(prefix):]
		digits := rest
		if i := strings.IndexAny(rest, "-."); i >= 0 {
			digits = rest[:i]
		}
		if n, err := strconv.Atoi(digits); err == nil && n > max {
			max = n
		}
	}
	return max + 1
}
