// k6 load test: many companies sending messages concurrently.
//
//   go run ./cmd/seedgen -n 4000 -out loadtest/seed
//   docker compose -f compose.yml -f loadtest/compose.loadtest.yml up -d --build
//   k6 run -e COMPANIES=4000 -e VUS=50 -e DURATION=1m loadtest/k6.js
//
// Mix per iteration: 60% queries (sync), 25% collections (async, auto-save),
// 15% chained commands (async). Async messages exercise the worker queue.
import http from 'k6/http';
import { check } from 'k6';
import { Counter } from 'k6/metrics';

const BASE = __ENV.BASE_URL || 'http://localhost:8080';
const COMPANIES = parseInt(__ENV.COMPANIES || '4000', 10);
const queued = new Counter('ledgerbot_async_queued');

export const options = {
  scenarios: {
    tenants: {
      executor: 'constant-vus',
      vus: parseInt(__ENV.VUS || '50', 10),
      duration: __ENV.DURATION || '1m',
    },
  },
  thresholds: {
    'http_req_failed': ['rate<0.01'],
    // Budgets for a single laptop running Postgres, 2 api, 3 workers and k6 together.
    'http_req_duration{kind:sync}': ['p(95)<250'],
    'http_req_duration{kind:async}': ['p(95)<150'],
  },
};

const queries = ['stok toner', 'kritik stok', 'vadesi geçen alacaklar', 'gün sonu özeti', "Ahmet Yılmaz'ın bakiyesi"];

export default function () {
  const n = 1 + Math.floor(Math.random() * COMPANIES);
  const id = 'load' + String(n).padStart(4, '0');
  const headers = {
    'Content-Type': 'application/json',
    Authorization: `Bearer lb_${id}_loadtestsecret000000000000`,
  };
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
  const res = http.post(`${BASE}/v1/dev/messages`, JSON.stringify(body), { headers, tags: { kind } });
  check(res, { 'status ok': (x) => x.status === 200 || x.status === 202 });
  if (res.status === 202) queued.add(1);
}
