/** Round T / nav2 at 390 px: every DOCKER page and every container tab fits without horizontal page scroll. */
import { test, expect } from './fixtures';
import { login, open, shot } from './helpers';

const PAGES = ['containers', 'containers/hello-api', 'containers/hello-api?tab=logs', 'containers/hello-api?tab=terminal', 'containers/hello-api?tab=monitoring', 'containers/hello-api?tab=inspect', 'containers/hello-api?tab=processes', 'containers/hello-api?tab=config', 'volumes', 'network', 'docker', 'agent?tab=shell'];

test('390 px: tools pages fit; terminal session works', async ({ page }) => {
  await login(page, '/_opm/app/app/containers');
  for (const p of PAGES) {
    if (p !== 'containers') await open(page, `/_opm/app/app/${p}`);
    await page.waitForTimeout(500);
    const over = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    expect(over, `horizontal overflow on ${p}`).toBeLessThanOrEqual(1);
    await shot(page, `tools-${p.replace(/[/?=]+/g, '-')}-m`, false);
  }
  await open(page, '/_opm/app/app/containers/hello-web?tab=terminal');
  await page.getByTestId('term-open').click();
  await expect(page.getByTestId('term-state')).toHaveText('connected');
  const over = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  expect(over).toBeLessThanOrEqual(1);
  await shot(page, 'tools-terminal-session-m', false);
});
