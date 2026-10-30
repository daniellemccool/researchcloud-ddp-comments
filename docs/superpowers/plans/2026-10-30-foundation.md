# Foundation Implementation Plan (plan 1 of 3: foundation, proxy, deploy, launch)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Put the enforcement layers, the Go module, the capture proxy, the compose file and init script, the template tool and a minimal gated desk on the sandbox, and prove the whole provisioning path with a throwaway portal launch.

**Architecture:** One Go module with two binaries: `deskproxy` (TLS-intercepting capture proxy and 4CAT boundary, from the spike) and `desk` (the gated web service and the operator subcommands, here only its gate, access and template parts). The item is a Docker Compose component: `deploy/init.sh` runs once as root before compose up, writes `.env`, the volume layout and two static nginx files; `deploy/compose.yml` runs the desk and a bundled 4CAT with no published port but the desk's loopback listener. Every check that a tool can hold runs from `scripts/check.sh`, on the box and in CI.

**Tech Stack:** Go 1.26 (built and tested in `golang:1.26` containers; no toolchain on the box), `github.com/elazarl/goproxy` v1.9.2, `github.com/andybalholm/brotli` v1.2.7, `modernc.org/sqlite` (CGO-free), `github.com/coder/websocket` (plan 2), `golangci-lint` with `depguard`, `shellcheck`, `shfmt`, Docker Compose v2, `adg` 4.x, GitHub Actions, GHCR.

**Spec:** `docs/superpowers/specs/2026-10-29-box-first-desk-design.md` (sections cited as §). ADRs: `docs/decisions/` (pull the brief with `adg lean brief --model docs/decisions <paths>` before editing any governed file).

**Plans 2 and 3** (the full desk: provisioning, render, relay, pages, admin, operator commands; then acceptance: `tests/box/`, Tier 3 checklist, catalog item record, final launch, retirement of the Python tree and build tooling) are written after this plan lands.

## Global Constraints

- Box-first (B1): every task's verification step runs on `checkjefeedzee` (`ssh -F ~/.config/claude-ssh/config desk`, plain commands only; longer work through the Mistral box worker with `--mkdir`). The box workspace is `/home/dmccool/ddp-comments`, mirrored by `scripts/box-sync.sh`; commits happen locally after reading the diff. Go tests run on the box as `sudo docker run --rm -v /home/dmccool/ddp-comments:/src -w /src -v ddp-gomod:/go/pkg/mod golang:1.26 go test ./...`, and locally the same way with the local path.
- Go for everything we write (B4); the init script is POSIX sh, checked by `shellcheck` and `shfmt -d`. No `.py` under `cmd/`, `internal/`, `deploy/`, `platforms/` (ADR-0019). The legacy `desk/`, `bin/`, `scripts/sandbox/`, `tests/` Python tree stays untouched until plan 3.
- Module path `github.com/daniellemccool/researchcloud-ddp-comments` (D8; the remote does not exist yet, Task 9 has Danielle create it).
- Parameter values are strings, never empty, no single or double quotes (§4.6). Username `^[a-z][a-z0-9._-]{0,31}$`; role `^src_co_[A-Za-z0-9_-]+$`; roles header split on `;`, whole-segment equality (ADR-0004).
- Exactly one published Compose port, `127.0.0.1:8110:8110` (ADR-0006); every `net-<user>` is `internal: true` (ADR-0007); every service `restart: unless-stopped`.
- Files Firefox reads: mode 0644, uid 1000, on the volume (ADR-0013). Secrets: `secrets/gate`, `secrets/co_token`, `secrets/4cat-postgres` root 0600; `secrets/4cat-tokens/<user>` uid 1000 0600.
- The init script starts no container and builds no image (ADR-0017); after it, nothing of ours touches nginx (ADR-0018). `/etc/nginx/app-location-conf.d/authentication.conf` and `/etc/rsc/` are never written.
- Safety-copy envelope (§5.5, ADR-0010): `t, user, session, platform, capture, kind, post_id, request{method,host,path,query}, response{status, ...response_keep}` plus `item` or `body`; `query` holds only `query_keep` keys; default `max_body_bytes` 16 MiB per rule.
- 4CAT boundary (ADR-0001): allowed exactly `POST /api/import-dataset/` and `GET /api/check-query/` on `fourcat.desk` → `http://4cat-frontend:5000`, `Host: fourcat.desk`, client `Cookie` replaced by `remember_token=<token>`; everything else on that host 403; hosts `localhost`, IP literals, dotless, other `*.desk` 403; resolved destinations in loopback, private, link-local or Docker ranges 403 except the 4CAT forward; redirects returned, never followed.
- Commits: conventional prefix with the component as scope (`feat(proxy):`, `feat(deploy):`, `chore(check):`, …); body ends with `Plan: docs/superpowers/plans/2026-10-30-foundation.md`, then `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>` and `Claude-Session: https://claude.ai/code/session_01BLz1rZUCiANfcnpaeCdUik`. Commit by pathspec. `scripts/check.sh` passes before every commit.

## Review Focus

1. A `PLUGIN_PARAMETERS` value with a trailing newline, Windows line endings, or a role name typed with a capital letter: the init script must refuse with a line naming the parameter, never write a half layout. Test in Task 6 (`tests/sh/init_test.sh`, cases `crlf-value`, `uppercase-role`).
2. A 4CAT response that is a 302 to `/login/`: the proxy must hand the 302 to the browser untouched, not follow it, not log the token. Test in Task 4 (`boundary_test.go`, `TestRedirectReturnedNotFollowed`).
3. A capture rule response whose JSON has the `items` key but a `null` value, or a non-array: one error record, no crash, browser unaffected. Test in Task 3 (`capture_test.go`, `TestItemsNullOrNotArray`).
4. A template profile with no `cookies.sqlite` at all, and one whose `idb` directory holds two extension UUIDs: the release must fail with a message naming the condition rather than publish an empty or ambiguous artifact. Test in Task 8 (`release_test.go`, `TestNoCookiesDB`, `TestTwoExtensionStores`).
5. The roles header present but empty, and the gate header present but with a different secret: both must land on 403 and be logged once per user per change, never on a collector page. Test in Task 7 (`gate_test.go`, `TestEmptyRolesIs403`, `TestWrongSecretIs403`).

