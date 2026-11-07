package channel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/niuniu-dev/niuniu/internal/opsbot/browser"
)

// BrowserPublishPayload is the pack-driven publish shape a recipe-backed
// channel accepts. The pack's content schema decides which limits apply; the
// adapter carries no platform-specific constants.
type BrowserPublishPayload struct {
	// Kind is note/post/article/video; recipes receive it verbatim.
	Kind      string
	Title     string
	Body      string
	Images    []string
	VideoPath string
	Cover     string
	Tags      []string
	// ImageRatio is checked against the pack schema when set.
	ImageRatio string
	// DurationSec is checked against the pack's video cap when set.
	DurationSec int
	// Params are forward-compatible extra recipe inputs.
	Params map[string]string
}

// BrowserLimits is the subset of packs.ContentSchema.Formats one format uses.
type BrowserLimits struct {
	MaxChars      int
	MaxTitleChars int
	MaxImages     int
	ImageRatio    string
	VideoSec      int
}

// BrowserAdapter drives any browser-recipe channel through the account's
// Platform Pack: recipes for publish/metrics/engagements/reply and limits come
// from the manifest, so a new channel needs a Pack, not a new adapter.
type BrowserAdapter struct {
	worker  BrowserWorker
	recipes RecipeSet
	caps    CapabilitySet
	limits  BrowserLimits
	// metricAliases maps a recipe's native metric key to the unified namespace.
	metricAliases map[string]string
	unified       []string
	// Backend selects the browser backend; empty means the local backend.
	Backend string

	mu    sync.RWMutex
	botID int64
}

var _ ChannelAdapter = (*BrowserAdapter)(nil)

// NewBrowserAdapter builds a recipe-driven adapter. capabilities limits what
// the account may attempt; a missing recipe still reports unsupported at call
// time so a partially generated pack degrades instead of panicking.
func NewBrowserAdapter(worker BrowserWorker, recipes RecipeSet, capabilities []string, limits BrowserLimits, metrics map[string]string) *BrowserAdapter {
	caps := CapabilitySet{}
	for _, c := range capabilities {
		switch Capability(strings.ToLower(strings.TrimSpace(c))) {
		case CapPublish:
			caps[CapPublish] = true
		case CapMetrics:
			caps[CapMetrics] = true
		case CapEngagements:
			caps[CapEngagements] = true
		case CapReply:
			caps[CapReply] = true
		}
	}
	if len(caps) == 0 {
		if _, ok := recipes[XHSRecipePublish]; ok {
			caps[CapPublish] = true
		}
		if _, ok := recipes[XHSRecipeMetrics]; ok {
			caps[CapMetrics] = true
		}
		if _, ok := recipes[XHSRecipeEngagements]; ok {
			caps[CapEngagements] = true
		}
		if _, ok := recipes[XHSRecipeReply]; ok {
			caps[CapReply] = true
		}
	}
	unified := make([]string, 0, len(metrics))
	for _, name := range metrics {
		if name = strings.TrimSpace(name); name != "" {
			unified = append(unified, name)
		}
	}
	return &BrowserAdapter{
		worker:        worker,
		recipes:       recipes,
		caps:          caps,
		limits:        limits,
		metricAliases: metrics,
		unified:       unified,
		Backend:       browser.BackendLocal,
	}
}

func (a *BrowserAdapter) Capabilities() CapabilitySet { return a.caps }

// BindAccount pins the adapter to a bot row. The resolver calls it when the
// adapter is built, so a caller that only knows the platform account name (for
// example the publisher) can still EnsureSession without a numeric ref.
func (a *BrowserAdapter) BindAccount(botID int64) {
	a.mu.Lock()
	a.botID = botID
	a.mu.Unlock()
}

// EnsureSession binds the account and lets the login recipe confirm (or start
// the QR re-login flow for) the stored session. accountRef may be the bot id;
// when it is not numeric the account bound by the resolver is kept.
func (a *BrowserAdapter) EnsureSession(ctx context.Context, accountRef string) error {
	botID, err := parseXHSAccountRef(accountRef)
	switch {
	case err == nil:
		a.BindAccount(botID)
	case !a.bound():
		return err
	}
	if _, ok := a.recipes[XHSRecipeLogin]; !ok {
		return nil
	}
	_, err = a.execute(ctx, CapPublish, XHSRecipeLogin, nil)
	return err
}

func (a *BrowserAdapter) HealthCheck(ctx context.Context) error {
	if _, ok := a.recipes[XHSRecipeHealth]; !ok {
		return nil
	}
	_, err := a.execute(ctx, CapMetrics, XHSRecipeHealth, nil)
	return err
}

func (a *BrowserAdapter) Publish(ctx context.Context, payload any) (Receipt, error) {
	p, ok := browserPayload(payload)
	if !ok {
		return Receipt{}, invalidPayload(CapPublish, fmt.Sprintf("channel: unsupported payload type %T", payload))
	}
	kind, input, err := a.publishInput(p)
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
	return xhsReceipt(res, "url"), nil
}

func browserPayload(payload any) (BrowserPublishPayload, bool) {
	switch p := payload.(type) {
	case BrowserPublishPayload:
		return p, true
	case *BrowserPublishPayload:
		if p == nil {
			return BrowserPublishPayload{}, false
		}
		return *p, true
	default:
		return BrowserPublishPayload{}, false
	}
}

