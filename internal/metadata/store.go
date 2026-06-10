// Package metadata provides a SQLite-backed store for the TorBox directory tree.
//
// Runs in WAL mode for high concurrent read performance. Browsing the
// virtual filesystem from Plex costs zero TorBox API calls.
package metadata

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Store is a SQLite-backed metadata cache.
type Store struct {
	db *sql.DB
}

// FileRecord represents a cached file entry from the TorBox directory.
// TorrentID and FileID together identify a file in the TorBox API
// for CDN URL generation.
type FileRecord struct {
	ID           int64  `json:"id"`             // Internal auto-increment ID
	TorrentID    int64  `json:"torrent_id"`     // TorBox torrent ID (for CDN URL)
	FileID       int64  `json:"file_id"`        // TorBox file ID within torrent (for CDN URL)
	Name         string `json:"name"`
	Path         string `json:"path"`
	Size         int64  `json:"size"`
	MimeType     string `json:"mime_type"`
	CreatedAt    string `json:"created_at,omitempty"` // From TorBox API torrent.created_at
	CDNURL       string `json:"cdn_url,omitempty"`
	CDNURLExpiry string `json:"cdn_url_expires,omitempty"`
}

// Open opens (or creates) the SQLite database at the given path.
// WAL mode is enabled for high concurrency.
func Open(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("opening sqlite database: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("pinging sqlite database: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("running migrations: %w", err)
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// migrate creates tables if they do not exist and runs schema migrations.
func (s *Store) migrate() error {
	// Create the files table with auto-increment primary key.
	// torrent_id + file_id together identify a file in the TorBox API.
	schema := `
	CREATE TABLE IF NOT EXISTS files (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		torrent_id      INTEGER NOT NULL DEFAULT 0,
		file_id         INTEGER NOT NULL DEFAULT 0,
		name            TEXT    NOT NULL,
		path            TEXT    NOT NULL UNIQUE,
		size            INTEGER NOT NULL DEFAULT 0,
		mime_type       TEXT    NOT NULL DEFAULT '',
		cdn_url         TEXT    NOT NULL DEFAULT '',
		cdn_url_expires TEXT    NOT NULL DEFAULT '',
		updated         TEXT    NOT NULL DEFAULT (datetime('now'))
	);
	CREATE INDEX IF NOT EXISTS idx_files_path ON files(path);
	CREATE INDEX IF NOT EXISTS idx_files_file_id ON files(file_id);
	`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}

	// Migrate v1 → v2: add torrent_id and file_id columns if missing.
	_, _ = s.db.Exec(`ALTER TABLE files ADD COLUMN torrent_id INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE files ADD COLUMN file_id INTEGER NOT NULL DEFAULT 0`)

	// Migrate v2 → v3: add created_at column if missing.
	_, _ = s.db.Exec(`ALTER TABLE files ADD COLUMN created_at TEXT NOT NULL DEFAULT ''`)

	return nil
}

// UpsertFile inserts or replaces a file record.
func (s *Store) UpsertFile(f FileRecord) error {
	_, err := s.db.Exec(`
		INSERT INTO files (torrent_id, file_id, name, path, size, mime_type, created_at, cdn_url, cdn_url_expires, updated)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(path) DO UPDATE SET
			torrent_id     = excluded.torrent_id,
			file_id        = excluded.file_id,
			name           = excluded.name,
			size           = excluded.size,
			mime_type      = excluded.mime_type,
			created_at     = excluded.created_at,
			cdn_url        = excluded.cdn_url,
			cdn_url_expires = excluded.cdn_url_expires,
			updated        = excluded.updated
	`, f.TorrentID, f.FileID, f.Name, f.Path, f.Size, f.MimeType, f.CreatedAt, f.CDNURL, f.CDNURLExpiry)
	return err
}

// ListDir returns all files under the given virtual directory path.
func (s *Store) ListDir(prefix string) ([]FileRecord, error) {
	rows, err := s.db.Query(`
		SELECT id, torrent_id, file_id, name, path, size, mime_type, created_at FROM files
		WHERE path LIKE ? ORDER BY name
	`, prefix+"%")
	if err != nil {
		return nil, fmt.Errorf("querying files: %w", err)
	}
	defer rows.Close()

	var files []FileRecord
	for rows.Next() {
		var f FileRecord
		if err := rows.Scan(&f.ID, &f.TorrentID, &f.FileID, &f.Name, &f.Path, &f.Size, &f.MimeType, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// GetFileByPath returns a single file record by its virtual path.
// Returns nil if the path is not found.
func (s *Store) GetFileByPath(path string) (*FileRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, torrent_id, file_id, name, path, size, mime_type, created_at, cdn_url, cdn_url_expires
		FROM files WHERE path = ?
	`, path)

	var f FileRecord
	err := row.Scan(&f.ID, &f.TorrentID, &f.FileID, &f.Name, &f.Path, &f.Size, &f.MimeType, &f.CreatedAt, &f.CDNURL, &f.CDNURLExpiry)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying file by path: %w", err)
	}
	return &f, nil
}

// GetFileByFileID returns a single file record by its TorBox file ID.
// Returns nil if the file_id is not found.
func (s *Store) GetFileByFileID(fileID int64) (*FileRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, torrent_id, file_id, name, path, size, mime_type, created_at, cdn_url, cdn_url_expires
		FROM files WHERE file_id = ? LIMIT 1
	`, fileID)

	var f FileRecord
	err := row.Scan(&f.ID, &f.TorrentID, &f.FileID, &f.Name, &f.Path, &f.Size, &f.MimeType, &f.CreatedAt, &f.CDNURL, &f.CDNURLExpiry)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying file by file_id: %w", err)
	}
	return &f, nil
}

// SetCDNURL stores a CDN download URL for a file identified by its internal ID.
func (s *Store) SetCDNURL(internalID int64, cdnURL string, expiresAt time.Time) error {
	_, err := s.db.Exec(`
		UPDATE files SET cdn_url = ?, cdn_url_expires = ?, updated = datetime('now')
		WHERE id = ?
	`, cdnURL, expiresAt.UTC().Format(time.RFC3339), internalID)
	return err
}

// GetCDNURL returns a cached CDN URL for a file identified by its internal ID.
func (s *Store) GetCDNURL(internalID int64) (string, error) {
	row := s.db.QueryRow(`
		SELECT cdn_url, cdn_url_expires FROM files WHERE id = ?
	`, internalID)

	var url, expires string
	if err := row.Scan(&url, &expires); err == sql.ErrNoRows {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("querying CDN URL: %w", err)
	}

	if url == "" {
		return "", nil
	}
	if expires == "" {
		return url, nil
	}

	expiryTime, err := time.Parse(time.RFC3339, expires)
	if err != nil {
		return "", nil
	}

	if time.Now().UTC().After(expiryTime) {
		return "", nil
	}

	return url, nil
}

// CountFiles returns the total number of file records in the store.
func (s *Store) CountFiles() (int, error) {
	row := s.db.QueryRow(`SELECT COUNT(*) FROM files`)
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("counting files: %w", err)
	}
	return count, nil
}

// GetTorrentIDByFileID returns the torrent_id for a given file_id.
// This is needed because TorBox's requestdl endpoint requires both.
func (s *Store) GetTorrentIDByFileID(fileID int64) (int64, error) {
	row := s.db.QueryRow(`SELECT torrent_id FROM files WHERE file_id = ? LIMIT 1`, fileID)
	var tid int64
	if err := row.Scan(&tid); err == sql.ErrNoRows {
		return 0, nil
	} else if err != nil {
		return 0, fmt.Errorf("querying torrent_id: %w", err)
	}
	return tid, nil
}
