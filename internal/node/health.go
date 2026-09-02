package node

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metricsServer is the daemon's operational surface: liveness, readiness and Prometheus
// metrics, on loopback and without authentication — a probe that needs a token is a probe
// that reports the wrong thing.
type metricsServer struct {
	http *http.Server
	ln   net.Listener
}

func (m *metricsServer) addr() string {
	if m == nil || m.ln == nil {
		return ""
	}
	return m.ln.Addr().String()
}

// serveMetrics binds cfg.MetricsListen and starts serving. A bind failure is fatal: an
// operator who asked for a metrics endpoint should hear that they did not get one.
func (n *Node) serveMetrics(ctx context.Context) error {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "podium_node_running_tasks",
			Help: "Tasks this node is currently running.",
		}, func() float64 { return float64(n.runningCount()) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "podium_node_free_slots",
			Help: "Task slots this node is advertising to the scheduler.",
		}, func() float64 { return float64(n.freeSlots()) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "podium_node_stream_connected",
			Help: "1 when the node holds an open stream to the control plane.",
		}, func() float64 {
			if n.connected.Load() {
				return 1
			}
			return 0
		}),
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writePlain(w, http.StatusOK, "ok")
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !n.connected.Load() {
			writePlain(w, http.StatusServiceUnavailable, "control plane stream is down")
			return
		}
		writePlain(w, http.StatusOK, "ok")
	})
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", n.cfg.MetricsListen)
	if err != nil {
		return fmt.Errorf("bind the node's metrics endpoint on %s: %w "+
			"(set PODIUM_NODE_METRICS_LISTEN to a free loopback port)", n.cfg.MetricsListen, err)
	}

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	n.metrics = &metricsServer{http: srv, ln: ln}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			n.logger.Error("node metrics endpoint stopped", "error", err)
		}
	}()
	n.logger.InfoContext(ctx, "node health and metrics listening", "addr", ln.Addr().String())
	return nil
}

func (n *Node) stopMetrics(ctx context.Context) {
	if n.metrics == nil {
		return
	}
	_ = n.metrics.http.Shutdown(ctx)
}

func writePlain(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body + "\n"))
}
