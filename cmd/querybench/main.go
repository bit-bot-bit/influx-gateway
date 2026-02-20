package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type result struct {
	latencyMillis float64
	status        int
	cache         string
	target        string
	err           error
}

func main() {
	var (
		url          = flag.String("url", "http://localhost:8090/api/v2/query?org=acme", "gateway query URL")
		token        = flag.String("token", "gateway-reader", "gateway bearer token")
		query        = flag.String("query", `from(bucket: "metrics") |> range(start: -1h, stop: now()) |> filter(fn: (r) => r._measurement == "http_requests" and r._field == "latency_ms") |> aggregateWindow(every: 1m, fn: mean, createEmpty: false)`, "flux query")
		concurrency  = flag.Int("concurrency", 20, "parallel workers")
		duration     = flag.Duration("duration", 60*time.Second, "benchmark duration")
		warmup       = flag.Int("warmup", 5, "warmup requests before timed run")
		requestBodyB = flag.Int("max-read-body-bytes", 4096, "max bytes to read on non-2xx responses")
		progress     = flag.Duration("progress-interval", 5*time.Second, "progress log interval during run")
	)
	flag.Parse()

	if *concurrency < 1 {
		*concurrency = 1
	}
	if *duration <= 0 {
		log.Fatal("duration must be > 0")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()
	startRun := time.Now()

	for i := 0; i < *warmup; i++ {
		res := doReq(client, *url, *token, *query, *requestBodyB)
		if res.err != nil {
			log.Printf("warmup request failed: %v", res.err)
		}
	}

	results := make(chan result, 4096)
	var sent atomic.Int64
	var errCount atomic.Int64
	var completed atomic.Int64
	var wg sync.WaitGroup

	if *progress > 0 {
		go func() {
			ticker := time.NewTicker(*progress)
			defer ticker.Stop()
			lastCompleted := int64(0)
			lastTick := startRun
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					c := completed.Load()
					e := errCount.Load()
					now := time.Now()
					interval := now.Sub(lastTick).Seconds()
					if interval <= 0 {
						interval = 1
					}
					delta := c - lastCompleted
					log.Printf("progress completed=%d errors=%d interval_rps=%.2f", c, e, float64(delta)/interval)
					lastCompleted = c
					lastTick = now
				}
			}
		}()
	}

	type aggregate struct {
		latencies    []float64
		statusCounts map[int]int
		cacheCounts  map[string]int
		targetCounts map[string]int
		errors       int
	}
	agg := aggregate{
		latencies:    make([]float64, 0, 8192),
		statusCounts: map[int]int{},
		cacheCounts:  map[string]int{},
		targetCounts: map[string]int{},
	}
	doneAgg := make(chan struct{})
	go func() {
		defer close(doneAgg)
		for r := range results {
			completed.Add(1)
			if r.err != nil {
				errCount.Add(1)
				agg.errors++
				continue
			}
			agg.latencies = append(agg.latencies, r.latencyMillis)
			agg.statusCounts[r.status]++
			if r.cache == "" {
				r.cache = "UNKNOWN"
			}
			agg.cacheCounts[r.cache]++
			if r.target == "" {
				r.target = "UNKNOWN"
			}
			agg.targetCounts[r.target]++
		}
	}()

	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					res := doReq(client, *url, *token, *query, *requestBodyB)
					sent.Add(1)
					results <- res
				}
			}
		}()
	}

	wg.Wait()
	close(results)
	<-doneAgg

	total := len(agg.latencies) + agg.errors
	if total == 0 {
		log.Fatal("no requests completed")
	}

	sort.Float64s(agg.latencies)
	sum := 0.0
	for _, v := range agg.latencies {
		sum += v
	}
	avg := 0.0
	if len(agg.latencies) > 0 {
		avg = sum / float64(len(agg.latencies))
	}

	rps := float64(total) / duration.Seconds()

	fmt.Println("=== Query Benchmark ===")
	fmt.Printf("url=%s\n", *url)
	fmt.Printf("concurrency=%d duration=%s warmup=%d\n", *concurrency, duration.String(), *warmup)
	fmt.Printf("total_requests=%d successful=%d errors=%d error_rate=%.2f%%\n", total, len(agg.latencies), agg.errors, pct(agg.errors, total))
	fmt.Printf("throughput_rps=%.2f\n", rps)
	if len(agg.latencies) > 0 {
		fmt.Printf("latency_ms min=%.2f avg=%.2f p50=%.2f p90=%.2f p95=%.2f p99=%.2f max=%.2f\n",
			agg.latencies[0], avg,
			percentile(agg.latencies, 50), percentile(agg.latencies, 90), percentile(agg.latencies, 95), percentile(agg.latencies, 99),
			agg.latencies[len(agg.latencies)-1],
		)
	}

	fmt.Println("status_counts:")
	printIntMap(agg.statusCounts)
	fmt.Println("cache_header_counts:")
	printStringMap(agg.cacheCounts)
	fmt.Println("target_header_counts:")
	printStringMap(agg.targetCounts)

	if len(agg.latencies) > 0 && (percentile(agg.latencies, 99) > 5000 || pct(agg.errors, total) > 1.0) {
		fmt.Fprintln(os.Stderr, "benchmark thresholds exceeded: p99>5000ms or error_rate>1%")
		os.Exit(2)
	}
}

func doReq(client *http.Client, url, token, query string, maxRead int) result {
	start := time.Now()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(query))
	if err != nil {
		return result{err: err}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/vnd.flux")
	req.Header.Set("Accept", "application/csv")

	resp, err := client.Do(req)
	if err != nil {
		return result{err: err}
	}
	defer resp.Body.Close()

	cache := strings.TrimSpace(resp.Header.Get("X-Cache"))
	target := strings.TrimSpace(resp.Header.Get("X-Target-Live"))
	if target == "" {
		target = strings.TrimSpace(resp.Header.Get("X-Target"))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, int64(maxRead)))
		return result{status: resp.StatusCode, cache: cache, target: target, err: fmt.Errorf("status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(b)))}
	}
	_, _ = io.Copy(io.Discard, resp.Body)

	return result{
		latencyMillis: float64(time.Since(start).Microseconds()) / 1000.0,
		status:        resp.StatusCode,
		cache:         cache,
		target:        target,
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	idx := int(math.Ceil((p/100.0)*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return (float64(n) / float64(total)) * 100.0
}

func printIntMap(m map[int]int) {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		fmt.Printf("  %d: %d\n", k, m[k])
	}
}

func printStringMap(m map[string]int) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("  %s: %d\n", k, m[k])
	}
}
