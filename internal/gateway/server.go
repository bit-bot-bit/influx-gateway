package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

type targetState struct {
	consecutiveFailures int
	circuitOpenUntil    time.Time
}

type Server struct {
	cfg          Config
	redis        *redis.Client
	client       *http.Client
	metrics      *Metrics
	rr           uint64
	targetStates map[string]*targetState
	mu           sync.RWMutex
}

func NewServer(cfg Config) *Server {
	s := &Server{
		cfg: cfg,
		redis: redis.NewClient(&redis.Options{
			Addr: cfg.RedisAddr,
		}),
		client: &http.Client{Timeout: cfg.UpstreamTimeout},
		metrics: NewMetrics(prometheus.DefaultRegisterer),
		targetStates: func() map[string]*targetState {
			out := make(map[string]*targetState, len(cfg.Targets))
			for _, t := range cfg.Targets {
				out[t.Name] = &targetState{}
			}
			return out
		}(),
	}

	if cfg.ProbeInterval > 0 {
		go s.healthLoop()
	}
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/api/v2/query", s.handleQuery)
	mux.HandleFunc("/api/v2/", s.handleV2ReadProxy)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.redis.Ping(ctx).Err(); err != nil {
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
	bodyBuilder := func(flux string) ([]byte, error) { return []byte(flux), nil }
	if strings.Contains(strings.ToLower(contentType), "application/json") {
		flux, builder, ok := parseFluxJSONBody(body)
		if ok {
			query = flux
			bodyBuilder = builder
		}
	}
	qsig := querySignature(query)
	qpreview := queryPreview(query, s.cfg.QueryPreviewChars)
	logCatalog := func(status int, split bool, stage, errMsg string) {
		dur := time.Since(started)
		failed := status < 200 || status >= 300 || errMsg != ""
		if !failed && (s.cfg.SlowQueryThreshold <= 0 || dur < s.cfg.SlowQueryThreshold) {
			return
		}
		event := "gateway_query_slow"
		if failed {
			event = "gateway_query_fail"
		}
		log.Printf("%s org=%s split=%t stage=%s status=%d dur_ms=%d sig=%s query=%q err=%q",
			event, org, split, stage, status, dur.Milliseconds(), qsig, qpreview, errMsg)
	}

	// Arrow is forwarded with retries/circuit-breakers, but split/merge currently supports CSV only.
	allowSplit := accept == "" || strings.Contains(lowerAccept, "csv")

	cutoff := splitCutoff(time.Now(), s.cfg.FreshnessWindow)
	cachedQuery, liveQuery, split := SplitNowQuery(query, cutoff)
	split = split && allowSplit
	if !split {
		payload, hdr, status, source, targetName, err := s.fetchOrCache(r.Context(), org, string(body), body, accept, contentType)
		if err != nil {
			logCatalog(http.StatusBadGateway, false, "fetch_or_cache", err.Error())
			log.Printf("gateway_query_error org=%s split=false err=%q", org, err)
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
		log.Printf("gateway_query_error org=%s split=true phase=cached err=%q", org, err)
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
		log.Printf("gateway_query_error org=%s split=true phase=live err=%q", org, err)
		http.Error(w, "live segment failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if liveStatus/100 != 2 {
		logCatalog(liveStatus, true, "live_status", "live segment non-2xx")
		w.WriteHeader(liveStatus)
		_, _ = w.Write(livePayload)
		return
	}

	merged := MergeCSV(cachedPayload, livePayload)
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
		log.Printf("gateway_read_error path=%s query=%q err=%q", r.URL.Path, r.URL.RawQuery, err)
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

	key := cacheKey(org, cacheSig)
	getStart := time.Now()
	cached, err := s.redis.Get(ctx, key).Bytes()
	s.metrics.ObserveRedis("get", err == nil || errors.Is(err, redis.Nil), time.Since(getStart))
	if err == nil {
		s.metrics.IncCacheResult("HIT")
		return cached, defaultCachedHeader(accept), http.StatusOK, "HIT", "", nil
	}
	if !errors.Is(err, redis.Nil) {
		return nil, nil, 0, "", "", err
	}

	lockKey := key + ":lock"
	lockToken := fmt.Sprintf("%d", time.Now().UnixNano())
	lockStart := time.Now()
	locked, err := s.redis.SetNX(ctx, lockKey, lockToken, s.cfg.CacheLockTTL).Result()
	s.metrics.ObserveRedis("setnx", err == nil, time.Since(lockStart))
	if err != nil {
		return nil, nil, 0, "", "", err
	}

	if !locked {
		deadline := time.Now().Add(s.cfg.CacheLockWait)
		for time.Now().Before(deadline) {
			waitGetStart := time.Now()
			cached, getErr := s.redis.Get(ctx, key).Bytes()
			s.metrics.ObserveRedis("get_wait", getErr == nil || errors.Is(getErr, redis.Nil), time.Since(waitGetStart))
			if getErr == nil {
				s.metrics.IncCacheResult("HIT_WAIT")
				return cached, defaultCachedHeader(accept), http.StatusOK, "HIT_WAIT", "", nil
			}
			if getErr != nil && !errors.Is(getErr, redis.Nil) {
				break
			}
			time.Sleep(30 * time.Millisecond)
		}
		payload, hdr, status, targetName, fwdErr := s.forward(ctx, org, forwardBody, accept, contentType)
		if fwdErr != nil {
			return nil, nil, 0, "", "", fwdErr
		}
		s.metrics.IncCacheResult("MISS_RACE")
		return payload, hdr, status, "MISS_RACE", targetName, nil
	}

	defer func() {
		_ = s.redis.Del(context.Background(), lockKey).Err()
	}()

	payload, hdr, status, targetName, err := s.forward(ctx, org, forwardBody, accept, contentType)
	if err != nil {
		return nil, nil, 0, "", "", err
	}
	if status/100 == 2 {
		setStart := time.Now()
		setErr := s.redis.Set(ctx, key, payload, s.cacheTTL()).Err()
		s.metrics.ObserveRedis("set", setErr == nil, time.Since(setStart))
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
		target, ok := s.pickTarget(tried)
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
			log.Printf("upstream_query_error target=%s org=%s attempt=%d/%d dur_ms=%d err=%q",
				target.Name, org, i+1, attempts, attemptDur.Milliseconds(), err)
			s.recordFailure(target.Name)
			continue
		}
		s.metrics.ObserveUpstream(target.Name, strconv.Itoa(status), attemptDur)
		if status >= 500 {
			lastErr = fmt.Errorf("upstream %s returned status %d", target.Name, status)
			log.Printf("upstream_query_5xx target=%s org=%s attempt=%d/%d status=%d dur_ms=%d",
				target.Name, org, i+1, attempts, status, attemptDur.Milliseconds())
			s.recordFailure(target.Name)
			if i < attempts-1 {
				continue
			}
		}

		if status < 500 {
			s.recordSuccess(target.Name)
		}
		return payload, hdr, status, target.Name, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no available targets")
	}
	log.Printf("upstream_query_exhausted org=%s attempts=%d err=%q", org, attempts, lastErr)
	return nil, nil, 0, "", lastErr
}

func (s *Server) forwardToTarget(ctx context.Context, target Target, org string, body []byte, accept, contentType string) ([]byte, http.Header, int, error) {
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
		target, ok := s.pickTarget(tried)
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
			log.Printf("upstream_read_error target=%s path=%s query=%q attempt=%d/%d dur_ms=%d err=%q",
				target.Name, in.URL.Path, in.URL.RawQuery, i+1, attempts, dur.Milliseconds(), err)
			s.recordFailure(target.Name)
			continue
		}
		s.metrics.ObserveUpstream(target.Name, strconv.Itoa(status), dur)
		if status >= 500 {
			lastErr = fmt.Errorf("upstream %s returned status %d", target.Name, status)
			log.Printf("upstream_read_5xx target=%s path=%s query=%q attempt=%d/%d status=%d dur_ms=%d",
				target.Name, in.URL.Path, in.URL.RawQuery, i+1, attempts, status, dur.Milliseconds())
			s.recordFailure(target.Name)
			if i < attempts-1 {
				continue
			}
		}
		if status < 500 {
			s.recordSuccess(target.Name)
		}
		return payload, hdr, status, target.Name, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no available targets")
	}
	log.Printf("upstream_read_exhausted path=%s query=%q attempts=%d err=%q",
		in.URL.Path, in.URL.RawQuery, attempts, lastErr)
	return nil, nil, 0, "", lastErr
}

