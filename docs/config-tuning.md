# Configuration Tuning

The commented [`config.yml.example`](../config.yml.example) documents every key with
its default, range, and purpose. This page builds on that — it covers how the
settings interact and what to consider when changing them.

The default config works well for a typical home media setup with one or two
simultaneous streams. The suggestions below are starting points, not rules.

## Quick Reference

| Key | Default | Range | Consider changing when... |
|-----|---------|-------|--------------------------|
| `throttle.requests_per_minute` | 250 | 10–1000 | You want more headroom below TorBox's 300 RPM limit, or you need faster sync throughput |
| `cache.max_cdn_connections` | 4 | 1–64 | Multiple simultaneous streams compete for CDN slots |
| `cache.cdn_url_ttl_minutes` | 120 | 1–1440 | You see `stale CDN URL detected` warnings — the URL is expiring before the TTL |
| `cache.cdn_url_auto_repair` | true | true/false | You'd rather serve errors than wait for repair (not recommended) |
| `cache.cdn_url_retry_attempts` | 1 | 0–10 | TorBox API is flaky — more retries increase success rate but burn rate budget |
| `cache.cdn_url_retry_backoff` | 1s | 1–60s | You want a longer pause between retries to avoid hammering the API |
| `cache.cdn_url_repair_retries` | 2 | 0–10 | Stale CDN URLs persist after the first repair attempt |
| `cache.negative_cache_ttl_seconds` | 30 | 1–300 | Plex retry storms are still hitting the API — lengthen the TTL |
| `cache.negative_cache_max_entries` | 5000 | 100–50000 | Memory is tight (lower), or you have many files and see cache thrashing (raise) |
| `cache.circuit_breaker_failures` | 5 | 1–100 | A single bad torrent is consuming too much rate budget — tighten this |
| `cache.circuit_breaker_window_seconds` | 60 | 1–3600 | Failures are spread out over longer periods — widen the window |
| `cache.circuit_breaker_stale_minutes` | 5 | 1–60 | You want quarantined torrents to recover faster or slower |
| `cache.circuit_breaker_max_entries` | 2000 | 50–20000 | Memory is tight (lower), or you have many active torrents (raise) |
| `cache.cleanup_interval_seconds` | 60 | 10–3600 | Stats recording also uses this interval — see interactions below |
| `sync.interval_minutes` | 5 | 1–1440 | New content shows up too slowly for your workflow |
| `sync.retry_attempts` | 3 | 0–10 | TorBox API is flaky during sync — increase for more resilience |
| `sync.retry_backoff` | 1s | 1–60s | Longer pauses between retries to avoid hammering the API |
| `sync.list_page_size` | 5000 | 1–10000 | You want to tweak API call frequency vs. pagination safety |
| `stats.retention_hours` | 24 | 1–720 | You want longer history on the sparkline charts |
| `stats.chart_minutes` | 60 | 1–1440 | You want the landing page chart to show a shorter or longer window |
| `auth.enabled` | false | true/false | The web UI is accessible to others on your network |
| `logging.level` | info | debug/info/warn/error | You're troubleshooting and need more detail |
| `logging.format` | text | text/json | You're sending logs to a structured log collector |
| `library.hook_timeout_seconds` | 30 | 1–3600 | Your on-* hook scripts take longer than 30 seconds |

## Key Interactions

### CDN connections and rclone transfers

Warpbox limits concurrent CDN data connections to `max_cdn_connections`. Each
file being downloaded (or seeking within a file) uses one slot. Rclone's
`--transfers` flag controls how many files rclone downloads at the same time,
so if `--transfers` is set to 4 and `max_cdn_connections` is also 4, there are
no slots left for seeks or new connections — requests queue up.

A good starting point is keeping rclone's `--transfers` at or below
`max_cdn_connections` minus 1. If `max_cdn_connections=4`, try
`--transfers=2` or `--transfers=3`.

### Sync interval and rclone poll interval

Warpbox's `sync.interval_minutes` controls how often it queries TorBox for new
or removed files. Rclone's `--poll-interval` controls how often rclone checks
warpbox for changes. New files only appear in the mount after both intervals
have elapsed. Keeping them roughly equal (both at 5 minutes, for example) gives
predictable behaviour.

### Sync retry

When the TorBox API returns transient errors (502, timeout, HTML error pages) during
a sync cycle, the sync worker retries `ListTorrents` and `ListUsenet` up to
`sync.retry_attempts` times with exponential backoff: `retry_backoff * 1s, * 2s, * 4s`.
A value of 0 disables retries — the sync fails immediately on the first transient error.

