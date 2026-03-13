package toolinjection

import (
	"strings"

	"github.com/chainreactors/IoM-go/proto/implant/implantpb"
	"github.com/tidwall/gjson"
)

const (
	lastNMessages = 1
)

// ParseLLMEvent parses raw LLM request/response JSON into a structured LLMEvent.
// eventType is "request" or "response"; format is "openai", "claude", or "openai-responses".
func ParseLLMEvent(rawJSON []byte, eventType, format string) *implantpb.LLMEvent {
	ev := &implantpb.LLMEvent{
		Type:   eventType,
		Format: format,
	}

	switch eventType {
	case "request":
		parseRequest(rawJSON, format, ev)
	case "response":
		parseResponse(rawJSON, format, ev)
	}

	return ev
}

// parseRequest extracts model, message count, last N messages, and tool results from a request.
func parseRequest(raw []byte, format string, ev *implantpb.LLMEvent) {
	ev.Model = gjson.GetBytes(raw, "model").String()

	switch format {
	case "openai":
		parseOpenAIRequest(raw, ev)
	case "claude":
		parseClaudeRequest(raw, ev)
	case "openai-responses":
		parseResponsesRequest(raw, ev)
	}
}

func parseOpenAIRequest(raw []byte, ev *implantpb.LLMEvent) {
	msgs := gjson.GetBytes(raw, "messages")
	if !msgs.Exists() || !msgs.IsArray() {
		return
	}

	arr := msgs.Array()
	ev.MessageCount = int32(len(arr))

	start := len(arr) - lastNMessages
	if start < 0 {
		start = 0
	}
	for _, m := range arr[start:] {
		role := m.Get("role").String()
		content := extractOpenAIContent(m)

		ev.Messages = append(ev.Messages, &implantpb.LLMMessage{
			Role:    role,
			Content: content,
		})

		if role == "tool" {
			ev.ToolResults = append(ev.ToolResults, &implantpb.LLMToolResult{
				CallId:  m.Get("tool_call_id").String(),
				Content: content,
			})
		}
	}
}

func parseClaudeRequest(raw []byte, ev *implantpb.LLMEvent) {
	msgs := gjson.GetBytes(raw, "messages")
	if !msgs.Exists() || !msgs.IsArray() {
		return
	}

	arr := msgs.Array()
	ev.MessageCount = int32(len(arr))

	start := len(arr) - lastNMessages
	if start < 0 {
		start = 0
	}
	for _, m := range arr[start:] {
		role := m.Get("role").String()
		content := extractClaudeContent(m)

		ev.Messages = append(ev.Messages, &implantpb.LLMMessage{
			Role:    role,
			Content: content,
		})

		if role == "user" {
			m.Get("content").ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "tool_result" {
					ev.ToolResults = append(ev.ToolResults, &implantpb.LLMToolResult{
						CallId:  block.Get("tool_use_id").String(),
						Content: extractClaudeBlockText(block),
					})
				}
				return true
			})
		}
	}
}

func parseResponsesRequest(raw []byte, ev *implantpb.LLMEvent) {
	input := gjson.GetBytes(raw, "input")
	if !input.Exists() || !input.IsArray() {
		return
	}

	arr := input.Array()
	ev.MessageCount = int32(len(arr))

	start := len(arr) - lastNMessages
	if start < 0 {
		start = 0
	}
	for _, item := range arr[start:] {
		itemType := item.Get("type").String()
		role := item.Get("role").String()

		switch itemType {
		case "message":
			content := extractResponsesContent(item)
			ev.Messages = append(ev.Messages, &implantpb.LLMMessage{
				Role:    role,
				Content: content,
			})
		case "function_call_output":
			ev.ToolResults = append(ev.ToolResults, &implantpb.LLMToolResult{
				CallId:  item.Get("call_id").String(),
				Content: item.Get("output").String(),
			})
		}
	}
}

