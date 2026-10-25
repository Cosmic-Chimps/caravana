package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The ring buffer of feature 025 (migration 0019). The sampler writes once a
// minute; a 10-minute slot is folded into one row as it ends. Every number a
// source didn't give is nil and stored as NULL: a gap, never a zero.

const (
	// MinuteRes and SlotRes are the two resolutions, in seconds.
	MinuteRes = 60
	SlotRes   = 600

	// MinuteKeep and SlotKeep are how long each kind of row is kept.
	MinuteKeep = 48 * time.Hour
	SlotKeep   = 8 * 24 * time.Hour
)

// MetricsSeries is one app service ever sampled.
type MetricsSeries struct {
	ID      int64
	AppID   string
	Service string
}

// MetricsSample is one service's row for a slot: a minute (Minutes is 1) or
// the fold of a 10-minute slot. A nil field is NULL. Layout is the ID that
// EnsureLayout gave the bucket bounds the Buckets text is for.
type MetricsSample struct {
	Series           int64
	TS               time.Time // the slot's start, UTC, whole seconds
	Minutes          int
	HTTPSecs         *int64
	Requests         *int64
	Errors           *int64
	Layout           *int64
	Buckets          *string
	CPUMHz           *int64
	CPUReservedMHz   *int64
	MemoryMB         *int64
	MemoryLimitMB    *int64
	MemoryReservedMB *int64
	Copies           *int64
	Restarts         *int64
}

// HostSample is a node's row for a slot.
type HostSample struct {
	Node             string
	TS               time.Time
	MemoryUsedMB     *int64
	MemoryReservedMB *int64
	MemoryTotalMB    *int64
	CPUUsedMHz       *int64
	CPUReservedMHz   *int64
	CPUTotalMHz      *int64
	CPUCores         *int64
}

// DiskSample is one watched filesystem of a node for a slot. Holds is "nomad",
// "docker" or "nomad,docker".
type DiskSample struct {
	Node    string
	TS      time.Time
	Mount   string
	Holds   string
	UsedMB  int64
	TotalMB int64
	FreeMB  int64
}

// MetricsRows is what one resolution of a tick writes.
type MetricsRows struct {
	Samples []MetricsSample
	Hosts   []HostSample
	Disks   []DiskSample
}

// MetricsTick is everything one sampler tick writes, in one transaction.
type MetricsTick struct {
	// At is the tick's time: the first tick ever records it as the start of
	// the baseline, and Prune counts back from it.
	At time.Time
	// Minute holds the minute rows (res 60).
	Minute MetricsRows
	// Fold holds the rows of a 10-minute slot that has just ended (res 600).
	// They replace any row already there for the slot.
	Fold MetricsRows
	// Prune drops minute rows older than MinuteKeep and slot rows older than
	// SlotKeep, both counted from At; a row exactly at the edge is kept.
	Prune bool
}

// EnsureSeries returns the ID of an app service's series, creating it the
// first time.
func (s *Store) EnsureSeries(ctx context.Context, appID, service string) (int64, error) {
	var id int64
	// The no-op update makes RETURNING answer for a row that already exists.
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO metrics_series (app_id, service) VALUES (?, ?)
		ON CONFLICT (app_id, service) DO UPDATE SET service = excluded.service
		RETURNING id`, appID, service).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("ensure metrics series %s/%s: %w", appID, service, err)
	}
	return id, nil
}

// ListMetricsSeries returns every series, by app and service.
func (s *Store) ListMetricsSeries(ctx context.Context) ([]MetricsSeries, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, app_id, service FROM metrics_series ORDER BY app_id, service")
	if err != nil {
		return nil, fmt.Errorf("list metrics series: %w", err)
	}
	defer rows.Close()
	var out []MetricsSeries
	for rows.Next() {
		var m MetricsSeries
		if err := rows.Scan(&m.ID, &m.AppID, &m.Service); err != nil {
			return nil, fmt.Errorf("list metrics series: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list metrics series: %w", err)
	}
	return out, nil
}

// EnsureLayout returns the ID of a set of histogram bounds (seconds,
// comma-separated, +Inf implied), adding it the first time.
func (s *Store) EnsureLayout(ctx context.Context, bounds string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO metrics_layouts (bounds) VALUES (?)
		ON CONFLICT (bounds) DO UPDATE SET bounds = excluded.bounds
		RETURNING id`, bounds).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("ensure metrics layout %q: %w", bounds, err)
	}
	return id, nil
}

