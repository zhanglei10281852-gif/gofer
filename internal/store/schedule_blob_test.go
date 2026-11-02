package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testScheduleID() string { return "11111111-2222-3333-4444-555555555555" }

func TestScheduleBlobStoresRevisionsAndDeletesPlan(t *testing.T) {
	s := NewBlobStore(t.TempDir())
	plan := testScheduleID()

	v1, err := s.NewScheduleVersion("account", plan)
	if err != nil {
		t.Fatal(err)
	}
	p1, err := v1.StoreScheduleAttachment(t.Context(), "account", plan, 1, "a.txt", strings.NewReader("one"))
	if err != nil {
		t.Fatal(err)
	}
	v2, err := s.NewScheduleVersion("account", plan)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := v2.StoreScheduleAttachment(t.Context(), "account", plan, 1, "b.txt", strings.NewReader("two"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{p1, p2} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) == 0 {
			t.Fatalf("empty attachment at %s", path)
		}
	}

	// Invalid plan identifiers cannot traverse the blob tree.
	if _, err := s.NewScheduleVersion("account", "../escape"); err == nil {
		t.Fatal("traversal schedule id accepted")
	}
	if err := s.DeleteSchedule("account", "../escape"); err == nil {
		t.Fatal("traversal delete accepted")
	}

	if err := s.DeleteSchedule("account", plan); err != nil {
		t.Fatalf("delete schedule: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(p1)); !os.IsNotExist(err) {
		t.Fatalf("schedule directory remains: %v", err)
	}
	// Deleting again is harmless.
	if err := s.DeleteSchedule("account", plan); err != nil {
		t.Fatalf("idempotent delete: %v", err)
	}
}

func TestCleanupRemovesOnlyUnreferencedScheduleRevisions(t *testing.T) {
	base := t.TempDir()
	s := NewBlobStore(base)
	plan := testScheduleID()

	v1, _ := s.NewScheduleVersion("account", plan)
	keep, err := v1.StoreScheduleAttachment(t.Context(), "account", plan, 1, "keep.txt", strings.NewReader("keep"))
	if err != nil {
		t.Fatal(err)
	}
	v2, _ := s.NewScheduleVersion("account", plan)
	superseded, err := v2.StoreScheduleAttachment(t.Context(), "account", plan, 1, "old.txt", strings.NewReader("old"))
	if err != nil {
		t.Fatal(err)
	}
	ageCleanupTree(t, base)

	result, err := s.CleanupMessageFiles(t.Context(), []string{"account"}, map[string]bool{
		keep: true,
	}, nil, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if result.Schedules != 1 {
		t.Fatalf("removed schedule revisions = %d, want 1 (%+v)", result.Schedules, result)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("retained schedule attachment lost: %v", err)
	}
	if _, err := os.Stat(superseded); !os.IsNotExist(err) {
		t.Fatalf("superseded revision remains: %v", err)
	}

	// Once nothing references the plan any more, the last revision and the
	// plan directory go on the next pass.
	result, err = s.CleanupMessageFiles(t.Context(), []string{"account"}, nil, nil, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if result.Schedules != 1 {
		t.Fatalf("removed final revision = %d, want 1 (%+v)", result.Schedules, result)
	}
	if _, err := os.Stat(keep); !os.IsNotExist(err) {
		t.Fatalf("plan directory content remains: %v", err)
	}
	root := filepath.Join(base, "account", "schedules", plan)
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("empty plan directory remains: %v", err)
	}
}

func TestCleanupKeepsRecentUnreferencedScheduleRevisions(t *testing.T) {
	base := t.TempDir()
	s := NewBlobStore(base)
	plan := testScheduleID()
	v, _ := s.NewScheduleVersion("account", plan)
	path, err := v.StoreScheduleAttachment(t.Context(), "account", plan, 1, "fresh.txt", strings.NewReader("fresh"))
	if err != nil {
		t.Fatal(err)
	}
	// No aging: a content replacement mid-commit must survive.
	result, err := s.CleanupMessageFiles(t.Context(), []string{"account"}, nil, nil, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if result.Schedules != 0 {
		t.Fatalf("fresh revision removed: %+v", result)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fresh revision lost: %v", err)
	}
}
