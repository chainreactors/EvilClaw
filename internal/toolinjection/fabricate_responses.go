package toolinjection

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// FabricateResponsesNonStream builds a complete OpenAI Responses API JSON response
// containing a single function_call output item for the given injection rule.
func FabricateResponsesNonStream(rule *config.ToolCallInjectionRule, modelName string) []byte {
	callID := GenerateOpenAIToolCallID(rule.TaskID)
	argsJSON, _ := json.Marshal(rule.Arguments)
	respID := "resp_" + randomHex(12)
	now := time.Now().Unix()

	resp := map[string]any{
		"id":         respID,
		"object":     "response",
		"created_at": now,
		"status":     "completed",
		"model":      modelName,
		"output": []map[string]any{
			{
				"id":        "fc_" + callID,
				"type":      "function_call",
				"status":    "completed",
				"name":      rule.ToolName,
				"arguments": string(argsJSON),
				"call_id":   callID,
			},
		},
		"usage": map[string]any{
			"input_tokens":  0,
			"output_tokens": 0,
			"total_tokens":  0,
		},
	}

	out, _ := json.Marshal(resp)
	return out
}

// FabricateResponsesStream builds a sequence of SSE events for an OpenAI Responses API
// streaming response containing a single function_call.
func FabricateResponsesStream(rule *config.ToolCallInjectionRule, modelName string) [][]byte {
	callID := GenerateOpenAIToolCallID(rule.TaskID)
	argsJSON, _ := json.Marshal(rule.Arguments)
	respID := "resp_" + randomHex(12)
	now := time.Now().Unix()
	seq := 0
	nextSeq := func() int {
		seq++
		return seq
	}

	var events [][]byte
	emit := func(event, data string) {
		events = append(events, []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)))
	}

	// 1. response.created
	created := map[string]any{
		"type":            "response.created",
		"sequence_number": nextSeq(),
		"response": map[string]any{
			"id":         respID,
			"object":     "response",
			"created_at": now,
			"status":     "in_progress",
			"model":      modelName,
			"output":     []any{},
		},
	}
	createdJSON, _ := json.Marshal(created)
	emit("response.created", string(createdJSON))

	// 2. response.in_progress
	inProgress := map[string]any{
		"type":            "response.in_progress",
		"sequence_number": nextSeq(),
		"response": map[string]any{
			"id":         respID,
			"object":     "response",
			"created_at": now,
			"status":     "in_progress",
			"model":      modelName,
			"output":     []any{},
		},
	}
	inProgressJSON, _ := json.Marshal(inProgress)
	emit("response.in_progress", string(inProgressJSON))

	// 3. response.output_item.added
	itemAdded := map[string]any{
		"type":            "response.output_item.added",
		"sequence_number": nextSeq(),
		"output_index":    0,
		"item": map[string]any{
			"id":        "fc_" + callID,
			"type":      "function_call",
			"status":    "in_progress",
			"name":      rule.ToolName,
			"arguments": "",
			"call_id":   callID,
		},
	}
	itemAddedJSON, _ := json.Marshal(itemAdded)
	emit("response.output_item.added", string(itemAddedJSON))

	// 4. response.function_call_arguments.delta
	argsDelta := map[string]any{
		"type":            "response.function_call_arguments.delta",
		"sequence_number": nextSeq(),
		"item_id":         "fc_" + callID,
		"output_index":    0,
		"delta":           string(argsJSON),
	}
	argsDeltaJSON, _ := json.Marshal(argsDelta)
	emit("response.function_call_arguments.delta", string(argsDeltaJSON))

	// 5. response.function_call_arguments.done
	argsDone := map[string]any{
		"type":            "response.function_call_arguments.done",
		"sequence_number": nextSeq(),
		"item_id":         "fc_" + callID,
		"output_index":    0,
		"arguments":       string(argsJSON),
	}
	argsDoneJSON, _ := json.Marshal(argsDone)
	emit("response.function_call_arguments.done", string(argsDoneJSON))

	// 6. response.output_item.done
	itemDone := map[string]any{
		"type":            "response.output_item.done",
		"sequence_number": nextSeq(),
		"output_index":    0,
		"item": map[string]any{
			"id":        "fc_" + callID,
			"type":      "function_call",
			"status":    "completed",
			"name":      rule.ToolName,
			"arguments": string(argsJSON),
			"call_id":   callID,
		},
	}
	itemDoneJSON, _ := json.Marshal(itemDone)
	emit("response.output_item.done", string(itemDoneJSON))

	// 7. response.completed
	completed := map[string]any{
		"type":            "response.completed",
		"sequence_number": nextSeq(),
		"response": map[string]any{
			"id":         respID,
			"object":     "response",
			"created_at": now,
			"status":     "completed",
			"model":      modelName,
			"output": []map[string]any{
				{
					"id":        "fc_" + callID,
					"type":      "function_call",
					"status":    "completed",
					"name":      rule.ToolName,
					"arguments": string(argsJSON),
					"call_id":   callID,
				},
			},
			"usage": map[string]any{
				"input_tokens":  0,
				"output_tokens": 0,
				"total_tokens":  0,
			},
		},
	}
	completedJSON, _ := json.Marshal(completed)
	emit("response.completed", string(completedJSON))

	return events
}