---

### Task 1: Repository foundation and the check script

**Files:**
- Create: `CLAUDE.md`, `scripts/check.sh`, `scripts/box-sync.sh`, `.golangci.yml`, `.github/workflows/check.yml`, `.editorconfig`
- Modify: `.gitignore` (add `/dist/`, `/.cache/`, `deploy/.resolved.yml`, `deploy/.env`)

**Interfaces:**
- Produces: `scripts/check.sh [layer...]` exits non-zero on any failure; layers `adr`, `shell`, `go`, `compose`, `shape`; with no argument runs all; a missing tool is a loud failure, never a skip, except `go`/`compose` layers which skip with a printed reason when `go.mod` or `deploy/compose.yml` does not exist yet. `scripts/box-sync.sh push|pull` mirrors the checkout with the box workspace (rsync, excludes `.git`, `.superpowers`, `dist`, `.cache`).

- [ ] **Step 1: Write `CLAUDE.md`** with four sections, each one paragraph: the box mirror (`/home/dmccool/ddp-comments`, `scripts/box-sync.sh`, commits local only, GitHub only from local), box-first verification (the `golang:1.26` container command from Global Constraints), the ADR rule (run `adg lean brief --model docs/decisions <paths>` before editing; `scripts/check.sh` before every commit), and the commit convention (prefix, `Plan:` anchor, attribution lines, pathspec commits).

- [ ] **Step 2: Write `scripts/box-sync.sh`**: `#!/usr/bin/env bash`, `set -euo pipefail`; `push` runs `rsync -az --delete --exclude .git --exclude .superpowers --exclude dist --exclude .cache -e "ssh -F $HOME/.config/claude-ssh/config" ./ desk:/home/dmccool/ddp-comments/`; `pull` the reverse without `--delete`; any other argument prints usage and exits 2.

