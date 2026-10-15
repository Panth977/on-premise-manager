/**
 * §21 #104 Branch smoke against a REAL agent mesh (project "real"):
 *   cd agent && go run ./cmd/opm-devmesh -branches        (fresh mesh: cloud head = commit 3, both sites merged at 1)
 *   OPM_E2E_REAL=1 OPM_AGENT_URL=http://127.0.0.1:8090 OPM_ACCESS_KEY=ABCDEFGHIJKLMNOPQRSTUVWX OPM_BR_NETWORK=opm_DepDevMesh0000000001_app \
 *     npx playwright test --project=real tests/branch-real.spec.ts
 * Branch page (cloud seq 3 vs this site) → a container started by hand on the site network shows as uncommitted →
 * commit it → merge from the cloud cherry-picking the new worker + "remove locally" the stray container → applied →
 * History shows both commits. §21 #105: then a GUI edit (Variables › theme → blue) stays in the browser (no Save) →
 * ONE Commit from the change bar → the agent applies it → History shows the commit. Screenshots → screenshots/br-real-*.png.
 * Removes the stray container at the end.
 */
import { execFileSync } from 'node:child_process';
import { test, expect } from './fixtures';
import { pickOption } from './helpers';

const KEY = process.env.OPM_ACCESS_KEY ?? '';
const NET = process.env.OPM_BR_NETWORK ?? '';
const EMAIL = 'e2e@opm.test';
const STRAY = 'stray';
const docker = (...a: string[]) => execFileSync('docker', a, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim();

test('real agent: branch overview → drift → commit → cherry-pick merge with remove locally → history', async ({ page }) => {
  test.skip(!KEY || !NET, 'OPM_ACCESS_KEY / OPM_BR_NETWORK not set');
  test.setTimeout(420_000);
  const go = async (path: string) => {
    await page.goto(path);
    await page.getByTestId('actor-email').fill(EMAIL);
    await page.getByTestId('actor-continue').click();
    await expect(page.getByTestId('actor-chip')).toContainText(EMAIL);
  };
  await page.goto('/_opm/');
  await page.waitForURL(/\/_opm\/login/);
  await page.getByTestId('access-key').fill(KEY);
  await page.getByRole('button', { name: 'Log in' }).click();
  await page.getByTestId('actor-email').fill(EMAIL);
  await page.getByTestId('actor-continue').click();
  await expect(page.getByTestId('actor-chip')).toContainText(EMAIL);
  try {
    await go('/_opm/app/app/branch');
    await expect(page.getByTestId('cloud-card')).toContainText('seq 3', { timeout: 60_000 });
    await expect(page.getByTestId('behind')).toHaveText('2 to merge');
    await expect(page.getByTestId('local-card')).toContainText('initial release');
    await page.screenshot({ path: 'screenshots/br-real-overview.png', fullPage: true });

    // a container started by hand on the site network → uncommitted (changed on the server · Docker)
    try {
      docker('rm', '-f', STRAY);
    } catch {
      /* none */
    }
    const image = docker('ps', '--filter', `network=${NET}`, '--format', '{{.Image}}').split('\n')[0] || 'busybox:1.36';
    docker('run', '-d', '--name', STRAY, '--network', NET, image, 'sleep', '3600');
    await page.getByTestId('rescan').click();
    await page.getByTestId('branch-tabs-uncommitted').click();
    const row = page.getByTestId('uncommitted-list').locator('tr', { hasText: STRAY });
    await expect(row).toBeVisible({ timeout: 90_000 });
    await expect(row).toContainText('changed on the server');
    await page.screenshot({ path: 'screenshots/br-real-uncommitted.png', fullPage: true });

    // merge is blocked while it is uncommitted
    await page.getByTestId('branch-tabs-overview').click();
    await expect(page.getByTestId('merge-blocked')).toContainText('Commit or discard');

    // commit it (drift: recorded at once, nothing restarts)
    await page.getByTestId('branch-tabs-uncommitted').click();
    await page.getByTestId('commit-selected').click();
    const dlg = page.getByTestId('commit-dialog');
    await dlg.getByTestId('commit-message').fill('Record the stray container started by hand');
    await dlg.getByTestId('commit-submit').click();
    await expect(page.getByTestId('uncommitted-list')).toContainText('Nothing uncommitted', { timeout: 60_000 });

    // merge from the cloud: only the new worker + remove the stray container locally
    await page.getByTestId('branch-tabs-overview').click();
    await expect(page.getByTestId('merge-blocked')).toHaveCount(0, { timeout: 30_000 });
    await page.getByTestId('merge-open').click();
    const view = page.getByTestId('merge-view');
    const worker = view.locator('[data-testid^="merge-item-"][data-testid$="/containers/worker"]');
    const stray = view.getByTestId('merge-group-removed').locator(`[data-testid^="merge-item-"][data-testid$="/containers/${STRAY}"]`);
    await expect(worker).toBeVisible({ timeout: 60_000 });
    await expect(stray).toHaveAttribute('data-effect', 'remove');
    await view.getByTestId('merge-clear').click();
    await worker.locator('input[type=checkbox]').first().check();
    await stray.locator('input[type=checkbox]').first().check();
    await expect(view.getByTestId('merge-submit')).toBeEnabled({ timeout: 30_000 });
    await page.screenshot({ path: 'screenshots/br-real-merge.png', fullPage: true });
    await view.getByTestId('merge-submit').click();
    const cd = page.getByTestId('merge-commit-dialog');
    await cd.getByTestId('commit-message').fill('Take the worker; remove the stray container');
    await cd.getByTestId('commit-submit').click();
    await expect(page).toHaveURL(/\/branch$/);
    await expect(page.getByTestId('update-result')).toContainText('Applied', { timeout: 300_000 });
    expect(docker('ps', '-a', '--filter', `name=^${STRAY}$`, '--format', '{{.Names}}')).toBe('');
    await page.screenshot({ path: 'screenshots/br-real-applied.png', fullPage: true });

    await page.getByTestId('branch-tabs-history').click();
    const hist = page.getByTestId('history');
    await expect(hist).toContainText('Take the worker; remove the stray container', { timeout: 30_000 });
    await expect(hist).toContainText('Record the stray container started by hand');
    await page.screenshot({ path: 'screenshots/br-real-history.png', fullPage: true });

    // §21 #105 a GUI edit → ONE commit (applied by the agent; the commit exists only when the apply succeeds)
    await page.getByTestId('side-variables').click();
    await pickOption(page, 'var-theme', 'blue');
    await expect(page.getByTestId('local-edit-bar')).toContainText('1 change (not committed)');
    await expect(page.getByTestId('save-working')).toHaveCount(0);
    await page.getByTestId('change-bar-commit').click();
    const lc = page.getByTestId('local-commit-dialog');
    await lc.getByTestId('commit-message').fill('Theme blue (GUI edit, one commit)');
    await page.screenshot({ path: 'screenshots/br-real-gui-commit.png', fullPage: false });
    await lc.getByTestId('commit-submit').click();
    await expect(page).toHaveURL(/\/branch$/);
    await expect(page.getByTestId('update-result')).toContainText('Applied', { timeout: 300_000 });
    await page.getByTestId('branch-tabs-history').click();
    await expect(page.getByTestId('history')).toContainText('Theme blue (GUI edit, one commit)', { timeout: 30_000 });
    await page.screenshot({ path: 'screenshots/br-real-gui-commit-history.png', fullPage: true });
  } finally {
    try {
      docker('rm', '-f', STRAY);
    } catch {
      /* already gone */
    }
  }
});
