package bridge

import (
	"github.com/chainreactors/IoM-go/consts"
	"github.com/chainreactors/IoM-go/proto/client/clientpb"
	log "github.com/sirupsen/logrus"
)

// handleJobStream processes pipeline lifecycle control commands from the C2 server.
func (b *Bridge) handleJobStream() {
	for {
		msg, err := b.jobStream.Recv()
		if err != nil {
			log.Errorf("[bridge] JobStream recv error: %v", err)
			return
		}

		switch msg.Ctrl {
		case consts.CtrlPipelineStart:
			log.Infof("[bridge] pipeline start acknowledged")
		case consts.CtrlPipelineStop:
			log.Infof("[bridge] pipeline stop requested")
		case consts.CtrlPipelineSync:
			log.Infof("[bridge] pipeline sync requested")
		default:
			log.Debugf("[bridge] unhandled job ctrl: %s", msg.Ctrl)
		}

		// Respond with success status, echoing the Job so the server event has pipeline info.
		if err := b.jobStream.Send(&clientpb.JobStatus{
			ListenerId: b.listenerID,
			Ctrl:       msg.Ctrl,
			CtrlId:     msg.Id,
			Status:     int32(consts.CtrlStatusSuccess),
			Job:        msg.Job,
		}); err != nil {
			log.Errorf("[bridge] failed to send job status: %v", err)
			return
		}
	}
}
