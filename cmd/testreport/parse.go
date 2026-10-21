package main

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ---- go test -json ------------------------------------------------------------

type testCase struct {
	Package, Name, Result string
	Elapsed               float64
	Output                []string
}

type pkgResult struct {
	Name, Result     string
	Elapsed          float64
	Coverage         float64 // -1 when unknown
	Pass, Fail, Skip int
	BuildOutput      []string
}

type suite struct {
	Name      string
	File      string
	Packages  []*pkgResult
	Tests     []*testCase
	Pass      int
	Fail      int
	Skip      int
	Elapsed   float64
	Sims      []simResult
	Missing   bool
	ExitNote  string
	failTests []*testCase
}

type simResult struct {
	Name            string  `json:"name"`
	Ops             float64 `json:"ops"`
	Errors          float64 `json:"errors"`
	DurationMs      float64 `json:"duration_ms"`
	P50, P95, P99   float64
	ThroughputOpsS  float64 `json:"throughput_ops_s"`
	Extra           map[string]any
	Package, Source string
}

type goEvent struct {
	Action     string
	Package    string
	ImportPath string
	Test       string
	Elapsed    float64
	Output     string
}

var coverRe = regexp.MustCompile(`coverage: ([\d.]+)% of statements`)

func parseSuite(name, path string) *suite {
	s := &suite{Name: name, File: filepath.Base(path)}
	f, err := os.Open(path)
	if err != nil {
		s.Missing = true
		return s
	}
	defer f.Close()
	pkgs := map[string]*pkgResult{}
	tests := map[string]*testCase{}
	pkg := func(n string) *pkgResult {
		p := pkgs[n]
		if p == nil {
			p = &pkgResult{Name: n, Coverage: -1}
			pkgs[n] = p
		}
		return p
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var ev goEvent
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if ev.Action == "build-output" || ev.Action == "build-fail" {
			p := pkg(ev.ImportPath)
			if ev.Action == "build-fail" {
				p.Result = "fail"
			} else if len(p.BuildOutput) < 60 {
				p.BuildOutput = append(p.BuildOutput, ev.Output)
			}
			continue
		}
		if ev.Package == "" {
			continue
		}
		p := pkg(ev.Package)
		if ev.Test == "" {
			switch ev.Action {
			case "pass", "fail", "skip":
				p.Result, p.Elapsed = ev.Action, ev.Elapsed
			case "output":
				if m := coverRe.FindStringSubmatch(ev.Output); m != nil {
					p.Coverage, _ = strconv.ParseFloat(m[1], 64)
				}
				if strings.Contains(ev.Output, "FAIL") || strings.Contains(ev.Output, "panic") || strings.HasPrefix(ev.Output, "#") {
					if len(p.BuildOutput) < 60 {
						p.BuildOutput = append(p.BuildOutput, ev.Output)
					}
				}
			}
			continue
		}
		key := ev.Package + "\x00" + ev.Test
		tc := tests[key]
		if tc == nil {
			tc = &testCase{Package: ev.Package, Name: ev.Test}
			tests[key] = tc
		}
		switch ev.Action {
		case "pass", "fail", "skip":
			tc.Result, tc.Elapsed = ev.Action, ev.Elapsed
		case "output":
			if i := strings.Index(ev.Output, "SIMRESULT "); i >= 0 {
				if r, ok := parseSim(ev.Output[i+len("SIMRESULT "):]); ok {
					r.Package, r.Source = ev.Package, ev.Test
					s.Sims = append(s.Sims, r)
				}
			}
			if len(tc.Output) < 80 {
				tc.Output = append(tc.Output, ev.Output)
			}
		}
	}
	for _, tc := range tests {
		if tc.Result == "" {
			tc.Result = "fail" // started but never finished: timeout or panic
		}
		p := pkg(tc.Package)
		switch tc.Result {
		case "pass":
			p.Pass++
		case "fail":
			p.Fail++
			if !hasFailedChild(tests, tc) {
				s.failTests = append(s.failTests, tc)
			}
		case "skip":
			p.Skip++
		}
		s.Tests = append(s.Tests, tc)
	}
	for _, p := range pkgs {
		if p.Result == "" && (p.Pass+p.Fail+p.Skip) > 0 {
			p.Result = "fail"
		}
		if p.Result == "" {
			p.Result = "skip"
		}
		s.Packages = append(s.Packages, p)
		s.Pass += p.Pass
		s.Fail += p.Fail
		s.Skip += p.Skip
		s.Elapsed += p.Elapsed
		if p.Result == "fail" && p.Fail == 0 {
			s.Fail++ // build failure or package-level panic
			s.failTests = append(s.failTests, &testCase{Package: p.Name, Name: "(package)", Result: "fail", Output: p.BuildOutput})
		}
	}
	sort.Slice(s.Packages, func(i, j int) bool { return s.Packages[i].Name < s.Packages[j].Name })
	sort.Slice(s.Tests, func(i, j int) bool { return s.Tests[i].Elapsed > s.Tests[j].Elapsed })
	sort.Slice(s.failTests, func(i, j int) bool {
		return s.failTests[i].Package+s.failTests[i].Name < s.failTests[j].Package+s.failTests[j].Name
	})
	return s
}

