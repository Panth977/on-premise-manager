import { defineConfig, devices } from '@playwright/test';

/**
 * OPM_E2E_MODE=mock (default): Vite dev server with VITE_MOCK=1 (in-memory fake API, seeded data) — every page.
 * OPM_E2E_MODE=emu: Vite dev server with VITE_USE_EMULATORS=1 against the Firebase Auth emulator (9099) and the
 *   Functions emulator API; needs `firebase emulators:start` (backend/) and a bootstrapped admin
 *   (OPM_E2E_EMAIL / OPM_E2E_PASSWORD, default admin@example.com / admin123). Runs tests/emu/*.spec.ts.
 * OPM_E2E_MODE=local (§21 #108): a running opm-cloud (VM build, local auth) at OPM_E2E_BASE (e.g. http://localhost:8790),
 *   OPM_E2E_EMAIL / OPM_E2E_PASSWORD; no Vite dev server. Runs tests/local-auth.spec.ts.
 */
const mode = process.env.OPM_E2E_MODE === 'emu' ? 'emu' : process.env.OPM_E2E_MODE === 'prod' ? 'prod' : process.env.OPM_E2E_MODE === 'local' ? 'local' : 'mock';
const external = mode === 'prod' || mode === 'local';
// emu: 5173 is in the backend's OPM_FRONTEND_ORIGINS (CORS)
const port = Number(process.env.OPM_E2E_PORT) || (mode === 'emu' ? 5173 : 5190);

export default defineConfig({
  testDir: 'tests',
  testMatch: mode === 'emu' ? 'emu/**/*.spec.ts' : mode === 'prod' ? 'prod/**/*.spec.ts' : mode === 'local' ? 'local-auth.spec.ts' : '**/*.spec.ts',
  testIgnore: mode === 'mock' ? ['emu/**', 'prod/**'] : [],
  timeout: 60_000,
  expect: { timeout: 10_000 },
  fullyParallel: true,
  workers: process.env.CI ? 2 : 4,
  retries: mode === 'prod' ? 1 : 0,
  reporter: [['list'], ['html', { open: 'never', outputFolder: 'playwright-report' }]],
  use: {
    baseURL: mode === 'prod' ? process.env.OPM_E2E_BASE ?? 'https://opm.example.com' : mode === 'local' ? process.env.OPM_E2E_BASE ?? 'http://localhost:8790' : `http://localhost:${port}`,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    viewport: { width: 1360, height: 860 },
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'], viewport: { width: 1360, height: 860 } } }],
  webServer: external ? undefined : {
    command: mode === 'emu' ? `VITE_USE_EMULATORS=1 npx vite dev --port ${port} --strictPort` : `VITE_MOCK=1 npx vite dev --port ${port} --strictPort`,
    url: `http://localhost:${port}/login`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
});
