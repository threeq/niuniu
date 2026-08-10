package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/fsmove"
	"github.com/niuniu-dev/niuniu/internal/git"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// allowedRemoteSchemes is the set of URL prefixes git itself supports.
var allowedRemoteSchemes = []string{
	"https://", "http://", "git://", "ssh://", "ftp://", "ftps://", "file://",
}

// validateRemoteURL returns an error if u is not a recognised git remote URL.
func validateRemoteURL(u string) error {
	for _, scheme := range allowedRemoteSchemes {
		if strings.HasPrefix(u, scheme) {
			return nil
		}
	}
	// SCP-like syntax: git@host:path — must contain '@' and ':' with colon not at index 1
	if strings.Contains(u, "@") {
		if idx := strings.Index(u, ":"); idx > 1 {
			return nil
		}
	}
	return fmt.Errorf("unsupported URL scheme — accepted: https://, http://, git://, ssh://, ftp://, ftps://, file://, or git@ SCP syntax")
}

// repoNameFromURL derives a safe directory name from a remote URL.
// e.g. https://github.com/user/repo.git → "repo"
func repoNameFromURL(rawURL string) string {
	var segment string
	if strings.Contains(rawURL, "://") {
		// Standard URL: use net/url to parse correctly (handles ports, query strings)
		if u, err := url.Parse(rawURL); err == nil {
			segment = path.Base(u.Path)
		}
	}
	if segment == "" {
		// SCP syntax: git@host:path/to/repo.git — split on ':'
		if idx := strings.LastIndex(rawURL, ":"); idx >= 0 {
			segment = rawURL[idx+1:]
		}
		segment = path.Base(segment)
	}
	segment = strings.TrimRight(segment, "/")
	segment = strings.TrimSuffix(segment, ".git")
	if segment == "" || segment == "." || segment == ".." {
		return "repo"
	}
	return segment
}

// sanitizeRepoName strips path separators so a name can never escape ~/git-repos/.
func sanitizeRepoName(name string) string {
	name = filepath.Base(name) // strips any directory component
	if name == "" || name == "." || name == ".." {
		return "repo"
	}
	return name
}

type RepositoryService struct {
	q       *store.Queries
	db      *store.DB
	authz   *Authz
	dataDir string // root data directory (e.g. ~/.niuniu)
}

func NewRepositoryService(q *store.Queries, db *sql.DB, authz *Authz, dataDir string) *RepositoryService {
	return &RepositoryService{q: q, db: store.Wrap(db), authz: authz, dataDir: dataDir}
}

func (s *RepositoryService) List(ctx context.Context) ([]store.Repository, error) {
	return s.q.ListRepositories(ctx)
}

// ListForUser returns repositories accessible to userID (personal + org memberships).
func (s *RepositoryService) ListForUser(ctx context.Context, userID int64) ([]store.Repository, error) {
	owners, err := s.authz.Accessible(ctx, userID)
	if err != nil {
		return nil, err
	}
	orgIDs := owners.OrgIDs
	if len(orgIDs) == 0 {
		orgIDs = []int64{-1}
	}
	return s.q.ListRepositoriesForOwners(ctx, store.ListRepositoriesForOwnersParams{
		OwnerID: owners.UserID,
		OrgIds:  orgIDs,
	})
}

// parseRepoID converts a string repository ID to int64.
func (s *RepositoryService) parseRepoID(id string) (int64, error) {
	return strconv.ParseInt(id, 10, 64)
}

func (s *RepositoryService) Get(ctx context.Context, id string) (store.Repository, error) {
	repoID, err := s.parseRepoID(id)
	if err != nil {
		return store.Repository{}, err
	}
	return s.q.GetRepository(ctx, repoID)
}

type CreateRepositoryInput struct {
	Path      string
	Name      string
	AutoInit  bool   // If true, create directory and init git if not exists
	RemoteURL string // If set, clone this URL into ~/git-repos/<name> first
	OwnerType string
	OwnerID   int64
}

// cloneRepository clones remoteURL into a staging directory under dataDir/repositories/staging/<name>
// and returns the cloned path. The caller is responsible for moving it to the final OwnerRef path.
// ctx is forwarded to git so the process is killed if the request is cancelled.
func (s *RepositoryService) cloneRepository(ctx context.Context, remoteURL, name string) (string, error) {
	// Use a staging area under dataDir when available, falling back to ~/git-repos.
	var stagingBase string
	if s.dataDir != "" {
		stagingBase = filepath.Join(s.dataDir, "repositories", "staging")
	} else {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot determine home directory: %w", err)
		}
		stagingBase = filepath.Join(homeDir, "git-repos")
	}
	if err := os.MkdirAll(stagingBase, 0755); err != nil {
		return "", fmt.Errorf("cannot create staging directory: %w", err)
	}

	destPath := filepath.Join(stagingBase, name)

	// Guard against path traversal: the resolved destination must stay inside stagingBase.
	separator := string(filepath.Separator)
	if !strings.HasPrefix(destPath+separator, stagingBase+separator) {
		return "", fmt.Errorf("invalid destination path")
	}

	// Fail early if destination already exists to give a clear error.
	if _, err := os.Stat(destPath); err == nil {
		return "", fmt.Errorf("destination already exists: %s", name)
	}

	cmd := exec.CommandContext(ctx, "git", "clone", remoteURL, destPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 500 {
			msg = "..." + msg[len(msg)-500:]
		}
		return "", fmt.Errorf("git clone failed: %s", msg)
	}
	return destPath, nil
}

