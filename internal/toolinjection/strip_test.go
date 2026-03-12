package toolinjection

import (
	"encoding/json"
	"testing"
)

func TestStripInjectedMessages_OpenAI(t *testing.T) {
	rawJSON := []byte(`{
		"model": "gpt-4",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "tool_calls": [{"id": "call_cpa_inject_aabb", "type": "function", "function": {"name": "ls", "arguments": "{}"}}]},
			{"role": "tool", "tool_call_id": "call_cpa_inject_aabb", "content": "file1.txt"},
			{"role": "user", "content": "now continue"}
		]
	}`)

	stripped := StripInjectedMessages(rawJSON, "openai")

	var parsed map[string]any
	if err := json.Unmarshal(stripped, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	msgs := parsed["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages after stripping, got %d", len(msgs))
	}
	// First message should be user "hello"
	msg0 := msgs[0].(map[string]any)
	if msg0["role"] != "user" || msg0["content"] != "hello" {
		t.Errorf("unexpected first message: %v", msg0)
	}
	// Second message should be user "now continue"
	msg1 := msgs[1].(map[string]any)
	if msg1["role"] != "user" || msg1["content"] != "now continue" {
		t.Errorf("unexpected second message: %v", msg1)
	}
}

func TestStripInjectedMessages_OpenAI_MixedToolCalls(t *testing.T) {
	rawJSON := []byte(`{
		"model": "gpt-4",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "tool_calls": [
				{"id": "call_cpa_inject_aabb", "type": "function", "function": {"name": "ls", "arguments": "{}"}},
				{"id": "call_real_1234", "type": "function", "function": {"name": "search", "arguments": "{}"}}
			]},
			{"role": "tool", "tool_call_id": "call_cpa_inject_aabb", "content": "injected result"},
			{"role": "tool", "tool_call_id": "call_real_1234", "content": "real result"}
		]
	}`)

	stripped := StripInjectedMessages(rawJSON, "openai")

	var parsed map[string]any
	if err := json.Unmarshal(stripped, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	msgs := parsed["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	// The assistant message should only have the real tool_call
	assistant := msgs[1].(map[string]any)
	tcs := assistant["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatalf("expected 1 tool_call remaining, got %d", len(tcs))
	}
	tc := tcs[0].(map[string]any)
	if tc["id"] != "call_real_1234" {
		t.Errorf("expected real tool_call to remain, got id %v", tc["id"])
	}
}

func TestStripInjectedMessages_Claude(t *testing.T) {
	rawJSON := []byte(`{
		"model": "claude-3-opus",
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "hello"}]},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "toolu_cpa_inject_aabb", "name": "ls", "input": {}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_cpa_inject_aabb", "content": "file1.txt"}]},
			{"role": "user", "content": [{"type": "text", "text": "now continue"}]}
		]
	}`)

	stripped := StripInjectedMessages(rawJSON, "claude")

	var parsed map[string]any
	if err := json.Unmarshal(stripped, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	msgs := parsed["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages after stripping, got %d", len(msgs))
	}
}

func TestStripInjectedMessages_NoInjected(t *testing.T) {
	rawJSON := []byte(`{
		"model": "gpt-4",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": "world"}
		]
	}`)

	stripped := StripInjectedMessages(rawJSON, "openai")
	// Should return unchanged (possibly re-serialized but semantically equal)
	var orig, result map[string]any
	_ = json.Unmarshal(rawJSON, &orig)
	_ = json.Unmarshal(stripped, &result)

	origMsgs := orig["messages"].([]any)
	resultMsgs := result["messages"].([]any)
	if len(origMsgs) != len(resultMsgs) {
		t.Errorf("message count changed: %d -> %d", len(origMsgs), len(resultMsgs))
	}
}
