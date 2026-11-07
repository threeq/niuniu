package packs

// Conversational Pack generation (design §5.5).
//
// A new channel is turned into a Pack draft from whatever source is available:
// an OpenAPI/Swagger document, an existing MCP provider, or — when neither
// exists — a browser-recipe skeleton. Missing facts (domains, selectors, field
// mappings, sample pages) are NEVER guessed silently: every one becomes a
// Question on the capability matrix card, and Publish refuses until the draft
// has no open questions left and a sandbox test has passed.
//
// Security rules, mirroring the OpenAPI draft pipeline:
//   - An external document is DATA. Only structural fields (paths, methods,
//     parameters, response schema property names) influence the draft. Titles,
//     summaries and descriptions are never read, so prompt-injection prose has
//     no surface here.
//   - No permission is inherited: an OpenAPI draft only maps the operations the
//     document declares, and auth is a credential REQUIREMENT, never a value.
//   - A generated Pack is stored in the owner's directory and stays `draft`
//     until Test passes and Publish is called by an explicit user action.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Capability names shared with the builtin Packs.
const (
	CapPublish     = "publish"
	CapMetrics     = "metrics"
	CapEngagements = "engagements"
	CapReply       = "reply"
)

// Generation source kinds.
const (
	SourceOpenAPI = "openapi"
	SourceMCP     = "mcp"
	SourceBrowser = "browser"
)

const (
	generatorMetaFile = "generator.json"
	testReportFile    = "test_report.json"
	contractFile      = "provider_contract.json"

	generatedRecipeDir = "browser/recipes"
	generatedFixture   = "fixtures"

	maxDocumentBytes = 8 << 20
	maxDocPaths      = 2000
	maxDocOps        = 4000
)

var (
	ErrChannelRequired = errors.New("pack generator: channel is required")
	ErrNoRecipeTester  = errors.New("pack generator: no recipe sandbox tester configured")
	ErrNotTested       = errors.New("pack generator: pack has not passed the sandbox test")
	ErrMissingInfo     = errors.New("pack generator: draft still has unanswered questions")
	ErrNoCapabilities  = errors.New("pack generator: the source declares no usable capability")
	ErrInvalidDocument = errors.New("pack generator: document is unusable")

	errDocumentFetcherMissing = errors.New("pack generator: no document fetcher configured")
)

// Owner identifies whose pack directory a draft belongs to.
type Owner struct {
	Type string
	ID   int64
}

func (o Owner) valid() bool {
	return (o.Type == "user" || o.Type == "org") && o.ID > 0
}

// RecipeTester runs one generated recipe in the caller's sandbox. The Pack
// package cannot import internal/opsbot/browser (browser imports packs), so the
// server injects browser.Sandbox here.
type RecipeTester func(ctx context.Context, root string, recipe Recipe, values map[string]string) error

// ContractTester optionally exercises a generated OpenAPI contract against a
// mock server or a test account. A nil tester falls back to structural checks.
type ContractTester func(ctx context.Context, contract ProviderContractDraft) error

// ContractDrafter persists a generated provider contract through the
// integration draft pipeline (`draft_integration`). The server injects it;
// returning the created draft id lets the Pack card point at it.
type ContractDrafter func(ctx context.Context, owner Owner, contract ProviderContractDraft, sourceName, sourceURL string) (int64, error)

// Generator turns a conversation's known facts into Pack drafts and moves them
// through test/publish. Registry and Owner may be nil/zero for in-memory drafts
// used by planning flows; persistence and Test/Publish require both.
type Generator struct {
	Registry *Registry
	// Owner is the default owner used when GenerateInput names none.
	Owner Owner
	// Fetch retrieves an OpenAPI document the user pointed at by URL. The
	// server injects the SSRF-guarded fetcher; without it a URL is answered with
	// a question asking for the document file or pasted content.
	Fetch func(ctx context.Context, rawURL string) ([]byte, error)
	// RecipeTester runs browser recipes in the sandbox.
	RecipeTester RecipeTester
	// ContractTester optionally probes a generated provider contract.
	ContractTester ContractTester
	// DraftContract persists a generated provider contract through the
	// integration draft pipeline. Optional.
	DraftContract ContractDrafter
	// Now overrides the clock in tests.
	Now func() time.Time
}

func (g *Generator) now() time.Time {
	if g.Now != nil {
		return g.Now().UTC()
	}
	return time.Now().UTC()
}

// GenerateInput is one draft request. Answers carries facts the user supplied in
// conversation (a base URL, a selector, a metric field mapping, sample HTML).
type GenerateInput struct {
	Channel string
	// OpenAPIPath reads a local JSON document. OpenAPIBytes is inline content.
	// OpenAPIURL needs Generator.Fetch.
	OpenAPIPath  string
	OpenAPIBytes []byte
	OpenAPIURL   string
	// ExistingMCP names an already-connected provider whose capabilities cover
	// this channel.
	ExistingMCP string
	// Answers maps question keys to user answers. Keys are stable so a
	// conversation can ask, collect and re-submit them.
	Answers map[string]string
	// Version forces a specific version. Zero means "next free version".
	Version int
	// SkipIncomplete leaves a draft with unanswered questions in memory only,
	// so a conversation can collect answers without writing one version per
	// answer. Once complete, Draft persists normally.
	SkipIncomplete bool
	OwnerType      string
	OwnerID        int64
}

func (in GenerateInput) owner(fallback Owner) Owner {
	owner := Owner{Type: in.OwnerType, ID: in.OwnerID}
	if owner.valid() {
		return owner
	}
	return fallback
}

// Question is one missing fact. Kind is a hint for the UI (url/selector/html/
// mapping/text); Key is what the answer must be stored under.
type Question struct {
	Key        string `json:"key"`
	Capability string `json:"capability,omitempty"`
	Prompt     string `json:"prompt"`
	Kind       string `json:"kind,omitempty"`
}

// CapabilityEntry is one row of the capability matrix card: where the
// capability will go, whether the draft has everything it needs, and the risks
// a user should weigh before publishing.
type CapabilityEntry struct {
	Capability string   `json:"capability"`
	Transport  string   `json:"transport,omitempty"`
	Provider   string   `json:"provider,omitempty"`
	Recipe     string   `json:"recipe,omitempty"`
	Bound      bool     `json:"bound"`
	Ready      bool     `json:"ready"`
	Missing    []string `json:"missing,omitempty"`
	Risks      []string `json:"risks,omitempty"`
}

// CapabilityMatrix is the user-facing summary of a draft.
type CapabilityMatrix struct {
	Channel      string            `json:"channel"`
	Source       string            `json:"source"`
	Capabilities []CapabilityEntry `json:"capabilities"`
	Questions    []Question        `json:"questions,omitempty"`
	Notes        []string          `json:"notes,omitempty"`
}

// Draft is the in-memory result of Generate. Files are pack-relative payload
// files (the manifest is written separately by Registry.Save).
type Draft struct {
	Manifest  Manifest
	Files     map[string][]byte
	Matrix    CapabilityMatrix
	Contract  *ProviderContractDraft
	Questions []Question
	// ContractDraftID is the integration draft created for Contract, when a
	// drafter was injected.
	ContractDraftID int64
	// Root is the persisted version directory, when the draft was saved.
	Root string
}

// Detection reports what the generator found for a channel before drafting.
type Detection struct {
	Channel      string
	Source       string
	Document     []byte
	DocumentName string
	ExistingMCP  string
	Capabilities []string
	Notes        []string
	Questions    []Question
}

// --- contract draft wire types ---------------------------------------------
//
// These mirror internal/service's ProviderContract JSON shape so a generated
// draft can be handed to the draft_integration pipeline unchanged. They are a
// separate declaration because service imports opsbot, so packs cannot import
// service.

type ProviderContractDraft struct {
	SchemaVersion   string                   `json:"schema_version"`
	Provider        string                   `json:"provider,omitempty"`
	BaseURL         string                   `json:"base_url,omitempty"`
	Auth            *ContractAuthDraft       `json:"auth,omitempty"`
	Capabilities    []string                 `json:"capabilities,omitempty"`
	VerifyOperation string                   `json:"verify_operation,omitempty"`
	Operations      []ContractOperationDraft `json:"operations"`
}

