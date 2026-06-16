package metadata

import (
	"testing"
	"time"
)

func TestRecordStatsAndQueryAll(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	metrics := map[string]float64{
		"api_calls_success": 42,
		"api_calls_failed":  3,
		"alloc_mb":          128,
	}

	if err := s.RecordStats(metrics); err != nil {
		t.Fatalf("RecordStats failed: %v", err)
	}

	since := time.Now().Add(-1 * time.Hour)
	result, err := s.QueryAllStatsSince(since)
	if err != nil {
		t.Fatalf("QueryAllStatsSince failed: %v", err)
	}

	if len(result) != 3 {
		t.Errorf("expected 3 metrics, got %d", len(result))
	}

	for _, name := range []string{"api_calls_success", "api_calls_failed", "alloc_mb"} {
		records, ok := result[name]
		if !ok {
			t.Errorf("metric %q not found in results", name)
			continue
		}
		if len(records) != 1 {
			t.Errorf("metric %q: expected 1 record, got %d", name, len(records))
			continue
		}
		if records[0].Metric != name {
			t.Errorf("expected metric name %q, got %q", name, records[0].Metric)
		}
	}
}

func TestRecordStatsEmptyMap(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	if err := s.RecordStats(map[string]float64{}); err != nil {
		t.Errorf("RecordStats with empty map should succeed: %v", err)
	}
}

func TestRecordStatsMultipleBatches(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	metrics1 := map[string]float64{"api_calls_success": 10}
	metrics2 := map[string]float64{"api_calls_success": 20}

	if err := s.RecordStats(metrics1); err != nil {
		t.Fatalf("first RecordStats failed: %v", err)
	}
	if err := s.RecordStats(metrics2); err != nil {
		t.Fatalf("second RecordStats failed: %v", err)
	}

	since := time.Now().Add(-1 * time.Hour)
	result, err := s.QueryAllStatsSince(since)
	if err != nil {
		t.Fatalf("QueryAllStatsSince failed: %v", err)
	}

	records := result["api_calls_success"]
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	if records[0].Value != 10 {
		t.Errorf("first record: expected value 10, got %f", records[0].Value)
	}
	if records[1].Value != 20 {
		t.Errorf("second record: expected value 20, got %f", records[1].Value)
	}

	// Both records should be within the last hour.
	for i, r := range records {
		if r.Timestamp.IsZero() {
			t.Errorf("record %d: timestamp is zero", i)
		}
	}
}

func TestQueryAllStatsSince_futureSince(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	if err := s.RecordStats(map[string]float64{"test": 1.0}); err != nil {
		t.Fatalf("RecordStats failed: %v", err)
	}

	future := time.Now().Add(1 * time.Hour)
	result, err := s.QueryAllStatsSince(future)
	if err != nil {
		t.Fatalf("QueryAllStatsSince failed: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("future since: expected empty map, got %d entries", len(result))
	}
}

func TestPruneStats_removesOld(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	if err := s.RecordStats(map[string]float64{"old": 1.0}); err != nil {
		t.Fatalf("RecordStats failed: %v", err)
	}

	time.Sleep(2 * time.Second)

	n, err := s.PruneStats(100 * time.Millisecond)
	if err != nil {
		t.Fatalf("PruneStats failed: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 row pruned, got %d", n)
	}

	all, err := s.QueryAllStatsSince(time.Now().Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("QueryAllStatsSince failed: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected no stats after prune, got %d metrics", len(all))
	}
}

func TestPruneStats_longRetention(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	if err := s.RecordStats(map[string]float64{"keep": 1.0}); err != nil {
		t.Fatalf("RecordStats failed: %v", err)
	}

	retention := 24 * time.Hour
	n, err := s.PruneStats(retention)
	if err != nil {
		t.Fatalf("PruneStats failed: %v", err)
	}
	if n != 0 {
		t.Errorf("long retention: expected 0 rows pruned, got %d", n)
	}
}

func TestQueryStatsMetric(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	if err := s.RecordStats(map[string]float64{"mem": 64.0}); err != nil {
		t.Fatalf("RecordStats failed: %v", err)
	}

	since := time.Now().Add(-1 * time.Hour)
	records, err := s.QueryStats("mem", since)
	if err != nil {
		t.Fatalf("QueryStats failed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Value != 64.0 {
		t.Errorf("expected value 64, got %f", records[0].Value)
	}
}
