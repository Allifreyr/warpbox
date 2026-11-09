package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/mainlink0435/warpbox/internal/metadata"
)

// DerivedView presents independent names while resolving original playback records.
type DerivedView interface {
	Name() string
	List(context.Context, string) ([]metadata.FileRecord, error)
	Resolve(context.Context, string) (*metadata.FileRecord, error)
	Alternatives(context.Context, string) ([]metadata.FileRecord, error)
}

func (s *Server) isDerived(root string) bool {
	return s.cfg.DerivedView != nil && root == s.root+"/"+s.cfg.DerivedView.Name()
}
func (s *Server) listView(ctx context.Context, root, prefix string) ([]metadata.FileRecord, error) {
	if s.isDerived(root) {
		return s.cfg.DerivedView.List(ctx, strings.Trim(prefix, "/"))
	}
	return s.store.ListDir(prefix)
}
func (s *Server) resolveView(ctx context.Context, root, p string) (*metadata.FileRecord, error) {
	if s.isDerived(root) {
		return s.cfg.DerivedView.Resolve(ctx, p)
	}
	return s.store.GetFileByPath(p)
}

func (s *Server) fallbackFile(file *metadata.FileRecord) (string, error) {
	if file.ViewPath == "" {
		return s.tryCDNFallback(file.Path)
	}
	if s.cfg.DerivedView == nil {
		return "", fmt.Errorf("derived view unavailable for path %q", file.ViewPath)
	}
	alts, err := s.cfg.DerivedView.Alternatives(context.Background(), file.ViewPath)
	if err != nil {
		return "", err
	}
	for _, alt := range alts {
		if alt.Source == file.Source && alt.ItemID == file.ItemID && alt.FileID == file.FileID {
			continue
		}
		var u string
		u, err = s.fetchCDNURL(alt.Source, alt.ItemID, alt.FileID)
		if err == nil {
			return u, nil
		}
	}
	if err == nil {
		return "", fmt.Errorf("no alternatives for derived path %q", file.ViewPath)
	}
	return "", err
}
