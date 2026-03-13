package management

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/sessions"
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// GetSessions returns all active sessions.
func (h *Handler) GetSessions(c *gin.Context) {
	list := sessions.Global().List()
	c.JSON(200, gin.H{"sessions": list})
}

// GetSession returns a single session with its tools.
func (h *Handler) GetSession(c *gin.Context) {
	id := c.Param("id")
	sess := sessions.Global().Get(id)
	if sess == nil {
		c.JSON(404, gin.H{"error": "session not found"})
		return
	}
	c.JSON(200, sess)
}

// PostSessionExec enqueues a command for execution in a session.
func (h *Handler) PostSessionExec(c *gin.Context) {
	id := c.Param("id")
	sess := sessions.Global().Get(id)
	if sess == nil {
		c.JSON(404, gin.H{"error": "session not found"})
		return
	}

	var body struct {
		Command string `json:"command"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Command == "" {
		c.JSON(400, gin.H{"error": "missing command"})
		return
	}

	// Pick the best shell tool for this session.
	toolName := sessions.PickShellTool(sess)
	if toolName == "" {
		c.JSON(400, gin.H{"error": "no shell tool found in session"})
		return
	}

	args := sessions.BuildCommandArguments(sess, toolName, body.Command)
	cmdID := sessions.GenerateCommandID()

	action := &sessions.PendingAction{
		ID:        cmdID,
		Type:      sessions.ActionToolCall,
		ToolName:  toolName,
		Arguments: args,
		CreatedAt: time.Now(),
	}

	if !sessions.Global().EnqueueAction(id, action) {
		c.JSON(500, gin.H{"error": "failed to enqueue command"})
		return
	}

	c.JSON(200, gin.H{
		"command_id": cmdID,
		"tool_name":  toolName,
		"arguments":  args,
		"status":     "pending",
	})
}

// GetSessionObserve upgrades to WebSocket and streams observe events (request/response) for a session.
func (h *Handler) GetSessionObserve(c *gin.Context) {
	id := c.Param("id")
	sess := sessions.Global().Get(id)
	if sess == nil {
		c.JSON(404, gin.H{"error": "session not found"})
		return
	}

	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	subID := sessions.GenerateCommandID()
	ch := sessions.Global().SubscribeObserve(id, subID)
	if ch == nil {
		return
	}
	defer sessions.Global().UnsubscribeObserve(id, subID)

	// Read goroutine to detect client disconnect.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return
			}
			if err := conn.WriteJSON(event); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}

// GetSessionWS upgrades to WebSocket and streams command results for a session.
func (h *Handler) GetSessionWS(c *gin.Context) {
	id := c.Param("id")
	sess := sessions.Global().Get(id)
	if sess == nil {
		c.JSON(404, gin.H{"error": "session not found"})
		return
	}

	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	subID := sessions.GenerateCommandID() // reuse as unique sub ID
	ch := sessions.Global().Subscribe(id, subID)
	if ch == nil {
		return
	}
	defer sessions.Global().Unsubscribe(id, subID)

	// Read goroutine to detect client disconnect.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case result, ok := <-ch:
			if !ok {
				return
			}
			if err := conn.WriteJSON(result); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}
