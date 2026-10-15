import { test, expect } from './fixtures';
import { KEY, EMAIL, knobs, login, declare, shot } from './helpers';

test('server fallback + login + actor popup + session chip + reload + change person + logout', async ({ page }) => {
  await page.goto('/_opm/');
  await page.waitForURL(/\/_opm\/login\?next=%2F_opm%2Fapp/);
  await expect(page.getByTestId('login-deployment')).toHaveText('DepHelloWorld0000001');
  await shot(page, 'login');

  await page.getByTestId('access-key').fill('aaaa bbbb cccc dddd eeee ffff');
  await expect(page.getByTestId('access-key')).toHaveValue('AAAA-BBBB-CCCC-DDDD-EEEE-FFFF');
  await page.getByRole('button', { name: 'Log in' }).click();
  await expect(page.getByTestId('login-error')).toHaveText('Wrong access key (1 of 5).');
  await shot(page, 'login-wrong-key');

  await page.getByTestId('access-key').fill(KEY.toLowerCase().replace(/-/g, ' '));
  await page.getByRole('button', { name: 'Log in' }).click();
  await page.waitForURL(/\/_opm\/app\/app\/sites$/); // nav2: /_opm/app → this site's Sites page
  await expect(page.getByTestId('actor-popup')).toBeVisible();
  await expect(page.getByTestId('actor-suggestion').first()).toContainText('ravi@acme.example');
  // blocking: Escape does not close it
  await page.keyboard.press('Escape');
  await expect(page.getByTestId('actor-popup')).toBeVisible();
  await page.getByTestId('actor-continue').click();
  await expect(page.getByText('Enter a valid email address')).toBeVisible();
  await shot(page, 'actor-popup');
  await page.getByTestId('actor-email').fill(EMAIL);
  await page.getByTestId('actor-continue').click();
  await expect(page.getByTestId('actor-chip')).toContainText(EMAIL);
  await expect(page.getByTestId('actor-chip')).toContainText(/session ends in 5 h 5\d m|session ends in 6 h 0 m/);

  // refresh → the popup again, with this browser's recent email first
  await page.reload();
  await expect(page.getByTestId('actor-popup')).toBeVisible();
  await expect(page.getByTestId('actor-suggestion').first()).toContainText(EMAIL);
  await page.getByTestId('actor-suggestion').first().click();
  await expect(page.getByTestId('actor-email')).toHaveValue(EMAIL);
  await page.getByTestId('actor-continue').click();
  await expect(page.getByTestId('actor-popup')).toBeHidden();

  // change person (same session)
  await page.getByTestId('change-person').click();
  await declare(page, 'mira@acme.example');

  await page.getByTestId('logout').click();
  await page.waitForURL(/\/_opm\/login$/);
  await page.goto('/_opm/app/variables');
  await page.waitForURL(/\/_opm\/login\?next=%2F_opm%2Fapp%2Fapp%2Fvariables/); // old URL → /_opm/app/app/variables
});

test('rate limit after 5 failures', async ({ page }) => {
  await page.goto('/_opm/login');
  for (let i = 1; i <= 5; i++) {
    await page.getByTestId('access-key').fill('AAAABBBBCCCCDDDDEEEEFFFF');
    await page.getByRole('button', { name: 'Log in' }).click();
    await expect(page.getByTestId('login-error')).toContainText(`(${i} of 5)`);
  }
  await page.getByTestId('access-key').fill('AAAABBBBCCCCDDDDEEEEFFFF');
  await page.getByRole('button', { name: 'Log in' }).click();
  await expect(page.getByTestId('login-error')).toContainText(/Too many attempts\. Try again in \d:\d\d/);
  await shot(page, 'login-rate-limited');
});

test('site without an access key → unavailable', async ({ page }) => {
  await knobs(page, { 'opm.mock.noKey': '1' });
  await page.goto('/_opm/login');
  await page.getByTestId('access-key').fill(KEY);
  await page.getByRole('button', { name: 'Log in' }).click();
  await expect(page.getByTestId('login-error')).toContainText('This site has no access key yet. It gets it from the cloud or from another site.');
});

test('session expiry (6 h) → back to login with a message', async ({ page }) => {
  await login(page, '/_opm/app/variables');
  await page.evaluate(() => {
    const s = JSON.parse(localStorage.getItem('opm.mock.session')!);
    s.expiresAt = new Date(Date.now() - 1000).toISOString();
    localStorage.setItem('opm.mock.session', JSON.stringify(s));
  });
  await page.reload();
  await page.waitForURL(/\/_opm\/login\?next=%2F_opm%2Fapp%2Fapp%2Fvariables/); // old URL → /_opm/app/app/variables
  await expect(page.getByText(/session ended/i)).toBeVisible();
  await shot(page, 'login-session-ended');
});

test('agent 404 (server) and SPA 404 page', async ({ page }) => {
  const r = await page.goto('/_opm/nothing-here');
  expect(r?.status()).toBe(404);
  await expect(page.getByText('OPM agent · 404')).toBeVisible();
  await login(page);
  await page.goto('/_opm/app/does/not/exist/here');
  await expect(page.getByText('Page not found')).toBeVisible();
  await expect(page.getByRole('link', { name: 'Back to /_opm/app' })).toBeVisible();
  await shot(page, 'spa-404');
});
