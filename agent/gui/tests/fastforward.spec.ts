// SPEC-final §21 #95 — "⏩ Hold to fast-forward" in every intent-wait modal (approve, rollback to a snapshot, reset a
// volume): hold → ×2 at once → ×4 after 1.5 s → release → eases back to ×1 (~400 ms); keyboard hold; the countdown ends
// sooner; the agent (mock) is authoritative and the GUI stays in sync with it; the history records the fast-forward.
import type { Locator, Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { knobs, login, shot, nav } from './helpers';

/** §21 #104: approving proposals is gone (merges have no wait); the roll-back-to-a-snapshot flow keeps the wait + ⏩ */
async function openApprove(page: Page): Promise<Locator> {
  await login(page, '/_opm/app/app/backups');
  const row = page.getByTestId('snapshots').locator('tr', { hasText: 'Before changing the theme' });
  await row.getByRole('button', { name: 'Roll back' }).click();
  await page.getByTestId('rollback-drawer').getByTestId('rollback-go').click();
  const flow = page.getByTestId('approve-flow');
  await expect(flow.getByTestId('countdown')).toBeVisible();
  return flow;
}
const num = async (l: Locator, attr: string) => Number(await l.getAttribute(attr));

/** samples the shown speed every animation frame for `ms` (in the page) */
async function sampleSpeeds(page: Page, ms: number): Promise<number[]> {
  return page.evaluate(
    (dur) =>
      new Promise<number[]>((resolve) => {
        const out: number[] = [];
        const t0 = performance.now();
        const f = () => {
          const el = document.querySelector('[data-testid="ff-speed"]');
          if (el) out.push(Number(el.getAttribute('data-speed')));
          if (performance.now() - t0 < dur) requestAnimationFrame(f);
          else resolve(out);
        };
        requestAnimationFrame(f);
      }),
    ms,
  );
}

test('pointer hold: ×2 at once, ×4 after 1.5 s, release eases back to ×1; the agent follows', async ({ page }) => {
  await knobs(page, { 'opm.mock.waitSec': '60' });
  const flow = await openApprove(page);
  const btn = flow.getByTestId('ff-hold');
  const badge = flow.getByTestId('ff-speed');
  const cd = flow.getByTestId('countdown');
  await expect(btn).toBeVisible();
  await expect(btn).toHaveText(/Hold to fast-forward/);
  await expect(btn).toHaveAttribute('aria-label', /Hold to fast-forward/);
  await expect(btn).toHaveCSS('touch-action', 'none');
  await expect(badge).toHaveText('×1');
  await expect(flow.getByTestId('ff-live')).toHaveText('Normal speed');

  await btn.hover();
  await page.mouse.down();
  await expect(badge).toHaveText('×2');
  await expect(btn).toHaveAttribute('aria-pressed', 'true');
  await expect(flow.getByTestId('ff-live')).toHaveText('Speed ×2');
  await expect(cd).toHaveAttribute('data-server-speed', '2'); // the agent got it
  await expect(badge).toHaveText('×4', { timeout: 2500 });
  await expect(flow.getByTestId('ff-live')).toHaveText('Speed ×4');
  await expect(cd).toHaveAttribute('data-server-speed', '4');
  await shot(page, 'ff-hold-x4', false);
  // heartbeats keep the agent at ×4 while held (it decays after 1.5 s without one)
  await page.waitForTimeout(2000);
  await expect(cd).toHaveAttribute('data-server-speed', '4');

  await page.mouse.up();
  const speeds = await sampleSpeeds(page, 700);
  expect(speeds.some((s) => s > 1.05 && s < 3.95), `eased values ${speeds.join(',')}`).toBe(true); // a slide, not a jump
  expect(speeds.at(-1)).toBe(1);
  await expect(badge).toHaveText('×1');
  await expect(btn).toHaveAttribute('aria-pressed', 'false');
  await expect(cd).toHaveAttribute('data-server-speed', '1');
  await expect(flow.getByTestId('ff-live')).toHaveText('Normal speed');

  // ~1.5 s ×2 + ~3.5 s ×4 + real time: well below 60 − real elapsed
  const left = await num(cd, 'data-remaining');
  expect(left).toBeLessThan(45);
  // the shown time follows the agent's numbers
  await page.waitForTimeout(1200);
  const shown = await num(cd, 'data-remaining');
  const server = await num(cd, 'data-server-remaining');
  expect(Math.abs(shown - server)).toBeLessThan(1.6);

  // leaving the button while holding releases too
  await btn.hover();
  await page.mouse.down();
  await expect(badge).toHaveText('×2');
  await cd.hover(); // pointerleave
  await expect(badge).toHaveText('×1');
  await expect(cd).toHaveAttribute('data-server-speed', '1');
  await page.mouse.up();
  await flow.getByTestId('approve-cancel').click();
  await expect(flow).toBeHidden();
});

test('keyboard hold (Space and Enter) works like the pointer; blur / hidden tab release', async ({ page }) => {
  await knobs(page, { 'opm.mock.waitSec': '60' });
  const flow = await openApprove(page);
  const btn = flow.getByTestId('ff-hold');
  const badge = flow.getByTestId('ff-speed');
  await btn.focus();
  await page.keyboard.down(' ');
  await expect(badge).toHaveText('×2');
  await expect(badge).toHaveText('×4', { timeout: 2500 });
  await page.keyboard.up(' ');
  await expect(badge).toHaveText('×1');
  await expect(flow.getByTestId('countdown')).toBeVisible(); // no click / submit happened

  await btn.focus();
  await page.keyboard.down('Enter');
  await expect(badge).toHaveText('×2');
  await page.keyboard.up('Enter');
  await expect(badge).toHaveText('×1');

  // a window blur (or a hidden tab) while holding → back to ×1
  await btn.focus();
  await page.keyboard.down(' ');
  await expect(badge).toHaveText('×2');
  await page.evaluate(() => window.dispatchEvent(new Event('blur')));
  await expect(badge).toHaveText('×1');
  await expect(flow.getByTestId('countdown')).toHaveAttribute('data-server-speed', '1');
  await page.keyboard.up(' ');
});

test('the countdown ends sooner (≥ wait / 4), ACCEPT works and the history records the fast-forward', async ({ page }) => {
  await knobs(page, { 'opm.mock.waitSec': '20' });
  const flow = await openApprove(page);
  const t0 = Date.now();
  await flow.getByTestId('ff-hold').hover();
  await page.mouse.down();
  // 1.5 s at ×2 (3 s) + 17 s / 4 = 4.25 s → ≈ 5.8 s instead of 20 s
  await expect(flow.getByTestId('accept-input')).toBeVisible({ timeout: 12_000 });
  const took = (Date.now() - t0) / 1000;
  await page.mouse.up();
  expect(took).toBeLessThan(11);
  expect(took).toBeGreaterThanOrEqual(20 / 4);
  await expect(flow.getByTestId('ff-done')).toContainText('up to ×4');
  await expect(flow.getByTestId('ff-hold')).toHaveCount(0);
  await shot(page, 'ff-accept', false);
  await flow.getByTestId('accept-input').fill('ACCEPT');
  // §21 #104: the rollback is a commit on this site's branch — its message is required
  await expect(flow.getByTestId('start-update')).toBeDisabled();
  await flow.getByTestId('rollback-message').fill('Roll back the theme test');
  await flow.getByTestId('start-update').click();
  await expect(page.getByTestId('update-progress')).toBeVisible();
  await expect(page.getByTestId('run-ff')).toContainText(/fast-forwarded \(×4, \d+ s real\)/);
  await shot(page, 'ff-history');
  // the activity carries it too
  await nav(page, 'activities'); // client-side: the mock lives in the page
  await expect(page.getByText(/fast-forwarded \(×4, \d+ s real\)/).first()).toBeVisible();
});

test('rollback and volume reset modals have the same button (the reset intent goes through /sites/{site}/intents)', async ({ page }) => {
  await knobs(page, { 'opm.mock.waitSec': '60' });
  await login(page, '/_opm/app/backups');
  const card = page.getByTestId('site-volumes-app');
  await card.getByTestId('reset-app-api-env').click();
  const reset = page.getByTestId('reset-flow');
  await expect(reset.getByTestId('countdown')).toBeVisible();
  await reset.getByTestId('ff-hold').hover();
  await page.mouse.down();
  await expect(reset.getByTestId('ff-speed')).toHaveText('×4', { timeout: 2500 });
  await expect(reset.getByTestId('countdown')).toHaveAttribute('data-server-speed', '4');
  await shot(page, 'ff-reset', false);
  await page.mouse.up();
  await reset.getByTestId('reset-cancel').click();
  await expect(reset).toBeHidden();
  const row = page.getByTestId('snapshots').locator('tr', { hasText: 'Before changing the theme' });
  await row.getByRole('button', { name: 'Roll back' }).click();
  await page.getByTestId('rollback-drawer').getByTestId('rollback-go').click();
  const flow = page.getByTestId('approve-flow');
  await expect(flow.getByTestId('countdown')).toBeVisible();
  await flow.getByTestId('ff-hold').hover();
  await page.mouse.down();
  await expect(flow.getByTestId('countdown')).toHaveAttribute('data-server-speed', '2');
  await page.mouse.up();
  await expect(flow.getByTestId('countdown')).toHaveAttribute('data-server-speed', '1');
  await flow.getByTestId('approve-cancel').click();
  await expect(flow).toBeHidden();

});
