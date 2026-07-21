package library

import (
	"strings"
	"testing"

	"github.com/mainlink0435/warpbox/internal/metadata"
)

func TestNewFilter(t *testing.T) {
	f, err := NewFilter("/tv", "(?i)season", "", `.*\.(mkv|mp4)$`, true)
	if err != nil {
		t.Fatalf("NewFilter failed: %v", err)
	}
	if f.Mount != "/tv" {
		t.Errorf("Mount = %q, want /tv", f.Mount)
	}
	if !f.LargestFileOnly {
		t.Error("LargestFileOnly should be true")
	}
}

func TestNewFilter_EmptyRegex(t *testing.T) {
	f, err := NewFilter("/all", "", "", "", false)
	if err != nil {
		t.Fatalf("NewFilter failed: %v", err)
	}
	if f.DirectoryInclude != nil {
		t.Error("DirectoryInclude should be nil for empty string")
	}
	if f.DirectoryExclude != nil {
		t.Error("DirectoryExclude should be nil for empty string")
	}
	if f.FileRegex != nil {
		t.Error("FileRegex should be nil for empty string")
	}
}

func TestNewFilter_InvalidInclude(t *testing.T) {
	_, err := NewFilter("/bad", "[invalid", "", "", false)
	if err == nil {
		t.Fatal("expected error for invalid include regex")
	}
}

func TestNewFilter_InvalidExclude(t *testing.T) {
	_, err := NewFilter("/bad", "", "[invalid", "", false)
	if err == nil {
		t.Fatal("expected error for invalid exclude regex")
	}
}

func TestNewFilter_InvalidFile(t *testing.T) {
	_, err := NewFilter("/bad", "", "", "[invalid", false)
	if err == nil {
		t.Fatal("expected error for invalid file regex")
	}
}

