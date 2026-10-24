// Generates golden protobuf bytes for kest's internal/rrhttp tests with the
// official messages (api-go/v6 http/v1) and Deterministic marshaling.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	httpV1 "github.com/roadrunner-server/api-go/v6/http/v1"
	"google.golang.org/protobuf/proto"
)

func hv(vs ...string) *httpV1.HeaderValue {
	v := &httpV1.HeaderValue{}
	for _, s := range vs {
		v.Value = append(v.Value, []byte(s))
	}
	return v
}

func write(dir, name string, m proto.Message) {
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		panic(err)
	}
	fmt.Printf("%s: %d bytes\n", name, len(b))
}

func main() {
	dir := os.Args[1]
	write(dir, "request_full.pb", &httpV1.Request{
		RemoteAddr: "192.0.2.10",
		Protocol:   "HTTP/1.1",
		Method:     "POST",
		Uri:        "http://example.com/path?a=1&b[]=2",
		Header: map[string]*httpV1.HeaderValue{
			"Accept":       hv("text/html"),
			"Content-Type": hv("application/x-www-form-urlencoded"),
			"X-Empty":      hv(),
			"X-Long":       hv(strings.Repeat("v", 300)),
			"X-Multi":      hv("one", "two", ""),
		},
		Cookies: map[string]*httpV1.HeaderValue{
			"session": hv("abc def"),
			"theme":   hv(""),
		},
		RawQuery:   "a=1&b[]=2",
		Parsed:     true,
		Attributes: map[string]*httpV1.HeaderValue{"route": hv("home")},
	})
	write(dir, "request_minimal.pb", &httpV1.Request{Method: "GET", Uri: "http://localhost/"})
	write(dir, "response_full.pb", &httpV1.Response{
		Status: 201,
		Headers: map[string]*httpV1.HeaderValue{
			"Content-Type": hv("application/json"),
			"Set-Cookie":   hv("a=1; Path=/", "b=2; Path=/"),
			"X-Empty":      hv(),
			"X-Long":       hv(strings.Repeat("v", 300)),
		},
	})
	write(dir, "response_negative_status.pb", &httpV1.Response{Status: -1})
}
