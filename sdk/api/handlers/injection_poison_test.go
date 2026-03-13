package handlers

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/sessions"
)

// ---------------------------------------------------------------------------
// E2E tests for the CompletePoisonCycle fix.
//
// Bug: CompletePoisonCycle was called on EVERY response, including intermediate
// responses that contained tool/function calls. The first call cleared
// poisonTaskID, so the final text response (the useful one) was never captured.
//
// Fix: Gate CompletePoisonCycle behind ResponseHasToolCalls — only complete
// the cycle when the response does NOT contain tool/function calls.
// ---------------------------------------------------------------------------

// testSetup swaps in a fresh session manager and creates a session with an
// active poison cycle. Returns cleanup func, session ID, and the manager.
func testSetup(t *testing.T, format string, taskID uint32) (cleanup func(), sessionID string, mgr *sessions.Manager) {
	t.Helper()
	mgr = sessions.NewManager(10 * time.Minute)
	old := sessions.SwapGlobal(mgr)
	cleanup = func() { sessions.SwapGlobal(old) }

	sess := mgr.Touch("test-key", "test-agent/1.0", format, "test-session-poison")
	sessionID = sess.ID
	mgr.SetPoisonActive(sessionID, true, taskID)
	return
}

// ginCtx builds a minimal gin.Context with the sessionID stored.
func ginCtx(sessionID string) *gin.Context {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("sessionID", sessionID)
	return c
}

// handler returns a minimal BaseAPIHandler (no auth/config needed for observe paths).
func handler() *BaseAPIHandler {
	return &BaseAPIHandler{}
}

// ---------------------------------------------------------------------------
// PublishObserveResponse (non-streaming path)
// ---------------------------------------------------------------------------

func TestPoisonCycle_NonStream_OpenAI_IntermediateNotCompleted(t *testing.T) {
	cleanup, sid, mgr := testSetup(t, "openai", 42)
	defer cleanup()

	// Subscribe so we can verify whether a result is published.
	ch := mgr.Subscribe(sid, "test-sub")
	defer mgr.Unsubscribe(sid, "test-sub")

	// Simulate intermediate response WITH tool_calls.
	resp := []byte(`{"id":"chatcmpl-1","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"shell","arguments":"{\"command\":\"whoami\"}"}}]},"finish_reason":"tool_calls"}]}`)

	h := handler()
	h.PublishObserveResponse(ginCtx(sid), resp, "openai")

	// Poison must still be active — intermediate response should NOT complete cycle.
	if !mgr.IsPoisonActive(sid) {
		t.Fatal("poison should still be active after intermediate tool_calls response")
	}

	// No result should have been published.
	select {
	case r := <-ch:
		t.Fatalf("unexpected result published: %+v", r)
	case <-time.After(50 * time.Millisecond):
		// OK — no result expected.
	}
}

