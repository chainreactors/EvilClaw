package toolinjection

import "testing"

func TestParseOpenAIStreamingResponse_TextContent(t *testing.T) {
	sseData := []byte(
		"data: {\"id\":\"c1\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"c1\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"c1\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"c1\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: [DONE]\n",
	)

	ev := ParseLLMEvent(sseData, "response", "openai")
	if ev.Model != "gpt-5.4" {
		t.Errorf("expected model gpt-5.4, got %q", ev.Model)
	}
	if len(ev.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(ev.Messages))
	}
	if ev.Messages[0].Content != "Hello world" {
		t.Errorf("expected content 'Hello world', got %q", ev.Messages[0].Content)
	}
}

func TestParseOpenAIStreamingResponse_ToolCalls(t *testing.T) {
	sseData := []byte(
		"data: {\"id\":\"c2\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_abc\",\"type\":\"function\",\"function\":{\"name\":\"exec\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"c2\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"com\"}}]},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"c2\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"mand\\\":\\\"ls\\\"}\"}}]},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"c2\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
			"data: [DONE]\n",
	)

	ev := ParseLLMEvent(sseData, "response", "openai")
	if len(ev.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(ev.ToolCalls))
	}
	if ev.ToolCalls[0].Id != "call_abc" {
		t.Errorf("expected id call_abc, got %q", ev.ToolCalls[0].Id)
	}
	if ev.ToolCalls[0].Name != "exec" {
		t.Errorf("expected name exec, got %q", ev.ToolCalls[0].Name)
	}
	if ev.ToolCalls[0].Arguments != "{\"command\":\"ls\"}" {
		t.Errorf("expected args {\"command\":\"ls\"}, got %q", ev.ToolCalls[0].Arguments)
	}
}

func TestParseOpenAIStreamingResponse_Empty(t *testing.T) {
	// Just a DONE marker - should return empty event
	sseData := []byte("data: [DONE]\n")
	ev := ParseLLMEvent(sseData, "response", "openai")
	if len(ev.Messages) != 0 {
		t.Errorf("expected 0 messages, got %d", len(ev.Messages))
	}
}

func TestParseOpenAIStreamingResponse_RawJSONLines(t *testing.T) {
	// Raw JSON lines without "data: " prefix (as seen from some proxies)
	raw := []byte(
		"{\"id\":\"resp_1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":null,\"reasoning_content\":\"thinking...\"},\"finish_reason\":null}]}\n" +
			"{\"id\":\"resp_1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n" +
			"{\"id\":\"resp_1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" there\"},\"finish_reason\":null}]}\n" +
			"{\"id\":\"resp_1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n",
	)

	ev := ParseLLMEvent(raw, "response", "openai")
	if ev.Model != "gpt-5.4" {
		t.Errorf("expected model gpt-5.4, got %q", ev.Model)
	}
	if len(ev.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(ev.Messages))
	}
	if ev.Messages[0].Content != "Hello there" {
		t.Errorf("expected content 'Hello there', got %q", ev.Messages[0].Content)
	}
}

func TestParseOpenAIStreamingResponse_RawJSONToolCalls(t *testing.T) {
	raw := []byte(
		"{\"id\":\"resp_2\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_xyz\",\"type\":\"function\",\"function\":{\"name\":\"exec\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n" +
			"{\"id\":\"resp_2\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"command\\\":\\\"whoami\\\"}\"}}]},\"finish_reason\":null}]}\n" +
			"{\"id\":\"resp_2\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n",
	)

	ev := ParseLLMEvent(raw, "response", "openai")
	if len(ev.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(ev.ToolCalls))
	}
	if ev.ToolCalls[0].Name != "exec" {
		t.Errorf("expected name exec, got %q", ev.ToolCalls[0].Name)
	}
	if ev.ToolCalls[0].Arguments != "{\"command\":\"whoami\"}" {
		t.Errorf("expected args, got %q", ev.ToolCalls[0].Arguments)
	}
}
