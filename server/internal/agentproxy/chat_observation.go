package agentproxy

import (
	"context"
	"errors"
	"strings"
)

// AnalyzeChatObservation is the one-shot backend for the Agent employee's
// proactive analysis (issue #664). It hands a rendered chat transcript prompt to a
// `claude -p` subprocess and decodes the schema-validated JSON verdict into dst.
//
// It deliberately mirrors SuggestGoalCondition: same RunOneShotCLI plumbing, same
// `--json-schema` validation (so the CLI re-prompts until the output conforms and
// we never scrape prose), same configDir semantics. The prompt, schema and verdict
// type are all supplied by the caller, which keeps the domain wording with the
// domain logic in internal/service and this package free of an import back into it.
//
// dst must be a non-nil pointer to the caller's verdict struct.
func AnalyzeChatObservation(parentCtx context.Context, prompt, jsonSchema, configDir string, dst any) error {
	if !ClaudeCLIAvailable() {
		return errors.New("claude CLI not available")
	}
	if strings.TrimSpace(prompt) == "" {
		return errors.New("empty analysis prompt")
	}
	out, err := RunOneShotCLI(parentCtx, prompt, jsonSchema, configDir)
	if err != nil {
		return err
	}
	return ParseOneShotOutput(out, dst)
}
