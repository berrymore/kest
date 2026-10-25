// Command loadgen sends HTTP requests from a number of keep-alive clients for
// a while and reports throughput and latency: the load generator of kest's
// benchmarks (docs/benchmarks.md). Unlike ab it uses every core.
//
//	loadgen -url http://127.0.0.1:8080/ -c 16 -d 15s
//	loadgen -url http://127.0.0.1:8080/info -body 'a=1&b[]=2' -c 64
//	loadgen -url http://127.0.0.1:8080/info -body-bytes 1048576
//
// With -rate, requests come at that rate (Poisson arrivals) whatever the
// answers take, as users do, each one giving up after -timeout; -c then bounds
// the requests in flight. That overloads a server, which a fixed number of
// clients can't.
//
//	loadgen -url http://127.0.0.1:8080/sleep/20 -rate 400 -timeout 5s -c 4000
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type result struct {
	Requests  int            `json:"requests"`
	Errors    int            `json:"errors"`
	Statuses  map[string]int `json:"statuses,omitempty"` // with -rate: failures by status, "timeout" for the given up
	RPS       float64        `json:"rps"`
	P50Micros float64        `json:"p50_us"`
	P90Micros float64        `json:"p90_us"`
	P99Micros float64        `json:"p99_us"`
	MaxMicros float64        `json:"max_us"`
}

func main() {
	url := flag.String("url", "", "the URL to request")
	body := flag.String("body", "", "send this body with POST, as a form")
	bodyBytes := flag.Int("body-bytes", 0, "send a raw body of this many bytes with POST")
	clients := flag.Int("c", 16, "concurrent clients, each with its own keep-alive connection")
	duration := flag.Duration("d", 15*time.Second, "how long to measure")
	warmup := flag.Duration("warmup", 3*time.Second, "how long to send requests before measuring")
	rate := flag.Float64("rate", 0, "requests per second, whatever the answers take (open loop); 0: -c clients in a loop")
	timeout := flag.Duration("timeout", 30*time.Second, "how long a request may take before its client gives up")
	flag.Parse()
	if *url == "" || *clients < 1 {
		flag.Usage()
		os.Exit(2)
	}

	transport := &http.Transport{MaxIdleConnsPerHost: *clients, DisableCompression: true}
	client := &http.Client{Transport: transport, Timeout: *timeout}
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
			return statusError(resp.StatusCode)
		}
		return nil
	}

	var (
		latencies []time.Duration
		errs      int
		statuses  map[string]int
		elapsed   time.Duration
	)
	if *rate > 0 {
		openLoop(*rate, *clients, *warmup, send)
		latencies, statuses, elapsed = openLoop(*rate, *clients, *duration, send)
		for _, n := range statuses {
			errs += n
		}
	} else {
		run(*clients, *warmup, send) // warm up workers and connections
		latencies, errs, elapsed = run(*clients, *duration, send)
	}
	transport.CloseIdleConnections()

	slices.Sort(latencies)
	pct := func(p float64) float64 {
		if len(latencies) == 0 {
			return 0
		}
		return float64(latencies[int(p*float64(len(latencies)-1))].Microseconds())
	}
	r := result{
		Requests: len(latencies) + errs, Errors: errs, Statuses: statuses, RPS: float64(len(latencies)) / elapsed.Seconds(),
		P50Micros: pct(0.50), P90Micros: pct(0.90), P99Micros: pct(0.99), MaxMicros: pct(1),
	}
	if err := json.NewEncoder(os.Stdout).Encode(r); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if errs > 0 && *rate == 0 {
		os.Exit(1)
	}
}

// statusError is a response other than 200.
type statusError int

func (e statusError) Error() string { return fmt.Sprintf("status %d", int(e)) }

// openLoop sends requests at rate per second for d, with exponential gaps,
// each in its own goroutine, at most limit at once (more are counted as
// "limit"). It returns the latencies of the successful ones, the failed ones
// by status ("timeout" when the client gave up, "error" otherwise), and how
// long it ran.
func openLoop(rate float64, limit int, d time.Duration, send func(context.Context) error) ([]time.Duration,
	map[string]int, time.Duration) {
	var (
		mu        sync.Mutex
		latencies []time.Duration
		statuses  = map[string]int{}
		wg        sync.WaitGroup
		inFlight  = make(chan struct{}, limit)
	)
	record := func(key string, lat time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		if key == "" {
			latencies = append(latencies, lat)
		} else {
			statuses[key]++
		}
	}
	start := time.Now()
	next := start
	for time.Since(start) < d {
		next = next.Add(time.Duration(rand.ExpFloat64() / rate * float64(time.Second))) //nolint:gosec // load, not security
		time.Sleep(time.Until(next))
		select {
		case inFlight <- struct{}{}:
		default:
			record("limit", 0)
			continue
		}
		wg.Go(func() {
			defer func() { <-inFlight }()
			t := time.Now()
			err := send(context.Background())
			var se statusError
			switch {
			case err == nil:
				record("", time.Since(t))
			case errors.As(err, &se):
				record(strconv.Itoa(int(se)), 0)
			case errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "Client.Timeout"):
				record("timeout", 0)
			default:
				record("error", 0)
			}
		})
	}
	wg.Wait()
	return latencies, statuses, d // the answers to the last requests come later, but within the rate
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
