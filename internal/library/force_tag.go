package library

import (
	"strings"
)

// ForceTagForName returns the classification force tag for a virtual path name.
// Example: "movies" → "forcedmovies", "tv" → "forcedtv".
func ForceTagForName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		return ""
	}
	return "forced" + name
}

// ForceTagForMount returns the force tag for a filter mount path (e.g. "/tv").
func ForceTagForMount(mount string) string {
	return ForceTagForName(strings.TrimPrefix(strings.TrimSpace(mount), "/"))
}

// IsForceTag reports whether tag is a force-class tag (prefix "forced" + non-empty suffix).
// The bare word "forced" and "rename" are not force tags.
func IsForceTag(tag string) bool {
	t := strings.ToLower(strings.TrimSpace(tag))
	return strings.HasPrefix(t, "forced") && len(t) > len("forced")
}

// ForceTargets returns all force-class tags present in a space-joined filter_tags string.
func ForceTargets(filterTags string) []string {
	if filterTags == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Fields(filterTags) {
		if IsForceTag(part) {
			out = append(out, strings.ToLower(part))
		}
	}
	return out
}

// MatchesMount reports whether forceTag is the force tag for pathName.
func MatchesMount(forceTag, pathName string) bool {
	want := ForceTagForName(pathName)
	if want == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(forceTag), want)
}

// BuildTagAllowlist builds the set of TorBox tags that may be stored in filter_tags.
// Always includes forced{name} for each virtual path name. Also includes every
// entry from overrideTags (e.g. "rename" and any custom regex-only tags).
func BuildTagAllowlist(overrideTags, pathNames []string) map[string]bool {
	m := make(map[string]bool)
	for _, t := range overrideTags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" {
			m[t] = true
		}
	}
	for _, name := range pathNames {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || name == "__all__" {
			continue
		}
		if ft := ForceTagForName(name); ft != "" {
			m[ft] = true
		}
	}
	return m
}

// ExpandOverrideTags returns an unordered slice of allowlisted tags for
// callers that still pass []string into the sync worker. Map iteration
// order is not guaranteed; callers must not rely on tag order.
func ExpandOverrideTags(overrideTags, pathNames []string) []string {
	m := BuildTagAllowlist(overrideTags, pathNames)
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
