package instance

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// ManagementConfig is private process-control material. It is never an API,
// audit, job payload or user-config DTO. Formatting intentionally omits secrets.
type ManagementConfig struct {
	InstanceID string `json:"-"`
	Host       string `json:"-"`
	Port       int    `json:"-"`
	Secret     string `json:"-"`
	Protocol   string `json:"-"`
}

func (c ManagementConfig) String() string   { return "[private management configuration]" }
func (c ManagementConfig) GoString() string { return c.String() }
func ManagementProtocol(version string) (string, error) {
	v, ok := parseSettingsVersion(version)
	if !ok || !v.atLeast(1, 21, 9) {
		return "", domain.E(domain.CodeUnsupported, "native management protocol is unavailable for this version")
	}
	if v.atLeast(26, 3, 0) {
		return "3.1.0", nil
	}
	if v.atLeast(26, 2, 0) {
		return "3.0.0", nil
	}
	if v.atLeast(1, 21, 11) {
		return "2.0.0", nil
	}
	return "1.0.0", nil
}
func (s *Service) Management(ctx context.Context, id string) (ManagementConfig, error) {
	if err := validID(id); err != nil {
		return ManagementConfig{}, err
	}
	var cfg ManagementConfig
	cfg.InstanceID = id
	var version string
	err := s.db.QueryRowContext(ctx, `SELECT m.host,m.port,m.secret,m.protocol,i.minecraft_version FROM instance_management m JOIN instance_tcp_ports p ON p.port=m.port AND p.instance_id=m.instance_id AND p.kind=m.kind JOIN instances i ON i.id=m.instance_id WHERE m.instance_id=?`, id).Scan(&cfg.Host, &cfg.Port, &cfg.Secret, &cfg.Protocol, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return ManagementConfig{}, domain.E(domain.CodeUnavailable, "private management configuration is unavailable")
	}
	if err != nil {
		return ManagementConfig{}, fmt.Errorf("read private management configuration: %w", err)
	}
	expected, err := ManagementProtocol(version)
	if err != nil || expected != cfg.Protocol || cfg.Host != "127.0.0.1" || cfg.Port < 1 || cfg.Port > 65535 || len(cfg.Secret) != 40 || strings.Trim(cfg.Secret, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") != "" {
		return ManagementConfig{}, domain.E(domain.CodeUnavailable, "private management configuration is invalid for this instance")
	}
	return cfg, nil
}
func (s *Service) optionalManagement(ctx context.Context, id string) (*ManagementConfig, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM instance_management WHERE instance_id=?`, id).Scan(&count); err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}
	cfg, err := s.Management(ctx, id)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}
func managementSecret() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	out := make([]byte, 0, 40)
	for len(out) < 40 {
		var b [64]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		for _, v := range b {
			if v < 248 {
				out = append(out, alphabet[int(v)%62])
				if len(out) == 40 {
					break
				}
			}
		}
	}
	return string(out), nil
}

// EnsureManagement operates only inside the already-authorized stopped job
// session. Desired private state remains durable if file activation fails; retry
// reuses it and returns success only after canonical materialization succeeds.
func (s *StoppedSession) EnsureManagement(ctx context.Context) (ManagementConfig, error) {
	scope, release, err := s.enter(ctx)
	if err != nil {
		return ManagementConfig{}, err
	}
	defer release()
	operation, cancel := sessionContext(ctx, scope.ctx)
	defer cancel()
	detail, err := scope.service.Get(operation, scope.id)
	if err != nil {
		return ManagementConfig{}, err
	}
	protocol, err := ManagementProtocol(detail.Instance.MinecraftVersion)
	if err != nil {
		return ManagementConfig{}, err
	}
	cfg, err := scope.service.optionalManagement(operation, scope.id)
	if err != nil {
		return ManagementConfig{}, err
	}
	if cfg == nil {
		secret, e := managementSecret()
		if e != nil {
			return ManagementConfig{}, fmt.Errorf("generate private management secret: %w", e)
		}
		for attempt := 0; attempt < 64; attempt++ {
			listener, e := (&net.ListenConfig{}).Listen(operation, "tcp4", "127.0.0.1:0")
			if e != nil {
				return ManagementConfig{}, domain.E(domain.CodeUnavailable, "a private management port is unavailable")
			}
			port := listener.Addr().(*net.TCPAddr).Port
			tx, e := scope.service.db.BeginTx(operation, nil)
			if e != nil {
				listener.Close()
				return ManagementConfig{}, e
			}
			var inserted int64
			res, e := tx.ExecContext(operation, `INSERT INTO instance_tcp_ports(port,instance_id,kind) VALUES(?,?,'management') ON CONFLICT DO NOTHING`, port, scope.id)
			if e == nil {
				inserted, e = res.RowsAffected()
			}
			if e == nil && inserted == 1 {
				_, e = tx.ExecContext(operation, `INSERT INTO instance_management(instance_id,host,port,secret,protocol) VALUES(?,'127.0.0.1',?,?,?)`, scope.id, port, secret, protocol)
			}
			if e != nil || inserted != 1 {
				tx.Rollback()
				listener.Close()
				if e != nil {
					return ManagementConfig{}, domain.E(domain.CodeUnavailable, "private management reservation failed")
				}
				continue
			}
			e = tx.Commit()
			listener.Close()
			if e != nil {
				return ManagementConfig{}, fmt.Errorf("commit private management reservation: %w", e)
			}
			cfg = &ManagementConfig{InstanceID: scope.id, Host: "127.0.0.1", Port: port, Secret: secret, Protocol: protocol}
			break
		}
		if cfg == nil {
			return ManagementConfig{}, domain.E(domain.CodePortInUse, "no private management port could be reserved")
		}
	}
	if err = scope.service.materialize(operation, scope.id); err != nil {
		return ManagementConfig{}, err
	}
	return *cfg, nil
}
func propertiesWithManagement(i domain.Instance, c domain.InstanceConfig, management *ManagementConfig) []byte {
	raw := Properties(i, c)
	if management == nil {
		return raw
	}
	raw = []byte(strings.Replace(string(raw), "management-server-enabled=false\n", "management-server-enabled=true\n", 1))
	for _, pair := range [][2]string{{"management-server-host", management.Host}, {"management-server-port", strconv.Itoa(management.Port)}, {"management-server-secret", management.Secret}, {"management-server-tls-enabled", "false"}, {"management-server-allowed-origins", ""}} {
		raw = append(raw, []byte(pair[0]+"="+propertyEscape(pair[1])+"\n")...)
	}
	return raw
}
