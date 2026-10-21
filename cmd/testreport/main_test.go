package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const failingRun = `{"Action":"run","Package":"x/p","Test":"TestA"}
{"Action":"output","Package":"x/p","Test":"TestA","Output":"    a_test.go:9: boom\n"}
{"Action":"fail","Package":"x/p","Test":"TestA","Elapsed":0.1}
{"Action":"run","Package":"x/p","Test":"TestB"}
{"Action":"output","Package":"x/p","Test":"TestB","Output":"    b_test.go:3: BUG: parser drops sign\n"}
{"Action":"skip","Package":"x/p","Test":"TestB","Elapsed":0}
{"Action":"run","Package":"x/p","Test":"TestSim"}
{"Action":"output","Package":"x/p","Test":"TestSim","Output":"    s_test.go:1: SIMRESULT {\"name\":\"s\",\"ops\":10,\"errors\":0,\"duration_ms\":5,\"p50_ms\":1,\"p95_ms\":2,\"p99_ms\":3,\"throughput_ops_s\":2000}\n"}
{"Action":"pass","Package":"x/p","Test":"TestSim","Elapsed":0.2}
{"Action":"output","Package":"x/p","Output":"coverage: 61.5% of statements\n"}
{"Action":"fail","Package":"x/p","Elapsed":0.4}
`

func TestReportFromFailingRun(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go-unit.json"), []byte(failingRun), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &report{}
	for _, s := range suiteOrder {
		r.Suites = append(r.Suites, parseSuite(s.title, filepath.Join(dir, "go-"+s.key+".json")))
	}
	unit := r.Suites[0]
	if unit.Pass != 1 || unit.Fail != 1 || unit.Skip != 1 {
		t.Fatalf("pass/fail/skip = %d/%d/%d", unit.Pass, unit.Fail, unit.Skip)
	}
	if unit.Packages[0].Coverage != 61.5 {
		t.Fatalf("coverage = %v", unit.Packages[0].Coverage)
	}
	sum := summarize(r)
	if sum.Verdict != "FAIL" || len(sum.KnownBugs) != 1 || len(sum.SimResults) != 1 || sum.SimResults[0].P95 != 2 {
		t.Fatalf("summary = %+v", sum)
	}
	b, err := json.MarshalIndent(jsonSafe(sum), "", "  ")
	if err != nil || !strings.Contains(string(b), `"verdict": "FAIL"`) {
		t.Fatalf("summary.json gate string missing: %v\n%s", err, b)
	}
	page := renderHTML(r)
	for _, want := range []string{"TestA", "boom", "parser drops sign", "FAIL"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestJSONSafeNaN(t *testing.T) {
	b, err := json.Marshal(jsonSafe(capacity{Profile: "p", SustainedRPS: math.NaN(), PeakRPS: math.Inf(1)}))
	if err != nil || !strings.Contains(string(b), `"sustained_rps":null`) {
		t.Fatalf("%v %s", err, b)
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]float64{"512MiB": 512, "1.5GiB": 1536, "2048KiB": 2, "0B": 0} {
		if got := parseSize(in); math.Abs(got-want) > 1e-9 {
			t.Errorf("parseSize(%q) = %v, want %v", in, got, want)
		}
	}
}
