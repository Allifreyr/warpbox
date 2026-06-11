# Decision Log

## D-001: Reject torbox-sdk-go

- **Date:** 2026-06-07
- **Context:** Need to integrate with TorBox API to list cached torrents and get CDN download URLs.
- **Decision:** Do not use the official `github.com/TorBox-App/torbox-sdk-go`.
- **Rationale:**
  - The `go.mod` declares `module torbox-sdk-go` (no GitHub path), causing Go toolchain rejection: `malformed module path "torbox-sdk-go": missing dot in first path element`.
  - 10,000+ lines of auto-generated code for features we don't need (Usenet, RSS, Web Downloads, Integrations).
  - Uses `*float64` for IDs and sizes, requiring constant casting.
  - Wraps `net/http` in a custom REST client that can't be easily routed through our throttle queue.
- **Alternatives considered:** Hand-written client informed by OpenAPI spec; `oapi-codegen` generation.
- **Outcome:** SDK is unimportable until TorBox fixes the `go.mod` module path.

## D-002: Reject oapi-codegen for TorBox OpenAPI 3.1 spec

- **Date:** 2026-06-07
- **Context:** TorBox hosts an OpenAPI 3.1 spec at `https://api.torbox.app/openapi.json`. `oapi-codegen v2.7.1` was tested to generate a Go client automatically.
- **Decision:** Do not use `oapi-codegen` for this spec.
- **Rationale:**
  - TorBox spec uses OpenAPI 3.1 `anyOf: [null, <type>]` patterns extensively. `oapi-codegen` doesn't support 3.1 (issue #373) and throws `error resolving primitive type: unhandled Schema type: &[null]`.
  - A Python downgrade script was written to convert 3.1 → 3.0 and strip `anyOf: [null]`. The generated code compiled but contained duplicate symbol errors because the spec has GET+POST on the same path with the same operationId.
  - Manual fix-up of the generated output would be fragile on spec updates.
- **Outcome:** Hand-written client is the correct approach for this API.

## D-003: Hand-written TorBox API client

- **Date:** 2026-06-07
- **Context:** Need to call `GET /v1/api/torrents/mylist` and `GET /v1/api/torrents/requestdl`.
- **Decision:** Write a thin, focused client in `internal/torbox/client.go` (~200 lines).
- **Rationale:**
  - We only need 2 of the ~50 available endpoints.
  - The official OpenAPI spec provides exact request/response shapes to model our types on.
  - Full control over error handling (401/429/5xx), HTTP client configuration, and context propagation.
  - No generated code bloat or dependency on fragile codegen tools.
  - Easy to test with a mock `http.RoundTripper`.
- **Key design:**
  - `do()` helper reads full response body, closes it, returns `[]byte` — no double-close bug.
  - `apiResponse[T]` generic wrapper matches TorBox's `{data, success, detail, error}` envelope.
  - `Torrent` and `TorrentFile` structs use `int64` for sizes/IDs (not `*float64` as in the SDK).

## D-004: Token auth asymmetry

- **Date:** 2026-06-07
- **Context:** TorBox API uses different auth mechanisms for different endpoints.
- **Decision:** Use Bearer header for `/mylist` and query parameter `token` for `/requestdl`.
- **Rationale:**
  - Discovered from the official OpenAPI spec: `/mylist` defines `security: [OAuth2PasswordBearer]`; `/requestdl` defines `token` as a required query parameter (no security scheme).
  - The SDK's `RequestDownloadLinkRequestParams` confirms the token is a query param.
  - The permalink URL pattern documented by TorBox also uses query param: `https://api.torbox.app/v1/api/torrents/requestdl?token=APIKEY&torrent_id=NUMBER&file_id=NUMBER&redirect=true`.

## D-005: CGO dependency via mattn/go-sqlite3

- **Date:** 2026-06-07
- **Context:** SQLite WAL mode is required for persistent metadata storage.
- **Decision:** Use `github.com/mattn/go-sqlite3` (cgo-based).
- **Rationale:**
  - `mattn/go-sqlite3` is the de facto standard Go SQLite driver, uses CGO + SQLite amalgamation.
  - Pure-Go alternatives (modernc.org/sqlite) exist but lack WAL mode support guarantees and have different performance characteristics.
  - MinGW-w64 GCC is available on the dev machine (`x86_64-posix-seh-rev0, Built by MinGW-Builds project, 15.2.0`).
