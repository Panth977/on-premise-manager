import { test, expect, type Page } from '@playwright/test';
import { login, expectToast, SEED, pick, pickCombo, commitAll, nav } from './helpers';

/**
 * §21 #97 dev-tool presets (SiteConfig.devTools) in the site editors: list + kind picker + drawer (every kind), shared
 * validation (missing container, no literal credential, unknown secret), edit / duplicate / delete, the empty list is
 * OMITTED from the saved site (never devTools: []), Compare · Uncommitted dev tool rows, commit warning for agents < 0.11.0.
 * Screenshots: screenshots/devtools-presets-{list,drawer-pg,drawer-http,diff}.png
 */
const DEP = `/app/deployments/${SEED.deployment}`;
const TPL = `/app/templates/${SEED.template}`;
const SHOTS = 'screenshots';

async function settled(page: Page) {
  await expect(page.locator('.skel')).toHaveCount(0);
  await page.waitForTimeout(250);
}
/** edit the persisted mock DB, then reload */
async function patchMockDb(page: Page, fn: string) {
  await page.waitForTimeout(1200); // the mock persists with a debounce
  await page.evaluate((src) => {
    const db = JSON.parse(localStorage.getItem('opm.mock.db')!);
    new Function('db', src)(db);
    localStorage.setItem('opm.mock.db', JSON.stringify(db));
  }, fn);
  await page.reload();
}
async function writes(page: Page): Promise<{ method: string; path: string; body: string }[]> {
  return page.evaluate(() => (window as unknown as { __opmMockWrites?: { method: string; path: string; body: string }[] }).__opmMockWrites ?? []);
}
async function addKind(page: Page, kind: string) {
  await page.getByTestId('add-devtool').click();
  await page.getByTestId('devtool-kind-picker').getByTestId(`dt-kind-${kind}`).click();
  const d = page.getByTestId('devtool-drawer');
  await expect(d).toBeVisible();
  await expect(d.locator('form')).toHaveAttribute('data-kind', kind);
  return d;
}

