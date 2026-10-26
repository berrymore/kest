// Package plugin holds the registry category of plugin types: named
// components of the config that PHP workers call through RPC, by name ("kv"
// for kv.Set) (ADR-0010). The interface is the SDK's, pkg/plugin, which
// built-in and external plugins share.
package plugin

import (
	"github.com/berrymore/kest/internal/registry"
	sdk "github.com/berrymore/kest/pkg/plugin"
)

// Plugin is sdk.Plugin.
type Plugin = sdk.Plugin

// Call is sdk.Call.
type Call = sdk.Call

// Codec is sdk.Codec.
type Codec = sdk.Codec

// Codecs.
const (
	CodecRaw     = sdk.CodecRaw
	CodecJSON    = sdk.CodecJSON
	CodecMsgpack = sdk.CodecMsgpack
	CodecGob     = sdk.CodecGob
	CodecProto   = sdk.CodecProto
)

// ErrUnknownMethod is sdk.ErrUnknownMethod.
var ErrUnknownMethod = sdk.ErrUnknownMethod

// Prober is implemented by plugins that can be unable to serve; /ready fails
// while one of them can't.
type Prober interface {
	Serving() bool
}

// Types is the registry category of plugin types.
var Types = registry.NewCategory[Plugin]("plugin")