type ContractAuthDraft struct {
	Strategy string `json:"strategy,omitempty"`
	Header   string `json:"header,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
	Param    string `json:"param,omitempty"`
	Note     string `json:"note,omitempty"`
}

type ContractOperationDraft struct {
	Name          string               `json:"name"`
	Method        string               `json:"method"`
	Path          string               `json:"path"`
	ReadOnly      bool                 `json:"read_only"`
	Parameters    []ContractParamDraft `json:"parameters,omitempty"`
	ResponseShape string               `json:"response_shape,omitempty"`
}

type ContractParamDraft struct {
	Name     string `json:"name"`
	In       string `json:"in"`
	Type     string `json:"type,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// generatorMeta is persisted next to a draft so a later conversation turn (or a
// different process) can see what is still missing and what the draft was
// derived from.
type generatorMeta struct {
	Channel      string            `json:"channel"`
	Source       string            `json:"source"`
	SourceDigest string            `json:"source_digest"`
	GeneratedAt  string            `json:"generated_at"`
	Answers      map[string]string `json:"answers,omitempty"`
	Missing      []string          `json:"missing,omitempty"`
	Questions    []Question        `json:"questions,omitempty"`
	Notes        []string          `json:"notes,omitempty"`
	// ContractDraftID points at an integration draft created from the same
	// OpenAPI document, so a reviewer can confirm the provider there.
	ContractDraftID int64 `json:"contract_draft_id,omitempty"`
}

type testReport struct {
	Status   string   `json:"status"` // passed | failed
	TestedAt string   `json:"tested_at"`
	Checks   []string `json:"checks"`
	Error    string   `json:"error,omitempty"`
}

// --- Detect ----------------------------------------------------------------

// Detect establishes where a channel's integration can come from. A URL that
// needs a fetcher the caller did not inject is not an error: the response asks
// the user for the document instead.
func (g *Generator) Detect(ctx context.Context, in GenerateInput) (Detection, error) {
	channel := strings.TrimSpace(in.Channel)
	if channel == "" {
		return Detection{}, ErrChannelRequired
	}
	det := Detection{Channel: channel}

	switch {
	case len(bytes.TrimSpace(in.OpenAPIBytes)) > 0 || strings.TrimSpace(in.OpenAPIPath) != "" || strings.TrimSpace(in.OpenAPIURL) != "":
		det.Source = SourceOpenAPI
		body, name, err := g.loadDocument(ctx, in)
		if errors.Is(err, errDocumentFetcherMissing) {
			det.Questions = append(det.Questions, Question{
				Key: "openapi_document", Kind: "document",
				Prompt: "已记录 OpenAPI 文档链接，但当前无法自动抓取。请提供文档文件，或把 JSON 内容粘贴到对话里。",
			})
			return det, nil
		}
		if err != nil {
			return Detection{}, err
		}
		det.Document = body
		det.DocumentName = safeDocumentName(name)
		doc, err := parseOpenAPIDocument(body)
		if err != nil {
			return Detection{}, err
		}
		det.Capabilities = doc.capabilities()
		det.Notes = append(det.Notes,
			"OpenAPI 文档按结构解析（路径/方法/参数/响应字段），文档中的说明文字不会被当作指令。")
	case strings.TrimSpace(in.ExistingMCP) != "":
		det.Source = SourceMCP
		det.ExistingMCP = strings.TrimSpace(in.ExistingMCP)
		det.Capabilities = []string{CapPublish, CapMetrics, CapEngagements, CapReply}
		det.Notes = append(det.Notes, "复用现有 MCP provider；不会新建外部连接。")
	default:
		det.Source = SourceBrowser
		det.Capabilities = []string{CapPublish, CapEngagements, CapReply}
		det.Notes = append(det.Notes,
			"未检测到 OpenAPI 或现成 MCP：将生成 browser recipe 草稿。域名、选择器、字段映射和沙箱样例页会在能力矩阵卡上逐项确认。")
	}
	return det, nil
}

func (g *Generator) loadDocument(ctx context.Context, in GenerateInput) ([]byte, string, error) {
	if body := bytes.TrimSpace(in.OpenAPIBytes); len(body) > 0 {
		if len(body) > maxDocumentBytes {
			return nil, "", fmt.Errorf("%w: document exceeds %d bytes", ErrInvalidDocument, maxDocumentBytes)
		}
		return body, "inline", nil
	}
	if name := strings.TrimSpace(in.OpenAPIPath); name != "" {
		f, err := os.Open(name)
		if err != nil {
			return nil, "", fmt.Errorf("pack generator: read openapi document: %w", err)
		}
		defer f.Close()
		body, err := io.ReadAll(io.LimitReader(f, maxDocumentBytes+1))
		if err != nil {
			return nil, "", fmt.Errorf("pack generator: read openapi document: %w", err)
		}
		if len(body) > maxDocumentBytes {
			return nil, "", fmt.Errorf("%w: document exceeds %d bytes", ErrInvalidDocument, maxDocumentBytes)
		}
		return body, filepath.Base(name), nil
	}

	rawURL := strings.TrimSpace(in.OpenAPIURL)
	if rawURL == "" {
		return nil, "", fmt.Errorf("%w: no document supplied", ErrInvalidDocument)
	}
	if g.Fetch == nil {
		return nil, "", errDocumentFetcherMissing
	}
	body, err := g.Fetch(ctx, rawURL)
	if err != nil {
		return nil, "", fmt.Errorf("pack generator: fetch openapi document: %w", err)
	}
	if len(body) > maxDocumentBytes {
		return nil, "", fmt.Errorf("%w: document exceeds %d bytes", ErrInvalidDocument, maxDocumentBytes)
	}
	return body, rawURL, nil
}

// safeDocumentName keeps a document reference safe to echo: no userinfo, no
// query (often a signed token), no fragment.
func safeDocumentName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	u, err := url.Parse(name)
	if err != nil || u.Host == "" {
		// A local file name or "inline": keep only the base name.
		if base := filepath.Base(name); base != "." && base != string(filepath.Separator) {
			return base
		}
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// --- Draft -----------------------------------------------------------------

// Draft generates a Pack draft and, when an owner is known, persists it as a
// new immutable version. Re-running with identical inputs returns the existing
// draft; changed inputs create the next version (so page redesigns upgrade
// rather than mutate history).
func (g *Generator) Draft(ctx context.Context, in GenerateInput) (*Draft, error) {
	det, err := g.Detect(ctx, in)
	if err != nil {
		return nil, err
	}

	var d *Draft
	switch det.Source {
	case SourceOpenAPI:
		d, err = g.draftFromOpenAPI(det, in)
	case SourceMCP:
		d, err = g.draftFromMCP(det, in)
	default:
		d, err = g.draftFromBrowser(det, in)
	}
	if err != nil {
		return nil, err
	}
	if len(d.Manifest.Capabilities) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoCapabilities, d.Manifest.ID)
	}

	digest := draftSourceDigest(det.Source, det.Document, in.Answers)
	owner := in.owner(g.Owner)
	meta := generatorMeta{
		Channel:      d.Manifest.ID,
		Source:       det.Source,
		SourceDigest: digest,
		GeneratedAt:  g.now().Format(time.RFC3339),
		Answers:      copyAnswers(in.Answers),
		Missing:      matrixMissing(d.Matrix),
		Questions:    d.Questions,
		Notes:        d.Matrix.Notes,
	}

	// A conversation collects missing facts over several turns; writing a new
	// version per answer would pollute the history. Incomplete drafts stay in
	// memory until every question is answered.
	if in.SkipIncomplete && len(meta.Missing) > 0 {
		d.Files[generatorMetaFile] = mustJSON(meta)
		d.Manifest.Version = in.Version
		return d, nil
	}

	if !owner.valid() || g.Registry == nil {
		d.Files[generatorMetaFile] = mustJSON(meta)
		if d.Manifest.Version <= 0 {
			d.Manifest.Version = 1
		}
		return d, nil
	}

	version := in.Version
	if version <= 0 {
		latest, err := g.latestVersion(owner, d.Manifest.ID)
		if err != nil {
			return nil, err
		}
		if latest > 0 && g.sameSource(owner, d.Manifest.ID, latest, digest) {
			m, err := g.Registry.Load(owner.Type, owner.ID, d.Manifest.ID, latest)
			if err != nil {
				return nil, err
			}
			d.Manifest = m
			d.Root, _ = g.Registry.Root(owner.Type, owner.ID, d.Manifest.ID, latest)
			d.ContractDraftID = g.readMeta(d.Root).ContractDraftID
			if d.ContractDraftID > 0 {
				d.Matrix.Notes = append(d.Matrix.Notes,
					fmt.Sprintf("已创建 provider 集成草稿 #%d；需在集成设置中确认后，Pack 的 API 绑定才可用。", d.ContractDraftID))
			}
			return d, nil
		}
		version = latest + 1
	}
	d.Manifest.Version = version

	// A new OpenAPI version also creates a provider-integration draft, so the
	// Pack's provider exists in the integration pipeline once the user confirms
	// it there. Drafting happens only for a version that is actually written,
	// never for an idempotent re-draft.
	if g.DraftContract != nil && d.Contract != nil && len(meta.Missing) == 0 {
		id, err := g.DraftContract(ctx, owner, *d.Contract, det.DocumentName, strings.TrimSpace(in.OpenAPIURL))
		if err != nil {
			return nil, fmt.Errorf("pack generator: draft provider contract: %w", err)
		}
		if id > 0 {
			d.ContractDraftID = id
			meta.ContractDraftID = id
			d.Matrix.Notes = append(d.Matrix.Notes,
				fmt.Sprintf("已创建 provider 集成草稿 #%d；需在集成设置中确认后，Pack 的 API 绑定才可用。", id))
		}
	}
	d.Files[generatorMetaFile] = mustJSON(meta)

	root, err := g.Registry.Save(owner.Type, owner.ID, d.Manifest, d.Files)
	if err != nil {
		return nil, err
	}
	d.Root = root
	return d, nil
}