// Create registers (and optionally clones / auto-inits) a repository. The
// returned []string carries non-fatal warnings surfaced to the user (currently
// only WarnGitLFSMissing when auto-init could not enable Git LFS); it is always
// safe to ignore.
func (s *RepositoryService) Create(ctx context.Context, input CreateRepositoryInput) (store.Repository, []string, error) {
	// Handle remote URL: validate → check name uniqueness → clone → register
	if input.RemoteURL != "" {
		if err := validateRemoteURL(input.RemoteURL); err != nil {
			return store.Repository{}, nil, fmt.Errorf("CLONE_FAILED: %s", err.Error())
		}

		name := sanitizeRepoName(input.Name)
		if name == "repo" && input.Name == "" {
			name = sanitizeRepoName(repoNameFromURL(input.RemoteURL))
		}

		// Check name uniqueness before cloning to avoid orphaned directories.
		existing, err := s.q.GetRepositoryByName(ctx, name)
		if err == nil && existing.Name == name {
			return store.Repository{}, nil, fmt.Errorf("REPO_NAME_EXISTS: repository with name '%s' already exists", name)
		}

		clonedPath, err := s.cloneRepository(ctx, input.RemoteURL, name)
		if err != nil {
			return store.Repository{}, nil, fmt.Errorf("CLONE_FAILED: %s", err.Error())
		}
		// Clean up the cloned directory if anything below fails.
		var createErr error
		defer func() {
			if createErr != nil {
				_ = os.RemoveAll(clonedPath)
			}
		}()

		input.Path = clonedPath
		input.Name = name
		input.AutoInit = false // already a proper git repo after clone

		repo, warnings, err := s.finishCreate(ctx, input)
		createErr = err
		return repo, warnings, err
	}

	// Local path: reject anything inside the niuniu data dir (~/.niuniu). The
	// directory picker enforces this client-side, but this is the server-side
	// backstop so a hand-crafted request can't register a repo inside niuniu's
	// own data tree. The remote-clone branch above stages under dataDir on
	// purpose, so this guard only covers the user-supplied local path.
	if input.Path != "" && pathWithin(input.Path, s.dataDir) {
		return store.Repository{}, nil, fmt.Errorf("PATH_FORBIDDEN: path is inside the niuniu data directory: %s", input.Path)
	}

	return s.finishCreate(ctx, input)
}

// FindByOwnerPath returns the repository owned by (ownerType, ownerID) whose
// on-disk path refers to the same directory as path, with ok=true when such a
// repo exists. It compares with os.SameFile so separator / case / symlink
// differences between the stored path and the caller-supplied path don't cause
// a false miss (Windows in particular hands us forward-slash, mixed-case
// paths). Returns ok=false with no error when path can't be stat'd or no owned
// repo matches — callers treat that as "not yet registered".
//
// Used by the studio "from local directory" flow so re-selecting an
// already-registered directory reuses the existing repository instead of
// failing with REPO_NAME_EXISTS.
func (s *RepositoryService) FindByOwnerPath(ctx context.Context, ownerType string, ownerID int64, path string) (store.Repository, bool, error) {
	if ownerType == "" {
		ownerType = "user"
	}
	target, err := os.Stat(path)
	if err != nil {
		return store.Repository{}, false, nil
	}
	repos, err := s.q.ListRepositories(ctx)
	if err != nil {
		return store.Repository{}, false, err
	}
	for _, repo := range repos {
		if repo.OwnerType != ownerType || repo.OwnerID != ownerID {
			continue
		}
		info, statErr := os.Stat(repo.Path)
		if statErr != nil {
			continue
		}
		if os.SameFile(target, info) {
			return repo, true, nil
		}
	}
	return store.Repository{}, false, nil
}

// WarnGitLFSMissing is appended to the Create warnings slice when an auto-init
// repo could not enable Git LFS (binary absent or `git lfs` failed). The
// frontend maps this code to a soft notice; large files are still committed,
// just not via LFS.
const WarnGitLFSMissing = "WARN_GIT_LFS_MISSING"

// defaultGitignoreBody is written to freshly auto-init'd repos so common OS
// cruft and heavy generated directories stay out of git from the first commit.
const defaultGitignoreBody = `# Created by niuniu on auto-init; edit freely.
# OS junk
.DS_Store
Thumbs.db
desktop.ini
*~

# Editors / tooling
.idea/
.vscode/

# Heavy generated directories
node_modules/
__pycache__/
*.log
`

// writeDefaultGitignore writes defaultGitignoreBody to dir/.gitignore. It never
// overwrites an existing .gitignore (idempotent / respects user intent).
func writeDefaultGitignore(dir string) error {
	p := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	return os.WriteFile(p, []byte(defaultGitignoreBody), 0o644)
}

