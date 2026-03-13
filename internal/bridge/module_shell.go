package bridge

import (
	"fmt"

	"github.com/chainreactors/IoM-go/consts"
	"github.com/chainreactors/IoM-go/proto/implant/implantpb"
	log "github.com/sirupsen/logrus"
)

// ShellModule wraps a shell command execution pattern into the Module interface.
// It builds an OS-specific shell command, injects it via the session's shell tool,
// waits for the result, and optionally parses it into a structured Spite response.
type ShellModule struct {
	name      string
	spiteName string

	// buildCommand generates the OS-specific shell command string.
	buildCommand func(osName string, req *implantpb.Request) string

	// buildResponse parses exec output into a Spite body.
	// If nil, the default ExecResponse wrapping is used.
	buildResponse func(output, osName string, req *implantpb.Request) *implantpb.Spite
}

// NewShellModule creates a ShellModule with the given command builder and optional response parser.
func NewShellModule(
	name, spiteName string,
	buildCmd func(string, *implantpb.Request) string,
	buildResp func(string, string, *implantpb.Request) *implantpb.Spite,
) *ShellModule {
	return &ShellModule{
		name:          name,
		spiteName:     spiteName,
		buildCommand:  buildCmd,
		buildResponse: buildResp,
	}
}

// Name returns the module's spite name.
func (m *ShellModule) Name() string { return m.name }

// Handle processes a shell module command.
func (m *ShellModule) Handle(ctx ModuleContext, sessionID string, taskID uint32, spite *implantpb.Spite) {
	ctx.Tasks.Create(sessionID, taskID, m.name)

	sess, toolName := acquireShellSession(ctx, sessionID, taskID, m.name)
	if sess == nil {
		return
	}

	// Determine OS from User-Agent.
	info := parseUserAgentFull(sess.UserAgent)
	osName := info.osName

	// Extract request args.
	var req *implantpb.Request
	if r := spite.GetRequest(); r != nil {
		req = r
	} else {
		req = &implantpb.Request{}
	}

	// Build the OS-specific command.
	command := m.buildCommand(osName, req)
	if command == "" {
		ctx.SendSpite(sessionID, taskID, execSpite(fmt.Sprintf("cannot build command for module %s on %s", m.name, osName)))
		ctx.Tasks.Fail(sessionID, taskID, "empty command")
		return
	}

	result := enqueueAndAwait(ctx, sessionID, taskID, sess, toolName, command)
	if result == nil {
		return
	}

	output := extractPlainOutput(result.Output)

	var resultSpite *implantpb.Spite
	if m.buildResponse != nil {
		resultSpite = m.buildResponse(output, osName, req)
	}
	if resultSpite == nil {
		// Fallback to ExecResponse.
		resp := parseToolOutput(result.Output)
		resp.End = true
		resultSpite = &implantpb.Spite{
			Name: consts.ModuleExecute,
			Body: &implantpb.Spite_ExecResponse{ExecResponse: resp},
		}
	}

	if err := ctx.SendSpite(sessionID, taskID, resultSpite); err != nil {
		log.Errorf("[bridge] failed to forward module %s task %d: %v", m.name, taskID, err)
		ctx.Tasks.Fail(sessionID, taskID, err.Error())
	} else {
		log.Infof("[bridge] forwarded module %s task %d result for session %s", m.name, taskID, sessionID)
		ctx.Tasks.Complete(sessionID, taskID)
	}
}
