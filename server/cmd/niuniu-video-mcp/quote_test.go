package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pricedRegistry is a fully-configured registry with known prices.
func pricedRegistry() *Registry {
	return &Registry{
		Problems: map[string]string{},
		TTS: &TTSBinding{
			Name: BackendOpenAICompat, Model: "tts-1",
			Price: PriceMeta{Unit: "元/千字符", UnitPrice: 0.02, Per: 1000, Configured: true},
		},
		Image: &ImageBinding{
			Name: BackendOpenAICompat, Model: "gpt-image-1",
			Price: PriceMeta{Unit: "元/张", UnitPrice: 0.5, Per: 1, Configured: true},
		},
		Video: &VideoBinding{
			Name: BackendSeedance, Model: "seedance-pro",
			Price: PriceMeta{Unit: "元/秒", UnitPrice: 0.8, Per: 1, Configured: true},
		},
	}
}

func TestBuildQuoteAggregatesItems(t *testing.T) {
	q, err := buildQuote(pricedRegistry(), []quoteRequestItem{
		{Capability: CapTTS, Quantity: 2000, Label: "旁白"},
		{Capability: CapImage, Quantity: 2},
		{Capability: CapVideo, Quantity: 10},
	}, "标准", "测试报价")
	if err != nil {
		t.Fatalf("buildQuote: %v", err)
	}
	if len(q.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(q.Items))
	}
	// 2000 chars / 1000 * 0.02 = 0.04
	if got := q.Items[0].Amount; got != 0.04 {
		t.Errorf("tts amount = %v, want 0.04", got)
	}
	if got := q.Items[1].Amount; got != 1.0 {
		t.Errorf("image amount = %v, want 1.0", got)
	}
	if got := q.Items[2].Amount; got != 8.0 {
		t.Errorf("video amount = %v, want 8.0", got)
	}
	if got := q.Total; got != 9.04 {
		t.Errorf("total = %v, want 9.04", got)
	}
	if len(q.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", q.Warnings)
	}
	for _, it := range q.Items {
		if !it.PriceConfigured {
			t.Errorf("item %s 应标记 price_configured", it.Capability)
		}
	}
	if q.QualityTier != "标准" || q.Note != "测试报价" || q.Currency != "CNY" {
		t.Errorf("报价单头部字段不符: %+v", q)
	}
	if !strings.HasPrefix(q.QuoteID, "q-") {
		t.Errorf("quote_id = %q, want q- 前缀", q.QuoteID)
	}
}

func TestBuildQuoteUnconfiguredPriceIsHonest(t *testing.T) {
	reg := pricedRegistry()
	reg.Video.Price = PriceMeta{Unit: "元/秒", Per: 1, Note: "未配置视频单价：请在能力配置中设置 price（元/秒）。"}
	q, err := buildQuote(reg, []quoteRequestItem{{Capability: CapVideo, Quantity: 5}}, "", "")
	if err != nil {
		t.Fatalf("buildQuote: %v", err)
	}
	item := q.Items[0]
	if item.PriceConfigured {
		t.Error("未配置单价时 price_configured 必须为 false（不得编造价钱）")
	}
	if item.Amount != 0 || q.Total != 0 {
		t.Errorf("未配置单价时金额应为 0，得到 amount=%v total=%v", item.Amount, q.Total)
	}
	if len(q.Warnings) == 0 {
		t.Error("未配置单价必须产出 warning")
	}
	if item.Note == "" {
		t.Error("未配置单价必须带 note 说明如何配置")
	}
}

func TestBuildQuoteExplicitPriceWins(t *testing.T) {
	q, err := buildQuote(pricedRegistry(), []quoteRequestItem{
		{Capability: CapImage, Quantity: 3, UnitPrice: floatPtr(1.5), Unit: "元/张(自定义)"},
	}, "", "")
	if err != nil {
		t.Fatalf("buildQuote: %v", err)
	}
	if got := q.Items[0].Amount; got != 4.5 {
		t.Errorf("amount = %v, want 4.5（显式单价优先）", got)
	}
	if q.Items[0].Unit != "元/张(自定义)" {
		t.Errorf("unit = %q, want 调用方给定值", q.Items[0].Unit)
	}
}

func TestBuildQuoteBackendMismatchWarns(t *testing.T) {
	q, err := buildQuote(pricedRegistry(), []quoteRequestItem{
		{Capability: CapVideo, Quantity: 5, Backend: "other-vendor"},
	}, "", "")
	if err != nil {
		t.Fatalf("buildQuote: %v", err)
	}
	if q.Items[0].Backend != "other-vendor" {
		t.Errorf("backend = %q, want other-vendor", q.Items[0].Backend)
	}
	if q.Items[0].PriceConfigured {
		t.Error("非当前配置的后端不得沿用当前后端价钱")
	}
	if len(q.Warnings) == 0 {
		t.Error("后端不一致必须产出 warning")
	}
}

func TestBuildQuoteValidationErrors(t *testing.T) {
	cases := []struct {
		name  string
		items []quoteRequestItem
		want  string
	}{
		{"空清单", nil, "至少一个"},
		{"数量为 0", []quoteRequestItem{{Capability: CapImage, Quantity: 0}}, "quantity 必须为正数"},
		{"能力非法", []quoteRequestItem{{Capability: "audio", Quantity: 1}}, "必须是 tts / image / video"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildQuote(pricedRegistry(), tc.items, "", "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want 含 %q", err, tc.want)
			}
		})
	}
}

func TestBuildQuoteRequiresConfiguredCapability(t *testing.T) {
	reg := &Registry{Problems: map[string]string{}}
	_, err := buildQuote(reg, []quoteRequestItem{{Capability: CapImage, Quantity: 1}}, "", "")
	if err == nil {
		t.Fatal("未配置图像能力时应报错")
	}
	if !strings.Contains(err.Error(), "设置→能力配置") {
		t.Errorf("错误信息应指向设置→能力配置，实得：%v", err)
	}
}

func TestQuotePersistenceAndLedger(t *testing.T) {
	ws := testWS(t)
	q, err := buildQuote(pricedRegistry(), []quoteRequestItem{{Capability: CapImage, Quantity: 2}}, "", "")
	if err != nil {
		t.Fatalf("buildQuote: %v", err)
	}
	rel, err := writeQuote(ws, q)
	if err != nil {
		t.Fatalf("writeQuote: %v", err)
	}
	if want := filepath.ToSlash(filepath.Join("video-project", "quotes", q.QuoteID+".json")); rel != want {
		t.Errorf("rel path = %q, want %q", rel, want)
	}
	qpath, err := quotePath(ws, q.QuoteID)
	if err != nil {
		t.Fatalf("quotePath: %v", err)
	}
	raw, err := os.ReadFile(qpath)
	if err != nil {
		t.Fatalf("读取报价单: %v", err)
	}
	var back Quote
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("报价单不是合法 JSON: %v", err)
	}
	if back.Total != q.Total || len(back.Items) != 1 {
		t.Errorf("持久化后报价单不一致: %+v", back)
	}

	if err := requireQuote(ws, q.QuoteID); err != nil {
		t.Errorf("requireQuote 已存在的单子报错: %v", err)
	}
	if err := requireQuote(ws, ""); err == nil || !strings.Contains(err.Error(), "quote_estimate") {
		t.Errorf("缺 quote_id 时的错误应指引调用 quote_estimate，实得：%v", err)
	}
	if err := requireQuote(ws, "q-20260927-153045-0000"); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Errorf("格式合法但不存在的报价单应报「不存在」，实得：%v", err)
	}
	if err := requireQuote(ws, "q-nope"); err == nil || !strings.Contains(err.Error(), "格式不合法") {
		t.Errorf("非法格式的 quote_id 应报「格式不合法」，实得：%v", err)
	}

	// Ledger: submitted (pre-call trace) then finalized.
	if err := appendCall(ws, q.QuoteID, CallRecord{Capability: CapImage, Quantity: 2, Status: "submitted"}); err != nil {
		t.Fatalf("appendCall: %v", err)
	}
	ledger, err := loadLedger(ws, q.QuoteID)
	if err != nil {
		t.Fatalf("loadLedger: %v", err)
	}
	if len(ledger.Calls) != 1 || ledger.Calls[0].Status != "submitted" {
		t.Fatalf("留痕初态不符: %+v", ledger.Calls)
	}
	callID := ledger.Calls[0].CallID
	if callID == "" {
		t.Fatal("留痕缺少 call_id")
	}
	if got := lastCallID(ws, q.QuoteID); got != callID {
		t.Errorf("lastCallID = %q, want %q", got, callID)
	}
	if err := finishCall(ws, q.QuoteID, callID, func(r *CallRecord) {
		r.Status = "succeeded"
		r.Files = "video-project/assets/img-01-1.png"
	}); err != nil {
		t.Fatalf("finishCall: %v", err)
	}
	ledger, _ = loadLedger(ws, q.QuoteID)
	if ledger.Calls[0].Status != "succeeded" || ledger.Calls[0].FinishedAt == "" {
		t.Errorf("留痕终态不符: %+v", ledger.Calls[0])
	}
	if err := finishCall(ws, q.QuoteID, "c-missing", func(r *CallRecord) {}); err == nil {
		t.Error("finalize 不存在的 call_id 应报错")
	}
}