func (s *RepositoryService) finishCreate(ctx context.Context, input CreateRepositoryInput) (store.Repository, []string, error) {
	var warnings []string
	// Step 1: Auto-detect name if not provided
	name := input.Name
	if name == "" {
		name = filepath.Base(input.Path)
	}

	// Step 2: Check name uniqueness
	existing, err := s.q.GetRepositoryByName(ctx, name)
	if err == nil && existing.Name == name {
		return store.Repository{}, warnings, fmt.Errorf("REPO_NAME_EXISTS: repository with name '%s' already exists", name)
	}

	// Step 3: Create directory if AutoInit is true and path doesn't exist.
	// dirCreated tracks whether we actually mkdir'd the path (vs it already
	// existed) — used later to decide whether the OwnerRef relocation should
	// kick in. We only want to relocate dirs we created ourselves; AutoInit on
	// a pre-existing user directory must not silently move user data.
	// INVARIANT: do not weaken createDirectory to "mkdir-or-stat-and-claim";
	// the relocation gate below relies on dirCreated meaning "we mkdir'd it
	// in this call". Breaking that lets us silently os.Rename pre-existing
	// user directories.
	var dirCreated bool
	if input.AutoInit {
		created, err := s.createDirectory(input.Path)
		if err != nil {
			return store.Repository{}, warnings, fmt.Errorf("PATH_CREATION_FAILED: %s", err.Error())
		}
		dirCreated = created
	}

	// Step 4: Validate path exists
	info, err := os.Stat(input.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return store.Repository{}, warnings, fmt.Errorf("PATH_DOES_NOT_EXIST: path does not exist: %s", input.Path)
		}
		return store.Repository{}, warnings, fmt.Errorf("PATH_ACCESS_ERROR: cannot access path: %s", err.Error())
	}
	if !info.IsDir() {
		return store.Repository{}, warnings, fmt.Errorf("NOT_A_DIRECTORY: path is not a directory: %s", input.Path)
	}

	// Step 5: Check if it's a git repository
	isGitRepo, err := s.isGitRepo(input.Path)
	if err != nil {
		return store.Repository{}, warnings, fmt.Errorf("GIT_CHECK_ERROR: %s", err.Error())
	}

	if !isGitRepo {
		if input.AutoInit {
			// Step 5b: pre-flight git identity check (commit will fail without it).
			identityName, identityEmail, idErr := git.GetGlobalIdentity(ctx)
			if idErr != nil {
				return store.Repository{}, warnings, fmt.Errorf("GIT_CHECK_ERROR: %s", idErr.Error())
			}
			if identityName == "" || identityEmail == "" {
				return store.Repository{}, warnings, fmt.Errorf("GIT_IDENTITY_MISSING: git user.name / user.email not configured")
			}
			// Step 6a: Auto init git repository
			if err := s.initGitRepo(input.Path); err != nil {
				return store.Repository{}, warnings, fmt.Errorf("GIT_INIT_FAILED: %s", err.Error())
			}
			// Step 6b: Write default .gitignore and enable Git LFS for large
			// media BEFORE the first commit, so .gitignore/.gitattributes land
			// in it (issue #233 — the one genuinely-new git step in P0). git-lfs
			// missing is non-fatal: warn + flag, never block the build.
			if err := writeDefaultGitignore(input.Path); err != nil {
				slog.Warn("repository.finishCreate: write default .gitignore failed",
					"path", input.Path, "error", err)
			}
			if git.LFSAvailable(ctx) {
				if err := git.EnableLFS(ctx, input.Path, git.MediaLFSPatterns); err != nil {
					slog.Warn("repository.finishCreate: enable Git LFS failed; large files not LFS-tracked",
						"path", input.Path, "error", err)
					warnings = append(warnings, WarnGitLFSMissing)
				}
			} else {
				slog.Warn("repository.finishCreate: git-lfs not installed; large files not LFS-tracked",
					"path", input.Path)
				warnings = append(warnings, WarnGitLFSMissing)
			}
			// Step 6c: Ensure first commit so HEAD exists.
			// Identity{} = fall through to git's global config — this code path
			// is only reached for first-time auto-init where the pre-flight
			// above already verified user.name/user.email are set globally.
			// Per-user attribution at commit time arrives in Phase 1.
			if err := git.EnsureInitialCommit(ctx, input.Path, git.Identity{}); err != nil {
				// Cleanup half-initialized state so the user can retry without
				// conflicts (spec §3.3: no half-initialized state on failure).
				_ = os.RemoveAll(filepath.Join(input.Path, ".git"))
				if dirCreated {
					_ = os.RemoveAll(input.Path)
				}
				return store.Repository{}, warnings, fmt.Errorf("GIT_INITIAL_COMMIT_FAILED: %s", err.Error())
			}
		} else {
			// Step 6d: Not a git repo and auto_init is false
			return store.Repository{}, warnings, fmt.Errorf("NOT_A_GIT_REPO: not a complete git repository (path: %s)", input.Path)
		}
	}

	// Auto-detect git remote
	gitRemote := s.detectGitRemote(input.Path)

	// Auto-detect default branch
	defaultBranch := s.detectDefaultBranch(input.Path)

	ownerType := input.OwnerType
	if ownerType == "" {
		ownerType = "user"
	}
	repo, err := s.q.CreateRepository(ctx, store.CreateRepositoryParams{
		Name:          name,
		Path:          input.Path,
		GitRemote:     toNullString(gitRemote),
		DefaultBranch: toNullString(defaultBranch),
		OwnerType:     ownerType,
		OwnerID:       input.OwnerID,
	})
	if err != nil {
		return store.Repository{}, warnings, fmt.Errorf("DB_WRITE_FAILED: %s", err.Error())
	}

	// If dataDir is set, relocate the directory to the OwnerRef-scoped path so
	// that disk layout mirrors owner hierarchy (e.g.
	// ~/.niuniu/orgs/<id>/repositories/<repoID>) and lives inside the
	// persistent volume on team-edition Docker deploys. Triggers when:
	//   - path is under our staging area (cloneRepository case), OR
	//   - we mkdir'd the path ourselves in step 3 (auto-init on a non-existing
	//     path the caller hinted at).
	// External "Add Path" registrations (AutoInit=false, OR AutoInit=true on a
	// dir that already existed) stay in place — those are user-owned locations
	// we must not silently move. Errors here are LOGGED, not swallowed: a
	// failed relocate leaves the repo at the original (possibly non-persistent)
	// path, and we want operators to see that in logs.
	if s.dataDir != "" {
		stagingBase := filepath.Join(s.dataDir, "repositories", "staging")
		separator := string(filepath.Separator)
		underStaging := strings.HasPrefix(repo.Path+separator, stagingBase+separator)
		if underStaging || dirCreated {
			owner := OwnerRef{Type: ownerType, ID: input.OwnerID}
			finalPath := owner.RepositoryPath(s.dataDir, repo.ID)
			// Compare cleaned paths so trailing-separator / "." / ".." noise
			// doesn't trick us into a no-op rename. Case-folding (HFS+/NTFS)
			// not handled — both sides are constructed by Go on this host so
			// case drift is unlikely; SameFile would be the hammer.
			if filepath.Clean(repo.Path) != filepath.Clean(finalPath) {
				if mkErr := os.MkdirAll(filepath.Dir(finalPath), 0755); mkErr != nil {
					slog.Warn("repository.finishCreate: mkdir parent of OwnerRef path failed; repo stays at original location",
						"repo_id", repo.ID, "from", repo.Path, "to", finalPath, "error", mkErr)
				} else if mvErr := fsmove.Rename(repo.Path, finalPath); mvErr != nil {
					// fsmove.Rename handles EXDEV (Docker bind-mount → overlay)
					// transparently via copy+remove. A failure here is genuinely
					// the disk's problem (perms, ENOSPC, partial copy), so log
					// loudly and leave the repo at its original location.
					slog.Warn("repository.finishCreate: rename to OwnerRef path failed; repo stays at original location",
						"repo_id", repo.ID, "from", repo.Path, "to", finalPath, "error", mvErr)
				} else if updated, err2 := s.q.UpdateRepository(ctx, store.UpdateRepositoryParams{
					ID:            repo.ID,
					Name:          repo.Name,
					Path:          finalPath,
					GitRemote:     repo.GitRemote,
					DefaultBranch: repo.DefaultBranch,
				}); err2 != nil {
					// Filesystem moved but DB still points at old path. Best
					// effort: rename back so DB and disk agree on the original
					// location. If that also fails, log loudly — operator needs
					// to manually reconcile.
					slog.Error("repository.finishCreate: DB update after rename failed; reverting filesystem move",
						"repo_id", repo.ID, "from", repo.Path, "to", finalPath, "error", err2)
					if revertErr := fsmove.Rename(finalPath, repo.Path); revertErr != nil {
						slog.Error("repository.finishCreate: revert rename failed; DB and disk diverged",
							"repo_id", repo.ID, "db_path", repo.Path, "disk_path", finalPath, "error", revertErr)
					}
				} else {
					repo = updated
				}
			}
		}
	}

	appendResourceAudit(ctx, s.db, nil, repo.OwnerType, repo.OwnerID, 0, "repository.created", "repository", repo.ID,
		resourceAuditPayload(repo.Name))
	return repo, warnings, nil
}

