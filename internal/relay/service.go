package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	relayv1 "github.com/lihongjie0209/relaycat/gen/relay/v1"
	"github.com/lihongjie0209/relaycat/internal/accesscode"
	"github.com/lihongjie0209/relaycat/internal/observability"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Config struct {
	MaxAgents          int
	MaxTunnelsPerAgent int
	HandshakeTimeout   time.Duration
}

type Service struct {
	relayv1.UnimplementedRelayServiceServer
	log      *slog.Logger
	cfg      Config
	mu       sync.Mutex
	agents   map[string]*agent
	sessions map[string]*session
}

type agent struct {
	route    string
	incoming chan *relayv1.RegisterAgentResponse
	done     chan struct{}
	active   int
}

type session struct {
	id            string
	agent         *agent
	ctx           context.Context
	cancel        context.CancelFunc
	ready         chan struct{}
	readyOnce     sync.Once
	agentHello    []byte
	clientToAgent chan []byte
	agentToClient chan []byte
}

func NewService(log *slog.Logger, cfg Config) *Service {
	if cfg.MaxAgents <= 0 {
		cfg.MaxAgents = 1000
	}
	if cfg.MaxTunnelsPerAgent <= 0 {
		cfg.MaxTunnelsPerAgent = 128
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = 10 * time.Second
	}
	return &Service{log: log, cfg: cfg, agents: make(map[string]*agent), sessions: make(map[string]*session)}
}

func (s *Service) RegisterAgent(req *relayv1.RegisterAgentRequest, stream relayv1.RelayService_RegisterAgentServer) error {
	if req.ProtocolVersion != accesscode.ProtocolVersion {
		return status.Errorf(codes.FailedPrecondition, "unsupported protocol version %d", req.ProtocolVersion)
	}
	if len(req.RouteId) != accesscode.RouteIDSize {
		return status.Error(codes.InvalidArgument, "invalid route ID")
	}
	key := string(req.RouteId)
	a := &agent{route: key, incoming: make(chan *relayv1.RegisterAgentResponse), done: make(chan struct{})}
	s.mu.Lock()
	if len(s.agents) >= s.cfg.MaxAgents {
		s.mu.Unlock()
		return status.Error(codes.ResourceExhausted, "agent capacity reached")
	}
	if _, exists := s.agents[key]; exists {
		s.mu.Unlock()
		return status.Error(codes.AlreadyExists, "route is already registered")
	}
	s.agents[key] = a
	observability.Agents.Inc()
	s.mu.Unlock()
	s.log.InfoContext(stream.Context(), "agent registered", "route", shortID(req.RouteId))
	defer func() {
		s.mu.Lock()
		if s.agents[key] == a {
			delete(s.agents, key)
			observability.Agents.Dec()
		}
		close(a.done)
		for _, sess := range s.sessions {
			if sess.agent == a {
				sess.cancel()
			}
		}
		s.mu.Unlock()
		s.log.Info("agent disconnected", "route", shortID(req.RouteId))
	}()
	for {
		select {
		case <-stream.Context().Done():
			return status.FromContextError(stream.Context().Err()).Err()
		case msg := <-a.incoming:
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
	}
}

func (s *Service) Connect(stream relayv1.RelayService_ConnectServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	open := first.GetClientOpen()
	if open == nil || open.ProtocolVersion != accesscode.ProtocolVersion || len(open.RouteId) != accesscode.RouteIDSize {
		return status.Error(codes.InvalidArgument, "first frame must be a valid client open")
	}
	a, err := s.reserveAgent(open.RouteId)
	if err != nil {
		observability.TunnelOpens.WithLabelValues("rejected").Inc()
		return err
	}
	observability.TunnelOpens.WithLabelValues("accepted").Inc()
	observability.Tunnels.Inc()
	defer observability.Tunnels.Dec()
	defer s.releaseAgent(a)
	sid := make([]byte, 16)
	if _, err := rand.Read(sid); err != nil {
		return status.Error(codes.Internal, "could not allocate session")
	}
	ctx, cancel := context.WithCancel(stream.Context())
	sess := &session{id: string(sid), agent: a, ctx: ctx, cancel: cancel, ready: make(chan struct{}), clientToAgent: make(chan []byte), agentToClient: make(chan []byte)}
	defer cancel()
	if err := stream.Send(&relayv1.ConnectResponse{Body: &relayv1.ConnectResponse_SessionAssigned{SessionAssigned: &relayv1.SessionAssigned{SessionId: sid}}}); err != nil {
		return err
	}
	handshakeFrame, err := stream.Recv()
	if err != nil {
		return err
	}
	handshake := handshakeFrame.GetClientHandshake()
	if handshake == nil || len(handshake.ClientHello) == 0 {
		return status.Error(codes.InvalidArgument, "second frame must be a client handshake")
	}
	s.mu.Lock()
	s.sessions[sess.id] = sess
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.sessions, sess.id); s.mu.Unlock() }()
	notify := &relayv1.RegisterAgentResponse{ProtocolVersion: accesscode.ProtocolVersion, SessionId: sid, ClientHello: handshake.ClientHello}
	select {
	case a.incoming <- notify:
	case <-a.done:
		return status.Error(codes.Unavailable, "agent is offline")
	case <-ctx.Done():
		return status.FromContextError(ctx.Err()).Err()
	case <-time.After(s.cfg.HandshakeTimeout):
		return status.Error(codes.DeadlineExceeded, "agent notification timed out")
	}
	timer := time.NewTimer(s.cfg.HandshakeTimeout)
	defer timer.Stop()
	select {
	case <-sess.ready:
	case <-ctx.Done():
		return status.FromContextError(ctx.Err()).Err()
	case <-timer.C:
		return status.Error(codes.DeadlineExceeded, "agent handshake timed out")
	}
	if err := stream.Send(&relayv1.ConnectResponse{Body: &relayv1.ConnectResponse_AgentAccept{AgentAccept: &relayv1.AgentAccept{ProtocolVersion: accesscode.ProtocolVersion, SessionId: sid, AgentHello: sess.agentHello}}}); err != nil {
		return err
	}
	errCh := make(chan error, 1)
	go func() { errCh <- recvClient(ctx, stream, sess.clientToAgent) }()
	for {
		select {
		case payload := <-sess.agentToClient:
			if err := stream.Send(connectCiphertext(payload)); err != nil {
				return err
			}
		case err := <-errCh:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		}
	}
}

