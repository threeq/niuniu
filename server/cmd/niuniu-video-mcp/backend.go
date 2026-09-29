package main

// Backend adapter layer for the video-gen capability module.
//
// Three capability families, one Go interface each (frozen contract, see
// docs/superpowers/plans/2026-09-27-video-creation-implementation.md §1.5):
//
//	TTSBackend   — synchronous, single audio artifact  (openai-compat)
//	ImageBackend — synchronous, N image candidates     (openai-compat)
//	VideoBackend — asynchronous task (Submit/Poll/Fetch) (seedance, kling)
//
// A registry selects ONE concrete adapter per capability from the injected
// NN_CAP_<CAP>_BACKEND environment variable (the capability-config domain on
// the server side resolves which account row wins and projects it). When a
// capability is unconfigured, its binding is nil and every tool that needs it
// returns an actionable Chinese hint pointing at Settings -> capability
// config — the module never panics and never fails silently.
//
// Price metadata travels WITH the adapter (design §7.3): quote_estimate
// aggregates the registered unit prices so a quote is itemized without
// re-typing prices at every call site.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Capability keys. These are also the CAP fragment of the NN_CAP_<CAP>_* env
// namespace, so they are part of the frozen contract — do not rename.
const (
	CapTTS   = "tts"
	CapImage = "image"
	CapVideo = "video"
)

// Adapter names (values of NN_CAP_<CAP>_BACKEND and the `backend` column in
// capability_backends).
const (
	BackendOpenAICompat = "openai-compat"
	BackendSeedance     = "seedance"
	BackendKling        = "kling"
)

// Task states, normalized across providers. Providers use their own
// vocabularies (ark: queued/running/succeeded/failed, kling:
// submitted/processing/succeed/failed); TaskStatus.RawStatus keeps the
// provider's original string for traceability.
type TaskState string

const (
	TaskPending   TaskState = "pending"
	TaskRunning   TaskState = "running"
	TaskSucceeded TaskState = "succeeded"
	TaskFailed    TaskState = "failed"
)

// Terminal reports whether no further polling is needed.
func (s TaskState) Terminal() bool { return s == TaskSucceeded || s == TaskFailed }

// TTSRequest is one speech synthesis request. Speed <= 0 means adapter default.
type TTSRequest struct {
	Text  string
	Voice string
	Speed float64
}

// TTSResult is the synthesized audio. ContentType is the response's MIME type
// (defaults to audio/mpeg when the provider omits it).
type TTSResult struct {
	Audio       []byte
	ContentType string
}

// ImageRequest is one text-to-image (or image-edit) request.
type ImageRequest struct {
	Prompt string
	N      int
	Size   string // e.g. "1536x1024"; empty = adapter default
	// ReferenceImage, when non-empty, switches the adapter to the
	// reference-guided (edit) endpoint.
	ReferenceImage     []byte
	ReferenceImageName string
}

// ImageCandidate is one generated image, either inline bytes or a remote URL
// the adapter already downloaded (Data is preferred; URL is kept for trace).
type ImageCandidate struct {
	Data []byte
	Ext  string // file extension without dot, e.g. "png"
	URL  string
}

// VideoRequest is one image-to-video submission (a single provider task; N
// candidates are realized by the tool submitting N times).
type VideoRequest struct {
	FirstFrame     []byte
	FirstFrameName string // file name hint incl. extension, decides the data: MIME
	Prompt         string
	DurationSec    float64
	NCandidates    int
	AspectRatio    string
	Model          string // empty = adapter default
}

// TaskHandle identifies one submitted provider task. Raw carries the submit
// response verbatim so the task record on disk is self-explanatory.
type TaskHandle struct {
	TaskID  string
	Backend string
	Model   string
	Raw     json.RawMessage
}

// TaskStatus is one poll result.
type TaskStatus struct {
	State     TaskState
	RawStatus string // provider's own status string, verbatim
	Err       string // provider error text, verbatim (never rewritten)
	Raw       json.RawMessage
}

// VideoCandidate is one finished video, bytes or remote URL.
type VideoCandidate struct {
	Data []byte
	Ext  string
	URL  string
}

// TTSBackend synthesizes speech from text.
type TTSBackend interface {
	Synthesize(ctx context.Context, req TTSRequest) (TTSResult, error)
}

