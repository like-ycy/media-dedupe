package cache

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"media-dedupe/internal/model"
)

type Cache struct {
	db  *sql.DB
	dsn string
}

func Open(path string) (*Cache, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Single writer discipline for embedded CLI.
	db.SetMaxOpenConns(1)
	c := &Cache{db: db, dsn: path}
	if err := c.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return c, nil
}

func (c *Cache) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

func (c *Cache) DB() *sql.DB { return c.db }

func (c *Cache) init() error {
	_, err := c.db.Exec(`
CREATE TABLE IF NOT EXISTS files (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    path TEXT NOT NULL UNIQUE,
    media_type TEXT NOT NULL,
    extension TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    mtime_ns INTEGER NOT NULL,
    inode INTEGER NOT NULL,
    device INTEGER NOT NULL,
    status TEXT NOT NULL,
    last_seen_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS file_hashes (
    file_id INTEGER PRIMARY KEY,
    size_bytes INTEGER NOT NULL,
    quick_hash TEXT,
    full_hash TEXT,
    hash_algorithm TEXT NOT NULL,
    FOREIGN KEY(file_id) REFERENCES files(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS media_metadata (
    file_id INTEGER PRIMARY KEY,
    width INTEGER,
    height INTEGER,
    duration_ms INTEGER,
    frame_rate REAL,
    bit_rate INTEGER,
    codec TEXT,
    format_name TEXT,
    orientation INTEGER,
    has_audio INTEGER,
    is_readable INTEGER NOT NULL,
    metadata_type TEXT NOT NULL,
    FOREIGN KEY(file_id) REFERENCES files(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS perceptual_hashes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id INTEGER NOT NULL,
    frame_index INTEGER,
    timestamp_ms INTEGER,
    hash_type TEXT NOT NULL,
    hash_value TEXT NOT NULL,
    hash_size INTEGER NOT NULL,
    FOREIGN KEY(file_id) REFERENCES files(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS text_facts (
    file_id INTEGER PRIMARY KEY,
    size_bytes INTEGER NOT NULL,
    normalized_length INTEGER NOT NULL,
    name_key TEXT NOT NULL,
    features_json TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 1,
    FOREIGN KEY(file_id) REFERENCES files(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS duplicate_groups (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    scan_run_id INTEGER,
    group_type TEXT NOT NULL,
    confidence REAL NOT NULL,
    recommended_file_id INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS duplicate_items (
    group_id INTEGER NOT NULL,
    file_id INTEGER NOT NULL,
    action TEXT NOT NULL,
    similarity REAL NOT NULL,
    quality_score REAL NOT NULL,
    reasons_json TEXT NOT NULL,
    size_bytes INTEGER NOT NULL DEFAULT 0,
    thumb_path TEXT,
    FOREIGN KEY(group_id) REFERENCES duplicate_groups(id) ON DELETE CASCADE,
    FOREIGN KEY(file_id) REFERENCES files(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS scan_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    input_paths_json TEXT NOT NULL,
    config_json TEXT NOT NULL,
    started_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at TEXT,
    files_seen INTEGER NOT NULL DEFAULT 0,
    files_failed INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS errors (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    scan_run_id INTEGER,
    path TEXT NOT NULL,
    stage TEXT NOT NULL,
    message TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_files_status ON files(status);
CREATE INDEX IF NOT EXISTS idx_ph_file_type ON perceptual_hashes(file_id, hash_type);
CREATE INDEX IF NOT EXISTS idx_files_size ON files(size_bytes);
`)
	if err != nil {
		return err
	}
	// Best-effort migrations for caches created before these columns existed.
	_, _ = c.db.Exec(`ALTER TABLE duplicate_items ADD COLUMN size_bytes INTEGER NOT NULL DEFAULT 0`)
	_, _ = c.db.Exec(`ALTER TABLE duplicate_items ADD COLUMN thumb_path TEXT`)
	_, _ = c.db.Exec(`ALTER TABLE duplicate_groups ADD COLUMN status TEXT NOT NULL DEFAULT 'pending'`)
	_, _ = c.db.Exec(`ALTER TABLE duplicate_groups ADD COLUMN stable_key TEXT`)
	_, _ = c.db.Exec(`ALTER TABLE duplicate_groups ADD COLUMN ignored_at TEXT`)
	_, _ = c.db.Exec(`ALTER TABLE duplicate_groups ADD COLUMN processed_at TEXT`)
	_, _ = c.db.Exec(`
CREATE TABLE IF NOT EXISTS delete_ops (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    mode TEXT NOT NULL,
    group_id INTEGER,
    file_id INTEGER,
    path TEXT NOT NULL,
    ok INTEGER NOT NULL,
    message TEXT
)`)
	_, _ = c.db.Exec(`
CREATE TABLE IF NOT EXISTS recent_dirs (
    path TEXT PRIMARY KEY,
    last_used_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
)`)
	_, _ = c.db.Exec(`
CREATE TABLE IF NOT EXISTS frame_previews (
    file_id INTEGER NOT NULL,
    frame_index INTEGER NOT NULL,
    path TEXT NOT NULL,
    PRIMARY KEY (file_id, frame_index)
)`)
	return nil
}

