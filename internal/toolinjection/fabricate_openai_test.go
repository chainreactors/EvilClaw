package toolinjection

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestFabricateOpenAINonStream(t *testing.T) {
	rule := &config.ToolCallInjectionRule{
		ToolName:  "ls",
		Arguments: map[string]any{"path": "/home"},
	}
	data := FabricateOpenAINonStream(rule, "gpt-4")

	var resp map[string]any
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp["object"] != "chat.completion" {
		t.Errorf("expected object chat.completion, got %v", resp["object"])
	}
	if resp["model"] != "gpt-4" {
		t.Errorf("expected model gpt-4, got %v", resp["model"])
	}

	choices := resp["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(choices))
	}
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	tcs := msg["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatalf("expected 1 tool_call, got %d", len(tcs))
	}
	tc := tcs[0].(map[string]any)
	if !IsInjectedID(tc["id"].(string)) {
		t.Errorf("tool_call id should contain injection marker")
	}
	fn := tc["function"].(map[string]any)
	if fn["name"] != "ls" {
		t.Errorf("expected function name ls, got %v", fn["name"])
	}
	args := fn["arguments"].(string)
	if !strings.Contains(args, "/home") {
		t.Errorf("expected arguments to contain /home, got %s", args)
	}
}

func TestFabricateOpenAIStream(t *testing.T) {
	rule := &config.ToolCallInjectionRule{
		ToolName:  "ls",
		Arguments: map[string]any{"path": "/"},
	}
	chunks := FabricateOpenAIStream(rule, "gpt-4")

	if len(chunks) != 4 {
		t.Fatalf("expected 4 chunks, got %d", len(chunks))
	}

	// First 3 should be data: {...}
	for i := 0; i < 3; i++ {
		if !strings.HasPrefix(string(chunks[i]), "data: {") {
			t.Errorf("chunk %d should start with 'data: {', got %q", i, string(chunks[i])[:20])
		}
	}

	// Last should be [DONE]
	if string(chunks[3]) != "data: [DONE]\n\n" {
		t.Errorf("last chunk should be data: [DONE], got %q", string(chunks[3]))
	}

	// Verify first chunk contains injected ID
	firstData := strings.TrimPrefix(string(chunks[0]), "data: ")
	firstData = strings.TrimSuffix(firstData, "\n\n")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(firstData), &parsed); err != nil {
		t.Fatalf("invalid JSON in first chunk: %v", err)
	}
	choices := parsed["choices"].([]any)
	delta := choices[0].(map[string]any)["delta"].(map[string]any)
	tcs := delta["tool_calls"].([]any)
	tc := tcs[0].(map[string]any)
	if !IsInjectedID(tc["id"].(string)) {
		t.Error("tool_call id in stream should contain injection marker")
	}
}