func (g *Generator) latestVersion(owner Owner, packID string) (int, error) {
	if !idPattern.MatchString(packID) {
		return 0, fmt.Errorf("%w: %q", ErrInvalidPackID, packID)
	}
	ownerRoot, err := g.Registry.ownerRoot(owner.Type, owner.ID)
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(filepath.Join(ownerRoot, "ops", "packs", packID))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	latest := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		n, err := strconv.Atoi(entry.Name())
		if err != nil || n <= 0 {
			continue
		}
		if n > latest {
			latest = n
		}
	}
	return latest, nil
}

func (g *Generator) sameSource(owner Owner, packID string, version int, digest string) bool {
	root, err := g.Registry.Root(owner.Type, owner.ID, packID, version)
	if err != nil {
		return false
	}
	data, err := os.ReadFile(filepath.Join(root, generatorMetaFile))
	if err != nil {
		return false
	}
	var meta generatorMeta
	if json.Unmarshal(data, &meta) != nil {
		return false
	}
	return meta.SourceDigest == digest
}

// matrixMissing flattens the unanswered keys across capabilities.
func matrixMissing(m CapabilityMatrix) []string {
	seen := map[string]bool{}
	var out []string
	for _, cap := range m.Capabilities {
		for _, key := range cap.Missing {
			if !seen[key] {
				seen[key] = true
				out = append(out, key)
			}
		}
	}
	sort.Strings(out)
	return out
}

// --- OpenAPI drafting ------------------------------------------------------

func (g *Generator) draftFromOpenAPI(det Detection, in GenerateInput) (*Draft, error) {
	doc, err := parseOpenAPIDocument(det.Document)
	if err != nil {
		return nil, err
	}
	answers := copyAnswers(in.Answers)
	id := channelID(det.Channel)
	display := displayName(det.Channel)

	byCap := map[string][]openAPIOp{}
	for _, op := range doc.ops {
		if c := classifyOperation(op.Method, op.Path); c != "" {
			byCap[c] = append(byCap[c], op)
		}
	}

	// A metrics map is derived from response schema property names only; when
	// that yields nothing the user must supply the mapping explicitly, or
	// explicitly decline metrics with "skip".
	metricsMap := map[string]string{}
	for _, op := range byCap[CapMetrics] {
		for platform, unified := range deriveMetricsMap(op.ResponseProperties) {
			metricsMap[platform] = unified
		}
	}
	metricsDeclined := isDeclined(answers[qMetricsMap])
	if len(metricsMap) == 0 && !metricsDeclined {
		if provided := parseFieldMap(answers["metrics_field_map"]); len(provided) > 0 {
			metricsMap = provided
		}
	}

	contract := doc.contract(id, answers["base_url"])
	draft := &Draft{
		Manifest: Manifest{
			ID:          id,
			Status:      "draft",
			DisplayName: display,
			Bindings:    map[string]Binding{},
			CredentialSlots: map[string]CredentialSlot{
				"account": {Required: true, Scope: "account"},
			},
			ContentSchema: ContentSchema{Formats: map[string]ContentFormat{
				"post": openAPIPublishFormat(byCap[CapPublish]),
			}},
			Compliance: defaultCompliance(),
			MetricsMap: MetricsMap{Fields: metricsMap},
		},
		Contract: contract,
	}
	draft.Files = map[string][]byte{contractFile: mustJSON(contract)}

	matrix := CapabilityMatrix{Channel: display, Source: SourceOpenAPI}
	for _, capability := range []string{CapPublish, CapMetrics, CapEngagements, CapReply} {
		entry := CapabilityEntry{Capability: capability}
		entry.Risks = openAPIRisks()
		switch capability {
		case CapPublish:
			if len(byCap[CapPublish]) > 0 {
				entry.Transport = "openapi"
				entry.Provider = id
				entry.Bound = true
			}
		case CapMetrics:
			if len(byCap[CapMetrics]) > 0 {
				entry.Transport = "openapi"
				entry.Provider = id
				if len(metricsMap) > 0 {
					entry.Bound = true
				}
			}
		default:
			if len(byCap[capability]) > 0 {
				entry.Transport = "openapi"
				entry.Provider = id
				entry.Bound = true
			}
		}
		matrix.Capabilities = append(matrix.Capabilities, entry)
	}

	var questions []Question
	if strings.TrimSpace(contract.BaseURL) == "" {
		questions = append(questions, Question{
			Key: "base_url", Kind: "url",
			Prompt: "文档没有给出可用的服务地址。这个渠道 API 的 Base URL 是什么？（例如 https://api.example.com/v1）",
		})
	}
	if len(byCap[CapMetrics]) > 0 && len(metricsMap) == 0 && !metricsDeclined {
		questions = append(questions, Question{
			Key: "metrics_field_map", Capability: CapMetrics, Kind: "mapping",
			Prompt: "文档的响应字段无法对应到统一指标。请给出映射，例如 view_count=views, like_count=likes；不需要指标能力请回复 skip。",
		})
	}
	matrix.Questions = questions
	matrix.Notes = append(matrix.Notes, det.Notes...)

	for i := range matrix.Capabilities {
		capability := matrix.Capabilities[i].Capability
		if metricsDeclined && capability == CapMetrics {
			matrix.Capabilities[i].Risks = append(matrix.Capabilities[i].Risks, "用户选择不启用指标能力")
		}
		if !matrix.Capabilities[i].Bound {
			// Unsupported is not the same as missing information: the matrix
			// shows the capability is not offered, and publishing the other
			// capabilities is not blocked by it.
			matrix.Capabilities[i].Risks = append(matrix.Capabilities[i].Risks,
				"来源未声明该能力，将不会启用")
		}
	}
	if strings.TrimSpace(contract.BaseURL) == "" {
		for i := range matrix.Capabilities {
			if matrix.Capabilities[i].Bound {
				matrix.Capabilities[i].Missing = appendUnique(matrix.Capabilities[i].Missing, "base_url")
			}
		}
	}
	if len(byCap[CapMetrics]) > 0 && len(metricsMap) == 0 && !metricsDeclined {
		setMissing(&matrix, CapMetrics, "metrics_field_map")
	}
	markReady(&matrix)

	draft.Matrix = matrix
	draft.Questions = questions

	for _, capability := range []string{CapPublish, CapMetrics, CapEngagements, CapReply} {
		entry := matrix.Capabilities[indexOf(matrix.Capabilities, capability)]
		if !entry.Bound {
			continue
		}
		draft.Manifest.Capabilities = append(draft.Manifest.Capabilities, capability)
		draft.Manifest.Bindings[capability] = Binding{Transport: "openapi", Provider: id}
	}
	if !contains(draft.Manifest.Capabilities, CapMetrics) {
		draft.Manifest.MetricsMap = MetricsMap{}
	}
	return draft, nil
}

