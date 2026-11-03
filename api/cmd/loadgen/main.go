// Command loadgen runs a configurable HTTP load test against the gateway API
// login endpoint and reports throughput and latency percentiles (ТЗ §18,
// T18.2). It supports concurrency ramping so the "≥500 concurrent,
// authorization ≤1s" acceptance criterion can be verified.
//
// Usage:
//
//	loadgen -url https://localhost -clients 500 -duration 20s -rate 0
//
// clamps: total requests = min(duration*fixed iteratees, ramp)
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	base := flag.String("base", "http://localhost:8080", "API base URL")
	clients := flag.Int("clients", 500, "number of concurrent worker goroutines (VU)")
	duration := flag.Duration("duration", 60*time.Second, "how long to run")
	email := flag.String("email", "load@example.com", "username for login attempts")
	password := flag.String("password", "LoadTestPass123!", "password for login attempts")
	invalid := flag.Bool("invalid", true, "use invalid password to exercise fast/full path")
	flag.Parse()

	c := &http.Client{Timeout: 30 * time.Second}

	var ok, fail atomic.Int64
	var lat []time.Duration
	var mu sync.Mutex

	wg := &sync.WaitGroup{}
	start := time.Now()
	stopCh := make(chan struct{})

	for i := 0; i < *clients; i++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			emails := []string{*email, "alice@example.com", "bob@example.com"}
			for {
				select {
				case <-stopCh:
					return
				default:
				}
				url := *base + "/login"
				body := fmt.Sprintf(`{"email":%q,"password":%q}`,
					emails[seed%len(emails)], *password)
				if *invalid {
					body = fmt.Sprintf(`{"email":%q,"password":"wrong-%d"}`,
						emails[seed%len(emails)], seed)
				}
				req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				t0 := time.Now()
				resp, err := c.Do(req)
				dur := time.Since(t0)
				if err != nil {
					fail.Add(1)
					continue
				}
				_ = resp.Body.Close()
				// A 429 means rate limiting kicked in; count as a request.
				if resp.StatusCode == http.StatusOK {
					ok.Add(1)
				} else {
					fail.Add(1)
				}
				mu.Lock()
				lat = append(lat, dur)
				mu.Unlock()
			}
		}(i)
	}

	time.Sleep(*duration)
	close(stopCh)
	wg.Wait()
	elapsed := time.Since(start)

	mu.Lock()
	all := make([]time.Duration, len(lat))
	copy(all, lat)
	mu.Unlock()
	sort.Slice(all, func(a, b int) bool { return all[a] < all[b] })

	total := ok.Load() + fail.Load()
	fmt.Printf("clients=%d duration=%s total_requests=%d ok=%d other=%d\n",
		*clients, *duration, total, ok.Load(), fail.Load())
	fmt.Printf("throughput=%.0f req/s p50=%s p90=%s p95=%s p99=%s max=%s\n",
		float64(total)/elapsed.Seconds(), pct(all, 0.50), pct(all, 0.90), pct(all, 0.95), pct(all, 0.99), pct(all, 1.00))
	if total == 0 {
		fmt.Fprintln(os.Stderr, "no requests completed — check target is up")
		os.Exit(1)
	}
}

func pct(all []time.Duration, q float64) time.Duration {
	if len(all) == 0 {
		return 0
	}
	i := int(q * float64(len(all)))
	if i >= len(all) {
		i = len(all) - 1
	}
	return all[i]
}