func (c *Cache) StartScanRun(paths []string, config map[string]any) (int64, error) {
	pathsJSON, _ := json.Marshal(paths)
	cfgJSON, _ := json.Marshal(config)
	res, err := c.db.Exec(
		`INSERT INTO scan_runs (input_paths_json, config_json) VALUES (?, ?)`,
		string(pathsJSON), string(cfgJSON),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (c *Cache) FinishScanRun(id int64, filesSeen, filesFailed int) error {
	_, err := c.db.Exec(
		`UPDATE scan_runs SET finished_at = CURRENT_TIMESTAMP, files_seen = ?, files_failed = ? WHERE id = ?`,
		filesSeen, filesFailed, id,
	)
	return err
}

func (c *Cache) RecordError(scanRunID int64, path, stage, message string) error {
	_, err := c.db.Exec(
		`INSERT INTO errors (scan_run_id, path, stage, message) VALUES (?, ?, ?, ?)`,
		scanRunID, path, stage, message,
	)
	return err
}

func (c *Cache) UpsertFile(d model.DiscoveredFile) (int64, error) {
	_, err := c.db.Exec(`
INSERT INTO files (path, media_type, extension, size_bytes, mtime_ns, inode, device, status, last_seen_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(path) DO UPDATE SET
  media_type=excluded.media_type,
  extension=excluded.extension,
  size_bytes=excluded.size_bytes,
  mtime_ns=excluded.mtime_ns,
  inode=excluded.inode,
  device=excluded.device,
  status=excluded.status,
  last_seen_at=CURRENT_TIMESTAMP
`, d.Path, string(d.MediaType), d.Extension, d.SizeBytes, d.MTimeNs, int64(d.Inode), int64(d.Device), string(model.StatusActive))
	if err != nil {
		return 0, err
	}
	var id int64
	err = c.db.QueryRow(`SELECT id FROM files WHERE path = ?`, d.Path).Scan(&id)
	return id, err
}

// IsUnchanged trusts size+mtime when status is active.
func (c *Cache) IsUnchanged(d model.DiscoveredFile) bool {
	var size, mtime int64
	var status string
	err := c.db.QueryRow(
		`SELECT size_bytes, mtime_ns, status FROM files WHERE path = ?`, d.Path,
	).Scan(&size, &mtime, &status)
	if err != nil {
		return false
	}
	return size == d.SizeBytes && mtime == d.MTimeNs && status == string(model.StatusActive)
}

// MarkMissingLastSeenBefore marks files not seen since timestamp
// (SQLite CURRENT_TIMESTAMP format: "2006-01-02 15:04:05").
func (c *Cache) MarkMissingLastSeenBefore(ts string) error {
	_, err := c.db.Exec(
		`UPDATE files SET status = ? WHERE last_seen_at < ?`,
		string(model.StatusMissing), ts,
	)
	return err
}

func (c *Cache) TouchFile(id int64) error {
	_, err := c.db.Exec(`UPDATE files SET last_seen_at = CURRENT_TIMESTAMP, status = ? WHERE id = ?`,
		string(model.StatusActive), id)
	return err
}

func (c *Cache) SaveFileHash(fileID int64, sizeBytes int64, fullHash string) error {
	_, err := c.db.Exec(`
INSERT INTO file_hashes (file_id, size_bytes, full_hash, hash_algorithm)
VALUES (?, ?, ?, 'sha256')
ON CONFLICT(file_id) DO UPDATE SET
  size_bytes=excluded.size_bytes,
  full_hash=excluded.full_hash,
  hash_algorithm=excluded.hash_algorithm
`, fileID, sizeBytes, fullHash)
	return err
}

func (c *Cache) LoadFileHash(fileID int64) (string, bool, error) {
	var full sql.NullString
	err := c.db.QueryRow(`SELECT full_hash FROM file_hashes WHERE file_id = ?`, fileID).Scan(&full)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !full.Valid || full.String == "" {
		return "", false, nil
	}
	return full.String, true, nil
}

const textFactVersion = 2

func (c *Cache) SaveTextFact(fileID, sizeBytes int64, length int, name string, features []uint64) error {
	b, err := json.Marshal(features)
	if err != nil {
		return err
	}
	_, err = c.db.Exec(`
INSERT INTO text_facts (file_id, size_bytes, normalized_length, name_key, features_json, version)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(file_id) DO UPDATE SET
  size_bytes=excluded.size_bytes,
  normalized_length=excluded.normalized_length,
  name_key=excluded.name_key,
  features_json=excluded.features_json,
  version=excluded.version
`, fileID, sizeBytes, length, name, string(b), textFactVersion)
	return err
}

func (c *Cache) LoadTextFact(fileID, sizeBytes int64) (name string, length int, features []uint64, ok bool, err error) {
	var raw string
	if err = c.db.QueryRow(`
SELECT name_key, normalized_length, features_json
FROM text_facts WHERE file_id = ? AND size_bytes = ? AND version = ?
`, fileID, sizeBytes, textFactVersion).Scan(&name, &length, &raw); err == sql.ErrNoRows {
		return "", 0, nil, false, nil
	} else if err != nil {
		return "", 0, nil, false, err
	}
	if err = json.Unmarshal([]byte(raw), &features); err != nil {
		return "", 0, nil, false, err
	}
	return name, length, features, true, nil
}

func (c *Cache) SaveImageMetadata(fileID int64, m model.ImageMetadata) error {
	readable := 0
	if m.IsReadable {
		readable = 1
	}
	_, err := c.db.Exec(`
INSERT INTO media_metadata (file_id, width, height, format_name, orientation, is_readable, metadata_type)
VALUES (?, ?, ?, ?, ?, ?, 'image')
ON CONFLICT(file_id) DO UPDATE SET
  width=excluded.width, height=excluded.height,
  duration_ms=NULL, frame_rate=NULL, bit_rate=NULL, codec=NULL,
  format_name=excluded.format_name, orientation=excluded.orientation,
  has_audio=NULL, is_readable=excluded.is_readable, metadata_type='image'
`, fileID, m.Width, m.Height, m.FormatName, m.Orientation, readable)
	return err
}

func (c *Cache) LoadImageMetadata(fileID int64) (model.ImageMetadata, bool, error) {
	var (
		width, height, readable int
		formatName              sql.NullString
		orientation             sql.NullInt64
	)
	err := c.db.QueryRow(`
SELECT width, height, format_name, orientation, is_readable
FROM media_metadata WHERE file_id = ? AND metadata_type = 'image'
`, fileID).Scan(&width, &height, &formatName, &orientation, &readable)
	if err == sql.ErrNoRows {
		return model.ImageMetadata{}, false, nil
	}
	if err != nil {
		return model.ImageMetadata{}, false, err
	}
	m := model.ImageMetadata{
		Width:      width,
		Height:     height,
		FormatName: formatName.String,
		IsReadable: readable == 1,
	}
	if orientation.Valid {
		m.Orientation = int(orientation.Int64)
	}
	return m, true, nil
}

func (c *Cache) SaveVideoMetadata(fileID int64, m model.VideoMetadata) error {
	readable := 0
	if m.IsReadable {
		readable = 1
	}
	hasAudio := 0
	if m.HasAudio {
		hasAudio = 1
	}
	_, err := c.db.Exec(`
INSERT INTO media_metadata (
  file_id, width, height, duration_ms, frame_rate, bit_rate, codec, has_audio, is_readable, metadata_type
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'video')
ON CONFLICT(file_id) DO UPDATE SET
  width=excluded.width, height=excluded.height,
  duration_ms=excluded.duration_ms, frame_rate=excluded.frame_rate,
  bit_rate=excluded.bit_rate, codec=excluded.codec,
  format_name=NULL, orientation=NULL,
  has_audio=excluded.has_audio, is_readable=excluded.is_readable, metadata_type='video'
`, fileID, m.Width, m.Height, m.DurationMs, m.FrameRate, m.BitRate, m.Codec, hasAudio, readable)
	return err
}

func (c *Cache) LoadVideoMetadata(fileID int64) (model.VideoMetadata, bool, error) {
	var (
		width, height, readable, hasAudio int
		durationMs                        int64
		frameRate                         sql.NullFloat64
		bitRate                           sql.NullInt64
		codec                             sql.NullString
	)
	err := c.db.QueryRow(`
SELECT duration_ms, width, height, frame_rate, bit_rate, codec, has_audio, is_readable
FROM media_metadata WHERE file_id = ? AND metadata_type = 'video'
`, fileID).Scan(&durationMs, &width, &height, &frameRate, &bitRate, &codec, &hasAudio, &readable)
	if err == sql.ErrNoRows {
		return model.VideoMetadata{}, false, nil
	}
	if err != nil {
		return model.VideoMetadata{}, false, err
	}
	m := model.VideoMetadata{
		DurationMs: durationMs,
		Width:      width,
		Height:     height,
		Codec:      codec.String,
		HasAudio:   hasAudio == 1,
		IsReadable: readable == 1,
	}
	if frameRate.Valid {
		v := frameRate.Float64
		m.FrameRate = &v
	}
	if bitRate.Valid {
		v := bitRate.Int64
		m.BitRate = &v
	}
	return m, true, nil
}

func (c *Cache) SavePerceptualHashes(fileID int64, hashType string, hashes []string) error {
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM perceptual_hashes WHERE file_id = ? AND hash_type = ?`, fileID, hashType); err != nil {
		return err
	}
	for i, h := range hashes {
		if _, err := tx.Exec(`
INSERT INTO perceptual_hashes (file_id, frame_index, timestamp_ms, hash_type, hash_value, hash_size)
VALUES (?, ?, NULL, ?, ?, ?)
`, fileID, i, hashType, h, len(h)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (c *Cache) LoadPerceptualHashes(fileID int64, hashType string) ([]string, error) {
	rows, err := c.db.Query(`
SELECT hash_value FROM perceptual_hashes
WHERE file_id = ? AND hash_type = ?
ORDER BY frame_index, id
`, fileID, hashType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (c *Cache) ReplaceReportGroups(groups []model.ReportGroup, scanRunID int64) error {
	// Preserve prior statuses by stable_key so ignore/processed survive rescan.
	prev := map[string]struct {
		status, ignoredAt, processedAt string
	}{}
	if rows, err := c.db.Query(`SELECT stable_key, COALESCE(status,'pending'), COALESCE(ignored_at,''), COALESCE(processed_at,'') FROM duplicate_groups WHERE stable_key IS NOT NULL AND stable_key != ''`); err == nil {
		for rows.Next() {
			var key, status, ignoredAt, processedAt string
			if err := rows.Scan(&key, &status, &ignoredAt, &processedAt); err != nil {
				break
			}
			prev[key] = struct{ status, ignoredAt, processedAt string }{status, ignoredAt, processedAt}
		}
		rows.Close()
	}

	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM duplicate_items`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM duplicate_groups`); err != nil {
		return err
	}
	for _, g := range groups {
		status := string(model.GroupPending)
		ignoredAt := sql.NullString{}
		processedAt := sql.NullString{}
		if g.Status != "" {
			status = string(g.Status)
		} else if g.StableKey != "" {
			if p, ok := prev[g.StableKey]; ok {
				status = p.status
				if p.ignoredAt != "" {
					ignoredAt = sql.NullString{String: p.ignoredAt, Valid: true}
				}
				if p.processedAt != "" {
					processedAt = sql.NullString{String: p.processedAt, Valid: true}
				}
			}
		}
		res, err := tx.Exec(`
INSERT INTO duplicate_groups (id, scan_run_id, group_type, confidence, recommended_file_id, status, stable_key, ignored_at, processed_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, g.GroupID, scanRunID, string(g.GroupType), g.Confidence, g.RecommendedFileID, status, g.StableKey, ignoredAt, processedAt)
		if err != nil {
			return err
		}
		gid, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for _, item := range g.Items {
			reasons, _ := json.Marshal(item.Reasons)
			if _, err := tx.Exec(`
INSERT INTO duplicate_items (group_id, file_id, action, similarity, quality_score, reasons_json, size_bytes, thumb_path)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, gid, item.FileID, string(item.Action), item.Similarity, item.QualityScore, string(reasons), item.SizeBytes, item.ThumbPath); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (c *Cache) SetGroupStatus(groupID int64, status model.GroupStatus) error {
	now := timeNow()
	switch status {
	case model.GroupIgnored:
		_, err := c.db.Exec(`UPDATE duplicate_groups SET status = ?, ignored_at = ?, processed_at = NULL WHERE id = ?`, string(status), now, groupID)
		return err
	case model.GroupProcessed:
		_, err := c.db.Exec(`UPDATE duplicate_groups SET status = ?, processed_at = ?, ignored_at = NULL WHERE id = ?`, string(status), now, groupID)
		return err
	default:
		_, err := c.db.Exec(`UPDATE duplicate_groups SET status = ?, ignored_at = NULL, processed_at = NULL WHERE id = ?`, string(status), groupID)
		return err
	}
}

func (c *Cache) UpdateRecommended(groupID, fileID int64) error {
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE duplicate_groups SET recommended_file_id = ? WHERE id = ?`, fileID, groupID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE duplicate_items SET action = ? WHERE group_id = ?`, string(model.ActionKeep), groupID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE duplicate_items SET action = ? WHERE group_id = ? AND file_id != ?`, string(model.ActionCleanup), groupID, fileID); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteOpItem is one deleted (or failed) member recorded against a group.
type DeleteOpItem struct {
	FileID  int64
	Path    string
	OK      bool
	Message string
}

func (c *Cache) RecordDeleteOps(mode string, groupID int64, items []DeleteOpItem) error {
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, it := range items {
		ok := 0
		if it.OK {
			ok = 1
		}
		if _, err := tx.Exec(
			`INSERT INTO delete_ops (mode, group_id, file_id, path, ok, message) VALUES (?, ?, ?, ?, ?, ?)`,
			mode, groupID, it.FileID, it.Path, ok, it.Message,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (c *Cache) SaveFramePreview(fileID int64, frameIndex int, path string) error {
	_, err := c.db.Exec(`
INSERT INTO frame_previews (file_id, frame_index, path) VALUES (?, ?, ?)
ON CONFLICT(file_id, frame_index) DO UPDATE SET path = excluded.path
`, fileID, frameIndex, path)
	return err
}

func (c *Cache) LoadFramePreviews(fileID int64) ([]string, error) {
	rows, err := c.db.Query(`SELECT path FROM frame_previews WHERE file_id = ? ORDER BY frame_index`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (c *Cache) AddRecentDir(path string) error {
	_, err := c.db.Exec(`
INSERT INTO recent_dirs (path, last_used_at) VALUES (?, ?)
ON CONFLICT(path) DO UPDATE SET last_used_at = excluded.last_used_at
`, path, timeNow())
	return err
}

func (c *Cache) ListRecentDirs(limit int) ([]string, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := c.db.Query(`SELECT path FROM recent_dirs ORDER BY last_used_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func timeNow() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func (c *Cache) LoadReportGroups() ([]model.ReportGroup, error) {
	rows, err := c.db.Query(`
SELECT id, group_type, confidence, recommended_file_id, COALESCE(stable_key, '')
FROM duplicate_groups ORDER BY id
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []model.ReportGroup
	type gkey struct {
		id, recID int64
		gtype     string
		conf      float64
	}
	var keys []gkey
	for rows.Next() {
		var (
			id, recID int64
			gtype     string
			conf      float64
			stableKey sql.NullString
		)
		if err := rows.Scan(&id, &gtype, &conf, &recID, &stableKey); err != nil {
			return nil, err
		}
		_ = stableKey
		keys = append(keys, gkey{id, recID, gtype, conf})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Load statuses separately (query above may predate status columns in rare partial migrations).
	statusBy := map[int64]struct {
		status, ignoredAt, processedAt string
	}{}
	if srows, err := c.db.Query(`SELECT id, COALESCE(status,'pending'), COALESCE(ignored_at,''), COALESCE(processed_at,'') FROM duplicate_groups`); err == nil {
		for srows.Next() {
			var id int64
			var status, ignoredAt, processedAt string
			if err := srows.Scan(&id, &status, &ignoredAt, &processedAt); err != nil {
				break
			}
			statusBy[id] = struct{ status, ignoredAt, processedAt string }{status, ignoredAt, processedAt}
		}
		srows.Close()
	}

	for _, k := range keys {
		itemRows, err := c.db.Query(`
SELECT duplicate_items.file_id, files.path, duplicate_items.action,
       duplicate_items.similarity, duplicate_items.quality_score, duplicate_items.reasons_json,
       COALESCE(duplicate_items.size_bytes, files.size_bytes, 0),
       COALESCE(duplicate_items.thumb_path, '')
FROM duplicate_items
JOIN files ON files.id = duplicate_items.file_id
WHERE duplicate_items.group_id = ?
ORDER BY duplicate_items.file_id
`, k.id)
		if err != nil {
			return nil, err
		}
		var items []model.ReportItem
		for itemRows.Next() {
			var (
				fileID      int64
				path        string
				action      string
				similarity  float64
				quality     float64
				reasonsJSON string
				sizeBytes   int64
				thumbPath   sql.NullString
			)
			if err := itemRows.Scan(&fileID, &path, &action, &similarity, &quality, &reasonsJSON, &sizeBytes, &thumbPath); err != nil {
				itemRows.Close()
				return nil, err
			}
			var reasons []string
			_ = json.Unmarshal([]byte(reasonsJSON), &reasons)
			items = append(items, model.ReportItem{
				FileID:       fileID,
				Path:         path,
				Action:       model.Action(action),
				Similarity:   similarity,
				QualityScore: quality,
				SizeBytes:    sizeBytes,
				ThumbPath:    thumbPath.String,
				Reasons:      reasons,
			})
		}
		itemRows.Close()
		st := statusBy[k.id]
		if st.status == "" {
			st.status = string(model.GroupPending)
		}
		groups = append(groups, model.ReportGroup{
			GroupID:           k.id,
			GroupType:         model.GroupType(k.gtype),
			Confidence:        k.conf,
			RecommendedFileID: k.recID,
			Items:             items,
			Status:            model.GroupStatus(st.status),
			IgnoredAt:         st.ignoredAt,
			ProcessedAt:       st.processedAt,
		})
	}
	return groups, nil
}

func (c *Cache) LoadErrors(scanRunID *int64) ([]model.ReportError, error) {
	query := `SELECT path, stage, message FROM errors`
	args := []any{}
	if scanRunID != nil {
		query += ` WHERE scan_run_id = ?`
		args = append(args, *scanRunID)
	}
	query += ` ORDER BY id`
	rows, err := c.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ReportError
	for rows.Next() {
		var e model.ReportError
		if err := rows.Scan(&e.Path, &e.Stage, &e.Message); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (c *Cache) LatestScanRunID() (int64, bool, error) {
	var id int64
	err := c.db.QueryRow(`SELECT id FROM scan_runs ORDER BY id DESC LIMIT 1`).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

func (c *Cache) Info() (map[string]int, error) {
	tables := []string{"files", "file_hashes", "media_metadata", "perceptual_hashes", "text_facts", "duplicate_groups", "duplicate_items", "scan_runs", "errors"}
	out := map[string]int{}
	for _, t := range tables {
		var n int
		if err := c.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s`, t)).Scan(&n); err != nil {
			return nil, err
		}
		out[t] = n
	}
	return out, nil
}

func (c *Cache) Clear() error {
	tables := []string{"duplicate_items", "duplicate_groups", "errors", "perceptual_hashes", "text_facts", "media_metadata", "file_hashes", "scan_runs", "files"}
	for _, t := range tables {
		if _, err := c.db.Exec(`DELETE FROM ` + t); err != nil {
			return err
		}
	}
	return nil
}
