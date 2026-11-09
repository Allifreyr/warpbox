package metadata

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// OpenReadOnly opens an existing database without migrations or cache writes.
func OpenReadOnly(dbPath string) (*Store, error) {
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	db, err := sql.Open("sqlite3", u.String()+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, dbPath: dbPath}, nil
}

// AnimeMapping is independent of files: published names survive source refreshes.
type AnimeMapping struct {
	Source       FileSource `json:"source"`
	ItemID       int64      `json:"item_id"`
	FileID       int64      `json:"file_id"`
	OriginalPath string     `json:"original_path"`
	Path         string     `json:"path,omitempty"`
	TMDBID       int        `json:"tmdb_id,omitempty"`
	Season       int        `json:"season"`
	Episodes     []int      `json:"episodes,omitempty"`
	Evidence     string     `json:"evidence,omitempty"`
	Reason       string     `json:"reason,omitempty"`
}

func (s *Store) initAnime() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS anime_mappings (
	 source INTEGER NOT NULL, item_id INTEGER NOT NULL, file_id INTEGER NOT NULL,
	 view_path TEXT NOT NULL, payload TEXT NOT NULL,
	 PRIMARY KEY(source,item_id,file_id));
	 CREATE INDEX IF NOT EXISTS idx_anime_path ON anime_mappings(view_path);
	 CREATE TABLE IF NOT EXISTS anime_cache (key TEXT PRIMARY KEY, body BLOB NOT NULL, fetched INTEGER NOT NULL);`)
	return err
}

func (s *Store) AnimeMappings(ctx context.Context) ([]AnimeMapping, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM anime_mappings ORDER BY source,item_id,file_id`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table: anime_mappings") {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	var out []AnimeMapping
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var m AnimeMapping
		if err := json.Unmarshal(body, &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) PutAnimeMapping(ctx context.Context, m AnimeMapping) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO anime_mappings(source,item_id,file_id,view_path,payload) VALUES(?,?,?,?,?)
	 ON CONFLICT(source,item_id,file_id) DO UPDATE SET view_path=excluded.view_path,payload=excluded.payload`, m.Source, m.ItemID, m.FileID, m.Path, body)
	return err
}

// AllFiles retains duplicate paths and the full TorBox identity for derived views.
func (s *Store) AllFiles(ctx context.Context) ([]FileRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,item_id,file_id,source,name,path,size,mime_type,created_at,filter_tags,cdn_url,cdn_url_expires FROM files ORDER BY source,item_id,file_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileRecord
	for rows.Next() {
		var f FileRecord
		if err := rows.Scan(&f.ID, &f.ItemID, &f.FileID, &f.Source, &f.Name, &f.Path, &f.Size, &f.MimeType, &f.CreatedAt, &f.FilterTags, &f.CDNURL, &f.CDNURLExpiry); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// animePathFilter uses literal, indexed comparisons. The directory prefix ends
// with '/', whose next byte '0' gives an exclusive upper bound for all children.
func animePathFilter(prefix string, exact bool) (string, []any) {
	if exact {
		// Empty paths represent unresolved mappings, never playable aliases.
		return `view_path<>'' AND view_path=?`, []any{prefix}
	}
	if prefix == "" {
		return `view_path<>''`, nil
	}
	base := strings.TrimRight(prefix, "/")
	return `view_path>=? AND view_path<?`, []any{base + "/", base + "0"}
}

// AnimeItemFiles loads only items contributing to the requested derived path,
// including their unmapped siblings for largest-file selection and sidecars.
func (s *Store) AnimeItemFiles(ctx context.Context, prefix string, exact bool) ([]FileRecord, error) {
	predicate, args := animePathFilter(prefix, exact)
	// Keep candidate items as the outer loop. Otherwise SQLite may choose a
	// full files-index scan to satisfy ORDER BY, despite returning few rows.
	rows, err := s.db.QueryContext(ctx, `SELECT f.id,f.item_id,f.file_id,f.source,f.name,f.path,f.size,f.mime_type,f.created_at,f.filter_tags,f.cdn_url,f.cdn_url_expires
	 FROM (SELECT DISTINCT source,item_id FROM anime_mappings WHERE `+predicate+`) AS items
	 CROSS JOIN files f WHERE f.source=items.source AND f.item_id=items.item_id
	 ORDER BY f.source,f.item_id,f.file_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileRecord
	for rows.Next() {
		var f FileRecord
		if err := rows.Scan(&f.ID, &f.ItemID, &f.FileID, &f.Source, &f.Name, &f.Path, &f.Size, &f.MimeType, &f.CreatedAt, &f.FilterTags, &f.CDNURL, &f.CDNURLExpiry); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// AnimeFiles joins on natural identity, never on a possibly changed row ID.
// A boundary-safe prefix prevents sibling names from leaking into a directory.
func (s *Store) AnimeFiles(ctx context.Context, prefix string, exact bool) ([]FileRecord, error) {
	predicate, args := animePathFilter(prefix, exact)
	query := `SELECT f.id,f.item_id,f.file_id,f.source,f.path,f.size,f.mime_type,f.created_at,f.cdn_url,f.cdn_url_expires,a.view_path
	 FROM anime_mappings a CROSS JOIN files f WHERE ` + predicate + ` AND f.source=a.source AND f.item_id=a.item_id AND f.file_id=a.file_id`
	query += ` ORDER BY a.view_path,f.source,f.item_id,f.file_id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileRecord
	for rows.Next() {
		var f FileRecord
		var vp string
		if err := rows.Scan(&f.ID, &f.ItemID, &f.FileID, &f.Source, &f.Path, &f.Size, &f.MimeType, &f.CreatedAt, &f.CDNURL, &f.CDNURLExpiry, &vp); err != nil {
			return nil, err
		}
		f.Name = path.Base(vp)
		f.ViewPath = vp
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) AnimeCache(ctx context.Context, key string) ([]byte, time.Time, error) {
	var b []byte
	var fetched int64
	err := s.db.QueryRowContext(ctx, `SELECT body,fetched FROM anime_cache WHERE key=?`, key).Scan(&b, &fetched)
	return b, time.Unix(fetched, 0), err
}
func (s *Store) PutAnimeCache(ctx context.Context, key string, b []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO anime_cache(key,body,fetched) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET body=excluded.body,fetched=excluded.fetched`, key, b, time.Now().Unix())
	return err
}
