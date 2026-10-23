/** §21 #125 port tunnels on the Agent page: the list of a site's tunnels and their on / off switches. */
import { test, expect } from './fixtures';
import { knobs, login, shot } from './helpers';

const noOverflow = async (page: import('@playwright/test').Page) => expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(1);

test('this site: the tunnels card lists the agent GUI + its tunnels; switch one on and off', async ({ page }) => {
  await login(page, '/_opm/app/app/tunnels');
  const card = page.getByTestId('tunnels-card');
  await expect(card).toBeVisible();
  await expect(card.getByRole('heading', { name: 'Tunnels' })).toBeVisible();
  await expect(card).toContainText('each stays off until switched on here');
  await expect(card.getByTestId('tunnel-agent-gui')).toContainText('Agent GUI');
  await expect(card.getByTestId('tunnel-agent-gui-state')).toHaveText('on');
  await expect(card.getByTestId('tunnel-row')).toHaveCount(2);

  const api = card.locator('[data-tunnel="tun-api"]');
  await expect(api).toContainText('API for the integration partner');
  await expect(api).toContainText('hello-api · http');
  await expect(api.getByTestId('tunnel-address')).toHaveText('opm.example.com:2001');
  await expect(api.getByTestId('tunnel-conns')).toHaveText('2 open');
  await expect(api.getByTestId('tunnel-state')).toHaveText('on');

  const edge = card.locator('[data-tunnel="tun-edge"]');
  await expect(edge).toContainText('edge https');
  await expect(edge.getByTestId('tunnel-error')).toContainText('connection refused');
  await expect(edge.getByTestId('tunnel-state')).toHaveText('off');
  await shot(page, 'tunnels-card', false);

  await edge.getByTestId('tunnel-on').click();
  await expect(edge.getByTestId('tunnel-state')).toHaveText('on');
  await expect(page.getByTestId('toast').filter({ hasText: 'Tunnel switched on' })).toBeVisible();
  await edge.getByTestId('tunnel-off').click();
  await expect(edge.getByTestId('tunnel-state')).toHaveText('off');
  await expect(edge.getByTestId('tunnel-on')).toBeEnabled();
});

test("another site's tunnels on its Agent page (its agent GUI tunnel off)", async ({ page }) => {
  await login(page, '/_opm/app/db/tunnels');
  const card = page.getByTestId('tunnels-card');
  await expect(card.getByTestId('tunnel-row')).toHaveCount(1);
  await expect(card.getByTestId('tunnel-agent-gui-state')).toHaveText('off');
  const pg = card.locator('[data-tunnel="tun-pg"]');
  await expect(pg).toContainText('server port 5432');
  await expect(pg.getByTestId('tunnel-address')).toHaveText('opm.example.com:2003');
  await pg.getByTestId('tunnel-on').click();
  await expect(pg.getByTestId('tunnel-state')).toHaveText('on');
  await expect(pg.getByTestId('tunnel-off')).toBeVisible();
});

test('no tunnels added yet: the empty text', async ({ page }) => {
  await knobs(page, { 'opm.mock.tunnels': 'empty' });
  await login(page, '/_opm/app/app/tunnels');
  const card = page.getByTestId('tunnels-card');
  await expect(card.getByTestId('tunnels-empty')).toHaveText('No tunnels yet — add one in the cloud console (Tunnels).');
  await expect(card.getByTestId('tunnel-agent-gui-state')).toHaveText('on');
  await expect(card.getByTestId('tunnel-row')).toHaveCount(0);
});

test('a cloud without tunnels: no rows', async ({ page }) => {
  await knobs(page, { 'opm.mock.tunnels': 'off' });
  await login(page, '/_opm/app/app/tunnels');
  const card = page.getByTestId('tunnels-card');
  await expect(card.getByTestId('tunnels-none-cloud')).toHaveText('This cloud does not carry port tunnels.');
  await expect(card.getByTestId('tunnel-row')).toHaveCount(0);
  await expect(card.getByTestId('tunnel-agent-gui')).toHaveCount(0);
});

test('through the cloud: switching on is disabled, switching off works', async ({ page }) => {
  const sid = '01REMOTESESSION000000000000';
  const exp = Math.floor(Date.now() / 1000) + 3600;
  const actorToken = 'mock.' + Buffer.from(JSON.stringify({ email: 'cloud.person@acme.example', sid, exp })).toString('base64');
  const expiresAt = new Date(exp * 1000).toISOString();
  await knobs(page, { 'opm.mock.session': JSON.stringify({ sessionId: sid, expiresAt, keyVersion: 1, remote: { email: 'cloud.person@acme.example', name: null, actorToken, expiresAt } }) });
  await page.goto('/_opm/app/app/tunnels');
  await expect(page.getByTestId('remote-chip')).toBeVisible();
  const card = page.getByTestId('tunnels-card');
  // through the cloud a tunnel switches on and off like on the site itself
  const edge = card.locator('[data-tunnel="tun-edge"]');
  await edge.getByTestId('tunnel-on').click();
  await expect(edge.getByTestId('tunnel-state')).toHaveText('on');
  await edge.getByTestId('tunnel-off').click();
  await expect(edge.getByTestId('tunnel-state')).toHaveText('off');
});

test.describe('390 px', () => {
  test.use({ viewport: { width: 390, height: 844 } });
  test('the tunnels card fits without sideways scroll', async ({ page }) => {
    await login(page, '/_opm/app/app/tunnels');
    const card = page.getByTestId('tunnels-card');
    await card.scrollIntoViewIfNeeded();
    await expect(card.getByTestId('tunnel-row')).toHaveCount(2);
    for (const b of await card.getByRole('button').all()) {
      const bb = (await b.boundingBox())!;
      expect(bb.x + bb.width).toBeLessThanOrEqual(391);
    }
    await noOverflow(page);
    await shot(page, 'tunnels-card-m', false);
  });
});

test('tunnels (§21 #125): own entry in every site tree; the Agent page no longer carries the card', async ({ page }) => {
  await login(page, '/_opm/app/app/agent');
  await expect(page.getByTestId('tunnels-card')).toHaveCount(0);
  await page.getByTestId('tree-app-tunnels').click();
  await expect(page).toHaveURL(/\/_opm\/app\/app\/tunnels$/);
  await expect(page.getByTestId('tunnels-card')).toBeVisible();
});
