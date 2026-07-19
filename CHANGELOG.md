# Changelog

All notable changes to Warpbox will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [v0.7.3-v0.6.0] - 2026-07-19

### Added
- Optional per–virtual-path `path_segment_exclude` regex to hide files under path segments such as `Extras/`, `Specials/`, or `Featurettes/` without dropping packs whose *title* merely contains those words. Default empty (off). Applied after `file_regex` and before size/`largest_file_only`.
- Mock CDN hang/poll integration tests (adapted from upstream): retries on data 429 and disguised text bodies, client disconnect, stream→hang routing, and multi-attempt hang recovery with shortened poll/cooldown for tests.

### Changed
- `ParseFileSize` overflow check uses `math.MaxInt64` (aligned with upstream clarity; same behavior).
- `cdnPollInterval` is a package `var` so tests can shorten hang/cooldown without changing production defaults.

## [v0.7.2-v0.5.1] - 2026-07-17

### Changed
- CDN hang/poll path maintainability: shared helpers for poll backoff, context-aware sleep, transient status checks, and a single `enterCDNHang` entry used for CDN data 429/5xx and disguised text error bodies. Same cooldown and backoff policy as before (including waiting before requestdl and again before the data request under concurrency). No intentional behavior change for listings, force tags, size filters, or playback.
- `Filter.WithSizeBounds` for setting per-mount min/max file size after `NewFilter` (same meaning: `0` = no bound).
- `ExpandOverrideTags` comment corrected (map iteration order is unordered, not “sorted-stable”).

## [v0.7.1-v0.5.0] - 2026-07-16

### Added
- Generic force-into-library tags: TorBox tag `forced` + virtual path name (e.g. path `movies` → `forcedmovies`, path `anime` → `forcedanime`). Semantic filter routing includes the target mount and excludes other mounts without requiring force tags in every regex. Auto-allowlisted for configured paths; `override_tags` default is `["rename"]` plus those force tags.

### Changed
- Preferred movies force tag is **`forcedmovies`** (not `forcedmovie`). Force routing no longer depends on hand-wiring `|forcedtv` / `|forcedmovie` into include/exclude patterns.
- Docs / `config.yml.example`: clearer split between TorBox force tags (`forced` + path name) and config `override_tags` (mainly `rename`).

## [v0.7.1-v0.4.0] - 2026-07-15

### Added
- Optional per–virtual-path `min_file_size` and `max_file_size` (e.g. `300MB`, `10GB`) to hide files outside a size range under that library view only. Defaults unset (no bounds). Complements `largest_file_only`; size filters run after name/regex filters and before largest-file selection. No bitrate filter (TorBox does not expose duration/bitrate; probing would break zero-API browse).

## [v0.7.1-v0.3.2] - 2026-07-15

### Fixed
- CDN hang/poll no longer thrash-retries immediately after a TorBox CDN *data* 429 (e.g. rapid `CDN transient error` / `CDN URL recovered` loops on thumbnail or hover-play). Per-item data cooldown, hang-side data retry with backoff, and acquiring the CDN connection semaphore *before* the upstream request reduce concurrent pressure and avoid streaming error bodies.

## [v0.7.1-v0.3.1] - 2026-07-15

### Fixed
- WebDAV/HTTP directory listings now percent-encode each path segment in `href`s so files with a literal `%` in the title (e.g. `30% Iron Chef`, `.07%`) no longer break rclone with `URL Join failed` / `invalid URL escape`. Stored paths and display names are unchanged.

## [v0.7.1-v0.3.0] - 2026-07-15

### Added
- TorBox dashboard `forcedmovie` classification override tag to default override tags, supporting movie classification overrides (forcing TV-pattern-matching torrents into the Movies library).
- Documentation for `forcedmovie` tag in `config-tuning.md` and configuration example comments.
- Test suite coverage for `forcedmovie` tag in library filter and sync worker.

### Changed
- Config default `override_tags` updated to include `"forcedmovie"`.

## [v0.7.1-v0.2.0] - 2026-07-13

### Added
- TorBox dashboard `rename` tag support to override the virtual directory name using the editable torrent name.
- Wraps single-file torrents in a directory named after their sanitized dashboard name.
- Replaces the top-level directory for multi-file torrents, preserving internal subdirectories.
- Test suite coverage for `rename` tag path replacement logic.

