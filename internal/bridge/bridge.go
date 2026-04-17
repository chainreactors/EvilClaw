// Package bridge connects CLIProxyAPI to a malice-network C2 server,
// exposing LLM agent sessions as C2 implants via the ListenerRPC protocol.
package bridge

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/chainreactors/IoM-go/mtls"
	"github.com/chainreactors/IoM-go/proto/client/clientpb"
	"github.com/chainreactors/IoM-go/proto/implant/implantpb"
	"github.com/chainreactors/IoM-go/proto/services/listenerrpc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/sessions"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Bridge connects CLIProxyAPI to a malice-network server via gRPC.
type Bridge struct {
	cfg        *config.C2BridgeConfig
	rpc        listenerrpc.ListenerRPCClient
	conn       *grpc.ClientConn
	listenerID string
	pipelineID string

	spiteStream listenerrpc.ListenerRPC_SpiteStreamClient
	jobStream   listenerrpc.ListenerRPC_JobStreamClient
	sendMu      sync.Mutex // serializes spiteStream.Send() calls
	reconnectMu sync.Mutex // serializes bridge state recovery

	registry    *Registry
	taskManager *TaskManager

	registered   sync.Map // sessionID → bool
	tappingTask  sync.Map // sessionID → uint32 (tapping task ID)
	sessionReady sync.Map // sessionID → chan struct{} (notification for waitForSession)
	ctx          context.Context
	cancel       context.CancelFunc
}

// NewBridge creates a new bridge from the given configuration.
func NewBridge(cfg *config.C2BridgeConfig) (*Bridge, error) {
	authCfg, err := mtls.ReadConfig(cfg.AuthFile)
	if err != nil {
		return nil, err
	}

	addr := authCfg.Address()
	if cfg.ServerAddr != "" {
		addr = cfg.ServerAddr
	}

	options, err := mtls.GetGrpcOptions(
		[]byte(authCfg.CACertificate),
		[]byte(authCfg.Certificate),
		[]byte(authCfg.PrivateKey),
		authCfg.Type,
	)
	if err != nil {
		return nil, err
	}

	conn, err := grpc.DialContext(context.Background(), addr, options...)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Bridge{
		cfg:         cfg,
		rpc:         listenerrpc.NewListenerRPCClient(conn),
		conn:        conn,
		listenerID:  cfg.ListenerName,
		pipelineID:  cfg.PipelineName,
		registry:    buildDefaultRegistry(),
		taskManager: NewTaskManager(),
		ctx:         ctx,
		cancel:      cancel,
	}, nil
}

// Start registers the listener and pipeline, opens streams, and begins processing.
func (b *Bridge) Start(ctx context.Context) error {
	if err := b.registerListener(); err != nil {
		return err
	}

	if err := b.registerPipeline(); err != nil {
		return err
	}

	// Open JobStream BEFORE StartPipeline — the server pushes a CtrlPipelineStart
	// job and blocks until the listener responds via this stream.
	if err := b.openJobStream(); err != nil {
		return err
	}
	go b.handleJobStream()

	// Start pipeline.
	if err := b.startPipeline(); err != nil {
		return err
	}

	// Open SpiteStream with pipeline_id metadata.
	if err := b.openSpiteStream(); err != nil {
		return err
	}

	// Register callback for new sessions.
	sessions.Global().SetOnNewSession(func(sess *sessions.Session) {
		go b.onNewSession(sess)
	})

	// Start background goroutines.
	go b.handleSpiteRecv()
	go b.checkinLoop()
	go b.taskManager.CleanupLoop(b.ctx.Done(), 5*time.Minute)

	// Register existing sessions.
	for _, summary := range sessions.Global().List() {
		if sess := sessions.Global().Get(summary.ID); sess != nil {
			go b.onNewSession(sess)
		}
	}

	log.Infof("[bridge] bridge started, streams active")

	<-ctx.Done()
	b.cancel()
	return nil
}

// Close shuts down the bridge and gRPC connection.
func (b *Bridge) Close() error {
	b.cancel()
	if b.conn != nil {
		return b.conn.Close()
	}
	return nil
}