// createDirectory creates a directory (and parent dirs) at the given path.
// Returns (created, err) where created==true iff this call actually mkdir'd
// the path; created==false means the dir already existed. finishCreate uses
// this to decide whether OwnerRef relocation should kick in — we only want to
// move dirs we created ourselves, never pre-existing user data.
func (s *RepositoryService) createDirectory(p string) (bool, error) {
	if _, err := os.Stat(p); err == nil {
		return false, nil // Already exists; don't claim ownership.
	}
	if err := os.MkdirAll(p, 0755); err != nil {
		return false, fmt.Errorf("failed to create directory: %s", err.Error())
	}
	return true, nil
}

// isGitRepo checks if the path is a git repository
func (s *RepositoryService) isGitRepo(path string) (bool, error) {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		return false, nil // Not a git repo
	}
	return true, nil
}

// initGitRepo initializes a git repository at the given path
func (s *RepositoryService) initGitRepo(path string) error {
	cmd := exec.Command("git", "init")
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git init failed: %s", err.Error())
	}
	return nil
}

type UpdateRepositoryInput struct {
	Name          string
	Path          string
	GitRemote     string
	DefaultBranch string
}

func (s *RepositoryService) Update(ctx context.Context, id string, input UpdateRepositoryInput) (store.Repository, error) {
	repoID, err := s.parseRepoID(id)
	if err != nil {
		return store.Repository{}, err
	}
	// Validate path exists if provided
	if input.Path != "" {
		info, err := os.Stat(input.Path)
		if err != nil {
			if os.IsNotExist(err) {
				return store.Repository{}, fmt.Errorf("path does not exist: %s", input.Path)
			}
			return store.Repository{}, fmt.Errorf("cannot access path: %s", err.Error())
		}
		if !info.IsDir() {
			return store.Repository{}, fmt.Errorf("path is not a directory: %s", input.Path)
		}

		// Verify it's a git repository
		if err := s.validateGitRepo(input.Path); err != nil {
			return store.Repository{}, fmt.Errorf("not a valid git repository: %s", err.Error())
		}
	}

	repo, err := s.q.UpdateRepository(ctx, store.UpdateRepositoryParams{
		ID:            repoID,
		Name:          input.Name,
		Path:          input.Path,
		GitRemote:     toNullString(input.GitRemote),
		DefaultBranch: toNullString(input.DefaultBranch),
	})
	if err != nil {
		return repo, err
	}
	appendResourceAudit(ctx, s.db, nil, repo.OwnerType, repo.OwnerID, 0, "repository.updated", "repository", repoID,
		resourceAuditPayload(repo.Name))
	return repo, nil
}