func TestExtractDirectory(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"Movie.Name.1999/file.mkv", "Movie.Name.1999"},
		{"TV.Show.S01/Season 1/ep1.mkv", "TV.Show.S01"},
		{"singlefile.mkv", "singlefile.mkv"},
		{"", ""},
		{"a/b/c/d.mkv", "a"},
	}
	for _, tt := range tests {
		got := ExtractDirectory(tt.path)
		if got != tt.want {
			t.Errorf("ExtractDirectory(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestExtractRelativePath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"Movie.Name.1999/file.mkv", "file.mkv"},
		{"TV.Show.S01/Season 1/ep1.mkv", "Season 1/ep1.mkv"},
		{"singlefile.mkv", "singlefile.mkv"},
		{"", ""},
	}
	for _, tt := range tests {
		got := ExtractRelativePath(tt.path)
		if got != tt.want {
			t.Errorf("ExtractRelativePath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

var tvRegex = "(?i)(season|episode)s?\\.?\\d?|[se]\\d\\d|\\b(tv|complete)|\\b(saison|stage)\\.?\\d|[a-z]\\s?-\\s?\\d{2,4}\\b|\\d{2,4}\\s?-\\s?\\d{2,4}\\b"

func TestMatchDirectory_Include(t *testing.T) {
	f, err := NewFilter("/tv", tvRegex, "", "", false)
	if err != nil {
		t.Fatalf("NewFilter failed: %v", err)
	}

	shouldMatch := []string{
		"Breaking.Bad.S01.1080p",
		"The.Office.Season.3.Complete",
		"Game.of.Thrones.S08E01",
		"Show.tv.Complete",
	}
	for _, dir := range shouldMatch {
		if !f.MatchDirectory(dir) {
			t.Errorf("include should match %q", dir)
		}
	}

	shouldNotMatch := []string{
		"The.Matrix.1999.1080p",
		"Inception.2010.4K",
	}
	for _, dir := range shouldNotMatch {
		if f.MatchDirectory(dir) {
			t.Errorf("include should NOT match %q", dir)
		}
	}
}

func TestMatchDirectory_Exclude(t *testing.T) {
	f, err := NewFilter("/movies", "", tvRegex, "", false)
	if err != nil {
		t.Fatalf("NewFilter failed: %v", err)
	}

	// Should NOT reject movies
	shouldMatch := []string{
		"The.Matrix.1999.1080p",
		"Inception.2010.4K",
	}
	for _, dir := range shouldMatch {
		if !f.MatchDirectory(dir) {
			t.Errorf("exclude only TV should not reject %q", dir)
		}
	}

	// Should reject TV shows
	shouldNotMatch := []string{
		"Breaking.Bad.S01.1080p",
		"The.Office.Season.3.Complete",
		"Game.of.Thrones.S08E01",
	}
	for _, dir := range shouldNotMatch {
		if f.MatchDirectory(dir) {
			t.Errorf("exclude TV should reject %q", dir)
		}
	}
}

func TestMatchDirectory_IncludeAndExclude(t *testing.T) {
	// Include "season" (must have this to pass), exclude "S01" (must NOT have this)
	f, err := NewFilter("/test", "(?i)season", "(?i)S01", "", false)
	if err != nil {
		t.Fatalf("NewFilter failed: %v", err)
	}
	if !f.MatchDirectory("Show.Season.1") {
		t.Error("Show.Season.1 should match")
	}
	if f.MatchDirectory("Show.Season.1.S01") {
		t.Error("Show.Season.1.S01 should be excluded (matches exclude)")
	}
	if f.MatchDirectory("Movie.2024") {
		t.Error("Movie.2024 should NOT match (no include match)")
	}
}

func TestMatchFile(t *testing.T) {
	f, err := NewFilter("/movies", "", "", `.*\.(mkv|mp4|avi)$`, false)
	if err != nil {
		t.Fatalf("NewFilter failed: %v", err)
	}

	if !f.MatchFile("movie.mkv") {
		t.Error("should match .mkv")
	}
	if !f.MatchFile("show.mp4") {
		t.Error("should match .mp4")
	}
	if !f.MatchFile("clip.avi") {
		t.Error("should match .avi")
	}
	if f.MatchFile("archive.rar") {
		t.Error("should NOT match .rar")
	}
	if f.MatchFile("sample.txt") {
		t.Error("should NOT match .txt")
	}
}

func TestMatchFile_RelativePath(t *testing.T) {
	f, err := NewFilter("/tv", "", "", `.*\.(mkv|mp4)$`, false)
	if err != nil {
		t.Fatalf("NewFilter failed: %v", err)
	}

	if !f.MatchFile("Season 1/episode.mkv") {
		t.Error("should match path with subdirectories")
	}
	if f.MatchFile("Season 1/sample.txt") {
		t.Error("should NOT match non-video in subdirectory")
	}
}

func TestKeepLargest(t *testing.T) {
	records := []metadata.FileRecord{
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Movie.A/file1.mkv", Size: 500},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Movie.A/file2.mkv", Size: 1000},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Movie.A/featurette.mkv", Size: 200},
	}
	got := KeepLargest(records)
	if len(got) != 1 {
		t.Fatalf("expected 1 record, got %d", len(got))
	}
	if got[0].Size != 1000 {
		t.Errorf("expected largest file (1000), got %d", got[0].Size)
	}
}

func TestKeepLargest_MultipleItems(t *testing.T) {
	records := []metadata.FileRecord{
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Movie.A/main.mkv", Size: 1000},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Movie.A/featurette.mkv", Size: 200},
		{ItemID: 2, Source: metadata.SourceTorrent, Path: "Show.B/ep1.mkv", Size: 500},
		{ItemID: 2, Source: metadata.SourceTorrent, Path: "Show.B/ep2.mkv", Size: 600},
	}
	got := KeepLargest(records)
	if len(got) != 2 {
		t.Fatalf("expected 2 records, got %d", len(got))
	}
	if got[0].Size != 1000 || got[0].ItemID != 1 {
		t.Errorf("expected item 1 largest (1000), got item %d size %d", got[0].ItemID, got[0].Size)
	}
	if got[1].Size != 600 || got[1].ItemID != 2 {
		t.Errorf("expected item 2 largest (600), got item %d size %d", got[1].ItemID, got[1].Size)
	}
}

func TestKeepLargest_SourceDisambiguation(t *testing.T) {
	records := []metadata.FileRecord{
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "item/file1.mkv", Size: 500},
		{ItemID: 1, Source: metadata.SourceUsenet, Path: "item/file2.mkv", Size: 600},
	}
	got := KeepLargest(records)
	if len(got) != 2 {
		t.Fatalf("expected 2 records (different sources), got %d", len(got))
	}
}

func TestApplyFilter_IncludeOnly(t *testing.T) {
	f, err := NewFilter("/tv", "(?i)S01|season", "", `.*\.(mkv|mp4)$`, false)
	if err != nil {
		t.Fatalf("NewFilter failed: %v", err)
	}
	records := []metadata.FileRecord{
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "TV.Show.S01/ep1.mkv", Size: 1000},
		{ItemID: 2, Source: metadata.SourceTorrent, Path: "The.Matrix.1999/movie.mkv", Size: 5000},
	}
	got := f.Apply(records)
	if len(got) != 1 {
		t.Fatalf("expected 1 record (only TV), got %d", len(got))
	}
	if got[0].ItemID != 1 {
		t.Errorf("expected TV show (item 1), got item %d", got[0].ItemID)
	}
}

func TestApplyFilter_ExcludeOnly(t *testing.T) {
	f, err := NewFilter("/movies", "", "(?i)S01|season", `.*\.(mkv|mp4)$`, false)
	if err != nil {
		t.Fatalf("NewFilter failed: %v", err)
	}
	records := []metadata.FileRecord{
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "TV.Show.S01/ep1.mkv", Size: 1000},
		{ItemID: 2, Source: metadata.SourceTorrent, Path: "The.Matrix.1999/movie.mkv", Size: 5000},
	}
	got := f.Apply(records)
	if len(got) != 1 {
		t.Fatalf("expected 1 record (only movie), got %d", len(got))
	}
	if got[0].ItemID != 2 {
		t.Errorf("expected movie (item 2), got item %d", got[0].ItemID)
	}
}

