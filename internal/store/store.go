// Package store persists subscriptions in SQLite: watched pull requests,
// watched repositories, and the review requests already seen per repository.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a subscription does not exist.
var ErrNotFound = errors.New("not found")

// Pull is a watched pull request. Since is the time of the last commit: the
// watch shows activity after this time as new.
type Pull struct {
	URL       string
	Domain    string
	Repo      string
	Number    int
	Since     time.Time
	CreatedAt time.Time
}

// Repo is a watched repository for review requests.
type Repo struct {
	Domain    string
	Repo      string
	CreatedAt time.Time
}

// Key is the "domain/owner/name" form used in messages.
func (r Repo) Key() string { return r.Domain + "/" + r.Repo }

const schema = `
CREATE TABLE IF NOT EXISTS pulls (
	url        TEXT PRIMARY KEY,
	domain     TEXT NOT NULL,
	repo       TEXT NOT NULL,
	number     INTEGER NOT NULL,
	since      TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS repos (
	domain     TEXT NOT NULL,
	repo       TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY (domain, repo)
);
CREATE TABLE IF NOT EXISTS review_seen (
	domain  TEXT NOT NULL,
	repo    TEXT NOT NULL,
	number  INTEGER NOT NULL,
	PRIMARY KEY (domain, repo, number),
	FOREIGN KEY (domain, repo) REFERENCES repos(domain, repo) ON DELETE CASCADE
);
`

// Store wraps the SQLite database.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// DefaultPath returns the default database path: $GHW_DB, or
// $XDG_DATA_HOME/ghw/ghw.db, with the default
// ~/.local/share/ghw/ghw.db.
func DefaultPath() string {
	if p := os.Getenv("GHW_DB"); p != "" {
		return p
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "ghw.db"
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "ghw", "ghw.db")
}

// Open opens the database at path and creates it when needed. It turns on
// WAL mode and foreign keys.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create data directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db, now: time.Now}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// SubscribePull adds a pull request to the watch list. When it exists, the
// call resets its since time.
func (s *Store) SubscribePull(p Pull) (Pull, error) {
	if p.Since.IsZero() {
		p.Since = s.now()
	}
	p.CreatedAt = s.now()
	_, err := s.db.Exec(
		`INSERT INTO pulls (url, domain, repo, number, since, created_at) VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(url) DO UPDATE SET since = excluded.since`,
		p.URL, p.Domain, p.Repo, p.Number, formatTime(p.Since), formatTime(p.CreatedAt),
	)
	if err != nil {
		return Pull{}, err
	}
	return s.GetPull(p.URL)
}

// UnsubscribePull removes a pull request from the watch list.
func (s *Store) UnsubscribePull(url string) error {
	res, err := s.db.Exec(`DELETE FROM pulls WHERE url = ?`, url)
	if err != nil {
		return err
	}
	if k, _ := res.RowsAffected(); k == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, url)
	}
	return nil
}

// GetPull returns one watched pull request.
func (s *Store) GetPull(url string) (Pull, error) {
	row := s.db.QueryRow(`SELECT url, domain, repo, number, since, created_at FROM pulls WHERE url = ?`, url)
	p, err := scanPull(row)
	if errors.Is(err, sql.ErrNoRows) {
		return p, fmt.Errorf("%w: %s", ErrNotFound, url)
	}
	return p, err
}

