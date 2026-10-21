// Waits until the gateway answers /readyz (used before each RAM profile).
import http from 'k6/http';
import { sleep } from 'k6';

export const options = { vus: 1, iterations: 1, thresholds: { checks: ['rate==1'] } };

export default function () {
  const base = __ENV.BASE_URL || 'http://gateway:8080';
  for (let i = 0; i < 180; i++) {
    const r = http.get(`${base}/readyz`, { timeout: '2s' });
    if (r.status === 200) return;
    sleep(1);
  }
  throw new Error('stack not ready');
}
