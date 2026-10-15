/**
 * Dev tools (SPEC §7.6 / §21 #97) against the in-browser mock: navigation (DEV TOOLS section, "*" → choose a site,
 * switched off), connection manager (create / test / reveal / edit / delete, duplicate a preset), SQL workbench (run,
 * selection, multi-statement, explain, paging, export, cancel, destructive confirm, read-only, inline edit, error
 * position, schema + autocomplete), Redis (browse, edit a string, console history, FLUSHDB confirm), MQTT (live,
 * subscribe, publish, retained), HTTP (env var, send, response tabs, save), History.
 * Screenshots → screenshots/devtools-*.png.
 */
import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { knobs, login, nav, open, pickOption, setEditor, shot } from './helpers';

const runSql = async (page: Page) => page.getByTestId('sql-run').click();

test('navigation: DEV TOOLS section, "*" asks for a site, postgres alias, switched off explains how to enable', async ({ page }) => {
  await login(page, '/_opm/app/*/devtools/sql');
  await expect(page.getByTestId('choose-site')).toContainText('Choose a site to see SQL');
  await page.getByTestId('choose-this-site').click();
  await expect(page).toHaveURL(/\/_opm\/app\/app\/devtools\/sql\?c=/);
  // Sites tree: site › Dev tools › the tools (the node of the page is open)
  await expect(page.getByTestId('tree-toggle-app-devtools')).toHaveAttribute('aria-expanded', 'true');
  await expect(page.getByTestId('tree-site-app').locator('[data-testid^="tree-app-devtools-"]')).toHaveText(['SQL', 'Redis', 'MQTT', 'HTTP client', 'History']);
  await expect(page.getByTestId('tree-app-devtools-sql')).toHaveAttribute('aria-current', 'page');
  await expect(page.getByTestId('breadcrumb')).toContainText('Dev tools');
  await expect(page.getByTestId('conn-select')).toContainText('App database');
  // devtools/sqlite = SQL workbench on a SQLite connection
  await page.goto('/_opm/app/app/devtools/sqlite');
  await page.getByTestId('actor-email').fill('tester@acme.example');
  await page.getByTestId('actor-continue').click();
  await expect(page).toHaveURL(/devtools\/sql\?c=c_01J9DEVSQLITE/);
  await expect(page.getByTestId('conn-select')).toContainText('API app.db');
});

test('switched off (agent.json tools.devTools false) → how to enable, link to the Agent setting', async ({ page }) => {
  await knobs(page, { 'opm.mock.devtoolsOff': '1' });
  await login(page, '/_opm/app/app/devtools');
  await expect(page.getByTestId('devtools-disabled')).toBeVisible();
  await expect(page.getByTestId('devtools-enable-json')).toHaveText('"tools": { "devTools": true }');
  await expect(page.getByTestId('devtools-config-path')).toHaveText('/var/lib/opm/agent.json');
  await shot(page, 'devtools-disabled-light');
  await page.getByTestId('devtools-agent-link').click();
  await expect(page.getByTestId('set-devTools')).not.toBeChecked();
});

