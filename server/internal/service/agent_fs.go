package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// AgentService manages file-based agents with DB metadata.
//
// Filesystem layout (flat, matches Claude Code convention):
//
//	<agentDir>/<name>.md
//
// The DB column `dir_path` stores the full path to the agent's .md file.
// (Historic name preserved to avoid a schema migration; semantically it is
// a file path.)
type AgentService struct {
	q        *store.Queries
	agentDir string // ~/.niuniu/agents/
	authz    *Authz
}

func NewAgentService(q *store.Queries, cfg *config.Config, authz *Authz) *AgentService {
	return &AgentService{
		q:        q,
		agentDir: filepath.Join(cfg.DataDir, "agents"),
		authz:    authz,
	}
}

// AgentDetail is returned by Get, includes file content.
type AgentDetail struct {
	store.Agent
	Content string `json:"content"` // agent .md file content
}

func (s *AgentService) List(ctx context.Context) ([]store.Agent, error) {
	return s.q.ListAgents(ctx)
}

// ListForUser returns agents accessible to userID (personal + org memberships).
func (s *AgentService) ListForUser(ctx context.Context, userID int64) ([]store.Agent, error) {
	owners, err := s.authz.Accessible(ctx, userID)
	if err != nil {
		return nil, err
	}
	orgIDs := owners.OrgIDs
	if len(orgIDs) == 0 {
		orgIDs = []int64{-1}
	}
	return s.q.ListAgentsForOwners(ctx, store.ListAgentsForOwnersParams{
		OwnerID: owners.UserID,
		OrgIds:  orgIDs,
	})
}

func (s *AgentService) Get(ctx context.Context, id int64) (AgentDetail, error) {
	agent, err := s.q.GetAgent(ctx, id)
	if err != nil {
		return AgentDetail{}, err
	}
	content, _ := os.ReadFile(agent.DirPath)
	return AgentDetail{Agent: agent, Content: string(content)}, nil
}

type CreateAgentInput struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Content     string  `json:"content"` // .md content
	SourceURL   *string `json:"source_url"`
}

func (s *AgentService) Create(ctx context.Context, input CreateAgentInput, ownerType string, ownerID int64) (store.Agent, error) {
	if err := os.MkdirAll(s.agentDir, 0o755); err != nil {
		return store.Agent{}, fmt.Errorf("create agents dir: %w", err)
	}

	filePath := filepath.Join(s.agentDir, input.Name+".md")
	content := syncFrontmatterMetadata(input.Content, input.Name, input.Description)
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		return store.Agent{}, fmt.Errorf("write agent file: %w", err)
	}

	hash := hashContent(content)
	var srcURL sql.NullString
	if input.SourceURL != nil && *input.SourceURL != "" {
		srcURL = sql.NullString{String: *input.SourceURL, Valid: true}
	}

	return s.q.CreateAgent(ctx, store.CreateAgentParams{
		Name:        input.Name,
		Description: input.Description,
		DirPath:     filePath,
		FileHash:    hash,
		SourceUrl:   srcURL,
		OwnerType:   ownerType,
		OwnerID:     ownerID,
	})
}

type UpdateAgentInput struct {
	Description string  `json:"description"`
	Content     string  `json:"content"`
	SourceURL   *string `json:"source_url"`
}

func (s *AgentService) Update(ctx context.Context, id int64, input UpdateAgentInput) error {
	agent, err := s.q.GetAgent(ctx, id)
	if err != nil {
		return err
	}

	content := syncFrontmatterMetadata(input.Content, agent.Name, input.Description)
	if err := os.WriteFile(agent.DirPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write agent file: %w", err)
	}

	hash := hashContent(content)
	var srcURL sql.NullString
	if input.SourceURL != nil && *input.SourceURL != "" {
		srcURL = sql.NullString{String: *input.SourceURL, Valid: true}
	}

	return s.q.UpdateAgent(ctx, store.UpdateAgentParams{
		Description: input.Description,
		DirPath:     agent.DirPath,
		FileHash:    hash,
		SourceUrl:   srcURL,
		ID:          id,
	})
}

func (s *AgentService) Delete(ctx context.Context, id int64) error {
	agent, err := s.q.GetAgent(ctx, id)
	if err != nil {
		return err
	}

	// DirPath may legacy-point to a directory (pre-flatten) or to a .md file.
	if info, statErr := os.Stat(agent.DirPath); statErr == nil && info.IsDir() {
		_ = os.RemoveAll(agent.DirPath)
	} else {
		_ = os.Remove(agent.DirPath)
	}
	return s.q.DeleteAgent(ctx, id)
}

// CleanWorkspaceAgents removes niuniu-managed agent files from every CLI's
// workspace subagent directory (.claude/agents, .qwen/agents, .codex/agents).
// Only files whose marker declares niuniu ownership are removed, so
// user-installed agents — even ones whose filename happens to start with
// "niuniu-" — are preserved.
func (s *AgentService) CleanWorkspaceAgents(workspacePath string) error {
	var firstErr error
	for _, ct := range agentCliTypes {
		dir := filepath.Join(workspacePath, workspaceAgentTargetFor(ct).dir)
		if err := cleanManagedAgentsDir(dir); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// cleanManagedAgentsDir removes every niuniu-managed agent file from agentsDir,
// leaving user-installed agents untouched. Ownership is read per format:
// markdown (.md) via the `managed_by: niuniu` frontmatter scalar, Codex TOML
// (.toml) via the top-level `managed_by = "niuniu"` key. A missing directory is
// a no-op. Shared by AgentService.CleanWorkspaceAgents and the scene projector.
func cleanManagedAgentsDir(agentsDir string) error {
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		path := filepath.Join(agentsDir, name)
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		var managed bool
		switch {
		case strings.HasSuffix(name, ".md"):
			managed = isManagedByNiuniu(string(data))
		case strings.HasSuffix(name, ".toml"):
			managed = isManagedByNiuniuTOML(string(data))
		default:
			continue
		}
		if managed {
			_ = os.Remove(path)
		}
	}
	return nil
}

func hashContent(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

// EnsureAgentDir creates the agents directory if it doesn't exist.
func (s *AgentService) EnsureAgentDir() error {
	return os.MkdirAll(s.agentDir, 0o755)
}
