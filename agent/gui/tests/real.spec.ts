/**
 * Smoke test against a REAL agent (project "real"):
 *   OPM_E2E_REAL=1 OPM_AGENT_URL=http://127.0.0.1:8090 OPM_ACCESS_KEY=XXXX-… npx playwright test --project=real
 * Read-only: logs in, declares an actor and opens every page (no approve / no writes except login + actor).
 */
import { test, expect } from './fixtures';

const KEY = process.env.OPM_ACCESS_KEY ?? '';
const PAGES = ['/_opm/app', '/_opm/app/variables', '/_opm/app/secrets', '/_opm/app/files', '/_opm/app/sites', '/_opm/app/updates', '/_opm/app/backups', '/_opm/app/internet', '/_opm/app/*/status', '/_opm/app/*/activities', '/_opm/app/*/debug', '/_opm/app/*/config', '/_opm/app/app/agent', '/_opm/app/db/agent', '/_opm/app/*/agent'];

test('real agent: login, actor, every page renders', async ({ page }) => {
  test.skip(!KEY, 'OPM_ACCESS_KEY not set');
  await page.goto('/_opm/');
  await page.waitForURL(/\/_opm\/login/);
  await page.getByTestId('access-key').fill(KEY);
  await page.getByRole('button', { name: 'Log in' }).click();
  await page.getByTestId('actor-email').fill('e2e@opm.test');
  await page.getByTestId('actor-continue').click();
  await expect(page.getByTestId('actor-chip')).toContainText('e2e@opm.test');
  for (const p of PAGES) {
    await page.goto(p);
    await page.getByTestId('actor-email').fill('e2e@opm.test');
    await page.getByTestId('actor-continue').click();
    await expect(page.getByTestId('error-state')).toHaveCount(0, { timeout: 8000 });
    await page.screenshot({ path: `screenshots/real-${p.replace(/[^a-z0-9]+/gi, '-')}.png`, fullPage: true });
  }
});
