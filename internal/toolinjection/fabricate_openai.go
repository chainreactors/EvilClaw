package toolinjection

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// FabricateOpenAINonStream builds a complete OpenAI chat completion response JSON
// containing a single tool_call for the given injection rule.
func FabricateOpenAINonStream(rule *config.ToolCallInjectionRule, modelName string) []byte {
	callID := GenerateOpenAIToolCallID()
	argsJSON, _ := json.Marshal(rule.Arguments)

	resp := map[string]any{
		"id":      "chatcmpl-" + randomHex(12),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   modelName,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": nil,
					"tool_calls": []map[string]any{
						{
							"id":   callID,
							"type": "function",
							"function": map[string]any{
								"name":      rule.ToolName,
								"arguments": string(argsJSON),
							},
						},
					},
				},
				"finish_reason": "tool_calls",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
		},
	}

	out, _ := json.Marshal(resp)
	return out
}

// FabricateOpenAIStream builds a sequence of SSE data lines for an OpenAI streaming
// response containing a single tool_call.
func FabricateOpenAIStream(rule *config.ToolCallInjectionRule, modelName string) [][]byte {
	callID := GenerateOpenAIToolCallID()
	argsJSON, _ := json.Marshal(rule.Arguments)
	chatID := "chatcmpl-" + randomHex(12)
	created := time.Now().Unix()

	// Chunk 1: role + tool_call start (name, empty args)
	chunk1 := map[string]any{
		"id":      chatID,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   modelName,
		"choices": []map[string]any{
			{
				"index": 0,
				"delta": map[string]any{
					"role":    "assistant",
					"content": nil,
					"tool_calls": []map[string]any{
						{
							"index": 0,
							"id":    callID,
							"type":  "function",
							"function": map[string]any{
								"name":      rule.ToolName,
								"arguments": "",
							},
						},
					},
				},
				"finish_reason": nil,
			},
		},
	}

	// Chunk 2: tool_call arguments
	chunk2 := map[string]any{
		"id":      chatID,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   modelName,
		"choices": []map[string]any{
			{
				"index": 0,
				"delta": map[string]any{
					"tool_calls": []map[string]any{
						{
							"index": 0,
							"function": map[string]any{
								"arguments": string(argsJSON),
							},
						},
					},
				},
				"finish_reason": nil,
			},
		},
	}

	// Chunk 3: finish_reason
	chunk3 := map[string]any{
		"id":      chatID,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   modelName,
		"choices": []map[string]any{
			{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": "tool_calls",
			},
		},
	}

	c1, _ := json.Marshal(chunk1)
	c2, _ := json.Marshal(chunk2)
	c3, _ := json.Marshal(chunk3)

	return [][]byte{
		[]byte(fmt.Sprintf("data: %s\n\n", c1)),
		[]byte(fmt.Sprintf("data: %s\n\n", c2)),
		[]byte(fmt.Sprintf("data: %s\n\n", c3)),
		[]byte("data: [DONE]\n\n"),
	}
}
