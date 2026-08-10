package harness

// DefaultSpecs returns the default global harness specs seeded on first run.
// Each spec carries its full typed config (Kind + relevant fields). The legacy
// Config JSON is kept as "{}" for backward compatibility with any consumer that
// still reads it; the checker layer ignores it in favor of the typed fields.
func DefaultSpecs() []Spec {
	return []Spec{
		{
			Scope:     "global",
			Category:  "commit",
			Name:      "conventional-commits",
			Enabled:   true,
			Severity:  "warning",
			Config:    `{}`,
			Kind:      KindRegexMatch,
			Target:    TargetCommitMessage,
			Pattern:   `^(feat|fix|refactor|docs|test|chore|perf|ci)(\(.+\))?: .+`,
			TriggerOn: TriggerPhaseExit,
		},
		{
			Scope:     "global",
			Category:  "commit",
			Name:      "branch-name",
			Enabled:   false,
			Severity:  "info",
			Config:    `{}`,
			Kind:      KindRegexMatch,
			Target:    TargetBranchName,
			Pattern:   `^(feat|fix|refactor|chore|docs|test|perf|ci|ws-\d+)/[a-z0-9][a-z0-9\-]*$`,
			TriggerOn: TriggerPhaseExit,
		},
		{
			Scope:          "global",
			Category:       "quality",
			Name:           "test-coverage",
			Enabled:        false,
			Severity:       "warning",
			Config:         `{"threshold":80}`,
			Kind:           KindCommandOutputMatch,
			Command:        "go test -cover ./...",
			TimeoutSec:     120,
			ExtractRegex:   `coverage:\s+(\d+\.?\d*)%`,
			ThresholdValue: 80,
			ThresholdOp:    ThresholdGTE,
			TriggerOn:      TriggerPhaseExit,
		},
		{
			Scope:      "global",
			Category:   "quality",
			Name:       "linter",
			Enabled:    false,
			Severity:   "warning",
			Config:     `{"command":""}`,
			Kind:       KindCommandExitCode,
			TimeoutSec: 60,
			TriggerOn:  TriggerPhaseExit,
		},
		{
			// build-test-pass is the default floor (底线) spec for the
			// "完成" column under blocking-semantics 方案 B: whether a gate
			// blocks is decided solely by spec.severity=='error' (see
			// CheckRunner.HasBlockingFailure). The pre-existing default
			// directory carried no severity=error spec — conventional-commits
			// is severity=warning and so never blocks — so a project's 完成
			// floor gate had nothing it could bind that would actually hold
			// the line. This seeds that missing severity=error baseline.
			//
			// Shipped disabled with an empty command (conservative default,
			// like linter/command-exit-code): CommandExitCode returns "skip"
			// for an empty command, and HasBlockingFailure only trips on a
			// "fail", so it never false-blocks until a user fills in their
			// project's build/test command. Binding it as the 完成 column
			// floor gate (column_gate_specs applicability='always') is a
			// later phase; this phase only guarantees the spec exists.
			Scope:      "global",
			Category:   "quality",
			Name:       "build-test-pass",
			Enabled:    false,
			Severity:   "error",
			Config:     `{"command":"","timeout_sec":300}`,
			Kind:       KindCommandExitCode,
			TimeoutSec: 300,
			TriggerOn:  TriggerPhaseExit,
		},
		{
			Scope:     "global",
			Category:  "workflow",
			Name:      "output-pattern",
			Enabled:   false,
			Severity:  "warning",
			Config:    `{"pattern":""}`,
			Kind:      KindRegexMatch,
			Target:    TargetAgentOutput,
			TriggerOn: TriggerPhaseExit,
		},
		{
			Scope:     "global",
			Category:  "workflow",
			Name:      "file-exists",
			Enabled:   false,
			Severity:  "warning",
			Config:    `{"paths":[]}`,
			Kind:      KindFileExists,
			FilePaths: `[]`,
			TriggerOn: TriggerPhaseExit,
		},
		{
			Scope:      "global",
			Category:   "workflow",
			Name:       "command-exit-code",
			Enabled:    false,
			Severity:   "warning",
			Config:     `{"command":"","timeout_sec":120}`,
			Kind:       KindCommandExitCode,
			TimeoutSec: 120,
			TriggerOn:  TriggerPhaseExit,
		},
		{
			Scope:      "global",
			Category:   "workflow",
			Name:       "command-output",
			Enabled:    false,
			Severity:   "warning",
			Config:     `{"command":"","pattern":"","timeout_sec":120}`,
			Kind:       KindCommandOutputMatch,
			TimeoutSec: 120,
			TriggerOn:  TriggerPhaseExit,
		},
		{
			// ai_judge example spec. Disabled by default because it requires
			// ANTHROPIC_API_KEY to be set on the server. Users enable it and
			// customise judge_prompt to match their codebase's rubric.
			Scope:       "global",
			Category:    "commit",
			Name:        "commit-message-quality-ai",
			Enabled:     false,
			Severity:    "warning",
			Config:      `{}`,
			Kind:        KindAIJudge,
			Target:      TargetCommitMessage,
			TimeoutSec:  60,
			TriggerOn:   TriggerPreCommit,
			JudgeModel:  "claude-haiku-4-5-20251001",
			JudgePrompt: `Evaluate whether this commit message clearly explains WHY the change was made, not just WHAT changed. A good commit message identifies the user-facing or technical motivation. Pass if intent is clear, fail if it only restates the diff.`,
		},
	}
}
