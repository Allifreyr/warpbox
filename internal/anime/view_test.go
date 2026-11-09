package anime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Allifreyr/warpbox/internal/config"
	"github.com/Allifreyr/warpbox/internal/metadata"
)

type fakeProvider struct {
	title string
	fail  bool
}

func (p *fakeProvider) Search(_ context.Context, title string) ([]Show, error) {
	s, err := p.Show(context.Background(), 1429)
	return []Show{s}, err
}
func (p *fakeProvider) Show(_ context.Context, id int) (Show, error) {
	if p.fail {
		return Show{}, fmt.Errorf("offline")
	}
	s := Show{ID: id, Name: p.title, Date: "2019-07-05"}
	for n := 0; n <= 4; n++ {
		s.Seasons = append(s.Seasons, struct {
			Number int `json:"season_number"`
		}{n})
	}
	return s, nil
}
func (p *fakeProvider) Episodes(_ context.Context, id, season int) ([]int, error) {
	if p.fail {
		return nil, fmt.Errorf("offline")
	}
	var out []int
	for n := 1; n <= []int{8, 24, 11, 22, 37}[season]; n++ {
		out = append(out, n)
	}
	return out, nil
}

func setup(t testing.TB) (*View, *fakeProvider) {
	t.Helper()
	store, err := metadata.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	v, err := New(store, config.LibraryConfig{Anime: config.AnimeConfig{Source: "anime", Name: "anime-plex", RefreshHours: 24}, VirtualPaths: []config.VirtualPathConfig{{Name: "anime", DirectoryInclude: `(?i)\[(subsplease|erai-raws|reza)\]`, FileRegex: `(?i)\.(mkv|mp4)$`, SidecarExtensions: []string{"srt", "ass"}}}})
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{title: "Dr. Stone"}
	v.Provider = p
	v.Client = nil
	return v, p
}
func add(t testing.TB, v *View, item, file int64, p string) metadata.FileRecord {
	t.Helper()
	f := metadata.FileRecord{ItemID: item, FileID: file, Path: p, Name: filepath.Base(p), Size: 1000, MimeType: "video/x-matroska"}
	if err := v.Store.UpsertFile(f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestInspectReportsRenamedSourceWithoutChangingAlias(t *testing.T) {
	v, _ := setup(t)
	ctx := context.Background()
	f := add(t, v, 1, 0, "[SubsPlease] original/[SubsPlease] Dr. Stone S2 - 01.mkv")
	if err := v.Rebuild(ctx, false); err != nil {
		t.Fatal(err)
	}
	before, err := v.Inspect(ctx)
	if err != nil || len(before) != 1 || before[0].Path == "" {
		t.Fatal(before, err)
	}
	f.Path = "[SubsPlease] renamed/[SubsPlease] Dr. Stone S2 - 01.mkv"
	if err := v.Store.UpsertFile(f); err != nil {
		t.Fatal(err)
	}
	for _, refresh := range []bool{false, true} {
		if refresh {
			if err := v.Rebuild(ctx, false); err != nil {
				t.Fatal(err)
			}
		}
		report, err := v.Inspect(ctx)
		if err != nil || len(report) != 1 {
			t.Fatal(report, err)
		}
		if report[0].OriginalPath != f.Path || report[0].Path != before[0].Path {
			t.Fatalf("stale source or changed alias: %+v", report[0])
		}
	}
	// An override keyed from inspection must match the renamed release.
	file := filepath.Join(t.TempDir(), "overrides.yml")
	if err := os.WriteFile(file, []byte("releases:\n  - release: '[SubsPlease] renamed'\n    exclude: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	v.Config.OverridesFile = file
	if err := v.Rebuild(ctx, true); err != nil {
		t.Fatal(err)
	}
	report, err := v.Inspect(ctx)
	if err != nil || len(report) != 1 || report[0].Path != "" || report[0].Reason != "excluded by override" {
		t.Fatal(report, err)
	}
}

func BenchmarkAnimeResolve(b *testing.B) {
	for _, unrelated := range []int{0, 10000} {
		b.Run(fmt.Sprintf("unrelated_%d", unrelated), func(b *testing.B) {
			v, _ := setup(b)
			ctx := context.Background()
			add(b, v, 1, 0, "[SubsPlease] batch/[SubsPlease] Dr. Stone S2 - 01.mkv")
			if err := v.Rebuild(ctx, false); err != nil {
				b.Fatal(err)
			}
			list, err := v.List(ctx, "")
			if err != nil || len(list) != 1 {
				b.Fatal(list, err)
			}
			for i := 0; i < unrelated; i++ {
				add(b, v, int64(i+2), 0, fmt.Sprintf("Movies/%d.mkv", i))
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if f, err := v.Resolve(ctx, list[0].Path); err != nil || f == nil {
					b.Fatal(f, err)
				}
			}
		})
	}
}

func TestAnimeEligibilityIncludesUnmappedItemSiblings(t *testing.T) {
	v, _ := setup(t)
	ctx := context.Background()
	v.Filter.LargestFileOnly = true
	primary := add(t, v, 1, 0, "[SubsPlease] batch/[SubsPlease] Dr. Stone S2 - 01.mkv")
	subtitle := add(t, v, 1, 1, strings.TrimSuffix(primary.Path, ".mkv")+".en.srt")
	if err := v.Rebuild(ctx, false); err != nil {
		t.Fatal(err)
	}
	list, err := v.List(ctx, "")
	if err != nil || len(list) != 2 {
		t.Fatal(list, err)
	}
	// A newly synced, unmapped sibling must still participate in selection.
	larger := add(t, v, 1, 2, "[SubsPlease] batch/[SubsPlease] Dr. Stone S2 - 02.mkv")
	larger.Size = primary.Size + 1
	if err := v.Store.UpsertFile(larger); err != nil {
		t.Fatal(err)
	}
	for _, f := range list {
		if got, err := v.Resolve(ctx, f.Path); err != nil || got != nil {
			t.Fatalf("ineligible video or sidecar resolved: %+v %v", got, err)
		}
	}
	if got, err := v.List(ctx, ""); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	// Removing the competing sibling restores both paths immediately.
	primary.SyncTag, subtitle.SyncTag = 1, 1
	for _, f := range []metadata.FileRecord{primary, subtitle} {
		if err := v.Store.UpsertFile(f); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := v.Store.PruneBySyncTag(1, metadata.SourceTorrent); err != nil {
		t.Fatal(err)
	}
	for _, f := range list {
		if got, err := v.Resolve(ctx, f.Path); err != nil || got == nil {
			t.Fatalf("eligible sibling missing: %+v %v", got, err)
		}
	}
}

func TestUnresolvedMappingCannotResolveAsRootFile(t *testing.T) {
	v, _ := setup(t)
	ctx := context.Background()
	f := add(t, v, 1, 0, "[SubsPlease] batch/[SubsPlease] Dr. Stone S2 - 00.mkv")
	if err := v.Store.PutAnimeMapping(ctx, metadata.AnimeMapping{ItemID: f.ItemID, FileID: f.FileID, OriginalPath: f.Path, Reason: "unresolved special"}); err != nil {
		t.Fatal(err)
	}
	if got, err := v.Resolve(ctx, ""); err != nil || got != nil {
		t.Fatalf("root resolved an unresolved file: %+v %v", got, err)
	}
	if got, err := v.Alternatives(ctx, ""); err != nil || len(got) != 0 {
		t.Fatalf("root returned unresolved alternatives: %+v %v", got, err)
	}
	if got, err := v.List(ctx, ""); err != nil || len(got) != 0 {
		t.Fatalf("unresolved file appeared in root listing: %+v %v", got, err)
	}
}

func TestParseRealReleaseNames(t *testing.T) {
	for _, tt := range []struct {
		path, title string
		season, ep  int
		explicit    bool
	}{
		{"[SubsPlease] Dr. Stone S2 (01-11) (1080p) [Batch]/[SubsPlease] Dr. Stone S2 - 04v2 (1080p) [BCE0AA4D].mkv", "Dr. Stone", 2, 4, false},
		{"[Erai-raws] Dr. Stone - 01 ~ 24 [1080p][Multiple Subtitle]/[Erai-raws] Dr. Stone - 01 [1080p][Multiple Subtitle].mkv", "Dr. Stone", 0, 1, false},
		{"[Reza] Ghost Stories BD/[Reza] Ghost Stories - S01E01.mkv", "Ghost Stories", 1, 1, true},
		{"[SubsPlease] Jujutsu Kaisen (25-47) (1080p) [Batch]/[SubsPlease] Jujutsu Kaisen - 25v2 (1080p) [1042A094].mkv", "Jujutsu Kaisen", 0, 25, false},
		{"[SubsPlease] Bleach - Sennen Kessen Hen (41-50)/[SubsPlease] Bleach - Sennen Kessen Hen - 41 (1080p) [00E43FC1].mkv", "Bleach - Sennen Kessen Hen", 0, 41, false},
		{"[SubsPlease] Kaijuu 8-gou - Narumi no Heijitsu/[SubsPlease] Kaijuu 8-gou - Narumi no Heijitsu - 01 (1080p).mkv", "Kaijuu 8-gou - Narumi no Heijitsu", 0, 1, false},
		{"[SubsPlease] Tsue to Tsurugi no Wistoria S2 (00-12)/[SubsPlease] Tsue to Tsurugi no Wistoria S2 - 00 (1080p).mkv", "Tsue to Tsurugi no Wistoria", 2, 0, false},
	} {
		t.Run(tt.path, func(t *testing.T) {
			r, err := Parse(tt.path)
			if err != nil || r.Title != tt.title || r.Season != tt.season || r.Episode != tt.ep || r.Explicit != tt.explicit {
				t.Fatalf("got %+v %v", r, err)
			}
		})
	}
	for _, p := range []string{"show/readme.txt", "show/S01E05-E01.mkv", "show/S01E01-E100000000.mkv"} {
		if _, err := Parse(p); err == nil {
			t.Fatalf("accepted %s", p)
		}
	}
}

func TestDrStoneGroupsSeasonsAndPinsAcrossRefresh(t *testing.T) {
	v, p := setup(t)
	ctx := context.Background()
	v.Catalog = Catalog{Titles: map[string][]Entry{normalize("Dr. Stone"): {{ID: 1, TMDB: "1429", Season: "1"}}}}
	files := []string{
		"[Erai-raws] Dr. Stone - 01 ~ 24 [1080p]/[Erai-raws] Dr. Stone - 01 [1080p].mkv",
		"[SubsPlease] Dr. Stone S2 (01-11)/[SubsPlease] Dr. Stone S2 - 04v2 (1080p) [ABC].mkv",
		"[SubsPlease] Dr. Stone S3 (01-22)/[SubsPlease] Dr. Stone S3 - 01v2 (1080p).mkv",
		"[SubsPlease] Dr. Stone S4 (01-24)/[SubsPlease] Dr. Stone S4 - 01v2 (1080p).mkv",
		"[SubsPlease] Dr. Stone S4 (25-37)/[SubsPlease] Dr. Stone S4 - 25 (1080p).mkv",
	}
	for i, f := range files {
		add(t, v, int64(i+1), 0, f)
	}
	before, _ := v.Store.AllFiles(ctx)
	if err := v.Rebuild(ctx, false); err != nil {
		t.Fatal(err)
	}
	list, err := v.List(ctx, "")
	if err != nil || len(list) != 5 {
		report, _ := v.Inspect(ctx)
		t.Fatalf("%v %+v", err, report)
	}
	root := strings.Split(list[0].Path, "/")[0]
	for _, f := range list {
		if strings.Split(f.Path, "/")[0] != root {
			t.Fatalf("split show: %s", f.Path)
		}
		raw, err := v.Resolve(ctx, f.Path)
		if err != nil || raw == nil || raw.Path == f.Path || raw.FileID != 0 {
			t.Fatalf("bad playback identity: %+v %v", raw, err)
		}
	}
	after, _ := v.Store.AllFiles(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("source records changed")
	}
	p.fail = true
	if err := v.Rebuild(ctx, false); err != nil {
		t.Fatal(err)
	}
	again, _ := v.List(ctx, "")
	if !reflect.DeepEqual(list, again) {
		t.Fatal("outage renamed published files")
	}
	p.fail = false
	p.title = "A changed metadata title"
	add(t, v, 6, 0, "[SubsPlease] Dr. Stone S4 batch/[SubsPlease] Dr. Stone S4 - 26 (1080p).mkv")
	// Explicit override supplies the ID after the metadata title change.
	overrideFile := filepath.Join(t.TempDir(), "overrides.yml")
	os.WriteFile(overrideFile, []byte("releases:\n  - release: '[SubsPlease] Dr. Stone S4 batch'\n    tmdb_id: 1429\n"), 0600)
	v.Config.OverridesFile = overrideFile
	if err := v.Rebuild(ctx, false); err != nil {
		t.Fatal(err)
	}
	again, _ = v.List(ctx, "")
	if len(again) != 6 {
		t.Fatal(again)
	}
	for _, f := range again {
		if !strings.HasPrefix(f.Path, root+"/") {
			t.Fatalf("metadata changed show folder: %s", f.Path)
		}
	}
}

func TestDuplicatesSidecarsEligibilityAndUnresolved(t *testing.T) {
	v, _ := setup(t)
	ctx := context.Background()
	a := add(t, v, 1, 0, "[SubsPlease] batch1/[SubsPlease] Dr. Stone S2 - 01 (1080p) [ABC].mkv")
	add(t, v, 2, 0, "[SubsPlease] batch2/"+filepath.Base(a.Path))
	add(t, v, 1, 1, strings.TrimSuffix(a.Path, ".mkv")+".en.forced.srt")
	add(t, v, 3, 0, "[SubsPlease] batch3/[SubsPlease] Dr. Stone S2 - 01v2 (1080p) [DEF].mkv")
	add(t, v, 4, 0, "[SubsPlease] batch4/[SubsPlease] Dr. Stone S2 - 00 (1080p).mkv")
	add(t, v, 5, 0, "[SubsPlease] batch5/[SubsPlease] Dr. Stone - 25 (1080p).mkv")
	if err := v.Rebuild(ctx, false); err != nil {
		t.Fatal(err)
	}
	list, _ := v.List(ctx, "")
	if len(list) != 3 {
		report, _ := v.Inspect(ctx)
		t.Fatalf("%+v %+v", list, report)
	}
	var duplicate string
	for _, f := range list {
		alts, _ := v.Alternatives(ctx, f.Path)
		if len(alts) == 2 {
			duplicate = f.Path
		}
	}
	if duplicate == "" {
		t.Fatal("overlapping batches not collapsed")
	}
	if files, _ := v.List(ctx, strings.Split(duplicate, "/")[0]+"other"); len(files) != 0 {
		t.Fatal("prefix crossed directory boundary")
	}
	// Force-routing change must immediately remove the source from this view.
	a.FilterTags = "forcedmovies"
	if err := v.Store.UpsertFile(a); err != nil {
		t.Fatal(err)
	}
	alts, _ := v.Alternatives(ctx, duplicate)
	if len(alts) != 1 || alts[0].ItemID != 2 {
		t.Fatalf("ineligible file leaked: %+v", alts)
	}
	// Deleting source rows cannot leave playable ghosts in the projection.
	v.Store.PruneBySyncTag(999, metadata.SourceTorrent)
	empty, _ := v.List(ctx, "")
	if len(empty) != 0 {
		t.Fatal(empty)
	}
}

func TestOverridesAbsoluteRangesSpecialsAndFiles(t *testing.T) {
	v, _ := setup(t)
	ctx := context.Background()
	for i, p := range []string{
		"[SubsPlease] absolute/[SubsPlease] Dr. Stone - 25 (1080p).mkv",
		"[SubsPlease] range/[SubsPlease] Dr. Stone - 48 (1080p).mkv",
		"[SubsPlease] special/[SubsPlease] Dr. Stone S2 - 00 (1080p).mkv",
		"[SubsPlease] weird/not-an-episode.mkv",
	} {
		add(t, v, int64(i+1), 0, p)
	}
	overrideFile := filepath.Join(t.TempDir(), "overrides.yml")
	body := `releases:
  - release: '[SubsPlease] absolute'
    tmdb_id: 1429
    numbering: absolute
  - release: '[SubsPlease] range'
    tmdb_id: 1429
    ranges:
      - {start: 48, end: 59, season: 3, episode_start: 1}
  - release: '[SubsPlease] special'
    tmdb_id: 1429
    episodes:
      0: {season: 0, episodes: [3]}
  - release: '[SubsPlease] weird'
    tmdb_id: 1429
    files:
      not-an-episode.mkv: {season: 1, episodes: [1, 2]}
`
	if err := os.WriteFile(overrideFile, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	v.Config.OverridesFile = overrideFile
	if err := v.Rebuild(ctx, true); err != nil {
		t.Fatal(err)
	}
	report, _ := v.Inspect(ctx)
	expected := []string{"S02E01", "S03E01", "S00E03", "S01E01-E02"}
	for i, m := range report {
		if m.Reason != "" || !strings.Contains(m.Path, expected[i]) {
			t.Fatalf("%+v", m)
		}
	}
	// Ambiguous titles and invalid targets fail closed.
	v.Catalog = Catalog{Titles: map[string][]Entry{normalize("Dr. Stone"): {{ID: 1, TMDB: "1", Season: "1"}, {ID: 2, TMDB: "2", Season: "1"}}}}
	f := metadata.FileRecord{Path: "[SubsPlease] x/[SubsPlease] Dr. Stone - 01.mkv"}
	m := v.resolve(ctx, f, nil)
	if !strings.Contains(m.Reason, "ambiguous") {
		t.Fatal(m)
	}
}

func TestCatalogTMDBOnlyRules(t *testing.T) {
	x := []byte(`<anime-list><anime anidbid="1" tmdbtv="1429" tmdbseason="2" tmdboffset="1"><name>Dr. Stone</name><mapping-list><mapping anidbseason="1" tvdbseason="9" start="1" end="99" offset="20"/><mapping anidbseason="1" tmdbseason="0">;3-2+3;</mapping></mapping-list></anime></anime-list>`)
	c, err := parseCatalog(x, []byte(`<animetitles><anime aid="1"><title>Stone alias</title></anime></animetitles>`))
	if err != nil {
		t.Fatal(err)
	}
	e := c.Titles[normalize("Stone alias")][0]
	season, eps, ok := e.target(1)
	if !ok || season != 2 || !reflect.DeepEqual(eps, []int{2}) {
		t.Fatalf("%d %v %v", season, eps, ok)
	}
	season, eps, ok = e.target(3)
	if !ok || season != 0 || !reflect.DeepEqual(eps, []int{2, 3}) {
		t.Fatal(season, eps, ok)
	}
}