test('connections: presets + manual, create with a stored password (test, reveal), edit, delete, duplicate a preset', async ({ page }) => {
  await login(page, '/_opm/app/app/devtools');
  await expect(page.getByTestId('conn-row-p:app-db')).toContainText('preset · from revision seq');
  await expect(page.getByTestId('conn-problem-p:reports')).toContainText('reports-db');
  await shot(page, 'devtools-connections-light');
  // create
  await pickOption(page, 'add-conn', 'postgres');
  const d = page.getByTestId('conn-drawer');
  await expect(d).toBeVisible();
  await d.getByTestId('dt-name').fill('Billing DB');
  await d.getByTestId('dt-target-host').click();
  await d.getByTestId('dt-host').fill('10.0.3.7');
  await d.getByTestId('dt-username-input').fill('billing');
  await d.getByTestId('dt-password-mode-literal').click();
  await d.getByTestId('dt-password-literal').fill('S3cret-billing');
  await d.getByTestId('conn-test').click();
  await expect(d.getByTestId('conn-test-result')).toContainText('Connected');
  await shot(page, 'devtools-connection-drawer-light', false);
  await d.getByTestId('conn-save').click();
  await expect(d).toBeHidden();
  const row = page.locator('[data-testid^="conn-row-c_"]', { hasText: 'Billing DB' });
  await expect(row).toBeVisible();
  // edit: the literal is masked; View reveals it for 60 s (logged)
  await row.locator('[data-testid^="conn-edit-"]').click();
  await expect(d.getByTestId('dt-password-reveal')).toHaveAttribute('data-state', 'masked');
  await d.getByTestId('dt-password-reveal-view').click();
  await expect(d.getByTestId('dt-password-reveal-value')).toHaveText('S3cret-billing');
  await d.getByTestId('dt-name').fill('Billing DB (ro)');
  await d.getByTestId('dt-readonly').check();
  await d.getByTestId('conn-save').click();
  await expect(d).toBeHidden();
  const row2 = page.locator('[data-testid^="conn-row-c_"]', { hasText: 'Billing DB (ro)' });
  await expect(row2).toContainText('read-only');
  // delete
  await row2.locator('[data-testid^="conn-del-c_"]').click();
  await page.getByTestId('conn-del-confirm').getByRole('button', { name: 'Delete' }).click();
  await expect(row2).toBeHidden();
  // duplicate a preset → a manual copy opens in the drawer
  await page.getByTestId('conn-dup-p:app-db').click();
  await expect(d).toBeVisible();
  await expect(d.getByTestId('dt-name')).toHaveValue('App database (local)');
  await expect(d.getByTestId('dt-password')).toHaveAttribute('data-mode', 'secret');
  await d.getByRole('button', { name: 'Cancel' }).click();
  await expect(page.locator('[data-testid^="conn-row-c_"]', { hasText: 'App database (local)' })).toContainText('manual');
});

test('§21 #103 Value mode: manual connection modes in order; a preset static value is masked, revealed below the form, duplicated', async ({ page }) => {
  await login(page, '/_opm/app/app/devtools');
  await pickOption(page, 'add-conn', 'mqtt');
  const d = page.getByTestId('conn-drawer');
  // the default target is the site's mqtt container → Container env is offered too
  await expect(d.getByTestId('dt-password-mode').getByRole('radio')).toHaveText(['None', 'Value', 'Secret', 'Container env', 'Expression']);
  await d.getByTestId('dt-password-mode-literal').click();
  await expect(d.getByTestId('dt-password-literal')).toHaveAttribute('type', 'password');
  await d.getByTestId('dt-password-literal').fill('Typed-Value-1');
  await d.getByTestId('dt-password-show').click();
  await expect(d.getByTestId('dt-password-literal')).toHaveAttribute('type', 'text');
  // a manual connection keeps the value on the agent: no plain-text-in-the-config warning
  await expect(d.getByTestId('dt-password-literal-warning')).toHaveCount(0);
  await d.getByRole('button', { name: 'Cancel' }).click();
  // the northwind preset p:broker holds a static value (cloud Value mode): masked in the read-only form, Reveal below
  await open(page, '/_opm/app/northwind/devtools');
  await page.getByTestId('conn-view-p:broker').click();
  await expect(d.getByTestId('dt-password')).toHaveAttribute('data-mode', 'literal');
  await expect(d.getByTestId('dt-password-literal-stored')).toContainText('••••••');
  await expect(d.getByTestId('dt-password-literal')).toHaveCount(0);
  await expect(d.getByTestId('preset-password-static')).toContainText('Static password value of this preset');
  await expect(d.getByTestId('dt-password-reveal')).toHaveAttribute('data-state', 'masked');
  await d.getByTestId('dt-password-reveal-view').click();
  await expect(d.getByTestId('dt-password-reveal-value')).toHaveText('northwind-Mqtt-Static-103');
  await shot(page, 'devtools-preset-static-value-light', false);
  await d.getByRole('button', { name: 'Close', exact: true }).last().click();
  // duplicate → a manual connection with the value stored on the agent (masked, revealable)
  await page.getByTestId('conn-dup-p:broker').click();
  await expect(d).toBeVisible();
  await expect(d.getByTestId('dt-password')).toHaveAttribute('data-mode', 'literal');
});

