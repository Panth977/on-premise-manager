import { test, expect } from '@playwright/test';
import { login } from './helpers';

/** Round E (A): a REQUIRED variable without a template value is "set at deployment" — never a warning in a template. */
const TPL = '/app/templates/TplRequiredVars00005';

test.describe('round E: required variables in templates', () => {
  test('no warnings for server-ip (site host + env), info for the optional null variable, placeholder token in previews', async ({ page }) => {
    await login(page, 'admin', TPL);
    const issues = page.getByTestId('tpl-issues');
    await expect(issues).toBeVisible();
    await expect(issues).not.toContainText('warning');
    await expect(issues).not.toContainText('V_HOST_AUTO');
    await expect(issues).not.toContainText('E_NULL');
    // one info (optional variable without a value), rendered neutral and not counted as a warning
    await expect(issues.locator('li.info')).toHaveCount(1);
    await expect(issues.locator('li.warning')).toHaveCount(0);
    await expect(issues).toContainText('V_VAR_OPTIONAL_NULL');
    await expect(page.getByText(/publish warning/)).toHaveCount(0);
    await expect(page.getByTestId('tree-badge-site-01')).toHaveCount(0);
    await page.screenshot({ path: 'screenshots/e-mock-required-vars.png' });
    // container page: SERVER_URL resolves with the "set at deployment" token, no E_NULL
    await page.goto(`${TPL}/sites/site-01/containers/backend-terminal`);
    const pv = page.getByTestId('pv-env-SERVER_URL');
    await expect(pv).toBeVisible();
    await expect(pv.getByTestId('deferred')).toHaveText('set at deployment');
    await expect(pv).not.toContainText('E_NULL');
    await expect(page.getByTestId('container-issues')).not.toContainText('warning');
  });
});
