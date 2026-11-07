package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/niuniu-dev/niuniu/internal/opsbot/browser"
	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
)

// XHS adapter error codes.
const (
	CodeLoginExpired = "login_expired"
	CodeRiskBlocked  = "risk_blocked"
	CodeRecipeBroken = "recipe_broken"
)

// Limits mirrored from the builtin xiaohongshu pack's content_schema.json.
const (
	xhsMaxTitleChars = 20
	xhsMaxBodyChars  = 1000
	xhsMaxNoteImages = 18
	xhsMaxVideoSec   = 900
	xhsImageRatio    = "3:4"
)

// Content kinds accepted by XHSPublishPayload.
const (
	XHSKindNote  = "note"
	XHSKindVideo = "video"
)

// Recipe keys callers use to build a RecipeSet. Publish uses
// XHSRecipePublishVideo for video notes when the pack provides it.
const (
	XHSRecipeLogin        = "login"
	XHSRecipePublish      = "publish"
	XHSRecipePublishVideo = "publish_video"
	XHSRecipeMetrics      = "metrics"
	XHSRecipeEngagements  = "engagements"
	XHSRecipeReply        = "reply"
	XHSRecipeHealth       = "health"
)

// Unified metric keys not covered by the WeChat adapter.
const (
	MetricSaves    = "saves"
	MetricComments = "comments"
)

var xhsMetricAliases = map[string]string{
	"view_num":    MetricViews,
	"like_num":    MetricLikes,
	"collect_num": MetricSaves,
	"comment_num": MetricComments,
	"share_num":   MetricShares,
	"fans_add":    MetricFollowerDelta,
}

var xhsUnifiedMetrics = []string{
	MetricViews, MetricLikes, MetricSaves, MetricComments, MetricShares, MetricFollowerDelta,
}

// RecipeSet maps a recipe name to its pack recipe. The caller loads it from the
// published Platform Pack, so the adapter never hardcodes selector logic.
type RecipeSet map[string]packs.Recipe

// BrowserWorker runs one recipe task on one account's browser session. The
// Worker in opsbot/browser satisfies it; tests inject a fake.
type BrowserWorker interface {
	Run(ctx context.Context, task browser.Task) (browser.Result, error)
}

// XHSPublishPayload is the payload accepted by XHSAdapter.Publish. It covers
// both image notes (Images) and video notes (VideoPath).
type XHSPublishPayload struct {
	// Kind is note (default) or video.
	Kind  string
	Title string
	Body  string
	// Images are the note images, or the cover for a video.
	Images []string
	// VideoPath is the video file for a video note.
	VideoPath string
	// Cover is an explicit cover image.
	Cover string
	Tags  []string
	// ImageRatio is checked against the pack's 3:4 requirement when set.
	ImageRatio string
	// DurationSec is checked against the 15-minute platform cap when set.
	DurationSec int
}

// XHSAdapter drives the xiaohongshu creator backend through browser recipes.
// The adapter owns no selector logic: every platform interaction is a recipe
// from the account's Platform Pack.
type XHSAdapter struct {
	worker  BrowserWorker
	recipes RecipeSet
	// Backend selects the browser backend; empty means the local backend.
	Backend string

	mu    sync.RWMutex
	botID int64
}

var _ ChannelAdapter = (*XHSAdapter)(nil)

func NewXHSAdapter(worker BrowserWorker, recipes RecipeSet) *XHSAdapter {
	return &XHSAdapter{worker: worker, recipes: recipes, Backend: browser.BackendLocal}
}

func (a *XHSAdapter) Capabilities() CapabilitySet {
	return CapabilitySet{
		CapPublish:     true,
		CapMetrics:     true,
		CapEngagements: true,
		CapReply:       true,
	}
}

