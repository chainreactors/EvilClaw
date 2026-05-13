package controlallowlist

import "testing"

func TestAllowsAgent(t *testing.T) {
	if !AllowsAgent("openclaw", "OpenAI/JS 6.26.0") {
		t.Fatal("openclaw agent should be allowed")
	}
	if !AllowsAgent("", "openclaw/1.0 (Linux; x86_64)") {
		t.Fatal("openclaw User-Agent should be allowed")
	}
	if AllowsAgent("claude-code", "claude-code/2.1.71") {
		t.Fatal("claude-code should be blocked")
	}
}
