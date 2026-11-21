// Package metrics serves rosetta's Prometheus metrics.
package metrics

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/raven-clown/rosetta"
)

// Metrics counts requests, runs, tokens and store errors on its own
// registry. The request series use the names shared by every mothership
// project.
type Metrics struct {
	reg         *prometheus.Registry
	runs        *prometheus.CounterVec
	duration    *prometheus.HistogramVec
	tokens      *prometheus.CounterVec
	storeErrors *prometheus.CounterVec
	requests    *prometheus.CounterVec
	reqDuration *prometheus.HistogramVec
	inflight    prometheus.Gauge
}

// New returns Metrics with Go and process collectors registered.
func New() *Metrics {
	m := &Metrics{
		reg: prometheus.NewRegistry(),
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rosetta_runs_total", Help: "Agent runs by final status.",
		}, []string{"agent", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "rosetta_run_duration_seconds", Help: "How long agent runs took.",
			Buckets: []float64{0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300},
		}, []string{"agent"}),
		tokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rosetta_tokens_total", Help: "Model tokens reported by agents.",
		}, []string{"agent", "direction"}),
		storeErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rosetta_store_errors_total", Help: "Failed session store calls.",
		}, []string{"op"}),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rosetta_requests_total", Help: "HTTP requests by route pattern and status code.",
		}, []string{"route", "code"}),
		reqDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "rosetta_request_duration_seconds", Help: "How long HTTP requests took, streams included.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route"}),
		inflight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "rosetta_inflight_requests", Help: "HTTP requests being served.",
		}),
	}
	m.reg.MustRegister(m.runs, m.duration, m.tokens, m.storeErrors, m.requests, m.reqDuration, m.inflight,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

// Middleware counts requests to next. The route label is the pattern mux
// would pick, or "other", so paths with IDs in them never become labels.
func (m *Metrics) Middleware(mux *http.ServeMux, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := "other"
		if _, pattern := mux.Handler(r); pattern != "" {
			route = pattern
		}
		m.inflight.Inc()
		defer m.inflight.Dec()
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		m.requests.WithLabelValues(route, strconv.Itoa(sw.status)).Inc()
		m.reqDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Flush keeps SSE streams working through the wrapper.
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the real writer.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// RunDone records a finished run.
func (m *Metrics) RunDone(agent string, err error, d time.Duration, u rosetta.Usage) {
	status := "completed"
	switch {
	case errors.Is(err, context.Canceled):
		status = "cancelled"
	case err != nil:
		status = "failed"
	}
	m.runs.WithLabelValues(agent, status).Inc()
	m.duration.WithLabelValues(agent).Observe(d.Seconds())
	if u.InputTokens > 0 {
		m.tokens.WithLabelValues(agent, "input").Add(float64(u.InputTokens))
	}
	if u.OutputTokens > 0 {
		m.tokens.WithLabelValues(agent, "output").Add(float64(u.OutputTokens))
	}
	if u.ReasoningTokens > 0 {
		m.tokens.WithLabelValues(agent, "reasoning").Add(float64(u.ReasoningTokens))
	}
}

// StoreError records a failed store call.
func (m *Metrics) StoreError(op string) { m.storeErrors.WithLabelValues(op).Inc() }

// Handler serves the metrics in the Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