func (s *RepositoryService) Delete(ctx context.Context, id string) error {
	repoID, err := s.parseRepoID(id)
	if err != nil {
		return err
	}
	// Load for audit before deletion.
	repo, repoErr := s.q.GetRepository(ctx, repoID)
	if err := s.q.DeleteRepository(ctx, repoID); err != nil {
		return err
	}
	if repoErr == nil {
		appendResourceAudit(ctx, s.db, nil, repo.OwnerType, repo.OwnerID, 0, "repository.deleted", "repository", repoID,
			resourceAuditPayload(repo.Name))
	}
	return nil
}

// DeleteDirectory removes the repository directory from disk
func (s *RepositoryService) DeleteDirectory(path string) error {
	return os.RemoveAll(path)
}

// BranchInfo contains the list of branches and the current branch.
type BranchInfo struct {
	Branches      []string `json:"branches"`
	CurrentBranch string   `json:"current_branch"`
}

// GetBranchInfo returns all branches and the current branch for a repository.
// Branches that are checked out in a worktree are excluded.
func (s *RepositoryService) GetBranchInfo(ctx context.Context, id string) (BranchInfo, error) {
	repoID, err := s.parseRepoID(id)
	if err != nil {
		return BranchInfo{}, err
	}
	repo, err := s.q.GetRepository(ctx, repoID)
	if err != nil {
		return BranchInfo{}, err
	}
	branches, err := git.ListBranches(repo.Path)
	if err != nil {
		return BranchInfo{}, err
	}
	current, _ := git.CurrentBranch(repo.Path)

	// Exclude branches checked out in worktrees
	worktrees, _ := git.ListWorktrees(repo.Path)
	if len(worktrees) > 1 { // >1 because main worktree is always listed
		wtBranches := make(map[string]bool)
		for _, wt := range worktrees {
			// git worktree list returns full ref like "refs/heads/branch-name"
			b := strings.TrimPrefix(wt.Branch, "refs/heads/")
			if !wt.IsCurrent { // keep the main worktree's branch visible
				wtBranches[b] = true
			}
		}
		filtered := make([]string, 0, len(branches))
		for _, b := range branches {
			if !wtBranches[b] {
				filtered = append(filtered, b)
			}
		}
		branches = filtered
	}

	return BranchInfo{
		Branches:      branches,
		CurrentBranch: current,
	}, nil
}

