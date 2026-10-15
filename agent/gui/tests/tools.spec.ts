/**
 * Round T toolbox, nav2 (SPEC §21 #97): DOCKER › Containers / Volumes / Docker / Network, the container full page
 * (Overview · Logs · Terminal · Monitoring · Inspect · Processes & events · Config), host monitoring on Status, the host
 * shell in Agent, the edge access log in Network. Mock agent (lib/mock/tools.ts).
 * Screenshots → screenshots/tools-<page>-{light,dark}.png (390 px: tools-mobile.spec.ts).
 */
import { test, expect } from './fixtures';
import { knobs, login, open, pickOption, shot } from './helpers';

test('Sites tree links each site Containers / Volumes / Server › Docker · Network; "*" merges containers', async ({ page }) => {
  await login(page, '/_opm/app/app/status');
  await expect(page.getByTestId('tree-app-containers')).toHaveAttribute('href', '/_opm/app/app/containers');
  await expect(page.getByTestId('tree-app-volumes')).toHaveAttribute('href', '/_opm/app/app/volumes');
  await page.getByTestId('tree-toggle-app-server').click();
  await expect(page.getByTestId('tree-app-docker')).toHaveAttribute('href', '/_opm/app/app/docker');
  await expect(page.getByTestId('tree-app-network')).toHaveAttribute('href', '/_opm/app/app/network');
  // "*" (All sites) still merges the containers of every site
  await open(page, '/_opm/app/*/containers');
  await expect(page.getByTestId('tools-containers')).toContainText('redis');
  // Network needs one site: the * empty state with an inline chooser
  await open(page, '/_opm/app/*/network');
  await expect(page.getByTestId('choose-site')).toContainText('Choose a site to see network checks');
  await pickOption(page, 'choose-site-select', 'db');
  await page.waitForURL(/\/app\/db\/network$/);
  await expect(page.getByTestId('tree-db-network')).toHaveAttribute('aria-current', 'page');
});

test('containers: stop warns about the health system, then "stopped manually"; a row opens the full container page', async ({ page }) => {
  await login(page, '/_opm/app/app/containers');
  await expect(page.getByTestId('state-hello-api-2')).toHaveText('stopped manually');
  await expect(page.getByTestId('manual-hello-api-2')).toContainText('ravi@acme.example');
  await page.getByTestId('row-hello-web').getByTestId('qa-stop').click();
  await expect(page.getByTestId('expected-running-warning')).toContainText('health system expects this container running');
  await page.getByTestId('container-action-confirm').getByRole('button', { name: 'Stop' }).click();
  await expect(page.getByTestId('state-hello-web')).toHaveText('stopped manually');
  await expect(page.getByTestId('manual-hello-web')).toContainText('tester@acme.example');
  // failed copy: read-only
  await expect(page.getByTestId('row-hello-api-failed-1759880000').getByTestId('qa-restart')).toHaveCount(0);
  // a row opens a PAGE (no drawer)
  await page.getByTestId('open-hello-api').click();
  await page.waitForURL(/\/app\/app\/containers\/hello-api$/);
  await expect(page.getByTestId('inspect-drawer')).toHaveCount(0);
  await expect(page.getByTestId('page-title')).toHaveText('hello-api');
  await expect(page.getByTestId('breadcrumb')).toContainText('Containers');
  await expect(page.getByTestId('health-history')).toBeVisible();
  await expect(page.getByTestId('env-table')).toContainText('API_TOKEN');
  await expect(page.getByTestId('env-table')).toContainText('••••••');
  await expect(page.getByTestId('c-mounts')).toContainText('/data');
  await expect(page.getByTestId('c-start-order')).toContainText('of 3');
  await page.getByTestId('ctab-processes').click();
  await expect(page).toHaveURL(/\?tab=processes$/);
  await expect(page.getByTestId('top-table')).toContainText('node dist/server.js');
  await expect(page.getByTestId('events-list')).toContainText('restart');
  await page.getByTestId('ctab-inspect').click();
  await expect(page.getByTestId('inspect-json')).toContainText('HostConfig');
  await page.getByTestId('inspect-json').getByRole('button', { name: /HostConfig/ }).click();
  await expect(page.getByTestId('inspect-json')).toContainText('RestartPolicy');
  await shot(page, 'tools-inspect-light', false);
  // keyboard: ← / → move between the tabs
  await page.getByTestId('ctab-inspect').focus();
  await page.keyboard.press('ArrowRight');
  await expect(page).toHaveURL(/\?tab=processes$/);
  await expect(page.getByTestId('ctab-processes')).toBeFocused();
});

