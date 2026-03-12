package sessions

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
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
	globalManager     *Manager
	globalManagerOnce sync.Once
)

// Global returns the process-wide session manager.
func Global() *Manager {
	globalManagerOnce.Do(func() {
		globalManager = NewManager(defaultExpiry)
		go globalManager.cleanupLoop()
	})
	return globalManager
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
func (m *Manager) Touch(apiKey, userAgent, format string) *Session {
	id := ComputeSessionID(apiKey, userAgent)
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
		ID:           id,
		APIKeyHash:   hashKey(apiKey),
		UserAgent:    userAgent,
		Format:       format,
		CreatedAt:    now,
		LastActivity: now,
		subscribers:  make(map[string]chan *CommandResult),
		observers:    make(map[string]chan *ObserveEvent),
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

// EnqueueCommand adds a pending command to a session.
func (m *Manager) EnqueueCommand(sessionID string, cmd *PendingCommand) bool {
	sess := m.Get(sessionID)
	if sess == nil {
		return false
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.pending = append(sess.pending, cmd)
	return true
}

// DequeueCommand removes and returns the first pending command, or nil.
func (m *Manager) DequeueCommand(sessionID string) *PendingCommand {
	sess := m.Get(sessionID)
	if sess == nil {
		return nil
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.pending) == 0 {
		return nil
	}
	cmd := sess.pending[0]
	sess.pending = sess.pending[1:]
	return cmd
}

// PushInflightTask records a C2 task ID for a dequeued command awaiting its result.
func (m *Manager) PushInflightTask(sessionID string, taskID uint32) {
	sess := m.Get(sessionID)
	if sess == nil {
		return
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.inflightTaskIDs = append(sess.inflightTaskIDs, taskID)
}

// PopInflightTask returns and removes the oldest inflight task ID, or 0 if none.
func (m *Manager) PopInflightTask(sessionID string) uint32 {
	sess := m.Get(sessionID)
	if sess == nil {
		return 0
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.inflightTaskIDs) == 0 {
		return 0
	}
	taskID := sess.inflightTaskIDs[0]
	sess.inflightTaskIDs = sess.inflightTaskIDs[1:]
	return taskID
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
		return
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for _, ch := range sess.subscribers {
		select {
		case ch <- result:
		default:
			// subscriber channel full, skip
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
		expired := sess.LastActivity.Before(cutoff)
		sess.mu.Unlock()
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
