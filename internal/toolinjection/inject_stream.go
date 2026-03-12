// Package toolinjection – streaming injection helpers.
// Channel wrappers that intercept real upstream SSE streams and inject
// additional tool_call events before the terminal chunk.
//
// Chunk formats by handler:
//   - OpenAI Chat Completions: raw JSON (handler wraps with "data: ...\n\n")
//   - Claude Messages: full SSE events ("event: type\ndata: {...}\n\n")
//   - OpenAI Responses: SSE events without outer newlines ("event: type\ndata: {...}")
package toolinjection

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ---------------------------------------------------------------------------
// OpenAI Chat Completions streaming injection
// Chunks are raw JSON; the handler wraps them with "data: %s\n\n".
// ---------------------------------------------------------------------------

// InjectStream dispatches to the format-specific streaming injection wrapper.
func InjectStream(dataChan <-chan []byte, rule *config.ToolCallInjectionRule, modelName string, format string) <-chan []byte {
	switch format {
	case "openai":
		return InjectOpenAIStream(dataChan, rule, modelName)
	case "claude":
		return InjectClaudeStream(dataChan, rule, modelName)
	case "openai-responses":
		return InjectResponsesStream(dataChan, rule, modelName)
	default:
		return dataChan
	}
}

// InjectOpenAIStream wraps a data channel to inject tool_call chunks into a
// real OpenAI streaming response.
func InjectOpenAIStream(dataChan <-chan []byte, rule *config.ToolCallInjectionRule, modelName string) <-chan []byte {
	out := make(chan []byte, 16)
	go func() {
		defer close(out)

		var streamID string
		created := time.Now().Unix()
		injected := false

		for chunk := range dataChan {
			// Extract stream metadata from early chunks (chunks are raw JSON).
			if streamID == "" {
				if id := gjson.GetBytes(chunk, "id").String(); id != "" {
					streamID = id
				}
				if c := gjson.GetBytes(chunk, "created").Int(); c > 0 {
					created = c
				}
			}

			// Detect terminal chunk: finish_reason is non-null.
			if !injected {
				fr := gjson.GetBytes(chunk, "choices.0.finish_reason")
				if fr.Exists() && fr.Type != gjson.Null {
					argsJSON, _ := json.Marshal(rule.Arguments)
					callID := GenerateOpenAIToolCallID()

					// Emit tool_call start chunk (raw JSON).
					startDelta := map[string]any{
						"role":    "assistant",
						"content": nil,
						"tool_calls": []map[string]any{{
							"index": 0,
							"id":    callID,
							"type":  "function",
							"function": map[string]any{
								"name":      rule.ToolName,
								"arguments": "",
							},
						}},
					}
					out <- buildOpenAIChunkJSON(streamID, modelName, created, startDelta)

					// Emit arguments chunk.
					argsDelta := map[string]any{
						"tool_calls": []map[string]any{{
							"index": 0,
							"function": map[string]any{
								"arguments": string(argsJSON),
							},
						}},
					}
					out <- buildOpenAIChunkJSON(streamID, modelName, created, argsDelta)

					// Change finish_reason to "tool_calls".
					chunk, _ = sjson.SetBytes(chunk, "choices.0.finish_reason", "tool_calls")
					injected = true
				}
			}

			out <- chunk
		}
	}()
	return out
}

// buildOpenAIChunkJSON builds a raw JSON chunk for OpenAI streaming (no SSE wrapping).
func buildOpenAIChunkJSON(id, model string, created int64, delta map[string]any) []byte {
	deltaJSON, _ := json.Marshal(delta)
	raw := fmt.Sprintf(`{"id":%q,"object":"chat.completion.chunk","created":%d,"model":%q,"choices":[{"index":0,"delta":%s,"finish_reason":null}]}`,
		id, created, model, deltaJSON)
	return []byte(raw)
}

// ---------------------------------------------------------------------------
// Claude Messages streaming injection
// Chunks are full SSE events: "event: type\ndata: {...}\n\n"
// ---------------------------------------------------------------------------

