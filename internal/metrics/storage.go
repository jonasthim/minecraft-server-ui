package metrics

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"math"
	"time"
)

type aggregate struct {
	Point                                 Point `json:"point"`
	CPU, Resident, Private, Disk, Players float64
	Missing                               bool
}

func (s *Service) store(ctx context.Context, id string, p Point) error {
	slot := p.CollectedAt.Unix() / 30 * 30
	hour := slot / 3600 * 3600
	tx, err := s.cfg.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin metric sample: %w", err)
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM instances WHERE id=?`, id).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return tx.Commit()
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO metric_samples(instance_id,slot,sample_json) VALUES(?,?,?)`, id, slot, string(raw))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return tx.Commit()
	}
	a := aggregate{Point: Point{CollectedAt: time.Unix(hour, 0).UTC(), Expected: 120, Sources: []Source{}}}
	var old string
	err = tx.QueryRowContext(ctx, `SELECT aggregate_json FROM metric_hours WHERE instance_id=? AND slot=?`, id, hour).Scan(&old)
	if err == nil {
		if json.Unmarshal([]byte(old), &a) != nil {
			return fmt.Errorf("invalid stored metric aggregate")
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	a.Point.Counts.Samples++
	if p.CPUPercent != nil {
		a.CPU += *p.CPUPercent
		a.Point.Counts.CPU++
	}
	if p.ResidentBytes != nil {
		a.Resident += float64(*p.ResidentBytes)
		a.Point.Counts.Resident++
	}
	if p.PrivateBytes != nil {
		a.Private += float64(*p.PrivateBytes)
		a.Point.Counts.Private++
	}
	if p.DiskBytes != nil {
		a.Disk += float64(*p.DiskBytes)
		a.Point.Counts.Disk++
	}
	if p.Players != nil {
		a.Players += *p.Players
		a.Point.Counts.Players++
	}
	// Keep latest typed provenance per field/source/gap, bounded by five fields and
	// finite collector reasons, rather than retaining every player or process ID.
	for _, source := range p.Sources {
		found := false
		for i, prior := range a.Point.Sources {
			if prior.Field == source.Field && prior.Kind == source.Kind && prior.GapReason == source.GapReason {
				a.Point.Sources[i] = source
				found = true
				break
			}
		}
		if !found && len(a.Point.Sources) < 40 {
			a.Point.Sources = append(a.Point.Sources, source)
		}
	}
	c := a.Point.Counts
	if c.CPU > 0 {
		v := a.CPU / float64(c.CPU)
		a.Point.CPUPercent = &v
	}
	if c.Resident > 0 {
		v := int64(math.Round(a.Resident / float64(c.Resident)))
		a.Point.ResidentBytes = &v
	}
	if c.Private > 0 {
		v := int64(math.Round(a.Private / float64(c.Private)))
		a.Point.PrivateBytes = &v
	}
	if c.Disk > 0 {
		v := int64(math.Round(a.Disk / float64(c.Disk)))
		a.Point.DiskBytes = &v
	}
	if c.Players > 0 {
		v := a.Players / float64(c.Players)
		a.Point.Players = &v
	}
	a.Missing = a.Missing || p.Gap
	a.Point.Gap = c.Samples < 120 || c.CPU < c.Samples || c.Resident < c.Samples || c.Disk < c.Samples || c.Players < c.Samples || a.Missing
	raw, err = json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO metric_hours(instance_id,slot,aggregate_json)VALUES(?,?,?) ON CONFLICT(instance_id,slot) DO UPDATE SET aggregate_json=excluded.aggregate_json`, id, hour, string(raw))
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Service) Retain(ctx context.Context) error {
	now := s.cfg.Now()
	tx, err := s.cfg.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM metric_samples WHERE slot<?`, now.Add(-RawRetention).Unix()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM metric_hours WHERE slot<?`, now.Add(-HourRetention).Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Service) Query(ctx context.Context, q Query) (Page, error) {
	page := Page{InstanceID: q.InstanceID, Resolution: q.Resolution, Points: []Point{}, CPUConvention: "100% = one fully busy logical CPU; multithreaded JVMs may exceed 100%. RSS is resident working set; private bytes are Windows committed private memory, not Java heap."}
	if domain.ValidateInstanceID(q.InstanceID) != nil {
		return page, domain.E(domain.CodeValidationFailed, "invalid instance id")
	}
	step := Interval
	retention := RawRetention
	table, col := "metric_samples", "sample_json"
	if q.Resolution == "hourly" {
		step = time.Hour
		retention = HourRetention
		table, col = "metric_hours", "aggregate_json"
	} else if q.Resolution != "raw" {
		return page, domain.E(domain.CodeValidationFailed, "resolution must be raw or hourly")
	}
	now := s.cfg.Now()
	if q.From.IsZero() || q.To.IsZero() || !q.To.After(q.From) || q.From.Before(now.Add(-retention)) || q.To.After(now.Add(time.Minute)) || q.To.Sub(q.From) > time.Duration(MaxPoints)*step {
		return page, domain.E(domain.CodeValidationFailed, "metric range exceeds retention or 1000 point limit")
	}
	if s.cfg.Instances == nil {
		return page, domain.E(domain.CodeUnavailable, "instance service unavailable")
	}
	if _, err := s.cfg.Instances.Get(ctx, q.InstanceID); err != nil {
		return page, err
	}
	page.IntervalSeconds = int(step / time.Second)
	start := q.From.Truncate(step)
	end := q.To
	if (end.Sub(start)+step-1)/step > MaxPoints {
		return page, domain.E(domain.CodeValidationFailed, "metric range exceeds 1000 point limit")
	}
	rows, err := s.cfg.DB.QueryContext(ctx, `SELECT slot,`+col+` FROM `+table+` WHERE instance_id=? AND slot>=? AND slot<? ORDER BY slot LIMIT 1000`, q.InstanceID, start.Unix(), end.Unix())
	if err != nil {
		return page, err
	}
	points := map[int64]Point{}
	for rows.Next() {
		var slot int64
		var raw string
		if err = rows.Scan(&slot, &raw); err != nil {
			break
		}
		var p Point
		if q.Resolution == "hourly" {
			var a aggregate
			err = json.Unmarshal([]byte(raw), &a)
			p = a.Point
		} else {
			err = json.Unmarshal([]byte(raw), &p)
		}
		if err != nil {
			break
		}
		points[slot] = p
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return page, fmt.Errorf("read metric history: %w", err)
	}
	for at := start; at.Before(end); at = at.Add(step) {
		p, ok := points[at.Unix()]
		if !ok {
			p = Point{CollectedAt: at.UTC(), Expected: int(step / Interval), Gap: true, Sources: []Source{}}
		}
		if !ok {
			for _, field := range []Field{CPU, Resident, Private, Disk, Players} {
				p.Sources = append(p.Sources, Source{Field: field, Kind: "sampling", ObservedAt: at.UTC(), GapReason: "no_observation"})
			}
		}
		page.Points = append(page.Points, p)
	}
	return page, nil
}
