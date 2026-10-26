package kv

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"os"
	"reflect"
	"strings"
	"testing"
)

// golden returns the messages of testdata/kv_v1.txt by name.
func golden(t *testing.T) map[string][]byte {
	t.Helper()
	f, err := os.Open("testdata/kv_v1.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	out := map[string][]byte{}
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		name, data, _ := strings.Cut(sc.Text(), " ")
		if strings.HasPrefix(name, "#") {
			continue
		}
		if data == "-" {
			data = ""
		}
		b, err := hex.DecodeString(data)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = b
	}
	return out
}

func TestDecodeRequestGolden(t *testing.T) {
	got, err := decodeRequest(golden(t)["request"])
	if err != nil {
		t.Fatal(err)
	}
	want := &request{storage: "local", items: []entry{
		{key: "a", value: []byte("\x00v"), timeout: "2026-10-27T10:00:00+00:00"},
		{key: "b"},
		{key: "long", value: bytes.Repeat([]byte("x"), 300)},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decodeRequest = %+v, want %+v", got, want)
	}
}

func TestEncodeResponseGolden(t *testing.T) {
	g := golden(t)
	got := encodeResponse([]entry{
		{key: "a", value: []byte("v")},
		{key: "b", timeout: "2026-10-27T10:00:00+00:00"},
		{key: "c"},
		{key: "long", value: bytes.Repeat([]byte("y"), 200)},
	})
	if !bytes.Equal(got, g["response"]) {
		t.Errorf("encodeResponse\n got %x\nwant %x", got, g["response"])
	}
	if got := encodeResponse(nil); len(got) != 0 || len(g["empty"]) != 0 {
		t.Errorf("an empty response is %x, PHP's %x", got, g["empty"])
	}
}

// Fields protobuf allows and kv.v1 doesn't have are skipped.
func TestDecodeRequestSkipsUnknownFields(t *testing.T) {
	var b []byte
	b = binary.AppendUvarint(b, 7<<3|wireVarint)
	b = binary.AppendUvarint(b, 300)
	b = binary.AppendUvarint(b, 8<<3|wireI64)
	b = append(b, make([]byte, 8)...)
	b = binary.AppendUvarint(b, 9<<3|wireI32)
	b = append(b, make([]byte, 4)...)
	b = appendString(b, 1, "s")
	got, err := decodeRequest(b)
	if err != nil || got.storage != "s" {
		t.Errorf("decodeRequest = %+v, %v", got, err)
	}
}

func TestDecodeRequestErrors(t *testing.T) {
	for name, b := range map[string][]byte{
		"truncated tag":    {0x80},
		"truncated length": {0x0a, 0x05, 'a'},
		"field zero":       {0x02, 0x00},
		"group":            {0x0b},
		"bad item":         {0x12, 0x02, 0x0a, 0x05},
	} {
		if _, err := decodeRequest(b); err == nil {
			t.Errorf("%s: decodeRequest(%x) = nil error", name, b)
		}
	}
}

func FuzzDecodeRequest(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x0a, 0x01, 'a', 0x12, 0x03, 0x0a, 0x01, 'k'})
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := decodeRequest(b)
		if err != nil {
			return
		}
		// What decodes encodes back to what decodes the same.
		again, err := decodeRequest(encodeRequest(r))
		if err != nil || again.storage != r.storage || len(again.items) != len(r.items) {
			t.Fatalf("round trip of %+v: %+v, %v", r, again, err)
		}
		for i := range r.items {
			if again.items[i].key != r.items[i].key || !bytes.Equal(again.items[i].value, r.items[i].value) ||
				again.items[i].timeout != r.items[i].timeout {
				t.Fatalf("item %d: %+v, want %+v", i, again.items[i], r.items[i])
			}
		}
	})
}

// encodeRequest encodes a kv.v1.Request, for tests.
func encodeRequest(r *request) []byte {
	b := appendString(nil, 1, r.storage)
	for _, e := range r.items {
		b = binary.AppendUvarint(b, 2<<3|wireLen)
		b = binary.AppendUvarint(b, uint64(itemSize(e)))
		b = appendString(b, 1, e.key)
		b = appendString(b, 2, string(e.value))
		b = appendString(b, 3, e.timeout)
	}
	return b
}
