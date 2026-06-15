// Package server implements the WebDAV HTTP handler for Warpbox.
//
// This file contains optional HTTP Basic Authentication middleware for the
// web management UI (landing page, logs, actions, stats, HTTP browser).
// Authentication is disabled by default for backward compatibility.
package server

import (
	"crypto/subtle"
	"net/http"
)

// AuthEnabled reports whether authentication is configured.
func (s *Server) AuthEnabled() bool {
	return s.cfg.AuthEnabled
}

// requireAuth returns an HTTP handler that enforces Basic Authentication.
// When auth is disabled, the handler passes through without checking.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	if !s.cfg.AuthEnabled {
		return next
	}

	username := s.cfg.AuthUsername
	password := s.cfg.AuthPassword

	return func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(user), []byte(username)) != 1 ||
			subtle.ConstantTimeCompare([]byte(pass), []byte(password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="warpbox"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}
