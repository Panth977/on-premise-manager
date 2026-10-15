/** Backend configuration from the environment (SPEC-final §19.3). Read lazily so tests can set env first. */
export const env = {
  get apiBase(): string {
    return (process.env.OPM_API_BASE ?? '').replace(/\/$/, '');
  },
  get frontendOrigins(): string[] {
    return (process.env.OPM_FRONTEND_ORIGINS ?? '')
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean);
  },
  /** cloud console origin sent to agents (CloudView / EnrollResponse consoleUrl): OPM_CONSOLE_URL, else the first https
   *  origin of OPM_FRONTEND_ORIGINS; null when neither is set. */
  get consoleUrl(): string | null {
    const v = (process.env.OPM_CONSOLE_URL ?? '').trim().replace(/\/$/, '');
    if (v) return v;
    return this.frontendOrigins.find((o) => o.startsWith('https://')) ?? null;
  },
  get releasePublicKey(): string {
    return process.env.OPM_RELEASE_PUBLIC_KEY ?? '';
  },
  get agentVersion(): string {
    return process.env.OPM_AGENT_VERSION ?? '';
  },
  get smtpUrl(): string {
    return process.env.OPM_SMTP_URL ?? '';
  },
  get smtpFrom(): string {
    return process.env.OPM_SMTP_FROM ?? 'On-Prem Manager <no-reply@opm.example>';
  },
  get blobRedirect(): boolean {
    return process.env.OPM_BLOB_REDIRECT === '1';
  },
  /** test hooks on (emulator, or OPM_TEST_HOOKS=1 for the local-adapter test stack, §21 #108) — never set in production */
  get emulator(): boolean {
    return process.env.FUNCTIONS_EMULATOR === 'true' || !!process.env.FIRESTORE_EMULATOR_HOST || process.env.OPM_TEST_HOOKS === '1';
  },
  /** Rate limit (per uid / per site, per instance): tokens per second + burst. */
  rate: { perSec: 20, burst: 100 },
};
