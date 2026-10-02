package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gaurishhs/live-code-server/internal/eventapi"
	"github.com/gaurishhs/live-code-server/internal/filesystem"
	"github.com/gaurishhs/live-code-server/internal/server"
	"github.com/gaurishhs/live-code-server/internal/watcher"
)

const shareHost = "127.0.0.1"

var sharePort = 8787

type shareOptions struct {
	directory, eventURL, password, cloudflared string
}

func runShareCommand(args []string) error {
	fs := flag.NewFlagSet("livecode share", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	eventURL := fs.String("event-url", envOr("LIVECODE_EVENT_API", "https://api.gaurishhs.xyz"), "event service API URL")
	password := fs.String("event-password", os.Getenv("LIVECODE_EVENT_PASSWORD"), "event service password")
	cloudflared := fs.String("cloudflared", envOr("LIVECODE_CLOUDFLARED", "cloudflared"), "cloudflared executable")
	if err := fs.Parse(reorderArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	if *password == "" {
		return errors.New("event password required; set --event-password or LIVECODE_EVENT_PASSWORD")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runShare(ctx, shareOptions{directory: dir, eventURL: *eventURL, password: *password, cloudflared: *cloudflared}, startTunnel)
}

func runShare(ctx context.Context, opts shareOptions, start tunnelStarter) error {
	bin, err := exec.LookPath(opts.cloudflared)
	if err != nil {
		return fmt.Errorf("cloudflared executable not found: %s", opts.cloudflared)
	}

	fmt.Println("Starting livecode server...")
	local, err := startShareServer(opts.directory)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = local.Stop(shutdownCtx)
	}()
	if err := waitForHealth(ctx, "http://"+net.JoinHostPort(shareHost, fmt.Sprint(sharePort))+"/api/health"); err != nil {
		return err
	}

	api, err := eventapi.NewClient(opts.eventURL, nil)
	if err != nil {
		return err
	}
	fmt.Println("Claiming livecode event slot...")
	claim, err := api.Claim(ctx, opts.password)
	if err != nil {
		return err
	}
	leaseExpires := time.UnixMilli(claim.ExpiresAt)

	var tunnel tunnelProcess
	defer func() {
		if tunnel != nil {
			tunnel.Stop()
			select {
			case <-tunnel.Done():
			case <-time.After(5 * time.Second):
			}
		}
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := api.Release(releaseCtx, claim.Lease); err != nil {
			fmt.Fprintf(os.Stderr, "warning: unable to release livecode event slot: %v\n", err)
		}
	}()
	publicURL, urlErr := url.Parse(claim.PublicURL)
	if urlErr != nil || publicURL.Scheme != "https" || publicURL.Host == "" || publicURL.User != nil || publicURL.RawQuery != "" || publicURL.Fragment != "" || strings.Contains(claim.PublicURL, claim.TunnelToken) {
		return errors.New("event service returned an invalid public URL")
	}

	if ctx.Err() != nil {
		return nil
	}
	fmt.Println("Starting Cloudflare Tunnel...")
	tunnel, err = start(bin, claim.TunnelToken)
	if err != nil {
		return fmt.Errorf("unable to start cloudflared: %w", err)
	}
	fmt.Printf("\nLivecode is now being shared.\n\nPublic URL:\n  %s\n\nPress Ctrl+C to stop sharing.\n", claim.PublicURL)
	return monitorShare(ctx, api, claim.Lease, leaseExpires, tunnel)
}

func monitorShare(ctx context.Context, api *eventapi.Client, lease string, expires time.Time, tunnel tunnelProcess) error {
	delay, err := renewalDelay(expires)
	if err != nil {
		return errors.New("event sharing lease is expired or too close to expiry")
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-tunnel.Done():
			if err != nil {
				return fmt.Errorf("Cloudflare Tunnel stopped unexpectedly: %s", safeExit(err))
			}
			return errors.New("Cloudflare Tunnel stopped unexpectedly")
		case <-timer.C:
			renewCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			result, err := api.Renew(renewCtx, lease)
			cancel()
			if err == nil {
				expires = time.UnixMilli(result.ExpiresAt)
				delay, delayErr := renewalDelay(expires)
				if delayErr != nil {
					return errors.New("event service renewed the lease with an invalid expiry")
				}
				resetTimer(timer, delay)
				continue
			}
			if errors.Is(err, eventapi.ErrLeaseExpired) {
				return errors.New("event sharing lease expired; stopping share")
			}
			var statusErr *eventapi.StatusError
			if errors.As(err, &statusErr) && statusErr.Status < 500 {
				return fmt.Errorf("unable to renew event sharing lease: %w", err)
			}
			remaining := time.Until(expires)
			if remaining <= time.Second {
				return errors.New("event sharing lease expired after renewal failed")
			}
			retry := 15 * time.Second
			if remaining-time.Second < retry {
				retry = remaining - time.Second
			}
			fmt.Fprintf(os.Stderr, "warning: unable to renew event lease; retrying before lease expiry\n")
			resetTimer(timer, retry)
		}
	}
}

func renewalDelay(expires time.Time) (time.Duration, error) {
	remaining := time.Until(expires)
	if remaining <= time.Second {
		return 0, errors.New("lease expires too soon")
	}
	delay := 5 * time.Minute
	if remaining <= delay {
		delay = remaining - time.Second
	}
	return delay, nil
}

func resetTimer(timer *time.Timer, d time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}
func safeExit(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.Error()
	}
	return "process exited"
}
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type tunnelProcess interface {
	Done() <-chan error
	Stop()
}
type tunnelStarter func(executable, token string) (tunnelProcess, error)

