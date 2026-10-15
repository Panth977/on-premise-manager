import { expect, type Page } from '@playwright/test';

export const KEY = 'K7QF-2M3X-PL4D-6WZT-R3VB-6HJN';
export const EMAIL = 'tester@acme.example';

/** mock knobs (localStorage) set before every page load */
export async function knobs(page: Page, k: Record<string, string>) {
  await page.addInitScript((kv) => {
    // init scripts also run in sandboxed srcdoc iframes (404-page thumbnails): no storage there
    try {
      for (const [a, b] of Object.entries(kv)) localStorage.setItem(a, b);
    } catch {
      /* sandboxed frame */
    }
  }, k);
}
export async function declare(page: Page, email = EMAIL) {
  await expect(page.getByTestId('actor-popup')).toBeVisible();
  await page.getByTestId('actor-email').fill(email);
  await page.getByTestId('actor-continue').click();
  await expect(page.getByTestId('actor-popup')).toBeHidden();
  await expect(page.getByTestId('actor-chip')).toContainText(email);
}
export async function login(page: Page, path = '/_opm/app', email = EMAIL) {
  await page.goto(path);
  await page.waitForURL(/\/_opm\/login/);
  await page.getByTestId('access-key').fill(KEY);
  await page.getByRole('button', { name: 'Log in' }).click();
  await page.waitForURL((u) => !u.pathname.endsWith('/login'));
  await declare(page, email);
}
/** navigate inside the logged-in app (a full load shows the actor popup again) */
export async function open(page: Page, path: string) {
  await page.goto(path);
  await declare(page);
}
export async function shot(page: Page, name: string, fullPage = true) {
  // let toasts / transitions settle; toasts never cover the evidence
  await page.waitForTimeout(250);
  const hide = await page.addStyleTag({ content: '[data-testid="toast"]{visibility:hidden !important}' }).catch(() => null);
  await page.screenshot({ path: `screenshots/${name}.png`, fullPage });
  await hide?.evaluate((el) => (el as Element).remove()).catch(() => {});
}

/** the overlay is fully inside the viewport and is the topmost element at its centre */
export async function expectOnTop(page: Page, sel: string) {
  await page.waitForFunction(() => document.getAnimations().every((a) => a.playState !== 'running'));
  const r = await page.evaluate((s) => {
    const el = document.querySelector(s) as HTMLElement | null;
    if (!el) return null;
    const b = el.getBoundingClientRect();
    const hit = document.elementFromPoint(b.left + b.width / 2, b.top + b.height / 2);
    return { b: { x: b.left, y: b.top, w: b.width, h: b.height }, vw: window.innerWidth, vh: window.innerHeight, inside: !!hit && el.contains(hit) };
  }, sel);
  expect(r, `overlay ${sel} not found`).not.toBeNull();
  expect(r!.b.w).toBeGreaterThan(20);
  expect(r!.b.x).toBeGreaterThanOrEqual(0);
  expect(r!.b.y).toBeGreaterThanOrEqual(0);
  expect(r!.b.x + r!.b.w).toBeLessThanOrEqual(r!.vw + 0.5);
  expect(r!.b.y + r!.b.h).toBeLessThanOrEqual(r!.vh + 0.5);
  expect(r!.inside, `elementFromPoint at the centre of ${sel} is not inside it`).toBe(true);
}


/**
 * Pick an option of a custom Select / MultiSelect (ui/Select.svelte) — the replacement of locator.selectOption().
 * testid = data-testid of the trigger; labelOrValue matches the option value (data-value) first, then the label.
 * MultiSelect: toggles that option and closes the list (Escape).
 */
export async function pickOption(page: Page, testid: string, labelOrValue: string) {
  const trigger = page.getByTestId(testid);
  await trigger.click();
  const list = page.getByTestId(`${testid}-list`);
  await expect(list).toBeVisible();
  const search = list.getByTestId(`${testid}-search`);
  let opt = list.locator(`[role="option"][data-value="${labelOrValue.replace(/"/g, '\\"')}"]`);
  if ((await opt.count()) === 0) {
    if (await search.count()) await search.fill(labelOrValue);
    opt = list.getByRole('option', { name: labelOrValue, exact: true });
    if ((await opt.count()) === 0) opt = list.getByRole('option', { name: labelOrValue });
  }
  await opt.first().click();
  if (await list.isVisible()) await page.keyboard.press('Escape');
  await expect(list).toBeHidden();
}

/**
 * Click a sidebar module. Deployment-wide modules are `side-<k>`; site modules are in the Sites tree under the site of
 * the current URL (else this site): `tree-<site>-<k>`, inside collapsible nodes (Dev tools › tool, Server › Docker /
 * Network / Agent / Debug) that are opened first. `config` = the config pages (Settings, then the Containers tab).
 * Client-side navigation (the mock state lives in the page).
 */
