package observedtools

import (
	"testing"
)

func TestRecord_Claude(t *testing.T) {
	s := NewStore()
	rawJSON := []byte(`{
		"model": "claude-3-opus",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [
			{"name": "Bash", "description": "Run bash", "input_schema": {"type": "object", "properties": {"command": {"type": "string"}}}},
			{"name": "Read", "description": "Read file", "input_schema": {"type": "object", "properties": {"path": {"type": "string"}}}}
		]
	}`)

	s.Record(rawJSON, "claude")
	tools := s.List()

	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}

	// Sorted by name
	if tools[0].Name != "Bash" || tools[0].Format != "claude" {
		t.Errorf("unexpected tool[0]: %+v", tools[0])
	}
	if tools[1].Name != "Read" || tools[1].Format != "claude" {
		t.Errorf("unexpected tool[1]: %+v", tools[1])
	}
	if tools[0].Schema == nil {
		t.Error("expected schema for Bash tool")
	}
}

func TestRecord_OpenAI(t *testing.T) {
	s := NewStore()
	rawJSON := []byte(`{
		"model": "gpt-4",
		"messages": [],
		"tools": [
			{"type": "function", "function": {"name": "ls", "parameters": {"type": "object", "properties": {"path": {"type": "string"}}}}}
		]
	}`)

	s.Record(rawJSON, "openai")
	tools := s.List()

	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name != "ls" || tools[0].Format != "openai" {
		t.Errorf("unexpected tool: %+v", tools[0])
	}
}

func TestRecord_NoTools(t *testing.T) {
	s := NewStore()
	s.Record([]byte(`{"model": "gpt-4", "messages": []}`), "openai")
	if tools := s.List(); len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
}

func TestRecord_Dedup(t *testing.T) {
	s := NewStore()
	rawJSON := []byte(`{
		"tools": [{"name": "Bash", "input_schema": {"type": "object"}}]
	}`)
	s.Record(rawJSON, "claude")
	s.Record(rawJSON, "claude")

	if tools := s.List(); len(tools) != 1 {
		t.Errorf("expected 1 tool after dedup, got %d", len(tools))
	}
}

func TestClear(t *testing.T) {
	s := NewStore()
	s.Record([]byte(`{"tools": [{"name": "Bash", "input_schema": {}}]}`), "claude")
	s.Clear()
	if tools := s.List(); len(tools) != 0 {
		t.Errorf("expected 0 tools after clear, got %d", len(tools))
	}
}

func TestGlobal(t *testing.T) {
	g := Global()
	if g == nil {
		t.Fatal("Global() returned nil")
	}
	if g != Global() {
		t.Error("Global() should return the same instance")
	}
}
