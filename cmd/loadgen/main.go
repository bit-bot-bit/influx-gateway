package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type targetCfg struct {
	Name string
	URL  string
}

type batchJob struct {
	id   int64
	size int64
}

type counters struct {
	bytesSent atomic.Int64
	jobsDone  atomic.Int64
}

type writeError struct {
	status int
	body   string
	err    error
}

func (e *writeError) Error() string {
	if e == nil {
		return ""
	}
	if e.err != nil {
		return e.err.Error()
	}
	return fmt.Sprintf("status=%d body=%s", e.status, e.body)
}

func main() {
	var (
		targetsRaw   = flag.String("targets", "influx-a=http://localhost:8086,influx-b=http://localhost:8087", "comma-separated target defs: name=url or url")
		org          = flag.String("org", "acme", "influx org")
		bucket       = flag.String("bucket", "metrics", "influx bucket")
		token        = flag.String("token", "admin-token", "influx write token")
		targetBytes  = flag.Int64("target-bytes", 4*1024*1024*1024, "approx bytes to send per target")
		workers      = flag.Int("workers", 4, "parallel batch workers")
		maxBatchB    = flag.Int64("max-batch-bytes", 512*1024, "batch payload upper bound")
		reportPeriod = flag.Duration("report-period", 5*time.Second, "progress log interval")
		seed         = flag.Int64("seed", 20260220, "deterministic generation seed")
		maxRetries   = flag.Int("max-retries", 15, "max retries per batch on retryable write errors")
		retryBackoff = flag.Duration("retry-backoff", 2*time.Second, "base retry backoff for retryable errors")
		batchSleep   = flag.Duration("inter-batch-sleep", 0, "sleep between successful batches to reduce write pressure")
		vizRatio     = flag.Float64("viz-ratio", 0.20, "fraction [0..1] of generated points written as low-cardinality dashboard-friendly series")
	)
	flag.Parse()

	if *token == "" {
		log.Fatal("token is required")
	}
	if *workers < 1 {
		*workers = 1
	}
	if *maxBatchB < 64*1024 {
		*maxBatchB = 64 * 1024
	}
	if *targetBytes <= 0 {
		log.Fatal("target-bytes must be > 0")
	}
	if *vizRatio < 0 {
		*vizRatio = 0
	}
	if *vizRatio > 1 {
		*vizRatio = 1
	}

	targets, err := parseTargets(*targetsRaw)
	if err != nil {
		log.Fatalf("invalid targets: %v", err)
	}
	if len(targets) == 0 {
		log.Fatal("at least one target is required")
	}

	writeURLs := make(map[string]string, len(targets))
	for _, t := range targets {
		u, err := url.JoinPath(t.URL, "/api/v2/write")
		if err != nil {
			log.Fatalf("target %s bad url: %v", t.Name, err)
		}
		writeURLs[t.Name] = u + "?org=" + url.QueryEscape(*org) + "&bucket=" + url.QueryEscape(*bucket) + "&precision=ns"
	}

	client := &http.Client{Timeout: 30 * time.Second}
	progress := &counters{}

	jobs := make(chan batchJob, *workers*2)
	start := time.Now()

	go planJobs(jobs, *targetBytes, *maxBatchB)

	stopReport := make(chan struct{})
	go reportLoop(progress, *targetBytes, *reportPeriod, stopReport)

	var wg sync.WaitGroup
	var firstErr atomic.Value
	for i := 0; i < *workers; i++ {
		workerID := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				if firstErr.Load() != nil {
					return
				}

					batch := generateBatch(*seed+job.id, job.size, *vizRatio)
					for attempt := 0; ; attempt++ {
						err := writeBatchToAll(client, targets, writeURLs, *token, batch)
						if err == nil {
							progress.bytesSent.Add(int64(len(batch)))
							progress.jobsDone.Add(1)
							if *batchSleep > 0 {
								time.Sleep(*batchSleep)
							}
							break
						}
						if isRetryable(err) && attempt < *maxRetries {
							sleep := *retryBackoff + time.Duration(attempt*150)*time.Millisecond
							log.Printf("worker=%d batch=%d retryable write error (attempt %d/%d): %v (sleep=%s)",
								workerID, job.id, attempt+1, *maxRetries, err, sleep)
							time.Sleep(sleep)
							continue
						}
						if firstErr.Load() == nil {
							firstErr.Store(err)
							log.Printf("worker=%d batch=%d write failed: %v", workerID, job.id, err)
						}
						return
				}
			}
		}()
	}

	wg.Wait()
	close(stopReport)

	if v := firstErr.Load(); v != nil {
		log.Fatalf("load generation failed: %v", v)
	}

	total := progress.bytesSent.Load()
	log.Printf("complete: bytes_per_target=%d jobs=%d elapsed=%s", total, progress.jobsDone.Load(), time.Since(start).Round(time.Second))
}