### Changed
- Config default `override_tags` updated to include `"rename"`.

## [v0.7.1-v0.1.0] - 2026-07-10

### Added
- TorBox dashboard tag-based filter override. Allows users to override automatic movie/TV virtual path classification by tagging torrents on the TorBox dashboard (e.g. with `forcedtv`). Tags are processed and matched at regex-filtering time only, preserving original virtual paths and mount directory stability.
- Configurable `library.override_tags` setting (defaults to `["forcedtv"]`) to control which dashboard tags participate in virtual path matching.

### Changed
- **Database schema v3:** Added `filter_tags` text column to the `files` table. Older schemas are automatically recreated on first startup, preserving the self-healing behavior of the database cache.
- `UpsertFile` and `ListDir` database queries updated to include the `filter_tags` field.

## [v0.7.0] - 2026-07-09

### Added
- CDN URL fallback to alternative TorBox items when the primary item's file cannot be fetched — improves resilience when duplicate downloads exist
- Unique path count on landing page (shown as "N total / M unique")
- `CountDistinctPaths()` store method for deduplicated file counts
- `GetFileAlternatives()` store method for querying duplicate entries

### Changed
- **Database schema v2:** File uniqueness is now enforced by `(source, item_id, file_id)` instead of `path`. Duplicate virtual paths from different TorBox items are preserved as separate rows. Existing databases are automatically recreated on first startup — the cache will repopulate on the next sync cycle. **This is a one-way upgrade; to downgrade, delete `warpbox.db` and re-sync.**
- `UpsertFile` conflict target changed from `path` to `(source, item_id, file_id)` — CDN URL cache fields are preserved on conflict
- `GetFileByPath` returns the highest-internal-ID record when duplicates exist (deterministic tiebreaker)
- `ListDir` deduplicates by path (one row per unique path)
- Landing page shows both total file rows and distinct virtual paths
- `dbinspect` diagnostic tool updated for new schema checks

## [v0.6.0] - 2026-06-26

