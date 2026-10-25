package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/berrymore/kest/internal/relay"
	"github.com/berrymore/kest/internal/worker/workertest"
)

var discard = slog.New(slog.DiscardHandler)

func TestHandshake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		p := fake.Start(1 << 20)
		defer p.Close()

		pid, err := handshake(t.Context(), p, discard)
		if err != nil || pid != p.Pid() {
			t.Fatalf("handshake = %d, %v; want %d", pid, err, p.Pid())
		}
		if !stopProcess(p, time.Second) {
			t.Error("the worker did not stop gracefully")
		}
	})
}

func TestHandshakeFailures(t *testing.T) {
	tests := []struct {
		name       string
		opts       workertest.Options
		cancel     bool
		wantErr    error  // matched with errors.Is
		wantMsg    string // in the error message
		wantStderr string
		wantTime   time.Duration
	}{
		{
			name:     "boot slower than the timeout",
			opts:     workertest.Options{BootTime: 2 * time.Minute},
			wantErr:  ErrStartTimeout,
			wantTime: time.Minute,
		},
		{
			name:       "boot failure",
			opts:       workertest.Options{BootFailure: "SQLSTATE[HY000] [2002] Connection refused"},
			wantMsg:    "(exit status 1)",
			wantStderr: "SQLSTATE[HY000] [2002] Connection refused",
		},
		{
			name:    "output on stdout",
			opts:    workertest.Options{BootOutput: workertest.Garbage},
			wantMsg: `handshake: stdout is reserved for the RoadRunner protocol, but the worker printed "PHP Fatal er" (`,
		},
		{
			name:    "pid 0",
			opts:    workertest.Options{BadPid: true},
			wantMsg: `invalid handshake reply "{\"pid\":0}"`,
		},
		{
			name:    "pool stopping",
			opts:    workertest.Options{BootTime: time.Hour},
			cancel:  true,
			wantErr: ErrStopped,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				fake := workertest.Fake{Behave: func(int) workertest.Options { return tt.opts }}
				p := fake.Start(1 << 20)
				defer p.Close()

				ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
				defer cancel()
				if tt.cancel {
					cancel()
				}
				start := time.Now()
				_, err := handshake(ctx, p, discard)

				var pe *ProcessError
				if !errors.As(err, &pe) || pe.Op != "handshake" || pe.Pid != p.Pid() {
					t.Fatalf("handshake error = %v, want a handshake *ProcessError", err)
				}
				if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
					t.Errorf("error = %v, want %v", err, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantMsg) {
					t.Errorf("error = %v, want it to contain %s", err, tt.wantMsg)
				}
				if pe.Stderr != tt.wantStderr {
					t.Errorf("Stderr = %q, want %q", pe.Stderr, tt.wantStderr)
				}
				if elapsed := time.Since(start); elapsed != tt.wantTime {
					t.Errorf("failed after %s, want %s", elapsed, tt.wantTime)
				}
				select {
				case <-p.Done():
				default:
					t.Error("the process is still running after a failed handshake")
				}
			})
		})
	}
}

func TestExecute(t *testing.T) {
	tests := []struct {
		name    string
		req     Payload
		ttl     time.Duration
		want    Payload
		wantErr func(error) bool
		wantMsg string // in the error message
		alive   bool   // the process stays in service after an error
		elapsed time.Duration
	}{
		{
			name: "echo",
			req:  Payload{Codec: relay.CodecJSON, Context: []byte(`{"a":1}`), Body: []byte("hello")},
			want: Payload{Context: []byte(`{"a":1}`), Body: []byte("hello")},
		},
		{name: "empty", req: Payload{}, want: Payload{}},
		{name: "within the ttl", req: Payload{Body: []byte("sleep 30s")}, ttl: time.Minute,
			want: Payload{Body: []byte("sleep 30s")}, elapsed: 30 * time.Second},
		{name: "no ttl", req: Payload{Body: []byte("sleep 5h")}, want: Payload{Body: []byte("sleep 5h")}, elapsed: 5 * time.Hour},
		{name: "job error", req: Payload{Body: []byte("error boom")}, wantErr: is[*JobError], wantMsg: "worker error: boom", alive: true},
		{name: "stop request", req: Payload{Body: []byte("stop")}, wantErr: isErr(ErrStopRequested), alive: true},
		{name: "exec ttl", req: Payload{Body: []byte("sleep 2m")}, ttl: time.Minute, wantErr: isErr(ErrExecTimeout),
			wantMsg: "(signal: killed)", elapsed: time.Minute},
		{name: "crash", req: Payload{Body: []byte("crash")}, wantErr: isErr(io.EOF),
			wantMsg: `worker 10000: exec: EOF (exit status 1); stderr: "fake worker crashed"`},
		{name: "exit", req: Payload{Body: []byte("exit")}, wantErr: isErr(io.EOF)},
		{name: "garbage", req: Payload{Body: []byte("garbage")}, wantErr: is[*relay.ProtocolError],
			wantMsg: `exec: stdout is reserved for the RoadRunner protocol, but the worker printed "PHP Fatal er" (`},
		{name: "stream", req: Payload{Body: []byte("stream")}, wantMsg: "streaming responses are not supported"},
		{name: "no options", req: Payload{Body: []byte("no-options")}, wantMsg: "invalid response: options []"},
		{name: "too large", req: Payload{Body: []byte("big 2000")}, wantErr: isErr(relay.ErrFrameTooLarge)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var fake workertest.Fake
				p := fake.Start(1024)
				defer p.Close()
				if _, err := handshake(t.Context(), p, discard); err != nil {
					t.Fatal(err)
				}

				start := time.Now()
				got, err := execute(p, tt.req, tt.ttl)
				if elapsed := time.Since(start); elapsed != tt.elapsed {
					t.Errorf("took %s, want %s", elapsed, tt.elapsed)
				}

				if tt.wantErr == nil && tt.wantMsg == "" {
					if err != nil {
						t.Fatalf("execute: %v", err)
					}
					if got.Codec != tt.want.Codec || !bytes.Equal(got.Context, tt.want.Context) ||
						!bytes.Equal(got.Body, tt.want.Body) {
						t.Errorf("execute = %+v, want %+v", got, tt.want)
					}
				} else {
					checkErr(t, err, tt.wantErr, tt.wantMsg)
					if isProcessErr := is[*ProcessError](err); isProcessErr == tt.alive {
						t.Errorf("error %v: *ProcessError = %v, want %v", err, isProcessErr, !tt.alive)
					}
				}

				select {
				case <-p.Done():
					if tt.alive || err == nil {
						t.Error("the process died")
					}
				default:
					if !tt.alive && err != nil {
						t.Error("the process is still running after a failure")
					}
					stopProcess(p, time.Second)
				}
			})
		})
	}
}

