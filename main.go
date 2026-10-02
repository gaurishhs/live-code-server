package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/gaurishhs/live-code-server/internal/filesystem"
	"github.com/gaurishhs/live-code-server/internal/server"
	"github.com/gaurishhs/live-code-server/internal/watcher"
)

var version = "0.1.0"

type settings struct {
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	Token       string   `json:"token"`
	Origins     []string `json:"allow_origin"`
	Ignore      []string `json:"ignore"`
	MaxFileSize int64    `json:"max_file_size"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
func run() error {
	fs := flag.NewFlagSet("livecode", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	host := fs.String("host", "127.0.0.1", "address to listen on")
	port := fs.Int("port", 8787, "port to listen on")
	pub := fs.Bool("public", false, "listen on all network interfaces")
	tunnel := fs.Bool("tunnel", false, "start a Cloudflare quick tunnel")
	tunnelHost := fs.String("tunnel-host", "", "named tunnel hostname (not supported yet)")
	ignore := fs.String("ignore", "", "comma-separated additional ignore patterns")
	max := fs.Int64("max-file-size", 10, "maximum file size in MB")
	token := fs.String("token", "", "optional bearer token")
	origins := fs.String("allow-origin", "", "comma-separated allowed CORS origins")
	verbose := fs.Bool("verbose", false, "enable verbose logging")
	ver := fs.Bool("version", false, "print version")
	config := fs.String("config", "", "JSON configuration file")
	if err := fs.Parse(reorderArgs(fs, os.Args[1:])); err != nil {
		return err
	}
	if *ver {
		fmt.Println("livecode", version)
		return nil
	}
	if *tunnelHost != "" {
		return errors.New("--tunnel-host requires named tunnel support; use --tunnel for a quick tunnel")
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	cfg := settings{Host: "127.0.0.1", Port: 8787, MaxFileSize: 10 * 1024 * 1024}
	if *config != "" {
		b, e := os.ReadFile(*config)
		if e != nil {
			return e
		}
		if e = json.Unmarshal(b, &cfg); e != nil {
			return fmt.Errorf("parse config: %w", e)
		}
	}
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	if cfg.Port == 0 {
		cfg.Port = 8787
	}
	if cfg.MaxFileSize == 0 {
		cfg.MaxFileSize = 10 * 1024 * 1024
	}
	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	if visited["host"] {
		cfg.Host = *host
	}
	if *pub {
		cfg.Host = "0.0.0.0"
	}
	if visited["port"] {
		cfg.Port = *port
	}
	if visited["token"] {
		cfg.Token = *token
	}
	if visited["max-file-size"] {
		cfg.MaxFileSize = *max * 1024 * 1024
	}
	if visited["allow-origin"] {
		cfg.Origins = split(*origins)
	}
	cfg.Ignore = append([]string{".git", "node_modules", "dist", "build", ".astro", ".cache", ".DS_Store"}, cfg.Ignore...)
	if visited["ignore"] {
		cfg.Ignore = append(cfg.Ignore, split(*ignore)...)
	}
	if cfg.MaxFileSize <= 0 {
		return errors.New("--max-file-size must be positive")
	}
	root, err := filesystem.New(dir, cfg.Ignore, cfg.MaxFileSize)
	if err != nil {
		return err
	}
	logger := log.New(os.Stderr, "", 0)
	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))
	srv := server.New(server.Config{Root: root, Token: cfg.Token, Origins: cfg.Origins, Logger: logger, Verbose: *verbose})
	w, err := watcher.New(root, srv.Hub(), 250*time.Millisecond, logger)
	if err != nil {
		return err
	}
	defer w.Close()
	done := make(chan struct{})
	go w.Run(done)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		close(done)
		return err
	}
	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if e := httpSrv.Serve(ln); e != nil && !errors.Is(e, http.ErrServerClosed) {
			logger.Printf("ERROR server: %v", e)
		}
	}()
	logger.Printf("INFO  server listening on %s", addr)
	logger.Printf("INFO  watching %s", root.Path)
	var child *exec.Cmd
	publicURL := ""
	if *tunnel {
		bin, e := exec.LookPath("cloudflared")
		if e != nil {
			close(done)
			return errors.New("--tunnel requires cloudflared to be installed.\nInstall cloudflared and try again.")
		}
		child = exec.Command(bin, "tunnel", "--url", "http://"+net.JoinHostPort(cfg.HostForTunnel(), fmt.Sprint(cfg.Port)))
		out, e := child.StdoutPipe()
		if e != nil {
			return e
		}
		stderr, e := child.StderrPipe()
		if e != nil {
			return e
		}
		if e = child.Start(); e != nil {
			return e
		}
		urlCh := make(chan string, 1)
		go scanTunnel(out, urlCh)
		go scanTunnel(stderr, urlCh)
		select {
		case publicURL = <-urlCh:
		case <-time.After(15 * time.Second):
			_ = child.Process.Kill()
			close(done)
			return errors.New("cloudflared did not report a quick tunnel URL")
		}
		if cfg.Token == "" {
			fmt.Fprintln(os.Stderr, "WARNING: Public tunnel is unauthenticated.\nAnyone with the URL can access your files.\nConsider using --token.")
		}
	}
	tree, _ := root.Tree()
	count := countFiles(tree)
	localHost := cfg.Host
	if localHost == "0.0.0.0" || localHost == "::" {
		localHost = "127.0.0.1"
	}
	scheme := "ws"
	if strings.HasPrefix(publicURL, "https://") {
		scheme = "wss"
	}
	fmt.Printf("\n  LiveCode v%s\n\n  Root\n    %s\n\n  Server\n    http://%s\n\n  WebSocket\n    %s://%s/ws\n\n  Watching\n    %d files\n", version, root.Path, net.JoinHostPort(localHost, fmt.Sprint(cfg.Port)), scheme, net.JoinHostPort(localHost, fmt.Sprint(cfg.Port)), count)
	if publicURL != "" {
		fmt.Printf("\n  Public\n    %s\n\n  WebSocket\n    wss://%s/ws\n", publicURL, strings.TrimPrefix(publicURL, "https://"))
	}
	fmt.Println("\n  Ready.")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	close(done)
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	_ = httpSrv.Shutdown(shutdownCtx)
	if child != nil {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	}
	return nil
}

func reorderArgs(fs *flag.FlagSet, args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)
		if strings.Contains(a, "=") {
			continue
		}
		name := strings.TrimLeft(a, "-")
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, positional...)
}
func split(s string) []string {
	var a []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			a = append(a, v)
		}
	}
	return a
}
func (s settings) HostForTunnel() string {
	if s.Host == "0.0.0.0" || s.Host == "::" || s.Host == "" {
		return "127.0.0.1"
	}
	return s.Host
}
func countFiles(e filesystem.Entry) int {
	n := 0
	for _, c := range e.Children {
		if c.Type == "file" {
			n++
		} else {
			n += countFiles(c)
		}
	}
	return n
}

var tunnelURL = regexp.MustCompile(`https://[a-zA-Z0-9-]+\.trycloudflare\.com`)

func scanTunnel(r interface{ Read([]byte) (int, error) }, ch chan<- string) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if u := tunnelURL.FindString(sc.Text()); u != "" {
			select {
			case ch <- u:
			default:
			}
			return
		}
	}
}
