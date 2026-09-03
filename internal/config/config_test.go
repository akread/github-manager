package config

import (
	"path/filepath"
	"reflect"
	"testing"
)

func testFile(t *testing.T) *File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("GHW_CONFIG", path)
	f, err := LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func reload(t *testing.T) *File {
	t.Helper()
	f, err := LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestSplitKey(t *testing.T) {
	cases := map[string][]string{
		"refresh_interval":                        {"refresh_interval"},
		`domains."github.com".excluded_usernames`: {"domains", "github.com", "excluded_usernames"},
		`domains.ghe.example.com`:                 {"domains", "ghe", "example", "com"},
	}
	for in, want := range cases {
		got, err := SplitKey(in)
		if err != nil {
			t.Fatalf("SplitKey(%q): %v", in, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("SplitKey(%q) = %v, want %v", in, got, want)
		}
	}
	for _, bad := range []string{"", "a..b", ".a", "a.", `domains."github.com`} {
		if _, err := SplitKey(bad); err == nil {
			t.Errorf("SplitKey(%q): expected error", bad)
		}
	}
}

func TestJoinKey(t *testing.T) {
	got := JoinKey([]string{"domains", "github.com", "excluded_usernames"})
	want := `domains."github.com".excluded_usernames`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSetGetRoundtrip(t *testing.T) {
	f := testFile(t)
	if err := f.Set("refresh_interval", "90s"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(`domains."github.com".excluded_usernames`, "bot-a", "bot-b"); err != nil {
		t.Fatal(err)
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}

	f = reload(t)
	v, err := f.Get(`domains."github.com".excluded_usernames`)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v, []any{"bot-a", "bot-b"}) {
		t.Fatalf("got %v", v)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	d, err := cfg.Interval()
	if err != nil {
		t.Fatal(err)
	}
	if d.Seconds() != 90 {
		t.Fatalf("interval: got %v", d)
	}
	if got := cfg.Excluded("github.com"); !reflect.DeepEqual(got, []string{"bot-a", "bot-b"}) {
		t.Fatalf("excluded: got %v", got)
	}
	if got := cfg.Excluded("other.example.com"); len(got) != 0 {
		t.Fatalf("unknown domain: got %v", got)
	}
}

func TestAddDelete(t *testing.T) {
	f := testFile(t)
	key := `domains."github.com".excluded_usernames`
	if err := f.Add(key, "bot-a"); err != nil {
		t.Fatal(err)
	}
	if err := f.Add(key, "bot-b"); err != nil {
		t.Fatal(err)
	}
	if err := f.Add("refresh_interval", "5m"); err == nil {
		t.Fatal("add to a single-value key must fail")
	}
	v := "bot-a"
	if err := f.Delete(key, &v); err != nil {
		t.Fatal(err)
	}
	got, err := f.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []any{"bot-b"}) {
		t.Fatalf("got %v", got)
	}
	if err := f.Delete(key, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Get(key); err == nil {
		t.Fatal("deleted key must not be found")
	}
}

func TestKeys(t *testing.T) {
	f := testFile(t)
	if err := f.Set("refresh_interval", "5m"); err != nil {
		t.Fatal(err)
	}
	if err := f.Add(`domains."github.com".excluded_usernames`, "bot"); err != nil {
		t.Fatal(err)
	}
	want := []string{`domains."github.com".excluded_usernames`, "refresh_interval"}
	if got := f.Keys(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestIntervalDefaultsAndErrors(t *testing.T) {
	cfg := &Config{}
	d, err := cfg.Interval()
	if err != nil || d != DefaultRefreshInterval {
		t.Fatalf("default: got %v, %v", d, err)
	}
	for _, bad := range []string{"nope", "10ms", "0"} {
		cfg.RefreshInterval = bad
		if _, err := cfg.Interval(); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}
