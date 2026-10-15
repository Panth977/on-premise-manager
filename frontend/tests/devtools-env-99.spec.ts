import { test, expect, type Page } from '@playwright/test';
import { login, SEED, nav } from './helpers';

/**
 * §21 #99 (user: "in the expression I am not able to select the env variable of container"): dev-tool preset fields are
 * expression field `devtool` — `{{ containers.<c>.env.<NAME> }}` completes level by level from an empty "{{"
 * (containers → postgress → env → POSTGRES_USER), previews (secrets masked), the drawer suggests the target container's
 * well-known env vars (chips) and the password gets a "Container env" mode; a plain env as password → warning; a
 * deployment whose presets use a container env warns for agents < 0.11.1.
 * Screenshots: screenshots/devtools-env-{completion,chips,password}.png (light)
 */
const TPL = '/app/templates/TplPostgresDevTool07';
const DEP = `/app/deployments/${SEED.deployment}`;
const SHOTS = 'screenshots';

async function settled(page: Page) {
  await expect(page.locator('.skel')).toHaveCount(0);
  await page.waitForTimeout(250);
}
async function patchMockDb(page: Page, fn: string) {
  await page.waitForTimeout(1200); // the mock persists with a debounce
  await page.evaluate((src) => {
    const db = JSON.parse(localStorage.getItem('opm.mock.db')!);
    new Function('db', src)(db);
    localStorage.setItem('opm.mock.db', JSON.stringify(db));
  }, fn);
  await page.reload();
}
async function newPg(page: Page) {
  await login(page, 'admin', `${TPL}/sites/db/devtools`);
  await settled(page);
  await page.getByTestId('add-devtool').click();
  await page.getByTestId('devtool-kind-picker').getByTestId('dt-kind-postgres').click();
  const d = page.getByTestId('devtool-drawer');
  await expect(d.locator('form')).toHaveAttribute('data-kind', 'postgres');
  await expect(d.getByTestId('dt-container')).toContainText('postgress');
  await expect(d.getByTestId('dt-port')).toContainText('service');
  return d;
}

