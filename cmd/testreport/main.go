// Command testreport turns the raw output of the heavy test suite into one
// self-contained English HTML report (plus summary.json and summary.md for CI).
// It never fails because tests failed: missing or broken inputs are reported
// in the page, so a report exists for every run.
//
//	go run ./cmd/testreport -in reports/raw -out reports
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var suiteOrder = []struct{ key, title, desc string }{
	{"unit", "Unit", "Package-level tests of internal/* without external services."},
	{"integration", "Integration", "PostgreSQL-backed tests: River queue, store, leases, RAG, worker."},
	{"feature", "Feature", "Black-box tests of every capability over the HTTP API."},
	{"simulation", "Simulation", "In-process simulations of many companies, business days and approval storms."},
}

const (
	capP95Ms   = 500.0
	capErrRate = 0.01
)

type report struct {
	Generated time.Time
	Commit    string
	Suites    []*suite
	Coverage  float64
	Benches   []bench
	PerfMeta  map[string]any
	Profiles  []*profile
	Notes     []string
}

func main() {
	in := flag.String("in", "reports/raw", "raw results directory")
	out := flag.String("out", "reports", "output directory")
	commit := flag.String("commit", os.Getenv("GITHUB_SHA"), "commit shown in the header")
	flag.Parse()

	r := &report{Generated: time.Now().UTC(), Commit: *commit}
	if len(r.Commit) > 12 {
		r.Commit = r.Commit[:12]
	}
	known := map[string]bool{}
	for _, s := range suiteOrder {
		known[s.key] = true
		st := parseSuite(s.title, filepath.Join(*in, "go-"+s.key+".json"))
		r.Suites = append(r.Suites, st)
	}
	extra, _ := filepath.Glob(filepath.Join(*in, "go-*.json"))
	for _, f := range extra {
		k := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "go-"), ".json")
		if !known[k] {
			r.Suites = append(r.Suites, parseSuite(strings.ToUpper(k[:1])+k[1:], f))
		}
	}
	r.Coverage = parseCoverTotal(filepath.Join(*in, "coverage.txt"))
	r.Benches = parseBench(filepath.Join(*in, "bench.txt"))
	r.PerfMeta, r.Profiles = parseProfiles(filepath.Join(*in, "perf"))
	if b, err := os.ReadFile(filepath.Join(*in, "notes.txt")); err == nil {
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				r.Notes = append(r.Notes, l)
			}
		}
	}

	if err := os.MkdirAll(*out, 0o750); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	page := renderHTML(r)
	must(os.WriteFile(filepath.Join(*out, "index.html"), []byte(page), 0o600))
	sum := summarize(r)
	j, _ := json.MarshalIndent(sum, "", "  ")
	must(os.WriteFile(filepath.Join(*out, "summary.json"), j, 0o600))
	must(os.WriteFile(filepath.Join(*out, "summary.md"), []byte(markdown(r, sum)), 0o600))
	fmt.Printf("report: %s (tests %d passed, %d failed, %d skipped; %d perf profiles)\n",
		filepath.Join(*out, "index.html"), sum.Passed, sum.Failed, sum.Skipped, len(r.Profiles))
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// ---- derived numbers ------------------------------------------------------------

type capacity struct {
	Profile        string   `json:"profile"`
	BudgetGB       float64  `json:"budget_gb"`
	SustainedRPS   float64  `json:"sustained_rps"`
	SustainedVUs   int      `json:"sustained_vus"`
	SaturationVUs  int      `json:"saturation_vus"`
	MaxSLOVUs      int      `json:"max_vus_within_slo"`
	AtLimit        []string `json:"services_at_memory_limit,omitempty"`
	PeakRPS        float64  `json:"peak_rps"`
	BestSyncP95Ms  float64  `json:"best_sync_p95_ms"`
	WorstErrRate   float64  `json:"worst_error_rate"`
	OOMKills       int      `json:"oom_kills"`
	Restarts       int      `json:"restarts"`
	UndrainedSteps int      `json:"undrained_steps"`
	InboundWaitP95 float64  `json:"inbound_wait_p95_ms_at_sustained"`
	InboundE2EP95  float64  `json:"inbound_e2e_p95_ms_at_sustained"`
	Error          string   `json:"error,omitempty"`
	capped         bool
}

