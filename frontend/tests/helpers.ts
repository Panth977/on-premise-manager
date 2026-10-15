import { expect, type Locator, type Page } from '@playwright/test';

export const USERS = {
  admin: { email: 'admin@example.com', password: 'admin123' },
  viewer: { email: 'viewer@example.com', password: 'viewer123' },
  ops: { email: 'ops@example.com', password: 'ops12345' },
} as const;

/** Log in through the real login form (mock mode: seeded accounts). Sets a small mock latency. */
export async function login(page: Page, who: keyof typeof USERS = 'admin', next = '/app/deployments') {
  await page.addInitScript(() => {
    try {
      localStorage.setItem('opm.mock.latency', '40');
    } catch {}
  });
  await page.goto(`/login?next=${encodeURIComponent(next)}`);
  await page.getByTestId('login-email').fill(USERS[who].email);
  await page.getByTestId('login-password').fill(USERS[who].password);
  await page.getByTestId('login-submit').click();
  await page.waitForURL((u) => u.pathname.startsWith(next.split('?')[0]!), { timeout: 15_000 });
}

export async function expectToast(page: Page, kind: 'success' | 'error', text?: string | RegExp) {
  const t = page.getByTestId(`toast-${kind}`).last();
  await expect(t).toBeVisible();
  if (text) await expect(t).toContainText(text);
}

/** Seeded ids (src/lib/mock/db.ts) */
export const SEED = {
  deployment: 'DepAcmePlant00000001',
  deploymentEmpty: 'DepNorthGrid00000002',
  template: 'TplHelloWorld0000001',
  svcWeb: 'hello-web',
  svcApi: 'hello-api',
  svcRedis: 'redis',
  svcCore: 'core-api',
  registryHub: 'RegDockerHub00000001',
  registryHubBot: 'RegDockerHubBot00001',
  registryHarbor: 'RegHarborOnPrem00001',
  iconAcme: 'IcnAcmeLogo000000001',
  page404: 'NfpHello000000000001',
  registryAr: 'RegArtifact000000001',
  uidAdmin: 'uAdmin00000000000001',
  uidViewer: 'uViewer0000000000002',
  uidOps: 'uOps0000000000000003',
} as const;

/** Open an IconPicker (trigger test id) and pick a Lucide icon by name. */
export async function pickIcon(page: Page, trigger: string, name: string) {
  await page.getByTestId(trigger).click();
  const m = page.getByTestId('icon-picker');
  await expect(m).toBeVisible();
  await m.getByTestId('ip-tab-icons').click();
  await m.getByTestId('ip-search').fill(name);
  await m.locator(`[data-testid="ip-icon"][data-name="${name}"]`).click();
  await expect(m).toHaveCount(0);
}

/** Type into a Combobox and pick the option with this data-value. */
export async function pickCombo(page: Page, testid: string, value: string, type?: string) {
  const input = page.getByTestId(testid);
  await input.click();
  if (type !== undefined) {
    // a console Combobox (input) or a shared $opm Select (button + search box `${testid}-search`, §21 #102)
    if ((await input.evaluate((e) => e.tagName)) === 'INPUT') await input.fill(type);
    else if (await page.getByTestId(`${testid}-search`).count()) await page.getByTestId(`${testid}-search`).fill(type);
  }
  await page.getByTestId(`${testid}-list`).locator(`[data-testid="combo-option"][data-value="${value}"], [role="option"][data-value="${value}"]`).first().click();
}

/** Open a GroupedSelect (e.g. a mount source `vol-<v>-source`) and pick the option with this value
 *  (`volume:<slug>`, `file:<slug>`, `secret:<slug>`, `host`, `none`, `new-volume`). */
export async function pickSource(page: Page, testid: string, value: string) {
  await page.getByTestId(testid).click();
  await page.getByTestId(`${testid}-list`).locator(`[role="option"][data-value="${value}"]`).click();
  // console GroupedSelect removes its list; the shared $opm Select hides its popover (§21 #102)
  await expect(page.getByTestId(`${testid}-list`)).toBeHidden();
}