// ListPulls returns the watched pull requests, ordered by repository and
// number.
func (s *Store) ListPulls() ([]Pull, error) {
	rows, err := s.db.Query(`SELECT url, domain, repo, number, since, created_at FROM pulls ORDER BY domain, repo, number`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Pull
	for rows.Next() {
		p, err := scanPull(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetPullSince records that activity up to t is seen.
func (s *Store) SetPullSince(url string, t time.Time) error {
	res, err := s.db.Exec(`UPDATE pulls SET since = ? WHERE url = ?`, formatTime(t), url)
	if err != nil {
		return err
	}
	if k, _ := res.RowsAffected(); k == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, url)
	}
	return nil
}

type scannable interface {
	Scan(dest ...any) error
}

func scanPull(row scannable) (Pull, error) {
	var p Pull
	var since, created string
	if err := row.Scan(&p.URL, &p.Domain, &p.Repo, &p.Number, &since, &created); err != nil {
		return p, err
	}
	p.Since = parseTime(since)
	p.CreatedAt = parseTime(created)
	return p, nil
}

// SubscribeRepo adds a repository to the review watch list. An existing
// repository keeps its seen set.
func (s *Store) SubscribeRepo(r Repo) (Repo, error) {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO repos (domain, repo, created_at) VALUES (?, ?, ?)`,
		r.Domain, r.Repo, formatTime(s.now()),
	)
	if err != nil {
		return Repo{}, err
	}
	return s.GetRepo(r.Domain, r.Repo)
}

// UnsubscribeRepo removes a repository and its seen set.
func (s *Store) UnsubscribeRepo(domain, repo string) error {
	res, err := s.db.Exec(`DELETE FROM repos WHERE domain = ? AND repo = ?`, domain, repo)
	if err != nil {
		return err
	}
	if k, _ := res.RowsAffected(); k == 0 {
		return fmt.Errorf("%w: %s/%s", ErrNotFound, domain, repo)
	}
	return nil
}

// GetRepo returns one watched repository.
func (s *Store) GetRepo(domain, repo string) (Repo, error) {
	var r Repo
	var created string
	err := s.db.QueryRow(`SELECT domain, repo, created_at FROM repos WHERE domain = ? AND repo = ?`, domain, repo).
		Scan(&r.Domain, &r.Repo, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("%w: %s/%s", ErrNotFound, domain, repo)
	}
	if err != nil {
		return r, err
	}
	r.CreatedAt = parseTime(created)
	return r, nil
}

// ListRepos returns the watched repositories, ordered by domain and name.
func (s *Store) ListRepos() ([]Repo, error) {
	rows, err := s.db.Query(`SELECT domain, repo, created_at FROM repos ORDER BY domain, repo`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Repo
	for rows.Next() {
		var r Repo
		var created string
		if err := rows.Scan(&r.Domain, &r.Repo, &created); err != nil {
			return nil, err
		}
		r.CreatedAt = parseTime(created)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SeenReviews returns the pull request numbers already seen for a
// repository.
func (s *Store) SeenReviews(domain, repo string) (map[int]bool, error) {
	rows, err := s.db.Query(`SELECT number FROM review_seen WHERE domain = ? AND repo = ?`, domain, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[int]bool{}
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		seen[n] = true
	}
	return seen, rows.Err()
}

// MarkSeen records pull request numbers as seen for a repository.
func (s *Store) MarkSeen(domain, repo string, numbers ...int) error {
	if len(numbers) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, n := range numbers {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO review_seen (domain, repo, number) VALUES (?, ?, ?)`, domain, repo, n); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PruneSeen removes seen numbers that are not in keep, so the seen set does
// not grow with closed pull requests.
func (s *Store) PruneSeen(domain, repo string, keep []int) error {
	if len(keep) == 0 {
		_, err := s.db.Exec(`DELETE FROM review_seen WHERE domain = ? AND repo = ?`, domain, repo)
		return err
	}
	args := []any{domain, repo}
	marks := make([]string, len(keep))
	for i, n := range keep {
		marks[i] = "?"
		args = append(args, n)
	}
	_, err := s.db.Exec(
		`DELETE FROM review_seen WHERE domain = ? AND repo = ? AND number NOT IN (`+strings.Join(marks, ",")+`)`, args...)
	return err
}

// Domains returns every domain that has a watched pull request or
// repository, sorted.
func (s *Store) Domains() ([]string, error) {
	rows, err := s.db.Query(`SELECT domain FROM pulls UNION SELECT domain FROM repos ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