// EnsureSession binds the account and, when the pack provides a login recipe,
// lets the worker confirm the stored session (or raise a QR re-login task).
func (a *XHSAdapter) EnsureSession(ctx context.Context, accountRef string) error {
	botID, err := parseXHSAccountRef(accountRef)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.botID = botID
	a.mu.Unlock()

	if _, ok := a.recipes[XHSRecipeLogin]; !ok {
		return nil
	}
	_, err = a.execute(ctx, CapPublish, XHSRecipeLogin, nil)
	return err
}

// HealthCheck opens the creator home through the health recipe. When the pack
// has no health recipe it falls back to the login recipe, and with neither it
// reports healthy rather than failing every caller.
func (a *XHSAdapter) HealthCheck(ctx context.Context) error {
	name := XHSRecipeHealth
	if _, ok := a.recipes[name]; !ok {
		name = XHSRecipeLogin
		if _, ok := a.recipes[name]; !ok {
			return nil
		}
	}
	_, err := a.execute(ctx, CapMetrics, name, nil)
	return err
}

// Publish validates the payload against the pack's content schema, then runs
// the image or video publish recipe. A risk-control block is never retried.
func (a *XHSAdapter) Publish(ctx context.Context, payload any) (Receipt, error) {
	p, ok := xhsPayload(payload)
	if !ok {
		return Receipt{}, invalidPayload(CapPublish, fmt.Sprintf("xiaohongshu: unsupported payload type %T", payload))
	}
	kind, input, err := xhsPublishInput(p)
	if err != nil {
		return Receipt{}, err
	}

	recipe := XHSRecipePublish
	if kind == XHSKindVideo {
		if _, ok := a.recipes[XHSRecipePublishVideo]; ok {
			recipe = XHSRecipePublishVideo
		}
	}

	res, err := a.execute(ctx, CapPublish, recipe, input)
	if err != nil {
		return Receipt{}, err
	}
	return xhsReceipt(res, "note_url"), nil
}

func xhsPayload(payload any) (XHSPublishPayload, bool) {
	switch p := payload.(type) {
	case XHSPublishPayload:
		return p, true
	case *XHSPublishPayload:
		if p == nil {
			return XHSPublishPayload{}, false
		}
		return *p, true
	default:
		return XHSPublishPayload{}, false
	}
}

// xhsPublishInput enforces the pack content schema before any browser action.
func xhsPublishInput(p XHSPublishPayload) (string, map[string]string, error) {
	kind := strings.ToLower(strings.TrimSpace(p.Kind))
	switch kind {
	case "":
		kind = XHSKindNote
	case "image", "图文":
		kind = XHSKindNote
	}
	if kind != XHSKindNote && kind != XHSKindVideo {
		return "", nil, &AdapterError{
			Capability: CapPublish,
			Code:       CodeUnsupported,
			Err:        fmt.Errorf("xiaohongshu: content kind %q is not supported", p.Kind),
		}
	}

	title := strings.TrimSpace(p.Title)
	body := strings.TrimSpace(p.Body)
	if title == "" {
		return "", nil, invalidPayload(CapPublish, "xiaohongshu: title is required")
	}
	if utf8.RuneCountInString(title) > xhsMaxTitleChars {
		return "", nil, invalidPayload(CapPublish, fmt.Sprintf("xiaohongshu: title exceeds %d characters", xhsMaxTitleChars))
	}
	if body == "" {
		return "", nil, invalidPayload(CapPublish, "xiaohongshu: body is required")
	}
	if utf8.RuneCountInString(body) > xhsMaxBodyChars {
		return "", nil, invalidPayload(CapPublish, fmt.Sprintf("xiaohongshu: body exceeds %d characters", xhsMaxBodyChars))
	}
	if ratio := strings.TrimSpace(p.ImageRatio); ratio != "" && ratio != xhsImageRatio {
		return "", nil, invalidPayload(CapPublish, fmt.Sprintf("xiaohongshu: image ratio %s, want %s", ratio, xhsImageRatio))
	}

	input := map[string]string{"kind": kind, "title": title, "body": body}
	switch kind {
	case XHSKindNote:
		if len(p.Images) == 0 {
			return "", nil, invalidPayload(CapPublish, "xiaohongshu: image note requires at least one image")
		}
		if len(p.Images) > xhsMaxNoteImages {
			return "", nil, invalidPayload(CapPublish, fmt.Sprintf("xiaohongshu: image note allows at most %d images", xhsMaxNoteImages))
		}
		input["images"] = strings.Join(p.Images, ",")
	case XHSKindVideo:
		if p.DurationSec < 0 || p.DurationSec > xhsMaxVideoSec {
			return "", nil, invalidPayload(CapPublish, fmt.Sprintf("xiaohongshu: video duration %ds exceeds %ds", p.DurationSec, xhsMaxVideoSec))
		}
		if strings.TrimSpace(p.VideoPath) == "" {
			return "", nil, invalidPayload(CapPublish, "xiaohongshu: video note requires a video path")
		}
		if len(p.Images) > 1 {
			return "", nil, invalidPayload(CapPublish, "xiaohongshu: video note allows at most one cover image")
		}
		input["video"] = p.VideoPath
		if len(p.Images) == 1 {
			input["images"] = p.Images[0]
		}
	}

	if tags := xhsTags(p.Tags); tags != "" {
		input["tags"] = tags
	}
	if cover := strings.TrimSpace(p.Cover); cover != "" {
		input["cover"] = cover
	}
	return kind, input, nil
}

