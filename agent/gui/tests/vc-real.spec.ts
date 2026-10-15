/**
 * §21 #101 persist-to-cloud volume smoke against a REAL agent mesh (project "real"):
 *   cd agent && go run ./cmd/opm-devmesh -volume-content        (fresh mesh per run: the demo sends ONE cloud edit)
 *   OPM_E2E_REAL=1 OPM_AGENT_URL=http://127.0.0.1:8090 OPM_ACCESS_KEY=ABCDEFGHIJKLMNOPQRSTUVWX OPM_VC_MODE=ff|conflict \
 *     npx playwright test --project=real tests/vc-real.spec.ts
 * Approves the revision with app/flows, then Cloud sync: state, Snapshot now → the devmesh sends its demo edit →
 *   ff:        plan = clean fast-forward → wait → applied (lib/util.js + cloud-note.md written)
 *   conflict:  lib/util.js edited on the site first (Files tab) → plan has a conflict → diff → Take cloud → applied
 * Screenshots → screenshots/vc-real-*.png.
 */
import { test, expect } from './fixtures';

const KEY = process.env.OPM_ACCESS_KEY ?? '';
const MODE = process.env.OPM_VC_MODE ?? 'ff';
const EMAIL = 'e2e@opm.test';

test(`real agent: persist-to-cloud volume app/flows (${MODE})`, async ({ page }) => {
  test.skip(!KEY || !process.env.OPM_VC_MODE, 'OPM_ACCESS_KEY / OPM_VC_MODE not set');
  test.setTimeout(420_000);
  await page.goto('/_opm/');
  await page.waitForURL(/\/_opm\/login/);
  await page.getByTestId('access-key').fill(KEY);
  await page.getByRole('button', { name: 'Log in' }).click();
  await page.getByTestId('actor-email').fill(EMAIL);
  await page.getByTestId('actor-continue').click();
  await expect(page.getByTestId('actor-chip')).toContainText(EMAIL);
  const go = async (path: string) => {
    await page.goto(path);
    await page.getByTestId('actor-email').fill(EMAIL);
    await page.getByTestId('actor-continue').click();
    await expect(page.getByTestId('actor-chip')).toContainText(EMAIL);
  };
  // approve the revision with the persist volume (skipped when it is already the head)
  await go('/_opm/app/app/diff');
  if (!/head seq [1-9]/.test((await page.getByTestId('head-seq').textContent()) ?? '')) {
    await expect(page.getByTestId('approve')).toBeVisible({ timeout: 180_000 });
    await page.getByTestId('approve').click();
    const flow = page.getByTestId('approve-flow');
    await expect(flow.getByTestId('accept-input')).toBeVisible({ timeout: 90_000 });
    await flow.getByTestId('accept-input').fill('ACCEPT');
    await flow.getByTestId('start-update').click();
    await expect(page.getByTestId('update-result')).toContainText('Applied', { timeout: 180_000 });
    await page.getByTestId('update-ok').click();
  }
  await go('/_opm/app/app/volumes?v=flows&tab=sync');
  const sp = page.getByTestId('vol-sync');
  await expect(sp).toBeVisible({ timeout: 30_000 });
  await expect(page.getByTestId('sync-badge-flows')).toBeVisible();
  await page.screenshot({ path: `screenshots/vc-real-${MODE}-state.png`, fullPage: true });
  // Snapshot now → uploaded → the devmesh sends its demo edit (the cloud view reaches the agent within its poll)
  await sp.getByTestId('sync-snapshot').click();
  await expect(sp.getByTestId('sync-last')).toContainText(/version \d+|queued/, { timeout: 30_000 });
  await expect(sp.getByTestId('sync-push')).toBeVisible({ timeout: 180_000 });
  await expect(sp.getByTestId('sync-push')).toContainText('demo edit from opm-devmesh');
  await page.screenshot({ path: `screenshots/vc-real-${MODE}-push.png`, fullPage: true });
  if (MODE === 'conflict') {
    // change lib/util.js on the site (the cloud changed it too)
    await page.getByTestId('vtab-files').click();
    await page.getByTestId('entry-lib').click();
    await page.getByTestId('edit-util.js').click();
    const ed = page.getByTestId('file-editor');
    await expect(ed.locator('textarea')).not.toHaveValue('');
    await ed.locator('textarea').fill('module.exports = (x) => x * 5; // edited on the site\n');
    await ed.getByTestId('file-review').click();
    await ed.getByTestId('file-save').click();
    await expect(ed).toBeHidden();
    await page.getByTestId('vtab-sync').click();
    await expect(page.getByTestId('sync-state')).toHaveText('Conflict', { timeout: 30_000 });
  }
  await page.getByTestId('sync-apply').click();
  const af = page.getByTestId('apply-flow');
  if (MODE === 'conflict') {
    await expect(af.getByTestId('plan-item-lib/util.js')).toHaveAttribute('data-kind', 'conflict', { timeout: 30_000 });
    await af.getByTestId('diff-btn-lib/util.js').click();
    await expect(af.getByTestId('file-diff-lib/util.js').getByTestId('diff-split')).toContainText('edited in the cloud', { timeout: 30_000 });
    await expect(af.getByTestId('file-diff-lib/util.js').getByTestId('diff-split')).toContainText('edited on the site');
    await page.screenshot({ path: `screenshots/vc-real-conflict-plan.png` });
    await af.getByTestId('choice-lib/util.js').getByRole('radio', { name: 'Take cloud' }).click();
  } else {
    await expect(af.getByTestId('plan-ff')).toBeVisible({ timeout: 30_000 });
    await expect(af.getByTestId('plan-item-cloud-note.md')).toBeVisible();
    await page.screenshot({ path: `screenshots/vc-real-ff-plan.png` });
  }
  await af.getByTestId('apply-continue').click();
  // an apply is a merge (§21 #104 (e)): one confirm, no intent wait
  const f = page.getByTestId('apply-confirm');
  await expect(f).toBeVisible({ timeout: 30_000 });
  await f.getByTestId('confirm-ok').click();
  const r = page.getByTestId('apply-result');
  await expect(r).toBeVisible({ timeout: 120_000 });
  await expect(r.getByTestId('res-written')).not.toHaveText('0');
  await expect(r.getByTestId('res-snapshot')).toBeVisible();
  await page.screenshot({ path: `screenshots/vc-real-${MODE}-result.png` });
  await r.getByTestId('apply-result-close').click();
  await expect(page.getByTestId('sync-push')).toHaveCount(0, { timeout: 30_000 });
  await page.getByTestId('vtab-files').click();
  await page.getByTestId('vol-path').getByRole('button').first().click(); // the volume root (conflict mode was in lib/)
  await expect(page.getByTestId('entry-cloud-note.md')).toBeVisible({ timeout: 30_000 });
});