func TestApplyFilter_LargestFileOnly(t *testing.T) {
	f, err := NewFilter("/movies", "", "", `.*\.(mkv|mp4)$`, true)
	if err != nil {
		t.Fatalf("NewFilter failed: %v", err)
	}
	records := []metadata.FileRecord{
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Movie/file.mkv", Size: 5000},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Movie/featurette.mkv", Size: 200},
	}
	got := f.Apply(records)
	if len(got) != 1 {
		t.Fatalf("expected 1 record (largest), got %d", len(got))
	}
	if got[0].Size != 5000 {
		t.Errorf("expected largest (5000), got %d", got[0].Size)
	}
}

func TestNormalizeSidecarExtensions(t *testing.T) {
	got := NormalizeSidecarExtensions([]string{".SRT", " ass ", "", ".", "..srt", "srt"})
	if len(got) != 2 {
		t.Fatalf("expected 2 unique exts, got %d: %v", len(got), got)
	}
	if _, ok := got["srt"]; !ok {
		t.Error("missing srt")
	}
	if _, ok := got[".srt"]; ok {
		t.Error("multi-leading-dot ..srt must normalize to srt, not .srt")
	}
	if _, ok := got["ass"]; !ok {
		t.Error("missing ass")
	}
	if NormalizeSidecarExtensions(nil) != nil {
		t.Error("nil input should yield nil map")
	}
}

func TestPrimaryStem(t *testing.T) {
	if got := PrimaryStem("Movie/Show.S01E01.mkv"); got != "Show.S01E01" {
		t.Errorf("got %q", got)
	}
	if got := PrimaryStem("file.MP4"); got != "file" {
		t.Errorf("got %q", got)
	}
}

func TestSidecarMatchesPrimary(t *testing.T) {
	exts := NormalizeSidecarExtensions([]string{"srt", "ass"})
	cases := []struct {
		sub, vid string
		want     bool
	}{
		{"Movie/Show.S01E01.srt", "Movie/Show.S01E01.mkv", true},
		{"Movie/Show.S01E01.en.srt", "Movie/Show.S01E01.mkv", true},
		{"Movie/Show.S01E01.en-us.srt", "Movie/Show.S01E01.mkv", true},
		{"Movie/Show.S01E01.es-419.srt", "Movie/Show.S01E01.mkv", true}, // UN M.49 region
		{"Movie/Show.S01E01.zh-hans.srt", "Movie/Show.S01E01.mkv", true},
		{"Movie/Show.S01E01.eng.forced.ass", "Movie/Show.S01E01.mkv", true},
		{"Movie/Show.S01E01.ASS", "Movie/Show.S01E01.mkv", true},
		{"Movie/other.srt", "Movie/Show.S01E01.mkv", false},
		{"Movie/Show.S01E02.srt", "Movie/Show.S01E01.mkv", false},
		// Bad regions / junk middle tokens
		{"Movie/Show.S01E01.en-1234.srt", "Movie/Show.S01E01.mkv", false}, // 4-digit region invalid
		{"Movie/Show.S01E01.english.srt", "Movie/Show.S01E01.mkv", false},  // full language name not a tag
		// Arbitrary middle tokens are not language tags — need matching video stem.
		{"Movie/Movie.2020.Sample.srt", "Movie/Movie.2020.mkv", false},
		{"Movie/Movie.2020.Featurette.en.srt", "Movie/Movie.2020.mkv", false},
		{"Movie/Movie.2020.Featurette.en.srt", "Movie/Movie.2020.Featurette.mkv", true},
		{"Movie/Movie.2020.Extras.srt", "Movie/Movie.2020.mkv", false},
	}
	for _, tc := range cases {
		if got := SidecarMatchesPrimary(tc.sub, tc.vid, exts); got != tc.want {
			t.Errorf("SidecarMatchesPrimary(%q,%q)=%v want %v", tc.sub, tc.vid, got, tc.want)
		}
	}
}

