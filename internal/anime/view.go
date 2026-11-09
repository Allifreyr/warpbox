package anime

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Allifreyr/warpbox/internal/config"
	"github.com/Allifreyr/warpbox/internal/library"
	"github.com/Allifreyr/warpbox/internal/metadata"
	"gopkg.in/yaml.v3"
)

type Target struct {
	Season   int   `yaml:"season"`
	Episodes []int `yaml:"episodes"`
}
type EpisodeRange struct {
	Start        int `yaml:"start"`
	End          int `yaml:"end"`
	Season       int `yaml:"season"`
	EpisodeStart int `yaml:"episode_start"`
}
type Override struct {
	Release   string            `yaml:"release"`
	TMDBID    int               `yaml:"tmdb_id"`
	Season    *int              `yaml:"season"`
	Offset    int               `yaml:"offset"`
	Numbering string            `yaml:"numbering"`
	Exclude   bool              `yaml:"exclude"`
	Ranges    []EpisodeRange    `yaml:"ranges"`
	Episodes  map[int]Target    `yaml:"episodes"`
	Files     map[string]Target `yaml:"files"`
}
type key struct {
	Source     metadata.FileSource
	Item, File int64
}

func identity(f metadata.FileRecord) key     { return key{f.Source, f.ItemID, f.FileID} }
func mappingKey(m metadata.AnimeMapping) key { return key{m.Source, m.ItemID, m.FileID} }

type View struct {
	Store    *metadata.Store
	Config   config.AnimeConfig
	Filter   *library.Filter
	Provider Provider
	Catalog  Catalog
	Client   *Client
	mu       sync.Mutex
	wake     chan struct{}
}

func New(store *metadata.Store, cfg config.LibraryConfig) (*View, error) {
	a := cfg.Anime
	var filter *library.Filter
	for _, vp := range cfg.VirtualPaths {
		if vp.Name != a.Source {
			continue
		}
		var err error
		filter, err = library.NewFilter("/"+vp.Name, vp.DirectoryInclude, vp.DirectoryExclude, vp.FileRegex, vp.LargestFileOnly)
		if err != nil {
			return nil, err
		}
		min, err := library.ParseFileSize(vp.MinFileSize)
		if err != nil {
			return nil, err
		}
		max, err := library.ParseFileSize(vp.MaxFileSize)
		if err != nil {
			return nil, err
		}
		filter.WithSizeBounds(min, max).WithSidecarExtensions(vp.SidecarExtensions)
		if _, err = filter.WithPathSegmentExclude(vp.PathSegmentExclude); err != nil {
			return nil, err
		}
	}
	if filter == nil {
		return nil, fmt.Errorf("anime source virtual path %q not found", a.Source)
	}
	client := &Client{Store: store, Token: a.TMDBToken, TTL: time.Duration(a.RefreshHours) * time.Hour}
	return &View{Store: store, Config: a, Filter: filter, Provider: client, Client: client, wake: make(chan struct{}, 1)}, nil
}
func (v *View) Name() string { return v.Config.Name }
func (v *View) Notify() {
	select {
	case v.wake <- struct{}{}:
	default:
	}
}
func (v *View) Run(ctx context.Context) {
	tick := time.NewTicker(time.Duration(v.Config.RefreshHours) * time.Hour)
	defer tick.Stop()
	v.Notify()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-v.wake:
		}
		if err := v.Rebuild(ctx, false); err != nil && ctx.Err() == nil {
			slog.Warn("anime mapping pass failed", "error", err)
		}
	}
}

