// Generates the golden protobuf files in ../ with the official messages.
// A separate module, so kest itself doesn't depend on protobuf:
//
//	cd internal/rrhttp/testdata/gen && go run . ..
module github.com/berrymore/kest/internal/rrhttp/testdata/gen

go 1.27

require (
	github.com/roadrunner-server/api-go/v6 v6.0.0
	google.golang.org/protobuf v1.36.12
)
