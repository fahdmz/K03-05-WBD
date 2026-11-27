// Stress test endpoint baca. Login di setup(), lalu VU memakai session hasil login
// secara round robin. Tidak ada request selain GET di fase beban.
import http from 'k6/http';
import { check, fail, sleep } from 'k6';
import exec from 'k6/execution';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const USERNAME = __ENV.K6_USERNAME || 'admin';
const PASSWORD = __ENV.K6_PASSWORD || 'admin123';
const SESSIONS = parseInt(__ENV.SESSIONS || '10', 10);
const NORMAL_VUS = parseInt(__ENV.NORMAL_VUS || '20', 10);
const PEAK_VUS = parseInt(__ENV.PEAK_VUS || '300', 10);
const LOW_VUS = parseInt(__ENV.LOW_VUS || '10', 10);
const THINK_TIME = parseFloat(__ENV.THINK_TIME || '0.5');
const SUMMARY_PATH = __ENV.SUMMARY_PATH || 'loadtest/results/summary.json';

const STAGES = [
  { duration: '30s', target: NORMAL_VUS, phase: 'normal' },
  { duration: '1m', target: NORMAL_VUS, phase: 'normal' },
  { duration: '1m', target: PEAK_VUS, phase: 'ramp_up' },
  { duration: '2m', target: PEAK_VUS, phase: 'peak' },
  { duration: '30s', target: LOW_VUS, phase: 'ramp_down' },
  { duration: '1m30s', target: LOW_VUS, phase: 'recovery' },
];
const PHASES = ['normal', 'ramp_up', 'peak', 'ramp_down', 'recovery'];

function seconds(d) {
  let total = 0;
  for (const [, n, unit] of d.matchAll(/(\d+)(m|s)/g)) {
    total += parseInt(n, 10) * (unit === 'm' ? 60 : 1);
  }
  return total;
}

// batas akhir tiap stage (detik sejak scenario mulai) dan durasi total per fase
const BOUNDS = [];
const PHASE_SECONDS = {};
let acc = 0;
for (const s of STAGES) {
  acc += seconds(s.duration);
  BOUNDS.push({ end: acc, phase: s.phase });
  PHASE_SECONDS[s.phase] = (PHASE_SECONDS[s.phase] || 0) + seconds(s.duration);
}

function phaseNow() {
  const elapsed = (Date.now() - exec.scenario.startTime) / 1000;
  for (const b of BOUNDS) {
    if (elapsed < b.end) return b.phase;
  }
  return 'recovery'; // graceful ramp down setelah stage terakhir
}

const phaseThresholds = {};
for (const p of PHASES) {
  phaseThresholds[`http_req_duration{phase:${p}}`] = ['max>=0'];
  phaseThresholds[`http_req_failed{phase:${p}}`] = ['rate>=0'];
  phaseThresholds[`http_reqs{phase:${p}}`] = ['count>=0'];
}

export const options = {
  scenarios: {
    stress: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: STAGES.map(({ duration, target }) => ({ duration, target })),
      gracefulRampDown: '30s',
    },
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
  thresholds: {
    ...phaseThresholds,
    http_req_duration: ['p(95)<500'],
    http_req_failed: ['rate<0.01'],
    checks: ['rate>0.99'],
    'http_req_duration{phase:normal}': ['p(95)<200'],
    'http_req_duration{phase:recovery}': ['p(95)<200'],
  },
};

function withSession(token, extra) {
  return Object.assign(
    { cookies: { session_id: { value: token, replace: true } } },
    extra,
  );
}

export function setup() {
  const tokens = [];
  for (let i = 0; i < SESSIONS; i++) {
    // jar kosong: login yang membawa cookie lama akan me-logout session itu
    const res = http.post(
      `${BASE_URL}/api/auth/login`,
      JSON.stringify({ username: USERNAME, password: PASSWORD }),
      {
        headers: { 'Content-Type': 'application/json' },
        jar: new http.CookieJar(),
        tags: { name: 'POST /api/auth/login', phase: 'setup' },
      },
    );
    if (res.status !== 200) fail(`login ke-${i + 1} gagal: status ${res.status}`);
    const cookie = res.cookies.session_id;
    if (!cookie || cookie.length === 0) fail('response login tanpa cookie session_id');
    tokens.push(cookie[0].value);
  }

  const setupTags = { tags: { phase: 'setup' } };
  const dbs = http.get(`${BASE_URL}/api/databases`, withSession(tokens[0], setupTags));
  if (dbs.status !== 200) fail(`GET /api/databases gagal: status ${dbs.status}`);
  const list = dbs.json();
  if (list.length === 0) fail(`user ${USERNAME} tidak punya akses ke database mana pun`);
  const dbId = __ENV.DB_ID ? parseInt(__ENV.DB_ID, 10) : list[0].id;

  const jobs = http.get(`${BASE_URL}/api/databases/${dbId}/jobs`, withSession(tokens[0], setupTags));
  if (jobs.status !== 200) fail(`GET /api/databases/${dbId}/jobs gagal: status ${jobs.status}`);
  const jobIds = jobs.json().map((j) => j.id);
  if (jobIds.length === 0) fail(`database ${dbId} belum punya job untuk GET /api/jobs/{id}`);

  return { tokens, dbId, jobIds };
}

function get(token, url, name, phase) {
  const res = http.get(url, withSession(token, {
    responseType: 'none',
    tags: { name, phase },
  }));
  check(res, { [`${name} status 200`]: (r) => r.status === 200 });
  return res;
}

export default function (data) {
  const token = data.tokens[(exec.vu.idInTest - 1) % data.tokens.length];
  const phase = phaseNow();
  const jobId = data.jobIds[Math.floor(Math.random() * data.jobIds.length)];

  get(token, `${BASE_URL}/api/databases`, 'GET /api/databases', phase);
  get(token, `${BASE_URL}/api/databases/${data.dbId}/history`, 'GET /api/databases/{id}/history', phase);
  get(token, `${BASE_URL}/api/databases/${data.dbId}/jobs`, 'GET /api/databases/{id}/jobs', phase);
  get(token, `${BASE_URL}/api/jobs/${jobId}`, 'GET /api/jobs/{id}', phase);

  sleep(THINK_TIME);
}

function num(v, digits = 1) {
  return typeof v === 'number' && isFinite(v) ? v.toFixed(digits) : '-';
}

export function handleSummary(data) {
  const m = data.metrics;
  const rows = [
    'fase        p50(ms)  p95(ms)  p99(ms)  error(%)  req     RPS',
  ];
  for (const p of PHASES) {
    const d = (m[`http_req_duration{phase:${p}}`] || {}).values || {};
    const f = (m[`http_req_failed{phase:${p}}`] || {}).values || {};
    const c = (m[`http_reqs{phase:${p}}`] || {}).values || {};
    rows.push([
      p.padEnd(10),
      num(d.med).padStart(8),
      num(d['p(95)']).padStart(8),
      num(d['p(99)']).padStart(8),
      num((f.rate || 0) * 100, 2).padStart(9),
      String(c.count || 0).padStart(7),
      num((c.count || 0) / PHASE_SECONDS[p]).padStart(7),
    ].join(' '));
  }
  const failed = Object.entries(m)
    .filter(([, v]) => v.thresholds && Object.values(v.thresholds).some((t) => !t.ok))
    .map(([k]) => k);
  rows.push('');
  rows.push(failed.length ? `threshold gagal: ${failed.join(', ')}` : 'semua threshold lolos');

  return {
    [SUMMARY_PATH]: JSON.stringify(data, null, 2),
    stdout: rows.join('\n') + '\n',
  };
}
