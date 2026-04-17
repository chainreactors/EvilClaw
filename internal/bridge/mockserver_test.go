package bridge

import (
	"context"
	"net"
	"sync"
	"testing"

	"github.com/chainreactors/IoM-go/proto/client/clientpb"
	"github.com/chainreactors/IoM-go/proto/implant/implantpb"
	"github.com/chainreactors/IoM-go/proto/services/listenerrpc"
	cfgpkg "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

// ---------------------------------------------------------------------------
// checkinRecord captures a single Checkin RPC call with extracted metadata.
// ---------------------------------------------------------------------------

type checkinRecord struct {
	sessionID string
	ping      *implantpb.Ping
}

// ---------------------------------------------------------------------------
// testServer — minimal mock of the malice-network ListenerRPC service.
// It embeds UnimplementedListenerRPCServer so only the methods the bridge
// actually calls need explicit implementations.
// ---------------------------------------------------------------------------

type testServer struct {
	listenerrpc.UnimplementedListenerRPCServer

	mu                  sync.Mutex
	registeredListeners []*clientpb.RegisterListener
	registeredPipelines []*clientpb.Pipeline
	startedPipelines    []*clientpb.CtrlPipeline
	registeredSessions  []*clientpb.RegisterSession

	checkins  []checkinRecord
	checkinCh chan checkinRecord // notifies tests when a Checkin arrives

	// SpiteStream channels: test injects requests via spiteReqCh,
	// bridge responses appear on spiteRespCh.
	spiteReqCh  chan *clientpb.SpiteRequest
	spiteRespCh chan *clientpb.SpiteResponse

	// JobStream channels: test injects control msgs via jobCtrlCh,
	// bridge status responses appear on jobStatusCh.
	jobCtrlCh   chan *clientpb.JobCtrl
	jobStatusCh chan *clientpb.JobStatus

	jobStreamDisconnectCh chan struct{}
	missingListener       bool
	missingPipeline       bool
}

func newTestServer() *testServer {
	return &testServer{
		checkinCh:             make(chan checkinRecord, 16),
		spiteReqCh:            make(chan *clientpb.SpiteRequest, 16),
		spiteRespCh:           make(chan *clientpb.SpiteResponse, 64),
		jobCtrlCh:             make(chan *clientpb.JobCtrl, 16),
		jobStatusCh:           make(chan *clientpb.JobStatus, 16),
		jobStreamDisconnectCh: make(chan struct{}),
	}
}

// -- Unary RPCs --------------------------------------------------------------

func (s *testServer) RegisterListener(_ context.Context, req *clientpb.RegisterListener) (*clientpb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registeredListeners = append(s.registeredListeners, req)
	s.missingListener = false
	return &clientpb.Empty{}, nil
}

func (s *testServer) RegisterPipeline(_ context.Context, req *clientpb.Pipeline) (*clientpb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registeredPipelines = append(s.registeredPipelines, req)
	s.missingPipeline = false
	return &clientpb.Empty{}, nil
}

func (s *testServer) StartPipeline(_ context.Context, req *clientpb.CtrlPipeline) (*clientpb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startedPipelines = append(s.startedPipelines, req)
	return &clientpb.Empty{}, nil
}

func (s *testServer) Register(_ context.Context, req *clientpb.RegisterSession) (*clientpb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registeredSessions = append(s.registeredSessions, req)
	return &clientpb.Empty{}, nil
}

// Checkin validates that session_id is present in gRPC metadata, mirroring
// the real server's getSessionID(ctx) call. Without it, returns InvalidArgument.
func (s *testServer) Checkin(ctx context.Context, req *implantpb.Ping) (*clientpb.Empty, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "missing metadata")
	}
	sids := md.Get("session_id")
	if len(sids) == 0 || sids[0] == "" {
		return nil, status.Error(codes.InvalidArgument, "missing session_id in metadata")
	}
	rec := checkinRecord{sessionID: sids[0], ping: req}

	s.mu.Lock()
	s.checkins = append(s.checkins, rec)
	s.mu.Unlock()

	// Non-blocking notify.
	select {
	case s.checkinCh <- rec:
	default:
	}
	return &clientpb.Empty{}, nil
}

// -- SpiteStream (bidirectional) ---------------------------------------------

