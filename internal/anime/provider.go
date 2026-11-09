package anime

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Allifreyr/warpbox/internal/metadata"
)

type Show struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	OriginalName string `json:"original_name"`
	Date         string `json:"first_air_date"`
	Seasons      []struct {
		Number int `json:"season_number"`
	} `json:"seasons"`
	Alternative struct {
		Results []struct {
			Title string `json:"title"`
		} `json:"results"`
	} `json:"alternative_titles"`
}
type Provider interface {
	Search(context.Context, string) ([]Show, error)
	Show(context.Context, int) (Show, error)
	Episodes(context.Context, int, int) ([]int, error)
}

// Client is used only by the serialized background worker, never request handlers.
type Client struct {
	Store       *metadata.Store
	Token       string
	TTL         time.Duration
	HTTP        *http.Client
	BaseURL     string
	next        time.Time
	failedUntil map[string]time.Time
}

func (c *Client) fetch(ctx context.Context, key, endpoint string) ([]byte, error) {
	body, fetched, cacheErr := c.Store.AnimeCache(ctx, key)
	if cacheErr == nil && time.Since(fetched) < c.TTL {
		return body, nil
	}
	if time.Now().Before(c.failedUntil[key]) {
		if cacheErr == nil {
			return body, nil
		}
		return nil, fmt.Errorf("metadata temporarily unavailable: %s", key)
	}
	if delay := time.Until(c.next); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	c.next = time.Now().Add(250 * time.Millisecond)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(key, "tmdb:") {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			b, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<20+1))
			err = readErr
			if err == nil && len(b) > 64<<20 {
				err = fmt.Errorf("metadata response too large")
			}
			if err == nil { // Validate before replacing a working cached response.
				if strings.HasPrefix(key, "tmdb:") {
					if !json.Valid(b) {
						err = fmt.Errorf("invalid metadata JSON")
					}
				} else {
					var doc struct{ XMLName xml.Name }
					err = xml.Unmarshal(b, &doc)
				}
			}
			if err == nil {
				if err = c.Store.PutAnimeCache(ctx, key, b); err == nil {
					return b, nil
				}
			}
		} else {
			err = fmt.Errorf("metadata HTTP %d", resp.StatusCode)
			if resp.StatusCode == 429 {
				seconds, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
				if seconds < 1 {
					seconds = 60
				}
				if seconds > 3600 {
					seconds = 3600
				}
				c.next = time.Now().Add(time.Duration(seconds) * time.Second)
			}
		}
	}
	if c.failedUntil == nil {
		c.failedUntil = map[string]time.Time{}
	}
	c.failedUntil[key] = time.Now().Add(5 * time.Minute)
	if cacheErr == nil {
		return body, nil
	}
	// Do not expose request headers, credentials, or URL errors in reports.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, fmt.Errorf("metadata unavailable: %s", key)
}
func (c *Client) tmdb(ctx context.Context, p string, out any) error {
	base := c.BaseURL
	if base == "" {
		base = "https://api.themoviedb.org/3"
	}
	body, err := c.fetch(ctx, "tmdb:"+p, base+p)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}
func (c *Client) Search(ctx context.Context, title string) ([]Show, error) {
	var result struct {
		Results []Show `json:"results"`
	}
	err := c.tmdb(ctx, "/search/tv?query="+url.QueryEscape(title), &result)
	return result.Results, err
}
func (c *Client) Show(ctx context.Context, id int) (Show, error) {
	var s Show
	err := c.tmdb(ctx, fmt.Sprintf("/tv/%d?append_to_response=alternative_titles", id), &s)
	if err == nil && (s.ID != id || s.Name == "") {
		err = fmt.Errorf("invalid TMDB series response")
	}
	return s, err
}
func (c *Client) Episodes(ctx context.Context, id, season int) ([]int, error) {
	var result struct {
		Episodes []struct {
			Number int `json:"episode_number"`
		} `json:"episodes"`
	}
	err := c.tmdb(ctx, fmt.Sprintf("/tv/%d/season/%d", id, season), &result)
	var out []int
	for _, e := range result.Episodes {
		out = append(out, e.Number)
	}
	return out, err
}

