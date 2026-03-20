package toolinjection

import (
	"bytes"

	"github.com/tidwall/gjson"
)

// ResponseHasToolCalls reports whether a response body (streaming buffer or
// non-streaming JSON) contains tool/function calls that require agent execution.
// When true, this is an intermediate response in a multi-turn conversation and
// NOT the final text answer — so CompletePoisonCycle should be deferred.
func ResponseHasToolCalls(buf []byte, format string) bool {
	f := GetFormat(format)
	if f == nil {
		return false
	}
	return f.HasToolCalls(buf)
}

// openAIHasToolCalls checks OpenAI Chat Completions format.
// Non-streaming: single JSON with choices[0].finish_reason.
// Streaming: concatenated raw JSON chunks; the last chunk has finish_reason.
func openAIHasToolCalls(buf []byte) bool {
	return bytes.Contains(buf, []byte(`"finish_reason":"tool_calls"`)) ||
		bytes.Contains(buf, []byte(`"finish_reason": "tool_calls"`))
}

// claudeHasToolCalls checks Claude Messages format.
// Non-streaming: single JSON with stop_reason.
// Streaming: SSE events; message_delta event has delta.stop_reason.
func claudeHasToolCalls(buf []byte) bool {
	return bytes.Contains(buf, []byte(`"stop_reason":"tool_use"`)) ||
		bytes.Contains(buf, []byte(`"stop_reason": "tool_use"`))
}

// responsesHasToolCalls checks OpenAI Responses API format.
// Looks for function_call items in the output array.
func responsesHasToolCalls(buf []byte) bool {
	// Try direct parsing first (non-streaming JSON).
	output := gjson.GetBytes(buf, "output")
	if output.Exists() && output.IsArray() {
		return outputArrayHasFunctionCall(output)
	}

	// For streaming: find the response.completed event and parse its output.
	idx := bytes.LastIndex(buf, []byte("event: response.completed"))
	if idx < 0 {
		return false
	}
	rest := buf[idx:]
	dataIdx := bytes.Index(rest, []byte("data: "))
	if dataIdx < 0 {
		return false
	}
	data := rest[dataIdx+6:]
	endIdx := bytes.Index(data, []byte("\n"))
	if endIdx > 0 {
		data = data[:endIdx]
	}
	if len(data) == 0 || data[0] != '{' {
		return false
	}
	output = gjson.GetBytes(data, "response.output")
	if !output.Exists() || !output.IsArray() {
		return false
	}
	return outputArrayHasFunctionCall(output)
}

func outputArrayHasFunctionCall(output gjson.Result) bool {
	found := false
	output.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "function_call" {
			found = true
			return false
		}
		return true
	})
	return found
}

// ResponseHasNonInjectedToolCalls is like ResponseHasToolCalls but ignores
// tool calls that were injected by this package (identified by the cpa_inject_ marker).
// This prevents injected tool calls from blocking CompletePoisonCycle.
func ResponseHasNonInjectedToolCalls(buf []byte, format string) bool {
	if !ResponseHasToolCalls(buf, format) {
		return false
	}
	// Response has tool calls — check if ALL of them are injected.
	return !allToolCallIDsAreInjected(buf, format)
}

// allToolCallIDsAreInjected scans the buffer for tool call IDs and returns true
// only if every found ID contains the injection marker.
func allToolCallIDsAreInjected(buf []byte, format string) bool {
	f := GetFormat(format)
	if f == nil {
		return false
	}
	ids := f.ExtractToolCallIDs(buf)

	if len(ids) == 0 {
		// Can't determine IDs — assume real tool calls to be safe.
		return false
	}
	for _, id := range ids {
		if !IsInjectedID(id) {
			return false
		}
	}
	return true
}

// extractAllOpenAIToolCallIDs finds all tool_call IDs in an OpenAI response buffer
// (handles both non-streaming JSON and accumulated raw JSON lines / SSE).
func extractAllOpenAIToolCallIDs(buf []byte) []string {
	var ids []string
	seen := make(map[string]bool)

	// Scan each line for tool_calls with IDs.
	lines := bytes.Split(buf, []byte("\n"))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		// Strip SSE "data: " prefix if present.
		data := line
		if bytes.HasPrefix(data, []byte("data:")) {
			data = bytes.TrimPrefix(data, []byte("data: "))
			data = bytes.TrimPrefix(data, []byte("data:"))
			data = bytes.TrimSpace(data)
		}
		if len(data) == 0 || data[0] != '{' {
			continue
		}

		// Check non-streaming: choices[0].message.tool_calls
		gjson.GetBytes(data, "choices.0.message.tool_calls").ForEach(func(_, tc gjson.Result) bool {
			if id := tc.Get("id").String(); id != "" && !seen[id] {
				ids = append(ids, id)
				seen[id] = true
			}
			return true
		})
		// Check streaming: choices[0].delta.tool_calls
		gjson.GetBytes(data, "choices.0.delta.tool_calls").ForEach(func(_, tc gjson.Result) bool {
			if id := tc.Get("id").String(); id != "" && !seen[id] {
				ids = append(ids, id)
				seen[id] = true
			}
			return true
		})
	}
	return ids
}

// extractAllClaudeToolUseIDs finds tool_use IDs in a Claude response buffer.
func extractAllClaudeToolUseIDs(buf []byte) []string {
	var ids []string
	seen := make(map[string]bool)

	lines := bytes.Split(buf, []byte("\n"))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		data := line
		if bytes.HasPrefix(data, []byte("data:")) {
			data = bytes.TrimPrefix(data, []byte("data: "))
			data = bytes.TrimPrefix(data, []byte("data:"))
			data = bytes.TrimSpace(data)
		}
		if len(data) == 0 || data[0] != '{' {
			continue
		}
		// Non-streaming: content[].type=="tool_use"
		gjson.GetBytes(data, "content").ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "tool_use" {
				if id := block.Get("id").String(); id != "" && !seen[id] {
					ids = append(ids, id)
					seen[id] = true
				}
			}
			return true
		})
		// Streaming: content_block_start with tool_use
		if gjson.GetBytes(data, "type").String() == "content_block_start" {
			cb := gjson.GetBytes(data, "content_block")
			if cb.Get("type").String() == "tool_use" {
				if id := cb.Get("id").String(); id != "" && !seen[id] {
					ids = append(ids, id)
					seen[id] = true
				}
			}
		}
	}
	return ids
}

// extractAllResponsesCallIDs finds function_call call_ids in a Responses API buffer.
func extractAllResponsesCallIDs(buf []byte) []string {
	var ids []string
	seen := make(map[string]bool)

	lines := bytes.Split(buf, []byte("\n"))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		data := line
		if bytes.HasPrefix(data, []byte("data:")) {
			data = bytes.TrimPrefix(data, []byte("data: "))
			data = bytes.TrimPrefix(data, []byte("data:"))
			data = bytes.TrimSpace(data)
		}
		if len(data) == 0 || data[0] != '{' {
			continue
		}
		// Non-streaming: output[].type=="function_call"
		gjson.GetBytes(data, "output").ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() == "function_call" {
				if id := item.Get("call_id").String(); id != "" && !seen[id] {
					ids = append(ids, id)
					seen[id] = true
				}
			}
			return true
		})
		// Streaming: response.output_item.added
		if gjson.GetBytes(data, "item.type").String() == "function_call" {
			if id := gjson.GetBytes(data, "item.call_id").String(); id != "" && !seen[id] {
				ids = append(ids, id)
				seen[id] = true
			}
		}
	}
	return ids
}
