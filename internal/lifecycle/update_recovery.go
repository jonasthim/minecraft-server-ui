package lifecycle

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// RecoverUpdateProcess signals only the exact returned NEW process identity
// retained by the coordinator. Matching runtime/cwd alone never proves a game
// generation. Uncertain identities remain an honest read-only administrator hold.
func (s *Service) RecoverUpdateProcess(ctx context.Context, id, transaction string) (string, error) {
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT t.journal_json FROM update_transactions t JOIN lifecycle_installations l ON l.instance_id=t.instance_id WHERE t.id=? AND t.instance_id=? AND t.kind='update' AND t.state IN ('boot_attempted','blocked') AND json_extract(t.journal_json,'$.boot_attempted')=1 AND json_extract(t.journal_json,'$.after.generation')=l.generation`, transaction, id).Scan(&raw); err != nil {
		return "", err
	}
	var receipt struct {
		Started *domain.ProcessIdentity `json:"started_identity"`
	}
	if json.Unmarshal([]byte(raw), &receipt) != nil {
		return "", domain.E(domain.CodeConflict, "malformed attempted-start receipt")
	}
	identity, err := s.native.Identity(ctx, id)
	if os.IsNotExist(err) {
		return "Managed JVM is stopped; NEW selection and current data retained for explicit recovery.", nil
	}
	if err != nil {
		return "Managed JVM identity is unknown; no signal or data/configuration write was attempted. Inspect and stop explicitly.", nil
	}
	observation, err := s.native.Inspect(ctx, identity)
	if err != nil || observation.State == domain.StateUnknown {
		return "Managed JVM state is unknown; no signal or data/configuration write was attempted. Inspect and stop explicitly.", nil
	}
	if !observation.ProcessRunning {
		return "Managed JVM is stopped; NEW selection and current data retained for explicit recovery.", nil
	}
	if receipt.Started == nil || receipt.Started.InstanceID != id || receipt.Started.PID <= 0 || receipt.Started.StartIdentity == "" || !reflect.DeepEqual(identity, *receipt.Started) {
		return "JVM remains running; no exact retained NEW start identity proves stop authority. Stop explicitly before confirmed instance rollback.", nil
	}
	stop, cancel := context.WithTimeout(ctx, 130*time.Second)
	defer cancel()
	if err = s.native.Stop(stop, identity); err != nil {
		return "Exact attempted NEW JVM remains running or unverified after stop failure. Inspect and stop explicitly before rollback.", nil
	}
	current, err := s.native.Identity(ctx, id)
	if os.IsNotExist(err) {
		return "Exact attempted NEW JVM stopped; current data retained for explicit recovery.", nil
	}
	if err == nil {
		observed, err := s.native.Inspect(ctx, current)
		if err == nil && !observed.ProcessRunning {
			return "Exact attempted NEW JVM stopped; current data retained for explicit recovery.", nil
		}
	}
	return "Stop was requested for the exact NEW identity, but stopped state is unverified. Inspect before rollback.", nil
}
