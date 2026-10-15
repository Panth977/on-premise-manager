/**
 * §21 #104 branches + commits in the cloud console (mock): commit dialog (message required), uncommitted counts,
 * Compare + cherry-pick with a conflict and a removal, History (open, compare two, revert), create-from
 * (template ← deployment, deployment ← deployment), Agent branches, the old Diff / Template sync redirects.
 * Seed (src/lib/mock/branches.ts): Hello world has commit 2 after ACME was cloned (greeting 2.0, redis on-failure, drops
 * support-email); ACME's pre-0.14.1 working copy (theme yellow, pg-password, dev-tool presets) was auto-committed by the
 * mock migrate-j as commit 2 (§21 #105).
 */
import { expect, test, type Page } from '@playwright/test';
import { login, SEED, expectToast, pickCombo } from './helpers';

const D = `/app/deployments/${SEED.deployment}`;
const T = `/app/templates/${SEED.template}`;

async function fresh(page: Page, next = '/app/deployments') {
  await login(page, 'admin', next);
  await page.evaluate(() => (window as unknown as { __opmMockReset: () => void }).__opmMockReset());
}

/** commit the browser change set through the change bar */
async function commitWorking(page: Page, message: string) {
  await page.getByTestId('change-bar-commit').click();
  const dlg = page.getByTestId('commit-dialog');
  await expect(dlg).toBeVisible();
  await dlg.getByTestId('commit-message').fill(message);
  await dlg.getByTestId('commit-submit').click();
  await expectToast(page, 'success', /Commit \d+ recorded/);
  await expect(dlg).toBeHidden();
}

