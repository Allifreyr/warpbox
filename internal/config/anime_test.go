package config

import "testing"

func TestAnimeConfiguration(t *testing.T) {
	base := func() *Config {
		c := &Config{Library: LibraryConfig{VirtualPaths: []VirtualPathConfig{{Name: "anime"}, {Name: "movies"}}}}
		setDefaults(c)
		return c
	}
	c := base()
	if c.Library.Anime.Enabled || c.Library.Anime.Source != "anime" || c.Library.Anime.Name != "anime-plex" || c.Library.Anime.RefreshHours != 24 {
		t.Fatalf("unsafe defaults: %+v", c.Library.Anime)
	}
	for _, tt := range []struct {
		name   string
		change func(*AnimeConfig)
		valid  bool
	}{
		{"valid", func(a *AnimeConfig) {}, true},
		{"missing token", func(a *AnimeConfig) { a.TMDBToken = "" }, false},
		{"missing source", func(a *AnimeConfig) { a.Source = "missing" }, false},
		{"name collision", func(a *AnimeConfig) { a.Name = "movies" }, false},
		{"raw name", func(a *AnimeConfig) { a.Name = "__all__" }, false},
		{"traversal", func(a *AnimeConfig) { a.Name = ".." }, false},
		{"separator", func(a *AnimeConfig) { a.Name = `anime\other` }, false},
		{"refresh", func(a *AnimeConfig) { a.RefreshHours = -1 }, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := base()
			c.Library.Anime.Enabled = true
			c.Library.Anime.TMDBToken = "test"
			tt.change(&c.Library.Anime)
			err := validateLibrary(&c.Library)
			if (err == nil) != tt.valid {
				t.Fatalf("validation=%v", err)
			}
		})
	}
}
