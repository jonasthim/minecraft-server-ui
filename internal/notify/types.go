// Package notify provides private, disabled-by-default notification destinations
// and durable bounded delivery. No provider transport errors enter public data.
package notify

import (
	"context"
	"database/sql"
	"sync"
	"time"
)

type Provider string

const (
	Discord  Provider = "discord"
	Slack    Provider = "slack"
	Ntfy     Provider = "ntfy"
	Telegram Provider = "telegram"
	Webhook  Provider = "webhook"
	Email    Provider = "email"
)

type EventKind string

const (
	ServerCrashed EventKind = "server_crashed"
	JobFailed     EventKind = "job_failed"
	JobSucceeded  EventKind = "job_succeeded"
)

type Event struct {
	ID         string    `json:"id"`
	InstanceID string    `json:"instance_id"`
	Kind       EventKind `json:"kind"`
	Operation  string    `json:"operation"`
	CreatedAt  time.Time `json:"created_at"`
}

// Destination is exclusively write-only API input/private manager DB data.
// Endpoint may contain provider URL tokens; it is never returned or logged.
type Destination struct {
	Endpoint     string `json:"endpoint"`
	Secret       string `json:"secret"`
	Recipient    string `json:"recipient"`
	SMTPHost     string `json:"smtp_host"`
	SMTPPort     int    `json:"smtp_port"`
	SMTPUser     string `json:"smtp_user"`
	SMTPPassword string `json:"smtp_password"`
	From         string `json:"from"`
	TLSMode      string `json:"tls_mode"`
}
type ChannelInput struct {
	Label        string       `json:"label"`
	Provider     Provider     `json:"provider"`
	Enabled      bool         `json:"enabled"`
	Events       []EventKind  `json:"events"`
	AllowPrivate bool         `json:"allow_private"`
	Replace      *Destination `json:"replace,omitempty"`
	Clear        bool         `json:"clear"`
}
type Channel struct {
	ID           string      `json:"id"`
	Label        string      `json:"label"`
	Provider     Provider    `json:"provider"`
	Enabled      bool        `json:"enabled"`
	Events       []EventKind `json:"events"`
	AllowPrivate bool        `json:"allow_private"`
	Configured   bool        `json:"configured"`
	SecretSet    bool        `json:"secret_set"`
	RecipientSet bool        `json:"recipient_set"`
}
type Delivery struct {
	ID          int64     `json:"id"`
	ChannelID   string    `json:"channel_id"`
	Provider    Provider  `json:"provider"`
	InstanceID  string    `json:"instance_id"`
	EventKind   EventKind `json:"event_kind"`
	Status      string    `json:"status"`
	Attempts    int       `json:"attempts"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	SafeFailure string    `json:"safe_failure"`
}
type History struct {
	Deliveries []Delivery `json:"deliveries"`
	NextCursor int64      `json:"next_cursor"`
}
type Config struct {
	DB  *sql.DB
	Now func() time.Time
}
type Service struct {
	cfg       Config
	mu        sync.Mutex
	transport func(context.Context, privateChannel, Event) error
}

func New(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, transport: send}
}

type privateChannel struct {
	Channel
	Destination Destination
}

const DeliveryTimeout = 10 * time.Second
const HistoryRetention = 30 * 24 * time.Hour
const MaxQueue = 1000
const MaxChannels = 24
