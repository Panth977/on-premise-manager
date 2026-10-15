/**
 * §21 #105 one action: Commit (mock). No Save button in any config editor; several forms feed ONE browser change set
 * committed once; a reload offers to restore the set (secret values are never stored); a head that moved behind the
 * browser's back → Review & rebase (clean) and the RebaseDialog (conflict); a 409 at Commit rebases and commits again.
 */
import { expect, test, type Page } from '@playwright/test';
import { login, SEED, expectToast, clearDrafts, readDrafts } from './helpers';

const D = `/app/deployments/${SEED.deployment}`;
const T = `/app/templates/${SEED.template}`;

async function fresh(page: Page, next = '/app/deployments') {
  await login(page, 'admin', '/app/deployments');
  await page.evaluate(() => {
    (window as unknown as { __opmMockReset: () => void }).__opmMockReset();
  });
  await clearDrafts(page);
  await page.waitForTimeout(150);
  await page.goto(next);
}
/** in-app navigation (a full page.goto reloads the app: the change set would come back only through the restore banner) */
async function nav(page: Page, path: string) {
  await page.evaluate((p) => {
    const a = document.createElement('a');
    a.href = p;
    document.body.appendChild(a);
    a.click();
    a.remove();
  }, path);
  await page.waitForURL((u) => u.pathname + u.search === path);
}

/** a commit made "by someone else" through the API (the open page does not know it): set one variable value */
async function commitBehind(page: Page, slug: string, value: string, message = 'Someone else') {
  await page.waitForFunction(() => typeof (window as unknown as { __opmMockFetch?: unknown }).__opmMockFetch === 'function');
  const status = await page.evaluate(
    async ({ dep, slug, value, message }) => {
      const f = (window as unknown as { __opmMockFetch: typeof fetch }).__opmMockFetch;
      const h = { Authorization: 'Bearer mock:uAdmin00000000000001', 'Content-Type': 'application/json' };
      const d = await (await f(`${location.origin}/v1/deployments/${dep}`, { headers: h })).json();
      const cfg = d.draft;
      const config = {
        variables: cfg.variables.map((v: { variableSlug: string; value: unknown }) => (v.variableSlug === slug ? { ...v, value } : v)),
        files: cfg.files,
        sites: cfg.sites,
        secrets: cfg.secrets.map(({ isSet: _i, rotatedAt: _r, ...s }: { isSet: boolean; rotatedAt: unknown }) => s),
      };
      const r = await f(`${location.origin}/v1/deployments/${dep}/commits`, { method: 'POST', headers: h, body: JSON.stringify({ message, baseCommitId: d.branch.head.commitId, config }) });
      await new Promise((ok) => setTimeout(ok, 120)); // the mock persists its DB with a short debounce
      return r.status;
    },
    { dep: SEED.deployment, slug, value, message },
  );
  expect(status).toBe(201);
}

async function commitNow(page: Page, message: string, toast: RegExp | string = /Commit \d+ recorded/) {
  await page.getByTestId('change-bar-commit').click();
  const dlg = page.getByTestId('commit-dialog');
  await expect(dlg).toBeVisible();
  await dlg.getByTestId('commit-message').fill(message);
  await dlg.getByTestId('commit-submit').click();
  await expectToast(page, 'success', toast);
}

const SAVE_TEXT = /^\s*Save( draft| template| value| content)?\s*$/;

