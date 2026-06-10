// Actions — POST endpoints for runtime operations accessible from the
// landing page as buttons (e.g., resync metadata, clear RAM cache).
package server

import (
	"log/slog"
	"net/http"
)

// ActionFunc is a callback that an action button triggers.
type ActionFunc func() error

// Server actions config, wired from main.go.
var actionResync  ActionFunc
var actionClearCache ActionFunc

// SetActions configures the action callbacks used by the /actions/ handlers.
func SetActions(resync, clearCache ActionFunc) {
	actionResync = resync
	actionClearCache = clearCache
}

// handleActions dispatches POST requests to the appropriate action handler.
func (s *Server) handleActions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	switch r.URL.Path {
	case "/actions/resync":
		s.handleResync(w, r)
	case "/actions/clearcache":
		s.handleClearCache(w, r)
	default:
		http.NotFound(w, r)
	}
}

// handleResync triggers an immediate metadata sync from TorBox.
func (s *Server) handleResync(w http.ResponseWriter, r *http.Request) {
	if actionResync == nil {
		http.Error(w, "Resync not configured", http.StatusInternalServerError)
		return
	}

	slog.Info("action: resync triggered from landing page")
	go func() {
		if err := actionResync(); err != nil {
			slog.Error("action: resync failed", "error", err)
		}
	}()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Resync triggered\n"))
}

// handleClearCache evicts all cached chunks from the RAM buffer.
func (s *Server) handleClearCache(w http.ResponseWriter, r *http.Request) {
	if actionClearCache == nil {
		http.Error(w, "Clear cache not configured", http.StatusInternalServerError)
		return
	}

	slog.Info("action: clear cache triggered from landing page")
	if err := actionClearCache(); err != nil {
		slog.Error("action: clear cache failed", "error", err)
		http.Error(w, "Clear cache failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Cache cleared\n"))
}