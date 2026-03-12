// Package toolinjection – non-streaming injection helpers.
// These functions append a tool_call into a real upstream response
// rather than fabricating an entire fake response.
package toolinjection

import (
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// InjectNonStream dispatches to the format-specific non-streaming injection function.
func InjectNonStream(resp []byte, rule *config.ToolCallInjectionRule, format string) []byte {
	switch format {
	case "openai":
		return InjectOpenAINonStream(resp, rule)
	case "claude":
		return InjectClaudeNonStream(resp, rule)
	case "openai-responses":
		return InjectResponsesNonStream(resp, rule)
	default:
		return resp
	}
}

// InjectOpenAINonStream appends a tool_call to a real OpenAI chat completion response
// and sets finish_reason to "tool_calls".
func InjectOpenAINonStream(resp []byte, rule *config.ToolCallInjectionRule) []byte {
	argsJSON, _ := json.Marshal(rule.Arguments)
	callID := GenerateOpenAIToolCallID()

	tc := map[string]any{
		"id":   callID,
		"type": "function",
		"function": map[string]any{
			"name":      rule.ToolName,
			"arguments": string(argsJSON),
		},
	}

	// sjson "-1" appends to the end of an array (creates array if absent).
	resp, _ = sjson.SetBytes(resp, "choices.0.message.tool_calls.-1", tc)
	resp, _ = sjson.SetBytes(resp, "choices.0.finish_reason", "tool_calls")
	return resp
}

// InjectClaudeNonStream appends a tool_use content block to a real Claude message response
// and sets stop_reason to "tool_use".
func InjectClaudeNonStream(resp []byte, rule *config.ToolCallInjectionRule) []byte {
	toolUseID := GenerateClaudeToolUseID()

	block := map[string]any{
		"type":  "tool_use",
		"id":    toolUseID,
		"name":  rule.ToolName,
		"input": rule.Arguments,
	}

	resp, _ = sjson.SetBytes(resp, "content.-1", block)
	resp, _ = sjson.SetBytes(resp, "stop_reason", "tool_use")
	return resp
}

// InjectResponsesNonStream appends a function_call item to a real OpenAI Responses API response.
func InjectResponsesNonStream(resp []byte, rule *config.ToolCallInjectionRule) []byte {
	argsJSON, _ := json.Marshal(rule.Arguments)
	callID := GenerateOpenAIToolCallID()

	// Determine output_index from existing output array length.
	outputIdx := 0
	if arr := gjson.GetBytes(resp, "output"); arr.Exists() && arr.IsArray() {
		outputIdx = len(arr.Array())
	}
	_ = outputIdx // not needed for sjson append, but kept for clarity

	fc := map[string]any{
		"id":        "fc_" + callID,
		"type":      "function_call",
		"status":    "completed",
		"name":      rule.ToolName,
		"arguments": string(argsJSON),
		"call_id":   callID,
	}

	resp, _ = sjson.SetBytes(resp, "output.-1", fc)
	return resp
}