// List and Resolve read only pinned local mappings. Playback retains the raw path.
func (v *View) List(ctx context.Context, prefix string) ([]metadata.FileRecord, error) {
	files, err := v.Store.AnimeFiles(ctx, prefix, false)
	if err != nil {
		return nil, err
	}
	eligible, err := v.eligible(ctx, prefix, false)
	if err != nil {
		return nil, err
	}
	var out []metadata.FileRecord
	seen := map[string]bool{}
	for _, f := range files {
		if !eligible[identity(f)] || seen[f.ViewPath] {
			continue
		}
		seen[f.ViewPath] = true
		f.Path = f.ViewPath
		out = append(out, f)
	}
	return out, nil
}
func (v *View) eligible(ctx context.Context, p string, exact bool) (map[key]bool, error) {
	// Full item siblings preserve largest-file and sidecar rules, without
	// loading other items or unrelated libraries for each Plex lookup.
	files, err := v.Store.AnimeItemFiles(ctx, p, exact)
	if err != nil {
		return nil, err
	}
	out := map[key]bool{}
	for _, f := range v.Filter.Apply(files) {
		out[identity(f)] = true
	}
	return out, nil
}
func (v *View) Resolve(ctx context.Context, p string) (*metadata.FileRecord, error) {
	files, err := v.Alternatives(ctx, p)
	if err != nil || len(files) == 0 {
		return nil, err
	}
	return &files[0], nil
}
func (v *View) Alternatives(ctx context.Context, p string) ([]metadata.FileRecord, error) {
	// A mount root is a directory even when unresolved mappings exist.
	if p == "" {
		return nil, nil
	}
	files, err := v.Store.AnimeFiles(ctx, p, true)
	if err != nil {
		return nil, err
	}
	eligible, err := v.eligible(ctx, p, true)
	if err != nil {
		return nil, err
	}
	var out []metadata.FileRecord
	for _, f := range files {
		if eligible[identity(f)] {
			out = append(out, f)
		}
	}
	return out, nil
}

