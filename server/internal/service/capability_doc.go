// Package service — the operable capability description (Epic #726 / #738).
//
// A CapabilityDoc is the "how do I drive this thing" document for one
// onboarding target — an external API provider (tool line) or a data source
// (data line). It has two layers:
//
//   - a STRUCTURED layer: the operations a provider exposes (semantic name,
//     method, path, parameters, read-only vs write) or a data source's usable
//     range (kind + allow/deny scope), plus the limits that apply (pagination,
//     row cap, single-statement, confirmation);
//   - a NATURAL-LANGUAGE layer: a short "how to use this system" paragraph and
//     representative task examples ("查订单：run_data_query(source=X, statement=…)").
//
// Single source of truth (the anti-drift rule). The document is a PURE FUNCTION
// of the CONFIRMED contract (provider line) or the CONFIRMED source metadata
// (data line) — never of the source document's prose, never of a model's
// narrative. There is deliberately no hand-written text and no agent-authored
// text anywhere in it: an agent cannot modify a capability description because
// there is no path that accepts one (see capability_service.go). When the
// contract changes its digest changes, the stored doc goes stale, and the next
// read regenerates it — so the description cannot drift from what the proxy
// actually enforces.
//
// Injection defence. Every free-text field the built doc carries (operation
// summaries/notes, parameter descriptions, provider notes, target names) is
// sanitized on the way in: marker sequences are neutralised so the text cannot
// break out of the data partition, and text that reads as an instruction is
// replaced outright (reusing the untrusted-input classifier). The rendered
// agent block is then presented as a DATA section, clearly fenced off from the
// agent's instructions — so a poisoned summary cannot turn into a directive.
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/agentproxy"
)

// CapabilitySchemaVersion versions the CapabilityDoc wire shape. Bump it when a
// field is renamed or its meaning changes (additive fields do not).
const CapabilitySchemaVersion = "1"

// Capability target kinds. A capability doc is always about exactly one target.
const (
	CapabilityTargetProvider   = "provider"
	CapabilityTargetDataSource = "data_source"
)

// Tool names the agent invokes for each line.
const (
	capabilityToolProvider   = "call_external_api"
	capabilityToolDataSource = "run_data_query"
)

// CapabilityDoc is the two-layer capability description for one target.
type CapabilityDoc struct {
	SchemaVersion string `json:"schema_version"`
	// TargetKind is "provider" or "data_source".
	TargetKind string `json:"target_kind"`
	// Name is the stable identifier the agent passes as `provider=` / `source=`.
	Name string `json:"name"`
	// DisplayName is a human label (provider label / source name). Display only.
	DisplayName string `json:"display_name,omitempty"`
	// SourceDigest is the digest of the CONFIRMED source this doc was rendered
	// from: the provider contract's digest, or the data source's metadata
	// digest. A stored doc whose SourceDigest differs from the current source
	// digest is stale and must be regenerated.
	SourceDigest string `json:"source_digest"`
	// Tool is the MCP tool the agent calls: call_external_api | run_data_query.
	Tool string `json:"tool"`

	// --- structured layer (system-rendered from the confirmed facts) ---

	// Operations is the provider line's operation list, in contract order.
	Operations []CapabilityOperation `json:"operations,omitempty"`
	// Scope is the data line's usable range, from scope_config.
	Scope *CapabilityScope `json:"scope,omitempty"`
	// Limits are the operational constraints an agent must respect.
	Limits []string `json:"limits,omitempty"`

	// --- natural-language layer (rendered from the structured facts) ---

	// Usage is a short "how to use this system" paragraph.
	Usage string `json:"usage"`
	// Examples are representative task → call pairs.
	Examples []CapabilityExample `json:"examples,omitempty"`
}

