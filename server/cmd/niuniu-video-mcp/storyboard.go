package main

// storyboard.json — the single generation-source-of-truth (design §5, plan
// §1.6). media_compose refuses anything that is not structurally valid AND
// review_status=="approved".
//
// 必填字段（冻结）:
//   {title, aspect_ratio, fps, resolution, review_status,
//    shots: [{id, duration_sec, narration, subtitle,
//             visual: {type, tier, prompt, asset},
//             tts: {voice, asset}, transition}]}
//
// Validation is deliberately minimal-but-precise: required key present + correct
// JSON type + enum membership where the schema freezes one. Every problem is
// reported in Chinese with a field path, all problems at once, so the agent can
// fix the storyboard in one pass. Extra keys are tolerated (the schema grows:
// character_ids / scene_id / candidates / selected / action / camera …；其中
// aigc_label / aigc_label_text 由工具层消费——合成时烧录 AIGC 标识角标）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Visual is a shot's visual instruction.
type Visual struct {
	Type   string `json:"type"`   // image | video
	Tier   string `json:"tier"`   // L1 | L2 | L3
	Prompt string `json:"prompt"` // 已含角色/场景/画风模板段
	Asset  string `json:"asset"`  // 素材路径（图片或视频），workspace 相对或绝对
}

// TTSField is a shot's narration binding.
type TTSField struct {
	Voice string `json:"voice"`
	Asset string `json:"asset"` // audio path; empty = 该镜无人声（合成为静音轨）
}

// ShotID accepts either a JSON number or a string id (the spec example uses 1;
// string ids like "c-01" are common too).
type ShotID string

// UnmarshalJSON normalizes number/string ids into their textual form.
func (s *ShotID) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*s = ""
		return nil
	}
	if b[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		*s = ShotID(str)
		return nil
	}
	*s = ShotID(string(b))
	return nil
}

// Shot is one storyboard shot.
type Shot struct {
	ID          ShotID   `json:"id"`
	DurationSec float64  `json:"duration_sec"`
	Narration   string   `json:"narration"`
	Subtitle    string   `json:"subtitle"`
	Visual      Visual   `json:"visual"`
	TTS         TTSField `json:"tts"`
	Transition  string   `json:"transition"` // cut | fade

	// Optional extensions (tolerated, used when present).
	Selected   string   `json:"selected,omitempty"`   // 选条：优先于 visual.asset 的素材
	Candidates []string `json:"candidates,omitempty"` // 候选留痕
}

// defaultAIGCLabelText is the AIGC label's default wording (GB 45438-2025
// 参考)，aigc_label=true 且 aigc_label_text 为空/空白时使用。
const defaultAIGCLabelText = "AI 生成"

// Storyboard is the parsed document.
type Storyboard struct {
	Title        string   `json:"title"`
	AspectRatio  string   `json:"aspect_ratio"`
	FPS          float64  `json:"fps"`
	Resolution   string   `json:"resolution"`
	ReviewStatus string   `json:"review_status"`
	Revision     *float64 `json:"revision,omitempty"`
	BGM          string   `json:"bgm,omitempty"` // 可选：整片 BGM（可被 media_compose 的 bgm 参数覆盖）

	// 可选：AIGC 显式标识（GB 45438-2025 参考）。缺省 false = 不烧录，
	// 老分镜不带该字段时行为与从前完全一致（向后兼容）。
	AIGCLabel     bool   `json:"aigc_label"`
	AIGCLabelText string `json:"aigc_label_text"` // 标识文字，缺省 "AI 生成"（仅 aigc_label=true 时消费）

	Shots []Shot `json:"shots"`
}

// ExpectedDuration sums the shot durations (seconds).
func (s *Storyboard) ExpectedDuration() float64 {
	total := 0.0
	for _, sh := range s.Shots {
		total += sh.DurationSec
	}
	return total
}

// ResolutionWH parses "1280x720" into width/height. Callers must have
// validated the document first (ValidateStoryboard guarantees the format).
func (s *Storyboard) ResolutionWH() (int, int, error) {
	m := resolutionRe.FindStringSubmatch(strings.TrimSpace(s.Resolution))
	if m == nil {
		return 0, 0, fmt.Errorf("storyboard.resolution %q 不是 WxH 格式（如 1280x720）", s.Resolution)
	}
	var w, h int
	fmt.Sscanf(m[1], "%d", &w)
	fmt.Sscanf(m[2], "%d", &h)
	return w, h, nil
}

// AssetForShot returns the preferred visual asset reference for a shot
// (selected wins over visual.asset).
func AssetForShot(sh Shot) string {
	if strings.TrimSpace(sh.Selected) != "" {
		return strings.TrimSpace(sh.Selected)
	}
	return strings.TrimSpace(sh.Visual.Asset)
}