// MetricsLayouts returns each layout's bounds by ID, for reading the buckets
// of a row.
func (s *Store) MetricsLayouts(ctx context.Context) (map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, bounds FROM metrics_layouts")
	if err != nil {
		return nil, fmt.Errorf("list metrics layouts: %w", err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var bounds string
		if err := rows.Scan(&id, &bounds); err != nil {
			return nil, fmt.Errorf("list metrics layouts: %w", err)
		}
		out[id] = bounds
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list metrics layouts: %w", err)
	}
	return out, nil
}

const sampleColumns = `series, res, ts, minutes, http_secs, requests, errors, layout, buckets,
	cpu_mhz, cpu_reserved_mhz, memory_mb, memory_limit_mb, memory_reserved_mb, copies, restarts`

// WriteMetricsTick writes a tick in one transaction: the minute rows, the
// fold's rows (each replacing the row of its slot), the prune when asked, and
// the start of the baseline the first time. A row written twice for the same
// slot keeps the later one.
func (s *Store) WriteMetricsTick(ctx context.Context, t MetricsTick) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("write metrics tick: %w", err)
	}
	defer tx.Rollback() // does nothing once Commit succeeds

	if err := writeMetricsRows(ctx, tx, MinuteRes, t.Minute); err != nil {
		return fmt.Errorf("write metrics tick: minute rows: %w", err)
	}
	if err := writeMetricsRows(ctx, tx, SlotRes, t.Fold); err != nil {
		return fmt.Errorf("write metrics tick: fold: %w", err)
	}
	if t.Prune {
		if err := pruneMetrics(ctx, tx, t.At); err != nil {
			return fmt.Errorf("write metrics tick: prune: %w", err)
		}
	}
	if !t.At.IsZero() {
		if _, err := tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO metrics_state (id, started_at) VALUES (1, ?)", formatTime(t.At)); err != nil {
			return fmt.Errorf("write metrics tick: start: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("write metrics tick: %w", err)
	}
	return nil
}

func writeMetricsRows(ctx context.Context, tx *sql.Tx, res int, rows MetricsRows) error {
	for _, m := range rows.Samples {
		minutes := m.Minutes
		if minutes == 0 {
			minutes = 1
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO metrics_samples (`+sampleColumns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (series, res, ts) DO UPDATE SET
				minutes = excluded.minutes, http_secs = excluded.http_secs,
				requests = excluded.requests, errors = excluded.errors, layout = excluded.layout,
				buckets = excluded.buckets, cpu_mhz = excluded.cpu_mhz,
				cpu_reserved_mhz = excluded.cpu_reserved_mhz, memory_mb = excluded.memory_mb,
				memory_limit_mb = excluded.memory_limit_mb,
				memory_reserved_mb = excluded.memory_reserved_mb, copies = excluded.copies,
				restarts = excluded.restarts`,
			m.Series, res, m.TS.Unix(), minutes, m.HTTPSecs, m.Requests, m.Errors, m.Layout, m.Buckets,
			m.CPUMHz, m.CPUReservedMHz, m.MemoryMB, m.MemoryLimitMB, m.MemoryReservedMB,
			m.Copies, m.Restarts); err != nil {
			return fmt.Errorf("series %d: %w", m.Series, err)
		}
	}
	for _, h := range rows.Hosts {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO metrics_host (node, res, ts, memory_used_mb, memory_reserved_mb,
				memory_total_mb, cpu_used_mhz, cpu_reserved_mhz, cpu_total_mhz, cpu_cores)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (node, res, ts) DO UPDATE SET
				memory_used_mb = excluded.memory_used_mb,
				memory_reserved_mb = excluded.memory_reserved_mb,
				memory_total_mb = excluded.memory_total_mb, cpu_used_mhz = excluded.cpu_used_mhz,
				cpu_reserved_mhz = excluded.cpu_reserved_mhz, cpu_total_mhz = excluded.cpu_total_mhz,
				cpu_cores = excluded.cpu_cores`,
			h.Node, res, h.TS.Unix(), h.MemoryUsedMB, h.MemoryReservedMB, h.MemoryTotalMB,
			h.CPUUsedMHz, h.CPUReservedMHz, h.CPUTotalMHz, h.CPUCores); err != nil {
			return fmt.Errorf("host %s: %w", h.Node, err)
		}
	}
	for _, d := range rows.Disks {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO metrics_disks (node, res, ts, mount, holds, used_mb, total_mb, free_mb)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (node, res, ts, mount) DO UPDATE SET
				holds = excluded.holds, used_mb = excluded.used_mb, total_mb = excluded.total_mb,
				free_mb = excluded.free_mb`,
			d.Node, res, d.TS.Unix(), d.Mount, d.Holds, d.UsedMB, d.TotalMB, d.FreeMB); err != nil {
			return fmt.Errorf("disk %s on %s: %w", d.Mount, d.Node, err)
		}
	}
	return nil
}

