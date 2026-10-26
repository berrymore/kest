# Plugins

A plugin is a named component of the config that PHP workers call through [RPC](config.md#rpc): `kv.Set` calls the
method `Set` of the plugin named `kv`. kest has one built in, [`kv`](config.md#kv); any other can be an **external
plugin**, a program that kest starts next to itself and talks to over a socket, so it can be written without
rebuilding kest. To PHP, a built-in plugin and an external one are the same.

This page shows how to write an external plugin in Go with kest's SDK, what kest does with it, and the protocol, for
plugins in other languages. The options are in the [configuration reference](config.md#external).

## Writing one in Go

The SDK is the package `github.com/berrymore/kest/pkg/plugin`. A plugin implements one method, `Call`, and `main`
hands it to `plugin.Serve`:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/berrymore/kest/pkg/plugin"
)

type greeter struct{ greeting string }

func (g *greeter) Call(_ context.Context, c plugin.Call) ([]byte, error) {
	if c.Method != "Hello" {
		return nil, plugin.ErrUnknownMethod
	}
	if c.Codec != plugin.CodecJSON {
		return nil, fmt.Errorf("Hello takes JSON, not %s", c.Codec)
	}
	var name string
	if err := json.Unmarshal(c.Body, &name); err != nil {
		return nil, err
	}
	return json.Marshal(g.greeting + ", " + name + "!")
}

func main() {
	err := plugin.Serve(func(s plugin.Setup) (plugin.Plugin, error) {
		var cfg struct{ Greeting string }
		if err := json.Unmarshal(s.Config, &cfg); err != nil {
			return nil, fmt.Errorf("plugin_config: %w", err)
		}
		return &greeter{greeting: cfg.Greeting}, nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

```yaml
plugins:
  - name: greeter
    type: external
    config:
      command: [./bin/greeter]
      plugin_config: { greeting: Hello }
rpc:
```

```php
use Spiral\Goridge\RPC\RPC;
use Spiral\RoadRunner\Environment;

$rpc = RPC::create(Environment::fromGlobals()->getRPCAddress());
echo $rpc->call('greeter.Hello', 'kest');   // Hello, kest!
```

- **`Call`** gets the method (what follows the plugin's name), the codec the caller used, and the body. It answers in
  the same codec. PHP's goridge sends JSON by default; `withCodec()` switches to raw bytes or protobuf, as
  `roadrunner/kv` does. Calls run concurrently, and `ctx` ends when the caller gives up (`rpc.call_timeout`, kest
  stopping): return then.
- **Errors** go back to PHP as their message, in a `ServiceException`. Return `plugin.ErrUnknownMethod` (or wrap it)
  for a method the plugin doesn't have: PHP gets `net/rpc`'s message, `rpc: can't find method greeter.Nope`, as from
  RoadRunner. A panic fails its call and the plugin goes on; the stack goes to stderr.
- **Setup** carries the plugin's name and its `plugin_config` as JSON. An error from the function given to `Serve`
  fails the start, and kest shows its message.
- **Start and stop.** If the plugin also has `Start(ctx) error`, it runs before the first call; `Stop(ctx) error`
  runs after the last.
- **Logs:** write to stderr or stdout. kest logs both, line by line, as `plugin stderr` and `plugin stdout`, with
  the plugin's name and pid. The protocol doesn't use either, so a stray print breaks nothing.
- Run by hand, `Serve` returns `plugin.ErrNotUnderKest`.

The SDK is public before kest 1.0: it may still change.

## What kest does with it

- **Start.** kest starts the command (relative to `dir`, the config file's directory by default) with its own
  environment unless `inherit_env: false`, then `env`, then the protocol's variables. The plugin has `start_timeout`
  to answer the handshake, its own setup included; a plugin that fails it fails kest's start.
- **Process.** The plugin runs in a process group of its own, so the terminal's signals don't reach it, and on Linux it
  dies with kest even if kest is killed. On macOS, it sees its socket close.
- **Calls.** At most `max_concurrent_calls` at once; more wait for a place within their own timeout. All of them
  share one socket and are answered in any order.
- **When it dies,** its calls in progress fail, calls fail at once while it is down, and `/ready` answers `503`
  (`not serving: plugin greeter`). kest restarts it at once, or after a pause that doubles from 100 ms to 30 s while it
  keeps dying within ten seconds of its start. `kest_plugin_up` and `kest_plugin_restarts_total` follow it.
- **Stop.** Plugins stop after the workers and the RPC server, so workers can call them until they exit. kest asks the
  plugin to stop; it answers the calls in progress, runs `Stop` and exits. One still running after `stop_timeout` is
  killed with its process group.
- **Trust.** kest runs the command the config names, with kest's user: there is no catalog of plugins and no
  checksum.

## The protocol

For plugins in other languages. Version 1; `internal/plugin/wire` and `pkg/plugin` are the reference.

### Transport

kest makes a pair of connected unix stream sockets and gives the plugin one end as **file descriptor 3**. The plugin's
environment has `KEST_PLUGIN_FD=3` and `KEST_PLUGIN_PROTOCOL=1`, the newest version kest speaks. There is no socket
file, and nothing else can connect. When kest's end closes, the plugin should exit.

### Frames

Every message is a goridge frame, the framing of RoadRunner's workers and RPC:

| Bytes | |
|---|---|
| 0 | version `1` in the high 4 bits; the header's length in 32-bit words, `3 +` the number of options, in the low 4 |
| 1 | flags: `0x01` CONTROL, `0x40` ERROR, and one codec: `0x04` raw, `0x08` JSON, `0x10` msgpack, `0x20` gob, `0x80` protobuf |
| 2–5 | the payload's length, unsigned, little-endian |
| 6–9 | CRC-32 (IEEE) of bytes 0–5, little-endian |
| 10 | stream flags: `0` here |
| 11 | reserved: `0` |
| 12… | the options, unsigned 32-bit little-endian each, at most 10; then the payload |

### Handshake

1. kest sends a CONTROL frame with a JSON payload (the flags `0x09`):

   ```json
   {"protocol": 1, "name": "greeter", "config": {"greeting": "Hello"}, "max_frame_size": 16777216}
   ```

   `config` is `plugin_config`, absent without one. `max_frame_size` bounds the payload of every frame either way.

2. The plugin answers a CONTROL frame with JSON, `{"protocol": 1}`: the version it speaks, at most kest's. Or it
   refuses with a CONTROL frame with ERROR (`0x41`) and the reason as the payload, and exits.

### Calls

A call is a frame with two options, `[id, len(method)]`, the payload `method ‖ body`, and the codec of the body in the
flags: what PHP sent, with the method after the plugin's name (`Hello` for `greeter.Hello`). The ids are kest's.

The plugin answers each call with a frame with the same id and method: options `[id, len(method)]`, the payload
`method ‖ answer`, the same codec. Calls may be answered in any order, and should run concurrently.

A failed call is answered with ERROR added to the codec (`0x48` for JSON), the message as the body, and a third option,
a code: `0` the plugin's error, `1` no such method.

### Control

kest sends CONTROL frames with JSON:

- `{"cancel": 7}`: the caller of call 7 gave up. Stop working on it; kest drops a late answer.
- `{"stop": true}`: answer the calls in progress, release what the plugin holds, and exit. No calls follow.

### Reserved

The protocol has no calls from the plugin to kest yet (a plugin that hands jobs to PHP workers would need them).
They will come with a new protocol version, negotiated in the handshake.
