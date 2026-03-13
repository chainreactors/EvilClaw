package toolinjection

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestGenerateOpenAIToolCallID(t *testing.T) {
	id := GenerateOpenAIToolCallID(0)
	if !strings.HasPrefix(id, "call_"+InjectedIDMarker) {
		t.Errorf("expected prefix call_%s, got %s", InjectedIDMarker, id)
	}
	if !IsInjectedID(id) {
		t.Errorf("IsInjectedID should return true for %s", id)
	}
}

func TestGenerateClaudeToolUseID(t *testing.T) {
	id := GenerateClaudeToolUseID(0)
	if !strings.HasPrefix(id, "toolu_"+InjectedIDMarker) {
		t.Errorf("expected prefix toolu_%s, got %s", InjectedIDMarker, id)
	}
	if !IsInjectedID(id) {
		t.Errorf("IsInjectedID should return true for %s", id)
	}
}

func TestExtractTaskID(t *testing.T) {
	// Round-trip: encode and extract
	id := GenerateOpenAIToolCallID(42)
	taskID, ok := ExtractTaskID(id)
	if !ok {
		t.Fatalf("ExtractTaskID failed for %s", id)
	}
	if taskID != 42 {
		t.Errorf("expected taskID 42, got %d", taskID)
	}

	// Claude format
	id2 := GenerateClaudeToolUseID(0xDEAD)
	taskID2, ok2 := ExtractTaskID(id2)
	if !ok2 {
		t.Fatalf("ExtractTaskID failed for %s", id2)
	}
	if taskID2 != 0xDEAD {
		t.Errorf("expected taskID 0xDEAD, got %d", taskID2)
	}

	// Non-injected ID
	_, ok3 := ExtractTaskID("call_abc123")
	if ok3 {
		t.Error("should not extract from non-injected ID")
	}

	// taskID 0 (global rules)
	id4 := GenerateOpenAIToolCallID(0)
	taskID4, ok4 := ExtractTaskID(id4)
	if !ok4 {
		t.Fatalf("ExtractTaskID failed for %s", id4)
	}
	if taskID4 != 0 {
		t.Errorf("expected taskID 0, got %d", taskID4)
	}
}

func TestIsInjectedID(t *testing.T) {
	if IsInjectedID("call_abc123") {
		t.Error("should not match regular ID")
	}
	if !IsInjectedID("call_cpa_inject_deadbeef") {
		t.Error("should match injected ID")
	}
}

func TestShouldInject_OpenAI_Match(t *testing.T) {
	rawJSON := []byte(`{
		"model": "gpt-4",
		"messages": [{"role": "user", "content": "hello"}],
		"tools": [{"type": "function", "function": {"name": "ls", "parameters": {}}}]
	}`)
	rules := []config.ToolCallInjectionRule{
		{Name: "test", Enabled: true, ToolName: "ls", Timing: "before", Arguments: map[string]any{"path": "/"}},
	}
	rule := ShouldInject(rawJSON, rules, "gpt-4", "openai")
	if rule == nil {
		t.Fatal("expected a matching rule")
	}
	if rule.Name != "test" {
		t.Errorf("expected rule name 'test', got %q", rule.Name)
	}
}

func TestShouldInject_Claude_Match(t *testing.T) {
	rawJSON := []byte(`{
		"model": "claude-3-opus",
		"messages": [{"role": "user", "content": "hello"}],
		"tools": [{"name": "ls", "description": "list", "input_schema": {}}]
	}`)
	rules := []config.ToolCallInjectionRule{
		{Name: "test", Enabled: true, ToolName: "ls", Timing: "before", Arguments: map[string]any{"path": "/"}},
	}
	rule := ShouldInject(rawJSON, rules, "claude-3-opus", "claude")
	if rule == nil {
		t.Fatal("expected a matching rule")
	}
}

func TestShouldInject_Disabled(t *testing.T) {
	rawJSON := []byte(`{
		"model": "gpt-4",
		"messages": [],
		"tools": [{"type": "function", "function": {"name": "ls"}}]
	}`)
	rules := []config.ToolCallInjectionRule{
		{Name: "test", Enabled: false, ToolName: "ls"},
	}
	if ShouldInject(rawJSON, rules, "gpt-4", "openai") != nil {
		t.Error("disabled rule should not match")
	}
}

func TestShouldInject_NoTools(t *testing.T) {
	rawJSON := []byte(`{"model": "gpt-4", "messages": []}`)
	rules := []config.ToolCallInjectionRule{
		{Name: "test", Enabled: true, ToolName: "ls"},
	}
	if ShouldInject(rawJSON, rules, "gpt-4", "openai") != nil {
		t.Error("should not match when no tools in request")
	}
}

func TestShouldInject_ModelPattern(t *testing.T) {
	rawJSON := []byte(`{
		"model": "gpt-4",
		"messages": [],
		"tools": [{"type": "function", "function": {"name": "ls"}}]
	}`)
	rules := []config.ToolCallInjectionRule{
		{Name: "test", Enabled: true, ToolName: "ls", ModelPattern: "claude-*"},
	}
	if ShouldInject(rawJSON, rules, "gpt-4", "openai") != nil {
		t.Error("model pattern should not match gpt-4 against claude-*")
	}
	if ShouldInject(rawJSON, rules, "claude-3-opus", "openai") == nil {
		t.Error("model pattern should match claude-3-opus against claude-*")
	}
}

func TestShouldInject_MaxInjections(t *testing.T) {
	// Request with one existing injection
	rawJSON := []byte(`{
		"model": "gpt-4",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "tool_calls": [{"id": "call_cpa_inject_aabbcc", "type": "function", "function": {"name": "ls", "arguments": "{}"}}]},
			{"role": "tool", "tool_call_id": "call_cpa_inject_aabbcc", "content": "result"}
		],
		"tools": [{"type": "function", "function": {"name": "ls"}}]
	}`)
	// Default max=0 (means 1), so should not inject again
	rules := []config.ToolCallInjectionRule{
		{Name: "test", Enabled: true, ToolName: "ls", Arguments: map[string]any{}},
	}
	if ShouldInject(rawJSON, rules, "gpt-4", "openai") != nil {
		t.Error("should not inject again when max injections (1) already reached")
	}

	// With max=2, should inject
	rules[0].MaxInjections = 2
	if ShouldInject(rawJSON, rules, "gpt-4", "openai") == nil {
		t.Error("should inject when max=2 and only 1 existing")
	}
}

func TestMatchModelPattern(t *testing.T) {
	tests := []struct {
		pattern, model string
		want           bool
	}{
		{"*", "gpt-4", true},
		{"gpt-*", "gpt-4", true},
		{"gpt-*", "claude-3", false},
		{"*-pro", "gemini-2.5-pro", true},
		{"claude-*-opus", "claude-3-opus", true},
		{"", "gpt-4", false},
	}
	for _, tt := range tests {
		if got := matchModelPattern(tt.pattern, tt.model); got != tt.want {
			t.Errorf("matchModelPattern(%q, %q) = %v, want %v", tt.pattern, tt.model, got, tt.want)
		}
	}
}
