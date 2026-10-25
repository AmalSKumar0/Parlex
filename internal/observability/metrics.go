package observability

import (
	"context"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics contains all application-level Prometheus metrics.
type Metrics struct {
	Registry             *prometheus.Registry
	WSConnections        prometheus.Gauge
	TurnsTotal           *prometheus.CounterVec
	FirstAudioSeconds    prometheus.Histogram
	TurnTotalSeconds     prometheus.Histogram
	ProviderOpenSeconds  prometheus.Histogram
	AudioInSecondsTotal  prometheus.Counter
	AudioOutSecondsTotal prometheus.Counter
	ProviderErrorsTotal  *prometheus.CounterVec
	BreakerState         *prometheus.GaugeVec
	QueueOverflowTotal   *prometheus.CounterVec
	ProtocolViolations   *prometheus.CounterVec
}

// NewMetrics creates and registers all required Prometheus metrics.
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()

	wsConn := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "translator_ws_connections",
		Help: "Current number of active WebSocket device connections.",
	})
	turnsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "translator_turns_total",
		Help: "Total translation turns partitioned by direction, provider, and completion status.",
	}, []string{"direction", "provider", "status"})

	firstAudioSecs := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "translator_first_audio_seconds",
		Help:    "Latency in seconds from turn.start received to first downlink audio byte.",
		Buckets: []float64{0.2, 0.4, 0.6, 0.8, 1.0, 1.5, 2.0, 3.0, 5.0, 10.0},
	})

	turnTotalSecs := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "translator_turn_total_seconds",
		Help:    "Total turn duration in seconds from turn.start to playback.done.",
		Buckets: []float64{1.0, 2.0, 5.0, 10.0, 20.0, 30.0, 60.0},
	})

	providerOpenSecs := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "translator_provider_open_seconds",
		Help:    "Latency in seconds to establish/open a translation provider session.",
		Buckets: []float64{0.05, 0.1, 0.2, 0.5, 1.0, 2.0, 5.0},
	})

	audioInSecs := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "translator_audio_in_seconds_total",
		Help: "Total seconds of uplink audio received.",
	})

	audioOutSecs := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "translator_audio_out_seconds_total",
		Help: "Total seconds of translated downlink audio transmitted.",
	})

	providerErrors := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "translator_provider_errors_total",
		Help: "Total provider errors by provider and error type.",
	}, []string{"provider", "type"})

	breakerState := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "translator_breaker_state",
		Help: "Circuit breaker state: 0 = closed (normal), 1 = open (tripped), 2 = half-open.",
	}, []string{"provider"})

	queueOverflow := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "translator_queue_overflow_total",
		Help: "Count of channel/buffer overflows by queue identifier.",
	}, []string{"queue"})

	protocolViolations := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "translator_protocol_violations_total",
		Help: "Count of protocol violations by error code.",
	}, []string{"code"})

	reg.MustRegister(
		wsConn,
		turnsTotal,
		firstAudioSecs,
		turnTotalSecs,
		providerOpenSecs,
		audioInSecs,
		audioOutSecs,
		providerErrors,
		breakerState,
		queueOverflow,
		protocolViolations,
	)

	return &Metrics{
		Registry:             reg,
		WSConnections:        wsConn,
		TurnsTotal:           turnsTotal,
		FirstAudioSeconds:    firstAudioSecs,
		TurnTotalSeconds:     turnTotalSecs,
		ProviderOpenSeconds:  providerOpenSecs,
		AudioInSecondsTotal:  audioInSecs,
		AudioOutSecondsTotal: audioOutSecs,
		ProviderErrorsTotal:  providerErrors,
		BreakerState:         breakerState,
		QueueOverflowTotal:   queueOverflow,
		ProtocolViolations:   protocolViolations,
	}
}

// StartInternalServer starts the internal listener serving /metrics and pprof.
func StartInternalServer(ctx context.Context, addr string, m *Metrics) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{}))

	// pprof endpoints
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		_ = srv.ListenAndServe()
	}()

	return srv
}
