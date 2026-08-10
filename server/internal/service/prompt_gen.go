package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// PromptGenService calls the Claude API to generate and optimize prompts.
type PromptGenService struct {
	apiKey string
	model  string
}

// NewPromptGenService creates a new PromptGenService.
// If apiKey is empty, methods will return an error at call time.
func NewPromptGenService(apiKey string) *PromptGenService {
	model := "claude-sonnet-4-20250514"
	return &PromptGenService{apiKey: apiKey, model: model}
}

// GenerateResult is the response from GeneratePrompt.
type GenerateResult struct {
	Prompt      string `json:"prompt"`
	Explanation string `json:"explanation"`
}

// OptimizeResult is the response from OptimizePrompt.
type OptimizeResult struct {
	Prompt  string   `json:"prompt"`
	Changes []string `json:"changes"`
}

// GeneratePrompt asks Claude to generate an agent role's system prompt.
func (s *PromptGenService) GeneratePrompt(ctx context.Context, name, description, projectContext string) (GenerateResult, error) {
	if s.apiKey == "" {
		return GenerateResult{}, errors.New("API key not configured")
	}

	systemPrompt := `You are an expert at writing system prompts for AI agent roles.
Given a role name, description, and project context, generate a high-quality system prompt that the agent should use.

You MUST respond with valid JSON only, no markdown fences, in this exact format:
{"prompt": "<the generated system prompt>", "explanation": "<brief explanation of design choices>"}`

	userMessage := fmt.Sprintf("Role name: %s\nDescription: %s\nProject context: %s", name, description, projectContext)

	raw, err := s.callClaude(ctx, systemPrompt, userMessage, 0) // 0 = default 4096
	if err != nil {
		return GenerateResult{}, fmt.Errorf("claude API call failed: %w", err)
	}

	var result GenerateResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return GenerateResult{}, fmt.Errorf("failed to parse claude response: %w", err)
	}
	return result, nil
}

// SuggestColumnOpResult holds AI-generated field suggestions for a kanban column.
type SuggestColumnOpResult struct {
	OpInstruction string `json:"op_instruction"`
	WhenToUse     string `json:"when_to_use"`
}

// SuggestColumnOpFields asks Claude to generate or refine an op_instruction template
// and a when_to_use routing hint for a kanban column. When currentOpInstruction or
// currentWhenToUse are non-empty, the model is asked to improve the existing content
// rather than generate from scratch. The generated text is in the same language as
// the column name.
func (s *PromptGenService) SuggestColumnOpFields(
	ctx context.Context,
	columnName string,
	gateSpecNames []string,
	currentOpInstruction, currentWhenToUse string,
) (SuggestColumnOpResult, error) {
	if s.apiKey == "" {
		return SuggestColumnOpResult{}, errors.New("API key not configured")
	}

	hasExisting := currentOpInstruction != "" || currentWhenToUse != ""

	var systemPrompt string
	if hasExisting {
		systemPrompt = `You are an expert at configuring AI-native kanban boards where AI agents execute work autonomously.

The user has already drafted content for a kanban column. Improve it based on the column name and any bound gate specs:
1. op_instruction: Refine the existing instruction to be more concise, imperative, and specific about what the AI agent must DO and DELIVER. Keep the intent; sharpen the wording.
2. when_to_use: Refine the existing routing hint to be clearer and under 50 characters.

IMPORTANT:
- Preserve the SAME LANGUAGE as the existing content.
- op_instruction must start with a verb and be actionable.
- when_to_use must be a brief condition phrase, not a sentence.
- If the existing content is already good, keep it — only improve where needed.

You MUST respond with valid JSON only, no markdown fences, in this exact format:
{"op_instruction": "...", "when_to_use": "..."}`
	} else {
		systemPrompt = `You are an expert at configuring AI-native kanban boards where AI agents execute work autonomously.

Given a kanban column name (and optional gate spec names), generate:
1. op_instruction: A concise, imperative task instruction (2-4 sentences) that tells the AI agent exactly what to DO and DELIVER when an issue enters this column. Be specific about the output expected.
2. when_to_use: A short routing hint (under 50 characters) that the AI orchestrator reads to decide whether to route an issue to this column.

IMPORTANT:
- Generate in the SAME LANGUAGE as the column name (Chinese column → Chinese output; English column → English output).
- op_instruction must start with a verb and be actionable.
- when_to_use must be a brief condition phrase, not a sentence.

You MUST respond with valid JSON only, no markdown fences, in this exact format:
{"op_instruction": "...", "when_to_use": "..."}`
	}

	specsNote := ""
	if len(gateSpecNames) > 0 {
		specsNote = fmt.Sprintf("\nBound gate specs (for context): %v", gateSpecNames)
	}

	var userMessage string
	if hasExisting {
		userMessage = fmt.Sprintf(
			"Column name: %s%s\nCurrent op_instruction: %s\nCurrent when_to_use: %s",
			columnName, specsNote, currentOpInstruction, currentWhenToUse,
		)
	} else {
		userMessage = fmt.Sprintf("Column name: %s%s", columnName, specsNote)
	}

	// 512 tokens is more than sufficient for two short strings (op_instruction
	// ~100 tokens, when_to_use ~15 tokens); 4096 would be ~20× overkill per call.
	raw, err := s.callClaude(ctx, systemPrompt, userMessage, 512)
	if err != nil {
		return SuggestColumnOpResult{}, fmt.Errorf("claude API call failed: %w", err)
	}

	var result SuggestColumnOpResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return SuggestColumnOpResult{}, fmt.Errorf("failed to parse claude response: %w", err)
	}
	return result, nil
}

// OptimizePrompt asks Claude to improve an existing prompt based on feedback.
func (s *PromptGenService) OptimizePrompt(ctx context.Context, currentPrompt, feedback string) (OptimizeResult, error) {
	if s.apiKey == "" {
		return OptimizeResult{}, errors.New("API key not configured")
	}

	systemPrompt := `You are an expert at optimizing system prompts for AI agents.
Given a current prompt and user feedback, produce an improved version.

You MUST respond with valid JSON only, no markdown fences, in this exact format:
{"prompt": "<the optimized prompt>", "changes": ["<change 1>", "<change 2>"]}`

	userMessage := fmt.Sprintf("Current prompt:\n%s\n\nFeedback:\n%s", currentPrompt, feedback)

	raw, err := s.callClaude(ctx, systemPrompt, userMessage, 0) // 0 = default 4096
	if err != nil {
		return OptimizeResult{}, fmt.Errorf("claude API call failed: %w", err)
	}

	var result OptimizeResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return OptimizeResult{}, fmt.Errorf("failed to parse claude response: %w", err)
	}
	return result, nil
}

// claudeResponse is the Anthropic Messages API response structure.
type claudeResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// callClaude makes a single request to the Anthropic Messages API.
// maxTokens defaults to 4096 when 0 is passed.
func (s *PromptGenService) callClaude(ctx context.Context, systemPrompt, userMessage string, maxTokens int) (string, error) {
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	reqBody := map[string]interface{}{
		"model":      s.model,
		"max_tokens": maxTokens,
		"system":     systemPrompt,
		"messages": []map[string]string{
			{"role": "user", "content": userMessage},
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", s.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("claude API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var cr claudeResponse
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return "", fmt.Errorf("unmarshal response: %w", err)
	}

	if cr.Error != nil {
		return "", fmt.Errorf("claude API error: %s: %s", cr.Error.Type, cr.Error.Message)
	}

	for _, block := range cr.Content {
		if block.Type == "text" {
			return block.Text, nil
		}
	}

	return "", errors.New("no text content in claude response")
}
