package proxy

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
)

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

type StatusResponseWriter struct {
	http.ResponseWriter
	StatusCode int
}

func NewStatusResponseWriter(w http.ResponseWriter) *StatusResponseWriter {
	return &StatusResponseWriter{ResponseWriter: w, StatusCode: http.StatusOK}
}

func (se *StatusResponseWriter) WriteHeader(code int) {
	se.StatusCode = code
	se.ResponseWriter.WriteHeader(code)
}