func hasFailedChild(all map[string]*testCase, parent *testCase) bool {
	prefix := parent.Name + "/"
	for _, tc := range all {
		if tc.Package == parent.Package && tc.Result == "fail" && strings.HasPrefix(tc.Name, prefix) {
			return true
		}
	}
	return false
}

func parseSim(s string) (simResult, bool) {
	var raw map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(s)), &raw) != nil {
		return simResult{}, false
	}
	num := func(k string) float64 {
		if v, ok := raw[k].(float64); ok {
			return v
		}
		return math.NaN()
	}
	r := simResult{Extra: map[string]any{}}
	r.Name, _ = raw["name"].(string)
	r.Ops, r.Errors, r.DurationMs = num("ops"), num("errors"), num("duration_ms")
	r.P50, r.P95, r.P99, r.ThroughputOpsS = num("p50_ms"), num("p95_ms"), num("p99_ms"), num("throughput_ops_s")
	for k, v := range raw {
		switch k {
		case "name", "ops", "errors", "duration_ms", "p50_ms", "p95_ms", "p99_ms", "throughput_ops_s":
		default:
			r.Extra[k] = v
		}
	}
	return r, true
}

// ---- benchmarks -------------------------------------------------------------

type bench struct {
	Package, Name            string
	N                        int64
	NsOp, BOp, AllocsOp, MBs float64
}

var benchRe = regexp.MustCompile(`^(Benchmark\S+?)(?:-\d+)?\s+(\d+)\s+([\d.]+) ns/op(.*)$`)

func parseBench(path string) []bench {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []bench
	pkg := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "pkg: ") {
			pkg = strings.TrimPrefix(line, "pkg: ")
			continue
		}
		m := benchRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		b := bench{Package: pkg, Name: m[1], BOp: math.NaN(), AllocsOp: math.NaN(), MBs: math.NaN()}
		b.N, _ = strconv.ParseInt(m[2], 10, 64)
		b.NsOp, _ = strconv.ParseFloat(m[3], 64)
		rest := strings.Fields(m[4])
		for i := 0; i+1 < len(rest); i += 2 {
			v, _ := strconv.ParseFloat(rest[i], 64)
			switch rest[i+1] {
			case "B/op":
				b.BOp = v
			case "allocs/op":
				b.AllocsOp = v
			case "MB/s":
				b.MBs = v
			}
		}
		out = append(out, b)
	}
	return out
}

func parseCoverTotal(path string) float64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "total:") {
			f := strings.Fields(line)
			v, err := strconv.ParseFloat(strings.TrimSuffix(f[len(f)-1], "%"), 64)
			if err == nil {
				return v
			}
		}
	}
	return -1
}

// ---- performance profiles -----------------------------------------------------

type profile struct {
	Dir        string
	Name       string
	Meta       map[string]any
	Error      string
	Steps      []*step
	Containers []containerState
	OOMEvents  int
	DieEvents  int
	Peaks      map[string]*svcPeak // service -> peak usage
}

type containerState struct {
	Name      string `json:"name"`
	Service   string `json:"service"`
	OOMKilled bool   `json:"oom_killed"`
	Restarts  int    `json:"restarts"`
	Status    string `json:"status"`
	ExitCode  int    `json:"exit_code"`
}

type svcPeak struct {
	MemMB, LimitMB, CPU float64
}

