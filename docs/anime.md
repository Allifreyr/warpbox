# Anime matching for Plex

Warpbox can expose an existing Anime library through an additional, normalized
view. This feature defaults to off. It leaves `/anime`, Movies, TV, watchlist
shelves, TorBox names, and the source database paths unchanged.

The new view groups releases by TMDB show ID and emits names like:

```text
anime-plex/Dr. Stone (2019) {tmdb-123}/Season 02/
  Dr. Stone - S02E01 [release-0123456789abcdef].mkv
```

The ID above is illustrative. Warpbox obtains the real ID from metadata. The
stable release suffix preserves different encodes and revisions. Identical
filenames and sizes in overlapping batches share one view path with alternate
playback sources. No files are downloaded or renamed at TorBox.

## Setup and trial

Keep your existing `library.virtual_paths` Anime entry. Add this alongside it:

```yaml
library:
  anime:
    enabled: false
    source: anime
    name: anime-plex
    tmdb_token: "YOUR_TMDB_API_READ_ACCESS_TOKEN"
    refresh_hours: 24
    overrides_file: anime-overrides.yml
  virtual_paths:
    # Keep your current definitions here, including anime.
```

Use a TMDB **API Read Access Token** (Bearer token), not the short v3 API key.
Obtain it from [TMDB account API settings](https://www.themoviedb.org/settings/api).
Keep it private. Omit `overrides_file` until you have created that file.

With the feature still disabled, use an existing synced Warpbox database:

```powershell
warpbox anime inspect -config config.yml -db warpbox.db
warpbox anime rebuild -config config.yml -db warpbox.db
warpbox anime inspect -config config.yml -db warpbox.db
```

`inspect` opens the database read-only, does not access the network, and prints
JSON with original paths, published paths, metadata IDs, evidence, and unresolved
reasons. Before the first rebuild, files are reported as pending. `rebuild`
fetches and caches metadata and explicitly recalculates mappings, including
previously published names. Stop the test instance while applying corrections
so Plex cannot scan an intermediate rebuild.

After reviewing the report, set `enabled: true` and restart your test instance.
Browse `/http/anime-plex/`; the rclone view is `/webdav/anime-plex/`. Point a
**separate Plex TV Shows test library** at that folder, use the **Plex TV Series**
agent, and set episode ordering to **The Movie Database (Aired)**. Scan only that
test library. Check Dr. Stone's grouping, season/episode assignments, subtitles,
and playback. Do not add both raw and normalized Anime paths to the same library.

Disabling the feature removes only the new view. Migration of Anime++ and any
history reconciliation are separate operator steps after validation.

## Matching behavior

The worker runs at startup, after full/single-item sync notifications, and at
`refresh_hours` intervals. Listings, HEAD, and playback use only local mappings.
TMDB responses and public Anime-Lists datasets are cached in SQLite. Outages
retain existing mappings and cached metadata. Confirmed paths survive restarts;
automatic refreshes cannot rename them, including their show folder names.

The resolver recognizes release groups, batch ranges, checksums, resolutions,
`S2 - 04v2`, and `S02E04`. It matches unique exact normalized titles/aliases,
using the Anime-Lists title catalog and TMDB mappings where available. It does
not choose a fuzzy search result automatically.

Explicit season notation is validated against TMDB episode data. Anime-Lists
offsets are used only when they describe TMDB, never copied from TVDB. Absolute
numbering requires a supported mapping or an explicit override; a batch starting
at episode 25 does not by itself establish a season. In particular, season-one
releases without a verified anime mapping may need a `season: 1` override.
Episode `00`, ambiguous titles, unsupported mappings, and unavailable episodes
are reported unresolved and omitted from the normalized view. They remain on
the original Anime path. This deliberately does not promise automatic coverage
of every anime release or numbering scheme.

Source filters, force tags, size limits, excluded segments, largest-file
selection, and configured sidecar rules still determine eligibility. The derived
mount does not create a `forcedanime-plex` tag. Configure subtitle extensions on
the source Anime entry if external subtitles should be included.

## Overrides

Use exact **sanitized source directory names** from `inspect.original_path`.
For a root-level single file, use its entire source path as the release key.
Overrides never modify the original view. For example:

```yaml
releases:
  - release: '[SubsPlease] Example S2 (01-12) (1080p) [Batch]'
    tmdb_id: 123                 # Replace with the verified TV show ID
    season: 2
    offset: 0                   # Source episode + offset -> target episode

  - release: '[SubsPlease] Example (25-47) (1080p) [Batch]'
    tmdb_id: 123
    ranges:
      - {start: 25, end: 47, season: 2, episode_start: 1}

  - release: '[SubsPlease] Example S2 (00-12) (1080p) [Batch]'
    tmdb_id: 123
    season: 2
    episodes:
      0: {season: 0, episodes: [3]}  # Use the actual TMDB special number

  - release: '[SubsPlease] Example absolute batch'
    tmdb_id: 123
    numbering: absolute

  - release: '[Reza] Example BD'
    tmdb_id: 123
    files:
      'unusual-name.mkv': {season: 1, episodes: [1, 2]}

  - release: '[SubsPlease] Excluded extras'
    exclude: true
```

Precedence: exclusion, filename exception, individual episode exception, range,
absolute-numbering override, season override, verified anime mapping, explicit
source season. `numbering: absolute` is your assertion that source numbering
follows consecutive TMDB regular episodes across sorted seasons, excluding
specials; it must not be used when the release follows a different order.
Ranges are inclusive. File exceptions can map otherwise unparseable filenames.
Multi-episode targets must be contiguous within one season and exist in TMDB.
Unknown YAML fields, duplicate release keys, and overlapping ranges are rejected.

Run `anime rebuild` after changing overrides. This is the explicit operation
that permits path corrections; a normal restart/refresh retains published paths.

## Metadata sources

This product uses the TMDB API but is not endorsed or certified by TMDB.
Metadata: [The Movie Database](https://www.themoviedb.org/).
Anime aliases and mappings: [Anime-Lists](https://github.com/Anime-Lists/anime-lists),
including its mirrored AniDB title catalog. Their coverage may lag new releases.
See also [Plex naming guidance](https://support.plex.tv/articles/naming-and-organizing-your-tv-show-files/).