type commandTunnel struct {
	cmd      *exec.Cmd
	done     chan error
	stopOnce sync.Once
}

func startTunnel(executable, token string) (tunnelProcess, error) {
	cmd := exec.Command(executable, "tunnel", "run", "--token", token)
	outRedactor := &secretRedactor{dst: os.Stdout, secret: []byte(token)}
	errRedactor := &secretRedactor{dst: os.Stderr, secret: []byte(token)}
	cmd.Stdout, cmd.Stderr = outRedactor, errRedactor
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &commandTunnel{cmd: cmd, done: make(chan error, 1)}
	go func() {
		err := cmd.Wait()
		_ = outRedactor.Flush()
		_ = errRedactor.Flush()
		p.done <- err
		close(p.done)
	}()
	return p, nil
}
func (p *commandTunnel) Done() <-chan error { return p.done }
func (p *commandTunnel) Stop() {
	p.stopOnce.Do(func() {
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
	})
}

type secretRedactor struct {
	mu              sync.Mutex
	dst             io.Writer
	secret, pending []byte
}

func (w *secretRedactor) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	combined := append(append([]byte(nil), w.pending...), p...)
	if len(w.secret) == 0 {
		return w.dst.Write(combined)
	}
	limit := len(combined) - len(w.secret) + 1
	if limit < 0 {
		limit = 0
	}
	maxOverlap := len(w.secret) - 1
	if maxOverlap > limit {
		maxOverlap = limit
	}
	for overlap := maxOverlap; overlap > 0; overlap-- {
		if bytes.Equal(combined[limit-overlap:limit], w.secret[:overlap]) {
			limit -= overlap
			break
		}
	}
	out := bytes.ReplaceAll(combined[:limit], w.secret, []byte("[redacted]"))
	if len(out) > 0 {
		if _, err := w.dst.Write(out); err != nil {
			return 0, err
		}
	}
	w.pending = append(w.pending[:0], combined[limit:]...)
	return len(p), nil
}
func (w *secretRedactor) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) == 0 {
		return nil
	}
	out := bytes.ReplaceAll(w.pending, w.secret, []byte("[redacted]"))
	_, err := w.dst.Write(out)
	w.pending = nil
	return err
}

type shareServer struct {
	http      *http.Server
	watcher   *watcher.Watcher
	hub       *server.Hub
	done      chan struct{}
	serveDone chan struct{}
	once      sync.Once
}

func startShareServer(dir string) (*shareServer, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	ignores := []string{".git", "node_modules", "dist", "build", ".astro", ".cache", ".DS_Store"}
	root, err := filesystem.New(absolute, ignores, 10*1024*1024)
	if err != nil {
		return nil, err
	}
	logger := log.New(os.Stderr, "", 0)
	api := server.New(server.Config{Root: root, Logger: logger})
	w, err := watcher.New(root, api.Hub(), 250*time.Millisecond, logger)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(shareHost, fmt.Sprint(sharePort)))
	if err != nil {
		_ = w.Close()
		return nil, err
	}
	httpSrv := &http.Server{Handler: api.Handler(), ReadHeaderTimeout: 10 * time.Second}
	s := &shareServer{http: httpSrv, watcher: w, hub: api.Hub(), done: make(chan struct{}), serveDone: make(chan struct{})}
	go w.Run(s.done)
	go func() {
		defer close(s.serveDone)
		if e := httpSrv.Serve(ln); e != nil && !errors.Is(e, http.ErrServerClosed) {
			logger.Printf("ERROR server: %v", e)
		}
	}()
	logger.Printf("INFO  server listening on %s", ln.Addr())
	logger.Printf("INFO  watching %s", root.Path)
	return s, nil
}
func (s *shareServer) Stop(ctx context.Context) error {
	var err error
	s.once.Do(func() {
		close(s.done)
		s.hub.Close()
		err = s.http.Shutdown(ctx)
		_ = s.watcher.Close()
		select {
		case <-s.serveDone:
		case <-ctx.Done():
			if err == nil {
				err = ctx.Err()
			}
			_ = s.http.Close()
		}
	})
	return err
}

func waitForHealth(ctx context.Context, endpoint string) error {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
			res, err := client.Do(req)
			if err == nil {
				res.Body.Close()
				if res.StatusCode == http.StatusOK {
					return nil
				}
			}
			timer.Reset(100 * time.Millisecond)
		}
	}
}
