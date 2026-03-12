package toolinjection

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestFabricateClaudeNonStream(t *testing.T) {
	rule := &config.ToolCallInjectionRule{
		ToolName:  "ls",
		Arguments: map[string]any{"path": "/home"},
	}
	data := FabricateClaudeNonStream(rule, "claude-3-opus")

	var resp map[string]any
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp["type"] != "message" {
		t.Errorf("expected type message, got %v", resp["type"])
	}
	if resp["role"] != "assistant" {
		t.Errorf("expected role assistant, got %v", resp["role"])
	}
	if resp["model"] != "claude-3-opus" {
		t.Errorf("expected model claude-3-opus, got %v", resp["model"])
	}
	if resp["stop_reason"] != "tool_use" {
		t.Errorf("expected stop_reason tool_use, got %v", resp["stop_reason"])
	}

	content := resp["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(content))
	}
	block := content[0].(map[string]any)
	if block["type"] != "tool_use" {
		t.Errorf("expected content block type tool_use, got %v", block["type"])
	}
	if !IsInjectedID(block["id"].(string)) {
		t.Error("tool_use id should contain injection marker")
	}
	if block["name"] != "ls" {
		t.Errorf("expected name ls, got %v", block["name"])
	}
	input := block["input"].(map[string]any)
	if input["path"] != "/home" {
		t.Errorf("expected input.path /home, got %v", input["path"])
	}
}

func TestFabricateClaudeStream(t *testing.T) {
	rule := &config.ToolCallInjectionRule{
		ToolName:  "ls",
		Arguments: map[string]any{"path": "/"},
	}
	events := FabricateClaudeStream(rule, "claude-3-opus")

	if len(events) != 6 {
		t.Fatalf("expected 6 events, got %d", len(events))
	}

	expectedEventTypes := []string{
		"message_start", "content_block_start", "content_block_delta",
		"content_block_stop", "message_delta", "message_stop",
	}
	for i, evt := range events {
		s := string(evt)
		if !strings.HasPrefix(s, "event: "+expectedEventTypes[i]) {
			t.Errorf("event %d: expected type %s, got %q", i, expectedEventTypes[i], s[:min(40, len(s))])
		}
		// Extract data portion and verify it's valid JSON
		lines := strings.SplitN(s, "\n", 3)
		if len(lines) < 2 {
			t.Errorf("event %d: expected at least 2 lines", i)
			continue
		}
		dataLine := strings.TrimPrefix(lines[1], "data: ")
		var parsed map[string]any
		if err := json.Unmarshal([]byte(dataLine), &parsed); err != nil {
			t.Errorf("event %d: invalid JSON: %v", i, err)
		}
	}

	// Verify content_block_start contains injected ID
	blockStartData := strings.TrimPrefix(strings.SplitN(string(events[1]), "\n", 3)[1], "data: ")
	var blockStart map[string]any
	_ = json.Unmarshal([]byte(blockStartData), &blockStart)
	cb := blockStart["content_block"].(map[string]any)
	if !IsInjectedID(cb["id"].(string)) {
		t.Error("content_block id should contain injection marker")
	}
}
