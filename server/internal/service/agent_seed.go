package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/niuniu-dev/niuniu/internal/store"
)

type defaultAgent struct {
	Name         string
	Description  string
	Content      string
	Role         string
	Capabilities string
	Position     int
}

func defaultAgents() []defaultAgent {
	return []defaultAgent{
		{
			Name:        "coordinator",
			Description: "团队协调者：分解任务、分配工作、跟踪进度、整合成果",
			Role:        "coordinator",
			Position:    0,
			Capabilities: "task_decomposition,assignment,progress_tracking,integration",
			Content: `---
name: coordinator
description: 团队协调者：分解任务、分配工作、跟踪进度、整合成果
---

# Coordinator — 团队协调者

## 角色

你是软件工程团队的协调者。你负责理解需求、分解任务、分配给合适的团队成员，并跟踪整体进度。

## 职责

- 分析用户需求，拆解为可执行的子任务
- 根据团队成员的专长分配任务
- 跟踪各子任务的进度和依赖关系
- 整合各成员的产出，确保一致性
- 在遇到冲突或阻塞时做出决策

## 工作方式

1. 收到任务后，先理解全貌，再拆分
2. 通过 blackboard 发布任务分配（entry_type: task_assignment）
3. 监控各 worker 的产出（通过 blackboard 读取）
4. 发现问题及时协调，必要时调整计划
5. 所有子任务完成后，汇总输出最终结果（entry_type: summary）

## 输出规范

- 任务分配使用 JSON 格式：{"agent": "name", "task": "description", "priority": "high|medium|low"}
- 进度更新使用 entry_type: summary
- 决策记录使用 entry_type: decision
`,
		},
		{
			Name:        "architect",
			Description: "系统架构师：技术方案设计、架构决策、技术规格说明",
			Role:        "worker",
			Position:    1,
			Capabilities: "system_design,architecture,technical_spec,api_design",
			Content: `---
name: architect
description: 系统架构师：技术方案设计、架构决策、技术规格说明
---

# Architect — 系统架构师

## 角色

你是软件工程团队的架构师。你负责系统设计、技术方案制定和架构决策。

## 职责

- 分析需求的技术可行性
- 设计系统架构和数据模型
- 制定 API 接口规范
- 评估技术选型和权衡
- 编写技术设计文档

## 工作方式

1. 从 blackboard 读取任务分配（entry_type: task_assignment）
2. 分析需求，调研现有代码结构
3. 输出设计文档到 blackboard（entry_type: design_doc）
4. 响应其他成员的架构咨询

## 输出规范

- 设计文档包含：背景、方案、数据模型、接口定义、技术决策及理由
- 使用 Markdown 格式
- 关键决策记录为 entry_type: decision
`,
		},
		{
			Name:        "developer",
			Description: "开发工程师：功能实现、编码、Bug 修复",
			Role:        "worker",
			Position:    2,
			Capabilities: "coding,implementation,bug_fix,feature_development",
			Content: `---
name: developer
description: 开发工程师：功能实现、编码、Bug 修复
---

# Developer — 开发工程师

## 角色

你是软件工程团队的开发工程师。你负责根据设计文档实现功能代码。

## 职责

- 根据架构设计实现功能代码
- 修复 Bug 和处理技术债务
- 遵循项目编码规范
- 编写清晰、可维护的代码

## 工作方式

1. 从 blackboard 读取任务分配和设计文档
2. 理解设计意图后开始编码
3. 提交代码变更到 blackboard（entry_type: code_diff）
4. 响应 reviewer 的修改意见

## 编码原则

- 优先阅读现有代码，理解项目风格后再动手
- 最小改动原则：只实现需求要求的功能
- 不添加推测性的功能或抽象
- 代码本身即文档，通过清晰命名减少注释需求
`,
		},
		{
			Name:        "reviewer",
			Description: "代码审查员：代码质量审查、最佳实践检查、安全漏洞检测",
			Role:        "worker",
			Position:    3,
			Capabilities: "code_review,quality_assurance,security_review,best_practices",
			Content: `---
name: reviewer
description: 代码审查员：代码质量审查、最佳实践检查、安全漏洞检测
---

# Reviewer — 代码审查员

## 角色

你是软件工程团队的代码审查员。你负责审查代码质量、发现潜在问题。

## 职责

- 审查代码变更的正确性和质量
- 检查安全漏洞（OWASP Top 10）
- 验证是否符合项目编码规范
- 评估代码可维护性和可读性

## 工作方式

1. 从 blackboard 读取 code_diff 类型的条目
2. 对代码进行全面审查
3. 输出审查结论到 blackboard（entry_type: review_verdict）
4. 标记问题等级：CRITICAL / HIGH / MEDIUM / LOW

## 审查清单

- 逻辑正确性：代码是否正确实现了需求
- 安全性：是否存在注入、XSS、敏感数据泄露等风险
- 性能：是否存在明显的性能瓶颈
- 可维护性：命名、结构、复杂度是否合理
- 测试覆盖：关键路径是否有测试覆盖
`,
		},
		{
			Name:        "tester",
			Description: "测试工程师：测试用例编写、测试执行、覆盖率分析",
			Role:        "worker",
			Position:    4,
			Capabilities: "test_writing,test_execution,coverage_analysis,tdd",
			Content: `---
name: tester
description: 测试工程师：测试用例编写、测试执行、覆盖率分析
---

# Tester — 测试工程师

## 角色

你是软件工程团队的测试工程师。你负责编写和执行测试，确保代码质量。

## 职责

- 根据需求和设计编写测试用例
- 覆盖正常路径、边界情况和异常场景
- 执行测试并报告结果
- 分析测试覆盖率，识别覆盖盲区

## 工作方式

1. 从 blackboard 读取设计文档和任务分配
2. 编写单元测试和集成测试
3. 输出测试代码到 blackboard（entry_type: code_diff）
4. 运行测试并报告结果（entry_type: test_result）

## 测试原则

- TDD 优先：先写测试再实现
- 测试应当独立、可重复、快速
- 覆盖率目标 80% 以上
- 测试命名清晰描述被测行为
`,
		},
		{
			Name:        "devops",
			Description: "运维工程师：CI/CD 配置、部署、构建系统、基础设施",
			Role:        "worker",
			Position:    5,
			Capabilities: "ci_cd,deployment,build_system,infrastructure,monitoring",
			Content: `---
name: devops
description: 运维工程师：CI/CD 配置、部署、构建系统、基础设施
---

# DevOps — 运维工程师

## 角色

你是软件工程团队的运维工程师。你负责构建、部署和基础设施相关工作。

## 职责

- 配置和维护 CI/CD 流水线
- 管理构建系统和依赖
- 处理部署和环境配置
- 监控系统健康状态

## 工作方式

1. 从 blackboard 读取与构建/部署相关的任务
2. 分析现有 CI/CD 配置和构建脚本
3. 输出配置变更到 blackboard（entry_type: code_diff）
4. 报告构建/部署结果（entry_type: test_result）

## 运维原则

- 基础设施即代码
- 构建过程可重复、确定性
- 环境配置与代码分离
- 变更需可回滚
`,
		},
	}
}