func (s *Service) Accept(stream relayv1.RelayService_AcceptServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	accept := first.GetAgentAccept()
	if accept == nil || accept.ProtocolVersion != accesscode.ProtocolVersion || len(accept.SessionId) != 16 || len(accept.AgentHello) == 0 {
		return status.Error(codes.InvalidArgument, "first frame must be a valid agent accept")
	}
	s.mu.Lock()
	sess := s.sessions[string(accept.SessionId)]
	if sess != nil {
		sess.readyOnce.Do(func() { sess.agentHello = append([]byte(nil), accept.AgentHello...); close(sess.ready) })
	}
	s.mu.Unlock()
	if sess == nil {
		return status.Error(codes.NotFound, "session is not pending")
	}
	defer sess.cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- recvAgent(sess.ctx, stream, sess.agentToClient) }()
	for {
		select {
		case payload := <-sess.clientToAgent:
			if err := stream.Send(&relayv1.AcceptResponse{Body: &relayv1.AcceptResponse_Ciphertext{Ciphertext: &relayv1.Ciphertext{Payload: payload}}}); err != nil {
				return err
			}
		case err := <-errCh:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case <-sess.ctx.Done():
			return nil
		}
	}
}

func (s *Service) reserveAgent(route []byte) (*agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.agents[string(route)]
	if a == nil {
		return nil, status.Error(codes.Unavailable, "agent is offline")
	}
	if a.active >= s.cfg.MaxTunnelsPerAgent {
		return nil, status.Error(codes.ResourceExhausted, "route tunnel capacity reached")
	}
	a.active++
	return a, nil
}

func (s *Service) releaseAgent(a *agent) { s.mu.Lock(); a.active--; s.mu.Unlock() }

func recvClient(ctx context.Context, stream relayv1.RelayService_ConnectServer, dst chan<- []byte) error {
	for {
		frame, err := stream.Recv()
		if err != nil {
			return err
		}
		ct := frame.GetCiphertext()
		if ct == nil {
			return status.Error(codes.InvalidArgument, "expected ciphertext frame")
		}
		select {
		case dst <- append([]byte(nil), ct.Payload...):
			observability.Bytes.WithLabelValues("client_to_agent").Add(float64(len(ct.Payload)))
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func recvAgent(ctx context.Context, stream relayv1.RelayService_AcceptServer, dst chan<- []byte) error {
	for {
		frame, err := stream.Recv()
		if err != nil {
			return err
		}
		ct := frame.GetCiphertext()
		if ct == nil {
			return status.Error(codes.InvalidArgument, "expected ciphertext frame")
		}
		select {
		case dst <- append([]byte(nil), ct.Payload...):
			observability.Bytes.WithLabelValues("agent_to_client").Add(float64(len(ct.Payload)))
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func connectCiphertext(b []byte) *relayv1.ConnectResponse {
	return &relayv1.ConnectResponse{Body: &relayv1.ConnectResponse_Ciphertext{Ciphertext: &relayv1.Ciphertext{Payload: b}}}
}

func shortID(id []byte) string {
	if len(id) < 4 {
		return "invalid"
	}
	return hex.EncodeToString(id[:4])
}