/** Pick a value in a custom Select / GroupedSelect (there is no native <select>): click the trigger `testid`
 *  (inside `scope` when several exist, e.g. a row) and click option `${testid}-opt-${value}` in the portaled list.
 *  The option is located by its data-value attribute, so values with dots, slashes, colons or an empty value work. */
export async function pick(page: Page, testid: string, value: string, scope?: Locator) {
  const trigger = (scope ?? page).getByTestId(testid).first();
  await trigger.click();
  const list = page.getByTestId(`${testid}-list`);
  await list.locator(`[role="option"][data-value=${JSON.stringify(value)}]`).click();
  await expect(list).toHaveCount(0);
  await expect(trigger).toHaveAttribute('data-value', value);
}

/**
 * §21 #105 commit the scope's browser change set through the change bar (Commit… → message → Commit). Replaces the
 * old "Save template / Save draft" steps: a full page.goto reloads the app, so a test that reloads after an edit
 * commits first.
 */
export async function commitAll(page: Page, message = 'Test commit') {
  await page.getByTestId('change-bar-commit').click();
  const dlg = page.getByTestId('commit-dialog');
  await expect(dlg).toBeVisible();
  await dlg.getByTestId('commit-message').fill(message);
  await dlg.getByTestId('commit-submit').click();
  await expectToast(page, 'success', /Commit \d+ recorded/);
  await expect(dlg).toBeHidden();
}
/** in-app navigation (keeps the browser change set; page.goto reloads the app) */
export async function nav(page: Page, path: string) {
  await page.evaluate((p) => {
    const a = document.createElement('a');
    a.href = p;
    document.body.appendChild(a);
    a.click();
    a.remove();
  }, path);
  await page.waitForURL((u) => u.pathname + u.search === path);
}

// ───── §21 #107 local drafts (IndexedDB `opm-drafts`: stores drafts / blobs / meta)
type DraftRow = { key: string; app: string; user: string; branch: string; count: number; baseCommitId: string | null; config: unknown; blobs?: string[] };
/** run one IndexedDB op on the drafts database inside the page */
async function draftsOp<T>(page: Page, op: 'drafts' | 'blobs' | 'clear' | 'meta', arg?: unknown): Promise<T> {
  return page.evaluate(
    ({ op, arg }) =>
      new Promise<unknown>((res) => {
        const rq = indexedDB.open('opm-drafts', 1);
        rq.onupgradeneeded = () => {
          const db = rq.result;
          db.createObjectStore('drafts', { keyPath: 'key' }).createIndex('app', 'app');
          db.createObjectStore('blobs', { keyPath: 'key' }).createIndex('owner', 'owner');
          db.createObjectStore('meta', { keyPath: 'key' });
        };
        rq.onerror = () => res(null);
        rq.onsuccess = () => {
          const db = rq.result;
          const stores = ['drafts', 'blobs', 'meta'];
          const tx = db.transaction(stores, op === 'drafts' || op === 'blobs' ? 'readonly' : 'readwrite');
          if (op === 'drafts' || op === 'blobs') {
            const g = tx.objectStore(op).getAll();
            g.onsuccess = () => res(op === 'blobs' ? (g.result as { key: string; size: number }[]).map((b) => ({ key: b.key, size: b.size })) : g.result);
          } else if (op === 'clear') {
            for (const s of stores) tx.objectStore(s).clear();
            tx.oncomplete = () => res(true);
          } else {
            tx.objectStore('meta').put(arg);
            tx.oncomplete = () => res(true);
          }
        };
      }),
    { op, arg },
  ) as Promise<T>;
}
export const readDrafts = (page: Page) => draftsOp<DraftRow[]>(page, 'drafts');
export const readDraftBlobs = (page: Page) => draftsOp<{ key: string; size: number }[]>(page, 'blobs');
export const clearDrafts = (page: Page) => draftsOp<boolean>(page, 'clear');
/** pretend the console was last open at `iso` (heartbeat meta) */
export const setLastAlive = (page: Page, iso: string) => draftsOp<boolean>(page, 'meta', { key: 'alive:cloud', lastAlive: iso });
