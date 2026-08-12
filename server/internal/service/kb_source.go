package service

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// KB source kinds. These mirror the knowledge_bases.source_kind CHECK set.
const (
	KBSourceLocal = "local" // a directory (or uploaded files) already on disk
	KBSourceURL   = "url"   // a network address; async download lands in datasets (#500)
	KBSourceRepo  = "repo"  // reserved; not implemented this wave
)

// kbTextExts is the allow-list of extensions ingested as plain UTF-8 text. The
// KB base reads text/markdown directly; richer extraction (PDF/Office) is out of
// scope for the foundation and can be layered on later.
var kbTextExts = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".mdx": true,
	".rst": true, ".text": true, ".csv": true, ".tsv": true,
	".log": true, ".json": true, ".yaml": true, ".yml": true,
	".html": true, ".htm": true,
}

// resolveSourceRoot turns a KB's configured source into a local directory whose
// files can be walked and ingested. This is the pluggable-source entry point:
//   - local: the directory is kb.source_addr, read in place.
//   - url:   files land under the owner's datasets dir via DownloadURLSource
//     (#500); this returns that directory (optionally narrowed to a configured
//     subdir for a curated subset).
//   - repo:  reserved; not implemented this wave.
func (s *KBService) resolveSourceRoot(owner OwnerRef, kb store.KnowledgeBase) (string, error) {
	switch kb.SourceKind {
	case KBSourceLocal:
		root := strings.TrimSpace(kb.SourceAddr)
		if root == "" {
			return "", fmt.Errorf("kb %d: local source has empty address", kb.ID)
		}
		// Security gate (choke point for every ingest read): on the hosted edition
		// a local source may only read inside the owner's datasets dir (where the
		// upload path writes), never an arbitrary server path. Runs before Stat so
		// a disallowed path isn't even probed.
		if err := s.ensureLocalSourceAllowed(owner, root); err != nil {
			return "", fmt.Errorf("kb %d: %w", kb.ID, err)
		}
		info, err := os.Stat(root)
		if err != nil {
			return "", fmt.Errorf("kb %d: source path: %w", kb.ID, err)
		}
		if !info.IsDir() {
			return filepath.Dir(root), nil // single-file source: walk its directory
		}
		return root, nil
	case KBSourceURL:
		// Landing location for files fetched by DownloadURLSource (#500). Same
		// per-owner dataset dir the upload path uses, so resolveSourceRoot, the
		// downloader, and Upload all agree on datasets/<kbID>.
		root := owner.DatasetsPath(s.dataDir, kb.ID)
		// If source_config pins a subdir (a curated subset, see #500), ingest only
		// that subtree of the downloaded corpus; empty means the full corpus.
		if sub := kbSourceSubdir(kb.SourceConfig); sub != "" {
			root = filepath.Join(root, sub)
		}
		if _, err := os.Stat(root); err != nil {
			return "", fmt.Errorf("kb %d: url source not yet downloaded: %w", kb.ID, err)
		}
		return root, nil
	case KBSourceRepo:
		return "", fmt.Errorf("kb %d: source kind 'repo' is reserved and not implemented", kb.ID)
	default:
		return "", fmt.Errorf("kb %d: unknown source kind %q", kb.ID, kb.SourceKind)
	}
}

// ensureLocalSourceAllowed enforces the source_kind=local read policy for owner.
// When local sources are enabled (personal edition) any readable path is fine.
// When disabled (hosted edition) the path must resolve inside the owner's
// datasets dir — the only place the upload flow writes — so a tenant cannot
// point a KB at an arbitrary server file (/etc, another tenant's datasets).
// Callers: resolveSourceRoot (the ingest choke point) and the Create handler
// (fail-fast before persisting the KB row).
func (s *KBService) ensureLocalSourceAllowed(owner OwnerRef, addr string) error {
	if kbAllowLocalSources {
		return nil
	}
	root := owner.DatasetsDir(s.dataDir)
	// pathWithin (directory.go) resolves symlinks and is Windows case-aware;
	// arg order is (path, base).
	if !pathWithin(addr, root) {
		return fmt.Errorf("local source path is outside the owner datasets dir (arbitrary local reads are disabled on this edition)")
	}
	return nil
}

// EnsureLocalSourceAllowed is the exported gate the REST layer calls before
// creating a source_kind=local KB, mirroring the check resolveSourceRoot applies
// at ingest time so a disallowed path is rejected up front.
func (s *KBService) EnsureLocalSourceAllowed(owner OwnerRef, addr string) error {
	return s.ensureLocalSourceAllowed(owner, strings.TrimSpace(addr))
}

// kbFile is one ingestible file discovered under a source root.
type kbFile struct {
	abs string // absolute path on disk
	rel string // forward-slash path relative to root (stable across platforms)
}

// copyDirContent recursively copies every entry under src into dst (a fresh
// materialized dataset dir), skipping hidden entries (dotfiles, .git) the same
// way gatherTextFiles does. The destination is a read-only workspace mount; the
// caller enforces read-only at the agent boundary. Best-effort per file: an
// unreadable entry is skipped, not fatal, so a single broken file never aborts
// a mount.
func copyDirContent(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate unreadable entries; skip them
		}
		name := d.Name()
		if p != src && strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// gatherTextFiles walks root and returns text files (by extension), skipping
// hidden entries (dotfiles, .git, etc.). rel paths use forward slashes.
func gatherTextFiles(root string) ([]kbFile, error) {
	var out []kbFile
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate unreadable entries; skip them
		}
		name := d.Name()
		if p != root && strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !kbTextExts[strings.ToLower(filepath.Ext(name))] {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		out = append(out, kbFile{abs: p, rel: filepath.ToSlash(rel)})
		return nil
	})
	return out, err
}
