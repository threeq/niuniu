package agentproxy

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// columnOpJSONSchema is the --json-schema argument for SuggestColumnOpFields.
const columnOpJSONSchema = `{"type":"object","properties":{"op_instruction":{"type":"string"},"when_to_use":{"type":"string","maxLength":100}},"required":["op_instruction","when_to_use"],"additionalProperties":false}`

// ColumnOpResult holds the two AI-generated fields for a kanban column.
type ColumnOpResult struct {
	OpInstruction string `json:"op_instruction"`
	WhenToUse     string `json:"when_to_use"`
}

// columnOpGeneratePrompt — generate both fields from scratch.
const columnOpGeneratePrompt = `OUTPUT FORMAT (mandatory, machine-parsed): a single JSON object on one line, no prose, no markdown fences. Schema:
{"op_instruction":"<task instruction>","when_to_use":"<routing hint, max 50 chars>"}

TASK: Generate two configuration fields for an AI-native kanban column.

Column name: %s%s

RULES:
- Generate in the SAME LANGUAGE as the column name (Chinese name → Chinese output; English name → English output).
- op_instruction: 2-4 imperative sentences telling the AI agent exactly what to DO and DELIVER when an issue enters this column. Start with a verb. Be specific about the expected output.
- when_to_use: a brief condition phrase (≤50 characters) the AI orchestrator reads to decide whether to route an issue here. NOT a full sentence.

Emit the JSON object and NOTHING else:
`

// columnOpRefinePrompt — improve existing user-drafted content.
const columnOpRefinePrompt = `OUTPUT FORMAT (mandatory, machine-parsed): a single JSON object on one line, no prose, no markdown fences. Schema:
{"op_instruction":"<task instruction>","when_to_use":"<routing hint, max 50 chars>"}

TASK: Improve the existing configuration for an AI-native kanban column. Preserve the intent; sharpen the wording.

Column name: %s%s

Existing content (improve this):
op_instruction: %s
when_to_use: %s

RULES:
- Preserve the SAME LANGUAGE as the existing content.
- op_instruction: 2-4 imperative sentences, starts with a verb, specific about what to DO and DELIVER.
- when_to_use: brief condition phrase, ≤50 characters, not a full sentence.
- If the existing content is already good, keep it — only change what needs improvement.

Emit the JSON object and NOTHING else:
`

// SuggestColumnOpFields spawns a one-shot `claude --print` subprocess that
// proposes op_instruction and when_to_use for a kanban column via RunOneShotCLI.
//
// When currentOpInstruction or currentWhenToUse are non-empty, the model
// refines the existing content (improve mode) rather than generating from
// scratch (generate mode).
//
// configDir is the resolved Claude account config directory (from
// ClaudeAccountService.ResolveAccountForUser). Empty = fall back to the
// CLI's native ~/.claude credentials.
func SuggestColumnOpFields(
	parentCtx context.Context,
	columnName string,
	gateSpecNames []string,
	currentOpInstruction, currentWhenToUse string,
	configDir string,
) (ColumnOpResult, error) {
	if !ClaudeCLIAvailable() {
		return ColumnOpResult{}, errors.New("claude CLI not available")
	}

	specsNote := ""
	if len(gateSpecNames) > 0 {
		specsNote = fmt.Sprintf("\nBound gate specs (for context): %s", strings.Join(gateSpecNames, ", "))
	}

	hasExisting := strings.TrimSpace(currentOpInstruction) != "" || strings.TrimSpace(currentWhenToUse) != ""

	var prompt string
	if hasExisting {
		prompt = fmt.Sprintf(columnOpRefinePrompt,
			truncateRunes(columnName, 100), specsNote,
			truncateRunes(currentOpInstruction, 800),
			truncateRunes(currentWhenToUse, 100),
		)
	} else {
		prompt = fmt.Sprintf(columnOpGeneratePrompt, truncateRunes(columnName, 100), specsNote)
	}

	out, err := RunOneShotCLI(parentCtx, prompt, columnOpJSONSchema, configDir)
	if err != nil {
		return ColumnOpResult{}, err
	}

	var result ColumnOpResult
	if err := ParseOneShotOutput(out, &result); err != nil {
		return ColumnOpResult{}, fmt.Errorf("column op parse failed: %w", err)
	}
	return result, nil
}
