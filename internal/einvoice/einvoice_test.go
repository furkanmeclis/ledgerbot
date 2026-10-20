package einvoice

import (
	"os"
	"testing"
)

func TestParseSample(t *testing.T) {
	data, err := os.ReadFile("testdata/TemelFaturaOrnegi.xml")
	if err != nil {
		t.Fatal(err)
	}
	if !Looks(data) {
		t.Fatal("Looks() = false for UBL")
	}
	d, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID == "" || d.Supplier.Name == "" {
		t.Fatalf("missing header fields: %+v", d)
	}
	if len(d.Lines) == 0 {
		t.Fatal("no lines parsed")
	}
	if d.Payable != 1788 {
		t.Fatalf("payable = %d, want 1788", d.Payable)
	}
	var sum int64
	for _, l := range d.Lines {
		if l.Name == "" {
			t.Errorf("line without name: %+v", l)
		}
		sum += int64(l.Total)
	}
	if sum == 0 {
		t.Error("line totals are zero")
	}
}

func TestParseRejectsNonInvoice(t *testing.T) {
	if _, err := Parse([]byte("<foo/>")); err == nil {
		t.Fatal("expected error")
	}
}

func BenchmarkParse(b *testing.B) {
	data, err := os.ReadFile("testdata/TemelFaturaOrnegi.xml")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Parse(data); err != nil {
			b.Fatal(err)
		}
	}
}
