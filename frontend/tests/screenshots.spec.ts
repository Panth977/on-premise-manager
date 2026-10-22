/**
 * Screenshots of the key pages (mock data) → frontend/screenshots/. Run: npm run screenshots
 * Skipped unless OPM_SCREENSHOTS=1.
 */
import { test, type Page } from '@playwright/test';
import { login, SEED } from './helpers';

test.skip(!process.env.OPM_SCREENSHOTS, 'set OPM_SCREENSHOTS=1');
test.describe.configure({ mode: 'serial' });

const D = `/app/deployments/${SEED.deployment}`;
const T = `/app/templates/${SEED.template}`;
const pages: [string, string, (p: Page) => Promise<void>][] = [
  ['01-deployments', '/app/deployments', async () => {}],
  ['02-deployment-overview', D, async () => {}],
  ['03-deployment-setup', `${D}/setup`, async () => {}],
  ['04-deployment-status', `${D}/status`, async () => {}],
  ['05-deployment-activities', `${D}/activities`, async () => {}],
  ['06-deployment-variables', `${D}/variables`, async () => {}],
  ['07-deployment-secrets', `${D}/secrets`, async () => {}],
  ['08-deployment-files', `${D}/files`, async () => {}],
  ['09-deployment-sites', `${D}/sites`, async () => {}],
  ['10-site-editor-containers', `${D}/sites/app`, async () => {}],
  [
    '11-site-editor-edge-test-url',
    `${D}/sites/app/edge`,
    async (p) => {
      await p.getByTestId('test-url-input').fill('http://127.0.0.1:8081/api/count');
      await p.getByTestId('test-run').click();
    },
  ],
  ['12-site-editor-site', `${D}/sites/app/settings`, async () => {}],
  ['13-container-editor', `${D}/sites/app/containers/${SEED.svcApi}`, async () => {}],
  ['14-diff', `${D}/diff`, async () => {}],
  ['15-history', `${D}/history`, async () => {}],
  ['16-shared-files', `${D}/shared-files`, async () => {}],
  ['17-templates', '/app/templates', async () => {}],
  ['18-template-overview', T, async () => {}],
  ['19-template-variables', `${T}/variables`, async () => {}],
  ['20-services', '/app/services', async () => {}],
  ['21-service', `/app/services/${SEED.svcWeb}`, async () => {}],
  ['22-register-image', `/app/services/${SEED.svcApi}/register-image`, async () => {}],
  ['23-image', `/app/services/${SEED.svcRedis}/image/7.4-alpine`, async () => {}],
  ['24-registries', '/app/registries', async () => {}],
  ['25-not-found-pages', '/app/not-found-pages', async () => {}],
  ['26-not-found-page', `/app/not-found-pages/${SEED.page404}`, async () => {}],
  ['27-tags', '/app/tags', async () => {}],
  ['28-accounts', '/app/accounts', async () => {}],
  ['29-account', `/app/accounts/${SEED.uidOps}`, async () => {}],
  ['30-audit-log', '/app/audit-log', async () => {}],
  [
    '31-route-drawer',
    `${D}/sites/app/edge`,
    async (p) => {
      await p.getByTestId('add-proxy-route').click();
      await p.getByTestId('route-editor').waitFor();
    },
  ],
  [
    '32-expression-autocomplete',
    `${D}/sites/app/containers/${SEED.svcWeb}`,
    async (p) => {
      await p.getByTestId('add-env').click();
      const inp = p.getByTestId('env-value-x2-input');
      await inp.click();
      await inp.pressSequentially('{{ si');
    },
  ],
];

async function shot(page: Page, name: string, full = true) {
  await page.waitForTimeout(900);
  await page.screenshot({ path: `screenshots/${name}.png`, fullPage: full });
}

test('desktop light', async ({ page }) => {
  test.setTimeout(240_000);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto('/login');
  await shot(page, '00-login');
  await login(page, 'admin');
  for (const [name, path, act] of pages) {
    await page.goto(path);
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(600);
    await act(page);
    await shot(page, name, !name.includes('drawer') && !name.includes('autocomplete'));
  }
});

test('mobile 390', async ({ page }) => {
  test.setTimeout(120_000);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto('/login');
  await shot(page, 'm-00-login');
  await login(page, 'admin');
  for (const [name, path] of [
    ['m-01-deployments', '/app/deployments'],
    ['m-02-overview', D],
    ['m-03-variables', `${D}/variables`],
    ['m-04-edge', `${D}/sites/app/edge`],
    ['m-05-container', `${D}/sites/app/containers/${SEED.svcApi}`],
    ['m-06-diff', `${D}/diff`],
    ['m-07-register-image', `/app/services/${SEED.svcApi}/register-image`],
  ] as const) {
    await page.goto(path);
    await page.waitForLoadState('networkidle');
    await shot(page, name);
  }
  await page.goto('/app/deployments');
  await page.getByTestId('nav-toggle').click();
  await shot(page, 'm-08-menu', false);
});

test('desktop dark', async ({ page }) => {
  test.setTimeout(120_000);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.addInitScript(() => localStorage.setItem('opm.theme', 'dark'));
  await login(page, 'admin');
  for (const [name, path] of [
    ['d-01-deployments', '/app/deployments'],
    ['d-02-overview', D],
    ['d-03-edge', `${D}/sites/app/edge`],
    ['d-04-container', `${D}/sites/app/containers/${SEED.svcApi}`],
    ['d-05-diff', `${D}/diff`],
    ['d-06-status', `${D}/status`],
    ['d-07-template', T],
  ] as const) {
    await page.goto(path);
    await page.waitForLoadState('networkidle');
    await shot(page, name);
  }
});
