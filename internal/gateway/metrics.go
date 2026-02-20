package gateway

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type Metrics struct {
	cacheResults *prometheus.CounterVec
	redisLatency *prometheus.HistogramVec
	upLatency    *prometheus.HistogramVec
}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		cacheResults: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "influx_gateway_cache_results_total",
				Help: "Count of cache outcomes in gateway read path.",
			},
			[]string{"result"},
		),
		redisLatency: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "influx_gateway_redis_latency_seconds",
				Help:    "Redis operation latency in seconds.",
				Buckets: []float64{0.0005, 0.001, 0.002, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25},
			},
			[]string{"op", "ok"},
		),
		upLatency: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "influx_gateway_upstream_latency_seconds",
				Help:    "Upstream Influx query latency in seconds.",
				Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
			},
			[]string{"target", "status"},
		),
	}

	reg.MustRegister(m.cacheResults, m.redisLatency, m.upLatency)
	return m
}

func (m *Metrics) IncCacheResult(result string) {
	m.cacheResults.WithLabelValues(result).Inc()
}

func (m *Metrics) ObserveRedis(op string, ok bool, dur time.Duration) {
	m.redisLatency.WithLabelValues(op, strconv.FormatBool(ok)).Observe(dur.Seconds())
}

func (m *Metrics) ObserveUpstream(target string, status string, dur time.Duration) {
	m.upLatency.WithLabelValues(target, status).Observe(dur.Seconds())
}

