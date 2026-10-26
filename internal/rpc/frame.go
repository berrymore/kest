package rpc

import (
	"fmt"

	"github.com/berrymore/kest/internal/plugin"
	"github.com/berrymore/kest/internal/relay"
)

// codecMsgpack is goridge's msgpack bit, which the relay package doesn't name.
const codecMsgpack relay.Flags = 0x10

// request is a call frame: options [seq, len(method)], payload method ‖ body.
type request struct {
	seq    uint32
	method string // "kv.Set"
	codec  plugin.Codec
	body   []byte
}

// parseRequest reads a call frame. A frame that isn't one ends the
// connection, as in goridge.
func parseRequest(f *relay.Frame) (*request, error) {
	if len(f.Options) != 2 {
		return nil, fmt.Errorf("a call has 2 options, the sequence number and the method length; got %d",
			len(f.Options))
	}
	n := f.Options[1]
	if uint64(n) > uint64(len(f.Payload)) {
		return nil, fmt.Errorf("method length %d is past the payload of %d bytes", n, len(f.Payload))
	}
	return &request{seq: f.Options[0], method: string(f.Payload[:n]), codec: codecOf(f.Flags), body: f.Payload[n:]},
		nil
}

// codecOf picks the codec of a frame's flags as goridge does: the first set
// of protobuf, JSON, raw and msgpack, else gob.
func codecOf(flags relay.Flags) plugin.Codec {
	switch {
	case flags&relay.CodecProto != 0:
		return plugin.CodecProto
	case flags&relay.CodecJSON != 0:
		return plugin.CodecJSON
	case flags&relay.CodecRaw != 0:
		return plugin.CodecRaw
	case flags&codecMsgpack != 0:
		return plugin.CodecMsgpack
	default:
		return plugin.CodecGob
	}
}

// answer returns the frame of a successful call: the request's codec,
// options [seq, len(method)], payload method ‖ body.
func answer(req *request, body []byte) *relay.Frame {
	payload := make([]byte, 0, len(req.method)+len(body))
	payload = append(append(payload, req.method...), body...)
	return &relay.Frame{
		Flags:   relay.Flags(req.codec),
		Options: []uint32{req.seq, uint32(len(req.method))}, //nolint:gosec // within the frame size limit
		Payload: payload,
	}
}

// failure returns the frame of a failed call: ERROR added to the codec, the
// message after the method.
func failure(req *request, msg string) *relay.Frame {
	f := answer(req, []byte(msg))
	f.Flags |= relay.FlagError
	return f
}
