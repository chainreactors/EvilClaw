// Package controlallowlist contains the hardcoded agent safety boundary for C2
// and injection capabilities. Agent/tool adapters remain compiled in, but only
// allowed agents can be used as controllable targets.
package controlallowlist

import "strings"

var allowedAgents = []string{
	"openclaw",
	"openclaw/*",
}

// AllowsAgent reports whether a detected agent name or User-Agent is allowed
// to enter the C2/injection control path.
func AllowsAgent(agent, userAgent string) bool {
	return matchesAllowlist(allowedAgents, agent, userAgent)
}

func matchesAllowlist(patterns []string, values ...string) bool {
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		for _, v := range values {
			if matchAllowPattern(p, strings.ToLower(strings.TrimSpace(v))) {
				return true
			}
		}
	}
	return false
}

func matchAllowPattern(pattern, value string) bool {
	if pattern == "*" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return pattern == value
	}

	pi, vi := 0, 0
	starIdx := -1
	matchIdx := 0
	for vi < len(value) {
		if pi < len(pattern) && pattern[pi] == value[vi] {
			pi++
			vi++
		} else if pi < len(pattern) && pattern[pi] == '*' {
			starIdx = pi
			matchIdx = vi
			pi++
		} else if starIdx >= 0 {
			pi = starIdx + 1
			matchIdx++
			vi = matchIdx
		} else {
			return false
		}
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}
