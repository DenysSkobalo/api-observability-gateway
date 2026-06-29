package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type Config struct {
	ProxyPort   string        `json:"proxy_port"`
	MetricsPort string        `json:"metrics_port"`
	Routing     []RouteConfig `json:"routing"`
}

type RouteConfig struct {
	Path      string   `json:"path"`
	Upstreams []string `json:"upstreams"`
}

type UpstreamPool struct {
	proxies []*httputil.ReverseProxy
	counter uint64
	total   uint64
}

type MetricsCollector struct {
	totalRequests uint64
	statusCodes   map[string]*uint64
	statusMutex   sync.RWMutex
}

func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		statusCodes: make(map[string]*uint64),
	}
}

func (m *MetricsCollector) IncRequest() {
	atomic.AddUint64(&m.totalRequests, 1)
}

func (m *MetricsCollector) IncStatus(code int) {
	codeStr := strconv.Itoa(code)
	
	m.statusMutex.RLock()
	counter, exists := m.statusCodes[codeStr]
	m.statusMutex.RUnlock()

	if exists {
		atomic.AddUint64(counter, 1)
		return
	}

	m.statusMutex.Lock()
	if counter, exists = m.statusCodes[codeStr]; !exists {
		var val uint64 = 1
		m.statusCodes[codeStr] = &val
	} else {
		atomic.AddUint64(counter, 1)
	}
	m.statusMutex.Unlock()
}

func NewUpstreamPool(prefix string, urls []string) (*UpstreamPool, error) {
	var proxies []*httputil.ReverseProxy
	for _, rawURL := range urls {
		target, err := url.Parse(rawURL)
		if err != nil {
			return nil, err
		}
		
		proxy := httputil.NewSingleHostReverseProxy(target)
		
		originalDirector := proxy.Director
		proxy.Director = func(req *http.Request) {
			originalDirector(req)
			if len(req.URL.Path) >= len(prefix) && req.URL.Path[:len(prefix)] == prefix {
				req.URL.Path = req.URL.Path[len(prefix):]
				if req.URL.Path == "" {
					req.URL.Path = "/"
				}
			}
		}
		proxies = append(proxies, proxy)
	}

	return &UpstreamPool{
		proxies: proxies,
		total:   uint64(len(proxies)),
	}, nil
}

func (p *UpstreamPool) Next() *httputil.ReverseProxy {
	if p.total == 0 {
		return nil
	}
	idx := atomic.AddUint64(&p.counter, 1)
	return p.proxies[(idx-1)%p.total]
}

type StatusResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func NewStatusResponseWriter(w http.ResponseWriter) *StatusResponseWriter {
	return &StatusResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
}

func (se *StatusResponseWriter) WriteHeader(code int) {
	se.statusCode = code
	se.ResponseWriter.WriteHeader(code)
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	slog.Info("Initializing Observability-First API Gateway...")

	configFile, err := os.ReadFile("config.json")
	if err != nil {
		slog.Error("Critical Error: Failed to read config.json", "error", err)
		os.Exit(1)
	}

	var config Config
	if err := json.Unmarshal(configFile, &config); err != nil {
		slog.Error("Critical Error: Failed to parse config.json", "error", err)
		os.Exit(1)
	}

	slog.Info("Configuration loaded successfully", "proxy_port", config.ProxyPort, "metrics_port", config.MetricsPort)

	metrics := NewMetricsCollector()
	route := config.Routing[0]
	pool, err := NewUpstreamPool(route.Path, route.Upstreams)
	if err != nil {
		slog.Error("Critical Error: Failed to initialize upstream pool", "error", err)
		os.Exit(1)
	}

	http.HandleFunc(route.Path+"/", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		metrics.IncRequest() 

		proxy := pool.Next()
		if proxy == nil {
			slog.Warn("No upstreams available for request", "path", r.URL.Path)
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
			metrics.IncStatus(http.StatusServiceUnavailable)
			return
		}

		clientIP := r.RemoteAddr
		r.Header.Set("X-Forwarded-For", clientIP)

		wrappedWriter := NewStatusResponseWriter(w)

		proxy.ServeHTTP(wrappedWriter, r)

		duration := time.Since(start)
		metrics.IncStatus(wrappedWriter.statusCode)

		slog.Info("Request processed",
			"method", r.Method,
			"path", r.URL.Path,
			"status", wrappedWriter.statusCode,
			"latency_ms", duration.Milliseconds(),
		)
	})

	metricsMux := http.NewServeMux()
	metricsMux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		
		fmt.Fprintf(w, "# HELP gateway_requests_total Total number of processed HTTP requests.\n")
		fmt.Fprintf(w, "# TYPE gateway_requests_total counter\n")
		fmt.Fprintf(w, "gateway_requests_total %d\n\n", atomic.LoadUint64(&metrics.totalRequests))

		fmt.Fprintf(w, "# HELP gateway_responses_by_status_total Total number of HTTP responses grouped by status code.\n")
		fmt.Fprintf(w, "# TYPE gateway_responses_by_status_total counter\n")
		
		metrics.statusMutex.RLock()
		for code, valPtr := range metrics.statusCodes {
			fmt.Fprintf(w, "gateway_responses_by_status_total{code=\"%s\"} %d\n", code, atomic.LoadUint64(valPtr))
		}
		metrics.statusMutex.RUnlock()
	})

	go func() {
		slog.Info("Starting Observability Plane server...", "port", config.MetricsPort)
		if err := http.ListenAndServe(config.MetricsPort, metricsMux); err != nil {
			slog.Error("Observability Plane server crashed", "error", err)
		}
	}()

	slog.Info("Starting Data Plane server...", "port", config.ProxyPort)
	if err := http.ListenAndServe(config.ProxyPort, nil); err != nil {
		slog.Error("Data Plane server crashed", "error", err)
		os.Exit(1)
	}
}
