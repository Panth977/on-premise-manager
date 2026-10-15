// Harness configuration. Everything is overridable by env; defaults = the emulator dev loop on this Mac.
import { existsSync, readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

export const E2E_DIR = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
export const OPM_DIR = resolve(E2E_DIR, '..');
export const SECRETS_DIR = existsSync(join(OPM_DIR, '.secrets')) ? join(OPM_DIR, '.secrets') : resolve(OPM_DIR, '../.secrets');

function readJson<T>(p: string): T | null {
  try {
    return JSON.parse(readFileSync(p, 'utf8')) as T;
  } catch {
    return null;
  }
}

const admin = readJson<{ email: string; password: string }>(join(SECRETS_DIR, 'admin.json'));
const web = readJson<{ apiKey: string; projectId: string }>(join(SECRETS_DIR, 'firebase-web-config.json'));
const e = process.env;

export const CFG = {
  /** cloud API base; requests go to apiBase + "/v1/…" */
  apiBase: (e.OPM_API_BASE ?? 'http://127.0.0.1:5002').replace(/\/$/, ''),
  /** cloud frontend origin (Playwright) */
  frontendUrl: (e.OPM_FRONTEND_URL ?? e.OPM_API_BASE ?? 'http://127.0.0.1:5002').replace(/\/$/, ''),
  /** "host:port" of the Firebase Auth emulator, or "" for real Firebase Auth */
  authEmulator: e.OPM_AUTH_EMULATOR ?? (e.OPM_API_BASE && !/127\.0\.0\.1|localhost/.test(e.OPM_API_BASE) ? '' : '127.0.0.1:9099'),
  firebaseApiKey: e.OPM_FIREBASE_API_KEY ?? web?.apiKey ?? 'fake-api-key',
  adminEmail: e.OPM_ADMIN_EMAIL ?? admin?.email ?? 'admin@opm.test',
  adminPassword: e.OPM_ADMIN_PASSWORD ?? admin?.password ?? 'admin-password-123',
  registryUrl: e.OPM_E2E_REGISTRY_URL ?? 'https://asia-south1-docker.pkg.dev',
  repoPrefix: e.OPM_E2E_REPO_PATH ?? 'example/opm-test',
  registryKeyFile: e.OPM_E2E_REGISTRY_KEY ?? join(SECRETS_DIR, 'registry-reader-key.json'),
  /** agent binary; "" = download it through /v1/install/binary (the real install path) */
  agentBin: e.OPM_AGENT_BIN ?? '',
  dockerHost: e.DOCKER_HOST ?? `unix://${e.HOME}/.colima/default/docker.sock`,
  approveWaitSec: Number(e.OPM_APPROVE_WAIT_SEC ?? 3),
  /** run the Playwright UI flows (cloud frontend + agent GUI) */
  ui: (e.OPM_E2E_UI ?? '1') !== '0',
  /** cloud frontend flows (OPM_E2E_UI_CLOUD=0 to skip only those) */
  uiCloud: (e.OPM_E2E_UI ?? '1') !== '0' && (e.OPM_E2E_UI_CLOUD ?? '1') !== '0',
  /** stop after this step number (debug) */
  untilStep: Number(e.OPM_E2E_UNTIL ?? 99),
  runDir: join(E2E_DIR, 'run'),
  resultsDir: join(E2E_DIR, 'results'),
  screenshotsDir: join(E2E_DIR, 'screenshots'),
  actor: 'ops@example.com',
  sites: {
    app: { agentPort: 7080, guiAliasPort: 8090, edgePort: 8081 },
    db: { agentPort: 7081, guiAliasPort: 8091, edgePort: 8082 },
  } as Record<'app' | 'db', { agentPort: number; guiAliasPort: number; edgePort: number }>,
};
export type SiteName = 'app' | 'db';
