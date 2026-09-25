package model

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// mustParseOrNil parses layer bytes; nil/invalid → nil.
func mustParseOrNil(data []byte) *ProfileFile {
	if len(data) == 0 {
		return nil
	}
	pf, err := ParseProfileFile(data)
	if err != nil {
		return nil
	}
	return pf
}

// LoadChain reads the profile file chain for cwd: --config path (when set)
// wins; otherwise project config before global. Missing files yield nil
// layers (legacy env-only path keeps working).
func LoadChain(cwd, configFlag, home string) (projectData, globalData []byte, projectPath string) {
	if configFlag != "" {
		data, err := os.ReadFile(configFlag)
		if err != nil {
			return nil, nil, configFlag
		}
		return data, nil, configFlag
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	projPath := filepath.Join(cwd, ".niuniu-agent", "config.json")
	if data, err := os.ReadFile(projPath); err == nil {
		return data, nil, projPath
	}
	if home != "" {
		globPath := filepath.Join(home, ".niuniu-agent", "config.json")
		if data, err := os.ReadFile(globPath); err == nil {
			return nil, data, globPath
		}
	}
	return nil, nil, ""
}

// ResolveFromCwd is the convenience entry for CLI commands: it loads the
// config chain for cwd and resolves the config honoring flags + env.
func ResolveFromCwd(cwd, configFlag string, flags Flags) (Config, error) {
	proj, glob, _ := LoadChain(cwd, configFlag, "")
	return Resolve(proj, glob, flags, cwd)
}

// RenderProfiles renders the `profiles` subcommand listing: available
// profiles with provider/model/credential-env names and which one is
// active. Credential VALUES are never shown — only env names and whether
// they are currently set.
func RenderProfiles(cwd, configFlag string, active Config) string {
	projData, globData, activePath := LoadChain(cwd, configFlag, "")
	proj, glob := mustParseOrNil(projData), mustParseOrNil(globData)
	var b strings.Builder
	fmt.Fprintf(&b, "config: %s\n", orDefault(activePath, "(none — env-only mode)"))
	if active.Model != "" {
		fmt.Fprintf(&b, "active: provider=%s model=%s\n", active.Provider, active.Model)
	}
	if proj == nil && glob == nil {
		b.WriteString("no profiles defined — env-only mode (set ANTHROPIC_*/OPENAI_* or create .niuniu-agent/config.json)\n")
		return b.String()
	}
	type row struct {
		name string
		p    Profile
	}
	var rows []row
	seen := map[string]bool{}
	for _, pf := range []*ProfileFile{proj, glob} {
		if pf == nil {
			continue
		}
		names := make([]string, 0, len(pf.Profiles))
		for n := range pf.Profiles {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if seen[n] {
				continue
			}
			seen[n] = true
			rows = append(rows, row{n, pf.Profiles[n]})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
	for _, r := range rows {
		activeMark := ""
		if r.name == profileNameOf(active, cwd, configFlag) {
			activeMark = "  ← active"
		}
		cred := r.p.AuthTokenEnv
		if cred == "" {
			cred = r.p.APIKeyEnv
		}
		credState := "unset"
		if cred != "" && os.Getenv(cred) != "" {
			credState = "set"
		}
		fmt.Fprintf(&b, "  %-16s provider=%-9s model=%-24s cred=%s(%s)%s\n",
			r.name, r.p.Provider, r.p.Model, orDefault(cred, "—"), credState, activeMark)
		if r.p.Thinking != "" {
			fmt.Fprintf(&b, "    thinking: %s\n", r.p.Thinking)
		}
	}
	return b.String()
}

// profileNameOf reverse-maps the active config to a profile name (best
// effort, for the listing's "← active" marker).
func profileNameOf(active Config, cwd, configFlag string) string {
	projData, globData, _ := LoadChain(cwd, configFlag, "")
	proj, glob := mustParseOrNil(projData), mustParseOrNil(globData)
	for _, pf := range []*ProfileFile{proj, glob} {
		if pf == nil {
			continue
		}
		for n, p := range pf.Profiles {
			if p.Model == active.Model && p.Provider == active.Provider {
				return n
			}
		}
	}
	return ""
}