func (v *View) overrides() (map[string]Override, error) {
	out := map[string]Override{}
	if v.Config.OverridesFile == "" {
		return out, nil
	}
	f, err := os.Open(v.Config.OverridesFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var doc struct {
		Releases []Override `yaml:"releases"`
	}
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	for _, o := range doc.Releases {
		if o.Release == "" {
			return nil, fmt.Errorf("override requires release")
		}
		if _, ok := out[o.Release]; ok {
			return nil, fmt.Errorf("duplicate release override %q", o.Release)
		}
		if o.TMDBID < 0 || o.Season != nil && *o.Season < 0 || o.Numbering != "" && o.Numbering != "season" && o.Numbering != "absolute" {
			return nil, fmt.Errorf("invalid override for %q", o.Release)
		}
		for i, r := range o.Ranges {
			if r.Start < 0 || r.End < r.Start || r.Season < 0 || r.EpisodeStart < 1 {
				return nil, fmt.Errorf("invalid episode range for %q", o.Release)
			}
			for _, other := range o.Ranges[:i] {
				if r.Start <= other.End && other.Start <= r.End {
					return nil, fmt.Errorf("overlapping episode ranges for %q", o.Release)
				}
			}
		}
		for _, t := range o.Episodes {
			if !validTarget(t) {
				return nil, fmt.Errorf("invalid episode exception for %q", o.Release)
			}
		}
		for _, t := range o.Files {
			if !validTarget(t) {
				return nil, fmt.Errorf("invalid file exception for %q", o.Release)
			}
		}
		out[o.Release] = o
	}
	return out, nil
}
func validTarget(t Target) bool {
	if t.Season < 0 || len(t.Episodes) == 0 {
		return false
	}
	for _, n := range t.Episodes {
		if n < 1 {
			return false
		}
	}
	return true
}

// Inspect is offline and does not fetch metadata or write mappings.
func (v *View) Inspect(ctx context.Context) ([]metadata.AnimeMapping, error) {
	files, err := v.Store.AllFiles(ctx)
	if err != nil {
		return nil, err
	}
	maps, err := v.Store.AnimeMappings(ctx)
	if err != nil {
		return nil, err
	}
	byKey := map[key]metadata.AnimeMapping{}
	for _, m := range maps {
		byKey[mappingKey(m)] = m
	}
	var out []metadata.AnimeMapping
	for _, f := range v.Filter.Apply(files) {
		m, ok := byKey[identity(f)]
		if !ok {
			m = metadata.AnimeMapping{Source: f.Source, ItemID: f.ItemID, FileID: f.FileID, OriginalPath: f.Path, Reason: "pending metadata resolution"}
			if _, err := Parse(f.Path); err != nil {
				m.Reason = err.Error()
			}
		}
		// The published alias is pinned; the operator-facing source follows sync.
		m.OriginalPath = f.Path
		out = append(out, m)
	}
	return out, nil
}

// Rebuild force=true is an explicit operator correction. Normal refreshes pin names.
func (v *View) Rebuild(ctx context.Context, force bool) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	overrides, err := v.overrides()
	if err != nil {
		return err
	}
	files, err := v.Store.AllFiles(ctx)
	if err != nil {
		return err
	}
	files = v.Filter.Apply(files)
	maps, err := v.Store.AnimeMappings(ctx)
	if err != nil {
		return err
	}
	pinned := map[key]metadata.AnimeMapping{}
	canonical := map[int]string{}
	for _, m := range maps {
		pinned[mappingKey(m)] = m
		if m.Path != "" && !force {
			if _, ok := canonical[m.TMDBID]; !ok {
				canonical[m.TMDBID] = m.Path
			}
		}
	}
	if v.Client != nil {
		cat, err := v.Client.Catalog(ctx)
		if err != nil {
			slog.Warn("anime title catalog unavailable; using TMDB and overrides", "error", err)
		} else {
			v.Catalog = cat
		}
	}
	var primaries, sidecars []metadata.FileRecord
	for _, f := range files {
		if library.IsSidecarPath(f.Path, v.Filter.SidecarExts) {
			sidecars = append(sidecars, f)
		} else {
			primaries = append(primaries, f)
		}
	}
	resolved := map[key]metadata.AnimeMapping{}
	mapped, unresolved := 0, 0
	for _, f := range primaries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		m, exists := pinned[identity(f)]
		if !exists || m.Path == "" || force {
			previous := m
			m = v.resolve(ctx, f, overrides)
			if m.Path == "" && previous.Path != "" && !overrides[library.ExtractDirectory(f.Path)].Exclude {
				// Failed corrections (including provider outages) cannot erase a
				// working mapping. Keep the reason visible for operator review.
				previous.Reason = "correction not applied: " + m.Reason
				m = previous
			}
			if m.Path != "" {
				if previous := canonical[m.TMDBID]; previous != "" {
					m.Path = canonicalPath(m.Path, previous)
				} else {
					canonical[m.TMDBID] = m.Path
				}
			}
			if err := v.Store.PutAnimeMapping(ctx, m); err != nil {
				return err
			}
		}
		resolved[identity(f)] = m
		if m.Path == "" {
			unresolved++
		} else {
			mapped++
		}
	}
	for _, f := range sidecars {
		if m, ok := pinned[identity(f)]; ok && m.Path != "" && !force {
			continue
		}
		m := metadata.AnimeMapping{Source: f.Source, ItemID: f.ItemID, FileID: f.FileID, OriginalPath: f.Path, Reason: "no mapped primary for sidecar"}
		var best *metadata.FileRecord
		for i, p := range primaries {
			if p.Source == f.Source && p.ItemID == f.ItemID && library.SidecarMatchesPrimary(f.Path, p.Path, v.Filter.SidecarExts) && (best == nil || len(library.PrimaryStem(p.Path)) > len(library.PrimaryStem(best.Path))) {
				best = &primaries[i]
			}
		}
		if best != nil {
			p := resolved[identity(*best)]
			if p.Path != "" {
				suffix := path.Base(f.Path)[len(library.PrimaryStem(best.Path)):]
				m.Path = strings.TrimSuffix(p.Path, path.Ext(p.Path)) + suffix
				m.TMDBID = p.TMDBID
				m.Season = p.Season
				m.Episodes = p.Episodes
				m.Evidence = "paired sidecar"
				m.Reason = ""
			}
		}
		if err := v.Store.PutAnimeMapping(ctx, m); err != nil {
			return err
		}
	}
	slog.Info("anime mapping pass complete", "mapped_videos", mapped, "unresolved_videos", unresolved)
	return nil
}