func planJobs(out chan<- batchJob, totalBytes, maxBatch int64) {
	defer close(out)
	var sent int64
	var id int64
	for sent < totalBytes {
		size := maxBatch
		rem := totalBytes - sent
		if rem < size {
			size = rem
		}
		out <- batchJob{id: id, size: size}
		sent += size
		id++
	}
}

func reportLoop(c *counters, targetBytes int64, period time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			sent := c.bytesSent.Load()
			jobs := c.jobsDone.Load()
			pct := (float64(sent) / float64(targetBytes)) * 100.0
			log.Printf("progress: bytes_per_target=%d jobs=%d %.1f%%", sent, jobs, pct)
		}
	}
}

func writeBatchToAll(client *http.Client, targets []targetCfg, writeURLs map[string]string, token string, batch []byte) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(targets))

	for _, t := range targets {
		t := t
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := writeBatch(client, writeURLs[t.Name], token, batch); err != nil {
				errCh <- fmt.Errorf("target=%s: %w", t.Name, err)
			}
		}()
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		return err
	}
	return nil
}

func writeBatch(client *http.Client, writeURL, token string, body []byte) error {
	req, err := http.NewRequest(http.MethodPost, writeURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Token "+token)
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &writeError{
			status: resp.StatusCode,
			body:   strings.TrimSpace(string(b)),
		}
	}
	return nil
}

