package relay

import "io"

// Conn exchanges frames with one worker over a byte stream: the worker's
// stdout and stdin for pipes, a net.Conn for sockets. Send and Receive may run
// in different goroutines, but each one in a single goroutine at a time.
//
// Conn has no deadlines and no Close: the owner of the streams enforces
// timeouts by killing the worker, which ends any blocked I/O.
type Conn struct {
	r *Reader
	w *Writer
}

// NewConn returns a Conn that receives frames from r, rejecting payloads
// over maxPayload bytes, and sends frames to w.
func NewConn(r io.Reader, w io.Writer, maxPayload int) *Conn {
	return &Conn{r: NewReader(r, maxPayload), w: NewWriter(w)}
}

// Send writes one frame.
func (c *Conn) Send(f *Frame) error {
	return c.w.WriteFrame(f)
}

// Receive reads one frame into f; see Reader.ReadFrame.
func (c *Conn) Receive(f *Frame) error {
	return c.r.ReadFrame(f)
}

// Buffered returns what Receive read from the stream past the last frame;
// see Reader.Buffered.
func (c *Conn) Buffered() []byte {
	return c.r.Buffered()
}
