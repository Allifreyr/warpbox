package library

import (
	"regexp"
	"strings"

	"github.com/mainlink0435/warpbox/internal/metadata"
)

type Filter struct {
	Mount            string
	DirectoryInclude *regexp.Regexp
	DirectoryExclude *regexp.Regexp
	FileRegex        *regexp.Regexp
	// PathSegmentExclude matches any path segment; if it matches, the file is dropped.
	PathSegmentExclude *regexp.Regexp
	LargestFileOnly    bool
	// MinSize / MaxSize are byte bounds applied after name filters.
	// Zero means no bound (unlimited). When SidecarExts is non-empty, size
	// bounds apply only to primary (non-sidecar) files.
	MinSize int64
	MaxSize int64
	// SidecarExts lists lowercase extensions without dots (e.g. "srt", "ass").
	// Empty = feature off. When set, matching sidecar files are re-attached to
	// kept primaries by basename stem after largest_file_only selection.
	SidecarExts map[string]struct{}
}

func NewFilter(mount, dirInclude, dirExclude, fileRegex string, largestFileOnly bool) (*Filter, error) {
	f := &Filter{Mount: mount, LargestFileOnly: largestFileOnly}
	if dirInclude != "" {
		r, err := regexp.Compile(dirInclude)
		if err != nil {
			return nil, err
		}
		f.DirectoryInclude = r
	}
	if dirExclude != "" {
		r, err := regexp.Compile(dirExclude)
		if err != nil {
			return nil, err
		}
		f.DirectoryExclude = r
	}
	if fileRegex != "" {
		r, err := regexp.Compile(fileRegex)
		if err != nil {
			return nil, err
		}
		f.FileRegex = r
	}
	return f, nil
}

// WithSizeBounds sets min/max file size in bytes (0 = no bound) and returns f
// for chaining after NewFilter.
func (f *Filter) WithSizeBounds(min, max int64) *Filter {
	f.MinSize = min
	f.MaxSize = max
	return f
}

// WithPathSegmentExclude compiles pattern and attaches it for segment matching.
// Empty pattern is a no-op. Returns an error if pattern is invalid.
func (f *Filter) WithPathSegmentExclude(pattern string) (*Filter, error) {
	if pattern == "" {
		return f, nil
	}
	r, err := regexp.Compile(pattern)
	if err != nil {
		return f, err
	}
	f.PathSegmentExclude = r
	return f, nil
}

// WithSidecarExtensions sets sidecar extensions (e.g. "srt", ".ASS") for sibling
// keep-after-primary selection. Empty list clears the feature. Invalid empty
// tokens are skipped. Returns f for chaining.
func (f *Filter) WithSidecarExtensions(exts []string) *Filter {
	f.SidecarExts = NormalizeSidecarExtensions(exts)
	return f
}

