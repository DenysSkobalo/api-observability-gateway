package telemetry

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
)

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

func (m *MetricsCollector) ExportPrometheus(w http.ResponseWriter) {
	fmt.Fprintf(w, "# HELP gateway_requests_total Total number of processed HTTP requests.\n")
	fmt.Fprintf(w, "# TYPE gateway_requests_total counter\n")
	fmt.Fprintf(w, "gateway_requests_total %d\n\n", atomic.LoadUint64(&m.totalRequests))

	fmt.Fprintf(w, "# HELP gateway_responses_by_status_total Total number of HTTP responses grouped by status code.\n")
	fmt.Fprintf(w, "# TYPE gateway_responses_by_status_total counter\n")
	
	m.statusMutex.RLock()
	for code, valPtr := range m.statusCodes {
		fmt.Fprintf(w, "gateway_responses_by_status_total{code=\"%s\"} %d\n", code, atomic.LoadUint64(valPtr))
	}
	m.statusMutex.RUnlock()
}
