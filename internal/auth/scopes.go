// Package auth handles bearer-token authentication and authorization for the
// control plane. It stores hashed tokens (master + device-scoped) on disk,
// validates them in constant time, and exposes middleware for both
// HTTP and WebSocket subprotocol authentication.
package auth

// Scope names follow `resource:verb` form. Coarse on purpose — finer grain
// can layer in later without renaming existing ones.
const (
	ScopeSessionsRead  = "sessions:read"
	ScopeSessionsWrite = "sessions:write"
	ScopeSessionsInput = "sessions:input"
	ScopeHooksRead     = "hooks:read"
	ScopeHooksWrite    = "hooks:write"
	ScopeTokensRead    = "tokens:read"
	ScopeTokensWrite   = "tokens:write"
	ScopeProxyRead     = "proxy:read"
	ScopeUsageRead     = "usage:read"
)

// AllScopes is the full set granted to the master token.
var AllScopes = []string{
	ScopeSessionsRead,
	ScopeSessionsWrite,
	ScopeSessionsInput,
	ScopeHooksRead,
	ScopeHooksWrite,
	ScopeTokensRead,
	ScopeTokensWrite,
	ScopeProxyRead,
	ScopeUsageRead,
}

// hasScope reports whether `granted` contains `wanted`. `"*"` is a wildcard
// that satisfies any single requested scope.
func hasScope(granted []string, wanted string) bool {
	for _, g := range granted {
		if g == "*" || g == wanted {
			return true
		}
	}
	return false
}

// hasAllScopes reports whether every `wanted` scope is in `granted`.
func hasAllScopes(granted []string, wanted []string) bool {
	for _, w := range wanted {
		if !hasScope(granted, w) {
			return false
		}
	}
	return true
}
