import { test, expect, type Page } from '@playwright/test';
import { login, expectToast, SEED, pickIcon, pick } from './helpers';

/** 8×8 red PNG */
const PNG = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAAEUlEQVR4nGO4o6aGFTEMLQkAF/tKAS/fz4YAAAAASUVORK5CYII=', 'base64');
const SHOTS = 'screenshots';

async function uploadIcon(page: Page, name: string) {
  const m = page.getByTestId('icon-picker');
  await m.getByTestId('ip-tab-uploads').click();
  await m.getByTestId('ip-file').setInputFiles({ name, mimeType: 'image/png', buffer: PNG });
}

test.describe('icon picker', () => {
  test('Lucide pick with the keyboard, upload a PNG, reuse it in another form (dedupe)', async ({ page }) => {
    await login(page, 'admin', '/app/services');
    await page.getByTestId('new-service').click();
    await page.getByTestId('svc-label').fill('Cache');
    // keyboard: search, ↓ into the grid, → moves, Enter picks
    await page.getByTestId('svc-icon').click();
    const m = page.getByTestId('icon-picker');
    await m.getByTestId('ip-search').fill('data');
    await m.getByTestId('ip-search').press('ArrowDown');
    const first = await m.getByTestId('ip-icon').nth(0).getAttribute('data-name');
    await expect(m.getByTestId('ip-icon').nth(0)).toBeFocused();
    await page.keyboard.press('ArrowRight');
    const second = await m.getByTestId('ip-icon').nth(1).getAttribute('data-name');
    await expect(m.getByTestId('ip-icon').nth(1)).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(m).toHaveCount(0);
    await expect(page.getByTestId('svc-icon')).toContainText(second!);
    expect(first).not.toBe(second);
    // upload a PNG → selected
    await page.getByTestId('svc-icon').click();
    await uploadIcon(page, 'cache-logo.png');
    await expectToast(page, 'success', 'Icon uploaded');
    await expect(page.getByTestId('svc-icon')).toContainText('cache-logo');
    await expect(page.getByTestId('svc-icon').locator('img')).toBeVisible();
    await page.getByTestId('svc-save').click();
    await page.waitForURL(/\/app\/services\/cache$/);
    await expect(page.locator('header.ph img')).toBeVisible();

    // second form (tag): the upload is listed for reuse; uploading the same bytes again reuses it
    await page.goto('/app/tags');
    await page.getByTestId('new-tag').click();
    await page.getByTestId('tag-label').fill('Edge');
    await page.getByTestId('tag-icon').click();
    await page.getByTestId('icon-picker').getByTestId('ip-tab-uploads').click();
    await expect(page.getByTestId('icon-picker').getByTestId('ip-upload').filter({ hasText: 'cache-logo' })).toBeVisible();
    await uploadIcon(page, 'same-bytes.png');
    await expect(page.getByTestId('toast-info').last()).toContainText('already uploaded — reused');
    await expect(page.getByTestId('tag-icon')).toContainText('cache-logo');
    // the unused seeded upload can be deleted; a used one has no delete button
    await page.getByTestId('tag-icon').click();
    const pm = page.getByTestId('icon-picker');
    await pm.getByTestId('ip-tab-uploads').click();
    await expect(pm.getByTestId('ip-upload').filter({ hasText: 'cache-logo' }).locator('..').getByTestId('ip-upload-delete')).toHaveCount(0);
    await pm.getByTestId('ip-upload').filter({ hasText: 'acme-logo' }).locator('..').getByTestId('ip-upload-delete').click();
    await expectToast(page, 'success', 'Icon deleted');
    await expect(pm.getByTestId('ip-upload').filter({ hasText: 'acme-logo' })).toHaveCount(0);
    // None = default icon
    await pm.getByTestId('ip-none').click();
    await expect(page.getByTestId('tag-icon')).toContainText('Default (tag)');
    // wrong file type is refused client-side
    await page.getByTestId('tag-icon').click();
    await pm.getByTestId('ip-tab-uploads').click();
    await pm.getByTestId('ip-file').setInputFiles({ name: 'x.gif', mimeType: 'image/gif', buffer: PNG });
    await expect(pm.getByTestId('ip-error')).toContainText('use PNG, SVG or WebP');
  });
});

test.describe('register image pickers', () => {
  test('host → repository → tag pickers; registered tags are disabled', async ({ page }) => {
    await login(page, 'admin', `/app/services/${SEED.svcCore}/register-image`);
    await expect(page.getByTestId('ri-version')).toHaveValue('v3');
    const ref = page.getByTestId('ri-ref');
    await ref.click();
    const list = page.getByTestId('ri-ref-list');
    await list.locator('[data-value="docker.io/"]').click();
    await expect(ref).toHaveValue('docker.io/');
    await ref.pressSequentially('redis');
    await list.locator('[data-value="docker.io/library/redis:"]').click();
    await expect(list.getByTestId('combo-option').first()).toHaveAttribute('data-value', 'docker.io/library/redis:7.4.1');
    await expect(list).toContainText('ago');
    await list.locator('[data-value="docker.io/library/redis:7.2.5"]').click();
    await expect(ref).toHaveValue('docker.io/library/redis:7.2.5');
    await expect(page.getByTestId('ri-ref-parts')).toContainText('library/redis');
    await expect(page.getByTestId('ri-registry')).toContainText('Docker Hub');
    // acme/core-api: v3.0.9 + v3.0.8 are registered (v2, v1) → disabled
    await ref.fill('docker.io/acme/core-api:');
    await expect(list.locator('[data-value="docker.io/acme/core-api:v3.0.9"]')).toContainText('already registered (v2)');
    await expect(list.locator('[data-value="docker.io/acme/core-api:v3.0.9"]')).toHaveAttribute('aria-disabled', 'true');
    // a registry that cannot list → message, free text stays
    await pick(page, 'ri-registry', SEED.registryHarbor);
    await ref.fill('harbor.acme.example/team/api:1.0');
    await expect(page.getByTestId('ri-ref-parts')).toContainText('harbor.acme.example');
  });

  test('choice env with options + default, V_OPTION; mountPath expression with container.env, V_PATH', async ({ page }) => {
    await login(page, 'admin', `/app/services/${SEED.svcApi}/register-image`);
    await page.getByTestId('ri-add-env').click();
    const row = page.getByTestId('ri-env-row').first();
    await row.getByTestId('env-name').fill('node_env');
    await expect(row.getByTestId('env-name')).toHaveValue('NODE_ENV');
    await pick(page, 'env-kind', 'choice', row);
    const opts = row.getByTestId('env-options').locator('input');
    for (const o of ['integration', 'dc', 'worker']) {
      await opts.fill(o);
      await opts.press('Enter');
    }
    await expect(row.getByTestId('env-choice-default')).toHaveAttribute('data-value', 'integration');
    await pick(page, 'env-choice-default', 'worker', row);
    // remove the default from the options → V_OPTION inline
    await row.getByTestId('env-options').getByRole('button', { name: 'Remove worker' }).click();
    await expect(row.getByTestId('field-error')).toContainText('must be one of the options');
    await pick(page, 'env-choice-default', 'dc', row);
    await expect(row.getByTestId('field-error')).toHaveCount(0);
    // list kind help text
    await page.getByTestId('ri-add-env').click();
    const row2 = page.getByTestId('ri-env-row').nth(1);
    await row2.getByTestId('env-name').fill('FEATURES');
    await pick(page, 'env-kind', 'list', row2);
    await expect(row2).toContainText('Stored as one comma-separated value');

    // volume with an expression reading container.env.NODE_ENV
    await page.getByTestId('ri-add-vol').click();
    const vol = page.getByTestId('ri-vol-row').first();
    await vol.getByTestId('vol-name').fill('config');
    await expect(vol.getByTestId('vol-mount')).toContainText('/app/helper/config/env/{{ container.env.NODE_ENV }}.ts');
    await vol.getByTestId('vol-mount-input').fill('helper/config/env/{{ container.env.NODE_ENV }}.ts');
    await expect(vol.getByTestId('field-error')).toContainText('must start with / — relative paths are invalid');
    await vol.getByTestId('vol-mount-input').fill('/app/helper/config/env/{{ container.env.');
    await expect(page.locator('[role=listbox]').filter({ hasText: 'container.env.NODE_ENV' })).toBeVisible();
    await vol.getByTestId('vol-mount-input').fill('/app/helper/config/env/{{ container.env.NODE_ENV }}.ts');
    await page.keyboard.press('Escape');
    await expect(vol.getByTestId('field-error')).toHaveCount(0);
    await expect(vol).toContainText('/app/helper/config/env/dc.ts');
    await expect(page.getByTestId('mount-help')).toHaveAttribute('data-tip', /container\.env\.NODE_ENV/);
    await page.screenshot({ path: `${SHOTS}/ux-mock-register-env-volumes.png`, fullPage: true });
  });
});

test.describe('clone image', () => {
  test('Clone from… prefills everything but the tag; tag list; registry diff', async ({ page }) => {
    await login(page, 'admin', `/app/services/${SEED.svcCore}/register-image`);
    // latest preselected, nothing applied yet
    await expect(page.getByTestId('clone-from')).toHaveValue('v2');
    await expect(page.getByTestId('ri-ref')).toHaveValue('');
    await expect(page.getByTestId('ri-env-row')).toHaveCount(0);
    await page.getByTestId('clone-apply').click();
    await expect(page.getByTestId('clone-applied')).toContainText('was v3.0.9');
    await expect(page.getByTestId('ri-version')).toHaveValue('v3');
    await expect(page.getByTestId('ri-ref')).toHaveValue('docker.io/acme/core-api:v3.0.9');
    // tag part selected for typing
    const sel = await page.getByTestId('ri-ref').evaluate((el: HTMLInputElement) => el.value.slice(el.selectionStart ?? 0, el.selectionEnd ?? 0));
    expect(sel).toBe('v3.0.9');
    // tag list: newest first, registered ones disabled, v3.0.10 free
    const list = page.getByTestId('ri-ref-list');
    await expect(list.getByTestId('combo-option').first()).toHaveAttribute('data-value', 'docker.io/acme/core-api:v3.0.10');
    await expect(list.locator('[data-value="docker.io/acme/core-api:v3.0.8"]')).toHaveAttribute('aria-disabled', 'true');
    await list.locator('[data-value="docker.io/acme/core-api:v3.0.10"]').click();
    await expect(page.getByTestId('ri-ref')).toHaveValue('docker.io/acme/core-api:v3.0.10');
    // everything else copied
    await expect(page.getByTestId('ri-env-row')).toHaveCount(4);
    await expect(page.getByTestId('ri-env-row').first().getByTestId('env-choice-default')).toHaveAttribute('data-value', 'integration');
    await expect(page.getByTestId('ri-vol-row').first().getByTestId('vol-mount-input')).toHaveValue('/app/helper/config/env/{{ container.env.NODE_ENV }}.ts');
    await expect(page.getByTestId('ri-registry')).toHaveAttribute('data-value', SEED.registryHubBot);
    await expect(page.getByTestId('hc-type-http')).toHaveAttribute('aria-checked', 'true');
    await page.screenshot({ path: `${SHOTS}/ux-mock-clone.png`, fullPage: true });
    // Read from registry → diff, keep the clone
    await page.getByTestId('ri-read').click();
    const diff = page.getByTestId('ri-diff');
    await expect(diff).toBeVisible();
    await expect(diff.getByRole('row', { name: /^env NODE_ENV changed/ })).toBeVisible();
    await page.screenshot({ path: `${SHOTS}/ux-mock-clone-diff.png` });
    await diff.getByTestId('diff-keep').click();
    await expect(page.getByTestId('ri-env-row').first().getByTestId('env-choice-default')).toHaveAttribute('data-value', 'integration');
    await page.getByTestId('ri-submit').click();
    await page.waitForURL(/\/image\/v3$/);
    await expect(page.getByTestId('image-reference')).toHaveText('docker.io/acme/core-api:v3.0.10');
  });

  test('image detail → Clone as new version opens the clone applied', async ({ page }) => {
    await login(page, 'admin', `/app/services/${SEED.svcCore}/image/v1`);
    await page.getByTestId('clone-image').click();
    await page.waitForURL(/register-image\?cloneFrom=v1/);
    await expect(page.getByTestId('clone-applied')).toContainText('was v3.0.8');
    await expect(page.getByTestId('ri-version')).toHaveValue('v3');
  });
});

