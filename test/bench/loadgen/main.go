// Command loadgen sends HTTP requests from a number of keep-alive clients for
// a while and reports throughput and latency: the load generator of kest's
// benchmarks (docs/benchmarks.md). Unlike ab it uses every core.
//
//	loadgen -url http://127.0.0.1:8080/ -c 16 -d 15s
//	loadgen -url http://127.0.0.1:8080/info -body 'a=1&b[]=2' -c 64
//	loadgen -url http://127.0.0.1:8080/info -body-bytes 1048576
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

type result struct {
	Requests  int     `json:"requests"`
	Errors    int     `json:"errors"`
	RPS       float64 `json:"rps"`
	P50Micros float64 `json:"p50_us"`
	P90Micros float64 `json:"p90_us"`
	P99Micros float64 `json:"p99_us"`
	MaxMicros float64 `json:"max_us"`
}

func main() {
	url := flag.String("url", "", "the URL to request")
	body := flag.String("body", "", "send this body with POST, as a form")
	bodyBytes := flag.Int("body-bytes", 0, "send a raw body of this many bytes with POST")
	clients := flag.Int("c", 16, "concurrent clients, each with its own keep-alive connection")
	duration := flag.Duration("d", 15*time.Second, "how long to measure")
	warmup := flag.Duration("warmup", 3*time.Second, "how long to send requests before measuring")
	flag.Parse()
	if *url == "" || *clients < 1 {
		flag.Usage()
		os.Exit(2)
	}

	transport := &http.Transport{MaxIdleConnsPerHost: *clients, DisableCompression: true}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	raw := strings.Repeat("x", *bodyBytes)
	send := func(ctx context.Context) error {
		method, reader, ctype := http.MethodGet, io.Reader(http.NoBody), ""
		switch {
		case *body != "":
			method, reader, ctype = http.MethodPost, strings.NewReader(*body), "application/x-www-form-urlencoded"
		case raw != "":
			method, reader, ctype = http.MethodPost, strings.NewReader(raw), "application/octet-stream"
		}
		req, err := http.NewRequestWithContext(ctx, method, *url, reader)
		if err != nil {
			return err
		}
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	}

	run(*clients, *warmup, send) // warm up workers and connections
	latencies, errs, elapsed := run(*clients, *duration, send)
	transport.CloseIdleConnections()

	slices.Sort(latencies)
	pct := func(p float64) float64 {
		if len(latencies) == 0 {
			return 0
		}
		return float64(latencies[int(p*float64(len(latencies)-1))].Microseconds())
	}
	r := result{
		Requests: len(latencies) + errs, Errors: errs, RPS: float64(len(latencies)) / elapsed.Seconds(),
		P50Micros: pct(0.50), P90Micros: pct(0.90), P99Micros: pct(0.99), MaxMicros: pct(1),
	}
	if err := json.NewEncoder(os.Stdout).Encode(r); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if errs > 0 {
		os.Exit(1)
	}
}

// run sends requests from n clients for d and returns the latencies of the
// successful ones, the number of failed ones and how long it ran.
func run(n int, d time.Duration, send func(context.Context) error) ([]time.Duration, int, time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	var (
		mu        sync.Mutex
		latencies []time.Duration
		errs      int
		wg        sync.WaitGroup
	)
	start := time.Now()
	for range n {
		wg.Go(func() {
			var mine []time.Duration
			failed := 0
			for ctx.Err() == nil {
				t := time.Now()
				if err := send(ctx); err != nil {
					if ctx.Err() == nil || !errors.Is(err, context.DeadlineExceeded) {
						failed++
					}
					continue
				}
				mine = append(mine, time.Since(t))
			}
			mu.Lock()
			latencies = append(latencies, mine...)
			errs += failed
			mu.Unlock()
		})
	}
	wg.Wait()
	return latencies, errs, time.Since(start)
}
