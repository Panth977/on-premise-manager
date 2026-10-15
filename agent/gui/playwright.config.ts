import { defineConfig, devices } from '@playwright/test';

// mock project: the mock build (VITE_MOCK=1) served by tests/serve.mjs (same routing rules as the Go agent).
// real project: OPM_AGENT_URL=http://127.0.0.1:8090 OPM_ACCESS_KEY=… npm run test:e2e:real
const real = !!process.env.OPM_E2E_REAL;
// parallel work (§21 #102 rounds): OPM_MOCK_DIR (a mock build dir) + OPM_E2E_PORT isolate one run from another
const mockDir = process.env.OPM_MOCK_DIR ?? 'build-mock';
const port = Number(process.env.OPM_E2E_PORT ?? 4173);
const origin = `http://127.0.0.1:${port}`;
export default defineConfig({
  testDir: './tests',
  timeout: 60_000,
  expect: { timeout: 10_000 },
  fullyParallel: true,
  workers: real ? 1 : 4,
  reporter: [['list']],
  use: { trace: 'retain-on-failure', screenshot: 'only-on-failure' },
  webServer: real ? undefined : { command: `node tests/serve.mjs ${mockDir} ${port}`, url: `${origin}/_opm/healthz`, reuseExistingServer: true },
  projects: real
    ? [{ name: 'real', testMatch: /real\.spec\.ts/, use: { ...devices['Desktop Chrome'], baseURL: process.env.OPM_AGENT_URL ?? 'http://127.0.0.1:8090' } }]
    : [
        { name: 'desktop', testIgnore: /real\.spec\.ts|mobile\.spec\.ts/, use: { ...devices['Desktop Chrome'], viewport: { width: 1360, height: 900 }, baseURL: origin } },
        { name: 'mobile', testMatch: /mobile\.spec\.ts/, use: { ...devices['Pixel 7'], viewport: { width: 390, height: 844 }, baseURL: origin } },
      ],
});