test.describe('§21 #105 commit only', () => {
  test('no Save button anywhere in the template and deployment config editors', async ({ page }) => {
    await fresh(page);
    const pages = [
      ...[T, D].flatMap((b) => [`${b}/variables`, `${b}/secrets`, `${b}/files`, `${b}/sites`, `${b}/sites/app/containers`, `${b}/sites/app/edge`, `${b}/sites/app/volumes`, `${b}/sites/app/settings`, `${b}/sites/app/devtools`]),
      `${T}/sites/app/containers/hello-web`,
      `${D}/sites/app/containers/hello-web`,
    ];
    for (const p of pages) {
      await page.goto(p);
      await expect(page.getByTestId('change-bar')).toBeVisible();
      await expect(page.getByRole('button', { name: SAVE_TEXT })).toHaveCount(0);
      for (const id of ['save-site', 'save-draft', 'save-container', 'vc-save']) await expect(page.getByTestId(id)).toHaveCount(0);
    }
    // the bar: N changes (not committed) · Discard · Review · Commit…
    await page.goto(`${D}/variables`);
    await expect(page.getByTestId('change-bar')).toContainText('No changes — everything is committed.');
    await expect(page.getByTestId('change-bar-commit')).toBeDisabled();
  });

  test('several forms → ONE commit (variable drawer, container env, edge port, secret value)', async ({ page }) => {
    await fresh(page, `${D}/variables`);
    const head0 = Number((await page.getByTestId('nav-head').innerText()).match(/\d+/)![0]);
    // 1 a variable through the drawer (Apply)
    await page.getByTestId('add-variable').click();
    await page.getByTestId('var-label').fill('Message of the day');
    await page.getByTestId('var-save').click();
    await expectToast(page, 'success', /applied|added/);
    await expect(page.getByTestId('change-bar-count')).toContainText('1 change (not committed)');
    // 2 a container env row in the container editor (Apply)
    await nav(page, `${D}/sites/app/containers/hello-web`);
    await page.getByTestId('add-env').click();
    await page.getByTestId('env-name-2').fill('LOG_LEVEL');
    await page.getByTestId('env-value-x2-input').fill('debug');
    await page.getByTestId('apply-container').click();
    await expect(page).toHaveURL(/\/sites\/app\/containers$/);
    await expect(page.getByTestId('change-bar-count')).toContainText('2 changes');
    // 3 an edge setting on the site page (in place)
    await nav(page, `${D}/sites/app/edge`);
    await page.getByTestId('edge-http').fill('8085');
    await page.getByTestId('edge-http').blur();
    await expect(page.getByTestId('change-bar-count')).toContainText('3 changes');
    // 4 a secret value (memory only)
    await nav(page, `${D}/secrets`);
    await page.getByTestId('set-api-token').click();
    await page.getByTestId('secret-value').fill('typed-secret-123');
    await page.getByTestId('secret-save').click();
    await expect(page.getByTestId('pending-api-token')).toContainText('not committed');
    await expect(page.getByTestId('change-bar-count')).toContainText(/[4-9] changes \(not committed\)/);
    // nothing was written to the server yet
    const writes = await page.evaluate(() => ((window as unknown as { __opmMockWrites?: { path: string }[] }).__opmMockWrites ?? []).map((w) => w.path));
    expect(writes.filter((p) => /draft|commits|templates\/|secrets\//.test(p))).toEqual([]);
    // ONE commit
    await page.getByTestId('change-bar-commit').click();
    const dlg = page.getByTestId('commit-dialog');
    await expect(dlg.getByTestId('commit-changes-toggle')).toContainText(/[4-9] changes/);
    await dlg.getByTestId('commit-message').fill('MOTD, debug logs, edge 8085, new API token');
    await dlg.getByTestId('commit-submit').click();
    await expectToast(page, 'success', `Commit ${head0 + 1} recorded`);
    await expect(page.getByTestId('nav-head')).toHaveText(`commit ${head0 + 1}`);
    await expect(page.getByTestId('change-bar-count')).toHaveCount(0);
    await expect(page.getByTestId('nav-uncommitted')).toHaveCount(0);
    // the commit body carried the typed value once; the history shows every item, the secret by slug only
    const body = await page.evaluate(() => ((window as unknown as { __opmMockWrites?: { path: string; body: string }[] }).__opmMockWrites ?? []).filter((w) => w.path.endsWith('/commits')).map((w) => JSON.parse(w.body)));
    expect(body).toHaveLength(1);
    expect(body[0].baseCommitId).toBeTruthy();
    await nav(page, `${D}/history`);
    await expect(page.getByTestId(`history-row-${head0 + 1}`)).toContainText('MOTD, debug logs');
    await page.getByTestId(`history-row-${head0 + 1}`).click();
    const detail = page.getByTestId('history-detail');
    for (const t of ['message-of-the-day', 'LOG_LEVEL', 'httpPort', 'api-token']) await expect(detail).toContainText(t);
    await expect(detail).not.toContainText('typed-secret-123');
  });

  test('reload restores the browser change set (secret values are re-entered)', async ({ page }) => {
    await fresh(page, `${D}/variables`);
    await page.getByTestId('val-greeting').fill('Hello after reload');
    await page.getByTestId('val-greeting').blur();
    await nav(page, `${D}/secrets`);
    await page.getByTestId('set-api-token').click();
    await page.getByTestId('secret-value').fill('never-stored-456');
    await page.getByTestId('secret-save').click();
    await expect(page.getByTestId('change-bar-count')).toContainText('2 changes');
    await page.waitForTimeout(400);
    const stored = JSON.stringify(await readDrafts(page)); // §21 #107 drafts live in IndexedDB
    expect(stored).toContain('Hello after reload');
    expect(stored).not.toContain('never-stored-456');
    await page.reload();
    const banner = page.getByTestId('pending-banner');
    await expect(banner).toHaveAttribute('data-state', 'same');
    await expect(banner.getByTestId('pending-banner-dropped')).toContainText('api-token');
    await banner.getByTestId('pending-banner-restore').click();
    await expectToast(page, 'success', /restored/);
    await nav(page, `${D}/variables`);
    await expect(page.getByTestId('val-greeting')).toHaveValue('Hello after reload');
    await expect(page.getByTestId('change-bar-count')).toContainText('1 change');
    // Discard drops it from memory and storage
    await page.getByTestId('change-bar-discard').click();
    await page.getByTestId('pending-discard-dialog').getByTestId('confirm-ok').click();
    await expect(page.getByTestId('change-bar-count')).toHaveCount(0);
    await page.reload();
    await expect(page.getByTestId('pending-banner')).toHaveCount(0);
  });

  test('§21 #107 a draft made on an older head is discarded at load (notice); a conflict at Commit opens the RebaseDialog', async ({ page }) => {
    await fresh(page, `${D}/variables`);
    await page.getByTestId('val-greeting').fill('Mine');
    await page.getByTestId('val-greeting').blur();
    await page.waitForTimeout(400);
    expect((await readDrafts(page)).length).toBe(1);
    // someone commits meanwhile → after a reload the draft is NOT offered: discarded with a notice
    await commitBehind(page, 'theme', 'blue', 'Blue theme (other person)');
    await page.reload();
    const banner = page.getByTestId('pending-banner');
    await expect(banner).toHaveAttribute('data-state', 'stale');
    await expect(banner.getByTestId('pending-banner-notice')).toContainText('so it was discarded');
    await expect(banner.getByTestId('pending-banner-restore')).toHaveCount(0);
    await expect(page.getByTestId('var-row-theme')).toContainText('blue');
    await expect(page.getByTestId('val-greeting')).not.toHaveValue('Mine');
    expect(await readDrafts(page)).toEqual([]);
    await banner.getByTestId('pending-banner-dismiss').click();
    await expect(banner).toHaveCount(0);
    // in the session: someone commits the SAME variable → 409 at Commit → RebaseDialog (Take mine) → commit
    await page.getByTestId('val-greeting').fill('Mine');
    await page.getByTestId('val-greeting').blur();
    await commitBehind(page, 'greeting', 'Theirs', 'Greeting (other person)');
    await page.getByTestId('change-bar-commit').click();
    const dlg = page.getByTestId('commit-dialog');
    await dlg.getByTestId('commit-message').fill('My greeting');
    await dlg.getByTestId('commit-submit').click();
    const rd = page.getByTestId('rebase-dialog');
    await expect(rd).toBeVisible();
    await expect(rd.getByTestId('rebase-item-variables/greeting/value')).toContainText('Theirs');
    await rd.getByTestId('rebase-resolve-variables/greeting/value').getByText('Take mine').click();
    await rd.getByTestId('rebase-dialog-apply').click();
    await expect(page.getByTestId('val-greeting')).toHaveValue('Mine');
    await dlg.getByTestId('commit-message').fill('My greeting after the rebase');
    await dlg.getByTestId('commit-submit').click();
    await expectToast(page, 'success', /Commit \d+ recorded/);
  });

  test('409 at Commit: rebased onto the new head, then committed', async ({ page }) => {
    await fresh(page, `${D}/variables`);
    await page.getByTestId('val-greeting').fill('Committed after a 409');
    await page.getByTestId('val-greeting').blur();
    await commitBehind(page, 'theme', 'red', 'Red theme (other person)');
    await page.getByTestId('change-bar-commit').click();
    const dlg = page.getByTestId('commit-dialog');
    await dlg.getByTestId('commit-message').fill('Greeting');
    await dlg.getByTestId('commit-submit').click();
    await expect(dlg.getByTestId('commit-info')).toContainText(/rebased onto commit \d+/i);
    await dlg.getByTestId('commit-message').fill('Greeting');
    await dlg.getByTestId('commit-submit').click();
    await expectToast(page, 'success', /Commit \d+ recorded/);
    await expect(page.getByTestId('var-row-theme')).toContainText('red');
    await expect(page.getByTestId('val-greeting')).toHaveValue('Committed after a 409');
  });

  test('template: edits + one commit; details are saved apart (Save details)', async ({ page }) => {
    await fresh(page, `${T}/variables`);
    await page.getByTestId('add-variable').click();
    await page.getByTestId('var-label').fill('Region');
    await page.getByTestId('var-save').click();
    await expect(page.getByTestId('change-bar-count')).toContainText('1 change');
    await commitNow(page, 'Region variable');
    await nav(page, T);
    await expect(page.getByTestId('tpl-save')).toHaveText('Save details');
  });
});
