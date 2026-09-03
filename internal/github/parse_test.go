package github

import "testing"

func TestParsePullURL(t *testing.T) {
	ref, err := ParsePullURL("https://github.com/owner/name/pull/42/files#diff")
	if err != nil {
		t.Fatal(err)
	}
	if ref.URL != "https://github.com/owner/name/pull/42" || ref.Domain != "github.com" || ref.Repo != "owner/name" || ref.Number != 42 {
		t.Fatalf("got %+v", ref)
	}
	ref, err = ParsePullURL("https://ghe.example.com/o/n/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Domain != "ghe.example.com" || ref.Number != 7 {
		t.Fatalf("got %+v", ref)
	}
	for _, bad := range []string{"", "owner/name", "https://github.com/owner/name", "https://github.com/owner/name/issues/3"} {
		if _, err := ParsePullURL(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestParseRepo(t *testing.T) {
	cases := map[string]RepoRef{
		"owner/name":                          {Domain: "github.com", Repo: "owner/name"},
		"https://github.com/owner/name":       {Domain: "github.com", Repo: "owner/name"},
		"https://ghe.example.com/owner/name/": {Domain: "ghe.example.com", Repo: "owner/name"},
	}
	for in, want := range cases {
		got, err := ParseRepo(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got != want {
			t.Errorf("%q: got %+v, want %+v", in, got, want)
		}
	}
	for _, bad := range []string{"", "name", "a/b/c", "https://github.com/owner"} {
		if _, err := ParseRepo(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
	if got := (RepoRef{Domain: "github.com", Repo: "o/n"}).URL(); got != "https://github.com/o/n" {
		t.Fatalf("url: %q", got)
	}
}
