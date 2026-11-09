package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
)

// OperationCall is one provider-contract operation handed to the shared
// integration proxy (auth injection, scoping, egress policy and audit all live
// there — never in a channel adapter).
type OperationCall struct {
	Provider  string
	Operation string
	Params    map[string]any
	// Body / Query carry an explicitly rendered request (templates); when nil
	// the proxy folds Params by the contract's parameter locations.
	Body  map[string]any
	Query map[string]any
	// Files maps an operation file param to a local path (declarative
	// multipart upload).
	Files map[string]string
	// Account scope: the proxy resolves THIS account's credential alias under
	// the bot's owner, so multi-account isolation is preserved without a
	// project source binding.
	OwnerType string
	OwnerID   int64
	UserID    int64
	Alias     string
	// Account is the platform account ref (e.g. a Facebook Page id). It is
	// passed as the "account" operation parameter so packs can scope paths.
	Account string
}

// OperationResult is the normalized proxy response.
type OperationResult struct {
	Status int
	Body   map[string]any
}

// OperationCaller executes contract operations. The server wires it to
// service.ExternalProxyService; tests can supply a fake.
type OperationCaller interface {
	CallOperation(ctx context.Context, call OperationCall) (OperationResult, error)
}

// OpenAPIPublishPayload is the platform-neutral publish request. Pack bindings
// decide which fields/params reach which contract operation, so adding a
// platform needs a Pack (and a provider contract), never new Go code.
type OpenAPIPublishPayload struct {
	Fields map[string]any
	Files  map[string]string
}

// openAPIBindings is the per-capability declarative binding set.
type openAPIBindings map[Capability]packs.Binding

// OpenAPIAdapter drives any platform that ships an OpenAPI provider contract.
// Every capability is a declared operation mapping — the adapter itself is
// platform-agnostic.
type OpenAPIAdapter struct {
	Caller   OperationCaller
	Provider string
	Bindings openAPIBindings
	// MetricsMap maps native metric keys to the unified namespace.
	MetricsMap map[string]string
	// Account scope handed to the proxy so it can pick this account's
	// credential (owner-scoped alias).
	OwnerType string
	OwnerID   int64
	UserID    int64
	Alias     string
	// Account is the platform account ref (e.g. a Facebook Page id). It is
	// passed as the "account" operation parameter so packs can scope paths.
	Account string
}

var _ ChannelAdapter = (*OpenAPIAdapter)(nil)

// NewOpenAPIAdapter builds the generic adapter. bindings keys are capabilities
// (publish/metrics/engagements/reply).
func NewOpenAPIAdapter(caller OperationCaller, provider string, bindings map[Capability]packs.Binding, metricsMap map[string]string) *OpenAPIAdapter {
	cp := make(openAPIBindings, len(bindings))
	for cap, b := range bindings {
		cp[cap] = b
	}
	return &OpenAPIAdapter{Caller: caller, Provider: provider, Bindings: cp, MetricsMap: metricsMap}
}

func (a *OpenAPIAdapter) call(ctx context.Context, operation string, params, body, query map[string]any, files map[string]string) (OperationResult, error) {
	if strings.TrimSpace(a.Account) != "" {
		if params == nil {
			params = map[string]any{}
		}
		if _, exists := params["account"]; !exists {
			params["account"] = a.Account
		}
	}
	return a.Caller.CallOperation(ctx, OperationCall{
		Provider:  a.Provider,
		Operation: operation,
		Params:    params,
		Body:      body,
		Query:     query,
		Files:     files,
		OwnerType: a.OwnerType,
		OwnerID:   a.OwnerID,
		UserID:    a.UserID,
		Alias:     a.Alias,
	})
}

func (a *OpenAPIAdapter) Capabilities() CapabilitySet {
	set := CapabilitySet{}
	for cap, b := range a.Bindings {
		if strings.TrimSpace(b.Operation) != "" || len(b.Operations) > 0 {
			set[cap] = true
		}
	}
	return set
}

