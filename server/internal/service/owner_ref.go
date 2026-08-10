package service

import (
	"fmt"
	"path/filepath"
	"strconv"
)

// OwnerRef identifies who owns a top-level resource.
// Type is "user" (personal space) or "org" (organization).
type OwnerRef struct {
	Type string
	ID   int64
}

func (o OwnerRef) Validate() error {
	if o.Type != "user" && o.Type != "org" {
		return fmt.Errorf("invalid owner type %q", o.Type)
	}
	if o.ID <= 0 {
		return fmt.Errorf("invalid owner id %d", o.ID)
	}
	return nil
}

func (o OwnerRef) Root(dataDir string) string {
	dir := "users"
	if o.Type == "org" {
		dir = "orgs"
	}
	return filepath.Join(dataDir, dir, strconv.FormatInt(o.ID, 10))
}

func (o OwnerRef) WorkspacesDir(dataDir string) string {
	return filepath.Join(o.Root(dataDir), "workspaces")
}

func (o OwnerRef) RepositoriesDir(dataDir string) string {
	return filepath.Join(o.Root(dataDir), "repositories")
}

func (o OwnerRef) WorkspacePath(dataDir string, workspaceID int64) string {
	return filepath.Join(o.WorkspacesDir(dataDir), strconv.FormatInt(workspaceID, 10))
}

func (o OwnerRef) RepositoryPath(dataDir string, repositoryID int64) string {
	return filepath.Join(o.RepositoriesDir(dataDir), strconv.FormatInt(repositoryID, 10))
}

// DatasetsDir is where a KB's downloaded/uploaded source files land
// (~/.niuniu/{owner}/datasets). url-kind sources (Wave2 #500) materialize here.
func (o OwnerRef) DatasetsDir(dataDir string) string {
	return filepath.Join(o.Root(dataDir), "datasets")
}

// DatasetsPath resolves one knowledge base's content directory under DatasetsDir.
func (o OwnerRef) DatasetsPath(dataDir string, kbID int64) string {
	return filepath.Join(o.DatasetsDir(dataDir), strconv.FormatInt(kbID, 10))
}

// KBIndexPath is the per-owner SQLite KB full-text index sidecar
// (~/.niuniu/{owner}/kb_index.db). It is a SQLite-only component independent of
// the main store; see internal/kbindex.
func (o OwnerRef) KBIndexPath(dataDir string) string {
	return filepath.Join(o.Root(dataDir), "kb_index.db")
}
