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
	switch format {
	case "openai":
		return openAIHasToolCalls(buf)
	case "claude":
		return claudeHasToolCalls(buf)
	case "openai-responses":
		return responsesHasToolCalls(buf)
	default:
		return false
	}
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