test('SQL: statement under the cursor, selection, run all, error position, schema + autocomplete', async ({ page }) => {
  await login(page, '/_opm/app/app/devtools/sql?c=p:app-db');
  await expect(page.getByTestId('tbl-users')).toBeVisible();
  await setEditor(page, 'sql-editor', 'SELECT 1 AS a;\nSELECT 2 AS b;\nSELECT 3 AS c;');
  // cursor on line 2 → Ctrl/⌘-Enter runs only that statement
  await page.keyboard.press('ControlOrMeta+Home');
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('ControlOrMeta+Enter');
  await expect(page.getByTestId('rtab-0')).toBeVisible();
  await expect(page.getByTestId('rtab-1')).toHaveCount(0);
  await expect(page.getByTestId('grid-0-h-b')).toBeVisible();
  // selection
  await page.keyboard.press('ControlOrMeta+Home');
  await page.keyboard.press('Shift+End');
  await runSql(page);
  await expect(page.getByTestId('grid-0-h-a')).toBeVisible();
  // run all → one result tab per statement
  await page.getByTestId('sql-run-all').click();
  await expect(page.getByTestId('rtab-2')).toBeVisible();
  await page.getByTestId('rtab-2').click();
  await expect(page.getByTestId('grid-2-h-c')).toBeVisible();
  // error with a position → marked in the editor
  await setEditor(page, 'sql-editor', 'SELECT nope FROM users');
  await runSql(page);
  await expect(page.getByTestId('result-error')).toContainText('column "nope" does not exist');
  await expect(page.getByTestId('result-error')).toContainText('42703');
  await expect(page.locator('[data-testid=sql-editor] .cm-err-mark')).toHaveText('nope');
  // schema-aware completion
  await setEditor(page, 'sql-editor', 'SELECT * FROM ');
  await page.keyboard.press('End');
  await page.keyboard.type('use');
  await expect(page.locator('.cm-tooltip-autocomplete')).toContainText('users');
  await page.keyboard.press('Escape');
  // columns of the referenced table rank above keywords (ORDER BY id → the column first, not IDENTITY / OIDS)
  await setEditor(page, 'sql-editor', 'SELECT * FROM users ORDER BY ');
  await page.keyboard.press('End');
  await page.keyboard.type('i');
  const first = page.locator('.cm-tooltip-autocomplete li').first();
  await expect(first).toContainText('id');
  await expect(first).toContainText('users');
  await page.keyboard.press('Escape');
  await setEditor(page, 'sql-editor', 'SELECT * FROM devices WHERE ');
  await page.keyboard.press('End');
  await page.keyboard.type('si');
  await expect(page.locator('.cm-tooltip-autocomplete li').first()).toContainText('site');
  await page.keyboard.press('Escape');
  // table detail drawer
  await page.getByTestId('tbl-devices').hover();
  await page.getByTestId('tbl-details-devices').click();
  await expect(page.getByTestId('td-indexes')).toContainText('devices_site_idx');
  await expect(page.getByTestId('td-ddl')).toContainText('CREATE TABLE public.devices');
  await page.getByTestId('td-select').click();
  await expect(page.getByTestId('grid-0-h-label')).toBeVisible();
  await expect(page.getByTestId('results')).toContainText('40 rows');
});