func (s *testServer) SpiteStream(stream listenerrpc.ListenerRPC_SpiteStreamServer) error {
	// Validate pipeline_id metadata (mirrors real server).
	md, ok := metadata.FromIncomingContext(stream.Context())
	if !ok {
		return status.Error(codes.InvalidArgument, "missing metadata")
	}
	pids := md.Get("pipeline_id")
	if len(pids) == 0 || pids[0] == "" {
		return status.Error(codes.InvalidArgument, "missing pipeline_id")
	}
	s.mu.Lock()
	missingPipeline := s.missingPipeline
	s.mu.Unlock()
	if missingPipeline {
		return status.Error(codes.NotFound, "Pipeline not found")
	}

	ctx := stream.Context()
	errCh := make(chan error, 2)

	// Send requests to bridge.
	go func() {
		for {
			select {
			case req, ok := <-s.spiteReqCh:
				if !ok {
					errCh <- context.Canceled // signal stream end
					return
				}
				if err := stream.Send(req); err != nil {
					errCh <- err
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Receive responses from bridge.
	go func() {
		for {
			resp, err := stream.Recv()
			if err != nil {
				errCh <- err
				return
			}
			select {
			case s.spiteRespCh <- resp:
			default:
				// drop if buffer full
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// -- JobStream (bidirectional) -----------------------------------------------

func (s *testServer) JobStream(stream listenerrpc.ListenerRPC_JobStreamServer) error {
	s.mu.Lock()
	missingListener := s.missingListener
	disconnectCh := s.jobStreamDisconnectCh
	s.mu.Unlock()
	if missingListener {
		return status.Error(codes.NotFound, "Listener not found")
	}

	ctx := stream.Context()
	errCh := make(chan error, 2)

	// Send job control messages to bridge.
	go func() {
		for {
			select {
			case ctrl, ok := <-s.jobCtrlCh:
				if !ok {
					return
				}
				if err := stream.Send(ctrl); err != nil {
					errCh <- err
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Receive job status from bridge.
	go func() {
		for {
			st, err := stream.Recv()
			if err != nil {
				errCh <- err
				return
			}
			select {
			case s.jobStatusCh <- st:
			default:
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-disconnectCh:
		return status.Error(codes.Unavailable, "job stream disconnected")
	case err := <-errCh:
		return err
	}
}

// -- Helpers for reading captured state --------------------------------------

func (s *testServer) getRegisteredSessions() []*clientpb.RegisterSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]*clientpb.RegisterSession, len(s.registeredSessions))
	copy(cp, s.registeredSessions)
	return cp
}

func (s *testServer) getCheckins() []checkinRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]checkinRecord, len(s.checkins))
	copy(cp, s.checkins)
	return cp
}

func (s *testServer) disconnectJobStreamsAndDropListener() {
	s.mu.Lock()
	s.missingListener = true
	close(s.jobStreamDisconnectCh)
	s.jobStreamDisconnectCh = make(chan struct{})
	s.mu.Unlock()
}

// ---------------------------------------------------------------------------
// startTestServer spins up an in-memory gRPC server with bufconn and returns
// the mock server, a client, and a cleanup function.
// ---------------------------------------------------------------------------

func startTestServer(t *testing.T) (*testServer, listenerrpc.ListenerRPCClient, func()) {
	t.Helper()

	srv := newTestServer()
	lis := bufconn.Listen(bufSize)

	gs := grpc.NewServer()
	listenerrpc.RegisterListenerRPCServer(gs, srv)

	go func() {
		if err := gs.Serve(lis); err != nil {
			// Ignore errors after cleanup.
		}
	}()

	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}

	conn, err := grpc.DialContext(
		context.Background(),
		"bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to dial bufconn: %v", err)
	}

	client := listenerrpc.NewListenerRPCClient(conn)

	cleanup := func() {
		conn.Close()
		gs.Stop()
		lis.Close()
	}

	return srv, client, cleanup
}

// ---------------------------------------------------------------------------
// newTestBridgeWithRPC creates a Bridge connected to a real gRPC client,
// bypassing NewBridge (which requires mTLS auth files).
// ---------------------------------------------------------------------------

func newTestBridgeWithRPC(t *testing.T, rpcClient listenerrpc.ListenerRPCClient) *Bridge {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	return &Bridge{
		cfg: &cfgpkg.C2BridgeConfig{
			ListenerName: "test-listener",
			ListenerIP:   "127.0.0.1",
			PipelineName: "test-pipeline",
		},
		rpc:         rpcClient,
		listenerID:  "test-listener",
		pipelineID:  "test-pipeline",
		registry:    buildDefaultRegistry(),
		taskManager: NewTaskManager(),
		ctx:         ctx,
		cancel:      cancel,
	}
}
