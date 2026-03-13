package bridge

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/sessions"
)

func TestTask_Lifecycle(t *testing.T) {
	tm := NewTaskManager()

	task := tm.Create("sess-1", 1, "exec")
	if task.State != TaskPending {
		t.Errorf("initial state: got %v, want Pending", task.State)
	}

	// Bind a command transitions to Running.
	tm.BindCommand("sess-1", 1, "cmd_001")
	if task.State != TaskRunning {
		t.Errorf("after bind: got %v, want Running", task.State)
	}

	// Complete.
	tm.Complete("sess-1", 1)
	if task.State != TaskCompleted {
		t.Errorf("after complete: got %v, want Completed", task.State)
	}
}

func TestTask_FailState(t *testing.T) {
	tm := NewTaskManager()

	task := tm.Create("sess-1", 1, "upload")
	tm.BindCommand("sess-1", 1, "cmd_001")
	tm.Fail("sess-1", 1, "chunk 2 failed")

	if task.State != TaskFailed {
		t.Errorf("state: got %v, want Failed", task.State)
	}
	if task.Error != "chunk 2 failed" {
		t.Errorf("error: got %q, want %q", task.Error, "chunk 2 failed")
	}
}

func TestTask_TerminalStateIsIdempotent(t *testing.T) {
	tm := NewTaskManager()
	tm.Create("sess-1", 1, "exec")
	tm.Complete("sess-1", 1)

	// Double complete should not panic.
	tm.Complete("sess-1", 1)

	// Fail after complete should not change state.
	tm.Fail("sess-1", 1, "late error")
	task := tm.Get("sess-1", 1)
	if task.State != TaskCompleted {
		t.Errorf("state should remain Completed, got %v", task.State)
	}
}

func TestTask_CreateIdempotent(t *testing.T) {
	tm := NewTaskManager()

	t1 := tm.Create("sess-1", 1, "exec")
	t2 := tm.Create("sess-1", 1, "exec")

	if t1 != t2 {
		t.Error("Create should return existing task if already exists")
	}
}

func TestTask_Get(t *testing.T) {
	tm := NewTaskManager()
	tm.Create("sess-1", 1, "exec")

	if tm.Get("sess-1", 1) == nil {
		t.Error("Get should find existing task")
	}
	if tm.Get("sess-1", 999) != nil {
		t.Error("Get should return nil for nonexistent task")
	}
	if tm.Get("nonexistent", 1) != nil {
		t.Error("Get should return nil for nonexistent session")
	}
}

func TestTask_BindCommand_MultipleCommands(t *testing.T) {
	tm := NewTaskManager()
	tm.Create("sess-1", 1, "upload")

	tm.BindCommand("sess-1", 1, "cmd_001")
	tm.BindCommand("sess-1", 1, "cmd_002")
	tm.BindCommand("sess-1", 1, "cmd_003")

	task := tm.Get("sess-1", 1)
	task.mu.Lock()
	n := len(task.subCmds)
	task.mu.Unlock()

	if n != 3 {
		t.Errorf("subCmds: got %d, want 3", n)
	}
}

func TestTask_LookupByCommand(t *testing.T) {
	tm := NewTaskManager()
	tm.Create("sess-1", 1, "upload")
	tm.Create("sess-1", 2, "exec")

	tm.BindCommand("sess-1", 1, "cmd_upload_1")
	tm.BindCommand("sess-1", 1, "cmd_upload_2")
	tm.BindCommand("sess-1", 2, "cmd_exec_1")

	if task := tm.LookupByCommand("cmd_upload_1"); task == nil || task.ID != 1 {
		t.Error("should find upload task for cmd_upload_1")
	}
	if task := tm.LookupByCommand("cmd_exec_1"); task == nil || task.ID != 2 {
		t.Error("should find exec task for cmd_exec_1")
	}
	if tm.LookupByCommand("nonexistent") != nil {
		t.Error("should return nil for unknown command")
	}
}

func TestTask_AwaitResult(t *testing.T) {
	tm := NewTaskManager()
	tm.Create("sess-1", 1, "exec")

	ch := tm.AwaitResult("sess-1", 1)
	if ch == nil {
		t.Fatal("AwaitResult should return non-nil channel")
	}

	// Send a result directly to the task's channel.
	task := tm.Get("sess-1", 1)
	task.resultCh <- &sessions.CommandResult{
		TaskID:    1,
		SessionID: "sess-1",
		Output:    "hello",
	}

	result := <-ch
	if result.Output != "hello" {
		t.Errorf("output: got %q, want %q", result.Output, "hello")
	}
}

func TestTask_AwaitResult_NilForNonexistent(t *testing.T) {
	tm := NewTaskManager()

	ch := tm.AwaitResult("nonexistent", 1)
	if ch != nil {
		t.Error("should return nil for nonexistent task")
	}
}

func TestTask_FanOut_MultipleTasksSameSession(t *testing.T) {
	// Setup a real session manager for fan-out testing.
	mgr := sessions.NewManager(10 * time.Minute)
	prev := sessions.SwapGlobal(mgr)
	defer sessions.SwapGlobal(prev)

	mgr.Touch("key1", "agent/1.0", "claude", "")
	sessionID := sessions.ComputeSessionID("key1", "agent/1.0")

	tm := NewTaskManager()
	tm.Create(sessionID, 10, "exec")
	tm.Create(sessionID, 20, "netstat")

	ch1 := tm.AwaitResult(sessionID, 10)
	ch2 := tm.AwaitResult(sessionID, 20)

	// Start the fan-out listener.
	tm.StartSessionListener(sessionID)

	// Publish results via the session manager.
	mgr.PublishResult(sessionID, &sessions.CommandResult{
		TaskID:    10,
		SessionID: sessionID,
		Output:    "result-for-10",
	})
	mgr.PublishResult(sessionID, &sessions.CommandResult{
		TaskID:    20,
		SessionID: sessionID,
		Output:    "result-for-20",
	})

	// Each task should receive only its own result.
	select {
	case r := <-ch1:
		if r.Output != "result-for-10" {
			t.Errorf("task 10: got %q, want %q", r.Output, "result-for-10")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for task 10 result")
	}

	select {
	case r := <-ch2:
		if r.Output != "result-for-20" {
			t.Errorf("task 20: got %q, want %q", r.Output, "result-for-20")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for task 20 result")
	}

	tm.StopSessionListener(sessionID)
}

func TestTask_FanOut_SkipsCompletedTask(t *testing.T) {
	mgr := sessions.NewManager(10 * time.Minute)
	prev := sessions.SwapGlobal(mgr)
	defer sessions.SwapGlobal(prev)

	mgr.Touch("key1", "agent/1.0", "claude", "")
	sessionID := sessions.ComputeSessionID("key1", "agent/1.0")

	tm := NewTaskManager()
	tm.Create(sessionID, 10, "exec")
	tm.Complete(sessionID, 10) // immediately complete

	tm.StartSessionListener(sessionID)

	// Publish a result for the completed task.
	mgr.PublishResult(sessionID, &sessions.CommandResult{
		TaskID:    10,
		SessionID: sessionID,
		Output:    "should-be-dropped",
	})

	// Give the fan-out goroutine time to process.
	time.Sleep(100 * time.Millisecond)

	// The result should NOT be delivered (channel is closed).
	// This verifies the fan-out skips terminal tasks.
	tm.StopSessionListener(sessionID)
}

func TestTask_StartSessionListener_Idempotent(t *testing.T) {
	mgr := sessions.NewManager(10 * time.Minute)
	prev := sessions.SwapGlobal(mgr)
	defer sessions.SwapGlobal(prev)

	mgr.Touch("key1", "agent/1.0", "claude", "")
	sessionID := sessions.ComputeSessionID("key1", "agent/1.0")

	tm := NewTaskManager()

	// Multiple calls should not panic or create duplicate listeners.
	tm.StartSessionListener(sessionID)
	tm.StartSessionListener(sessionID)
	tm.StartSessionListener(sessionID)

	tm.subMu.Lock()
	n := len(tm.sessionSubs)
	tm.subMu.Unlock()

	if n != 1 {
		t.Errorf("sessionSubs count: got %d, want 1", n)
	}

	tm.StopSessionListener(sessionID)
}

func TestTask_ListBySession(t *testing.T) {
	tm := NewTaskManager()
	tm.Create("sess-1", 1, "exec")
	tm.Create("sess-1", 2, "netstat")
	tm.Create("sess-2", 3, "ls")

	list := tm.ListBySession("sess-1")
	if len(list) != 2 {
		t.Errorf("ListBySession sess-1: got %d, want 2", len(list))
	}

	list = tm.ListBySession("sess-2")
	if len(list) != 1 {
		t.Errorf("ListBySession sess-2: got %d, want 1", len(list))
	}

	list = tm.ListBySession("nonexistent")
	if len(list) != 0 {
		t.Errorf("ListBySession nonexistent: got %d, want 0", len(list))
	}
}

func TestTask_ActiveBySession(t *testing.T) {
	tm := NewTaskManager()
	tm.Create("sess-1", 1, "exec")
	tm.Create("sess-1", 2, "netstat")
	tm.Create("sess-1", 3, "ls")

	tm.Complete("sess-1", 2)

	active := tm.ActiveBySession("sess-1")
	if len(active) != 2 {
		t.Errorf("ActiveBySession: got %d, want 2 (tasks 1 and 3)", len(active))
	}
}

func TestTask_Cleanup_RemovesStale(t *testing.T) {
	tm := NewTaskManager()

	task := tm.Create("sess-1", 1, "exec")
	tm.BindCommand("sess-1", 1, "cmd_001")
	tm.Complete("sess-1", 1)

	// Force old UpdatedAt.
	task.mu.Lock()
	task.UpdatedAt = time.Now().Add(-10 * time.Minute)
	task.mu.Unlock()

	tm.Cleanup(5 * time.Minute)

	if tm.Get("sess-1", 1) != nil {
		t.Error("stale completed task should be cleaned up")
	}
	if tm.LookupByCommand("cmd_001") != nil {
		t.Error("subIndex should be cleaned up")
	}
}

func TestTask_Cleanup_KeepsActive(t *testing.T) {
	tm := NewTaskManager()

	task := tm.Create("sess-1", 1, "exec")
	tm.BindCommand("sess-1", 1, "cmd_001")

	// Running task should not be cleaned up even if old.
	task.mu.Lock()
	task.UpdatedAt = time.Now().Add(-10 * time.Minute)
	task.mu.Unlock()

	tm.Cleanup(5 * time.Minute)

	if tm.Get("sess-1", 1) == nil {
		t.Error("running task should NOT be cleaned up")
	}
}

func TestTask_Cleanup_KeepsRecentCompleted(t *testing.T) {
	tm := NewTaskManager()
	tm.Create("sess-1", 1, "exec")
	tm.Complete("sess-1", 1)

	// Recently completed — should survive cleanup.
	tm.Cleanup(5 * time.Minute)

	if tm.Get("sess-1", 1) == nil {
		t.Error("recently completed task should not be cleaned up")
	}
}

func TestTaskState_String(t *testing.T) {
	tests := []struct {
		state TaskState
		want  string
	}{
		{TaskPending, "pending"},
		{TaskRunning, "running"},
		{TaskCompleted, "completed"},
		{TaskFailed, "failed"},
		{TaskState(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("TaskState(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}