test('SQL: paging, export link, explain plan, cancel, destructive confirm, read-only refusal', async ({ page }) => {
  await login(page, '/_opm/app/app/devtools/sql?c=p:app-db');
  await setEditor(page, 'sql-editor', 'SELECT * FROM users ORDER BY id');
  await runSql(page);
  await expect(page.getByTestId('result-0')).toContainText('1,000 of 2,500 rows');
  await page.getByTestId('load-more').click();
  await expect(page.getByTestId('result-0')).toContainText('2,000 of 2,500 rows');
  await page.getByTestId('export').click();
  await expect(page.getByTestId('export-csv')).toHaveAttribute('href', /\/devtools\/results\/r_[^/]+\/export\?format=csv$/);
  await shot(page, 'devtools-sql-light', false);
  // explain analyze → plan tree
  await setEditor(page, 'sql-editor', "SELECT * FROM devices WHERE site = 'pune' ORDER BY last_seen LIMIT 5");
  await page.getByTestId('sql-explain-analyze').click();
  await expect(page.getByTestId('plan-tree')).toContainText('Seq Scan');
  await expect(page.getByTestId('plan-tree')).toContainText('Sort');
  await shot(page, 'devtools-explain-light', false);
  await page.getByTestId('plan-view').getByRole('radio', { name: 'Text' }).click();
  await expect(page.getByTestId('plan-text')).toContainText('Execution Time');
  // cancel a long statement
  await setEditor(page, 'sql-editor', 'SELECT pg_sleep(20)');
  await runSql(page);
  await page.getByTestId('sql-cancel').click();
  await expect(page.getByTestId('result-error')).toContainText('canceling statement due to user request');
  // destructive → typed connection name
  await setEditor(page, 'sql-editor', 'DELETE FROM audit.events');
  await runSql(page);
  const c = page.getByTestId('dt-confirm');
  await expect(c).toBeVisible();
  await expect(c.getByTestId('dt-confirm-reasons')).toContainText('DELETE without WHERE');
  await c.getByTestId('dt-confirm-input').fill('app database');
  await expect(c.getByTestId('dt-confirm-go')).toBeDisabled();
  await c.getByTestId('dt-confirm-input').fill('App database');
  await shot(page, 'devtools-destructive-light', false);
  await c.getByTestId('dt-confirm-go').click();
  await expect(page.getByTestId('result-command')).toContainText('DELETE 300');
  // read-only connection: the server refuses writes (BEGIN READ ONLY)
  await pickOption(page, 'conn-select', 'c_01J9DEVANALYTICS0000000001');
  await expect(page.getByTestId('conn-readonly-badge')).toBeVisible();
  await setEditor(page, 'sql-editor', "UPDATE users SET role = 'admin' WHERE id = 3");
  await runSql(page);
  await expect(page.getByTestId('result-error')).toContainText('cannot execute UPDATE in a read-only transaction');
  await expect(page.getByTestId('sql-tx')).toBeDisabled();
});

test('SQL: inline edit of SELECT * generates an UPDATE by primary key', async ({ page }) => {
  await login(page, '/_opm/app/app/devtools/sql?c=p:app-db');
  await setEditor(page, 'sql-editor', 'SELECT * FROM users WHERE id <= 5 ORDER BY id');
  await runSql(page);
  await expect(page.getByTestId('result-0')).toContainText('editable');
  await expect(page.getByTestId('grid-0-h-id')).toContainText('🔑');
  await page.locator('[data-testid=grid-0] .gr[data-row="1"] .cell[data-col="name"]').dblclick();
  await page.getByTestId('grid-0-edit-input').fill("Ravi O'Neil");
  await page.getByTestId('grid-0-edit-ok').click();
  await expect(page.getByTestId('edit-pending')).toHaveText('1 pending');
  await page.getByTestId('edit-save').click();
  await expect(page.getByTestId('edit-sql')).toHaveText("UPDATE public.users SET name = 'Ravi O''Neil' WHERE id = 2;");
  await page.getByTestId('edit-run').click();
  await expect(page.locator('[data-testid=grid-0] .gr[data-row="1"] .cell[data-col="name"]')).toHaveText("Ravi O'Neil");
  await expect(page.getByTestId('edit-pending')).toHaveCount(0);
  // the generated statement is in the history (logged like any query)
  await page.getByTestId('side-tab-history').click();
  await expect(page.getByTestId('history-panel')).toContainText("UPDATE public.users SET name = 'Ravi O''Neil' WHERE id = 2");
  // History page (client-side: the mock state stays)
  await nav(page, 'devtools-history');
  await expect(page.getByTestId('dh-table')).toContainText("UPDATE public.users SET name = 'Ravi O''Neil'");
  await pickOption(page, 'dh-kind', 'redis');
  await expect(page.getByTestId('dh-row').first()).toContainText('Cache');
  await page.getByTestId('dh-ok').getByRole('radio', { name: 'Failed' }).click();
  await expect(page.getByTestId('dh-table')).toContainText('No dev tools activity matches');
  await shot(page, 'devtools-history-light');
});

