package relay

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
)

func TestConnBothDirections(t *testing.T) {
	server, worker := net.Pipe()
	defer server.Close()
	sc := NewConn(server, server, 1<<10)
	wc := NewConn(worker, worker, 1<<10)

	// The worker side echoes frames until the stream ends.
	var wg sync.WaitGroup
	wg.Go(func() {
		defer worker.Close()
		for {
			var f Frame
			if err := wc.Receive(&f); err != nil {
				return
			}
			f.Payload = append([]byte("echo:"), f.Payload...)
			if err := wc.Send(&f); err != nil {
				return
			}
		}
	})

	for _, payload := range []string{"one", "two", ""} {
		if err := sc.Send(&Frame{Flags: CodecRaw, Options: []uint32{0}, Payload: []byte(payload)}); err != nil {
			t.Fatalf("Send: %v", err)
		}
		var f Frame
		if err := sc.Receive(&f); err != nil {
			t.Fatalf("Receive: %v", err)
		}
		if want := "echo:" + payload; !bytes.Equal(f.Payload, []byte(want)) || f.Flags != CodecRaw {
			t.Errorf("got %q with flags %#x, want %q with %#x", f.Payload, f.Flags, want, CodecRaw)
		}
	}

	server.Close()
	wg.Wait()
	var f Frame
	if err := sc.Receive(&f); !errors.Is(err, io.ErrClosedPipe) {
		t.Errorf("Receive after close = %v, want %v", err, io.ErrClosedPipe)
	}
}