// EnsureSession has nothing platform-specific to do: the proxy resolves and
// verifies the account credential on every call.
func (a *OpenAPIAdapter) EnsureSession(_ context.Context, accountRef string) error {
	if strings.TrimSpace(accountRef) == "" {
		return &AdapterError{Capability: CapPublish, Code: CodeInvalidPayload,
			Err: fmt.Errorf("opsbot: account reference is required")}
	}
	return nil
}

func (a *OpenAPIAdapter) HealthCheck(context.Context) error { return nil }

func (a *OpenAPIAdapter) Publish(ctx context.Context, payload any) (Receipt, error) {
	b, ok := a.Bindings[CapPublish]
	if !ok {
		return Receipt{}, unsupported(CapPublish)
	}
	fields, files, err := publishPayload(payload)
	if err != nil {
		return Receipt{}, invalidPayload(CapPublish, err.Error())
	}

	steps, err := orderedSteps(b)
	if err != nil {
		return Receipt{}, invalidPayload(CapPublish, err.Error())
	}

	carried := map[string]any{}
	var last OperationResult
	for _, step := range steps {
		bag := mergedBag(fields, carried)

		params := map[string]any{}
		allowed := b.StepFields[step.name]
		for key, value := range fields {
			if len(allowed) > 0 && !containsString(allowed, key) {
				continue
			}
			params[mapKey(b.Params, key)] = value
		}
		for key, value := range carried {
			params[key] = value
		}

		stepFiles := map[string]string{}
		for param, field := range b.Files[step.name] {
			path := strings.TrimSpace(files[field])
			if path == "" {
				path = strings.TrimSpace(files[param])
			}
			if path != "" {
				stepFiles[param] = path
			}
		}

		var body map[string]any
		if template, ok := b.Templates[step.name]; ok {
			rendered, err := renderTemplate(template, bag)
			if err != nil {
				return Receipt{}, invalidPayload(CapPublish, fmt.Sprintf("template %s: %v", step.name, err))
			}
			if object, ok := rendered.(map[string]any); ok {
				body = object
			} else {
				return Receipt{}, invalidPayload(CapPublish, fmt.Sprintf("template %s must render an object", step.name))
			}
		}

		result, err := a.call(ctx, step.operation, params, body, nil, stepFiles)
		if err != nil {
			return Receipt{}, callError(CapPublish, err)
		}
		if result.Status >= 400 {
			return Receipt{}, statusError(CapPublish, result.Status)
		}
		last = result
		// Carry scalar outputs forward so a later step can reference them
		// (e.g. draft media_id -> freepublish media_id) in its template.
		for key, value := range result.Body {
			switch value.(type) {
			case string, float64, bool, json.Number:
				carried[key] = value
			}
		}
	}

	receipt := Receipt{Raw: stringify(last.Body)}
	if field := strings.TrimSpace(b.Outputs["id"]); field != "" {
		receipt.ExternalID = scalarString(last.Body[field])
	}
	if field := strings.TrimSpace(b.Outputs["url"]); field != "" {
		receipt.URL = scalarString(last.Body[field])
	}
	return receipt, nil
}

func (a *OpenAPIAdapter) FetchMetrics(ctx context.Context, from, to string) (map[string]float64, error) {
	b, ok := a.Bindings[CapMetrics]
	if !ok {
		return nil, unsupported(CapMetrics)
	}
	params := map[string]any{
		mapKey(b.Params, "from"): from,
		mapKey(b.Params, "to"):   to,
	}
	result, err := a.call(ctx, b.Operation, params, nil, nil, nil)
	if err != nil {
		return nil, callError(CapMetrics, err)
	}
	if result.Status >= 400 {
		return nil, statusError(CapMetrics, result.Status)
	}
	out := map[string]float64{}
	if b.Items != "" {
		// Insights-style response: [{name:"page_fans", values:[{value:...}]}].
		raw, ok := result.Body[b.Items].([]any)
		if ok {
			for _, entry := range raw {
				row, ok := entry.(map[string]any)
				if !ok {
					continue
				}
				name := scalarString(row["name"])
				if name == "" {
					continue
				}
				metric, ok := a.MetricsMap[name]
				if !ok {
					metric = name
				}
				if number, ok := insightValue(row, b.ValueKey); ok {
					out[metric] = number
				}
			}
		}
	} else {
		for key, value := range result.Body {
			metric, ok := a.MetricsMap[key]
			if !ok {
				metric = key
			}
			if number, ok := numeric(value); ok {
				out[metric] = number
			}
		}
	}
	for _, key := range unifiedMetrics {
		if _, ok := out[key]; !ok {
			out[key] = 0
		}
	}
	return out, nil
}

