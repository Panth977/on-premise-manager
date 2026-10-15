// Playwright UI flows: cloud frontend (SPEC §17) + agent GUI (§18) + the hello page.
// Selectors follow the spec wording (labels / button names); every flow screenshots to e2e/screenshots/.
import { chromium, type Browser, type BrowserContext, type Page } from 'playwright';
import { join } from 'node:path';
import { CFG } from './lib/env.js';
import { check, log, shot } from './lib/report.js';

let browser: Browser | null = null;
let cloudCtx: BrowserContext | null = null;
const agentCtx = new Map<string, BrowserContext>();

async function b(): Promise<Browser> {
  if (!browser) browser = await chromium.launch({ headless: process.env.OPM_E2E_HEADED !== '1' });
  return browser;
}
async function snap(page: Page, name: string): Promise<string> {
  const p = join(CFG.screenshotsDir, `${name}.png`);
  await page.screenshot({ path: p, fullPage: true });
  shot(p);
  log(`screenshot ${p}`);
  return p;
}
export async function close(): Promise<void> {
  await browser?.close();
  browser = null;
  cloudCtx = null;
  agentCtx.clear();
}

// ───────────────────────── cloud frontend
async function cloudPage(base: string): Promise<Page> {
  if (!cloudCtx) {
    cloudCtx = await (await b()).newContext({ viewport: { width: 1440, height: 900 }, baseURL: base });
  }
  return cloudCtx.newPage();
}

export async function cloudLogin(base: string, email: string, password: string): Promise<void> {
  const page = await cloudPage(base);
  try {
    await page.goto('/login');
    await page.getByTestId('login-email').fill(email);
    await page.getByTestId('login-password').fill(password);
    await snap(page, 'cloud-01-login');
    await page.getByTestId('login-submit').click();
    await page.waitForURL(/\/app\/deployments/, { timeout: 20_000 });
    await page.waitForLoadState('networkidle').catch(() => {});
    await snap(page, 'cloud-02-deployments');
    check('frontend: login → /app/deployments (Playwright)', true);
  } finally {
    await page.close();
  }
}

/** visit the main pages of the run (read only) + screenshot each */
export async function cloudTour(base: string, dep: string): Promise<void> {
  const page = await cloudPage(base);
  try {
    const pages: [string, string][] = [
      ['/app/registries', 'cloud-03-registries'],
      ['/app/services', 'cloud-04-services'],
      ['/app/not-found-pages', 'cloud-05-not-found-pages'],
      ['/app/templates', 'cloud-06-templates'],
      [`/app/deployments/${dep}`, 'cloud-07-deployment-overview'],
      [`/app/deployments/${dep}/variables`, 'cloud-08-variables'],
      [`/app/deployments/${dep}/sites`, 'cloud-09-sites'],
      [`/app/deployments/${dep}/diff`, 'cloud-10-diff'],
      [`/app/deployments/${dep}/setup`, 'cloud-11-setup'],
    ];
    for (const [path, name] of pages) {
      await page.goto(path);
      await page.waitForLoadState('networkidle').catch(() => {});
      await page.waitForTimeout(800);
      const errorText = await page.getByText(/something went wrong|error loading|forbidden/i).count();
      check(`frontend page ${path} renders`, errorText === 0 && !page.url().includes('/login'), page.url());
      await snap(page, name);
    }
    // registry list shows the registry, services list shows the 3 services
    await page.goto('/app/registries');
    await page.waitForLoadState('networkidle').catch(() => {});
    check('frontend: registries list shows "E2E Artifact Registry"', (await page.getByText('E2E Artifact Registry').count()) > 0);
    await page.goto('/app/services');
    await page.waitForLoadState('networkidle').catch(() => {});
    for (const s of ['hello-web', 'hello-api', 'redis']) check(`frontend: services list shows ${s}`, (await page.getByText(s, { exact: false }).count()) > 0);
  } finally {
    await page.close();
  }
}

/** Variables → theme = <theme> → Save draft → Diff → Publish proposal (modal summary) */
export async function cloudSetThemeAndPublish(base: string, dep: string, theme: string, summary: string): Promise<void> {
  const page = await cloudPage(base);
  try {
    await page.goto(`/app/deployments/${dep}/variables`);
    await page.waitForLoadState('networkidle').catch(() => {});
    const row = page.getByTestId('var-row-theme');
    await row.waitFor({ timeout: 20_000 });
    await row.locator('select').first().selectOption(theme);
    await snap(page, `cloud-20-variables-theme-${theme}`);
    await page.getByTestId('save-draft').click();
    await page.getByTestId('save-draft').isDisabled().catch(() => false);
    await page.waitForFunction(() => document.querySelector('[data-testid="save-draft"]')?.hasAttribute('disabled'), null, { timeout: 20_000 });
    await page.goto(`/app/deployments/${dep}/diff`);
    await page.waitForLoadState('networkidle').catch(() => {});
    await page.getByText(theme, { exact: false }).first().waitFor({ timeout: 15_000 });
    await snap(page, `cloud-21-diff-theme-${theme}`);
    await page.getByTestId('publish').click();
    await page.getByTestId('publish-summary').fill(summary);
    await snap(page, `cloud-22-publish-modal`);
    await page.getByTestId('publish-submit').click();
    await page.getByTestId('publish-submit').waitFor({ state: 'detached', timeout: 30_000 });
    await page.waitForTimeout(1500);
    await snap(page, `cloud-23-published`);
  } finally {
    await page.close();
  }
}