test('container Logs tab: follow appends lines, search highlights + counts, download; merge with another container', async ({ page }) => {
  await login(page, '/_opm/app/app/containers/hello-api?tab=logs');
  const lines = page.getByTestId('log-lines');
  await expect(lines).toContainText('GET /api/counter');
  await expect(page.getByTestId('log-state')).toHaveText('live');
  const n0 = await lines.locator('.ln').count();
  await expect.poll(async () => lines.locator('.ln').count(), { timeout: 5000 }).toBeGreaterThan(n0);
  await page.getByTestId('log-search').fill('timeout');
  await expect(page.getByTestId('log-matches')).toContainText('/');
  await expect(lines.locator('mark').first()).toHaveText('timeout');
  const dl = page.waitForEvent('download');
  await page.getByTestId('log-download').click();
  expect((await dl).suggestedFilename()).toMatch(/^opm-app-logs-.*\.log$/);
  // container-scoped: only this container until "Merge with…"
  await expect(page.getByTestId('log-containers')).toContainText('hello-api');
  await expect(lines.locator('.tag')).toHaveCount(0);
  await pickOption(page, 'log-merge', 'hello-web');
  await expect(lines.locator('.tag').first()).toBeVisible();
});

test('edge access log lives in Network; the agent log in Debug', async ({ page }) => {
  await login(page, '/_opm/app/app/network');
  await page.getByTestId('net-tabs-access').click();
  await expect(page.getByTestId('access-log')).toContainText('/api/counter');
  await expect(page.getByTestId('access-log')).toContainText('502');
  await open(page, '/_opm/app/app/debug?tab=agent');
  await expect(page.getByTestId('log-lines')).toBeVisible();
  await expect(page.getByTestId('log-level')).toBeVisible();
  await open(page, '/_opm/app/*/debug?tab=agent');
  await expect(page.getByTestId('choose-site')).toContainText('the live agent log');
});

test('container Terminal tab echoes; the host shell is Agent › Host shell (typed site name)', async ({ page }) => {
  await login(page, '/_opm/app/app/containers/hello-api?tab=terminal');
  await expect(page.getByTestId('term-container')).toHaveText('hello-api');
  await page.getByTestId('term-open').click();
  await expect(page.getByTestId('term-state')).toHaveText('connected');
  await page.locator('[data-testid="xterm"] textarea').focus();
  await page.keyboard.type('echo hi');
  await page.keyboard.press('Enter');
  await expect(page.getByTestId('xterm')).toContainText('hi');
  await expect(page.getByTestId('term-sessions')).toContainText('hello-api');
  await shot(page, 'tools-terminal-light', false);
  // transcripts of this container only
  await expect(page.getByTestId('transcripts')).not.toContainText('host (root)');
  await page.getByTestId('transcript-view').first().click();
  await expect(page.getByTestId('transcript-text')).toContainText('Script');
  await page.keyboard.press('Escape');
  // host shell
  await open(page, '/_opm/app/app/agent?tab=shell');
  await page.getByTestId('term-open-host').click();
  const m = page.getByTestId('host-confirm');
  await expect(m).toContainText('Runs as root on app');
  await expect(page.getByTestId('host-go')).toBeDisabled();
  await page.getByTestId('host-typed').fill('db');
  await expect(page.getByTestId('host-go')).toBeDisabled();
  await page.getByTestId('host-typed').fill('app');
  await shot(page, 'tools-terminal-host-confirm-light', false);
  await page.getByTestId('host-go').click();
  await expect(page.getByTestId('term-tab')).toHaveCount(1);
  await expect(page.getByTestId('term-state')).toHaveText('connected');
  await page.locator('[data-testid="terminal-pane"]:visible [data-testid="xterm"] textarea').focus();
  await page.keyboard.type('exit');
  await page.keyboard.press('Enter');
  await expect(page.getByTestId('term-state')).toHaveText('ended');
  await expect(page.getByTestId('transcripts')).toContainText('host (root)');
});