// TestQuoteIDRejectsTraversal is the regression test for the review finding:
// quoteID lands in a file name, so a traversal-shaped id must be rejected
// before any path is built (and nothing may be written outside the workspace).
func TestQuoteIDRejectsTraversal(t *testing.T) {
	ws := testWS(t)
	bad := []string{
		"../../escape",
		`..\..\escape`,
		"q-20260927-153045-1a2b/../../escape",
		"sub/dir",
		`sub\dir`,
		"q-nope",
		"q-20260927-153045-1A2B", // 大写十六进制不是本模块产出的 id
		"q-20260927-153045-1a2",  // 长度不符
		"",
		"q-../../etc/passwd",
	}
	for _, id := range bad {
		if p, err := quotePath(ws, id); err == nil {
			t.Errorf("quotePath(%q) 应被拒绝，实得 %q", id, p)
		} else if !strings.Contains(err.Error(), "格式不合法") {
			t.Errorf("quotePath(%q) 错误信息应说明格式不合法：%v", id, err)
		}
		if p, err := ledgerPath(ws, id); err == nil {
			t.Errorf("ledgerPath(%q) 应被拒绝，实得 %q", id, p)
		}
		if id == "" {
			continue // appendCall 对空 id 是既定的 no-op（无留痕需求）
		}
		if err := appendCall(ws, id, CallRecord{Capability: CapImage}); err == nil {
			t.Errorf("appendCall(%q) 应被拒绝", id)
		}
		if _, err := loadLedger(ws, id); err == nil {
			t.Errorf("loadLedger(%q) 应被拒绝", id)
		}
	}
	for _, name := range []string{"escape.json", "escape.calls.json"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(ws), name)); err == nil {
			t.Errorf("非法 quote_id 不应写出工作空间外的文件 %s", name)
		}
	}

	// 正样本：newQuoteID 产出的 id 可用，且路径收敛在 quotes/ 内。
	good := newQuoteID()
	if !validQuoteID(good) {
		t.Fatalf("newQuoteID 产出的 id %q 未通过校验", good)
	}
	p, err := quotePath(ws, good)
	if err != nil {
		t.Fatalf("合法 id 被拒: %v", err)
	}
	if !withinDir(projectPath(ws, "quotes"), p) {
		t.Errorf("quotePath 越出 quotes 目录: %s", p)
	}
	if want := projectPath(ws, "quotes", good+".json"); p != want {
		t.Errorf("quotePath = %q, want %q", p, want)
	}
	lp, err := ledgerPath(ws, good)
	if err != nil || !withinDir(projectPath(ws, "quotes"), lp) {
		t.Errorf("ledgerPath = %q, %v", lp, err)
	}
}

