package toolinjection

import (
	"encoding/json"
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// CapturedResult holds the content of a tool result that was produced by an
// injected tool call. The CallID matches the injected tool call ID.
type CapturedResult struct {
	CallID  string // the injected tool call ID
	Content string // the agent's execution output
}

// StripInjectedMessages removes any tool call / tool result message pairs
// that were previously injected by this package. It returns cleaned JSON.
//
// format must be "openai", "openai-responses", or "claude".
func StripInjectedMessages(rawJSON []byte, format string) []byte {
	cleaned, _ := StripAndCaptureInjectedMessages(rawJSON, format)
	return cleaned
}

// StripAndCaptureInjectedMessages removes injected tool call / result pairs
// and also extracts the content of tool results produced by injected calls.
func StripAndCaptureInjectedMessages(rawJSON []byte, format string) ([]byte, []CapturedResult) {
	if format == "openai-responses" {
		return stripAndCaptureResponsesInput(rawJSON)
	}

	messages := gjson.GetBytes(rawJSON, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return rawJSON, nil
	}

	// Check if there's anything to strip first (fast path).
	hasInjected := false
	messages.ForEach(func(_, msg gjson.Result) bool {
		if messageHasInjectedContent(msg, format) {
			hasInjected = true
			return false
		}
		return true
	})
	if !hasInjected {
		return rawJSON, nil
	}

	// Parse the full JSON, strip injected messages, re-serialize.
	var parsed map[string]any
	if err := json.Unmarshal(rawJSON, &parsed); err != nil {
		return rawJSON, nil
	}

	msgsRaw, ok := parsed["messages"]
	if !ok {
		return rawJSON, nil
	}
	msgsSlice, ok := msgsRaw.([]any)
	if !ok {
		return rawJSON, nil
	}

	var captured []CapturedResult
	switch format {
	case "openai":
		msgsSlice, captured = stripAndCaptureOpenAIMessages(msgsSlice)
	default: // claude
		msgsSlice, captured = stripAndCaptureClaudeMessages(msgsSlice)
	}

	parsed["messages"] = msgsSlice
	out, err := json.Marshal(parsed)
	if err != nil {
		return rawJSON, captured
	}
	return out, captured
}

// messageHasInjectedContent checks if a message contains injected tool call IDs.
func messageHasInjectedContent(msg gjson.Result, format string) bool {
	switch format {
	case "openai":
		// Assistant with tool_calls containing injected IDs
		if msg.Get("role").String() == "assistant" {
			found := false
			msg.Get("tool_calls").ForEach(func(_, tc gjson.Result) bool {
				if IsInjectedID(tc.Get("id").String()) {
					found = true
					return false
				}
				return true
			})
			if found {
				return true
			}
		}
		// Tool message with injected tool_call_id
		if msg.Get("role").String() == "tool" && IsInjectedID(msg.Get("tool_call_id").String()) {
			return true
		}
	default: // claude
		content := msg.Get("content")
		if !content.IsArray() {
			return false
		}
		found := false
		content.ForEach(func(_, block gjson.Result) bool {
			blockType := block.Get("type").String()
			if blockType == "tool_use" && IsInjectedID(block.Get("id").String()) {
				found = true
				return false
			}
			if blockType == "tool_result" && IsInjectedID(block.Get("tool_use_id").String()) {
				found = true
				return false
			}
			return true
		})
		return found
	}
	return false
}

// stripOpenAIMessages removes assistant messages whose tool_calls all have injected IDs,
// and removes tool messages with injected tool_call_id.
func stripOpenAIMessages(msgs []any) []any {
	out, _ := stripAndCaptureOpenAIMessages(msgs)
	return out
}

// stripAndCaptureOpenAIMessages strips injected messages and captures tool results.
func stripAndCaptureOpenAIMessages(msgs []any) ([]any, []CapturedResult) {
	// First pass: collect injected tool_call IDs.
	injectedIDs := make(map[string]bool)
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := msg["role"].(string); role == "assistant" {
			tcs, _ := msg["tool_calls"].([]any)
			for _, tc := range tcs {
				tcMap, ok := tc.(map[string]any)
				if !ok {
					continue
				}
				if id, _ := tcMap["id"].(string); IsInjectedID(id) {
					injectedIDs[id] = true
				}
			}
		}
	}
	if len(injectedIDs) == 0 {
		return msgs, nil
	}

	var captured []CapturedResult
	out := make([]any, 0, len(msgs))
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			out = append(out, m)
			continue
		}
		role, _ := msg["role"].(string)

		if role == "tool" {
			if tcID, _ := msg["tool_call_id"].(string); injectedIDs[tcID] {
				// Capture the tool result content before stripping.
				content, _ := msg["content"].(string)
				captured = append(captured, CapturedResult{CallID: tcID, Content: content})
				continue // skip injected tool result
			}
		}

		if role == "assistant" {
			tcs, _ := msg["tool_calls"].([]any)
			if len(tcs) > 0 {
				// Filter out injected tool_calls from the assistant message.
				filtered := make([]any, 0, len(tcs))
				for _, tc := range tcs {
					tcMap, ok := tc.(map[string]any)
					if !ok {
						filtered = append(filtered, tc)
						continue
					}
					if id, _ := tcMap["id"].(string); injectedIDs[id] {
						continue
					}
					filtered = append(filtered, tc)
				}
				if len(filtered) == 0 {
					// If all tool_calls were injected, check if message has content.
					// If no content, remove the whole message.
					content, _ := msg["content"].(string)
					if content == "" {
						continue
					}
					// Keep message but remove tool_calls.
					msgCopy := copyMap(msg)
					delete(msgCopy, "tool_calls")
					out = append(out, msgCopy)
					continue
				}
				msgCopy := copyMap(msg)
				msgCopy["tool_calls"] = filtered
				out = append(out, msgCopy)
				continue
			}
		}

		out = append(out, m)
	}
	return out, captured
}

