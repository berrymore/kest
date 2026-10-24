package relay

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/berrymore/kest/internal/leaktest"
)

func TestMain(m *testing.M) { leaktest.VerifyTestMain(m) }

// goldenFrames are frames produced by the reference implementations on
// 2026-10-24: the server side by goridge v4.0.0 (built the way pool v2.0.0
// builds them), the worker side by roadrunner/worker 3.8.2 on PHP 8.5.12.
var goldenFrames = []struct {
	name  string
	hex   string
	frame Frame
}{
	{
		name:  "server/handshake",
		hex:   "13090c0000005f0c467300007b22706964223a313233347d",
		frame: Frame{Flags: FlagControl | CodecJSON, Payload: []byte(`{"pid":1234}`)},
	},
	{
		name:  "server/stop",
		hex:   "13090d0000003a6bfacb00007b2273746f70223a747275657d",
		frame: Frame{Flags: FlagControl | CodecJSON, Payload: []byte(`{"stop":true}`)},
	},
	{
		name:  "server/request",
		hex:   "140408000000016fb11900000300000068647268656c6c6f",
		frame: Frame{Flags: CodecRaw, Options: []uint32{3}, Payload: []byte("hdrhello")},
	},
	{
		name:  "server/stream stop",
		hex:   "13000000000096d180340200",
		frame: Frame{Stream: StreamStop},
	},
	{
		name:  "server/pong",
		hex:   "13000000000096d180340800",
		frame: Frame{Stream: StreamPong},
	},
	{
		name:  "server/three options, no payload",
		hex:   "168000000000b7f0abd5000000000000efbeadde07000000",
		frame: Frame{Flags: CodecProto, Options: []uint32{0, 0xdeadbeef, 7}},
	},
	{
		name:  "worker/handshake reply",
		hex:   "13010d000000fb208afb00007b22706964223a38363935317d",
		frame: Frame{Flags: FlagControl, Payload: []byte(`{"pid":86951}`)},
	},
	{
		name:  "worker/response",
		hex:   "140011000000d4d120c10000070000006374783a6864726563686f3a68656c6c6f",
		frame: Frame{Options: []uint32{7}, Payload: []byte("ctx:hdrecho:hello")},
	},
	{
		name:  "worker/error",
		hex:   "134004000000881e11e30000626f6f6d",
		frame: Frame{Flags: FlagError, Payload: []byte("boom")},
	},
	{
		name:  "worker/stop request",
		hex:   "14000d000000f339efdb00000d0000007b2273746f70223a747275657d",
		frame: Frame{Options: []uint32{13}, Payload: []byte(`{"stop":true}`)},
	},
}

func TestGoldenFrames(t *testing.T) {
	for _, tt := range goldenFrames {
		t.Run(tt.name, func(t *testing.T) {
			raw := fromHex(t, tt.hex)

			got, err := AppendFrame(nil, &tt.frame)
			if err != nil {
				t.Fatalf("AppendFrame: %v", err)
			}
			if !bytes.Equal(got, raw) {
				t.Errorf("AppendFrame = %x, want %x", got, raw)
			}

			var f Frame
			if err := NewReader(bytes.NewReader(raw), 1<<10).ReadFrame(&f); err != nil {
				t.Fatalf("ReadFrame: %v", err)
			}
			assertFrame(t, &f, &tt.frame)
		})
	}
}

func TestReadFrameSequence(t *testing.T) {
	var stream []byte
	for _, tt := range goldenFrames {
		stream = append(stream, fromHex(t, tt.hex)...)
	}

	r := NewReader(bytes.NewReader(stream), 1<<10)
	for _, tt := range goldenFrames {
		var f Frame
		if err := r.ReadFrame(&f); err != nil {
			t.Fatalf("frame %q: %v", tt.name, err)
		}
		assertFrame(t, &f, &tt.frame)
	}
	var f Frame
	if err := r.ReadFrame(&f); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadFrame after the last frame: %v, want io.EOF", err)
	}
}