// NormalizeSidecarExtensions lowercases, strips leading dots, and drops empties.
func NormalizeSidecarExtensions(exts []string) map[string]struct{} {
	if len(exts) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(exts))
	for _, e := range exts {
		e = strings.ToLower(strings.TrimSpace(e))
		e = strings.TrimPrefix(e, ".")
		if e == "" {
			continue
		}
		out[e] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func ExtractDirectory(path string) string {
	if idx := strings.IndexByte(path, '/'); idx >= 0 {
		return path[:idx]
	}
	return path
}

func ExtractRelativePath(path string) string {
	if idx := strings.IndexByte(path, '/'); idx >= 0 {
		return path[idx+1:]
	}
	return path
}

func (f *Filter) MatchDirectory(name string) bool {
	if f.DirectoryInclude != nil && !f.DirectoryInclude.MatchString(name) {
		return false
	}
	if f.DirectoryExclude != nil && f.DirectoryExclude.MatchString(name) {
		return false
	}
	return true
}

func (f *Filter) MatchFile(relPath string) bool {
	if f.FileRegex == nil {
		return true
	}
	return f.FileRegex.MatchString(relPath)
}

// MatchSize reports whether size (bytes) is within configured min/max bounds.
// A zero MinSize or MaxSize means that bound is not applied.
func (f *Filter) MatchSize(size int64) bool {
	if f.MinSize > 0 && size < f.MinSize {
		return false
	}
	if f.MaxSize > 0 && size > f.MaxSize {
		return false
	}
	return true
}

// MatchPathSegments reports whether path should be kept given path_segment_exclude.
// true = keep. Empty/nil exclude keeps all paths. Any segment matching the
// regex causes exclusion (e.g. folder named Extras or Specials).
func (f *Filter) MatchPathSegments(path string) bool {
	if f.PathSegmentExclude == nil {
		return true
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == "" {
			continue
		}
		if f.PathSegmentExclude.MatchString(seg) {
			return false
		}
	}
	return true
}

// MatchDirectoryForItem applies force-tag routing, then normal include/exclude.
//
// Force tags follow the virtual path name: tag "forced" + path name (e.g. path
// "movies" → "forcedmovies"). If the item has this mount's force tag, it is
// included. If it has any other force tag, it is excluded. Otherwise directory
// include/exclude run on dir (+ filter_tags for advanced regex tags).
// The stored virtual path is never modified.
func (f *Filter) MatchDirectoryForItem(dir, filterTags string) bool {
	pathName := strings.TrimPrefix(f.Mount, "/")
	forceTags := ForceTargets(filterTags)
	if len(forceTags) > 0 {
		hasMine, hasOther := false, false
		for _, t := range forceTags {
			if MatchesMount(t, pathName) {
				hasMine = true
			} else {
				hasOther = true
			}
		}
		if hasMine {
			return true
		}
		if hasOther {
			return false
		}
	}

	matchStr := dir
	if filterTags != "" {
		matchStr = dir + " " + filterTags
	}
	return f.MatchDirectory(matchStr)
}

func (f *Filter) Apply(records []metadata.FileRecord) []metadata.FileRecord {
	// Cache key: directory + tags (force routing depends on both).
	dirMatchCache := make(map[string]bool, len(records)/2)
	sidecarsOn := len(f.SidecarExts) > 0

	primaries := make([]metadata.FileRecord, 0, len(records))
	sidecarCands := make([]metadata.FileRecord, 0)

	for _, rec := range records {
		dir := ExtractDirectory(rec.Path)
		cacheKey := dir + "\x00" + rec.FilterTags
		ok, cached := dirMatchCache[cacheKey]
		if !cached {
			ok = f.MatchDirectoryForItem(dir, rec.FilterTags)
			dirMatchCache[cacheKey] = ok
		}
		if !ok {
			continue
		}
		// Drop files under Extras/Specials/… segments (not top-level title substrings).
		if !f.MatchPathSegments(rec.Path) {
			continue
		}

		rel := ExtractRelativePath(rec.Path)
		isSidecar := sidecarsOn && IsSidecarPath(rec.Path, f.SidecarExts)

		if isSidecar {
			// Sidecars skip file_regex and size bounds (min_file_size would kill every .srt).
			sidecarCands = append(sidecarCands, rec)
			continue
		}

		if !f.MatchFile(rel) {
			continue
		}
		// Size bounds before largest_file_only so samples under min drop first.
		if !f.MatchSize(rec.Size) {
			continue
		}
		primaries = append(primaries, rec)
	}

	if f.LargestFileOnly {
		primaries = KeepLargest(primaries)
	}

	if !sidecarsOn || len(sidecarCands) == 0 {
		return primaries
	}

	return attachMatchingSidecars(primaries, sidecarCands, f.SidecarExts)
}

// knownVideoExts used to derive a primary stem for sidecar pairing.
var knownVideoExts = map[string]struct{}{
	"mkv": {}, "mp4": {}, "avi": {}, "m4v": {}, "mov": {},
	"ts": {}, "wmv": {}, "webm": {}, "m2ts": {}, "mpg": {}, "mpeg": {},
}

// FileExt returns the lowercase extension without a leading dot, or "".
func FileExt(name string) string {
	base := name
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		base = name[i+1:]
	}
	dot := strings.LastIndexByte(base, '.')
	if dot < 0 || dot == len(base)-1 {
		return ""
	}
	return strings.ToLower(base[dot+1:])
}