func (a *BrowserAdapter) publishInput(p BrowserPublishPayload) (string, map[string]string, error) {
	kind := strings.ToLower(strings.TrimSpace(p.Kind))
	if kind == "" {
		kind = "post"
	}
	title := strings.TrimSpace(p.Title)
	body := strings.TrimSpace(p.Body)
	if title == "" {
		return "", nil, invalidPayload(CapPublish, "channel: title is required")
	}
	if body == "" {
		return "", nil, invalidPayload(CapPublish, "channel: body is required")
	}
	if max := a.limits.MaxTitleChars; max > 0 && utf8Len(title) > max {
		return "", nil, invalidPayload(CapPublish,
			fmt.Sprintf("channel: title exceeds %d characters", max))
	}
	if max := a.limits.MaxChars; max > 0 && utf8Len(body) > max {
		return "", nil, invalidPayload(CapPublish,
			fmt.Sprintf("channel: body exceeds %d characters", max))
	}
	if max := a.limits.MaxImages; max > 0 && len(p.Images) > max {
		return "", nil, invalidPayload(CapPublish,
			fmt.Sprintf("channel: at most %d images are allowed", max))
	}
	if ratio := strings.TrimSpace(p.ImageRatio); ratio != "" && a.limits.ImageRatio != "" && ratio != a.limits.ImageRatio {
		return "", nil, invalidPayload(CapPublish,
			fmt.Sprintf("channel: image ratio %s, want %s", ratio, a.limits.ImageRatio))
	}
	if max := a.limits.VideoSec; max > 0 && p.DurationSec > max {
		return "", nil, invalidPayload(CapPublish,
			fmt.Sprintf("channel: video duration %ds exceeds %ds", p.DurationSec, max))
	}
	if kind == XHSKindVideo && strings.TrimSpace(p.VideoPath) == "" {
		return "", nil, invalidPayload(CapPublish, "channel: video requires a video path")
	}

	input := map[string]string{
		"kind":  kind,
		"title": title,
		"body":  body,
	}
	if len(p.Images) > 0 {
		input["images"] = strings.Join(p.Images, ",")
	}
	if video := strings.TrimSpace(p.VideoPath); video != "" {
		input["video"] = video
	}
	if cover := strings.TrimSpace(p.Cover); cover != "" {
		input["cover"] = cover
	}
	if tags := xhsTags(p.Tags); tags != "" {
		input["tags"] = tags
	}
	for key, value := range p.Params {
		if _, exists := input[key]; !exists && strings.TrimSpace(value) != "" {
			input[key] = value
		}
	}
	return kind, input, nil
}

// FetchMetrics runs the metrics recipe and folds its native keys into the
// unified namespace declared by the pack.
func (a *BrowserAdapter) FetchMetrics(ctx context.Context, from, to string) (map[string]float64, error) {
	rng, err := xhsDateRange(from, to)
	if err != nil {
		return nil, err
	}
	res, err := a.execute(ctx, CapMetrics, XHSRecipeMetrics, map[string]string{
		"range": rng,
		"from":  strings.TrimSpace(from),
		"to":    strings.TrimSpace(to),
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(a.unified))
	for native, raw := range res.Values {
		unified, ok := a.metricAliases[native]
		if !ok {
			continue
		}
		value, err := parseXHSMetric(raw)
		if err != nil {
			return nil, invalidPayload(CapMetrics, fmt.Sprintf("channel: metric %s: %v", native, err))
		}
		out[unified] = value
	}
	for _, key := range a.unified {
		if _, ok := out[key]; !ok {
			out[key] = 0
		}
	}
	return out, nil
}

func (a *BrowserAdapter) ListEngagements(ctx context.Context, cursor string) ([]Engagement, error) {
	res, err := a.execute(ctx, CapEngagements, XHSRecipeEngagements, map[string]string{
		"cursor": strings.TrimSpace(cursor),
	})
	if err != nil {
		return nil, err
	}
	return parseXHSEngagements(res.Values["engagements"])
}

func (a *BrowserAdapter) Reply(ctx context.Context, engagementID, text string) (Receipt, error) {
	id := strings.TrimSpace(engagementID)
	body := strings.TrimSpace(text)
	if id == "" {
		return Receipt{}, invalidPayload(CapReply, "channel: engagement id is required")
	}
	if body == "" {
		return Receipt{}, invalidPayload(CapReply, "channel: reply text is required")
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

func (a *BrowserAdapter) execute(ctx context.Context, capability Capability, recipeName string, input map[string]string) (browser.Result, error) {
	if a.worker == nil {
		return browser.Result{}, &AdapterError{
			Capability: capability,
			Code:       CodeUnsupported,
			Err:        errors.New("channel: browser worker is not configured"),
		}
	}
	recipe, ok := a.recipes[recipeName]
	if !ok {
		return browser.Result{}, &AdapterError{
			Capability: capability,
			Code:       CodeUnsupported,
			Err:        fmt.Errorf("channel: recipe %q is not configured", recipeName),
		}
	}
	botID, ok := a.account()
	if !ok {
		return browser.Result{}, &AdapterError{
			Capability: capability,
			Code:       CodeCredentialInvalid,
			Err:        errors.New("channel: no account bound; call EnsureSession first"),
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

func (a *BrowserAdapter) account() (int64, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.botID, a.botID > 0
}

func (a *BrowserAdapter) bound() bool {
	_, ok := a.account()
	return ok
}

func (a *BrowserAdapter) backend() string {
	if a.Backend == "" {
		return browser.BackendLocal
	}
	return a.Backend
}

// utf8Len counts characters the way platform limits do (runes, not bytes).
func utf8Len(s string) int {
	return utf8.RuneCountInString(s)
}
