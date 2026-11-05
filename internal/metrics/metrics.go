// Package metrics serves rosetta's Prometheus metrics.
package metrics

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/raven-clown/rosetta"
)

// Metrics counts runs, tokens and store errors on its own registry.
type Metrics struct {
	reg         *prometheus.Registry
	runs        *prometheus.CounterVec
	duration    *prometheus.HistogramVec
	tokens      *prometheus.CounterVec
	storeErrors *prometheus.CounterVec
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
	}
	m.reg.MustRegister(m.runs, m.duration, m.tokens, m.storeErrors,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

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
}

// StoreError records a failed store call.
func (m *Metrics) StoreError(op string) { m.storeErrors.WithLabelValues(op).Inc() }

// Handler serves the metrics in the Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