func (v *View) find(ctx context.Context, r Release) (Show, *Entry, error) {
	keys := []string{r.Title}
	if r.Season > 1 {
		keys = []string{fmt.Sprintf("%s S%d", r.Title, r.Season), fmt.Sprintf("%s %d", r.Title, r.Season)}
		suffix := map[int]string{2: "2nd", 3: "3rd"}[r.Season]
		if suffix == "" {
			suffix = fmt.Sprintf("%dth", r.Season)
		}
		keys = append(keys, r.Title+" "+suffix+" Season")
	}
	entries := map[int]Entry{}
	for _, k := range keys {
		for _, e := range v.Catalog.Titles[normalize(k)] {
			entries[e.ID] = e
		}
	}
	if len(entries) > 1 {
		return Show{}, nil, fmt.Errorf("ambiguous anime title mapping")
	}
	for _, e := range entries {
		id, _ := strconv.Atoi(e.TMDB)
		s, err := v.Provider.Show(ctx, id)
		return s, &e, err
	}
	results, err := v.Provider.Search(ctx, r.Title)
	if err != nil {
		return Show{}, nil, err
	}
	var matches []Show
	for _, candidate := range results {
		s, err := v.Provider.Show(ctx, candidate.ID)
		if err != nil {
			return Show{}, nil, err
		}
		exact := normalize(s.Name) == normalize(r.Title) || normalize(s.OriginalName) == normalize(r.Title)
		for _, a := range s.Alternative.Results {
			exact = exact || normalize(a.Title) == normalize(r.Title)
		}
		if exact {
			matches = append(matches, s)
		}
	}
	if len(matches) != 1 {
		var suggestions []string
		for _, s := range results {
			if len(suggestions) == 5 {
				break
			}
			suggestions = append(suggestions, fmt.Sprintf("%s {tmdb-%d}", s.Name, s.ID))
		}
		return Show{}, nil, fmt.Errorf("expected one exact title/alias match; found %d (override required); search suggestions: %s", len(matches), strings.Join(suggestions, ", "))
	}
	return matches[0], nil, nil
}