The retry only applies to errors that `torbox.IsRetryable()` considers transient.
Permanent errors (401 unauthorized, 404 not found, API-level errors) are not retried.

### CDN URL TTL

TorBox CDN URLs expire after a few hours. The `cdn_url_ttl_minutes` default of
120 is conservative — it usually refreshes well before the real expiry. If you
see `stale CDN URL detected` in the logs, the TTL might be too long for your
use pattern. The auto-repair feature (default on) handles stale URLs
transparently, but each repair costs one API call.

### Mylist pagination

When fetching your torrent and Usenet lists, Warpbox pages through TorBox's API
in windows of `sync.list_page_size` items (default 5000). TorBox itself caps
each response at ~10,000 items regardless of the requested `limit`, so pagination
is required to avoid silently dropping the oldest items on larger libraries.

The tradeoff is page size vs. API calls:
- **5000** — 3 calls for a 10k library, safe headroom below TorBox's cap
- **8000** — 2 calls, tighter but TorBox's ~10k cap still provides margin
- **1000** — ~11 calls, most conservative if you're paranoid about the cap lowering

You probably don't need to change this unless you're on a very slow connection
and want to minimise API calls, or you have a very small library and want a
faster initial sync.

### Sync limit and library size

Warpbox syncs all torrents and Usenet items by paginating through TorBox's API
(the page window is controlled by `sync.list_page_size`). No cap — everything
in your account is visible in the mount.

### Circuit breaker settings

The three circuit breaker values work together:
- `failures` over `window` seconds triggers quarantine
- Quarantine lasts `stale_minutes`

If you tighten `failures` (lower) or `window` (shorter), the breaker trips
faster — good for stopping problematic torrents, but it may quarantine
legitimate files during transient CDN blips. Loosening them does the opposite.

### Cleanup interval and stats recording

The `cleanup_interval_seconds` key drives both cache expiry sweeps and stats
recording frequency. Shorter intervals (minimum 10 seconds) give finer-grained
stats but increase CPU and disk I/O. Longer intervals (60 seconds or more) are
gentler but produce smoother charts.

## Suggested Profiles

These are starting points based on common scenarios. Adjust from there.

### Default (most setups)

Most keys at their defaults. This handles 1–2 streams on a typical NAS or
server with adequate RAM. Only change `torbox.api_key` and optionally `auth`
credentials.

### Low-memory device (Raspberry Pi, small VPS)

| Key | Suggested | Why |
|-----|-----------|-----|
| `negative_cache_max_entries` | 500 | Reduce in-memory map size |
| `circuit_breaker_max_entries` | 200 | Same reason |
| `sync.list_page_size` | 1000 | Fewer items per page, slower sync, more API calls |
| `cleanup_interval_seconds` | 120 | Less frequent stats I/O |

### Large library (10 000+ files)

| Key | Suggested | Why |
|-----|-----------|-----|
| `sync.list_page_size` | 8000 | Fewer API calls per sync cycle |
| `sync.interval_minutes` | 10 | Give the longer sync time to complete before the next cycle |

### Heavy streaming (3+ simultaneous 4K streams)

| Key | Suggested | Why |
|-----|-----------|-----|
| `max_cdn_connections` | 6–8 | More concurrent CDN slots |
| On rclone side | `--transfers` 3, `--buffer-size 256M` | Match the higher CDN capacity |

### Conservative (avoid TorBox warnings, maximise rate-limit headroom)

| Key | Suggested | Why |
|-----|-----------|-----|
| `throttle.requests_per_minute` | 150 | 50% headroom below TorBox's 300 RPM limit |
| `circuit_breaker_failures` | 3 | Trip faster on problematic torrents |

## Virtual Path Tuning

`library.virtual_paths` lets you create filtered views of your TorBox content.
Each virtual path is a name plus three regex filters and a `largest_file_only` flag.

| Field | What it filters on | Example |
|-------|--------------------|---------|
| `directory_include` | Torrent-level directory name. If set, only torrents matching this regex are included. | Include season/episode patterns for TV |
| `directory_exclude` | Torrent-level directory name. Torrents matching this regex are excluded. | Exclude season/episode patterns from movies |
| `file_regex` | Relative file path inside the torrent. Only matching files appear. | Only show `.mkv`, `.mp4`, `.avi` files |
| `largest_file_only` | When true, only the largest file in the torrent is shown. Hides extras (sample files, subtitles, etc.) within the filtered view. | Usually want this on for both movies and TV |

The `__all__` virtual path is always available and shows everything unfiltered.

