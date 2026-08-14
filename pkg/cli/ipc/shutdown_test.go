package ipc

// Wire-contract pins for the server.shutdown method (WEB-42): the {ok: true}
// response shape and the graceful stop of the Serve loop — Serve returns nil
// and the socket file is removed. All tests run against the stub seam
// (newStubServer), so no Chromium is involved.

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestServerShutdownStopsServe pins the full raw-wire contract: a
// server.shutdown request answers {"jsonrpc":"2.0","id":1,"result":{"ok":true}}
// and makes Serve return nil (the accept loop stops and the socket file is
// removed) — the same path `web-cli serve stop` exercises.
func TestServerShutdownStopsServe(t *testing.T) {
	s := newStubServer()
	path := filepath.Join(t.TempDir(), "shutdown.sock")
	if err := s.Bind(path); err != nil {
		t.Fatalf("Bind(%s): %v", path, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()

	conn := dialRaw(t, path)
	r := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))

	if _, err := conn.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"server.shutdown"}` + "\n")); err != nil {
		t.Fatalf("write server.shutdown: %v", err)
	}
	line := readLineFrom(t, r)

	var resp Response
	resp.Result = &ShutdownResult{}
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("decode server.shutdown response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("server.shutdown response has error: %+v", resp.Error)
	}
	if sr, ok := resp.Result.(*ShutdownResult); !ok || !sr.OK {
		t.Errorf("server.shutdown result = %v, want {ok: true}", resp.Result)
	}

	// The response must be flushed BEFORE the loop stops: Serve returns nil
	// shortly after, and the socket file is removed.
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned %v, want nil on a graceful shutdown", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after server.shutdown")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("socket file should be removed after shutdown, stat err = %v", err)
	}
}

// TestClientServerShutdownRoundTrip drives the shutdown through the client half
// (Dial + Client.Call), mirroring what `serve stop` does: the call succeeds and
// the server stops serving.
func TestClientServerShutdownRoundTrip(t *testing.T) {
	s := newStubServer()
	path := filepath.Join(t.TempDir(), "client-shutdown.sock")
	if err := s.Bind(path); err != nil {
		t.Fatalf("Bind(%s): %v", path, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()

	c, err := Dial(path) // the Dial probe also proves the server is up
	if err != nil {
		t.Fatalf("Dial(%s): %v", path, err)
	}
	defer c.Close()

	var res ShutdownResult
	if err := c.Call(context.Background(), MethodServerShutdown, nil, &res); err != nil {
		t.Fatalf("Call(server.shutdown): %v", err)
	}
	if !res.OK {
		t.Error("server.shutdown ok = false, want true")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned %v, want nil on a graceful shutdown", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after server.shutdown")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("socket file should be removed after shutdown, stat err = %v", err)
	}
}

// TestServerShutdownNotification pins that a server.shutdown sent as a
// JSON-RPC notification (no id) still stops the serve loop, even though no
// response line is produced.
func TestServerShutdownNotification(t *testing.T) {
	s := newStubServer()
	path := filepath.Join(t.TempDir(), "notify-shutdown.sock")
	if err := s.Bind(path); err != nil {
		t.Fatalf("Bind(%s): %v", path, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()

	conn := dialRaw(t, path)
	if _, err := conn.Write([]byte(`{"jsonrpc":"2.0","method":"server.shutdown"}` + "\n")); err != nil {
		t.Fatalf("write server.shutdown notification: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned %v, want nil on a graceful shutdown", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after a server.shutdown notification")
	}
}