test('terminal: a disabled host shell is explained (agent.json)', async ({ page }) => {
  await knobs(page, { 'opm.mock.noHostShell': '1' });
  await login(page, '/_opm/app/app/agent?tab=shell');
  await expect(page.getByTestId('tool-disabled')).toContainText('"hostShell": true');
  await expect(page.getByTestId('term-open-host')).toBeDisabled();
  await shot(page, 'tools-terminal-disabled-light', false);
});

test('monitoring: host cards + alerts on Status (drawer range switch); container charts on the Monitoring tab', async ({ page }) => {
  await login(page, '/_opm/app/app/status');
  await expect(page.getByTestId('host-cards')).toBeVisible();
  await expect(page.getByTestId('metric-alerts')).toContainText('Disk / 94 % full');
  await expect(page.getByTestId('disk-root')).toContainText('> 90 %');
  await page.getByTestId('host-cpu').click();
  await expect(page.getByTestId('metrics-drawer').getByTestId('metrics-charts')).toHaveAttribute('data-range', '1h');
  await page.getByTestId('metrics-range').getByRole('radio', { name: '7 d' }).click();
  await expect(page.getByTestId('metrics-charts')).toHaveAttribute('data-range', '7d');
  await page.keyboard.press('Escape');
  await open(page, '/_opm/app/app/containers/hello-web?tab=monitoring');
  await expect(page.getByTestId('metrics-panel')).toBeVisible();
  await expect(page.getByTestId('metrics-charts')).toHaveAttribute('data-range', '1h');
  await page.getByTestId('metrics-range').getByRole('radio', { name: '24 h' }).click();
  await expect(page.getByTestId('metrics-charts')).toHaveAttribute('data-range', '24h');
  await page.mouse.move(1000, 360);
  await shot(page, 'tools-monitoring-drawer-light', false);
});

test('volumes: browse, view a text file, binary notice, download links', async ({ page }) => {
  await login(page, '/_opm/app/app/volumes?tab=files');
  await expect(page.getByTestId('vol-entries')).toContainText('worker.ts');
  await page.getByTestId('entry-archive').click();
  await expect(page.getByTestId('vol-path')).toContainText('archive');
  await page.getByTestId('entry-dump.bin').click();
  await expect(page.getByTestId('file-view')).toContainText('Binary file');
  await page.keyboard.press('Escape');
  await page.getByTestId('vol-path').getByRole('button', { name: 'api-env' }).click();
  await page.getByTestId('entry-worker.ts').click();
  await expect(page.getByTestId('file-text')).toContainText("queue: 'jobs'");
  const dl = page.waitForEvent('download');
  await page.getByTestId('file-download').click();
  expect((await dl).suggestedFilename()).toBe('worker.ts');
  await page.keyboard.press('Escape');
  const dl2 = page.waitForEvent('download');
  await page.getByTestId('vol-download-dir').click();
  expect((await dl2).suggestedFilename()).toBe('api-env.tar.gz');
});