// IsSidecarPath reports whether path's extension is in the sidecar set.
func IsSidecarPath(path string, exts map[string]struct{}) bool {
	if len(exts) == 0 {
		return false
	}
	_, ok := exts[FileExt(path)]
	return ok
}

// PrimaryStem returns the basename without a known video extension (or last ext).
func PrimaryStem(path string) string {
	base := path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		base = path[i+1:]
	}
	dot := strings.LastIndexByte(base, '.')
	if dot <= 0 {
		return base
	}
	ext := strings.ToLower(base[dot+1:])
	if _, ok := knownVideoExts[ext]; ok {
		return base[:dot]
	}
	// Fallback: strip last extension even if unknown.
	return base[:dot]
}

// SidecarMatchesPrimary reports whether a sidecar basename pairs with a primary.
// Matches exact stem.ext or stem.<tags>.ext (e.g. Movie.en.srt, Movie.forced.ass).
func SidecarMatchesPrimary(sidecarPath, primaryPath string, sidecarExts map[string]struct{}) bool {
	if !IsSidecarPath(sidecarPath, sidecarExts) {
		return false
	}
	sBase := sidecarPath
	if i := strings.LastIndexByte(sidecarPath, '/'); i >= 0 {
		sBase = sidecarPath[i+1:]
	}
	stem := PrimaryStem(primaryPath)
	if stem == "" {
		return false
	}
	// Compare case-insensitively.
	sLower := strings.ToLower(sBase)
	stemLower := strings.ToLower(stem)
	ext := FileExt(sBase)
	// Exact: stem.srt
	if sLower == stemLower+"."+ext {
		return true
	}
	// Prefixed tags: stem.en.srt, stem.eng.forced.ass
	if strings.HasPrefix(sLower, stemLower+".") && strings.HasSuffix(sLower, "."+ext) {
		return true
	}
	return false
}

// attachMatchingSidecars appends sidecars that match any kept primary by stem.
// Dedupes by path. Preserves primary order, then sidecars in candidate order.
func attachMatchingSidecars(primaries, sidecars []metadata.FileRecord, exts map[string]struct{}) []metadata.FileRecord {
	if len(primaries) == 0 || len(sidecars) == 0 {
		return primaries
	}
	seen := make(map[string]bool, len(primaries)+len(sidecars))
	out := make([]metadata.FileRecord, 0, len(primaries)+len(sidecars))
	for _, p := range primaries {
		out = append(out, p)
		seen[p.Path] = true
	}
	for _, s := range sidecars {
		if seen[s.Path] {
			continue
		}
		for _, p := range primaries {
			if p.Source != s.Source || p.ItemID != s.ItemID {
				continue
			}
			if SidecarMatchesPrimary(s.Path, p.Path, exts) {
				out = append(out, s)
				seen[s.Path] = true
				break
			}
		}
	}
	return out
}

func KeepLargest(records []metadata.FileRecord) []metadata.FileRecord {
	type key struct {
		source metadata.FileSource
		itemID int64
	}
	best := make(map[key]metadata.FileRecord)
	order := make([]key, 0, len(records)/2)
	for _, rec := range records {
		k := key{source: rec.Source, itemID: rec.ItemID}
		existing, has := best[k]
		if !has {
			best[k] = rec
			order = append(order, k)
		} else if rec.Size > existing.Size {
			best[k] = rec
		}
	}
	result := make([]metadata.FileRecord, 0, len(order))
	for _, k := range order {
		result = append(result, best[k])
	}
	return result
}
