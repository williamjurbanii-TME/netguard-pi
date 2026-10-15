// Package store persists devices, groups, DHCP leases and the query log in SQLite.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DefaultGroupID is the group unknown devices fall into. It cannot be deleted.
const DefaultGroupID int64 = 1

type Group struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Categories  []string `json:"categories"`
	Services    []string `json:"services"`
	CustomBlock []string `json:"custom_block"`
	CustomAllow []string `json:"custom_allow"`
	SafeSearch  bool     `json:"safe_search"`
	BlockDoH    bool     `json:"block_doh"`
}

// Device is a physical device. It is reached through one or more identifiers
// (see Alias), because phones rotate private MAC addresses.
type Device struct {
	ID             string `json:"id"` // opaque; the first identifier the device was seen with
	Name           string `json:"name"`
	GroupID        int64  `json:"group_id"`
	LastIP         string `json:"last_ip"`
	LastIPv4       string `json:"last_ipv4"`
	Hostname       string `json:"hostname"`
	HostnameSource string `json:"hostname_source"` // mdns, router, netbios, dhcp
	StaticIP       string `json:"static_ip"`
	Paused         bool   `json:"paused"`
	FirstSeen      int64  `json:"first_seen"`
	LastSeen       int64  `json:"last_seen"`
}

// Alias maps a network identifier (a MAC address, "ip:<addr>" when the MAC is
// unknown, or "local") to a device.
type Alias struct {
	Identifier string `json:"identifier"`
	DeviceID   string `json:"device_id"`
	LastIP     string `json:"last_ip"`
	LastSeen   int64  `json:"last_seen"`
}

type Lease struct {
	MAC      string `json:"mac"`
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
	Expires  int64  `json:"expires"`
}

type QueryEntry struct {
	Identifier string `json:"-"` // which alias of the device sent it
	TS         int64  `json:"ts"`
	DeviceID   string `json:"device_id"`
	ClientIP   string `json:"client_ip"`
	Domain     string `json:"domain"`
	QType      string `json:"qtype"`
	Status     string `json:"status"` // allowed, cached, blocked, safesearch, error
	Reason     string `json:"reason"`
	Ms         int64  `json:"ms"`
}

type Seen struct {
	DeviceID string
	IP       string
	TS       int64
}

type Store struct {
	db   *sql.DB
	path string
}

