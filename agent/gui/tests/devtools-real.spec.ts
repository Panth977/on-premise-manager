/**
 * Dev tools smoke against a REAL agent mesh (project "real"; §21 #97):
 *   cd agent && go run ./cmd/opm-devmesh -devtools          (postgres, redis, mosquitto, httpbin on 127.0.0.1 ports)
 *   OPM_E2E_REAL=1 OPM_AGENT_URL=http://127.0.0.1:8090 OPM_ACCESS_KEY=ABCDEFGHIJKLMNOPQRSTUVWX \
 *   OPM_DT_PG=<port> OPM_DT_REDIS=<port> OPM_DT_MQTT=<port> OPM_DT_HTTP=<port> npx playwright test --project=real tests/devtools-real.spec.ts
 * Creates MANUAL connections through the connection drawer (works with or without the devmesh presets), then runs one
 * SQL query, Redis commands, an MQTT publish and an HTTP request on this site. Screenshots → screenshots/devtools-real-*.png.
 */
import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { pickOption, setEditor, nav } from './helpers';

const KEY = process.env.OPM_ACCESS_KEY ?? '';
const PORT = { pg: process.env.OPM_DT_PG, redis: process.env.OPM_DT_REDIS, mqtt: process.env.OPM_DT_MQTT, http: process.env.OPM_DT_HTTP };
const EMAIL = 'e2e@opm.test';
const RUN = Date.now().toString(36);
async function go(page: Page, path: string) {
  await page.goto(path);
  await page.getByTestId('actor-email').fill(EMAIL);
  await page.getByTestId('actor-continue').click();
  await expect(page.getByTestId('actor-chip')).toContainText(EMAIL);
}
/** create a manual host connection from the Connections page */
async function create(page: Page, kind: string, name: string, port: string, opts: { user?: string; password?: string; url?: string } = {}) {
  await pickOption(page, 'add-conn', kind);
  const d = page.getByTestId('conn-drawer');
  await d.getByTestId('dt-name').fill(name);
  if (opts.url) {
    await d.getByTestId('dt-target-url').click();
    await d.getByTestId('dt-url-input').fill(opts.url);
  } else {
    await d.getByTestId('dt-target-host').click();
    await d.getByTestId('dt-host').fill('127.0.0.1');
    await d.getByTestId('dt-hport').fill(port);
  }
  if (opts.user !== undefined) await d.getByTestId('dt-username-input').fill(opts.user);
  if (opts.password) {
    await d.getByTestId('dt-password-mode-literal').click();
    await d.getByTestId('dt-password-literal').fill(opts.password);
  }
  await d.getByTestId('conn-test').click();
  await expect(d.getByTestId('conn-test-result')).toContainText('Connected', { timeout: 30_000 });
  await d.getByTestId('conn-save').click();
  await expect(d).toBeHidden();
}

