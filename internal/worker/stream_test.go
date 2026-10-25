package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/berrymore/kest/internal/worker/workertest"
)

func execStream(t *testing.T, p *Pool, body string) (Payload, *Stream) {
	t.Helper()
	first, stream, err := p.ExecStream(t.Context(), Payload{Context: []byte("ctx"), Body: []byte(body)})
	if err != nil {
		t.Fatalf("ExecStream %q: %v", body, err)
	}
	if stream == nil {
		t.Fatalf("ExecStream %q: no stream", body)
	}
	return first, stream
}

// rest reads the stream to its end and returns the bodies.
func rest(t *testing.T, s *Stream) (string, error) {
	t.Helper()
	var b strings.Builder
	for {
		f, ok, err := s.Next(t.Context())
		if !ok {
			return b.String(), err
		}
		b.Write(f.Body)
	}
}

func TestStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		p := startTestPool(t, testConfig(1), &fake)

		// Seven frames: the sixth pings, and the worker carries on after the pong.
		first, stream := execStream(t, p, "stream 7 1s")
		if string(first.Context) != "ctx" || string(first.Body) != "chunk 0\n" {
			t.Errorf("first frame %q %q", first.Context, first.Body)
		}
		got, err := rest(t, stream)
		stream.Close()
		if want := "chunk 1\nchunk 2\nchunk 3\nchunk 4\nchunk 5\nchunk 6\nend"; err != nil || got != want {
			t.Errorf("stream = %q, %v; want %q", got, err, want)
		}
		if w := p.Workers()[0]; w.Jobs != 1 {
			t.Errorf("jobs = %d, want 1", w.Jobs)
		}
		mustExec(t, p, "after the stream")

		// Exec joins the frames.
		resp, err := execBody(t, p, "stream 2")
		if err != nil || string(resp.Body) != "chunk 0\nchunk 1\nend" {
			t.Errorf("Exec of a stream = %q, %v", resp.Body, err)
		}
		if fake.Started() != 1 {
			t.Errorf("%d workers started, want 1", fake.Started())
		}
	})
}

// A stream closed early stops the worker's stream: it notices the STOP after
// its next ping and serves the next job.
func TestStreamClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		p := startTestPool(t, testConfig(1), &fake)

		start := time.Now()
		_, stream := execStream(t, p, "stream 1000 1s")
		if f, ok, err := stream.Next(t.Context()); !ok || string(f.Body) != "chunk 1\n" {
			t.Fatalf("Next = %q, %v", f.Body, err)
		}
		stream.Close()
		stream.Close() // twice is fine
		mustExec(t, p, "next")
		// Frame 5 pings; before frame 6 the worker reads the STOP.
		if elapsed := time.Since(start); elapsed != 6*time.Second {
			t.Errorf("the next job ran after %s, want 6s", elapsed)
		}
		if _, ok, err := stream.Next(t.Context()); ok || err != nil {
			t.Errorf("Next after Close = %v, %v; want the end", ok, err)
		}
		if fake.Started() != 1 {
			t.Errorf("%d workers started, want 1", fake.Started())
		}
	})
}

// A caller that gives up before the first frame stops the stream too.
func TestStreamCallerGone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		p := startTestPool(t, testConfig(1), &fake)

		ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
		defer cancel()
		_, stream, err := p.ExecStream(ctx, Payload{Body: []byte("stream 1000 1s")})
		if !errors.Is(err, context.DeadlineExceeded) || stream != nil {
			t.Fatalf("ExecStream = %v, %v; want the deadline", stream, err)
		}
		mustExec(t, p, "next")
		if fake.Started() != 1 {
			t.Errorf("%d workers started, want 1", fake.Started())
		}

		// Exec reading a stream until its deadline closes it as well.
		ctx, cancel = context.WithTimeout(t.Context(), 3500*time.Millisecond)
		defer cancel()
		if _, err := p.Exec(ctx, Payload{Body: []byte("stream 1000 1s")}); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Exec = %v, want the deadline", err)
		}
		mustExec(t, p, "next again")
	})
}

// A worker that keeps streaming after the STOP is killed after the longer of
// ExecTTL and DestroyTimeout, and replaced.
func TestStreamIgnoresStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		cfg := testConfig(1)
		cfg.ExecTTL = 20 * time.Second
		cfg.DestroyTimeout = 30 * time.Second
		p := startTestPool(t, cfg, &fake)

		_, stream := execStream(t, p, "stream 1000 1s nostop")
		closed := time.Now()
		stream.Close()
		mustExec(t, p, "on the replacement")
		if elapsed := time.Since(closed); elapsed < 30*time.Second || elapsed > 32*time.Second {
			t.Errorf("replaced after %s, want about 30s", elapsed)
		}
		if fake.Started() != 2 || !errors.Is(fake.Process(10000).Err(), workertest.ErrKilled) {
			t.Errorf("the worker should be killed and replaced: %d started", fake.Started())
		}
	})
}

// ExecTTL bounds each frame of a stream, not the whole stream.
func TestStreamExecTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		cfg := testConfig(1)
		cfg.ExecTTL = time.Minute
		p := startTestPool(t, cfg, &fake)

		start := time.Now()
		_, stream := execStream(t, p, "stream 6 30s")
		got, err := rest(t, stream)
		stream.Close()
		if err != nil || !strings.HasSuffix(got, "end") || time.Since(start) != 3*time.Minute {
			t.Errorf("stream = %q, %v after %s; want the whole stream after 3m", got, err, time.Since(start))
		}

		_, stream = execStream(t, p, "stream 3 30s")
		defer stream.Close()
		fake.Process(10000).Kill()
		if got, err := rest(t, stream); !errors.As(err, new(*ProcessError)) {
			t.Errorf("stream of a killed worker: %q %v, want a *ProcessError", got, err)
		}

		_, _, err = p.ExecStream(t.Context(), Payload{Body: []byte("stream 3 2m")})
		if !errors.Is(err, ErrExecTimeout) {
			t.Errorf("a first frame after 2m: %v, want ErrExecTimeout", err)
		}
	})
}

func TestExecStreamTooLarge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		cfg := testConfig(1)
		cfg.MaxFrameSize = 30
		p := startTestPool(t, cfg, &fake)
		if _, err := execBody(t, p, "stream 10"); !errors.Is(err, ErrStreamTooLarge) {
			t.Errorf("Exec = %v, want ErrStreamTooLarge", err)
		}
		mustExec(t, p, "next")
	})
}
