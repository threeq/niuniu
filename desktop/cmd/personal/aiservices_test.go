package main

import (
	"net/url"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu-desktop/internal/config"
)

func TestBuiltinAIServicesInvariants(t *testing.T) {
	svcs := builtinAIServices()
	if len(svcs) == 0 {
		t.Fatal("builtin catalog is empty")
	}
	validCat := map[string]bool{catChat: true, catImage: true, catVideo: true}
	seen := map[string]bool{}
	var haveChat, haveImage, haveVideo bool
	for _, s := range svcs {
		if s.ID == "" || s.Name == "" || s.URL == "" {
			t.Errorf("service has empty field: %+v", s)
		}
		if seen[s.ID] {
			t.Errorf("duplicate service ID: %s", s.ID)
		}
		seen[s.ID] = true
		if !validCat[s.Category] {
			t.Errorf("service %s has invalid category %q", s.ID, s.Category)
		}
		if u, err := url.Parse(s.URL); err != nil || u.Scheme == "" || u.Host == "" {
			t.Errorf("service %s has unparseable URL %q", s.ID, s.URL)
		}
		switch s.Category {
		case catChat:
			haveChat = true
		case catImage:
			haveImage = true
		case catVideo:
			haveVideo = true
		}
	}
	// Spec requires coverage of 对话/图文/视频, international + domestic.
	if !haveChat || !haveImage || !haveVideo {
		t.Errorf("catalog missing a category: chat=%v image=%v video=%v", haveChat, haveImage, haveVideo)
	}
}

func TestFaviconURL(t *testing.T) {
	cases := map[string]string{
		"https://chatgpt.com":                "https://chatgpt.com/favicon.ico",
		"https://tongyi.aliyun.com/wanxiang": "https://tongyi.aliyun.com/favicon.ico",
		"http://example.com:8080/path?q=1":   "http://example.com:8080/favicon.ico",
		"":                                   "",
		"not a url":                          "",
		"ftp://":                             "",
	}
	for in, want := range cases {
		if got := faviconURL(in); got != want {
			t.Errorf("faviconURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeServiceURL(t *testing.T) {
	cases := map[string]string{
		"chat.example.com": "https://chat.example.com",
		"https://x.com":    "https://x.com",
		"http://y.com":     "http://y.com",
		"  spaced.com  ":   "https://spaced.com",
		"":                 "",
		"   ":              "",
	}
	for in, want := range cases {
		if got := normalizeServiceURL(in); got != want {
			t.Errorf("normalizeServiceURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMergeAIServices(t *testing.T) {
	builtins := builtinAIServices()
	cfg := config.AIConfig{
		HiddenBuiltins:   []string{"chatgpt"},
		DefaultServiceID: "deepseek",
		LastServiceID:    "gemini",
		CustomServices: []config.AIService{
			{ID: "ai-1", Name: "Mine", URL: "https://mine.example.com"},
		},
	}
	merged := mergeAIServices(builtins, cfg)

	// Hidden builtin excluded; custom appended with catCustom.
	var sawChatgpt, sawCustom bool
	var defaultCount, lastCount int
	for _, v := range merged {
		if v.ID == "chatgpt" {
			sawChatgpt = true
		}
		if v.ID == "ai-1" {
			sawCustom = true
			if v.Category != catCustom || !v.Custom {
				t.Errorf("custom service not tagged: %+v", v)
			}
			if v.Favicon != "https://mine.example.com/favicon.ico" {
				t.Errorf("custom favicon = %q", v.Favicon)
			}
		}
		if v.IsDefault {
			defaultCount++
			if v.ID != "deepseek" {
				t.Errorf("wrong default: %s", v.ID)
			}
		}
		if v.IsLast {
			lastCount++
			if v.ID != "gemini" {
				t.Errorf("wrong last: %s", v.ID)
			}
		}
	}
	if sawChatgpt {
		t.Error("hidden builtin chatgpt should be excluded")
	}
	if !sawCustom {
		t.Error("custom service missing from merge")
	}
	if defaultCount != 1 || lastCount != 1 {
		t.Errorf("default/last markers off: default=%d last=%d", defaultCount, lastCount)
	}
	if len(merged) != len(builtins)-1+1 {
		t.Errorf("merged length = %d, want %d", len(merged), len(builtins))
	}
}

func TestFindAIService(t *testing.T) {
	builtins := builtinAIServices()
	cfg := config.AIConfig{HiddenBuiltins: []string{"grok"}}

	if svc, ok := findAIService(builtins, cfg, "deepseek"); !ok || !strings.Contains(svc.URL, "deepseek.com") {
		t.Errorf("findAIService(deepseek) = %+v, ok=%v", svc, ok)
	}
	if _, ok := findAIService(builtins, cfg, "grok"); ok {
		t.Error("hidden service should not be found")
	}
	if _, ok := findAIService(builtins, cfg, "nope"); ok {
		t.Error("unknown service should not be found")
	}
}
