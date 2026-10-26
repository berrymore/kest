// Package wire is the protocol between kest and an external plugin, shared
// by both ends: kest's external plugin type and the SDK (pkg/plugin).
// docs/plugins.md describes it for plugins in other languages.
//
// kest starts the plugin with one end of a unix socket pair as file
// descriptor FD, and speaks goridge frames over it:
//
//   - Handshake: kest sends Hello as a CONTROL frame (JSON); the plugin
//     answers Ready, or an ERROR frame with the reason.
//   - Calls: kest sends call frames, options [id, len(method)], payload
//     method ‖ body, the codec in the flags, as goridge RPC does; the plugin
//     answers each, in any order, with the same id and method. An error adds
//     ERROR to the flags, with the message as the body, and a third option,
//     its code.
//   - Control: kest sends Control frames to cancel a call or stop the
//     plugin, which then answers its calls in progress and exits.
package wire

import (
	"encoding/json"
	"fmt"

	"github.com/berrymore/kest/internal/relay"
)

// The plugin's environment.
const (
	EnvFD       = "KEST_PLUGIN_FD"       // the socket's descriptor, "3"
	EnvProtocol = "KEST_PLUGIN_PROTOCOL" // the newest version kest speaks
	FD          = 3                      // the first of exec.Cmd.ExtraFiles
)

// Version is the protocol's version. A plugin answers the version it
// speaks, at most the one in Hello.
const Version = 1

// Hello starts the handshake.
type Hello struct {
	Protocol int             `json:"protocol"`
	Name     string          `json:"name"`             // the plugin's name in kest's config
	Config   json.RawMessage `json:"config,omitempty"` // plugin_config
	// MaxFrameSize bounds the payload of a frame either way.
	MaxFrameSize int `json:"max_frame_size"`
}

// Ready ends a successful handshake.
type Ready struct {
	Protocol int `json:"protocol"`
}

// Control asks the plugin to cancel a call or to stop.
type Control struct {
	Cancel *uint32 `json:"cancel,omitempty"` // the id of a call to cancel
	Stop   bool    `json:"stop,omitempty"`
}

// Error codes, the third option of an error answer.
const (
	CodeError         = 0 // the plugin's own error
	CodeUnknownMethod = 1 // no such method: kest answers net/rpc's message
)

// Call is a call frame or its answer.
type Call struct {
	ID     uint32
	Method string
	Codec  relay.Flags // a codec bit
	Body   []byte      // the answer's message when Failed
	Failed bool
	Code   uint32 // of a failed answer
}

// Frame encodes c.
func (c *Call) Frame() *relay.Frame {
	payload := make([]byte, 0, len(c.Method)+len(c.Body))
	payload = append(append(payload, c.Method...), c.Body...)
	f := &relay.Frame{Flags: c.Codec, Options: []uint32{c.ID, uint32(len(c.Method))}, Payload: payload} //nolint:gosec // within the frame size
	if c.Failed {
		f.Flags |= relay.FlagError
		f.Options = append(f.Options, c.Code)
	}
	return f
}

// ParseCall decodes a call frame or an answer.
func ParseCall(f *relay.Frame) (*Call, error) {
	failed := f.Flags.Has(relay.FlagError)
	if n := len(f.Options); n != 2 && (!failed || n != 3) {
		return nil, fmt.Errorf("a call frame has 2 options, a failed answer 3; got %d", n)
	}
	n := f.Options[1]
	if uint64(n) > uint64(len(f.Payload)) {
		return nil, fmt.Errorf("method length %d is past the payload of %d bytes", n, len(f.Payload))
	}
	c := &Call{ID: f.Options[0], Method: string(f.Payload[:n]), Codec: f.Flags & relay.CodecMask,
		Body: f.Payload[n:], Failed: failed}
	if failed && len(f.Options) == 3 {
		c.Code = f.Options[2]
	}
	return c, nil
}

// ControlFrame encodes a CONTROL message.
func ControlFrame(v any) (*relay.Frame, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return &relay.Frame{Flags: relay.FlagControl | relay.CodecJSON, Payload: b}, nil
}

// ErrorFrame is a CONTROL frame with ERROR: a failed handshake.
func ErrorFrame(msg string) *relay.Frame {
	return &relay.Frame{Flags: relay.FlagControl | relay.FlagError, Payload: []byte(msg)}
}