func xhsTags(tags []string) string {
	cleaned := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(tag), "#"))
		if tag != "" {
			cleaned = append(cleaned, tag)
		}
	}
	return strings.Join(cleaned, ",")
}

// FetchMetrics runs the metrics recipe for the narrowest preset covering the
// range and folds the native keys into the unified namespace.
func (a *XHSAdapter) FetchMetrics(ctx context.Context, from, to string) (map[string]float64, error) {
	rng, err := xhsDateRange(from, to)
	if err != nil {
		return nil, err
	}
	res, err := a.execute(ctx, CapMetrics, XHSRecipeMetrics, map[string]string{"range": rng})
	if err != nil {
		return nil, err
	}

	out := make(map[string]float64, len(xhsUnifiedMetrics))
	for native, raw := range res.Values {
		unified, ok := xhsMetricAliases[native]
		if !ok {
			continue
		}
		value, err := parseXHSMetric(raw)
		if err != nil {
			return nil, invalidPayload(CapMetrics, fmt.Sprintf("xiaohongshu: metric %s: %v", native, err))
		}
		out[unified] = value
	}
	for _, key := range xhsUnifiedMetrics {
		if _, ok := out[key]; !ok {
			out[key] = 0
		}
	}
	return out, nil
}

func xhsDateRange(from, to string) (string, error) {
	start, err := time.Parse("2006-01-02", strings.TrimSpace(from))
	if err != nil {
		return "", invalidPayload(CapMetrics, fmt.Sprintf("xiaohongshu: invalid metrics start %q", from))
	}
	end, err := time.Parse("2006-01-02", strings.TrimSpace(to))
	if err != nil {
		return "", invalidPayload(CapMetrics, fmt.Sprintf("xiaohongshu: invalid metrics end %q", to))
	}
	if end.Before(start) {
		return "", invalidPayload(CapMetrics, fmt.Sprintf("xiaohongshu: metrics range %q..%q is reversed", from, to))
	}
	days := int(end.Sub(start).Hours()/24) + 1
	switch {
	case days <= 7:
		return "7d", nil
	case days <= 30:
		return "30d", nil
	case days <= 90:
		return "90d", nil
	default:
		return "", invalidPayload(CapMetrics, fmt.Sprintf("xiaohongshu: metrics range %d days exceeds the 90-day preset", days))
	}
}

// ListEngagements pulls comments and direct messages through the engagements
// recipe. cursor is forwarded to the recipe verbatim.
func (a *XHSAdapter) ListEngagements(ctx context.Context, cursor string) ([]Engagement, error) {
	res, err := a.execute(ctx, CapEngagements, XHSRecipeEngagements, map[string]string{"cursor": strings.TrimSpace(cursor)})
	if err != nil {
		return nil, err
	}
	return parseXHSEngagements(res.Values["engagements"])
}