func capacityOf(p *profile) capacity {
	c := capacity{Profile: p.Name, SustainedRPS: math.NaN(), BestSyncP95Ms: math.NaN(), InboundWaitP95: math.NaN(), InboundE2EP95: math.NaN(), Error: p.Error}
	c.BudgetGB, _ = p.Meta["gb"].(float64)
	c.capped, _ = p.Meta["capped_by_host"].(bool)
	for _, s := range p.Steps {
		if !math.IsNaN(s.RPS) {
			c.PeakRPS = math.Max(c.PeakRPS, s.RPS)
		}
		if !math.IsNaN(s.FailRate) {
			c.WorstErrRate = math.Max(c.WorstErrRate, s.FailRate)
		}
		if !math.IsNaN(s.SyncP95) && (math.IsNaN(c.BestSyncP95Ms) || s.SyncP95 < c.BestSyncP95Ms) {
			c.BestSyncP95Ms = s.SyncP95
		}
		if !s.Drained && !s.MissingK6 {
			c.UndrainedSteps++
		}
		ok := !s.MissingK6 && s.FailRate < capErrRate && s.SyncP95 < capP95Ms
		if ok && (math.IsNaN(c.SustainedRPS) || s.RPS > c.SustainedRPS) {
			c.SustainedRPS, c.SustainedVUs = s.RPS, s.VUs
			if q, ok := s.Queues["inbound"]; ok && len(q.WaitMs) == 3 {
				c.InboundWaitP95, c.InboundE2EP95 = q.WaitMs[1], q.E2EMs[1]
			}
		}
		if ok && s.VUs > c.MaxSLOVUs {
			c.MaxSLOVUs = s.VUs
		}
		if !ok && c.SaturationVUs == 0 {
			c.SaturationVUs = s.VUs
		}
	}
	for _, ct := range p.Containers {
		if ct.OOMKilled {
			c.OOMKills++
		}
		c.Restarts += ct.Restarts
	}
	c.OOMKills = max(c.OOMKills, p.OOMEvents)
	for _, svc := range []string{"postgres", "api", "worker", "gateway"} {
		if pk := p.Peaks[svc]; pk != nil && pk.LimitMB > 0 && pk.MemMB >= pk.LimitMB*0.97 && !c.capped {
			c.AtLimit = append(c.AtLimit, svc)
		}
	}
	return c
}

type summary struct {
	Generated  time.Time         `json:"generated"`
	Commit     string            `json:"commit"`
	Passed     int               `json:"passed"`
	Failed     int               `json:"failed"`
	Skipped    int               `json:"skipped"`
	KnownBugs  []string          `json:"known_bugs"`
	Coverage   float64           `json:"coverage_percent"`
	Suites     map[string][3]int `json:"suites"`
	Capacity   []capacity        `json:"capacity"`
	Findings   []string          `json:"findings"`
	Verdict    string            `json:"verdict"`
	SimResults []simResult       `json:"simulations"`
}