// contract renders the provider contract draft. Text from the document never
// reaches it: provider is the channel id (user supplied) and operations come
// from structure only.
func (d openAPIDoc) contract(channelID, baseURLOverride string) *ProviderContractDraft {
	baseURL := strings.TrimSpace(baseURLOverride)
	if baseURL == "" {
		baseURL = d.baseURL
	}
	c := &ProviderContractDraft{
		SchemaVersion: "1",
		Provider:      channelID,
		BaseURL:       baseURL,
		Auth:          d.auth,
		Operations:    make([]ContractOperationDraft, 0, len(d.ops)),
	}
	seenCaps := map[string]bool{}
	for _, op := range d.ops {
		c.Operations = append(c.Operations, ContractOperationDraft{
			Name:          op.Name,
			Method:        op.Method,
			Path:          op.Path,
			ReadOnly:      op.ReadOnly,
			Parameters:    op.Parameters,
			ResponseShape: op.ResponseShape,
		})
		if capability := classifyOperation(op.Method, op.Path); capability != "" && !seenCaps[capability] {
			seenCaps[capability] = true
			c.Capabilities = append(c.Capabilities, capability)
		}
	}
	sort.Strings(c.Capabilities)
	for _, op := range d.ops {
		if op.ReadOnly && !hasPathParameter(op.Parameters) {
			c.VerifyOperation = op.Name
			break
		}
	}
	return c
}

// openAPIPublishFormat uses the publish operations' request-body property names
// as the content fields; with no body schema it falls back to the generic shape
// and the matrix calls the limits out for confirmation.
func openAPIPublishFormat(ops []openAPIOp) ContentFormat {
	fields := map[string]bool{}
	for _, op := range ops {
		for _, name := range op.BodyFields {
			fields[name] = true
		}
	}
	out := make([]string, 0, len(fields))
	for name := range fields {
		out = append(out, name)
	}
	sort.Strings(out)
	if len(out) == 0 {
		out = []string{"title", "body", "media"}
	}
	return ContentFormat{Fields: out}
}

func openAPIRisks() []string {
	return []string{
		"需要平台 App / 权限审核，未获授权时操作会失败",
		"存在调用配额与 ToS 限制，需确认商用许可",
	}
}

// --- MCP drafting ----------------------------------------------------------

func (g *Generator) draftFromMCP(det Detection, in GenerateInput) (*Draft, error) {
	id := channelID(det.Channel)
	metricsMap := parseFieldMap(in.Answers["metrics_field_map"])
	draft := &Draft{
		Manifest: Manifest{
			ID:          id,
			Status:      "draft",
			DisplayName: displayName(det.Channel),
			Bindings:    map[string]Binding{},
			CredentialSlots: map[string]CredentialSlot{
				"account": {Required: true, Scope: "account"},
			},
			ContentSchema: ContentSchema{Formats: map[string]ContentFormat{
				"post": {Fields: []string{"title", "body", "media"}},
			}},
			Compliance: defaultCompliance(),
			MetricsMap: MetricsMap{Fields: metricsMap},
		},
		Files: map[string][]byte{},
	}

	matrix := CapabilityMatrix{Channel: det.Channel, Source: SourceMCP, Notes: det.Notes}
	metricsDeclined := isDeclined(in.Answers[qMetricsMap])
	var questions []Question
	for _, capability := range []string{CapPublish, CapMetrics, CapEngagements, CapReply} {
		entry := CapabilityEntry{
			Capability: capability,
			Transport:  "mcp",
			Provider:   det.ExistingMCP,
			Bound:      true,
			Risks:      []string{"依赖现有 MCP 服务的可用性与配额"},
		}
		if capability == CapMetrics {
			switch {
			case len(metricsMap) > 0:
			case metricsDeclined:
				entry.Bound = false
				entry.Risks = append(entry.Risks, "用户选择不启用指标能力")
			default:
				// Without a field mapping the pack cannot declare metrics (the
				// manifest validation requires a non-empty map), so the
				// capability stays unbound and the mapping is asked for.
				entry.Bound = false
				entry.Missing = append(entry.Missing, qMetricsMap)
				questions = append(questions, Question{
					Key: qMetricsMap, Capability: CapMetrics, Kind: "mapping",
					Prompt: "该 MCP 的指标字段如何对应到统一指标？例如 view_count=views, like_count=likes；不需要指标能力请回复 skip。",
				})
			}
		}
		matrix.Capabilities = append(matrix.Capabilities, entry)
	}
	matrix.Questions = questions
	markReady(&matrix)
	draft.Matrix = matrix
	draft.Questions = questions

	for _, capability := range []string{CapPublish, CapMetrics, CapEngagements, CapReply} {
		entry := matrix.Capabilities[indexOf(matrix.Capabilities, capability)]
		if !entry.Bound {
			continue
		}
		draft.Manifest.Capabilities = append(draft.Manifest.Capabilities, capability)
		draft.Manifest.Bindings[capability] = Binding{Transport: "mcp", Provider: det.ExistingMCP}
	}
	if !contains(draft.Manifest.Capabilities, CapMetrics) {
		draft.Manifest.MetricsMap = MetricsMap{}
	}
	return draft, nil
}

// --- Browser recipe drafting ----------------------------------------------

type recipeSpec struct {
	capability string
	file       string
	build      func(v map[string]string) Recipe
	questions  []Question
}

const (
	qLoginURL      = "login_url"
	qLoggedIn      = "logged_in_selector"
	qPublishURL    = "publish_url"
	qUpload        = "upload_selector"
	qTitle         = "title_selector"
	qBody          = "body_selector"
	qSubmit        = "submit_selector"
	qResult        = "result_selector"
	qMetricsURL    = "metrics_url"
	qMetricsSel    = "metrics_selector"
	qMetricsMap    = "metrics_field_map"
	qEngageURL     = "engagements_url"
	qEngageList    = "engagement_list_selector"
	qReplyURL      = "reply_url"
	qReplyInput    = "reply_input_selector"
	qReplySend     = "reply_send_selector"
	qFixturePrefix = "fixture."
)

func recipeSpecs() []recipeSpec {
	return []recipeSpec{
		{
			capability: CapPublish,
			file:       path.Join(generatedRecipeDir, "publish.json"),
			build: func(v map[string]string) Recipe {
				return Recipe{
					Name: "publish",
					Steps: []RecipeStep{
						{Action: "goto", Value: valueOr(v, qPublishURL)},
						{Action: "upload", Selector: valueOr(v, qUpload), Value: "{{media_path}}"},
						{Action: "fill", Selector: valueOr(v, qTitle), Value: "{{title}}"},
						{Action: "fill", Selector: valueOr(v, qBody), Value: "{{body}}"},
						{Action: "click", Selector: valueOr(v, qSubmit)},
						{Action: "extract", Selector: valueOr(v, qResult), Value: "published_url"},
					},
				}
			},
			questions: []Question{
				{Key: qPublishURL, Capability: CapPublish, Kind: "url", Prompt: "发布页的完整 URL 是什么？"},
				{Key: qUpload, Capability: CapPublish, Kind: "selector", Prompt: "发布页上传媒体（图片/视频）的控件选择器是什么？"},
				{Key: qTitle, Capability: CapPublish, Kind: "selector", Prompt: "标题输入框的选择器是什么？"},
				{Key: qBody, Capability: CapPublish, Kind: "selector", Prompt: "正文输入框的选择器是什么？"},
				{Key: qSubmit, Capability: CapPublish, Kind: "selector", Prompt: "提交/发布按钮的选择器是什么？"},
				{Key: qResult, Capability: CapPublish, Kind: "selector", Prompt: "发布成功后读取笔记/文章链接的元素选择器是什么？"},
			},
		},
		{
			capability: CapEngagements,
			file:       path.Join(generatedRecipeDir, "engagements.json"),
			build: func(v map[string]string) Recipe {
				return Recipe{
					Name: "engagements",
					Steps: []RecipeStep{
						{Action: "goto", Value: valueOr(v, qEngageURL)},
						{Action: "loop", Selector: valueOr(v, qEngageList), Args: map[string]string{
							"until": "no_new_items", "max_iterations": "20",
						}},
						{Action: "extract", Selector: valueOr(v, qEngageList), Value: "engagements"},
					},
				}
			},
			questions: []Question{
				{Key: qEngageURL, Capability: CapEngagements, Kind: "url", Prompt: "评论/互动列表页的完整 URL 是什么？"},
				{Key: qEngageList, Capability: CapEngagements, Kind: "selector", Prompt: "互动列表（评论/私信）条目的选择器是什么？"},
			},
		},
		{
			capability: CapReply,
			file:       path.Join(generatedRecipeDir, "reply.json"),
			build: func(v map[string]string) Recipe {
				return Recipe{
					Name: "reply",
					Steps: []RecipeStep{
						{Action: "goto", Value: valueOr(v, qReplyURL)},
						{Action: "fill", Selector: valueOr(v, qReplyInput), Value: "{{reply_text}}"},
						{Action: "click", Selector: valueOr(v, qReplySend)},
						{Action: "assert", Selector: valueOr(v, qReplySend)},
					},
				}
			},
			questions: []Question{
				{Key: qReplyURL, Capability: CapReply, Kind: "url", Prompt: "回复页/会话页的完整 URL 是什么？"},
				{Key: qReplyInput, Capability: CapReply, Kind: "selector", Prompt: "回复输入框的选择器是什么？"},
				{Key: qReplySend, Capability: CapReply, Kind: "selector", Prompt: "发送回复按钮的选择器是什么？"},
			},
		},
		{
			capability: CapMetrics,
			file:       path.Join(generatedRecipeDir, "metrics.json"),
			build: func(v map[string]string) Recipe {
				return Recipe{
					Name: "metrics",
					Steps: []RecipeStep{
						{Action: "goto", Value: valueOr(v, qMetricsURL)},
						{Action: "extract", Selector: valueOr(v, qMetricsSel), Value: "metrics"},
					},
				}
			},
			questions: []Question{
				{Key: qMetricsURL, Capability: CapMetrics, Kind: "url", Prompt: "数据/指标页的完整 URL 是什么？"},
				{Key: qMetricsSel, Capability: CapMetrics, Kind: "selector", Prompt: "读取数据指标的区块选择器是什么？"},
			},
		},
		{
			capability: "login",
			file:       path.Join(generatedRecipeDir, "login.json"),
			build: func(v map[string]string) Recipe {
				return Recipe{
					Name: "login",
					Steps: []RecipeStep{
						{Action: "goto", Value: valueOr(v, qLoginURL)},
						{Action: "wait", Selector: valueOr(v, qLoggedIn)},
					},
				}
			},
			questions: []Question{
				{Key: qLoginURL, Capability: "login", Kind: "url", Prompt: "登录页的完整 URL 是什么？"},
				{Key: qLoggedIn, Capability: "login", Kind: "selector", Prompt: "判断「已登录」的元素选择器是什么？"},
			},
		},
	}
}