func (v *View) resolve(ctx context.Context, f metadata.FileRecord, overrides map[string]Override) metadata.AnimeMapping {
	m := metadata.AnimeMapping{Source: f.Source, ItemID: f.ItemID, FileID: f.FileID, OriginalPath: f.Path}
	fail := func(err error) metadata.AnimeMapping { m.Reason = err.Error(); return m }
	o, override := overrides[library.ExtractDirectory(f.Path)]
	if override && o.Exclude {
		return fail(fmt.Errorf("excluded by override"))
	}
	r, parseErr := Parse(f.Path)
	fileTarget, hasFile := o.Files[path.Base(f.Path)]
	if parseErr != nil && !hasFile {
		return fail(parseErr)
	}
	var show Show
	var entry *Entry
	var err error
	if o.TMDBID > 0 {
		show, err = v.Provider.Show(ctx, o.TMDBID)
		m.Evidence = "release override"
	} else {
		show, entry, err = v.find(ctx, r)
		m.Evidence = "exact title/alias and verified TMDB episodes"
	}
	if err != nil {
		return fail(err)
	}
	m.TMDBID = show.ID
	target := Target{}
	if hasFile {
		target = fileTarget
		m.Evidence = "file override"
	} else if t, ok := o.Episodes[r.Episode]; ok {
		target = t
		m.Evidence = "episode override"
	} else {
		for _, rr := range o.Ranges {
			if r.Episode >= rr.Start && r.Episode <= rr.End {
				target = Target{rr.Season, []int{rr.EpisodeStart + r.Episode - rr.Start}}
				m.Evidence = "range override"
				break
			}
		}
		if len(target.Episodes) == 0 {
			if r.Episode == 0 {
				return fail(fmt.Errorf("episode 00 requires a verified special override"))
			}
			if o.Numbering == "absolute" {
				var seasons []int
				for _, s := range show.Seasons {
					if s.Number > 0 {
						seasons = append(seasons, s.Number)
					}
				}
				sort.Ints(seasons)
				n := r.Episode + o.Offset
				for _, s := range seasons {
					episodes, err := v.Provider.Episodes(ctx, show.ID, s)
					if err != nil {
						return fail(err)
					}
					sort.Ints(episodes)
					if n > 0 && n <= len(episodes) {
						target = Target{s, []int{episodes[n-1]}}
						break
					}
					n -= len(episodes)
				}
				m.Evidence = "absolute numbering override with TMDB order"
			} else if o.Season != nil {
				target = Target{*o.Season, []int{r.Episode + o.Offset}}
				m.Evidence = "season override"
			} else if entry != nil && !r.Explicit {
				season, episodes, ok := entry.target(r.Episode)
				if ok {
					target = Target{season, episodes}
					m.Evidence = fmt.Sprintf("Anime-Lists AniDB %d TMDB mapping", entry.ID)
				}
			} else if r.Explicit || r.Season > 0 {
				target = Target{r.Season, []int{r.Episode}}
			} else {
				return fail(fmt.Errorf("numbering not established; requires anime mapping or season/absolute override"))
			}
		}
	}
	if r.End > r.Episode && !hasFile { // Explicit multi-episode files only; do not invent noncontiguous mappings.
		if !r.Explicit || hasFile || entry != nil || override {
			return fail(fmt.Errorf("multi-episode mapping requires an explicit file override"))
		}
		for n := r.Episode + 1; n <= r.End; n++ {
			target.Episodes = append(target.Episodes, n)
		}
	}
	if !validTarget(target) {
		return fail(fmt.Errorf("no supported TMDB episode mapping"))
	}
	episodes, err := v.Provider.Episodes(ctx, show.ID, target.Season)
	if err != nil {
		return fail(err)
	}
	known := map[int]bool{}
	for _, n := range episodes {
		known[n] = true
	}
	sort.Ints(target.Episodes)
	for i, n := range target.Episodes {
		if !known[n] {
			return fail(fmt.Errorf("TMDB season %d does not contain episode %d", target.Season, n))
		}
		if i > 0 && n != target.Episodes[i-1]+1 {
			return fail(fmt.Errorf("noncontiguous multi-episode mapping requires review"))
		}
	}
	m.Season = target.Season
	m.Episodes = target.Episodes
	title := safe(show.Name)
	year := ""
	if len(show.Date) >= 4 {
		year = " (" + show.Date[:4] + ")"
	}
	label := fmt.Sprintf("S%02dE%02d", m.Season, m.Episodes[0])
	if len(m.Episodes) > 1 {
		label += fmt.Sprintf("-E%02d", m.Episodes[len(m.Episodes)-1])
	}
	// A source filename identifies identical copies in overlapping batches. Keep
	// different encodes/revisions addressable without renaming the first variant.
	variant := variantID(fmt.Sprintf("%s:%d", path.Base(f.Path), f.Size))
	m.Path = fmt.Sprintf("%s%s {tmdb-%d}/Season %02d/%s - %s [%s]%s", title, year, show.ID, m.Season, title, label, variant, path.Ext(f.Path))
	return m
}

func variantID(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("release-%x", h[:8])
}

func canonicalPath(current, previous string) string {
	parts := strings.Split(current, "/")
	old := strings.Split(previous, "/")
	if len(parts) != 3 || len(old) != 3 {
		return current
	}
	i := strings.LastIndex(parts[2], " - S")
	j := strings.LastIndex(old[2], " - S")
	if i < 0 || j < 0 {
		return current
	}
	parts[0] = old[0]
	parts[2] = old[2][:j] + parts[2][i:]
	return strings.Join(parts, "/")
}
