package service

import "strings"

// EnvHasKey returns true if the given env slice (KEY=VALUE strings, like
// os.Environ()) already contains an entry for the named key. Case-sensitive
// to match OS env semantics on Unix; on Windows env keys are case-insensitive
// but our injection target keys (CLAUDE_CONFIG_DIR) are uppercase by
// convention so this is fine. Exported for use by the agentproxy package.
func EnvHasKey(env []string, key string) bool {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}