func TestReadFrameErrors(t *testing.T) {
	request := fromHex(t, "140408000000016fb11900000300000068647268656c6c6f")
	badCRC := slices.Clone(request)
	badCRC[6] ^= 0xff
	reserved := slices.Clone(request)
	reserved[11] = 0x5a

	tests := []struct {
		name       string
		data       []byte
		maxPayload int
		wantErr    error  // matched with errors.Is
		wantReason string // a *ProtocolError with this reason
	}{
		{name: "empty stream", data: nil, wantErr: io.EOF},
		{name: "truncated header", data: request[:5], wantErr: io.ErrUnexpectedEOF},
		{name: "truncated options", data: request[:14], wantErr: io.ErrUnexpectedEOF},
		{name: "truncated payload", data: request[:20], wantErr: io.ErrUnexpectedEOF},
		{name: "bad checksum", data: badCRC, wantReason: "header checksum mismatch"},
		{
			name:       "text on stdout",
			data:       []byte("PHP Fatal error:  Uncaught Error"),
			wantReason: "header checksum mismatch",
		},
		{name: "version 2", data: header(2<<4|3, 0), wantReason: "unsupported version 2"},
		{name: "version 0", data: header(0<<4|3, 0), wantReason: "unsupported version 0"},
		{name: "header too short", data: header(1<<4|2, 0), wantReason: "invalid header length of 2 words"},
		{name: "too many options", data: header(1<<4|14, 0), wantReason: "invalid header length of 14 words"},
		{name: "payload over the limit", data: request, maxPayload: 7, wantErr: ErrFrameTooLarge},
		{name: "4 GiB payload", data: header(1<<4|3, 0xffffffff), maxPayload: 1 << 20, wantErr: ErrFrameTooLarge},
		{name: "reserved byte is ignored", data: reserved},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewReader(bytes.NewReader(tt.data), cmp.Or(tt.maxPayload, 1<<10))
			var f Frame
			err := r.ReadFrame(&f)

			var pe *ProtocolError
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ReadFrame error = %v, want %v", err, tt.wantErr)
				}
			case tt.wantReason != "":
				if !errors.As(err, &pe) || pe.Reason != tt.wantReason {
					t.Fatalf("ReadFrame error = %v, want a ProtocolError %q", err, tt.wantReason)
				}
				if want := tt.data[:HeaderSize]; !bytes.Equal(pe.Data, want) {
					t.Errorf("ProtocolError.Data = %q, want %q", pe.Data, want)
				}
			default:
				if err != nil {
					t.Fatalf("ReadFrame: %v", err)
				}
			}
		})
	}
}

func TestProtocolErrorMessage(t *testing.T) {
	err := &ProtocolError{Reason: "header checksum mismatch", Data: []byte("Parse error\n")}
	want := `invalid frame: header checksum mismatch (read "Parse error\n")`
	if err.Error() != want {
		t.Errorf("Error() = %s, want %s", err.Error(), want)
	}
}

func TestAppendHeaderErrors(t *testing.T) {
	f := &Frame{Options: make([]uint32, MaxOptions+1)}
	if _, err := AppendHeader(nil, f); err == nil || !strings.Contains(err.Error(), "11 options, at most 10") {
		t.Errorf("AppendHeader with 11 options: %v", err)
	}

	f = &Frame{Options: make([]uint32, MaxOptions)}
	got, err := AppendHeader(nil, f)
	if err != nil {
		t.Fatalf("AppendHeader with 10 options: %v", err)
	}
	if len(got) != MaxHeaderSize {
		t.Errorf("header with 10 options is %d bytes, want %d", len(got), MaxHeaderSize)
	}
}

func TestWriterCoalescesSmallFrames(t *testing.T) {
	tests := []struct {
		name      string
		payload   int
		wantCalls int
	}{
		{name: "empty", payload: 0, wantCalls: 1},
		{name: "small", payload: 100, wantCalls: 1},
		{name: "at the limit", payload: coalesceLimit, wantCalls: 1},
		{name: "over the limit", payload: coalesceLimit + 1, wantCalls: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &Frame{Flags: CodecRaw, Options: []uint32{1}, Payload: bytes.Repeat([]byte{'x'}, tt.payload)}
			var w countingWriter
			if err := NewWriter(&w).WriteFrame(f); err != nil {
				t.Fatalf("WriteFrame: %v", err)
			}
			if w.calls != tt.wantCalls {
				t.Errorf("Write called %d times, want %d", w.calls, tt.wantCalls)
			}
			want, err := AppendFrame(nil, f)
			if err != nil {
				t.Fatalf("AppendFrame: %v", err)
			}
			if !bytes.Equal(w.buf.Bytes(), want) {
				t.Error("WriteFrame output differs from AppendFrame")
			}
		})
	}
}