// Helper: validate that path is a git repository
func (s *RepositoryService) validateGitRepo(path string) error {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

// Helper: detect git remote URL
func (s *RepositoryService) detectGitRemote(path string) string {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = path
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(output[:len(output)-1]) // Remove trailing newline
}

// Helper: detect default branch
func (s *RepositoryService) detectDefaultBranch(path string) string {
	cmd := exec.Command("git", "symbolic-ref", "refs/remotes/origin/HEAD")
	cmd.Dir = path
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	// Output format: refs/remotes/origin/main
	// We want to extract the branch name
	result := string(output)
	if len(result) > 0 {
		// Remove trailing newline
		result = result[:len(result)-1]
		// Extract branch name after last "/"
		for i := len(result) - 1; i >= 0; i-- {
			if result[i] == '/' {
				return result[i+1:]
			}
		}
	}
	return ""
}

// RepositoryStats holds repository statistics.
type RepositoryStats struct {
	TotalCommits      int    `json:"total_commits"`
	TotalBranches     int    `json:"total_branches"`
	TotalContributors int    `json:"total_contributors"`
	LastCommitDate    string `json:"last_commit_date"`
}

// GetStats returns repository statistics.
func (s *RepositoryService) GetStats(ctx context.Context, id string) (RepositoryStats, error) {
	repoID, err := s.parseRepoID(id)
	if err != nil {
		return RepositoryStats{}, err
	}
	repo, err := s.q.GetRepository(ctx, repoID)
	if err != nil {
		return RepositoryStats{}, err
	}

	commitCount, _ := git.GetCommitCount(repo.Path)
	contributorCount, _ := git.GetContributorCount(repo.Path)
	lastCommitDate, _ := git.GetLastCommitDate(repo.Path)
	branches, _ := git.ListBranches(repo.Path)

	return RepositoryStats{
		TotalCommits:      commitCount,
		TotalBranches:     len(branches),
		TotalContributors: contributorCount,
		LastCommitDate:    lastCommitDate,
	}, nil
}

// ListFiles returns file entries in the repository at the given path.
func (s *RepositoryService) ListFiles(ctx context.Context, id string, path string) ([]git.FileEntry, error) {
	repoID, err := s.parseRepoID(id)
	if err != nil {
		return nil, err
	}
	repo, err := s.q.GetRepository(ctx, repoID)
	if err != nil {
		return nil, err
	}
	return git.ListFiles(repo.Path, repo.DefaultBranch.String, path)
}

// GetFileContent returns the content of a file at the given path.
func (s *RepositoryService) GetFileContent(ctx context.Context, id string, path string) (string, error) {
	repoID, err := s.parseRepoID(id)
	if err != nil {
		return "", err
	}
	repo, err := s.q.GetRepository(ctx, repoID)
	if err != nil {
		return "", err
	}
	return git.GetFileContent(repo.Path, repo.DefaultBranch.String, path)
}

// GetFileBytes returns the raw bytes of a file at the given path, read from the
// repository's default-branch tree. Backs the raw media preview endpoint.
func (s *RepositoryService) GetFileBytes(ctx context.Context, id string, path string) ([]byte, error) {
	repoID, err := s.parseRepoID(id)
	if err != nil {
		return nil, err
	}
	repo, err := s.q.GetRepository(ctx, repoID)
	if err != nil {
		return nil, err
	}
	return git.GetFileBytes(repo.Path, repo.DefaultBranch.String, path)
}

// ListCommits returns commit history with pagination.
func (s *RepositoryService) ListCommits(ctx context.Context, id string, page, limit int) ([]git.LogEntry, error) {
	repoID, err := s.parseRepoID(id)
	if err != nil {
		return nil, err
	}
	repo, err := s.q.GetRepository(ctx, repoID)
	if err != nil {
		return nil, err
	}
	return git.Log(repo.Path, page*limit)
}

// CreateBranch creates a new branch.
func (s *RepositoryService) CreateBranch(ctx context.Context, id, branchName string) error {
	repoID, err := s.parseRepoID(id)
	if err != nil {
		return err
	}
	repo, err := s.q.GetRepository(ctx, repoID)
	if err != nil {
		return err
	}
	return git.CreateBranch(repo.Path, branchName)
}

// DeleteBranch deletes a branch.
// If the branch is checked out in a worktree, removes the worktree first.
func (s *RepositoryService) DeleteBranch(ctx context.Context, id, branchName string) error {
	repoID, err := s.parseRepoID(id)
	if err != nil {
		return err
	}
	repo, err := s.q.GetRepository(ctx, repoID)
	if err != nil {
		return err
	}

	// Check if a worktree is using this branch and remove it first
	worktrees, _ := git.ListWorktrees(repo.Path)
	for _, wt := range worktrees {
		wtBranch := strings.TrimPrefix(wt.Branch, "refs/heads/")
		if wtBranch == branchName {
			_ = git.WorktreeRemove(repo.Path, wt.Path)
		}
	}

	// Also clean up DB worktree records for this branch
	dbWorktrees, _ := s.q.ListWorktreesByRepository(ctx, repoID)
	for _, dbWt := range dbWorktrees {
		if dbWt.Branch == branchName {
			_ = s.q.DeleteWorktree(ctx, dbWt.ID)
		}
	}

	return git.DeleteBranch(repo.Path, branchName)
}

// CheckoutBranch switches to a branch.
func (s *RepositoryService) CheckoutBranch(ctx context.Context, id, branchName string) error {
	repoID, err := s.parseRepoID(id)
	if err != nil {
		return err
	}
	repo, err := s.q.GetRepository(ctx, repoID)
	if err != nil {
		return err
	}
	return git.CheckoutBranch(repo.Path, branchName)
}

// WorktreeWithWorkspace represents a worktree with its associated workspace info.
type WorktreeWithWorkspace struct {
	ID            int64  `json:"id,omitempty"`
	Path          string `json:"path"`
	Branch        string `json:"branch"`
	IsCurrent     bool   `json:"is_current"`
	HasChanges    bool   `json:"has_changes"`
	WorkspaceID   string `json:"workspace_id,omitempty"`
	WorkspacePath string `json:"workspace_path,omitempty"`
}

// ListWorktrees returns all worktrees for the repository with workspace associations.
func (s *RepositoryService) ListWorktrees(ctx context.Context, id string) ([]WorktreeWithWorkspace, error) {
	repoID, err := s.parseRepoID(id)
	if err != nil {
		return nil, err
	}
	repo, err := s.q.GetRepository(ctx, repoID)
	if err != nil {
		return nil, err
	}

	// Get git worktrees
	gitWorktrees, err := git.ListWorktrees(repo.Path)
	if err != nil {
		return nil, err
	}

	// Get workspace associations for this repository
	wsRepos, err := s.q.ListWorktreesByRepositoryWithWorkspace(ctx, repoID)
	if err != nil {
		// If error, just return worktrees without workspace info
		result := make([]WorktreeWithWorkspace, len(gitWorktrees))
		for i, wt := range gitWorktrees {
			result[i] = WorktreeWithWorkspace{
				Path:       wt.Path,
				Branch:     wt.Branch,
				IsCurrent:  wt.IsCurrent,
				HasChanges: wt.HasChanges,
			}
		}
		return result, nil
	}

	// Build a map of worktree_path -> workspace info
	wsMap := make(map[string]struct {
		id            int64
		workspaceID   int64
		workspacePath string
	})
	for _, wr := range wsRepos {
		wsMap[wr.WorktreePath] = struct {
			id            int64
			workspaceID   int64
			workspacePath string
		}{
			id:            wr.ID,
			workspaceID:   wr.WorkspaceID,
			workspacePath: wr.WorkspacePath,
		}
	}

	// Enrich worktrees with workspace info
	result := make([]WorktreeWithWorkspace, len(gitWorktrees))
	for i, wt := range gitWorktrees {
		result[i] = WorktreeWithWorkspace{
			Path:       wt.Path,
			Branch:     wt.Branch,
			IsCurrent:  wt.IsCurrent,
			HasChanges: wt.HasChanges,
		}
		if wsInfo, ok := wsMap[wt.Path]; ok {
			result[i].ID = wsInfo.id
			result[i].WorkspaceID = fmt.Sprintf("%d", wsInfo.workspaceID)
			result[i].WorkspacePath = wsInfo.workspacePath
		}
	}

	return result, nil
}

// CreateWorktree creates a new worktree.
func (s *RepositoryService) CreateWorktree(ctx context.Context, id, worktreePath, branch string) error {
	repoID, err := s.parseRepoID(id)
	if err != nil {
		return err
	}
	repo, err := s.q.GetRepository(ctx, repoID)
	if err != nil {
		return err
	}
	return git.WorktreeAdd(repo.Path, worktreePath, branch, "")
}

// RemoveWorktree removes a worktree by its database ID.
// If the worktree path no longer exists on disk, it still deletes the DB record and returns success.
func (s *RepositoryService) RemoveWorktree(ctx context.Context, id, worktreeID string) error {
	repoID, err := s.parseRepoID(id)
	if err != nil {
		return err
	}
	wtID, err := strconv.ParseInt(worktreeID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid worktree id: %w", err)
	}

	repo, err := s.q.GetRepository(ctx, repoID)
	if err != nil {
		return err
	}

	wt, err := s.q.GetWorktree(ctx, wtID)
	if err != nil {
		return err
	}
	if wt.RepositoryID != repoID {
		return fmt.Errorf("worktree %d does not belong to repository %d", wtID, repoID)
	}

	// Try to remove the git worktree; ignore errors if path is already gone
	if err := git.WorktreeRemove(repo.Path, wt.WorktreePath); err != nil {
		if _, statErr := os.Stat(wt.WorktreePath); os.IsNotExist(statErr) {
			// Path already deleted — just clean up the DB record
		} else {
			return err
		}
	}

	// Delete the associated branch (best-effort, ignore errors)
	if wt.Branch != "" {
		_ = git.DeleteBranch(repo.Path, wt.Branch)
	}

	return s.q.DeleteWorktree(ctx, wtID)
}

type RepositoryDetail struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Path          string    `json:"path"`
	DefaultBranch string    `json:"default_branch"`
	Git           GitInfo   `json:"git"`
	Files         FilesInfo `json:"files"`
}

