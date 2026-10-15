/**
 * §21 #107 — one Commit = one transaction including the bytes. File contents and volume content are pending changes
 * of the browser change set (bytes in memory + IndexedDB); nothing is uploaded before Commit; Commit stages the new
 * blobs (only those the store lacks), then commits with the stageId; a failure keeps everything; Discard clears it.
 */
import { test, expect, type Page } from '@playwright/test';
import { login, expectToast, nav, readDrafts, readDraftBlobs, clearDrafts, setLastAlive, SEED } from './helpers';

const TPL = '/app/templates/TplNodeRedContent007';
const CONTENT = `${TPL}/sites/flows/volumes/content?volume=nodered-data`;
const node = (page: Page, p: string) => page.locator(`[data-testid="vc-node"][data-path="${p}"]`);

type StageLog = { op: string; stageId: string; sha?: string; size?: number; requested?: number; missing?: number };
const stageState = (page: Page) => page.evaluate(() => (window as unknown as { __opmMockStages: () => { open: { stageId: string; blobs: string[] }[]; log: StageLog[] } }).__opmMockStages());
/** pending-bytes records in IndexedDB (drafts database, store blobs) */
const idbCount = async (page: Page) => (await readDraftBlobs(page)).length;
const writes = (page: Page, re: RegExp) =>
  page.evaluate((src) => ((window as unknown as { __opmMockWrites?: { method: string; path: string }[] }).__opmMockWrites ?? []).filter((w) => new RegExp(src).test(w.path)).map((w) => `${w.method} ${w.path}`), re.source);

async function addNotesFile(page: Page) {
  await page.getByTestId('add-file').click();
  await page.getByTestId('file-label').fill('Notes');
  await expect(page.getByTestId('file-slug')).toHaveValue('notes');
  await page.getByTestId('file-save').click();
  await expect(page.getByTestId('file-row-notes')).toBeVisible();
}
async function typeFileContent(page: Page, slug: string, text: string) {
  await page.getByTestId(`edit-content-${slug}`).click();
  const d = page.getByTestId('file-content-drawer');
  await expect(d).toBeVisible();
  await expect(d.getByTestId('save-content')).toHaveCount(0); // no Save / Apply: edits are live
  await d.getByTestId('file-content-content').click();
  await page.keyboard.press('ControlOrMeta+a');
  await page.keyboard.insertText(text);
  await expect(d.getByTestId('content-state')).toContainText('changed — not committed');
  await d.getByTestId('close-content').click();
  await expect(d).toBeHidden();
}
async function editFlows(page: Page, extra: string) {
  await node(page, 'flows.json').click();
  await expect(page.getByTestId('vc-open-path')).toHaveText('flows.json');
  await page.getByTestId('vc-code-content').click();
  await page.keyboard.press('ControlOrMeta+End');
  await page.keyboard.insertText(extra);
  await expect(page.getByTestId('vc-changed')).toContainText('1 file not committed');
}