func (a *OpenAPIAdapter) ListEngagements(ctx context.Context, cursor string) ([]Engagement, error) {
	b, ok := a.Bindings[CapEngagements]
	if !ok {
		return nil, unsupported(CapEngagements)
	}
	// Two-step listing (e.g. WeChat: published articles -> their comments):
	// Operations{"list": ..., "comments": ...}. The list step enumerates parent
	// items, then the comments step runs once per item.
	if listOp, ok := b.Operations["list"]; ok {
		return a.listNestedEngagements(ctx, b, listOp, cursor)
	}
	params := map[string]any{}
	if cursor != "" {
		params[mapKey(b.Params, "cursor")] = cursor
	}
	result, err := a.call(ctx, b.Operation, params, nil, nil, nil)
	if err != nil {
		return nil, callError(CapEngagements, err)
	}
	if result.Status >= 400 {
		return nil, statusError(CapEngagements, result.Status)
	}
	items, err := engagementItems(result.Body, b.Items)
	if err != nil {
		return nil, invalidPayload(CapEngagements, err.Error())
	}
	out := make([]Engagement, 0, len(items))
	for _, item := range items {
		engagement := Engagement{
			ExtID:  scalarString(item[fieldName(b.Fields, "id", "id")]),
			Author: scalarString(item[fieldName(b.Fields, "author", "author")]),
			Text:   scalarString(item[fieldName(b.Fields, "text", "text")]),
			Kind:   scalarString(item[fieldName(b.Fields, "kind", "kind")]),
		}
		if engagement.Kind == "" {
			engagement.Kind = "comment"
		}
		if engagement.ExtID == "" {
			continue
		}
		out = append(out, engagement)
	}
	return out, nil
}

func (a *OpenAPIAdapter) Reply(ctx context.Context, engagementID, text string) (Receipt, error) {
	b, ok := a.Bindings[CapReply]
	if !ok {
		return Receipt{}, unsupported(CapReply)
	}
	if strings.TrimSpace(engagementID) == "" || strings.TrimSpace(text) == "" {
		return Receipt{}, invalidPayload(CapReply, "opsbot: reply needs an engagement id and text")
	}
	params := map[string]any{}
	for key, value := range b.Fixed {
		params[key] = value
	}
	// Nested listings encode "<parent>:<comment>"; when the binding declares a
	// parent parameter the id is split so the reply targets the right article.
	if parentParam, ok := b.Params["parent"]; ok {
		parts := strings.SplitN(engagementID, ":", 2)
		if len(parts) != 2 {
			return Receipt{}, invalidPayload(CapReply, "opsbot: engagement id is malformed")
		}
		params[parentParam] = parts[0]
		params[mapKey(b.Params, "id")] = parts[1]
	} else {
		params[mapKey(b.Params, "id")] = engagementID
	}
	params[mapKey(b.Params, "text")] = text
	result, err := a.call(ctx, b.Operation, params, nil, nil, nil)
	if err != nil {
		return Receipt{}, callError(CapReply, err)
	}
	if result.Status >= 400 {
		return Receipt{}, statusError(CapReply, result.Status)
	}
	receipt := Receipt{ExternalID: engagementID, Raw: stringify(result.Body)}
	if field := strings.TrimSpace(b.Outputs["id"]); field != "" {
		if id := scalarString(result.Body[field]); id != "" {
			receipt.ExternalID = id
		}
	}
	return receipt, nil
}

type publishStep struct {
	name      string
	operation string
}

// stepOrder is the fixed execution order for multi-step publishes. Unknown step
// names run afterwards in lexical order.
var stepOrder = []string{"upload", "draft", "publish"}