type step struct {
	VUs                                int
	Reqs, RPS, FailRate                float64
	SyncP50, SyncP95, SyncP99, SyncMax float64
	AsyncP50, AsyncP95, AsyncP99       float64
	SyncFail, AsyncFail                float64
	Err5xx, Err429, ErrTransport       float64
	Queued                             float64
	DrainS                             float64
	Drained                            bool
	Queues                             map[string]queueStat
	PeakMem                            map[string]float64 // service -> MB (max replica)
	MissingK6                          bool
}

type queueStat struct {
	Queue     string    `json:"queue"`
	Jobs      float64   `json:"jobs"`
	Completed float64   `json:"completed"`
	Failed    float64   `json:"failed"`
	Retried   float64   `json:"retried"`
	WaitMs    []float64 `json:"wait_ms"`
	RunMs     []float64 `json:"run_ms"`
	E2EMs     []float64 `json:"e2e_ms"`
	E2EMaxMs  float64   `json:"e2e_max_ms"`
}

func parseProfiles(dir string) (map[string]any, []*profile) {
	var meta map[string]any
	if b, err := os.ReadFile(filepath.Join(dir, "meta.json")); err == nil {
		_ = json.Unmarshal(b, &meta)
	}
	entries, _ := os.ReadDir(dir)
	var out []*profile
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := &profile{Dir: filepath.Join(dir, e.Name()), Name: e.Name(), Peaks: map[string]*svcPeak{}}
		if b, err := os.ReadFile(filepath.Join(p.Dir, "profile.json")); err == nil {
			_ = json.Unmarshal(b, &p.Meta)
		}
		if b, err := os.ReadFile(filepath.Join(p.Dir, "error.json")); err == nil {
			var m map[string]string
			_ = json.Unmarshal(b, &m)
			p.Error = m["error"]
		}
		if b, err := os.ReadFile(filepath.Join(p.Dir, "containers.json")); err == nil {
			_ = json.Unmarshal(b, &p.Containers)
		}
		p.OOMEvents, p.DieEvents = countEvents(filepath.Join(p.Dir, "events.jsonl"))
		files, _ := filepath.Glob(filepath.Join(p.Dir, "step-*.json"))
		rivers, _ := filepath.Glob(filepath.Join(p.Dir, "river-*.json"))
		vusSet := map[int]bool{}
		for _, f := range append(files, rivers...) {
			base := filepath.Base(f)
			n, err := strconv.Atoi(strings.TrimSuffix(base[strings.Index(base, "-")+1:], ".json"))
			if err == nil {
				vusSet[n] = true
			}
		}
		for v := range vusSet {
			p.Steps = append(p.Steps, parseStep(p.Dir, v))
		}
		sort.Slice(p.Steps, func(i, j int) bool { return p.Steps[i].VUs < p.Steps[j].VUs })
		parseStats(p)
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return profileOrder(out[i]) < profileOrder(out[j]) })
	return meta, out
}

func profileOrder(p *profile) float64 {
	gb, _ := p.Meta["gb"].(float64)
	if t, _ := p.Meta["tuned"].(bool); t {
		gb += 100
	}
	return gb
}

func countEvents(path string) (oom, die int) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		switch {
		case strings.Contains(line, `"status":"oom"`) || strings.Contains(line, `"Action":"oom"`):
			oom++
		case strings.Contains(line, `"status":"die"`) || strings.Contains(line, `"Action":"die"`):
			die++
		}
	}
	return oom, die
}