test('Redis: tree browse, edit a string, TTL, console history, FLUSHDB asks for the connection name', async ({ page }) => {
  await login(page, '/_opm/app/app/devtools/redis');
  await page.getByTestId('rf-user').click();
  await page.getByTestId('rk-user:1').click();
  await expect(page.getByTestId('redis-members')).toContainText('user1@acme.example');
  await page.getByTestId('rf-config').click();
  await page.getByTestId('rk-config:feature-flags').click();
  await expect(page.getByTestId('redis-fmt').getByRole('radio', { name: 'JSON' })).toHaveAttribute('aria-checked', 'true');
  await shot(page, 'devtools-redis-light', false);
  await page.getByTestId('rk-config:maintenance').click();
  await setEditor(page, 'redis-editor', 'on');
  await page.getByTestId('redis-save').click();
  await page.getByTestId('rk-config:feature-flags').click();
  await page.getByTestId('rk-config:maintenance').click();
  await expect(page.getByTestId('redis-editor')).toContainText('on');
  // TTL
  await page.getByTestId('redis-ttl').click();
  await page.getByTestId('redis-ttl-input').fill('120');
  await page.getByTestId('redis-ttl-set').click();
  await expect(page.getByTestId('redis-ttl')).toContainText('TTL 2 m');
  // list view
  await page.getByTestId('redis-view').getByRole('radio', { name: 'List' }).click();
  await expect(page.getByTestId('rl-counter:visits')).toBeVisible();
  // console + history
  await page.getByTestId('redis-tab-console').click();
  const cli = page.getByTestId('redis-cli');
  await cli.fill('SET demo:greeting "hello world"');
  await cli.press('Enter');
  await cli.fill('GET demo:greeting');
  await cli.press('Enter');
  await expect(page.getByTestId('redis-out').last()).toContainText('"hello world"');
  await cli.fill('HGETALL user:3');
  await cli.press('Enter');
  await expect(page.getByTestId('redis-out').last()).toContainText('⇒');
  await cli.press('ArrowUp');
  await expect(cli).toHaveValue('HGETALL user:3');
  await cli.press('ArrowUp');
  await expect(cli).toHaveValue('GET demo:greeting');
  await cli.press('ArrowDown');
  await cli.press('ArrowDown');
  await expect(cli).toHaveValue('');
  await cli.fill('MONITOR');
  await cli.press('Enter');
  await expect(page.getByTestId('redis-out').last()).toContainText('blocked');
  await shot(page, 'devtools-redis-console-light', false);
  await cli.fill('FLUSHDB');
  await cli.press('Enter');
  const c = page.getByTestId('dt-confirm');
  await expect(c.getByTestId('dt-confirm-reasons')).toContainText('FLUSHDB');
  await c.getByTestId('dt-confirm-input').fill('Cache');
  await c.getByTestId('dt-confirm-go').click();
  await expect(page.getByTestId('redis-out').last()).toContainText('OK');
  await cli.fill('DBSIZE');
  await cli.press('Enter');
  await expect(page.getByTestId('redis-out').last()).toContainText('(integer) 0');
  // info
  await page.getByTestId('redis-tab-info').click();
  await expect(page.getByTestId('redis-dbs')).toContainText('this connection');
  await expect(page.getByTestId('redis-slowlog')).toContainText('KEYS *');
});