var (
	aspectRatioRe  = regexp.MustCompile(`^\d{1,2}:\d{1,2}$`)
	resolutionRe   = regexp.MustCompile(`^(\d{2,5})x(\d{2,5})$`)
	validReview_   = map[string]bool{"draft": true, "in-review": true, "approved": true}
	validVisualTy_ = map[string]bool{"image": true, "video": true}
	validTier_     = map[string]bool{"L1": true, "L2": true, "L3": true}
	validTransit_  = map[string]bool{"cut": true, "fade": true}
)

// LoadStoryboard reads and validates <ws>/video-project/storyboard.json.
func LoadStoryboard(wsDir string) (*Storyboard, string, error) {
	path := projectPath(wsDir, "storyboard.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, path, fmt.Errorf("未找到分镜文件 %s：请先按 shortvideo-forge 流程产出 storyboard.json 并通过评审", relToWS(wsDir, path))
		}
		return nil, path, fmt.Errorf("读取分镜文件 %s 失败: %w", relToWS(wsDir, path), err)
	}
	sb, err := ValidateStoryboard(raw)
	if err != nil {
		return nil, path, err
	}
	return sb, string(raw), nil
}

// ValidateStoryboard validates the frozen required-field contract and returns
// the parsed document. Type-level problems are reported against the raw JSON
// (so a wrong type never surfaces as a cryptic English decoder error), then the
// typed decode runs over an already-verified document.
func ValidateStoryboard(raw []byte) (*Storyboard, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("storyboard.json 不是合法 JSON：%v", err)
	}
	v := &sbValidator{}
	v.checkStoryboard(doc)
	if len(v.problems) > 0 {
		return nil, fmt.Errorf("storyboard.json 校验失败（%d 处）：\n  - %s", len(v.problems), strings.Join(v.problems, "\n  - "))
	}
	var sb Storyboard
	if err := json.Unmarshal(raw, &sb); err != nil {
		return nil, fmt.Errorf("storyboard.json 解析失败：%v", err)
	}
	// aigc_label=true 且未给文字（"" 或全空白）时补缺省文字；aigc_label=false
	// 时不动 aigc_label_text（该字段无意义，保持透传以便排查）。
	if sb.AIGCLabel && strings.TrimSpace(sb.AIGCLabelText) == "" {
		sb.AIGCLabelText = defaultAIGCLabelText
	}
	return &sb, nil
}

// sbValidator accumulates every problem found while walking the document.
type sbValidator struct{ problems []string }

func (v *sbValidator) addf(format string, args ...any) {
	v.problems = append(v.problems, fmt.Sprintf(format, args...))
}

// jsonTypeName names a decoded JSON value's type for error messages.
func jsonTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "字符串"
	case float64:
		return "数字"
	case bool:
		return "布尔值"
	case []any:
		return "数组"
	case map[string]any:
		return "对象"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// str requires obj[key] to be a (possibly empty) string.
func (v *sbValidator) str(obj map[string]any, path, key string) string {
	raw, ok := obj[key]
	if !ok || raw == nil {
		v.addf("%s.%s 缺失", path, key)
		return ""
	}
	s, ok := raw.(string)
	if !ok {
		v.addf("%s.%s 必须是字符串（当前为%s）", path, key, jsonTypeName(raw))
		return ""
	}
	return s
}

// nonEmptyStr is str + a non-empty requirement.
func (v *sbValidator) nonEmptyStr(obj map[string]any, path, key string) string {
	s := v.str(obj, path, key)
	if s == "" {
		if _, ok := obj[key]; ok {
			v.addf("%s.%s 不能为空", path, key)
		}
	}
	return s
}

// obj requires obj[key] to be an object.
func (v *sbValidator) obj(obj map[string]any, path, key string) map[string]any {
	raw, ok := obj[key]
	if !ok || raw == nil {
		v.addf("%s.%s 缺失（应为对象）", path, key)
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		v.addf("%s.%s 必须是对象（当前为%s）", path, key, jsonTypeName(raw))
		return nil
	}
	return m
}

// num requires obj[key] to be a number.
func (v *sbValidator) num(obj map[string]any, path, key string) float64 {
	raw, ok := obj[key]
	if !ok || raw == nil {
		v.addf("%s.%s 缺失（应为数字）", path, key)
		return 0
	}
	f, ok := raw.(float64)
	if !ok {
		v.addf("%s.%s 必须是数字（当前为%s）", path, key, jsonTypeName(raw))
		return 0
	}
	return f
}