test.describe('branches', () => {
  test('commit dialog: message required, pending counts, commit clears them', async ({ page }) => {
    await fresh(page);
    await page.goto(`${D}/history`);
    // §21 #105 migrate-j: the old working copy became commit 2
    await expect(page.getByTestId('nav-head')).toHaveText('commit 2');
    await expect(page.getByTestId('history-row-2')).toContainText('Uncommitted changes from before 0.14.1 (auto-committed)');
    await page.goto(`${D}/variables`);
    await expect(page.getByTestId('change-bar-commit')).toBeDisabled();
    await page.getByTestId('val-greeting').fill('Welcome to the ACME plant');
    await expect(page.getByTestId('nav-uncommitted')).toHaveText('1 not committed');
    await expect(page.getByTestId('change-bar-count')).toContainText('1 change');
    await page.getByTestId('change-bar-commit').click();
    const dlg = page.getByTestId('commit-dialog');
    await expect(dlg).toBeVisible();
    await expect(dlg.getByTestId('commit-submit')).toBeDisabled();
    await dlg.getByTestId('commit-message').fill('   ');
    await expect(dlg.getByTestId('commit-submit')).toBeDisabled();
    // agents db (0.9) + lab (< 0.14) still get proposals
    await expect(dlg.getByTestId('commit-warning').filter({ hasText: /0\.14/ })).toHaveCount(2);
    await expect(dlg.getByTestId('commit-changes')).toContainText(/greeting/i);
    await dlg.getByTestId('commit-message').fill('Greeting for ACME\n\nAsked by the plant manager.');
    await dlg.getByTestId('commit-submit').click();
    await expectToast(page, 'success', 'Commit 3 recorded');
    await expect(page.getByTestId('nav-head')).toHaveText('commit 3');
    await expect(page.getByTestId('nav-uncommitted')).toHaveCount(0);
    await expect(page.getByTestId('change-bar-count')).toHaveCount(0);
    await page.goto('/app/deployments');
    const card = page.getByTestId('deployment-card').filter({ hasText: 'ACME plant' });
    await expect(card.getByTestId('dep-head')).toContainText('Greeting for ACME');
    await expect(card.getByTestId('dep-uncommitted')).toHaveCount(0);
  });

  test('site editor: the scope change bar commits (disabled without changes)', async ({ page }) => {
    await fresh(page);
    await page.goto(`${D}/sites/app/settings`);
    const btn = page.getByTestId('change-bar-commit');
    await expect(btn).toBeDisabled();
    await page.getByTestId('site-label').fill('Application site (renamed)');
    await expect(btn).toBeEnabled();
    await btn.click();
    await expect(page.getByTestId('commit-dialog')).toBeVisible();
    await page.getByTestId('commit-cancel').click();
  });

  test('compare + cherry-pick: conflict, removal, merge', async ({ page }) => {
    await fresh(page);
    await page.goto(`${D}/variables`);
    await page.getByTestId('val-greeting').fill('Welcome to ACME');
    await commitWorking(page, 'Welcome greeting');
    await page.getByTestId('nav-compare').click();
    // no ?source → the origin template
    await expect(page.getByTestId('merge-view')).toBeVisible();
    const conflict = page.locator('[data-testid^="merge-item-"][data-kind="conflict"]');
    await expect(conflict).toHaveCount(1);
    await expect(conflict).toContainText(/greeting/i);
    // removals: support-email (the template dropped it) + what only ACME has (presets, pg-password) — none ticked
    const removals = page.locator('[data-testid^="merge-item-"][data-effect="remove"]');
    expect(await removals.count()).toBeGreaterThanOrEqual(2);
    await expect(page.getByTestId('merge-group-removed')).toContainText(/Removed by this merge/i);
    const removal = removals.filter({ hasText: 'support-email' });
    await expect(removal).toHaveAttribute('data-kind', 'theirs');
    // removals are not preselected; the merge waits for the conflict
    await expect(page.getByTestId('merge-submit')).toBeDisabled();
    const key = (await conflict.getAttribute('data-testid'))!.slice('merge-item-'.length);
    await page.getByTestId(`merge-resolve-${key}-target`).click();
    const rkey = (await removal.getAttribute('data-testid'))!.slice('merge-item-'.length);
    await page.getByTestId(`merge-take-${rkey}`).check();
    await expect(page.getByTestId('merge-submit')).toBeEnabled();
    await page.getByTestId('merge-submit').click();
    const dlg = page.getByTestId('merge-commit-dialog');
    await expect(dlg).toBeVisible();
    await dlg.getByTestId('commit-message').fill('Take redis restart + drop support-email from Hello world');
    await dlg.getByTestId('commit-submit').click();
    await expect(page.getByTestId('nav-head')).toHaveText('commit 4');
    await page.goto(`${D}/variables`);
    await expect(page.getByText('support-email')).toHaveCount(0);
    await page.goto(`${D}/history`);
    await expect(page.getByTestId('history-row-4')).toContainText('Take redis restart');
  });

  test('history: open, compare two, revert', async ({ page }) => {
    await fresh(page);
    await page.goto(`${D}/history`);
    await expect(page.getByTestId('history-row-2')).toBeVisible();
    await expect(page.getByTestId('history-row-1')).toBeVisible();
    await page.getByTestId('history-open-2').click();
    await expect(page.getByTestId('history-detail')).toContainText(/theme/i);
    await page.getByTestId('history-pick-1').check();
    await page.getByTestId('history-pick-2').check();
    await page.getByTestId('history-compare').click();
    await expect(page.getByTestId('history-compare-result')).toContainText(/theme/i);
    await page.getByTestId('history-detail').getByTestId('revert').click();
    const dlg = page.getByTestId('history-commit-dialog');
    await expect(dlg).toBeVisible();
    await dlg.getByTestId('commit-message').fill('Revert the yellow theme');
    await dlg.getByTestId('commit-submit').click();
    await expect(page.getByTestId('history-row-3')).toContainText('Revert the yellow theme');
    await expect(page.getByTestId('nav-head')).toHaveText('commit 3');
  });

  test('template: commit, history; create from a deployment', async ({ page }) => {
    await fresh(page, '/app/templates');
    await page.goto(`${T}/history`);
    await expect(page.getByTestId('template-head')).toHaveText('commit 2');
    await expect(page.getByTestId('history-row-2')).toContainText('Greeting for 2.0');
    // template ← deployment
    await page.goto('/app/templates');
    await expect(page.getByTestId('template-row').filter({ hasText: 'Hello world' }).getByTestId('tpl-head')).toContainText('Greeting for 2.0');
    await page.getByTestId('new-template').click();
    await page.getByTestId('nt-label').fill('ACME as a template');
    await page.getByTestId('nt-src-deployment').click();
    await pickCombo(page, 'nt-deployment', SEED.deployment, 'ACME');
    await expect(page.getByTestId('nt-commit')).toBeVisible();
    await page.getByTestId('nt-create').click();
    await expectToast(page, 'success', 'ACME as a template');
    await expect(page.getByTestId('tpl-name')).toHaveText('ACME as a template');
    await expect(page.getByTestId('template-head')).toHaveText('commit 1');
    await page.getByTestId('nav-history').click();
    await expect(page.getByTestId('history-row-1')).toContainText(/ACME plant/);
  });

  test('deployment ← deployment (copy secret values)', async ({ page }) => {
    await fresh(page);
    await page.getByTestId('new-deployment').click();
    await page.getByTestId('dep-label').fill('ACME copy');
    await page.getByTestId('dep-src-deployment').click();
    await pickCombo(page, 'dep-deployment', SEED.deployment, 'ACME');
    await expect(page.getByTestId('dep-copy-secrets')).toBeVisible();
    await page.getByTestId('dep-copy-secrets').click();
    await page.getByTestId('dep-save').click();
    await expectToast(page, 'success', 'ACME copy');
    await page.waitForURL(/\/setup$/);
    await expect(page.getByTestId('nav-head')).toHaveText('commit 1');
    await page.getByTestId('nav-history').click();
    await expect(page.getByTestId('history-row-1')).toContainText(/ACME plant/);
    // the copied pg-password (no generate rule) has its value
    const w = await page.evaluate(() => (window as unknown as { __opmMockWrites: { path: string; body: string }[] }).__opmMockWrites.filter((x) => x.path === '/v1/deployments').at(-1)!.body);
    expect(JSON.parse(w)).toMatchObject({ from: { branch: { kind: 'deployment', deploymentId: 'DepAcmePlant00000001' }, at: 'head' }, copySecretValues: true });
  });

  test('agent branches: table, history drawer, compare with a site', async ({ page }) => {
    await fresh(page);
    await page.goto(D);
    await expect(page.getByTestId('card-agent-branches')).toContainText('3');
    await page.getByTestId('nav-branches').click();
    await expect(page.getByTestId('ab-state-app')).toContainText('1 ahead');
    await expect(page.getByTestId('ab-drift-app')).toContainText('2 changes made on the server');
    await expect(page.getByTestId('ab-needs-db')).toBeVisible();
    await page.getByTestId('ab-history-app').click();
    await expect(page.getByTestId('ab-history-drawer')).toBeVisible();
    await expect(page.getByTestId('history-row-2')).toContainText('LOG_LEVEL=debug');
    await page.keyboard.press('Escape');
    await page.getByTestId('ab-compare-app').click();
    await expect(page).toHaveURL(/compare\?source=agent/);
    await expect(page.getByTestId('compare-agent-banner')).toBeVisible();
    await expect(page.locator('[data-testid^="merge-item-"]').filter({ hasText: /LOG_LEVEL/ })).toHaveCount(1);
  });

  test('old Diff / Template sync URLs redirect to Compare', async ({ page }) => {
    await fresh(page);
    await page.goto(`${D}/diff`);
    await expect(page).toHaveURL(/\/compare\?review=working$/);
    await expect(page.getByText('Nothing to review')).toBeVisible();
    await page.goto(`${D}/template-sync`);
    await expect(page).toHaveURL(new RegExp(`/compare\\?source=template%3A${SEED.template}$`));
  });
});