func TestApplyFilter_LargestWithSidecars(t *testing.T) {
	f, err := NewFilter("/movies", "", "", `.*\.(mkv|mp4)$`, true)
	if err != nil {
		t.Fatal(err)
	}
	f.WithSidecarExtensions([]string{"srt", "ass"})
	records := []metadata.FileRecord{
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Movie/Movie.2020.mkv", Size: 5_000_000_000},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Movie/sample.mkv", Size: 20_000_000},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Movie/Movie.2020.en.srt", Size: 50_000},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Movie/Movie.2020.forced.ass", Size: 40_000},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Movie/unrelated.srt", Size: 10_000},
	}
	got := f.Apply(records)
	paths := map[string]bool{}
	for _, r := range got {
		paths[r.Path] = true
	}
	if !paths["Movie/Movie.2020.mkv"] {
		t.Error("expected main video")
	}
	if paths["Movie/sample.mkv"] {
		t.Error("sample should be dropped by largest_file_only")
	}
	if !paths["Movie/Movie.2020.en.srt"] || !paths["Movie/Movie.2020.forced.ass"] {
		t.Errorf("expected matching subs, got %v", paths)
	}
	if paths["Movie/unrelated.srt"] {
		t.Error("unrelated srt should not match stem")
	}
	if len(got) != 3 {
		t.Errorf("expected 3 files, got %d: %v", len(got), paths)
	}
}

// Longest-stem: Featurette.en.srt must not attach to main Movie.2020.mkv when
// Featurette.mkv lost largest_file_only.
func TestApplyFilter_SidecarLongestStemNotMain(t *testing.T) {
	f, err := NewFilter("/movies", "", "", `.*\.mkv$`, true)
	if err != nil {
		t.Fatal(err)
	}
	f.WithSidecarExtensions([]string{"srt"})
	records := []metadata.FileRecord{
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "P/Movie.2020.mkv", Size: 5_000_000_000},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "P/Movie.2020.Featurette.mkv", Size: 200_000_000},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "P/Movie.2020.en.srt", Size: 50_000},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "P/Movie.2020.Featurette.en.srt", Size: 40_000},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "P/Movie.2020.Sample.srt", Size: 5_000},
	}
	got := f.Apply(records)
	paths := map[string]bool{}
	for _, r := range got {
		paths[r.Path] = true
	}
	if !paths["P/Movie.2020.mkv"] {
		t.Fatal("expected main video")
	}
	if paths["P/Movie.2020.Featurette.mkv"] {
		t.Error("featurette video should be dropped")
	}
	if !paths["P/Movie.2020.en.srt"] {
		t.Error("main en.srt should attach to main video")
	}
	if paths["P/Movie.2020.Featurette.en.srt"] {
		t.Error("featurette srt must not attach to shorter-stem main")
	}
	if paths["P/Movie.2020.Sample.srt"] {
		t.Error("sample srt with no matching kept video should drop")
	}
}

func TestApplyFilter_LargestWithSrtInRegexStillDropsWithoutSidecars(t *testing.T) {
	// file_regex includes srt but no sidecar_extensions → KeepLargest drops tiny srt.
	f, err := NewFilter("/movies", "", "", `.*\.(mkv|srt)$`, true)
	if err != nil {
		t.Fatal(err)
	}
	records := []metadata.FileRecord{
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "M/main.mkv", Size: 1000},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "M/main.srt", Size: 10},
	}
	got := f.Apply(records)
	if len(got) != 1 || got[0].Path != "M/main.mkv" {
		t.Fatalf("expected only main.mkv, got %+v", got)
	}
}

func TestApplyFilter_SidecarsSkipMinSize(t *testing.T) {
	f, err := NewFilter("/tv", "", "", `.*\.mkv$`, true)
	if err != nil {
		t.Fatal(err)
	}
	f.WithSizeBounds(300*1024*1024, 0)
	f.WithSidecarExtensions([]string{"srt"})
	records := []metadata.FileRecord{
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "S/ep.mkv", Size: 500 * 1024 * 1024},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "S/ep.en.srt", Size: 80_000},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "S/tiny.mkv", Size: 50 * 1024 * 1024},
	}
	got := f.Apply(records)
	paths := map[string]bool{}
	for _, r := range got {
		paths[r.Path] = true
	}
	if !paths["S/ep.mkv"] || !paths["S/ep.en.srt"] {
		t.Errorf("expected ep + srt, got %v", paths)
	}
	if paths["S/tiny.mkv"] {
		t.Error("tiny video under min_file_size should drop")
	}
}

