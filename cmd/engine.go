package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/J0sh0909/rift/internal/core"
)

// status is the outcome of an operation on one target.
type status int

const (
	statusOK      status = iota
	statusSkipped        // nothing to do or not applicable (already running, unsupported...)
	statusFailed         // the operation failed; makes rift exit 1
)

// result is the outcome of an operation on one target.
type result struct {
	t      core.Target
	status status
	code   string // error code for skipped/failed results
	msg    string
	output string // command output shown under the VM (exec, verify)
	quiet  bool   // don't print a status line for an OK result (output says it all)

	transient bool // failure worth retrying (hypervisor CLI flakiness)
}

func success(t core.Target, format string, a ...any) result {
	return result{t: t, status: statusOK, msg: fmt.Sprintf(format, a...)}
}

func skipped(t core.Target, code, format string, a ...any) result {
	return result{t: t, status: statusSkipped, code: code, msg: fmt.Sprintf(format, a...)}
}

func failed(t core.Target, code, format string, a ...any) result {
	return result{t: t, status: statusFailed, code: code, msg: fmt.Sprintf(format, a...)}
}

// flaky records a failure worth retrying. vmrun in particular intermittently
// fails (0xffffffff) when many calls run at once; a retry resolves it without
// limiting parallelism.
func flaky(t core.Target, code string, err error) result {
	r := failed(t, code, "%s", err)
	r.transient = true
	return r
}

// plan describes how to run an operation over targets.
type plan struct {
	verb       string // shown in summaries, e.g. "start"
	sequential bool   // one target at a time per backend (backends still run concurrently)
	retries    int    // extra attempts for transient failures
	warm       bool   // pre-read VMX encryption state before parallel vmrun calls

	// render replaces the default one-line-per-VM console output.
	render func([]result)
}

// powerPlan is the plan shared by power operations.
func powerPlan(verb string) plan {
	return plan{verb: verb, retries: 2, warm: true}
}

// retryBackoff is the delay before the first retry; it grows per attempt.
var retryBackoff = 750 * time.Millisecond

// parallelism caps concurrent operations per backend. Hyper-V calls each
// spawn a PowerShell process (~100 MB), so it gets a lower default.
// RIFT_MAX_PARALLEL in .env overrides both.
func parallelism(backend string) int {
	if settings.MaxParallel > 0 {
		return settings.MaxParallel
	}
	if backend == "hyperv" {
		return 4
	}
	return 8
}

// run executes fn for every target and returns results in target order.
// Targets on different backends run concurrently; within a backend,
// concurrency is capped by parallelism (or 1 for sequential plans).
// Transient failures are retried up to p.retries times.
func run(targets []core.Target, p plan, fn func(core.Target) result) []result {
	results := make([]result, len(targets))
	if len(targets) == 0 {
		return results
	}
	if p.warm {
		warmCaches(targets)
	}
	pending := make([]int, len(targets))
	for i := range pending {
		pending[i] = i
	}
	for attempt := 0; ; attempt++ {
		runBatch(targets, pending, p, fn, results)
		if attempt >= p.retries {
			break
		}
		var retry []int
		for _, i := range pending {
			if results[i].status == statusFailed && results[i].transient {
				retry = append(retry, i)
			}
		}
		if len(retry) == 0 {
			break
		}
		time.Sleep(time.Duration(attempt+1) * retryBackoff)
		pending = retry
	}
	return results
}

func runBatch(targets []core.Target, idx []int, p plan, fn func(core.Target) result, results []result) {
	sems := map[string]chan struct{}{}
	for _, i := range idx {
		b := targets[i].Backend
		if sems[b] == nil {
			n := parallelism(b)
			if p.sequential {
				n = 1
			}
			sems[b] = make(chan struct{}, n)
		}
	}
	var wg sync.WaitGroup
	for _, i := range idx {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem := sems[targets[i].Backend]
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = safeCall(fn, targets[i])
		}()
	}
	wg.Wait()
}