type GitInfo struct {
	CurrentBranch string   `json:"current_branch"`
	Branches      []string `json:"branches"`
	HasChanges    bool     `json:"has_changes"`
}

type FilesInfo struct {
	Tree TreeNode `json:"tree"`
}

func (s *RepositoryService) GetDetail(ctx context.Context, id string) (*RepositoryDetail, error) {
	repoID, err := s.parseRepoID(id)
	if err != nil {
		return nil, err
	}

	repo, err := s.q.GetRepository(ctx, repoID)
	if err != nil {
		return nil, err
	}

	// Check if valid git repo
	if ok, _ := s.isGitRepo(repo.Path); !ok {
		return nil, fmt.Errorf("NOT_A_GIT_REPO: %s", repo.Path)
	}

	// Get current branch
	currentBranch, _ := git.CurrentBranch(repo.Path)

	// Get all branches (local + remote)
	branches, _ := git.ListBranches(repo.Path)

	// Check for changes
	status, _ := git.Status(repo.Path)
	hasChanges := len(status) > 0

	// Build nested tree structure
	tree := s.buildRepoTree(repo.Path)

	defaultBranch := repo.DefaultBranch.String
	if defaultBranch == "" {
		defaultBranch = "main"
	}

	return &RepositoryDetail{
		ID:            repo.ID,
		Name:          repo.Name,
		Path:          repo.Path,
		DefaultBranch: defaultBranch,
		Git: GitInfo{
			CurrentBranch: currentBranch,
			Branches:      branches,
			HasChanges:    hasChanges,
		},
		Files: FilesInfo{Tree: tree},
	}, nil
}