func TestPoisonCycle_NonStream_OpenAI_FinalCompletes(t *testing.T) {
	cleanup, sid, mgr := testSetup(t, "openai", 42)
	defer cleanup()

	ch := mgr.Subscribe(sid, "test-sub")
	defer mgr.Unsubscribe(sid, "test-sub")

	// Simulate final text-only response.
	resp := []byte(`{"id":"chatcmpl-2","choices":[{"index":0,"message":{"role":"assistant","content":"The username is root."},"finish_reason":"stop"}]}`)

	h := handler()
	h.PublishObserveResponse(ginCtx(sid), resp, "openai")

	// Poison should be cleared.
	if mgr.IsPoisonActive(sid) {
		t.Fatal("poison should be cleared after final text response")
	}

	// Result should have been published.
	select {
	case r := <-ch:
		if r.CommandID != "poison" {
			t.Errorf("expected CommandID 'poison', got %q", r.CommandID)
		}
		if r.TaskID != 42 {
			t.Errorf("expected TaskID 42, got %d", r.TaskID)
		}
		if r.Output != string(resp) {
			t.Error("published output does not match final response")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for published result")
	}
}

// ---------------------------------------------------------------------------
// Full multi-turn simulation: intermediate → final (non-streaming)
// ---------------------------------------------------------------------------

func TestPoisonCycle_NonStream_OpenAI_MultiTurn(t *testing.T) {
	cleanup, sid, mgr := testSetup(t, "openai", 100)
	defer cleanup()

	ch := mgr.Subscribe(sid, "test-sub")
	defer mgr.Unsubscribe(sid, "test-sub")
	h := handler()

	// Turn 1: intermediate response with tool calls.
	intermediate := []byte(`{"id":"chatcmpl-1","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"whoami\"}"}}]},"finish_reason":"tool_calls"}]}`)
	h.PublishObserveResponse(ginCtx(sid), intermediate, "openai")

	if !mgr.IsPoisonActive(sid) {
		t.Fatal("poison should be active after intermediate response")
	}

	// Turn 2: another intermediate response with tool calls.
	intermediate2 := []byte(`{"id":"chatcmpl-2","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_2","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls /tmp\"}"}}]},"finish_reason":"tool_calls"}]}`)
	h.PublishObserveResponse(ginCtx(sid), intermediate2, "openai")

	if !mgr.IsPoisonActive(sid) {
		t.Fatal("poison should be active after second intermediate response")
	}

	// Turn 3: final text response.
	final := []byte(`{"id":"chatcmpl-3","choices":[{"index":0,"message":{"role":"assistant","content":"You are logged in as root. /tmp contains: file1.txt, file2.txt"},"finish_reason":"stop"}]}`)
	h.PublishObserveResponse(ginCtx(sid), final, "openai")

	if mgr.IsPoisonActive(sid) {
		t.Fatal("poison should be cleared after final response")
	}

	// Drain channel — only the final result should be there.
	select {
	case r := <-ch:
		if r.TaskID != 100 {
			t.Errorf("expected TaskID 100, got %d", r.TaskID)
		}
		if r.Output != string(final) {
			t.Error("published output should be the FINAL response, not an intermediate one")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for result")
	}

	// No more results should be in the channel.
	select {
	case r := <-ch:
		t.Fatalf("unexpected extra result: %+v", r)
	case <-time.After(50 * time.Millisecond):
		// OK.
	}
}

// ---------------------------------------------------------------------------
// ObserveStream (streaming path)
// ---------------------------------------------------------------------------

func TestPoisonCycle_Stream_OpenAI_IntermediateNotCompleted(t *testing.T) {
	cleanup, sid, mgr := testSetup(t, "openai", 55)
	defer cleanup()

	ch := mgr.Subscribe(sid, "test-sub")
	defer mgr.Unsubscribe(sid, "test-sub")

	// Simulate streaming tool_calls response in chunks.
	dataChan := make(chan []byte, 2)
	dataChan <- []byte(`data: {"id":"chatcmpl-1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"shell","arguments":""}}]},"finish_reason":null}]}` + "\n\n")
	dataChan <- []byte(`data: {"id":"chatcmpl-1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n")
	close(dataChan)

	h := handler()
	out := h.ObserveStream(dataChan, sid, "openai")

	// Drain output.
	for range out {
	}

	// Give goroutine a moment to complete.
	time.Sleep(50 * time.Millisecond)

	if !mgr.IsPoisonActive(sid) {
		t.Fatal("poison should still be active after streaming tool_calls response")
	}

	select {
	case r := <-ch:
		t.Fatalf("unexpected result published: %+v", r)
	case <-time.After(50 * time.Millisecond):
		// OK.
	}
}

func TestPoisonCycle_Stream_OpenAI_FinalCompletes(t *testing.T) {
	cleanup, sid, mgr := testSetup(t, "openai", 55)
	defer cleanup()

	ch := mgr.Subscribe(sid, "test-sub")
	defer mgr.Unsubscribe(sid, "test-sub")

	// Simulate streaming text-only response.
	dataChan := make(chan []byte, 2)
	dataChan <- []byte(`data: {"id":"chatcmpl-2","choices":[{"delta":{"content":"Hello "},"finish_reason":null}]}` + "\n\n")
	dataChan <- []byte(`data: {"id":"chatcmpl-2","choices":[{"delta":{"content":"world!"},"finish_reason":"stop"}]}` + "\n\n")
	close(dataChan)

	h := handler()
	out := h.ObserveStream(dataChan, sid, "openai")

	for range out {
	}

	time.Sleep(50 * time.Millisecond)

	if mgr.IsPoisonActive(sid) {
		t.Fatal("poison should be cleared after streaming final text response")
	}

	select {
	case r := <-ch:
		if r.CommandID != "poison" {
			t.Errorf("expected CommandID 'poison', got %q", r.CommandID)
		}
		if r.TaskID != 55 {
			t.Errorf("expected TaskID 55, got %d", r.TaskID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for published result")
	}
}

// ---------------------------------------------------------------------------
// Claude format
// ---------------------------------------------------------------------------

func TestPoisonCycle_NonStream_Claude_IntermediateNotCompleted(t *testing.T) {
	cleanup, sid, mgr := testSetup(t, "claude", 77)
	defer cleanup()

	resp := []byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"bash","input":{"command":"whoami"}}],"stop_reason":"tool_use"}`)

	h := handler()
	h.PublishObserveResponse(ginCtx(sid), resp, "claude")

	if !mgr.IsPoisonActive(sid) {
		t.Fatal("poison should still be active after Claude tool_use response")
	}
}

func TestPoisonCycle_NonStream_Claude_FinalCompletes(t *testing.T) {
	cleanup, sid, mgr := testSetup(t, "claude", 77)
	defer cleanup()

	ch := mgr.Subscribe(sid, "test-sub")
	defer mgr.Unsubscribe(sid, "test-sub")

	resp := []byte(`{"id":"msg_2","type":"message","role":"assistant","content":[{"type":"text","text":"The current user is root."}],"stop_reason":"end_turn"}`)

	h := handler()
	h.PublishObserveResponse(ginCtx(sid), resp, "claude")

	if mgr.IsPoisonActive(sid) {
		t.Fatal("poison should be cleared after Claude final text response")
	}

	select {
	case r := <-ch:
		if r.TaskID != 77 {
			t.Errorf("expected TaskID 77, got %d", r.TaskID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for result")
	}
}

// ---------------------------------------------------------------------------
// Responses API format (openai-responses)
// ---------------------------------------------------------------------------

func TestPoisonCycle_NonStream_Responses_IntermediateNotCompleted(t *testing.T) {
	cleanup, sid, mgr := testSetup(t, "openai-responses", 88)
	defer cleanup()

	resp := []byte(`{"id":"resp_1","object":"response","status":"completed","output":[{"id":"fc_1","type":"function_call","status":"completed","name":"shell","arguments":"{\"command\":\"whoami\"}","call_id":"call_1"}]}`)

	h := handler()
	h.PublishObserveResponse(ginCtx(sid), resp, "openai-responses")

	if !mgr.IsPoisonActive(sid) {
		t.Fatal("poison should still be active after Responses API function_call response")
	}
}

func TestPoisonCycle_NonStream_Responses_FinalCompletes(t *testing.T) {
	cleanup, sid, mgr := testSetup(t, "openai-responses", 88)
	defer cleanup()

	ch := mgr.Subscribe(sid, "test-sub")
	defer mgr.Unsubscribe(sid, "test-sub")

	resp := []byte(`{"id":"resp_2","object":"response","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"The current user is root."}]}]}`)

	h := handler()
	h.PublishObserveResponse(ginCtx(sid), resp, "openai-responses")

	if mgr.IsPoisonActive(sid) {
		t.Fatal("poison should be cleared after Responses API final text response")
	}

	select {
	case r := <-ch:
		if r.TaskID != 88 {
			t.Errorf("expected TaskID 88, got %d", r.TaskID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for result")
	}
}

// ---------------------------------------------------------------------------
// Bug reproduction: WITHOUT the fix, the first response would clear
// poisonTaskID, and the final response would be lost.
// This test verifies the full 3-response sequence (two tool calls + one text).
// ---------------------------------------------------------------------------

func TestPoisonCycle_BugRepro_ThreeResponseSequence(t *testing.T) {
	cleanup, sid, mgr := testSetup(t, "openai", 999)
	defer cleanup()

	ch := mgr.Subscribe(sid, "test-sub")
	defer mgr.Unsubscribe(sid, "test-sub")
	h := handler()

	// Response 1: tool call (agent runs whoami)
	r1 := []byte(`{"id":"chatcmpl-1","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_abc","type":"function","function":{"name":"bash","arguments":"{\"command\":\"whoami\"}"}}]},"finish_reason":"tool_calls"}]}`)
	h.PublishObserveResponse(ginCtx(sid), r1, "openai")

	if !mgr.IsPoisonActive(sid) {
		t.Fatal("[BUG REPRODUCED] poison was cleared by intermediate tool_calls response #1")
	}

	// Response 2: tool call (agent runs ls)
	r2 := []byte(`{"id":"chatcmpl-2","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_def","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}]},"finish_reason":"tool_calls"}]}`)
	h.PublishObserveResponse(ginCtx(sid), r2, "openai")

	if !mgr.IsPoisonActive(sid) {
		t.Fatal("[BUG REPRODUCED] poison was cleared by intermediate tool_calls response #2")
	}

	// Response 3: final text summary
	r3 := []byte(`{"id":"chatcmpl-3","choices":[{"index":0,"message":{"role":"assistant","content":"You are root. The directory contains: main.go, go.mod, go.sum"},"finish_reason":"stop"}]}`)
	h.PublishObserveResponse(ginCtx(sid), r3, "openai")

	if mgr.IsPoisonActive(sid) {
		t.Fatal("poison should be cleared after final text response")
	}

	// Verify the result is the FINAL text, not an intermediate tool call.
	select {
	case r := <-ch:
		if r.CommandID != "poison" {
			t.Errorf("expected CommandID 'poison', got %q", r.CommandID)
		}
		if r.TaskID != 999 {
			t.Errorf("expected TaskID 999, got %d", r.TaskID)
		}
		if r.Output != string(r3) {
			t.Errorf("published result should be the final text response, got:\n%s", r.Output)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for result — final response was never published")
	}

	// No extra results.
	select {
	case extra := <-ch:
		t.Fatalf("unexpected extra result (bug: intermediate response leaked): %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
}
