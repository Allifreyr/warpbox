// Package anime builds a persistent, optional Plex view of an existing library.
package anime

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var (
	brackets   = regexp.MustCompile(`\[[^\]]*\]`)
	batch      = regexp.MustCompile(`(?i)\(\d+\s*[-~]\s*\d+\)|\(?\b\d{3,4}p\)?|\bBD$`)
	rangeTail  = regexp.MustCompile(`\s*-\s*\d+\s*~\s*\d+\s*$`)
	seasonTail = regexp.MustCompile(`(?i)\s+S(\d+)\s*$`)
	explicit   = regexp.MustCompile(`(?i)^(.*?)\s*[-. ]*S(\d+)E(\d+)(?:[- ]?E(\d+))?(?:v\d+)?(?:\s.*)?$`)
	numbered   = regexp.MustCompile(`(?i)^(.*?)\s+-\s+(\d+)(?:v\d+)?\s*$`)
)

type Release struct {
	Title    string
	Season   int
	Episode  int
	End      int
	Explicit bool
	Revision string
}

func clean(s string) string { return strings.TrimSpace(brackets.ReplaceAllString(s, "")) }
func normalize(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, s)
}
func titleSeason(s string) (string, int) {
	s = strings.TrimSpace(s)
	m := seasonTail.FindStringSubmatch(s)
	if m == nil {
		return s, 0
	}
	n, _ := strconv.Atoi(m[1])
	return strings.TrimSpace(s[:len(s)-len(m[0])]), n
}

func Parse(p string) (Release, error) {
	base := clean(strings.TrimSuffix(path.Base(p), path.Ext(p)))
	// Remove technical parenthesized suffixes, not meaningful title punctuation.
	base = strings.TrimSpace(batch.ReplaceAllString(base, ""))
	var r Release
	if m := explicit.FindStringSubmatch(base); m != nil {
		r.Title = strings.Trim(m[1], " -. ")
		r.Season, _ = strconv.Atoi(m[2])
		r.Episode, _ = strconv.Atoi(m[3])
		r.Explicit = true
		if m[4] != "" {
			r.End, _ = strconv.Atoi(m[4])
		}
	} else if m := numbered.FindStringSubmatch(base); m != nil {
		r.Title, r.Season = titleSeason(m[1])
		r.Episode, _ = strconv.Atoi(m[2])
	} else {
		return r, fmt.Errorf("unrecognized episode filename")
	}
	if r.Title == "" {
		folder := strings.Split(p, "/")[0]
		folder = rangeTail.ReplaceAllString(batch.ReplaceAllString(clean(folder), ""), "")
		r.Title, _ = titleSeason(folder)
	}
	if r.Title == "" || normalize(r.Title) == "" {
		return r, fmt.Errorf("missing series title")
	}
	if !r.Explicit && r.Season == 0 {
		folder := strings.Split(p, "/")[0]
		folder = rangeTail.ReplaceAllString(batch.ReplaceAllString(clean(folder), ""), "")
		_, r.Season = titleSeason(folder)
	}
	if strings.Contains(p, "/") {
		folder := rangeTail.ReplaceAllString(batch.ReplaceAllString(clean(strings.Split(p, "/")[0]), ""), "")
		_, folderSeason := titleSeason(folder)
		if folderSeason > 0 && r.Season != folderSeason {
			return r, fmt.Errorf("conflicting folder and filename seasons")
		}
	}
	if r.End != 0 && r.End < r.Episode {
		return r, fmt.Errorf("reversed episode range")
	}
	if r.End-r.Episode > 1000 {
		return r, fmt.Errorf("episode range too large")
	}
	return r, nil
}

func safe(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|&`, r) || unicode.IsControl(r) {
			return '_'
		}
		return r
	}, s)
	s = strings.Trim(s, " .")
	if s == "" {
		return "Unknown"
	}
	return s
}
