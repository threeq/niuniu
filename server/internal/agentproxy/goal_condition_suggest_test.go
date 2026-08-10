package agentproxy

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestParseSuggestOutput_BareJSON(t *testing.T) {
	raw := []byte(`{"suggestion": "All unit tests pass and make test exits 0"}`)
	got, err := parseSuggestOutput(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "All unit tests pass and make test exits 0" {
		t.Fatalf("unexpected suggestion: %q", got)
	}
}

func TestParseSuggestOutput_EnvelopeWrapped(t *testing.T) {
	// `claude --output-format json` wraps the model reply in an envelope.
	raw := []byte(`{"type":"result","result":"{\"suggestion\":\"PR is merge-ready\"}"}`)
	got, err := parseSuggestOutput(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "PR is merge-ready" {
		t.Fatalf("expected envelope-unwrapped suggestion, got %q", got)
	}
}

func TestParseSuggestOutput_StripsMarkdownFence(t *testing.T) {
	raw := []byte("```json\n{\"suggestion\":\"npm test passes\"}\n```")
	got, err := parseSuggestOutput(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "npm test passes" {
		t.Fatalf("expected fence-stripped suggestion, got %q", got)
	}
}

func TestParseSuggestOutput_EmptySuggestion(t *testing.T) {
	raw := []byte(`{"suggestion":"   "}`)
	if _, err := parseSuggestOutput(raw); err == nil {
		t.Fatal("expected error for whitespace-only suggestion")
	}
}

func TestParseSuggestOutput_InvalidJSON(t *testing.T) {
	raw := []byte(`not json at all`)
	if _, err := parseSuggestOutput(raw); err == nil {
		t.Fatal("expected parse error for non-JSON")
	}
}

func TestParseSuggestOutput_SkipsStatusFramesBeforeEnvelope(t *testing.T) {
	// Regression: production hit `{"status":"ready"}\n{"type":"result",...}`
	// where a plugin / auto-memory handshake wrote a status frame before
	// the real envelope. Single-object parsing picked the wrong one and
	// returned "empty suggestion". Multi-object scan must skip status
	// frames (no `suggestion`, no `result`) and find the envelope.
	raw := []byte(`{"status":"ready"}` + "\n" +
		`{"type":"result","subtype":"success","is_error":false,"result":"{\"suggestion\":\"All tests pass and PR merged\"}"}`)
	got, err := parseSuggestOutput(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "All tests pass and PR merged" {
		t.Fatalf("expected suggestion from second object, got %q", got)
	}
}

func TestParseSuggestOutput_SkipsStatusFramesAfterEnvelope(t *testing.T) {
	// Same idea but the status frame trails the envelope.
	raw := []byte(`{"type":"result","is_error":false,"result":"{\"suggestion\":\"x\"}"}` + "\n" +
		`{"status":"done"}`)
	got, err := parseSuggestOutput(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "x" {
		t.Fatalf("expected suggestion from envelope, got %q", got)
	}
}

func TestParseSuggestOutput_StructuredOutputFieldWins(t *testing.T) {
	// Regression: claude CLI 2.1.x with --json-schema does NOT put the
	// model's schema-validated reply into `result`. It emits a NEW top-
	// level `structured_output` field on the envelope, and `result` is
	// "" (or model's free text — empty under JSON schema constraint).
	// parseSuggestOutput was only looking at `result`, silently dropping
	// every successful suggest call and returning "empty suggestion".
	//
	// Fixture is trimmed from a real production stdout (cwd=tmp, default
	// ~/.claude account, our exact flag set), with the volatile fields
	// (duration_ms, session_id, uuid, cost) stripped. The presence and
	// nesting of `structured_output.suggestion` is what matters.
	t.Parallel()
	raw := []byte(`{"type":"result","subtype":"success","is_error":false,"result":"","structured_output":{"suggestion":"GET /api/health returns 200 with {\"status\":\"ok\"} and the new unit test exits 0"},"stop_reason":"end_turn"}`)
	got, err := parseSuggestOutput(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `GET /api/health returns 200 with {"status":"ok"} and the new unit test exits 0`
	if got != want {
		t.Fatalf("expected suggestion from structured_output, got %q", got)
	}
}

func TestParseSuggestOutput_StructuredOutputBeatsResult(t *testing.T) {
	// Belt-and-suspenders: when an envelope carries BOTH a non-empty
	// `result` AND `structured_output.suggestion`, prefer the schema-
	// validated one. `result` under --json-schema is usually "" but the
	// CLI is known to populate it during retries / fallback rounds; we
	// always want the schema-conformant value.
	t.Parallel()
	raw := []byte(`{"type":"result","is_error":false,"result":"{\"suggestion\":\"OLD-from-result\"}","structured_output":{"suggestion":"NEW-from-structured"}}`)
	got, err := parseSuggestOutput(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "NEW-from-structured" {
		t.Fatalf("expected structured_output to win, got %q", got)
	}
}

func TestParseSuggestOutput_StructuredOutputWithStatusFrame(t *testing.T) {
	// Multi-object stdout (status frame + envelope) under --json-schema:
	// the scan must still find structured_output inside the right object,
	// not get fooled into picking the status frame.
	t.Parallel()
	raw := []byte(`{"status":"ready"}` + "\n" +
		`{"type":"result","is_error":false,"result":"","structured_output":{"suggestion":"all tests pass"}}`)
	got, err := parseSuggestOutput(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "all tests pass" {
		t.Fatalf("expected suggestion from envelope, got %q", got)
	}
}

func TestParseSuggestOutput_DirectSuggestionWithoutEnvelope(t *testing.T) {
	// Some claude invocations (e.g., when keychain reads succeed but
	// envelope is suppressed by --bare-style modes) emit raw model JSON.
	// Multi-object scan should still pick {"suggestion":...} directly.
	raw := []byte(`{"suggestion":"All unit tests pass"}`)
	got, err := parseSuggestOutput(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "All unit tests pass" {
		t.Fatalf("expected direct suggestion, got %q", got)
	}
}

func TestParseSuggestOutput_OnlyStatusFramesReturnsEmpty(t *testing.T) {
	// Sanity: if all stdout is status frames with no real envelope, we
	// must NOT silently accept one of them (suggestion would be empty
	// string). Return "empty suggestion" so the operator sees the issue.
	raw := []byte(`{"status":"ready"}` + "\n" + `{"response":"ready"}` + "\n" + `{"done":true}`)
	if _, err := parseSuggestOutput(raw); err == nil {
		t.Fatal("expected error when only status frames present")
	}
}

func TestExtractAllJSONObjects_MultipleOnSameLine(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"a":1}{"b":2} text {"c":3}`)
	objs := extractAllJSONObjects(raw)
	if len(objs) != 3 {
		t.Fatalf("expected 3 objects, got %d: %v", len(objs), objs)
	}
	if string(objs[0]) != `{"a":1}` || string(objs[1]) != `{"b":2}` || string(objs[2]) != `{"c":3}` {
		t.Errorf("unexpected extraction: %v", objs)
	}
}

func TestParseSuggestOutput_ProseWrappedJSONExtracted(t *testing.T) {
	// Regression: haiku occasionally ignores "no prose" and emits
	// "Sure, here's the criterion: {...} Let me know if..." despite the
	// prompt's explicit JSON-only instruction. parser must extract the
	// inner balanced object instead of bailing with parse error.
	raw := []byte(`Sure, here is the criterion: {"suggestion":"All tests pass"} Let me know!`)
	got, err := parseSuggestOutput(raw)
	if err != nil {
		t.Fatalf("expected lenient parse, got error: %v", err)
	}
	if got != "All tests pass" {
		t.Fatalf("expected extracted suggestion, got %q", got)
	}
}

func TestExtractEmbeddedJSONObject_BalanceTracking(t *testing.T) {
	// Skip stray closing brace, find the first BALANCED top-level object,
	// and ignore '{'/'}' inside string literals (including \"-escape).
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", `{"a":1}`, `{"a":1}`},
		{"with-prose-before", `Sure, here: {"a":1} thanks`, `{"a":1}`},
		{"nested", `prefix {"a":{"b":2},"c":3} suffix`, `{"a":{"b":2},"c":3}`},
		{"brace-in-string", `note {"k":"with } inside"} end`, `{"k":"with } inside"}`},
		{"escaped-quote-in-string", `say {"k":"with \"quoted\" text"} end`, `{"k":"with \"quoted\" text"}`},
		{"stray-close-first", `} stray {"a":1}`, `{"a":1}`},
		{"none", `no object here`, ``},
		{"unbalanced-open", `{ "a": 1 `, ``},
	}
	for _, tc := range cases {
		got := extractEmbeddedJSONObject([]byte(tc.in))
		if string(got) != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSuggestPromptTemplate_JSONOnlyEmphasized(t *testing.T) {
	// JSON-only directive must appear at BOTH the start (output format
	// preamble) and the end ("Now emit the JSON object and NOTHING else")
	// so haiku doesn't lose the format hint between the long rules block
	// and the response.
	t.Parallel()
	prompt := suggestPromptTemplate
	if !strings.Contains(prompt, "OUTPUT FORMAT") {
		t.Errorf("prompt missing top-of-prompt OUTPUT FORMAT directive; got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "NOTHING else") {
		t.Errorf("prompt missing closing JSON-only emphasis; got:\n%s", prompt)
	}
	if !strings.Contains(prompt, `no leading "Sure"`) {
		t.Errorf("prompt missing explicit prefix-ban hint; got:\n%s", prompt)
	}
}

func TestParseSuggestOutput_TruncatesOverlong(t *testing.T) {
	// 250 ASCII chars in the suggestion → should clamp to 200.
	long := strings.Repeat("x", 250)
	raw := []byte(`{"suggestion":"` + long + `"}`)
	got, err := parseSuggestOutput(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != strings.Repeat("x", 200) {
		t.Fatalf("expected 200-char truncation, got len=%d", len(got))
	}
}

func TestParseSuggestOutput_TruncatesByRunesNotBytes(t *testing.T) {
	// 250 Chinese chars × 3 bytes/rune = 750 bytes; trimming by bytes would
	// split a UTF-8 sequence and produce invalid output. Verify rune-safe.
	long := strings.Repeat("中", 250)
	raw := []byte(`{"suggestion":"` + long + `"}`)
	got, err := parseSuggestOutput(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if utf8.RuneCountInString(got) != 200 {
		t.Fatalf("expected 200 runes, got %d", utf8.RuneCountInString(got))
	}
	if !utf8.ValidString(got) {
		t.Fatal("rune truncation produced invalid UTF-8")
	}
}

func TestParseSuggestOutput_StripsNewlines(t *testing.T) {
	// Despite "no markdown" prompting, the model occasionally emits a
	// multi-line value. We coerce to a single line.
	raw := []byte(`{"suggestion":"line one\nline two\r\nline three"}`)
	got, err := parseSuggestOutput(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("expected no line breaks, got %q", got)
	}
	if got != "line one line two line three" {
		t.Fatalf("unexpected line-joined output: %q", got)
	}
}

func TestSanitizeSuggestInputs_EscapesClosingTags(t *testing.T) {
	t.Parallel()
	title := "Title with </issue_title> inside"
	desc := "Desc with </issue_description> inside"
	safeT, safeD := sanitizeSuggestInputs(title, desc)
	if strings.Contains(safeT, "</issue_title>") {
		t.Fatalf("title still contains raw closing tag: %q", safeT)
	}
	if !strings.Contains(safeT, "</issue_title_escaped>") {
		t.Fatalf("title missing escaped closing tag: %q", safeT)
	}
	if strings.Contains(safeD, "</issue_description>") {
		t.Fatalf("description still contains raw closing tag: %q", safeD)
	}
	if !strings.Contains(safeD, "</issue_description_escaped>") {
		t.Fatalf("description missing escaped closing tag: %q", safeD)
	}
}

func TestSanitizeSuggestInputs_EscapesOpeningTags(t *testing.T) {
	// Both fields are user-edited; a pasted opening tag is a plausible
	// injection (could create nested/duplicate <issue_title> blocks and
	// confuse the model). Verify all four variants are neutralized.
	t.Parallel()
	title := "Title with <issue_title> inside"
	desc := "Desc with <issue_description> inside"
	safeT, safeD := sanitizeSuggestInputs(title, desc)
	if strings.Contains(safeT, "<issue_title>") {
		t.Fatalf("title still contains raw opening tag: %q", safeT)
	}
	if !strings.Contains(safeT, "<issue_title_escaped>") {
		t.Fatalf("title missing escaped opening tag: %q", safeT)
	}
	if strings.Contains(safeD, "<issue_description>") {
		t.Fatalf("description still contains raw opening tag: %q", safeD)
	}
	if !strings.Contains(safeD, "<issue_description_escaped>") {
		t.Fatalf("description missing escaped opening tag: %q", safeD)
	}
}

func TestSanitizeSuggestInputs_TruncatesByRunes(t *testing.T) {
	t.Parallel()
	title := strings.Repeat("a", suggestTitleCap+50)
	desc := strings.Repeat("b", suggestDescCap+500)
	safeT, safeD := sanitizeSuggestInputs(title, desc)
	if utf8.RuneCountInString(safeT) != suggestTitleCap {
		t.Fatalf("title not truncated to %d runes: got %d", suggestTitleCap, utf8.RuneCountInString(safeT))
	}
	if utf8.RuneCountInString(safeD) != suggestDescCap {
		t.Fatalf("description not truncated to %d runes: got %d", suggestDescCap, utf8.RuneCountInString(safeD))
	}
}

func TestCompressDescription(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"whitespace-only", "  \t\n  \n", ""},
		{"normalize-crlf", "line1\r\nline2\r\nline3", "line1\nline2\nline3"},
		{"normalize-cr-mac", "line1\rline2", "line1\nline2"},
		{"trailing-whitespace-stripped", "alpha   \nbeta\t\n", "alpha\nbeta"},
		{"intra-line-multispace-collapse", "a    b\t\tc", "a b c"},
		{"three-blank-lines-collapsed-to-one", "p1\n\n\n\n\np2", "p1\n\np2"},
		{"preserves-single-blank-line", "p1\n\np2", "p1\n\np2"},
		{"preserves-leading-indent", "code:\n    indented\n    more", "code:\n indented\n more"},
		// 注意 leading-indent 内部多空格被合并成单空格 (markdown 视觉对齐被丢)；
		// 这是 acceptable tradeoff —— goal_condition 推断不需要保留缩进语义。
		{"realistic-markdown", "## 任务\n\n\n实现 /api/health：\n\n- 返回 200\n- 含 status:ok\n\n   \n   \n## 验收\n所有测试通过", "## 任务\n\n实现 /api/health：\n\n- 返回 200\n- 含 status:ok\n\n## 验收\n所有测试通过"},
		{"idempotent", "p1\n\np2", "p1\n\np2"},
	}
	for _, tc := range cases {
		got := compressDescription(tc.in)
		if got != tc.want {
			t.Errorf("%s: compressDescription(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
		// Idempotence: applying twice equals applying once.
		if got2 := compressDescription(got); got2 != got {
			t.Errorf("%s: NOT idempotent — second pass changed %q to %q", tc.name, got, got2)
		}
	}
}

func TestCompressDescription_ReducesTokenWeightOnRealisticInput(t *testing.T) {
	// Sanity bound: a realistic copy-pasted markdown description with the
	// usual padding noise should shrink measurably (>= 10%). The point of
	// this test is not the exact ratio but to fail-loud if a future
	// refactor silently disables one of the compression passes (the
	// threshold trips at 0%); 10% is the floor a single pass typically
	// hits even on modestly noisy input.
	t.Parallel()
	in := strings.Join([]string{
		"## Description   ",
		"",
		"",
		"   ",
		"Implement the `/api/health` endpoint:    ",
		"",
		"- Returns 200 OK",
		"- JSON body {\"status\":\"ok\"}",
		"",
		"",
		"## Acceptance",
		"",
		"",
		"All  unit  tests  pass\t\tand\t\t`make test` exits 0.",
	}, "\n")
	out := compressDescription(in)
	if len(out) >= len(in) {
		t.Fatalf("compressDescription failed to shrink realistic input: in=%d out=%d", len(in), len(out))
	}
	reduction := float64(len(in)-len(out)) / float64(len(in))
	if reduction < 0.10 {
		t.Errorf("expected >=10%% byte reduction on realistic noisy input, got %.1f%% (in=%d out=%d)",
			reduction*100, len(in), len(out))
	}
	t.Logf("compression: %d → %d bytes (%.1f%% reduction)", len(in), len(out), reduction*100)
}

// TestCompressDescription_RegressionTimedOutIssue187 pins the exact
// description body from the issue that triggered "suggest call timed
// out" in production (ws-349, issue 187).
//
// IMPORTANT — this fixture is already clean markdown (one blank line
// between sections, no trailing whitespace, no multi-space alignment),
// so compression is a NO-OP on it (0% reduction). That's correct: the
// production timeout on this input was caused by call latency (cache-miss
// CLI init + API roundtrip > 30s), and was fixed by the 60s timeout bump,
// NOT by compression. Compression earns its keep on noisier descriptions
// — that's exercised by TestCompressDescription_ReducesTokenWeightOnRealisticInput.
//
// What THIS test guards: semantic preservation. A future "smarter"
// compression pass that drops bilingual content, code spans, or
// markdown structure would break LLM grounding. The mustKeep list is
// the contract: every load-bearing token in a real timed-out request
// must survive every form of compression we add.
func TestCompressDescription_RegressionTimedOutIssue187(t *testing.T) {
	t.Parallel()
	// Verbatim paste from the timed-out request. DO NOT reformat —
	// the regression value depends on byte-identical fixture content
	// (whitespace, em-dashes, full/half-width punctuation included).
	in := `## 背景

ws-342 落地后用户痛点：goal_condition 字段是可选的，但大多数用户不会主动写。让 LLM 判停发挥价值的前提是 condition 被填写。

## 目标

在 issue 详情页 'IssueGoalConditionPanel' 加一个 '✨ AI 建议' 按钮：
- 点击后调用 ` + "`claude --print --model haiku`" + ` 子进程（复用 callClaudeJudge 的 spawn pattern）
- 输入：` + "`issue.title`" + ` + ` + "`issue.description`" + `
- 输出：建议的 goal_condition（一行，<200 字符）
- 用户可编辑后保存，或丢弃

## 改动范围

后端：
- 新增 ` + "`server/internal/agentproxy/goal_condition_suggest.go`" + ` —— spawn ` + "`claude -p`" + ` 调用
- 新增 ` + "`POST /api/issues/:id/suggest-goal-condition`" + ` handler（rate-limit per user 防滥用）

前端：
- ` + "`IssueGoalConditionPanel`" + ` 加 'AI 建议' 按钮 + loading state
- 调用上述 endpoint，返回内容填到 textarea（用户决定是否保存）

## 验收

- 单测覆盖 suggest helper + handler
- e2e: 拿一个真实 issue 试一遍
- 成本：单次调用 <$0.003（haiku）

## 相关

- spec §8 演进
- 复用 ` + "`callClaudeJudge`" + ` 的 prompt-injection 防御模式`

	out := compressDescription(in)

	// (1) Semantic preservation: every load-bearing token must survive.
	// These are the bits the model actually grounds its suggestion on —
	// drop any one and the suggestion quality degrades silently.
	mustKeep := []string{
		"## 背景", "## 目标", "## 改动范围", "## 验收", "## 相关",
		"goal_condition",
		"IssueGoalConditionPanel",
		"AI 建议",
		"claude --print --model haiku",
		"issue.title", "issue.description",
		"<200 字符",
		"goal_condition_suggest.go",
		"POST /api/issues/:id/suggest-goal-condition",
		"rate-limit per user",
		"<$0.003",
		"haiku",
		"callClaudeJudge",
		"prompt-injection",
		"ws-342",
	}
	for _, tok := range mustKeep {
		if !strings.Contains(out, tok) {
			t.Errorf("compression dropped semantic token %q from issue 187 description", tok)
		}
	}

	// (2) Post-compression invariants.
	if strings.Contains(out, "\r") {
		t.Errorf("output retains \\r — line-ending normalization failed")
	}
	if strings.Contains(out, "\n\n\n") {
		t.Errorf("output has 3+ consecutive newlines — paragraph collapse failed")
	}
	for _, line := range strings.Split(out, "\n") {
		if line != strings.TrimRight(line, " \t") {
			t.Errorf("output has line with trailing whitespace: %q", line)
		}
	}

	// (3) Idempotence on this exact input.
	if again := compressDescription(out); again != out {
		t.Errorf("compression NOT idempotent on issue 187 description")
	}

	// (4) Compression ratio is observational only on this fixture — see
	// the test docstring. Logging it makes the no-op nature visible so
	// a future reader doesn't expect a number here.
	var reductionPct float64
	if len(in) > 0 {
		reductionPct = float64(len(in)-len(out)) / float64(len(in)) * 100
	}
	t.Logf("issue 187 description: %d → %d bytes (%.1f%% reduction — expected ~0%% since fixture is clean markdown)",
		len(in), len(out), reductionPct)

	// (5) End-to-end: sanitize → compressed AND within cap. Description
	// is comfortably under the 4000-rune cap so the rune cap is a no-op
	// here; this is a sanity check that the full pipeline works.
	_, safeD := sanitizeSuggestInputs("dummy title", in)
	if !strings.Contains(safeD, "goal_condition") {
		t.Error("sanitize pipeline dropped goal_condition keyword")
	}
	if strings.Contains(safeD, "</issue_description>") {
		t.Error("sanitize pipeline must escape </issue_description> tag")
	}
}

func TestSanitizeSuggestInputs_DescriptionGetsCompressed(t *testing.T) {
	// End-to-end: a description with realistic whitespace noise should
	// come out of sanitizeSuggestInputs already whitespace-normalized.
	// Title is untouched by compression (single-line by convention).
	t.Parallel()
	title := "ordinary title"
	desc := "alpha   \n\n\n\nbeta\t\tgamma"
	_, safeD := sanitizeSuggestInputs(title, desc)
	if safeD != "alpha\n\nbeta gamma" {
		t.Errorf("description not compressed; got %q", safeD)
	}
}

func TestSuggestCallTimeout_AtLeast60s(t *testing.T) {
	// Locks in the 2026-05-15 bump from 30s. Production hit timeouts on
	// long-description prompts; 60s gives headroom for cache-miss CLI
	// init (~25s) + JSON-schema retry rounds. Drop below 60s only if
	// you have measured evidence it's safe.
	if oneShotTimeout < 60*time.Second {
		t.Errorf("oneShotTimeout = %v, want >= 60s (regression: was bumped from 30s on 2026-05-15)",
			oneShotTimeout)
	}
}

func TestExtractEnvelopeAPIError_AuthFailure(t *testing.T) {
	// claude 2.x exits 1 when the API returns an error; the envelope is
	// still on stdout. We must surface the human-readable error so the
	// operator sees "API 403: Failed to authenticate ..." instead of a
	// useless "exit status 1".
	t.Parallel()
	raw := []byte(`{"type":"result","subtype":"success","is_error":true,"api_error_status":403,"result":"Failed to authenticate. API Error: 403 Request not allowed"}`)
	got := extractEnvelopeAPIError(raw)
	if got == "" {
		t.Fatal("expected non-empty error message")
	}
	if !strings.Contains(got, "403") {
		t.Errorf("missing status code in error message: %q", got)
	}
	if !strings.Contains(got, "Failed to authenticate") {
		t.Errorf("missing result text in error message: %q", got)
	}
}

func TestExtractEnvelopeAPIError_NoEnvelope(t *testing.T) {
	// Plain text on stdout (e.g., panic output) → return "" so caller
	// falls back to its generic wrap with exec.ExitError details.
	t.Parallel()
	if got := extractEnvelopeAPIError([]byte("kernel panic at 0xdeadbeef")); got != "" {
		t.Errorf("expected empty for non-envelope, got %q", got)
	}
}

func TestExtractEnvelopeAPIError_SuccessEnvelopeIgnored(t *testing.T) {
	// is_error=false envelopes are happy-path output; this function only
	// surfaces error envelopes. Caller's parseSuggestOutput handles
	// success.
	t.Parallel()
	raw := []byte(`{"type":"result","is_error":false,"result":"{\"suggestion\":\"ok\"}"}`)
	if got := extractEnvelopeAPIError(raw); got != "" {
		t.Errorf("expected empty for is_error=false, got %q", got)
	}
}

func TestExtractEnvelopeAPIError_NoAPIErrorStatusFallsBackToResult(t *testing.T) {
	// Some envelopes carry is_error=true without a numeric API status
	// (e.g., local CLI errors). Surface the result text alone.
	t.Parallel()
	raw := []byte(`{"type":"result","is_error":true,"result":"Budget exhausted"}`)
	got := extractEnvelopeAPIError(raw)
	if got != "Budget exhausted" {
		t.Errorf("expected raw result fallback, got %q", got)
	}
}

func TestExtractEnvelopeAPIError_BudgetExceeded(t *testing.T) {
	// claude CLI returns subtype "error_max_budget_usd" with an errors array
	// when --max-budget-usd is exceeded. The envelope has is_error=true but
	// no api_error_status and result is empty — we must fall back to the
	// errors array so the operator sees "Reached maximum budget ($0.20)".
	t.Parallel()
	raw := []byte(`{"type":"result","subtype":"error_max_budget_usd","is_error":true,"errors":["Reached maximum budget ($0.20)"]}`)
	got := extractEnvelopeAPIError(raw)
	if got == "" {
		t.Fatal("expected non-empty error message for budget exceeded")
	}
	if !strings.Contains(got, "maximum budget") {
		t.Errorf("expected budget error text, got %q", got)
	}
}

func TestExtractEnvelopeAPIError_SubtypeOnlyFallback(t *testing.T) {
	// Envelope with is_error=true, no result, no errors array — fall back to
	// the subtype string itself.
	t.Parallel()
	raw := []byte(`{"type":"result","subtype":"error_something","is_error":true}`)
	got := extractEnvelopeAPIError(raw)
	if got != "error_something" {
		t.Errorf("expected subtype fallback, got %q", got)
	}
}

func TestExtractEnvelopeAPIError_SkipsStatusFramesAroundEnvelope(t *testing.T) {
	// Regression: production hit "suggest subprocess: exit status 1" with NO
	// API error surfaced even though stdout carried the envelope, because a
	// plugin / auto-memory handshake emitted `{"status":"ready"}` alongside
	// the result envelope. Naive json.Unmarshal on the multi-object stdout
	// fails; we must scan every balanced top-level object and pick the
	// is_error=true envelope. Mirrors parseSuggestOutput's multi-object scan.
	t.Parallel()
	cases := []struct {
		name string
		raw  string
	}{
		{
			"frame-before",
			`{"status":"ready"}` + "\n" +
				`{"type":"result","is_error":true,"api_error_status":403,"result":"Failed to authenticate. API Error: 403 Request not allowed"}`,
		},
		{
			"frame-after",
			`{"type":"result","is_error":true,"api_error_status":403,"result":"Failed to authenticate. API Error: 403 Request not allowed"}` + "\n" +
				`{"status":"done"}`,
		},
		{
			"frames-on-both-sides",
			`{"status":"ready"}` + "\n" +
				`{"type":"result","is_error":true,"api_error_status":403,"result":"Failed to authenticate. API Error: 403 Request not allowed"}` + "\n" +
				`{"response":"ready"}`,
		},
	}
	for _, tc := range cases {
		got := extractEnvelopeAPIError([]byte(tc.raw))
		if got == "" {
			t.Errorf("%s: expected envelope extracted from multi-object stdout, got empty", tc.name)
			continue
		}
		if !strings.Contains(got, "403") || !strings.Contains(got, "Failed to authenticate") {
			t.Errorf("%s: expected API 403 message, got %q", tc.name, got)
		}
	}
}

func TestExtractEnvelopeAPIError_OnlyStatusFramesReturnsEmpty(t *testing.T) {
	// Sanity: status-frames-only stdout has no error envelope; return ""
	// so the caller falls back to its generic wrap (which now surfaces
	// the raw stdout for diagnosis).
	t.Parallel()
	raw := []byte(`{"status":"ready"}` + "\n" + `{"response":"ready"}`)
	if got := extractEnvelopeAPIError(raw); got != "" {
		t.Errorf("expected empty for status-frames-only stdout, got %q", got)
	}
}

func TestTruncateForError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		in       string
		maxRunes int
		want     string
	}{
		{"empty", "", 100, ""},
		{"whitespace-only", "   \n\t  ", 100, ""},
		{"trims-and-keeps", "  hello  ", 100, "hello"},
		{"under-limit", "short", 100, "short"},
		{"exact-limit", "12345", 5, "12345"},
		{"over-limit-ascii", "0123456789abcdef", 8, "01234567...(truncated)"},
		// Rune-safe slice: 5 Chinese characters fit in maxRunes=5 even though
		// they're 15 bytes — byte truncation would split a UTF-8 sequence.
		{"runes-not-bytes", "中文中文中文中文", 5, "中文中文中...(truncated)"},
	}
	for _, tc := range cases {
		got := truncateForError(tc.in, tc.maxRunes)
		if got != tc.want {
			t.Errorf("%s: truncateForError(%q, %d) = %q, want %q", tc.name, tc.in, tc.maxRunes, got, tc.want)
		}
	}
}

func TestSuggestPromptTemplate_TreatsTitleAndDescriptionEqually(t *testing.T) {
	// Regression: empty description + meaningful title used to fall through
	// to the "too vague" default because the prompt language/inference hook
	// only mentioned description. The prompt must now (a) mention BOTH
	// title and description for language selection, and (b) treat them as
	// equally-weighted inference signals so a title-only issue doesn't
	// get the boilerplate "All new/changed unit tests pass" fallback.
	t.Parallel()
	prompt := suggestPromptTemplate
	if !strings.Contains(prompt, "title and description") {
		t.Errorf("prompt must reference both title AND description for language selection; got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "BOTH title and description") {
		t.Errorf("prompt vague-fallback must require BOTH (not just description) to be vague; got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "if the description is empty, follow the title") {
		t.Errorf("prompt must spell out the empty-description fallback to title; got:\n%s", prompt)
	}
}

func TestSuggestGoalCondition_CLIUnavailable(t *testing.T) {
	prev := cliAvailable.Load()
	cliAvailable.Store(false)
	defer cliAvailable.Store(prev)

	_, err := SuggestGoalCondition(context.Background(), "title", "description", "")
	if err == nil {
		t.Fatal("expected error when CLI unavailable")
	}
	if !strings.Contains(err.Error(), "claude CLI not available") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// TestSuggestGoalCondition_LiveDefaultAccount spawns the real claude CLI
// against the host's default `~/.claude` config (configDir=""), covering
// the end-to-end happy path: flag combinations accepted by the CLI, real
// stdout shape, parseSuggestOutput against a live response.
//
// Gated by NIUNIU_TEST_SUGGEST_LIVE=1 because it (a) spawns a subprocess,
// (b) costs real API tokens (~$0.06/run on haiku per claude's pricing),
// (c) needs working auth in ~/.claude — so `make test` and CI must NOT
// invoke it automatically. Run it manually when validating the spawn
// path or after touching flag/env code in goal_condition_suggest.go.
//
// IMPORTANT — run from a terminal NOT launched by Claude Code itself.
// When the parent shell is a Claude Code session, env markers like
// CLAUDE_CODE_SESSION_ID / CLAUDECODE / AI_AGENT=claude-code_* propagate
// to the spawned child, and the claude CLI's recursion guard returns
// 403 "Failed to authenticate" regardless of credential state at
// ~/.claude. From PowerShell or plain bash:
//
//	NIUNIU_TEST_SUGGEST_LIVE=1 go test -run TestSuggestGoalCondition_LiveDefaultAccount \
//	  -v -count=1 -timeout=2m ./internal/agentproxy/
//
// The matching production failure (auth=403 on a niuniu-managed
// claude-accounts config dir) does NOT reproduce here because we pass
// configDir="" — the test verifies the spawn pipeline, not credential
// management for the per-account config dirs niuniu manages itself.
func TestSuggestGoalCondition_LiveDefaultAccount(t *testing.T) {
	if os.Getenv("NIUNIU_TEST_SUGGEST_LIVE") != "1" {
		t.Skip("set NIUNIU_TEST_SUGGEST_LIVE=1 to run (spawns real claude CLI, costs API tokens)")
	}

	// ProbeClaudeCLI populates cliAvailable; bail early with a clear message
	// if the host doesn't have the CLI or one of the required flags so the
	// failure isn't masked as a 503-style "claude CLI not available" wrap.
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer probeCancel()
	ProbeClaudeCLI(probeCtx)
	if !ClaudeCLIAvailable() {
		t.Fatal("ProbeClaudeCLI failed; install claude CLI and ensure --json-schema flag is present")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// Concrete title + description so the model has enough signal to emit a
	// real criterion (matches the production prompt's "infer from BOTH"
	// rule). Vague inputs trigger the "All new/changed unit tests pass"
	// boilerplate fallback, which is still a valid suggestion but tests
	// less.
	suggestion, err := SuggestGoalCondition(ctx,
		"Add /api/health endpoint",
		"Wire a GET /api/health handler that returns 200 with {\"status\":\"ok\"}. Cover with one unit test.",
		"")
	if err != nil {
		t.Fatalf("SuggestGoalCondition with default account failed: %v", err)
	}
	if strings.TrimSpace(suggestion) == "" {
		t.Fatal("expected non-empty suggestion")
	}
	if n := utf8.RuneCountInString(suggestion); n > suggestMaxRunes {
		t.Errorf("suggestion exceeds %d-rune cap: got %d runes (%q)", suggestMaxRunes, n, suggestion)
	}
	if strings.ContainsAny(suggestion, "\r\n") {
		t.Errorf("suggestion should be single-line, got %q", suggestion)
	}
	t.Logf("live suggestion (%d runes): %s", utf8.RuneCountInString(suggestion), suggestion)
}