test.describe('overlays render at document.body (not clipped by the header)', () => {
  for (const w of [1440, 390]) {
    test(`My profile + IconPicker from a drawer at ${w}px`, async ({ page }) => {
      await page.setViewportSize({ width: w, height: w === 390 ? 844 : 900 });
      await login(page, 'admin', '/app/services');
      const inside = async (testid: string) => {
        const d = page.getByTestId(testid);
        await expect(d).toBeVisible();
        await page.waitForTimeout(250); // transitions
        const box = (await d.boundingBox())!;
        const vp = page.viewportSize()!;
        expect(box.x).toBeGreaterThanOrEqual(0);
        expect(box.y).toBeGreaterThanOrEqual(0);
        expect(box.x + box.width).toBeLessThanOrEqual(vp.width + 0.5);
        expect(box.y + box.height).toBeLessThanOrEqual(vp.height + 0.5);
        const hit = await page.evaluate(({ x, y, id }) => !!document.elementFromPoint(x, y)?.closest(`[data-testid="${id}"]`), { x: box.x + box.width / 2, y: box.y + box.height / 2, id: testid });
        expect(hit).toBe(true);
        expect(await d.evaluate((el) => el.closest('header') === null)).toBe(true);
      };
      // round D: the account menu lives in the sidebar footer (phones: inside the drawer)
      if (w === 390) await page.getByTestId('nav-toggle').click();
      await page.getByTestId('account-chip').click();
      await page.getByTestId('menu-profile').click();
      await inside('profile-modal');
      await expect(page.getByTestId('profile-name')).toBeVisible();
      await expect(page.getByTestId('profile-img')).toBeVisible();
      await page.screenshot({ path: `${SHOTS}/ux-mock-profile-${w}.png` });
      // avatar picker inside the profile modal
      await page.getByTestId('profile-img').click();
      await inside('icon-picker');
      await page.keyboard.press('Escape');
      await page.keyboard.press('Escape');
      await expect(page.getByTestId('profile-modal')).toHaveCount(0);
      // IconPicker from inside the New service drawer
      await page.getByTestId('new-service').click();
      await page.getByTestId('svc-icon').click();
      await inside('icon-picker');
      await page.screenshot({ path: `${SHOTS}/ux-mock-iconpicker-${w}.png` });
      await page.getByTestId('icon-picker').getByTestId('ip-tab-uploads').click();
      await page.screenshot({ path: `${SHOTS}/ux-mock-iconpicker-uploads-${w}.png` });
    });
  }
});

test.describe('screenshots', () => {
  test('forms in light + dark', async ({ page }) => {
    await login(page, 'admin', '/app/services');
    await page.getByTestId('new-service').click();
    await page.getByTestId('svc-label').fill('Core cache');
    await pickIcon(page, 'svc-icon', 'database');
    await page.screenshot({ path: `${SHOTS}/ux-mock-new-service.png` });
    await page.goto(`/app/services/${SEED.svcCore}`);
    await expect(page.getByTestId('image-row')).toHaveCount(2);
    await page.screenshot({ path: `${SHOTS}/ux-mock-service.png`, fullPage: true });
    await page.goto('/app/registries');
    await page.getByTestId('new-registry').click();
    await page.getByTestId('reg-provider-ar').click();
    await page.screenshot({ path: `${SHOTS}/ux-mock-registry-form.png` });
    await page.goto('/app/tags');
    await page.getByTestId('new-tag').click();
    await page.getByTestId('tag-label').fill('Field trial');
    await page.screenshot({ path: `${SHOTS}/ux-mock-tag-form.png` });
    await page.goto('/app/deployments');
    await page.getByTestId('new-deployment').click();
    await page.getByTestId('dep-label').fill('South depot');
    await page.getByTestId('dep-tags').locator('[role=combobox]').click();
    await page.screenshot({ path: `${SHOTS}/ux-mock-new-deployment.png` });
    await page.keyboard.press('Escape');
    await page.goto(`/app/accounts/${SEED.uidOps}`);
    await page.screenshot({ path: `${SHOTS}/ux-mock-account.png`, fullPage: true });
    // dark
    await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'dark'));
    await page.goto(`/app/services/${SEED.svcCore}/register-image?cloneFrom=v2`);
    await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'dark'));
    await expect(page.getByTestId('clone-applied')).toBeVisible();
    await page.screenshot({ path: `${SHOTS}/ux-mock-register-dark.png`, fullPage: true });
    await page.goto('/app/tags');
    await page.evaluate(() => document.documentElement.setAttribute('data-theme', 'dark'));
    await page.getByTestId('new-tag').click();
    await page.getByTestId('tag-icon').click();
    await page.screenshot({ path: `${SHOTS}/ux-mock-iconpicker-dark.png` });
  });
  test('390 px register image', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await login(page, 'admin', `/app/services/${SEED.svcCore}/register-image?cloneFrom=v2`);
    await expect(page.getByTestId('clone-applied')).toBeVisible();
    await page.keyboard.press('Escape');
    await page.getByRole('button', { name: 'Dismiss' }).first().click();
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.screenshot({ path: `${SHOTS}/ux-mock-register-390.png`, fullPage: true });
    await page.goto('/app/services');
    await page.getByTestId('new-service').click();
    await page.screenshot({ path: `${SHOTS}/ux-mock-new-service-390.png` });
  });
});
