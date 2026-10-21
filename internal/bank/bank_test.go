package bank

import (
	"bytes"
	"testing"

	"github.com/xuri/excelize/v2"
)

const sampleCSV = `Hesap Hareketleri
Müşteri: DEMO
İşlem Tarihi;Açıklama;Tutar;Bakiye;Dekont No
01.10.2026;FAST GELEN AHMET YILMAZ FTR2026000123;12.500,00;112.500,00;D1
02.10.2026;KIRA ODEMESI DEMIR YAPI;-25.000,00;87.500,00;D2
03.10.2026;EFT GELEN MEHMET KAYA;1.250,50;88.750,50;D3
;Toplam;;;
`

func TestParseCSV(t *testing.T) {
	txs, err := Parse([]byte(sampleCSV), "text/csv", "ekstre.csv")
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) != 3 {
		t.Fatalf("want 3 rows, got %d: %+v", len(txs), txs)
	}
	if txs[0].Amount != 1250000 || txs[1].Amount != -2500000 || txs[2].Amount != 125050 || txs[0].Reference != "D1" {
		t.Fatalf("amounts: %+v", txs)
	}
}

func TestParseDebitCreditColumns(t *testing.T) {
	csv := "Tarih,Açıklama,Borç,Alacak,Bakiye\n05/10/2026,POS SATIS,,\"3.000,00\",\"10.000,00\"\n06/10/2026,FATURA,\"450,00\",,\"9.550,00\"\n"
	txs, err := Parse([]byte(csv), "text/csv", "x.csv")
	if err != nil || len(txs) != 2 || txs[0].Amount != 300000 || txs[1].Amount != -45000 {
		t.Fatalf("debit/credit: %+v %v", txs, err)
	}
}

func TestParseXLSX(t *testing.T) {
	f := excelize.NewFile()
	rows := [][]any{{"Tarih", "Açıklama", "Tutar"}, {"07.10.2026", "HAVALE AHMET YILMAZ", "5.000,00"}, {"08.10.2026", "MASRAF", "-12,50"}}
	for i, r := range rows {
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		_ = f.SetSheetRow("Sheet1", cell, &r)
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	txs, err := Parse(buf.Bytes(), "", "hareketler.xlsx")
	if err != nil || len(txs) != 2 || txs[0].Amount != 500000 || txs[1].Amount != -1250 {
		t.Fatalf("xlsx: %+v %v", txs, err)
	}
}

func TestLooks(t *testing.T) {
	if !Looks("application/pdf", "Ekim_ekstre.pdf", "") || Looks("application/pdf", "fatura.pdf", "") || !Looks("text/csv", "a.csv", "") {
		t.Fatal("Looks heuristics")
	}
}

func BenchmarkParseCSV(b *testing.B) {
	data := []byte(sampleCSV)
	b.ReportAllocs()
	for b.Loop() {
		_, _ = Parse(data, "text/csv", "x.csv")
	}
}
