package cmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/J0sh0909/rift/internal/core"
	"github.com/spf13/cobra"
)

// version is set at build time:
//
//	go build -ldflags "-X github.com/J0sh0909/rift/cmd.version=1.5.0"
var version = "dev"

// versionString returns the version plus the git commit Go embeds at build.
func versionString() string {
	v := version
	if bi, ok := debug.ReadBuildInfo(); ok {
		var rev string
		var dirty bool
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if len(rev) > 7 {
			rev = rev[:7]
		}
		if rev != "" {
			if dirty {
				rev += "-dirty"
			}
			v += " (" + rev + ")"
		}
	}
	return v
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the rift version",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("rift %s %s/%s %s\n", versionString(), runtime.GOOS, runtime.GOARCH, runtime.Version())
	},
}

// ---------------------------------------------------------------------------
// Pipeline mode
// ---------------------------------------------------------------------------

// pipeline holds the state of a dispatched (CI) run.
var pipeline struct {
	enabled    bool
	command    string
	node       string
	body       strings.Builder // markdown for $GITHUB_STEP_SUMMARY
	structured bool            // a results table was written
	capture    *stdoutCapture
	flushOnce  sync.Once
}

var dispatchEnvFlag string

var dispatchCmd = &cobra.Command{
	Use:   "dispatch [command line]",
	Short: "Run a rift command line given as one string (CI pipeline entry point)",
	Long: `dispatch runs a rift command line received as a single string, the way the
GitHub Actions workflow does on every node:

  rift dispatch --env RIFT_COMMAND     (reads the command from $RIFT_COMMAND)
  rift dispatch "exec -f DEV ip a"

The command line is split without a shell, so workflow inputs can't inject
shell code. Quote arguments with spaces: snapshot create -f DEV -n "pre upgrade".

In dispatch mode rift:
  - prints the node name and version first,
  - treats folders/VMs this node doesn't have as notices, not failures,
    since every node receives the same command,
  - appends a results table (and command output) to $GITHUB_STEP_SUMMARY.`,
	Args: cobra.ArbitraryArgs,
	Run: func(cmd *cobra.Command, args []string) {
		line := strings.Join(args, " ")
		if dispatchEnvFlag != "" {
			line = os.Getenv(dispatchEnvFlag)
		}
		tokens, err := core.SplitCommandLine(line)
		if err != nil {
			fail(core.ErrConfig, "%s", err)
			exit(1)
		}
		if len(tokens) > 0 && (tokens[0] == "rift" || strings.EqualFold(tokens[0], "rift.exe")) {
			tokens = tokens[1:]
		}
		switch {
		case len(tokens) == 0:
			fail(core.ErrConfig, "no command given")
			exit(1)
		case tokens[0] == "dispatch":
			fail(core.ErrConfig, "dispatch cannot run itself")
			exit(1)
		}

		s, _ := core.LoadSettings() // best effort: only for the node name here
		pipeline.enabled = true
		pipeline.command = displayArgs(tokens)
		pipeline.node = s.Node()
		fmt.Printf("rift %s · node %s\n$ rift %s\n", versionString(), pipeline.node, pipeline.command)
		pipeline.capture = startCapture()

		rootCmd.SetArgs(tokens)
		if err := rootCmd.Execute(); err != nil {
			failures++
			pipelineNote("**error:** " + err.Error())
			exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(dispatchCmd, versionCmd)
	dispatchCmd.Flags().StringVar(&dispatchEnvFlag, "env", "", "Read the command line from this environment variable")
	// Everything after the first argument belongs to the dispatched command.
	dispatchCmd.Flags().SetInterspersed(false)
}

// pipelineNote adds a markdown paragraph to the step summary.
func pipelineNote(md string) {
	if !pipeline.enabled {
		return
	}
	pipeline.body.WriteString(md + "\n\n")
}

// pipelineBackends records which hypervisors this node has.
func pipelineBackends(inv *core.Inventory) {
	counts := map[string]int{}
	for _, t := range inv.All() {
		counts[t.Backend]++
	}
	var parts []string
	for _, b := range inv.Backends() {
		parts = append(parts, fmt.Sprintf("%s (%d VMs)", core.BackendLabel(b), counts[b]))
	}
	line := strings.Join(parts, ", ")
	fmt.Printf("node %s: %s\n", pipeline.node, line)
	pipelineNote("Hypervisors: " + line)
}

var statusIcon = map[status]string{
	statusOK:      "✅ ok",
	statusSkipped: "➖ skipped",
	statusFailed:  "❌ failed",
}

// pipelineResults adds a results table, plus each VM's output, to the summary.
func pipelineResults(p plan, rs []result) {
	if !pipeline.enabled || len(rs) == 0 {
		return
	}
	pipeline.structured = true
	b := &pipeline.body
	b.WriteString("| VM | Folder | Hypervisor | Result | Detail |\n|---|---|---|---|---|\n")
	for _, r := range rs {
		detail := r.msg
		if r.code != "" {
			detail = "[" + r.code + "] " + detail
		}
		fmt.Fprintf(b, "| %s | %s | %s | %s | %s |\n", mdCell(r.t.Name), mdCell(r.t.Folder),
			mdCell(core.BackendLabel(r.t.Backend)), statusIcon[r.status], mdCell(detail))
	}
	c := count(rs)
	fmt.Fprintf(b, "\n**%s:** %d ok, %d skipped, %d failed\n\n", p.verb, c.ok, c.skipped, c.failed)
	for _, r := range rs {
		if r.output == "" {
			continue
		}
		fmt.Fprintf(b, "<details open><summary><code>%s</code> output</summary>\n\n%s\n</details>\n\n",
			htmlEscape(r.t.Name), codeBlock(truncate(r.output, 64<<10)))
	}
}

// flushPipeline stops capturing stdout and appends the summary to
// $GITHUB_STEP_SUMMARY. Safe to call more than once.
func flushPipeline() {
	if !pipeline.enabled {
		return
	}
	pipeline.flushOnce.Do(func() {
		var captured string
		if pipeline.capture != nil {
			captured = pipeline.capture.stop()
		}
		path := os.Getenv("GITHUB_STEP_SUMMARY")
		if path == "" {
			return
		}
		var md strings.Builder
		fmt.Fprintf(&md, "### `rift %s` · node %s\n\n", strings.ReplaceAll(pipeline.command, "`", "'"), mdCell(pipeline.node))
		md.WriteString(pipeline.body.String())
		if !pipeline.structured && strings.TrimSpace(captured) != "" {
			md.WriteString(codeBlock(truncate(captured, 256<<10)) + "\n")
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: writing step summary: %s\n", err)
			return
		}
		defer f.Close()
		f.WriteString(truncate(md.String(), 900<<10)) // GitHub caps a step summary at 1 MiB
	})
}

// stdoutCapture tees os.Stdout into a buffer so the output of commands that
// don't produce a results table (list, overview, ...) reaches the summary.
type stdoutCapture struct {
	orig *os.File
	w    *os.File
	buf  bytes.Buffer
	done chan struct{}
}

const maxCapture = 512 << 10

func startCapture() *stdoutCapture {
	r, w, err := os.Pipe()
	if err != nil {
		return nil
	}
	c := &stdoutCapture{orig: os.Stdout, w: w, done: make(chan struct{})}
	os.Stdout = w
	go func() {
		io.Copy(io.MultiWriter(c.orig, capWriter{&c.buf}), r)
		r.Close()
		close(c.done)
	}()
	return c
}

func (c *stdoutCapture) stop() string {
	os.Stdout = c.orig
	c.w.Close()
	<-c.done
	return c.buf.String()
}

// capWriter keeps the first maxCapture bytes and always reports success, so
// the console copy never stops when the buffer is full.
type capWriter struct{ buf *bytes.Buffer }

func (w capWriter) Write(p []byte) (int, error) {
	if room := maxCapture - w.buf.Len(); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		w.buf.Write(p[:room])
	}
	return len(p), nil
}

// ---------------------------------------------------------------------------
// Markdown helpers
// ---------------------------------------------------------------------------

func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.ReplaceAll(s, "\r", "")
	return strings.ReplaceAll(s, "\n", " ")
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// codeBlock fences s with more backticks than any run inside it.
func codeBlock(s string) string {
	longest, run := 0, 0
	for _, c := range s {
		if c == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", max(3, longest+1))
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return fence + "text\n" + s + fence + "\n"
}

// displayArgs joins arguments for display, double-quoting the ones that
// need it, so the printed command can be pasted back into dispatch.
func displayArgs(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\n\"'") {
			a = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(a) + `"`
		}
		out[i] = a
	}
	return strings.Join(out, " ")
}

// truncate shortens s to at most n bytes without splitting a UTF-8 character.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "\n… (truncated)\n"
}

// sortedKeys returns map keys in order (for deterministic output).
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