func orderedSteps(b packs.Binding) ([]publishStep, error) {
	if len(b.Operations) == 0 {
		op := strings.TrimSpace(b.Operation)
		if op == "" {
			return nil, fmt.Errorf("opsbot: publish binding declares no operation")
		}
		return []publishStep{{name: "publish", operation: op}}, nil
	}
	steps := make([]publishStep, 0, len(b.Operations))
	seen := map[string]bool{}
	for _, name := range stepOrder {
		if op, ok := b.Operations[name]; ok {
			steps = append(steps, publishStep{name: name, operation: op})
			seen[name] = true
		}
	}
	rest := make([]string, 0, len(b.Operations))
	for name := range b.Operations {
		if !seen[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	for _, name := range rest {
		steps = append(steps, publishStep{name: name, operation: b.Operations[name]})
	}
	return steps, nil
}

func publishPayload(payload any) (map[string]any, map[string]string, error) {
	switch p := payload.(type) {
	case OpenAPIPublishPayload:
		return p.Fields, p.Files, nil
	case *OpenAPIPublishPayload:
		if p == nil {
			return nil, nil, fmt.Errorf("opsbot: publish payload is nil")
		}
		return p.Fields, p.Files, nil
	case map[string]any:
		return p, nil, nil
	case map[string]string:
		fields := make(map[string]any, len(p))
		for key, value := range p {
			fields[key] = value
		}
		return fields, nil, nil
	default:
		return nil, nil, fmt.Errorf("opsbot: unsupported openapi publish payload %T", payload)
	}
}

func mapKey(mapping map[string]string, key string) string {
	if mapped := strings.TrimSpace(mapping[key]); mapped != "" {
		return mapped
	}
	return key
}

func fieldName(mapping map[string]string, semantic, fallback string) string {
	if mapped := strings.TrimSpace(mapping[semantic]); mapped != "" {
		return mapped
	}
	return fallback
}

func engagementItems(body map[string]any, itemsKey string) ([]map[string]any, error) {
	raw, ok := body[itemsKey]
	if !ok || itemsKey == "" {
		if items, ok := body["items"].([]any); ok {
			return toMaps(items)
		}
		return nil, fmt.Errorf("opsbot: response has no %q engagement list", itemsKey)
	}
	switch list := raw.(type) {
	case []any:
		return toMaps(list)
	case map[string]any:
		return []map[string]any{list}, nil
	default:
		return nil, fmt.Errorf("opsbot: %q is not an engagement list", itemsKey)
	}
}

func toMaps(items []any) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

func scalarString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.6f", v), "0"), ".")
	default:
		return ""
	}
}

func insightValue(row map[string]any, valueKey string) (float64, bool) {
	if valueKey == "" {
		return numeric(row["value"])
	}
	values, ok := row[valueKey].([]any)
	if !ok || len(values) == 0 {
		return 0, false
	}
	first, ok := values[0].(map[string]any)
	if !ok {
		return numeric(values[0])
	}
	return numeric(first["value"])
}