// parseResponse extracts assistant content and tool calls from a response.
func parseResponse(raw []byte, format string, ev *implantpb.LLMEvent) {
	ev.Model = gjson.GetBytes(raw, "model").String()

	switch format {
	case "openai":
		parseOpenAIResponse(raw, ev)
	case "claude":
		parseClaudeResponse(raw, ev)
	case "openai-responses":
		parseResponsesResponse(raw, ev)
	}
}

func parseOpenAIResponse(raw []byte, ev *implantpb.LLMEvent) {
	msg := gjson.GetBytes(raw, "choices.0.message")
	if !msg.Exists() {
		// Streaming accumulated SSE — try to extract from last complete JSON
		if parsed := extractSSEFinalJSON(raw); parsed != nil {
			parseOpenAIResponse(parsed, ev)
			return
		}
		return
	}

	content := extractOpenAIContent(msg)
	if content != "" {
		ev.Messages = append(ev.Messages, &implantpb.LLMMessage{
			Role:    "assistant",
			Content: content,
		})
	}

	msg.Get("tool_calls").ForEach(func(_, tc gjson.Result) bool {
		ev.ToolCalls = append(ev.ToolCalls, &implantpb.LLMToolCall{
			Id:        tc.Get("id").String(),
			Name:      tc.Get("function.name").String(),
			Arguments: tc.Get("function.arguments").String(),
		})
		return true
	})
}

func parseClaudeResponse(raw []byte, ev *implantpb.LLMEvent) {
	content := gjson.GetBytes(raw, "content")
	if !content.Exists() || !content.IsArray() {
		// Streaming accumulated SSE — try to extract from last complete JSON
		if parsed := extractSSEFinalJSON(raw); parsed != nil {
			parseClaudeResponse(parsed, ev)
			return
		}
		return
	}

	content.ForEach(func(_, block gjson.Result) bool {
		blockType := block.Get("type").String()
		// Skip thinking blocks
		if blockType == "thinking" {
			return true
		}
		switch blockType {
		case "text":
			ev.Messages = append(ev.Messages, &implantpb.LLMMessage{
				Role:    "assistant",
				Content: block.Get("text").String(),
			})
		case "tool_use":
			ev.ToolCalls = append(ev.ToolCalls, &implantpb.LLMToolCall{
				Id:        block.Get("id").String(),
				Name:      block.Get("name").String(),
				Arguments: block.Get("input").Raw,
			})
		}
		return true
	})
}

func parseResponsesResponse(raw []byte, ev *implantpb.LLMEvent) {
	output := gjson.GetBytes(raw, "output")
	if !output.Exists() || !output.IsArray() {
		// Streaming accumulated SSE — try to extract response.completed event
		if parsed := extractSSEResponseCompleted(raw); parsed != nil {
			ev.Model = gjson.GetBytes(parsed, "model").String()
			parseResponsesResponse(parsed, ev)
			return
		}
		return
	}

	output.ForEach(func(_, item gjson.Result) bool {
		itemType := item.Get("type").String()
		switch itemType {
		case "message":
			content := extractResponsesContent(item)
			ev.Messages = append(ev.Messages, &implantpb.LLMMessage{
				Role:    "assistant",
				Content: content,
			})
		case "function_call":
			ev.ToolCalls = append(ev.ToolCalls, &implantpb.LLMToolCall{
				Id:        item.Get("call_id").String(),
				Name:      item.Get("name").String(),
				Arguments: item.Get("arguments").String(),
			})
		case "reasoning":
			// Skip thinking/reasoning output
		}
		return true
	})
}

// --- SSE parsing helpers ---

