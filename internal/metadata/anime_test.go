package metadata

import (
	"context"
	"path/filepath"
	"testing"
)

func TestAnimeMappingPersistenceReadOnlyAndBoundaries(t *testing.T) {
	p := filepath.Join(t.TempDir(), "library.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	f := FileRecord{ItemID: 1, FileID: 0, Name: "raw.mkv", Path: "original/raw.mkv", Size: 1000}
	if err := s.UpsertFile(f); err != nil {
		t.Fatal(err)
	}
	m := AnimeMapping{ItemID: 1, FileID: 0, OriginalPath: f.Path, Path: "100%_Example/Season 01/Episode.mkv"}
	if err := s.PutAnimeMapping(ctx, m); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if raw, _ := s.GetFileByPath(f.Path); raw == nil || raw.FileID != 0 {
		t.Fatal("migration lost file")
	}
	for _, prefix := range []string{"100%_Example", "100%_Example/Season 01"} {
		rows, err := s.AnimeFiles(ctx, prefix, false)
		if err != nil || len(rows) != 1 || rows[0].Path != f.Path {
			t.Fatal(rows, err)
		}
	}
	for _, prefix := range []string{"100", "100%_Exampl", "100ZZExample"} {
		rows, _ := s.AnimeFiles(ctx, prefix, false)
		if len(rows) != 0 {
			t.Fatal("prefix/wildcard leak")
		}
	}
	ro, err := OpenReadOnly(p)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if rows, err := ro.AnimeMappings(ctx); err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	if err := ro.PutAnimeMapping(ctx, m); err == nil {
		t.Fatal("read-only inspection can mutate mappings")
	}
	if err := ro.UpsertFile(f); err == nil {
		t.Fatal("read-only inspection can mutate source")
	}
}

func TestAnimeItemFilesScopesSiblingsBySourceAndItem(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	ctx := context.Background()
	for _, f := range []FileRecord{
		{ItemID: 1, FileID: 0, Name: "mapped.mkv", Path: "batch/mapped.mkv"},
		{ItemID: 1, FileID: 1, Name: "unmapped.mkv", Path: "batch/unmapped.mkv"},
		{ItemID: 1, FileID: 2, Name: "mapped.en.srt", Path: "batch/mapped.en.srt"},
		{ItemID: 1, FileID: 0, Source: SourceUsenet, Name: "other.mkv", Path: "usenet/other.mkv"},
		{ItemID: 2, FileID: 0, Name: "other.mkv", Path: "movies/other.mkv"},
	} {
		if err := s.UpsertFile(f); err != nil {
			t.Fatal(err)
		}
	}
	const alias = "100%_例/Season 01/Episode.mkv"
	for _, m := range []AnimeMapping{
		{ItemID: 1, FileID: 0, Path: alias},
		{ItemID: 1, FileID: 2, Path: "100%_例/Season 01/Episode.en.srt"},
		{ItemID: 1, FileID: 0, Source: SourceUsenet, Path: "Other/Season 01/Other.mkv"},
		{ItemID: 2, FileID: 0, Reason: "unresolved"},
	} {
		if err := s.PutAnimeMapping(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		path  string
		exact bool
		count int
	}{
		{alias, true, 3}, {"100%_例", false, 3}, {"100%_例/Season 01/", false, 3},
		{"100%_", false, 0}, {"missing", true, 0}, {"Other", false, 1}, {"", true, 0},
	} {
		files, err := s.AnimeItemFiles(ctx, tt.path, tt.exact)
		if err != nil || len(files) != tt.count {
			t.Fatalf("%q: %+v %v", tt.path, files, err)
		}
		for _, f := range files {
			if f.ItemID != 1 {
				t.Fatalf("unrelated item: %+v", f)
			}
		}
	}
	if files, err := s.AnimeFiles(ctx, "", true); err != nil || len(files) != 0 {
		t.Fatalf("empty alias returned unresolved files: %+v %v", files, err)
	}
}
