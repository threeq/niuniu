// Package i18n provides minimal, native-layer localization for the desktop
// shell. Window titles and the process app name are set in the OS layer (Wails
// Options.Name / WebviewWindowOptions.Title) where the SPA's i18n is not
// reachable, so the desktop binary needs its own tiny dictionary.
//
// Scope is intentionally small: brand name plus the local/remote/manage labels
// used to assemble window titles. The language is resolved once from the OS
// locale at startup (see DetectLang); changing the system language requires a
// restart to take effect, which is acceptable for native chrome.
package i18n

import "strings"

// Dictionary keys. Kept as exported constants so callers don't pass bare
// strings that can drift from the map below.
const (
	// KeyBrand is the short brand used as the window-title prefix
	// ("牛牛" / "Niuniu"), e.g. "牛牛 · 本地".
	KeyBrand = "BRAND"
	// KeyAppName is the full product/app name ("牛牛桌面版" / "Niuniu Desktop")
	// used for the process app name (Options.Name → macOS Dock / Windows taskbar
	// group) and the tray tooltip — NOT the window title.
	KeyAppName = "APP_NAME"
	KeyLocal   = "LOCAL"
	KeyRemote  = "REMOTE"
	KeyManage  = "MANAGE"
	// KeyAI is the AI-aggregation window label ("AI 直达" / "AI Hub").
	KeyAI = "AI"
	// KeyRunners is the global local-runner manager label
	// ("执行器管理" / "Runner Manage").
	KeyRunners = "RUNNERS"
	// KeyBooting is the local boot-splash heading verb ("启动" / "Starting"),
	// composed with AppName into "启动 牛牛桌面版…" / "Starting Niuniu Desktop…"
	// (see LocalBootHeading).
	KeyBooting = "BOOTING"
	// KeyInitLocalService is the local boot-splash sub-status shown while the
	// embedded server spawns ("正在初始化本地服务" / "Initializing local service").
	KeyInitLocalService = "INIT_LOCAL_SERVICE"
)

// defaultLang is used both as the dictionary fallback and as the result of
// DetectLang when the OS locale can't be resolved or isn't recognized.
const defaultLang = "en"

// sep joins the brand-prefixed title segments: "{BRAND} · {…}". The middle dot
// (U+00B7) with surrounding spaces matches the spec examples
// ("牛牛 · 本地" / "Niuniu · Local").
const sep = " · "

// dict maps lang → key → translation. Add new languages here; DetectLang only
// distinguishes zh from the en default today, but T tolerates any lang present.
var dict = map[string]map[string]string{
	"en": {
		KeyBrand:            "Niuniu",
		KeyAppName:          "Niuniu Desktop",
		KeyLocal:            "Local",
		KeyRemote:           "Remote",
		KeyManage:           "Manage Connections",
		KeyAI:               "AI Hub",
		KeyRunners:          "Runner Manage",
		KeyBooting:          "Starting",
		KeyInitLocalService: "Initializing local service",
	},
	"zh": {
		KeyBrand:            "牛牛",
		KeyAppName:          "牛牛桌面版",
		KeyLocal:            "本地",
		KeyRemote:           "远端",
		KeyManage:           "管理连接",
		KeyAI:               "AI 直达",
		KeyRunners:          "执行器管理",
		KeyBooting:          "启动",
		KeyInitLocalService: "正在初始化本地服务",
	},
}

// T returns the translation for key in lang, falling back to the default
// language (and finally the raw key) when a lang or key is missing.
func T(lang, key string) string {
	if m, ok := dict[lang]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	if v, ok := dict[defaultLang][key]; ok {
		return v
	}
	return key
}

// Normalize maps an arbitrary OS locale string (e.g. "zh-CN", "zh_Hans_CN",
// "en_US.UTF-8") to one of the supported language codes. Anything that begins
// with "zh" is Chinese; everything else (including empty) is the default.
func Normalize(locale string) string {
	l := strings.ToLower(strings.TrimSpace(locale))
	if strings.HasPrefix(l, "zh") {
		return "zh"
	}
	return defaultLang
}

// DetectLang resolves the OS locale (platform-specific, see detect_*.go) and
// normalizes it. Callers should cache the result once at startup.
func DetectLang() string {
	return Normalize(osLocale())
}

// Brand returns the short localized brand ("牛牛" / "Niuniu") used as the
// window-title prefix.
func Brand(lang string) string {
	return T(lang, KeyBrand)
}

// AppName returns the full product/app name ("牛牛桌面版" / "Niuniu Desktop")
// for the process app name (Options.Name → macOS Dock / Windows taskbar group)
// and the tray tooltip.
func AppName(lang string) string {
	return T(lang, KeyAppName)
}

// LocalTitle is the title for the local main window: "{BRAND} · {LOCAL}".
func LocalTitle(lang string) string {
	return T(lang, KeyBrand) + sep + T(lang, KeyLocal)
}

// LocalBootHeading is the local boot-splash heading: "{BOOTING} {APP_NAME}…"
// ("启动 牛牛桌面版…" / "Starting Niuniu Desktop…"), shown while the embedded
// server spawns.
func LocalBootHeading(lang string) string {
	return T(lang, KeyBooting) + " " + AppName(lang) + "…"
}

// RemoteTitle is the title for a remote connection window:
// "{BRAND} · {REMOTE} · {name} ({hostPort})".
func RemoteTitle(lang, name, hostPort string) string {
	return T(lang, KeyBrand) + sep + T(lang, KeyRemote) + sep + name + " (" + hostPort + ")"
}

// ManageTitle is the title for the connection-manager (picker) window:
// "{BRAND} · {MANAGE}".
func ManageTitle(lang string) string {
	return T(lang, KeyBrand) + sep + T(lang, KeyManage)
}

// AITitle is the title for the AI-aggregation hub window: "{BRAND} · {AI}".
func AITitle(lang string) string {
	return T(lang, KeyBrand) + sep + T(lang, KeyAI)
}

// RunnersTitle is the title for the global local-runner manager window:
// "{BRAND} - {RUNNERS}" ("牛牛 - 执行器管理" / "Niuniu - Runner Manage"). Uses a
// hyphen separator (not the middle dot the other windows use) per the spec'd
// label.
func RunnersTitle(lang string) string {
	return T(lang, KeyBrand) + " - " + T(lang, KeyRunners)
}

// AIServiceTitle is the title for one AI service's webview window:
// "{BRAND} · {AI} · {name}".
func AIServiceTitle(lang, name string) string {
	return T(lang, KeyBrand) + sep + T(lang, KeyAI) + sep + name
}