func (g *Generator) draftFromBrowser(det Detection, in GenerateInput) (*Draft, error) {
	id := channelID(det.Channel)
	answers := copyAnswers(in.Answers)
	metricsMap := parseFieldMap(answers[qMetricsMap])

	draft := &Draft{
		Manifest: Manifest{
			ID:          id,
			Status:      "draft",
			DisplayName: displayName(det.Channel),
			Bindings:    map[string]Binding{},
			CredentialSlots: map[string]CredentialSlot{
				"account": {Required: true, Scope: "account"},
			},
			ContentSchema: ContentSchema{Formats: map[string]ContentFormat{
				"post": {Fields: []string{"title", "body", "media"}},
			}},
			Compliance:   defaultCompliance(),
			MetricsMap:   MetricsMap{Fields: metricsMap},
			AllowedHosts: browserHosts(answers),
		},
		Files: map[string][]byte{},
	}

	metricsDeclined := isDeclined(answers[qMetricsMap])
	matrix := CapabilityMatrix{Channel: det.Channel, Source: SourceBrowser, Notes: det.Notes}
	var questions []Question

	for _, spec := range recipeSpecs() {
		recipe := spec.build(answers)
		// Sample HTML the user pasted becomes the recipe's sandbox fixture.
		if html := strings.TrimSpace(answers[qFixturePrefix+spec.capability]); html != "" {
			name := path.Join(generatedFixture, spec.capability+".html")
			draft.Files[name] = []byte(html)
			recipe.Fixtures = []string{name}
		}
		draft.Files[spec.file] = mustJSON(recipe)

		entry := CapabilityEntry{
			Capability: spec.capability,
			Transport:  "browser",
			Recipe:     spec.file,
			Bound:      true,
			Risks: []string{
				"页面改版会使 recipe 失效，需要重新生成新版本",
				"可能需要处理验证码/风控，高并发下会被限流",
				"需确认平台 ToS 允许自动化操作",
			},
		}
		skipQuestions := spec.capability == CapMetrics && metricsDeclined
		if !skipQuestions {
			for _, q := range spec.questions {
				if strings.TrimSpace(answers[q.Key]) == "" {
					entry.Missing = appendUnique(entry.Missing, q.Key)
					questions = append(questions, q)
				}
			}
			fixtureKey := qFixturePrefix + spec.capability
			if strings.TrimSpace(answers[fixtureKey]) == "" {
				entry.Missing = appendUnique(entry.Missing, fixtureKey)
				questions = append(questions, Question{
					Key: fixtureKey, Capability: spec.capability, Kind: "html",
					Prompt: fmt.Sprintf("请提供「%s」流程的沙箱样例页面 HTML（发布页/数据页/会话页或已登录页）。", spec.capability),
				})
			}
		}
		if spec.capability == CapMetrics {
			switch {
			case len(metricsMap) > 0:
				entry.Bound = true
			case metricsDeclined:
				entry.Bound = false
				entry.Missing = nil
				entry.Risks = append(entry.Risks, "用户选择不启用指标能力")
			default:
				entry.Bound = false
				entry.Missing = appendUnique(entry.Missing, qMetricsMap)
				questions = append(questions, Question{
					Key: qMetricsMap, Capability: CapMetrics, Kind: "mapping",
					Prompt: "指标字段如何对应到统一指标？例如 view_num=views, like_num=likes, collect_num=saves；不需要指标能力请回复 skip。",
				})
			}
		}
		if spec.capability != CapMetrics || len(metricsMap) > 0 {
			draft.Manifest.Capabilities = append(draft.Manifest.Capabilities, spec.capability)
			draft.Manifest.Bindings[spec.capability] = Binding{Transport: "browser", Recipe: spec.file}
		}
		matrix.Capabilities = append(matrix.Capabilities, entry)
	}

	if len(draft.Manifest.AllowedHosts) == 0 {
		for i := range matrix.Capabilities {
			matrix.Capabilities[i].Missing = appendUnique(matrix.Capabilities[i].Missing, "allowed_hosts")
		}
		questions = append(questions, Question{
			Key: "allowed_hosts", Kind: "hosts",
			Prompt: "这个渠道允许 recipe 访问哪些域名？用逗号分隔，例如 example.com, api.example.com。",
		})
	}

	matrix.Questions = questions
	markReady(&matrix)
	draft.Matrix = matrix
	draft.Questions = questions
	if !contains(draft.Manifest.Capabilities, CapMetrics) {
		draft.Manifest.MetricsMap = MetricsMap{}
	}
	return draft, nil
}

// browserHosts reads the egress allowlist from an answer. The value is a plain
// host list; URL forms are reduced to their hostname.
func browserHosts(answers map[string]string) []string {
	raw := strings.TrimSpace(answers["allowed_hosts"])
	if raw == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' }) {
		host := strings.ToLower(strings.TrimSpace(part))
		if host == "" {
			continue
		}
		if strings.Contains(host, "://") {
			if u, err := url.Parse(host); err == nil && u.Hostname() != "" {
				host = strings.ToLower(u.Hostname())
			}
		}
		host = strings.TrimPrefix(host, "*.")
		if host != "" && !seen[host] {
			seen[host] = true
			out = append(out, host)
		}
	}
	sort.Strings(out)
	return out
}

// --- Test ------------------------------------------------------------------