A pair of virtual paths for movies and TV is enabled by default. You can add
more — for example, a `documentaries` path with different regexes — or
customise the existing ones to match your naming convention.

## Tag-Based Overrides

Warpbox recognizes special tags from your TorBox dashboard to override virtual path behavior. Tags are configured via `library.override_tags` (default: `["forcedtv", "forcedmovie", "rename"]`).

### `forcedtv` — Force Classification as TV

For torrents that lack traditional naming indicators (e.g. older TV shows or complete cartoon packs that get misclassified into `/movies` because they lack standard tags like `S01`, `Season`, or `Complete`), you can use the `forcedtv` tag to force them into the correct virtual library path.

**How it works:**
1. When you sync from TorBox, Warpbox checks each torrent's tags against `library.override_tags`.
2. Any matching tag is stored as metadata alongside the files.
3. When Warpbox evaluates the `directory_include` and `directory_exclude` regexes, it appends the matching tags to the directory name (e.g. `Cow and Chicken forcedtv`).
4. **Important**: The actual virtual path of the files is **never mutated** (preserving path stability, rclone cache, and Plex watch history). The tag is only used temporarily during filter matching.

### `forcedmovie` — Force Classification as Movie

For torrents that contain TV-like naming markers but are actually movies (e.g. a movie with "Complete" or a year range in the title that triggers the TV regex), you can use the `forcedmovie` tag to force them into the movie virtual library path. This is the exact opposite of `forcedtv`.

**How it works:** Identical to `forcedtv` — the tag `forcedmovie` is appended to the directory name during filter matching only. The virtual path is never changed.

### Setup for `forcedtv` and `forcedmovie`

Add `forcedtv` and `forcedmovie` to your directory include and exclude regexes:

```yaml
library:
  override_tags:
    - forcedtv
    - forcedmovie
    - rename

  virtual_paths:
    - name: movies
      directory_exclude: "(?i)(season|episode)s?\\.?\\d?|[se]\\d\\d|\\b(tv|complete)|\\b(saison|stage)\\.?\\d|[a-z]\\s?-\\s?\\d{2,4}\\b|\\d{2,4}\\s?-\\s?\\d{2,4}\\b|forcedtv"
      directory_include: "forcedmovie"
      file_regex: ".*\\.(mkv|mp4|avi)$"
      largest_file_only: true
    - name: tv
      directory_include: "(?i)(season|episode)s?\\.?\\d?|[se]\\d\\d|\\b(tv|complete)|\\b(saison|stage)\\.?\\d|[a-z]\\s?-\\s?\\d{2,4}\\b|\\d{2,4}\\s?-\\s?\\d{2,4}\\b|forcedtv"
      directory_exclude: "forcedmovie"
      file_regex: ".*\\.(mkv|mp4|avi)$"
      largest_file_only: true
```

Once configured, tag the torrent on your TorBox dashboard with `forcedtv` or `forcedmovie` and it will move to the correct section on the next sync.

### `rename` — Override Virtual Directory Name

For torrents whose S3-derived directory name is incorrect or undesirable, the `rename` tag tells Warpbox to use the **editable torrent Name from the TorBox dashboard** as the virtual directory name.

**How it works:**
1. Add the `rename` tag to a torrent on your TorBox dashboard.
2. Edit the torrent's name on the dashboard to the desired directory name (e.g. `Cow and Chicken S01-04`).
3. On the next sync, Warpbox replaces the top-level directory with the dashboard name.
4. Subdirectories (e.g. `Season 1/`) are preserved — only the top-level directory is replaced.
5. Single-file torrents are wrapped in a directory named after the dashboard name.

**Example:**
- S3 path: `hash/Cow and Chicken/episode.avi`
- Without `rename` tag: virtual path = `Cow and Chicken/episode.avi`
- With `rename` tag and dashboard name `Cow and Chicken S01-04`: virtual path = `Cow and Chicken S01-04/episode.avi`

> **Note:** Changing the dashboard name while the `rename` tag is active will change the virtual path on the next sync. This may trigger a Plex rescan for the affected content. This is by design — you are intentionally renaming the content.

> **Tip:** Removing the `rename` tag reverts the virtual path to the original S3-derived name on the next sync.

### Combining Tags

Tags can be combined freely. For example:
- `rename` + `forcedtv`: Renames the virtual directory AND forces TV classification
- `rename` + `forcedmovie`: Renames the virtual directory AND forces movie classification

This is useful for content that has both a wrong S3 name and incorrect classification — you fix the name and force it into the correct library simultaneously.