test('real agent: dev tools (manual connections, SQL, Redis, MQTT, HTTP, history)', async ({ page }) => {
  test.skip(!KEY || !PORT.pg, 'OPM_ACCESS_KEY / OPM_DT_* not set');
  test.setTimeout(300_000);
  await page.goto('/_opm/');
  await page.waitForURL(/\/_opm\/login/);
  await page.getByTestId('access-key').fill(KEY);
  await page.getByRole('button', { name: 'Log in' }).click();
  await page.getByTestId('actor-email').fill(EMAIL);
  await page.getByTestId('actor-continue').click();
  await go(page, '/_opm/app/app/devtools');
  await create(page, 'postgres', `pg ${RUN}`, PORT.pg!, { user: 'postgres', password: 'devmesh-pg-pass' });
  await create(page, 'redis', `redis ${RUN}`, PORT.redis!, { password: 'devmesh-redis-pass' });
  await create(page, 'mqtt', `mqtt ${RUN}`, PORT.mqtt!);
  await create(page, 'http', `httpbin ${RUN}`, '', { url: `http://127.0.0.1:${PORT.http}` });
  await page.screenshot({ path: 'screenshots/devtools-real-connections.png', fullPage: true });
  const open = async (tool: string, name: string) => {
    await page.getByTestId('conn-table').getByRole('link', { name, exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`devtools/${tool}\\?c=`));
  };
  // SQL
  await open('sql', `pg ${RUN}`);
  await expect(page.getByTestId('schema-tree')).toBeVisible();
  await setEditor(page, 'sql-editor', `CREATE TABLE IF NOT EXISTS opm_e2e (id int PRIMARY KEY, note text);\nINSERT INTO opm_e2e VALUES (1, 'hello') ON CONFLICT (id) DO UPDATE SET note = 'hello';\nSELECT version() AS v, now() AS t, * FROM opm_e2e;`);
  await page.getByTestId('sql-run-all').click();
  await expect(page.getByTestId('rtab-2')).toBeVisible({ timeout: 30_000 });
  await page.getByTestId('rtab-2').click();
  await expect(page.getByTestId('grid-2-h-note')).toBeVisible();
  await page.screenshot({ path: 'screenshots/devtools-real-sql.png' });
  // Redis
  await nav(page, 'devtools');
  await open('redis', `redis ${RUN}`);
  await page.getByTestId('redis-tab-console').click();
  for (const l of ['SET opm:e2e "hello from the dev tools"', 'GET opm:e2e', 'HSET opm:e2e:h a 1 b 2', 'HGETALL opm:e2e:h']) {
    await page.getByTestId('redis-cli').fill(l);
    await page.getByTestId('redis-cli').press('Enter');
    await expect(page.getByTestId('redis-cli')).toBeEnabled();
  }
  await expect(page.getByTestId('redis-out').nth(1)).toContainText('hello from the dev tools', { timeout: 20_000 });
  await page.getByTestId('redis-scan').click();
  await page.getByTestId('rf-opm').click();
  await page.screenshot({ path: 'screenshots/devtools-real-redis.png' });
  // MQTT
  await nav(page, 'devtools');
  await open('mqtt', `mqtt ${RUN}`);
  await expect(page.getByTestId('mqtt-status')).toHaveText('live', { timeout: 30_000 });
  await page.getByTestId('mqtt-pub-topic').fill('opm/e2e');
  await setEditor(page, 'mqtt-pub-payload', '{"from":"playwright"}');
  await page.getByTestId('mqtt-pub-go').click();
  await expect(page.getByTestId('mqtt-messages')).toContainText('opm/e2e', { timeout: 20_000 });
  await page.getByTestId('mqtt-msg').filter({ hasText: 'opm/e2e' }).first().click();
  await page.screenshot({ path: 'screenshots/devtools-real-mqtt.png' });
  // HTTP
  await nav(page, 'devtools');
  await open('http', `httpbin ${RUN}`);
  await setEditor(page, 'http-url', '/get?from=opm');
  await page.getByTestId('http-send').click();
  await expect(page.getByTestId('http-status')).toContainText('200', { timeout: 30_000 });
  await page.screenshot({ path: 'screenshots/devtools-real-http.png' });
  // history has them
  await nav(page, 'devtools/history');
  await expect(page.getByTestId('dh-table')).toContainText('opm:e2e');
  await page.screenshot({ path: 'screenshots/devtools-real-history.png', fullPage: true });
});

/**
 * With the devmesh PRESETS (OPM_DT_PRESETS=1): approve the "dev tools presets" revision through the GUI, the 5 presets
 * on site db, a pg query on p:main-db from site app (forwarded over the mesh) and on db directly (its own GUI, port 8091),
 * the read-only preset refusing a write, Redis on Cache, MQTT on Broker, HTTP on httpbin, History.
 */