// Test runs every browser recipe in the injected sandbox and structurally
// checks every OpenAPI contract. A pass is recorded in the pack version, and
// Publish refuses without it.
func (g *Generator) Test(ctx context.Context, owner Owner, packID string, version int) error {
	if g.Registry == nil {
		return errors.New("pack generator: no registry configured")
	}
	root, err := g.Registry.Root(owner.Type, owner.ID, packID, version)
	if err != nil {
		return err
	}
	m, err := g.Registry.Load(owner.Type, owner.ID, packID, version)
	if err != nil {
		return err
	}
	meta := g.readMeta(root)
	values := testValues(meta.Answers)

	var checks []string
	contractChecked := false
	testedRecipes := map[string]bool{}
	for _, capability := range m.Capabilities {
		binding := m.Bindings[capability]
		switch binding.Transport {
		case "browser":
			if g.RecipeTester == nil {
				return g.recordTest(root, checks, ErrNoRecipeTester)
			}
			if err := g.testRecipe(ctx, root, binding.Recipe, values); err != nil {
				return g.recordTest(root, checks, fmt.Errorf("%s recipe: %w", capability, err))
			}
			testedRecipes[binding.Recipe] = true
			checks = append(checks, "recipe:"+capability)
		case "openapi":
			if !contractChecked {
				if err := g.checkContract(root); err != nil {
					return g.recordTest(root, checks, err)
				}
				contractChecked = true
			}
			checks = append(checks, "openapi-contract:"+capability)
		case "mcp":
			if strings.TrimSpace(binding.Provider) == "" {
				return g.recordTest(root, checks, errors.New("mcp binding has no provider"))
			}
			checks = append(checks, "mcp:"+capability+":declared")
		default:
			return g.recordTest(root, checks, fmt.Errorf("unknown transport %q", binding.Transport))
		}
	}

	// Setup recipes such as login are not capabilities, but a pack whose login
	// flow was never exercised is not tested. Run any generated recipe that a
	// capability binding did not already cover.
	if g.RecipeTester != nil {
		for _, name := range generatedRecipeFiles(root) {
			if testedRecipes[name] {
				continue
			}
			if err := g.testRecipe(ctx, root, name, values); err != nil {
				return g.recordTest(root, checks, fmt.Errorf("%s recipe: %w", name, err))
			}
			checks = append(checks, "recipe:"+name)
		}
	}
	return g.recordTest(root, checks, nil)
}

func (g *Generator) testRecipe(ctx context.Context, root, name string, values map[string]string) error {
	recipe, err := readRecipe(root, name)
	if err != nil {
		return err
	}
	return g.RecipeTester(ctx, root, recipe, values)
}

func generatedRecipeFiles(root string) []string {
	dir := filepath.Join(root, filepath.FromSlash(generatedRecipeDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		out = append(out, path.Join(generatedRecipeDir, entry.Name()))
	}
	sort.Strings(out)
	return out
}

func (g *Generator) checkContract(root string) error {
	data, err := readInsideRoot(root, contractFile)
	if err != nil {
		return fmt.Errorf("openapi contract missing: %w", err)
	}
	var contract ProviderContractDraft
	if err := json.Unmarshal(data, &contract); err != nil {
		return fmt.Errorf("openapi contract is invalid: %w", err)
	}
	if len(contract.Operations) == 0 {
		return errors.New("openapi contract declares no operations")
	}
	if strings.TrimSpace(contract.BaseURL) == "" {
		return errors.New("openapi contract has no base_url; confirm the channel's API base URL first")
	}
	if g.ContractTester != nil {
		if err := g.ContractTester(context.Background(), contract); err != nil {
			return fmt.Errorf("openapi contract probe: %w", err)
		}
	}
	return nil
}

func readRecipe(root, name string) (Recipe, error) {
	data, err := readInsideRoot(root, name)
	if err != nil {
		return Recipe{}, err
	}
	var recipe Recipe
	if err := json.Unmarshal(data, &recipe); err != nil {
		return Recipe{}, fmt.Errorf("recipe %s: %w", name, err)
	}
	return recipe, nil
}

// testValues supplies the runtime placeholders a recipe needs to execute. They
// are sample content, not configuration: configuration placeholders must be
// answered by the user or the recipe stays unresolved and the test fails.
func testValues(answers map[string]string) map[string]string {
	values := copyAnswers(answers)
	values["title"] = "niuniu 沙箱测试标题"
	values["body"] = "niuniu 沙箱测试正文"
	values["media_path"] = "fixtures/media.png"
	values["reply_text"] = "niuniu 沙箱测试回复"
	return values
}

func (g *Generator) recordTest(root string, checks []string, testErr error) error {
	report := testReport{Status: "passed", TestedAt: g.now().Format(time.RFC3339), Checks: checks}
	if testErr != nil {
		report.Status = "failed"
		report.Error = testErr.Error()
	}
	if err := writeJSONFile(filepath.Join(root, testReportFile), report); err != nil {
		if testErr != nil {
			return testErr
		}
		return err
	}
	return testErr
}

// --- Publish ---------------------------------------------------------------

// Publish is the only path from draft to published. It refuses while questions
// are unanswered and refuses without a passing test report, so neither guessing
// nor an untested recipe can reach a live Pack.
func (g *Generator) Publish(ctx context.Context, owner Owner, packID string, version int) error {
	root, err := g.Registry.Root(owner.Type, owner.ID, packID, version)
	if err != nil {
		return err
	}
	meta := g.readMeta(root)
	if len(meta.Missing) > 0 {
		return fmt.Errorf("%w: %s", ErrMissingInfo, strings.Join(meta.Missing, ", "))
	}
	report, err := readTestReport(root)
	if err != nil {
		return err
	}
	if report == nil || report.Status != "passed" {
		return ErrNotTested
	}
	return g.Registry.Publish(owner.Type, owner.ID, packID, version)
}

func (g *Generator) readMeta(root string) generatorMeta {
	var meta generatorMeta
	data, err := os.ReadFile(filepath.Join(root, generatorMetaFile))
	if err != nil {
		return meta
	}
	_ = json.Unmarshal(data, &meta)
	return meta
}

func readTestReport(root string) (*testReport, error) {
	data, err := os.ReadFile(filepath.Join(root, testReportFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var report testReport
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("test report: %w", err)
	}
	return &report, nil
}

// --- matrix helpers --------------------------------------------------------

func markReady(m *CapabilityMatrix) {
	for i := range m.Capabilities {
		m.Capabilities[i].Ready = m.Capabilities[i].Bound && len(m.Capabilities[i].Missing) == 0
	}
}

func setMissing(m *CapabilityMatrix, capability, key string) {
	i := indexOf(m.Capabilities, capability)
	if i < 0 {
		return
	}
	m.Capabilities[i].Missing = appendUnique(m.Capabilities[i].Missing, key)
}

func indexOf(entries []CapabilityEntry, capability string) int {
	for i := range entries {
		if entries[i].Capability == capability {
			return i
		}
	}
	return -1
}

func appendUnique(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// --- OpenAPI document model ------------------------------------------------

type openAPIDoc struct {
	OpenAPI string `json:"openapi"`
	Swagger string `json:"swagger"`
	Info    struct {
		Title string `json:"title"`
	} `json:"info"`
	Servers []struct {
		URL string `json:"url"`
	} `json:"servers"`
	Host       string   `json:"host"`
	BasePath   string   `json:"basePath"`
	Schemes    []string `json:"schemes"`
	Components *struct {
		SecuritySchemes map[string]openAPISecurityScheme `json:"securitySchemes"`
	} `json:"components"`
	SecurityDefinitions map[string]openAPISecurityScheme `json:"securityDefinitions"`
	Paths               map[string]openAPIPathItem       `json:"paths"`

	ops     []openAPIOp
	baseURL string
	auth    *ContractAuthDraft
}

type openAPIPathItem struct {
	Parameters []openAPIParameter `json:"parameters"`
	Get        *openAPIOperation  `json:"get"`
	Post       *openAPIOperation  `json:"post"`
	Put        *openAPIOperation  `json:"put"`
	Patch      *openAPIOperation  `json:"patch"`
	Delete     *openAPIOperation  `json:"delete"`
	Head       *openAPIOperation  `json:"head"`
	Options    *openAPIOperation  `json:"options"`
}

type openAPIOperation struct {
	OperationID string             `json:"operationId"`
	Tags        []string           `json:"tags"`
	Parameters  []openAPIParameter `json:"parameters"`
	RequestBody *struct {
		Content map[string]struct {
			Schema json.RawMessage `json:"schema"`
		} `json:"content"`
	} `json:"requestBody"`
	Responses map[string]openAPIResponse `json:"responses"`
}

type openAPIParameter struct {
	Name     string          `json:"name"`
	In       string          `json:"in"`
	Type     string          `json:"type"`
	Required bool            `json:"required"`
	Schema   json.RawMessage `json:"schema"`
}

type openAPIResponse struct {
	Content map[string]struct {
		Schema json.RawMessage `json:"schema"`
	} `json:"content"`
	Schema json.RawMessage `json:"schema"`
}

type openAPISecurityScheme struct {
	Type   string `json:"type"`
	In     string `json:"in"`
	Name   string `json:"name"`
	Scheme string `json:"scheme"`
}

type openAPIOp struct {
	Name               string
	Method             string
	Path               string
	ReadOnly           bool
	Parameters         []ContractParamDraft
	BodyFields         []string
	ResponseProperties []string
	ResponseShape      string
}

// parseOpenAPIDocument reads only the structural parts of an OpenAPI 3.x or
// Swagger 2.0 document. Descriptions and summaries are not even modeled.
func parseOpenAPIDocument(body []byte) (openAPIDoc, error) {
	var doc openAPIDoc
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return doc, fmt.Errorf("%w: document is empty", ErrInvalidDocument)
	}
	if err := json.Unmarshal(trimmed, &doc); err != nil {
		return doc, fmt.Errorf("%w: not valid JSON (only JSON OpenAPI/Swagger is supported): %v", ErrInvalidDocument, err)
	}
	openapi3 := strings.HasPrefix(strings.TrimSpace(doc.OpenAPI), "3.")
	swagger2 := strings.TrimSpace(doc.Swagger) == "2.0"
	if !openapi3 && !swagger2 {
		return doc, fmt.Errorf("%w: need openapi 3.x or swagger 2.0", ErrInvalidDocument)
	}
	if doc.Paths == nil {
		return doc, fmt.Errorf("%w: document has no paths object", ErrInvalidDocument)
	}
	if len(doc.Paths) > maxDocPaths {
		return doc, fmt.Errorf("%w: %d paths exceeds the %d cap", ErrInvalidDocument, len(doc.Paths), maxDocPaths)
	}

	doc.baseURL = doc.endpointURL(openapi3)
	doc.auth = doc.securityAuth(openapi3)

	seen := map[string]int{}
	for _, rawPath := range sortedMapKeys(doc.Paths) {
		item := doc.Paths[rawPath]
		p := normalizeDocumentPath(rawPath)
		if p == "" {
			continue
		}
		for _, methodOp := range item.operations() {
			if methodOp.op == nil || len(doc.ops) >= maxDocOps {
				continue
			}
			op := methodOp.op
			name := slugify(op.OperationID)
			if name == "" {
				name = slugify(methodOp.method + " " + p)
			}
			name = uniqueName(name, seen)
			responseProps, responseShape := responseFields(op)
			params := mergeParameters(item.Parameters, op.Parameters)
			if len(params) > 64 {
				params = params[:64]
			}
			doc.ops = append(doc.ops, openAPIOp{
				Name:               name,
				Method:             methodOp.method,
				Path:               p,
				ReadOnly:           methodOp.method == "GET" || methodOp.method == "HEAD",
				Parameters:         params,
				BodyFields:         bodyFields(op),
				ResponseProperties: responseProps,
				ResponseShape:      responseShape,
			})
		}
	}
	if len(doc.ops) == 0 {
		return doc, fmt.Errorf("%w: document declares no operations", ErrInvalidDocument)
	}
	return doc, nil
}

func (doc openAPIDoc) capabilities() []string {
	seen := map[string]bool{}
	var out []string
	for _, op := range doc.ops {
		if c := classifyOperation(op.Method, op.Path); c != "" && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

func (it openAPIPathItem) operations() []struct {
	method string
	op     *openAPIOperation
} {
	return []struct {
		method string
		op     *openAPIOperation
	}{
		{"GET", it.Get}, {"POST", it.Post}, {"PUT", it.Put},
		{"PATCH", it.Patch}, {"DELETE", it.Delete}, {"HEAD", it.Head},
		{"OPTIONS", it.Options},
	}
}

func (doc openAPIDoc) endpointURL(openapi3 bool) string {
	if openapi3 {
		for _, server := range doc.Servers {
			raw := strings.TrimSpace(server.URL)
			if raw == "" {
				continue
			}
			u, err := url.Parse(raw)
			if err != nil {
				continue
			}
			if u.Host == "" {
				return "" // relative server: host unknown, base path alone is not usable
			}
			return strings.TrimRight(u.String(), "/")
		}
		return ""
	}
	host := strings.TrimSpace(doc.Host)
	if host == "" {
		return ""
	}
	scheme := "https"
	for _, candidate := range doc.Schemes {
		if strings.EqualFold(strings.TrimSpace(candidate), "http") {
			scheme = "http"
			break
		}
	}
	base := cleanDocumentPath(doc.BasePath)
	return scheme + "://" + host + base
}

func (doc openAPIDoc) securityAuth(openapi3 bool) *ContractAuthDraft {
	schemes := doc.SecurityDefinitions
	if openapi3 && doc.Components != nil && len(doc.Components.SecuritySchemes) > 0 {
		schemes = doc.Components.SecuritySchemes
	}
	for _, name := range sortedMapKeys(schemes) {
		scheme := schemes[name]
		switch strings.ToLower(strings.TrimSpace(scheme.Type)) {
		case "apikey":
			if strings.EqualFold(strings.TrimSpace(scheme.In), "header") && strings.TrimSpace(scheme.Name) != "" {
				return &ContractAuthDraft{Strategy: "custom_header", Header: scheme.Name,
					Note: "injected automatically by the niuniu proxy — do NOT include it yourself."}
			}
		case "http":
			switch strings.ToLower(strings.TrimSpace(scheme.Scheme)) {
			case "bearer":
				return &ContractAuthDraft{Strategy: "bearer", Header: "Authorization", Prefix: "Bearer ",
					Note: "injected automatically by the niuniu proxy — do NOT include it yourself."}
			case "basic":
				return &ContractAuthDraft{Strategy: "basic",
					Note: "injected automatically by the niuniu proxy — do NOT include it yourself."}
			}
		case "basic":
			return &ContractAuthDraft{Strategy: "basic",
				Note: "injected automatically by the niuniu proxy — do NOT include it yourself."}
		case "oauth2", "openidconnect":
			return &ContractAuthDraft{Strategy: "oauth2_refresh",
				Note: "OAuth2 requires manual token-flow setup; the draft cannot configure it."}
		}
	}
	return nil
}

func normalizeDocumentPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || strings.Contains(p, "://") || strings.ContainsAny(p, "?#") {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == ".." {
			return ""
		}
	}
	return p
}

func cleanDocumentPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" || strings.ContainsAny(p, "?#") {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == ".." {
			return ""
		}
	}
	return strings.TrimRight(p, "/")
}

