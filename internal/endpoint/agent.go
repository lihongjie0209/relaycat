package endpoint

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	relayv1 "github.com/lihongjie0209/relaycat/gen/relay/v1"
	"github.com/lihongjie0209/relaycat/internal/accesscode"
	"github.com/lihongjie0209/relaycat/internal/tunnelcrypto"
)

type AgentConfig struct {
	Code          accesscode.Code
	Target        string
	Token         string
	CAFile        string
	AllowInsecure bool
	MaxTunnels    int
	DialTimeout   time.Duration
	IdleTimeout   time.Duration
	Logger        *slog.Logger
}

func RunAgent(ctx context.Context, cfg AgentConfig) error {
	if cfg.MaxTunnels <= 0 {
		cfg.MaxTunnels = 128
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 30 * time.Minute
	}
	conn, client, err := Dial(DialConfig{RelayURL: cfg.Code.RelayURL, CAFile: cfg.CAFile, Token: cfg.Token, AllowInsecure: cfg.AllowInsecure})
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	sem := make(chan struct{}, cfg.MaxTunnels)
	backoff := time.Second
	for {
		err := registerOnce(ctx, client, cfg, sem)
		if ctx.Err() != nil {
			return nil
		}
		cfg.Logger.Warn("agent registration lost", "error", err, "retry_in", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func registerOnce(ctx context.Context, client relayv1.RelayServiceClient, cfg AgentConfig, sem chan struct{}) error {
	stream, err := client.RegisterAgent(AuthContext(ctx, cfg.Token), &relayv1.RegisterAgentRequest{ProtocolVersion: accesscode.ProtocolVersion, RouteId: cfg.Code.RouteID})
	if err != nil {
		return fmt.Errorf("registering agent: %w", err)
	}
	for {
		incoming, err := stream.Recv()
		if err != nil {
			return fmt.Errorf("receiving tunnel request: %w", err)
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		go func() {
			defer func() { <-sem }()
			if err := handleAgentTunnel(ctx, client, cfg, incoming); err != nil && ctx.Err() == nil {
				cfg.Logger.Warn("tunnel ended", "error", err)
			}
		}()
	}
}

func handleAgentTunnel(ctx context.Context, client relayv1.RelayServiceClient, cfg AgentConfig, incoming *relayv1.RegisterAgentResponse) error {
	if incoming.ProtocolVersion != accesscode.ProtocolVersion || len(incoming.SessionId) != 16 {
		return fmt.Errorf("invalid incoming tunnel notification")
	}
	agentHello, crypt, err := tunnelcrypto.Respond(cfg.Code.PSK, cfg.Code.RouteID, incoming.SessionId, incoming.ClientHello)
	if err != nil {
		return err
	}
	stream, err := client.Accept(AuthContext(ctx, cfg.Token))
	if err != nil {
		return fmt.Errorf("opening accept stream: %w", err)
	}
	adapter := &agentCipherStream{stream: stream}
	if err := stream.Send(&relayv1.AcceptRequest{Body: &relayv1.AcceptRequest_AgentAccept{AgentAccept: &relayv1.AgentAccept{ProtocolVersion: accesscode.ProtocolVersion, SessionId: incoming.SessionId, AgentHello: agentHello}}}); err != nil {
		return fmt.Errorf("accepting tunnel: %w", err)
	}
	dialer := net.Dialer{Timeout: cfg.DialTimeout}
	target, err := dialer.DialContext(ctx, "tcp", cfg.Target)
	if err != nil {
		_ = SendClose(adapter, crypt, relayv1.CloseCode_CLOSE_CODE_TARGET_UNREACHABLE, "target is unreachable")
		_ = stream.CloseSend()
		return fmt.Errorf("dialing target: %w", err)
	}
	defer func() { _ = target.Close() }()
	return Bridge(ctx, target, adapter, crypt, cfg.IdleTimeout)
}

type agentCipherStream struct {
	stream relayv1.RelayService_AcceptClient
	sendMu sync.Mutex
}

func (s *agentCipherStream) SendCiphertext(b []byte) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.stream.Send(&relayv1.AcceptRequest{Body: &relayv1.AcceptRequest_Ciphertext{Ciphertext: &relayv1.Ciphertext{Payload: b}}})
}
func (s *agentCipherStream) RecvCiphertext() ([]byte, error) {
	f, err := s.stream.Recv()
	if err != nil {
		return nil, err
	}
	c := f.GetCiphertext()
	if c == nil {
		return nil, io.ErrUnexpectedEOF
	}
	return c.Payload, nil
}
func (s *agentCipherStream) CloseSend() error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.stream.CloseSend()
}
