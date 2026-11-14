<div align="center">
  <img src="warpbox.svg" width="100" alt="Warpbox" />
  <h1>Warpbox</h1>
  <p>A WebDAV proxy for TorBox</p>
</div>

I connected rclone to TorBox and immediately hit rate limits. Every library scan triggered a stack of "API rate limit reached"... 429s. I tried `tpslimit=1` - it sort-of worked but hobbled everything else. I tried a few other tools too; they were buggy and sometimes made things worse.

So I built this. I've been using it for weeks and it works well enough that I wanted to share it.

## Quick Start

### Docker Compose

```yaml
services:
  warpbox:
    image: ghcr.io/allifreyr/warpbox:latest
    container_name: warpbox
    ports:
      - "1412:1412"
    volumes:
      - warpbox_data:/data
    restart: unless-stopped

  rclone:
    image: rclone/rclone:latest
    container_name: warpbox-rclone
    restart: unless-stopped
    environment:
      - PUID=1000
      - PGID=1000
      - RCLONE_CONFIG_WARPBOX_TYPE=webdav
      - RCLONE_CONFIG_WARPBOX_URL=http://warpbox:1412/webdav/
      - RCLONE_CONFIG_WARPBOX_VENDOR=other
    volumes:
      - /mnt/warpbox:/data:rshared
      - rclone_cache:/cache
    cap_add:
      - SYS_ADMIN
    security_opt:
      - apparmor:unconfined
    devices:
      - /dev/fuse:/dev/fuse:rwm
    depends_on:
      - warpbox
    command: >
      mount warpbox: /data
      --uid ${PUID}
      --gid ${PGID}
      --cache-dir /cache
      --vfs-cache-mode full
      --vfs-cache-max-age 24h
      --vfs-cache-max-size 100G
      --vfs-cache-min-free-space 20G
      --vfs-read-chunk-size 32M
      --vfs-read-chunk-size-limit 256M
      --vfs-read-ahead 256M
      --buffer-size 128M
      --transfers 2
      --checkers 8
      --timeout 300s
      --contimeout 30s
      --low-level-retries 3
      --dir-cache-time 10m
      --attr-timeout 24h
      --poll-interval 5m
      --no-checksum
      --no-modtime
      --allow-other
      --allow-non-empty
      --vfs-fast-fingerprint
      --ignore-case
      --log-level NOTICE

volumes:
  warpbox_data:
  rclone_cache:
```

### First run

Warpbox auto-generates `config.yml` from the template on first start. Change
`api_key: "changeme"` to your TorBox API key, then restart:

```
docker compose restart warpbox
```

> **Edit the config:** Since it lives inside a Docker volume, use:
> ```
> docker exec -it warpbox vi /data/config.yml
> ```
> Or view it: `docker exec warpbox cat /data/config.yml`
>
> Prefer editing on the host? Bind a directory instead of the named volume, e.g.
> `./data:/data`, and put `config.yml` (and the DB) under that folder.

### Library change hooks

Warpbox can run shell commands when new or removed items are detected during a sync. Configure `library.on_items_added` and `library.on_items_removed` in your `config.yml`. Hook commands run from the config file's directory (`/data/` in Docker), so a script placed alongside `config.yml` can be referenced with a relative path like `sh myscript.sh`.

### On-demand sync and optional integrations

Use **Actions > Fetch item** on the landing page to fetch one TorBox torrent or
usenet item without waiting for the next full sync. Scripts can use the same
`POST /actions/sync-item` endpoint; integrations can request JSON readiness and
target-mount visibility using the lightweight `GET /api/csrf` bootstrap.

