package jobs

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

var admissionKey = regexp.MustCompile(`^[a-zA-Z0-9_:.-]{1,256}$`)

// EnqueueIdempotent is an internal producer capability, never a public generic
// enqueue API. A durable producer must persist its key before calling this.
// The exact request and key are admitted atomically; retries return the same
// job even after completion. Keys remain tombstones after history retention.
func (s *Service) EnqueueIdempotent(ctx context.Context, key string, r Request) (Job, error) {
	if !admissionKey.MatchString(key) {
		return Job{}, domain.E(domain.CodeValidationFailed, "invalid private admission key")
	}
	return s.enqueue(ctx, r, key)
}
func fingerprint(r Request) (string, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(r.Payload))
	decoder.UseNumber()
	if e := decoder.Decode(&value); e != nil {
		return "", e
	}
	canonical, e := json.Marshal(value)
	if e != nil {
		return "", e
	}
	raw, e := json.Marshal(struct {
		Instance, Kind string
		Payload        json.RawMessage
		Actor          int64
		Cancellable    bool
	}{r.InstanceID, r.Kind, canonical, r.RequestedBy, r.Cancellable})
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// insertAdmission runs under queue admission locking. No callbacks or event
// publication occur inside this transaction.
func (s *Service) insertAdmission(ctx context.Context, r Request, key string) (string, error) {
	hash, e := fingerprint(r)
	if e != nil {
		return "", e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return "", fmt.Errorf("begin job admission: %w", e)
	}
	defer tx.Rollback()
	if key != "" {
		var saved, id string
		e = tx.QueryRowContext(ctx, `SELECT fingerprint,job_id FROM job_admissions WHERE admission_key=?`, key).Scan(&saved, &id)
		if e == nil {
			if saved != hash {
				return "", domain.E(domain.CodeConflict, "admission key already belongs to a different request")
			}
			return id, nil
		}
		if e != sql.ErrNoRows {
			return "", fmt.Errorf("read job admission: %w", e)
		}
	}
	var raw [16]byte
	if _, e = rand.Read(raw[:]); e != nil {
		return "", fmt.Errorf("job id: %w", e)
	}
	id := hex.EncodeToString(raw[:])
	var instance any
	if r.InstanceID != "" {
		instance = r.InstanceID
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO jobs(id,instance_id,kind,state,created_at,payload,requested_by,cancellable) VALUES(?,?,?,'queued',?,?,?,?)`, id, instance, r.Kind, stamp(), string(r.Payload), r.RequestedBy, r.Cancellable); e != nil {
		return "", fmt.Errorf("enqueue job: %w", e)
	}
	if key != "" {
		if _, e = tx.ExecContext(ctx, `INSERT INTO job_admissions(admission_key,fingerprint,job_id,created_at) VALUES(?,?,?,?)`, key, hash, id, stamp()); e != nil {
			return "", fmt.Errorf("record job admission: %w", e)
		}
	}
	if e = tx.Commit(); e != nil {
		return "", fmt.Errorf("commit job admission: %w", e)
	}
	return id, nil
}