// arr requires obj[key] to be an array.
func (v *sbValidator) arr(obj map[string]any, path, key string) []any {
	raw, ok := obj[key]
	if !ok || raw == nil {
		v.addf("%s.%s 缺失（应为数组）", path, key)
		return nil
	}
	a, ok := raw.([]any)
	if !ok {
		v.addf("%s.%s 必须是数组（当前为%s）", path, key, jsonTypeName(raw))
		return nil
	}
	return a
}

// enum records a problem when value is non-empty but outside allowed.
func (v *sbValidator) enum(value, path string, allowed map[string]bool, hint string) {
	if value == "" {
		return
	}
	if !allowed[value] {
		v.addf("%s 的值 %q 不合法（%s）", path, value, hint)
	}
}

func (v *sbValidator) checkStoryboard(doc map[string]any) {
	v.nonEmptyStr(doc, "storyboard", "title")
	aspect := v.nonEmptyStr(doc, "storyboard", "aspect_ratio")
	if aspect != "" && !aspectRatioRe.MatchString(aspect) {
		v.addf("storyboard.aspect_ratio 的值 %q 不是 W:H 格式（如 16:9 / 9:16 / 1:1）", aspect)
	}
	fps := v.num(doc, "storyboard", "fps")
	if _, ok := doc["fps"]; ok && fps <= 0 {
		v.addf("storyboard.fps 必须为正数（当前 %v）", fps)
	}
	res := v.nonEmptyStr(doc, "storyboard", "resolution")
	if res != "" && !resolutionRe.MatchString(strings.TrimSpace(res)) {
		v.addf("storyboard.resolution 的值 %q 不是 WxH 格式（如 1280x720）", res)
	}
	status := v.nonEmptyStr(doc, "storyboard", "review_status")
	v.enum(status, "storyboard.review_status", validReview_, "只能是 draft / in-review / approved")

	// 可选扩展：AIGC 标识（合规项，缺省 false）。null 视同缺省；类型非法时
	// 【显式报错】而不是静默按缺省容错——合规标识被静默忽略会产出无标识成片
	// 而无人察觉，与 G5「缺标识不放行」冲突。
	if raw, ok := doc["aigc_label"]; ok && raw != nil {
		if _, isBool := raw.(bool); !isBool {
			v.addf("storyboard.aigc_label 必须是布尔值（当前为%s）", jsonTypeName(raw))
		}
	}
	if raw, ok := doc["aigc_label_text"]; ok && raw != nil {
		if _, isStr := raw.(string); !isStr {
			v.addf("storyboard.aigc_label_text 必须是字符串（当前为%s）", jsonTypeName(raw))
		}
	}

	shots := v.arr(doc, "storyboard", "shots")
	if shots == nil {
		return
	}
	if len(shots) == 0 {
		v.addf("storyboard.shots 不能为空（至少 1 个镜头）")
		return
	}
	for i, raw := range shots {
		path := fmt.Sprintf("shots[%d]", i)
		shot, ok := raw.(map[string]any)
		if !ok {
			v.addf("%s 必须是对象（当前为%s）", path, jsonTypeName(raw))
			continue
		}
		v.checkShot(shot, path)
	}
}

func (v *sbValidator) checkShot(shot map[string]any, path string) {
	// id: number or non-empty string.
	switch idv := shot["id"].(type) {
	case nil:
		v.addf("%s.id 缺失（应为数字或字符串）", path)
	case string:
		if strings.TrimSpace(idv) == "" {
			v.addf("%s.id 不能为空", path)
		}
	case float64:
	default:
		v.addf("%s.id 必须是数字或字符串（当前为%s）", path, jsonTypeName(idv))
	}

	dur := v.num(shot, path, "duration_sec")
	if _, ok := shot["duration_sec"]; ok && dur <= 0 {
		v.addf("%s.duration_sec 必须为正数（当前 %v）", path, dur)
	}
	v.str(shot, path, "narration")
	v.str(shot, path, "subtitle")

	if vis := v.obj(shot, path, "visual"); vis != nil {
		vp := path + ".visual"
		v.enum(v.nonEmptyStr(vis, vp, "type"), vp+".type", validVisualTy_, "只能是 image 或 video")
		v.enum(v.nonEmptyStr(vis, vp, "tier"), vp+".tier", validTier_, "只能是 L1 / L2 / L3")
		v.str(vis, vp, "prompt")
		v.nonEmptyStr(vis, vp, "asset")
	}
	if tts := v.obj(shot, path, "tts"); tts != nil {
		tp := path + ".tts"
		v.str(tts, tp, "voice")
		v.str(tts, tp, "asset")
	}
	v.enum(v.nonEmptyStr(shot, path, "transition"), path+".transition", validTransit_, "只能是 cut 或 fade")
}