test('MQTT: live session, subscribe, publish, retained filter, clear a retained message with confirmation', async ({ page }) => {
  await login(page, '/_opm/app/app/devtools/mqtt');
  await expect(page.getByTestId('mqtt-status')).toHaveText('live');
  await expect(page.getByTestId('mqtt-msg').nth(8)).toBeVisible();
  await expect(page.getByTestId('mqtt-tree')).toContainText('northwind');
  await page.getByTestId('mqtt-filter').fill('test/#');
  await page.getByTestId('mqtt-sub').click();
  await expect(page.getByTestId('mqtt-subs')).toContainText('test/#');
  // publish JSON
  await page.getByTestId('mqtt-pub-topic').fill('test/hello');
  await setEditor(page, 'mqtt-pub-payload', '{"hello":"site"}');
  await page.getByTestId('mqtt-pub-go').click();
  await page.getByTestId('mqtt-q').fill('test/hello');
  await expect(page.getByTestId('mqtt-msg').first()).toContainText('{"hello":"site"}');
  await page.getByTestId('mqtt-msg').first().click();
  await expect(page.getByTestId('mqtt-payload')).toContainText('"hello": "site"');
  await page.getByTestId('mqtt-q').fill('');
  // retained only
  await page.getByTestId('mqtt-retained-only').check();
  await expect(page.getByTestId('mqtt-messages')).toContainText('northwind/config/thresholds');
  for (const t of await page.getByTestId('mqtt-msg').allTextContents()) expect(t).toContain('retained');
  await page.getByTestId('mqtt-msg').filter({ hasText: 'northwind/config/thresholds' }).first().click();
  await page.getByTestId('mqtt-view').getByRole('radio', { name: 'Hex' }).click();
  await expect(page.getByTestId('mqtt-payload')).toContainText('7b 22 70 6d');
  await shot(page, 'devtools-mqtt-light', false);
  await page.getByTestId('mqtt-clear-retained').click();
  const c = page.getByTestId('dt-confirm');
  await c.getByTestId('dt-confirm-input').fill('Broker');
  await c.getByTestId('dt-confirm-go').click();
  await expect(page.getByText('retained message cleared')).toBeVisible();
  await page.getByTestId('mqtt-pause').click();
  await expect(page.getByTestId('mqtt-pause')).toContainText('Resume');
  await page.getByTestId('mqtt-stop').click();
  await expect(page.getByTestId('mqtt-status')).toHaveText('idle');
});

test('MQTT: an unexpected end of the stream opens a new session', async ({ page }) => {
  await knobs(page, { 'opm.mock.mqttEndAfter': '6' });
  await login(page, '/_opm/app/app/devtools/mqtt');
  await expect(page.getByTestId('mqtt-status-msg')).toContainText('reconnecting');
  await expect(page.getByTestId('mqtt-status')).toHaveText('live');
});

test('HTTP: environment variable, send, response tabs, curl, save to a collection', async ({ page }) => {
  await login(page, '/_opm/app/app/devtools/http?c=p:api');
  await setEditor(page, 'http-url', '/users/{{userId}}?expand=profile');
  await expect(page.locator('[data-testid=http-url] .cm-ph')).toHaveText('{{userId}}');
  await page.getByTestId('http-rtab-params').click();
  await expect(page.getByTestId('http-params-name-0')).toHaveValue('expand');
  await pickOption(page, 'http-env', 'Local');
  await expect(page.getByTestId('http-resolved')).toContainText('http://hello-api:3000/api/users/7?expand=profile');
  await page.getByTestId('http-send').click();
  await expect(page.getByTestId('http-status')).toContainText('200 OK');
  await expect(page.getByTestId('http-body')).toContainText('"id": 7');
  await page.getByTestId('http-tab-headers').click();
  await expect(page.getByTestId('http-headers')).toContainText('application/json');
  await page.getByTestId('http-tab-timing').click();
  await expect(page.getByTestId('http-timing')).toContainText('Waiting (TTFB)');
  await page.getByTestId('http-tab-sent').click();
  await expect(page.getByTestId('http-sent')).toContainText('GET http://hello-api:3000/api/users/7?expand=profile');
  await expect(page.getByTestId('http-sent')).toContainText('Bearer ••••••');
  await page.getByTestId('http-tab-body').click();
  await shot(page, 'devtools-http-light', false);
  // POST JSON with an env var in the body
  await pickOption(page, 'http-method', 'POST');
  await setEditor(page, 'http-url', '/users');
  await page.getByTestId('http-rtab-body').click();
  await page.getByTestId('http-body-kind').getByRole('radio', { name: 'JSON' }).click();
  await setEditor(page, 'http-body-editor', '{"email":"{{email}}"}');
  await page.getByTestId('http-send').click();
  await expect(page.getByTestId('http-status')).toContainText('201');
  await expect(page.getByTestId('http-body')).toContainText('new.user@acme.example');
  // curl keeps placeholders
  await page.getByTestId('http-curl').click();
  await expect(page.getByTestId('http-curl-text')).toContainText('{{email}}');
  await expect(page.getByTestId('http-curl-text')).toContainText('Authorization: Bearer {{ secret.api-token }}');
  await page.getByTestId('http-curl-modal').getByRole('button', { name: 'Close' }).click();
  // save to a collection
  await page.getByTestId('http-save').click();
  await page.getByTestId('save-name').fill('Create user (env)');
  await page.getByTestId('save-collection').fill('API');
  await page.getByTestId('save-ok').click();
  await expect(page.getByTestId('saved-Create user (env)')).toBeVisible();
  // HTML preview in a sandboxed iframe
  await pickOption(page, 'http-method', 'GET');
  await setEditor(page, 'http-url', '/html');
  await page.getByTestId('http-send').click();
  await expect(page.getByTestId('http-iframe')).toHaveAttribute('sandbox', 'allow-scripts');
});