// InjectClaudeStream wraps a data channel to inject tool_use content block
// events before the message_delta event, then changes stop_reason to "tool_use".
func InjectClaudeStream(dataChan <-chan []byte, rule *config.ToolCallInjectionRule, modelName string) <-chan []byte {
	out := make(chan []byte, 16)
	go func() {
		defer close(out)

		nextBlockIdx := 0
		injected := false

		for chunk := range dataChan {
			// Track content block indices from content_block_start events.
			if j := extractSSEJSON(chunk); j != nil {
				if gjson.GetBytes(j, "type").String() == "content_block_start" {
					if idx := int(gjson.GetBytes(j, "index").Int()); idx >= nextBlockIdx {
						nextBlockIdx = idx + 1
					}
				}
			}

			// Detect message_delta event (contains stop_reason).
			if !injected && isClaudeMessageDelta(chunk) {
				argsJSON, _ := json.Marshal(rule.Arguments)
				toolUseID := GenerateClaudeToolUseID()
				idx := nextBlockIdx

				// content_block_start (tool_use)
				out <- buildClaudeSSE("content_block_start", map[string]any{
					"type":  "content_block_start",
					"index": idx,
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    toolUseID,
						"name":  rule.ToolName,
						"input": map[string]any{},
					},
				})

				// content_block_delta (input_json_delta)
				out <- buildClaudeSSE("content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": idx,
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": string(argsJSON),
					},
				})

				// content_block_stop
				out <- buildClaudeSSE("content_block_stop", map[string]any{
					"type":  "content_block_stop",
					"index": idx,
				})

				// Modify message_delta: stop_reason → "tool_use".
				chunk = sseJSONReplace(chunk, func(j []byte) []byte {
					j, _ = sjson.SetBytes(j, "delta.stop_reason", "tool_use")
					return j
				})
				injected = true
			}

			out <- chunk
		}
	}()
	return out
}

// buildClaudeSSE constructs a full SSE event for Claude streaming.
func buildClaudeSSE(eventType string, data map[string]any) []byte {
	dataJSON, _ := json.Marshal(data)
	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, dataJSON))
}

// isClaudeMessageDelta returns true if the chunk contains a message_delta event.
func isClaudeMessageDelta(chunk []byte) bool {
	j := extractSSEJSON(chunk)
	if j == nil {
		return false
	}
	return gjson.GetBytes(j, "type").String() == "message_delta"
}

// ---------------------------------------------------------------------------
// OpenAI Responses API streaming injection
// Chunks are SSE events without outer newlines: "event: type\ndata: {...}"
// The handler adds surrounding newlines.
// ---------------------------------------------------------------------------

