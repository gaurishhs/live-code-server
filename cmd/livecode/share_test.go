package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestShareReleasesLeaseWhenTunnelFailsToStart(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	oldPort := sharePort
	sharePort = port
	defer func() { sharePort = oldPort }()
	var released atomic.Bool
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/claim":
			if r.Header.Get("Authorization") != "Bearer test-password" {
				t.Errorf("claim authorization missing")
			}
			fmt.Fprintf(w, `{"lease":"lease-test","expiresAt":%d,"tunnelToken":"tunnel-test","publicUrl":"https://viewer.test"}`, time.Now().Add(30*time.Minute).UnixMilli())
		case "/release":
			if r.Header.Get("Authorization") != "Bearer lease-test" {
				t.Errorf("release authorization missing")
			}
			released.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer worker.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	startErr := errors.New("simulated process start failure")
	err = runShare(context.Background(), shareOptions{directory: t.TempDir(), eventURL: worker.URL, password: "test-password", cloudflared: executable}, func(_ string, token string) (tunnelProcess, error) {
		if token != "tunnel-test" {
			t.Errorf("unexpected tunnel credential")
		}
		return nil, startErr
	})
	if err == nil || !released.Load() {
		t.Fatalf("err=%v lease released=%t", err, released.Load())
	}
}

func TestShareChecksCloudflaredBeforeStartingServer(t *testing.T) {
	err := runShare(context.Background(), shareOptions{directory: t.TempDir(), eventURL: "https://example.invalid", password: "pw", cloudflared: filepath.Join(t.TempDir(), "missing-cloudflared")}, func(string, string) (tunnelProcess, error) {
		t.Fatal("tunnel starter must not be called")
		return nil, nil
	})
	if err == nil {
		t.Fatal("expected missing cloudflared error")
	}
}

func TestSecretRedactorAcrossWriteBoundaries(t *testing.T) {
	var out bytes.Buffer
	r := &secretRedactor{dst: &out, secret: []byte("sensitive-token")}
	_, _ = r.Write([]byte("output sensitive-"))
	_, _ = r.Write([]byte("token done"))
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "output [redacted] done" {
		t.Fatalf("output %q", got)
	}
}

func TestShareServerAppliesAllowedOriginToAPIAndWebsocket(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	oldPort := sharePort
	sharePort = port
	defer func() { sharePort = oldPort }()
	s, err := startShareServer(t.TempDir(), []string{"https://viewer.test"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Stop(ctx)
	}()
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	req, _ := http.NewRequest(http.MethodOptions, base+"/api/health", nil)
	req.Header.Set("Origin", "https://viewer.test")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Header.Get("Access-Control-Allow-Origin") != "https://viewer.test" {
		t.Fatalf("CORS origin %q", res.Header.Get("Access-Control-Allow-Origin"))
	}
	url := "ws" + base[4:] + "/ws"
	headers := http.Header{"Origin": []string{"https://viewer.test"}}
	conn, _, err := websocket.DefaultDialer.Dial(url, headers)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
}
