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
Each virtual path is a name plus regex filters, optional size bounds, and a `largest_file_only` flag.

| Field | What it filters on | Example |
|-------|--------------------|---------|
| `directory_include` | Torrent-level directory name. If set, only torrents matching this regex are included. | Include season/episode patterns for TV |
| `directory_exclude` | Torrent-level directory name. Torrents matching this regex are excluded. | Exclude season/episode patterns from movies |
| `file_regex` | Relative file path inside the torrent. Only matching files appear. | Only show `.mkv`, `.mp4`, `.avi` files |
| `min_file_size` | Optional minimum file size (e.g. `300MB`). Omitting = no minimum. Binary units (1MB = 1024² bytes). | Hide samples and tiny junk |
| `max_file_size` | Optional maximum file size (e.g. `10GB`). Omitting = no maximum. | Hide oversized remuxes from a mount |
| `largest_file_only` | When true, only the largest file in the torrent is shown. Hides extras (sample files, subtitles, etc.) within the filtered view. | Usually want this on for both movies and TV |

Size bounds run **after** name filters and **before** `largest_file_only`. They only affect visibility under that virtual path — stored paths and `/webdav/__all__/` are unchanged. Changing min/max can make items appear or disappear on the next media-server scan (same class of change as editing `file_regex`).

The `__all__` virtual path is always available and shows everything unfiltered.

A pair of virtual paths for movies and TV is enabled by default. You can add
more — for example, a `documentaries` path with different regexes — or
customise the existing ones to match your naming convention.

## Tag-Based Overrides

### Force into a library: `forced{virtual_path_name}`

Tag a torrent on **TorBox** (not in `config.yml`) with **`forced` + the exact virtual path `name`** to force it into that mount (and out of other mounts).

| Virtual path `name` | TorBox dashboard tag |
|---------------------|----------------------|
| `tv` | `forcedtv` |
| `movies` | **`forcedmovies`** (not `forcedmovie`) |
| `anime` | `forcedanime` |
| `animemovies` | `forcedanimemovies` |
| `anime-movies` | `forcedanime-movies` (hyphen kept: `forced` + full name) |

**`override_tags` vs force tags:** Force tags are **automatic** for every configured path — you do **not** need to list `forcedtv` / `forcedmovies` / etc. under `library.override_tags`. That list is for **`rename`** and any *extra* custom tags you still match via regex. Listing force tags there is harmless but redundant.

**How it works:**
1. On sync, Warpbox stores tags that are either in `library.override_tags` or equal to `forced` + a configured path name.
2. When listing a mount, if the item has that mount’s force tag → it is **included** (directory include/exclude are skipped for classification).
3. If the item has a force tag for a **different** mount → it is **excluded** from this mount.
4. If there is no force tag → normal directory include/exclude regexes apply.
5. The **stored virtual path is never changed** by force tags (path stability).

You do **not** need to put `|forcedtv` / `|forcedmovies` in every regex. Force routing is semantic.

**Movies tip:** Do **not** set `directory_include: "forcedmovies"` alone — that would hide every untagged movie. Movies should use **exclude-only** TV patterns; use the TorBox tag **`forcedmovies`** when a TV-like name must still appear under movies.

Example virtual paths (regex for automatic split; force tags for exceptions):

```yaml
library:
  override_tags:
    - rename

  virtual_paths:
    - name: movies
      directory_exclude: "(?i)(season|episode)s?\\.?\\d?|[se]\\d\\d|\\b(tv|complete)|\\b(saison|stage)\\.?\\d|[a-z]\\s?-\\s?\\d{2,4}\\b|\\d{2,4}\\s?-\\s?\\d{2,4}\\b"
      file_regex: ".*\\.(mkv|mp4|avi)$"
      largest_file_only: true
    - name: tv
      directory_include: "(?i)(season|episode)s?\\.?\\d?|[se]\\d\\d|\\b(tv|complete)|\\b(saison|stage)\\.?\\d|[a-z]\\s?-\\s?\\d{2,4}\\b|\\d{2,4}\\s?-\\s?\\d{2,4}\\b"
      file_regex: ".*\\.(mkv|mp4|avi)$"
      largest_file_only: false
    - name: anime
      directory_include: "(?i)subsplease|horriblesubs|\\[ember\\]"
      file_regex: ".*\\.(mkv|mp4|avi)$"
      largest_file_only: false
```

Tag on TorBox: `forcedtv`, `forcedmovies`, `forcedanime`, etc. Then resync (or wait for the next interval).

### `rename` — Override Virtual Directory Name

For torrents whose S3-derived directory name is incorrect or undesirable, the `rename` tag tells Warpbox to use the **editable torrent Name from the TorBox dashboard** as the virtual directory name. Include `rename` in `library.override_tags` (default when the list is empty).

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

- `rename` + `forcedtv`: Renames the virtual directory **and** forces the item into the `tv` mount.
- `rename` + `forcedmovies`: Renames and forces into `movies`.
