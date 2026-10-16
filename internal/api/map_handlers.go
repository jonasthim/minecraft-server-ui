package api

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/mapdata"
	"github.com/jonasthim/minecraft-server-ui/internal/mapview"
	"net/http"
	"strconv"
)

type MapService interface {
	Summary(context.Context, string) (mapdata.Summary, error)
	View(context.Context, string, mapdata.Request) (mapview.View, error)
}

// MountMap belongs in the authenticated API group. Each finite read checks the
// current role/token scope; no stream can retain revoked access.
func MountMap(r chi.Router, s MapService) {
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireRole(domain.RoleViewer))
		r.Get("/instances/{instanceId}/map", func(w http.ResponseWriter, r *http.Request) {
			result, e := s.Summary(r.Context(), chi.URLParam(r, "instanceId"))
			if e != nil {
				WriteError(w, e)
				return
			}
			WriteJSON(w, 200, result)
		})
		r.Get("/instances/{instanceId}/map/view", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			req := mapdata.Request{Dimension: q.Get("dimension")}
			for key, values := range q {
				if len(values) != 1 || (key != "dimension" && key != "chunk_x" && key != "chunk_z" && key != "max_y") {
					WriteError(w, domain.E(domain.CodeValidationFailed, "Unknown or repeated map parameter."))
					return
				}
			}
			for key, target := range map[string]*int32{"chunk_x": &req.ChunkX, "chunk_z": &req.ChunkZ} {
				n, e := strconv.ParseInt(q.Get(key), 10, 32)
				if e != nil {
					WriteError(w, domain.E(domain.CodeValidationFailed, "Map chunk coordinates are required integers."))
					return
				}
				*target = int32(n)
			}
			if values, ok := q["max_y"]; ok {
				n, e := strconv.ParseInt(values[0], 10, 32)
				if e != nil {
					WriteError(w, domain.E(domain.CodeValidationFailed, "Maximum height must be an integer."))
					return
				}
				y := int32(n)
				req.MaxY = &y
			}
			result, e := s.View(r.Context(), chi.URLParam(r, "instanceId"), req)
			if e != nil {
				WriteError(w, e)
				return
			}
			WriteJSON(w, http.StatusOK, result)
		})
	})
}
