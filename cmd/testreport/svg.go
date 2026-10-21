package main

import (
	"fmt"
	"html"
	"math"
	"strings"
)

// Small dependency-free SVG charts so the report is one self-contained file
// that opens offline from a CI artifact. Colors are CSS variables defined in
// the page (light and dark), marks carry <title> tooltips.

type series struct {
	Name   string
	Values []float64 // NaN = missing
}

const (
	chartW = 640
	chartH = 300
	padL   = 64
	padR   = 16
	padT   = 16
	padB   = 44
)

func esc(s string) string { return html.EscapeString(s) }

func niceMax(v float64) float64 {
	if v <= 0 || math.IsNaN(v) {
		return 1
	}
	exp := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if m*exp >= v {
			return m * exp
		}
	}
	return 10 * exp
}

func fmtNum(v float64) string {
	switch {
	case math.IsNaN(v):
		return "–"
	case math.Abs(v) >= 10000:
		return fmt.Sprintf("%.0f", v)
	case math.Abs(v) >= 100:
		return fmt.Sprintf("%.0f", v)
	case math.Abs(v) >= 10:
		return fmt.Sprintf("%.1f", v)
	default:
		return fmt.Sprintf("%.2f", v)
	}
}

func legend(names []string) string {
	if len(names) < 2 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class="legend">`)
	for i, n := range names {
		fmt.Fprintf(&b, `<span><i style="background:var(--s%d)"></i>%s</span>`, i%8+1, esc(n))
	}
	b.WriteString(`</div>`)
	return b.String()
}

// lineChart plots series over categorical x labels, one y axis.
func lineChart(xLabels []string, ss []series, yUnit string) string {
	maxV := 0.0
	for _, s := range ss {
		for _, v := range s.Values {
			if !math.IsNaN(v) && !math.IsInf(v, 0) {
				maxV = math.Max(maxV, v)
			}
		}
	}
	top := niceMax(maxV)
	iw, ih := float64(chartW-padL-padR), float64(chartH-padT-padB)
	x := func(i int) float64 {
		if len(xLabels) == 1 {
			return padL + iw/2
		}
		return padL + iw*float64(i)/float64(len(xLabels)-1)
	}
	y := func(v float64) float64 { return padT + ih - ih*v/top }
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d %d" role="img" class="chart">`, chartW, chartH)
	for i := 0; i <= 4; i++ {
		v := top * float64(i) / 4
		fmt.Fprintf(&b, `<line x1="%d" x2="%d" y1="%.1f" y2="%.1f" class="grid"/>`, padL, chartW-padR, y(v), y(v))
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" class="tick" text-anchor="end">%s</text>`, padL-8, y(v)+4, fmtNum(v))
	}
	for i, l := range xLabels {
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" class="tick" text-anchor="middle">%s</text>`, x(i), chartH-padB+18, esc(l))
	}
	fmt.Fprintf(&b, `<text x="%d" y="%d" class="axis" text-anchor="middle">virtual users (parallel clients)</text>`, padL+int(iw/2), chartH-6)
	fmt.Fprintf(&b, `<text x="14" y="%d" class="axis" text-anchor="middle" transform="rotate(-90 14 %d)">%s</text>`, padT+int(ih/2), padT+int(ih/2), esc(yUnit))
	for si, s := range ss {
		color := fmt.Sprintf("var(--s%d)", si%8+1)
		var pts []string
		flush := func() {
			if len(pts) > 1 {
				fmt.Fprintf(&b, `<polyline points="%s" fill="none" stroke="%s" stroke-width="2" stroke-linejoin="round"/>`, strings.Join(pts, " "), color)
			}
			pts = pts[:0]
		}
		for i, v := range s.Values {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				flush()
				continue
			}
			pts = append(pts, fmt.Sprintf("%.1f,%.1f", x(i), y(v)))
		}
		flush()
		for i, v := range s.Values {
			if math.IsNaN(v) || math.IsInf(v, 0) || i >= len(xLabels) {
				continue
			}
			fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="4" fill="%s" class="dot"><title>%s · %s VUs: %s %s</title></circle>`,
				x(i), y(v), color, esc(s.Name), esc(xLabels[i]), fmtNum(v), esc(yUnit))
		}
		// Direct label at the last point (selective labelling).
		for i := len(s.Values) - 1; i >= 0; i-- {
			if v := s.Values[i]; !math.IsNaN(v) && !math.IsInf(v, 0) && len(ss) <= 5 {
				fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" class="dlabel" text-anchor="end">%s</text>`, x(i)-6, y(v)-8, esc(s.Name))
				break
			}
		}
	}
	b.WriteString(`</svg>`)
	names := make([]string, len(ss))
	for i, s := range ss {
		names[i] = s.Name
	}
	return legend(names) + b.String()
}

