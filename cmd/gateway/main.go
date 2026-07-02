package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"api-observability-gateway/internal/config"
	"api-observability-gateway/internal/proxy"
	"api-observability-gateway/internal/telemetry"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	slog.Info("Initializing Observability-First API Gateway...")

	cfg, err := config.LoadConfig("config.json")
	if err != nil {
		slog.Error("Critical Error: Failed to load config", "error", err)
		os.Exit(1)
	}

	slog.Info("Configuration loaded successfully", "proxy_port", cfg.ProxyPort, "metrics_port", cfg.MetricsPort)

	metrics := telemetry.NewMetricsCollector()
	route := cfg.Routing[0]
	
	pool, err := proxy.NewUpstreamPool(route.Path, route.Upstreams)
	if err != nil {
		slog.Error("Critical Error: Failed to initialize upstream pool", "error", err)
		os.Exit(1)
	}

	http.HandleFunc(route.Path+"/", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		metrics.IncRequest()

		upstream := pool.Next()
		if upstream == nil {
			slog.Warn("No upstreams available for request", "path", r.URL.Path)
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
			metrics.IncStatus(http.StatusServiceUnavailable)
			return
		}

		r.Header.Set("X-Forwarded-For", r.RemoteAddr)
		wrappedWriter := proxy.NewStatusResponseWriter(w)

		upstream.ServeHTTP(wrappedWriter, r)

		duration := time.Since(start)
		metrics.IncStatus(wrappedWriter.StatusCode)

		slog.Info("Request processed",
			"method", r.Method,
			"path", r.URL.Path,
			"status", wrappedWriter.StatusCode,
			"latency_ms", duration.Milliseconds(),
		)
	})

	metricsMux := http.NewServeMux()
	metricsMux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		metrics.ExportPrometheus(w)
	})

	go func() {
		slog.Info("Starting Observability Plane server...", "port", cfg.MetricsPort)
		if err := http.ListenAndServe(cfg.MetricsPort, metricsMux); err != nil {
			slog.Error("Observability Plane server crashed", "error", err)
		}
	}()

	slog.Info("Starting Data Plane server...", "port", cfg.ProxyPort)
	if err := http.ListenAndServe(cfg.ProxyPort, nil); err != nil {
		slog.Error("Data Plane server crashed", "error", err)
		os.Exit(1)
	}
}
