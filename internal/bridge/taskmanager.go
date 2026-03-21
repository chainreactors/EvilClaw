package bridge

import (
	"fmt"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/sessions"
	log "github.com/sirupsen/logrus"
)

// TaskState represents the lifecycle state of a C2 task.
type TaskState int

const (
	TaskPending   TaskState = iota // Task created, not yet executing
	TaskRunning                    // At least one command has been injected
	TaskCompleted                  // All commands finished successfully
	TaskFailed                     // Task encountered an error
)

const (
	taskResultBufferSize = 8
)

// String returns a human-readable representation of a TaskState.
func (s TaskState) String() string {
	switch s {
	case TaskPending:
		return "pending"
	case TaskRunning:
		return "running"
	case TaskCompleted:
		return "completed"
	case TaskFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// Task represents a single C2 task with its lifecycle and associated commands.
type Task struct {
	ID        uint32
	SessionID string
	Module    string // spite.Name that created this task
	State     TaskState
	Error     string // populated when State == TaskFailed
	CreatedAt time.Time
	UpdatedAt time.Time

	// resultCh receives command results routed by the fan-out listener.
	// One channel per task replaces the scattered per-waiter subscribe pattern.
	resultCh chan *sessions.CommandResult

	// subCmds tracks all injected command IDs belonging to this task.
	// For multi-step operations (e.g. chunked upload), multiple commands
	// map to a single task.
	subCmds []string

	mu sync.Mutex
}

// taskKey uniquely identifies a task within the manager.
type taskKey struct {
	sessionID string
	taskID    uint32
}

// TaskManager provides centralized task lifecycle management for the bridge.
// It replaces the scattered subscribe/unsubscribe + taskID-filtering pattern
// with a single per-session subscriber that fans out results to per-task channels.
type TaskManager struct {
	mu       sync.RWMutex
	tasks    map[taskKey]*Task
	subIndex map[string]*Task // commandID → Task reverse lookup

	// sessionSubs tracks active per-session listener goroutines.
	// Key: sessionID, Value: subID used with sessions.Global().Subscribe.
	sessionSubs map[string]string
	subMu       sync.Mutex // protects sessionSubs
}

// NewTaskManager creates a new TaskManager.
func NewTaskManager() *TaskManager {
	return &TaskManager{
		tasks:       make(map[taskKey]*Task),
		subIndex:    make(map[string]*Task),
		sessionSubs: make(map[string]string),
	}
}

// Create registers a new task in Pending state.
// If a task with the same sessionID+taskID already exists, it is returned as-is.
func (tm *TaskManager) Create(sessionID string, taskID uint32, module string) *Task {
	key := taskKey{sessionID, taskID}
	now := time.Now()

	tm.mu.Lock()
	defer tm.mu.Unlock()

	if existing, ok := tm.tasks[key]; ok {
		return existing
	}

	task := &Task{
		ID:        taskID,
		SessionID: sessionID,
		Module:    module,
		State:     TaskPending,
		CreatedAt: now,
		UpdatedAt: now,
		resultCh:  make(chan *sessions.CommandResult, taskResultBufferSize),
	}
	tm.tasks[key] = task
	return task
}

// Get returns the task for the given sessionID+taskID, or nil.
func (tm *TaskManager) Get(sessionID string, taskID uint32) *Task {
	key := taskKey{sessionID, taskID}
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.tasks[key]
}

// ListBySession returns all tasks for a session (any state).
func (tm *TaskManager) ListBySession(sessionID string) []*Task {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	var out []*Task
	for k, t := range tm.tasks {
		if k.sessionID == sessionID {
			out = append(out, t)
		}
	}
	return out
}

// ActiveBySession returns tasks in Pending or Running state for a session.
func (tm *TaskManager) ActiveBySession(sessionID string) []*Task {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	var out []*Task
	for k, t := range tm.tasks {
		if k.sessionID == sessionID {
			t.mu.Lock()
			active := t.State == TaskPending || t.State == TaskRunning
			t.mu.Unlock()
			if active {
				out = append(out, t)
			}
		}
	}
	return out
}

// BindCommand associates an injected command ID with a task.
// This enables reverse lookup from command results to tasks.
func (tm *TaskManager) BindCommand(sessionID string, taskID uint32, cmdID string) {
	key := taskKey{sessionID, taskID}

	tm.mu.Lock()
	defer tm.mu.Unlock()

	task, ok := tm.tasks[key]
	if !ok {
		return
	}

	task.mu.Lock()
	task.subCmds = append(task.subCmds, cmdID)
	if task.State == TaskPending {
		task.State = TaskRunning
	}
	task.UpdatedAt = time.Now()
	task.mu.Unlock()

	tm.subIndex[cmdID] = task
}

// LookupByCommand returns the task associated with a command ID, or nil.
func (tm *TaskManager) LookupByCommand(cmdID string) *Task {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.subIndex[cmdID]
}

// AwaitResult returns the task's result channel for reading.
// Callers should read from this channel to receive results routed by the fan-out.
func (tm *TaskManager) AwaitResult(sessionID string, taskID uint32) <-chan *sessions.CommandResult {
	task := tm.Get(sessionID, taskID)
	if task == nil {
		return nil
	}
	return task.resultCh
}

// Complete marks a task as completed and closes its result channel.
func (tm *TaskManager) Complete(sessionID string, taskID uint32) {
	task := tm.Get(sessionID, taskID)
	if task == nil {
		return
	}
	task.mu.Lock()
	defer task.mu.Unlock()

	if task.State == TaskCompleted || task.State == TaskFailed {
		return // already terminal
	}
	task.State = TaskCompleted
	task.UpdatedAt = time.Now()
	close(task.resultCh)
}

// Fail marks a task as failed with a reason and closes its result channel.
func (tm *TaskManager) Fail(sessionID string, taskID uint32, reason string) {
	task := tm.Get(sessionID, taskID)
	if task == nil {
		return
	}
	task.mu.Lock()
	defer task.mu.Unlock()

	if task.State == TaskCompleted || task.State == TaskFailed {
		return // already terminal
	}
	task.State = TaskFailed
	task.Error = reason
	task.UpdatedAt = time.Now()
	close(task.resultCh)
}

// StartSessionListener ensures a single subscriber exists for the given session.
// The subscriber fans out incoming results to per-task channels based on TaskID.
// This is idempotent — calling it multiple times for the same session is safe.
func (tm *TaskManager) StartSessionListener(sessionID string) {
	tm.subMu.Lock()
	if _, exists := tm.sessionSubs[sessionID]; exists {
		tm.subMu.Unlock()
		return
	}

	subID := fmt.Sprintf("bridge-tm-%s", sessionID)
	ch := sessions.Global().Subscribe(sessionID, subID)
	if ch == nil {
		tm.subMu.Unlock()
		log.Warnf("[taskmanager] failed to subscribe to session %s", sessionID)
		return
	}

	tm.sessionSubs[sessionID] = subID
	tm.subMu.Unlock()

	go tm.fanOutLoop(sessionID, ch)
}

// fanOutLoop reads from a session's result channel and routes each result
// to the corresponding task's resultCh based on TaskID.
func (tm *TaskManager) fanOutLoop(sessionID string, ch <-chan *sessions.CommandResult) {
	log.Infof("[taskmanager] fanOutLoop started for session %s", sessionID)
	for result := range ch {
		log.Infof("[taskmanager] fanOutLoop received result for session=%s taskID=%d cmdID=%s output_len=%d",
			sessionID, result.TaskID, result.CommandID, len(result.Output))
		key := taskKey{sessionID, result.TaskID}

		tm.mu.RLock()
		task, ok := tm.tasks[key]
		tm.mu.RUnlock()

		if !ok {
			log.Warnf("[taskmanager] no task for session=%s taskID=%d, skipping", sessionID, result.TaskID)
			continue
		}

		task.mu.Lock()
		if task.State == TaskCompleted || task.State == TaskFailed {
			task.mu.Unlock()
			continue
		}
		task.UpdatedAt = time.Now()
		task.mu.Unlock()

		// Non-blocking send — drop if buffer is full.
		select {
		case task.resultCh <- result:
		default:
			log.Warnf("[taskmanager] result channel full for session=%s taskID=%d", sessionID, result.TaskID)
		}
	}

	// Channel closed (session expired or unsubscribed).
	tm.subMu.Lock()
	delete(tm.sessionSubs, sessionID)
	tm.subMu.Unlock()
	log.Debugf("[taskmanager] session listener ended for %s", sessionID)
}

// StopSessionListener unsubscribes the fan-out listener for a session.
func (tm *TaskManager) StopSessionListener(sessionID string) {
	tm.subMu.Lock()
	subID, ok := tm.sessionSubs[sessionID]
	if !ok {
		tm.subMu.Unlock()
		return
	}
	delete(tm.sessionSubs, sessionID)
	tm.subMu.Unlock()

	sessions.Global().Unsubscribe(sessionID, subID)
}

// Cleanup removes tasks that have been in a terminal state (Completed/Failed)
// for longer than maxAge, and cleans up their subIndex entries.
func (tm *TaskManager) Cleanup(maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)

	tm.mu.Lock()
	defer tm.mu.Unlock()

	for key, task := range tm.tasks {
		task.mu.Lock()
		terminal := task.State == TaskCompleted || task.State == TaskFailed
		stale := task.UpdatedAt.Before(cutoff)
		cmds := task.subCmds
		task.mu.Unlock()

		if terminal && stale {
			for _, cmdID := range cmds {
				delete(tm.subIndex, cmdID)
			}
			delete(tm.tasks, key)
		}
	}
}

// CleanupLoop runs Cleanup periodically until ctx is cancelled.
func (tm *TaskManager) CleanupLoop(done <-chan struct{}, maxAge time.Duration) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			tm.Cleanup(maxAge)
		case <-done:
			return
		}
	}
}
