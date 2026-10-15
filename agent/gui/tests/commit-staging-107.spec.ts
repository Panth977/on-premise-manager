// §21 #107 one Commit = one transaction incl. the bytes. A File content edit is a pending change: its bytes stay in this
// browser (memory + IndexedDB) and NOTHING is uploaded before Commit; Commit stages the new blobs (POST /_opm/api/stages →
// PUT …/stages/{id}/blobs/{sha}) and then commits { message, baseCommitId, config, stageId }. A failed commit keeps every
// change (bytes included); Discard clears the bytes too. Screenshots (OPM_S107_SHOTS=1): screenshots/s107-*-{light,dark}.png
// (390 px: commit-staging-107-mobile.spec.ts).
import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { knobs, login, open, shot, idbBlobs, idbDrafts, setAlive } from './helpers';

const SHOTS = process.env.OPM_S107_SHOTS === '1';
const bar = (page: Page) => page.getByTestId('local-edit-bar');
const NEW_TEXT = 'Welcome to the plant, night shift (#107)\n';

const calls = (page: Page) => page.evaluate(() => [...((window as unknown as { __opmMockCalls?: string[] }).__opmMockCalls ?? [])]);
const resetCalls = (page: Page) => page.evaluate(() => ((window as unknown as { __opmMockCalls?: string[] }).__opmMockCalls = []));
const idb = idbBlobs;

async function editBanner(page: Page, text = NEW_TEXT) {
  await page.getByTestId('edit-banner').click();
  const ed = page.getByTestId('file-drawer').getByTestId('code-editor');
  await expect(ed).toHaveValue('Welcome to the hello-world deployment\n');
  await ed.fill(text);
  // applies as you type (debounced): the change bar counts it while the drawer is still open
  await expect(bar(page)).toContainText('1 change (not committed)');
  await expect(page.getByTestId('file-drawer').getByTestId('file-applied')).toContainText('in this browser until you commit');
}

test('a File content edit is a pending change: no Save / upload button, nothing sent before Commit; one Commit uploads then commits', async ({ page }) => {
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/app/files');
  await resetCalls(page);
  await editBanner(page);
  const drawer = page.getByTestId('file-drawer');
  await expect(drawer.getByRole('button', { name: /^(Save|Apply|Use this content|Upload)\b/ })).toHaveCount(0);
  await expect(drawer.getByTestId('file-revert')).toBeVisible();
  if (SHOTS) await shot(page, 's107-file-edit-light', false);
  await drawer.getByTestId('file-done').click();
  await expect(page.getByTestId('files')).toContainText('changed');
  // nothing reached the agent: no blob upload, no stage
  const before = await calls(page);
  expect(before.filter((c) => /\/_opm\/api\/(blobs|stages)/.test(c))).toEqual([]);
  // the bytes are kept in IndexedDB for this site's change set
  const recs = await idb(page);
  expect(recs).toHaveLength(1);
  expect(recs[0].owner).toMatch(/^agent\|tester@acme\.example\|agent:/);
  expect(recs[0].size).toBe(new TextEncoder().encode(NEW_TEXT).length);
  // Review: the per-file text diff
  await page.getByTestId('review-changes').click();
  await expect(page.getByTestId('pending-file-diff-banner')).toContainText('banner');
  await expect(page.getByTestId('pending-text-diff-banner')).toContainText('night shift (#107)');
  if (SHOTS) await shot(page, 's107-review-light', false);
  await page.getByTestId('review-commit').click();
  const dlg = page.getByTestId('local-commit-dialog');
  await expect(dlg.getByTestId('commit-upload-note')).toContainText('uploaded with this commit');
  await dlg.getByTestId('commit-message').fill('Banner for the night shift');
  if (SHOTS) await shot(page, 's107-commit-light', false);
  await resetCalls(page);
  await dlg.getByTestId('commit-submit').click();
  await expect(page).toHaveURL(/\/branch$/);
  await expect(page.getByTestId('update-result')).toContainText('Applied', { timeout: 40_000 });
  const seq = (await calls(page)).filter((c) => /\/_opm\/api\/(stages|branch\/commit)/.test(c));
  expect(seq[0]).toBe('POST /_opm/api/stages');
  expect(seq[1]).toMatch(/^PUT \/_opm\/api\/stages\/[0-9A-Z]{26}\/blobs\/sha256(:|%3A)[0-9a-f]{64}$/);
  expect(seq[2]).toBe('POST /_opm/api/branch/commit');
  expect(seq).toHaveLength(3);
  await page.getByTestId('branch-tabs-history').click();
  await expect(page.getByTestId('history-row-8')).toContainText('Banner for the night shift', { timeout: 10_000 });
  // committed: the browser keeps nothing (localStorage + IndexedDB)
  await expect.poll(async () => (await idb(page)).length).toBe(0);
  await expect.poll(async () => (await idbDrafts(page)).length).toBe(0);
  // the agent now serves the new content
  await page.getByTestId('side-files').click();
  await page.getByTestId('edit-banner').click();
  await expect(page.getByTestId('file-drawer').getByTestId('code-editor')).toHaveValue(NEW_TEXT);
});