const schema = `
CREATE TABLE IF NOT EXISTS groups (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT UNIQUE NOT NULL,
	categories TEXT NOT NULL DEFAULT '[]',
	services TEXT NOT NULL DEFAULT '[]',
	custom_block TEXT NOT NULL DEFAULT '[]',
	custom_allow TEXT NOT NULL DEFAULT '[]',
	safe_search INTEGER NOT NULL DEFAULT 0,
	block_doh INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS devices (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL DEFAULT '',
	group_id INTEGER NOT NULL DEFAULT 1,
	last_ip TEXT NOT NULL DEFAULT '',
	hostname TEXT NOT NULL DEFAULT '',
	static_ip TEXT NOT NULL DEFAULT '',
	paused INTEGER NOT NULL DEFAULT 0,
	first_seen INTEGER NOT NULL DEFAULT 0,
	last_seen INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS aliases (
	identifier TEXT PRIMARY KEY,
	device_id TEXT NOT NULL,
	last_ip TEXT NOT NULL DEFAULT '',
	last_seen INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_aliases_device ON aliases(device_id);
CREATE TABLE IF NOT EXISTS leases (
	mac TEXT PRIMARY KEY,
	ip TEXT NOT NULL,
	hostname TEXT NOT NULL DEFAULT '',
	expires INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS queries (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts INTEGER NOT NULL,
	device_id TEXT NOT NULL,
	client_ip TEXT NOT NULL,
	domain TEXT NOT NULL,
	qtype TEXT NOT NULL,
	status TEXT NOT NULL,
	reason TEXT NOT NULL DEFAULT '',
	ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_queries_ts ON queries(ts);
CREATE INDEX IF NOT EXISTS idx_queries_dev_ts ON queries(device_id, ts);
CREATE INDEX IF NOT EXISTS idx_queries_reason_ts ON queries(reason, ts);
`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// One connection serialises access; plenty for a home network and avoids
	// SQLITE_BUSY. Never run a query while iterating another's rows.
	db.SetMaxOpenConns(1)
	for _, p := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=NORMAL", "PRAGMA busy_timeout=5000"} {
		if _, err := db.Exec(p); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	s := &Store{db: db, path: path}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := s.seed(); err != nil {
		return nil, fmt.Errorf("seed: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// migrate upgrades databases created by earlier versions.
func (s *Store) migrate() error {
	cols := map[string]bool{}
	rows, err := s.db.Query(`PRAGMA table_info(devices)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		cols[name] = true
	}
	rows.Close()
	for _, c := range []string{"hostname_source", "last_ipv4"} {
		if !cols[c] {
			if _, err := s.db.Exec(`ALTER TABLE devices ADD COLUMN ` + c + ` TEXT NOT NULL DEFAULT ''`); err != nil {
				return err
			}
		}
	}
	// Devices from before aliases existed are reachable through their own ID.
	_, err = s.db.Exec(`INSERT OR IGNORE INTO aliases (identifier, device_id, last_ip, last_seen)
		SELECT id, id, last_ip, last_seen FROM devices WHERE id NOT IN (SELECT device_id FROM aliases)`)
	return err
}

func (s *Store) seed() error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM groups`).Scan(&n); err != nil || n > 0 {
		return err
	}
	defaults := []Group{
		{Name: "Default", Categories: []string{"ads", "malware"}},
		{Name: "Kids", Categories: []string{"ads", "malware", "adult", "gambling"},
			Services: []string{"youtube", "tiktok"}, SafeSearch: true, BlockDoH: true},
		{Name: "School", Categories: []string{"ads", "malware", "adult", "gambling"},
			SafeSearch: true, BlockDoH: true},
		{Name: "TVs", Categories: []string{"ads", "malware", "smarttv"}},
	}
	for i := range defaults {
		if _, err := s.SaveGroup(&defaults[i]); err != nil {
			return err
		}
	}
	return nil
}

// ---- groups ----

func toJSON(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func fromJSON(s string) []string {
	var v []string
	_ = json.Unmarshal([]byte(s), &v)
	if v == nil {
		v = []string{}
	}
	return v
}

func (s *Store) ListGroups() ([]Group, error) {
	rows, err := s.db.Query(`SELECT id, name, categories, services, custom_block, custom_allow, safe_search, block_doh FROM groups ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		var cats, svcs, blk, alw string
		if err := rows.Scan(&g.ID, &g.Name, &cats, &svcs, &blk, &alw, &g.SafeSearch, &g.BlockDoH); err != nil {
			return nil, err
		}
		g.Categories, g.Services, g.CustomBlock, g.CustomAllow = fromJSON(cats), fromJSON(svcs), fromJSON(blk), fromJSON(alw)
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) GetGroup(id int64) (*Group, error) {
	groups, err := s.ListGroups()
	if err != nil {
		return nil, err
	}
	for i := range groups {
		if groups[i].ID == id {
			return &groups[i], nil
		}
	}
	return nil, sql.ErrNoRows
}

// SaveGroup inserts the group when ID is 0, otherwise updates it. Returns the ID.
func (s *Store) SaveGroup(g *Group) (int64, error) {
	args := []any{g.Name, toJSON(g.Categories), toJSON(g.Services), toJSON(g.CustomBlock), toJSON(g.CustomAllow), g.SafeSearch, g.BlockDoH}
	if g.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO groups (name, categories, services, custom_block, custom_allow, safe_search, block_doh) VALUES (?,?,?,?,?,?,?)`, args...)
		if err != nil {
			return 0, err
		}
		g.ID, err = res.LastInsertId()
		return g.ID, err
	}
	_, err := s.db.Exec(`UPDATE groups SET name=?, categories=?, services=?, custom_block=?, custom_allow=?, safe_search=?, block_doh=? WHERE id=?`, append(args, g.ID)...)
	return g.ID, err
}

func (s *Store) DeleteGroup(id int64) error {
	if id == DefaultGroupID {
		return fmt.Errorf("the default group cannot be deleted")
	}
	if _, err := s.db.Exec(`UPDATE devices SET group_id=? WHERE group_id=?`, DefaultGroupID, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM groups WHERE id=?`, id)
	return err
}

// ---- devices ----

const deviceCols = `id, name, group_id, last_ip, last_ipv4, hostname, hostname_source, static_ip, paused, first_seen, last_seen`

func scanDevice(sc interface{ Scan(...any) error }) (Device, error) {
	var d Device
	err := sc.Scan(&d.ID, &d.Name, &d.GroupID, &d.LastIP, &d.LastIPv4, &d.Hostname, &d.HostnameSource, &d.StaticIP, &d.Paused, &d.FirstSeen, &d.LastSeen)
	return d, err
}

func (s *Store) ListDevices() ([]Device, error) {
	rows, err := s.db.Query(`SELECT ` + deviceCols + ` FROM devices ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) GetDevice(id string) (*Device, error) {
	d, err := scanDevice(s.db.QueryRow(`SELECT `+deviceCols+` FROM devices WHERE id=?`, id))
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// InsertDevice creates a device reachable through identifier.
func (s *Store) InsertDevice(d *Device, identifier string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO devices (`+deviceCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, d.Name, d.GroupID, d.LastIP, d.LastIPv4, d.Hostname, d.HostnameSource, d.StaticIP, d.Paused, d.FirstSeen, d.LastSeen); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO aliases (identifier, device_id, last_ip, last_seen) VALUES (?,?,?,?)`,
		identifier, d.ID, d.LastIP, d.LastSeen); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateDevice saves the user-editable fields.
func (s *Store) UpdateDevice(d *Device) error {
	_, err := s.db.Exec(`UPDATE devices SET name=?, group_id=?, static_ip=?, paused=? WHERE id=?`,
		d.Name, d.GroupID, d.StaticIP, d.Paused, d.ID)
	return err
}

func (s *Store) SetHostname(id, hostname, source string) error {
	_, err := s.db.Exec(`UPDATE devices SET hostname=?, hostname_source=? WHERE id=?`, hostname, source, id)
	return err
}

func (s *Store) DeleteDevice(id string) error {
	if _, err := s.db.Exec(`DELETE FROM aliases WHERE device_id=?`, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM devices WHERE id=?`, id)
	return err
}

// ---- aliases ----

func (s *Store) ListAliases() ([]Alias, error) {
	rows, err := s.db.Query(`SELECT identifier, device_id, last_ip, last_seen FROM aliases ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Alias
	for rows.Next() {
		var a Alias
		if err := rows.Scan(&a.Identifier, &a.DeviceID, &a.LastIP, &a.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetAlias points identifier at deviceID, creating or moving the alias.
func (s *Store) SetAlias(identifier, deviceID, ip string, seen int64) error {
	_, err := s.db.Exec(`INSERT INTO aliases (identifier, device_id, last_ip, last_seen) VALUES (?,?,?,?)
		ON CONFLICT(identifier) DO UPDATE SET device_id=excluded.device_id`, identifier, deviceID, ip, seen)
	return err
}

func (s *Store) DeleteAlias(identifier string) error {
	_, err := s.db.Exec(`DELETE FROM aliases WHERE identifier=?`, identifier)
	return err
}

// MergeDevices moves src's identifiers and query history onto dst and
// deletes src. dst keeps its name, group and settings.
func (s *Store) MergeDevices(src, dst string) error {
	if src == dst {
		return fmt.Errorf("cannot merge a device into itself")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`UPDATE aliases SET device_id=? WHERE device_id=?`,
		`UPDATE queries SET device_id=? WHERE device_id=?`,
	} {
		if _, err := tx.Exec(q, dst, src); err != nil {
			return err
		}
	}
	// Keep the most recent sighting and fill in a hostname if dst has none.
	if _, err := tx.Exec(`UPDATE devices SET
			last_seen = MAX(last_seen, (SELECT last_seen FROM devices WHERE id=?)),
			first_seen = MIN(first_seen, (SELECT first_seen FROM devices WHERE id=?)),
			hostname = CASE WHEN hostname='' THEN (SELECT hostname FROM devices WHERE id=?) ELSE hostname END,
			hostname_source = CASE WHEN hostname='' THEN (SELECT hostname_source FROM devices WHERE id=?) ELSE hostname_source END
		WHERE id=?`, src, src, src, src, dst); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM devices WHERE id=?`, src); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- leases ----

func (s *Store) ListLeases() ([]Lease, error) {
	rows, err := s.db.Query(`SELECT mac, ip, hostname, expires FROM leases ORDER BY ip`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Lease
	for rows.Next() {
		var l Lease
		if err := rows.Scan(&l.MAC, &l.IP, &l.Hostname, &l.Expires); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) UpsertLease(l Lease) error {
	_, err := s.db.Exec(`INSERT INTO leases (mac, ip, hostname, expires) VALUES (?,?,?,?)
		ON CONFLICT(mac) DO UPDATE SET ip=excluded.ip, hostname=excluded.hostname, expires=excluded.expires`,
		l.MAC, l.IP, l.Hostname, l.Expires)
	return err
}

func (s *Store) DeleteLease(mac string) error {
	_, err := s.db.Exec(`DELETE FROM leases WHERE mac=?`, mac)
	return err
}

// ---- query log ----

// WriteBatch inserts query log entries and updates device last-seen info in one transaction.
func (s *Store) WriteBatch(entries []QueryEntry, seen map[string]Seen) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if len(entries) > 0 {
		stmt, err := tx.Prepare(`INSERT INTO queries (ts, device_id, client_ip, domain, qtype, status, reason, ms) VALUES (?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if _, err := stmt.Exec(e.TS, e.DeviceID, e.ClientIP, e.Domain, e.QType, e.Status, e.Reason, e.Ms); err != nil {
				stmt.Close()
				return err
			}
		}
		stmt.Close()
	}
	for ident, sn := range seen {
		if _, err := tx.Exec(`UPDATE aliases SET last_seen=?, last_ip=? WHERE identifier=?`, sn.TS, sn.IP, ident); err != nil {
			return err
		}
		ipv4 := ""
		if ip := net.ParseIP(sn.IP); ip != nil && ip.To4() != nil {
			ipv4 = sn.IP
		}
		if _, err := tx.Exec(`UPDATE devices SET last_seen=MAX(last_seen, ?), last_ip=?,
				last_ipv4=CASE WHEN ?='' THEN last_ipv4 ELSE ? END WHERE id=?`,
			sn.TS, sn.IP, ipv4, ipv4, sn.DeviceID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type QueryFilter struct {
	DeviceID string
	Status   string
	Search   string
	Limit    int
}

func (s *Store) RecentQueries(f QueryFilter) ([]QueryEntry, error) {
	var where []string
	var args []any
	if f.DeviceID != "" {
		where = append(where, "device_id=?")
		args = append(args, f.DeviceID)
	}
	if f.Status != "" {
		where = append(where, "status=?")
		args = append(args, f.Status)
	}
	if f.Search != "" {
		where = append(where, "domain LIKE ?")
		args = append(args, "%"+f.Search+"%")
	}
	if f.Limit <= 0 || f.Limit > 2000 {
		f.Limit = 200
	}
	q := `SELECT ts, device_id, client_ip, domain, qtype, status, reason, ms FROM queries`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, f.Limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QueryEntry{}
	for rows.Next() {
		var e QueryEntry
		if err := rows.Scan(&e.TS, &e.DeviceID, &e.ClientIP, &e.Domain, &e.QType, &e.Status, &e.Reason, &e.Ms); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

type Counts struct {
	Total   int64 `json:"total"`
	Blocked int64 `json:"blocked"`
}

func (s *Store) CountsByDevice(since int64) (map[string]Counts, error) {
	rows, err := s.db.Query(`SELECT device_id, COUNT(*), SUM(status='blocked') FROM queries WHERE ts>=? GROUP BY device_id`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Counts{}
	for rows.Next() {
		var id string
		var c Counts
		if err := rows.Scan(&id, &c.Total, &c.Blocked); err != nil {
			return nil, err
		}
		out[id] = c
	}
	return out, rows.Err()
}

type DomainCount struct {
	Domain string `json:"domain"`
	Count  int64  `json:"count"`
}

// TopDomains returns the most-queried domains. deviceID "" means all devices.
func (s *Store) TopDomains(deviceID string, blocked bool, since int64, limit int) ([]DomainCount, error) {
	q := `SELECT domain, COUNT(*) c FROM queries WHERE ts>=?`
	args := []any{since}
	if blocked {
		q += ` AND status='blocked'`
	} else {
		q += ` AND status!='blocked'`
	}
	if deviceID != "" {
		q += ` AND device_id=?`
		args = append(args, deviceID)
	}
	q += ` GROUP BY domain ORDER BY c DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DomainCount{}
	for rows.Next() {
		var d DomainCount
		if err := rows.Scan(&d.Domain, &d.Count); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

type HourBucket struct {
	Hour    int64 `json:"hour"` // unix seconds at start of hour
	Total   int64 `json:"total"`
	Blocked int64 `json:"blocked"`
}

// Hourly returns per-hour totals. deviceID "" means all devices.
func (s *Store) Hourly(deviceID string, since int64) ([]HourBucket, error) {
	q := `SELECT (ts/3600)*3600 h, COUNT(*), SUM(status='blocked') FROM queries WHERE ts>=?`
	args := []any{since}
	if deviceID != "" {
		q += ` AND device_id=?`
		args = append(args, deviceID)
	}
	q += ` GROUP BY h ORDER BY h`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HourBucket{}
	for rows.Next() {
		var b HourBucket
		if err := rows.Scan(&b.Hour, &b.Total, &b.Blocked); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ---- history / ad statistics ----

type HistoryInfo struct {
	Queries int64 `json:"queries"`
	Oldest  int64 `json:"oldest"` // unix seconds, 0 if empty
	Bytes   int64 `json:"bytes"`  // database file size including the WAL
}

func (s *Store) History() HistoryInfo {
	var h HistoryInfo
	var oldest sql.NullInt64
	_ = s.db.QueryRow(`SELECT COUNT(*), MIN(ts) FROM queries`).Scan(&h.Queries, &oldest)
	h.Oldest = oldest.Int64
	for _, p := range []string{s.path, s.path + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			h.Bytes += fi.Size()
		}
	}
	return h
}

// AdFilter selects ad/tracking rows: Blocked and Seen are the reason values
// for blocked and merely-tagged lookups (see filter.AdReasons).
type AdFilter struct {
	Blocked, Seen []string
	Since         int64
	DeviceID      string // "" = all devices
}

func (f AdFilter) where() (string, []any) {
	reasons := append(append([]string{}, f.Blocked...), f.Seen...)
	q := `reason IN (` + strings.TrimSuffix(strings.Repeat("?,", len(reasons)), ",") + `) AND ts>=?`
	args := make([]any, 0, len(reasons)+2)
	for _, r := range reasons {
		args = append(args, r)
	}
	args = append(args, f.Since)
	if f.DeviceID != "" {
		q += ` AND device_id=?`
		args = append(args, f.DeviceID)
	}
	return q, args
}

// AdCount is blocked vs. allowed-but-tagged ad/tracking lookups.
type AdCount struct {
	Blocked int64 `json:"blocked"`
	Allowed int64 `json:"allowed"`
}

type AdBucket struct {
	T int64 `json:"t"` // bucket start, unix seconds
	AdCount
}

// AdSeries buckets ad lookups by time. bucket is the width in seconds;
// offset shifts bucket boundaries (e.g. the local UTC offset for days).
func (s *Store) AdSeries(f AdFilter, bucket, offset int64) ([]AdBucket, error) {
	w, args := f.where()
	rows, err := s.db.Query(`SELECT ((ts+?)/?)*?-?, SUM(status='blocked'), SUM(status!='blocked') FROM queries WHERE `+w+` GROUP BY 1 ORDER BY 1`,
		append([]any{offset, bucket, bucket, offset}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdBucket{}
	for rows.Next() {
		var b AdBucket
		if err := rows.Scan(&b.T, &b.Blocked, &b.Allowed); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) AdByDevice(f AdFilter) (map[string]AdCount, error) {
	w, args := f.where()
	rows, err := s.db.Query(`SELECT device_id, SUM(status='blocked'), SUM(status!='blocked') FROM queries WHERE `+w+` GROUP BY device_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]AdCount{}
	for rows.Next() {
		var id string
		var c AdCount
		if err := rows.Scan(&id, &c.Blocked, &c.Allowed); err != nil {
			return nil, err
		}
		out[id] = c
	}
	return out, rows.Err()
}

type AdDomainRow struct {
	Domain   string
	DeviceID string
	AdCount
}

// AdDomains returns the busiest (domain, device) pairs.
func (s *Store) AdDomains(f AdFilter, limit int) ([]AdDomainRow, error) {
	w, args := f.where()
	rows, err := s.db.Query(`SELECT domain, device_id, SUM(status='blocked') b, SUM(status!='blocked') a FROM queries WHERE `+w+
		` GROUP BY domain, device_id ORDER BY b+a DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdDomainRow{}
	for rows.Next() {
		var r AdDomainRow
		if err := rows.Scan(&r.Domain, &r.DeviceID, &r.Blocked, &r.Allowed); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UntaggedDomains lists domains with allowed lookups since `since` that carry
// no reason yet (logged before tagging existed, or before a list was added).
func (s *Store) UntaggedDomains(since int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT domain FROM queries WHERE ts>=? AND reason='' AND status IN ('allowed','cached')`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// TagDomains sets the reason on untagged allowed lookups of each domain.
func (s *Store) TagDomains(reasons map[string]string, since int64) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var n int64
	for domain, reason := range reasons {
		res, err := tx.Exec(`UPDATE queries SET reason=? WHERE domain=? AND ts>=? AND reason='' AND status IN ('allowed','cached')`, reason, domain, since)
		if err != nil {
			return 0, err
		}
		c, _ := res.RowsAffected()
		n += c
	}
	return n, tx.Commit()
}

// DomainActivity is one device's lookups of one hostname for one reason.
type DomainActivity struct {
	Domain, Reason             string
	Lookups, Blocked, Restrict int64 // Restrict: answered with SafeSearch / Restricted Mode
}

// DeviceActivity returns a device's lookups since `since`, grouped by
// hostname and reason (local answers excluded). Powers the device summary.
func (s *Store) DeviceActivity(deviceID string, since int64) ([]DomainActivity, error) {
	rows, err := s.db.Query(`SELECT domain, reason, COUNT(*), SUM(status='blocked'), SUM(status='safesearch')
		FROM queries WHERE device_id=? AND ts>=? AND status!='local' GROUP BY domain, reason`, deviceID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DomainActivity{}
	for rows.Next() {
		var a DomainActivity
		if err := rows.Scan(&a.Domain, &a.Reason, &a.Lookups, &a.Blocked, &a.Restrict); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SiteRow aggregates lookups of one hostname by one device for one reason.
type SiteRow struct {
	Domain, DeviceID, Reason string
	Lookups, Blocked         int64
	First, Last              int64
}

// SiteRows powers the collapsed "Sites" view of the query log, newest first.
// Local answers (wpad.lan, *.local, ...) are left out.
func (s *Store) SiteRows(f QueryFilter, since int64, limit int) ([]SiteRow, error) {
	q := `SELECT domain, device_id, reason, COUNT(*), SUM(status='blocked'), MIN(ts), MAX(ts)
		FROM queries WHERE ts>=? AND status!='local'`
	args := []any{since}
	if f.DeviceID != "" {
		q += ` AND device_id=?`
		args = append(args, f.DeviceID)
	}
	if f.Search != "" {
		q += ` AND domain LIKE ?`
		args = append(args, "%"+f.Search+"%")
	}
	q += ` GROUP BY domain, device_id, reason ORDER BY MAX(ts) DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SiteRow{}
	for rows.Next() {
		var r SiteRow
		if err := rows.Scan(&r.Domain, &r.DeviceID, &r.Reason, &r.Lookups, &r.Blocked, &r.First, &r.Last); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) PruneQueries(olderThan time.Duration) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM queries WHERE ts<?`, time.Now().Add(-olderThan).Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
