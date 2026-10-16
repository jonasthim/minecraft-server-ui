package game

import (
	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"time"
)

type Event struct {
	ID            int64                  `json:"id"`
	InstanceID    string                 `json:"instance_id"`
	SourceCursor  int64                  `json:"source_cursor"`
	ObservedAt    time.Time              `json:"observed_at"`
	ServerTime    *time.Time             `json:"server_time"`
	Kind          string                 `json:"kind"`
	Player        console.PlayerIdentity `json:"player"`
	Message       string                 `json:"message"`
	Source        string                 `json:"source"`
	SourceVersion string                 `json:"source_version"`
	ParserVersion string                 `json:"parser_version"`
	Confidence    string                 `json:"confidence"`
	Gap           bool                   `json:"gap"`
}
type Coverage struct {
	Available         bool      `json:"available"`
	ConfiguredVersion string    `json:"configured_version"`
	SourceVersion     string    `json:"source_version"`
	ParserCapability  string    `json:"parser_capability"`
	ObservedAt        time.Time `json:"observed_at"`
	LogsObservedAt    time.Time `json:"logs_observed_at"`
	Reason            string    `json:"reason"`
}
type Page struct {
	InstanceID     string   `json:"instance_id"`
	Events         []Event  `json:"events"`
	NextCursor     int64    `json:"next_cursor"`
	RetentionFloor int64    `json:"retention_floor"`
	More           bool     `json:"more"`
	Gap            bool     `json:"gap"`
	Reason         string   `json:"reason"`
	Coverage       Coverage `json:"coverage"`
}