func numeric(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case string:
		var f float64
		if _, err := fmt.Sscanf(strings.TrimSpace(v), "%f", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

func cloneParams(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func stringify(body map[string]any) string {
	data, err := json.Marshal(body)
	if err != nil {
		return ""
	}
	return string(data)
}

func unsupported(cap Capability) *AdapterError {
	return &AdapterError{Capability: cap, Code: CodeUnsupported,
		Err: fmt.Errorf("opsbot: %s is not supported by this pack", cap)}
}

func callError(cap Capability, err error) *AdapterError {
	return &AdapterError{Capability: cap, Code: CodeTransport, Retryable: true, Err: err}
}

func statusError(cap Capability, status int) *AdapterError {
	code := CodeTransport
	retryable := status >= 500
	switch status {
	case 401, 403:
		code, retryable = CodeCredentialInvalid, false
	case 429:
		code, retryable = CodeRateLimited, true
	}
	return &AdapterError{Capability: cap, Code: code, Retryable: retryable,
		Err: fmt.Errorf("opsbot: integration proxy returned http %d", status)}
}

func mergedBag(fields, carried map[string]any) map[string]any {
	out := make(map[string]any, len(fields)+len(carried))
	for key, value := range fields {
		out[key] = value
	}
	for key, value := range carried {
		out[key] = value
	}
	return out
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// renderTemplate substitutes {{key}} placeholders from the bag, recursively,
// so a Pack declares the request shape (e.g. WeChat's articles array) without
// any Go code.
func renderTemplate(template any, bag map[string]any) (any, error) {
	switch value := template.(type) {
	case string:
		return renderTemplateString(value, bag), nil
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, item := range value {
			rendered, err := renderTemplate(item, bag)
			if err != nil {
				return nil, err
			}
			out[key] = rendered
		}
		return out, nil
	case []any:
		out := make([]any, 0, len(value))
		for _, item := range value {
			rendered, err := renderTemplate(item, bag)
			if err != nil {
				return nil, err
			}
			out = append(out, rendered)
		}
		return out, nil
	default:
		return value, nil
	}
}

func renderTemplateString(template string, bag map[string]any) string {
	if !strings.Contains(template, "{{") {
		return template
	}
	var out strings.Builder
	rest := template
	for {
		start := strings.Index(rest, "{{")
		if start < 0 {
			out.WriteString(rest)
			break
		}
		end := strings.Index(rest[start:], "}}")
		if end < 0 {
			out.WriteString(rest)
			break
		}
		out.WriteString(rest[:start])
		key := strings.TrimSpace(rest[start+2 : start+end])
		out.WriteString(scalarString(bag[key]))
		rest = rest[start+end+2:]
	}
	return out.String()
}

// listNestedEngagements enumerates parent items (published articles) and then
// pulls the engagement list for each — declared entirely in the binding:
// Operations{"list","comments"}, Items (parent array key), Fields (engagement
// field map) and Params (the parent key passed to the comments operation).
func (a *OpenAPIAdapter) listNestedEngagements(ctx context.Context, b packs.Binding, listOp, cursor string) ([]Engagement, error) {
	listParams := map[string]any{}
	if cursor != "" {
		listParams[mapKey(b.Params, "cursor")] = cursor
	}
	listed, err := a.call(ctx, listOp, listParams, nil, nil, nil)
	if err != nil {
		return nil, callError(CapEngagements, err)
	}
	if listed.Status >= 400 {
		return nil, statusError(CapEngagements, listed.Status)
	}
	parents, err := engagementItems(listed.Body, b.Items)
	if err != nil {
		return nil, invalidPayload(CapEngagements, err.Error())
	}
	parentKey := mapKey(b.Params, "parent")
	out := make([]Engagement, 0)
	for _, parent := range parents {
		id := scalarString(parent[fieldName(b.Fields, "parent_id", "article_id")])
		if id == "" {
			continue
		}
		params := map[string]any{parentKey: id}
		for key, value := range b.Fixed {
			params[key] = value
		}
		result, err := a.call(ctx, b.Operations["comments"], params, nil, nil, nil)
		if err != nil {
			return nil, callError(CapEngagements, err)
		}
		if result.Status >= 400 {
			return nil, statusError(CapEngagements, result.Status)
		}
		items, err := engagementItems(result.Body, defaultStr(b.SubItems))
		if err != nil {
			continue
		}
		for _, item := range items {
			commentID := scalarString(item[fieldName(b.Fields, "id", "id")])
			engagement := Engagement{
				ExtID:  commentID,
				Author: scalarString(item[fieldName(b.Fields, "author", "author")]),
				Text:   scalarString(item[fieldName(b.Fields, "text", "text")]),
				Kind:   scalarString(item[fieldName(b.Fields, "kind", "kind")]),
			}
			if commentID != "" {
				engagement.ExtID = id + ":" + commentID
			}
			if engagement.Kind == "" {
				engagement.Kind = "comment"
			}
			if engagement.ExtID != "" {
				out = append(out, engagement)
			}
		}
	}
	return out, nil
}

func defaultStr(value string) string {
	if strings.TrimSpace(value) == "" {
		return "comment"
	}
	return value
}