// ───────────────────────── agent GUI
async function agentPage(base: string): Promise<Page> {
  let ctx = agentCtx.get(base);
  if (!ctx) {
    ctx = await (await b()).newContext({ viewport: { width: 1440, height: 900 }, baseURL: base });
    // dry runs against the GUI mock: OPM_E2E_UI_INIT_LS='{"opm.mock.waitSec":"3"}'
    const ls = process.env.OPM_E2E_UI_INIT_LS;
    if (ls) await ctx.addInitScript((j) => { for (const [k, v] of Object.entries(JSON.parse(j) as Record<string, string>)) localStorage.setItem(k, v); }, ls);
    agentCtx.set(base, ctx);
  }
  return ctx.newPage();
}

export async function agentLoginPage(base: string): Promise<void> {
  const page = await agentPage(base);
  try {
    await page.goto('/_opm/');
    await page.waitForURL(/\/_opm\/login/, { timeout: 15_000 });
    await page.getByRole('button', { name: /log in/i }).waitFor({ timeout: 10_000 });
    await snap(page, 'agent-01-login-page');
    check('agent GUI: /_opm/ → login page (Playwright)', true, page.url());
  } finally {
    await page.close();
  }
}

/** the actor popup appears on every full page load after login: answer it when it shows */
async function ensureActor(page: Page, actor: string, shotIt = false): Promise<void> {
  const dlg = page.getByRole('dialog').filter({ hasText: /who is working/i });
  try {
    await dlg.waitFor({ timeout: 8_000 });
  } catch {
    return;
  }
  await dlg.getByLabel(/email/i).fill(actor);
  if (shotIt) await snap(page, 'agent-02-actor-popup');
  await dlg.getByRole('button', { name: /continue/i }).click();
  await dlg.waitFor({ state: 'hidden', timeout: 10_000 });
}

/** go to an agent GUI path: log in with the access key when redirected to the login page, then answer the actor popup */
async function agentGoto(page: Page, path: string, key: string, actor: string): Promise<void> {
  await page.goto(path);
  await page.waitForLoadState('networkidle').catch(() => {});
  if (page.url().includes('/_opm/login')) {
    await page.getByLabel(/access key/i).fill(key);
    await page.getByRole('button', { name: /log in/i }).click();
    await page.waitForURL((u) => !u.pathname.startsWith('/_opm/login'), { timeout: 15_000 });
    await ensureActor(page, actor, true);
    if (!page.url().endsWith(path)) {
      await page.goto(path);
      await ensureActor(page, actor);
    }
  } else await ensureActor(page, actor);
  await page.waitForLoadState('networkidle').catch(() => {});
}

async function countdownAndAccept(page: Page, shotName: string): Promise<void> {
  await page.getByRole('dialog').last().waitFor({ timeout: 15_000 });
  await snap(page, `${shotName}-countdown`);
  // after the countdown the top dialog shows the "Type ACCEPT" input
  const top = page.getByRole('dialog').filter({ hasText: /type accept/i }).last();
  const input = top.getByRole('textbox').first();
  await input.waitFor({ timeout: (CFG.approveWaitSec + 30) * 1000 });
  const startBtn = top.getByRole('button', { name: /start (update|rollback)/i });
  await input.fill('accept');
  check('agent GUI: start button disabled for a wrong word', await startBtn.isDisabled());
  await input.fill('ACCEPT');
  await snap(page, `${shotName}-accept`);
  await startBtn.click();
}

/** Updates → ?rev=<revId> → Approve → countdown → ACCEPT → returns the updateId (read from the API afterwards) */
export async function agentApproveUi(base: string, key: string, actor: string, revId: string, shotName: string): Promise<string> {
  const page = await agentPage(base);
  try {
    await agentGoto(page, revId ? `/_opm/app/updates?rev=${revId}` : '/_opm/app/updates', key, actor);
    if (!revId) {
      // dry run: open the first ready proposal of the list
      await page.locator('a[href*="rev="]').first().click();
    }
    const approveBtn = page.getByRole('button', { name: /^approve$/i });
    await approveBtn.waitFor({ timeout: 180_000 });
    await snap(page, `${shotName}-detail`);
    const started = page.waitForResponse((r) => /\/_opm\/api\/proposals\/[^/]+\/approve$/.test(new URL(r.url()).pathname) && r.request().method() === 'POST', { timeout: (CFG.approveWaitSec + 60) * 1000 });
    await approveBtn.click();
    await countdownAndAccept(page, shotName);
    const res = await started.catch(() => null); // null = no network call seen (GUI mock); caller finds the run via the API
    let updateId = '';
    if (res) {
      const body = (await res.json()) as { updateId?: string };
      if (!res.ok() || !body.updateId) throw new Error(`approve via GUI → ${res.status()} ${JSON.stringify(body).slice(0, 200)}`);
      updateId = body.updateId;
    }
    await page.getByText(/update applied|update failed|rolled back|failed-rolled-back/i).first().waitFor({ timeout: 15 * 60_000 }).catch(() => {});
    await snap(page, `${shotName}-result`);
    return updateId;
  } finally {
    await page.close();
  }
}