func TestExecuteSequence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake workertest.Fake
		p := fake.Start(1 << 20)
		defer p.Close()
		if _, err := handshake(t.Context(), p, discard); err != nil {
			t.Fatal(err)
		}
		for _, body := range []string{"one", "error soft", "two", "three"} {
			resp, err := execute(p, Payload{Context: []byte("c"), Body: []byte(body)}, time.Minute)
			var jobErr *JobError
			if err != nil && !errors.As(err, &jobErr) {
				t.Fatalf("execute %q: %v", body, err)
			}
			if err == nil && string(resp.Body) != body {
				t.Errorf("response %q, want %q", resp.Body, body)
			}
		}
		if !stopProcess(p, time.Second) {
			t.Error("the worker did not stop gracefully")
		}
	})
}

func TestStopProcess(t *testing.T) {
	t.Run("ignores the stop frame", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fake := workertest.Fake{Behave: func(int) workertest.Options { return workertest.Options{IgnoreStop: true} }}
			p := fake.Start(1 << 20)
			defer p.Close()
			if _, err := handshake(t.Context(), p, discard); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			if stopProcess(p, 10*time.Second) {
				t.Error("stopProcess reported a graceful stop")
			}
			if elapsed := time.Since(start); elapsed != 10*time.Second {
				t.Errorf("killed after %s, want 10s", elapsed)
			}
			if !errors.Is(p.Err(), workertest.ErrKilled) {
				t.Errorf("exit error = %v, want killed", p.Err())
			}
		})
	})

	t.Run("already exited", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var fake workertest.Fake
			p := fake.Start(1 << 20)
			defer p.Close()
			if _, err := handshake(t.Context(), p, discard); err != nil {
				t.Fatal(err)
			}
			if _, err := execute(p, Payload{Body: []byte("exit")}, 0); err == nil {
				t.Fatal("exit command: no error")
			}
			if !stopProcess(p, time.Second) {
				t.Error("stopProcess of an exited process reported a kill")
			}
		})
	})
}

// The same failures with a real process: the error carries what the
// operator needs to see.
func TestHandshakeRealProcess(t *testing.T) {
	tests := []struct {
		name string
		opts workertest.Options
		want string
	}{
		{
			name: "output on stdout",
			opts: workertest.Options{BootOutput: workertest.Garbage},
			// More than the relay read: the rest is still in the pipe.
			want: fmt.Sprintf(`handshake: stdout is reserved for the RoadRunner protocol, but the worker printed %q (`,
				workertest.Garbage),
		},
		{
			name: "long output on stdout",
			opts: workertest.Options{BootOutput: strings.Repeat("x", 1000)},
			want: `but the worker printed "` + strings.Repeat("x", stdoutLimit) + `"... (`,
		},
		{
			name: "boot failure",
			opts: workertest.Options{BootFailure: "Could not open input file: worker.php"},
			want: `handshake: EOF (exit status 1); stderr: "Could not open input file: worker.php"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := helperConfig("fake")
			cfg.env = append(cfg.env, workertest.EnvFor(tt.opts))
			p := startHelper(t, cfg, discard)

			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			_, err := handshake(ctx, p, discard)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("handshake error = %v, want it to contain %s", err, tt.want)
			}
		})
	}
}

// A plain script that prints and exits before kest writes the handshake
// breaks the pipe instead: the output is still shown.
func TestHandshakePrintAndExit(t *testing.T) {
	p := startHelper(t, helperConfig("print"), discard)
	if err := waitExit(t, p); err != nil {
		t.Fatalf("helper exited with %v", err)
	}
	_, err := handshake(t.Context(), p, discard)
	want := fmt.Sprintf(`handshake: stdout is reserved for the RoadRunner protocol, but the worker printed %q`, printOutput)
	if !errors.Is(err, syscall.EPIPE) || !strings.Contains(err.Error(), want) {
		t.Errorf("handshake error = %v, want EPIPE with %s", err, want)
	}
}

func checkErr(t *testing.T, err error, want func(error) bool, msg string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want one with %q", msg)
	}
	if want != nil && !want(err) {
		t.Errorf("error = %v (%T): not the expected kind", err, err)
	}
	if !strings.Contains(err.Error(), msg) {
		t.Errorf("error = %v, want it to contain %s", err, msg)
	}
}

// is reports whether err has an error of type E in its chain.
func is[E error](err error) bool {
	_, ok := errors.AsType[E](err)
	return ok
}

// isErr returns a check for errors.Is(err, target).
func isErr(target error) func(error) bool {
	return func(err error) bool { return errors.Is(err, target) }
}