// pruneMetrics deletes what is older than the keep times. A row whose slot
// starts exactly at the edge stays.
func pruneMetrics(ctx context.Context, tx *sql.Tx, now time.Time) error {
	minuteEdge := now.Add(-MinuteKeep).Unix()
	slotEdge := now.Add(-SlotKeep).Unix()
	for _, table := range []string{"metrics_samples", "metrics_host", "metrics_disks"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE res = ? AND ts < ?", MinuteRes, minuteEdge); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE res = ? AND ts < ?", SlotRes, slotEdge); err != nil {
			return err
		}
	}
	return nil
}

// MinuteRows returns a series' minute rows with from <= ts < to, oldest first.
func (s *Store) MinuteRows(ctx context.Context, series int64, from, to time.Time) ([]MetricsSample, error) {
	return s.sampleRows(ctx, MinuteRes, series, from, to)
}

// SlotRows returns a series' 10-minute rows with from <= ts < to, oldest first.
func (s *Store) SlotRows(ctx context.Context, series int64, from, to time.Time) ([]MetricsSample, error) {
	return s.sampleRows(ctx, SlotRes, series, from, to)
}

func (s *Store) sampleRows(ctx context.Context, res int, series int64, from, to time.Time) ([]MetricsSample, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT series, ts, minutes, http_secs, requests, errors, layout, buckets,
			cpu_mhz, cpu_reserved_mhz, memory_mb, memory_limit_mb, memory_reserved_mb, copies, restarts
		FROM metrics_samples WHERE series = ? AND res = ? AND ts >= ? AND ts < ? ORDER BY ts`,
		series, res, from.Unix(), to.Unix())
	if err != nil {
		return nil, fmt.Errorf("read metrics rows of series %d: %w", series, err)
	}
	defer rows.Close()
	var out []MetricsSample
	for rows.Next() {
		var m MetricsSample
		var ts int64
		var httpSecs, requests, errs, layout, cpu, cpuRes, mem, memLimit, memRes, copies, restarts sql.Null[int64]
		var buckets sql.NullString
		if err := rows.Scan(&m.Series, &ts, &m.Minutes, &httpSecs, &requests, &errs, &layout, &buckets,
			&cpu, &cpuRes, &mem, &memLimit, &memRes, &copies, &restarts); err != nil {
			return nil, fmt.Errorf("read metrics rows of series %d: %w", series, err)
		}
		m.TS = time.Unix(ts, 0).UTC()
		m.HTTPSecs, m.Requests, m.Errors, m.Layout = nullable(httpSecs), nullable(requests), nullable(errs), nullable(layout)
		if buckets.Valid {
			m.Buckets = &buckets.String
		}
		m.CPUMHz, m.CPUReservedMHz = nullable(cpu), nullable(cpuRes)
		m.MemoryMB, m.MemoryLimitMB, m.MemoryReservedMB = nullable(mem), nullable(memLimit), nullable(memRes)
		m.Copies, m.Restarts = nullable(copies), nullable(restarts)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read metrics rows of series %d: %w", series, err)
	}
	return out, nil
}

// nullable turns a scanned column into a pointer: nil for NULL.
func nullable(n sql.Null[int64]) *int64 {
	if !n.Valid {
		return nil
	}
	return &n.V
}

const hostColumns = `node, ts, memory_used_mb, memory_reserved_mb, memory_total_mb,
	cpu_used_mhz, cpu_reserved_mhz, cpu_total_mhz, cpu_cores`

// HostRows returns the host rows of one resolution (MinuteRes or SlotRes) with
// from <= ts < to, oldest first, then by node.
func (s *Store) HostRows(ctx context.Context, res int, from, to time.Time) ([]HostSample, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+hostColumns+`
		FROM metrics_host WHERE res = ? AND ts >= ? AND ts < ? ORDER BY ts, node`,
		res, from.Unix(), to.Unix())
	if err != nil {
		return nil, fmt.Errorf("read host rows: %w", err)
	}
	defer rows.Close()
	var out []HostSample
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, fmt.Errorf("read host rows: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read host rows: %w", err)
	}
	return out, nil
}

// NewestHost returns the newest minute row of the host (the later node's when
// there are several), or ErrNotFound when nothing was sampled.
func (s *Store) NewestHost(ctx context.Context) (HostSample, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+hostColumns+`
		FROM metrics_host WHERE res = ? ORDER BY ts DESC, node LIMIT 1`, MinuteRes)
	h, err := scanHost(row)
	if errors.Is(err, sql.ErrNoRows) {
		return HostSample{}, fmt.Errorf("newest host sample: %w", ErrNotFound)
	}
	if err != nil {
		return HostSample{}, fmt.Errorf("newest host sample: %w", err)
	}
	return h, nil
}

func scanHost(r interface{ Scan(...any) error }) (HostSample, error) {
	var h HostSample
	var ts int64
	var memUsed, memRes, memTotal, cpuUsed, cpuRes, cpuTotal, cores sql.Null[int64]
	if err := r.Scan(&h.Node, &ts, &memUsed, &memRes, &memTotal, &cpuUsed, &cpuRes, &cpuTotal, &cores); err != nil {
		return HostSample{}, err
	}
	h.TS = time.Unix(ts, 0).UTC()
	h.MemoryUsedMB, h.MemoryReservedMB, h.MemoryTotalMB = nullable(memUsed), nullable(memRes), nullable(memTotal)
	h.CPUUsedMHz, h.CPUReservedMHz, h.CPUTotalMHz, h.CPUCores = nullable(cpuUsed), nullable(cpuRes), nullable(cpuTotal), nullable(cores)
	return h, nil
}

// DiskRows returns the disk rows of one resolution (MinuteRes or SlotRes) with
// from <= ts < to, oldest first, then by node and mount.
func (s *Store) DiskRows(ctx context.Context, res int, from, to time.Time) ([]DiskSample, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT node, ts, mount, holds, used_mb, total_mb, free_mb
		FROM metrics_disks WHERE res = ? AND ts >= ? AND ts < ? ORDER BY ts, node, mount`,
		res, from.Unix(), to.Unix())
	if err != nil {
		return nil, fmt.Errorf("read disk rows: %w", err)
	}
	defer rows.Close()
	var out []DiskSample
	for rows.Next() {
		var d DiskSample
		var ts int64
		if err := rows.Scan(&d.Node, &ts, &d.Mount, &d.Holds, &d.UsedMB, &d.TotalMB, &d.FreeMB); err != nil {
			return nil, fmt.Errorf("read disk rows: %w", err)
		}
		d.TS = time.Unix(ts, 0).UTC()
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read disk rows: %w", err)
	}
	return out, nil
}

// MetricsStartedAt returns when the first tick ever was written, or
// ErrNotFound before any.
func (s *Store) MetricsStartedAt(ctx context.Context) (time.Time, error) {
	var text string
	err := s.db.QueryRowContext(ctx, "SELECT started_at FROM metrics_state WHERE id = 1").Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, fmt.Errorf("metrics start: %w", ErrNotFound)
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("metrics start: %w", err)
	}
	return parseTime(text)
}