func TestWriterReusesBuffer(t *testing.T) {
	var out bytes.Buffer
	w := NewWriter(&out)
	r := NewReader(&out, 1<<10)
	for _, payload := range []string{"first frame, longer", "second"} {
		if err := w.WriteFrame(&Frame{Payload: []byte(payload)}); err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
	}
	for _, want := range []string{"first frame, longer", "second"} {
		var f Frame
		if err := r.ReadFrame(&f); err != nil {
			t.Fatalf("ReadFrame: %v", err)
		}
		if string(f.Payload) != want {
			t.Errorf("payload = %q, want %q", f.Payload, want)
		}
	}
}

func TestWriterErrors(t *testing.T) {
	broken := errors.New("broken pipe")
	for _, size := range []int{10, coalesceLimit + 1} {
		w := NewWriter(failingWriter{err: broken})
		if err := w.WriteFrame(&Frame{Payload: make([]byte, size)}); !errors.Is(err, broken) {
			t.Errorf("WriteFrame with %d bytes: %v, want %v", size, err, broken)
		}
	}
	w := NewWriter(io.Discard)
	if err := w.WriteFrame(&Frame{Options: make([]uint32, MaxOptions+1)}); err == nil {
		t.Error("WriteFrame with 11 options succeeded")
	}
}

func TestNewReaderPanicsOnBadLimit(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewReader(r, 0) did not panic")
		}
	}()
	NewReader(bytes.NewReader(nil), 0)
}

func TestFlagsHas(t *testing.T) {
	f := FlagControl | CodecJSON
	if !f.Has(FlagControl) || !f.Has(CodecJSON) || !f.Has(FlagControl|CodecJSON) {
		t.Errorf("%#x.Has: set bits not reported", f)
	}
	if f.Has(FlagError) || f.Has(FlagControl|FlagError) {
		t.Errorf("%#x.Has: unset bits reported", f)
	}
	s := StreamMore | StreamPing
	if !s.Has(StreamPing) || s.Has(StreamStop) {
		t.Errorf("%#x.Has: wrong result", s)
	}
}

func FuzzReadFrame(f *testing.F) {
	for _, tt := range goldenFrames {
		f.Add(fromHex(f, tt.hex))
	}
	f.Add([]byte("Could not open input file: worker.php\n"))
	f.Add(header(1<<4|13, 4))

	f.Fuzz(func(t *testing.T, data []byte) {
		readAll(t, data)
		// Random bytes almost never carry a valid checksum. Fix the first
		// header's, so the checks after it get explored too.
		if len(data) >= HeaderSize {
			fixed := slices.Clone(data)
			binary.LittleEndian.PutUint32(fixed[6:], crc32.ChecksumIEEE(fixed[:6]))
			readAll(t, fixed)
		}
	})
}

// readAll reads frames from data until an error, checking the error class,
// the limits, and that re-encoding gives back the bytes that were read.
func readAll(t *testing.T, data []byte) {
	t.Helper()
	const limit = 1 << 10
	br := bytes.NewReader(data)
	r := NewReader(br, limit)
	for {
		start := len(data) - br.Len()
		var fr Frame
		err := r.ReadFrame(&fr)
		if err != nil {
			var pe *ProtocolError
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) &&
				!errors.Is(err, ErrFrameTooLarge) && !errors.As(err, &pe) {
				t.Fatalf("unexpected error: %v", err)
			}
			return
		}
		if len(fr.Payload) > limit || len(fr.Options) > MaxOptions {
			t.Fatalf("frame over the limits: %d options, %d payload bytes", len(fr.Options), len(fr.Payload))
		}

		// Byte 11 is reserved: it is ignored on read and written as zero.
		got, err := AppendFrame(nil, &fr)
		if err != nil {
			t.Fatalf("AppendFrame: %v", err)
		}
		want := slices.Clone(data[start : len(data)-br.Len()])
		want[11] = 0
		if !bytes.Equal(got, want) {
			t.Fatalf("re-encoded %x, read %x", got, want)
		}
	}
}

