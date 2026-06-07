# Warpbox

Warpbox is a high-performance, lightweight WebDAV proxy written in Go, designed to be consumed by rclone's WebDAV backend. It mounts a cloud-hosted torrent cache (TorBox) as a native, stable local filesystem via rclone's FUSE layer. Its primary function is to act as a defensive interceptor, protecting strict upstream API limits from the aggressive scanning behaviours of media servers like Plex and Jellyfin.

```
Plex/Jellyfin → rclone (FUSE mount) → WebDAV → Warpbox → TorBox API
```

## The Problem: The API Collision

Standard mounting tools (like running rclone directly against an HTTP endpoint) act as literal translators between the operating system and the cloud provider. This creates catastrophic failures when paired with TorBox:

1. **The `ffprobe` Avalanche:** Media servers scan files by requesting specific byte ranges (the start and end of a file) to extract metadata, codecs, and sonic data. rclone faithfully translates every byte-range request into a WebDAV call to Warpbox. Every range request forces Warpbox to ask the TorBox API for a secure CDN token. A single library scan can generate hundreds of API requests in seconds, instantly breaching TorBox's strict **300 requests/minute** limit and triggering account lockouts.
2. **The Retention Trap:** TorBox relies on a 30-day inactivity timer to clear cloud storage. If a media server constantly probes every file for metadata, TorBox registers this as active "access" and resets the timer. TorBox actively monitors for this abuse and will ban accounts attempting to artificially retain data without active human playback.

## The Solution: The Warpbox Architecture

Warpbox solves this by decoupling the filesystem speed from the network speed. It lies to the media server (via rclone) to protect the upstream API.

### 1. SQLite State Mapping (Zero-API Browsing)

Warpbox periodically synchronises the TorBox directory structure into a local SQLite database (running in WAL mode for high concurrency). When rclone requests directory listings or file timestamps, Warpbox serves this data instantly from SQLite. **Cost: 0 API calls.**

### 2. Just-In-Time (JIT) RAM Buffering

Warpbox distinguishes between metadata scans and human playback based on byte-range requests. When a media server (via rclone) requests the first 500 KB of a file:

* Warpbox requests a secure CDN link and downloads a larger chunk (e.g., 16 MB) directly into the server's RAM.
* It serves the 500 KB to rclone instantly.
* When rclone subsequently asks for the next few megabytes, Warpbox serves it directly from the RAM buffer.
* Unused chunks evaporate from RAM after a configurable TTL.

### 3. The Blocking Throttle

Warpbox never fails fast. If a user imports files and rclone propagates 200 simultaneous reads, Warpbox intercepts all 200 requests, places them in a blocking queue, and trickles the API calls to TorBox at a safe, configured rate (e.g., 4 requests per second). rclone simply perceives a slow mechanical hard drive; Plex does not crash, and the TorBox API remains secure.

### 4. Smart Playback Handoff

When Warpbox detects a request for a byte range deep within the file (indicating active human playback rather than a header scan), it establishes a continuous stream from the TorBox CDN, or issues an HTTP 302 redirect to offload bandwidth entirely, depending on the configuration.

## Technical Specifications

* **Language:** Go (Golang)
* **Configuration:** Exclusively managed via a declarative `config.yml`
* **Dependencies:** Minimal footprint; relies primarily on standard Go libraries (`net/http`, `log/slog`).

## Status

Active Development / Concept Phase.