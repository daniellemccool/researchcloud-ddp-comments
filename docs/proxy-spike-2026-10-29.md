# proxy-spike report: Go intercepting proxy vs mitmproxy for the ddp-comments "safety copy"

Date: 2026-10-29. Host: `checkjefeedzee.onlinehate.src.surf-hosted.nl`. All work under `/home/dmccool/proxy-spike/`; containers `spike-proxy`, `spike-firefox`; network `spike-net`. Nothing outside `spike-*` was created or modified.

## Verdict

A ~310-line Go program on `github.com/elazarl/goproxy` does the whole safety-copy job: TLS interception with a self-generated CA that Firefox accepts via enterprise policy, full-body NDJSON capture with gzip/deflate/brotli decoding, a deny-rule list, per-request stderr logging, and a fully functional browser (websocket 101 observed, 5.2 MB extension download passed through, no TLS errors). goproxy was usable as-is; no fallback to martian or a hand-rolled proxy was needed.

## Library and why

- `github.com/elazarl/goproxy v1.9.2` — mature MITM proxy library. Custom CA plugs in via the package var `goproxy.GoproxyCa` (the `AlwaysMitm` handler's `TLSConfigFromCA(&GoproxyCa)` closure dereferences it at connect time, so assigning our CA before serving is sufficient). `proxy.OnRequest().HandleConnect(goproxy.AlwaysMitm)` MITMs every CONNECT; `OnRequest().DoFunc` / `OnResponse().DoFunc` give request/response hooks; `ctx.UserData` carries the matched rule from request to response handler. goproxy also has built-in websocket upgrade tunneling (`websocket.go`), which we observed working (101).
- `github.com/andybalholm/brotli v1.2.7` — brotli decoding (stdlib has none).
- One deliberate config change: `proxy.Tr.DisableCompression = true`. Without it, Go's transport auto-decompresses when the *client* didn't send `Accept-Encoding`, which would hide the real `Content-Encoding`. With it, upstream bodies arrive raw, the browser's own `Accept-Encoding` is forwarded untouched, and we decode ourselves. The response delivered to the browser is byte-identical (verified with `cmp`).

## Size

| metric | value |
|---|---|
| Go source lines (`wc -l proxy/*.go`) | 313 (`main.go` 241, `ca.go` 72) |
| static binary (`-ldflags "-s -w"`, CGO_ENABLED=0) | 8,007,842 bytes (~7.6 MiB) |
| runtime image `spike-proxy:latest` (alpine:3.21 + ca-certificates) | 25 MB |
| dependencies | goproxy v1.9.2, brotli v1.2.7 (+ indirect x/net, x/text, coder/websocket); `go.sum` kept in tree |

## Verification steps (all commands run as `dmccool` with `sudo docker`)

1. **Build.** `cd /home/dmccool/proxy-spike && sudo docker compose build` → `proxy Built`; `sudo docker images spike-proxy:latest` → `25MB`. Compile check before that: `go vet` + `go build` inside `golang:1.26-alpine` → `BUILD_OK`.
2. **Proxy alone, CA generation.** `sudo docker compose up -d proxy`; after ~1 s `ca/ca.pem` (1200 B) and `ca/ca-key.pem` (1675 B, 0600) appeared; log: `loaded 3 capture rules, 1 deny rules` / `CA ready (/ca/ca.pem)` / `spike-proxy listening on :8080`.
3. **curl through the proxy** (`curl --proxy http://127.0.0.1:8099 --cacert ca/ca.pem ...`, port published as `127.0.0.1:8099:8080`):
   - `https://httpbin.org/gzip` → `HTTP/1.1 200 OK`, `Content-Encoding: gzip`, body still gzip on the wire (proxy did not alter it). Capture line: `ce='gzip' body_len=196`, decoded body is valid JSON.
   - `https://httpbin.org/brotli` → 200, `Content-Encoding: br`. Capture line: `ce='br' body_len=195`, decoded body valid JSON.
   - `https://httpbin.org/bytes/2000000` → **400 from httpbin itself** (`invalid byte count: 2000000 not in range [1, 1048576]`; identical 400 without the proxy). httpbin/httpbingo cap all body endpoints at 1 MiB, so the >1 MB proof used a one-off `spike-bigfile` container (python http.server on `spike-net`, alias `bigfile`, 2,000,000 random bytes) plus a `bigfile-verify` capture rule: `curl --proxy http://127.0.0.1:8099 http://bigfile/big.bin` → `curl_http=200 size=2000000`, `cmp` against source → `DOWNLOAD IDENTICAL TO SOURCE`, capture line `body_len=2000000`. Helper container removed afterwards.
   - `https://example.org/` → `HTTP/1.1 403 Forbidden`, body `spike-proxy: denied by rule` (deny rule works).
   - `https://www.tiktok.com/` → `curl_http=200 size=421620` HTML, **no capture line** (rule is `^/api/comment/list` only).
   - Capture file after all of the above: 5 NDJSON lines, each with `t, method, host, path, query, status, content_type, content_encoding, body_len, body`; flushed per line (`Sync()` after each write).
4. **Firefox.** `sudo docker compose up -d firefox` (started only after `ca/ca.pem` existed). `docker logs spike-firefox` → all cont-init scripts done, `Mozilla Firefox 151.0.3` running, nginx web UI on 5800. Policy mounted at `/usr/lib/firefox/distribution/policies.json` (proxy `proxy:8080` locked, CA `/ca/ca.pem`, Zeeschuimer `ExtensionSettings` copied from the desk). After restart with readable policy: `spike-proxy-ca` present in `/config/profile/cert9.db`, Zeeschuimer xpi present in `/config/profile/extensions/`. Proxy stderr log shows browser traffic flowing through it with **no TLS errors**:
   - `GET detectportal.firefox.com/canonical.html status=200`
   - `GET push.services.mozilla.com/ status=101` (websocket upgrade passed through)
   - `GET github.com/.../zeeschuimer-1.14.2.xpi status=302` then `GET release-assets.githubusercontent.com/... status=200 bytes=5192395` (5.2 MB through the proxy)
   - `GET services.addons.mozilla.org/api/v4/addons/search/?guid={677bb78c-...} status=200`
   - `captures/captures.ndjson` still had exactly 5 lines — browser traffic correctly not captured.

## What did not work, and what I did

- **httpbin `/bytes/2000000` and `/stream-bytes/2000000` return 400** — httpbin (and httpbingo) cap body endpoints at 1 MiB; the 400 comes from httpbin, not the proxy (verified direct). Proved >1 MB capture with the local `spike-bigfile` helper instead. httpbingo also lacks `/brotli` (501), so httpbin.org stayed the verification host.
- **Firefox initially ignored the policy entirely** (no CA in cert9.db, no extension, direct 443 connections to Google, zero proxy log lines). Cause: files created by the agent tooling were mode `0600` owned by uid 5050 (= dmccool), and Firefox runs as uid 1000 (`app`), so it could not read `/usr/lib/firefox/distribution/policies.json`; Firefox fails silently on unreadable policies. Fixed with `chmod 644` on the spike files and a `docker compose restart firefox`. After that everything applied. Lesson for the desk: policy files must be world-readable.
- **Log line `bytes=-1`** for chunked responses (e.g. `www.tiktok.com/`): non-matching responses are deliberately not buffered, so only `Content-Length` is logged; `-1` means chunked. Matching responses log the true buffered length.

## Gaps vs a production capture proxy (mitmproxy 12.2.3 + desk addon)

- **HTTP/2 to the browser**: goproxy's MITM TLS config (`defaultTLSConfig`) sets no `NextProtos`, so the browser negotiates HTTP/1.1 with the proxy (observed: all proxied requests are HTTP/1.1). Upstream `proxy.Tr` also doesn't force h2. Functionally fine for Firefox, but it changes connection multiplexing vs the desk's mitmproxy (which does h2 to the browser). Fixable by setting `NextProtos: ["h2","http/1.1"]` on the MITM TLS config and `ForceAttemptHTTP2` on `Tr`.
- **Websockets**: work (101 observed) via goproxy's upgrade tunneling, but frames are not inspected — same as the desk's mitmproxy config (`rawtcp=false` only affects raw TCP).
- **Body size limits**: none. Matching responses are fully buffered in memory (`io.ReadAll`) — a hostile/huge matching response could OOM the proxy. mitmproxy streams with `body_size_limit=32m` / `stream_large_bodies=1m`. A production Go version needs a per-rule `max_body_bytes` cap and streaming NDJSON writes for oversized bodies.
- **Per-rule body caps / rule metadata**: the desk's rules carry `query_keep`, `response_keep`, `fields`, `post_id.from=query param=aweme_id`, `no_stream`. This spike captures the raw decoded body only; post-id extraction from the query string and field filtering are not implemented (easy: parse `req.URL.Query().Get("aweme_id")` at capture time).
- **Performance**: one goroutine per connection, no pooling issues observed at browser scale; the capture path does an extra full-body copy (raw + decoded). Fine for one browser; untested at many-concurrent-collectors scale.
- **Robustness**: no cert-store caching beyond goproxy's default, no OCSP stapling concerns, no graceful reload of `/rules.json` (read once at startup), no metrics. Deny rules only match host suffix (no path regex) — sufficient for the 4CAT boundary mirror.
- **Logging**: one stderr line per request with method/host/URI/status/bytes as required; `bytes` is `Content-Length` (or `-1` for chunked) for non-matching responses.

## Files created (all under `/home/dmccool/proxy-spike/`)

```
├── REPORT.md                  this report
├── Dockerfile                 multi-stage golang:1.26-alpine → alpine:3.21
├── compose.yml                services spike-proxy (build ., 127.0.0.1:8099:8080) + spike-firefox, network spike-net
├── rules.json                 tiktok-comments rule (from /opt/ddp-comments/platforms/tiktok/platform.json captures: ^/api/comment/list on tiktok.com),
│                              httpbin-verify rule, bigfile-verify rule, deny example.org:403
├── policy/policies.json       desk policy shape: Proxy locked to proxy:8080, Certificates.Install /ca/ca.pem, Zeeschuimer ExtensionSettings as-is
├── proxy/go.mod               module spikeproxy, go 1.24, goproxy v1.9.2 + brotli v1.2.7
├── proxy/go.sum               recorded module versions
├── proxy/main.go              rules loading, deny rules, capture matching, NDJSON capture, request logging (241 lines)
├── proxy/ca.go                CA load-or-generate (2048-bit RSA, /ca/ca.pem + /ca/ca-key.pem) (72 lines)
├── ca/                        generated at runtime: ca.pem, ca-key.pem
├── captures/captures.ndjson   5 verification capture lines
├── firefox-config/            fresh Firefox /config
└── bigfile-srv/big.bin        2 MB test payload (helper container spike-bigfile removed after verification)
```

Containers `spike-proxy` and `spike-firefox` are left RUNNING on network `spike-net`.
