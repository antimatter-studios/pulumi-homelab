package transport

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSilentPeerHandshakeTimeout(t *testing.T) {
	for _, name := range []string{"direct", "through jump"} {
		t.Run(name, func(t *testing.T) {
			user, identity := newKey(t)
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { ln.Close() })
			peerClosed := make(chan error, 1)
			go func() {
				conn, err := ln.Accept()
				if err == nil {
					defer conn.Close()
					// Keep the socket open without sending an SSH banner.
					_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
					_, err = io.Copy(io.Discard, conn)
				}
				peerClosed <- err
			}()

			target := Hop{Address: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, User: "target"}
			cfg := Config{Hops: []Hop{target}, Identity: identity, ConnectTimeout: 300 * time.Millisecond}
			wantErr := os.ErrDeadlineExceeded
			if name == "through jump" {
				jump := startSSHD(t, user.PublicKey())
				cfg.Hops = []Hop{jump.hop("admin"), target}
				cfg.HostKeys = []string{jump.pin()}
				wantErr = context.DeadlineExceeded
			}
			tr := newT(t, cfg)
			start := time.Now()
			_, _, _, err = tr.Run(context.Background(), "true", nil)
			if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "ssh handshake with "+target.String()) || time.Since(start) >= 2*time.Second {
				t.Fatalf("want a prompt handshake timeout for %s, got %v after %v", target, err, time.Since(start))
			}
			select {
			case err := <-peerClosed:
				if err != nil {
					t.Fatalf("waiting for the client to close the connection: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("client left the connection open after the timeout")
			}
		})
	}
}
