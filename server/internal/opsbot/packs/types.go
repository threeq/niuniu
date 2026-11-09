package packs

// Binding declares how one capability reaches its platform: an OpenAPI
// integration provider, an MCP server, a declarative browser recipe, or a
// manual hand-off. A Pack never stores credentials here.
//
// For transport=openapi the binding is purely declarative: it names the
// provider-contract operation(s) to call and how to map the payload/response.
// No channel-specific Go code is involved — the shared integration proxy
// executes the contract with auth injection, scoping and audit.
type Binding struct {
	Transport string `json:"transport"` // openapi | mcp | browser | manual
	Provider  string `json:"provider,omitempty"`
	Recipe    string `json:"recipe,omitempty"`

	// Operation is the primary contract operation for this capability
	// (metrics / engagements / reply, and single-step publish).
	Operation string `json:"operation,omitempty"`
	// Operations names the ordered sub-steps of a multi-step publish
	// (e.g. {"upload": "material_upload", "draft": "draft_add",
	// "publish": "freepublish_submit"}). When set, publish runs them in order.
	Operations map[string]string `json:"operations,omitempty"`
	// Outputs maps a semantic output ("id", "url") to the response field that
	// carries it, used for the returned receipt.
	Outputs map[string]string `json:"outputs,omitempty"`
	// Params maps payload fields to operation parameter names. Absent keys fall
	// back to the same name.
	Params map[string]string `json:"params,omitempty"`
	// Fixed carries constant operation parameters (e.g. {"index": 0}).
	Fixed map[string]any `json:"fixed,omitempty"`
	// StepFields narrows which payload fields each publish step receives
	// ({"draft": ["title", "content"]}); unset means "all fields".
	StepFields map[string][]string `json:"step_fields,omitempty"`
	// Templates renders a step's JSON body from the merged field/output bag
	// ({"draft": {"articles": [{"title": "{{title}}"}]}}). Values that are
	// strings and contain placeholders are substituted recursively; other
	// values pass through unchanged.
	Templates map[string]any `json:"templates,omitempty"`
	// Files declares multipart uploads per step
	// ({"upload": {"media": "cover_path"}}: operation file param -> payload
	// field holding the local path).
	Files map[string]map[string]string `json:"files,omitempty"`
	// Items names the response key holding the parent list (nested listings),
	// or the engagement list for single-step listings.
	Items string `json:"items,omitempty"`
	// SubItems names the response key holding the engagement list of each
	// parent in a nested listing.
	SubItems string `json:"sub_items,omitempty"`
	// ValueKey names the numeric field inside a metrics row (e.g. insights
	// returns [{name, values:[{value}]}] → "values").
	ValueKey string `json:"value_key,omitempty"`
	// Fields maps engagement fields to response fields
	// ({"id": "user_comment_id", "author": "openid", "text": "content"}).
	Fields map[string]string `json:"fields,omitempty"`
}

// CredentialSlot names a credential a Pack needs and who owns it.
type CredentialSlot struct {
	Required bool   `json:"required"`
	Scope    string `json:"scope"` // owner | account
	// Fields names the credential form fields the one-time link renders
	// (e.g. ["page_token"]). Empty means the AppID/AppSecret default.
	Fields []string `json:"fields,omitempty"`
}

// ContentFormat constrains one kind of content the platform accepts.
type ContentFormat struct {
	Fields        []string `json:"fields"`
	MaxChars      int      `json:"max_chars"`
	MaxTitleChars int      `json:"max_title_chars"`
	ImageRatio    string   `json:"image_ratio"`
	MaxImages     int      `json:"max_images"`
	VideoSec      int      `json:"video_sec"`
}

// ContentSchema maps a named content format (article, note, video, post) to its
// constraints.
type ContentSchema struct {
	Formats map[string]ContentFormat `json:"formats"`
}

// Compliance is the platform red-line checklist applied before publishing.
type Compliance struct {
	ForbiddenTerms []string `json:"forbidden_terms"`
	RequiredChecks []string `json:"required_checks"`
}

// MetricsMap translates platform metric keys to the unified metric namespace.
type MetricsMap struct {
	Fields map[string]string `json:"fields"`
}

// RecipeStep is one declarative browser action.
type RecipeStep struct {
	Action   string            `json:"action"`
	Selector string            `json:"selector,omitempty"`
	Value    string            `json:"value,omitempty"`
	Fallback []string          `json:"fallback,omitempty"`
	Timeout  int               `json:"timeout_ms,omitempty"`
	Args     map[string]string `json:"args,omitempty"`
}

// Recipe is a declarative browser automation script with sandbox fixtures.
type Recipe struct {
	Name     string       `json:"name"`
	Steps    []RecipeStep `json:"steps"`
	Fixtures []string     `json:"fixtures,omitempty"`
}

// Manifest is a Platform Pack's identity, capabilities and their bindings.
type Manifest struct {
	ID              string                    `json:"id"`
	Version         int                       `json:"version"`
	Status          string                    `json:"status"` // draft | testing | published | deprecated
	DisplayName     string                    `json:"display_name"`
	Capabilities    []string                  `json:"capabilities"`
	Bindings        map[string]Binding        `json:"bindings"`
	CredentialSlots map[string]CredentialSlot `json:"credential_slots"`
	ContentSchema   ContentSchema             `json:"content_schema"`
	Compliance      Compliance                `json:"compliance"`
	MetricsMap      MetricsMap                `json:"metrics_map"`
	// AllowedHosts is the browser-recipe egress allowlist. Browser recipes may
	// only navigate to these hosts; an empty list denies every navigation.
	AllowedHosts []string `json:"allowed_hosts,omitempty"`
}
