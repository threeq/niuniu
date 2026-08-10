package main

import (
	"net/url"
	"strings"

	"github.com/niuniu-dev/niuniu-desktop/internal/config"
)

// aiservices.go holds the built-in AI web-service catalog for the AI-aggregation
// window and the pure (Wails-free) helpers that merge it with the user's custom
// services / hidden list. Window plumbing lives in aiwin.go; everything here is
// unit-testable without a running app.
//
// Category is a stable code key (not a display label) so the frontend can
// localize it. Service Name keeps the product's own brand casing (proper noun).
const (
	catChat   = "chat"   // 对话大模型
	catImage  = "image"  // 图文 / 图像生成
	catVideo  = "video"  // 视频生成
	catCustom = "custom" // 用户自定义
)

// aiCategoryOrder is the display order of built-in categories in the rail.
var aiCategoryOrder = []string{catChat, catImage, catVideo, catCustom}

// AIServiceDef is a code-defined (built-in) service. IDs are stable slugs so a
// user's default/last/hidden preferences survive across releases.
type AIServiceDef struct {
	ID       string
	Name     string
	URL      string
	Category string
}

// builtinAIServices returns the recommended default catalog (spec §"内置服务清单").
// URLs may drift with site redesigns / regional differences — the user can add
// or hide services, so this is a starting set, not a hard contract.
func builtinAIServices() []AIServiceDef {
	return []AIServiceDef{
		// 对话大模型
		{ID: "chatgpt", Name: "ChatGPT", URL: "https://chatgpt.com", Category: catChat},
		// Claude / Perplexity are intentionally omitted: their Cloudflare Turnstile
		// aggressively re-challenges an embedded WebView2 session, making them a poor
		// in-app experience. Users can still add them as custom services if desired.
		{ID: "gemini", Name: "Gemini", URL: "https://gemini.google.com", Category: catChat},
		{ID: "grok", Name: "Grok", URL: "https://grok.com", Category: catChat},
		{ID: "deepseek", Name: "DeepSeek", URL: "https://chat.deepseek.com", Category: catChat},
		{ID: "kimi", Name: "Kimi", URL: "https://www.kimi.com", Category: catChat},
		{ID: "tongyi", Name: "通义千问", URL: "https://www.tongyi.com", Category: catChat},
		{ID: "doubao", Name: "豆包", URL: "https://www.doubao.com", Category: catChat},
		{ID: "yiyan", Name: "文心一言", URL: "https://yiyan.baidu.com", Category: catChat},
		{ID: "yuanbao", Name: "腾讯元宝", URL: "https://yuanbao.tencent.com", Category: catChat},
		{ID: "chatglm", Name: "智谱清言", URL: "https://chatglm.cn", Category: catChat},

		// 图文 / 图像生成
		{ID: "midjourney", Name: "Midjourney", URL: "https://www.midjourney.com", Category: catImage},
		{ID: "ideogram", Name: "Ideogram", URL: "https://ideogram.ai", Category: catImage},
		{ID: "jimeng", Name: "即梦", URL: "https://jimeng.jianying.com", Category: catImage},
		{ID: "wanxiang", Name: "通义万相", URL: "https://tongyi.aliyun.com/wanxiang", Category: catImage},

		// 视频生成
		{ID: "runway", Name: "Runway", URL: "https://runwayml.com", Category: catVideo},
		{ID: "kling", Name: "可灵 Kling", URL: "https://klingai.com", Category: catVideo},
		{ID: "hailuo", Name: "海螺 Hailuo", URL: "https://hailuoai.com", Category: catVideo},
		{ID: "vidu", Name: "Vidu", URL: "https://www.vidu.studio", Category: catVideo},
	}
}

// AIServiceView is the frontend-facing shape of a service: the definition plus
// derived/state fields (favicon, custom flag, default/last markers).
type AIServiceView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Category  string `json:"category"`
	Favicon   string `json:"favicon"`
	Custom    bool   `json:"custom"`
	IsBuiltin bool   `json:"is_builtin"`
	IsDefault bool   `json:"is_default"`
	IsLast    bool   `json:"is_last"`
}

// faviconURL derives a best-effort logo URL from a service URL: the site's own
// origin + "/favicon.ico". This is the "auto-fetch site logo" mechanism for
// both built-in and custom services — no third-party (Google/DuckDuckGo) favicon
// proxy, which would be unreliable behind the GFW. The frontend falls back to a
// letter badge if the favicon 404s. Returns "" when the URL can't be parsed.
func faviconURL(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/favicon.ico"
}

// mergeAIServices produces the ordered, filtered view list shown in the rail:
// built-ins (minus the user's hidden set) followed by custom services, each
// annotated with favicon + default/last markers. Pure function — no app state.
func mergeAIServices(builtins []AIServiceDef, cfg config.AIConfig) []AIServiceView {
	out := make([]AIServiceView, 0, len(builtins)+len(cfg.CustomServices))
	for _, b := range builtins {
		if cfg.IsBuiltinHidden(b.ID) {
			continue
		}
		out = append(out, AIServiceView{
			ID:        b.ID,
			Name:      b.Name,
			URL:       b.URL,
			Category:  b.Category,
			Favicon:   faviconURL(b.URL),
			IsBuiltin: true,
			IsDefault: b.ID == cfg.DefaultServiceID,
			IsLast:    b.ID == cfg.LastServiceID,
		})
	}
	for _, c := range cfg.CustomServices {
		out = append(out, AIServiceView{
			ID:        c.ID,
			Name:      c.Name,
			URL:       c.URL,
			Category:  catCustom,
			Favicon:   faviconURL(c.URL),
			Custom:    true,
			IsDefault: c.ID == cfg.DefaultServiceID,
			IsLast:    c.ID == cfg.LastServiceID,
		})
	}
	return out
}

// findAIService resolves a service ID to its URL and display name across the
// merged built-in + custom set. ok is false when the ID is unknown or hidden.
func findAIService(builtins []AIServiceDef, cfg config.AIConfig, id string) (svc AIServiceView, ok bool) {
	for _, v := range mergeAIServices(builtins, cfg) {
		if v.ID == id {
			return v, true
		}
	}
	return AIServiceView{}, false
}

// normalizeServiceURL trims a user-entered service URL and prepends https:// if
// no scheme is present, so "chat.example.com" becomes a loadable URL. Returns
// "" for blank input.
func normalizeServiceURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	return s
}
