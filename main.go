package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sync/atomic"
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
	if err = json.Unmarshal(configFile, &config); err != nil {
		slog.Error("Critical Error: Failed to parse config.json", "error", err)
		os.Exit(1)
	}

	slog.Info("Configuration loaded successfully", 
		"proxy_port", config.ProxyPort, 
		"metrics_port", config.MetricsPort,
	)

	if len(config.Routing) == 0 {
		slog.Error("Critical Error: No routing configuration found")
		os.Exit(1)
	}

	route := config.Routing[0]
	pool, err := NewUpstreamPool(route.Path, route.Upstreams)
	if err != nil {
		slog.Error("Critical Error: Failed to initialize upstream pool", "error", err)
		os.Exit(1)
	}

	slog.Info("Upstream pool initialized", "path", route.Path, "count", pool.total)

	http.HandleFunc(route.Path+"/", func(w http.ResponseWriter, r *http.Request) {
		proxy := pool.Next()
		if proxy == nil {
			slog.Warn("No upstreams available for request", "path", r.URL.Path)
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
			return;
		}

		clientIP := r.RemoteAddr
		r.Header.Set("X-Forwarded-For", clientIP)

		slog.Info("Proxying request", 
			"method", r.Method, 
			"path", r.URL.Path, 
			"client_ip", clientIP,
		)

		proxy.ServeHTTP(w, r)
	})

	slog.Info("Starting Data Plane server...", "port", config.ProxyPort)
	if err := http.ListenAndServe(config.ProxyPort, nil); err != nil {
		slog.Error("Data Plane server crashed", "error", err)
		os.Exit(1)
	}
}