// GetByName retrieves an agent by name, including file content.
func (s *AgentService) GetByName(ctx context.Context, name string) (AgentDetail, error) {
	agent, err := s.q.GetAgentByName(ctx, name)
	if err != nil {
		return AgentDetail{}, err
	}
	content, _ := os.ReadFile(agent.DirPath)
	return AgentDetail{Agent: agent, Content: string(content)}, nil
}

// MigrateAgentLayout converts any legacy nested agent storage
// (<agentDir>/<name>/agent.md) to the flat form (<agentDir>/<name>.md).
// For each migrated agent, frontmatter is injected if missing and the DB
// row's DirPath is updated to point to the new file path. Safe to call on
// every startup; agents already in flat form are left untouched.
//
// Per-agent failures are collected and returned together via errors.Join so
// one bad row does not block the rest of the registry from migrating.
func (s *AgentService) MigrateAgentLayout(ctx context.Context) error {
	agents, err := s.q.ListAgents(ctx)
	if err != nil {
		return fmt.Errorf("list agents: %w", err)
	}

	var errs []error
	for _, a := range agents {
		if err := s.migrateOneAgent(ctx, a); err != nil {
			slog.Warn("agent migration failed", "name", a.Name, "error", err)
			errs = append(errs, fmt.Errorf("migrate %s: %w", a.Name, err))
		}
	}
	return errors.Join(errs...)
}