func (s *Server) forwardReadToTarget(ctx context.Context, target Target, in *http.Request) ([]byte, http.Header, int, error) {
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

func (s *Server) healthLoop() {
	ticker := time.NewTicker(s.cfg.ProbeInterval)
	defer ticker.Stop()

	for range ticker.C {
		for _, target := range s.cfg.Targets {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			healthURL, err := url.JoinPath(target.URL, "/health")
			if err != nil {
				cancel()
				s.recordFailure(target.Name)
				continue
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
			if err != nil {
				cancel()
				s.recordFailure(target.Name)
				continue
			}
			resp, err := s.client.Do(req)
			cancel()
			if err != nil {
				s.recordFailure(target.Name)
				continue
			}
			_ = resp.Body.Close()
			if resp.StatusCode/100 == 2 {
				s.recordSuccess(target.Name)
			} else {
				s.recordFailure(target.Name)
			}
		}
	}
}

func (s *Server) pickTarget(exclude map[string]struct{}) (Target, bool) {
	now := time.Now()
	weighted := make([]Target, 0, len(s.cfg.Targets)*2)
	fallback := make([]Target, 0, len(s.cfg.Targets)*2)

	s.mu.RLock()
	for _, t := range s.cfg.Targets {
		if _, skip := exclude[t.Name]; skip {
			continue
		}
		state := s.targetStates[t.Name]
		isOpen := state != nil && now.Before(state.circuitOpenUntil)
		for i := 0; i < t.Weight; i++ {
			fallback = append(fallback, t)
			if !isOpen {
				weighted = append(weighted, t)
			}
		}
	}
	s.mu.RUnlock()

	candidates := weighted
	if len(candidates) == 0 {
		candidates = fallback
	}
	if len(candidates) == 0 {
		return Target{}, false
	}
	idx := atomic.AddUint64(&s.rr, 1)
	return candidates[idx%uint64(len(candidates))], true
}

func (s *Server) recordSuccess(targetName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.ensureStateLocked(targetName)
	state.consecutiveFailures = 0
	state.circuitOpenUntil = time.Time{}
}

func (s *Server) recordFailure(targetName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.ensureStateLocked(targetName)
	state.consecutiveFailures++
	if state.consecutiveFailures >= s.cfg.BreakerFailures {
		state.circuitOpenUntil = time.Now().Add(s.cfg.BreakerCooldown)
		state.consecutiveFailures = 0
		log.Printf("target %s circuit opened for %s", targetName, s.cfg.BreakerCooldown)
	}
}

func (s *Server) ensureStateLocked(targetName string) *targetState {
	state, ok := s.targetStates[targetName]
	if !ok {
		state = &targetState{}
		s.targetStates[targetName] = state
	}
	return state
}

func cacheKey(org, query string) string {
	sum := sha256.Sum256([]byte(org + "\n" + query))
	return fmt.Sprintf("igw:v1:%s:%s", org, hex.EncodeToString(sum[:]))
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

func splitCutoff(now time.Time, freshnessWindow time.Duration) time.Time {
	if freshnessWindow <= 0 {
		return now
	}
	// Quantize to window boundaries so repeated queries share the same cached segment key.
	return now.UTC().Truncate(freshnessWindow)
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

func (s *Server) cacheTTL() time.Duration {
	ttl := s.cfg.CacheTTL
	jitter := s.cfg.CacheTTLJitter
	if jitter <= 0 {
		return ttl
	}
	// Uniform jitter in [-jitter, +jitter] to avoid synchronized cache expirations.
	delta := time.Duration(rand.Int63n(int64(jitter)*2+1)) - jitter
	ttl += delta
	if ttl < time.Second {
		return time.Second
	}
	return ttl
}

func parseFluxJSONBody(body []byte) (string, func(string) ([]byte, error), bool) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", nil, false
	}
	rawQuery, ok := payload["query"]
	if !ok {
		return "", nil, false
	}
	var flux string
	if err := json.Unmarshal(rawQuery, &flux); err != nil || strings.TrimSpace(flux) == "" {
		return "", nil, false
	}
	builder := func(nextFlux string) ([]byte, error) {
		clone := make(map[string]json.RawMessage, len(payload))
		for k, v := range payload {
			clone[k] = v
		}
		b, err := json.Marshal(nextFlux)
		if err != nil {
			return nil, err
		}
		clone["query"] = b
		return json.Marshal(clone)
	}
	return flux, builder, true
}

func querySignature(query string) string {
	sum := sha256.Sum256([]byte(query))
	return hex.EncodeToString(sum[:8])
}

func queryPreview(query string, max int) string {
	query = strings.TrimSpace(strings.Join(strings.Fields(query), " "))
	if max <= 0 || len(query) <= max {
		return query
	}
	return query[:max] + "..."
}
