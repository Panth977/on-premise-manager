/**
 * §21 #125 Tunnels page: sidebar entry after Status, the agent GUI tunnel of every site (Open, never removed), port
 * tunnels (seeded edge http of app on 2000) + add a container port / edge https / server port tunnel through the dialog,
 * remove with a confirm, a port taken meanwhile (409), the unavailable banner, read-only users, 390 px.
 * Mock: host opm.example.com, free ports 2000-2010; localStorage['opm.mock.tunnels'] = 'unavailable' → not a VM cloud.
 */
import { test, expect, type Page } from '@playwright/test';
import { login, expectToast, pickCombo, SEED } from './helpers';

const D = `/app/deployments/${SEED.deployment}`;
const URL_T = `${D}/tunnels`;
const row = (page: Page, port: number) => page.locator(`[data-testid=tunnel-row][data-port="${port}"]`);

async function pick(page: Page, testid: string, value: string) {
  await page.getByTestId(testid).click();
  await page.getByTestId(`${testid}-opt-${value}`).click();
}
async function openAdd(page: Page) {
  await page.getByTestId('add-tunnel').click();
  await expect(page.getByTestId('add-tunnel-modal')).toBeVisible();
}

test.describe('tunnels (§21 #125)', () => {
  test('sidebar: Tunnels right after Status, navigates', async ({ page }) => {
    await login(page, 'admin', D);
    const nav = page.getByTestId('nav-tunnels');
    await expect(nav).toBeVisible();
    // the entry right after Status
    const labels = await nav.locator('xpath=..').locator('a').allInnerTexts();
    const i = labels.findIndex((l) => l.trim() === 'Status');
    expect(i).toBeGreaterThanOrEqual(0);
    expect(labels[i + 1]?.trim()).toBe('Tunnels');
    await nav.click();
    await page.waitForURL((u) => u.pathname === URL_T);
    await expect(page.getByTestId('page-title')).toHaveText('Tunnels');
  });

  test('agent GUI rows: one per site, Open, no delete', async ({ page }) => {
    await login(page, 'admin', URL_T);
    const app = page.getByTestId('agent-row-app');
    const db = page.getByTestId('agent-row-db');
    await expect(app).toBeVisible();
    await expect(db).toBeVisible();
    await expect(app.getByTestId('agent-state')).toHaveText('on');
    await expect(db.getByTestId('agent-state')).toHaveText('off on the site');
    await expect(page.getByTestId('agent-tunnels').getByTestId('tunnel-delete')).toHaveCount(0);
    const popup = page.waitForEvent('popup');
    await app.getByTestId('open-agent-app').click();
    const p = await popup;
    await expect.poll(() => p.url()).toContain('opm-remote-app');
  });

  test('seeded tunnel: address, copy, open link, state', async ({ page }) => {
    await login(page, 'admin', URL_T);
    const r = row(page, 2000);
    await expect(r).toContainText('Plant web');
    await expect(r.getByTestId('tunnel-target')).toHaveText('edge http');
    await expect(r.getByTestId('tunnel-address')).toHaveText('opm.example.com:2000');
    await expect(r.getByTestId('tunnel-copy')).toBeVisible();
    await expect(r.getByTestId('tunnel-open')).toHaveAttribute('href', 'http://opm.example.com:2000');
    await expect(r.getByTestId('tunnel-state')).toHaveAttribute('data-state', 'on');
    await expect(r).toContainText('1 open');
  });

  test('add a container port, an edge https and a server port tunnel; remove one', async ({ page }) => {
    await login(page, 'admin', URL_T);
    await expect(row(page, 2000)).toBeVisible();

    // 1. container port of app on 2001
    await openAdd(page);
    await pick(page, 'tn-site', 'app');
    await page.getByTestId('tn-target').click();
    const copt = page.getByTestId('tn-target-list').locator('[role=option][data-value^="c:"]').first();
    await expect(copt).toBeVisible();
    const cval = (await copt.getAttribute('data-value'))!;
    const [, cslug, pname] = /^c:([^:]+):(.+)$/.exec(cval)!;
    await copt.click();
    await pickCombo(page, 'tn-port', '2001', '2001');
    await page.getByTestId('tn-label').fill('App container');
    await page.getByTestId('tn-submit').click();
    await expectToast(page, 'success', 'opm.example.com:2001');
    await expect(page.getByTestId('add-tunnel-modal')).toHaveCount(0);
    const r1 = row(page, 2001);
    await expect(r1.getByTestId('tunnel-target')).toHaveText(`${cslug} · ${pname}`);
    await expect(r1.getByTestId('tunnel-address')).toHaveText('opm.example.com:2001');
    await expect(r1.getByTestId('tunnel-open')).toHaveCount(0);
    await expect(r1.getByTestId('tunnel-state')).toHaveAttribute('data-state', 'off');
    await expect(r1).toContainText("switch it on in the site's agent GUI");

    // 2. edge https of db on 2002
    await openAdd(page);
    await pick(page, 'tn-site', 'db');
    await pick(page, 'tn-target', 'edge:https');
    await pickCombo(page, 'tn-port', '2002', '2002');
    await page.getByTestId('tn-submit').click();
    await expectToast(page, 'success');
    const r2 = row(page, 2002);
    await expect(r2.getByTestId('tunnel-target')).toHaveText('edge https');
    await expect(r2.getByTestId('tunnel-open')).toHaveAttribute('href', 'https://opm.example.com:2002');

    // 3. a port on db's server on 2003 (validation first)
    await openAdd(page);
    await pick(page, 'tn-site', 'db');
    await pick(page, 'tn-target', 'host');
    await page.getByTestId('tn-host-port').fill('70000');
    await pickCombo(page, 'tn-port', '2003', '2003');
    await page.getByTestId('tn-submit').click();
    await expect(page.getByTestId('add-tunnel-modal').getByTestId('field-error')).toContainText('1–65535');
    await page.getByTestId('tn-host-port').fill('5432');
    await page.getByTestId('tn-submit').click();
    await expectToast(page, 'success');
    await expect(row(page, 2003).getByTestId('tunnel-target')).toHaveText('server port 5432');

    // the request bodies
    const writes = await page.evaluate(() => (window as unknown as { __opmMockWrites: { method: string; path: string; body: string }[] }).__opmMockWrites.filter((w) => w.path.endsWith('/tunnels')).map((w) => JSON.parse(w.body)));
    expect(writes).toEqual([
      { siteSlug: 'app', target: { kind: 'container', containerSlug: cslug, portName: pname }, publicPort: 2001, label: 'App container' },
      { siteSlug: 'db', target: { kind: 'edge', scheme: 'https' }, publicPort: 2002 },
      { siteSlug: 'db', target: { kind: 'host', port: 5432 }, publicPort: 2003 },
    ]);

    // used ports are not offered any more
    await openAdd(page);
    await page.getByTestId('tn-port').click();
    await expect(page.getByTestId('tn-port-list').locator('[data-testid=combo-option]').first()).toHaveAttribute('data-value', '2004');
    await page.keyboard.press('Escape');
    await page.getByRole('button', { name: 'Cancel' }).click();

    // remove (confirm)
    await row(page, 2002).getByTestId('tunnel-delete').click();
    const dlg = page.getByTestId('confirm-dialog');
    await expect(dlg).toContainText('port 2002');
    await dlg.getByRole('button', { name: 'Remove' }).click();
    await expectToast(page, 'success', 'port 2002 removed');
    await expect(row(page, 2002)).toHaveCount(0);
    await expect(row(page, 2001)).toBeVisible();
  });

  test('a public port taken meanwhile: the error shows and the free ports reload', async ({ page }) => {
    await login(page, 'admin', URL_T);
    await openAdd(page);
    await pick(page, 'tn-site', 'app');
    await pick(page, 'tn-target', 'edge:https');
    await pickCombo(page, 'tn-port', '2005', '2005');
    // someone else takes 2005
    await page.evaluate(async (dep) => {
      const f = (window as unknown as { __opmMockFetch: typeof fetch }).__opmMockFetch;
      const r = await f(`http://x/v1/deployments/${dep}/tunnels`, { method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer mock:uAdmin00000000000001' }, body: JSON.stringify({ siteSlug: 'db', target: { kind: 'edge', scheme: 'http' }, publicPort: 2005 }) });
      if (r.status !== 201) throw new Error(`setup failed ${r.status}`);
    }, SEED.deployment);
    await page.getByTestId('tn-submit').click();
    await expect(page.getByTestId('add-tunnel-error')).toContainText('2005 is already used');
    await expect(page.getByTestId('add-tunnel-modal')).toBeVisible();
    await expect(page.getByTestId('tn-port')).toHaveValue('');
    await page.getByTestId('tn-port').click();
    await expect(page.getByTestId('tn-port-list').locator('[data-testid=combo-option][data-value="2005"]')).toHaveCount(0);
  });

  test('unavailable cloud: banner, agent rows still listed, no Add', async ({ page }) => {
    await page.addInitScript(() => localStorage.setItem('opm.mock.tunnels', 'unavailable'));
    await login(page, 'admin', URL_T);
    await expect(page.getByTestId('tunnels-unavailable')).toContainText('single-process cloud on a VM');
    await expect(page.getByTestId('agent-row-app')).toBeVisible();
    await expect(page.getByTestId('agent-row-app').getByTestId('agent-state')).toHaveText('not connected');
    await expect(page.getByTestId('open-agent-app')).toBeVisible();
    await expect(page.getByTestId('add-tunnel')).toHaveCount(0);
    await expect(page.getByTestId('tunnel-row')).toHaveCount(0);
  });

  test('read-only user: no Add, no remove', async ({ page }) => {
    await login(page, 'viewer', URL_T);
    await expect(row(page, 2000)).toBeVisible();
    await expect(page.getByTestId('add-tunnel')).toHaveCount(0);
    await expect(page.getByTestId('tunnel-delete')).toHaveCount(0);
  });

  test('mobile 390 px: no page overflow (page + dialog)', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await login(page, 'admin', URL_T);
    await expect(row(page, 2000)).toBeVisible();
    const overflow = () => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(await overflow()).toBeLessThanOrEqual(0);
    await page.getByTestId('add-tunnel').click();
    await expect(page.getByTestId('add-tunnel-modal')).toBeVisible();
    expect(await overflow()).toBeLessThanOrEqual(0);
  });
});
