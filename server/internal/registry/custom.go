package registry

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type CustomSource struct {
	dir string
}

type CreateCustomAgentInput struct {
	Name        string
	Description string
	Content     string
	ClonedFrom  string
}

type UpdateCustomAgentInput struct {
	Description string
	Content     string
}

func NewCustomSource(dir string) *CustomSource {
	return &CustomSource{dir: dir}
}

func (s *CustomSource) Type() string { return "custom" }

func (s *CustomSource) List(ctx context.Context) ([]AgentInfo, error) {
	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return nil, fmt.Errorf("ensure dir: %w", err)
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("read dir: %w", err)
	}
	var agents []AgentInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		filePath := filepath.Join(s.dir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}
		fm, _, err := parseFrontmatter(string(data))
		if err != nil {
			continue
		}
		name := fm["name"]
		if name == "" {
			name = strings.TrimSuffix(entry.Name(), ".md")
		}
		info := AgentInfo{
			Source:      "custom",
			Name:        name,
			Description: fm["description"],
			ClonedFrom:  fm["cloned_from"],
			FilePath:    filePath,
		}
		agents = append(agents, info)
	}
	return agents, nil
}

func (s *CustomSource) Get(ctx context.Context, name string) (*AgentDetail, error) {
	if err := ValidateAgentName(name); err != nil {
		return nil, err
	}
	filePath := filepath.Join(s.dir, name+".md")
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("custom agent %q not found", name)
		}
		return nil, fmt.Errorf("read file: %w", err)
	}
	content := string(data)
	fm, body, err := parseFrontmatter(content)
	if err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}
	agentName := fm["name"]
	if agentName == "" {
		agentName = name
	}
	return &AgentDetail{
		AgentInfo: AgentInfo{
			Source:      "custom",
			Name:        agentName,
			Description: fm["description"],
			ClonedFrom:  fm["cloned_from"],
			FilePath:    filePath,
		},
		Content: body,
	}, nil
}

func (s *CustomSource) Refresh(ctx context.Context) error {
	return nil
}

func (s *CustomSource) Create(ctx context.Context, input CreateCustomAgentInput) (*AgentInfo, error) {
	if err := ValidateAgentName(input.Name); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return nil, fmt.Errorf("ensure dir: %w", err)
	}
	filePath := filepath.Join(s.dir, input.Name+".md")
	if _, err := os.Stat(filePath); err == nil {
		return nil, fmt.Errorf("agent %q already exists", input.Name)
	}

	fields := map[string]string{
		"name":        input.Name,
		"description": input.Description,
		"created_at":  time.Now().Format("2006-01-02"),
	}
	if input.ClonedFrom != "" {
		fields["cloned_from"] = input.ClonedFrom
	}
	fileContent := buildFrontmatter(fields, input.Content)

	if err := os.WriteFile(filePath, []byte(fileContent), 0644); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}
	return &AgentInfo{
		Source:      "custom",
		Name:        input.Name,
		Description: input.Description,
		ClonedFrom:  input.ClonedFrom,
		FilePath:    filePath,
	}, nil
}

func (s *CustomSource) Update(ctx context.Context, name string, input UpdateCustomAgentInput) error {
	if err := ValidateAgentName(name); err != nil {
		return err
	}
	filePath := filepath.Join(s.dir, name+".md")
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}
	fm, _, err := parseFrontmatter(string(data))
	if err != nil {
		return fmt.Errorf("parse frontmatter: %w", err)
	}
	fm["description"] = input.Description
	fileContent := buildFrontmatter(fm, input.Content)
	return os.WriteFile(filePath, []byte(fileContent), 0644)
}

func (s *CustomSource) Delete(ctx context.Context, name string) error {
	if err := ValidateAgentName(name); err != nil {
		return err
	}
	filePath := filepath.Join(s.dir, name+".md")
	if err := os.Remove(filePath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("agent %q not found", name)
		}
		return fmt.Errorf("remove file: %w", err)
	}
	return nil
}
