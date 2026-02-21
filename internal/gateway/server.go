package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"influx-bouncer/internal/cache"
	"influx-bouncer/internal/config"
	"influx-bouncer/internal/flux"
	"influx-bouncer/internal/metrics"
	"influx-bouncer/internal/upstream"
)

type Server struct {
	cfg      config.Config
	cache    *cache.Service
	upstream *upstream.Manager
	client   *http.Client
	metrics  *metrics.Metrics
}

func NewServer(cfg config.Config) *Server {
	m := metrics.NewMetrics(prometheus.DefaultRegisterer)
	// Instrument HTTP client with OpenTelemetry
	client := &http.Client{
		Timeout: cfg.UpstreamTimeout,
		Transport: otelhttp.NewTransport(
			http.DefaultTransport,
			otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
				return "upstream " + r.Method + " " + r.URL.Host
			}),
		),
	}

	return &Server{
		cfg:      cfg,
		cache:    cache.NewService(cfg, m),
		upstream: upstream.NewManager(cfg, client, m),
		client:   client,
		metrics:  m,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/ready", s.handleReady)
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/api/v2/query", s.handleQuery)
	mux.HandleFunc("/api/v2/", s.handleV2ReadProxy)

	// Instrument HTTP server with OpenTelemetry
	return otelhttp.NewHandler(mux, "gateway",
		otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
	)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	// Liveness probe
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	// Readiness probe
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.cache.Ping(ctx); err != nil {
		http.Error(w, "redis unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !validGatewayAuth(r.Header.Get("Authorization"), s.cfg.GatewayToken) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	org := r.URL.Query().Get("org")
	if org == "" {
		http.Error(w, "org is required", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}
	query := string(body)
	accept := r.Header.Get("Accept")
	contentType := r.Header.Get("Content-Type")
	lowerAccept := strings.ToLower(accept)

	bodyBuilder := func(fluxQuery string) ([]byte, error) { return []byte(fluxQuery), nil }
	if strings.Contains(strings.ToLower(contentType), "application/json") {
		f, builder, ok := flux.ParseFluxJSONBody(body)
		if ok {
			query = f
			bodyBuilder = builder
		}
	}

	qsig := flux.QuerySignature(query)
	qpreview := flux.QueryPreview(query, s.cfg.QueryPreviewChars)

	logCatalog := func(status int, split bool, stage, errMsg string) {
		dur := time.Since(started)
		failed := status < 200 || status >= 300 || errMsg != ""
		if !failed && (s.cfg.SlowQueryThreshold <= 0 || dur < s.cfg.SlowQueryThreshold) {
			return
		}
		level := slog.LevelInfo
		msg := "gateway_query_slow"
		if failed {
			level = slog.LevelError
			msg = "gateway_query_fail"
		}

		slog.Log(r.Context(), level, msg,
			"org", org,
			"split", split,
			"stage", stage,
			"status", status,
			"dur_ms", dur.Milliseconds(),
			"sig", qsig,
			"query", qpreview,
			"err", errMsg,
		)
	}

	// Arrow is forwarded with retries/circuit-breakers, but split/merge currently supports CSV only.
	allowSplit := accept == "" || strings.Contains(lowerAccept, "csv")

	cutoff := flux.SplitCutoff(time.Now(), s.cfg.FreshnessWindow)
	cachedQuery, liveQuery, split := flux.SplitNowQuery(query, cutoff)
	split = split && allowSplit

	if !split {
		payload, hdr, status, source, targetName, err := s.fetchOrCache(r.Context(), org, string(body), body, accept, contentType)
		if err != nil {
			logCatalog(http.StatusBadGateway, false, "fetch_or_cache", err.Error())
			slog.ErrorContext(r.Context(), "gateway_query_error", "org", org, "split", false, "err", err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeHeaders(w.Header(), hdr)
		w.Header().Set("X-Cache", source)
		if targetName != "" {
			w.Header().Set("X-Target", targetName)
		}
		logCatalog(status, false, "nonsplit", "")
		w.WriteHeader(status)
		_, _ = w.Write(payload)
		return
	}

	cachedBody, err := bodyBuilder(cachedQuery)
	if err != nil {
		http.Error(w, "cached body build failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	liveBody, err := bodyBuilder(liveQuery)
	if err != nil {
		http.Error(w, "live body build failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Use the actual rewritten request body as cache signature to avoid cross-range key collisions.
	// This is conservative for JSON payloads (may reduce hit rate), but preserves correctness.
	cachedSig := string(cachedBody)

	cachedPayload, _, cachedStatus, cachedSource, cachedTarget, err := s.fetchOrCache(r.Context(), org, cachedSig, cachedBody, accept, contentType)
	if err != nil {
		logCatalog(http.StatusBadGateway, true, "cached_fetch", err.Error())
		slog.ErrorContext(r.Context(), "gateway_query_error", "org", org, "split", true, "phase", "cached", "err", err)
		http.Error(w, "cached segment failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if cachedStatus/100 != 2 {
		logCatalog(cachedStatus, true, "cached_status", "cached segment non-2xx")
		w.WriteHeader(cachedStatus)
		_, _ = w.Write(cachedPayload)
		return
	}

	livePayload, liveHdr, liveStatus, liveTarget, err := s.forward(r.Context(), org, liveBody, accept, contentType)
	if err != nil {
		logCatalog(http.StatusBadGateway, true, "live_forward", err.Error())
		slog.ErrorContext(r.Context(), "gateway_query_error", "org", org, "split", true, "phase", "live", "err", err)
		http.Error(w, "live segment failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if liveStatus/100 != 2 {
		logCatalog(liveStatus, true, "live_status", "live segment non-2xx")
		w.WriteHeader(liveStatus)
		_, _ = w.Write(livePayload)
		return
	}

	merged := flux.MergeCSV(cachedPayload, livePayload)
	writeHeaders(w.Header(), liveHdr)
	w.Header().Set("X-Now-Split", "true")
	w.Header().Set("X-Cache", cachedSource)
	if cachedTarget != "" {
		w.Header().Set("X-Target-Cached", cachedTarget)
	}
	if liveTarget != "" {
		w.Header().Set("X-Target-Live", liveTarget)
	}
	logCatalog(http.StatusOK, true, "merged", "")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(merged)
}

func (s *Server) handleV2ReadProxy(w http.ResponseWriter, r *http.Request) {
	// /api/v2/query has a dedicated path with split/cache behavior.
	if r.URL.Path == "/api/v2/query" {
		s.handleQuery(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !validGatewayAuth(r.Header.Get("Authorization"), s.cfg.GatewayToken) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	payload, hdr, status, targetName, err := s.forwardRead(r.Context(), r)
	if err != nil {
		slog.ErrorContext(r.Context(), "gateway_read_error", "path", r.URL.Path, "query", r.URL.RawQuery, "err", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeHeaders(w.Header(), hdr)
	if targetName != "" {
		w.Header().Set("X-Target", targetName)
	}
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(payload)
	}
}

func (s *Server) fetchOrCache(ctx context.Context, org, cacheSig string, forwardBody []byte, accept, contentType string) ([]byte, http.Header, int, string, string, error) {
	lowerAccept := strings.ToLower(accept)
	cacheable := accept == "" || strings.Contains(lowerAccept, "csv") || strings.Contains(lowerAccept, "arrow")
	if !cacheable {
		payload, hdr, status, targetName, err := s.forward(ctx, org, forwardBody, accept, contentType)
		if err != nil {
			return nil, nil, 0, "", "", err
		}
		s.metrics.IncCacheResult("BYPASS")
		return payload, hdr, status, "BYPASS", targetName, nil
	}

	key := cache.Key(org, cacheSig)

	cached, err := s.cache.Get(ctx, key)
	if err == nil {
		s.metrics.IncCacheResult("HIT")
		return cached, defaultCachedHeader(accept), http.StatusOK, "HIT", "", nil
	}
	if !errors.Is(err, redis.Nil) {
		return nil, nil, 0, "", "", err
	}

	locked, lockKey, err := s.cache.AcquireLock(ctx, key)
	if err != nil {
		return nil, nil, 0, "", "", err
	}

	if !locked {
		// Wait for lock
		cached, err := s.cache.WaitForLock(ctx, key)
		if err == nil {
			s.metrics.IncCacheResult("HIT_WAIT")
			return cached, defaultCachedHeader(accept), http.StatusOK, "HIT_WAIT", "", nil
		}

		// If we timed out or got error, just forward
		payload, hdr, status, targetName, fwdErr := s.forward(ctx, org, forwardBody, accept, contentType)
		if fwdErr != nil {
			return nil, nil, 0, "", "", fwdErr
		}
		s.metrics.IncCacheResult("MISS_RACE")
		return payload, hdr, status, "MISS_RACE", targetName, nil
	}

	defer func() {
		_ = s.cache.ReleaseLock(context.Background(), lockKey)
	}()

	payload, hdr, status, targetName, err := s.forward(ctx, org, forwardBody, accept, contentType)
	if err != nil {
		return nil, nil, 0, "", "", err
	}
	if status/100 == 2 {
		_ = s.cache.Set(ctx, key, payload)
	}
	s.metrics.IncCacheResult("MISS")
	return payload, hdr, status, "MISS", targetName, nil
}

func (s *Server) forward(ctx context.Context, org string, body []byte, accept, contentType string) ([]byte, http.Header, int, string, error) {
	attempts := s.cfg.ForwardRetries + 1
	if attempts < 1 {
		attempts = 1
	}

	tried := make(map[string]struct{}, attempts)
	var lastErr error
	for i := 0; i < attempts; i++ {
		target, ok := s.upstream.PickTarget(tried)
		if !ok {
			break
		}
		tried[target.Name] = struct{}{}
		attemptStart := time.Now()
		payload, hdr, status, err := s.forwardToTarget(ctx, target, org, body, accept, contentType)
		attemptDur := time.Since(attemptStart)
		if err != nil {
			lastErr = err
			s.metrics.ObserveUpstream(target.Name, "error", attemptDur)
			slog.ErrorContext(ctx, "upstream_query_error",
				"target", target.Name, "org", org, "attempt", i+1, "total_attempts", attempts,
				"dur_ms", attemptDur.Milliseconds(), "err", err)
			s.upstream.RecordFailure(target.Name)
			continue
		}
		s.metrics.ObserveUpstream(target.Name, strconv.Itoa(status), attemptDur)
		if status >= 500 {
			lastErr = fmt.Errorf("upstream %s returned status %d", target.Name, status)
			slog.ErrorContext(ctx, "upstream_query_5xx",
				"target", target.Name, "org", org, "attempt", i+1, "total_attempts", attempts,
				"status", status, "dur_ms", attemptDur.Milliseconds())
			s.upstream.RecordFailure(target.Name)
			if i < attempts-1 {
				continue
			}
		}

		if status < 500 {
			s.upstream.RecordSuccess(target.Name)
		}
		return payload, hdr, status, target.Name, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no available targets")
	}
	slog.ErrorContext(ctx, "upstream_query_exhausted", "org", org, "attempts", attempts, "err", lastErr)
	return nil, nil, 0, "", lastErr
}

func (s *Server) forwardToTarget(ctx context.Context, target config.Target, org string, body []byte, accept, contentType string) ([]byte, http.Header, int, error) {
	endpoint, err := url.JoinPath(target.URL, "/api/v2/query")
	if err != nil {
		return nil, nil, 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"?org="+url.QueryEscape(org), strings.NewReader(string(body)))
	if err != nil {
		return nil, nil, 0, err
	}
	req.Header.Set("Authorization", "Token "+target.Token)
	if accept != "" {
		req.Header.Set("Accept", accept)
	} else {
		req.Header.Set("Accept", "application/csv")
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	} else {
		req.Header.Set("Content-Type", "application/vnd.flux")
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, nil, 0, err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, 0, err
	}
	return payload, resp.Header.Clone(), resp.StatusCode, nil
}

func (s *Server) forwardRead(ctx context.Context, in *http.Request) ([]byte, http.Header, int, string, error) {
	attempts := s.cfg.ForwardRetries + 1
	if attempts < 1 {
		attempts = 1
	}

	tried := make(map[string]struct{}, attempts)
	var lastErr error
	for i := 0; i < attempts; i++ {
		target, ok := s.upstream.PickTarget(tried)
		if !ok {
			break
		}
		tried[target.Name] = struct{}{}
		start := time.Now()
		payload, hdr, status, err := s.forwardReadToTarget(ctx, target, in)
		dur := time.Since(start)
		if err != nil {
			lastErr = err
			s.metrics.ObserveUpstream(target.Name, "error", dur)
			slog.ErrorContext(ctx, "upstream_read_error",
				"target", target.Name, "path", in.URL.Path, "query", in.URL.RawQuery,
				"attempt", i+1, "total_attempts", attempts, "dur_ms", dur.Milliseconds(), "err", err)
			s.upstream.RecordFailure(target.Name)
			continue
		}
		s.metrics.ObserveUpstream(target.Name, strconv.Itoa(status), dur)
		if status >= 500 {
			lastErr = fmt.Errorf("upstream %s returned status %d", target.Name, status)
			slog.ErrorContext(ctx, "upstream_read_5xx",
				"target", target.Name, "path", in.URL.Path, "query", in.URL.RawQuery,
				"attempt", i+1, "total_attempts", attempts, "status", status, "dur_ms", dur.Milliseconds())
			s.upstream.RecordFailure(target.Name)
			if i < attempts-1 {
				continue
			}
		}
		if status < 500 {
			s.upstream.RecordSuccess(target.Name)
		}
		return payload, hdr, status, target.Name, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no available targets")
	}
	slog.ErrorContext(ctx, "upstream_read_exhausted", "path", in.URL.Path, "query", in.URL.RawQuery, "attempts", attempts, "err", lastErr)
	return nil, nil, 0, "", lastErr
}

func (s *Server) forwardReadToTarget(ctx context.Context, target config.Target, in *http.Request) ([]byte, http.Header, int, error) {
	endpoint, err := url.JoinPath(target.URL, in.URL.Path)
	if err != nil {
		return nil, nil, 0, err
	}
	if in.URL.RawQuery != "" {
		endpoint += "?" + in.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(ctx, in.Method, endpoint, nil)
	if err != nil {
		return nil, nil, 0, err
	}
	req.Header.Set("Authorization", "Token "+target.Token)
	if accept := in.Header.Get("Accept"); accept != "" {
		req.Header.Set("Accept", accept)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, nil, 0, err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, 0, err
	}
	return payload, resp.Header.Clone(), resp.StatusCode, nil
}

func validGatewayAuth(header, token string) bool {
	if token == "" {
		return false
	}
	header = strings.TrimSpace(header)
	if header == "" {
		return false
	}
	// Support both Bearer (manual curl) and Token (Grafana Influx datasource).
	for _, pfx := range []string{"Bearer ", "Token "} {
		if strings.HasPrefix(header, pfx) {
			return strings.TrimSpace(strings.TrimPrefix(header, pfx)) == token
		}
	}
	return false
}

func writeHeaders(dst, src http.Header) {
	for k, vals := range src {
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

func defaultCachedHeader(accept string) http.Header {
	lower := strings.ToLower(accept)
	if strings.Contains(lower, "arrow") {
		return http.Header{"Content-Type": []string{"application/vnd.apache.arrow.stream"}}
	}
	return http.Header{"Content-Type": []string{"text/csv; charset=utf-8"}}
}