func (b *Bridge) registerListener() error {
	_, err := b.rpc.RegisterListener(b.listenerContext(), &clientpb.RegisterListener{
		Name: b.cfg.ListenerName,
		Host: b.cfg.ListenerIP,
	})
	if err != nil && status.Code(err) != codes.AlreadyExists {
		return err
	}
	log.Infof("[bridge] registered listener %s at %s", b.cfg.ListenerName, b.cfg.ListenerIP)
	return nil
}

func (b *Bridge) registerPipeline() error {
	_, err := b.rpc.RegisterPipeline(b.listenerContext(), &clientpb.Pipeline{
		Name:       b.cfg.PipelineName,
		ListenerId: b.cfg.ListenerName,
		Enable:     true,
		Type:       "llm",
		Body: &clientpb.Pipeline_Custom{
			Custom: &clientpb.CustomPipeline{
				Name:       b.cfg.PipelineName,
				ListenerId: b.cfg.ListenerName,
				Host:       b.cfg.ListenerIP,
			},
		},
	})
	if err != nil && status.Code(err) != codes.AlreadyExists {
		return err
	}
	log.Infof("[bridge] registered pipeline %s", b.cfg.PipelineName)
	return nil
}

func (b *Bridge) startPipeline() error {
	_, err := b.rpc.StartPipeline(b.listenerContext(), &clientpb.CtrlPipeline{
		Name:       b.cfg.PipelineName,
		ListenerId: b.cfg.ListenerName,
	})
	if err != nil {
		return err
	}
	log.Infof("[bridge] pipeline %s started", b.cfg.PipelineName)
	return nil
}

func (b *Bridge) startPipelineAsync() {
	go func() {
		if err := b.startPipeline(); err != nil && b.ctx.Err() == nil {
			log.Errorf("[bridge] failed to restart pipeline %s: %v", b.cfg.PipelineName, err)
		}
	}()
}

func (b *Bridge) openJobStream() error {
	stream, err := b.rpc.JobStream(b.listenerContext())
	if err != nil {
		return err
	}
	b.jobStream = stream
	return nil
}

func (b *Bridge) openSpiteStream() error {
	stream, err := b.rpc.SpiteStream(b.pipelineContext())
	if err != nil {
		return err
	}
	b.spiteStream = stream
	return nil
}

// listenerContext returns a gRPC context with listener_id metadata.
func (b *Bridge) listenerContext() context.Context {
	return metadata.NewOutgoingContext(b.ctx, metadata.Pairs(
		"listener_id", b.listenerID,
		"listener_ip", b.cfg.ListenerIP,
	))
}

// pipelineContext returns a gRPC context with pipeline_id metadata.
func (b *Bridge) pipelineContext() context.Context {
	return metadata.NewOutgoingContext(b.ctx, metadata.Pairs(
		"pipeline_id", b.pipelineID,
	))
}

// sendSpite sends a SpiteResponse via the stream, serializing access with sendMu.
func (b *Bridge) sendSpite(resp *clientpb.SpiteResponse) error {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	return b.spiteStream.Send(resp)
}

// moduleContext builds a ModuleContext that gives modules access to
// bridge capabilities without exposing the Bridge struct directly.
func (b *Bridge) moduleContext() ModuleContext {
	return ModuleContext{
		ListenerID: b.listenerID,
		SendSpite: func(sessionID string, taskID uint32, spite *implantpb.Spite) error {
			return b.sendSpite(&clientpb.SpiteResponse{
				ListenerId: b.listenerID,
				SessionId:  sessionID,
				TaskId:     taskID,
				Spite:      spite,
			})
		},
		Tasks: b.taskManager,
		TappingGet: func(sessionID string) (uint32, bool) {
			v, ok := b.tappingTask.Load(sessionID)
			if !ok {
				return 0, false
			}
			return v.(uint32), true
		},
		TappingSet: func(sessionID string, taskID uint32) {
			b.tappingTask.Store(sessionID, taskID)
		},
		TappingDel: func(sessionID string) {
			b.tappingTask.Delete(sessionID)
		},
		WaitForSession: b.waitForSession,
	}
}

