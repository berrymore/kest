// Package plugin is kest's plugin SDK: what a plugin implements, and Serve,
// which runs one as an external binary that kest starts (ADR-0010).
//
// A plugin is named in kest's config, and PHP workers call its methods
// through kest's RPC channel by that name: kv.Set calls the method Set of the
// plugin named kv. kest's built-in plugins implement the same interface, so
// a plugin can be compiled into kest or run beside it:
//
//	func main() {
//		err := plugin.Serve(func(s plugin.Setup) (plugin.Plugin, error) {
//			return &greeter{name: s.Name}, nil
//		})
//		if err != nil {
//			fmt.Fprintln(os.Stderr, err)
//			os.Exit(1)
//		}
//	}
//
// The protocol is documented in kest's docs/plugins.md, for plugins in
// other languages.
//
// This package is public before kest 1.0: it may change, and the changelog
// says how.
package plugin

import (
	"context"
	"errors"
	"fmt"
)

// Codec is how a call's body and its answer are encoded. The values are the
// codec bits of goridge frames, as PHP's goridge sends them.
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
// A plugin may also implement Start(ctx) error and Stop(ctx) error: Start
// runs before the first call, Stop after the last.
type Plugin interface {
	Call(ctx context.Context, c Call) ([]byte, error)
}

// ErrUnknownMethod is what Call returns, possibly wrapped, for a method the
// plugin doesn't have: the caller gets net/rpc's message, which PHP
// libraries look for.
var ErrUnknownMethod = errors.New("unknown method")