// CapabilityOperation is one named provider operation, rendered for the agent.
type CapabilityOperation struct {
	Name     string            `json:"name"`
	Method   string            `json:"method"`
	Path     string            `json:"path"`
	Summary  string            `json:"summary,omitempty"`
	ReadOnly bool              `json:"read_only"`
	Params   []CapabilityParam `json:"parameters,omitempty"`
	// Notes carries the contract's operation-specific guidance (pagination,
	// rate limits, common errors) verbatim-but-sanitized.
	Notes string `json:"notes,omitempty"`
}

// CapabilityParam is one operation input.
type CapabilityParam struct {
	Name        string `json:"name"`
	In          string `json:"in"`
	Required    bool   `json:"required,omitempty"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
}

// CapabilityScope is the data line's authorized range, interpreted per kind.
type CapabilityScope struct {
	Kind string `json:"kind"`
	// Allow/Deny are the per-kind allow/deny lists (table names, key prefixes,
	// collections, indices, or HTTP path prefixes).
	Allow []string `json:"allow"`
	Deny  []string `json:"deny,omitempty"`
	// Databases is the SQL/Trino database allow-list.
	Databases []string `json:"databases,omitempty"`
	// Methods is the http kind's verb allow-list.
	Methods []string `json:"methods,omitempty"`
	// AccessMode is the source's default access mode (read | readwrite).
	AccessMode string `json:"default_access_mode"`
}

// CapabilityExample is one task → call pair.
type CapabilityExample struct {
	Task string `json:"task"`
	Call string `json:"call"`
}

// --- generation: provider line ---------------------------------------------

// BuildProviderCapabilityDoc renders a provider's capability doc from its
// CONFIRMED contract. It is a pure function: identical contracts yield byte-
// identical docs (no timestamps, contract order preserved), so the injected
// agent block is stable across spawns and the prompt prefix stays cacheable.
func BuildProviderCapabilityDoc(p *ProviderDef) *CapabilityDoc {
	if p == nil {
		return nil
	}
	contract := p.Contract() // derives + finalizes (digest reflects current bytes)
	doc := &CapabilityDoc{
		SchemaVersion: CapabilitySchemaVersion,
		TargetKind:    CapabilityTargetProvider,
		Name:          sanitizeCapabilityText(p.Name),
		DisplayName:   sanitizeCapabilityProse(pickNonEmpty(p.Label, p.Name)),
		SourceDigest:  contract.Digest,
		Tool:          capabilityToolProvider,
		Operations:    []CapabilityOperation{},
		Limits:        []string{},
		Examples:      []CapabilityExample{},
	}
	for _, op := range contract.Operations {
		cop := CapabilityOperation{
			Name:     sanitizeCapabilityText(op.Name),
			Method:   strings.ToUpper(strings.TrimSpace(op.Method)),
			Path:     sanitizeCapabilityText(op.Path),
			Summary:  sanitizeCapabilityProse(op.Summary),
			ReadOnly: op.ReadOnly,
			Notes:    sanitizeCapabilityProse(op.Notes),
		}
		for _, prm := range op.Parameters {
			cop.Params = append(cop.Params, CapabilityParam{
				Name:        sanitizeCapabilityText(prm.Name),
				In:          strings.TrimSpace(prm.In),
				Required:    prm.Required,
				Type:        sanitizeCapabilityText(prm.Type),
				Description: sanitizeCapabilityProse(prm.Description),
			})
		}
		doc.Operations = append(doc.Operations, cop)
	}
	doc.Limits = providerLimits(contract)
	doc.Usage = providerUsage(doc)
	doc.Examples = providerExamples(doc)
	return doc
}

// notesOrEmpty returns the contract's free-text agent guidance, or "" for nil.
func (c *ProviderContract) notesOrEmpty() string {
	if c == nil {
		return ""
	}
	return c.Notes
}

// providerUsage renders the natural-language "how to use" paragraph.
func providerUsage(doc *CapabilityDoc) string {
	var b strings.Builder
	fmt.Fprintf(&b, "这是一个外部 API 集成（工具型）。用 %s(provider=%q, operation=<语义操作名>) 调用；"+
		"先用 get_provider_schema(provider=%q) 读取最新操作清单与参数。", doc.Tool, doc.Name, doc.Name)
	if doc.DisplayName != "" && doc.DisplayName != doc.Name {
		fmt.Fprintf(&b, "（%s）", doc.DisplayName)
	}
	b.WriteString("鉴权由 niuniu 代理在出口自动注入——不要在调用里自带凭证。")
	if n := len(doc.Operations); n > 0 {
		fmt.Fprintf(&b, "共 %d 个操作（%d 只读 / %d 写或敏感）。", n, countReadOnly(doc.Operations), n-countReadOnly(doc.Operations))
	}
	return b.String()
}

func countReadOnly(ops []CapabilityOperation) int {
	n := 0
	for _, op := range ops {
		if op.ReadOnly {
			n++
		}
	}
	return n
}

// providerLimits lists the constraints that apply to every provider call.
func providerLimits(c *ProviderContract) []string {
	limits := []string{
		"写 / 敏感操作需要用户在确认卡批准后才可执行；只读操作按契约直接放行。",
		"每次调用都要显式给出 provider 与 operation（或 method+path）；路径参数会被自动折进 URL。",
	}
	if c != nil && c.VerifyOperation != "" {
		limits = append(limits, "连通性/鉴权可用 verify_operation="+sanitizeCapabilityText(c.VerifyOperation)+" 探针自检。")
	}
	if note := sanitizeCapabilityProse(c.notesOrEmpty()); note != "" {
		// The contract author's own guidance (pagination / rate limits / common
		// error semantics) rides along as a limit.
		limits = append(limits, note)
	}
	if c != nil && len(c.Operations) == 0 {
		limits = append(limits, "该 provider 尚未提供结构化契约（仅有 legacy profile），操作清单可能不完整。")
	}
	return limits
}

// providerExamples renders up to capabilityMaxExamples representative read-only
// calls, so the agent can construct its first request without any document.
const capabilityMaxExamples = 5

func providerExamples(doc *CapabilityDoc) []CapabilityExample {
	var out []CapabilityExample
	for _, op := range doc.Operations {
		if len(out) >= capabilityMaxExamples {
			break
		}
		if !op.ReadOnly {
			continue
		}
		out = append(out, CapabilityExample{
			Task: "调用只读操作 " + op.Name,
			Call: providerCallExample(doc.Name, op),
		})
	}
	if len(out) == 0 && len(doc.Operations) > 0 {
		// No read-only operation (or a legacy contract): still show one call
		// shape so the agent knows the envelope, marked as a write.
		op := doc.Operations[0]
		out = append(out, CapabilityExample{
			Task: "调用操作 " + op.Name + "（写/敏感，需用户确认）",
			Call: providerCallExample(doc.Name, op),
		})
	}
	return out
}

// providerCallExample renders one runnable call_external_api example. Required
// parameters are filled with a placeholder so the call shape is unambiguous.
func providerCallExample(provider string, op CapabilityOperation) string {
	parts := []string{fmt.Sprintf("provider=%q", provider), fmt.Sprintf("operation=%q", op.Name)}
	var pathParams, queryParams []string
	for _, prm := range op.Params {
		if !prm.Required {
			continue
		}
		switch prm.In {
		case "path":
			pathParams = append(pathParams, prm.Name)
		case "query":
			queryParams = append(queryParams, fmt.Sprintf("%q: %q", prm.Name, "<"+prm.Name+">"))
		}
	}
	if len(pathParams) > 0 {
		// Path parameters are folded from query/body by the proxy, so show them
		// in the query object where the caller supplies them.
		for _, n := range pathParams {
			queryParams = append(queryParams, fmt.Sprintf("%q: %q", n, "<"+n+">"))
		}
	}
	if len(queryParams) > 0 {
		parts = append(parts, "query={"+strings.Join(queryParams, ", ")+"}")
	}
	return fmt.Sprintf("%s(%s)", capabilityToolProvider, strings.Join(parts, ", "))
}

// --- generation: data-source line ------------------------------------------

// BuildDataSourceCapabilityDoc renders a data source's capability doc from its
// CONFIRMED metadata (kind + scope_config + non-secret config). Pure and
// deterministic, like the provider builder.
func BuildDataSourceCapabilityDoc(dto *DataSourceDTO) *CapabilityDoc {
	if dto == nil {
		return nil
	}
	doc := &CapabilityDoc{
		SchemaVersion: CapabilitySchemaVersion,
		TargetKind:    CapabilityTargetDataSource,
		Name:          sanitizeCapabilityText(dto.Name),
		DisplayName:   sanitizeCapabilityProse(dto.Name),
		SourceDigest:  DataSourceCapabilityDigest(dto),
		Tool:          capabilityToolDataSource,
		Scope:         dataSourceScope(dto),
		Limits:        dataSourceLimits(dto),
		Examples:      []CapabilityExample{},
	}
	doc.Usage = dataSourceUsage(doc, dto)
	doc.Examples = dataSourceExamples(doc, dto)
	return doc
}

// dataSourceScope extracts the authorized range from scope_config, interpreted
// per kind (tables_allow/deny are table names, key prefixes, collections,
// indices, or HTTP path prefixes depending on the kind).
func dataSourceScope(dto *DataSourceDTO) *CapabilityScope {
	sc := &CapabilityScope{
		Kind:       strings.TrimSpace(dto.Kind),
		Allow:      []string{},
		Deny:       []string{},
		Databases:  []string{},
		Methods:    []string{},
		AccessMode: normalizeAccessMode(dto.DefaultAccessMode),
	}
	if dto.ScopeConfig == nil {
		return sc
	}
	sc.Allow = sanitizeStringList(dto.ScopeConfig["tables_allow"])
	sc.Deny = sanitizeStringList(dto.ScopeConfig["tables_deny"])
	sc.Databases = sanitizeStringList(dto.ScopeConfig["databases"])
	sc.Methods = sanitizeStringList(dto.ScopeConfig["methods"])
	return sc
}

// dataSourceLimits renders the kind-specific constraints.
func dataSourceLimits(dto *DataSourceDTO) []string {
	kind := strings.ToLower(strings.TrimSpace(dto.Kind))
	limits := []string{"一次调用只跑一个 statement（SQL/Trino）或一个 operation（redis/mongo/elasticsearch/http）。"}
	switch kind {
	case "http":
		limits = append(limits,
			"http 源：请求路径必须落在 scope 的路径前缀内；GET/HEAD/POST 视为读，PUT/PATCH/DELETE 视为写。",
			"响应是 JSON 数组则展开为多行，对象则视为单行；嵌套列表用 http_list_path 指定（如 \"data.list\"）。")
	case "redis":
		limits = append(limits, "redis 源：每次只发一条命令，args 全部按字符串传。")
	case "mongo":
		limits = append(limits, "mongo 源：管理/DDL 操作（drop/createIndex/runCommand 等）被拒绝。")
	case "elasticsearch":
		limits = append(limits, "elasticsearch 源：index 必须是一个具体索引（不支持通配符/逗号）。")
	case "trino":
		limits = append(limits, "trino 源：表名需写成 catalog.schema.table。")
	default:
		limits = append(limits, "SQL 源：语句须为单条 SELECT/SHOW/EXPLAIN/WITH…SELECT；写语句需要确认。")
	}
	if normalizeAccessMode(dto.DefaultAccessMode) == "read" {
		limits = append(limits, "访问模式为 read：所有写操作被拒绝。")
	} else {
		limits = append(limits, "访问模式为 readwrite：写操作仍需用户在确认卡批准。")
	}
	if dto.RequireConfirm == "always" {
		limits = append(limits, "该源 require_confirm=always：读操作也需要确认。")
	}
	limits = append(limits, "返回行数默认上限 1000（可用 row_limit 调低）。")
	return limits
}

// dataSourceUsage renders the natural-language "how to use" paragraph.
func dataSourceUsage(doc *CapabilityDoc, dto *DataSourceDTO) string {
	var b strings.Builder
	fmt.Fprintf(&b, "这是一个数据源（数据型，kind=%s）。用 %s(source=%q, %s) 查询；",
		doc.Scope.Kind, doc.Tool, doc.Name, dataSourceArgHint(dto.Kind))
	b.WriteString("先看 list_data_sources 确认可见源。")
	if len(doc.Scope.Allow) > 0 {
		fmt.Fprintf(&b, "可用范围限于：%s。", strings.Join(doc.Scope.Allow, ", "))
	} else if len(doc.Scope.Databases) > 0 {
		fmt.Fprintf(&b, "可用范围限于数据库：%s。", strings.Join(doc.Scope.Databases, ", "))
	} else {
		b.WriteString("当前 scope 未放行任何对象。")
	}
	b.WriteString("连接凭证由 niuniu 在出口注入并在静态加密存储——不要向用户索要密码。")
	return b.String()
}

// dataSourceArgHint names the exact argument run_data_query expects for a kind.
func dataSourceArgHint(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "redis", "mongo", "elasticsearch", "http":
		return "operation={...}"
	default:
		return "statement=\"...\""
	}
}

// dataSourceExamples renders one runnable call shape per kind.
func dataSourceExamples(doc *CapabilityDoc, dto *DataSourceDTO) []CapabilityExample {
	name := doc.Name
	switch strings.ToLower(strings.TrimSpace(dto.Kind)) {
	case "redis":
		return []CapabilityExample{{
			Task: "读取一个键",
			Call: fmt.Sprintf("%s(source=%q, operation={\"command\": \"GET\", \"args\": [\"user:42\"]})", capabilityToolDataSource, name),
		}}
	case "mongo":
		return []CapabilityExample{{
			Task: "按条件查文档",
			Call: fmt.Sprintf("%s(source=%q, operation={\"collection\": \"users\", \"mongo_op\": \"find\", \"filter\": {}})", capabilityToolDataSource, name),
		}}
	case "elasticsearch":
		return []CapabilityExample{{
			Task: "检索索引",
			Call: fmt.Sprintf("%s(source=%q, operation={\"index\": \"logs-2026.10.11\", \"es_method\": \"GET\", \"es_path\": \"_search\", \"query\": {\"size\": 10}})", capabilityToolDataSource, name),
		}}
	case "http":
		example := firstOr(doc.Scope.Allow, "/v1/pets")
		method := firstOr(doc.Scope.Methods, "GET")
		return []CapabilityExample{{
			Task: "调用一个只读接口",
			Call: fmt.Sprintf("%s(source=%q, operation={\"http_method\": %q, \"http_path\": %q})", capabilityToolDataSource, name, method, example),
		}}
	default:
		table := firstOr(doc.Scope.Allow, firstOr(doc.Scope.Databases, "your_table"))
		return []CapabilityExample{{
			Task: "查一批数据",
			Call: fmt.Sprintf("%s(source=%q, statement=\"SELECT * FROM %s LIMIT 10\")", capabilityToolDataSource, name, table),
		}}
	}
}

// --- digest + staleness -----------------------------------------------------

// DataSourceCapabilityDigest is the digest of a data source's capability-
// relevant, non-secret metadata. It changes iff the kind, name, scope, or
// non-secret connection facts change — so a scope widening invalidates the
// stored description.
func DataSourceCapabilityDigest(dto *DataSourceDTO) string {
	if dto == nil {
		return ""
	}
	payload := strings.Join([]string{
		CapabilitySchemaVersion,
		strings.TrimSpace(dto.Kind),
		strings.TrimSpace(dto.Name),
		canonicalJSON(dto.ScopeConfig),
		canonicalJSON(nonSecretConfig(dto.Config)),
		normalizeAccessMode(dto.DefaultAccessMode),
		normalizeRequireConfirm(dto.RequireConfirm),
	}, "|")
	sum := sha256.Sum256([]byte(payload))
	return digestPrefix + hex.EncodeToString(sum[:])
}

// nonSecretConfig keeps only the non-sensitive connection facts a capability
// description may mention. The stored config is already redacted on the DTO,
// but this keeps the digest independent of any secret placeholder.
func nonSecretConfig(cfg map[string]any) map[string]any {
	if cfg == nil {
		return nil
	}
	keep := map[string]bool{
		"host": true, "port": true, "database": true, "scheme": true,
		"base_path": true, "list_path": true, "user": true,
	}
	out := map[string]any{}
	for k, v := range cfg {
		if keep[k] {
			out[k] = v
		}
	}
	return out
}

// IsStale reports whether this doc no longer reflects the current source
// digest (or was never generated).
func (d *CapabilityDoc) IsStale(currentDigest string) bool {
	return d == nil || d.SourceDigest == "" || d.SourceDigest != currentDigest
}

// --- rendering: the agent-facing data block ---------------------------------

// RenderCapabilityAgentBlock renders the ordered docs into the managed,
// DATA-fenced block injected into the agent's instruction file. It returns ""
// for an empty set (the caller then removes the section). Deterministic: docs
// are compacted into a stable order and no timestamps are emitted, so repeated
// renders of an unchanged contract produce identical bytes and do not perturb
// the prompt cache.
//
// The wrapper (markers + DATA fence) is owned by agentproxy so the block an API
// caller is shown is byte-identical to what lands on disk. Security framing: the
// block is presented as DATA, and every contract-derived free-text field was
// sanitized at build time, so a poisoned summary cannot break out of the
// partition or read as a directive.
func RenderCapabilityAgentBlock(docs []*CapabilityDoc) string {
	compacted := compactCapabilityDocs(docs)
	caps := make([]agentproxy.AgentCapability, 0, len(compacted))
	for _, d := range compacted {
		caps = append(caps, agentproxy.AgentCapability{
			TargetKind:  d.TargetKind,
			Name:        d.Name,
			DisplayName: d.DisplayName,
			Section:     RenderCapabilitySection(d),
		})
	}
	return agentproxy.RenderCapabilityBlock(caps)
}

// compactCapabilityDocs drops nil docs and returns a stable, sorted copy.
func compactCapabilityDocs(docs []*CapabilityDoc) []*CapabilityDoc {
	out := make([]*CapabilityDoc, 0, len(docs))
	for _, d := range docs {
		if d != nil {
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TargetKind != out[j].TargetKind {
			return out[i].TargetKind < out[j].TargetKind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// RenderCapabilitySection renders one doc as a markdown section inside the data
// block.
func RenderCapabilitySection(d *CapabilityDoc) string {
	var b strings.Builder
	switch d.TargetKind {
	case CapabilityTargetDataSource:
		kind := ""
		if d.Scope != nil {
			kind = d.Scope.Kind
		}
		fmt.Fprintf(&b, "### 数据源 · %s（%s）\n", d.DisplayName, kind)
	default:
		fmt.Fprintf(&b, "### 外部 API · %s\n", d.DisplayName)
	}
	if d.Usage != "" {
		b.WriteString("- 怎么用：" + d.Usage + "\n")
	}
	if d.TargetKind == CapabilityTargetProvider {
		b.WriteString("- 可用操作：\n")
		if len(d.Operations) == 0 {
			b.WriteString("  - （无结构化操作；见 legacy profile）\n")
		}
		for _, op := range d.Operations {
			kind := "写/敏感"
			if op.ReadOnly {
				kind = "只读"
			}
			line := fmt.Sprintf("  - `%s` (%s %s) — %s", op.Name, op.Method, op.Path, kind)
			if op.Summary != "" {
				line += " — " + op.Summary
			}
			if params := renderParamsInline(op.Params); params != "" {
				line += " — params: " + params
			}
			b.WriteString(line + "\n")
			if op.Notes != "" {
				b.WriteString("    - 备注：" + op.Notes + "\n")
			}
		}
	} else if d.Scope != nil {
		if len(d.Scope.Databases) > 0 {
			b.WriteString("  - 可用数据库：" + strings.Join(d.Scope.Databases, ", ") + "\n")
		}
		if len(d.Scope.Allow) > 0 {
			b.WriteString("  - 可用范围（" + scopeSubjectLabel(d.Scope.Kind) + "）：" + strings.Join(d.Scope.Allow, ", ") + "\n")
		}
		if len(d.Scope.Deny) > 0 {
			b.WriteString("  - 明确排除：" + strings.Join(d.Scope.Deny, ", ") + "\n")
		}
		if len(d.Scope.Methods) > 0 {
			b.WriteString("  - 允许方法：" + strings.Join(d.Scope.Methods, ", ") + "\n")
		}
		b.WriteString("  - 访问模式：" + d.Scope.AccessMode + "\n")
	}
	for _, ex := range d.Examples {
		fmt.Fprintf(&b, "- 示例（%s）：`%s`\n", ex.Task, ex.Call)
	}
	if len(d.Limits) > 0 {
		b.WriteString("- 限制：\n")
		for _, l := range d.Limits {
			b.WriteString("  - " + l + "\n")
		}
	}
	return b.String()
}

func renderParamsInline(ps []CapabilityParam) string {
	if len(ps) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		s := p.Name + "(" + p.In
		if p.Required {
			s += ", required"
		}
		s += ")"
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

func scopeSubjectLabel(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "redis":
		return "键前缀"
	case "mongo":
		return "collection"
	case "elasticsearch":
		return "index"
	case "http":
		return "路径前缀"
	default:
		return "表"
	}
}

// --- sanitization (injection defence) ---------------------------------------

// sanitizeCapabilityText neutralises an IDENTITY field (an operation name,
// path, method, parameter name, scope object) before it enters a capability doc.
// It strips marker / comment sequences and collapses whitespace, so the text can
// neither close the data partition nor forge a new block line. Identity values
// are NOT run through the instruction classifier: they must survive verbatim to
// stay usable in a worked example (`operation="<name>"`), and they are rendered
// as inline code inside a block explicitly framed as data.
func sanitizeCapabilityText(s string) string {
	if s == "" {
		return ""
	}
	// Strip anything that could terminate or forge the managed block.
	s = strings.ReplaceAll(s, agentproxy.CapabilityBlockBegin, "")
	s = strings.ReplaceAll(s, agentproxy.CapabilityBlockEnd, "")
	s = strings.ReplaceAll(s, "-->", "")
	s = strings.ReplaceAll(s, "<!--", "")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}

// sanitizeCapabilityProse neutralises a PROSE field (an operation summary, a
// parameter description, a display label). Beyond the identity treatment it
// applies a second, stronger defence: text that reads as an instruction (per the
// untrusted-input classifier) is replaced outright, so an instruction smuggled
// into a summary can never be rendered as a directive.
func sanitizeCapabilityProse(s string) string {
	s = sanitizeCapabilityText(s)
	if s == "" {
		return ""
	}
	if class, _ := ClassifyDocText(s); class == DocTextImperative {
		return "[已屏蔽：疑似指令性文本 / instruction-like text withheld]"
	}
	return s
}

// sanitizeStringList sanitizes each element of a JSON-derived string list.
func sanitizeStringList(v any) []string {
	raw := toStringList(v)
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		if t := sanitizeCapabilityText(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// toStringList coerces a JSON value ([]any of strings, or a single string) into
// a string slice.
func toStringList(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		if strings.TrimSpace(t) == "" {
			return nil
		}
		return []string{t}
	default:
		return nil
	}
}

// --- small helpers ----------------------------------------------------------

func pickNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func firstOr(list []string, fallback string) string {
	if len(list) > 0 && strings.TrimSpace(list[0]) != "" {
		return list[0]
	}
	return fallback
}

// canonicalJSON marshals a value to a stable JSON string (Go sorts map keys).
func canonicalJSON(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