test('a reload restores the change set INCLUDING the bytes; the commit then uploads them', async ({ page }) => {
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/app/files');
  await editBanner(page);
  await page.getByTestId('file-drawer').getByTestId('file-done').click();
  await page.reload();
  await page.getByTestId('actor-email').fill('tester@acme.example');
  await page.getByTestId('actor-continue').click();
  await expect(page.getByTestId('pending-banner')).toBeVisible();
  await page.getByTestId('pending-banner-restore').click();
  await expect(bar(page)).toContainText('1 change (not committed)');
  await page.getByTestId('edit-banner').click();
  await expect(page.getByTestId('file-drawer').getByTestId('code-editor')).toHaveValue(NEW_TEXT);
  await page.getByTestId('file-drawer').getByTestId('file-done').click();
  await page.getByTestId('change-bar-commit').click();
  const dlg = page.getByTestId('local-commit-dialog');
  await dlg.getByTestId('commit-message').fill('Restored banner');
  await dlg.getByTestId('commit-submit').click();
  await expect(page.getByTestId('update-result')).toContainText('Applied', { timeout: 40_000 });
  await page.getByTestId('side-files').click();
  await page.getByTestId('edit-banner').click();
  await expect(page.getByTestId('file-drawer').getByTestId('code-editor')).toHaveValue(NEW_TEXT);
});

test('a failed commit keeps every change (bytes included); the retry commits', async ({ page }) => {
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/app/files');
  await editBanner(page);
  await page.getByTestId('file-drawer').getByTestId('file-done').click();
  await page.evaluate(() => localStorage.setItem('opm.mock.stage.fail', 'commit'));
  await page.getByTestId('change-bar-commit').click();
  const dlg = page.getByTestId('local-commit-dialog');
  await dlg.getByTestId('commit-message').fill('This one fails');
  await dlg.getByTestId('commit-submit').click();
  await expect(dlg.getByTestId('commit-error')).toContainText('nothing was committed');
  if (SHOTS) await shot(page, 's107-commit-failed-light', false);
  await expect(page).toHaveURL(/\/files$/);
  expect(await idb(page)).toHaveLength(1);
  await dlg.getByTestId('commit-cancel').click();
  await expect(bar(page)).toContainText('1 change (not committed)');
  // retry: the knob was one-shot
  await page.getByTestId('change-bar-commit').click();
  await dlg.getByTestId('commit-message').fill('Second try');
  await dlg.getByTestId('commit-submit').click();
  await expect(page.getByTestId('update-result')).toContainText('Applied', { timeout: 40_000 });
  await expect.poll(async () => (await idb(page)).length).toBe(0);
});

test('Discard clears the change set and the stored bytes', async ({ page }) => {
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/app/files');
  await editBanner(page);
  await page.getByTestId('file-drawer').getByTestId('file-done').click();
  expect(await idb(page)).toHaveLength(1);
  await page.getByTestId('discard-local').click();
  await page.getByRole('button', { name: 'Discard' }).last().click();
  await expect(bar(page)).toContainText('No changes');
  await expect.poll(async () => (await idb(page)).length).toBe(0);
  await expect.poll(async () => (await idbDrafts(page)).length).toBe(0);
});

test('typing the committed text back is no change; Revert to committed drops the edit', async ({ page }) => {
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/app/files');
  await editBanner(page);
  const ed = page.getByTestId('file-drawer').getByTestId('code-editor');
  await ed.fill('Welcome to the hello-world deployment\n');
  await expect(bar(page)).toContainText('No changes');
  await ed.fill('x\n');
  await expect(bar(page)).toContainText('1 change (not committed)');
  await page.getByTestId('file-drawer').getByTestId('file-revert').click();
  await expect(bar(page)).toContainText('No changes');
  await expect(ed).toHaveValue('Welcome to the hello-world deployment\n');
});

const reloadAs = async (page: Page, email = 'tester@acme.example') => {
  await page.reload();
  await page.getByTestId('actor-email').fill(email);
  await page.getByTestId('actor-continue').click();
};

test('auto-saved draft: refresh → "unsaved changes … Restore or Discard?"; Discard deletes it', async ({ page }) => {
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/app/files');
  await editBanner(page);
  await expect.poll(async () => (await idbDrafts(page)).length).toBe(1);
  const d = (await idbDrafts(page))[0];
  expect(d.user).toBe('tester@acme.example');
  // no "leave the site?" prompt for drafts: they are saved
  await reloadAs(page);
  const banner = page.getByTestId('pending-banner');
  await expect(banner).toHaveAttribute('data-state', 'same');
  await expect(banner).toContainText('unsaved changes');
  if (SHOTS) await shot(page, 's107-draft-prompt-light', false);
  await page.getByTestId('pending-banner-discard').click();
  await expect(banner).toHaveCount(0);
  await expect(bar(page)).toContainText('No changes');
  await expect.poll(async () => (await idbDrafts(page)).length).toBe(0);
  await expect.poll(async () => (await idbBlobs(page)).length).toBe(0);
});

