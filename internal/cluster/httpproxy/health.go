package httpproxy

import (
	"context"
	"io"
	"net/http"
	"time"
)

// watch checks e every interval until ctx ends, and changes its health after
// healthy_threshold successes or unhealthy_threshold failures in a row.
func (c *Cluster) watch(ctx context.Context, e *endpoint) {
	hc := c.cfg.HealthCheck
	ticker := time.NewTicker(hc.Interval.Std())
	defer ticker.Stop()
	successes, failures := 0, 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if c.check(ctx, e) {
			successes, failures = successes+1, 0
		} else {
			successes, failures = 0, failures+1
		}
		switch {
		case !e.healthy.Load() && successes >= hc.HealthyThreshold:
			e.healthy.Store(true)
			c.logger.Info("endpoint healthy again", "endpoint", e.name)
		case e.healthy.Load() && failures >= hc.UnhealthyThreshold:
			e.healthy.Store(false)
			c.logger.Warn("endpoint unhealthy", "endpoint", e.name, "failures", failures)
		}
	}
}

// check sends one health check: a GET of the path that answers 2xx or 3xx
// within the timeout passes.
func (c *Cluster) check(ctx context.Context, e *endpoint) bool {
	hc := c.cfg.HealthCheck
	ctx, cancel := context.WithTimeout(ctx, hc.Timeout.Std())
	defer cancel()
	u := *e.url
	u.Path = hc.Path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return false
	}
	if c.cfg.Host != "" {
		req.Host = c.cfg.Host
	}
	req.Header.Set("User-Agent", "kest-health-check")
	resp, err := c.transport.RoundTrip(req)
	if err != nil {
		c.logger.Debug("health check failed", "endpoint", e.name, "err", err)
		return false
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	ok := resp.StatusCode >= 200 && resp.StatusCode < 400
	if !ok {
		c.logger.Debug("health check failed", "endpoint", e.name, "status", resp.StatusCode)
	}
	return ok
}