Warpbox works independently of Plex and plex_debrid. Existing standalone setups
need no additional settings. See the [integration API contract](docs/tech-spec.md#actions-actions)
for readiness and visibility fields. Watchlist persistence, delivery retries,
rclone cache refreshes, and Plex scan verification belong to the integration client.

### Bare mount (without Docker)

```bash
rclone mount warpbox: /mnt/warpbox \
  --webdav-url http://localhost:1412/webdav/ \
  --webdav-vendor other \
  --cache-dir /tmp/rclone-cache \
  --vfs-cache-mode full \
  --vfs-cache-max-age 24h \
  --vfs-cache-max-size 100G \
  --vfs-cache-min-free-space 20G \
  --vfs-read-chunk-size 32M \
  --vfs-read-chunk-size-limit 256M \
  --vfs-read-ahead 256M \
  --buffer-size 128M \
  --transfers 2 \
  --checkers 8 \
  --timeout 300s \
  --contimeout 30s \
  --low-level-retries 3 \
  --dir-cache-time 10m \
  --attr-timeout 24h \
  --poll-interval 5m \
  --no-checksum \
  --no-modtime \
  --allow-other \
  --allow-non-empty \
  --vfs-fast-fingerprint \
  --ignore-case \
  --daemon
```

A flag-by-flag explanation of each rclone setting is in [`docs/rclone-config.md`](docs/rclone-config.md).

## Troubleshooting

Having problems? Check [`docs/troubleshooting.md`](docs/troubleshooting.md) for fixes to common issues.

## Media Server Setup

How to add the mount to Plex, Jellyfin, Emby, and Infuse: [`docs/media-servers.md`](docs/media-servers.md).

## Configuration

Every config key with defaults, interactions, and suggested profiles: [`docs/config-tuning.md`](docs/config-tuning.md).

The [`config.yml.example`](config.yml.example) documents all options inline with comments.

### Build from Source

Requires Go 1.26.2+:

```bash
# Clone the repository
git clone https://github.com/Allifreyr/warpbox.git
cd warpbox

# Build the binary
go build -o warpbox ./cmd/warpbox

# Run with a config file
# (see config.yml.example for all options)
./warpbox -config /path/to/config.yml
```

The only runtime dependency besides Go is a working TorBox API key set in `config.yml`.

### Docker permissions (PUID / PGID)

The `PUID` and `PGID` environment variables control which user owns the cached files on disk. Set them to match the user that runs your media server (Plex, Jellyfin, etc.):

- If Plex runs as `plex:plex`, find its IDs: `id plex` → e.g. `uid=1001, gid=1001`, then set `PUID=1001, PGID=1001`.
- If all your containers run as a shared user (common setup), use that user's ID.
- If you're not sure, start with `PUID=1000, PGID=1000` (typical for the first user on most Linux systems).

Mismatched permissions mean cached files are owned by the wrong user, and Plex may not be able to read them - which defeats the purpose of caching.

## Are you struggling with this like I was?

You set up rclone pointing at TorBox, tried to scan your library, and got rate-limited into oblivion. Maybe you tried `tpslimit=1` ... it sort-of worked but hobbled everything else.

You're not the first, and it's not your fault. Here's what's actually happening:

Media servers (Plex, Jellyfin) scan files by probing specific byte ranges, the start and end of a file. rclone turns each byte-range request into a WebDAV call, and every WebDAV call forces warpbox to ask TorBox's API for a CDN link. A single library scan can generate hundreds of API calls, instantly breaching TorBox's strict 300 requests/minute limit and triggering account lockouts.

That's the ffprobe avalanche. There's also the retention trap: TorBox clears inactive files after 30 days, but constant probes from a media server look like "access", which resets the timer. TorBox monitors for this and will ban accounts attempting to artificially retain data.

Warpbox sits between rclone and TorBox and intercepts those aggressive scans. It caches metadata locally, caches CDN URLs, and trickles API calls at a safe rate. To your media server, everything looks normal.

```
Plex/Jellyfin → rclone (FUSE mount) → WebDAV → Warpbox → TorBox API
```

## What about just using rclone directly?

A few things people may have tried before landing here:

- **rclone `--tpslimit=1`** ... it sort-of works but it's a band-aid. Curves your throughput and doesn't fix the underlying problem. Library scans still burn API calls on every metadata probe.
- **Generic WebDAV proxies** ... they don't understand TorBox's rate limits, CDN URL expiration patterns, or how to tell the difference between a metadata scan and actual playback. Everything passes straight through. No protection at all.
- Other tools ... I've tried them. The ones I tried were buggy and bloated.

Warpbox is tuned specifically to how TorBox's API behaves. It's also open source, unlike most alternatives in this space.

## How it works

Warpbox decouples what the media server sees from what the API actually does. Five key pieces:

### 1. Local metadata cache (zero-API browsing)

Warpbox periodically syncs the TorBox directory structure into a local SQLite database (WAL mode for high concurrency). When rclone requests directory listings or file timestamps, warpbox serves them from the database. **Cost: 0 API calls.**

### 2. CDN URL caching with defences

Every CDN download link from TorBox expires. Warpbox caches these URLs in SQLite with a configurable TTL so repeated requests to the same file don't hit the API. Stale-link repair handles 403/404 and recognized HTTP 400 `Invalid Presigned Token` responses through bounded, shared refreshes. Ordinary URL-fetch failures use a negative cache and per-item circuit breaker; an explicit daily-quota refusal uses the shared pause described below.

### 3. Blocking throttle

Playback and metadata sync use separate serial queues with one shared rate budget (default 250 requests per minute). Every TorBox API HTTP attempt passes through that budget, including sync pages, retries, redirect hops, and startup account information. A slow sync response does not hold the limiter, and cancelled admission sends no request. Cached browsing remains local; API pacing does not impose a daily byte budget or coordinate other applications.

### 4. Hang/poll mode

For recoverable CDN failures, Warpbox flushes media headers and waits for recovery, backing off repeated rate limits instead of streaming error pages. Explicit `DAILY_BANDWIDTH_LIMIT_EXCEEDED` responses pause new downloads across this process. Waiting readers share one quota check after 5 minutes, increasing to 10, 20, then 30 minutes on further quota refusals and respecting longer provider delays. Metadata sync and browsing continue. Client timeouts still apply; Warpbox sends no fake media bytes. See [quota handling and signed-link recovery](docs/config-tuning.md#daily-bandwidth-refusals-and-invalid-signed-links).

### 5. Smart redirect for full-file downloads

When a WebDAV request has no byte Range header and a valid cached CDN URL exists, Warpbox can issue an HTTP 302 redirect directly to the CDN. Redirects wait during a quota pause. An uncached full-file read is proxied, and responses fetched directly by the client cannot be inspected or repaired by Warpbox.

## Detailed rclone Configuration

A full explanation of every rclone flag, why each matters, and how to tune them for your hardware is in [`docs/rclone-config.md`](docs/rclone-config.md).

## Technical Details

- **Language:** Go
- **Configuration:** Exclusively managed via a declarative `config.yml`
- **Dependencies:** Minimal - relies primarily on Go standard library plus three external modules (`go-sqlite3`, `yaml.v3`, `chi`)

## TorBox Terms of Service Compliance

Warpbox is designed to operate within TorBox's Terms of Service, available at [github.com/TorBox-App/hosted-terms_of_service](https://github.com/TorBox-App/hosted-terms_of_service).

- **Rate limiting:** Warpbox's throttle queues (metadata sync + playback) share a single rate budget that enforces a configurable limit (default 250 requests/minute) to stay below TorBox's 300 RPM limit. It never bypasses or circumvents API rate limits.
- **Private access:** All CDN URLs and content access tokens stay on your local machine. Warpbox does not share, cache publicly, or distribute any private access links.
- **No account sharing:** Warpbox uses your own TorBox API key, configured locally. It does not bundle, resell, or distribute API keys.
- **Fair usage:** Warpbox's architecture (SQLite metadata cache, CDN URL caching) minimises API calls rather than generating unnecessary requests.

You are responsible for ensuring your use of warpbox complies with TorBox's current TOS. The TOS may change - refer to the canonical source linked above.

## AI Disclosure

This project was developed with heavy AI assistance. If that bothers you, fair enough. However every commit is reviewed by me, and CI-validated. See [AI_DISCLOSURE.md](AI_DISCLOSURE.md).