- [ ] **Step 3: Write `scripts/check.sh`** (bash): layer functions `layer_adr` (`adg lean index --model docs/decisions --root . && adg lean check --model docs/decisions --root .`; `command -v adg` missing → print `adg missing: install it` and fail), `layer_shell` (`shellcheck` and `shfmt -d` over `scripts/*.sh` and `deploy/*.sh` that exist), `layer_go` (skip with reason unless `go.mod` exists; else `gofmt -l` must print nothing, `go vet ./...`, `golangci-lint run`, `go test ./...`; runs inside `golang:1.26` via `docker run` when `go` is not on PATH, with `golangci-lint` installed in that container from its install script pinned to a version), `layer_compose` (skip unless `deploy/compose.yml`; else `docker compose -f deploy/compose.yml --env-file deploy/.env.example config -q`), `layer_shape` (fail on any `*.py` under `cmd internal deploy platforms`; fail on `nginx|systemctl|/etc/nginx` under `cmd/desk internal` excluding `internal/arch` and `*_test.go`, mirroring ADR-0018's check for CI without adg). Print `== <layer>` before each, a summary line, exit 1 on any failure.

- [ ] **Step 4: Write `.golangci.yml`** (v2 schema): enable `govet`, `staticcheck`, `errcheck`, `gocritic`, `depguard`; depguard rules: `main` files under `cmd/deskproxy` may import `github.com/elazarl/goproxy`, no other package may; `github.com/docker/docker` only in `internal/docker`; `os/exec` only in `internal/docker`; `modernc.org/sqlite` only in `internal/template`.

- [ ] **Step 5: Write `.github/workflows/check.yml`**: on push and pull_request; ubuntu-latest; installs `adg` (pinned release from its GitHub), `shellcheck`, `shfmt`; sets up Go 1.26 and `golangci-lint`; runs `scripts/check.sh`.

- [ ] **Step 6: Run locally**: `scripts/check.sh` → `== adr` passes (19 ADRs, warnings only), `== shell` passes, `== go` and `== compose` print their skip reason, `== shape` passes; exit 0.

- [ ] **Step 7: Run on the box**: `scripts/box-sync.sh push`, then `ssh -F ~/.config/claude-ssh/config desk 'cd /home/dmccool/ddp-comments && scripts/check.sh shell shape'` → both pass (`adg` is not on the box; the `adr` layer is a desktop and CI layer, note this in `CLAUDE.md`).

- [ ] **Step 8: Commit** `chore(check): check script, box sync, lint config and CI` with the pathspec of the created files.

---

### Task 2: Go module, strict platform loader, identity AST rule

**Files:**
- Create: `go.mod`, `go.sum`, `internal/platform/platform.go`, `internal/platform/platform_test.go`, `internal/arch/identity_test.go`, `internal/arch/doc.go`, `platforms/tiktok/fixtures/comments.json`, `scripts/box-test.sh`

**Interfaces:**
- Produces (`internal/platform`):
```go
type CaptureRule struct {
    Name string; Path *regexp.Regexp; Items string; ID string
    PostID PostIDSpec            // From "query"|"path"; Param or Group
    QueryKeep []string; ResponseKeep []string; Fields map[string]string
    NoStream bool; MaxBodyBytes int64   // default 16 << 20
}
type Platform struct {
    Name, Label string; ZeeschuimerModules []string; UploadHint string
    ItemKinds map[string]ItemKind; DefaultKind string; Hosts []string
    OpenPattern *regexp.Regexp; Captures []CaptureRule; Login string
    KeepCookies []CookieSpec; Problems []Problem; Dir string
}
func Load(path string) (*Platform, error)      // strict: unknown key → error naming it
func (p *Platform) HostMatches(host string) bool // exact or subdomain of any Hosts entry
func (p *Platform) ItemURL(kind, id string) (string, error)
```
- `internal/arch/identity_test.go` walks every `.go` file under `cmd/` and `internal/` except `internal/access/` and `internal/arch/` with `go/parser` and fails on any call `X.Get("REMOTE_USER"|"X-Desk-Roles"|"X-Desk-Gate")` or any string literal equal to those header names.

- [ ] **Step 1: `go mod init github.com/daniellemccool/researchcloud-ddp-comments`** with `go 1.26`.

- [ ] **Step 2: Write `scripts/box-test.sh`**: runs the Global Constraints container command for `go test ./... "$@"` against the box workspace when `--box` is given (through a bare ssh line) and against the local tree otherwise.

- [ ] **Step 3: Write the failing tests** in `platform_test.go`: `TestLoadTikTok` (loads `platforms/tiktok/platform.json`; asserts one rule named `comments`, `PostID.From == "query"`, `MaxBodyBytes == 16<<20`, three `KeepCookies`), `TestUnknownKeyRefused` (a copy with `"foo": 1` → error containing `foo`), `TestHostMatches` (`www.tiktok.com` true, `tiktok.com` true, `nottiktok.com` false), `TestItemURL` (`video`, `123` → the URL; `video`, `abc` → error).

- [ ] **Step 4: Run**: `scripts/box-test.sh ./internal/platform/` → FAIL (package missing).

- [ ] **Step 5: Implement `platform.go`** with `encoding/json` `Decoder.DisallowUnknownFields()` into an intermediate raw struct, compiling regexes and applying defaults in `Load`; `Dir` is the directory of `path`.

- [ ] **Step 6: Write `internal/arch/identity_test.go`** as specified; add a doc comment in `doc.go` saying the package holds architecture tests only.

- [ ] **Step 7: Write `platforms/tiktok/fixtures/comments.json`**: a synthetic `/api/comment/list/` response with the real top-level shape (`comments` array of three objects with `cid`, `text`, `user.unique_id`, `user.nickname`, `user.uid`, `create_time`, `digg_count`, `reply_id`, `reply_comment_total`, `comment_language`; `has_more`, `cursor`, `total`, `status_code`) and invented values. No real usernames.

- [ ] **Step 8: Run**: `scripts/box-test.sh ./...` → PASS; `scripts/check.sh` → `go` layer passes (lint, vet, fmt).

- [ ] **Step 9: Commit** `feat(platform): Go module, strict platform loader and the identity AST rule`.

---

### Task 3: deskproxy capture: rules, decoding, records

**Files:**
- Create: `cmd/deskproxy/main.go`, `cmd/deskproxy/ca.go`, `cmd/deskproxy/capture.go`, `cmd/deskproxy/decode.go`, `cmd/deskproxy/capture_test.go`, `cmd/deskproxy/Dockerfile`

**Interfaces:**
- Consumes: `platform.Load`, `CaptureRule`.
- Produces: `deskproxy` flags `--platform PATH --user U --session S --captures DIR --ca DIR --listen :8080 --health :8081`; writes `DIR/captures.ndjson` (append, fsync per line); stderr one line per request `method host path status bytes`.
```go
type Record struct {
    T int64 `json:"t"`; User, Session, Platform, Capture, Kind string
    PostID string `json:"post_id"`; Request ReqInfo; Response RespInfo
    Item json.RawMessage `json:"item,omitempty"`; Body json.RawMessage `json:"body,omitempty"`
    Error string `json:"error,omitempty"`
}
func decodeBody(encoding string, r io.Reader, max int64) ([]byte, error) // gzip, deflate, br, identity; ErrTooLarge past max
func recordsFor(rule *platform.CaptureRule, req *http.Request, resp *http.Response, body []byte, meta Meta) []Record
```

- [ ] **Step 1: Write the failing tests** in `capture_test.go`: `TestDecodeGzipBrDeflate` (each encoding round-trips a 3-byte body), `TestBodyCapYieldsErrorRecord` (a rule with `MaxBodyBytes: 10` and a 11-byte body → one record with `Error == "body too large"` and no `Body`), `TestItemsRecordsFromFixture` (the Task 2 fixture → three records, `PostID` from `aweme_id`, `Request.Query` has only `aweme_id,cursor,count`, `Response` has `has_more,cursor,total,status_code`, each `Item` is the raw element and `Fields` columns are present beside it), `TestBodyRuleStoresWholeBody` (a rule without `Items` → one record whose `Body` equals the decoded bytes), `TestItemsNullOrNotArray` (`{"comments": null}` and `{"comments": 1}` → one error record each, `Error` naming the key).

- [ ] **Step 2: Run**: `scripts/box-test.sh ./cmd/deskproxy/` → FAIL.

- [ ] **Step 3: Implement `decode.go` and `capture.go`**, then `main.go`: goproxy with `AlwaysMitm`, `Tr.DisableCompression = true`, `OnRequest` matching host (`HostMatches`) and rule path, `OnResponse` buffering only matching responses (`io.LimitReader(max+1)`), delivering the original bytes to the browser; `ca.go` from the spike (load or generate into `--ca`, 0644 `ca.pem`, 0600 `ca-key.pem`). MITM TLS config gets `NextProtos: []string{"h2", "http/1.1"}` only if goproxy's handler accepts it without breaking the spike's websocket pass-through; otherwise leave h1 and record why in a code comment (Task 4 re-tests).

- [ ] **Step 4: Write `cmd/deskproxy/Dockerfile`**: multi-stage `golang:1.26-alpine` → `alpine:3.21` with `ca-certificates`, `CGO_ENABLED=0`, `-ldflags "-s -w"`, binary at `/usr/local/bin/deskproxy`, `USER 1000:1000` (captures and the CA dir are owned by uid 1000 on the volume; the init script creates them so).

- [ ] **Step 5: Run**: `scripts/box-test.sh ./cmd/deskproxy/` → PASS; `scripts/check.sh` passes.

- [ ] **Step 6: Build on the box**: `ssh -F ~/.config/claude-ssh/config desk 'cd /home/dmccool/ddp-comments && sudo docker build -f cmd/deskproxy/Dockerfile -t deskproxy:dev .'` → image built, size printed.

- [ ] **Step 7: Commit** `feat(proxy): capture proxy with decoded raw records and per-rule body cap`.

---

### Task 4: deskproxy boundary: 4CAT allowlist, token, inward refusal

**Files:**
- Create: `cmd/deskproxy/boundary.go`, `cmd/deskproxy/token.go`, `cmd/deskproxy/boundary_test.go`, `cmd/deskproxy/token_test.go`
- Modify: `cmd/deskproxy/main.go` (wire the boundary before capture)

**Interfaces:**
- Produces: flag `--token-file PATH` (default `/token`), `--fourcat-upstream http://4cat-frontend:5000`, `--fqdn <workspace fqdn>`.
```go
type verdict int // allow, forward4CAT, deny
func classify(req *http.Request, fqdn string) verdict
func inwardAddr(ip net.IP) bool      // loopback, private (RFC1918), link-local, 172.16/12 Docker, fc00::/7
type tokenFile struct{ path string; mtime time.Time; value string }
func (t *tokenFile) current() (string, error) // re-read on mtime change; error when missing
```

- [ ] **Step 1: Write the failing tests** in `boundary_test.go`: `TestAllowlistExact` (table: the two allowed method+path pairs → forward; `GET /api/import-dataset/`, `POST /api/check-query/`, `GET /`, `GET /api/check-query` (no slash) → deny), `TestInternalHostsDenied` (`localhost`, `127.0.0.1`, `10.0.0.5`, `frontend`, `other.desk` → deny; `www.tiktok.com` → allow; the fqdn → allow), `TestResolvedInwardDenied` (a custom resolver mapping `evil.example` to `172.18.0.4` → deny; to `93.184.216.34` → allow), `TestRedirectReturnedNotFollowed` (an httptest 4CAT answering 302 `/login/` → the browser gets 302 with that `Location`, upstream saw one request), `TestCookieReplaced` (upstream sees exactly `Cookie: remember_token=abc`, never the client's `Cookie: x=y`), `TestMissingTokenIs403`; in `token_test.go`: `TestTokenReloadsOnMtime`.

- [ ] **Step 2: Run** → FAIL.

- [ ] **Step 3: Implement** with a `net.Dialer` whose `DialContext` resolves the host first, applies `inwardAddr`, and dials the checked IP; the 4CAT forward uses its own transport to the upstream with `CheckRedirect` returning `http.ErrUseLastResponse`; 403 bodies are `deskproxy: denied by rule`.

- [ ] **Step 4: Run** → PASS; `scripts/check.sh` passes.

- [ ] **Step 5: Throwaway check on the box** (under `/home/dmccool/proxy-spike2/`, containers and network `spike-*`): the spike's compose with `deskproxy:dev` in place of the spike image, `--token-file` pointing at a file containing `abc`, and a stand-in for the 4CAT frontend: `nginx:alpine` named `4cat-frontend` on `spike-net`, listening on 5000 with a one-line config that returns 200 and logs the `Cookie` header. Through the proxy with curl: `POST https://fourcat.desk/api/import-dataset/` → the stand-in's 200 and its log shows `remember_token=abc` and not the client's cookie; `GET https://fourcat.desk/` → 403; `https://example.org/` → 200; `http://172.18.0.1/` and `http://localhost/` → 403. Tear down.

- [ ] **Step 6: Commit** `feat(proxy): 4CAT boundary, per-collector token injection and inward-address refusal`.

---

### Task 5: Compose file, nginx files, compose architecture test

**Files:**
- Create: `deploy/compose.yml`, `deploy/.env.example`, `deploy/nginx/desk.conf`, `deploy/nginx/fourcat.conf`, `cmd/desk/Dockerfile`, `internal/arch/compose_test.go`

**Interfaces:**
- Produces: `.env` keys the compose file interpolates: `DESK_ROOT` (absolute path on the volume), `FQDN`, `DESK_IMAGE`, `DESKPROXY_IMAGE`, `FOURCAT_IMAGE`, `POSTGRES_PASSWORD`, `GHCR_NS` (`ghcr.io/daniellemccool/researchcloud-ddp-comments`). Services: `desk` (image `${DESK_IMAGE}`, `ports: ["127.0.0.1:8110:8110"]`, volumes `${DESK_ROOT}:/desk`, `/var/run/docker.sock:/var/run/docker.sock`, `/rsc/plugins/ddp-comments:/repo:ro`, networks `desk-fourcat`, `desk-egress`), `4cat-db` (postgres:17, `${DESK_ROOT}/4cat/db:/var/lib/postgresql/data`), `4cat-memcached`, `4cat-backend`, `4cat-frontend` (image `${FOURCAT_IMAGE}`, env from 4CAT's `docker-compose_public_ip.yml` with `SERVER_NAME=${FQDN}`, `SERVER_BIND_ADDRESS` unused, no `ports:`, volumes `${DESK_ROOT}/4cat/{data,config,logs}`), networks `desk-fourcat` (bridge), `desk-egress` (bridge), `4cat-internal` (internal, db/memcached/backend/frontend). Every service `restart: unless-stopped`; 4CAT services `depends_on` with healthchecks copied from upstream.
- nginx: `desk.conf` has `location /desk/ { error_page 401 = @custom_401; auth_request /validate; auth_request_set $username $upstream_http_username; auth_request_set $src_roles $upstream_http_src_co_roles; proxy_set_header REMOTE_USER $username; proxy_set_header X-Desk-Roles $src_roles; proxy_set_header X-Desk-Gate "__GATE__"; proxy_http_version 1.1; proxy_set_header Upgrade $http_upgrade; proxy_set_header Connection $connection_upgrade; proxy_read_timeout 86400s; proxy_pass http://127.0.0.1:8110; }` and `fourcat.conf` the same for `location /` with `proxy_pass http://127.0.0.1:8110/fourcat/;` (the desk proxies to 4CAT for admins in plan 2; until then it answers 503 "not yet"). `__GATE__` is replaced by the init script.

- [ ] **Step 1: Write `internal/arch/compose_test.go`**: the test reads the **resolved** configuration from `deploy/.resolved.yml` with `gopkg.in/yaml.v3` and skips with a message when that file is absent. `scripts/check.sh`'s compose layer produces it with `docker compose -f deploy/compose.yml --env-file deploy/.env.example config > deploy/.resolved.yml`, runs `go test -run TestCompose ./internal/arch/`, and deletes it (the file is git-ignored). Assertions: exactly one `ports` entry across all services and it is `127.0.0.1:8110:8110`; no `network_mode`; every service has `restart: unless-stopped`; the `4cat-internal` network is `internal: true`; no binding on `0.0.0.0`. The same test later takes rendered per-user files (plan 2).

- [ ] **Step 2: Write `deploy/compose.yml`, `.env.example`, the two nginx files, `cmd/desk/Dockerfile`** (same shape as the proxy's; binary `desk`; plan 1's desk is Task 7, so the Dockerfile builds `./cmd/desk` which must exist: create `cmd/desk/main.go` here as `package main; func main(){}` placeholder, replaced in Task 7).

- [ ] **Step 3: Run on the box**: `scripts/box-sync.sh push`; `ssh ... 'cd /home/dmccool/ddp-comments && scripts/check.sh compose'` → resolved config valid and the arch test passes; `sudo docker compose -f deploy/compose.yml --env-file deploy/.env.example config | grep -c "127.0.0.1:8110"` → 1.

- [ ] **Step 4: Commit** `feat(deploy): compose file with bundled 4CAT, static nginx files and the compose architecture test`.

---

### Task 6: The init script

**Files:**
- Create: `deploy/init.sh`, `deploy/lib.sh`, `tests/sh/init_test.sh`, `tests/sh/fixtures/params-ok.json`, `tests/sh/fixtures/workspace.json`
- Modify: `scripts/check.sh` (shell layer runs `tests/sh/init_test.sh`)

**Interfaces:**
- Produces: `init.sh` with no argument = the platform path (reads `PLUGIN_PARAMETERS`); `init.sh update <release>`; `init.sh apply`. Environment overrides for tests: `DESK_SYSROOT` (prefix for `/etc/nginx`, `/etc/rsc`, `/data`, `/usr/local/bin`), `DESK_NO_DOCKER=1` (skip pulls), `DESK_NO_NGINX=1` (skip `nginx -t`/reload), `DESK_NO_FETCH=1` (skip the artifact download; use `DESK_TEMPLATE_FILE`), `DESK_CO_MEMBERS_FILE` (fixture instead of the CO API call).
- Produces on the volume: `desk.json` (every parameter as applied, `layout_version: 1`, `package_version`), `.env` next to the compose file, `secrets/{gate,co_token,4cat-postgres}`, `templates/release/<platform>-<version>/`, `state/`, `4cat/{db,data,config,logs}`, `firefox/`, `compose/`, `pool.csv` header, two nginx files with the gate secret substituted, `/usr/local/bin/desk` wrapper (`exec docker compose -f /rsc/plugins/ddp-comments/deploy/compose.yml exec desk desk "$@"`).

- [ ] **Step 1: Write the failing shell test** `tests/sh/init_test.sh` (bash, a `t()` helper with a temp sysroot per case, fixtures above, `PLUGIN_PARAMETERS` from the fixture, `DESK_NO_DOCKER=1 DESK_NO_NGINX=1 DESK_NO_FETCH=1`): cases `ok-fresh` (exit 0; `desk.json` has the parameters; `.env` has `DESK_ROOT`, `FQDN`; `secrets/gate` is 32 hex chars mode 0600; both nginx files contain that secret and no `__GATE__`; `/usr/local/bin/desk` exists and is executable), `placeholder-role` (`src_co_<fill-me-in>` → exit 2, output names `desk_collector_role`), `uppercase-role`, `crlf-value`, `quote-in-contact` (each exit 2 naming the parameter), `role-held-by-nobody` (members fixture without the role → exit 2), `admin-role-held-by-nobody`, `two-volumes` (two dirs under `$DESK_SYSROOT/data` → exit 2 naming both), `reattach` (a `desk.json` already present → exit 0, output contains `reattach`, `secrets/4cat-postgres` unchanged, `secrets/gate` changed), `sha-mismatch` (`template_sha256` ≠ file → exit 2, nothing written under `templates/`), `apply-mode` (`init.sh apply` with `DESK_NO_DOCKER=1` → exit 0, rewrites `.env`, prints the render command it would run).

- [ ] **Step 2: Run**: `tests/sh/init_test.sh` → every case FAIL (script missing).

- [ ] **Step 3: Implement `deploy/lib.sh`** (functions: `param KEY` reading `PLUGIN_PARAMETERS` with `jq -r`, `require_match KEY REGEX`, `refuse_placeholder KEY`, `refuse_quotes KEY`, `detect_volume`, `co_members` (curl to `co_user_api_endpoint` with the token from `workspace.json`, or the fixture), `role_held ROLE`, `gen_secret`, `write_env`, `write_desk_json`, `layout`, `nginx_files`, `fetch_template`, `pull_images`) and `deploy/init.sh` (`set -eu`, POSIX sh, dispatch on `$1`, validation first and all failures printed before exit 2). Order on the platform path: validate → volume → layout and secrets (reattach keeps `4cat-postgres`) → `.env` and `desk.json` → nginx files → `nginx -t && systemctl reload nginx` → template fetch and sha256 → image pulls → wrapper → print a summary. Never a container start.

- [ ] **Step 4: Run** `tests/sh/init_test.sh` → PASS; `shellcheck deploy/*.sh` clean; `shfmt -d deploy/*.sh` empty; `scripts/check.sh` passes.

- [ ] **Step 5: Dry-run on the box** with a scratch sysroot: `scripts/box-sync.sh push`; `ssh ... 'cd /home/dmccool/ddp-comments && DESK_SYSROOT=/home/dmccool/init-dry DESK_NO_DOCKER=1 DESK_NO_NGINX=1 DESK_NO_FETCH=1 PLUGIN_PARAMETERS="$(cat tests/sh/fixtures/params-ok.json)" sh deploy/init.sh'` → exit 0 with the summary; inspect the tree. (This is the one place a `$( )` is needed; send it through the box worker or write the parameters to a file and `PLUGIN_PARAMETERS=$(cat …)` inside a script copied by scp.)

- [ ] **Step 6: Commit** `feat(deploy): init script with validation, volume layout, secrets, nginx files and reattach mode`.

---

### Task 7: Minimal desk: gate, access, pages, container listener

**Files:**
- Create: `internal/access/identity.go`, `internal/access/gate.go`, `internal/access/roles.go`, `internal/access/gate_test.go`, `internal/access/roles_test.go`, `internal/config/config.go`, `internal/config/config_test.go`, `internal/web/server.go`, `internal/web/pages.go`, `internal/web/templates/*.html`, `internal/web/server_test.go`, `cmd/desk/main.go` (replaces the placeholder)
- Test: as listed

**Interfaces:**
- Produces (`internal/access`):
```go
type Identity struct{ User string; Roles []string; Collector, Admin bool }
type Config struct{ CollectorRole, AdminRole, GateSecret string }
func FromRequest(r *http.Request, c Config) (Identity, error)  // ErrGate, ErrNoUser, ErrBadUser; the only reader of the three headers
func IsRole(header, role string) bool                           // whole-segment equality
```
- `internal/config.Load(path string) (*Config, error)` reads `desk.json` (strict), exposes `DeskRoot`, `FQDN`, `CollectorRole`, `AdminRole`, `Contact`, `Platform`, caps, and reads `secrets/gate`.
- `internal/web.New(cfg, access.Config, platform) *http.Server` for the gated listener (`:8110`) and `NewContainer() *http.Server` for `:8111` with only `GET /healthz` → 200 `ok` and `404` elsewhere. Routes on the gated listener: `GET /desk/` → 403 page for non-members (contact in text), "not ready yet" page for collectors (plan 2 replaces it), admin page placeholder for admins; `GET /fourcat/` → 503 "not yet"; every route logs `user roles path` once per user per roles change into `state/access.ndjson`.
- `cmd/desk`: subcommands `serve --desk-root /desk` and `template …` (Task 8).

- [ ] **Step 1: Write the failing tests**: `roles_test.go` `TestIsRoleWholeSegment` (table including `src_co_students_x` vs `src_co_students` false, leading/trailing `;`, empty header false); `gate_test.go` `TestWrongSecretIs403`, `TestMissingSecretIs403`, `TestEmptyRolesIs403`, `TestBadUsernameIs403` (`J.Smith`, `../x`), `TestAdminIsCollector`; `config_test.go` `TestLoadDeskJSONStrict`; `server_test.go` `TestNonMember403HasContact`, `TestCollectorNotReadyPage`, `TestAdminPlaceholder`, `TestContainerListenerOnlyHealthz` (`/healthz` 200, `/desk/` 404, a request carrying identity headers still 404), `TestAccessLoggedOncePerChange`.

- [ ] **Step 2: Run** → FAIL.

- [ ] **Step 3: Implement** `internal/access` (reads the three headers in exactly one function), `internal/config`, `internal/web` with `html/template` pages (plain, no JavaScript yet; copy the 403 and not-ready wording from `desk/desk_page.py` and `desk_stub.py` where it names the contact), `cmd/desk/main.go` with `flag`-based subcommands.

- [ ] **Step 4: Run** → PASS; `scripts/check.sh` passes (the identity AST rule now has real files to inspect; confirm it fails if a `r.Header.Get("REMOTE_USER")` is placed in `internal/web` temporarily, then remove it).

- [ ] **Step 5: Commit** `feat(desk): gated listener with gate secret, whole-segment roles, pages and container health listener`.

---

### Task 8: Template tool and the first release artifact

**Files:**
- Create: `internal/template/release.go`, `internal/template/sanitise.go`, `internal/template/verify.go`, `internal/template/idb.go`, `internal/template/cookies.go`, `internal/template/manifest.go`, `internal/template/release_test.go`, `cmd/desk/template.go`
- Reference (read, do not run): `desk/desklib/template.py` for the allowlist, the prefs filter, the IndexedDB table names and the verification rules (R47).

**Interfaces:**
- Produces: `desk template release --profile DIR --platform NAME --platforms-dir DIR --zeeschuimer 1.14.2 --out DIR` → `DIR/ddp-comments-template-<platform>-<version>.tar` plus `.sha256`; refuses when the profile lock exists, when verification fails, when the extension store is absent or ambiguous.
```go
type Manifest struct{ Kind string /* "release" */; Platform, Zeeschuimer string; Modules []string; BuiltOn string; SHA256 string }
func Release(profile string, p *platform.Platform, zsVersion, out string) (Manifest, error)
func Sanitise(profile string, p *platform.Platform) error     // allowlist from the Python tool: prefs.js filtered, extensions.json, extension-preferences.json, extensions/*.xpi, storage/default/moz-extension+++*, cookies rows in KeepCookies, chrome/userContent.css; everything else deleted
func Verify(profile string) (Counts, error)                   // items, uploads, nav, settings from the extension's IndexedDB sqlite; ErrNoStore, ErrAmbiguousStore
```

- [ ] **Step 1: Write the failing tests** in `release_test.go` with fixture profiles built in `TestMain` using `modernc.org/sqlite` (a `cookies.sqlite` with the Firefox `moz_cookies` schema and three rows, two of them `cookie-consent`; an `idb` SQLite with `object_store` rows named `items`, `uploads`, `nav`, `settings` and `object_data` rows per case; `prefs.js` with extension and unrelated lines; `places.sqlite` as a junk file): `TestSanitiseKeepsAllowlistOnly`, `TestCookiesFiltered`, `TestVerifyCleanProfilePasses`, `TestVerifyItemsFails`, `TestVerifyUploadsFails`, `TestVerifyNavFails`, `TestVerifyNoSettingsFails`, `TestNoStoreFails`, `TestTwoExtensionStores`, `TestNoCookiesDB`, `TestLockedProfileRefused`, `TestTarAndManifest` (tar lists only allowlisted paths; manifest `Kind == "release"`; sha256 file matches).

- [ ] **Step 2: Run** → FAIL.

- [ ] **Step 3: Implement** the package and the `template` subcommand (the nav store is emptied before `VACUUM INTO` as in R47; `VACUUM INTO` via modernc works on a copy opened read-write in a temp dir).

- [ ] **Step 4: Run** → PASS; `scripts/check.sh` passes.

- [ ] **Step 5: Build the first artifact on the box** from the sandbox's existing profile: `sudo docker build -f cmd/desk/Dockerfile -t desk:dev .`; copy `/data/onlinehate_zeeschuimer/desk/state/firefox/_template/profile` to `/home/dmccool/template-work/profile` (sudo cp -a, then chown to 1000); run `sudo docker run --rm -u 1000:1000 -v /home/dmccool/template-work:/w -v /home/dmccool/ddp-comments/platforms:/platforms:ro desk:dev desk template release --profile /w/profile --platform tiktok --platforms-dir /platforms --zeeschuimer 1.14.2 --out /w/out` → prints the manifest and the sha256; `tar -tf` lists only allowlisted paths. Record the sha256 in `docs/catalog-item.md` (created here as a stub with the item and component values from §5.1 and §5.2, including `template_sha256`).

- [ ] **Step 6: Commit** `feat(template): release tool with allowlist sanitiser, store verification and manifest` and `docs(catalog): item record stub with the first template digest`.

---

### Task 9: Public repository and the release workflow

**Files:**
- Create: `.github/workflows/release.yml`, `docs/release.md`
- Modify: `deploy/.env.example` (GHCR namespace)

**Interfaces:**
- Produces: on a tag `v*`, the workflow builds and pushes `ghcr.io/daniellemccool/researchcloud-ddp-comments/desk:<tag>` and `/deskproxy:<tag>`, and mirrors `digitalmethodsinitiative/4cat:<pinned tag from deploy/.env.example>` to `/4cat:<that tag>`. The workflow builds images only; the template artifact and its `.sha256` are built on the box (Task 8) and uploaded to the GitHub release with `gh release upload` by Danielle, as `docs/release.md` describes. Packages are set public once, by hand, the first time.

- [ ] **Step 1: Danielle** creates the public repository `daniellemccool/researchcloud-ddp-comments` on GitHub and runs `git remote add origin git@github.com:daniellemccool/researchcloud-ddp-comments.git` in the main checkout; pushes the branch. (Her terminal.)

- [ ] **Step 2: Write `release.yml`** (docker/login-action to GHCR with `GITHUB_TOKEN`, docker/build-push-action for both Dockerfiles with the tag, a `skopeo copy` or `docker pull/tag/push` step for the 4CAT mirror) and `docs/release.md` (steps: tag, wait for images, build the template on the box, `gh release upload`, set `template_sha256` and image tags in the component parameters, set packages public the first time).

- [ ] **Step 3: Tag `v0.1.0-alpha.1`** and push; verify in Actions that three images exist under the namespace; `docker pull ghcr.io/…/deskproxy:v0.1.0-alpha.1` anonymously from the box succeeds after Danielle sets the packages public.

- [ ] **Step 4: Commit** `ci(release): image build, 4CAT mirror and the release runbook`.

---

### Task 10: Reshape the sandbox in place

**Files:**
- Create: `scripts/sandbox/retire-hand-install.sh` (run once, as root, on the box), `docs/sandbox-log.md` (append a new section)
- Reference: `docs/box-inventory-2026-10-29.md` §3, §4, §6 for the exact paths.

**Interfaces:**
- Consumes: everything above. Produces: a sandbox whose only state of ours is `/rsc/plugins/ddp-comments` (a root-0600 copy of the repository, as the plugin would make), the two nginx files, `/usr/local/bin/desk`, and the desk root on the volume.

- [ ] **Step 1: Write `retire-hand-install.sh`**: `systemctl disable --now desk-stub.service`; `rm /etc/systemd/system/desk-stub.service`; `docker compose -p desk down` on the old desk compose; `docker compose -f /home/dmccool/4cat/docker-compose_public_ip.yml -f /home/dmccool/4cat/docker-compose.desk.yml down -v`; remove `/opt/ddp-comments`, the three `/usr/local/bin/desk-*` symlinks, the four old nginx files, `/data/onlinehate_zeeschuimer/desk` (B12: nothing kept; the `_template` profile was already copied in Task 8), `/home/dmccool/4cat`, `/home/dmccool/spike`, `/home/dmccool/ddp-comments-old` if any, the `desk_net-*` networks; `nginx -t && systemctl reload nginx`; print what remains. Danielle runs it (`sudo bash /tmp/retire-hand-install.sh` after scp) because it is destructive; each removal is echoed.

- [ ] **Step 2: Simulate the platform**: `scripts/box-sync.sh push`; on the box as root: `mkdir -p /rsc/plugins && cp -a /home/dmccool/ddp-comments /rsc/plugins/ddp-comments && chmod -R go-rwx /rsc/plugins/ddp-comments`; write `/tmp/params.json` with the OnlineHate values (`desk_collector_role=src_co_students`, `desk_admin_role=src_co_admin`, `desk_platform=tiktok`, `desk_contact=Danielle`, `template_sha256=<Task 8>`, image tags from Task 9); run `PLUGIN_PARAMETERS=$(cat /tmp/params.json) sh /rsc/plugins/ddp-comments/deploy/init.sh` from a script copied by scp; then `docker compose -f /rsc/plugins/ddp-comments/deploy/compose.yml up -d`.

- [ ] **Step 3: Verify on the box**: `docker ps` shows `desk`, `4cat-frontend`, `4cat-backend`, `4cat-db`, `4cat-memcached` up; `docker inspect -f '{{json .NetworkSettings.Ports}}' desk` shows only `127.0.0.1:8110`; the 4CAT containers show no port bindings; `curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8110/desk/` → 403 (no gate header); `curl -H 'X-Desk-Gate: <secret>' -H 'REMOTE_USER: dmccool2' -H 'X-Desk-Roles: src_co_students' http://127.0.0.1:8110/desk/` → the not-ready page; `curl … 'X-Desk-Roles: '` → 403 with the contact; `desk --help` works through the wrapper; `curl -s http://<desk ip on desk-fourcat>:8111/healthz` → `ok`.

- [ ] **Step 4: Verify through the gate**: Danielle opens `https://checkjefeedzee.onlinehate.src.surf-hosted.nl/desk/` as `dmccool` (admin placeholder) and as `dmccool2` in a private window (not-ready page); `state/access.ndjson` on the volume shows both with their roles.

- [ ] **Step 5: Append to `docs/sandbox-log.md`** the commands, outputs and the final `docker ps`, `ls /rsc/plugins`, `ls /etc/nginx/app-location-conf.d` state.

- [ ] **Step 6: Commit** `chore(sandbox): retire the hand install and run the compose desk from the plugin path` (the script and the log).

---

### Task 11: Catalog component and the throwaway portal launch

**Files:**
- Modify: `docs/catalog-item.md` (the full component and item record: repository, path `deploy/compose.yml`, tag, `docker_initialization_command=init.sh`, the parameter declarations from §5.2 with source types, overwrites, access button, firewall), `docs/sandbox-log.md` (launch log)

- [ ] **Step 1: Danielle, in the portal** (values from `docs/catalog-item.md`): create the `ddp-comments` component in the D3I CO (script type Docker Compose, the public repo, path `deploy/compose.yml`, tag `v0.1.0-alpha.1`, parameters declared, `docker_initialization_command` initial value `init.sh`); clone the current sandbox item into a test item `ddp-comments (dev)` with components SRC-OS, SRC-CO, SRC-Nginx, SRC-External Docker Compose, ddp-comments (development version); overwrites per §5.1; make `desk_collector_role`, `desk_platform`, `desk_contact` interactive; create a small volume in OnlineHate; launch in OnlineHate with the real groups.

- [ ] **Step 2: Observe**: provisioning log green (download it); no ssh fix. Danielle opens `/desk/` as `dmccool` and `dmccool2`. Then ssh in: the Task 10 Step 3 checks, plus `docker version` shows docker-ce; `ls -la /rsc/plugins/ddp-comments` shows the root-0600 copy; `cat /rsc/plugins/ddp-comments/deploy/src_ansible.env` exists after `init.sh` (ordering confirmed).

- [ ] **Step 3: Record** everything in `docs/sandbox-log.md` ("Throwaway launch 1"), including cold provisioning wall-clock and the image-pull time; file any failure as a snag in `docs/catalog-item.md`. Delete the workspace.

- [ ] **Step 4: Commit** `docs(catalog): component and item record, throwaway launch log`.

---

## Self-review notes

Spec coverage: §5.1–5.2 (Tasks 6, 11), §5.3 listeners and compose (5, 7), §5.4 gate and roles (7; CO poll and provisioning are plan 2), §5.5 (3, 4), §5.6 templates (8), §5.7 layout and reattach (6), §6 ssh list (6 `update`/`apply`), §8 build process (1), §9.1 (every task), §9.3 early launch (11), §10 enforcement (1, 2, 5). Not in this plan, by design: relay, provisioning, render, admin page, reaper, `tests/box/`, retirement of the Python tree.

Type consistency: `platform.CaptureRule` fields are used by name in Task 3; `access.Config.GateSecret` is read by `config.Load` in Task 7; `.env` keys named in Task 5 are the ones `init.sh` writes in Task 6.
