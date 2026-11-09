package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/Allifreyr/warpbox/internal/anime"
	"github.com/Allifreyr/warpbox/internal/config"
	"github.com/Allifreyr/warpbox/internal/metadata"
)

func runAnimeCLI(args []string) bool {
	if len(args) == 0 || args[0] != "anime" {
		return false
	}
	if err := animeCLI(args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return true
}
func animeCLI(args []string) error {
	if len(args) == 0 || args[0] != "inspect" && args[0] != "rebuild" {
		return fmt.Errorf("usage: warpbox anime inspect|rebuild -config config.yml -db warpbox.db")
	}
	command := args[0]
	flags := flag.NewFlagSet("anime "+command, flag.ContinueOnError)
	configPath := flags.String("config", "config.yml", "Configuration file")
	dbPath := flags.String("db", "warpbox.db", "Existing metadata database")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(*dbPath); err != nil {
		return fmt.Errorf("anime requires an existing synced database: %w", err)
	}
	var store *metadata.Store
	if command == "inspect" {
		store, err = metadata.OpenReadOnly(*dbPath)
	} else {
		store, err = metadata.Open(*dbPath)
	}
	if err != nil {
		return err
	}
	defer store.Close()
	if cfg.Library.Anime.OverridesFile != "" && !filepath.IsAbs(cfg.Library.Anime.OverridesFile) {
		cfg.Library.Anime.OverridesFile = filepath.Join(filepath.Dir(*configPath), cfg.Library.Anime.OverridesFile)
	}
	view, err := anime.New(store, cfg.Library)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if command == "rebuild" {
		if cfg.Library.Anime.TMDBToken == "" {
			return fmt.Errorf("library.anime.tmdb_token is required to rebuild")
		}
		if err := view.Rebuild(ctx, true); err != nil {
			return err
		}
	}
	report, err := view.Inspect(ctx)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}
