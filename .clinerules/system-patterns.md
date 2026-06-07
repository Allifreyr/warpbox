# System Patterns: Warpbox

## 1. Core Architecture

Warpbox operates as an intercepting WebDAV proxy, designed to be consumed by rclone's WebDAV backend. It acts as a shield between aggressive local media servers (accessed via rclone) and strict cloud APIs (TorBox). The primary pattern is **decoupling filesystem speed from network speed**.

```
Plex/Jellyfin → rclone (FUSE mount) → WebDAV → Warpbox → TorBox API
```

## 2. Configuration Management

* All application settings must be driven by a declarative `config.yml` file.
* The structure should logically separate upstream cloud credentials, local WebDAV server settings, caching rules, and rate-limiting parameters.
* The exact schema is flexible but must support graceful degradation if optional parameters are omitted.

## 3. State & Caching Patterns

* **Persistent State (Metadata):** Use SQLite running in WAL (Write-Ahead Logging) mode to store the virtual directory structure, file metadata, and cache pointers. This allows zero-API directory browsing.
* **Ephemeral State (Data):** Use Just-In-Time (JIT) RAM buffering for video chunk look-aheads. File headers and media chunks should be held in memory temporarily to serve rapid sequential byte-range requests, then evaporated based on a configurable TTL.

## 4. Logging

* Exclusively use Go's native structured logging package (`log/slog`).
* The logging implementation must support toggling between human-readable text output (for local development/debugging) and structured JSON output (for production/containerised environments).

## 5. Network & Rate Limiting

* **Never fail fast with HTTP 429s to the media server.** \* Implement blocking queues and internal throttling to manage massive concurrent read requests. The proxy must absorb burst traffic from Plex and drip-feed it to the TorBox API strictly below the 300 requests/minute limit.

## 6. Decision Tracking

* Always consult decision-log.md before implementing complex logic to avoid repeating failed experiments. Whenever a significant architectural decision is made, a workaround is implemented, or an approach fails, you must immediately document the context, decision, and rationale in decision-log.md.
