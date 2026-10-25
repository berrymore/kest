// Package relay implements goridge framing: the wire protocol between kest and
// RoadRunner-compatible worker processes (ADR-0003). The format matches goridge
// v4 (Go) and roadrunner/goridge (PHP); .ai/research/roadrunner.md describes it.
package relay

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
)

// Frame layout limits.
const (
	// Version is the only protocol version goridge has.
	Version = 1
	// HeaderSize is the size of a header without options.
	HeaderSize = 12
	// MaxOptions is the most options a frame may carry.
	MaxOptions = 10
	// MaxHeaderSize is the size of a header with MaxOptions options.
	MaxHeaderSize = HeaderSize + 4*MaxOptions
)

// Flags is byte 1 of a header: the frame type and the payload codec. Bits can
// be combined.
type Flags byte

// Frame flags. A frame without FlagControl or FlagError carries data.
const (
	FlagControl Flags = 0x01 // handshake or stop
	CodecRaw    Flags = 0x04
	CodecJSON   Flags = 0x08
	FlagError   Flags = 0x40 // the payload is an error message from the worker
	CodecProto  Flags = 0x80
)

// CodecMask selects the codec bits of Flags, including codecs kest doesn't
// name (msgpack 0x10, gob 0x20).
const CodecMask Flags = CodecRaw | CodecJSON | 0x10 | 0x20 | CodecProto

// Has reports whether all bits of x are set.
func (f Flags) Has(x Flags) bool {
	return f&x == x
}

// Stream is byte 10 of a header: stream control bits.
type Stream byte

// Stream bits.
const (
	StreamMore Stream = 0x01 // more frames of this response follow
	StreamStop Stream = 0x02 // the server cancels a stream
	StreamPing Stream = 0x04 // the worker asks for a StreamPong
	StreamPong Stream = 0x08
)

// Has reports whether all bits of x are set.
func (s Stream) Has(x Stream) bool {
	return s&x == x
}

// Frame is one goridge message. Byte 11 of the header is reserved: it is
// written as zero and ignored on read.
type Frame struct {
	Flags   Flags
	Stream  Stream
	Options []uint32 // at most MaxOptions
	Payload []byte
}

// ErrFrameTooLarge is returned for a payload over the limit: the Reader's
// limit on read, 4 GiB on write.
var ErrFrameTooLarge = errors.New("frame payload too large")

// ProtocolError reports bytes that are not a valid frame. A worker that prints
// to stdout, which is the relay, causes one.
type ProtocolError struct {
	Reason string
	Data   []byte // the header bytes as read
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("invalid frame: %s (read %q)", e.Reason, e.Data)
}

// AppendHeader appends the header of f to dst. The header covers the payload
// length, so f.Payload must be final.
func AppendHeader(dst []byte, f *Frame) ([]byte, error) {
	if len(f.Options) > MaxOptions {
		return dst, fmt.Errorf("frame has %d options, at most %d allowed", len(f.Options), MaxOptions)
	}
	if uint64(len(f.Payload)) > math.MaxUint32 {
		return dst, fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, len(f.Payload))
	}

	start := len(dst)
	dst = append(dst, Version<<4|byte(3+len(f.Options)), byte(f.Flags)) //nolint:gosec // at most 13, checked above
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(f.Payload))) //nolint:gosec // checked above
	dst = binary.LittleEndian.AppendUint32(dst, crc32.ChecksumIEEE(dst[start:start+6]))
	dst = append(dst, byte(f.Stream), 0)
	for _, o := range f.Options {
		dst = binary.LittleEndian.AppendUint32(dst, o)
	}
	return dst, nil
}

// AppendFrame appends the encoded frame, header and payload, to dst.
func AppendFrame(dst []byte, f *Frame) ([]byte, error) {
	dst, err := AppendHeader(dst, f)
	if err != nil {
		return dst, err
	}
	return append(dst, f.Payload...), nil
}

// coalesceLimit is the largest payload that Writer copies next to the header
// to send the frame in one Write call. Larger payloads are written as they are.
const coalesceLimit = 32 << 10

// Writer writes frames to a stream. It is not safe for concurrent use.
type Writer struct {
	w   io.Writer
	buf []byte
}

