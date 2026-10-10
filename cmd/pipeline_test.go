package cmd

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/J0sh0909/rift/internal/core"
)

func TestTruncateKeepsUTF8Valid(t *testing.T) {
	s := strings.Repeat("é", 10) // 2 bytes each
	for n := 0; n <= len(s); n++ {
		if got := truncate(s, n); !utf8.ValidString(got) {
			t.Fatalf("truncate(%d) produced invalid UTF-8: %q", n, got)
		}
	}
}

func TestCodeBlockFence(t *testing.T) {
	got := codeBlock("a ``` b")
	if !strings.HasPrefix(got, "````text\n") || !strings.HasSuffix(got, "\n````\n") {
		t.Errorf("fence must be longer than any backtick run: %q", got)
	}
}

func TestDisplayArgsRoundTrip(t *testing.T) {
	args := []string{"snapshot", "create", "-n", `pre "upgrade"`, `C:\Program Files\x`, "", "it's"}
	back, err := core.SplitCommandLine(displayArgs(args))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(back, "|") != strings.Join(args, "|") {
		t.Errorf("round trip:\n got %q\nwant %q", back, args)
	}
}

func TestMdCell(t *testing.T) {
	if got := mdCell("a|b\r\nc"); got != `a\|b c` {
		t.Errorf("mdCell = %q", got)
	}
}