func knownBugs(r *report) []string {
	var out []string
	for _, s := range r.Suites {
		for _, t := range s.Tests {
			if t.Result != "skip" {
				continue
			}
			for _, o := range t.Output {
				if i := strings.Index(o, "BUG:"); i >= 0 {
					out = append(out, fmt.Sprintf("%s %s: %s", shortPkg(t.Package), t.Name, strings.TrimSpace(o[i+4:])))
					break
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func summarize(r *report) summary {
	s := summary{Generated: r.Generated, Commit: r.Commit, Coverage: r.Coverage, Suites: map[string][3]int{}}
	for _, st := range r.Suites {
		s.Passed += st.Pass
		s.Failed += st.Fail
		s.Skipped += st.Skip
		s.Suites[st.Name] = [3]int{st.Pass, st.Fail, st.Skip}
		s.SimResults = append(s.SimResults, st.Sims...)
	}
	s.KnownBugs = knownBugs(r)
	for _, p := range r.Profiles {
		s.Capacity = append(s.Capacity, capacityOf(p))
	}
	s.Findings = findings(r, s)
	missing := 0
	for _, st := range r.Suites {
		if st.Missing {
			missing++
		}
	}
	switch {
	case s.Failed > 0:
		s.Verdict = "FAIL"
	case missing > 0 || len(r.Profiles) == 0:
		s.Verdict = "INCOMPLETE"
	default:
		s.Verdict = "PASS"
	}
	return s
}

func findings(r *report, s summary) []string {
	var f []string
	if s.Failed > 0 {
		f = append(f, fmt.Sprintf("%d test(s) failed; details are in the Failures section.", s.Failed))
	}
	for _, st := range r.Suites {
		if st.Missing {
			f = append(f, fmt.Sprintf("The %s suite produced no results (file %s missing).", st.Name, st.File))
		}
	}
	if len(s.KnownBugs) > 0 {
		f = append(f, fmt.Sprintf("%d known defect(s) are recorded as BUG skips; they are listed under Known defects.", len(s.KnownBugs)))
	}
	var best, worst *capacity
	for i := range s.Capacity {
		c := &s.Capacity[i]
		if c.Error != "" {
			f = append(f, fmt.Sprintf("Profile %s could not run: %s.", c.Profile, c.Error))
			continue
		}
		if c.OOMKills > 0 {
			f = append(f, fmt.Sprintf("Profile %s: the kernel OOM killer fired %d time(s) inside the memory limit (restarts: %d); worst error rate %.1f%%.", c.Profile, c.OOMKills, c.Restarts, c.WorstErrRate*100))
		}
		if len(c.AtLimit) > 0 {
			f = append(f, fmt.Sprintf("Profile %s: these containers hit their memory limit during the run: %s.", c.Profile, strings.Join(c.AtLimit, ", ")))
		}
		if c.SaturationVUs > 0 && c.MaxSLOVUs > 0 {
			f = append(f, fmt.Sprintf("Profile %s keeps the service level up to %d parallel clients and breaks it at %d.", c.Profile, c.MaxSLOVUs, c.SaturationVUs))
		}
		if c.UndrainedSteps > 0 {
			f = append(f, fmt.Sprintf("Profile %s: the queue did not drain within the timeout after %d step(s).", c.Profile, c.UndrainedSteps))
		}
		if c.capped {
			f = append(f, fmt.Sprintf("Profile %s asks for more memory than the Docker host has; limits above the host size are not enforced, so it behaves like the largest profile that fits.", c.Profile))
		}
		if math.IsNaN(c.SustainedRPS) {
			f = append(f, fmt.Sprintf("Profile %s never met the service level (p95 < %.0f ms and errors < %.0f%%) at any load step.", c.Profile, capP95Ms, capErrRate*100))
			continue
		}
		if best == nil || c.SustainedRPS > best.SustainedRPS {
			best = c
		}
		if worst == nil || c.SustainedRPS < worst.SustainedRPS {
			worst = c
		}
	}
	if best != nil {
		f = append(f, fmt.Sprintf("Highest sustained throughput: %.0f req/s on %s at %d parallel clients (sync p95 under %.0f ms, errors under %.0f%%).", best.SustainedRPS, best.Profile, best.SustainedVUs, capP95Ms, capErrRate*100))
	}
	if best != nil && worst != nil && best != worst {
		f = append(f, fmt.Sprintf("Lowest sustained throughput: %.0f req/s on %s, %.1fx below the best profile.", worst.SustainedRPS, worst.Profile, best.SustainedRPS/worst.SustainedRPS))
	}
	for _, sim := range s.SimResults {
		if sim.Errors > 0 {
			f = append(f, fmt.Sprintf("Simulation %q finished with %.0f error(s) out of %.0f operations.", sim.Name, sim.Errors, sim.Ops))
		}
	}
	return f
}

func shortPkg(p string) string { return strings.TrimPrefix(p, "github.com/furkanmeclis/ledgerbot/") }

// ---- HTML -------------------------------------------------------------------------

func renderHTML(r *report) string {
	sum := summarize(r)
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Ledgerbot Test Report</title><style>%s</style></head><body><main>`, css)
	verdictCls := map[string]string{"PASS": "good", "FAIL": "bad", "INCOMPLETE": "warn"}[sum.Verdict]
	w(`<header><div><h1>Ledgerbot test and performance report</h1><p class="sub">Generated %s · commit <code>%s</code>%s</p></div><span class="verdict %s">%s</span></header>`,
		r.Generated.Format("2006-01-02 15:04 UTC"), esc(orDash(r.Commit)), hostLine(r.PerfMeta), verdictCls, sum.Verdict)

	// KPI tiles
	total := sum.Passed + sum.Failed + sum.Skipped
	bestRPS, minOOM := math.NaN(), 0
	for _, c := range sum.Capacity {
		if !math.IsNaN(c.SustainedRPS) && (math.IsNaN(bestRPS) || c.SustainedRPS > bestRPS) {
			bestRPS = c.SustainedRPS
		}
		minOOM += c.OOMKills
	}
	w(`<section class="tiles">`)
	tile(&b, "Tests passed", fmt.Sprintf("%d / %d", sum.Passed, total), fmt.Sprintf("%d failed · %d skipped", sum.Failed, sum.Skipped))
	tile(&b, "Statement coverage", pct(r.Coverage), "all packages, unit + integration")
	tile(&b, "Peak sustained throughput", fmtNum(bestRPS)+" req/s", fmt.Sprintf("p95 < %.0f ms, errors < %.0f%%", capP95Ms, capErrRate*100))
	tile(&b, "RAM profiles measured", fmt.Sprintf("%d", len(r.Profiles)), fmt.Sprintf("%d OOM kill(s) in total", minOOM))
	w(`</section>`)

	w(`<section><h2>Key findings</h2><ul class="findings">`)
	if len(sum.Findings) == 0 {
		w(`<li>Nothing stood out: every suite passed and every profile met the service level.</li>`)
	}
	for _, f := range sum.Findings {
		w(`<li>%s</li>`, esc(f))
	}
	for _, n := range r.Notes {
		w(`<li>%s</li>`, esc(n))
	}
	w(`</ul></section>`)

	renderTests(&b, r, sum)
	renderSims(&b, sum.SimResults)
	renderBench(&b, r.Benches)
	renderPerf(&b, r, sum)
	renderMethod(&b, r)
	w(`</main></body></html>`)
	return b.String()
}

func hostLine(m map[string]any) string {
	if m == nil {
		return ""
	}
	mem, _ := m["host_mem_bytes"].(float64)
	cpus, _ := m["host_cpus"].(float64)
	return fmt.Sprintf(" · host %s, %.0f CPUs, %.1f GiB for Docker · runner %v", esc(fmt.Sprint(m["os"])), cpus, mem/(1<<30), esc(fmt.Sprint(m["runner"])))
}

func orDash(s string) string {
	if s == "" {
		return "local"
	}
	return s
}

func pct(v float64) string {
	if v < 0 || math.IsNaN(v) {
		return "–"
	}
	return fmt.Sprintf("%.1f%%", v)
}

func tile(b *strings.Builder, label, value, note string) {
	fmt.Fprintf(b, `<div class="tile"><div class="label">%s</div><div class="value">%s</div><div class="note">%s</div></div>`, esc(label), esc(value), esc(note))
}

func renderTests(b *strings.Builder, r *report, sum summary) {
	w := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	w(`<section><h2>Functional tests</h2><p class="sub">Unit, integration, feature and simulation suites run with the race detector. Bars show passed, failed and skipped tests per suite.</p>`)
	w(`<div class="card">%s</div>`, outcomeBars(r.Suites))
	w(`<table><thead><tr><th>Suite</th><th>What it covers</th><th class="n">Packages</th><th class="n">Passed</th><th class="n">Failed</th><th class="n">Skipped</th><th class="n">Time</th></tr></thead><tbody>`)
	for i, s := range r.Suites {
		desc := ""
		if i < len(suiteOrder) {
			desc = suiteOrder[i].desc
		}
		if s.Missing {
			desc += " (not run)"
		}
		w(`<tr><td>%s</td><td>%s</td><td class="n">%d</td><td class="n">%d</td><td class="n %s">%d</td><td class="n">%d</td><td class="n">%.1fs</td></tr>`,
			esc(s.Name), esc(desc), len(s.Packages), s.Pass, cls(s.Fail > 0, "badtext"), s.Fail, s.Skip, s.Elapsed)
	}
	w(`</tbody></table>`)

	var fails []*testCase
	for _, s := range r.Suites {
		fails = append(fails, s.failTests...)
	}
	w(`<h3>Failures</h3>`)
	if len(fails) == 0 {
		w(`<p>No failing tests.</p>`)
	}
	for _, t := range fails {
		w(`<details class="fail"><summary><b>%s</b> · %s</summary><pre>%s</pre></details>`, esc(t.Name), esc(shortPkg(t.Package)), esc(strings.Join(t.Output, "")))
	}
	w(`<h3>Known defects</h3>`)
	if len(sum.KnownBugs) == 0 {
		w(`<p>No tests are skipped as known bugs.</p>`)
	} else {
		w(`<p class="sub">Tests that found a real defect are kept as skips with a <code>BUG:</code> reason so the suite stays green while the bug is open.</p><ul>`)
		for _, k := range sum.KnownBugs {
			w(`<li>%s</li>`, esc(k))
		}
		w(`</ul>`)
	}

	// Coverage per package
	type pc struct {
		name string
		cov  float64
	}
	covs := map[string]float64{}
	for _, s := range r.Suites {
		for _, p := range s.Packages {
			if p.Coverage >= 0 {
				covs[p.Name] = math.Max(covs[p.Name], p.Coverage)
			}
		}
	}
	var list []pc
	for k, v := range covs {
		list = append(list, pc{k, v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].cov > list[j].cov })
	if len(list) > 0 {
		w(`<h3>Coverage by package</h3><table class="cov"><tbody>`)
		for _, p := range list {
			w(`<tr><td>%s</td><td class="barcell"><span style="width:%.1f%%"></span></td><td class="n">%.1f%%</td></tr>`, esc(shortPkg(p.name)), p.cov, p.cov)
		}
		w(`</tbody></table>`)
	}

	var all []*testCase
	for _, s := range r.Suites {
		all = append(all, s.Tests...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Elapsed > all[j].Elapsed })
	if len(all) > 15 {
		all = all[:15]
	}
	if len(all) > 0 {
		w(`<h3>Slowest tests</h3><table><thead><tr><th>Test</th><th>Package</th><th>Result</th><th class="n">Time</th></tr></thead><tbody>`)
		for _, t := range all {
			w(`<tr><td>%s</td><td>%s</td><td>%s</td><td class="n">%.2fs</td></tr>`, esc(t.Name), esc(shortPkg(t.Package)), esc(t.Result), t.Elapsed)
		}
		w(`</tbody></table>`)
	}
	w(`</section>`)
}

func cls(c bool, name string) string {
	if c {
		return name
	}
	return ""
}

func renderSims(b *strings.Builder, sims []simResult) {
	w := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	w(`<section><h2>Simulations</h2>`)
	if len(sims) == 0 {
		w(`<p>No simulation results were recorded.</p></section>`)
		return
	}
	w(`<p class="sub">Each simulation drives the assistant in-process with many goroutines and reports its own latency percentiles.</p>`)
	cats := make([]string, len(sims))
	p50, p95, p99 := series{Name: "p50"}, series{Name: "p95"}, series{Name: "p99"}
	for i, s := range sims {
		cats[i] = s.Name
		p50.Values, p95.Values, p99.Values = append(p50.Values, s.P50), append(p95.Values, s.P95), append(p99.Values, s.P99)
	}
	w(`<div class="card"><h4>Latency per simulation</h4>%s</div>`, barChart(cats, []series{p50, p95, p99}, "ms", 0))
	w(`<table><thead><tr><th>Simulation</th><th class="n">Operations</th><th class="n">Errors</th><th class="n">Duration</th><th class="n">Throughput</th><th class="n">p50</th><th class="n">p95</th><th class="n">p99</th><th>Details</th></tr></thead><tbody>`)
	for _, s := range sims {
		var extra []string
		keys := make([]string, 0, len(s.Extra))
		for k := range s.Extra {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			extra = append(extra, fmt.Sprintf("%s=%v", k, s.Extra[k]))
		}
		w(`<tr><td>%s</td><td class="n">%s</td><td class="n %s">%s</td><td class="n">%s ms</td><td class="n">%s op/s</td><td class="n">%s ms</td><td class="n">%s ms</td><td class="n">%s ms</td><td class="small">%s</td></tr>`,
			esc(s.Name), fmtNum(s.Ops), cls(s.Errors > 0, "badtext"), fmtNum(s.Errors), fmtNum(s.DurationMs), fmtNum(s.ThroughputOpsS), fmtNum(s.P50), fmtNum(s.P95), fmtNum(s.P99), esc(strings.Join(extra, " · ")))
	}
	w(`</tbody></table></section>`)
}

func renderBench(b *strings.Builder, bs []bench) {
	w := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	w(`<section><h2>Micro benchmarks</h2>`)
	if len(bs) == 0 {
		w(`<p>No benchmark output was recorded.</p></section>`)
		return
	}
	w(`<p class="sub">Hot paths measured with <code>go test -bench -benchmem</code>. Lower is better.</p><table><thead><tr><th>Benchmark</th><th>Package</th><th class="n">ns/op</th><th class="n">ops/s</th><th class="n">B/op</th><th class="n">allocs/op</th></tr></thead><tbody>`)
	for _, x := range bs {
		w(`<tr><td>%s</td><td>%s</td><td class="n">%s</td><td class="n">%s</td><td class="n">%s</td><td class="n">%s</td></tr>`,
			esc(strings.TrimPrefix(x.Name, "Benchmark")), esc(shortPkg(x.Package)), fmtNum(x.NsOp), fmtNum(1e9/x.NsOp), fmtNum(x.BOp), fmtNum(x.AllocsOp))
	}
	w(`</tbody></table></section>`)
}

func renderPerf(b *strings.Builder, r *report, sum summary) {
	w := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }
	w(`<section><h2>Load under RAM budgets</h2>`)
	if len(r.Profiles) == 0 {
		w(`<p>No performance profiles were recorded.</p></section>`)
		return
	}
	w(`<p class="sub">The whole stack (PostgreSQL, 2 api replicas, 3 workers, gateway) runs under one total memory budget per profile. Each step runs a fixed number of parallel clients against 4,000 companies with 60%% synchronous queries and 40%% queued writes.</p>`)

	// x axis = union of steps
	vusSet := map[int]bool{}
	for _, p := range r.Profiles {
		for _, s := range p.Steps {
			vusSet[s.VUs] = true
		}
	}
	var vus []int
	for v := range vusSet {
		vus = append(vus, v)
	}
	sort.Ints(vus)
	labels := make([]string, len(vus))
	for i, v := range vus {
		labels[i] = fmt.Sprint(v)
	}
	metric := func(f func(*step) float64) []series {
		var ss []series
		for _, p := range r.Profiles {
			sr := series{Name: p.Name}
			idx := map[int]*step{}
			for _, s := range p.Steps {
				idx[s.VUs] = s
			}
			for _, v := range vus {
				if s := idx[v]; s != nil && !s.MissingK6 {
					sr.Values = append(sr.Values, f(s))
				} else {
					sr.Values = append(sr.Values, math.NaN())
				}
			}
			ss = append(ss, sr)
		}
		return ss
	}
	qv := func(q string, field string, i int) func(*step) float64 {
		return func(s *step) float64 {
			x, ok := s.Queues[q]
			if !ok {
				return math.NaN()
			}
			var arr []float64
			switch field {
			case "wait":
				arr = x.WaitMs
			case "run":
				arr = x.RunMs
			default:
				arr = x.E2EMs
			}
			if len(arr) <= i {
				return math.NaN()
			}
			return arr[i]
		}
	}

	// Capacity
	cats := make([]string, 0, len(sum.Capacity))
	sus, peak := series{Name: "sustained (meets SLO)"}, series{Name: "peak measured"}
	for _, c := range sum.Capacity {
		cats = append(cats, c.Profile)
		sus.Values = append(sus.Values, c.SustainedRPS)
		peak.Values = append(peak.Values, c.PeakRPS)
	}
	w(`<div class="card"><h4>Capacity per RAM profile</h4><p class="sub">Sustained = highest throughput where sync p95 &lt; %.0f ms and errors &lt; %.0f%%.</p>%s</div>`, capP95Ms, capErrRate*100, barChart(cats, []series{sus, peak}, "requests / s", 0))
	w(`<table><thead><tr><th>Profile</th><th class="n">Sustained req/s</th><th class="n">at VUs</th><th class="n">Max VUs within SLO</th><th class="n">Saturates at VUs</th><th class="n">Peak req/s</th><th class="n">Best sync p95</th><th class="n">Worst errors</th><th class="n">Queue wait p95</th><th class="n">Queue end-to-end p95</th><th class="n">OOM kills</th><th class="n">Restarts</th></tr></thead><tbody>`)
	for _, c := range sum.Capacity {
		sat := "–"
		if c.SaturationVUs > 0 {
			sat = fmt.Sprint(c.SaturationVUs)
		}
		w(`<tr><td>%s</td><td class="n">%s</td><td class="n">%d</td><td class="n">%d</td><td class="n">%s</td><td class="n">%s</td><td class="n">%s ms</td><td class="n %s">%.2f%%</td><td class="n">%s ms</td><td class="n">%s ms</td><td class="n %s">%d</td><td class="n">%d</td></tr>`,
			esc(c.Profile), fmtNum(c.SustainedRPS), c.SustainedVUs, c.MaxSLOVUs, sat, fmtNum(c.PeakRPS), fmtNum(c.BestSyncP95Ms), cls(c.WorstErrRate >= capErrRate, "badtext"), c.WorstErrRate*100,
			fmtNum(c.InboundWaitP95), fmtNum(c.InboundE2EP95), cls(c.OOMKills > 0, "badtext"), c.OOMKills, c.Restarts)
	}
	w(`</tbody></table>`)

	w(`<div class="grid2">`)
	chart := func(title, sub, unit string, ss []series) {
		w(`<div class="card"><h4>%s</h4><p class="sub">%s</p>%s</div>`, esc(title), esc(sub), lineChart(labels, ss, unit))
	}
	chart("Throughput", "Completed HTTP requests per second.", "requests / s", metric(func(s *step) float64 { return s.RPS }))
	chart("Error rate", "Share of requests that failed (5xx, timeouts, transport).", "% failed", metric(func(s *step) float64 { return s.FailRate * 100 }))
	chart("Sync reply latency p95", "Synchronous answers (balance, stock, summaries).", "ms", metric(func(s *step) float64 { return s.SyncP95 }))
	chart("Sync reply latency p99", "Tail latency of synchronous answers.", "ms", metric(func(s *step) float64 { return s.SyncP99 }))
	chart("Enqueue latency p95", "Time to accept a queued write (HTTP 202).", "ms", metric(func(s *step) float64 { return s.AsyncP95 }))
	chart("Inbound queue wait p95", "Time a message job waits before a worker picks it up.", "ms", metric(qv("inbound", "wait", 1)))
	chart("Inbound end-to-end p95", "From enqueue to processed (understand, permit, save).", "ms", metric(qv("inbound", "e2e", 1)))
	chart("Outbound end-to-end p95", "From reply created to delivered by the channel worker.", "ms", metric(qv("outbound", "e2e", 1)))
	chart("Queue drain time", "Seconds until every job of the step finished after load stopped.", "s", metric(func(s *step) float64 { return s.DrainS }))
	chart("PostgreSQL peak memory", "Highest sampled memory of the database container.", "MiB", metric(func(s *step) float64 { return orNaN(s.PeakMem["postgres"]) }))
	w(`</div>`)

	// Memory by service
	svcs := []string{"postgres", "api", "worker", "gateway"}
	var memSeries []series
	for _, svc := range svcs {
		sr := series{Name: svc}
		for _, p := range r.Profiles {
			if pk := p.Peaks[svc]; pk != nil && pk.LimitMB > 0 {
				sr.Values = append(sr.Values, pk.MemMB/pk.LimitMB*100)
			} else {
				sr.Values = append(sr.Values, math.NaN())
			}
		}
		memSeries = append(memSeries, sr)
	}
	w(`<div class="card"><h4>Peak memory as share of its limit</h4><p class="sub">Highest sampled container memory divided by that container's limit (max replica). 100%% means the limit was hit.</p>%s</div>`, barChart(cats, memSeries, "% of limit", 100))

	// Profile configs and step tables
	w(`<h3>Profiles</h3><table><thead><tr><th>Profile</th><th class="n">Budget</th><th class="n">PostgreSQL</th><th class="n">shared_buffers</th><th class="n">api</th><th class="n">worker</th><th class="n">DB conns / process</th><th class="n">Worker concurrency</th><th>GOMEMLIMIT api / worker</th></tr></thead><tbody>`)
	for _, p := range r.Profiles {
		m := p.Meta
		w(`<tr><td>%s</td><td class="n">%v MB</td><td class="n">%v MB</td><td class="n">%v</td><td class="n">%v × %v MB</td><td class="n">%v × %v MB</td><td class="n">%v</td><td class="n">%v</td><td>%v / %v</td></tr>`,
			esc(p.Name), m["budget_mb"], m["postgres_mb"], m["pg_shared_buffers"], m["api_replicas"], m["api_mb"], m["worker_replicas"], m["worker_mb"], m["db_max_conns"], m["worker_concurrency"], m["api_gomemlimit"], m["worker_gomemlimit"])
	}
	w(`</tbody></table>`)
	for _, p := range r.Profiles {
		w(`<details><summary>Every step of %s</summary><table><thead><tr><th class="n">VUs</th><th class="n">Requests</th><th class="n">req/s</th><th class="n">Errors</th><th class="n">5xx</th><th class="n">Timeouts</th><th class="n">Sync p50 / p95 / p99 / max</th><th class="n">Enqueue p95</th><th class="n">Inbound jobs</th><th class="n">Wait p50 / p95 / p99</th><th class="n">Run p95</th><th class="n">E2E p95 / max</th><th class="n">Retried</th><th class="n">Drain</th></tr></thead><tbody>`, esc(p.Name))
		for _, s := range p.Steps {
			q := s.Queues["inbound"]
			g := func(a []float64, i int) string {
				if len(a) > i {
					return fmtNum(a[i])
				}
				return "–"
			}
			drain := fmt.Sprintf("%.0fs", s.DrainS)
			if !s.Drained {
				drain += " (timeout)"
			}
			w(`<tr><td class="n">%d</td><td class="n">%.0f</td><td class="n">%s</td><td class="n %s">%.2f%%</td><td class="n">%.0f</td><td class="n">%.0f</td><td class="n">%s / %s / %s / %s ms</td><td class="n">%s ms</td><td class="n">%.0f</td><td class="n">%s / %s / %s ms</td><td class="n">%s ms</td><td class="n">%s / %s ms</td><td class="n">%.0f</td><td class="n">%s</td></tr>`,
				s.VUs, s.Reqs, fmtNum(s.RPS), cls(s.FailRate >= capErrRate, "badtext"), s.FailRate*100, s.Err5xx, s.ErrTransport,
				fmtNum(s.SyncP50), fmtNum(s.SyncP95), fmtNum(s.SyncP99), fmtNum(s.SyncMax), fmtNum(s.AsyncP95), q.Jobs,
				g(q.WaitMs, 0), g(q.WaitMs, 1), g(q.WaitMs, 2), g(q.RunMs, 1), g(q.E2EMs, 1), fmtNum(q.E2EMaxMs), q.Retried, drain)
		}
		w(`</tbody></table>`)
		if len(p.Containers) > 0 {
			w(`<p class="small">Containers at the end: `)
			for _, c := range p.Containers {
				flag := ""
				if c.OOMKilled {
					flag = " OOM-killed"
				}
				w(`%s (%s, restarts %d%s) · `, esc(strings.TrimPrefix(c.Name, "/")), esc(c.Status), c.Restarts, flag)
			}
			w(`</p>`)
		}
		w(`</details>`)
	}
	w(`</section>`)
}

func orNaN(v float64) float64 {
	if v == 0 {
		return math.NaN()
	}
	return v
}

func renderMethod(b *strings.Builder, r *report) {
	steps, dur := "", ""
	if r.PerfMeta != nil {
		steps, dur = fmt.Sprint(r.PerfMeta["steps"]), fmt.Sprint(r.PerfMeta["step_duration"])
	}
	fmt.Fprintf(b, `<section><h2>Method</h2><ul class="small">
<li>Functional suites: <code>go test -race -json</code> over <code>./internal/...</code> (unit), <code>./tests/integration</code> (PostgreSQL 17 + pgvector), <code>./tests/feature</code> and <code>./tests/simulation</code>. Coverage is the statement coverage of the unit and integration runs.</li>
<li>Load: <code>scripts/perf/run-profiles.sh</code> starts <code>loadtest/perf/compose.yml</code> with a total memory budget split 35%% PostgreSQL, 2 × 10%% api, 3 × 12%% worker and 6%% (at least 160 MB) for the gateway. Go processes get <code>GOMEMLIMIT</code> at 85%% of their container limit; PostgreSQL gets shared_buffers at 25%% of its limit. Profiles with a <code>-tuned</code> suffix use fewer database connections per process and lower worker concurrency.</li>
<li>Each profile runs k6 steps of %s virtual users, %s each, against 4,000 generated companies (random company per request), all AI providers offline (fake), fake data provider, static FX rates.</li>
<li>Queue times come from River's job table: wait = attempted − created, run = finalized − attempted, end-to-end = finalized − created, for jobs created during the step. Memory and CPU come from <code>docker stats</code> sampled about every two seconds; OOM kills from <code>docker inspect</code> and <code>docker events</code>.</li>
<li>The load generator runs on the same host as the stack, so CPU is shared; absolute numbers are a floor for a dedicated server, the comparison between profiles is the point.</li>
</ul></section>`, esc(steps), esc(dur))
}

func markdown(r *report, s summary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Ledgerbot test report: %s\n\n", s.Verdict)
	fmt.Fprintf(&b, "Tests: **%d passed**, %d failed, %d skipped · coverage %s\n\n", s.Passed, s.Failed, s.Skipped, pct(s.Coverage))
	b.WriteString("| Suite | Passed | Failed | Skipped |\n|---|---|---|---|\n")
	for _, st := range r.Suites {
		fmt.Fprintf(&b, "| %s | %d | %d | %d |\n", st.Name, st.Pass, st.Fail, st.Skip)
	}
	if len(s.Capacity) > 0 {
		b.WriteString("\n| RAM profile | Sustained req/s | at VUs | Max VUs within SLO | Peak req/s | Best sync p95 | Worst errors | OOM kills |\n|---|---|---|---|---|---|---|---|\n")
		for _, c := range s.Capacity {
			fmt.Fprintf(&b, "| %s | %s | %d | %d | %s | %s ms | %.2f%% | %d |\n", c.Profile, fmtNum(c.SustainedRPS), c.SustainedVUs, c.MaxSLOVUs, fmtNum(c.PeakRPS), fmtNum(c.BestSyncP95Ms), c.WorstErrRate*100, c.OOMKills)
		}
	}
	if len(s.Findings) > 0 {
		b.WriteString("\n**Findings**\n\n")
		for _, f := range s.Findings {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}
	if len(s.KnownBugs) > 0 {
		b.WriteString("\n**Known defects (BUG skips)**\n\n")
		for _, k := range s.KnownBugs {
			fmt.Fprintf(&b, "- %s\n", k)
		}
	}
	return b.String()
}
