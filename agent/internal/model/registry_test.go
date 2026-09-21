package model

import (
	"context"
	"strings"
	"testing"
)

func TestRegistryBuiltins(t *testing.T) {
	names := ProviderNames()
	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}
	if !found["anthropic"] || !found["openai"] {
		t.Fatalf("builtins missing: %v", names)
	}
	// 可构建。
	m1, err := NewForProvider("anthropic", Config{Provider: ProviderAnthropic, AuthToken: "t", Model: "m"})
	if err != nil || m1 == nil {
		t.Fatalf("anthropic factory: %v", err)
	}
	m2, err := NewForProvider("openai", Config{Provider: ProviderOpenAI, APIKey: "k", Model: "m"})
	if err != nil || m2 == nil {
		t.Fatalf("openai factory: %v", err)
	}
	// 缺凭据报错。
	if _, err := NewForProvider("anthropic", Config{Provider: ProviderAnthropic, Model: "m"}); err == nil {
		t.Error("want credential error")
	}
	// 未知名字。
	_, err = NewForProvider("gemini", Config{})
	if err == nil || !strings.Contains(err.Error(), "registered") {
		t.Errorf("err = %v, want unknown-provider with registry list", err)
	}
}

// 自定义家族：实现 Model 接口注册即可被 profile 引用。
type stubVendor struct{}

func (stubVendor) Complete(_ context.Context, _ Request) (*Response, error) {
	return &Response{Message: Message{Role: RoleAssistant, Blocks: []Block{{Type: BlockText, Text: "hi"}}}}, nil
}

func TestRegistryCustomFamily(t *testing.T) {
	RegisterProvider("stubvendor", func(cfg Config) (Model, error) { return stubVendor{}, nil })
	m, err := NewForProvider("stubvendor", Config{Provider: "stubvendor"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := m.Complete(testCtx(), Request{})
	if err != nil || resp.Message.Text() != "hi" {
		t.Fatalf("resp = %+v err = %v", resp, err)
	}
}

func TestDefaultThinkingTier(t *testing.T) {
	cases := map[string]string{
		"GLM-5.3-Flash":   "medium",
		"kimi-k2":         "medium",
		"DeepSeek-R2":     "high",
		"qwen3-coder":     "low",
		"claude-sonnet-5": "",
	}
	for model, want := range cases {
		if got := DefaultThinkingTier(model); got != want {
			t.Errorf("DefaultThinkingTier(%q) = %q, want %q", model, got, want)
		}
	}
}