func TestBuildRegistryFromEnv(t *testing.T) {
	client := &http.Client{}

	t.Run("全空=三个能力都未配置且无问题串", func(t *testing.T) {
		reg := BuildRegistry(envMap(nil), client)
		if reg.TTS != nil || reg.Image != nil || reg.Video != nil {
			t.Error("空环境下不应有绑定")
		}
		if got := reg.ProblemSummary(); got != "" {
			t.Errorf("空环境不应有问题串，实得：%s", got)
		}
		msg := reg.UnconfiguredMessage(CapVideo, "视频")
		if !strings.Contains(msg, "设置→能力配置") {
			t.Errorf("未配置提示应指向设置→能力配置，实得：%s", msg)
		}
	})

	t.Run("tts 缺 Base URL 时给出精确问题", func(t *testing.T) {
		reg := BuildRegistry(envMap(map[string]string{"NN_CAP_TTS_BACKEND": "openai-compat"}), client)
		if reg.TTS != nil {
			t.Error("缺 Base URL 不应完成绑定")
		}
		if p := reg.Problem(CapTTS); !strings.Contains(p, "Base URL") {
			t.Errorf("问题串 = %q, want 含 Base URL", p)
		}
	})

	t.Run("tts 完整配置含价格元数据", func(t *testing.T) {
		reg := BuildRegistry(envMap(map[string]string{
			"NN_CAP_TTS_BACKEND":    "openai-compat",
			"NN_CAP_TTS_BASE_URL":   "https://tts.example.com/v1",
			"NN_CAP_TTS_API_KEY":    "sk-test",
			"NN_CAP_TTS_MODEL":      "my-tts",
			"NN_CAP_TTS_VOICE":      "nova",
			"NN_CAP_TTS_PRICE":      "0.03",
			"NN_CAP_TTS_PRICE_UNIT": "元/千字符",
		}), client)
		if reg.TTS == nil {
			t.Fatal("完整配置应完成绑定")
		}
		if reg.TTS.Model != "my-tts" || reg.TTS.DefaultVoice != "nova" {
			t.Errorf("model/voice = %q/%q, want my-tts/nova", reg.TTS.Model, reg.TTS.DefaultVoice)
		}
		if reg.TTS.Price.UnitPrice != 0.03 || !reg.TTS.Price.Configured {
			t.Errorf("价格元数据 = %+v, want 0.03/configured", reg.TTS.Price)
		}
		if reg.TTS.Price.Per != 1000 {
			t.Errorf("Per = %v, want 1000", reg.TTS.Price.Per)
		}
	})

	t.Run("未知后端=明确问题而非启动失败", func(t *testing.T) {
		reg := BuildRegistry(envMap(map[string]string{"NN_CAP_IMAGE_BACKEND": "midjourney-magic"}), client)
		if reg.Image != nil {
			t.Error("未知后端不应绑定")
		}
		if p := reg.Problem(CapImage); !strings.Contains(p, "不支持") {
			t.Errorf("问题串 = %q, want 含「不支持」", p)
		}
	})

	t.Run("seedance 默认 Base URL", func(t *testing.T) {
		reg := BuildRegistry(envMap(map[string]string{
			"NN_CAP_VIDEO_BACKEND": "seedance",
			"NN_CAP_VIDEO_API_KEY": "ark-key",
		}), client)
		if reg.Video == nil || reg.Video.Name != BackendSeedance {
			t.Fatalf("video binding = %+v", reg.Video)
		}
		sb, ok := reg.Video.Backend.(*SeedanceBackend)
		if !ok {
			t.Fatalf("backend 类型 = %T", reg.Video.Backend)
		}
		if sb.baseURL != "https://ark.cn-beijing.volces.com" {
			t.Errorf("baseURL = %q", sb.baseURL)
		}
		if reg.Video.Model != defaultSeedanceModel {
			t.Errorf("model = %q, want %q", reg.Video.Model, defaultSeedanceModel)
		}
		if reg.Video.Price.Configured {
			t.Error("未给 price 时价格应保持未配置（不得编造价钱）")
		}
	})

	t.Run("kling 接受 ak:sk 打包形式", func(t *testing.T) {
		reg := BuildRegistry(envMap(map[string]string{
			"NN_CAP_VIDEO_BACKEND":     "kling",
			"NN_CAP_VIDEO_API_KEY":     "my-ak:my-sk",
			"NN_CAP_VIDEO_TIMEOUT_SEC": "30",
		}), client)
		if reg.Video == nil {
			t.Fatalf("kling 应以 ak:sk 打包形式完成绑定，problem=%q", reg.Problem(CapVideo))
		}
		kb := reg.Video.Backend.(*KlingBackend)
		if kb.accessKey != "my-ak" || kb.secretKey != "my-sk" {
			t.Errorf("ak/sk = %q/%q", kb.accessKey, kb.secretKey)
		}
		if kb.timeout.Seconds() != 30 {
			t.Errorf("timeout = %v, want 30s", kb.timeout)
		}
	})

	t.Run("kling 缺密钥对时给出精确问题", func(t *testing.T) {
		reg := BuildRegistry(envMap(map[string]string{"NN_CAP_VIDEO_BACKEND": "kling"}), client)
		if reg.Video != nil {
			t.Error("缺密钥不应绑定")
		}
		if p := reg.Problem(CapVideo); !strings.Contains(p, "access_key") {
			t.Errorf("问题串 = %q, want 指引填写 access_key", p)
		}
	})
}

func floatPtr(f float64) *float64 { return &f }
