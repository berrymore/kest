// Package plugin defines plugins: named components of the config that PHP
// workers call through RPC, by name ("kv" for kv.Set), and the registry
// category of their types (ADR-0010).
package plugin

import (
	"context"
	"errors"
	"fmt"

	"github.com/berrymore/kest/internal/registry"
)

// Codec is how a call's body and its answer are encoded. The values are the
// codec bits of goridge frames.
type Codec uint8

// Codecs.
const (
	CodecRaw     Codec = 0x04 // bytes as they are
	CodecJSON    Codec = 0x08
	CodecMsgpack Codec = 0x10
	CodecGob     Codec = 0x20 // what goridge assumes when a frame names no codec
	CodecProto   Codec = 0x80
)

// String returns the codec's name.
func (c Codec) String() string {
	switch c {
	case CodecRaw:
		return "raw"
	case CodecJSON:
		return "json"
	case CodecMsgpack:
		return "msgpack"
	case CodecGob:
		return "gob"
	case CodecProto:
		return "protobuf"
	}
	return fmt.Sprintf("codec %#x", uint8(c))
}

// Call is one call to a plugin.
type Call struct {
	Method string // what follows the plugin's name: "Set" for kv.Set
	Codec  Codec  // of Body, and of the answer
	Body   []byte // owned by the plugin
}

// Plugin serves calls. Call may run concurrently, and must return once ctx
// is done. The answer is encoded in c.Codec; an error goes back to the
// caller as its message, so it should say what the caller can act on.
//
// A plugin may also implement Start(ctx) error and Stop(ctx) error, which run
// before the first call and after the last, and Prober.
type Plugin interface {
	Call(ctx context.Context, c Call) ([]byte, error)
}

// Prober is implemented by plugins that can be unable to serve; /ready fails
// while one of them can't.
type Prober interface {
	Serving() bool
}

// ErrUnknownMethod is what Call returns, possibly wrapped, for a method the
// plugin doesn't have; the caller gets net/rpc's message, which PHP libraries
// look for.
var ErrUnknownMethod = errors.New("unknown method")

// Types is the registry category of plugin types.
var Types = registry.NewCategory[Plugin]("plugin")