/** Backups → row of the snapshot → Roll back → planner → Roll back → countdown → ACCEPT */
export async function agentRollbackUi(base: string, key: string, actor: string, snapshotId: string, shotName: string): Promise<string> {
  const page = await agentPage(base);
  try {
    await agentGoto(page, '/_opm/app/backups', key, actor);
    await snap(page, `${shotName}-backups`);
    const row = snapshotId ? page.getByRole('row').filter({ hasText: snapshotId.slice(0, 10) }).first() : page.getByRole('row').filter({ hasText: /manual/i }).first();
    await row.getByRole('button', { name: /roll back/i }).click();
    const go = page.getByRole('button', { name: /^roll back$/i }).last();
    await go.waitFor({ timeout: 120_000 });
    await snap(page, `${shotName}-plan`);
    const started = page.waitForResponse((r) => /\/_opm\/api\/snapshots\/[^/]+\/rollback$/.test(new URL(r.url()).pathname) && r.request().method() === 'POST', { timeout: (CFG.approveWaitSec + 60) * 1000 });
    await go.click();
    await countdownAndAccept(page, shotName);
    const res = await started.catch(() => null); // null = no network call seen (GUI mock); caller finds the run via the API
    let updateId = '';
    if (res) {
      const body = (await res.json()) as { updateId?: string };
      if (!res.ok() || !body.updateId) throw new Error(`rollback via GUI → ${res.status()} ${JSON.stringify(body).slice(0, 200)}`);
      updateId = body.updateId;
    }
    await page.getByText(/rollback applied|rollback failed|rolled back/i).first().waitFor({ timeout: 15 * 60_000 }).catch(() => {});
    await snap(page, `${shotName}-result`);
    return updateId;
  } finally {
    await page.close();
  }
}

// ───────────────────────── hello page
/** screenshot the hello page; click = press the Increment button once (counter +1). Returns the count shown last. */
export async function helloShot(url: string, name: string, theme: string, click = false): Promise<number> {
  const page = await (await b()).newPage({ viewport: { width: 1000, height: 640 } });
  try {
    await page.goto(url);
    await page.waitForSelector('#count[data-loaded="1"]', { timeout: 15_000 });
    const before = Number(await page.textContent('#count'));
    if (click) {
      await page.click('#inc');
      await page.waitForFunction((b) => document.querySelector('#count')?.textContent !== String(b), before, { timeout: 10_000 });
    }
    const after = Number(await page.textContent('#count'));
    const t = await page.getAttribute('body', 'data-theme');
    const bg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
    const want: Record<string, string> = { green: 'rgb(22, 163, 74)', yellow: 'rgb(234, 179, 8)', blue: 'rgb(37, 99, 235)', red: 'rgb(220, 38, 38)' };
    await snap(page, name);
    check(`hello page in a browser: theme ${t}, background ${bg}${click ? `, Increment button ${before} → ${after}` : `, count ${after}`}`, t === theme && bg === want[theme] && after === before + (click ? 1 : 0));
    return after;
  } finally {
    await page.close();
  }
}

/** final evidence: screenshot the deployment pages after the run (activities, status, history, diff, overview) */
export async function cloudFinal(base: string, dep: string): Promise<void> {
  const page = await cloudPage(base);
  try {
    for (const [path, name] of [
      [`/app/deployments/${dep}`, 'cloud-90-overview'],
      [`/app/deployments/${dep}/status`, 'cloud-91-status'],
      [`/app/deployments/${dep}/activities`, 'cloud-92-activities'],
      [`/app/deployments/${dep}/history`, 'cloud-93-history'],
      [`/app/deployments/${dep}/diff`, 'cloud-94-diff'],
    ] as [string, string][]) {
      await page.goto(path);
      await page.waitForLoadState('networkidle').catch(() => {});
      await page.waitForTimeout(1500);
      await snap(page, name);
    }
    await page.goto(`/app/deployments/${dep}/activities`);
    await page.getByTestId('activity-row').first().waitFor({ timeout: 20_000 });
    const n = await page.getByTestId('activity-row').count();
    const updates = await page.getByTestId('activity-row').filter({ hasText: 'update.applied' }).count();
    check(`frontend activities page lists ${n} rows (${updates} update.applied on the first page)`, n > 0 && updates > 0);
  } finally {
    await page.close();
  }
}