func mergeParameters(pathLevel, opLevel []openAPIParameter) []ContractParamDraft {
	byKey := map[string]openAPIParameter{}
	var order []string
	add := func(params []openAPIParameter) {
		for _, p := range params {
			name := strings.TrimSpace(p.Name)
			if name == "" {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(p.In)) + "\x00" + name
			if _, ok := byKey[key]; !ok {
				order = append(order, key)
			}
			byKey[key] = p
		}
	}
	add(pathLevel)
	add(opLevel)

	out := make([]ContractParamDraft, 0, len(order))
	for _, key := range order {
		p := byKey[key]
		loc := strings.ToLower(strings.TrimSpace(p.In))
		switch loc {
		case "query", "path", "body", "header":
		case "formdata":
			loc = "body"
		default:
			continue
		}
		out = append(out, ContractParamDraft{
			Name:     p.Name,
			In:       loc,
			Type:     parameterType(p),
			Required: p.Required,
		})
	}
	return out
}

func parameterType(p openAPIParameter) string {
	if t := strings.TrimSpace(p.Type); t != "" {
		return t
	}
	if len(p.Schema) == 0 {
		return ""
	}
	var schema struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(p.Schema, &schema) == nil {
		return strings.TrimSpace(schema.Type)
	}
	return ""
}

func bodyFields(op *openAPIOperation) []string {
	if op.RequestBody == nil {
		return nil
	}
	var schema json.RawMessage
	for _, key := range sortedMapKeys(op.RequestBody.Content) {
		if strings.Contains(key, "json") && len(op.RequestBody.Content[key].Schema) > 0 {
			schema = op.RequestBody.Content[key].Schema
			break
		}
	}
	if len(schema) == 0 {
		return nil
	}
	var body struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(schema, &body) != nil || body.Type != "object" {
		return nil
	}
	return capStrings(sortedMapKeys(body.Properties), 32)
}

func capStrings(values []string, max int) []string {
	if max > 0 && len(values) > max {
		return values[:max]
	}
	return values
}