func FuzzRoundTrip(f *testing.F) {
	f.Add(byte(FlagControl|CodecJSON), byte(0), []byte{}, []byte(`{"pid":1}`))
	f.Add(byte(CodecRaw), byte(StreamMore|StreamPing), []byte{3, 0, 0, 0}, []byte("hdrbody"))

	f.Fuzz(func(t *testing.T, flags, stream byte, opts, payload []byte) {
		in := Frame{Flags: Flags(flags), Stream: Stream(stream)}
		for i := 0; i+4 <= len(opts) && len(in.Options) < MaxOptions; i += 4 {
			in.Options = append(in.Options, binary.LittleEndian.Uint32(opts[i:]))
		}
		if len(payload) > 0 {
			in.Payload = payload
		}

		var buf bytes.Buffer
		if err := NewWriter(&buf).WriteFrame(&in); err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
		var out Frame
		if err := NewReader(&buf, max(len(payload), 1)).ReadFrame(&out); err != nil {
			t.Fatalf("ReadFrame: %v", err)
		}
		assertFrame(t, &out, &in)
	})
}

var benchSizes = []struct {
	name string
	size int
}{
	{"1KiB", 1 << 10},
	{"64KiB", 64 << 10},
	{"1MiB", 1 << 20},
}

func BenchmarkWriteFrame(b *testing.B) {
	for _, bs := range benchSizes {
		b.Run(bs.name, func(b *testing.B) {
			f := &Frame{Flags: CodecProto, Options: []uint32{100}, Payload: make([]byte, bs.size)}
			w := NewWriter(io.Discard)
			b.SetBytes(int64(bs.size))
			b.ReportAllocs()
			for b.Loop() {
				if err := w.WriteFrame(f); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkReadFrame(b *testing.B) {
	for _, bs := range benchSizes {
		b.Run(bs.name, func(b *testing.B) {
			raw, err := AppendFrame(nil, &Frame{Flags: CodecProto, Options: []uint32{100}, Payload: make([]byte, bs.size)})
			if err != nil {
				b.Fatal(err)
			}
			br := bytes.NewReader(raw)
			r := NewReader(br, bs.size)
			b.SetBytes(int64(bs.size))
			b.ReportAllocs()
			for b.Loop() {
				br.Reset(raw)
				var f Frame
				if err := r.ReadFrame(&f); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// header builds a 12-byte header with a valid checksum.
func header(b0 byte, size uint32) []byte {
	h := []byte{b0, 0}
	h = binary.LittleEndian.AppendUint32(h, size)
	h = binary.LittleEndian.AppendUint32(h, crc32.ChecksumIEEE(h))
	return append(h, 0, 0)
}

func fromHex(tb testing.TB, s string) []byte {
	tb.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		tb.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

func assertFrame(t *testing.T, got, want *Frame) {
	t.Helper()
	if got.Flags != want.Flags || got.Stream != want.Stream {
		t.Errorf("flags %#x, stream %#x; want %#x, %#x", got.Flags, got.Stream, want.Flags, want.Stream)
	}
	if !slices.Equal(got.Options, want.Options) {
		t.Errorf("options %v, want %v", got.Options, want.Options)
	}
	if !bytes.Equal(got.Payload, want.Payload) {
		t.Errorf("payload %q, want %q", got.Payload, want.Payload)
	}
	if (got.Options == nil) != (len(want.Options) == 0) || (got.Payload == nil) != (len(want.Payload) == 0) {
		t.Errorf("empty options and payload must be nil: got %#v", got)
	}
}

type countingWriter struct {
	buf   bytes.Buffer
	calls int
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.calls++
	return w.buf.Write(p)
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }
