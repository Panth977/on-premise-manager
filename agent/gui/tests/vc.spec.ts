// SPEC-final §21 #101 — persist-to-cloud volumes in Docker › Volumes (mock agent, lib/mock/volsync.ts): every sync
// state badge, Snapshot now, Apply cloud version (fast-forward, conflict per file with the diff viewer, overwrite, keep
// live, 409 "live changed since the plan" → re-plan), and Config › Volumes starter content / persist READ-ONLY.
import type { Page } from '@playwright/test';
import { test, expect } from './fixtures';
import { knobs, login, open, shot } from './helpers';

const V = (site: string, q = '') => `/_opm/app/${site}/volumes${q}`;

test.beforeEach(async ({ page }) => {
  await knobs(page, { 'opm.mock.waitSec': '2' });
});

/** an apply is a merge (§21 #104 (e)): one confirm, no intent wait */
async function confirmApply(page: Page) {
  const f = page.getByTestId('apply-confirm');
  await expect(f).toBeVisible();
  await f.getByTestId('confirm-ok').click();
}

test('sync state badges: one volume per state on the volume list + the Cloud sync tab', async ({ page }) => {
  await login(page, V('app'));
  await expect(page.getByTestId('sync-badge-hello-api-data')).toHaveText('In sync');
  await expect(page.getByTestId('sync-badge-api-env')).toHaveText('Waiting to upload');
  await expect(page.getByTestId('sync-badge-hello-api-2-data')).toHaveText('No snapshot yet');
  await page.getByTestId('vol-api-env').click();
  await expect(page.getByTestId('ov-sync')).toHaveText('Waiting to upload');
  await page.getByTestId('ov-sync-open').click();
  const sp = page.getByTestId('vol-sync');
  await expect(sp).toHaveAttribute('data-state', 'pending-upload');
  await expect(sp.getByTestId('sync-queued')).toHaveText('queued');
  await expect(sp.getByTestId('sync-queue')).toHaveText('1');
  // §21 #102: the shared persist form, read-only (patterns are disabled inputs)
  await expect(sp.getByTestId('sync-persist').locator('[data-testid^="persist-exclude-"]').first()).toBeDisabled();
  expect(await sp.getByTestId('sync-persist').locator('input[data-testid^="persist-exclude-"]').evaluateAll((els) => els.map((e) => (e as HTMLInputElement).value))).toContain('archive/');
  await expect(sp.getByTestId('sync-history')).toContainText('Snapshot now (site)');
  await open(page, V('db', '?v=redis-data&tab=sync'));
  await expect(page.getByTestId('sync-badge-redis-data')).toHaveText('Error');
  await expect(page.getByTestId('sync-error')).toContainText('over the cap');
  await open(page, V('northwind'));
  await expect(page.getByTestId('sync-badge-nodered-data')).toHaveText('Conflict');
  await expect(page.getByTestId('sync-badge-ez-integrations')).toHaveText('Cloud ahead');
  await expect(page.getByTestId('sync-badge-ez-scripts')).toHaveText('Live changed');
  // a non-persist volume has no badge and no Cloud sync tab
  await expect(page.getByTestId('sync-badge-admin-runtime')).toHaveCount(0);
  await page.getByTestId('vol-nodered-data').click();
  await page.getByTestId('vtab-sync').click();
  await expect(page.getByTestId('sync-push')).toContainText('ACME webhook + normalize step');
  await expect(page.getByTestId('sync-push')).toContainText('dev@acme.example');
  await expect(page.getByTestId('push-version')).toContainText('version seq 3');
  await expect(page.getByTestId('sync-console')).toHaveAttribute('href', /\/sites\/northwind\/volumes\/content\?volume=nodered-data&tab=cloud$/);
  await shot(page, 'vc-volumes-sync-light');
  await page.emulateMedia({ colorScheme: 'dark' });
  await shot(page, 'vc-volumes-sync-dark');
});

test('Snapshot now: live changed → in sync (uploaded); unchanged → "no change"; over the cap → too-large', async ({ page }) => {
  await login(page, V('northwind', '?v=ez-scripts&tab=sync'));
  const sp = page.getByTestId('vol-sync');
  await expect(sp.getByTestId('sync-state')).toHaveText('Live changed');
  await expect(sp.getByTestId('sync-live')).toContainText('changed since the last snapshot');
  await sp.getByTestId('sync-snapshot').click();
  await expect(page.getByText('Snapshot of ez-scripts taken')).toBeVisible();
  await expect(sp.getByTestId('sync-state')).toHaveText('In sync');
  await expect(page.getByTestId('sync-badge-ez-scripts')).toHaveText('In sync');
  await expect(sp.getByTestId('sync-last')).toContainText('version 2');
  await sp.getByTestId('sync-snapshot').click();
  await expect(page.getByText('No change in ez-scripts')).toBeVisible();
  // the live tree is the volume manager's file browser
  await sp.getByTestId('sync-browse').click();
  await expect(page.getByTestId('vol-entries')).toContainText('backup.sh');
  await open(page, V('db', '?v=redis-data&tab=sync'));
  await page.getByTestId('sync-snapshot').click();
  await expect(page.getByTestId('snap-too-large')).toContainText('appendonly.aof');
});

test('Apply cloud version — clean fast-forward (no question) → confirm → result', async ({ page }) => {
  await login(page, V('northwind', '?v=ez-integrations&tab=sync'));
  await page.getByTestId('sync-apply').click();
  const af = page.getByTestId('apply-flow');
  await expect(af.getByTestId('plan-ff')).toBeVisible();
  await expect(af.getByTestId('apply-resolution')).toHaveCount(0);
  await expect(af.getByTestId('plan-item-mapping.json')).toHaveAttribute('data-outcome', 'cloud');
  await expect(af.getByTestId('plan-item-vendors/beta.js')).toBeVisible();
  // restart defaults to the push (no) and lists the users
  await expect(af.getByTestId('apply-restart')).toContainText('northwind-backend-integration');
  await expect(af.getByTestId('apply-restart').locator('input')).not.toBeChecked();
  await af.getByTestId('apply-continue').click();
  await confirmApply(page);
  const r = page.getByTestId('apply-result');
  await expect(r).toBeVisible();
  await expect(r.getByTestId('res-written')).toHaveText('2');
  await expect(r.getByTestId('res-deleted')).toHaveText('0');
  await expect(r.getByTestId('res-snapshot')).toBeVisible();
  await expect(r.getByTestId('res-restarted')).toHaveText('none');
  await r.getByTestId('apply-result-close').click();
  await expect(page.getByTestId('sync-state')).toHaveText('In sync');
  await expect(page.getByTestId('sync-push')).toHaveCount(0);
  await page.getByTestId('vtab-files').click();
  await expect(page.getByTestId('entry-vendors')).toBeVisible();
});

test('Apply — conflict per file: list, diff viewer (side by side / unified / binary), every conflict needs a choice', async ({ page }) => {
  await login(page, V('northwind', '?v=nodered-data&tab=sync'));
  await page.getByTestId('sync-apply').click();
  const af = page.getByTestId('apply-flow');
  await expect(af.getByTestId('plan-conflicts')).toHaveText('2');
  await expect(af.getByTestId('apply-resolution')).toBeVisible();
  await expect(af.getByTestId('plan-item-flows.json')).toHaveAttribute('data-kind', 'conflict');
  await expect(af.getByTestId('plan-item-assets/logo.png')).toHaveAttribute('data-kind', 'conflict');
  await expect(af.getByTestId('plan-item-settings.js')).toHaveAttribute('data-kind', 'cloud');
  await expect(af.getByTestId('plan-item-lib/local-only.js')).toHaveAttribute('data-kind', 'live');
  await expect(af.getByTestId('plan-item-README.md')).toHaveAttribute('data-outcome', 'cloud'); // removed by the cloud
  // excluded paths are never in the plan
  await expect(af.getByTestId('plan-item-node-red.log')).toHaveCount(0);
  await expect(af.getByTestId('apply-continue')).toBeDisabled();
  await expect(af.getByTestId('apply-undecided')).toContainText('2 conflicts');
  // text diff, side by side then unified
  await af.getByTestId('diff-btn-flows.json').click();
  const fd = af.getByTestId('file-diff-flows.json');
  await expect(fd.getByTestId('diff-split')).toContainText('Device types (v2)');
  await expect(fd.getByTestId('diff-split')).toContainText('mqtt in');
  await shot(page, 'vc-apply-conflict-light', false);
  await page.emulateMedia({ colorScheme: 'dark' });
  await shot(page, 'vc-apply-conflict-dark', false);
  await page.emulateMedia({ colorScheme: 'light' });
  await fd.getByTestId('diff-view').getByRole('radio', { name: 'Unified' }).click();
  await expect(fd.getByTestId('diff-unified').locator('.dl.add')).toContainText(['normalize']);
  await fd.getByTestId('diff-pair').getByRole('radio', { name: 'Base → Live' }).click();
  await expect(fd.getByTestId('diff-unified').locator('.dl.add')).toContainText(['mqtt in']);
  // binary: sizes + hashes
  await af.getByTestId('diff-btn-assets/logo.png').click();
  await expect(af.getByTestId('file-diff-assets/logo.png').getByTestId('diff-binary')).toContainText('KiB');
  // choices
  await af.getByTestId('choice-flows.json').getByRole('radio', { name: 'Keep live' }).click();
  await expect(af.getByTestId('apply-undecided')).toContainText('1 conflict');
  await af.getByTestId('choice-assets/logo.png').getByRole('radio', { name: 'Take cloud' }).click();
  await expect(af.getByTestId('apply-undecided')).toHaveCount(0);
  await expect(af.getByTestId('plan-item-flows.json')).toHaveAttribute('data-outcome', 'live');
  await expect(af.getByTestId('apply-restart').locator('input')).toBeChecked();
  await af.getByTestId('apply-continue').click();
  await confirmApply(page);
  const r = page.getByTestId('apply-result');
  // written: settings.js, acme.js, logo.png · deleted: README.md · kept live: flows.json + lib/local-only.js
  await expect(r.getByTestId('res-written')).toHaveText('3');
  await expect(r.getByTestId('res-deleted')).toHaveText('1');
  await expect(r.getByTestId('res-kept')).toHaveText('2');
  await expect(r.getByTestId('res-restarted')).toHaveText('nodered');
  await r.getByTestId('apply-result-close').click();
  await page.getByTestId('vtab-files').click();
  await expect(page.getByTestId('entry-local-only.js').or(page.getByTestId('entry-lib'))).toBeVisible();
  await expect(page.getByTestId('entry-README.md')).toHaveCount(0);
  await expect(page.getByTestId('entry-node-red.log')).toBeVisible(); // excluded: untouched
});

test('Apply — overwrite (cloud wins); "all conflicts" buttons', async ({ page }) => {
  await login(page, V('northwind', '?v=nodered-data&tab=sync'));
  await page.getByTestId('sync-apply').click();
  const af = page.getByTestId('apply-flow');
  await af.getByTestId('all-live').click();
  await expect(af.getByTestId('apply-undecided')).toHaveCount(0);
  await af.getByTestId('apply-resolution').getByRole('radio', { name: 'Overwrite' }).click();
  await expect(af.getByTestId('resolution-hint')).toContainText('cloud version wins');
  await expect(af.getByTestId('plan-item-lib/local-only.js')).toHaveAttribute('data-outcome', 'cloud');
  await af.getByTestId('apply-continue').click();
  await confirmApply(page);
  const r = page.getByTestId('apply-result');
  await expect(r.getByTestId('res-kept')).toHaveText('0');
  await expect(r.getByTestId('res-deleted')).toHaveText('2'); // README.md + lib/local-only.js
  await r.getByTestId('apply-result-close').click();
  await expect(page.getByTestId('sync-state')).toHaveText('In sync');
});

test('Apply — keep live = a confirm, nothing written', async ({ page }) => {
  await login(page, V('northwind', '?v=nodered-data&tab=sync'));
  await page.getByTestId('sync-apply').click();
  const af = page.getByTestId('apply-flow');
  await af.getByTestId('apply-resolution').getByRole('radio', { name: 'Keep live' }).click();
  await expect(af.getByTestId('apply-restart')).toHaveCount(0);
  await af.getByTestId('apply-keep-live').click();
  const c = page.getByTestId('keep-live-confirm');
  await expect(c).toContainText('Nothing is written');
  await c.getByRole('button', { name: 'Keep live' }).click();
  await expect(page.getByText('Kept the live content of nodered-data')).toBeVisible();
  await expect(page.getByTestId('sync-push')).toHaveCount(0);
  await expect(page.getByTestId('sync-state')).toHaveText('In sync');
  await expect(page.getByTestId('sync-history')).toContainText('Snapshot now (site)');
});

test('Apply — 409 "live changed since the plan" → re-plan, then apply', async ({ page }) => {
  await knobs(page, { 'opm.mock.vsyncLiveMoves': '1' });
  await login(page, V('northwind', '?v=ez-integrations&tab=sync'));
  await page.getByTestId('sync-apply').click();
  const af = page.getByTestId('apply-flow');
  await expect(af.getByTestId('plan-ff')).toBeVisible();
  await af.getByTestId('apply-continue').click();
  await confirmApply(page);
  await expect(af.getByTestId('apply-replanned')).toBeVisible();
  // the file written meanwhile makes it a real 3-way plan now (live changed too)
  await expect(af.getByTestId('plan-item-written-meanwhile.txt')).toHaveAttribute('data-kind', 'live');
  await expect(af.getByTestId('plan-ff')).toHaveCount(0);
  await af.getByTestId('apply-continue').click();
  await confirmApply(page);
  await expect(page.getByTestId('apply-result').getByTestId('res-kept')).toHaveText('1');
});

test('Config › Volumes: starter content + persist read-only, legacy File seed, no "From files" UI', async ({ page }) => {
  await login(page, '/_opm/app/northwind/config/volumes');
  await expect(page.getByTestId('vseed-text-nodered-data')).toContainText('2 files');
  await expect(page.getByTestId('vpersist-nodered-data')).toBeVisible();
  await expect(page.getByTestId('vseed-text-admin-runtime')).toContainText('1 file');
  await page.getByTestId('edit-nodered-data').click();
  const d = page.getByTestId('volume-modal');
  await expect(d.getByTestId('vm-seed-view-tree')).toContainText('settings.js');
  await expect(d.getByTestId('vm-seed-view-tree')).toContainText('template (once per site)');
  await expect(d.getByTestId('vm-seed-view-tree')).toContainText('lib/');
  await expect(d.getByTestId('vm-persist')).toContainText('node_modules/');
  await expect(d.getByTestId('vm-console-link')).toHaveAttribute('href', /\/app\/deployments\/.+\/sites\/northwind\/volumes\/content\?volume=nodered-data$/);
  await expect(d.getByTestId('vm-seed-mode-files')).toHaveCount(0);
  await expect(d.getByTestId('seed-add')).toHaveCount(0);
  await shot(page, 'vc-config-volume-readonly', false);
  // saving a label keeps content + persist (no V_LOCAL_VOLUME_CONTENT)
  await d.getByTestId('vm-label').fill('Node-RED data (flows)');
  await d.getByTestId('vm-save').click();
  await expect(page.getByTestId('volume-row-nodered-data')).toContainText('edited');
  await expect(page.getByText('V_LOCAL_VOLUME_CONTENT')).toHaveCount(0);
  await open(page, '/_opm/app/app/config/volumes');
  await expect(page.getByTestId('vseed-text-api-env')).toHaveText('(legacy) from Files api-config');
  await page.getByTestId('edit-api-env').click();
  await expect(page.getByTestId('volume-modal').getByTestId('vm-seed-view')).toContainText('worker.ts ← file api-config');
  // a new volume starts empty (content is cloud configuration)
  await page.keyboard.press('Escape');
  await page.getByTestId('add-volume').click();
  await expect(page.getByTestId('volume-modal').getByTestId('vm-seed-own')).toContainText('starts empty');
});
