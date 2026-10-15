/**
 * §21 #106 screenshots → screenshots/gh-*.png (light, dark, 390 px): Admin → GitHub (configured, with an error row,
 * the notes popover, the token modal) and the not-configured setup. Skipped unless OPM_SCREENSHOTS=1.
 */
import { expect, test, type Page } from "@playwright/test";
import { login } from "./helpers";

test.skip(!process.env.OPM_SCREENSHOTS, "set OPM_SCREENSHOTS=1");
const still = (page: Page) => page.waitForTimeout(500);

for (const [name, theme, width] of [
  ["light", "light", 1360],
  ["dark", "dark", 1360],
  ["m", "light", 390],
] as const) {
  test(`gh screens ${name}`, async ({ page }) => {
    await page.setViewportSize({ width, height: width < 600 ? 844 : 900 });
    await page.addInitScript(
      (t) => localStorage.setItem("opm.theme", t),
      theme,
    );
    await login(page, "admin", "/app/github");
    await page.evaluate(() =>
      (window as unknown as { __opmMockReset: () => void }).__opmMockReset(),
    );
    await page.reload();
    const sfx = name === "light" ? "" : `-${name}`;
    await expect(page.getByTestId("gh-row").first()).toBeVisible();
    await page.getByTestId("gh-test").click();
    await expect(page.getByTestId("gh-test-summary")).toContainText("OK");
    await page.mouse.move(1, 1); // no hover tooltip
    await still(page);
    // no horizontal page scroll (the table scrolls in its own container)
    const sw = await page.evaluate(
      () =>
        document.documentElement.scrollWidth -
        document.documentElement.clientWidth,
    );
    expect(sw).toBeLessThanOrEqual(0);
    await page.screenshot({
      path: `screenshots/gh-page${sfx}.png`,
      fullPage: true,
    });
    await page.getByTestId("gh-branches-card").scrollIntoViewIfNeeded();
    await page.getByTestId("gh-notes-deployment/acme-plant/cloud").click();
    await still(page);
    await page.screenshot({ path: `screenshots/gh-notes${sfx}.png` });
    await page.keyboard.press("Escape");
    await page.getByTestId("gh-token-replace").click();
    await still(page);
    await page.screenshot({ path: `screenshots/gh-token-modal${sfx}.png` });
    await page.keyboard.press("Escape");
    await page.evaluate(() =>
      (
        window as unknown as { __opmMockGithub: (s: string) => void }
      ).__opmMockGithub("unconfigured"),
    );
    await page.reload();
    await expect(page.getByTestId("gh-setup")).toBeVisible();
    await still(page);
    await page.screenshot({
      path: `screenshots/gh-setup${sfx}.png`,
      fullPage: true,
    });
  });
}
