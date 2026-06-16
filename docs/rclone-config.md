# Detailed rclone Configuration

Refer to the [README](../README.md) for a quick-start Docker Compose setup and bare mount command.

Each setting below has a purpose. Read through them once, then adjust the few that depend on your hardware (`PUID`/`PGID`, `--vfs-cache-max-size`, `--cache-dir`).

## File caching & persistence

| Flag | Recommended | Why |
|------|-------------|-----|
| `--vfs-cache-mode` | `full` | **Required.** Saves every downloaded chunk to disk. Without this, seeking or scrubbing in a video forces a full re-download from TorBox. With `full`, the file stays on your drive after the first watch. |
| `--vfs-cache-max-age` | `24h` | How long downloaded chunks survive on disk. If this is too short (e.g. 30 seconds), chunks are deleted faster than you can use them and you re-download everything every time. 24 hours means last night's episode is still cached this morning. |
| `--vfs-cache-max-size` | `100G` | Hard disk limit for the cache. Once the cache fills up, rclone removes the oldest files to make room. Set this to whatever free space you can spare — more means less re-downloading. |
| `--vfs-cache-min-free-space` | `20G` | Safety valve. If your disk drops below 20 GB free, rclone stops caching to avoid filling the drive completely. |
| `--cache-dir` | `/cache` | Where cached files are stored on disk. In Docker, this should be a named volume (so the cache survives container restarts). On bare metal, point it somewhere with plenty of free space. |

## Chunk download tuning

| Flag | Recommended | Why |
|------|-------------|-----|
| `--vfs-read-chunk-size` | `32M` | How much data rclone fetches in a single request to warpbox. Each request = one call to the TorBox CDN. 32 MB is big enough to be efficient (not many round-trips) but small enough to arrive quickly. |
| `--vfs-read-chunk-size-limit` | `256M` | When rclone sees you're playing a file sequentially (e.g. watching a movie), it gradually doubles the chunk size up to this limit. Fewer chunk requests = less CDN overhead as playback continues. |
| `--vfs-read-ahead` | `256M` | How far ahead rclone pre-fetches data beyond your current position. Imagine you're at minute 5 of a video — rclone quietly downloads up to minute 10 in the background. That makes seeking instant. 256 MB covers about 2 minutes of 1080p video or 15 seconds of a 4K remux. |
| `--buffer-size` | `128M` | Memory buffer per open file. Data passes through this RAM buffer on its way to disk. 128 MB can hold two chunks at once (one being written to disk, one being downloaded), which is plenty. |

## Concurrency & timeouts

| Flag | Recommended | Why |
|------|-------------|-----|
| `--transfers` | `2` | How many files rclone downloads at the same time. Keep this low — warpbox only opens 4 CDN connections total, and each file stream uses at least one slot. |
| `--checkers` | `8` | How many files rclone scans in parallel during library refreshes. This only reads metadata (name, size, type) — it does NOT download anything. Warpbox serves metadata from a local SQLite database, so more checkers = faster scans with zero API cost. |
| `--timeout` | `300s` | How long rclone waits for warpbox to respond before giving up. When the TorBox CDN has a temporary hiccup, warpbox holds the connection open and retries every 15 seconds. 5 minutes gives warpbox 20 attempts to recover. |
| `--contimeout` | `30s` | How long rclone waits to establish the initial network connection. 30 seconds is more than enough on a local network. |
| `--low-level-retries` | `3` | How many times rclone retries a failed request before calling it an error. Catches brief network blips. |

## Caching & metadata

| Flag | Recommended | Why |
|------|-------------|-----|
| `--dir-cache-time` | `10m` | How long rclone remembers directory listings in memory before asking warpbox again. Reduces repeated queries during library scans. |
| `--attr-timeout` | `24h` | How long rclone caches file metadata (size, type, timestamps). Since warpbox syncs with TorBox every 5 minutes, new files appear quickly. This prevents rclone from re-checking files that haven't changed. |
| `--poll-interval` | `5m` | How often rclone checks for new or removed files. Sets the maximum delay before new TorBox content shows up in your mount. |

## Safety & compatibility

| Flag | Recommended | Why |
|------|-------------|-----|
| `--no-checksum` | (include) | Stops rclone from reading every file to compute a checksum. Without this, a library scan would download a chunk of every single file — hundreds of unnecessary CDN requests. |
| `--no-modtime` | (include) | Prevents rclone from trying to set file modification times. Not needed for streaming, and avoids pointless write attempts to a read-only mount. |
| `--allow-other` | (include) | Lets other users and containers (like Plex or Jellyfin) access the mounted files. Required for multi-service setups. |
| `--allow-non-empty` | (include) | Allows mounting on a directory that might already contain files. Prevents rclone from refusing to mount. |
| `--vfs-fast-fingerprint` | (include) | Identifies files by size + modification time instead of hashing their contents. Fast and accurate — warpbox provides correct metadata, so hashing is unnecessary. |
| `--ignore-case` | (include) | Makes file name lookups case-insensitive. Prevents "file not found" errors when torrent names use unexpected capitalisation. |
| `--log-level` | `NOTICE` | Hides routine log messages but shows warnings and errors. Keeps logs readable. Set to `DEBUG` only when troubleshooting. |
