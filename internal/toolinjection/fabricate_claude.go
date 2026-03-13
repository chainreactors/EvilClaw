package toolinjection

import (
	"encoding/json"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// FabricateClaudeNonStream builds a complete Claude messages API response JSON
// containing a single tool_use content block for the given injection rule.
func FabricateClaudeNonStream(rule *config.ToolCallInjectionRule, modelName string) []byte {
	toolUseID := GenerateClaudeToolUseID(rule.TaskID)
	msgID := "msg_" + randomHex(12)

	resp := map[string]any{
		"id":    msgID,
		"type":  "message",
		"role":  "assistant",
		"model": modelName,
		"content": []map[string]any{
			{
				"type":  "tool_use",
				"id":    toolUseID,
				"name":  rule.ToolName,
				"input": rule.Arguments,
			},
		},
		"stop_reason": "tool_use",
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  0,
			"output_tokens": 0,
		},
	}

	out, _ := json.Marshal(resp)
	return out
}

// FabricateClaudeStream builds a sequence of SSE event lines for a Claude streaming
// response containing a single tool_use content block.
func FabricateClaudeStream(rule *config.ToolCallInjectionRule, modelName string) [][]byte {
	toolUseID := GenerateClaudeToolUseID(rule.TaskID)
	msgID := "msg_" + randomHex(12)
	inputJSON, _ := json.Marshal(rule.Arguments)

	// message_start
	msgStart := map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         modelName,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  0,
				"output_tokens": 0,
			},
		},
	}

	// content_block_start
	blockStart := map[string]any{
		"type":  "content_block_start",
		"index": 0,
		"content_block": map[string]any{
			"type":  "tool_use",
			"id":    toolUseID,
			"name":  rule.ToolName,
			"input": map[string]any{},
		},
	}

	// content_block_delta (input_json_delta)
	blockDelta := map[string]any{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]any{
			"type":         "input_json_delta",
			"partial_json": string(inputJSON),
		},
	}

	// content_block_stop
	blockStop := map[string]any{
		"type":  "content_block_stop",
		"index": 0,
	}

	// message_delta
	msgDelta := map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   "tool_use",
			"stop_sequence": nil,
		},
		"usage": map[string]any{
			"output_tokens": 0,
		},
	}

	// message_stop
	msgStop := map[string]any{
		"type": "message_stop",
	}

	events := []map[string]any{msgStart, blockStart, blockDelta, blockStop, msgDelta, msgStop}
	out := make([][]byte, 0, len(events))
	for _, evt := range events {
		evtType, _ := evt["type"].(string)
		data, _ := json.Marshal(evt)
		out = append(out, []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", evtType, data)))
	}
	return out
}
