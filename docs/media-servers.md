# Media Server Setup

Once warpbox and rclone are running with the mount accessible, add the mount to your media server. Each server works slightly differently.

If you used the README `docker-compose.yml`, the mount is at `/mnt/warpbox` on the host. By default, warpbox is configured with two virtual paths:

| Path | Contains |
|------|----------|
| `/mnt/warpbox/movies/` | Files filtered for movie content |
| `/mnt/warpbox/tv/` | Files filtered for TV content |
| `/mnt/warpbox/__all__/` | Everything unfiltered |

The raw directory under `/__all__/` shows all your TorBox content in its original folder structure. The filtered paths use regex to sort content automatically. You can adjust the filters in `config.yml` under `library.virtual_paths`.

> If something doesn't work, see [troubleshooting.md](troubleshooting.md).

## Plex

1. Open Plex Web → Settings → Manage → Libraries → **Add Library**.
2. Choose the library type: **Movies** or **TV Shows**.
3. Click **Add folders** and browse to your mount:
   - For movies: `/mnt/warpbox/movies`
   - For TV: `/mnt/warpbox/tv`
   - Without virtual paths: `/mnt/warpbox`
4. Agent and scanner should auto-detect. Defaults (Plex Movie / Plex TV Series) work fine.
5. Under the library's **Advanced** settings:
   - **Disable "Generate video preview thumbnails"** — this reads every frame of every file, burning CDN bandwidth for cosmetic benefit. Warpbox isn't designed for sustained full-file scanning at thumbnail density.
   - **Disable "Generate chapter thumbnails"** — same reason.
   - **Intro detection and credit detection can stay on** — these only probe the first and last few minutes of each file, which warpbox handles easily.
   - **Set "Empty trash automatically after every scan" to OFF** initially — if rclone disconnects briefly, Plex will see an empty mount and delete your entire library. Turn it on once everything is stable.
   - **Disable or set a long interval on "Scan my library periodically"** — rclone's `--poll-interval` handles change detection. You don't need Plex polling too.
6. Click **Add Library**. Plex will scan and fetch metadata.

Plex probes each file for codecs, resolution, and duration — mostly small byte-range requests that warpbox serves from cache. The initial scan of a large library may take a while but should stay within TorBox rate limits.

## Jellyfin

1. Open Jellyfin → Dashboard → Libraries → **Add Media Library**.
2. Choose content type: **Movies** or **Shows**.
3. Under **Folders**, click the + button and enter your mount path:
   - For movies: `/mnt/warpbox/movies`
   - For TV: `/mnt/warpbox/tv`
   - Without virtual paths: `/mnt/warpbox`
4. Under the library settings:
   - **Disable "Enable real-time monitoring"** — rclone's FUSE mount does not reliably support filesystem event notifications (inotify). Real-time monitoring causes errors or does nothing.
   - **Disable "Generate trickplay images"** — same sustained full-file read issue as Plex thumbnails.
   - Intro and credit detection are fine to leave on.
5. Configure metadata downloaders as you prefer (these hit external services, not warpbox).
6. Click **OK**. Jellyfin will scan the library.

## Emby

1. Open Emby Server Dashboard → Library → **Add Library**.
2. Choose content type: **Movies** or **TV Shows**.
3. Click the + button next to **Folders** and enter your mount path:
   - For movies: `/mnt/warpbox/movies`
   - For TV: `/mnt/warpbox/tv`
   - Without virtual paths: `/mnt/warpbox`
4. Under library settings:
   - **Disable "Enable real-time monitoring"** — same inotify limitation as Jellyfin.
   - **Disable "Thumbnail image extraction"** — sustained reads across entire files.
   - Chapter image extraction reads embedded data (safe to leave on). Intro detection is fine.
5. Configure other metadata providers as desired.
6. Click **OK**.

## Infuse (iOS / Apple TV)

Infuse connects via WebDAV directly — no rclone mount needed. It speaks WebDAV natively.

1. Open Infuse → Settings → **Add Files**.
2. Choose **WebDAV** as the connection type.
3. Enter the connection details:
   - **Address:** your warpbox host IP (e.g. `192.168.1.100`)
   - **Port:** `1412` (or whatever `server.listen_addr` is set to)
   - **Path:** `/infuse/` or `/webdav/` (both work — warpbox routes them to the same handler)
   - **Username / Password:** leave blank. Warpbox excludes WebDAV and Infuse paths from authentication, so Infuse connects without credentials even when `auth.enabled` is true.
4. Tap **Save**. Infuse will scan the directory and build its library.

Infuse does its own metadata fetching and caching. It does not generate server-side thumbnails, so no special settings needed. The initial scan fetches directory listings (served from warpbox's SQLite cache) and probes a few files for codec info.

> **Note:** If you remove files from TorBox and they still appear in Infuse, use Infuse's **Clear Metadata** option for that share. Infuse caches aggressively.

## What about other servers?

Warpbox presents as a standard WebDAV server. Any media server that can mount a WebDAV share (directly or through rclone) should work. The general principles apply:

- Disable real-time filesystem monitoring.
- Disable thumbnail / video preview generation.
- Intro and credit detection is fine to leave on.
- Set scan intervals to reasonable values.