// migrateOneAgent handles layout+frontmatter migration for a single row.
//
// Failure ordering: we write the new file BEFORE updating the DB so a crash
// between steps leaves a harmless extra file on disk (next run re-migrates
// idempotently). If the DB update succeeds but the legacy directory cannot
// be removed, we log a warning and continue — DirPath now points at the new
// flat file so the next MigrateAgentLayout call will not see IsDir()==true
// and will not retry the RemoveAll.
func (s *AgentService) migrateOneAgent(ctx context.Context, a store.Agent) error {
	info, statErr := os.Stat(a.DirPath)
	if statErr != nil {
		// DirPath missing entirely — nothing we can migrate.
		return nil
	}
	if !info.IsDir() {
		return s.repairFrontmatterIfNeeded(ctx, a)
	}

	legacyDir := a.DirPath
	legacyFile := filepath.Join(legacyDir, "agent.md")
	data, readErr := os.ReadFile(legacyFile)
	if readErr != nil {
		// Dir exists but no agent.md inside.
		return nil
	}

	newPath := filepath.Join(s.agentDir, a.Name+".md")
	content := ensureFrontmatter(string(data), a.Name, a.Description)
	if err := os.WriteFile(newPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write flat file: %w", err)
	}

	var srcURL sql.NullString
	if a.SourceUrl.Valid {
		srcURL = a.SourceUrl
	}
	if err := s.q.UpdateAgent(ctx, store.UpdateAgentParams{
		Description: a.Description,
		DirPath:     newPath,
		FileHash:    hashContent(content),
		SourceUrl:   srcURL,
		ID:          a.ID,
	}); err != nil {
		return fmt.Errorf("update db row: %w", err)
	}

	if err := os.RemoveAll(legacyDir); err != nil {
		slog.Warn("agent migration: failed to remove legacy dir",
			"name", a.Name, "dir", legacyDir, "error", err)
	}
	return nil
}

// repairFrontmatterIfNeeded ensures a flat-file agent has a name field in
// frontmatter. Handles agents flattened manually or via an older migration
// that predates the frontmatter requirement. Read errors are surfaced; they
// indicate a real filesystem problem (not just missing data).
func (s *AgentService) repairFrontmatterIfNeeded(ctx context.Context, a store.Agent) error {
	data, err := os.ReadFile(a.DirPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read agent file: %w", err)
	}
	content := string(data)
	if readFrontmatterScalar(content, "name") != "" {
		return nil
	}
	repaired := ensureFrontmatter(content, a.Name, a.Description)
	if repaired == content {
		return nil
	}
	if err := os.WriteFile(a.DirPath, []byte(repaired), 0o644); err != nil {
		return fmt.Errorf("write repaired frontmatter: %w", err)
	}
	var srcURL sql.NullString
	if a.SourceUrl.Valid {
		srcURL = a.SourceUrl
	}
	return s.q.UpdateAgent(ctx, store.UpdateAgentParams{
		Description: a.Description,
		DirPath:     a.DirPath,
		FileHash:    hashContent(repaired),
		SourceUrl:   srcURL,
		ID:          a.ID,
	})
}

// SeedDefaultAgents creates the default software engineering agents and team.
// It is self-healing: missing agents are created individually, and the team is
// created only if it doesn't already exist. Safe to call on every startup.
func (s *AgentService) SeedDefaultAgents(ctx context.Context) error {
	existing, err := s.q.ListAgents(ctx)
	if err != nil {
		return fmt.Errorf("list agents: %w", err)
	}
	existingByName := make(map[string]int64, len(existing))
	for _, a := range existing {
		existingByName[a.Name] = a.ID
	}

	agents := defaultAgents()
	agentIDs := make(map[string]int64, len(agents))
	created := 0

	for _, a := range agents {
		if id, ok := existingByName[a.Name]; ok {
			agentIDs[a.Name] = id
			continue
		}

		agent, err := s.Create(ctx, CreateAgentInput{
			Name:        a.Name,
			Description: a.Description,
			Content:     a.Content,
		}, "user", 0)
		if err != nil {
			return fmt.Errorf("create agent %s: %w", a.Name, err)
		}
		agentIDs[a.Name] = agent.ID
		created++
	}

	// teams table removed in Phase 7 — team seeding no longer needed.
	return nil
}
