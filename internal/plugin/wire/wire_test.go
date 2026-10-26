package wire

import (
	"reflect"
	"testing"

	"github.com/berrymore/kest/internal/relay"
)

func TestCallFrames(t *testing.T) {
	for _, c := range []*Call{
		{ID: 1, Method: "Set", Codec: relay.CodecProto, Body: []byte("x")},
		{ID: 2, Method: "Nope", Codec: relay.CodecJSON, Body: []byte("no such method"), Failed: true,
			Code: CodeUnknownMethod},
		{ID: 3, Method: "Fail", Codec: relay.CodecRaw, Body: []byte("boom"), Failed: true},
	} {
		f := c.Frame()
		got, err := ParseCall(f)
		if err != nil || !reflect.DeepEqual(got, c) {
			t.Errorf("ParseCall(%+v) = %+v, %v; want %+v", f, got, err, c)
		}
	}
	for name, f := range map[string]*relay.Frame{
		"one option":                 {Options: []uint32{1}},
		"three options, not failed":  {Options: []uint32{1, 0, 1}},
		"method length past payload": {Options: []uint32{1, 5}, Payload: []byte("Set")},
	} {
		if _, err := ParseCall(f); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
