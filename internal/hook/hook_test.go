package hook

import (
	"strings"
	"testing"
	"time"
)

var in = Input{URL: "https://github.com/o/r/pull/7", Domain: "github.com", Repo: "o/r", Number: 7, Title: "Fix auth", Author: "alice"}

func TestRunParsesOutput(t *testing.T) {
	c := Categorizer{Command: `printf '%s' '{"priority":"HIGH","category":"security","summary":"touches\nauth"}'`, Timeout: time.Minute}
	r, err := c.Run(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Priority != "high" || r.Category != "security" || r.Summary != "touches auth" {
		t.Fatalf("result: %+v", r)
	}
}

func TestRunEnvAndStdin(t *testing.T) {
	// the number comes from the environment, the title from stdin
	c := Categorizer{Command: `title=$(sed 's/.*"title":"\([^"]*\)".*/\1/'); echo "log line"; echo "{\"priority\":\"low\",\"category\":\"$GHW_PULL_REPO#$GHW_PULL_NUMBER\",\"summary\":\"$title by $GHW_PULL_AUTHOR\"}"`}
	r, err := c.Run(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Category != "o/r#7" || r.Summary != "Fix auth by alice" {
		t.Fatalf("result: %+v", r)
	}
}

func TestRunErrors(t *testing.T) {
	cases := map[string]string{
		`echo '{"priority":"meh"}'`:                         `priority "meh" is not one of high, normal, low`,
		`echo 'not json'`:                                   "no JSON object",
		`echo '{"priority":'`:                               "decode output",
		`echo '{"priority":"high"}'; echo boom >&2; exit 3`: "categorize: boom",
		`exit 2`: "exit status 2",
	}
	for cmd, want := range cases {
		_, err := Categorizer{Command: cmd}.Run(in)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", cmd, err, want)
		}
	}
}

func TestRunTimeout(t *testing.T) {
	_, err := Categorizer{Command: "sleep 5", Timeout: 50 * time.Millisecond}.Run(in)
	if err == nil || !strings.Contains(err.Error(), "timed out after 50ms") {
		t.Fatalf("got %v", err)
	}
}

func TestParseResultTruncatesCategory(t *testing.T) {
	r, err := ParseResult([]byte(`{"priority":"normal","category":"` + strings.Repeat("x", 40) + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(r.Category)); n != maxCategory || !strings.HasSuffix(r.Category, "…") {
		t.Fatalf("category %q (%d runes)", r.Category, n)
	}
}