// safeCall turns a panic in one target's operation into a failed result
// instead of crashing the whole run.
func safeCall(fn func(core.Target) result, t core.Target) (r result) {
	defer func() {
		if p := recover(); p != nil {
			r = failed(t, core.ErrHypervisorInit, "internal error: %v", p)
		}
	}()
	r = fn(t)
	r.t = t
	return r
}

// warmCaches pre-reads VMX encryption state sequentially, per backend, so
// parallel goroutines don't all hit the filesystem at once (which disrupts
// vmrun timing on Windows).
func warmCaches(targets []core.Target) {
	paths := map[core.Hypervisor][]string{}
	var order []core.Hypervisor
	for _, t := range targets {
		if _, seen := paths[t.HV]; !seen {
			order = append(order, t.HV)
		}
		paths[t.HV] = append(paths[t.HV], t.Path)
	}
	for _, hv := range order {
		hv.WarmEncryptionCache(paths[hv])
	}
}

// ---------------------------------------------------------------------------
// Reporting
// ---------------------------------------------------------------------------

// failures counts failed targets and resolution errors in this process.
var failures int

type tally struct{ ok, skipped, failed int }

func count(results []result) tally {
	var c tally
	for _, r := range results {
		switch r.status {
		case statusOK:
			c.ok++
		case statusSkipped:
			c.skipped++
		default:
			c.failed++
		}
	}
	return c
}

// report prints results sorted by VM name, records failures for the exit
// status, and adds them to the pipeline summary.
func report(p plan, results []result) {
	sorted := make([]result, len(results))
	copy(sorted, results)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := strings.ToLower(sorted[i].t.Name), strings.ToLower(sorted[j].t.Name)
		if a != b {
			return a < b
		}
		return sorted[i].t.Backend < sorted[j].t.Backend
	})

	if p.render != nil {
		p.render(sorted)
	} else {
		printResults(sorted)
	}

	c := count(sorted)
	if len(sorted) > 1 {
		fmt.Printf("%s: %d ok, %d skipped, %d failed\n", p.verb, c.ok, c.skipped, c.failed)
	}
	failures += c.failed
	pipelineResults(p, sorted)
}

// printResults prints each result's output (under a header when there are
// several targets) followed by its status line.
func printResults(rs []result) {
	multi := spansBackends(rs)
	for _, r := range rs {
		name := targetLabel(r.t, multi)
		if r.output != "" {
			if len(rs) > 1 {
				fmt.Printf("── %s ──\n", name)
			}
			fmt.Print(r.output)
			if !strings.HasSuffix(r.output, "\n") {
				fmt.Println()
			}
		}
		printStatus(r, name)
	}
}

func printStatus(r result, name string) {
	switch {
	case r.status == statusOK && r.quiet:
	case r.status == statusOK:
		core.LogInfo(name, "%s", r.msg)
	default:
		core.LogError(r.code, name, "%s", r.msg)
	}
}

// spansBackends reports whether results come from more than one backend, in
// which case VM labels include the backend to tell same-named VMs apart.
func spansBackends(rs []result) bool {
	for _, r := range rs {
		if r.t.Backend != rs[0].t.Backend {
			return true
		}
	}
	return false
}

func targetLabel(t core.Target, withBackend bool) string {
	if withBackend {
		return t.Name + " [" + t.Backend + "]"
	}
	return t.Name
}

// fail records a failure that is not tied to a target (bad arguments,
// resolution errors) so rift exits 1.
func fail(code, format string, a ...any) {
	core.LogError(code, "", format, a...)
	failures++
	pipelineNote("**error:** " + fmt.Sprintf(format, a...))
}

// finish flushes the pipeline summary and exits 1 if anything failed.
func finish() {
	if failures > 0 {
		exit(1)
	}
	flushPipeline()
}

// exit flushes the pipeline summary and exits.
func exit(code int) {
	flushPipeline()
	os.Exit(code)
}