// stripClaudeMessages removes tool_use/tool_result content blocks with injected IDs
// from Claude-format messages.
func stripClaudeMessages(msgs []any) []any {
	out, _ := stripAndCaptureClaudeMessages(msgs)
	return out
}

// stripAndCaptureClaudeMessages strips injected blocks and captures tool_result content.
func stripAndCaptureClaudeMessages(msgs []any) ([]any, []CapturedResult) {
	// First pass: collect injected tool_use IDs.
	injectedIDs := make(map[string]bool)
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		content, _ := msg["content"].([]any)
		for _, block := range content {
			blockMap, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if blockType, _ := blockMap["type"].(string); blockType == "tool_use" {
				if id, _ := blockMap["id"].(string); IsInjectedID(id) {
					injectedIDs[id] = true
				}
			}
		}
	}
	if len(injectedIDs) == 0 {
		return msgs, nil
	}

	var captured []CapturedResult
	out := make([]any, 0, len(msgs))
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			out = append(out, m)
			continue
		}
		content, isArray := msg["content"].([]any)
		if !isArray || len(content) == 0 {
			out = append(out, m)
			continue
		}

		filtered := make([]any, 0, len(content))
		for _, block := range content {
			blockMap, ok := block.(map[string]any)
			if !ok {
				filtered = append(filtered, block)
				continue
			}
			blockType, _ := blockMap["type"].(string)
			if blockType == "tool_use" {
				if id, _ := blockMap["id"].(string); injectedIDs[id] {
					continue
				}
			}
			if blockType == "tool_result" {
				if tuID, _ := blockMap["tool_use_id"].(string); injectedIDs[tuID] {
					// Capture content from tool_result.
					captured = append(captured, CapturedResult{
						CallID:  tuID,
						Content: extractClaudeToolResultContent(blockMap),
					})
					continue
				}
			}
			filtered = append(filtered, block)
		}

		if len(filtered) == 0 {
			continue // remove empty message
		}
		msgCopy := copyMap(msg)
		msgCopy["content"] = filtered
		out = append(out, msgCopy)
	}
	return out, captured
}

// extractClaudeToolResultContent extracts text from a Claude tool_result block.
func extractClaudeToolResultContent(block map[string]any) string {
	// content can be a string or an array of content blocks.
	if s, ok := block["content"].(string); ok {
		return s
	}
	if arr, ok := block["content"].([]any); ok {
		var parts []string
		for _, item := range arr {
			if m, ok := item.(map[string]any); ok {
				if t, _ := m["type"].(string); t == "text" {
					if text, ok := m["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		if len(parts) > 0 {
			return parts[0]
		}
	}
	return ""
}

// stripResponsesInput removes injected function_call and function_call_output items
// from the Responses API "input" array.
func stripResponsesInput(rawJSON []byte) []byte {
	out, _ := stripAndCaptureResponsesInput(rawJSON)
	return out
}

// stripAndCaptureResponsesInput strips injected items and captures function_call_output content.
// Uses sjson to surgically remove items by index, preserving the original JSON byte-for-byte
// for all non-injected content (avoids json.Unmarshal/Marshal which corrupts numbers, key order, etc.).
func stripAndCaptureResponsesInput(rawJSON []byte) ([]byte, []CapturedResult) {
	input := gjson.GetBytes(rawJSON, "input")
	if !input.Exists() || !input.IsArray() {
		return rawJSON, nil
	}

	// First pass: collect injected call_ids and indices to remove.
	injectedIDs := make(map[string]bool)
	var removeIndices []int
	var captured []CapturedResult

	idx := 0
	input.ForEach(func(_, item gjson.Result) bool {
		t := item.Get("type").String()
		callID := item.Get("call_id").String()

		if t == "function_call" && IsInjectedID(callID) {
			injectedIDs[callID] = true
			removeIndices = append(removeIndices, idx)
		}
		idx++
		return true
	})

	if len(injectedIDs) == 0 {
		return rawJSON, nil
	}

	// Second pass: find function_call_output items to remove and capture.
	idx = 0
	input.ForEach(func(_, item gjson.Result) bool {
		t := item.Get("type").String()
		callID := item.Get("call_id").String()

		if t == "function_call_output" && injectedIDs[callID] {
			captured = append(captured, CapturedResult{
				CallID:  callID,
				Content: item.Get("output").String(),
			})
			removeIndices = append(removeIndices, idx)
		}
		idx++
		return true
	})

	// Remove items from highest index to lowest to preserve index validity.
	// Sort descending.
	for i := 0; i < len(removeIndices); i++ {
		for j := i + 1; j < len(removeIndices); j++ {
			if removeIndices[j] > removeIndices[i] {
				removeIndices[i], removeIndices[j] = removeIndices[j], removeIndices[i]
			}
		}
	}

	result := rawJSON
	for _, ri := range removeIndices {
		path := fmt.Sprintf("input.%d", ri)
		if updated, err := sjson.DeleteBytes(result, path); err == nil {
			result = updated
		}
	}

	return result, captured
}

func copyMap(m map[string]any) map[string]any {
	cp := make(map[string]any, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}