// BranchTree returns local and remote branches organized hierarchically.
type BranchTree struct {
	CurrentBranch  string   `json:"current_branch"`
	LocalBranches  []string `json:"local_branches"`
	RemoteBranches []string `json:"remote_branches"`
}

func (s *RepositoryService) GetBranchTree(ctx context.Context, id string) (BranchTree, error) {
	repoID, err := s.parseRepoID(id)
	if err != nil {
		return BranchTree{}, err
	}
	repo, err := s.q.GetRepository(ctx, repoID)
	if err != nil {
		return BranchTree{}, err
	}
	local, _ := git.ListBranches(repo.Path)
	remote, _ := git.ListRemoteBranches(repo.Path)
	current, _ := git.CurrentBranch(repo.Path)
	return BranchTree{
		CurrentBranch:  current,
		LocalBranches:  local,
		RemoteBranches: remote,
	}, nil
}

// GetCommitDetail returns detail info for a single commit.
func (s *RepositoryService) GetCommitDetail(ctx context.Context, id, commitHash string) (*git.CommitDetail, error) {
	repoID, err := s.parseRepoID(id)
	if err != nil {
		return nil, err
	}
	repo, err := s.q.GetRepository(ctx, repoID)
	if err != nil {
		return nil, err
	}
	return git.GetCommitDetail(repo.Path, commitHash)
}

// GetGraphLog returns commit graph data for the repository.
func (s *RepositoryService) GetGraphLog(ctx context.Context, id string, limit int, allBranches bool) ([]git.GraphCommit, error) {
	repoID, err := s.parseRepoID(id)
	if err != nil {
		return nil, err
	}
	repo, err := s.q.GetRepository(ctx, repoID)
	if err != nil {
		return nil, err
	}
	return git.GraphLog(repo.Path, limit, allBranches)
}

// GetPath returns the filesystem path of a repository.
func (s *RepositoryService) GetPath(ctx context.Context, id string) (string, error) {
	repoID, err := s.parseRepoID(id)
	if err != nil {
		return "", err
	}
	repo, err := s.q.GetRepository(ctx, repoID)
	if err != nil {
		return "", err
	}
	return repo.Path, nil
}

// CommitAll stages and commits all changes in the repository.
func (s *RepositoryService) CommitAll(ctx context.Context, id, message string) error {
	path, err := s.GetPath(ctx, id)
	if err != nil {
		return err
	}
	return git.CommitAll(path, message)
}

// DiscardAll discards all changes in the repository.
func (s *RepositoryService) DiscardAll(ctx context.Context, id string) error {
	path, err := s.GetPath(ctx, id)
	if err != nil {
		return err
	}
	return git.DiscardAll(path)
}

// DiscardFile discards changes for a single file.
func (s *RepositoryService) DiscardFile(ctx context.Context, id, filePath string) error {
	path, err := s.GetPath(ctx, id)
	if err != nil {
		return err
	}
	return git.DiscardFile(path, filePath)
}

// buildRepoTree builds a nested TreeNode tree from a repository path
func (s *RepositoryService) buildRepoTree(repoPath string) TreeNode {
	root := TreeNode{
		Name:  filepath.Base(repoPath),
		Path:  "/",
		IsDir: true,
	}

	nodeMap := map[string]*TreeNode{"/": &root}

	filepath.WalkDir(repoPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Skip root directory itself
		if path == repoPath {
			return nil
		}

		// Skip .git directory but keep other hidden files/dirs
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == ".git" {
			return nil
		}
		// Skip other hidden directories (like node_modules, etc.)
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") && d.Name() != ".git" {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), ".") && d.Name() != ".git" {
			return nil
		}

		// Build relative path
		relPath, err := filepath.Rel(repoPath, path)
		if err != nil {
			return err
		}

		treePath := "/" + filepath.ToSlash(relPath)
		parentPath := "/" + filepath.ToSlash(filepath.Dir(relPath))
		if parentPath == "/." {
			parentPath = "/"
		}

		node := TreeNode{
			Name:  d.Name(),
			Path:  treePath,
			IsDir: d.IsDir(),
		}

		// Find parent and append
		parent, ok := nodeMap[parentPath]
		if !ok {
			parent = &root
		}
		parent.Children = append(parent.Children, node)

		// Register directory in nodeMap for future children
		if d.IsDir() {
			nodeMap[treePath] = &parent.Children[len(parent.Children)-1]
		}

		return nil
	})

	return root
}
