package worker

import (
	"context"
	"sync"
	"time"
)

// Stream is the rest of a streamed answer: the frames a worker sends after
// the first one (RoadRunner's STREAM bit). The worker stays busy until the
// stream ends; Close ends it early.
type Stream struct {
	frames chan streamFrame // closed by the slot after the last frame
	stop   chan struct{}    // closed by Close
	once   sync.Once
}

type streamFrame struct {
	payload Payload
	err     error
}

func newStream() *Stream {
	// One frame of read-ahead: frames may be large (up to MaxFrameSize).
	return &Stream{frames: make(chan streamFrame, 1), stop: make(chan struct{})}
}

// Next returns the next frame; ok is false after the last one. Errors are
// those of Exec: a *JobError if the worker reported one (it stays in
// service), a *ProcessError if it failed (it is gone; it may wrap io.EOF,
// which is why the end of the stream is not io.EOF), or ctx's error.
func (s *Stream) Next(ctx context.Context) (p Payload, ok bool, err error) {
	select {
	case <-s.stop:
		return Payload{}, false, nil // closed: a frame read ahead is dropped too
	default:
	}
	select {
	case f, open := <-s.frames:
		if !open {
			return Payload{}, false, nil
		}
		return f.payload, f.err == nil, f.err
	case <-ctx.Done():
		return Payload{}, false, ctx.Err()
	}
}

// Close ends the stream. If the worker is still streaming, it is told to
// stop (a STOP frame) and its remaining frames are dropped; it serves other
// jobs once its stream has ended. Close doesn't wait for that, and may be
// called more than once and after the last frame.
func (s *Stream) Close() {
	s.once.Do(func() { close(s.stop) })
}

// stream hands the frames after the first to st until the worker sends the
// last one, or st is closed or the caller is gone (j.gone), which stops the
// worker's stream. The error is that of the frame that failed, as from
// receiveFrame or stopStream.
func (s *slot) stream(proc process, j *job, st *Stream) error {
	defer close(st.frames)
	cfg := s.pool.cfg
	for {
		f, more, err := receiveFrame(proc, cfg.ExecTTL)
		if err != nil {
			select {
			case st.frames <- streamFrame{err: err}:
			case <-st.stop:
			case <-j.gone:
			}
			return err
		}
		select {
		case st.frames <- streamFrame{payload: f}:
		case <-st.stop:
			return stopIfMore(proc, more, cfg)
		case <-j.gone:
			return stopIfMore(proc, more, cfg)
		}
		if !more {
			return nil
		}
	}
}

// stopIfMore stops a stream that hasn't sent its last frame yet. The worker
// gets the longer of ExecTTL and DestroyTimeout to end it: a PHP worker
// notices the STOP only when it pings, every fifth frame.
func stopIfMore(proc process, more bool, cfg Config) error {
	if !more {
		return nil
	}
	return stopStream(proc, max(cfg.ExecTTL, cfg.DestroyTimeout, time.Second))
}
