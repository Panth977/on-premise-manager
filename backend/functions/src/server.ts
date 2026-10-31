/**
 * opm-cloud — the OPM cloud as ONE Node process for a VM (SPEC §21 #108).
 *
 *   node dist/opm-cloud.cjs [serve]            API (/v1) + /auth + /_blob + jobs + outbox dispatcher + static frontend
 *   node dist/opm-cloud.cjs create-admin --email E [--password P] [--name N]
 *   node dist/opm-cloud.cjs reset-link --email E
 *   node dist/opm-cloud.cjs release-key          create the data dir's agent release key (prints the public key)
 *   node dist/opm-cloud.cjs upload-agent --dir D --version V [--latest]
 *   node dist/opm-cloud.cjs upload-deps [--docker-version 28.5.2] [--agent-version V] [--agent-notes T] [--from-dir D]
 *   node dist/opm-cloud.cjs backup --out DIR    consistent online copy (SQLite VACUUM INTO + blobs + secrets)
 *
 * Config: env (OPM_DATA_DIR, OPM_PUBLIC_URL, OPM_PORT, OPM_STATIC_DIR, OPM_OIDC_*, OPM_SMTP_URL …) or a JSON
 * file (--config / OPM_CONFIG), see docs-v2/self-host.md.
 */
import { setPorts, logger } from './ports/index.js';
import { handleAuthHttp, handleBlobHttp, localPorts, startOutboxDispatcher, sweepDocTtl } from './adapters/local/index.js';
import { applyEnv, loadConfig, type CloudConfig } from './server/config.js';

export const LIFECYCLE_RULES = [{ prefixes: ['staging/'], ageDays: 1 }]; // = backend/storage.lifecycle.json

function argOf(name: string, argv = process.argv): string | undefined {
  const i = argv.indexOf(`--${name}`);
  if (i >= 0 && i + 1 < argv.length && !argv[i + 1]!.startsWith('--')) return argv[i + 1];
  const eq = argv.find((a) => a.startsWith(`--${name}=`));
  return eq ? eq.slice(name.length + 3) : undefined;
}

export function installPorts(cfg: CloudConfig) {
  const p = localPorts({ dataDir: cfg.dataDir, publicUrl: cfg.publicUrl, consoleUrl: cfg.consoleUrl });
  setPorts(p);
  return p;
}

async function serve(cfg: CloudConfig) {
  const lp = installPorts(cfg);
  const { createServer, VERSION } = await import('./server/http.js');
  const { TRIGGERS, JOBS } = await import('./app.js');
  const { sendMail } = await import('./routes/accounts.js');
  const { env } = await import('./core/env.js');
  const stops: (() => Promise<void>)[] = [];
  const local: import('./server/http.js').LocalHttpHandlers = {
    blob: (r) => handleBlobHttp(r, lp.blobs, { allowedOrigins: cfg.frontendOrigins }),
    auth: (r) => handleAuthHttp(r, lp.auth, { sendMail: env.smtpUrl ? sendMail : undefined, oidc: cfg.oidc, consoleUrl: cfg.consoleUrl }),
    authConfig: () => ({ provider: 'local', passwordReset: !!env.smtpUrl, oidc: cfg.oidc ? { label: cfg.oidc.label } : null }),
  };
  if (cfg.runJobs) {
    const { startScheduler } = await import('./server/scheduler.js');
    const d = startOutboxDispatcher(lp.store, TRIGGERS, { concurrency: 4 });
    stops.push(() => d.stop());
    const s = startScheduler([
      ...JOBS,
      { name: 'docTtl', schedule: 'every 10 minutes', timeoutSeconds: 300, memory: '256MiB', run: async () => void (await sweepDocTtl(lp.store, Date.now())) },
      { name: 'blobLifecycle', schedule: 'every 60 minutes', timeoutSeconds: 900, memory: '256MiB', run: async () => void (await lp.blobs.sweep(LIFECYCLE_RULES)) },
    ]);
    stops.push(() => s.stop());
  }
  const server = createServer(cfg, local);
  // §21 #125 port tunnels: listen on the public port of every stored tunnel
  const { startPortTunnels, stopPortTunnels } = await import('./core/port-tunnels.js');
  const { allPortTunnels } = await import('./routes/port-tunnels.js');
  startPortTunnels(await allPortTunnels());
  stops.push(async () => stopPortTunnels());
  server.requestTimeout = 15 * 60_000;
  server.keepAliveTimeout = 65_000;
  server.headersTimeout = 70_000;
  await new Promise<void>((resolve) => server.listen(cfg.port, cfg.host, resolve));
  logger.info('opm-cloud listening', { version: VERSION, url: cfg.publicUrl, port: cfg.port, dataDir: cfg.dataDir });
  let closing = false;
  const shutdown = async (sig: string) => {
    if (closing) return;
    closing = true;
    logger.info('opm-cloud stopping', { signal: sig });
    server.close();
    server.closeIdleConnections?.();
    const hard = setTimeout(() => process.exit(0), 20_000);
    hard.unref();
    for (const s of stops) await s().catch(() => undefined);
    await lp.close().catch(() => undefined);
    process.exit(0);
  };
  process.on('SIGTERM', () => void shutdown('SIGTERM'));
  process.on('SIGINT', () => void shutdown('SIGINT'));
}

async function main() {
  const argv = process.argv.slice(2);
  const cmd = argv[0] && !argv[0].startsWith('--') ? argv[0] : 'serve';
  const cfg = loadConfig();
  applyEnv(cfg);
  if (cmd === 'serve') return serve(cfg);
  const ops = await import('./server/ops.js');
  installPorts(cfg);
  const code = await ops.run(cmd, cfg, (n: string) => argOf(n), (n: string) => process.argv.includes(`--${n}`));
  process.exit(code);
}

// esbuild bundles this file as dist/opm-cloud.cjs (tsx can run it from source too); imported by tests → not main
const isMain = /(opm-cloud(\.c?js)?|server\.(ts|js|cjs))$/.test(process.argv[1] ?? '') && process.env.OPM_CLOUD_NO_MAIN !== '1';
if (isMain)
  main().catch((e) => {
    logger.error('opm-cloud failed', { error: (e as Error)?.stack ?? String(e) });
    process.exit(1);
  });