export async function nav(page: Page, k: string) {
  const wide = page.getByTestId(k === 'sites' ? 'nav-sites' : `side-${k}`);
  if (await wide.count()) return wide.click();
  const m = /\/_opm\/app\/([^/?#]+)/.exec(new URL(page.url()).pathname);
  const site = m && m[1] !== '*' ? decodeURIComponent(m[1]) : ((await page.getByTestId('this-site').textContent()) ?? '').trim();
  const open = async (node: string, probe: string) => {
    if (!(await page.getByTestId(probe).isVisible())) await page.getByTestId(`tree-toggle-${node}`).click();
  };
  await open(site, `tree-${site}-status`);
  const server = ['docker', 'network', 'agent', 'debug'];
  if (server.includes(k)) await open(`${site}-server`, `tree-${site}-${k}`);
  if (k.startsWith('devtools/')) await open(`${site}-devtools`, `tree-${site}-${k.replace('/', '-')}`);
  if (k === 'config') {
    await page.getByTestId(`tree-${site}-settings`).click();
    return page.getByTestId('nav-containers').click();
  }
  await page.getByTestId(`tree-${site}-${k.replace('/', '-')}`).click();
}

/** §21 #97: replace the text of a CodeMirror editor (data-testid on the host) — select all + insertText (one input event) */
export async function setEditor(page: Page, testid: string, text: string) {
  const host = page.getByTestId(testid);
  await expect(host).toHaveAttribute('data-ready', '1');
  await host.locator('.cm-content').click();
  await page.keyboard.press('ControlOrMeta+A');
  if (text) await page.keyboard.insertText(text);
  else await page.keyboard.press('Delete');
  // close a completion list the insert may have opened
  if (await page.locator('.cm-tooltip-autocomplete').count()) await page.keyboard.press('Escape');
}

// ───── §21 #107 local drafts (IndexedDB opm-drafts: drafts / blobs / meta)
type DraftRec = { key: string; user: string; branch: string; baseCommitId: string | null; count: number; json: string };
/** every stored draft (json = the whole record, to look for leaked values) */
export async function idbDrafts(page: Page): Promise<DraftRec[]> {
  return page.evaluate(
    () =>
      new Promise<DraftRec[]>((res) => {
        const rq = indexedDB.open('opm-drafts');
        rq.onerror = () => res([]);
        rq.onsuccess = () => {
          const db = rq.result;
          if (!db.objectStoreNames.contains('drafts')) {
            db.close();
            return res([]);
          }
          const g = db.transaction('drafts').objectStore('drafts').getAll();
          g.onsuccess = () => {
            const out = (g.result as Record<string, unknown>[]).map((r) => ({ key: String(r.key), user: String(r.user), branch: String(r.branch), baseCommitId: (r.baseCommitId as string | null) ?? null, count: Number(r.count), json: JSON.stringify(r) }));
            db.close();
            res(out);
          };
          g.onerror = () => (db.close(), res([]));
        };
      }),
  );
}
/** stored pending bytes (owner = draft key) */
export async function idbBlobs(page: Page): Promise<{ owner: string; sha256: string; size: number }[]> {
  return page.evaluate(
    () =>
      new Promise<{ owner: string; sha256: string; size: number }[]>((res) => {
        const rq = indexedDB.open('opm-drafts');
        rq.onerror = () => res([]);
        rq.onsuccess = () => {
          const db = rq.result;
          if (!db.objectStoreNames.contains('blobs')) {
            db.close();
            return res([]);
          }
          const g = db.transaction('blobs').objectStore('blobs').getAll();
          g.onsuccess = () => {
            const out = (g.result as { owner: string; sha256: string; size: number }[]).map(({ owner, sha256, size }) => ({ owner, sha256, size }));
            db.close();
            res(out);
          };
          g.onerror = () => (db.close(), res([]));
        };
      }),
  );
}
/** set the app heartbeat (meta alive:agent) — simulates "the GUI was last open at <iso>" */
export async function setAlive(page: Page, iso: string) {
  await page.evaluate(
    (t) =>
      new Promise<void>((res) => {
        const rq = indexedDB.open('opm-drafts');
        rq.onerror = () => res();
        rq.onsuccess = () => {
          const db = rq.result;
          const tx = db.transaction('meta', 'readwrite');
          tx.objectStore('meta').put({ key: 'alive:agent', lastAlive: t });
          tx.oncomplete = () => (db.close(), res());
          tx.onerror = () => (db.close(), res());
        };
      }),
    iso,
  );
}
