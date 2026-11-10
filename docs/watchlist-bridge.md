# On-demand sync and optional watchlist integration

Warpbox can fetch one TorBox item on demand. This works from **Actions > Fetch
item** on the landing page, from scripts, or from an optional client such as
plex_debrid. Warpbox does not require Plex or plex_debrid and never calls Plex.

This release adds integration API v1. It preserves existing WebDAV paths, mount
filters, soft-tag routing, and the default text response format. Existing
standalone installations need no new configuration or database migration.

## API

### CSRF bootstrap

`GET /api/csrf` uses the same optional HTTP Basic authentication as the landing
page. It returns JSON without querying metadata counts or landing-page statistics:

```json
{"csrf_token": "<token>", "delivery_api": 1}
```

The response includes `Cache-Control: no-store`. Tokens are generated for the
running server; refresh the token if a restart causes a POST to return HTTP 403.
The landing-page token continues to work for existing clients.

### Fetch one item

`POST /actions/sync-item` accepts form fields:

| Field | Meaning |
|---|---|
| `source` | Required: `torrent` or `usenet` (`torrents` is also accepted) |
| `id` | Required: positive TorBox item ID |
| `mount` | Optional: name of an existing configured virtual path, such as `watchlisted-tv` |

Send `X-CSRF-Token` and, when configured, HTTP Basic credentials. Add
`Accept: application/json` to request the structured result. For example:

```sh
curl http://localhost:1412/api/csrf
# Copy csrf_token from the response into the next request.
curl -X POST http://localhost:1412/actions/sync-item \
  -H 'X-CSRF-Token: <token>' \
  -H 'Accept: application/json' \
  --data-urlencode source=torrent \
  --data-urlencode id=42 \
  --data-urlencode mount=watchlisted-tv
```

When HTTP Basic authentication is enabled, add `-u 'username:password'` to both
requests. The example uses `curl` shell syntax; on Windows use `curl.exe` with
the appropriate shell line continuation or place the command on one line.

A successful JSON response has these fields:

| Field | Meaning |
|---|---|
| `item_id`, `source` | Requested item ID and normalized source |
| `ready`, `message` | Metadata readiness and a human-readable explanation |
| `files_upserted`, `files_failed` | File-write counts; write failures return an HTTP error |
| `mount` | Requested virtual path, or an empty string |
| `visible_files` | Primary videos visible on that mount: `file_id`, `path`, `size` |
| `excluded_files` | Primary videos excluded from that mount: `file_id`, `path`, `reason` |

Visibility uses the same listing/filter logic as WebDAV, including tags, size
bounds, file patterns, path exclusions, and largest-file selection. It does not
request CDN playback links. File-write counts cover metadata records, while the
visibility arrays contain primary videos only. Without a target mount, the arrays
are empty.

**Ready means metadata was stored successfully.** It does not mean a file passed
the target mount's filters, appeared through rclone's directory cache, or was
indexed by Plex. A ready item can have no visible videos on the selected mount.

| Result | HTTP behavior |
|---|---|
| Ready or still caching / no usable files | HTTP 200; check `ready` in JSON, or the explanation in the default text response |
| Invalid source, ID, or mount | HTTP 400 |
| Missing/incorrect configured authentication | HTTP 401 |
| Missing/invalid CSRF token | HTTP 403 |
| TorBox item not found | HTTP 404 |
| Fetch failure, metadata write failure, or sync deadline | HTTP 502 |

Error responses remain plain text, including for JSON clients. Other internal
errors can return HTTP 500. Without the JSON Accept header, successful fetches
retain the existing plain-text response.

Single-item sync is serialized with full sync and never prunes unrelated records.
Its 60-second request deadline now applies while waiting for the sync lock and
API queue as well as during the TorBox fetch. Under prolonged load it may return
a timeout instead of continuing to wait. Retry transient failures with bounded
backoff; do not repeatedly retry authentication or invalid-input failures.
TorBox calls continue through the shared rate-limit budget.

## Optional shared watchlist shelves

The companion plex_debrid fork owns feed polling, torrent acquisition, soft-tag
writes, persistent recovery, rclone cache invalidation, and Plex scanning. Warpbox
owns TorBox metadata and the virtual paths. The implemented acquisition bridge is
for torrents; Warpbox's item-sync API also accepts usenet IDs.

The shelf flow is:

1. plex_debrid accepts a friends-watchlist request and adds a selected torrent.
2. It merges `watchlistmovies` or `watchlisttv` into the item's existing TorBox tags.
3. It calls Warpbox sync-item and checks metadata readiness and shelf visibility.
4. It checks the mounted files, scans the matching Plex shelf, and verifies indexing.

These are **soft tags**. Add them to `library.override_tags` and use them in the
corresponding virtual paths' `directory_include`. For example:

```yaml
library:
  override_tags:
    - rename
    - watchlistmovies
    - watchlisttv
  virtual_paths:
    # Add these alongside your existing paths. Set file patterns, size bounds,
    # largest_file_only and sidecar settings to suit each shelf.
    - name: watchlisted-movies
      directory_include: '(?i)\bwatchlistmovies\b'
    - name: watchlisted-tv
      directory_include: '(?i)\bwatchlisttv\b'
```

Use `watchlisted-movies` and `watchlisted-tv` as the Plex library roots for the
companion delivery client. Its mount checks require access to the same filesystem
paths as Plex. Section IDs, mount locations, credentials and size limits are
deployment-specific; do not copy another installation's values.

Soft tags permit dual visibility through the normal movie/TV/anime paths when
their filters match. Do not use `forcedwatchlisted-*` tags for this shared shelf:
force tags route an item exclusively. Removing a title from a watchlist does not
delete its TorBox item or remove the shelf tag.

If a tag is wrong, correct it on TorBox and fetch the item again (or wait for the
next full sync). An item hidden by shelf filters requires reviewing the tag and
filter settings; repeating metadata sync alone will not bypass those filters.

Deploy this Warpbox API before the updated plex_debrid client. The latter creates
and maintains its own delivery ledger; Warpbox does not manage that state. See
the companion client's `docs/WATCHLIST_DELIVERY.md` for deployment, recovery and
manual server-test steps. [Virtual-path settings](config-tuning.md) and
[mount setup](rclone-config.md) describe the Warpbox side.