// Reply sends one operator-approved reply and returns the platform receipt.
func (a *XHSAdapter) Reply(ctx context.Context, engagementID, text string) (Receipt, error) {
	id := strings.TrimSpace(engagementID)
	body := strings.TrimSpace(text)
	if id == "" {
		return Receipt{}, invalidPayload(CapReply, "xiaohongshu: engagement id is required")
	}
	if body == "" {
		return Receipt{}, invalidPayload(CapReply, "xiaohongshu: reply text is required")
	}

	res, err := a.execute(ctx, CapReply, XHSRecipeReply, map[string]string{
		"engagement_id": id,
		"reply_text":    body,
	})
	if err != nil {
		return Receipt{}, err
	}
	return xhsReceipt(res, "receipt"), nil
}

func (a *XHSAdapter) execute(ctx context.Context, capability Capability, recipeName string, input map[string]string) (browser.Result, error) {
	if a.worker == nil {
		return browser.Result{}, &AdapterError{
			Capability: capability,
			Code:       CodeUnsupported,
			Err:        errors.New("xiaohongshu: browser worker is not configured"),
		}
	}
	recipe, ok := a.recipes[recipeName]
	if !ok {
		return browser.Result{}, &AdapterError{
			Capability: capability,
			Code:       CodeUnsupported,
			Err:        fmt.Errorf("xiaohongshu: recipe %q is not configured", recipeName),
		}
	}
	botID, ok := a.account()
	if !ok {
		return browser.Result{}, &AdapterError{
			Capability: capability,
			Code:       CodeCredentialInvalid,
			Err:        errors.New("xiaohongshu: no account bound; call EnsureSession first"),
		}
	}

	name := recipe.Name
	if name == "" {
		name = recipeName
	}
	res, err := a.worker.Run(ctx, browser.Task{
		BotID:   botID,
		Backend: a.backend(),
		Recipe:  name,
		Input:   input,
	})
	if err != nil {
		return browser.Result{}, xhsError(capability, err)
	}
	return res, nil
}

func (a *XHSAdapter) account() (int64, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.botID, a.botID > 0
}

func (a *XHSAdapter) backend() string {
	if a.Backend == "" {
		return browser.BackendLocal
	}
	return a.Backend
}

func parseXHSAccountRef(accountRef string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(accountRef), 10, 64)
	if err != nil || id <= 0 {
		return 0, &AdapterError{
			Capability: CapPublish,
			Code:       CodeCredentialInvalid,
			Err:        fmt.Errorf("xiaohongshu: invalid account reference %q", accountRef),
		}
	}
	return id, nil
}

func xhsReceipt(res browser.Result, primary string) Receipt {
	value := strings.TrimSpace(res.Values[primary])
	for _, fallback := range []string{"url", "message_id", "note_id"} {
		if value != "" {
			break
		}
		value = strings.TrimSpace(res.Values[fallback])
	}
	receipt := Receipt{ExternalID: value, Raw: res.EvidencePath}
	if strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "http://") {
		receipt.URL = value
	}
	return receipt
}

