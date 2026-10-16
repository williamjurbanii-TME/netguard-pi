package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// The daily table is a rollup of the query log: one row per local day,
// device, domain, reason and status, with a lookup count. It is ~16x smaller
// than the raw log, so views over several days (Ads & Tracking, the Sites
// view, device summaries) read it instead of millions of raw rows. It is
// maintained in WriteBatch and kept in step by merges, retagging and pruning.
const dailySchema = `
CREATE TABLE IF NOT EXISTS daily (
	day INTEGER NOT NULL,      -- unix seconds of local midnight
	device_id TEXT NOT NULL,
	domain TEXT NOT NULL,
	reason TEXT NOT NULL,
	status TEXT NOT NULL,
	lookups INTEGER NOT NULL,
	first_ts INTEGER NOT NULL,
	last_ts INTEGER NOT NULL,
	PRIMARY KEY (day, device_id, domain, reason, status)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_daily_dev ON daily(device_id, day);
CREATE INDEX IF NOT EXISTS idx_daily_reason ON daily(reason, day);
`

// rollupAfter: time ranges longer than this are answered from the daily
// rollup (whole local days, so "7 days" starts at midnight 7 days ago).
const rollupAfter = 48 * 3600

func useDaily(since int64) bool { return time.Now().Unix()-since > rollupAfter }

// dayStart returns local midnight of the day containing ts.
func dayStart(ts int64) int64 {
	t := time.Unix(ts, 0)
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location()).Unix()
}

const dailyBlocked = `SUM(CASE WHEN status='blocked' THEN lookups ELSE 0 END)`

const dailyMergeOnConflict = ` ON CONFLICT(day, device_id, domain, reason, status) DO UPDATE SET
	lookups=lookups+excluded.lookups, first_ts=MIN(first_ts, excluded.first_ts), last_ts=MAX(last_ts, excluded.last_ts)`

type dailyKey struct {
	day                            int64
	device, domain, reason, status string
}

type dailyVal struct{ n, first, last int64 }

// addDaily folds query log entries into the rollup (inside the caller's tx).
func addDaily(tx *sql.Tx, entries []QueryEntry) error {
	agg := map[dailyKey]*dailyVal{}
	for _, e := range entries {
		k := dailyKey{dayStart(e.TS), e.DeviceID, e.Domain, e.Reason, e.Status}
		v := agg[k]
		if v == nil {
			v = &dailyVal{first: e.TS, last: e.TS}
			agg[k] = v
		}
		v.n++
		v.first = min(v.first, e.TS)
		v.last = max(v.last, e.TS)
	}
	stmt, err := tx.Prepare(`INSERT INTO daily (day, device_id, domain, reason, status, lookups, first_ts, last_ts)
		VALUES (?,?,?,?,?,?,?,?)` + dailyMergeOnConflict)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for k, v := range agg {
		if _, err := stmt.Exec(k.day, k.device, k.domain, k.reason, k.status, v.n, v.first, v.last); err != nil {
			return err
		}
	}
	return nil
}

// moveDaily re-keys rollup rows matching where (to a new device or reason),
// adding them to any rows already at the destination key.
func moveDaily(tx *sql.Tx, newDevice, newReason, where string, args ...any) error {
	dev, reason := "device_id", "reason"
	var sel []any
	if newDevice != "" {
		dev = "?"
		sel = append(sel, newDevice)
	}
	if newReason != "" {
		reason = "?"
		sel = append(sel, newReason)
	}
	if _, err := tx.Exec(fmt.Sprintf(`INSERT INTO daily (day, device_id, domain, reason, status, lookups, first_ts, last_ts)
		SELECT day, %s, domain, %s, status, lookups, first_ts, last_ts FROM daily WHERE %s`, dev, reason, where)+dailyMergeOnConflict,
		append(sel, args...)...); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM daily WHERE `+where, args...)
	return err
}

// backfillDaily builds the rollup from existing history the first time a
// database is opened by a version that has it.
func (s *Store) backfillDaily() error {
	var have, raw int
	if err := s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM (SELECT 1 FROM daily LIMIT 1)), (SELECT COUNT(*) FROM (SELECT 1 FROM queries LIMIT 1))`).Scan(&have, &raw); err != nil {
		return err
	}
	if have > 0 || raw == 0 {
		return nil
	}
	_, offset := time.Now().Zone()
	_, err := s.db.Exec(`INSERT INTO daily (day, device_id, domain, reason, status, lookups, first_ts, last_ts)
		SELECT ((ts+?)/86400)*86400-?, device_id, domain, reason, status, COUNT(*), MIN(ts), MAX(ts)
		FROM queries WHERE true GROUP BY 1, 2, 3, 4, 5`, offset, offset)
	return err
}

// Warm runs the Overview's queries once so the last day of data is in the
// OS page cache; otherwise the first dashboard load after a restart reads
// it from the SD card and takes seconds.
func (s *Store) Warm() {
	since := time.Now().Add(-24 * time.Hour).Unix()
	_, _ = s.CountsByDevice(since)
	_, _ = s.Hourly("", since)
	_, _ = s.TopDomains("", true, since, 10)
	_, _ = s.TopDomains("", false, since, 10)
}

// Optimize lets SQLite refresh its query planner statistics. Cheap; run periodically.
func (s *Store) Optimize() error {
	_, err := s.db.Exec(`PRAGMA optimize`)
	return err
}

// dailyWhere builds "day>=? [AND device_id=?] [AND domain LIKE ?]" for the rollup.
func dailyWhere(since int64, deviceID, search string) (string, []any) {
	w := []string{"day>=?"}
	args := []any{dayStart(since)}
	if deviceID != "" {
		w = append(w, "device_id=?")
		args = append(args, deviceID)
	}
	if search != "" {
		w = append(w, "domain LIKE ?")
		args = append(args, "%"+search+"%")
	}
	return strings.Join(w, " AND "), args
}