// ImageBackend generates image candidates from a prompt (plus optional
// reference image).
type ImageBackend interface {
	Generate(ctx context.Context, req ImageRequest) ([]ImageCandidate, error)
}

// VideoBackend drives an asynchronous image-to-video task.
type VideoBackend interface {
	Submit(ctx context.Context, req VideoRequest) (TaskHandle, error)
	Poll(ctx context.Context, h TaskHandle) (TaskStatus, error)
	Fetch(ctx context.Context, h TaskHandle) ([]VideoCandidate, error)
}

// PriceMeta is the adapter-registered price metadata used by quote_estimate.
// UnitPrice is in Currency per Per units (Per=1000 for TTS: per 1k chars).
type PriceMeta struct {
	Unit       string  // human unit, e.g. "元/千字符" / "元/张" / "元/秒"
	UnitPrice  float64 // price per Per units
	Per        float64 // quantity divisor; 1 unless the unit says otherwise
	Configured bool    // false = placeholder, quote marks the item as unconfigured
	Note       string  // why it is unconfigured / how to configure it
}

// amount computes the money for `quantity` units of this capability.
func (p PriceMeta) amount(quantity float64) float64 {
	per := p.Per
	if per <= 0 {
		per = 1
	}
	return p.UnitPrice * quantity / per
}

// TTSBinding is the resolved TTS capability: adapter + model + price.
type TTSBinding struct {
	Name         string // adapter name (openai-compat)
	Backend      TTSBackend
	Model        string
	DefaultVoice string
	Price        PriceMeta
}

// ImageBinding is the resolved image capability.
type ImageBinding struct {
	Name    string
	Backend ImageBackend
	Model   string
	Price   PriceMeta
}

// VideoBinding is the resolved video capability.
type VideoBinding struct {
	Name    string
	Backend VideoBackend
	Model   string
	Price   PriceMeta
}

// Registry holds the resolved bindings. A nil binding means "not configured";
// Problems carries the specific reason so tools can surface a precise hint.
type Registry struct {
	TTS      *TTSBinding
	Image    *ImageBinding
	Video    *VideoBinding
	Problems map[string]string // capability -> human-readable configuration problem
}

// Problem returns the capability's configuration problem, or "" if it is
// configured fine.
func (r *Registry) Problem(capability string) string {
	if r == nil || r.Problems == nil {
		return ""
	}
	return r.Problems[capability]
}

// UnconfiguredMessage builds the actionable Chinese hint a tool returns when a
// capability has no usable account. `capLabel` is the display name used in the
// Settings UI (配音 / 图像 / 视频).
func (r *Registry) UnconfiguredMessage(capability, capLabel string) string {
	if p := r.Problem(capability); p != "" {
		return p
	}
	return fmt.Sprintf("未配置%s能力：请在设置→能力配置中添加账号（选择后端实现并填写 Base URL / API Key 后，"+
		"建舱时会为工具进程注入 NN_CAP_%s_* 环境变量）。", capLabel, strings.ToUpper(capability))
}

// envLookup abstracts os.Getenv so tests can inject a fake environment.
type envLookup func(key string) string

// capEnv reads NN_CAP_<CAP>_<SUFFIX>. The suffix is upper-cased (the frozen
// naming: extra_config key `model` -> NN_CAP_TTS_MODEL).
func capEnv(lookup envLookup, capability, suffix string) string {
	if lookup == nil {
		return ""
	}
	return strings.TrimSpace(lookup("NN_CAP_" + strings.ToUpper(capability) + "_" + strings.ToUpper(suffix)))
}

// capExtra collects every NN_CAP_<CAP>_<KEY> env var into a lower-cased key
// map, minus the reserved suffixes (BACKEND/BASE_URL/API_KEY). Extra config
// keys documented by this module: model, voice, price, price_unit, price_per,
// access_key, secret_key, timeout_sec.
func capExtra(lookup envLookup, capability string) map[string]string {
	out := map[string]string{}
	if lookup == nil {
		return out
	}
	prefix := "NN_CAP_" + strings.ToUpper(capability) + "_"
	reserved := map[string]bool{"BACKEND": true, "BASE_URL": true, "API_KEY": true}
	// envLookup is a plain function, so we cannot enumerate the environment
	// through it. Known keys are probed explicitly — the set is small and
	// finite (documented above), which keeps the seam test-friendly.
	for _, key := range []string{"model", "voice", "price", "price_unit", "price_per", "access_key", "secret_key", "timeout_sec"} {
		if reserved[strings.ToUpper(key)] {
			continue
		}
		if v := strings.TrimSpace(lookup(prefix + strings.ToUpper(key))); v != "" {
			out[key] = v
		}
	}
	return out
}