func TestApplyFilter_SidecarsWithoutLargest(t *testing.T) {
	f, err := NewFilter("/tv", "", "", `.*\.mkv$`, false)
	if err != nil {
		t.Fatal(err)
	}
	f.WithSidecarExtensions([]string{"srt"})
	records := []metadata.FileRecord{
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "S/e1.mkv", Size: 100},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "S/e2.mkv", Size: 200},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "S/e1.srt", Size: 1},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "S/e2.en.srt", Size: 1},
	}
	got := f.Apply(records)
	if len(got) != 4 {
		t.Fatalf("expected all 4, got %d", len(got))
	}
}

// Regex-failed same-stem sibling must not steal longest-stem ownership.
// Movie.mp4 fails file_regex; Movie.mkv is kept; srt must still attach.
func TestApplyFilter_SidecarIgnoresRegexFailedSibling(t *testing.T) {
	f, err := NewFilter("/movies", "", "", `.*\.mkv$`, true)
	if err != nil {
		t.Fatal(err)
	}
	f.WithSidecarExtensions([]string{"srt"})
	// Put mp4 first so order-dependent first-wins would wrongly pick it if scanned.
	records := []metadata.FileRecord{
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "M/Movie.mp4", Size: 9_000_000_000},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "M/Movie.mkv", Size: 5_000_000_000},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "M/Movie.en.srt", Size: 50_000},
	}
	got := f.Apply(records)
	paths := map[string]bool{}
	for _, r := range got {
		paths[r.Path] = true
	}
	if !paths["M/Movie.mkv"] {
		t.Fatal("expected mkv primary")
	}
	if paths["M/Movie.mp4"] {
		t.Error("mp4 should fail file_regex")
	}
	if !paths["M/Movie.en.srt"] {
		t.Error("srt must attach to kept mkv, not be stolen by regex-failed mp4")
	}
}

// Equal stem length: prefer the kept primary over a smaller same-stem copy.
func TestApplyFilter_SidecarEqualStemPrefersKept(t *testing.T) {
	f, err := NewFilter("/movies", "", "", `.*\.mkv$`, true)
	if err != nil {
		t.Fatal(err)
	}
	f.WithSidecarExtensions([]string{"srt"})
	// Smaller path first alphabetically-ish; largest_file_only keeps the larger.
	records := []metadata.FileRecord{
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Pack/B/Movie.mkv", Size: 100},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Pack/A/Movie.mkv", Size: 5000},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Pack/Movie.en.srt", Size: 10},
	}
	// Note: sidecar path stem is Movie; both videos stem Movie.
	// Put srt at Pack/ root with basename Movie.en.srt
	records[2].Path = "Pack/A/Movie.en.srt"
	got := f.Apply(records)
	paths := map[string]bool{}
	for _, r := range got {
		paths[r.Path] = true
	}
	if !paths["Pack/A/Movie.mkv"] {
		t.Fatal("expected larger A/Movie.mkv kept")
	}
	if paths["Pack/B/Movie.mkv"] {
		t.Error("smaller B copy should drop")
	}
	if !paths["Pack/A/Movie.en.srt"] {
		t.Error("srt must attach to kept A copy on equal stem length")
	}
}

func TestApplyFilter_SidecarPathSegmentExclude(t *testing.T) {
	f, err := NewFilter("/movies", "", "", `.*\.mkv$`, true)
	if err != nil {
		t.Fatal(err)
	}
	f, err = f.WithPathSegmentExclude(`(?i)^(extras|sample|samples)$`)
	if err != nil {
		t.Fatal(err)
	}
	f.WithSidecarExtensions([]string{"srt"})
	records := []metadata.FileRecord{
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Movie/Movie.mkv", Size: 5000},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Movie/Movie.en.srt", Size: 50},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Movie/Extras/bonus.mkv", Size: 200},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Movie/Extras/bonus.en.srt", Size: 10},
	}
	got := f.Apply(records)
	paths := map[string]bool{}
	for _, r := range got {
		paths[r.Path] = true
	}
	if !paths["Movie/Movie.mkv"] || !paths["Movie/Movie.en.srt"] {
		t.Errorf("expected main + srt, got %v", paths)
	}
	if paths["Movie/Extras/bonus.mkv"] || paths["Movie/Extras/bonus.en.srt"] {
		t.Errorf("extras path should be excluded for both video and sidecar, got %v", paths)
	}
}

