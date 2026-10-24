package workertest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/berrymore/kest/internal/leaktest"
	"github.com/berrymore/kest/internal/relay"
)

func TestMain(m *testing.M) { leaktest.VerifyTestMain(m) }

func TestFakeEchoAndStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake Fake
		p := fake.Start(1 << 20)
		defer p.Close()
		handshake(t, p, `{"pid":10000}`)

		resp := request(t, p, "hdr", "hello")
		if !bytes.Equal(resp.Payload, []byte("hdrhello")) || len(resp.Options) != 1 || resp.Options[0] != 3 {
			t.Errorf("echo = %v %q, want [3] %q", resp.Options, resp.Payload, "hdrhello")
		}

		send(t, p, &relay.Frame{Flags: relay.FlagControl | relay.CodecJSON, Payload: []byte(`{"stop":true}`)})
		<-p.Done()
		if err := p.Err(); err != nil {
			t.Errorf("exit after stop: %v", err)
		}
		if got := fake.Running(); len(got) != 0 {
			t.Errorf("Running() = %d processes after exit", len(got))
		}
		if fake.Process(10000) != p || fake.Process(10001) != nil || fake.Started() != 1 {
			t.Error("Process/Started don't match the started process")
		}
	})
}

func TestFakeCommands(t *testing.T) {
	tests := []struct {
		body    string
		check   func(p *Process, f *relay.Frame, err error) string // a problem, or ""
		wantErr error                                              // the exit error, after Kill for commands that don't exit
	}{
		{body: "error boom", check: func(_ *Process, f *relay.Frame, err error) string {
			if err != nil || f.Flags != relay.FlagError || string(f.Payload) != "boom" {
				return fmt.Sprintf("got %v %#x %q, want an error frame %q", err, f.Flags, f.Payload, "boom")
			}
			return ""
		}, wantErr: ErrKilled},
		{body: "stop", check: func(_ *Process, f *relay.Frame, err error) string {
			if err != nil || string(f.Payload) != `{"stop":true}` || f.Options[0] != 13 {
				return fmt.Sprintf("got %v %v %q, want the stop request", err, f.Options, f.Payload)
			}
			return ""
		}, wantErr: ErrKilled},
		{body: "crash", check: func(p *Process, _ *relay.Frame, err error) string {
			if !errors.Is(err, io.EOF) || p.Stderr() != "fake worker crashed" {
				return fmt.Sprintf("got %v, stderr %q; want EOF and a crash message", err, p.Stderr())
			}
			return ""
		}, wantErr: &ExitError{Code: 1}},
		{body: "exit", check: func(_ *Process, _ *relay.Frame, err error) string {
			if !errors.Is(err, io.EOF) {
				return fmt.Sprintf("got %v, want EOF", err)
			}
			return ""
		}},
		{body: "garbage", check: func(_ *Process, _ *relay.Frame, err error) string {
			var pe *relay.ProtocolError
			if !errors.As(err, &pe) || !strings.HasPrefix(Garbage, string(pe.Data)) {
				return fmt.Sprintf("got %v, want a protocol error with the garbage", err)
			}
			return ""
		}, wantErr: ErrKilled},
		{body: "stream", check: func(_ *Process, f *relay.Frame, err error) string {
			if err != nil || !f.Stream.Has(relay.StreamMore) {
				return fmt.Sprintf("got %v, stream bits %#x; want StreamMore", err, f.Stream)
			}
			return ""
		}, wantErr: ErrKilled},
		{body: "big 100", check: func(_ *Process, f *relay.Frame, err error) string {
			if err != nil || len(f.Payload) != 3+100 {
				return fmt.Sprintf("got %v, %d bytes; want 103", err, len(f.Payload))
			}
			return ""
		}, wantErr: ErrKilled},
		{body: "memory 5000", check: func(p *Process, _ *relay.Frame, err error) string {
			if err != nil || p.RSS() != 5000 {
				return fmt.Sprintf("got %v, RSS %d; want 5000", err, p.RSS())
			}
			return ""
		}, wantErr: ErrKilled},
		{body: "no-options", check: func(_ *Process, f *relay.Frame, err error) string {
			if err != nil || f.Options != nil {
				return fmt.Sprintf("got %v, options %v; want none", err, f.Options)
			}
			return ""
		}, wantErr: ErrKilled},
	}

	for _, tt := range tests {
		t.Run(tt.body, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var fake Fake
				p := fake.Start(1 << 20)
				defer p.Close()
				handshake(t, p, `{"pid":10000}`)

				send(t, p, requestFrame("hdr", tt.body))
				var f relay.Frame
				err := p.Conn().Receive(&f)
				if problem := tt.check(p, &f, err); problem != "" {
					t.Error(problem)
				}

				p.Kill()
				<-p.Done()
				if err := p.Err(); !sameExit(err, tt.wantErr) {
					t.Errorf("exit error = %v, want %v", err, tt.wantErr)
				}
			})
		})
	}
}

func TestFakeSleepUsesTheClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var fake Fake
		p := fake.Start(1 << 20)
		defer p.Close()
		handshake(t, p, `{"pid":10000}`)

		start := time.Now()
		resp := request(t, p, "", "sleep 90s")
		if elapsed := time.Since(start); elapsed != 90*time.Second {
			t.Errorf("sleep took %s, want 90s", elapsed)
		}
		if string(resp.Payload) != "sleep 90s" {
			t.Errorf("response %q, want the echo", resp.Payload)
		}

		// Kill ends a sleep at once.
		send(t, p, requestFrame("", "sleep 1h"))
		synctest.Wait()
		p.Kill()
		<-p.Done()
		if elapsed := time.Since(start); elapsed != 90*time.Second {
			t.Errorf("killed during sleep after %s, want no time to pass", elapsed-90*time.Second)
		}
	})
}

func TestFakeBoot(t *testing.T) {
	t.Run("boot time", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fake := Fake{Behave: func(int) Options { return Options{BootTime: 30 * time.Second} }}
			p := fake.Start(1 << 20)
			defer p.Close()
			start := time.Now()
			handshake(t, p, `{"pid":10000}`)
			if elapsed := time.Since(start); elapsed != 30*time.Second {
				t.Errorf("handshake after %s, want 30s", elapsed)
			}
			p.Kill()
			<-p.Done()
		})
	})

	t.Run("boot failure", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fake := Fake{Behave: func(int) Options { return Options{Stderr: "booting\n", BootFailure: "no database"} }}
			p := fake.Start(1 << 20)
			defer p.Close()
			<-p.Done()
			if !sameExit(p.Err(), &ExitError{Code: 1}) || p.Stderr() != "booting\nno database" {
				t.Errorf("exit %v, stderr %q", p.Err(), p.Stderr())
			}
			err := p.Conn().Send(hello())
			if err == nil {
				var f relay.Frame
				err = p.Conn().Receive(&f)
			}
			if err == nil {
				t.Error("handshake with a failed worker succeeded")
			}
		})
	})

	t.Run("boot output", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fake := Fake{Behave: func(int) Options { return Options{BootOutput: Garbage} }}
			p := fake.Start(1 << 20)
			defer p.Close()
			send(t, p, hello())
			var f relay.Frame
			var pe *relay.ProtocolError
			if err := p.Conn().Receive(&f); !errors.As(err, &pe) {
				t.Errorf("handshake reply: %v, want a protocol error", err)
			}
			p.Kill()
			<-p.Done()
		})
	})

	t.Run("bad pid", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fake := Fake{Behave: func(int) Options { return Options{BadPid: true} }}
			p := fake.Start(1 << 20)
			defer p.Close()
			handshake(t, p, `{"pid":0}`)
			p.Kill()
			<-p.Done()
		})
	})

	t.Run("ignore stop", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fake := Fake{Behave: func(int) Options { return Options{IgnoreStop: true} }}
			p := fake.Start(1 << 20)
			defer p.Close()
			handshake(t, p, `{"pid":10000}`)
			send(t, p, &relay.Frame{Flags: relay.FlagControl | relay.CodecJSON, Payload: []byte(`{"stop":true}`)})
			time.Sleep(time.Hour)
			select {
			case <-p.Done():
				t.Fatal("the fake exited after the stop frame")
			default:
			}
			p.Kill()
			<-p.Done()
			if !errors.Is(p.Err(), ErrKilled) {
				t.Errorf("exit error = %v, want %v", p.Err(), ErrKilled)
			}
		})
	})

	t.Run("invalid handshake", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var fake Fake
			p := fake.Start(1 << 20)
			defer p.Close()
			send(t, p, requestFrame("", "not a handshake"))
			<-p.Done()
			if !strings.Contains(p.Stderr(), "invalid handshake frame") {
				t.Errorf("stderr = %q", p.Stderr())
			}
		})
	})
}

func TestEnvFor(t *testing.T) {
	got := EnvFor(Options{BootTime: time.Second, IgnoreStop: true})
	if want := OptionsEnv + `={"boot_time":1000000000,"ignore_stop":true}`; got != want {
		t.Errorf("EnvFor = %s, want %s", got, want)
	}
}

func hello() *relay.Frame {
	return &relay.Frame{Flags: relay.FlagControl | relay.CodecJSON, Payload: []byte(`{"pid":1}`)}
}

func handshake(t *testing.T, p *Process, wantReply string) {
	t.Helper()
	send(t, p, hello())
	var f relay.Frame
	if err := p.Conn().Receive(&f); err != nil {
		t.Fatalf("handshake reply: %v", err)
	}
	if f.Flags != relay.FlagControl || string(f.Payload) != wantReply {
		t.Fatalf("handshake reply %#x %q, want CONTROL %q", f.Flags, f.Payload, wantReply)
	}
}

func requestFrame(header, body string) *relay.Frame {
	return &relay.Frame{
		Flags:   relay.CodecRaw,
		Options: []uint32{uint32(len(header))},
		Payload: []byte(header + body),
	}
}

func request(t *testing.T, p *Process, header, body string) *relay.Frame {
	t.Helper()
	send(t, p, requestFrame(header, body))
	var f relay.Frame
	if err := p.Conn().Receive(&f); err != nil {
		t.Fatalf("response to %q: %v", body, err)
	}
	return &f
}

func send(t *testing.T, p *Process, f *relay.Frame) {
	t.Helper()
	if err := p.Conn().Send(f); err != nil {
		t.Fatalf("send: %v", err)
	}
}

func sameExit(got, want error) bool {
	var g, w *ExitError
	if errors.As(want, &w) {
		return errors.As(got, &g) && g.Code == w.Code
	}
	return errors.Is(got, want)
}