// barChart draws grouped vertical bars: categories on x, one bar per series.
func barChart(cats []string, ss []series, yUnit string, ref float64) string {
	maxV := ref
	for _, s := range ss {
		for _, v := range s.Values {
			if !math.IsNaN(v) {
				maxV = math.Max(maxV, v)
			}
		}
	}
	top := niceMax(maxV)
	iw, ih := float64(chartW-padL-padR), float64(chartH-padT-padB)
	y := func(v float64) float64 { return padT + ih - ih*v/top }
	group := iw / float64(max(len(cats), 1))
	bw := math.Min(28, (group-12)/float64(max(len(ss), 1))-2)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d %d" role="img" class="chart">`, chartW, chartH)
	for i := 0; i <= 4; i++ {
		v := top * float64(i) / 4
		fmt.Fprintf(&b, `<line x1="%d" x2="%d" y1="%.1f" y2="%.1f" class="grid"/>`, padL, chartW-padR, y(v), y(v))
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" class="tick" text-anchor="end">%s</text>`, padL-8, y(v)+4, fmtNum(v))
	}
	if ref > 0 {
		fmt.Fprintf(&b, `<line x1="%d" x2="%d" y1="%.1f" y2="%.1f" class="ref"/><text x="%d" y="%.1f" class="tick" text-anchor="end">limit</text>`, padL, chartW-padR, y(ref), y(ref), chartW-padR, y(ref)-4)
	}
	fmt.Fprintf(&b, `<text x="14" y="%d" class="axis" text-anchor="middle" transform="rotate(-90 14 %d)">%s</text>`, padT+int(ih/2), padT+int(ih/2), esc(yUnit))
	for ci, c := range cats {
		gx := padL + group*float64(ci)
		width := float64(len(ss))*(bw+2) - 2
		x0 := gx + (group-width)/2
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" class="tick" text-anchor="middle">%s</text>`, gx+group/2, chartH-padB+18, esc(c))
		for si, s := range ss {
			if ci >= len(s.Values) || math.IsNaN(s.Values[ci]) {
				continue
			}
			v := s.Values[ci]
			h := math.Max(ih*v/top, 1)
			bx := x0 + float64(si)*(bw+2)
			fmt.Fprintf(&b, `<path d="M%.1f,%.1f v%.1f a4,4 0 0 1 4,-4 h%.1f a4,4 0 0 1 4,4 v%.1f z" fill="var(--s%d)" class="bar"><title>%s · %s: %s %s</title></path>`,
				bx, padT+ih, -(h - 4), bw-8, h-4, si%8+1, esc(s.Name), esc(c), fmtNum(v), esc(yUnit))
		}
	}
	b.WriteString(`</svg>`)
	names := make([]string, len(ss))
	for i, s := range ss {
		names[i] = s.Name
	}
	return legend(names) + b.String()
}

// outcomeBars draws one stacked horizontal bar per suite (passed/failed/skipped).
func outcomeBars(suites []*suite) string {
	maxT := 1
	for _, s := range suites {
		maxT = max(maxT, s.Pass+s.Fail+s.Skip)
	}
	rowH := 34
	h := padT + rowH*len(suites) + 8
	labelW := 110.0
	iw := float64(chartW) - labelW - 70
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d %d" role="img" class="chart">`, chartW, h)
	for i, s := range suites {
		y := float64(padT + i*rowH)
		fmt.Fprintf(&b, `<text x="%.0f" y="%.1f" class="tick" text-anchor="end">%s</text>`, labelW-8, y+16, esc(s.Name))
		x := labelW
		for _, part := range []struct {
			n    int
			cls  string
			name string
		}{{s.Pass, "good", "passed"}, {s.Fail, "bad", "failed"}, {s.Skip, "muted", "skipped"}} {
			if part.n == 0 {
				continue
			}
			w := math.Max(iw*float64(part.n)/float64(maxT), 3)
			fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="20" rx="3" class="%s"><title>%s: %d %s</title></rect>`, x, y+2, w-2, part.cls, esc(s.Name), part.n, part.name)
			x += w
		}
		label := fmt.Sprintf("%d / %d / %d", s.Pass, s.Fail, s.Skip)
		if s.Missing {
			label = "not run"
		}
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" class="tick">%s</text>`, x+6, y+16, label)
	}
	b.WriteString(`</svg>`)
	return `<div class="legend"><span><i class="good"></i>passed</span><span><i class="bad"></i>failed</span><span><i class="muted"></i>skipped</span></div>` + b.String()
}