### Added
- Mylist pagination — all torrents/usenet items sync regardless of account size. TorBox caps each response at ~10,000 items; warpbox pages through with offset until exhaustion. (thanks @Fredddi43, closes #1)
- Configurable `sync.list_page_size` — controls the per-request page window when paginating mylist API calls (default 5000, range 1–10000), shown on landing page
- Exponential backoff in CDN hang/poll mode on repeated 429 rate-limit errors (15s → 30s → 60s → 2min → 5min max), preventing per-item requestdl death spirals
- Item count on landing page — distinct torrents/usenet items alongside total files

### Fixed
- CDN text error body is no longer streamed and cached as file data. TorBox's CDN sometimes returns HTTP 200/206 with "Too many requests" or HTML body instead of 429; the GET handler now checks Content-Type before streaming. (thanks @Fredddi43, closes #3)

### Changed
- Removed `sync.limit` — pagination now fetches all items without a ceiling. The old cap was a workaround from before the pagination engine existed.

## [v0.5.4] - 2026-06-23

### Added
- Configurable sync retry: `sync.retry_attempts` (default 3) and `sync.retry_backoff` (default 1s) control exponential backoff for transient API errors during metadata sync

### Fixed
- TorBox API transient errors (502, timeouts, HTML error pages) during metadata sync now trigger retry with exponential backoff instead of failing immediately
- TorBox API returning HTML error pages with HTTP 200 now logs the body at WARN (200-char preview) and DEBUG (full body) instead of a cryptic `invalid character '<'` error
- CDN 403/404 failures after URL repair exhaustion are cached in the negative cache, preventing Plex retry storms from burning TorBox API calls
- Pre-existing data race in `Status()`/`syncOnce()` on `lastError`/`lastSuccess` — now protected by mutex

## [v0.5.3] - 2026-06-19

### Fixed
- Set `largest_file_only: false` for `tv` virtual path — season packs now show all episode files instead of just one, refs #172
- Remove `:ro` from docker-compose config volume mount so `GenerateTemplate` can create `config.yml` on first run, refs #172

## [v0.5.2] - 2026-06-16

### Fixed
- Always inject `__all__` synthetic directory at /webdav/, /http/, /infuse/ root even when no virtual paths are configured
- Silently ignore user-configured virtual path named `__all__` instead of returning a validation error

## [v0.5.1] - 2026-06-16

### Added
- Graceful HTTP server shutdown with 30s timeout, refs #162

### Changed
- Wire stats.interval_seconds, log dropped errors, update stale docs, refs #163 #166 #168
- Address audit findings and expand test coverage, refs #153 #156 #158 #157
- Docker tag to :latest and add source-build section, refs #152
- Humanise README and contributing guide, refs #84 #106

### Fixed
- Log discarded time.Parse errors in stats queries, refs #160
- Pass caller context in ringBufferHandler instead of context.Background(), refs #159
- Change prune gate to check API success not count>0, refs #155
- Log discarded ListItemDirs errors in sync change detection, refs #160
- Remove invalid directory_regex and duplicate entries in config.yml.example, refs #162
- Correct ListenAddr default comment from :8080 to :1412, refs #152 #154

## [v0.5.0] - 2026-06-16

### Added
- Virtual library paths with directory/file regex filtering and change hooks, refs #32 #33
- Chi router for structured HTTP routing with middleware support, refs #43
- Chi-driven OpenAPI spec generation via route introspection, refs #53
- Optional HTTP Basic Authentication for web management UI, refs #79
- Sync worker restart action via landing page, refs #95
- Pre-release codebase audit script, refs #96
- Report disclaimer and use deepseek-pro model for audits, refs #96
- Code comment quality check in audit prompt, refs #145
- HTTP browser folder sizes and column sorting (name, size, modified), refs #146
- `/healthz` endpoint for container health checks, refs #111
- Audit self-reports now emit individual issue findings with run metadata, refs #147

### Changed
- Consolidate health/metrics into single DB-backed source of truth — remove redundant 5-minute memory stats log ticker (`cache.memory_stats_interval_minutes` removed), closes #98, closes #99
- Replace `directory_regex` with `directory_include` / `directory_exclude` for path filtering
- Replace `sync.Cond` with channel-based throttle queue to prevent goroutine leak, refs #142
- Use `url.JoinPath` instead of raw string concatenation for URL construction, refs #113
- Use `defer` for CDN connection release in non-hang streaming path, refs #112
- Migrate all documentation to standard conventions with `docs/tech-spec.md` skeleton, refs #96
- Move internal AI instructions and Git Authorship rules into docs/

### Fixed
- HTTP browser hrefs missing virtual path mount prefix in breadcrumbs and links
- Virtual paths now correctly nested under `/webdav/` as subdirectories
- Remove DEBUG-level per-row UpsertFile logging that flooded logs during sync
- Record `gc_cycles` as per-interval delta instead of cumulative gauge in stats charts
- Replace `torrent_id` with `item_id` in dbinspect queries, refs #141
- Gate `/debug/pprof/` behind `enable_pprof` config flag, wire SyncLimit, fix stale comment, refs #107, refs #108, refs #140
- Batch prune deletes and retry SetCDNURL to prevent SQLite lock contention, refs #100
- Remove live API credentials from repo — switch to `.template` files, refs #143
- Fix pre-release audit documentation issues across multiple tickets, refs #109 #110 #138 #139

[Unreleased]: /compare/v0.7.1-v0.5.0...HEAD
[v0.7.1-v0.5.0]: /compare/v0.7.1-v0.4.0...v0.7.1-v0.5.0
[v0.7.1-v0.4.0]: /compare/v0.7.1-v0.3.2...v0.7.1-v0.4.0
[v0.7.1-v0.3.2]: /compare/v0.7.1-v0.3.1...v0.7.1-v0.3.2
[v0.7.1-v0.3.1]: /compare/v0.7.1-v0.3.0...v0.7.1-v0.3.1
[v0.7.1-v0.3.0]: /compare/v0.7.1-v0.2.0...v0.7.1-v0.3.0
[v0.7.1-v0.2.0]: /compare/v0.7.1-v0.1.0...v0.7.1-v0.2.0
[v0.7.1-v0.1.0]: /compare/v0.7.1...v0.7.1-v0.1.0
[v0.7.0]: /compare/v0.6.0...v0.7.0
[v0.6.0]: /compare/v0.5.4...v0.6.0

[v0.5.4]: /compare/v0.5.3...v0.5.4

[v0.5.3]: /compare/v0.5.2...v0.5.3

[v0.5.2]: /compare/v0.5.1...v0.5.2

[v0.5.1]: /compare/v0.5.0...v0.5.1

