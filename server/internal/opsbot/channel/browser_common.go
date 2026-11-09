package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/opsbot/browser"
	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
)

// Canonical recipe names every browser pack uses. A pack that needs a variant
// (e.g. publish_video) declares it explicitly.
const (
	RecipeLogin        = "login"
	RecipePublish      = "publish"
	RecipePublishVideo = "publish_video"
	RecipeMetrics      = "metrics"
	RecipeEngagements  = "engagements"
	RecipeReply        = "reply"
	RecipeHealth       = "health"
)

const (
	KindNote  = "note"
	KindVideo = "video"
)

const (
	MetricSaves    = "saves"
	MetricComments = "comments"
)

const (
	CodeLoginExpired = "login_expired"
	CodeRiskBlocked  = "risk_blocked"
	CodeRecipeBroken = "recipe_broken"
)

func parseAccountRef(accountRef string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(accountRef), 10, 64)
	if err != nil || id <= 0 {
		return 0, &AdapterError{
			Capability: CapPublish,
			Code:       CodeCredentialInvalid,
			Err:        fmt.Errorf("channel: invalid account reference %q", accountRef),
		}
	}
	return id, nil
}

func browserReceipt(res browser.Result, primary string) Receipt {
	value := strings.TrimSpace(res.Values[primary])
	for _, fallback := range []string{"url", "message_id", "note_id", "article_url"} {
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

// parseEngagementList accepts the JSON array a recipe extracts, with a
// newline/tab (or pipe) fallback for flat text extraction.
func parseEngagementList(raw string) ([]Engagement, error) {
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
					ExtID:  firstField(fields, "id", "engagement_id", "comment_id", "message_id"),
					Kind:   firstField(fields, "kind", "type"),
					Author: firstField(fields, "author", "nickname", "user"),
					Text:   firstField(fields, "text", "content", "body"),
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
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			fields = strings.Split(line, "|")
		}
		if len(fields) < 2 {
			continue
		}
		item := Engagement{ExtID: strings.TrimSpace(fields[0]), Kind: strings.TrimSpace(fields[1])}
		if len(fields) > 2 {
			item.Author = strings.TrimSpace(fields[2])
		}
		if len(fields) > 3 {
			item.Text = strings.Join(fields[3:], " ")
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

func firstField(item map[string]any, keys ...string) string {
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

// parseMetricValue turns dashboard display text ("1,234", "1.2万") into a
// number. An empty value is a legitimate zero.
func parseMetricValue(raw string) (float64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, nil
	}
	s = strings.NewReplacer(",", "", "，", "", " ", "", "+", "").Replace(s)
	multiplier := 1.0
	for _, unit := range []struct {
		suffix string
		scale  float64
	}{{"亿", 1e8}, {"万", 1e4}, {"w", 1e4}, {"W", 1e4}} {
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

// BrowserWorker runs one recipe task on one account's browser session. The
// Worker in opsbot/browser satisfies it; tests inject a fake.
type BrowserWorker interface {
	Run(ctx context.Context, task browser.Task) (browser.Result, error)
}

// RecipeSet maps a canonical recipe name to its parsed recipe.
type RecipeSet map[string]packs.Recipe

func tagsValue(tags []string) string {
	cleaned := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(tag), "#"))
		if tag != "" {
			cleaned = append(cleaned, tag)
		}
	}
	return strings.Join(cleaned, ",")
}

func dateRangePreset(from, to string) (string, error) {
	start, err := time.Parse("2006-01-02", strings.TrimSpace(from))
	if err != nil {
		return "", invalidPayload(CapMetrics, fmt.Sprintf("channel: invalid metrics start %q", from))
	}
	end, err := time.Parse("2006-01-02", strings.TrimSpace(to))
	if err != nil {
		return "", invalidPayload(CapMetrics, fmt.Sprintf("channel: invalid metrics end %q", to))
	}
	if end.Before(start) {
		return "", invalidPayload(CapMetrics, fmt.Sprintf("channel: metrics range %q..%q is reversed", from, to))
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
		return "", invalidPayload(CapMetrics, fmt.Sprintf("channel: metrics range %d days exceeds the 90-day preset", days))
	}
}

func browserError(capability Capability, err error) error {
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
