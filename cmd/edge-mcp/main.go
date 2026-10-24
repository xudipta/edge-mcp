// Command edge-mcp is a Model Context Protocol server for Kubernetes clusters at the edge.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/xudipta/edge-mcp/internal/edge"
	"github.com/xudipta/edge-mcp/internal/edgetest"
	"github.com/xudipta/edge-mcp/internal/server"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "edge-mcp:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := edge.DefaultConfig()
	var (
		kubeconfig   = flag.String("kubeconfig", "", "path to a kubeconfig (default: $KUBECONFIG, ~/.kube/config, or in-cluster)")
		kubeContext  = flag.String("context", "", "kubeconfig context to use")
		httpAddr     = flag.String("http", "", "serve Streamable HTTP on this address (e.g. 127.0.0.1:8080) instead of stdio")
		httpToken    = flag.String("http-token", os.Getenv("EDGE_MCP_TOKEN"), "require this bearer token on every HTTP request (default: $EDGE_MCP_TOKEN)")
		httpInsecure = flag.Bool("http-insecure", false, "allow --http on a non-loopback address without a token")
		namespaces   = flag.String("namespaces", "", "comma-separated namespace allowlist (default: all namespaces)")
		maxReplicas  = flag.Int("max-replicas", int(cfg.MaxReplicas), "upper bound for scale_deployment")
		version      = flag.Bool("version", false, "print the version and exit")
		demo         = flag.Bool("demo", false, "serve a built-in sample edge fleet instead of a real cluster (no kubeconfig needed)")
	)
	flag.StringVar(&cfg.SiteLabel, "site-label", cfg.SiteLabel, "node label that names a node's site")
	flag.BoolVar(&cfg.AllowWrites, "allow-writes", false, "enable cordon/uncordon/restart/scale tools (each still dry-runs by default)")
	flag.DurationVar(&cfg.StaleHeartbeat, "stale-heartbeat", cfg.StaleHeartbeat, "flag nodes whose last heartbeat is older than this")
	flag.Parse()

	if *version {
		fmt.Println("edge-mcp", server.Version)
		return nil
	}
	// stdout carries the MCP protocol in stdio mode, so all logging goes to stderr.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	if *maxReplicas < 1 || *maxReplicas > math.MaxInt32 {
		return fmt.Errorf("--max-replicas must be between 1 and %d", math.MaxInt32)
	}
	cfg.MaxReplicas = int32(*maxReplicas) // #nosec G115 -- range checked above
	for _, ns := range strings.Split(*namespaces, ",") {
		if ns = strings.TrimSpace(ns); ns != "" {
			cfg.Namespaces = append(cfg.Namespaces, ns)
		}
	}

	var cluster *edge.Cluster
	if *demo {
		cluster = edge.New(edgetest.Fleet(), cfg).WithClock(func() time.Time { return edgetest.Now }).WithLogSource(edgetest.Logs)
	} else {
		client, err := kubeClient(*kubeconfig, *kubeContext)
		if err != nil {
			return err
		}
		cluster = edge.New(client, cfg)
	}

	srv := server.New(cluster)
	slog.Info("edge-mcp starting", "version", server.Version, "demo", *demo, "writes", cfg.AllowWrites, "namespaces", cfg.Namespaces, "site_label", cfg.SiteLabel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *httpAddr == "" {
		// Piped input closes stdin right after the last request; hold EOF back until it is answered.
		in, out := drainingStdio(os.Stdin, nopCloser{os.Stdout}, 30*time.Second)
		return srv.Run(ctx, &mcp.IOTransport{Reader: in, Writer: out})
	}
	if err := checkHTTPExposure(*httpAddr, *httpToken, *httpInsecure); err != nil {
		return err
	}
	var handler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	if *httpToken != "" {
		handler = bearerAuth(*httpToken, handler)
	} else {
		slog.Warn("HTTP mode is running without authentication", "addr", *httpAddr)
	}
	hs := &http.Server{Addr: *httpAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdown)
	}()
	slog.Info("serving MCP over Streamable HTTP", "addr", *httpAddr)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// kubeClient loads a client from an explicit kubeconfig, $KUBECONFIG, ~/.kube/config or the in-cluster service account.
func kubeClient(path, kubeContext string) (kubernetes.Interface, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if path != "" {
		rules.ExplicitPath = path
	}
	restCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules,
		&clientcmd.ConfigOverrides{CurrentContext: kubeContext}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}
	restCfg.UserAgent = "edge-mcp/" + server.Version
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}
	return client, nil
}

// nopCloser keeps the transport from closing os.Stdout.
type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }
