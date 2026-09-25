package store

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestPulls(t *testing.T) {
	st := testStore(t)
	fixed := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return fixed }

	p, err := st.SubscribePull(Pull{URL: "https://github.com/o/r/pull/1", Domain: "github.com", Repo: "o/r", Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Since.Equal(fixed) {
		t.Fatalf("since defaults to now: got %v", p.Since)
	}
	if _, err := st.SubscribePull(Pull{URL: "https://github.com/o/r/pull/2", Domain: "github.com", Repo: "o/r", Number: 2}); err != nil {
		t.Fatal(err)
	}

	pulls, err := st.ListPulls()
	if err != nil {
		t.Fatal(err)
	}
	if len(pulls) != 2 || pulls[0].Number != 1 || pulls[1].Number != 2 {
		t.Fatalf("got %+v", pulls)
	}

	later := fixed.Add(time.Hour)
	if err := st.SetPullSince(p.URL, later); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetPull(p.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Since.Equal(later) {
		t.Fatalf("since: got %v, want %v", got.Since, later)
	}

	// a second subscribe resets since to now
	st.now = func() time.Time { return later.Add(time.Hour) }
	got, err = st.SubscribePull(Pull{URL: p.URL, Domain: "github.com", Repo: "o/r", Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Since.Equal(later.Add(time.Hour)) {
		t.Fatalf("resubscribe since: got %v", got.Since)
	}

	if err := st.UnsubscribePull(p.URL); err != nil {
		t.Fatal(err)
	}
	if err := st.UnsubscribePull(p.URL); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second unsubscribe: got %v", err)
	}
	if err := st.SetPullSince(p.URL, later); !errors.Is(err, ErrNotFound) {
		t.Fatalf("since on missing: got %v", err)
	}
}

func TestReposAndSeen(t *testing.T) {
	st := testStore(t)
	r, err := st.SubscribeRepo(Repo{Domain: "github.com", Repo: "o/r"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Key() != "github.com/o/r" {
		t.Fatalf("key: %q", r.Key())
	}
	if err := st.MarkSeen("github.com", "o/r", 1, 2, 3); err != nil {
		t.Fatal(err)
	}
	// subscribe again keeps the seen set
	if _, err := st.SubscribeRepo(Repo{Domain: "github.com", Repo: "o/r"}); err != nil {
		t.Fatal(err)
	}
	seen, err := st.SeenReviews("github.com", "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seen, map[int]bool{1: true, 2: true, 3: true}) {
		t.Fatalf("seen: %v", seen)
	}
	if err := st.PruneSeen("github.com", "o/r", []int{2}); err != nil {
		t.Fatal(err)
	}
	seen, _ = st.SeenReviews("github.com", "o/r")
	if !reflect.DeepEqual(seen, map[int]bool{2: true}) {
		t.Fatalf("after prune: %v", seen)
	}
	if err := st.PruneSeen("github.com", "o/r", nil); err != nil {
		t.Fatal(err)
	}
	seen, _ = st.SeenReviews("github.com", "o/r")
	if len(seen) != 0 {
		t.Fatalf("after prune all: %v", seen)
	}

	if err := st.MarkSeen("github.com", "o/r", 9); err != nil {
		t.Fatal(err)
	}
	if err := st.UnsubscribeRepo("github.com", "o/r"); err != nil {
		t.Fatal(err)
	}
	seen, _ = st.SeenReviews("github.com", "o/r")
	if len(seen) != 0 {
		t.Fatalf("seen must cascade on unsubscribe: %v", seen)
	}
	if err := st.UnsubscribeRepo("github.com", "o/r"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second unsubscribe: %v", err)
	}
}

func TestCategories(t *testing.T) {
	st := testStore(t)
	if _, err := st.SubscribeRepo(Repo{Domain: "github.com", Repo: "o/r"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCategory("github.com", "o/r", 1, ReviewCategory{Priority: "high", Category: "security", Summary: "touches auth"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCategory("github.com", "o/r", 2, ReviewCategory{Priority: "low"}); err != nil {
		t.Fatal(err)
	}
	// a second set replaces the row
	if err := st.SetCategory("github.com", "o/r", 2, ReviewCategory{Priority: "normal", Category: "docs"}); err != nil {
		t.Fatal(err)
	}
	cats, err := st.Categories("github.com", "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if len(cats) != 2 || cats[1].Priority != "high" || cats[1].Category != "security" || cats[1].Summary != "touches auth" {
		t.Fatalf("categories: %+v", cats)
	}
	if cats[2].Priority != "normal" || cats[2].Category != "docs" || cats[2].CreatedAt.IsZero() {
		t.Fatalf("replaced row: %+v", cats[2])
	}
	if err := st.DeleteCategory("github.com", "o/r", 1); err != nil {
		t.Fatal(err)
	}
	cats, _ = st.Categories("github.com", "o/r")
	if _, ok := cats[1]; ok || len(cats) != 1 {
		t.Fatalf("after delete: %+v", cats)
	}
	st.SetCategory("github.com", "o/r", 3, ReviewCategory{Priority: "low"})
	if err := st.PruneCategories("github.com", "o/r", []int{3}); err != nil {
		t.Fatal(err)
	}
	cats, _ = st.Categories("github.com", "o/r")
	if _, ok := cats[3]; !ok || len(cats) != 1 {
		t.Fatalf("after prune: %+v", cats)
	}
	if err := st.UnsubscribeRepo("github.com", "o/r"); err != nil {
		t.Fatal(err)
	}
	cats, _ = st.Categories("github.com", "o/r")
	if len(cats) != 0 {
		t.Fatalf("categories must cascade on unsubscribe: %v", cats)
	}
	// no repo row: the foreign key rejects the insert
	if err := st.SetCategory("github.com", "o/none", 1, ReviewCategory{Priority: "low"}); err == nil {
		t.Fatal("expected foreign key error")
	}
}

func TestDomains(t *testing.T) {
	st := testStore(t)
	st.SubscribePull(Pull{URL: "https://b.example/o/r/pull/1", Domain: "b.example", Repo: "o/r", Number: 1})
	st.SubscribeRepo(Repo{Domain: "a.example", Repo: "o/r"})
	st.SubscribeRepo(Repo{Domain: "b.example", Repo: "o/r"})
	got, err := st.Domains()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"a.example", "b.example"}) {
		t.Fatalf("got %v", got)
	}
}
