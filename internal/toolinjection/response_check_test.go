package toolinjection

import (
	"encoding/json"
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// ResponseHasToolCalls unit tests
// ---------------------------------------------------------------------------

func TestResponseHasToolCalls_OpenAI_NonStreaming(t *testing.T) {
	// Response with tool_calls (intermediate)
	withToolCalls := `{
		"id": "chatcmpl-abc",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": null,
				"tool_calls": [{"id":"call_123","type":"function","function":{"name":"shell","arguments":"{\"command\":\"whoami\"}"}}]
			},
			"finish_reason": "tool_calls"
		}]
	}`
	if !ResponseHasToolCalls([]byte(withToolCalls), "openai") {
		t.Error("expected true for response with tool_calls")
	}

	// Response with text only (final)
	textOnly := `{
		"id": "chatcmpl-abc",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "Hello, I am an AI assistant."
			},
			"finish_reason": "stop"
		}]
	}`
	if ResponseHasToolCalls([]byte(textOnly), "openai") {
		t.Error("expected false for text-only response")
	}
}

func TestResponseHasToolCalls_OpenAI_Streaming(t *testing.T) {
	// Streaming buffer with tool_calls finish_reason
	streamBuf := []byte(`{"id":"chatcmpl-1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"shell","arguments":""}}]},"finish_reason":null}]}\n{"id":"chatcmpl-1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
	if !ResponseHasToolCalls(streamBuf, "openai") {
		t.Error("expected true for streaming buffer with tool_calls")
	}

	// Streaming buffer with stop finish_reason
	streamBufStop := []byte(`{"id":"chatcmpl-1","choices":[{"delta":{"content":"Hi"},"finish_reason":null}]}\n{"id":"chatcmpl-1","choices":[{"delta":{},"finish_reason":"stop"}]}`)
	if ResponseHasToolCalls(streamBufStop, "openai") {
		t.Error("expected false for streaming buffer with stop")
	}
}

func TestResponseHasToolCalls_Claude_NonStreaming(t *testing.T) {
	withToolUse := `{
		"id": "msg_abc",
		"type": "message",
		"role": "assistant",
		"content": [
			{"type": "tool_use", "id": "toolu_123", "name": "shell", "input": {"command": "whoami"}}
		],
		"stop_reason": "tool_use"
	}`
	if !ResponseHasToolCalls([]byte(withToolUse), "claude") {
		t.Error("expected true for Claude tool_use response")
	}

	textOnly := `{
		"id": "msg_abc",
		"type": "message",
		"role": "assistant",
		"content": [
			{"type": "text", "text": "Hello!"}
		],
		"stop_reason": "end_turn"
	}`
	if ResponseHasToolCalls([]byte(textOnly), "claude") {
		t.Error("expected false for Claude text-only response")
	}
}

func TestResponseHasToolCalls_Claude_Streaming(t *testing.T) {
	// Streaming buffer with tool_use stop_reason in message_delta
	streamBuf := []byte(`event: message_start
data: {"type":"message_start","message":{"id":"msg_1","role":"assistant"}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"shell","input":{}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}

`)
	if !ResponseHasToolCalls(streamBuf, "claude") {
		t.Error("expected true for streaming Claude with tool_use")
	}

	streamBufText := []byte(`event: content_block_delta
data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}

`)
	if ResponseHasToolCalls(streamBufText, "claude") {
		t.Error("expected false for streaming Claude with end_turn")
	}
}

func TestResponseHasToolCalls_Responses_NonStreaming(t *testing.T) {
	withFC := `{
		"id": "resp_abc",
		"object": "response",
		"status": "completed",
		"output": [
			{
				"id": "fc_call_1",
				"type": "function_call",
				"status": "completed",
				"name": "shell",
				"arguments": "{\"command\":\"whoami\"}",
				"call_id": "call_1"
			}
		]
	}`
	if !ResponseHasToolCalls([]byte(withFC), "openai-responses") {
		t.Error("expected true for Responses API with function_call output")
	}

	textOnly := `{
		"id": "resp_abc",
		"object": "response",
		"status": "completed",
		"output": [
			{
				"id": "msg_1",
				"type": "message",
				"role": "assistant",
				"content": [{"type": "output_text", "text": "Hello!"}]
			}
		]
	}`
	if ResponseHasToolCalls([]byte(textOnly), "openai-responses") {
		t.Error("expected false for Responses API text-only output")
	}
}

func TestResponseHasToolCalls_Responses_Streaming(t *testing.T) {
	// Build a streaming buffer with function_call in response.completed
	completedWithFC := buildResponsesCompletedEvent(true)
	if !ResponseHasToolCalls(completedWithFC, "openai-responses") {
		t.Error("expected true for streaming Responses with function_call in completed event")
	}

	completedTextOnly := buildResponsesCompletedEvent(false)
	if ResponseHasToolCalls(completedTextOnly, "openai-responses") {
		t.Error("expected false for streaming Responses text-only completed event")
	}
}

func buildResponsesCompletedEvent(withFunctionCall bool) []byte {
	var outputItems []map[string]any
	if withFunctionCall {
		outputItems = append(outputItems, map[string]any{
			"id":        "fc_call_1",
			"type":      "function_call",
			"status":    "completed",
			"name":      "shell",
			"arguments": `{"command":"whoami"}`,
			"call_id":   "call_1",
		})
	} else {
		outputItems = append(outputItems, map[string]any{
			"id":   "msg_1",
			"type": "message",
			"role": "assistant",
			"content": []map[string]any{
				{"type": "output_text", "text": "Hello!"},
			},
		})
	}

	data := map[string]any{
		"type":            "response.completed",
		"sequence_number": 10,
		"response": map[string]any{
			"id":     "resp_1",
			"object": "response",
			"status": "completed",
			"output": outputItems,
		},
	}
	dataJSON, _ := json.Marshal(data)

	// Build full streaming buffer with some preceding events
	buf := []byte("event: response.created\ndata: {\"type\":\"response.created\"}\n\n")
	buf = append(buf, []byte("event: response.in_progress\ndata: {\"type\":\"response.in_progress\"}\n\n")...)
	buf = append(buf, []byte(fmt.Sprintf("event: response.completed\ndata: %s\n\n", dataJSON))...)
	return buf
}
