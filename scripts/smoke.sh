#!/usr/bin/env bash
# End-to-end smoke test against a running stack (dev mode, demo seed).
set -euo pipefail
BASE=${BASE_URL:-http://localhost:8080}
KEY=${LEDGERBOT_KEY:-lb_demo_demosecret0000000000000000}
H=(-H "Authorization: Bearer $KEY" -H 'Content-Type: application/json')

post() { curl -fsS -X POST "$BASE$1" "${H[@]}" -d "$2"; }
expect() { grep -q "$1" <<<"$2" || { echo "FAIL: expected '$1' in: $2"; exit 1; }; }

echo "== health";      curl -fsS "$BASE/healthz" >/dev/null; curl -fsS "$BASE/readyz" >/dev/null
echo "== capabilities"; out=$(curl -fsS "$BASE/v1/capabilities" "${H[@]}"); expect record_collection "$out"
echo "== ask approval"; out=$(post /v1/dev/messages '{"sender":"accountant","text":"Ahmet Yılmaz'"'"'dan 5.000 TL tahsilat"}'); expect '"status":"pending"' "$out"
echo "== approve";      out=$(post /v1/dev/messages '{"sender":"accountant","text":"evet"}'); expect '"status":"committed"' "$out"
echo "== permission";   out=$(post /v1/dev/messages '{"sender":"sales","text":"Mehmet'"'"'e 100 TL ödeme yaptım"}'); expect 'yetkiniz yok' "$out"
echo "== chain";        out=$(post /v1/dev/messages '{"sender":"owner","text":"Ahmet Yılmaz'"'"'a ekstre at ve vadesi geçenleri göster"}'); expect 'ekstresi' "$out"
echo "== voice (fake STT)"; audio=$(printf '%s' "gün sonu özeti" | base64)
out=$(post /v1/dev/messages '{"sender":"owner","media":{"mime":"audio/ogg","data":"'"$audio"'"}}'); expect 'özeti' "$out"
echo "== async queue";  post /v1/dev/messages '{"sender":"owner","text":"kritik stok","async":true}' >/dev/null
for i in $(seq 1 30); do
  out=$(curl -fsS "$BASE/v1/dev/outbox?to=owner" "${H[@]}")
  grep -q 'Kritik' <<<"$out" && break; sleep 1
done
expect 'Kritik' "$out"
echo "== idempotency"
a=$(curl -fsS -X POST "$BASE/v1/messages/send" "${H[@]}" -H 'Idempotency-Key: smoke-1' -d '{"channel":"dev","to":"owner","text":"hi"}')
b=$(curl -fsS -X POST "$BASE/v1/messages/send" "${H[@]}" -H 'Idempotency-Key: smoke-1' -d '{"channel":"dev","to":"owner","text":"hi"}')
[ "$a" = "$b" ] || { echo "FAIL: idempotent replay differs"; exit 1; }
echo "== memory";       post /v1/dev/messages '{"sender":"owner","text":"not al: depo kirası her ayın 5inde Demir Yapıya ödenir"}' >/dev/null
for i in $(seq 1 30); do
  out=$(post /v1/dev/messages '{"sender":"owner","text":"notlarda depo kirası ara"}')
  grep -q 'Demir' <<<"$out" && break; sleep 1
done
expect 'Demir' "$out"
echo "== reports";      out=$(post /v1/dev/messages '{"sender":"owner","text":"haftalık rapor"}'); expect 'Haftalık' "$out"
out=$(post /v1/dev/messages '{"sender":"accountant","text":"KDV tahmini"}'); expect 'KDV' "$out"
echo "== bank import";  csv=$(printf 'Tarih;Açıklama;Tutar\n01.10.2026;FAST GELEN AHMET YILMAZ %s;1.000,00\n02.10.2026;BILINMEYEN;-50,00\n' "$(date +%s)" | base64)
out=$(post /v1/dev/messages '{"sender":"owner","media":{"mime":"text/csv","filename":"ekstre.csv","data":"'"$csv"'"}}'); expect 'Banka ekstresi' "$out"
echo "== cash forecast"; out=$(post /v1/dev/messages '{"sender":"owner","text":"nakit akışı"}'); expect 'akit' "$out"
echo "== usage";        out=$(curl -fsS "$BASE/v1/usage" "${H[@]}"); expect total_calls "$out"
echo "OK"
