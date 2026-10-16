package store

import (
	"context"
	"fmt"
	"time"
)

// VolumeSize is the last measured size of one volume.
type VolumeSize struct {
	// KB is the size in kilobytes, the unit du reports.
	KB         int64
	MeasuredAt time.Time
}

// SaveVolumeSize records the size of an app's volume, replacing the one
// before it. A measurement older than the stored one is ignored, so two
// copies read in any order leave the newest.
func (s *Store) SaveVolumeSize(ctx context.Context, appID, volume string, kb int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO volume_sizes (app_id, volume, kilobytes, measured_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (app_id, volume) DO UPDATE SET kilobytes = excluded.kilobytes, measured_at = excluded.measured_at
		WHERE excluded.measured_at >= volume_sizes.measured_at`,
		appID, volume, kb, formatTime(at))
	if err != nil {
		return fmt.Errorf("save size of volume %q of %q: %w", volume, appID, err)
	}
	return nil
}

// VolumeSizes returns the last measured size of each of an app's volumes,
// by volume name. A volume never measured is missing from the map.
func (s *Store) VolumeSizes(ctx context.Context, appID string) (map[string]VolumeSize, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT volume, kilobytes, measured_at FROM volume_sizes WHERE app_id = ?", appID)
	if err != nil {
		return nil, fmt.Errorf("volume sizes of %q: %w", appID, err)
	}
	defer rows.Close()
	out := map[string]VolumeSize{}
	for rows.Next() {
		var name, measured string
		var size VolumeSize
		if err := rows.Scan(&name, &size.KB, &measured); err != nil {
			return nil, fmt.Errorf("volume sizes of %q: %w", appID, err)
		}
		if size.MeasuredAt, err = parseTime(measured); err != nil {
			return nil, fmt.Errorf("volume sizes of %q: %w", appID, err)
		}
		out[name] = size
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("volume sizes of %q: %w", appID, err)
	}
	return out, nil
}
