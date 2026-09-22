package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/niuniu-dev/niuniu/agent/internal/lsp"
	"github.com/niuniu-dev/niuniu/agent/internal/model"
)

// LSP gives the agent language-server navigation: go-to-definition,
// find-references, hover, and document symbols — precision that text
// grep cannot provide (same-name symbols, cross-file renames). Servers are
// declared in .niuniu-agent/lsp.json and started lazily per workspace.
type LSP struct{ Mgr *lsp.Manager }

type lspInput struct {
	Operation string `json:"operation"` // definition | references | hover | symbols
	Path      string `json:"path"`
	// Line/Character are 1-based (editor convention the model knows);
	// converted to LSP's 0-based internally.
	Line      int `json:"line"`
	Character int `json:"character"`
}

func (l LSP) Def() model.ToolDef {
	return model.ToolDef{
		Name:        "LSP",
		Description: "Language-server navigation: 'definition' (where is this symbol defined), 'references' (every usage), 'hover' (signature/type info), 'symbols' (outline of a file). Positions are 1-based line/character. Requires a language server in .niuniu-agent/lsp.json.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"operation":{"type":"string","enum":["definition","references","hover","symbols"]},` +
			`"path":{"type":"string","description":"File to query"},` +
			`"line":{"type":"integer","description":"1-based line of the symbol"},` +
			`"character":{"type":"integer","description":"1-based column of the symbol"}},` +
			`"required":["operation","path"]}`),
	}
}

func (l LSP) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	if l.Mgr == nil {
		return "", fmt.Errorf("LSP unavailable: no lsp.json configured")
	}
	var in lspInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if in.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	switch in.Operation {
	case "definition", "references":
		if in.Line <= 0 || in.Character <= 0 {
			return "", fmt.Errorf("line/character (1-based) are required for %s", in.Operation)
		}
	}
	switch in.Operation {
	case "definition":
		locs, err := l.Mgr.Definition(ctx, in.Path, in.Line-1, in.Character-1)
		return formatLocations("definition", locs), wrapErr(err)
	case "references":
		locs, err := l.Mgr.References(ctx, in.Path, in.Line-1, in.Character-1)
		return formatLocations("references", locs), wrapErr(err)
	case "hover":
		text, err := l.Mgr.Hover(ctx, in.Path, in.Line-1, in.Character-1)
		if err != nil {
			return "", wrapErr(err)
		}
		if text == "" {
			return "(no hover information at this position)", nil
		}
		return text, nil
	case "symbols":
		syms, err := l.Mgr.Symbols(ctx, in.Path)
		if err != nil {
			return "", wrapErr(err)
		}
		var b []byte
		for _, s := range syms {
			b = append(b, fmt.Sprintf("%d:%d  %s  %s\n", s.Line+1, s.Character+1, s.Kind, s.Name)...)
		}
		if len(b) == 0 {
			return "(no symbols)", nil
		}
		return string(b), nil
	default:
		return "", fmt.Errorf("unknown operation %q (want definition|references|hover|symbols)", in.Operation)
	}
}

func formatLocations(op string, locs []lsp.Location) string {
	if len(locs) == 0 {
		return "(" + op + ": no results)"
	}
	out := ""
	for _, l := range locs {
		out += fmt.Sprintf("%s:%d:%d\n", l.Path, l.Line+1, l.Character+1)
	}
	return out
}

func wrapErr(err error) error {
	if err != nil {
		return fmt.Errorf("LSP: %w", err)
	}
	return nil
}
