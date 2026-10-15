import { test, expect, type Page } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import { login, pick, expectToast, nav, commitAll } from './helpers';

/** Round VC (§21 #101): volume content workspace (template + deployment), persist to cloud, cloud copy, diff, legacy convert. */
const TPL = '/app/templates/TplNodeRedContent007/sites/flows';
const DEP = '/app/deployments/DepNodeRedFlows00003';
const CONTENT = `${TPL}/volumes/content?volume=nodered-data`;
const LEGACY = '/app/templates/TplLegacySeeds000008/sites/site-01';
const CLOUD = `${DEP}/sites/flows/volumes/content?volume=nodered-data&tab=cloud`;

const node = (page: Page, p: string) => page.locator(`[data-testid="vc-node"][data-path="${p}"]`);
async function askName(page: Page, button: string, name: string) {
  await page.getByTestId(button).click();
  const m = page.getByTestId('vc-name-modal');
  await m.getByTestId('vc-name-input').fill(name);
  await m.getByTestId('vc-name-ok').click();
  await expect(m).toHaveCount(0);
}
/** the last JSON write to a path (window.__opmMockWrites) */
async function lastWrite(page: Page, re: RegExp): Promise<{ method: string; path: string; body: string } | null> {
  return page.evaluate((src) => {
    const w = (window as unknown as { __opmMockWrites?: { method: string; path: string; body: string }[] }).__opmMockWrites ?? [];
    const r = new RegExp(src);
    return [...w].reverse().find((x) => r.test(x.path)) ?? null;
  }, re.source);
}