// NewWriter returns a Writer that writes to w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w}
}

// WriteFrame writes f. After an error the stream is out of sync and must be
// closed.
func (w *Writer) WriteFrame(f *Frame) error {
	buf, err := AppendHeader(w.buf[:0], f)
	if err != nil {
		return err
	}
	if len(f.Payload) <= coalesceLimit {
		buf = append(buf, f.Payload...)
		w.buf = buf
		_, err = w.w.Write(buf)
		return err
	}
	w.buf = buf
	if _, err := w.w.Write(buf); err != nil {
		return err
	}
	_, err = w.w.Write(f.Payload)
	return err
}

// readBuffer is the size of a Reader's buffer: a frame that fits, header
// and payload, takes one read from the stream instead of three (header,
// options, payload). Larger payloads are read past the buffer.
const readBuffer = 8 << 10

// Reader reads frames from a stream. It is not safe for concurrent use.
type Reader struct {
	r          *bufio.Reader
	maxPayload uint32
	hdr        [MaxHeaderSize]byte
}

// NewReader returns a Reader that reads from r and rejects payloads larger
// than maxPayload bytes. It panics if maxPayload is not in 1…math.MaxUint32.
// It reads ahead: Buffered returns what it read past the last frame.
func NewReader(r io.Reader, maxPayload int) *Reader {
	if maxPayload <= 0 || maxPayload > math.MaxUint32 {
		panic(fmt.Sprintf("relay: invalid payload limit %d", maxPayload))
	}
	return &Reader{r: bufio.NewReaderSize(r, readBuffer), maxPayload: uint32(maxPayload)}
}

// Buffered returns a copy of the bytes read from the stream and not yet
// consumed, such as what follows a header that was not a frame.
func (r *Reader) Buffered() []byte {
	b, _ := r.r.Peek(r.r.Buffered())
	return bytes.Clone(b)
}

// ReadFrame reads the next frame into f. f.Options and f.Payload are newly
// allocated, or nil when empty, so f may be handed to another goroutine.
//
// It returns io.EOF if the stream ends before a frame, io.ErrUnexpectedEOF if
// it ends inside one, a *ProtocolError for bytes that are not a frame, and an
// error wrapping ErrFrameTooLarge for a payload over the limit. After any
// error the stream is out of sync and must be closed.
func (r *Reader) ReadFrame(f *Frame) error {
	h := r.hdr[:HeaderSize]
	if _, err := io.ReadFull(r.r, h); err != nil {
		return err
	}
	if crc32.ChecksumIEEE(h[:6]) != binary.LittleEndian.Uint32(h[6:10]) {
		return &ProtocolError{Reason: "header checksum mismatch", Data: bytes.Clone(h)}
	}
	if v := h[0] >> 4; v != Version {
		return &ProtocolError{Reason: fmt.Sprintf("unsupported version %d", v), Data: bytes.Clone(h)}
	}
	words := int(h[0] & 0x0f)
	if words < 3 || words > 3+MaxOptions {
		return &ProtocolError{Reason: fmt.Sprintf("invalid header length of %d words", words), Data: bytes.Clone(h)}
	}
	size := binary.LittleEndian.Uint32(h[2:6])
	if size > r.maxPayload {
		return fmt.Errorf("%w: %d bytes, limit %d", ErrFrameTooLarge, size, r.maxPayload)
	}

	*f = Frame{Flags: Flags(h[1]), Stream: Stream(h[10])}
	if words > 3 {
		opts := r.hdr[HeaderSize : 4*words]
		if _, err := io.ReadFull(r.r, opts); err != nil {
			return insideFrame(err)
		}
		f.Options = make([]uint32, words-3)
		for i := range f.Options {
			f.Options[i] = binary.LittleEndian.Uint32(opts[4*i:])
		}
	}
	if size > 0 {
		f.Payload = make([]byte, size)
		if _, err := io.ReadFull(r.r, f.Payload); err != nil {
			return insideFrame(err)
		}
	}
	return nil
}

// insideFrame reports a clean EOF in the middle of a frame as truncation.
func insideFrame(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