test.describe('§21 #107 commit staging', () => {
  test('content edits are pending; one Commit uploads then commits; reload restores the bytes; a failure keeps everything', async ({ page }) => {
    await login(page, 'admin', `${TPL}/files`);
    await addNotesFile(page);
    await typeFileContent(page, 'notes', 'hello notes 107');

    await nav(page, CONTENT);
    await expect(page.getByTestId('volume-content-page')).toBeVisible();
    await expect(page.getByTestId('vc-apply')).toHaveCount(0);
    await expect(page.getByTestId('vc-discard')).toHaveCount(0);
    await editFlows(page, '\n// edited by #107');
    await expect(page.getByTestId('vc-open-dirty')).toHaveText('not committed');
    // the change bar counts the file + the volume file; nothing went to the server
    const bar = page.getByTestId('change-bar');
    await expect(bar).toBeVisible();
    await expect.poll(async () => Number(await bar.getAttribute('data-count'))).toBeGreaterThanOrEqual(2);
    expect(await writes(page, /^\/v1\/(blobs|stages|uploads)/)).toEqual([]);
    await expect.poll(() => idbCount(page)).toBe(2);

    // reload → Restore → the bytes come back from IndexedDB (the store does not have them)
    await page.waitForTimeout(400);
    await page.reload();
    await page.getByTestId('pending-banner-restore').click();
    await expect(node(page, 'flows.json')).toBeVisible();
    await node(page, 'flows.json').click();
    await expect(page.getByTestId('vc-code-content')).toContainText('// edited by #107');

    // a failed commit (after the blobs were promoted) keeps every change, bytes included
    await page.evaluate(() => localStorage.setItem('opm.mock.commitFault', 'after-promote'));
    await page.getByTestId('change-bar-commit').click();
    const dlg = page.getByTestId('commit-dialog');
    await expect(dlg.getByTestId('commit-upload-note')).toContainText('2 files');
    await dlg.getByTestId('commit-message').fill('Notes + flows (#107)');
    await dlg.getByTestId('commit-submit').click();
    await expect(dlg.getByTestId('commit-error')).toContainText('mock fault');
    let st = await stageState(page);
    expect(st.open).toEqual([]); // the stage was dropped
    expect(st.log.filter((l) => l.op === 'put').length).toBe(2);
    await expect.poll(() => idbCount(page)).toBe(2);
    await expect(bar).toHaveAttribute('data-count', /[2-9]/);

    // retry: ONE commit uploads (only what the store lacks) and commits
    await page.evaluate(() => localStorage.removeItem('opm.mock.commitFault'));
    await dlg.getByTestId('commit-submit').click();
    await expectToast(page, 'success', /Commit \d+ recorded/);
    st = await stageState(page);
    const creates = st.log.filter((l) => l.op === 'create');
    expect(creates.length).toBe(2);
    expect(creates[0]!.requested).toBe(2); // only the new blobs (the 4 unchanged files are not even asked about)
    expect(creates[1]!.missing).toBe(0); // the failed attempt had promoted them: nothing re-uploaded
    expect(st.open).toEqual([]);
    expect(await writes(page, /^\/v1\/blobs/)).toEqual([]); // no direct uploads any more
    await expect.poll(() => idbCount(page)).toBe(0);
    await expect(page.getByTestId('vc-changed')).toHaveCount(0);
    // the committed content is served by the store
    await page.reload();
    await node(page, 'flows.json').click();
    await expect(page.getByTestId('vc-code-content')).toContainText('// edited by #107');
  });

  test('upload progress is shown and Discard clears changes + bytes', async ({ page }) => {
    await login(page, 'admin', CONTENT);
    await expect(page.getByTestId('volume-content-page')).toBeVisible();
    await editFlows(page, '\n// to be discarded');
    await expect.poll(() => idbCount(page)).toBe(1);
    await page.getByTestId('change-bar-discard').click();
    await page.getByTestId('pending-discard-dialog').getByTestId('confirm-ok').click();
    await expect(page.getByTestId('vc-changed')).toHaveCount(0);
    await expect(page.getByTestId('vc-code-content')).not.toContainText('// to be discarded');
    await expect.poll(() => idbCount(page)).toBe(0);
    await expect.poll(async () => (await readDrafts(page)).length).toBe(0);

    // progress: a slower mock shows "Uploading 1 of 1 file" in the dialog
    await editFlows(page, '\n// progress');
    await page.evaluate(() => localStorage.setItem('opm.mock.latency', '700'));
    await page.getByTestId('change-bar-commit').click();
    const dlg = page.getByTestId('commit-dialog');
    await dlg.getByTestId('commit-message').fill('progress');
    await dlg.getByTestId('commit-submit').click();
    await expect(dlg.getByTestId('commit-progress')).toContainText(/Uploading 0 of 1 file|Uploading 1 of 1 file|Committing/);
    await expectToast(page, 'success', /Commit \d+ recorded/);
  });

  test('drafts: a reload offers Restore incl. the bytes; Discard on the prompt drops draft + bytes', async ({ page }) => {
    await login(page, 'admin', CONTENT);
    await clearDrafts(page);
    await page.reload();
    await editFlows(page, '\n// draft 1');
    await expect.poll(async () => (await readDrafts(page)).map((d) => `${d.branch}:${d.count}`)).toEqual(['template:TplNodeRedContent007:1']);
    const d = (await readDrafts(page))[0]!;
    expect(d.user).toBe(SEED.uidAdmin);
    expect(d.blobs?.length).toBe(1);
    await page.reload();
    const banner = page.getByTestId('pending-banner');
    await expect(banner).toContainText('unsaved changes');
    await banner.getByTestId('pending-banner-discard').click();
    await expect(banner).toHaveCount(0);
    await expect.poll(async () => (await readDrafts(page)).length).toBe(0);
    await expect.poll(() => idbCount(page)).toBe(0);
    await node(page, 'flows.json').click();
    await expect(page.getByTestId('vc-code-content')).not.toContainText('// draft 1');
  });

  test('drafts expire 1 h after the console was last open; an open tab (heartbeat) keeps them', async ({ page, context }) => {
    await login(page, 'admin', CONTENT);
    await editFlows(page, '\n// draft expiry');
    await expect.poll(async () => (await readDrafts(page)).length).toBe(1);
    // close the console (no tab with the app open), the heartbeat says it was last open 2 h ago
    const other = await context.newPage();
    await page.close();
    await other.goto('/nope-107'); // the error page: no app layout, no heartbeat
    await setLastAlive(other, new Date(Date.now() - 2 * 3600_000).toISOString());
    await other.goto(CONTENT);
    await expect(other.getByTestId('volume-content-page')).toBeVisible();
    await expect(other.getByTestId('pending-banner')).toHaveCount(0);
    expect(await readDrafts(other)).toEqual([]);
    expect(await idbCount(other)).toBe(0);
  });

  test('drafts: a long-open tab keeps its draft past 1 h (heartbeat)', async ({ page }) => {
    await page.clock.install();
    await login(page, 'admin', CONTENT);
    await editFlows(page, '\n// kept by the heartbeat');
    await expect.poll(async () => (await readDrafts(page)).length).toBe(1);
    const alive = async () => (await page.evaluate(() => new Promise<string | null>((res) => {
      const rq = indexedDB.open('opm-drafts', 1);
      rq.onsuccess = () => {
        const g = rq.result.transaction('meta', 'readonly').objectStore('meta').get('alive:cloud');
        g.onsuccess = () => res((g.result as { lastAlive?: string } | undefined)?.lastAlive ?? null);
      };
    })))!;
    const before = Date.parse(await alive());
    await page.clock.runFor(2 * 3600_000); // 2 h with the tab open: the heartbeat keeps writing
    await expect.poll(async () => Date.parse(await alive()) - before).toBeGreaterThan(3600_000);
    await page.reload();
    await expect(page.getByTestId('pending-banner')).toContainText('unsaved changes');
    await page.getByTestId('pending-banner-restore').click();
    await node(page, 'flows.json').click();
    await expect(page.getByTestId('vc-code-content')).toContainText('// kept by the heartbeat');
  });

  test('drafts are per account: another account signing in clears the previous one, logout clears its own', async ({ page, context }) => {
    await login(page, 'admin', CONTENT);
    await editFlows(page, '\n// admin draft');
    await expect.poll(async () => (await readDrafts(page)).length).toBe(1);
    // switch account without logging out (e.g. the session of another person in this browser)
    const other = await context.newPage();
    await page.close();
    await other.goto('/nope-107');
    await other.evaluate((uid) => localStorage.setItem('opm.mock.session', uid), SEED.uidOps);
    await other.goto('/app/templates');
    await expect.poll(async () => (await readDrafts(other)).length).toBe(0);
    expect(await idbCount(other)).toBe(0);
  });
});
