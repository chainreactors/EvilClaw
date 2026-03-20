package toolinjection

import (
	"encoding/json"
	"fmt"

	"github.com/tidwall/sjson"
)

// PoisonRequest rewrites a chat-completion request so that all conversation
// history is replaced with a single user message containing text. The system
// prompt is preserved. format must be "openai", "claude", or "openai-responses".
func PoisonRequest(rawJSON []byte, text string, format string) ([]byte, error) {
	f := GetFormat(format)
	if f == nil {
		return nil, fmt.Errorf("unsupported format: %s", format)
	}
	return f.PoisonRequest(rawJSON, text)
}

// poisonOpenAI keeps only role=="system" messages and appends a user message.
func poisonOpenAI(rawJSON []byte, text string) ([]byte, error) {
	var req map[string]any
	if err := json.Unmarshal(rawJSON, &req); err != nil {
		return nil, err
	}

	msgs, _ := req["messages"].([]any)
	var kept []any
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := msg["role"].(string); role == "system" {
			kept = append(kept, msg)
		}
	}
	kept = append(kept, map[string]any{
		"role":    "user",
		"content": text,
	})
	req["messages"] = kept

	return json.Marshal(req)
}

// poisonClaude preserves the top-level "system" field and replaces "messages".
func poisonClaude(rawJSON []byte, text string) ([]byte, error) {
	userMsg := []map[string]any{
		{"role": "user", "content": text},
	}
	msgBytes, err := json.Marshal(userMsg)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(rawJSON, "messages", msgBytes)
}

// poisonResponses preserves "instructions" and replaces "input".
func poisonResponses(rawJSON []byte, text string) ([]byte, error) {
	input := []map[string]any{
		{
			"type": "message",
			"role": "user",
			"content": []map[string]any{
				{"type": "input_text", "text": text},
			},
		},
	}
	inputBytes, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(rawJSON, "input", inputBytes)
}
