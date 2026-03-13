package bridge

import (
	"github.com/chainreactors/IoM-go/consts"
	"github.com/chainreactors/IoM-go/proto/implant/implantpb"
	log "github.com/sirupsen/logrus"
)

// ExecModule handles the "exec" C2 command by injecting a shell command
// into the session and forwarding the result.
type ExecModule struct{}

func (m *ExecModule) Name() string { return consts.ModuleExecute }

func (m *ExecModule) Handle(ctx ModuleContext, sessionID string, taskID uint32, spite *implantpb.Spite) {
	ctx.Tasks.Create(sessionID, taskID, m.Name())

	exec := spite.GetExecRequest()
	if exec == nil {
		ctx.SendSpite(sessionID, taskID, execSpite("missing ExecRequest"))
		ctx.Tasks.Fail(sessionID, taskID, "missing ExecRequest")
		return
	}

	command := extractCommand(exec.Path, exec.Args)

	sess, toolName := acquireShellSession(ctx, sessionID, taskID, m.Name())
	if sess == nil {
		return
	}

	result := enqueueAndAwait(ctx, sessionID, taskID, sess, toolName, command)
	if result == nil {
		return
	}

	resp := parseToolOutput(result.Output)
	resp.End = true
	resultSpite := &implantpb.Spite{
		Name: consts.ModuleExecute,
		Body: &implantpb.Spite_ExecResponse{ExecResponse: resp},
	}

	if err := ctx.SendSpite(sessionID, taskID, resultSpite); err != nil {
		log.Errorf("[bridge] failed to forward exec task %d: %v", taskID, err)
		ctx.Tasks.Fail(sessionID, taskID, err.Error())
	} else {
		log.Infof("[bridge] forwarded exec task %d result for session %s", taskID, sessionID)
		ctx.Tasks.Complete(sessionID, taskID)
	}
}
