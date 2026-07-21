// Actions — POST endpoints for runtime operations accessible from the
// landing page as buttons (e.g., resync metadata, toggle log level).
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mainlink0435/warpbox/internal/config"
	"github.com/mainlink0435/warpbox/internal/metadata"
	"github.com/mainlink0435/warpbox/internal/torbox"
)

// ActionFunc is a callback that an action button triggers.
type ActionFunc func() error

// SyncItemFunc fetches one TorBox item into the local metadata store.
// source is metadata.SourceTorrent or metadata.SourceUsenet.
type SyncItemFunc func(ctx context.Context, source metadata.FileSource, itemID int64) (metadata.SyncItemResult, error)

// actions holds all named action callbacks wired from main.go.
// The map is populated once at startup before any HTTP requests arrive.
var actions = make(map[string]ActionFunc)

// syncItemFn is the optional single-item sync callback.
var syncItemFn SyncItemFunc

// SetActions configures the named action callbacks used by the /actions/ handlers.
func SetActions(funcs map[string]ActionFunc) {
	for name, fn := range funcs {
		actions[name] = fn
	}
}

// SetSyncItemHandler configures the single-item fetch action (landing + integrations).
func SetSyncItemHandler(fn SyncItemFunc) {
	syncItemFn = fn
}

// handleResync triggers an immediate metadata sync from TorBox.
func (s *Server) handleResync(w http.ResponseWriter, r *http.Request) {
	fn, ok := actions["resync"]
	if !ok {
		http.Error(w, "Resync not configured", http.StatusInternalServerError)
		return
	}

	slog.Info("action: resync triggered from landing page")
	go func() {
		if err := fn(); err != nil {
			slog.Error("action: resync failed", "error", err)
		}
	}()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Resync triggered\n"))
}

// handleRestartSync stops the sync worker loop and starts a fresh one.
func (s *Server) handleRestartSync(w http.ResponseWriter, r *http.Request) {
	fn, ok := actions["restart-sync"]
	if !ok {
		http.Error(w, "Restart sync not configured", http.StatusInternalServerError)
		return
	}

	slog.Info("action: restart-sync triggered from landing page")
	go func() {
		if err := fn(); err != nil {
			slog.Error("action: restart-sync failed", "error", err)
		}
	}()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Sync worker restart triggered\n"))
}

// handleSyncItem fetches one TorBox torrent or usenet item by id into SQLite.
// Form fields: source=torrent|usenet, id=<positive int>.
// Never runs a full prune — safe for on-demand use.
func (s *Server) handleSyncItem(w http.ResponseWriter, r *http.Request) {
	if syncItemFn == nil {
		http.Error(w, "Sync item not configured", http.StatusInternalServerError)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	sourceStr := strings.ToLower(strings.TrimSpace(r.FormValue("source")))
	idStr := strings.TrimSpace(r.FormValue("id"))
	if sourceStr == "" || idStr == "" {
		http.Error(w, "Missing required parameters: source (torrent|usenet) and id", http.StatusBadRequest)
		return
	}

	var source metadata.FileSource
	switch sourceStr {
	case "torrent", "torrents":
		source = metadata.SourceTorrent
	case "usenet":
		source = metadata.SourceUsenet
	default:
		http.Error(w, "Invalid source: use torrent or usenet", http.StatusBadRequest)
		return
	}

	itemID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || itemID <= 0 {
		http.Error(w, "Invalid id: must be a positive integer", http.StatusBadRequest)
		return
	}

	slog.Info("action: sync-item triggered", "source", sourceStr, "id", itemID)

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	result, err := syncItemFn(ctx, source, itemID)
	if err != nil {
		if torbox.IsNotFound(err) {
			http.Error(w, fmt.Sprintf("Item not found: %s id %d", sourceStr, itemID), http.StatusNotFound)
			return
		}
		slog.Error("action: sync-item failed", "source", sourceStr, "id", itemID, "error", err)
		http.Error(w, "Sync item failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	msg := result.Message
	if msg == "" {
		if result.Ready {
			msg = fmt.Sprintf("Synced %s id %d: %d file(s)", sourceStr, itemID, result.FilesUpserted)
		} else {
			msg = fmt.Sprintf("Item %s id %d is not ready yet", sourceStr, itemID)
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// 200 even when not ready — not an error; client may retry later.
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(msg + "\n"))
}

// handleLogLevel changes the runtime log level and persists it to config.yml.
// Accepts form value "level=debug|info|warn|error".
func (s *Server) handleLogLevel(w http.ResponseWriter, r *http.Request) {
	newLevel := r.FormValue("level")
	if newLevel == "" {
		http.Error(w, "Missing 'level' parameter", http.StatusBadRequest)
		return
	}

	// Validate and parse the level.
	parsedLevel, err := config.ParseLevel(newLevel)
	if err != nil {
		http.Error(w, "Invalid level: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Persist to config file.
	cfgPath := s.ConfigPath()
	if cfgPath != "" {
		if err := config.UpdateLogLevel(cfgPath, newLevel); err != nil {
			slog.Error("action: failed to persist log level to config", "path", cfgPath, "error", err)
			http.Error(w, "Failed to persist log level", http.StatusInternalServerError)
			return
		}
	}

	// Atomically swap the log level at runtime via LevelVar.
	// This takes effect immediately for all slog handlers that reference it.
	s.cfg.LevelVar.Set(parsedLevel)

	slog.Info("action: log level changed", "level", newLevel)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Log level changed to " + newLevel + "\n"))
}