// parseXHSEngagements accepts the JSON array a recipe executor extracts, with a
// newline/tab (or pipe) fallback for flat text extraction.
func parseXHSEngagements(raw string) ([]Engagement, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []Engagement{}, nil
	}

	if strings.HasPrefix(raw, "[") {
		var items []map[string]any
		if err := json.Unmarshal([]byte(raw), &items); err == nil {
			out := make([]Engagement, 0, len(items))
			for _, fields := range items {
				engagement := Engagement{
					ExtID:  firstXHSField(fields, "id", "engagement_id", "comment_id", "message_id"),
					Kind:   firstXHSField(fields, "kind", "type"),
					Author: firstXHSField(fields, "author", "nickname", "user"),
					Text:   firstXHSField(fields, "text", "content", "body"),
				}
				if engagement.ExtID == "" {
					continue
				}
				if engagement.Kind == "" {
					engagement.Kind = "comment"
				}
				out = append(out, engagement)
			}
			return out, nil
		}
	}

	out := make([]Engagement, 0)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sep := "\t"
		fields := strings.Split(line, sep)
		if len(fields) < 2 {
			sep = "|"
			fields = strings.Split(line, sep)
		}
		if len(fields) < 2 {
			continue
		}
		item := Engagement{
			ExtID: strings.TrimSpace(fields[0]),
			Kind:  strings.TrimSpace(fields[1]),
		}
		if len(fields) > 2 {
			item.Author = strings.TrimSpace(fields[2])
		}
		if len(fields) > 3 {
			item.Text = strings.Join(fields[3:], sep)
		}
		if item.ExtID == "" {
			continue
		}
		if item.Kind == "" {
			item.Kind = "comment"
		}
		out = append(out, item)
	}
	return out, nil
}

func firstXHSField(item map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := item[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		}
	}
	return ""
}

// parseXHSMetric turns the creator dashboard's display text ("1,234", "1.2万")
// into a number. An empty value is a legitimate zero.
func parseXHSMetric(raw string) (float64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, nil
	}
	s = strings.NewReplacer(",", "", "，", "", " ", "", " ", "", "+", "").Replace(s)

	multiplier := 1.0
	for _, unit := range []struct {
		suffix string
		scale  float64
	}{
		{"亿", 1e8},
		{"万", 1e4},
		{"w", 1e4},
		{"W", 1e4},
	} {
		if strings.HasSuffix(s, unit.suffix) {
			multiplier = unit.scale
			s = strings.TrimSuffix(s, unit.suffix)
			break
		}
	}

	value, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("unparseable metric value %q", raw)
	}
	return value * multiplier, nil
}

// xhsError maps browser sentinels onto the adapter error contract. Blocked and
// broken-recipe failures are never retried; login expiry is reported so the
// worker's re-login task can ask the operator to scan a QR code.
func xhsError(capability Capability, err error) error {
	if err == nil {
		return nil
	}

	var adapterErr *AdapterError
	if errors.As(err, &adapterErr) {
		return adapterErr
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &AdapterError{Capability: capability, Code: CodeTransport, Err: err}
	}

	switch {
	case errors.Is(err, browser.ErrLoginExpired):
		return &AdapterError{Capability: capability, Code: CodeLoginExpired, Err: err}
	case errors.Is(err, browser.ErrBlocked):
		return &AdapterError{Capability: capability, Code: CodeRiskBlocked, Err: err}
	case errors.Is(err, browser.ErrRateLimited):
		return &AdapterError{Capability: capability, Code: CodeRateLimited, Retryable: true, Err: err}
	case errors.Is(err, browser.ErrRecipeBroken),
		errors.Is(err, browser.ErrInvalidRecipe),
		errors.Is(err, browser.ErrMissingInput):
		return &AdapterError{Capability: capability, Code: CodeRecipeBroken, Err: err}
	case errors.Is(err, browser.ErrTimeout), errors.Is(err, browser.ErrTransient):
		return &AdapterError{Capability: capability, Code: CodeTransport, Retryable: true, Err: err}
	case errors.Is(err, browser.ErrLeaseBusy):
		return &AdapterError{Capability: capability, Code: CodeSystemBusy, Retryable: true, Err: err}
	case errors.Is(err, browser.ErrNoExecutor):
		return &AdapterError{Capability: capability, Code: CodeUnsupported, Err: err}
	case errors.Is(err, browser.ErrSessionNotFound),
		errors.Is(err, browser.ErrInvalidSession),
		errors.Is(err, browser.ErrLeaseLost):
		return &AdapterError{Capability: capability, Code: CodeCredentialInvalid, Err: err}
	default:
		return &AdapterError{Capability: capability, Code: CodeTransport, Retryable: true, Err: err}
	}
}