test('network: http + dns results inline; tcp format error stays on the field', async ({ page }) => {
  await login(page, '/_opm/app/app/network');
  await page.getByTestId('nc-target').fill('http://hello-api:3000/health');
  await page.getByTestId('nc-run').click();
  await expect(page.getByTestId('nc-result').first()).toContainText('200 OK');
  await pickOption(page, 'nc-from', 'hello-api');
  await page.getByTestId('nc-check').getByRole('radio', { name: 'DNS' }).click();
  await page.getByTestId('nc-target').fill('nope.invalid');
  await page.getByTestId('nc-run').click();
  await expect(page.getByTestId('nc-result').first()).toContainText('no such host');
  await expect(page.getByTestId('nc-result').first()).toContainText('getent');
  await page.getByTestId('nc-check').getByRole('radio', { name: 'TCP' }).click();
  await page.getByTestId('nc-target').fill('redis.db.opm');
  await page.getByTestId('nc-run').click();
  await expect(page.locator('.field-err')).toContainText('host:port');
  await expect(page.getByTestId('nc-result')).toHaveCount(2);
});

test('docker: df + images; prune needs PRUNE and shows the result', async ({ page }) => {
  await login(page, '/_opm/app/app/docker');
  await expect(page.getByTestId('docker-df')).toContainText('reclaimable');
  await expect(page.getByTestId('images')).toContainText('busybox:1.36');
  await page.getByTestId('prune-open').click();
  const c = page.getByTestId('prune-confirm');
  await expect(c.getByRole('button', { name: 'Prune' })).toBeDisabled();
  await c.getByRole('textbox').fill('PRUNE');
  await c.getByRole('button', { name: 'Prune' }).click();
  await expect(page.getByTestId('prune-result')).toContainText('3 images removed');
  await expect(page.getByTestId('images')).not.toContainText('busybox:1.36');
});

test('status: container rows have quick actions into the tabs of the container page', async ({ page }) => {
  await login(page, '/_opm/app/app/status');
  const qa = page.getByTestId('qa-hello-web');
  await expect(qa.getByTestId('qa-logs')).toHaveAttribute('href', '/_opm/app/app/containers/hello-web?tab=logs');
  await expect(qa.getByTestId('qa-terminal')).toHaveAttribute('href', '/_opm/app/app/containers/hello-web?tab=terminal');
  await expect(qa.getByTestId('qa-monitoring')).toHaveAttribute('href', '/_opm/app/app/containers/hello-web?tab=monitoring');
  await qa.getByTestId('qa-restart').click();
  await page.getByTestId('container-action-confirm').getByRole('button', { name: 'Restart' }).click();
  await expect(page.getByText('hello-web restarted')).toBeVisible();
  await expect(page.getByTestId('status-open-tools')).toHaveAttribute('href', '/_opm/app/app/containers');
  await page.getByTestId('status-name-hello-web').getByRole('link').click();
  await page.waitForURL(/\/app\/app\/containers\/hello-web$/);
});

// screenshots of every tools page, light + dark
const SHOTS: [string, string, string][] = [
  ['logs', '/_opm/app/app/containers/hello-api?tab=logs', 'log-lines'],
  ['container', '/_opm/app/app/containers/hello-api', 'health-history'],
  ['terminal', '/_opm/app/app/containers/hello-api?tab=terminal', 'term-new'],
  ['monitoring', '/_opm/app/app/containers/hello-api?tab=monitoring', 'metrics-charts'],
  ['host', '/_opm/app/app/status', 'host-cards'],
  ['containers', '/_opm/app/app/containers', 'tools-containers'],
  ['volumes', '/_opm/app/app/volumes?tab=files', 'vol-entries'],
  ['network', '/_opm/app/app/network', 'nc-target'],
  ['docker', '/_opm/app/app/docker', 'images'],
];
for (const scheme of ['light', 'dark'] as const) {
  test(`screenshots: every tools page (${scheme})`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: scheme });
    await login(page, SHOTS[0][1]);
    for (const [name, path, tid] of SHOTS) {
      if (name !== 'logs') await open(page, path);
      await expect(page.getByTestId(tid)).toBeVisible();
      if (name === 'network') {
        await page.getByTestId('nc-target').fill('http://hello-api:3000/health');
        await page.getByTestId('nc-run').click();
        await expect(page.getByTestId('nc-result')).toBeVisible();
      }
      await shot(page, `tools-${name}-${scheme}`, false);
    }
  });
}
