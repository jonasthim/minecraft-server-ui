package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
)

const maxPendingRevision = 9007199254740991 // Largest exact JavaScript integer.

// mountPendingSettings stays inside the same authenticated CSRF-protected group
// as instance mutations. A draft never grants permission to mutate a live JVM.
func mountPendingSettings(r chi.Router, s *instance.Service) {
	path := "/instances/{instanceId}/pending-settings"
	r.With(auth.RequireRole(domain.RoleViewer)).Get(path, func(w http.ResponseWriter, r *http.Request) {
		state, err := s.GetPendingSettings(r.Context(), chi.URLParam(r, "instanceId"))
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, state)
	})
	r.With(auth.RequireRole(domain.RoleAdmin)).Put(path, func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Name                      *string         `json:"name"`
			MemoryMiB                 *int            `json:"memory_mib"`
			Config                    json.RawMessage `json:"config"`
			ExpectedRevision          *int64          `json:"expected_revision"`
			ExpectedInstanceUpdatedAt *string         `json:"expected_instance_updated_at"`
		}
		if err := DecodeJSON(r, &input); err != nil {
			WriteError(w, err)
			return
		}
		if input.Name == nil || input.MemoryMiB == nil || input.ExpectedRevision == nil || input.ExpectedInstanceUpdatedAt == nil || !validPendingRevision(*input.ExpectedRevision) {
			WriteError(w, domain.E(domain.CodeValidationFailed, "name, memory_mib, config, expected_revision and expected_instance_updated_at are required; revision must be an exact nonnegative integer"))
			return
		}
		if _, err := time.Parse(time.RFC3339Nano, *input.ExpectedInstanceUpdatedAt); err != nil {
			WriteError(w, domain.E(domain.CodeValidationFailed, "expected_instance_updated_at must be the loaded instance timestamp"))
			return
		}
		config, err := decodePendingConfig(input.Config)
		if err != nil {
			WriteError(w, err)
			return
		}
		state, err := s.SavePendingSettings(r.Context(), *auth.UserFrom(r.Context()), chi.URLParam(r, "instanceId"), instance.SavePendingSettings{
			PendingSettingsDraft: instance.PendingSettingsDraft{Name: *input.Name, MemoryMiB: *input.MemoryMiB, Config: config},
			ExpectedRevision:     *input.ExpectedRevision, ExpectedInstanceUpdatedAt: *input.ExpectedInstanceUpdatedAt,
		})
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, state)
	})
	r.With(auth.RequireRole(domain.RoleAdmin)).Delete(path, func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ExpectedRevision *int64 `json:"expected_revision"`
		}
		if err := DecodeJSON(r, &input); err != nil {
			WriteError(w, err)
			return
		}
		if input.ExpectedRevision == nil || !validPendingRevision(*input.ExpectedRevision) {
			WriteError(w, domain.E(domain.CodeValidationFailed, "expected_revision is required and must be an exact nonnegative integer"))
			return
		}
		state, err := s.DiscardPendingSettings(r.Context(), *auth.UserFrom(r.Context()), chi.URLParam(r, "instanceId"), *input.ExpectedRevision)
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, state)
	})
}

func validPendingRevision(revision int64) bool {
	return revision >= 0 && revision <= maxPendingRevision
}

func decodePendingConfig(raw json.RawMessage) (domain.InstanceConfig, error) {
	var config domain.InstanceConfig
	var fields map[string]json.RawMessage
	invalid := func() (domain.InstanceConfig, error) {
		return config, domain.E(domain.CodeValidationFailed, "config requires every typed property, including explicit online_mode and pvp values")
	}
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return invalid()
	}
	for _, key := range []string{"motd", "difficulty", "game_mode", "max_players", "online_mode", "pvp", "view_distance", "simulation_distance", "level_name", "level_seed", "extra"} {
		value, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return invalid()
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return invalid()
	}
	if err := instance.ValidateConfig(config); err != nil {
		return config, err
	}
	return config, nil
}