test('dark: SQL workbench + connections', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'dark' });
  await login(page, '/_opm/app/app/devtools/sql?c=p:app-db');
  await page.getByTestId('tbl-users').hover();
  await page.getByTestId('tbl-select-users').click();
  await expect(page.getByTestId('grid-0-h-email')).toBeVisible();
  await shot(page, 'devtools-sql-dark', false);
  await nav(page, 'devtools-redis');
  await page.getByTestId('redis-tab-console').click();
  await page.getByTestId('redis-cli').fill('HGETALL user:3');
  await page.getByTestId('redis-cli').press('Enter');
  await expect(page.getByTestId('redis-out')).toContainText('⇒');
  await page.getByTestId('rf-user').click();
  await shot(page, 'devtools-redis-dark', false);
  await nav(page, 'devtools');
  await expect(page.getByTestId('conn-table')).toBeVisible();
  await shot(page, 'devtools-connections-dark');
});

test('§21 #99: connection fields complete {{ containers.<c>.env.<NAME> }} (names from devtools info, secrets masked)', async ({ page }) => {
  await login(page, '/_opm/app/app/devtools');
  await pickOption(page, 'add-conn', 'postgres');
  const d = page.getByTestId('conn-drawer');
  await expect(d).toBeVisible();
  const user = d.getByTestId('dt-username-input');
  await user.click();
  await user.pressSequentially('{{ containers.postgres.env.');
  const sug = page.locator('[data-testid="dt-username-ac"]:popover-open');
  await expect(sug).toContainText('containers.postgres.env.POSTGRES_USER');
  await expect(sug).toContainText('containers.postgres.env.POSTGRES_PASSWORD');
  await user.fill('{{ containers.postgres.env.POSTGRES_USER }}');
  await expect(d.getByTestId('dt-username-preview')).toContainText('(env POSTGRES_USER)');
  await expect(d.getByTestId('dt-username-preview')).not.toContainText('E_');
  await user.fill('{{ containers.postgres.env.POSTGRES_PASSWORD }}');
  await expect(d.getByTestId('dt-username-preview')).toContainText('•');
});

test('§21 #102 / #104: a cloud commit that adds a dev-tool preset shows it in the merge view (dev tools group)', async ({ page }) => {
  await knobs(page, { 'opm.mock.presetProposal': '1', 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/app/branch/merge');
  const item = page.getByTestId('merge-view').getByTestId('merge-item-sites/app/devtools/plant-db');
  await expect(item).toBeVisible();
  await expect(item).toHaveAttribute('data-effect', 'add');
});

test('§21 #102 / #113: variables + secrets open the SAME forms as the cloud (definition and value are local edits)', async ({ page }) => {
  await login(page, '/_opm/app/app/variables');
  await page.getByTestId('var-open-greeting').click();
  const d = page.getByTestId('variable-drawer');
  await expect(d.getByTestId('variable-form')).toBeVisible();
  await expect(d.getByTestId('var-label')).toBeEnabled();
  await expect(d.getByTestId('var-type-string')).toBeEnabled();
  await d.getByTestId('var-value').fill('Hello from the shared form');
  await d.getByTestId('var-save').click();
  await expect(d).not.toBeVisible();
  await expect(page.locator('#var-greeting')).toHaveValue('Hello from the shared form');
  await expect(page.getByTestId('local-edit-bar')).toContainText('1 change (not committed)');
  await shot(page, 'gc-agent-variable-form-light');
  await nav(page, 'secrets');
  await page.getByTestId('sec-open-api-token').click();
  const s = page.getByTestId('secret-drawer');
  await expect(s.getByTestId('secret-form')).toBeVisible();
  await expect(s.getByTestId('sec-label')).toBeEnabled();
  await expect(s.getByTestId('sec-generate')).toBeVisible();
});
