package packs

// Binding declares how one capability reaches its platform: an OpenAPI
// integration provider, an MCP server, a declarative browser recipe, or a
// manual hand-off. A Pack never stores credentials here.
type Binding struct {
	Transport string `json:"transport"` // openapi | mcp | browser | manual
	Provider  string `json:"provider,omitempty"`
	Recipe    string `json:"recipe,omitempty"`
}

// CredentialSlot names a credential a Pack needs and who owns it.
type CredentialSlot struct {
	Required bool   `json:"required"`
	Scope    string `json:"scope"` // owner | account
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
