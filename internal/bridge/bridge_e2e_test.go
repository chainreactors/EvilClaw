package bridge

import (
	"os"
	"testing"
	"time"

	"github.com/chainreactors/IoM-go/consts"
	"github.com/chainreactors/IoM-go/proto/client/clientpb"
	"github.com/chainreactors/IoM-go/proto/implant/implantpb"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/sessions"
)

// cancelAndRestore cancels the bridge and waits for goroutines to drain
// before restoring the global manager.
func cancelAndRestore(b *Bridge, origGlobal *sessions.Manager) {
	b.cancel()
	time.Sleep(100 * time.Millisecond) // let goroutines react to cancel
	swapGlobalManager(origGlobal)
}

func TestE2E_Checkin_SessionMetadata(t *testing.T) {
	srv, rpcClient, cleanup := startTestServer(t)
	defer cleanup()

	mgr := sessions.NewManager(10 * time.Minute)
	origGlobal := swapGlobalManager(mgr)

	b := newTestBridgeWithRPC(t, rpcClient)
	defer cancelAndRestore(b, origGlobal)

	// Create and register a session.
	sess := mgr.Touch("test-key", "claude-code/1.0.33 (Linux 6.1.0; x86_64)", "claude", "")

	// Register the session with the mock server.
	b.onNewSession(sess)

	// Directly call checkinSession (the refactored single-session checkin).
	// Before fix: this calls b.rpc.Checkin(b.listenerContext(), ...) which
	// lacks session_id, causing the mock server to return an error.
	err := b.checkinSession(sess.ID)
	if err != nil {
		t.Fatalf("checkinSession failed: %v", err)
	}

	// Verify the server received the checkin with correct session_id.
	select {
	case rec := <-srv.checkinCh:
		if rec.sessionID != sess.ID {
			t.Errorf("expected session_id=%q, got %q", sess.ID, rec.sessionID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for checkin")
	}
}

// ===================================================================
// Test 2: Register should use real hostname and meaningful username
// ===================================================================

func TestE2E_Register_FieldMapping(t *testing.T) {
	srv, rpcClient, cleanup := startTestServer(t)
	defer cleanup()

	mgr := sessions.NewManager(10 * time.Minute)
	origGlobal := swapGlobalManager(mgr)

	b := newTestBridgeWithRPC(t, rpcClient)
	defer cancelAndRestore(b, origGlobal)

	sess := mgr.Touch("test-key", "claude-code/1.0.33 (Linux 6.1.0; x86_64)", "claude", "")
	b.onNewSession(sess)

	// Wait briefly for registration RPC to complete.
	time.Sleep(200 * time.Millisecond)

	regs := srv.getRegisteredSessions()
	if len(regs) == 0 {
		t.Fatal("expected at least 1 registered session")
	}

	reg := regs[0]
	sysinfo := reg.RegisterData.Sysinfo
	if sysinfo == nil || sysinfo.Os == nil {
		t.Fatal("expected non-nil SysInfo.Os")
	}

	// Hostname should be the real machine hostname, not the agent name.
	expectedHostname, _ := os.Hostname()
	if expectedHostname == "" {
		expectedHostname = "unknown"
	}
	if sysinfo.Os.Hostname != expectedHostname {
		t.Errorf("expected Hostname=%q (real hostname), got %q", expectedHostname, sysinfo.Os.Hostname)
	}

	// Username should be agent_name/version, not an API key hash.
	if sysinfo.Os.Username != "claude-code/1.0.33" {
		t.Errorf("expected Username=%q, got %q", "claude-code/1.0.33", sysinfo.Os.Username)
	}
}

// ===================================================================
// Test 3: waitForSession should respond quickly (not poll every 1s)
// ===================================================================

func TestE2E_WaitForSession_Latency(t *testing.T) {
	mgr := sessions.NewManager(10 * time.Minute)
	origGlobal := swapGlobalManager(mgr)

	_, rpcClient, cleanup := startTestServer(t)
	defer cleanup()

	b := newTestBridgeWithRPC(t, rpcClient)
	defer cancelAndRestore(b, origGlobal)

	targetID := sessions.ComputeSessionID("delayed-key", "claude-code/1.0.33 (Linux 6.1.0; x86_64)")

	// Create the session after 100ms.
	go func() {
		time.Sleep(100 * time.Millisecond)
		sess := mgr.Touch("delayed-key", "claude-code/1.0.33 (Linux 6.1.0; x86_64)", "claude", "")
		// Signal sessionReady so channel-based waitForSession wakes up.
		b.notifySessionReady(sess.ID)
	}()

	start := time.Now()
	sess := b.waitForSession(targetID, 5*time.Second)
	elapsed := time.Since(start)

	if sess == nil {
		t.Fatal("waitForSession returned nil")
	}
	if sess.ID != targetID {
		t.Errorf("expected session %s, got %s", targetID, sess.ID)
	}
	// With channel-based notification, should respond well under 1 second.
	if elapsed > 500*time.Millisecond {
		t.Errorf("waitForSession took %v, expected < 500ms (channel-based should be ~100ms)", elapsed)
	}
}

// ===================================================================
// Test 4: Full exec command roundtrip through mock gRPC server
// ===================================================================

func TestE2E_SpiteStream_ExecRoundtrip(t *testing.T) {
	srv, rpcClient, cleanup := startTestServer(t)
	defer cleanup()

	mgr := sessions.NewManager(10 * time.Minute)
	origGlobal := swapGlobalManager(mgr)

	b := newTestBridgeWithRPC(t, rpcClient)
	defer cancelAndRestore(b, origGlobal)

	// Open SpiteStream.
	var err error
	b.spiteStream, err = b.rpc.SpiteStream(b.pipelineContext())
	if err != nil {
		t.Fatalf("failed to open SpiteStream: %v", err)
	}

	// Create session with Bash tool.
	sess := mgr.Touch("test-key", "claude-code/1.0.33 (Linux 6.1.0; x86_64)", "claude", "")
	sess.RecordToolsDirect(testClaudeTools)
	b.registered.Store(sess.ID, true)
	b.notifySessionReady(sess.ID)

	// Start receiving commands.
	go b.handleSpiteRecv()

	taskID := uint32(42)

	// Simulate tool result arriving 100ms after command injection.
	simulateToolResult(mgr, sess.ID, taskID, "Exit code: 0\nOutput:\nhello world\n", 200*time.Millisecond)

	// Send ExecRequest via mock server.
	srv.spiteReqCh <- &clientpb.SpiteRequest{
		Session: &clientpb.Session{SessionId: sess.ID},
		Task:    &clientpb.Task{TaskId: taskID},
		Spite: &implantpb.Spite{
			Name: consts.ModuleExecute,
			Body: &implantpb.Spite_ExecRequest{
				ExecRequest: &implantpb.ExecRequest{
					Path: "/bin/sh",
					Args: []string{"-c", "echo hello world"},
				},
			},
		},
	}

	// Wait for response from bridge.
	select {
	case resp := <-srv.spiteRespCh:
		if resp.TaskId != taskID {
			t.Errorf("expected taskID=%d, got %d", taskID, resp.TaskId)
		}
		if resp.SessionId != sess.ID {
			t.Errorf("expected sessionID=%q, got %q", sess.ID, resp.SessionId)
		}
		er := resp.Spite.GetExecResponse()
		if er == nil {
			t.Fatal("expected ExecResponse body")
		}
		if string(er.Stdout) != "hello world\n" {
			t.Errorf("expected stdout=%q, got %q", "hello world\n", string(er.Stdout))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for exec response")
	}
}

// ===================================================================
// Test 5: SpiteStream reconnection after disconnect
// ===================================================================

func TestE2E_SpiteStream_Reconnect(t *testing.T) {
	srv, rpcClient, cleanup := startTestServer(t)
	defer cleanup()

	mgr := sessions.NewManager(10 * time.Minute)
	origGlobal := swapGlobalManager(mgr)

	b := newTestBridgeWithRPC(t, rpcClient)
	defer cancelAndRestore(b, origGlobal)

	// Open SpiteStream.
	var err error
	b.spiteStream, err = b.rpc.SpiteStream(b.pipelineContext())
	if err != nil {
		t.Fatalf("failed to open SpiteStream: %v", err)
	}

	sess := mgr.Touch("test-key", "claude-code/1.0.33 (Linux 6.1.0; x86_64)", "claude", "")
	sess.RecordToolsDirect(testClaudeTools)
	b.registered.Store(sess.ID, true)
	b.notifySessionReady(sess.ID)

	// Start handler.
	recvReady := make(chan struct{})
	go func() {
		close(recvReady)
		b.handleSpiteRecv()
	}()
	<-recvReady

	// Send first command and verify it works.
	taskID1 := uint32(100)
	simulateToolResult(mgr, sess.ID, taskID1, "first", 100*time.Millisecond)
	srv.spiteReqCh <- &clientpb.SpiteRequest{
		Session: &clientpb.Session{SessionId: sess.ID},
		Task:    &clientpb.Task{TaskId: taskID1},
		Spite: &implantpb.Spite{
			Name: consts.ModuleExecute,
			Body: &implantpb.Spite_ExecRequest{
				ExecRequest: &implantpb.ExecRequest{Path: "echo", Args: []string{"first"}},
			},
		},
	}

	select {
	case <-srv.spiteRespCh:
		// First command processed, good.
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for first command response")
	}

	// Force stream disconnection by closing the channel, which causes the
	// mock server's SpiteStream goroutine to exit and the bridge's Recv() to error.
	// The bridge should reconnect automatically.
	close(srv.spiteReqCh)

	// Immediately recreate the request channel so the reconnected stream
	// handler can read from it. (reconnectDelay(1)=2s, so the bridge
	// won't call SpiteStream until ~2s from now.)
	srv.spiteReqCh = make(chan *clientpb.SpiteRequest, 16)

	// Wait for bridge to reconnect (reconnectDelay(1)=2s + stream setup).
	time.Sleep(4 * time.Second)

	// Send second command after reconnection.
	taskID2 := uint32(200)
	simulateToolResult(mgr, sess.ID, taskID2, "second", 200*time.Millisecond)

	select {
	case srv.spiteReqCh <- &clientpb.SpiteRequest{
		Session: &clientpb.Session{SessionId: sess.ID},
		Task:    &clientpb.Task{TaskId: taskID2},
		Spite: &implantpb.Spite{
			Name: consts.ModuleExecute,
			Body: &implantpb.Spite_ExecRequest{
				ExecRequest: &implantpb.ExecRequest{Path: "echo", Args: []string{"second"}},
			},
		},
	}:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout sending second command after reconnection")
	}

	select {
	case resp := <-srv.spiteRespCh:
		if resp.TaskId != taskID2 {
			t.Errorf("expected taskID=%d after reconnect, got %d", taskID2, resp.TaskId)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for response after reconnection")
	}
}

// ===================================================================
// Test 6: Observe events forwarded with correct tapping task ID
// ===================================================================

func TestE2E_ObserveForward(t *testing.T) {
	srv, rpcClient, cleanup := startTestServer(t)
	defer cleanup()

	mgr := sessions.NewManager(10 * time.Minute)
	origGlobal := swapGlobalManager(mgr)

	b := newTestBridgeWithRPC(t, rpcClient)
	defer cancelAndRestore(b, origGlobal)

	// Open SpiteStream.
	var err error
	b.spiteStream, err = b.rpc.SpiteStream(b.pipelineContext())
	if err != nil {
		t.Fatalf("failed to open SpiteStream: %v", err)
	}

	sess := mgr.Touch("test-key", "claude-code/1.0.33 (Linux 6.1.0; x86_64)", "claude", "")
	b.registered.Store(sess.ID, true)

	// Start observing.
	go b.observeSession(sess.ID)
	time.Sleep(100 * time.Millisecond) // let observer subscribe

	// Activate tapping for this session.
	tappingTaskID := uint32(99)
	b.tappingTask.Store(sess.ID, tappingTaskID)

	// Publish an observe event.
	mgr.PublishObserve(sess.ID, &sessions.ObserveEvent{
		Type:      "response",
		SessionID: sess.ID,
		Format:    "claude",
		RawJSON:   `{"type":"message","role":"assistant","content":[{"type":"text","text":"hello"}]}`,
		Timestamp: time.Now(),
	})

	// Wait for the forwarded spite to arrive at the mock server.
	select {
	case resp := <-srv.spiteRespCh:
		if resp.Spite.Name != "llm.observe" {
			t.Errorf("expected spite name %q, got %q", "llm.observe", resp.Spite.Name)
		}
		if resp.TaskId != tappingTaskID {
			t.Errorf("expected taskID=%d (tapping), got %d", tappingTaskID, resp.TaskId)
		}
		if resp.SessionId != sess.ID {
			t.Errorf("expected sessionID=%q, got %q", sess.ID, resp.SessionId)
		}
		llm := resp.Spite.GetLlmEvent()
		if llm == nil {
			t.Fatal("expected LlmEvent body")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for observe event")
	}
}