type Entry struct {
	ID       int    `xml:"anidbid,attr"`
	TMDB     string `xml:"tmdbtv,attr"`
	Season   string `xml:"tmdbseason,attr"`
	Offset   int    `xml:"tmdboffset,attr"`
	Name     string `xml:"name"`
	Mappings []struct {
		SourceSeason string `xml:"anidbseason,attr"`
		Season       string `xml:"tmdbseason,attr"`
		Start        int    `xml:"start,attr"`
		End          int    `xml:"end,attr"`
		Offset       int    `xml:"offset,attr"`
		Pairs        string `xml:",chardata"`
	} `xml:"mapping-list>mapping"`
}
type Catalog struct{ Titles map[string][]Entry }

func (c *Client) Catalog(ctx context.Context) (Catalog, error) {
	const root = "https://raw.githubusercontent.com/Anime-Lists/anime-lists/master/"
	b, err := c.fetch(ctx, "anime:maps", root+"anime-list-full.xml")
	if err != nil {
		return Catalog{}, err
	}
	t, err := c.fetch(ctx, "anime:titles", root+"animetitles.xml")
	if err != nil {
		return Catalog{}, err
	}
	return parseCatalog(b, t)
}
func parseCatalog(b, t []byte) (Catalog, error) {
	var entries struct {
		Entries []Entry `xml:"anime"`
	}
	if err := xml.Unmarshal(b, &entries); err != nil {
		return Catalog{}, err
	}
	var titles struct {
		Anime []struct {
			ID     int      `xml:"aid,attr"`
			Titles []string `xml:"title"`
		} `xml:"anime"`
	}
	if err := xml.Unmarshal(t, &titles); err != nil {
		return Catalog{}, err
	}
	byID := map[int]Entry{}
	out := Catalog{Titles: map[string][]Entry{}}
	add := func(title string, e Entry) {
		key := normalize(title)
		if key == "" {
			return
		}
		for _, v := range out.Titles[key] {
			if v.ID == e.ID {
				return
			}
		}
		out.Titles[key] = append(out.Titles[key], e)
	}
	for _, e := range entries.Entries {
		if _, err := strconv.Atoi(e.TMDB); err == nil {
			byID[e.ID] = e
			add(e.Name, e)
		}
	}
	for _, a := range titles.Anime {
		if e, ok := byID[a.ID]; ok {
			for _, title := range a.Titles {
				add(title, e)
			}
		}
	}
	return out, nil
}

// target applies only TMDB rules; absent/unsupported mappings never borrow TVDB rules.
func (e Entry) target(ep int) (int, []int, bool) {
	for _, m := range e.Mappings {
		if m.SourceSeason != "1" || m.Season == "" {
			continue
		}
		s, err := strconv.Atoi(m.Season)
		if err != nil {
			continue
		}
		for _, pair := range strings.Split(m.Pairs, ";") {
			parts := strings.Split(strings.TrimSpace(pair), "-")
			if len(parts) != 2 {
				continue
			}
			n, err := strconv.Atoi(parts[0])
			if err != nil || n != ep {
				continue
			}
			var episodes []int
			for _, v := range strings.Split(parts[1], "+") {
				n, err := strconv.Atoi(v)
				if err != nil || n < 1 {
					return 0, nil, false
				}
				episodes = append(episodes, n)
			}
			return s, episodes, true
		}
	}
	for _, m := range e.Mappings {
		if m.SourceSeason == "1" && m.Season != "" && m.Start > 0 && ep >= m.Start && ep <= m.End {
			s, err := strconv.Atoi(m.Season)
			if err == nil && ep+m.Offset > 0 {
				return s, []int{ep + m.Offset}, true
			}
		}
	}
	s, err := strconv.Atoi(e.Season)
	if err != nil || ep+e.Offset < 1 {
		return 0, nil, false
	}
	return s, []int{ep + e.Offset}, true
}
