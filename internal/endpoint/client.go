package endpoint

import (
	"context"
	"errors"
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

type ClientConfig struct {
	Code          accesscode.Code
	Listen        string
	Token         string
	CAFile        string
	AllowInsecure bool
	Once          bool
	Logger        *slog.Logger
	IdleTimeout   time.Duration
}

func RunClient(ctx context.Context, cfg ClientConfig, ready func(net.Addr)) error {
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 30 * time.Minute
	}
	conn, client, err := Dial(DialConfig{RelayURL: cfg.Code.RelayURL, CAFile: cfg.CAFile, Token: cfg.Token, AllowInsecure: cfg.AllowInsecure})
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listening locally: %w", err)
	}
	defer func() { _ = ln.Close() }()
	if ready != nil {
		ready(ln.Addr())
	}
	go func() { <-ctx.Done(); _ = ln.Close() }()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		local, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accepting local connection: %w", err)
		}
		wg.Go(func() {
			defer func() { _ = local.Close() }()
			if err := handleClientTunnel(ctx, client, cfg, local); err != nil && ctx.Err() == nil {
				cfg.Logger.Warn("tunnel ended", "error", err)
			}
		})
		if cfg.Once {
			return nil
		}
	}
}

func handleClientTunnel(ctx context.Context, client relayv1.RelayServiceClient, cfg ClientConfig, local net.Conn) error {
	stream, err := client.Connect(AuthContext(ctx, cfg.Token))
	if err != nil {
		return fmt.Errorf("opening tunnel: %w", err)
	}
	if err := stream.Send(&relayv1.ConnectRequest{Body: &relayv1.ConnectRequest_ClientOpen{ClientOpen: &relayv1.ClientOpen{ProtocolVersion: accesscode.ProtocolVersion, RouteId: cfg.Code.RouteID}}}); err != nil {
		return err
	}
	assignedFrame, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("allocating session: %w", err)
	}
	assigned := assignedFrame.GetSessionAssigned()
	if assigned == nil || len(assigned.SessionId) != 16 {
		return errors.New("relay returned invalid session")
	}
	clientHello, initiator, err := tunnelcrypto.StartInitiator(cfg.Code.PSK, cfg.Code.RouteID, assigned.SessionId)
	if err != nil {
		return err
	}
	if err := stream.Send(&relayv1.ConnectRequest{Body: &relayv1.ConnectRequest_ClientHandshake{ClientHandshake: &relayv1.ClientHandshake{ClientHello: clientHello}}}); err != nil {
		return err
	}
	acceptFrame, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("waiting for agent: %w", err)
	}
	accept := acceptFrame.GetAgentAccept()
	if accept == nil {
		return errors.New("relay returned invalid agent handshake")
	}
	crypt, err := initiator.Finish(accept.AgentHello)
	if err != nil {
		return err
	}
	return Bridge(ctx, local, &clientCipherStream{stream: stream}, crypt, cfg.IdleTimeout)
}

type clientCipherStream struct {
	stream relayv1.RelayService_ConnectClient
	sendMu sync.Mutex
}

func (s *clientCipherStream) SendCiphertext(b []byte) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.stream.Send(&relayv1.ConnectRequest{Body: &relayv1.ConnectRequest_Ciphertext{Ciphertext: &relayv1.Ciphertext{Payload: b}}})
}
func (s *clientCipherStream) RecvCiphertext() ([]byte, error) {
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
func (s *clientCipherStream) CloseSend() error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.stream.CloseSend()
}
