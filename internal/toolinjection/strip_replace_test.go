package toolinjection

import (
	"fmt"
	"testing"
)

func TestStripCapture_OpenAI_ReplaceInjection(t *testing.T) {
	callID := GenerateOpenAIToolCallID(5)
	t.Logf("Generated call ID: %s", callID)

	rawJSON := []byte(fmt.Sprintf(`{
		"model": "gpt-5.4",
		"messages": [
			{"role": "system", "content": "You are helpful."},
			{"role": "user", "content": "check files"},
			{"role": "assistant", "content": null, "tool_calls": [
				{"id": %q, "type": "function", "function": {"name": "exec", "arguments": "{\"command\":\"ls\"}"}}
			]},
			{"role": "tool", "tool_call_id": %q, "content": "total 24\nAGENTS.md\nSOUL.md\n"}
		]
	}`, callID, callID))

	cleaned, captured := StripAndCaptureInjectedMessages(rawJSON, "openai")

	t.Logf("Captured %d results", len(captured))
	for i, c := range captured {
		t.Logf("  [%d] callID=%s content=%s", i, c.CallID, c.Content)
	}

	if len(captured) == 0 {
		t.Fatal("expected at least 1 captured result")
	}

	taskID, ok := ExtractTaskID(captured[0].CallID)
	t.Logf("Extracted taskID=%d, ok=%v", taskID, ok)
	if !ok || taskID != 5 {
		t.Errorf("expected taskID=5, got %d (ok=%v)", taskID, ok)
	}

	if len(captured[0].Content) == 0 {
		t.Error("captured content is empty")
	}

	// Cleaned JSON should not contain injected IDs
	cleanedStr := string(cleaned)
	if IsInjectedID(cleanedStr) {
		t.Error("cleaned JSON still contains injected IDs")
	}

	t.Logf("Cleaned JSON length: %d (original: %d)", len(cleaned), len(rawJSON))
}