func TestApplyFilter_SidecarsSkipMaxSize(t *testing.T) {
	f, err := NewFilter("/movies", "", "", `.*\.mkv$`, true)
	if err != nil {
		t.Fatal(err)
	}
	// Cap primaries at 1GB; large external audio-like sidecar should still attach.
	f.WithSizeBounds(0, 1024*1024*1024)
	f.WithSidecarExtensions([]string{"srt", "mka"})
	records := []metadata.FileRecord{
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "M/Movie.mkv", Size: 500 * 1024 * 1024},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "M/Movie.en.srt", Size: 80_000},
		// Huge companion over max_file_size — still a sidecar, must not drop.
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "M/Movie.en.mka", Size: 2 * 1024 * 1024 * 1024},
		// Oversized primary drops.
		{ItemID: 2, Source: metadata.SourceTorrent, Path: "Big/Big.mkv", Size: 5 * 1024 * 1024 * 1024},
		{ItemID: 2, Source: metadata.SourceTorrent, Path: "Big/Big.en.srt", Size: 10},
	}
	got := f.Apply(records)
	paths := map[string]bool{}
	for _, r := range got {
		paths[r.Path] = true
	}
	if !paths["M/Movie.mkv"] || !paths["M/Movie.en.srt"] || !paths["M/Movie.en.mka"] {
		t.Errorf("expected movie + srt + mka, got %v", paths)
	}
	if paths["Big/Big.mkv"] || paths["Big/Big.en.srt"] {
		t.Errorf("oversized primary item should drop (and its sub with it), got %v", paths)
	}
}

func TestApplyFilter_WithFilterTags_IncludeOverride(t *testing.T) {
	// TV filter with "forcedtv" added to the include regex.
	f, err := NewFilter("/tv", "(?i)(season|episode)s?\\.?\\d?|[se]\\d\\d|forcedtv", "", `.*\.(mkv|mp4|avi)$`, false)
	if err != nil {
		t.Fatalf("NewFilter failed: %v", err)
	}

	records := []metadata.FileRecord{
		// "Cow and Chicken" has no TV indicators — but has forcedtv tag.
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Cow and Chicken/episode.avi", Size: 500, FilterTags: "forcedtv"},
		// Normal TV show — matches via S01.
		{ItemID: 2, Source: metadata.SourceTorrent, Path: "Breaking.Bad.S01/ep1.mkv", Size: 1000},
		// Movie — no tag, no TV indicators.
		{ItemID: 3, Source: metadata.SourceTorrent, Path: "The.Matrix.1999/movie.mkv", Size: 5000},
	}

	got := f.Apply(records)
	if len(got) != 2 {
		t.Fatalf("expected 2 records (Cow+Chicken and Breaking Bad), got %d", len(got))
	}

	foundCow := false
	foundBad := false
	for _, r := range got {
		if r.ItemID == 1 {
			foundCow = true
		}
		if r.ItemID == 2 {
			foundBad = true
		}
	}
	if !foundCow {
		t.Error("expected Cow and Chicken (forcedtv tag) to be included in TV")
	}
	if !foundBad {
		t.Error("expected Breaking Bad (S01 indicator) to be included in TV")
	}
}

func TestApplyFilter_WithFilterTags_ExcludeOverride(t *testing.T) {
	// Movies filter with "forcedtv" added to the exclude regex.
	f, err := NewFilter("/movies", "", "(?i)(season|episode)s?\\.?\\d?|[se]\\d\\d|forcedtv", `.*\.(mkv|mp4|avi)$`, false)
	if err != nil {
		t.Fatalf("NewFilter failed: %v", err)
	}

	records := []metadata.FileRecord{
		// "Cow and Chicken" with forcedtv tag — should be EXCLUDED from movies.
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Cow and Chicken/episode.avi", Size: 500, FilterTags: "forcedtv"},
		// Movie — no tag, no TV indicators — should remain in movies.
		{ItemID: 2, Source: metadata.SourceTorrent, Path: "The.Matrix.1999/movie.mkv", Size: 5000},
	}

	got := f.Apply(records)
	if len(got) != 1 {
		t.Fatalf("expected 1 record (only Matrix), got %d", len(got))
	}
	if got[0].ItemID != 2 {
		t.Errorf("expected Matrix (item 2), got item %d", got[0].ItemID)
	}
}

