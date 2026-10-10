package core

import (
	"fmt"
	"strings"
)

// SplitCommandLine splits a command line into arguments without invoking a
// shell, so a command received as a single string (for example from a
// workflow_dispatch input) can be run without shell injection.
//
// Rules: whitespace separates arguments; '...' is literal; inside "..." a
// backslash escapes only `"` and `\`; outside quotes a backslash is literal so
// Windows paths need no escaping. Quotes can be adjacent to other text
// (--name="a b" is one argument).
func SplitCommandLine(s string) ([]string, error) {
	var (
		args    []string
		cur     strings.Builder
		inToken bool
	)
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if inToken {
				args = append(args, cur.String())
				cur.Reset()
				inToken = false
			}
		case c == '\'':
			inToken = true
			end := indexRune(runes, i+1, '\'')
			if end < 0 {
				return nil, fmt.Errorf("unterminated ' quote in %q", s)
			}
			cur.WriteString(string(runes[i+1 : end]))
			i = end
		case c == '"':
			inToken = true
			closed := false
			for i++; i < len(runes); i++ {
				r := runes[i]
				if r == '\\' && i+1 < len(runes) && (runes[i+1] == '"' || runes[i+1] == '\\') {
					i++
					cur.WriteRune(runes[i])
					continue
				}
				if r == '"' {
					closed = true
					break
				}
				cur.WriteRune(r)
			}
			if !closed {
				return nil, fmt.Errorf("unterminated \" quote in %q", s)
			}
		default:
			inToken = true
			cur.WriteRune(c)
		}
	}
	if inToken {
		args = append(args, cur.String())
	}
	return args, nil
}

func indexRune(runes []rune, from int, r rune) int {
	for i := from; i < len(runes); i++ {
		if runes[i] == r {
			return i
		}
	}
	return -1
}
