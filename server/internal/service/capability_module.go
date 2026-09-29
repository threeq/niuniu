package service

import "encoding/json"

// CapabilityModule describes one installable capability module: the module
// name the server projects into scenes (.mcp.json), its display label, the
// config schema the settings UI renders, and the scenes that ship it.
//
// ConfigSchema is an opaque JSON document with the shape
//
//	{"capabilities":[{"key","label","backends":[{"value","label","fields":[
//	   {"key","label","type"}]}]}]}
//
// consumed verbatim by the settings page (flow E of the video-creation plan);
// the server never interprets it.
type CapabilityModule struct {
	Name         string          `json:"name"`
	DisplayName  string          `json:"display_name"`
	ConfigSchema json.RawMessage `json:"config_schema"`
	Scenes       []string        `json:"scenes"`
}

// videoGenConfigSchema is the video-gen module's config schema. The backend
// values are the adapter implementation names the module's registry resolves
// at runtime (see the plan's §1.5): openai-compat for tts/image, plus the
// seedance and kling adapters for video. api_key fields carry type "secret"
// so the UI masks them; every field maps to one extra_config key (or one of
// the canonical base_url / api_key columns).
const videoGenConfigSchema = `{
  "capabilities": [
    {
      "key": "tts",
      "label": "配音",
      "backends": [
        {
          "value": "openai-compat",
          "label": "OpenAI 兼容",
          "fields": [
            {"key": "base_url", "label": "Base URL", "type": "string"},
            {"key": "api_key", "label": "API Key", "type": "secret"},
            {"key": "model", "label": "模型", "type": "string"}
          ]
        }
      ]
    },
    {
      "key": "image",
      "label": "图像",
      "backends": [
        {
          "value": "openai-compat",
          "label": "OpenAI 兼容",
          "fields": [
            {"key": "base_url", "label": "Base URL", "type": "string"},
            {"key": "api_key", "label": "API Key", "type": "secret"},
            {"key": "model", "label": "模型", "type": "string"}
          ]
        }
      ]
    },
    {
      "key": "video",
      "label": "视频",
      "backends": [
        {
          "value": "seedance",
          "label": "Seedance（豆包）",
          "fields": [
            {"key": "base_url", "label": "Base URL", "type": "string"},
            {"key": "api_key", "label": "API Key", "type": "secret"},
            {"key": "model", "label": "模型", "type": "string"}
          ]
        },
        {
          "value": "kling",
          "label": "可灵 Kling",
          "fields": [
            {"key": "base_url", "label": "Base URL", "type": "string"},
            {"key": "api_key", "label": "API Key", "type": "secret"},
            {"key": "model", "label": "模型", "type": "string"}
          ]
        }
      ]
    }
  ]
}`

// videoGenModule is the only capability module this wave ships.
var videoGenModule = CapabilityModule{
	Name:         "video-gen",
	DisplayName:  "视频创作",
	ConfigSchema: json.RawMessage(videoGenConfigSchema),
	Scenes:       []string{"media-studio"},
}

// ListCapabilityModules returns the capability modules this build knows about.
// The registry is a static in-process list for now — modules are compiled in,
// not installed at runtime — so the list endpoint needs no DB access.
func ListCapabilityModules() []CapabilityModule {
	return []CapabilityModule{videoGenModule}
}