test.describe('§21 #97 dev tools presets', () => {
  test('deployment: seeded presets list, auth summary, read-only badge, old-agent warning, drawer', async ({ page }) => {
    await login(page, 'admin', `${DEP}/sites/db/devtools`);
    await settled(page);
    await expect(page.getByTestId('site-editor')).toHaveAttribute('data-section', 'devtools');
    await expect(page.getByTestId('devtools-info')).toContainText('a secret ({{ secret.x }}, recommended)');
    const pg = page.getByTestId('devtool-row-postgres');
    await expect(pg).toContainText('Plant database');
    await expect(pg).toContainText('PostgreSQL');
    await expect(page.getByTestId('devtool-target-postgres')).toHaveText('10.0.0.12:5432');
    await expect(page.getByTestId('devtool-auth-postgres')).toHaveText('user postgres · password: secret pg-password · db acme');
    await expect(page.getByTestId('devtool-target-redis')).toHaveText('redis:tcp');
    await expect(page.getByTestId('devtool-ro-redis')).toBeVisible();
    await expect(page.getByTestId('devtool-ro-postgres')).toHaveCount(0);
    await expect(page.getByTestId('devtool-errors-postgres')).toHaveCount(0);
    await expect(page.getByTestId('tree-db-devtools')).toContainText('2');
    // db runs agent 0.9.0 → presets need 0.11.0
    await expect(page.getByTestId('devtools-agent-warning')).toContainText('Site db runs agent 0.9.0-10080530; dev tool presets need agent 0.11.0 or newer — upgrade the agent first, otherwise the site refuses the revision.');
    await page.screenshot({ path: `${SHOTS}/devtools-presets-list.png` });

    // drawer of the pg preset: secret picker, no password input anywhere
    await page.getByTestId('dt-edit-postgres').click();
    const d = page.getByTestId('devtool-drawer');
    await expect(d.getByTestId('dt-slug')).toBeDisabled();
    await expect(d.getByTestId('dt-password')).toHaveAttribute('data-mode', 'secret');
    await expect(d.getByTestId('dt-password-secret')).toHaveValue('pg-password');
    await expect(d.locator('input[type="password"]')).toHaveCount(0);
    await expect(d.getByTestId('dt-password-mode').getByRole('radio')).toHaveText(['None', 'Value', 'Secret', 'Expression']);
    await expect(d.getByTestId('dt-target')).toBeVisible();
    await d.getByTestId('dt-password').scrollIntoViewIfNeeded();
    await page.waitForTimeout(300);
    await page.screenshot({ path: `${SHOTS}/devtools-presets-drawer-pg.png` });
    await d.getByTestId('dt-cancel').click();
    await expect(d).toHaveCount(0);
  });

  test('template: add every kind (HTTP, PostgreSQL, SQLite, Cassandra, Redis, MQTT), save, reload', async ({ page }) => {
    await login(page, 'admin', `${TPL}/sites/db/devtools`);
    await settled(page);
    await expect(page.getByTestId('empty-state')).toContainText('No dev tool presets');

    // HTTP: URL target + bearer token from a secret + a default header
    let d = await addKind(page, 'http');
    await expect(d.getByTestId('dt-slug')).toHaveValue('http');
    await d.getByTestId('dt-target-url').click();
    await d.getByTestId('dt-url-input').fill('https://api.example.com');
    await d.getByTestId('dt-http-scheme-bearer').click();
    await expect(d.getByTestId('dt-summary')).toContainText('bearer auth needs a token');
    await expect(d.getByTestId('dt-save')).toBeDisabled();
    await d.getByTestId('dt-token-mode-secret').click();
    await pickCombo(page, 'dt-token-secret', 'api-token');
    await d.getByTestId('dt-basepath').fill('/v1');
    await d.getByTestId('dt-hadd').click();
    await d.getByTestId('dt-hname-0').fill('X-Tenant');
    await d.getByTestId('dt-hvalue-0-input').fill('acme');
    await expect(d.getByTestId('dt-summary')).toHaveCount(0);
    await page.waitForTimeout(300);
    await page.screenshot({ path: `${SHOTS}/devtools-presets-drawer-http.png` });
    await d.getByTestId('dt-save').click();
    await expect(d).toHaveCount(0);
    await expect(page.getByTestId('devtool-auth-http')).toHaveText('bearer · token: secret api-token');

    // PostgreSQL: host target, password from a secret, database
    d = await addKind(page, 'postgres');
    await d.getByTestId('dt-target-host').click();
    await expect(d.getByTestId('dt-hport')).toHaveValue('5432');
    await d.getByTestId('dt-host').fill('10.0.0.20');
    await expect(d.getByTestId('dt-username-input')).toHaveValue('postgres');
    await d.getByTestId('dt-password-mode-secret').click();
    await pickCombo(page, 'dt-password-secret', 'api-token');
    await d.getByTestId('dt-database-input').fill('plant');
    await pick(page, 'dt-sslmode', 'require');
    await d.getByTestId('dt-readonly').click();
    await d.getByTestId('dt-save').click();
    await expect(page.getByTestId('devtool-auth-postgres')).toHaveText('user postgres · password: secret api-token · db plant');
    await expect(page.getByTestId('devtool-ro-postgres')).toBeVisible();

    // SQLite: file in the declared volume redis-data
    d = await addKind(page, 'sqlite');
    await expect(d.getByTestId('dt-target-volume-file')).toHaveAttribute('aria-checked', 'true');
    await pick(page, 'dt-volume', 'redis-data');
    await d.getByTestId('dt-vpath').fill('../escape.db');
    await expect(d).toContainText('must be a relative path inside the volume');
    await d.getByTestId('dt-vpath').fill('app.db');
    await d.getByTestId('dt-save').click();
    await expect(page.getByTestId('devtool-target-sqlite')).toHaveText('volume redis-data → app.db');

    // Cassandra: username + password both or none
    d = await addKind(page, 'cassandra');
    await d.getByTestId('dt-target-host').click();
    await d.getByTestId('dt-host').fill('cassandra.internal');
    await expect(d.getByTestId('dt-hport')).toHaveValue('9042');
    await d.getByTestId('dt-username-input').fill('cassandra');
    await expect(d.getByTestId('dt-summary')).toContainText('Cassandra needs both username and password, or neither');
    await d.getByTestId('dt-username-input').fill('');
    await pick(page, 'dt-consistency', 'QUORUM');
    await d.getByTestId('dt-save').click();
    await expect(page.getByTestId('devtool-target-cassandra')).toHaveText('cassandra.internal:9042');

    // Redis: the container redis, default port (6379 = tcp) preselected, db index
    d = await addKind(page, 'redis');
    await expect(d.getByTestId('dt-container')).toHaveAttribute('data-value', 'redis');
    await expect(d.getByTestId('dt-port')).toHaveAttribute('data-value', 'tcp');
    await pick(page, 'dt-redis-db', '2');
    await d.getByTestId('dt-save').click();
    await expect(page.getByTestId('devtool-auth-redis')).toHaveText('db 2');

    // MQTT: host + client id + keep alive range
    d = await addKind(page, 'mqtt');
    await d.getByTestId('dt-target-host').click();
    await d.getByTestId('dt-host').fill('10.0.0.30');
    await expect(d.getByTestId('dt-hport')).toHaveValue('1883');
    await d.getByTestId('dt-clientid-input').fill('opm-console');
    await d.getByTestId('dt-keepalive').fill('70000');
    await expect(d).toContainText('keepAliveSec must be an integer 0–65535');
    await d.getByTestId('dt-keepalive').fill('60');
    await d.getByTestId('dt-save').click();
    await expect(page.getByTestId('devtool-auth-mqtt')).toHaveText('client opm-console');

    await expect(page.getByTestId('devtools-table').locator('tbody tr')).toHaveCount(6);
    await commitAll(page);
    const w = (await writes(page)).filter((x) => x.method === 'POST' && x.path.endsWith('/commits'));
    const body = (JSON.parse(w.at(-1)!.body) as { config: { sites: { siteSlug: string; config: { devTools?: { presetSlug: string }[] } }[] } }).config;
    expect(body.sites.find((s) => s.siteSlug === 'db')!.config.devTools!.map((p) => p.presetSlug)).toEqual(['http', 'postgres', 'sqlite', 'cassandra', 'redis', 'mqtt']);
    expect(Object.keys(body.sites.find((s) => s.siteSlug === 'app')!.config)).not.toContain('devTools');
    await page.reload();
    await settled(page);
    await expect(page.getByTestId('devtools-table').locator('tbody tr')).toHaveCount(6);
    await expect(page.getByTestId('devtool-target-http')).toHaveText('https://api.example.com');
  });

  test('validation: missing container, static value = warning only (§21 #103), unknown secret; fixing it', async ({ page }) => {
    await login(page, 'admin', `${TPL}/sites/db/devtools`);
    await settled(page);
    // a stored preset the editor cannot produce: ghost container + a literal password
    await patchMockDb(
      page,
      `const s = db.templates['${SEED.template}'].sites.find((x) => x.siteSlug === 'db');
       s.config.devTools = [{ presetSlug: 'pg', name: 'PG', description: '', kind: 'postgres', target: { kind: 'container', containerSlug: 'ghost', portName: 'pg' },
         auth: { username: 'postgres', password: { kind: 'literal', value: 'hunter2' }, database: null, clientId: null, httpScheme: 'none', token: { kind: 'none' }, apiKeyHeader: null },
         options: { tls: false, tlsInsecure: false, tlsServerName: null, sslMode: null, timeoutSec: null, maxRows: null, consistency: null, scheme: null, basePath: null, followRedirects: null, defaultHeaders: [], cleanSession: null, keepAliveSec: null }, readOnly: false }];`,
    );
    await settled(page);
    const issues = page.getByTestId('devtool-issue-pg');
    await expect(issues).toContainText(['container ghost does not exist on site db', 'stored in plain text in the config — anyone who can read the config sees it; use a secret for production']);
    await expect(page.getByTestId('devtool-errors-pg')).toHaveText('1');
    await expect(page.getByTestId('tree-db-devtools').locator('.nav-cnt.red')).toHaveText('1');
    await expect(page.getByTestId('site-issues')).toContainText('V_DEVTOOL_CONTAINER');

    await page.getByTestId('dt-edit-pg').click();
    const d = page.getByTestId('devtool-drawer');
    await expect(d.getByTestId('dt-summary')).toContainText('container ghost does not exist');
    // §21 #103: the stored literal is the "Value" mode — masked, with a show toggle and the plain-text warning
    await expect(d.getByTestId('dt-password')).toHaveAttribute('data-mode', 'literal');
    await expect(d.getByTestId('dt-password-mode').getByRole('radio')).toHaveText(['None', 'Value', 'Secret', 'Expression']);
    await expect(d.getByTestId('dt-password-literal')).toHaveAttribute('type', 'password');
    await expect(d.getByTestId('dt-password-literal')).toHaveValue('hunter2');
    await expect(d.getByTestId('dt-password-literal-warning')).toHaveText('Stored in plain text in the config — anyone who can read the config sees it; use a secret for production.');
    await expect(d.getByTestId('dt-save')).toBeDisabled(); // the ghost container (an error), not the value
    await expect(d.getByTestId('dt-summary')).not.toContainText('password');
    // an expression with an unknown secret
    await d.getByTestId('dt-password-mode-expr').click();
    await d.getByTestId('dt-password-expr-input').fill('{{ secret.nope }}');
    await expect(d.getByTestId('dt-summary')).toContainText('secret nope does not exist');
    // fix: the secret picker + an existing container (port preselected)
    await d.getByTestId('dt-password-mode-secret').click();
    await pickCombo(page, 'dt-password-secret', 'api-token');
    await pick(page, 'dt-container', 'redis');
    await expect(d.getByTestId('dt-port')).toHaveAttribute('data-value', 'tcp');
    await expect(d.getByTestId('dt-summary')).toHaveCount(0);
    await d.getByTestId('dt-save').click();
    await expect(d).toHaveCount(0);
    await expect(page.getByTestId('devtool-errors-pg')).toHaveCount(0);
    await expect(page.getByTestId('devtool-auth-pg')).toHaveText('user postgres · password: secret api-token');
  });

  test('deployment: edit, duplicate, delete all → saved site has NO devTools key; diff shows Dev tools', async ({ page }) => {
    await login(page, 'admin', `${DEP}/sites/db/devtools`);
    await settled(page);
    // edit
    await page.getByTestId('dt-edit-redis').click();
    let d = page.getByTestId('devtool-drawer');
    await d.getByTestId('dt-name').fill('Redis (cache)');
    await d.getByTestId('dt-readonly').click();
    await d.getByTestId('dt-save').click();
    await expect(page.getByTestId('devtool-row-redis')).toContainText('Redis (cache)');
    await expect(page.getByTestId('devtool-ro-redis')).toHaveCount(0);
    await expect(page.getByTestId('dirty-dot')).toBeVisible();
    // duplicate → new preset (slug editable, prefilled)
    await page.getByTestId('dt-dup-postgres').click();
    d = page.getByTestId('devtool-drawer');
    await expect(d.getByTestId('dt-slug')).toHaveValue('postgres-copy');
    await expect(d.getByTestId('dt-name')).toHaveValue('Plant database (copy)');
    await d.getByTestId('dt-slug').fill('redis');
    await expect(d.getByTestId('dt-summary')).toContainText('site db already has a preset redis');
    await d.getByTestId('dt-slug').fill('pg-reporting');
    await d.getByTestId('dt-save').click();
    await expect(page.getByTestId('devtool-row-pg-reporting')).toContainText('Plant database (copy)');
    // §21 #105 Compare · Not committed: the preset changes (dev tool rows of site db), then ONE commit
    await nav(page, `${DEP}/compare?review=working`);
    await expect(page.getByTestId('merge-item-sites/db/devtools/pg-reporting')).toHaveAttribute('data-effect', 'add');
    await expect(page.getByTestId('merge-item-sites/db/devtools/redis')).toContainText('dev tool redis');
    await settled(page);
    await page.screenshot({ path: `${SHOTS}/devtools-presets-diff.png` });
    await commitAll(page);
    const dbOf = (b: string) => (JSON.parse(b) as { config: { sites: { siteSlug: string; config: { devTools?: { presetSlug: string }[] } }[] } }).config.sites.find((x) => x.siteSlug === 'db')!.config;
    let put = (await writes(page)).filter((x) => x.method === 'POST' && x.path.endsWith('/commits')).at(-1)!;
    expect(dbOf(put.body).devTools!.map((p) => p.presetSlug)).toEqual(['postgres', 'redis', 'pg-reporting']);

    // delete all three (confirm) → the site PUT carries no devTools key
    await nav(page, `${DEP}/sites/db/devtools`);
    await settled(page);
    for (const s of ['postgres', 'redis', 'pg-reporting']) {
      await page.getByTestId(`dt-delete-${s}`).click();
      await expect(page.getByTestId('delete-devtool')).toContainText('removed from the site config');
      await page.getByTestId('confirm-ok').click();
      await expect(page.getByTestId(`devtool-row-${s}`)).toHaveCount(0);
    }
    await expect(page.getByTestId('empty-state')).toContainText('No dev tool presets');
    await commitAll(page);
    put = (await writes(page)).filter((x) => x.method === 'POST' && x.path.endsWith('/commits')).at(-1)!;
    expect(Object.keys(dbOf(put.body))).not.toContain('devTools');
    await page.reload();
    await settled(page);
    await expect(page.getByTestId('empty-state')).toContainText('No dev tool presets');
    await expect(page.getByTestId('dirty-dot')).toHaveCount(0);
  });

  test('compare with the template lists a template preset under the site\'s dev tools', async ({ page }) => {
    await login(page, 'admin', `${DEP}/compare`);
    await patchMockDb(
      page,
      `const t = db.templates['${SEED.template}'];
       t.version += 1;
       t.sites.find((x) => x.siteSlug === 'app').config.devTools = [{ presetSlug: 'api', name: 'Hello API', description: '', kind: 'http', target: { kind: 'container', containerSlug: 'hello-api', portName: 'http' },
         auth: { username: null, password: { kind: 'none' }, database: null, clientId: null, httpScheme: 'bearer', token: { kind: 'expr', expr: '{{ secret.api-token }}' }, apiKeyHeader: null },
         options: { tls: false, tlsInsecure: false, tlsServerName: null, sslMode: null, timeoutSec: null, maxRows: null, consistency: null, scheme: null, basePath: '/api', followRedirects: null, defaultHeaders: [], cleanSession: null, keepAliveSec: null }, readOnly: false }];`,
    );
    // §21 #105: a commit on the template (its config from the patched copy); Compare (ACME ← its origin) lists the preset
    await page.reload();
    await page.waitForFunction(() => typeof (window as unknown as { __opmMockFetch?: unknown }).__opmMockFetch === 'function');
    await page.evaluate(async (tid) => {
      const f = (window as unknown as { __opmMockFetch: typeof fetch }).__opmMockFetch;
      const h = { Authorization: 'Bearer mock:uAdmin00000000000001', 'Content-Type': 'application/json' };
      const t = await (await f(`${location.origin}/v1/templates/${tid}`, { headers: h })).json();
      const config = { variables: t.variables, files: t.files, sites: t.sites, secrets: t.secrets.map(({ value: _v, rotatedAt: _r, ...s }: Record<string, unknown>) => s) };
      const r = await f(`${location.origin}/v1/templates/${tid}/commits`, { method: 'POST', headers: h, body: JSON.stringify({ message: 'Hello API preset', baseCommitId: t.branch.head.commitId, config }) });
      if (!r.ok) throw new Error(await r.text());
      await new Promise((ok) => setTimeout(ok, 150));
    }, SEED.template);
    await page.reload();
    const row = page.getByTestId('merge-item-sites/app/devtools/api');
    await expect(row).toHaveAttribute('data-kind', 'theirs');
    await expect(row).toContainText('dev tool api');
    await page.getByTestId('merge-expand-sites/app/devtools/api').click();
    await expect(page.getByTestId('merge-sides-sites/app/devtools/api')).toContainText('Hello API');
  });

  test('read-only account: view only (no add / edit / delete), drawer disabled', async ({ page }) => {
    await login(page, 'viewer', `${DEP}/sites/db/devtools`);
    await settled(page);
    await expect(page.getByTestId('devtool-row-postgres')).toBeVisible();
    await expect(page.getByTestId('add-devtool')).toHaveCount(0);
    await expect(page.getByTestId('dt-edit-postgres')).toHaveCount(0);
    await expect(page.getByTestId('dt-delete-postgres')).toHaveCount(0);
    await page.getByTestId('dt-view-postgres').click();
    const d = page.getByTestId('devtool-drawer');
    await expect(d.getByTestId('dt-name')).toBeDisabled();
    await expect(d.getByTestId('dt-save')).toHaveCount(0);
    await d.getByTestId('dt-cancel').click();
    await expect(d).toHaveCount(0);
  });

  test('publish dialog warns per site whose agent is older than 0.11.0', async ({ page }) => {
    await login(page, 'admin', `${DEP}/diff`);
    await expect(page.getByTestId('working-checks')).toBeVisible();
    await expect(page.getByTestId('devtools-agent-banner')).toHaveCount(1);
    await nav(page, `${DEP}/variables`);
    await page.getByTestId('val-greeting').fill('Hi');
    await page.getByTestId('change-bar-commit').click();
    const m = page.getByTestId('commit-dialog');
    const dw = m.getByTestId('commit-warning').filter({ hasText: 'dev tool presets' });
    await expect(dw).toHaveCount(1);
    await expect(dw).toContainText('Site db runs agent 0.9.0-10080530; dev tool presets need agent 0.11.0 or newer — upgrade the agent first, otherwise the site refuses the revision.');
    await m.getByTestId('commit-cancel').click();
    // an upgraded agent → no warning
    await page.evaluate((dep) => (window as unknown as { __opmMockAgent: (d: string, s: string, p: Record<string, unknown>) => boolean }).__opmMockAgent(dep, 'db', { agentVersion: '0.11.0-10090000' }), SEED.deployment);
    await page.goto(`${DEP}/compare?review=working`);
    await expect(page.getByTestId('working-checks')).toBeVisible();
    await expect(page.getByTestId('devtools-agent-banner')).toHaveCount(0);
    await page.getByTestId('pending-banner-restore').click();
    await page.getByTestId('change-bar-commit').click();
    await expect(page.getByTestId('commit-dialog')).toBeVisible();
    await expect(page.getByTestId('commit-warning').filter({ hasText: 'dev tool presets' })).toHaveCount(0);
  });
});