test('a draft made on another commit is discarded automatically with a notice', async ({ page }) => {
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/app/files');
  await editBanner(page);
  await expect.poll(async () => (await idbDrafts(page)).length).toBe(1);
  // the head moved while the GUI was closed: simulated by moving the draft's base to an older commit
  await page.evaluate(
    () =>
      new Promise<void>((res) => {
        const rq = indexedDB.open('opm-drafts');
        rq.onsuccess = () => {
          const db = rq.result;
          const tx = db.transaction('drafts', 'readwrite');
          const st = tx.objectStore('drafts');
          const g = st.getAll();
          g.onsuccess = () => {
            for (const r of g.result as Record<string, unknown>[]) st.put({ ...r, baseCommitId: '01JAMQCK0000000000000REV04', baseSeq: 4 });
          };
          tx.oncomplete = () => (db.close(), res());
        };
      }),
  );
  await reloadAs(page);
  const banner = page.getByTestId('pending-banner');
  await expect(banner).toHaveAttribute('data-state', 'stale');
  await expect(page.getByTestId('pending-banner-notice')).toContainText('was based on commit 4');
  await expect(page.getByTestId('pending-banner-notice')).toContainText('so it was discarded');
  await expect(page.getByTestId('pending-banner-restore')).toHaveCount(0);
  if (SHOTS) await shot(page, 's107-draft-stale-light', false);
  await expect.poll(async () => (await idbDrafts(page)).length).toBe(0);
  await expect.poll(async () => (await idbBlobs(page)).length).toBe(0);
  await page.getByTestId('pending-banner-dismiss').click();
  await expect(banner).toHaveCount(0);
});

test('drafts expire when the GUI was not open for more than 1 h', async ({ page }) => {
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/app/files');
  await editBanner(page);
  await expect.poll(async () => (await idbDrafts(page)).length).toBe(1);
  // leave the app (the login page does not keep drafts alive), then pretend it was last open 2 h ago
  await page.goto('/_opm/healthz');
  await setAlive(page, new Date(Date.now() - 2 * 3600_000).toISOString());
  await open(page, '/_opm/app/app/files');
  await expect(bar(page)).toContainText('No changes');
  await expect(page.getByTestId('pending-banner')).toHaveCount(0);
  await expect.poll(async () => (await idbDrafts(page)).length).toBe(0);
  await expect.poll(async () => (await idbBlobs(page)).length).toBe(0);
});

test('an open tab keeps its draft alive past 1 h (heartbeat)', async ({ page }) => {
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/app/files');
  await editBanner(page);
  await expect.poll(async () => (await idbDrafts(page)).length).toBe(1);
  // the last heartbeat is 2 h old, but the tab is still open: its next heartbeat (visibility change) renews it
  await setAlive(page, new Date(Date.now() - 2 * 3600_000).toISOString());
  await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
  await reloadAs(page);
  await expect(page.getByTestId('pending-banner')).toHaveAttribute('data-state', 'same');
  await page.getByTestId('pending-banner-restore').click();
  await expect(bar(page)).toContainText('1 change (not committed)');
});

test('drafts are per person: another person in this browser does not see them (and they are removed)', async ({ page }) => {
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/app/files');
  await editBanner(page);
  await expect.poll(async () => (await idbDrafts(page)).length).toBe(1);
  await reloadAs(page, 'night-shift@acme.example');
  await expect(page.getByTestId('actor-chip')).toContainText('night-shift@acme.example');
  await expect(page.getByTestId('pending-banner')).toHaveCount(0);
  await expect(bar(page)).toContainText('No changes');
  await expect.poll(async () => (await idbDrafts(page)).filter((d) => d.user === 'tester@acme.example').length).toBe(0);
});

test('screens · dark', async ({ page }) => {
  test.skip(!SHOTS, 'screenshots only (OPM_S107_SHOTS=1)');
  await page.emulateMedia({ colorScheme: 'dark' });
  await knobs(page, { 'opm.mock.branch.clean': '1' });
  await login(page, '/_opm/app/app/files');
  await editBanner(page);
  await shot(page, 's107-file-edit-dark', false);
  await page.getByTestId('file-drawer').getByTestId('file-done').click();
  await page.getByTestId('review-changes').click();
  await expect(page.getByTestId('pending-text-diff-banner')).toBeVisible();
  await shot(page, 's107-review-dark', false);
  await page.getByTestId('review-commit').click();
  await page.getByTestId('local-commit-dialog').getByTestId('commit-message').fill('Banner for the night shift');
  await shot(page, 's107-commit-dark', false);
  await page.evaluate(() => localStorage.setItem('opm.mock.stage.fail', 'commit'));
  await page.getByTestId('local-commit-dialog').getByTestId('commit-submit').click();
  await expect(page.getByTestId('local-commit-dialog').getByTestId('commit-error')).toBeVisible();
  await shot(page, 's107-commit-failed-dark', false);
  await page.getByTestId('local-commit-dialog').getByTestId('commit-cancel').click();
  await expect.poll(async () => (await idbDrafts(page)).length).toBe(1);
  await reloadAs(page);
  await expect(page.getByTestId('pending-banner')).toBeVisible();
  await shot(page, 's107-draft-prompt-dark', false);
});