test('real agent: devmesh presets on site db (approve, forwarded + direct SQL, read-only, Redis, MQTT, HTTP)', async ({ page }) => {
  test.skip(!KEY || !process.env.OPM_DT_PRESETS, 'OPM_ACCESS_KEY / OPM_DT_PRESETS not set');
  test.setTimeout(420_000);
  const login = async (origin: string) => {
    await page.goto(`${origin}/_opm/`);
    await page.waitForURL(/\/_opm\/login/);
    await page.getByTestId('access-key').fill(KEY);
    await page.getByRole('button', { name: 'Log in' }).click();
    await page.getByTestId('actor-email').fill(EMAIL);
    await page.getByTestId('actor-continue').click();
    await expect(page.getByTestId('actor-chip')).toContainText(EMAIL);
  };
  const APP = 'http://127.0.0.1:8090';
  const DB = 'http://127.0.0.1:8091';
  await login(APP);
  // approve the preset revision (skipped when it is already the head)
  await go(page, `${APP}/_opm/app/app/diff`);
  if (!/head seq [1-9]/.test((await page.getByTestId('head-seq').textContent()) ?? '')) {
    await expect(page.getByTestId('proposal-head')).toContainText('dev tools presets', { timeout: 120_000 });
    await expect(page.getByTestId('approve')).toBeVisible({ timeout: 180_000 });
    await page.getByTestId('approve').click();
    const flow = page.getByTestId('approve-flow');
    await expect(flow.getByTestId('accept-input')).toBeVisible({ timeout: 60_000 });
    await flow.getByTestId('accept-input').fill('ACCEPT');
    await flow.getByTestId('start-update').click();
    await expect(page.getByTestId('update-result')).toContainText('Applied', { timeout: 180_000 });
    await page.getByTestId('update-ok').click();
  }
  // the 5 presets of site db (seen from app: forwarded list)
  await go(page, `${APP}/_opm/app/db/devtools`);
  const table = page.getByTestId('conn-table');
  for (const n of ['Main DB', 'Main DB (read-only)', 'Cache', 'Broker', 'httpbin']) await expect(table.getByRole('link', { name: n, exact: true })).toBeVisible({ timeout: 30_000 });
  await expect(page.getByTestId('conn-row-p:main-db')).toContainText('preset · from revision seq');
  await page.screenshot({ path: 'screenshots/devtools-real-presets.png', fullPage: true });
  // pg on p:main-db from site app → forwarded to db
  await go(page, `${APP}/_opm/app/db/devtools/sql?c=p:main-db`);
  await expect(page.getByTestId('conn-preset')).toBeVisible();
  await setEditor(page, 'sql-editor', "SELECT current_database() AS db, current_user AS who, 'via app → db' AS path;");
  await page.getByTestId('sql-run').click();
  await expect(page.getByTestId('grid-0-h-path')).toBeVisible({ timeout: 30_000 });
  await expect(page.getByTestId('grid-0')).toContainText('via app → db');
  await page.screenshot({ path: 'screenshots/devtools-real-sql-preset.png' });
  // read-only preset refuses a write
  await go(page, `${APP}/_opm/app/db/devtools/sql?c=p:main-db-ro`);
  await expect(page.getByTestId('conn-readonly-badge')).toBeVisible();
  await setEditor(page, 'sql-editor', 'CREATE TABLE opm_ro_probe (id int);');
  await page.getByTestId('sql-run').click();
  await expect(page.getByTestId('result-error')).toContainText(/read-only/i, { timeout: 30_000 });
  // Redis on Cache, MQTT on Broker, HTTP on httpbin (still forwarded from app)
  await go(page, `${APP}/_opm/app/db/devtools/redis?c=p:cache`);
  await page.getByTestId('redis-tab-console').click();
  for (const l of ['SET opm:preset "from a preset"', 'GET opm:preset']) {
    await page.getByTestId('redis-cli').fill(l);
    await page.getByTestId('redis-cli').press('Enter');
    await expect(page.getByTestId('redis-cli')).toBeEnabled();
  }
  await expect(page.getByTestId('redis-out').nth(1)).toContainText('from a preset', { timeout: 20_000 });
  await go(page, `${APP}/_opm/app/db/devtools/mqtt?c=p:broker`);
  await expect(page.getByTestId('mqtt-status')).toHaveText('live', { timeout: 30_000 });
  await page.getByTestId('mqtt-pub-topic').fill('opm/preset');
  await setEditor(page, 'mqtt-pub-payload', '{"preset":"broker"}');
  await page.getByTestId('mqtt-pub-go').click();
  await expect(page.getByTestId('mqtt-messages')).toContainText('opm/preset', { timeout: 20_000 });
  await go(page, `${APP}/_opm/app/db/devtools/http?c=p:httpbin`);
  await setEditor(page, 'http-url', '/anything/opm?preset=1');
  await page.getByTestId('http-send').click();
  await expect(page.getByTestId('http-status')).toContainText('200', { timeout: 30_000 });
  // pg on db directly (its own GUI)
  await login(DB);
  await go(page, `${DB}/_opm/app/db/devtools/sql?c=p:main-db`);
  await setEditor(page, 'sql-editor', "SELECT 'direct on db' AS path, version() AS v;");
  await page.getByTestId('sql-run').click();
  await expect(page.getByTestId('grid-0')).toContainText('direct on db', { timeout: 30_000 });
  // History of db lists them
  await go(page, `${DB}/_opm/app/db/devtools/history`);
  const h = page.getByTestId('dh-table');
  for (const t of ['via app → db', 'direct on db', 'opm_ro_probe', 'opm:preset', 'opm/preset', '/anything/opm']) await expect(h).toContainText(t, { timeout: 20_000 });
  await page.screenshot({ path: 'screenshots/devtools-real-history-presets.png', fullPage: true });
});
