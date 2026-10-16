package api

import (
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
)

// MountAuth registers the complete identity API at /api/v1.
func MountAuth(r chi.Router, s *auth.Service) { s.Mount(r) }
