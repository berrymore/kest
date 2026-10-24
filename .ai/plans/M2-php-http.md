# Plan: M2 — PHP HTTP (MVP)

- **Status:** Active (2026-10-25). The user delegated M2 end to end ("research, plan, code, commit to the branch,
  open the PR"), so the decisions below were taken by the agent; the user reviews them in the PR.
- **Milestone:** M2
- **Backlog:** K-040…K-048

## Goal

A `php_workers` cluster serves HTTP requests through a pool of unmodified `roadrunner/http` PSR-7 workers:
`curl localhost:8080/` reaches PHP and comes back, with headers, cookies, query, forms, uploads and errors mapped the
way RoadRunner maps them. `examples/php-hello` runs, a Symfony app boots and passes a smoke test, a load test runs
clean, and the PHP tests pass on PHP 8.3, 8.4 and 8.5 in Docker.

## Design notes

Protocol facts: `research/roadrunner.md` § "HTTP over the worker protocol" (verified against RR v3.0.1's http
plugin v6.0.0 and `roadrunner/http` 4.2.1, and by experiment). Layering: `cluster/phpworkers` → `rrhttp` → `worker`
→ `relay`.

### Packages

- `internal/rrhttp` — the RoadRunner HTTP payload, no I/O beyond reading the request body and writing the response:
  `EncodeRequest` (an `*http.Request` → `worker.Payload` plus a cleanup for upload temp files), `DecodeResponse` /
  `WriteResponse` (payload → status, headers, body), the JSON and protobuf codecs, the PHP-style form tree, uploads,
  and the error → HTTP status mapping.
- `internal/cluster/phpworkers` — the `php_workers` cluster type: YAML config, defaults, validation, environment,
  mapping to `worker.Config`, `Start`/`Stop`, `ServeHTTP` gluing `rrhttp` and the pool. Registered from
  `cmd/kest/extensions.go`.

### Decisions (taken by the agent, see Status)

- **D1. Codec: both, JSON by default.** `codec: json | proto`. The PHP library detects the codec per process and
  answers in kind, so both work with `roadrunner/http` 3.5+ and only JSON works with 2.x–3.4. Without `ext-protobuf`
  the PHP side spends 141 µs per request decoding protobuf and 28.5 µs decoding JSON (measured). `proto` is what RR
  v3 sends, kept for when the PHP side drops JSON (`roadrunner/worker` says it will in v4). Responses are decoded by
  their frame flag, whatever was sent. The protobuf code is hand-written (four small messages): no new dependency;
  golden bytes come from the official `api-go` messages, and the PHP integration tests decode it with the
  independent `google/protobuf` PHP library.
- **D2. Config schema** (maps onto `worker.Config`; nested like the draft in `architecture.md` § 3):

  ```yaml
  - name: app
    type: php_workers
    config:
      command: [php, worker.php]  # required
      dir: ""                     # working directory; relative to the config file; default: the config file's directory
      env: {APP_ENV: prod}        # added to the environment
      inherit_env: true           # pass kest's own environment to workers
      codec: json                 # json | proto
      max_request_body: 16MB      # 413 above it (RR: 1000 MB)
      max_response_size: 64MB     # a bigger response frame kills the worker, 502
      raw_body: false             # send form and multipart bodies raw instead of parsed
      uploads:
        dir: ""                   # default: the OS temp dir
        forbid: [".php"]          # extensions (lower case, with the dot); default: none, as in RR
        allow: []                 # if set, only these
      pool:
        workers: 4                # default: number of CPUs
        max_jobs: 0
        max_queue_size: 1000
        allocate_timeout: 60s
        start_timeout: 60s        # spawn + handshake, i.e. the application's boot
        destroy_timeout: 10s
      supervisor:
        ttl: 0s
        idle_ttl: 0s
        exec_ttl: 60s
        max_worker_memory: 0      # a size, e.g. 256MB; 0 = off
        watch_tick: 5s
  ```

  No `relay` field until there is a second relay (K-029).
- **D3. Relative paths** (`dir`, `uploads.dir`) resolve against the config file's directory, so
  `kest serve -c examples/php-hello/kest.yaml` works from any directory. `registry.BuildContext` gets the config
  directory.
- **D4. Worker environment:** kest's own environment (unless `inherit_env: false`), then `env`, then `RR_MODE=http`
  and `RR_RELAY=pipes`, which always win. Inheriting is what RR does and what containerized PHP apps expect (their
  settings come from the container's environment). No `RR_VERSION` (kest is not RR, and nothing reads it) and no
  `RR_RPC` (no RPC yet, Q-G).
- **D5. Errors → HTTP status:** `ErrQueueFull`, `ErrNoWorker`, `ErrStopped` → 503; `ErrExecTimeout` and the route
  timeout → 504; `*ProcessError` (crash, protocol error, oversize or streamed response), `ErrStopRequested` twice and
  an undecodable response → 502; `*JobError` (`$worker->error()`) → 500; body over `max_request_body` → 413;
  unreadable body or malformed form → 400; client gone → nothing written. Bodies are short plain text; details go
  to the log only. RR answers 500 to all of these.
- **D6. Request mapping deviations from RR** (all in RR's favour of data loss or randomness): form keys are mounted
  in order of appearance (RR: random map order, so conflicts resolve randomly), and `x-www-form-urlencoded` bodies
  are parsed for every method that has a body (RR: POST, PUT and PATCH only; others lose the body). A parsed request
  with no fields sends `{}` as the body (needed by the JSON codec; PHP does the same for protobuf).
- **D7. Response hygiene:** kest frames the body itself: it sets `Content-Length` to the body length and drops the
  worker's `Content-Length` and `Transfer-Encoding` (except for HEAD, where the worker's `Content-Length` is kept if
  valid); no body for 204, 304 and HEAD. Header names that aren't HTTP tokens and values with control characters are
  dropped (logged at debug). `Trailer` works as in RR. A final status outside 200–599 → 502 (RR writes 1xx as an
  informational response, which only makes sense in stream mode). No HTTP/2 server push.
- **D8. Out of scope:** streaming responses (a `STREAM` frame is a protocol error, the worker is replaced, 502;
  `PSR7Worker` doesn't stream unless `$chunkSize` is set), request attributes (no filter sets any yet), trusted
  proxies / `ipAddress`, RPC.

### Request flow in `ServeHTTP`

1. `http.MaxBytesReader(w, r.Body, max_request_body)`.
2. `rrhttp.EncodeRequest(r, opts)` → payload + cleanup (temp files of uploads and of `ParseMultipartForm`).
3. `pool.Exec(r.Context(), payload)`; the route timeout is the context deadline.
4. `rrhttp.WriteResponse(w, r, resp)`, or `rrhttp.WriteError(w, err)`.
5. Cleanup. If the caller gave up while the worker still runs the job, the temp files go away under it; the
   response is dropped anyway.

### Test strategy

- `rrhttp`: table tests for every request field and body kind, both codecs; golden protobuf bytes produced by
  `google.golang.org/protobuf` with `Deterministic: true` from `api-go/v6` messages (scratch program, recorded in
  `testdata/`); the form tree checked against RR's own `parse.go` with a differential fuzz run in a scratch module
  (not committed: it needs RR's code); fuzz targets for the response decoders and the form key parser; benchmarks
  for request encoding.
- `phpworkers`: config tests with exact locations (`kest.yaml:12:5: clusters[0].config.pool.workers: …`), env
  assembly, `ServeHTTP` against a fake executor (every row of D5, route timeout, client gone).
- PHP integration (`-tags integration`, `internal/cluster/phpworkers/testdata/php`, unmodified `roadrunner/http`
  `PSR7Worker` + `nyholm/psr7` echo app, `composer.lock` committed): both codecs, every request field, forms,
  uploads, response headers and statuses, errors, crashes, `exec_ttl`.
- e2e (`-tags e2e,integration`): the real binary serving `examples/php-hello`, and the Symfony smoke test (K-046);
  `make test-integration` runs composer for every fixture first.
- Load: `oha` or `wrk` against `examples/php-hello` (manual, recorded in the log).

## Steps

1. [x] **Research** (K-040) — `research/roadrunner.md` § "HTTP over the worker protocol" ✅ with pinned links and
   an experiment (both codecs against a real `PSR7Worker`, timings).
2. [ ] **Request encoding** (K-042) — `rrhttp`: context (JSON + protobuf), cookies, URI, remote address, body kinds,
   form tree for urlencoded and multipart values. Acceptance: table tests, golden bytes, differential fuzz vs RR's
   tree clean, fuzz targets clean.
3. [ ] **Response decoding and errors** (K-044) — `rrhttp`: both codecs, header hygiene, trailers, body framing,
   error → status. Acceptance: tests for each rule in D5 and D7; decoder fuzz clean.
4. [ ] **`php_workers` cluster** (K-041) — config, defaults, validation, env, `BuildContext` config dir,
   `ServeHTTP`, registration. Acceptance: config and handler tests; PHP integration tests green on both codecs.
5. [ ] **`examples/php-hello`** (K-045) — composer project, PSR-7 worker, `kest.yaml`, README; e2e test with the
   real binary. Acceptance: `curl` works; e2e green. **The vertical slice is done here.**
6. [ ] **Uploads** (K-043) — temp files, `uploads.dir`/`forbid`/`allow`, error codes, cleanup (also on timeouts and
   errors). Acceptance: PHP integration test uploads files and reads them back; no temp file is left.
7. [ ] **Framework smoke test** (K-046) — Symfony skeleton with `runtime/roadrunner-symfony-nyholm`, e2e test.
8. [ ] **Docker PHP matrix** (K-047) — `make test-matrix`: PHP 8.3 / 8.4 / 8.5 images with composer, running the
   integration and PHP e2e tests on Linux.
9. [ ] **Docs** (K-048) — `docs/getting-started.md`; `docs/config.md` for `php_workers`; README; architecture,
   status, backlog, roadmap.
10. [ ] **Load test and PR** — `oha`/`wrk` run without errors or leaks; `make check` + `make test-integration` +
    matrix green; push, `gh pr create`.

Commits: one per backlog item on `feat/m2-php-http`, plus `docs:` commits; the user allowed the agent to commit and
open the PR without asking (2026-10-25).

## Risks / open questions

- The JSON codec may disappear from a future `roadrunner/http` (v5?). Mitigation: `codec: proto`, tested as well.
- Symfony's RoadRunner runtime requires `spiral/roadrunner-http ^2.0 || ^3.0`, i.e. 3.6 at most: fine with JSON,
  and 3.6 also speaks protobuf.
- `max_request_body` 16 MB is far below RR's 1000 MB: uploads of big files need an explicit setting. Documented.

## Log

- 2026-10-25 — K-040 done: RR v3 sends protobuf; the PHP library accepts JSON and protobuf and answers in kind;
  protobuf costs 5× more on the PHP side without `ext-protobuf`. Plan written; decisions D1–D8 taken by the agent.