func generateBatch(seed, targetBytes int64, vizRatio float64) []byte {
	rng := rand.New(rand.NewSource(seed))
	var b strings.Builder
	b.Grow(int(targetBytes) + 1024)
	for b.Len() < int(targetBytes) {
		b.WriteString(generatePoint(rng, vizRatio))
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func generatePoint(rng *rand.Rand, vizRatio float64) string {
	if rng.Float64() < vizRatio {
		return generateVizPoint(rng)
	}
	return generateHighCardPoint(rng)
}

func generateHighCardPoint(rng *rand.Rand) string {
	now := time.Now().UnixNano() - rng.Int63n(int64(24*time.Hour))

	tenant := "tenant-" + strconv.Itoa(rng.Intn(1000))
	host := "host-" + strconv.Itoa(rng.Intn(20000))
	service := "svc-" + strconv.Itoa(rng.Intn(1200))
	region := []string{"us-east", "us-west", "eu-west", "ap-south"}[rng.Intn(4)]
	cluster := "cluster-" + strconv.Itoa(rng.Intn(80))
	pod := "pod-" + strconv.Itoa(rng.Intn(250000))
	session := "sess-" + strconv.Itoa(rng.Intn(2000000))
	endpoint := []string{"/api/search", "/api/orders", "/api/auth", "/api/cart", "/api/report"}[rng.Intn(5)]
	method := []string{"GET", "POST", "PUT", "DELETE"}[rng.Intn(4)]
	status := []int{200, 201, 204, 400, 401, 404, 429, 500, 503}[rng.Intn(9)]

	switch rng.Intn(4) {
	case 0:
		return fmt.Sprintf("cpu,profile=stress,tenant=%s,host=%s,region=%s,cluster=%s,pod=%s,service=%s usage_user=%.4f,usage_sys=%.4f,cores=%di,online=%t,temp_c=%.2f,node_version=\"%s\" %d",
			tenant, host, region, cluster, pod, service,
			rng.Float64()*100.0, rng.Float64()*100.0, rng.Intn(64)+1, rng.Intn(100) > 1,
			rng.Float64()*50.0+20.0, []string{"v1.2.0", "v1.2.1", "v1.2.2", "v1.3.0"}[rng.Intn(4)], now)
	case 1:
		return fmt.Sprintf("http_requests,profile=stress,tenant=%s,host=%s,region=%s,service=%s,endpoint=%s,method=%s,status=%d,session=%s latency_ms=%.3f,bytes_out=%di,retry=%di,cache_hit=%t,error=\"%s\" %d",
			tenant, host, region, service, escapeTag(endpoint), method, status, session,
			rng.Float64()*900.0+10.0, rng.Intn(4*1024*1024), rng.Intn(5), rng.Intn(100) < 40,
			[]string{"none", "timeout", "upstream_5xx", "quota"}[rng.Intn(4)], now)
	case 2:
		return fmt.Sprintf("orders,profile=stress,tenant=%s,region=%s,service=%s,customer_id=%s,order_id=%s total=%.2f,item_count=%di,discount=%.2f,priority=%t,currency=\"%s\",state=\"%s\" %d",
			tenant, region, service,
			"cust-"+strconv.Itoa(rng.Intn(2500000)), "ord-"+strconv.Itoa(rng.Intn(5000000)),
			rng.Float64()*2500.0, rng.Intn(30)+1, rng.Float64()*80.0, rng.Intn(100) < 20,
			[]string{"USD", "EUR", "JPY"}[rng.Intn(3)], []string{"new", "paid", "shipped", "cancelled"}[rng.Intn(4)], now)
	default:
		return fmt.Sprintf("security_events,profile=stress,tenant=%s,region=%s,host=%s,service=%s,actor=%s,severity=%s attempts=%di,blocked=%t,score=%.2f,rule=\"%s\",message=\"%s\" %d",
			tenant, region, host, service,
			"user-"+strconv.Itoa(rng.Intn(7000000)), []string{"low", "medium", "high", "critical"}[rng.Intn(4)],
			rng.Intn(20)+1, rng.Intn(100) < 70, rng.Float64()*100.0,
			[]string{"geo-velocity", "token-reuse", "burst-login", "impossible-travel"}[rng.Intn(4)],
			[]string{"ok", "flagged", "blocked", "review"}[rng.Intn(4)], now)
	}
}

func generateVizPoint(rng *rand.Rand) string {
	now := time.Now().UnixNano() - rng.Int63n(int64(45*time.Minute))
	region := []string{"us-east", "us-west", "eu-west"}[rng.Intn(3)]
	service := []string{"api", "billing", "auth", "search"}[rng.Intn(4)]
	host := []string{"edge-a", "edge-b", "edge-c", "edge-d"}[rng.Intn(4)]
	tenant := []string{"tenant-a", "tenant-b", "tenant-c"}[rng.Intn(3)]
	endpoint := []string{"/api/search", "/api/orders", "/api/auth", "/api/cart", "/api/report"}[rng.Intn(5)]
	method := []string{"GET", "POST", "PUT"}[rng.Intn(3)]
	status := []int{200, 201, 204, 400, 401, 404, 429, 500}[rng.Intn(8)]

	phase := float64(now%int64(15*time.Minute)) / float64((15 * time.Minute).Nanoseconds()) * 2 * math.Pi
	cpuBase := 45.0 + 25.0*math.Sin(phase)
	latBase := 140.0 + 60.0*math.Sin(phase+0.6)
	scoreBase := 50.0 + 20.0*math.Sin(phase+1.2)

	switch rng.Intn(4) {
	case 0:
		return fmt.Sprintf("cpu,profile=viz,tenant=%s,host=%s,region=%s,service=%s usage_user=%.3f,usage_sys=%.3f %d",
			tenant, host, region, service,
			clamp(cpuBase+rng.Float64()*8.0, 2, 98), clamp(cpuBase*0.62+rng.Float64()*6.0, 1, 92), now)
	case 1:
		return fmt.Sprintf("http_requests,profile=viz,tenant=%s,host=%s,region=%s,service=%s,endpoint=%s,method=%s,status=%d latency_ms=%.3f,bytes_out=%di,retry=%di %d",
			tenant, host, region, service, escapeTag(endpoint), method, status,
			clamp(latBase+rng.Float64()*55.0, 8, 2000), rng.Intn(512*1024), rng.Intn(3), now)
	case 2:
		return fmt.Sprintf("orders,profile=viz,tenant=%s,region=%s,service=%s total=%.2f,item_count=%di,discount=%.2f %d",
			tenant, region, service,
			clamp(120+rng.Float64()*480, 20, 3000), rng.Intn(12)+1, clamp(rng.Float64()*25, 0, 80), now)
	default:
		return fmt.Sprintf("security_events,profile=viz,tenant=%s,region=%s,host=%s,service=%s,severity=%s attempts=%di,blocked=%t,score=%.2f %d",
			tenant, region, host, service,
			[]string{"low", "medium", "high", "critical"}[rng.Intn(4)],
			rng.Intn(8)+1, rng.Intn(100) < 55, clamp(scoreBase+rng.Float64()*18.0, 1, 100), now)
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func parseTargets(raw string) ([]targetCfg, error) {
	parts := strings.Split(strings.TrimSpace(raw), ",")
	out := make([]targetCfg, 0, len(parts))
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		name := fmt.Sprintf("target-%d", i+1)
		addr := p
		if strings.Contains(p, "=") {
			toks := strings.SplitN(p, "=", 2)
			name = strings.TrimSpace(toks[0])
			addr = strings.TrimSpace(toks[1])
		}
		if name == "" || addr == "" {
			return nil, fmt.Errorf("invalid target entry: %q", p)
		}
		if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
			return nil, fmt.Errorf("target %s url must start with http:// or https://", name)
		}
		out = append(out, targetCfg{Name: name, URL: strings.TrimRight(addr, "/")})
	}
	return out, nil
}

func escapeTag(s string) string {
	s = strings.ReplaceAll(s, ",", "\\,")
	s = strings.ReplaceAll(s, " ", "\\ ")
	s = strings.ReplaceAll(s, "=", "\\=")
	return s
}

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	var werr *writeError
	if errors.As(err, &werr) {
		if werr.status >= 500 {
			return true
		}
		msg := strings.ToLower(werr.body)
		if strings.Contains(msg, "cache-max-memory-size exceeded") ||
			strings.Contains(msg, "timeout") ||
			strings.Contains(msg, "temporar") {
			return true
		}
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") || strings.Contains(msg, "temporar")
}