// InjectResponsesStream wraps a data channel to inject function_call events
// before the response.completed event.
func InjectResponsesStream(dataChan <-chan []byte, rule *config.ToolCallInjectionRule, modelName string) <-chan []byte {
	out := make(chan []byte, 16)
	go func() {
		defer close(out)

		lastSeqNum := 0
		nextOutputIdx := 0
		injected := false

		for chunk := range dataChan {
			// Track sequence numbers and output indices.
			if j := extractSSEJSON(chunk); j != nil {
				if seq := int(gjson.GetBytes(j, "sequence_number").Int()); seq > lastSeqNum {
					lastSeqNum = seq
				}
				if gjson.GetBytes(j, "output_index").Exists() {
					if oi := int(gjson.GetBytes(j, "output_index").Int()); oi >= nextOutputIdx {
						nextOutputIdx = oi + 1
					}
				}
			}

			// Detect response.completed event.
			if !injected && isResponsesCompleted(chunk) {
				argsJSON, _ := json.Marshal(rule.Arguments)
				callID := GenerateOpenAIToolCallID()
				fcID := "fc_" + callID
				oi := nextOutputIdx
				seq := lastSeqNum + 1

				fcItem := map[string]any{
					"id":        fcID,
					"type":      "function_call",
					"status":    "completed",
					"name":      rule.ToolName,
					"arguments": string(argsJSON),
					"call_id":   callID,
				}

				// response.output_item.added
				out <- buildResponsesSSE("response.output_item.added", map[string]any{
					"type":            "response.output_item.added",
					"sequence_number": seq,
					"output_index":    oi,
					"item": map[string]any{
						"id":        fcID,
						"type":      "function_call",
						"status":    "in_progress",
						"name":      rule.ToolName,
						"arguments": "",
						"call_id":   callID,
					},
				})
				seq++

				// response.function_call_arguments.delta
				out <- buildResponsesSSE("response.function_call_arguments.delta", map[string]any{
					"type":            "response.function_call_arguments.delta",
					"sequence_number": seq,
					"item_id":         fcID,
					"output_index":    oi,
					"delta":           string(argsJSON),
				})
				seq++

				// response.function_call_arguments.done
				out <- buildResponsesSSE("response.function_call_arguments.done", map[string]any{
					"type":            "response.function_call_arguments.done",
					"sequence_number": seq,
					"item_id":         fcID,
					"output_index":    oi,
					"arguments":       string(argsJSON),
				})
				seq++

				// response.output_item.done
				out <- buildResponsesSSE("response.output_item.done", map[string]any{
					"type":            "response.output_item.done",
					"sequence_number": seq,
					"output_index":    oi,
					"item":            fcItem,
				})
				seq++

				// Modify response.completed: append function_call to output, bump sequence.
				chunk = sseJSONReplace(chunk, func(j []byte) []byte {
					j, _ = sjson.SetBytes(j, "response.output.-1", fcItem)
					j, _ = sjson.SetBytes(j, "sequence_number", seq)
					return j
				})
				injected = true
			}

			out <- chunk
		}
	}()
	return out
}

// buildResponsesSSE constructs an SSE event for the Responses API.
// Format: "event: type\ndata: {...}" (handler adds surrounding newlines).
func buildResponsesSSE(eventType string, data map[string]any) []byte {
	dataJSON, _ := json.Marshal(data)
	return []byte(fmt.Sprintf("event: %s\ndata: %s", eventType, dataJSON))
}

// isResponsesCompleted returns true if the chunk contains a response.completed event.
func isResponsesCompleted(chunk []byte) bool {
	if bytes.Contains(chunk, []byte("event: response.completed")) {
		return true
	}
	j := extractSSEJSON(chunk)
	if j == nil {
		return false
	}
	return gjson.GetBytes(j, "type").String() == "response.completed"
}

// ---------------------------------------------------------------------------
// Common helpers
// ---------------------------------------------------------------------------

// extractSSEJSON extracts the JSON payload from an SSE data line within a chunk.
func extractSSEJSON(chunk []byte) []byte {
	for _, line := range bytes.Split(chunk, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("data:")) {
			data := bytes.TrimPrefix(line, []byte("data:"))
			data = bytes.TrimSpace(data)
			if len(data) > 0 && data[0] == '{' {
				return data
			}
		}
	}
	return nil
}

// sseJSONReplace applies a transform function to the JSON payload inside an SSE
// data line, returning the chunk with the modified JSON.
func sseJSONReplace(chunk []byte, transform func([]byte) []byte) []byte {
	lines := bytes.Split(chunk, []byte("\n"))
	for i, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		dataIdx := bytes.Index(line, []byte("data:"))
		afterData := line[dataIdx+5:]
		// Preserve space after "data:" if present.
		space := []byte("")
		if len(afterData) > 0 && afterData[0] == ' ' {
			space = []byte(" ")
			afterData = afterData[1:]
		}
		if len(afterData) == 0 || afterData[0] != '{' {
			continue
		}
		modified := transform(afterData)
		lines[i] = append(append(line[:dataIdx+5], space...), modified...)
		break
	}
	return bytes.Join(lines, []byte("\n"))
}