test.describe('§21 #99 containers.<c>.env.<NAME> in dev-tool presets', () => {
  test('typing {{ in the username: containers → postgress → env → POSTGRES_USER', async ({ page }) => {
    const d = await newPg(page);
    const user = d.getByTestId('dt-username-input');
    await user.evaluate((e) => e.scrollIntoView({ block: 'center' }));
    await user.fill('');
    await user.pressSequentially('{{');
    const ac = page.getByTestId('dt-username-ac');
    await expect(ac).toBeVisible();
    // one level at a time: roots first
    await expect(ac.locator('[data-path="containers."]')).toBeVisible();
    await expect(ac.locator('[data-path="secret.pg-password"]')).toBeVisible();
    await expect(ac.locator('[data-path^="container."]')).toHaveCount(0); // container.* is not allowed in presets
    await ac.locator('[data-path="containers."]').click();
    await expect(user).toHaveValue('{{ containers.');
    await ac.locator('[data-path="containers.postgress."]').click();
    await expect(user).toHaveValue('{{ containers.postgress.');
    await expect(ac.locator('[data-path="containers.postgress.host"]')).toBeVisible();
    await expect(ac.locator('[data-path="containers.postgress.port.service"]')).toBeVisible();
    await expect(ac.locator('[data-path="containers.postgress.env."]')).toContainText('4 env vars');
    await ac.locator('[data-path="containers.postgress.env."]').click();
    await expect(user).toHaveValue('{{ containers.postgress.env.');
    // image env + container env, values resolved (secret masked)
    await expect(ac.locator('[data-path^="containers.postgress.env."]')).toHaveCount(4);
    await expect(ac.locator('[data-path="containers.postgress.env.POSTGRES_USER"]')).toContainText('plant');
    await expect(ac.locator('[data-path="containers.postgress.env.POSTGRES_PASSWORD"]')).toContainText('••• secret');
    await expect(ac.locator('[data-path="containers.postgress.env.PGDATA"]')).toContainText('/var/lib/postgresql/data');
    await expect(ac.locator('[data-path="containers.postgress.env.POSTGRES_DB"]')).toContainText('no value');
    await page.waitForTimeout(200);
    await page.screenshot({ path: `${SHOTS}/devtools-env-completion.png` });
    await ac.locator('[data-path="containers.postgress.env.POSTGRES_USER"]').click();
    await expect(user).toHaveValue('{{ containers.postgress.env.POSTGRES_USER }}');
    await expect(d.getByTestId('dt-username-preview')).toContainText('plant');
    // the username chip disappears once used; keyboard: "{{ POSTGRES" finds env names by substring
    await expect(d.getByTestId('dt-username-env-POSTGRES_USER')).toHaveCount(0);
    await user.fill('');
    await user.pressSequentially('{{ POSTGRES_PASS');
    await expect(ac.locator('[data-path="containers.postgress.env.POSTGRES_PASSWORD"]')).toBeVisible();
    await page.keyboard.press('Enter');
    await expect(user).toHaveValue('{{ containers.postgress.env.POSTGRES_PASSWORD }}');
    await expect(d.getByTestId('dt-username-preview')).toContainText('••••');
  });

  test('suggestion chips + "Container env" password; plain env → warning; saved preset', async ({ page }) => {
    const d = await newPg(page);
    await expect(d.getByTestId('dt-username-env-POSTGRES_USER')).toContainText('Use env POSTGRES_USER of postgress');
    await expect(d.getByTestId('dt-password-env-POSTGRES_PASSWORD')).toContainText('secret');
    await expect(d.getByTestId('dt-database-env-POSTGRES_DB')).toBeVisible();
    await expect(d.getByTestId('dt-password-mode').getByRole('radio')).toHaveText(['None', 'Value', 'Secret', 'Container env', 'Expression']);
    await d.getByTestId('dt-password-env-POSTGRES_PASSWORD').scrollIntoViewIfNeeded();
    await page.waitForTimeout(300);
    await page.screenshot({ path: `${SHOTS}/devtools-env-chips.png` });
    await d.getByTestId('dt-username-env-POSTGRES_USER').click();
    await expect(d.getByTestId('dt-username-input')).toHaveValue('{{ containers.postgress.env.POSTGRES_USER }}');
    await d.getByTestId('dt-password-env-POSTGRES_PASSWORD').click();
    await expect(d.getByTestId('dt-password')).toHaveAttribute('data-mode', 'env');
    await expect(d.getByTestId('dt-password-env')).toHaveValue('POSTGRES_PASSWORD');
    await expect(d.getByTestId('dt-password-env-note')).toContainText('from a secret');
    // a plain env as the password: warning (not an error — Save stays enabled)
    await d.getByTestId('dt-password-env').click();
    await page.getByTestId('dt-password-env-list').locator('[data-value="POSTGRES_USER"]').click();
    await expect(d.getByTestId('dt-password-env-note')).toContainText('plant (plain value)');
    await expect(d.getByTestId('dt-password')).toBeVisible();
    await expect(d.locator('.field-warn')).toContainText('holds a plain value, not a secret');
    await expect(d.getByTestId('dt-save')).toBeEnabled();
    await d.getByTestId('dt-password').scrollIntoViewIfNeeded();
    await page.waitForTimeout(300);
    await page.screenshot({ path: `${SHOTS}/devtools-env-password.png` });
    // back to the secret one and save
    await d.getByTestId('dt-password-env').click();
    await page.getByTestId('dt-password-env-list').locator('[data-value="POSTGRES_PASSWORD"]').click();
    await expect(d.locator('.field-warn')).toHaveCount(0);
    await d.getByTestId('dt-save').click();
    await expect(d).toHaveCount(0);
    await expect(page.getByTestId('devtool-auth-postgres')).toHaveText('user {{ containers.postgress.env.POSTGRES_USER }} · password: env POSTGRES_PASSWORD of postgress');
    await expect(page.getByTestId('devtool-errors-postgres')).toHaveCount(0);
  });

  test('unknown env name → V_DEVTOOL_ENV; expression mode previews masked', async ({ page }) => {
    const d = await newPg(page);
    await d.getByTestId('dt-password-mode').getByRole('radio', { name: 'Expression' }).click();
    const pw = d.getByTestId('dt-password-expr-input');
    await pw.fill('{{ containers.postgress.env.NOPE }}');
    await expect(d.getByTestId('dt-summary')).toContainText('container postgress has no env var NOPE');
    await expect(d.getByTestId('dt-save')).toBeDisabled();
    await pw.fill('{{ containers.postgress.env.POSTGRES_PASSWORD }}');
    await expect(d.getByTestId('dt-password-expr-preview')).toContainText('••••');
    await expect(d.getByTestId('dt-summary')).toHaveCount(0);
  });

  test('deployment: presets using a container env warn for agents < 0.11.1 (advisory)', async ({ page }) => {
    await login(page, 'admin', `${DEP}/sites/db/devtools`);
    await settled(page);
    await patchMockDb(
      page,
      `const s = db.deployments['${SEED.deployment}'].draft.sites.find((x) => x.siteSlug === 'db');
       const r = s.config.containers.find((c) => c.containerSlug === 'redis');
       r.env.push({ name: 'REDIS_PASSWORD', value: null, secretRef: 'pg-password' });
       s.config.devTools.find((p) => p.presetSlug === 'redis').auth.password = { kind: 'expr', expr: '{{ containers.redis.env.REDIS_PASSWORD }}' };`,
    );
    await settled(page);
    await page.waitForFunction(() => typeof (window as unknown as { __opmMockAgent?: unknown }).__opmMockAgent === 'function');
    await page.evaluate((dep) => (window as unknown as { __opmMockAgent: (d: string, s: string, p: Record<string, unknown>) => boolean }).__opmMockAgent(dep, 'db', { agentVersion: '0.11.0-10082309' }), SEED.deployment);
    await page.reload();
    await settled(page);
    await expect(page.getByTestId('devtools-agent-warning')).toContainText('Site db runs agent 0.11.0-10082309; a preset uses a container env ({{ containers.redis.env.REDIS_PASSWORD }}), which needs agent 0.11.1 or newer');
    await nav(page, `${DEP}/compare?review=working`); // §21 #104 Diff → Compare · Not committed
    await expect(page.getByTestId('working-checks')).toBeVisible();
    // §21 #105 the commit dialog needs a change in the browser
    await nav(page, `${DEP}/variables`);
    await page.getByTestId('val-greeting').fill('Hi');
    await page.getByTestId('change-bar-commit').click();
    const m = page.getByTestId('commit-dialog');
    await expect(m.getByTestId('commit-warning').filter({ hasText: 'needs agent 0.11.1 or newer' })).toHaveCount(1);
    await expect(m.getByTestId('commit-submit')).toBeVisible(); // advisory: never blocks
    await m.getByTestId('commit-cancel').click();
    await page.evaluate((dep) => (window as unknown as { __opmMockAgent: (d: string, s: string, p: Record<string, unknown>) => boolean }).__opmMockAgent(dep, 'db', { agentVersion: '0.11.1-10090000' }), SEED.deployment);
    await page.goto(`${DEP}/compare?review=working`);
    await expect(page.getByTestId('working-checks')).toBeVisible();
    await expect(page.getByTestId('devtools-agent-banner')).toHaveCount(0);
  });
});
