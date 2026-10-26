package rpc

import (
	"testing"

	"github.com/berrymore/kest/internal/relay"
)

func FuzzParseRequest(f *testing.F) {
	f.Add(uint8(relay.CodecJSON), uint32(1), uint32(6), []byte(`kv.Set{"a":1}`), 2)
	f.Add(uint8(0), uint32(7), uint32(0), []byte{}, 2)
	f.Add(uint8(relay.CodecProto), uint32(1), uint32(99), []byte("kv.Set"), 2)
	f.Add(uint8(relay.CodecRaw), uint32(1), uint32(1), []byte("x"), 1)
	f.Fuzz(func(t *testing.T, flags uint8, seq, n uint32, payload []byte, options int) {
		fr := &relay.Frame{Flags: relay.Flags(flags), Payload: payload}
		for i := range options % (relay.MaxOptions + 1) {
			fr.Options = append(fr.Options, []uint32{seq, n}[min(i, 1)])
		}
		req, err := parseRequest(fr)
		if err != nil {
			return
		}
		if req.method+string(req.body) != string(payload) || len(req.method) != int(n) || req.seq != seq {
			t.Fatalf("request %+v from frame %+v", req, fr)
		}
		a := answer(req, []byte("ok"))
		if a.Options[0] != seq || int(a.Options[1]) != len(req.method) || a.Flags != relay.Flags(req.codec) {
			t.Fatalf("answer %+v to request %+v", a, req)
		}
	})
}