- **Trade-off:** Cross-compilation for non-Windows targets requires a C cross-compiler or a different driver. For initial development on Windows, this is acceptable.

## D-006: Use Gitea Issues instead of active-context.md for work tracking

- **Date:** 2026-06-10
- **Context:** The project's `.clinerules/active-context.md` was being manually updated to track progress but quickly became stale as development accelerated.
- **Decision:** Delete `active-context.md` and rely entirely on Gitea Issues for feature/bug/priority tracking.
- **Rationale:**
  - Gitea Issues provide structured labels (`bug`, `enhancement`, `infra`), priorities (`priority:high`, `priority:low`), milestones, and comments — none of which a Markdown file can offer.
  - Commit messages use `closes #N` to auto-close issues, keeping the trail of what was done and why in the issue itself.
  - The AI assistant reads issues via the `gitea-mcp` server, making the issue tracker directly actionable.
  - A single `active-context.md` duplicated the issue tracker and was never the authoritative source of truth.
- **Outcome:** Work tracking lives in Gitea Issues. The decision log remains only for non-obvious architectural/technical choices.

## D-007: Gitea Projects + Wiki for agile workflow

- **Date:** 2026-06-11
- **Context:** The repo had 11 open issues with no project board, no milestones (deferred), and no structured workflow. The Testing Suite issue (#51) was too large and evolving for a single issue — it needed a living document.
- **Decision:** Created the "Warpbox Kanban" project board with 6 columns (Backlog → Research/Spikes → Ready to Dev → In Progress → Review/QA → Done) and a WIP limit of 2. Moved Research issues (#43, #53, #54) to the Spikes column. Created new labels (`testing`, `research`, `architecture`, `refactor`, `breaking`). Moved the Testing Suite strategy to the Gitea Wiki as a living page, with issue #51 serving as a tracker.
- **Rationale:**
  - A solo developer Kanban board gives visual priority ordering without requiring milestones.
  - The Research column gives architecture discussions a visible home without blocking the dev pipeline.
  - The Wiki is the right place for a testing strategy that evolves over time; the issue tracks completion.
  - Updated `.clinerules/source-control.md` and `.clinerules/system-patterns.md` so future AI sessions follow the same rules.
- **Outcome:** Codified in the `.clinerules/` rules files. The project board still needs manual column assignment via the Gitea web UI (the Projects API is not exposed in Gitea 1.25.5).

## D-008: Exponential backoff + negative cache + circuit breaker for CDN URL fetches

- **Date:** 2026-06-11
- **Context:** Plex's ~2s retry loop on files with expired TorBox CDN URLs caused a death spiral: 500 errors → more API calls → TorBox abuse protection returns 429 → all calls fail. The throttle was working correctly (250 req/min limit, 240ms spacing) but Plex only produces ~30 req/min. The 429 wasn't a rate limit violation — TorBox was punishing the *pattern* of repeated failed requests on the same torrent IDs.
- **Decision:** Implement three mitigation layers:
  1. **Exponential backoff + retry (1s, 2s, 4s)** for 5xx and 429 errors from TorBox API. 429s get a 5s backoff as a safer default. Max 3 retries.
  2. **Negative cache** (30s TTL) mapping `(torrent_id, file_id)` → error. Subsequent Plex retries for the same file return the cached error without hitting the API.
  3. **Circuit breaker** per torrent: 5 failures in a 60s sliding window marks the torrent "stale" for 5 minutes. All API calls for files in a stale torrent are skipped until the stale period expires.
- **Rationale:** 
  - The negative cache is the most important layer — it breaks Plex's retry loop at the application level without any API calls.
  - The circuit breaker prevents a single expired torrent from consuming all rate budget. When the metadata sync refreshes torrent data, stale torrents may become valid again.
  - Retry with backoff handles transient TorBox errors (brief downtime, temporary rate limit) without manual intervention.
  - All thresholds are hardcoded as constants. Defer config-ifying until real-world data shows what values work.
  - The TorBox client `do()` method now logs non-200 response bodies at WARN level, truncated to 512 chars, using `url.Redacted()` to protect the API key. This was essential for diagnosing the 500 errors.
- **Thresholds:** retries=3, backoff=[1s,2s,4s], 429 backoff=5s, negative-cache TTL=30s, circuit-breaker=[5 failures, 60s window, 5min stale]
- **Issue:** #59

## D-009: extea-as-subprocess rejected; Python web session also blocked

- **Date:** 2026-06-11
- **Context:** Gitea has no REST API for project boards. The CLI tool `extea` (a `tea` wrapper) was evaluated as a way to add kanban board operations to the `gitea-unified` MCP server.
- **Decision:** Do not integrate extea into the MCP server. Boards are CLI-only, invoked via pwsh.
- **Rationale:**
  - extea's web session auth was successfully reimplemented in Python (`BoardSession` class), and `projects_create` worked (created Board #1 in warpbox), but `columns_create` returned HTTP 500 regardless of CSRF method used.
  - Spawning extea.exe as a Python subprocess timed out despite correct env vars, stdin=DEVNULL, correct flag ordering, and existing tea config — extea appears to require a TTY for an unknown internal reason.
  - Both approaches consumed ~100 tool calls across 2 hours with no working outcome.
- **Alternatives considered:**
  1. Direct HTTP web session (partially worked, column CRUD got 500)
  2. extea subprocess (extea hangs on stdin)
  3. ConPTY wrapper (Go and C) — failed due to ConPTY syscall complexity and output capture issues
- **Resolution (D-012):** See D-012. pwsh + `execute_command` with foreground terminal works.

## D-010: Build-script approach for dev-deploy

- **Date:** 2026-06-11
- **Context:** The dev-deploy script (`dev-deploy script`) needed to compile a Go binary inside a throwaway `golang:1.26-alpine` container on REDACTED. Initially used an inline `sh -c "...\"...\"..."` command, which broke due to nested quoting across 4 shell layers (PowerShell → SSH → bash → sh).
- **Decision:** Use a standalone `docker-build.sh` script file instead of inline shell commands.
- **Rationale:** The script is uploaded with the source code via tar pipe (step 3), then invoked as `sh /src/docker-build.sh`. Zero quoting issues because double quotes in the Go `-ldflags` are written naturally in a real shell file.
- **Alternatives considered:** Inline `sh -c` with various quoting strategies (double quotes with `\"` escape, single quotes with `''` PowerShell escape). All failed because the `&&` operator or `-ldflags` arguments got parsed by the wrong shell layer.
- **Outcome:** `docker-build.sh` created. The docker run command is now just `golang:1.26-alpine sh /src/docker-build.sh`. Verified working.

## D-011: `docker exec` binary swap before restart

- **Date:** 2026-06-11
- **Context:** The running warpbox container uses the production image's `ENTRYPOINT ["warpbox", ...]` which does not check for `/data/warpbox-next`. The self-upgrading entrypoint (`docker-entrypoint.sh`) only ships in future image releases.
- **Decision:** The dev-deploy script uses `docker exec` to copy the new binary into the container's filesystem *before* `docker restart`. The overlay filesystem persists across restarts.
- **Rationale:** Works with the current production image without rebuilding it. The entrypoint script is still valuable for future images (cleaner pattern), but `docker exec` is needed for backward compatibility.
- **Outcome:** Step 5 of `dev-deploy script` now does `docker exec warpbox cp /data/warpbox-next /usr/local/bin/warpbox` followed by `docker restart warpbox`. Verified working.

## D-012: extea via pwsh + execute_command

- **Date:** 2026-06-11
- **Context:** The AI assistant needed to manage Gitea project board columns from a headless Cline background process. extea requires an interactive TTY (isatty()), which isn't available in background exec mode.
- **Decision:** Invoke extea.exe through `pwsh -noprofile -Command` via `execute_command` with `requires_approval: true`. PowerShell 7 provides a foreground terminal handle that satisfies isatty().
- **Rationale:**
  - AllocConsole/CREATE_NEW_CONSOLE approaches in Go and C could create a console handle but couldn't capture output — stdout went to the invisible console, not the caller.
  - ConPTY wrappers crashed due to Windows API calling convention issues with syscall/unsafe.
  - pwsh spawns a real foreground terminal with stdout/stderr pipes intact, resolving both the TTY requirement and output capture.
  - PS7 was already installed (TLS 1.3+ capable) — no new dependencies.
- **Usage pattern:**
  ```
  pwsh -noprofile -Command "$env:GITEA_PASSWORD='...'; & 'extea.exe' projects list -r ben/warpbox -l cline -o json"
  ```
- **Outcome:** All kanban board operations work. Updated `.clinerules/system-patterns.md` §8 to document the pwsh-based approach. Created the 6 kanban columns (Backlog, Research/Spikes, Ready to Dev, In Progress, Review/QA, Done) that were previously missing.
