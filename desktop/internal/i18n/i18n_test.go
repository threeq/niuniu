package i18n

import "testing"

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"zh":          "zh",
		"zh-CN":       "zh",
		"zh_Hans_CN":  "zh",
		"ZH-cn":       "zh",
		"zh_CN.UTF-8": "zh",
		"  zh-TW  ":   "zh",
		"en":          "en",
		"en-US":       "en",
		"en_US.UTF-8": "en",
		"fr-FR":       "en", // unsupported → default
		"":            "en",
		"de":          "en",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTFallback(t *testing.T) {
	// Known lang/key.
	if got := T("zh", KeyBrand); got != "牛牛" {
		t.Errorf("T(zh, BRAND) = %q, want 牛牛", got)
	}
	if got := T("zh", KeyAppName); got != "牛牛桌面版" {
		t.Errorf("T(zh, APP_NAME) = %q, want 牛牛桌面版", got)
	}
	if got := T("en", KeyManage); got != "Manage Connections" {
		t.Errorf("T(en, MANAGE) = %q", got)
	}
	// Unknown lang → falls back to en.
	if got := T("fr", KeyLocal); got != "Local" {
		t.Errorf("T(fr, LOCAL) = %q, want en fallback Local", got)
	}
	// Unknown key → returns the raw key.
	if got := T("en", "NOPE"); got != "NOPE" {
		t.Errorf("T(en, NOPE) = %q, want raw key", got)
	}
}

func TestTitleAssembly(t *testing.T) {
	cases := []struct {
		lang             string
		local, manage    string
		remoteName, addr string
		remote           string
		brand            string
		appName          string
	}{
		{
			lang:       "zh",
			local:      "牛牛 · 本地",
			manage:     "牛牛 · 管理连接",
			remoteName: "公司机", addr: "192.168.1.20:3000",
			remote:  "牛牛 · 远端 · 公司机 (192.168.1.20:3000)",
			brand:   "牛牛",
			appName: "牛牛桌面版",
		},
		{
			lang:       "en",
			local:      "Niuniu · Local",
			manage:     "Niuniu · Manage Connections",
			remoteName: "Office", addr: "192.168.1.20:3000",
			remote:  "Niuniu · Remote · Office (192.168.1.20:3000)",
			brand:   "Niuniu",
			appName: "Niuniu Desktop",
		},
	}
	for _, c := range cases {
		if got := LocalTitle(c.lang); got != c.local {
			t.Errorf("LocalTitle(%s) = %q, want %q", c.lang, got, c.local)
		}
		if got := ManageTitle(c.lang); got != c.manage {
			t.Errorf("ManageTitle(%s) = %q, want %q", c.lang, got, c.manage)
		}
		if got := RemoteTitle(c.lang, c.remoteName, c.addr); got != c.remote {
			t.Errorf("RemoteTitle(%s) = %q, want %q", c.lang, got, c.remote)
		}
		if got := Brand(c.lang); got != c.brand {
			t.Errorf("Brand(%s) = %q, want %q", c.lang, got, c.brand)
		}
		if got := AppName(c.lang); got != c.appName {
			t.Errorf("AppName(%s) = %q, want %q", c.lang, got, c.appName)
		}
	}
}

func TestDetectLangReturnsSupported(t *testing.T) {
	// Can't assert a specific value cross-platform, but it must be one of the
	// supported codes and never empty.
	got := DetectLang()
	if got != "zh" && got != "en" {
		t.Errorf("DetectLang() = %q, want zh or en", got)
	}
}