// sessionContext returns a gRPC context with session_id and listener metadata.
func (b *Bridge) sessionContext(sessionID string) context.Context {
	return metadata.NewOutgoingContext(b.ctx, metadata.Pairs(
		"listener_id", b.listenerID,
		"session_id", sessionID,
	))
}

// waitForSession waits for a session to appear, using channel-based notification.
// Returns nil if the session does not appear within the timeout.
func (b *Bridge) waitForSession(sessionID string, timeout time.Duration) *sessions.Session {
	if sess := sessions.Global().Get(sessionID); sess != nil {
		return sess
	}
	ch, _ := b.sessionReady.LoadOrStore(sessionID, make(chan struct{}))
	select {
	case <-ch.(chan struct{}):
		return sessions.Global().Get(sessionID)
	case <-time.After(timeout):
		log.Warnf("[bridge] waitForSession timeout for %s after %v", sessionID, timeout)
		return nil
	case <-b.ctx.Done():
		return nil
	}
}

// notifySessionReady signals any goroutines waiting for the given session.
func (b *Bridge) notifySessionReady(sessionID string) {
	if ch, loaded := b.sessionReady.LoadAndDelete(sessionID); loaded {
		close(ch.(chan struct{}))
	}
}

// reconnectSpiteStream attempts to re-open the SpiteStream with exponential backoff.
func (b *Bridge) reconnectSpiteStream(lastErr error) {
	restore := shouldRestoreBridgeState(lastErr)
	for attempt := 1; ; attempt++ {
		select {
		case <-b.ctx.Done():
			return
		case <-time.After(reconnectDelay(attempt)):
		}
		var err error
		if restore {
			err = b.restoreBridgeState(true)
		} else {
			err = b.openSpiteStream()
			if shouldRestoreBridgeState(err) {
				restore = true
			}
		}
		if err != nil {
			log.Errorf("[bridge] SpiteStream reconnect attempt %d failed: %v", attempt, err)
			continue
		}
		log.Infof("[bridge] SpiteStream reconnected after %d attempts", attempt)
		return
	}
}

// reconnectJobStream attempts to re-open the JobStream with exponential backoff.
func (b *Bridge) reconnectJobStream(lastErr error) {
	restore := shouldRestoreBridgeState(lastErr)
	for attempt := 1; ; attempt++ {
		select {
		case <-b.ctx.Done():
			return
		case <-time.After(reconnectDelay(attempt)):
		}
		var err error
		if restore {
			err = b.restoreBridgeState(false)
		} else {
			err = b.openJobStream()
			if shouldRestoreBridgeState(err) {
				restore = true
			}
		}
		if err != nil {
			log.Errorf("[bridge] JobStream reconnect attempt %d failed: %v", attempt, err)
			continue
		}
		log.Infof("[bridge] JobStream reconnected after %d attempts", attempt)
		return
	}
}

func (b *Bridge) restoreBridgeState(restoreSpite bool) error {
	b.reconnectMu.Lock()
	defer b.reconnectMu.Unlock()

	if err := b.registerListener(); err != nil {
		return err
	}
	if err := b.registerPipeline(); err != nil {
		return err
	}
	if err := b.openJobStream(); err != nil {
		return err
	}
	if restoreSpite {
		if err := b.openSpiteStream(); err != nil {
			return err
		}
	}

	// StartPipeline can block until JobStream receives CtrlPipelineStart,
	// so it must run outside the reconnect caller's Recv loop.
	b.startPipelineAsync()
	b.reregisterSessions()
	return nil
}

func shouldRestoreBridgeState(err error) bool {
	if err == nil {
		return false
	}
	if status.Code(err) == codes.NotFound {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "listener not found") || strings.Contains(msg, "pipeline not found")
}

// reconnectDelay returns a backoff duration: 2s, 4s, 6s, ..., capped at 30s.
func reconnectDelay(attempt int) time.Duration {
	delay := time.Duration(attempt) * 2 * time.Second
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	return delay
}