func parseStep(dir string, vus int) *step {
	s := &step{VUs: vus, Queues: map[string]queueStat{}, PeakMem: map[string]float64{}}
	nan := math.NaN()
	s.SyncP50, s.SyncP95, s.SyncP99, s.SyncMax, s.AsyncP50, s.AsyncP95, s.AsyncP99 = nan, nan, nan, nan, nan, nan, nan
	s.RPS, s.FailRate, s.DrainS = nan, nan, nan
	b, err := os.ReadFile(filepath.Join(dir, "step-"+strconv.Itoa(vus)+".json"))
	if err != nil {
		s.MissingK6 = true
	} else {
		var sum struct {
			Metrics map[string]struct {
				Values map[string]float64 `json:"values"`
			} `json:"metrics"`
		}
		_ = json.Unmarshal(b, &sum)
		get := func(m, k string) float64 {
			if x, ok := sum.Metrics[m]; ok {
				if v, ok := x.Values[k]; ok {
					return v
				}
			}
			return nan
		}
		zero := func(v float64) float64 {
			if math.IsNaN(v) {
				return 0
			}
			return v
		}
		s.Reqs, s.RPS = zero(get("http_reqs", "count")), get("http_reqs", "rate")
		s.FailRate = get("http_req_failed", "rate")
		s.SyncP50, s.SyncP95, s.SyncP99, s.SyncMax = get("http_req_duration{kind:sync}", "med"), get("http_req_duration{kind:sync}", "p(95)"), get("http_req_duration{kind:sync}", "p(99)"), get("http_req_duration{kind:sync}", "max")
		s.AsyncP50, s.AsyncP95, s.AsyncP99 = get("http_req_duration{kind:async}", "med"), get("http_req_duration{kind:async}", "p(95)"), get("http_req_duration{kind:async}", "p(99)")
		s.SyncFail, s.AsyncFail = get("http_req_failed{kind:sync}", "rate"), get("http_req_failed{kind:async}", "rate")
		s.Err5xx, s.Err429, s.ErrTransport = zero(get("ledgerbot_5xx", "count")), zero(get("ledgerbot_429", "count")), zero(get("ledgerbot_transport_errors", "count"))
		s.Queued = zero(get("ledgerbot_async_queued", "count"))
	}
	if b, err := os.ReadFile(filepath.Join(dir, "river-"+strconv.Itoa(vus)+".json")); err == nil {
		var r struct {
			DrainS  float64     `json:"drain_s"`
			Drained bool        `json:"drained"`
			Queues  []queueStat `json:"queues"`
		}
		if json.Unmarshal(b, &r) == nil {
			s.DrainS, s.Drained = r.DrainS, r.Drained
			for _, q := range r.Queues {
				s.Queues[q.Queue] = q
			}
		}
	}
	return s
}

type statLine struct {
	T    int64  `json:"t"`
	Step string `json:"step"`
	S    struct {
		Name     string `json:"Name"`
		MemUsage string `json:"MemUsage"`
		CPUPerc  string `json:"CPUPerc"`
	} `json:"s"`
}

func parseStats(p *profile) {
	f, err := os.Open(filepath.Join(p.Dir, "stats.jsonl"))
	if err != nil {
		return
	}
	defer f.Close()
	project := "ledgerbot-perf"
	byStep := map[int]*step{}
	for _, s := range p.Steps {
		byStep[s.VUs] = s
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 4<<20)
	for sc.Scan() {
		var l statLine
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		svc := serviceOf(l.S.Name, project)
		parts := strings.Split(l.S.MemUsage, "/")
		if len(parts) != 2 {
			continue
		}
		used, limit := parseSize(parts[0]), parseSize(parts[1])
		cpu, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(l.S.CPUPerc), "%"), 64)
		pk := p.Peaks[svc]
		if pk == nil {
			pk = &svcPeak{}
			p.Peaks[svc] = pk
		}
		pk.MemMB = math.Max(pk.MemMB, used)
		pk.LimitMB = limit
		pk.CPU = math.Max(pk.CPU, cpu)
		stepName := strings.TrimPrefix(l.Step, "drain-")
		if n, err := strconv.Atoi(stepName); err == nil {
			if s := byStep[n]; s != nil {
				s.PeakMem[svc] = math.Max(s.PeakMem[svc], used)
			}
		}
	}
}

var replicaRe = regexp.MustCompile(`-\d+$`)

func serviceOf(name, project string) string {
	n := strings.TrimPrefix(strings.TrimPrefix(name, "/"), project+"-")
	return replicaRe.ReplaceAllString(n, "")
}

func parseSize(s string) float64 {
	s = strings.TrimSpace(s)
	units := []struct {
		suf string
		mb  float64
	}{{"GiB", 1024}, {"MiB", 1}, {"KiB", 1.0 / 1024}, {"GB", 1000 * 1000 * 1000 / 1048576.0}, {"MB", 1000 * 1000 / 1048576.0}, {"kB", 1000 / 1048576.0}, {"B", 1 / 1048576.0}}
	for _, u := range units {
		if strings.HasSuffix(s, u.suf) {
			v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, u.suf)), 64)
			if err != nil {
				return 0
			}
			return v * u.mb
		}
	}
	return 0
}
