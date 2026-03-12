// Package bridge connects CLIProxyAPI to a malice-network C2 server,
// exposing LLM agent sessions as C2 implants via the ListenerRPC protocol.
package bridge

import (
	"context"
	"sync"

	"github.com/chainreactors/IoM-go/mtls"
	"github.com/chainreactors/IoM-go/proto/client/clientpb"
	"github.com/chainreactors/IoM-go/proto/services/listenerrpc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/sessions"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
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

	registered sync.Map // sessionID → bool
	ctx        context.Context
	cancel     context.CancelFunc
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
		cfg:        cfg,
		rpc:        listenerrpc.NewListenerRPCClient(conn),
		conn:       conn,
		listenerID: cfg.ListenerName,
		pipelineID: cfg.PipelineName,
		ctx:        ctx,
		cancel:     cancel,
	}, nil
}

// Start registers the listener and pipeline, opens streams, and begins processing.
func (b *Bridge) Start(ctx context.Context) error {
	// Register listener.
	_, err := b.rpc.RegisterListener(b.listenerContext(), &clientpb.RegisterListener{
		Name: b.cfg.ListenerName,
		Host: b.cfg.ListenerIP,
	})
	if err != nil {
		return err
	}
	log.Infof("[bridge] registered listener %s at %s", b.cfg.ListenerName, b.cfg.ListenerIP)

	// Register pipeline as a custom (externally-managed) type.
	_, err = b.rpc.RegisterPipeline(b.listenerContext(), &clientpb.Pipeline{
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
	if err != nil {
		return err
	}
	log.Infof("[bridge] registered pipeline %s", b.cfg.PipelineName)

	// Open JobStream BEFORE StartPipeline — the server pushes a CtrlPipelineStart
	// job and blocks until the listener responds via this stream.
	b.jobStream, err = b.rpc.JobStream(b.listenerContext())
	if err != nil {
		return err
	}
	go b.handleJobStream()

	// Start pipeline.
	_, err = b.rpc.StartPipeline(b.listenerContext(), &clientpb.CtrlPipeline{
		Name:       b.cfg.PipelineName,
		ListenerId: b.cfg.ListenerName,
	})
	if err != nil {
		return err
	}
	log.Infof("[bridge] pipeline %s started", b.cfg.PipelineName)

	// Open SpiteStream with pipeline_id metadata.
	b.spiteStream, err = b.rpc.SpiteStream(b.pipelineContext())
	if err != nil {
		return err
	}

	// Register callback for new sessions.
	sessions.Global().SetOnNewSession(func(sess *sessions.Session) {
		go b.onNewSession(sess)
	})

	// Start background goroutines.
	go b.handleSpiteRecv()
	go b.checkinLoop()

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
