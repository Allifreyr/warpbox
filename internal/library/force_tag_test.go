package library

import (
	"testing"
)

func TestForceTagForName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"movies", "forcedmovies"},
		{"tv", "forcedtv"},
		{"anime", "forcedanime"},
		{"animemovies", "forcedanimemovies"},
		{"anime-movies", "forcedanime-movies"},
		{"/movies", "forcedmovies"},
		{" Movies ", "forcedmovies"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := ForceTagForName(tc.in); got != tc.want {
			t.Errorf("ForceTagForName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestForceTagForMount(t *testing.T) {
	if got := ForceTagForMount("/tv"); got != "forcedtv" {
		t.Errorf("ForceTagForMount(/tv) = %q", got)
	}
}

func TestIsForceTag(t *testing.T) {
	if !IsForceTag("forcedtv") || !IsForceTag("forcedmovies") {
		t.Error("expected force tags")
	}
	if IsForceTag("forced") || IsForceTag("rename") || IsForceTag("tv") || IsForceTag("") {
		t.Error("non-force tags should be false")
	}
}

func TestForceTargets(t *testing.T) {
	got := ForceTargets("forcedtv rename forcedanime")
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	if got[0] != "forcedtv" || got[1] != "forcedanime" {
		t.Errorf("got %v", got)
	}
	if ForceTargets("") != nil && len(ForceTargets("")) != 0 {
		t.Error("empty should yield no targets")
	}
}

func TestMatchesMount(t *testing.T) {
	if !MatchesMount("forcedmovies", "movies") {
		t.Error("forcedmovies should match movies")
	}
	if MatchesMount("forcedmovie", "movies") {
		t.Error("forcedmovie is not an alias; should not match movies")
	}
	if !MatchesMount("ForcedTV", "tv") {
		t.Error("case-insensitive match")
	}
}

func TestBuildTagAllowlist(t *testing.T) {
	m := BuildTagAllowlist([]string{"rename"}, []string{"movies", "tv", "anime", "__all__"})
	for _, want := range []string{"rename", "forcedmovies", "forcedtv", "forcedanime"} {
		if !m[want] {
			t.Errorf("missing %q in allowlist", want)
		}
	}
	if m["forced__all__"] || m["forced"] {
		t.Error("should not allowlist __all__ or bare forced")
	}
	// Extra user tag preserved
	m2 := BuildTagAllowlist([]string{"rename", "customtag"}, []string{"tv"})
	if !m2["customtag"] || !m2["forcedtv"] {
		t.Error("expected customtag and forcedtv")
	}
}