func TestApplyFilter_EmptyFilterTags_NoChange(t *testing.T) {
	// Verify that empty FilterTags produces identical behavior to current code.
	f, err := NewFilter("/movies", "", "(?i)S01|season", `.*\.(mkv|mp4)$`, false)
	if err != nil {
		t.Fatalf("NewFilter failed: %v", err)
	}

	records := []metadata.FileRecord{
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "TV.Show.S01/ep1.mkv", Size: 1000, FilterTags: ""},
		{ItemID: 2, Source: metadata.SourceTorrent, Path: "The.Matrix.1999/movie.mkv", Size: 5000, FilterTags: ""},
	}

	got := f.Apply(records)
	if len(got) != 1 {
		t.Fatalf("expected 1 record (only movie), got %d", len(got))
	}
	if got[0].ItemID != 2 {
		t.Errorf("expected movie (item 2), got item %d", got[0].ItemID)
	}
}

func TestApplyFilter_ForceTagIntoMoviesDespiteTVName(t *testing.T) {
	// Semantic force: forcedmovies bypasses exclude even when name has "complete".
	f, err := NewFilter("/movies", "", "(?i)(season|episode)s?\\.?\\d?|[se]\\d\\d|\\b(tv|complete)", `.*\.(mkv|mp4|avi)$`, false)
	if err != nil {
		t.Fatalf("NewFilter failed: %v", err)
	}

	records := []metadata.FileRecord{
		// Would be excluded by "complete" without force tag.
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Chowder Complete Collection/movie.mkv", Size: 5000, FilterTags: "forcedmovies"},
		{ItemID: 2, Source: metadata.SourceTorrent, Path: "The.Matrix.1999/movie.mkv", Size: 5000},
		{ItemID: 3, Source: metadata.SourceTorrent, Path: "Breaking.Bad.S01/ep1.mkv", Size: 1000},
	}

	got := f.Apply(records)
	if len(got) != 2 {
		t.Fatalf("expected 2 records (Chowder + Matrix), got %d", len(got))
	}
	foundChowder, foundMatrix := false, false
	for _, r := range got {
		if r.ItemID == 1 {
			foundChowder = true
		}
		if r.ItemID == 2 {
			foundMatrix = true
		}
	}
	if !foundChowder {
		t.Error("expected Chowder (forcedmovies) in movies despite Complete in name")
	}
	if !foundMatrix {
		t.Error("expected Matrix to stay in movies")
	}
}

func TestApplyFilter_ForceTagExcludesOtherMounts(t *testing.T) {
	// forcedmovies on item → not in TV even if name matches TV include.
	tv, err := NewFilter("/tv", "(?i)(season|episode)s?\\.?\\d?|[se]\\d\\d|\\b(tv|complete)", "", `.*\.(mkv|mp4|avi)$`, false)
	if err != nil {
		t.Fatalf("NewFilter failed: %v", err)
	}

	records := []metadata.FileRecord{
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Chowder Complete Collection/movie.mkv", Size: 5000, FilterTags: "forcedmovies"},
		{ItemID: 2, Source: metadata.SourceTorrent, Path: "Breaking.Bad.S01/ep1.mkv", Size: 1000},
	}

	got := tv.Apply(records)
	if len(got) != 1 {
		t.Fatalf("expected 1 record (only Breaking Bad), got %d", len(got))
	}
	if got[0].ItemID != 2 {
		t.Errorf("expected Breaking Bad (item 2), got item %d", got[0].ItemID)
	}
}

func TestApplyFilter_ForceTagAnimeMount(t *testing.T) {
	anime, err := NewFilter("/anime", "(?i)subsplease", "", `.*\.mkv$`, false)
	if err != nil {
		t.Fatalf("NewFilter failed: %v", err)
	}
	movies, err := NewFilter("/movies", "", "(?i)S01|season", `.*\.mkv$`, false)
	if err != nil {
		t.Fatalf("NewFilter failed: %v", err)
	}

	records := []metadata.FileRecord{
		// No subsplease in name — only force tag routes to anime.
		{ItemID: 1, Path: "Odd Title/ep.mkv", Size: 500, FilterTags: "forcedanime"},
		{ItemID: 2, Path: "The.Matrix.1999/m.mkv", Size: 5000},
	}

	a := anime.Apply(records)
	if len(a) != 1 || a[0].ItemID != 1 {
		t.Fatalf("anime mount: expected only forced item, got %+v", a)
	}
	m := movies.Apply(records)
	if len(m) != 1 || m[0].ItemID != 2 {
		t.Fatalf("movies: expected only Matrix (forcedanime excluded), got %+v", m)
	}
}

