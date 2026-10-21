// One load step of the heavy performance suite: VUS virtual users for DURATION,
// random company per request. Mix: 60% sync queries, 25% async collections
// (auto-save, queued), 15% async chained commands. Writes the k6 summary to
// /out/$OUT so scripts/perf/run-profiles.sh can collect it.
import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';

const BASE = __ENV.BASE_URL || 'http://gateway:8080';
const COMPANIES = parseInt(__ENV.COMPANIES || '4000', 10);
const status5xx = new Counter('ledgerbot_5xx');
const status429 = new Counter('ledgerbot_429');
const transport = new Counter('ledgerbot_transport_errors');
const queued = new Counter('ledgerbot_async_queued');

export const options = {
  discardResponseBodies: true,
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
  scenarios: {
    step: {
      executor: 'constant-vus',
      vus: parseInt(__ENV.VUS || '50', 10),
      duration: __ENV.DURATION || '30s',
      gracefulStop: '5s',
    },
  },
  thresholds: {
    'http_req_duration{kind:sync}': ['p(95)>=0'],
    'http_req_duration{kind:async}': ['p(95)>=0'],
    'http_req_failed{kind:sync}': ['rate>=0'],
    'http_req_failed{kind:async}': ['rate>=0'],
  },
};

const queries = ['stok toner', 'kritik stok', 'vadesi geçen alacaklar', 'gün sonu özeti', "Ahmet Yılmaz'ın bakiyesi"];

export default function () {
  const n = 1 + Math.floor(Math.random() * COMPANIES);
  const id = 'load' + String(n).padStart(4, '0');
  const headers = { 'Content-Type': 'application/json', Authorization: `Bearer lb_${id}_loadtestsecret000000000000` };
  const r = Math.random();
  let body, kind;
  if (r < 0.6) {
    body = { sender: 'owner', text: queries[Math.floor(Math.random() * queries.length)] };
    kind = 'sync';
  } else if (r < 0.85) {
    body = { sender: 'owner', text: `Ahmet Yılmaz'dan ${100 + Math.floor(Math.random() * 9000)} TL tahsilat`, async: true };
    kind = 'async';
  } else {
    body = { sender: 'owner', text: "Ahmet Yılmaz'a ekstre at ve vadesi geçenleri göster", async: true };
    kind = 'async';
  }
  const res = http.post(`${BASE}/v1/dev/messages`, JSON.stringify(body), { headers, tags: { kind }, timeout: '15s' });
  check(res, { 'status ok': (x) => x.status === 200 || x.status === 202 });
  if (res.status === 0) transport.add(1);
  else if (res.status >= 500) status5xx.add(1);
  else if (res.status === 429) status429.add(1);
  if (res.status === 202) queued.add(1);
}

export function handleSummary(data) {
  const out = {};
  out[`/out/${__ENV.OUT || 'summary.json'}`] = JSON.stringify(data);
  return out;
}
