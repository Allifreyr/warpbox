package metadata

import (
	"testing"
	"time"
)

func TestInvalidateCDNURLIfCurrentPreservesNewerURL(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	if err := s.UpsertFile(FileRecord{ItemID: 1, FileID: 0, Path: "Show/file.mkv", Name: "file.mkv", Size: 4}); err != nil {
		t.Fatal(err)
	}
	f, err := s.GetFileByPath("Show/file.mkv")
	if err != nil || f == nil {
		t.Fatal(f, err)
	}
	if err := s.SetCDNURL(f.ID, "new", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.InvalidateCDNURLIfCurrent(f.ID, "old"); err != nil || changed {
		t.Fatal(changed, err)
	}
	if got, err := s.GetCDNURL(f.ID); err != nil || got != "new" {
		t.Fatal(got, err)
	}
	if changed, err := s.InvalidateCDNURLIfCurrent(f.ID, "new"); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if got, err := s.GetCDNURL(f.ID); err != nil || got != "" {
		t.Fatal(got, err)
	}
}