func TestWithSizeBounds(t *testing.T) {
	f, err := NewFilter("/m", "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	f.WithSizeBounds(100, 1000)
	if f.MinSize != 100 || f.MaxSize != 1000 {
		t.Errorf("WithSizeBounds: got min=%d max=%d", f.MinSize, f.MaxSize)
	}
}

func TestMatchSize(t *testing.T) {
	f := &Filter{MinSize: 100, MaxSize: 1000}
	if !f.MatchSize(100) || !f.MatchSize(500) || !f.MatchSize(1000) {
		t.Error("sizes in range should match")
	}
	if f.MatchSize(99) || f.MatchSize(1001) {
		t.Error("sizes out of range should not match")
	}
	// Zero bounds = unlimited.
	open := &Filter{}
	if !open.MatchSize(0) || !open.MatchSize(1<<40) {
		t.Error("zero min/max should accept any size")
	}
}

func TestApplyFilter_MinMaxFileSize(t *testing.T) {
	f, err := NewFilter("/movies", "", "", `.*\.mkv$`, false)
	if err != nil {
		t.Fatalf("NewFilter failed: %v", err)
	}
	f.MinSize = 300
	f.MaxSize = 5000

	records := []metadata.FileRecord{
		{ItemID: 1, Path: "A/sample.mkv", Size: 50},
		{ItemID: 2, Path: "B/main.mkv", Size: 1000},
		{ItemID: 3, Path: "C/remux.mkv", Size: 9000},
		{ItemID: 4, Path: "D/ok.mkv", Size: 5000},
	}
	got := f.Apply(records)
	if len(got) != 2 {
		t.Fatalf("expected 2 records in range, got %d", len(got))
	}
	if got[0].ItemID != 2 || got[1].ItemID != 4 {
		t.Errorf("unexpected items: %+v", got)
	}
}

func TestApplyFilter_SizeThenLargest(t *testing.T) {
	// Sample under min is dropped before largest_file_only picks a winner.
	f, err := NewFilter("/movies", "", "", `.*\.mkv$`, true)
	if err != nil {
		t.Fatalf("NewFilter failed: %v", err)
	}
	f.MinSize = 400

	records := []metadata.FileRecord{
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Movie/sample.mkv", Size: 100},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Movie/feature.mkv", Size: 2000},
		{ItemID: 1, Source: metadata.SourceTorrent, Path: "Movie/extra.mkv", Size: 500},
	}
	got := f.Apply(records)
	if len(got) != 1 {
		t.Fatalf("expected 1 record, got %d", len(got))
	}
	if got[0].Size != 2000 {
		t.Errorf("expected feature (2000) after size filter + largest, got %d", got[0].Size)
	}
}

func TestMatchPathSegments(t *testing.T) {
	f, err := NewFilter("/tv", "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WithPathSegmentExclude(`(?i)^(extras|specials|featurettes)$`); err != nil {
		t.Fatal(err)
	}
	if !f.MatchPathSegments("Show/Season 1/ep.mkv") {
		t.Error("season path should keep")
	}
	if f.MatchPathSegments("Show/Extras/trailer.mkv") {
		t.Error("Extras segment should drop")
	}
	if f.MatchPathSegments("Show/Specials/S00E01.mkv") {
		t.Error("Specials segment should drop")
	}
	// Top-level release title contains "Specials" but is not a segment named Specials.
	top := "Robot Chicken (2001) Season 1-11 S01-11 Specials (1080p HMAX)/Season 10/ep.mkv"
	if !f.MatchPathSegments(top) {
		t.Error("Specials in top-level title only must not drop season eps")
	}
	// Actual Specials subfolder under that pack.
	if f.MatchPathSegments("Robot Chicken (2001) Season 1-11 S01-11 Specials (1080p HMAX)/Specials/bonus.mkv") {
		t.Error("Specials folder should drop")
	}
}

func TestApplyFilter_PathSegmentExclude(t *testing.T) {
	f, err := NewFilter("/anime", "", "", `.*\.mkv$`, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WithPathSegmentExclude(`(?i)^(extras|specials|featurettes)$`); err != nil {
		t.Fatal(err)
	}
	records := []metadata.FileRecord{
		{ItemID: 1, Path: "[Reza] Ghost Stories BD/ep01.mkv", Size: 1000},
		{ItemID: 1, Path: "[Reza] Ghost Stories BD/Extras/trailer.mkv", Size: 50},
		{ItemID: 2, Path: "Show/Featurettes/making-of.mkv", Size: 200},
		{ItemID: 3, Path: "Show/Season 1/ep.mkv", Size: 800},
	}
	got := f.Apply(records)
	if len(got) != 2 {
		t.Fatalf("expected 2 records, got %d: %+v", len(got), got)
	}
	for _, r := range got {
		if strings.Contains(strings.ToLower(r.Path), "/extras/") ||
			strings.Contains(strings.ToLower(r.Path), "/featurettes/") {
			t.Errorf("should not include extras path: %s", r.Path)
		}
	}
}