test.describe('round VC: volume content', () => {
  test('editor: create, edit, rename, move (drag + Move to…), duplicate, delete, upload files + a folder, save', async ({ page }, info) => {
    await login(page, 'admin', `${TPL}/volumes`);
    await expect(page.getByTestId('volume-seed-nodered-data')).toHaveText('5 files');
    await expect(page.getByTestId('volume-persist-nodered-data')).toBeVisible();
    await page.getByTestId('vol-content-nodered-data').click();
    await expect(page.getByTestId('volume-content-page')).toBeVisible();
    await expect(node(page, 'flows.json')).toBeVisible();

    // new folder + new file inside it (focus = the folder)
    await askName(page, 'vc-new-folder', 'subflows');
    await expect(node(page, 'subflows')).toHaveAttribute('data-dir', 'true');
    await askName(page, 'vc-new-file', 'alarm.json');
    await expect(node(page, 'subflows/alarm.json')).toBeVisible();
    await expect(page.getByTestId('vc-open-path')).toHaveText('subflows/alarm.json');
    // a bad name is refused live
    await page.getByTestId('vc-new-file').click();
    await page.getByTestId('vc-name-input').fill('../x');
    await expect(page.getByTestId('vc-name-modal')).toContainText('"." or ".."');
    await expect(page.getByTestId('vc-name-ok')).toBeDisabled();
    await page.keyboard.press('Escape');

    // edit the new file (CodeMirror, JSON)
    await page.getByTestId('vc-code-content').click();
    await page.keyboard.type('{"alarm": true}');
    await expect(page.getByTestId('vc-open-dirty')).toBeVisible();
    await expect(page.getByTestId('vc-changed')).toBeVisible(); // §21 #107 live: already in the change set

    // rename (toolbar) — the open file follows
    await node(page, 'subflows/alarm.json').click();
    await page.getByTestId('vc-rename').click();
    await page.getByTestId('vc-name-input').fill('alarms.json');
    await page.getByTestId('vc-name-ok').click();
    await expect(node(page, 'subflows/alarms.json')).toBeVisible();
    await expect(page.getByTestId('vc-open-path')).toHaveText('subflows/alarms.json');

    // move by drag and drop: README.md → lib
    await node(page, 'README.md').dragTo(node(page, 'lib'));
    await expect(node(page, 'lib/README.md')).toBeVisible();
    await expect(node(page, 'README.md')).toHaveCount(0);
    // move with "Move to…" (context menu): package.json → subflows
    await node(page, 'package.json').click({ button: 'right' });
    await page.getByTestId('vc-menu-move').click();
    await pick(page, 'vc-move-to', 'subflows');
    await page.getByTestId('vc-move-ok').click();
    await expect(node(page, 'subflows/package.json')).toBeVisible();

    // duplicate (keyboard focus + toolbar), delete with confirm
    await node(page, 'flows.json').click();
    await page.getByTestId('vc-duplicate').click();
    await expect(node(page, 'flows copy.json')).toBeVisible();
    await node(page, 'flows copy.json').click();
    await page.keyboard.press('Delete');
    await page.getByTestId('vc-delete-dialog').getByRole('button', { name: 'Delete' }).click();
    await expect(node(page, 'flows copy.json')).toHaveCount(0);

    // upload two files into lib (one clashes → keep both) and a folder (relative paths kept)
    await node(page, 'lib').click();
    await page.getByTestId('vc-upload-files-input').setInputFiles([
      { name: 'helper.js', mimeType: 'text/javascript', buffer: Buffer.from('module.exports = 2;\n') },
      { name: 'logo.png', mimeType: 'image/png', buffer: Buffer.from([0x89, 0x50, 0x4e, 0x47, 0, 1, 2, 3]) },
    ]);
    await expect(page.getByTestId('vc-conflict-modal')).toContainText('lib/helper.js');
    await page.getByTestId('vc-conflict-rename').click();
    await expect(node(page, 'lib/helper copy.js')).toBeVisible();
    await expect(node(page, 'lib/logo.png')).toBeVisible();
    await node(page, 'lib/logo.png').click();
    await expect(page.getByTestId('vc-binary')).toContainText('binary file');
    const dir = info.outputPath('nodes');
    fs.mkdirSync(path.join(dir, 'inner'), { recursive: true });
    fs.writeFileSync(path.join(dir, 'index.js'), 'module.exports = {};\n');
    fs.writeFileSync(path.join(dir, 'inner', 'deep.txt'), 'deep\n');
    await page.getByTestId('vc-upload-folder-input').setInputFiles(dir);
    await expect(node(page, 'lib/nodes/inner')).toBeVisible();
    await expect(node(page, 'lib/nodes/inner/deep.txt')).toBeVisible(); // uploads reveal their folders
    await expect(node(page, 'lib/nodes/index.js')).toBeVisible();

    // mode + render of a file
    await node(page, 'settings.js').click();
    await expect(page.getByTestId('vc-render')).toHaveAttribute('data-checked', 'true');
    await page.getByTestId('vc-mode').fill('0600');
    await page.getByTestId('vc-mode').press('Enter');

    // §21 #107 no Apply: every edit is already in the change set; ONE Commit stages the new bytes, then commits
    await expect(page.getByTestId('vc-apply')).toHaveCount(0);
    await page.waitForTimeout(400); // the last text edit is applied 300 ms after typing
    await commitAll(page, 'Starter content');
    await expect(page.getByTestId('vc-changed')).toHaveCount(0);
    const w = await lastWrite(page, /\/v1\/templates\/TplNodeRedContent007\/commits$/);
    const body = (JSON.parse(w!.body) as { config: unknown }).config as { sites: { config: { volumes: { seed: { kind: string; entries: { path: string; mode: string; render: string; size: number }[]; dirs: string[] } }[] } }[] };
    const seed = body.sites[0]!.config.volumes[0]!.seed;
    expect(seed.kind).toBe('tree');
    const paths = seed.entries.map((e) => e.path);
    expect(paths).toEqual(expect.arrayContaining(['lib/README.md', 'subflows/package.json', 'subflows/alarms.json', 'lib/helper copy.js', 'lib/logo.png', 'lib/nodes/index.js', 'lib/nodes/inner/deep.txt']));
    expect(paths).not.toContain('flows copy.json');
    expect(seed.entries.find((e) => e.path === 'subflows/alarms.json')!.size).toBe(15);
    expect(seed.entries.find((e) => e.path === 'settings.js')).toMatchObject({ mode: '0600', render: 'expressions' });
    expect(seed.dirs).toEqual(['backups']);
    // reload: the saved content is back
    await page.reload();
    await expect(node(page, 'subflows')).toBeVisible();
    await node(page, 'subflows').click();
    await node(page, 'subflows/alarms.json').click();
    await expect(page.getByTestId('vc-code-content')).toContainText('"alarm": true');
  });

  test('persist settings: toggle, exclude errors + live preview, unsaved guard', async ({ page }) => {
    await login(page, 'admin', `${CONTENT}&tab=persist`);
    const card = page.getByTestId('persist-card');
    await expect(card.getByTestId('persist-toggle')).toHaveAttribute('data-checked', 'true');
    await expect(card.getByTestId('persist-preview-none')).toBeVisible();
    await card.getByTestId('persist-exclude-add').click();
    await card.getByTestId('persist-exclude-4').fill('!lib');
    await expect(card).toContainText('negated patterns');
    await expect(page.getByTestId('change-bar-commit')).toBeDisabled();
    await card.getByTestId('persist-exclude-4').fill('lib/');
    await expect(card.getByTestId('persist-preview-item')).toHaveText(['lib/']);
    await card.getByTestId('persist-exclude-4').fill('*.md');
    await expect(card.getByTestId('persist-preview-item')).toHaveText(['README.md']);
    await expect(page.getByTestId('change-bar-commit')).toBeEnabled();
    // §21 #105 persist settings edit the change set in place: leaving keeps them (no prompt)
    await page.getByRole('link', { name: 'Variables' }).click();
    await expect(page.getByTestId('leave-modal')).toHaveCount(0);
    await expect(page.getByTestId('change-bar-count')).toContainText('1 change');
    await nav(page, `${CONTENT}&tab=persist`);
    // off → the key is deleted
    await page.getByTestId('persist-card').getByTestId('persist-toggle').click();
    await commitAll(page, 'Persist off');
    const w = await lastWrite(page, /\/v1\/templates\/TplNodeRedContent007\/commits$/);
    expect(w!.body).not.toContain('"persist"');
  });

  test('deployment persist card previews the excludes on the latest cloud version; migrated mock templates have no legacy seeds', async ({ page }) => {
    await login(page, 'admin', `${DEP}/sites/flows/volumes/content?volume=nodered-data&tab=persist`);
    const pv = page.getByTestId('persist-preview');
    await expect(pv).toContainText('Excluded in the latest cloud version');
    await page.getByTestId('persist-exclude-add').click();
    await page.getByTestId('persist-exclude-4').fill('extra.js');
    await expect(pv.getByTestId('persist-preview-item')).toHaveText(['lib/extra.js']);
    await page.getByTestId('change-bar-discard').click();
    await page.getByTestId('pending-discard-dialog').getByTestId('confirm-ok').click();
    await nav(page, '/app/templates/TplVolumeKinds000006/sites/site-01/volumes');
    await expect(page.getByTestId('volume-seed-nodered-flow')).toHaveText(/1 file/);
    await expect(page.locator('[data-testid^="volume-seed-legacy-"]')).toHaveCount(0);
  });

  test('cloud copy: snapshot request, edit → new version, 409, send with restart, cancel, restore, use as starter content', async ({ page }) => {
    await login(page, 'admin', CLOUD);
    await expect(page.getByTestId('cloud-sync-badge')).toHaveAttribute('data-state', 'conflict');
    await expect(page.getByTestId('cloud-pending-push')).toContainText('Version #2');
    await page.getByTestId('cloud-tabs').locator('[data-tab="history"]').click();
    await expect(page.getByTestId('cloud-version-row')).toHaveCount(3);
    // diff of #3 vs #2 with a text diff
    await page.locator('[data-testid="cloud-version-row"][data-seq="3"]').getByTestId('cloud-version-diff').click();
    const ch = page.getByTestId('cloud-version-changes');
    await expect(ch.getByTestId('cloud-version-changes-item')).toHaveCount(2);
    await ch.locator('[data-path="flows.json"]').getByTestId('cloud-version-changes-toggle').click();
    await expect(ch.getByTestId('diff-line-add').first()).toBeVisible();

    // Snapshot now → the (mock) site answers with version #4 (manual-cloud)
    await page.getByTestId('cloud-snapshot').click();
    await expect(page.getByTestId('cloud-snapshot-requested')).toBeVisible();
    await expect(page.getByTestId('cloud-snapshot-requested')).toHaveCount(0, { timeout: 15_000 });
    // same tree as #3 → no new version; the request is answered
    await expect(page.getByTestId('cloud-version-row')).toHaveCount(3);

    // edit the latest → Save as new version (#4)
    await page.getByTestId('cloud-tabs').locator('[data-tab="latest"]').click();
    await node(page, 'lib').click();
    await node(page, 'lib/extra.js').click();
    await page.getByTestId('vc-code-content').click();
    await page.keyboard.press('ControlOrMeta+End');
    await page.keyboard.type('// edited in the cloud\n');
    await page.getByTestId('cloud-note').fill('client hook tweak');
    await page.getByTestId('cloud-save').click();
    await expectToast(page, 'success', 'Version 4 saved');
    await expect(page.getByTestId('cloud-tabs')).toContainText('Latest version (#4)');

    // 409: someone else saves a version meanwhile
    await node(page, 'package.json').click();
    await page.getByTestId('vc-code-content').click();
    await page.keyboard.type(' ');
    await page.evaluate(async () => {
      const f = (window as unknown as { __opmMockFetch: typeof fetch }).__opmMockFetch;
      const base = 'http://localhost/v1/deployments/DepNodeRedFlows00003/volume-content/flows/nodered-data';
      const h = { Authorization: 'Bearer mock:uAdmin00000000000001', 'Content-Type': 'application/json' };
      const st = await (await f(base, { headers: h })).json();
      const cur = await (await f(`${base}/versions/${st.latest.versionId}`, { headers: h })).json();
      const tree = { entries: cur.tree.entries.filter((e: { path: string }) => e.path !== 'lib/helper.js'), dirs: cur.tree.dirs };
      await f(`${base}/versions`, { method: 'POST', headers: h, body: JSON.stringify({ baseVersionId: st.latest.versionId, tree, note: 'by ops' }) });
    });
    await page.getByTestId('cloud-save').click();
    await expect(page.getByTestId('cloud-newer')).toContainText('#5');
    await page.getByTestId('cloud-compare').click();
    await expect(page.getByTestId('compare-list').locator('[data-path="package.json"]')).toContainText('changed');
    await expect(page.getByTestId('compare-list').locator('[data-path="lib/helper.js"]')).toContainText('added');
    await page.keyboard.press('Escape');
    await page.getByTestId('cloud-reload').click();
    await expect(page.getByTestId('cloud-newer')).toHaveCount(0);
    await expect(page.getByTestId('cloud-tabs')).toContainText('Latest version (#5)');

    // send with restart (agent 0.11.1 → warning)
    await page.getByTestId('cloud-send').click();
    const m = page.getByTestId('send-modal');
    await expect(m.getByTestId('send-restart')).toBeChecked();
    await expect(m.getByTestId('send-agent-warning')).toContainText('0.12.0');
    await expect(m.getByTestId('send-supersedes')).toContainText('#2');
    await m.getByTestId('send-note').fill('please apply after the shift');
    await m.getByTestId('send-submit').click();
    await expectToast(page, 'success', 'Version #5 sent to flows');
    await expect(page.getByTestId('cloud-pending-push')).toContainText('Version #5');
    const sent = await lastWrite(page, /\/push$/);
    expect(JSON.parse(sent!.body)).toMatchObject({ restart: true, note: 'please apply after the shift' });
    // cancel the pending push
    await page.getByTestId('cloud-cancel-push').click();
    await page.getByTestId('cancel-push-dialog').getByRole('button', { name: 'Cancel push' }).click();
    await expect(page.getByTestId('cloud-pending-push')).toHaveCount(0);
    await expect(page.getByTestId('cloud-last-push')).toContainText('cancelled');

    // restore #1 → #6
    await page.getByTestId('cloud-tabs').locator('[data-tab="history"]').click();
    await page.locator('[data-testid="cloud-version-row"][data-seq="1"]').getByTestId('cloud-version-restore').click();
    await page.getByTestId('restore-note').fill('back to the first');
    await page.getByTestId('restore-dialog').getByRole('button', { name: 'Restore' }).click();
    await expectToast(page, 'success', 'Version 1 restored as version 6');
    await expect(page.locator('[data-testid="cloud-version-row"][data-seq="6"]')).toContainText('Restored from #1');

    // use #3 as starter content → the draft's seed = #3's files, draft saved
    await page.getByTestId('cloud-tabs').locator('[data-tab="history"]').click();
    await page.locator('[data-testid="cloud-version-row"][data-seq="3"]').getByTestId('cloud-version-starter').click();
    await page.getByTestId('starter-dialog').getByRole('button', { name: 'Use as starter content' }).click();
    await expectToast(page, 'success', 'Starter content = version #3');
    await expect(node(page, 'lib')).toBeVisible();
    await node(page, 'lib').click();
    await expect(node(page, 'lib/extra.js')).toBeVisible();
    // §21 #105 the starter content is a change in the browser until Commit
    await commitAll(page, 'Starter content from cloud version 3');
    const put = await lastWrite(page, /\/commits$/);
    expect(put!.body).toContain('lib/extra.js');
  });

  test('compare · uncommitted: per-file content changes with a text diff; commit dialog warns about agent < 0.12', async ({ page }) => {
    // §21 #105: the content change is made in this browser (the seed's old working copy was auto-committed)
    await login(page, 'admin', `${DEP}/sites/flows/volumes/content?volume=nodered-data`);
    await node(page, 'flows.json').click();
    await page.getByTestId('vc-code-content').click();
    await page.keyboard.press('ControlOrMeta+a');
    await page.keyboard.type('[{"id":"t","label":"Plant integration (v3)"}]');
    await page.keyboard.press('Escape');
    await expect(page.getByTestId('vc-changed')).toBeVisible(); // §21 #107 applied as you type
    await page.getByTestId('review-changes').click();
    await expect(page).toHaveURL(/compare\?review=working/);
    const items = page.locator('[data-testid^="merge-item-"]');
    const flows = items.filter({ hasText: 'flows.json' });
    await expect(flows).toHaveCount(1);
    const key = (await flows.getAttribute('data-testid'))!.slice('merge-item-'.length);
    await page.getByTestId(`merge-expand-${key}`).click();
    const td = page.getByTestId(`merge-textdiff-${key}`);
    await expect(td).toContainText('Plant integration (v3)');
    await expect(page.getByTestId('content-agent-banner')).toContainText('0.12.0');
    await page.getByTestId('change-bar-commit').click();
    await expect(page.getByTestId('commit-dialog').getByTestId('commit-warning').filter({ hasText: 'Site flows runs agent 0.11.1' })).toHaveCount(1);
  });

  test('legacy File seeds: error card → Convert to own content (folder + single file + nested File), then save', async ({ page }) => {
    await login(page, 'admin', `${LEGACY}/volumes`);
    await expect(page.getByTestId('volume-seed-legacy-nodered-data')).toBeVisible();
    await page.getByTestId('vol-edit-nodered-data').click();
    await expect(page.getByTestId('vm-seed-legacy')).toContainText('Files are static');
    await page.getByTestId('vm-seed-legacy-link').click();
    await expect(page.getByTestId('vc-legacy')).toContainText('flows.json ← nodered-flow');
    await page.getByTestId('vc-convert').click();
    const lines = page.getByTestId('vc-convert-lines');
    await expect(lines).toContainText('content flows.json ← content of file nodered-flow');
    await expect(lines).toContainText('moved into the volume\'s content as lib/defaults.js');
    await expect(lines).toContainText('volume nodered-settings (single file) ← content of file nodered-settings');
    await page.getByTestId('vc-convert-ok').click();
    await expect(page.getByTestId('vc-legacy')).toHaveCount(0);
    await node(page, 'flows.json').click();
    await expect(page.getByTestId('vc-code-content')).toContainText('[');
    await expect(node(page, 'lib')).toBeVisible();
    await commitAll(page, 'Convert legacy seeds');
    const w = await lastWrite(page, /\/v1\/templates\/TplLegacySeeds000008\/commits$/);
    expect(w!.body).toContain('"kind":"tree"');
    expect(w!.body).toContain('"kind":"single"');
    expect(w!.body).not.toContain('"kind":"files"');
    // the single-file volume: one editor
    await nav(page, `${LEGACY}/volumes/content?volume=nodered-settings`);
    await expect(page.getByTestId('vc-open-path')).toHaveText('nodered-settings');
    await expect(page.getByTestId('vc-code-content')).toContainText('module.exports');
    await expect(page.getByTestId('vc-seed-summary')).toContainText('1 file');
  });

  test('container page: a File inside a writable volume → V_FILE_IN_WRITABLE_VOLUME + "Move into the volume\'s content"', async ({ page }) => {
    await login(page, 'admin', `${LEGACY}/containers/node-red`);
    const issue = page.locator('[data-testid="extra-1-issue"][data-code="V_FILE_IN_WRITABLE_VOLUME"]');
    await expect(issue).toContainText('inside the writable volume nodered-data');
    await page.getByTestId('extra-1-move-into').click();
    await expectToast(page, 'success', "Moved into the volume's content");
    await expect(page.locator('[data-code="V_FILE_IN_WRITABLE_VOLUME"]')).toHaveCount(0);
    await expect(page.getByTestId('extra-row-1')).toHaveCount(0);
  });
});