// priceFromEnv overlays NN_CAP_<CAP>_PRICE / _PRICE_UNIT / _PRICE_PER on the
// adapter's default PriceMeta. A price set here (or per quote item) is what
// turns a quote from "price unconfigured" into a real number.
func priceFromEnv(lookup envLookup, capability string, def PriceMeta) PriceMeta {
	meta := def
	if v := capEnv(lookup, capability, "price"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			meta.UnitPrice = f
			meta.Configured = true
			meta.Note = ""
		} else {
			meta.Note = fmt.Sprintf("NN_CAP_%s_PRICE=%q 不是合法数字，忽略。", strings.ToUpper(capability), v)
		}
	}
	if v := capEnv(lookup, capability, "price_unit"); v != "" {
		meta.Unit = v
	}
	if v := capEnv(lookup, capability, "price_per"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			meta.Per = f
		}
	}
	return meta
}

// httpTimeout parses NN_CAP_<CAP>_TIMEOUT_SEC (default 120s). Media generation
// is slow, so the adapter default is generous; polling is cheap.
func httpTimeout(lookup envLookup, capability string, def time.Duration) time.Duration {
	if v := capEnv(lookup, capability, "timeout_sec"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return time.Duration(f * float64(time.Second))
		}
	}
	return def
}

// BuildRegistry resolves the three capability bindings from the NN_CAP_*
// environment (frozen contract §1.3). It returns a registry whose nil bindings
// mean "not configured here" plus, per capability, a precise Chinese problem
// string. An unknown adapter name is NOT fatal: the capability stays nil with
// an explicit problem, so the tools degrade with a clear message instead of
// failing at process start.
func BuildRegistry(lookup envLookup, client *http.Client) *Registry {
	reg := &Registry{Problems: map[string]string{}}
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}

	// --- TTS ---
	ttsBackend := capEnv(lookup, CapTTS, "backend")
	ttsBase := capEnv(lookup, CapTTS, "base_url")
	ttsKey := capEnv(lookup, CapTTS, "api_key")
	ttsExtra := capExtra(lookup, CapTTS)
	ttsDef := PriceMeta{
		Unit: "元/千字符", Per: 1000,
		Note: "未配置 TTS 单价：请在报价清单中显式给出 unit_price，或在能力配置的扩展参数中设置 price（元/千字符）。",
	}
	switch {
	case ttsBackend == "":
		// leave nil — "not configured"
	case ttsBackend == BackendOpenAICompat:
		if ttsBase == "" {
			reg.Problems[CapTTS] = "TTS 后端（openai-compat）缺少 Base URL：请在设置→能力配置中为该账号填写 Base URL。"
			break
		}
		model := ttsExtra["model"]
		if model == "" {
			model = "tts-1"
		}
		reg.TTS = &TTSBinding{
			Name:         BackendOpenAICompat,
			Backend:      &OpenAICompatTTS{baseURL: ttsBase, apiKey: ttsKey, model: model, client: client, timeout: httpTimeout(lookup, CapTTS, 120*time.Second)},
			Model:        model,
			DefaultVoice: ttsExtra["voice"],
			Price:        priceFromEnv(lookup, CapTTS, ttsDef),
		}
	default:
		reg.Problems[CapTTS] = fmt.Sprintf("不支持的 TTS 后端实现 %q（支持：%s）：请在设置→能力配置中更换后端。", ttsBackend, BackendOpenAICompat)
	}

	// --- Image ---
	imgBackend := capEnv(lookup, CapImage, "backend")
	imgBase := capEnv(lookup, CapImage, "base_url")
	imgKey := capEnv(lookup, CapImage, "api_key")
	imgExtra := capExtra(lookup, CapImage)
	imgDef := PriceMeta{
		Unit: "元/张", Per: 1,
		Note: "未配置图像单价：请在报价清单中显式给出 unit_price，或在能力配置的扩展参数中设置 price（元/张）。",
	}
	switch {
	case imgBackend == "":
	case imgBackend == BackendOpenAICompat:
		if imgBase == "" {
			reg.Problems[CapImage] = "图像后端（openai-compat）缺少 Base URL：请在设置→能力配置中为该账号填写 Base URL。"
			break
		}
		model := imgExtra["model"]
		if model == "" {
			model = "gpt-image-1"
		}
		reg.Image = &ImageBinding{
			Name:    BackendOpenAICompat,
			Backend: &OpenAICompatImage{baseURL: imgBase, apiKey: imgKey, model: model, client: client, timeout: httpTimeout(lookup, CapImage, 180*time.Second)},
			Model:   model,
			Price:   priceFromEnv(lookup, CapImage, imgDef),
		}
	default:
		reg.Problems[CapImage] = fmt.Sprintf("不支持的图像后端实现 %q（支持：%s）：请在设置→能力配置中更换后端。", imgBackend, BackendOpenAICompat)
	}

	// --- Video ---
	vidBackend := capEnv(lookup, CapVideo, "backend")
	vidBase := capEnv(lookup, CapVideo, "base_url")
	vidKey := capEnv(lookup, CapVideo, "api_key")
	vidExtra := capExtra(lookup, CapVideo)
	vidDef := PriceMeta{
		Unit: "元/秒", Per: 1,
		Note: "未配置视频单价：L3 按秒计费，请在能力配置中设置 price（元/秒）或在报价清单中显式给出 unit_price。",
	}
	switch {
	case vidBackend == "":
	case vidBackend == BackendSeedance:
		base := vidBase
		if base == "" {
			base = "https://ark.cn-beijing.volces.com"
		}
		model := vidExtra["model"]
		if model == "" {
			model = defaultSeedanceModel
		}
		reg.Video = &VideoBinding{
			Name:    BackendSeedance,
			Backend: &SeedanceBackend{baseURL: base, apiKey: vidKey, model: model, client: client, timeout: httpTimeout(lookup, CapVideo, 120*time.Second)},
			Model:   model,
			Price:   priceFromEnv(lookup, CapVideo, vidDef),
		}
	case vidBackend == BackendKling:
		base := vidBase
		if base == "" {
			base = "https://api.klingai.com"
		}
		model := vidExtra["model"]
		if model == "" {
			model = defaultKlingModel
		}
		ak, sk, prob := klingCredentials(vidKey, vidExtra)
		if prob != "" {
			reg.Problems[CapVideo] = prob
			break
		}
		reg.Video = &VideoBinding{
			Name:    BackendKling,
			Backend: &KlingBackend{baseURL: base, accessKey: ak, secretKey: sk, model: model, client: client, timeout: httpTimeout(lookup, CapVideo, 120*time.Second)},
			Model:   model,
			Price:   priceFromEnv(lookup, CapVideo, vidDef),
		}
	default:
		reg.Problems[CapVideo] = fmt.Sprintf("不支持的视频后端实现 %q（支持：%s, %s）：请在设置→能力配置中更换后端。", vidBackend, BackendSeedance, BackendKling)
	}

	return reg
}

// klingCredentials resolves the kling access/secret key pair. Kling signs
// every request with a JWT built from an AK/SK pair, which does not fit the
// single api_key column — accept either dedicated extra keys or the
// "ak:sk" packed form in the API Key field.
func klingCredentials(apiKey string, extra map[string]string) (ak, sk, problem string) {
	ak, sk = extra["access_key"], extra["secret_key"]
	if ak != "" && sk != "" {
		return ak, sk, ""
	}
	if i := strings.Index(apiKey, ":"); i > 0 {
		a, s := strings.TrimSpace(apiKey[:i]), strings.TrimSpace(apiKey[i+1:])
		if a != "" && s != "" {
			return a, s, ""
		}
	}
	return "", "", "可灵后端需要 Access Key 与 Secret Key 对（JWT 签名用）：请在设置→能力配置的扩展参数中填写 " +
		"access_key / secret_key，或把 API Key 填成 ak:sk 形式。"
}

// ProblemSummary renders the configuration problems for the startup log.
func (r *Registry) ProblemSummary() string {
	if r == nil || len(r.Problems) == 0 {
		return ""
	}
	keys := make([]string, 0, len(r.Problems))
	for k := range r.Problems {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+r.Problems[k])
	}
	return strings.Join(parts, " | ")
}