// responseFields returns the property names of the first 2xx response (for
// metrics mapping) and a compact structural shape hint.
func responseFields(op *openAPIOperation) ([]string, string) {
	var schema json.RawMessage
	for _, code := range sortedMapKeys(op.Responses) {
		if !strings.HasPrefix(code, "2") {
			continue
		}
		if s := op.Responses[code].schema(); len(s) > 0 {
			schema = s
			break
		}
	}
	if len(schema) == 0 {
		if r, ok := op.Responses["default"]; ok {
			schema = r.schema()
		}
	}
	if len(schema) == 0 {
		return nil, ""
	}
	var node struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Items      json.RawMessage            `json:"items"`
	}
	if json.Unmarshal(schema, &node) != nil {
		return nil, ""
	}
	if node.Type == "array" {
		var item struct {
			Type       string                     `json:"type"`
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if json.Unmarshal(node.Items, &item) == nil && item.Type == "object" {
			props := sortedMapKeys(item.Properties)
			return props, "array<object>"
		}
		return nil, "array"
	}
	if node.Type == "object" {
		props := sortedMapKeys(node.Properties)
		// A list envelope ("data"/"items"/...) contributes its inner rows.
		for _, envelope := range []string{"data", "items", "results", "list", "records", "rows"} {
			raw, ok := node.Properties[envelope]
			if !ok {
				continue
			}
			var inner struct {
				Type       string                     `json:"type"`
				Properties map[string]json.RawMessage `json:"properties"`
				Items      json.RawMessage            `json:"items"`
			}
			if json.Unmarshal(raw, &inner) != nil {
				continue
			}
			if inner.Type == "array" {
				var row struct {
					Type       string                     `json:"type"`
					Properties map[string]json.RawMessage `json:"properties"`
				}
				if json.Unmarshal(inner.Items, &row) == nil && row.Type == "object" {
					return capStrings(sortedMapKeys(row.Properties), 64), "object{" + envelope + ":array<object>}"
				}
			}
			if inner.Type == "object" {
				return sortedMapKeys(inner.Properties), "object{" + envelope + ":object}"
			}
		}
		return capStrings(props, 64), "object"
	}
	return nil, ""
}

func (r openAPIResponse) schema() json.RawMessage {
	if len(r.Schema) > 0 {
		return r.Schema
	}
	for _, key := range sortedMapKeys(r.Content) {
		if strings.Contains(key, "json") && len(r.Content[key].Schema) > 0 {
			return r.Content[key].Schema
		}
	}
	for _, key := range sortedMapKeys(r.Content) {
		if len(r.Content[key].Schema) > 0 {
			return r.Content[key].Schema
		}
	}
	return nil
}

func hasPathParameter(params []ContractParamDraft) bool {
	for _, p := range params {
		if p.In == "path" {
			return true
		}
	}
	return false
}

// --- classification --------------------------------------------------------

// classifyOperation maps a structural (method, path) pair to a capability.
// Order matters: "create comment" must read as reply, not publish.
func classifyOperation(method, rawPath string) string {
	p := strings.ToLower(rawPath)
	tokens := pathTokens(p)
	has := func(words ...string) bool {
		for _, word := range words {
			if tokens[word] {
				return true
			}
		}
		return false
	}
	write := method == "POST" || method == "PUT" || method == "PATCH" || method == "DELETE"

	if has("metrics", "insights", "analytics", "stats", "statistics", "report", "reports", "dashboard") {
		return CapMetrics
	}
	if has("comments", "comment", "replies", "reply", "responses", "messages", "inbox", "mentions") {
		if write {
			return CapReply
		}
		return CapEngagements
	}
	if has("engagements", "interactions", "reviews", "feeds") {
		return CapEngagements
	}
	if write && has("publish", "posts", "post", "articles", "article", "notes", "note", "content",
		"media", "upload", "statuses", "status", "share", "feed") {
		return CapPublish
	}
	return ""
}

// pathTokens splits a URL path into word tokens so "posts" does not match
// inside an unrelated segment and "{pageId}" contributes nothing useful.
func pathTokens(p string) map[string]bool {
	tokens := map[string]bool{}
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			tokens[b.String()] = true
			b.Reset()
		}
	}
	for _, r := range p {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return tokens
}

// --- metric mapping --------------------------------------------------------

var metricVocabulary = map[string]string{
	"views": "views", "view": "views", "view_count": "views", "view_num": "views",
	"read_count": "views", "reads": "views", "impressions": "views",
	"impression_count": "views", "plays": "views", "play_count": "views",
	"page_views": "views",
	"likes":      "likes", "like": "likes", "like_count": "likes", "like_num": "likes",
	"reactions": "likes", "upvotes": "likes",
	"comments": "comments", "comment": "comments", "comment_count": "comments",
	"comment_num": "comments",
	"shares":      "shares", "share": "shares", "share_count": "shares", "share_num": "shares",
	"reposts": "shares", "repost_count": "shares",
	"saves": "saves", "save": "saves", "save_count": "saves", "collects": "saves",
	"collect_num": "saves", "collect_count": "saves", "favorites": "saves",
	"favorite_count": "saves", "bookmarks": "saves",
	"follower_delta": "follower_delta", "fans_add": "follower_delta",
	"net_fans": "follower_delta", "new_followers": "follower_delta",
	"followers_delta": "follower_delta", "subscribers_delta": "follower_delta",
}

func deriveMetricsMap(properties []string) map[string]string {
	out := map[string]string{}
	for _, prop := range properties {
		if unified, ok := metricVocabulary[strings.ToLower(strings.TrimSpace(prop))]; ok {
			out[prop] = unified
		}
	}
	return out
}

// parseFieldMap accepts either a JSON object or "key=value, key=value".
// isDeclined reports an explicit "do not enable this capability" answer.
func isDeclined(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "skip", "no", "none", "off", "不需要", "不启用", "跳过":
		return true
	}
	return false
}

func parseFieldMap(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	out := map[string]string{}
	if strings.HasPrefix(raw, "{") {
		if json.Unmarshal([]byte(raw), &out) != nil {
			return nil
		}
		clean := map[string]string{}
		for k, v := range out {
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			if k != "" && v != "" {
				clean[k] = v
			}
		}
		if len(clean) == 0 {
			return nil
		}
		return clean
	}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n'
	}) {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if key != "" && value != "" {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// --- small helpers ---------------------------------------------------------

func defaultCompliance() Compliance {
	return Compliance{RequiredChecks: []string{
		"平台社区规范与广告法自查",
		"版权、来源与转载授权确认",
		"资质与功效宣称核查",
	}}
}

// channelID derives a stable pack id. A non-latin channel name gets a
// deterministic hash suffix rather than a guessed transliteration.
func channelID(channel string) string {
	if id := slugify(channel); id != "" {
		return id
	}
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(channel))))
	return "channel_" + hex.EncodeToString(sum[:4])
}

func displayName(channel string) string {
	clean := sanitizeLabel(channel)
	if clean == "" {
		return channelID(channel)
	}
	return clean
}

func sanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 32 || r == 127 {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if r := []rune(out); len(r) > 120 {
		out = string(r[:120])
	}
	return out
}

var slugPattern = func() *strings.Replacer {
	return strings.NewReplacer(" ", "-", "_", "-", ".", "-")
}()

// slugify lowercases and keeps [a-z0-9-]; separators collapse, edges trim.
func slugify(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	raw = slugPattern.Replace(raw)
	var b strings.Builder
	lastDash := false
	for _, r := range raw {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		case r == '-':
			if b.Len() > 0 && !lastDash {
				b.WriteRune(r)
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func uniqueName(name string, seen map[string]int) string {
	if name == "" {
		name = "operation"
	}
	seen[name]++
	if n := seen[name]; n > 1 {
		return name + "_" + strconv.Itoa(n)
	}
	return name
}

func copyAnswers(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		if strings.TrimSpace(k) == "" {
			continue
		}
		out[k] = v
	}
	return out
}

// valueOr keeps an unanswered fact as an explicit placeholder, so a draft
// artifact shows exactly what is missing instead of an empty selector.
func valueOr(values map[string]string, key string) string {
	if value := strings.TrimSpace(values[key]); value != "" {
		return value
	}
	return "{{" + key + "}}"
}

func draftSourceDigest(source string, document []byte, answers map[string]string) string {
	payload := struct {
		Source   string            `json:"source"`
		Document string            `json:"document"`
		Answers  map[string]string `json:"answers,omitempty"`
	}{
		Source:   source,
		Document: sha256Hex(document),
		Answers:  answers,
	}
	return "sha256:" + sha256Hex(mustJSON(payload))
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func mustJSON(v any) []byte {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		// Only plain structs/maps are ever marshaled here; a failure is a
		// programming error, and an empty document would silently break a pack.
		panic(fmt.Sprintf("pack generator: marshal: %v", err))
	}
	return data
}

func writeJSONFile(name string, v any) error {
	return atomicWriteFile(name, mustJSON(v))
}

func sortedMapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
