import { test, expect, type Page } from "@playwright/test";
import { login, expectToast, nav } from "./helpers";

/**
 * §21 #106 Admin → GitHub (mock): repository + enabled, token (masked, View 60 s, Replace, Remove), Test connection,
 * per-branch status table, Sync now / Resync everything, paused banner, sidebar entry (admins only), slugs.
 */
const P = "/app/github";

async function mockState(
  page: Page,
  s: "unconfigured" | "paused" | "disabled",
) {
  await page.evaluate(
    (x) =>
      (
        window as unknown as { __opmMockGithub: (s: string) => void }
      ).__opmMockGithub(x),
    s,
  );
  await page.reload();
}

test.describe("Admin → GitHub", () => {
  test("sidebar entry in the Admin group; the page shows repository, token, branches", async ({
    page,
  }) => {
    await login(page, "admin", "/app/deployments");
    const link = page.locator('a[href="/app/github"]').first();
    await expect(link).toBeVisible();
    await link.click();
    await page.waitForURL(`**${P}`);
    await expect(page.getByTestId("gh-owner")).toHaveValue("acme-ops");
    await expect(page.getByTestId("gh-repo")).toHaveValue("opm-mirror");
    await expect(page.getByTestId("gh-repo-link")).toHaveAttribute(
      "href",
      "https://github.com/acme-ops/opm-mirror",
    );
    await expect(page.getByTestId("gh-token-masked")).toBeVisible();
    await expect(page.getByTestId("gh-token-meta")).toContainText(
      "github_pat_•••zYxW",
    );
    await expect(page.getByTestId("gh-token-meta")).not.toContainText(
      "mockTokenValue",
    );
    await expect(page.getByTestId("gh-help")).toContainText(
      "deployment/<slug>/uncommitted/<site>",
    );
  });

  test("non-admin: no sidebar entry, the page says admins only", async ({
    page,
  }) => {
    await login(page, "viewer", "/app/deployments");
    await expect(page.locator('a[href="/app/github"]')).toHaveCount(0);
    await nav(page, P);
    await expect(page.getByText("Admins only")).toBeVisible();
    await expect(page.getByTestId("gh-owner")).toHaveCount(0);
  });

  test("branch table: every kind, links, error + notes popovers, seq · message", async ({
    page,
  }) => {
    await login(page, "admin", P);
    const rows = page.getByTestId("gh-row");
    await expect(rows.first()).toBeVisible();
    const names = await rows.evaluateAll((els) =>
      els.map((e) => e.getAttribute("data-branch")),
    );
    expect(names).toContain("template/hello-world");
    expect(names).toContain("deployment/acme-plant/cloud");
    expect(
      names.some((n) => /^deployment\/acme-plant\/agent\/[a-z0-9-]+$/.test(n!)),
    ).toBe(true);
    for (const n of names)
      expect(n).toMatch(
        /^(template\/[a-z][a-z0-9-]+|deployment\/[a-z][a-z0-9-]+\/(cloud|(agent|uncommitted)\/[a-z][a-z0-9-]+))$/,
      );
    // synced deployment row: tree + sha links
    const dep = page.locator(
      '[data-testid="gh-row"][data-branch="deployment/acme-plant/cloud"]',
    );
    await expect(dep.getByTestId("gh-status")).toHaveText("synced");
    await expect(dep.getByTestId("gh-tree-link")).toHaveAttribute(
      "href",
      "https://github.com/acme-ops/opm-mirror/tree/deployment/acme-plant/cloud",
    );
    await expect(dep.getByTestId("gh-sha-link")).toHaveText(/^[0-9a-f]{7}$/);
    await expect(dep.getByTestId("gh-sha-link")).toHaveAttribute(
      "href",
      /\/commit\/[0-9a-f]{40}$/,
    );
    // error row with popover
    const err = page
      .locator('[data-testid="gh-row"]')
      .filter({
        has: page.locator('[data-testid="gh-status"]', { hasText: "error" }),
      })
      .first();
    await expect(err).toBeVisible();
    const branch = (await err.getAttribute("data-branch"))!;
    await err.getByTestId(`gh-error-${branch}`).click();
    await expect(page.getByTestId(`gh-error-${branch}-pop`)).toContainText(
      "contents: write",
    );
    await expect(page.getByTestId(`gh-error-${branch}-pop`)).toContainText(
      "HTTP 403",
    );
    await page.keyboard.press("Escape");
    await expect(page.getByTestId(`gh-error-${branch}-pop`)).toHaveCount(0);
    // notes popover
    await dep.getByTestId("gh-notes-deployment/acme-plant/cloud").click();
    await expect(
      page.getByTestId("gh-notes-deployment/acme-plant/cloud-pop"),
    ).toContainText("HEALTH_LOG");
    await page.keyboard.press("Escape");
    // a never-pushed template row is queued
    await expect(page.getByTestId("gh-summary")).toContainText("branches");
    // filter
    await page.getByTestId("gh-kind").getByText("Problems").click();
    await expect(rows).toHaveCount(1);
    await page.getByTestId("gh-kind").getByText("Templates").click();
    for (const n of await rows.evaluateAll((els) =>
      els.map((e) => e.getAttribute("data-branch")),
    ))
      expect(n).toMatch(/^template\//);
  });

  test("token: View 60 s with the ring, re-masks; Replace (validation) and Remove", async ({
    page,
  }) => {
    await page.clock.install();
    await login(page, "admin", P);
    await page.getByTestId("gh-token-view").click();
    await expect(page.getByTestId("gh-token-value")).toHaveText(/^github_pat_/);
    const left = Number(
      await page.getByTestId("gh-token-countdown").getAttribute("data-left"),
    );
    expect(left).toBeGreaterThanOrEqual(58);
    await page.clock.fastForward(61_000);
    await expect(page.getByTestId("gh-token-value")).toHaveCount(0);
    await expect(page.getByTestId("gh-token-masked")).toBeVisible();
    // Replace: validation, then save
    await page.getByTestId("gh-token-replace").click();
    const m = page.getByTestId("gh-token-modal");
    await expect(m).toContainText("Contents: Read and write");
    await m.getByTestId("gh-token-input").fill("short");
    await m.getByTestId("gh-token-save").click();
    await expect(m).toContainText("20–255 printable characters");
    await m
      .getByTestId("gh-token-input")
      .fill("github_pat_11NEWTOKEN000000000000_replacementValueAbcd");
    await m.getByTestId("gh-token-save").click();
    await expectToast(page, "success", "Token saved");
    await expect(page.getByTestId("gh-token-meta")).toContainText(
      "github_pat_•••Abcd",
    );
    // the audit log shows the change, never the value
    await nav(page, "/app/audit-log");
    await expect(page.getByText("github.updated").first()).toBeVisible();
    await expect(page.locator("body")).not.toContainText(
      "replacementValueAbcd",
    );
    await nav(page, P);
    // Remove
    await page.getByTestId("gh-token-remove").click();
    await page
      .getByTestId("gh-token-remove-dialog")
      .getByRole("button", { name: "Remove token" })
      .click();
    await expectToast(page, "success", "Token removed");
    await expect(page.getByTestId("gh-token-none")).toBeVisible();
    await expect(page.getByTestId("gh-enabled")).not.toBeChecked();
  });

  test("repository: validation, save, disable / enable", async ({ page }) => {
    await login(page, "admin", P);
    await page.getByTestId("gh-owner").fill("-bad owner");
    await page.getByTestId("gh-save").click();
    await expect(page.getByTestId("gh-repo-card")).toContainText(
      "A GitHub user or organization",
    );
    await page.getByTestId("gh-owner").fill("acme-ops");
    await page.getByTestId("gh-repo").fill("a/b");
    await expect(page.getByTestId("gh-repo-card")).toContainText(
      "Letters, digits",
    );
    await expect(page.getByTestId("gh-save")).toBeDisabled();
    await page.getByTestId("gh-repo").fill("opm-backup");
    await page.getByTestId("gh-save").click();
    await expectToast(page, "success", "GitHub mirror saved");
    await expect(page.getByTestId("gh-repo-link")).toHaveAttribute(
      "href",
      "https://github.com/acme-ops/opm-backup",
    );
    // a new repository: every branch is pushed again
    await expect(
      page
        .locator('[data-testid="gh-status"]')
        .filter({ hasText: /queued|pushing/ })
        .first(),
    ).toBeVisible();
    await expect
      .poll(
        async () =>
          page
            .locator('[data-testid="gh-status"]', { hasText: "synced" })
            .count(),
        { timeout: 15_000 },
      )
      .toBeGreaterThan(3);
    // disable
    await page.getByTestId("gh-enabled").click();
    await page.getByTestId("gh-save").click();
    await expectToast(page, "success", "GitHub mirror saved");
    await expect(page.getByTestId("gh-disabled")).toBeVisible();
    await expect(page.getByTestId("gh-sync")).toBeDisabled();
  });

  test("Test connection: ok, then a missing repository fails with the reason", async ({
    page,
  }) => {
    await login(page, "admin", P);
    await page.getByTestId("gh-test").click();
    await expectToast(page, "success", "Connection OK");
    await expect(page.getByTestId("gh-test-summary")).toContainText("OK");
    await expect(page.getByTestId("gh-test-summary")).toContainText(
      "acme-ops-bot",
    );
    await expect(page.getByTestId("gh-check-contents-write")).toContainText(
      "can write",
    );
    await page.getByTestId("gh-repo").fill("missing-repo");
    await page.getByTestId("gh-save").click();
    await expectToast(page, "success", "GitHub mirror saved");
    await page.getByTestId("gh-test").click();
    await expectToast(page, "error", "Connection failed");
    await expect(page.getByTestId("gh-check-repo")).toContainText("404");
    await expect(page.getByTestId("gh-test-summary")).toContainText("failed");
  });

  test("Sync now retries the failing branch; Resync asks first", async ({
    page,
  }) => {
    await login(page, "admin", P);
    await expect(
      page.locator('[data-testid="gh-status"]', { hasText: "error" }),
    ).toHaveCount(1);
    await page.getByTestId("gh-sync").click();
    await expectToast(page, "success", "Sync now");
    await expect(
      page.locator('[data-testid="gh-status"]', { hasText: "pushing" }).first(),
    ).toBeVisible();
    // the mock fails the first retry of the 403 branch once more, then a second Sync pushes it
    await expect
      .poll(
        async () =>
          page
            .locator('[data-testid="gh-status"]', { hasText: /pushing|queued/ })
            .count(),
        { timeout: 15_000 },
      )
      .toBe(0);
    await page.getByTestId("gh-sync").click();
    await expect
      .poll(
        async () =>
          page
            .locator('[data-testid="gh-status"]', { hasText: "error" })
            .count(),
        { timeout: 15_000 },
      )
      .toBe(0);
    // per-row sync
    await page
      .locator('[data-testid="gh-row"][data-branch="template/hello-world"]')
      .getByTestId("gh-row-sync")
      .click();
    await expectToast(page, "success", "Sync template/hello-world");
    // Resync confirm
    await page.getByTestId("gh-resync").click();
    const dlg = page.getByTestId("gh-resync-dialog");
    await expect(dlg).toContainText("FORCE-pushed");
    await dlg.getByRole("button", { name: "Rebuild and force-push" }).click();
    await expectToast(page, "success", "Resync started");
    await expect(
      page.locator('[data-testid="gh-status"]', { hasText: "pushing" }).first(),
    ).toBeVisible();
  });

  test("paused banner; not configured = setup steps", async ({ page }) => {
    await login(page, "admin", P);
    await mockState(page, "paused");
    await expect(page.getByTestId("gh-paused")).toContainText("rate limit");
    await expect(
      page.locator('[data-testid="gh-status"]', { hasText: "paused" }).first(),
    ).toBeVisible();
    await mockState(page, "unconfigured");
    await expect(page.getByTestId("gh-setup")).toContainText(
      "Contents: Read and write",
    );
    await expect(page.getByTestId("gh-token-none")).toBeVisible();
    await expect(page.getByTestId("gh-test")).toBeDisabled();
    await expect(page.getByTestId("gh-sync")).toHaveCount(0);
    // configure from scratch
    await page.getByTestId("gh-owner").fill("acme-ops");
    await page.getByTestId("gh-repo").fill("opm-mirror");
    await page.getByTestId("gh-enabled").click();
    await page.getByTestId("gh-save").click();
    await expect(page.getByTestId("gh-repo-card")).toContainText(
      "Add a token first",
    );
    await page.getByTestId("gh-enabled").click();
    await page.getByTestId("gh-save").click();
    await expectToast(page, "success", "GitHub mirror saved");
    await page.getByTestId("gh-token-add").click();
    await page
      .getByTestId("gh-token-input")
      .fill("github_pat_11FRESH00000000000000_freshTokenValueQrst");
    await page.getByTestId("gh-token-save").click();
    await expectToast(page, "success", "Token saved");
    await page.getByTestId("gh-enabled").click();
    await page.getByTestId("gh-save").click();
    await expectToast(page, "success", "Every branch is queued");
    await expect
      .poll(
        async () =>
          page
            .locator('[data-testid="gh-status"]', { hasText: "synced" })
            .count(),
        { timeout: 15_000 },
      )
      .toBeGreaterThan(3);
  });
});

test.describe("entity slugs (§21 #106)", () => {
  test("template: slug preview from the label, custom slug, fixed afterwards", async ({
    page,
  }) => {
    await login(page, "admin", "/app/templates");
    await page.getByTestId("new-template").click();
    await page.getByTestId("nt-label").fill("Plant Ünïcode 7");
    await expect(page.getByTestId("nt-slug")).toHaveValue("plant-unicode-7");
    await expect(page.getByTestId("template-modal")).toContainText(
      "template/plant-unicode-7",
    );
    await page.getByTestId("nt-slug-edit").click();
    await page.getByTestId("nt-slug").fill("Bad Slug");
    await page.getByTestId("nt-create").click();
    await expect(page.getByTestId("template-modal")).toContainText(
      "starting with a letter",
    );
    await page.getByTestId("nt-slug").fill("plant-7");
    await page.getByTestId("nt-create").click();
    await expectToast(page, "success", "created");
    await expect(page.getByTestId("tpl-slug")).toHaveValue("plant-7");
    await expect(page.getByTestId("tpl-slug")).toBeDisabled();
  });

  test("template: a taken slug answers 409 (slug-taken)", async ({ page }) => {
    await login(page, "admin", "/app/templates");
    await page.getByTestId("new-template").click();
    await page.getByTestId("nt-label").fill("Hello world");
    await page.getByTestId("nt-slug-edit").click();
    await page.getByTestId("nt-slug").fill("hello-world");
    await page.getByTestId("nt-create").click();
    await expectToast(page, "error", "already used");
  });

  test("deployment: slug derived at create (unique), shown on the overview, locked in Edit", async ({
    page,
  }) => {
    await login(page, "admin", "/app/deployments");
    await page.getByTestId("new-deployment").click();
    await page.getByTestId("dep-label").fill("ACME plant");
    await expect(page.getByTestId("dep-slug")).toHaveValue("acme-plant");
    await page.getByTestId("dep-src-empty").click();
    await page.getByTestId("dep-save").click();
    await page.waitForURL(/\/setup$/);
    await nav(page, new URL(page.url()).pathname.replace(/\/setup$/, ""));
    // the server made it unique (acme-plant exists)
    await expect(page.getByTestId("dep-slug-meta")).toContainText(
      "acme-plant-2",
    );
    await page.getByTestId("edit-deployment").click();
    await expect(page.getByTestId("dep-slug")).toHaveValue("acme-plant-2");
    await expect(page.getByTestId("dep-slug")).toHaveAttribute("readonly", "");
  });
});
