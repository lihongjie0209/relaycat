package endpoint

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	relayv1 "github.com/lihongjie0209/relaycat/gen/relay/v1"
	"github.com/lihongjie0209/relaycat/internal/tunnelcrypto"
	"google.golang.org/protobuf/proto"
)

const chunkSize = 32 << 10

type CipherStream interface {
	SendCiphertext([]byte) error
	RecvCiphertext() ([]byte, error)
	CloseSend() error
}

func Bridge(ctx context.Context, conn net.Conn, stream CipherStream, crypt *tunnelcrypto.Transport, idleTimeout time.Duration) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	localEOF := make(chan struct{})
	remoteEOF := make(chan struct{})
	errCh := make(chan error, 2)
	go func() { errCh <- sendLoop(ctx, conn, stream, crypt, localEOF, remoteEOF, idleTimeout) }()
	go func() { errCh <- recvLoop(ctx, conn, stream, crypt, remoteEOF, idleTimeout) }()
	first := <-errCh
	cancel()
	_ = conn.Close()
	_ = stream.CloseSend()
	second := <-errCh
	if first != nil && !isNormalClose(first) {
		return first
	}
	if second != nil && !isNormalClose(second) {
		return second
	}
	return nil
}

func sendLoop(ctx context.Context, conn net.Conn, stream CipherStream, crypt *tunnelcrypto.Transport, localEOF chan<- struct{}, remoteEOF <-chan struct{}, idleTimeout time.Duration) error {
	buf := make([]byte, chunkSize)
	for {
		if idleTimeout > 0 {
			_ = conn.SetDeadline(time.Now().Add(idleTimeout))
		}
		n, err := conn.Read(buf)
		if n > 0 {
			if sendErr := sendPlain(stream, crypt, &relayv1.PlainFrame{Body: &relayv1.PlainFrame_Data{Data: append([]byte(nil), buf[:n]...)}}); sendErr != nil {
				return sendErr
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return fmt.Errorf("reading TCP connection: %w", err)
			}
			if err := sendPlain(stream, crypt, &relayv1.PlainFrame{Body: &relayv1.PlainFrame_HalfClose{HalfClose: &relayv1.HalfClose{}}}); err != nil {
				return err
			}
			close(localEOF)
			select {
			case <-remoteEOF:
			case <-ctx.Done():
				return ctx.Err()
			}
			return sendPlain(stream, crypt, &relayv1.PlainFrame{Body: &relayv1.PlainFrame_Close{Close: &relayv1.Close{Code: relayv1.CloseCode_CLOSE_CODE_NORMAL}}})
		}
	}
}

func recvLoop(ctx context.Context, conn net.Conn, stream CipherStream, crypt *tunnelcrypto.Transport, remoteEOF chan<- struct{}, idleTimeout time.Duration) error {
	var once sync.Once
	for {
		ciphertext, err := stream.RecvCiphertext()
		if err != nil {
			return err
		}
		plaintext, err := crypt.Decrypt(ciphertext)
		if err != nil {
			return err
		}
		var frame relayv1.PlainFrame
		if err := proto.Unmarshal(plaintext, &frame); err != nil {
			return errors.New("invalid encrypted frame")
		}
		switch body := frame.Body.(type) {
		case *relayv1.PlainFrame_Data:
			if len(body.Data) == 0 {
				continue
			}
			if idleTimeout > 0 {
				_ = conn.SetDeadline(time.Now().Add(idleTimeout))
			}
			if _, err := conn.Write(body.Data); err != nil {
				return fmt.Errorf("writing TCP connection: %w", err)
			}
		case *relayv1.PlainFrame_HalfClose:
			if cw, ok := conn.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
			once.Do(func() { close(remoteEOF) })
		case *relayv1.PlainFrame_Close:
			once.Do(func() { close(remoteEOF) })
			if body.Close.Code == relayv1.CloseCode_CLOSE_CODE_NORMAL {
				return nil
			}
			return fmt.Errorf("remote closed tunnel: %s", body.Close.Message)
		default:
			return errors.New("empty encrypted frame")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
}

func sendPlain(stream CipherStream, crypt *tunnelcrypto.Transport, frame *relayv1.PlainFrame) error {
	b, err := proto.Marshal(frame)
	if err != nil {
		return fmt.Errorf("encoding frame: %w", err)
	}
	b, err = crypt.Encrypt(b)
	if err != nil {
		return err
	}
	return stream.SendCiphertext(b)
}

func SendClose(stream CipherStream, crypt *tunnelcrypto.Transport, code relayv1.CloseCode, msg string) error {
	return sendPlain(stream, crypt, &relayv1.PlainFrame{Body: &relayv1.PlainFrame_Close{Close: &relayv1.Close{Code: code, Message: msg}}})
}

func isNormalClose(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)
}