// extractSSEResponseCompleted finds the "response.completed" SSE event and
// returns the inner "response" JSON object from its data payload.
// Uses substring search instead of line-by-line matching to handle buffers
// where SSE chunks may be concatenated without separating newlines.
func extractSSEResponseCompleted(raw []byte) []byte {
	s := string(raw)

	// Find the last "event: response.completed" (or "event:response.completed")
	// using substring search — robust against merged SSE lines.
	idx := strings.LastIndex(s, "event: response.completed")
	if idx < 0 {
		idx = strings.LastIndex(s, "event:response.completed")
	}
	if idx < 0 {
		return nil
	}

	rest := s[idx:]
	// Find the "data:" payload after the event marker.
	dataIdx := strings.Index(rest, "\ndata:")
	if dataIdx < 0 {
		// Chunk may have "data: " on same logical line after the event tag.
		dataIdx = strings.Index(rest, "\ndata: ")
		if dataIdx < 0 {
			return nil
		}
	}
	dataLine := rest[dataIdx+1:] // skip the leading \n
	dataLine = strings.TrimPrefix(dataLine, "data: ")
	dataLine = strings.TrimPrefix(dataLine, "data:")
	dataLine = strings.TrimSpace(dataLine)

	// Trim at the next newline (end of the data payload).
	if endIdx := strings.Index(dataLine, "\n"); endIdx > 0 {
		dataLine = dataLine[:endIdx]
	}

	// Extract the "response" field from the event data.
	resp := gjson.Get(dataLine, "response")
	if resp.Exists() && resp.Type == gjson.JSON {
		return []byte(resp.Raw)
	}
	if gjson.Valid(dataLine) {
		return []byte(dataLine)
	}
	return nil
}

// extractSSEFinalJSON tries to find the last valid complete JSON object
// in accumulated SSE data (for openai/claude streaming).
func extractSSEFinalJSON(raw []byte) []byte {
	s := string(raw)
	lines := strings.Split(s, "\n")

	// Walk backwards to find the last "data: {...}" line with valid JSON
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		data = strings.TrimPrefix(data, "data:")
		data = strings.TrimSpace(data)
		if data == "[DONE]" || data == "" {
			continue
		}
		if gjson.Valid(data) {
			return []byte(data)
		}
	}
	return nil
}

// --- Content extraction helpers ---

// extractOpenAIContent gets text content from an OpenAI message.
// Handles both string content and array content blocks.
func extractOpenAIContent(msg gjson.Result) string {
	c := msg.Get("content")
	if c.Type == gjson.String {
		return c.String()
	}
	if c.IsArray() {
		var text string
		c.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "text" {
				text += block.Get("text").String()
			}
			return true
		})
		return text
	}
	return ""
}

// extractClaudeContent gets text content from a Claude message, skipping thinking blocks.
func extractClaudeContent(msg gjson.Result) string {
	c := msg.Get("content")
	if c.Type == gjson.String {
		return c.String()
	}
	if c.IsArray() {
		var text string
		c.ForEach(func(_, block gjson.Result) bool {
			blockType := block.Get("type").String()
			if blockType == "thinking" {
				return true // skip thinking
			}
			if blockType == "text" {
				text += block.Get("text").String()
			}
			return true
		})
		return text
	}
	return ""
}

// extractClaudeBlockText extracts text from a Claude tool_result content block.
func extractClaudeBlockText(block gjson.Result) string {
	c := block.Get("content")
	if c.Type == gjson.String {
		return c.String()
	}
	if c.IsArray() {
		var text string
		c.ForEach(func(_, b gjson.Result) bool {
			if b.Get("type").String() == "text" {
				text += b.Get("text").String()
			}
			return true
		})
		return text
	}
	return ""
}

// extractResponsesContent gets text from an OpenAI Responses API message item,
// skipping reasoning/thinking content.
func extractResponsesContent(item gjson.Result) string {
	c := item.Get("content")
	if c.Type == gjson.String {
		return c.String()
	}
	if c.IsArray() {
		var text string
		c.ForEach(func(_, block gjson.Result) bool {
			t := block.Get("type").String()
			// Skip reasoning/thinking content
			if t == "reasoning" || t == "thinking" {
				return true
			}
			if t == "input_text" || t == "output_text" || t == "text" {
				text += block.Get("text").String()
			}
			return true
		})
		return text
	}
	return ""
}
