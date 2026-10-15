/** opm-cloud operator commands (§21 #108) — all through the ports, so they work on the adapter the config selects. */
import { generateKeyPairSync, randomBytes } from 'node:crypto';
import { chmodSync, cpSync, existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fullAdminRules, type AccountDoc } from '@opm/shared';
import { authAdmin, blobs, store } from '../ports/index.js';
import { bucketShim } from '../ports/bucket-shim.js';
import type { CloudConfig } from './config.js';
import { DOCKER_VERSION, uploadAgentDir, uploadDeps } from './release.js';

type Arg = (name: string) => string | undefined;
type Flag = (name: string) => boolean;

const HELP = `usage: opm-cloud <command> [options]   (config: env OPM_* or --config <file>)
  serve                                   run the server (default)
  create-admin --email E [--password P] [--name N]
                                          create / reset an admin (prints the password only when it generated one)
  reset-link --email E                    print a one-hour password-reset link for an existing user
  release-key                             create <dataDir>/secrets/release-key.json (agent signing key) if missing; print the public key
  upload-agent --dir D --version V [--latest]
                                          upload agent binaries (signed with the data dir's release key or OPM_RELEASE_PRIVATE_KEY)
  upload-deps [--docker-version 28.5.2] [--agent-version V] [--agent-notes T] [--from-dir D]
  backup --out DIR                        consistent online backup (SQLite VACUUM INTO + blobs + secrets)
  (migrate: from the repo checkout — cd backend && npx tsx scripts/migrate-cloud.ts --from firebase --to local …)`;

export async function createAdminWith(email0: string, password: string, name: string) {
  const email = email0.trim().toLowerCase();
  const auth = authAdmin();
  let uid: string;
  try {
    const u = await auth.getUserByEmail(email);
    uid = u.uid;
    await auth.updateUser(uid, { password, displayName: name, disabled: false });
  } catch (e) {
    if ((e as { code?: string }).code !== 'auth/user-not-found') throw e;
    uid = (await auth.createUser({ email, password, displayName: name, emailVerified: true })).uid;
  }
  const ref = store().collection('accounts').doc(uid);
  const cur = (await ref.get()).data() as AccountDoc | undefined;
  const account: AccountDoc = {
    uid,
    img: cur?.img ?? null,
    name,
    email,
    phone: cur?.phone ?? null,
    access: fullAdminRules(),
    disabled: false,
    createdAt: cur?.createdAt ?? new Date().toISOString(),
    createdBy: cur?.createdBy ?? 'bootstrap',
    lastLoginAt: cur?.lastLoginAt ?? null,
    lastSetPassword: null,
    lastResetLink: null,
  };
  await ref.set(account);
  return { uid, email };
}

function releaseKeyFile(cfg: CloudConfig) {
  return path.join(cfg.dataDir, 'secrets', 'release-key.json');
}
/** Ed25519 key pair in the same format as .secrets/release-key.json ({ privateKeySeedB64, publicKeyB64 }) */
function ensureReleaseKey(cfg: CloudConfig): { privateKeySeedB64: string; publicKeyB64: string; created: boolean } {
  const f = releaseKeyFile(cfg);
  if (existsSync(f)) return { ...(JSON.parse(readFileSync(f, 'utf8')) as { privateKeySeedB64: string; publicKeyB64: string }), created: false };
  const { privateKey, publicKey } = generateKeyPairSync('ed25519');
  const seed = privateKey.export({ format: 'der', type: 'pkcs8' }).subarray(-32);
  const pub = publicKey.export({ format: 'der', type: 'spki' }).subarray(-32);
  const k = { privateKeySeedB64: Buffer.from(seed).toString('base64'), publicKeyB64: Buffer.from(pub).toString('base64') };
  mkdirSync(path.dirname(f), { recursive: true, mode: 0o700 });
  writeFileSync(f, JSON.stringify(k, null, 2) + '\n', { mode: 0o600 });
  chmodSync(f, 0o600);
  return { ...k, created: true };
}

export async function run(cmd: string, cfg: CloudConfig, arg: Arg, flag: Flag): Promise<number> {
  switch (cmd) {
    case 'help':
    case '--help':
      console.log(HELP);
      return 0;
    case 'create-admin': {
      const email = arg('email');
      if (!email) return usage('create-admin needs --email');
      const given = arg('password');
      const password = given ?? randomBytes(15).toString('base64url') + 'aA1';
      const r = await createAdminWith(email, password, arg('name') ?? 'Admin');
      // the password is printed only when this command generated it (the operator passed none)
      console.log(JSON.stringify({ ok: true, uid: r.uid, email: r.email, ...(given ? {} : { password }) }));
      return 0;
    }
    case 'reset-link': {
      const email = arg('email');
      if (!email) return usage('reset-link needs --email');
      console.log(await authAdmin().generatePasswordResetLink(email.trim().toLowerCase()));
      return 0;
    }
    case 'release-key': {
      if (cfg.adapter !== 'local') return usage('release-key manages the local data dir key (adapter local)');
      const k = ensureReleaseKey(cfg);
      console.log(JSON.stringify({ ok: true, created: k.created, file: releaseKeyFile(cfg), publicKeyB64: k.publicKeyB64 }));
      return 0;
    }
    case 'upload-agent': {
      const dir = arg('dir');
      const version = arg('version');
      if (!dir || !version || !/^[0-9A-Za-z._+-]{1,64}$/.test(version)) return usage('upload-agent needs --dir and --version');
      const seedB64 = process.env.OPM_RELEASE_PRIVATE_KEY || (cfg.adapter === 'local' ? ensureReleaseKey(cfg).privateKeySeedB64 : '');
      if (!seedB64) return usage('no release key: set OPM_RELEASE_PRIVATE_KEY');
      await uploadAgentDir({ bucket: bucketShim(blobs()), dir: path.resolve(dir), version, latest: flag('latest'), seedB64 });
      return 0;
    }
    case 'upload-deps': {
      const dockerVersion = arg('docker-version') ?? DOCKER_VERSION;
      if (!/^\d+\.\d+\.\d+$/.test(dockerVersion)) return usage('--docker-version must be an exact version');
      let agentVersion = arg('agent-version') ?? null;
      if (!agentVersion) agentVersion = (await blobs().get('agent/latest'))?.toString('utf8').trim() || null;
      const notes = process.argv.some((a) => a === '--agent-notes' || a.startsWith('--agent-notes=')) ? (arg('agent-notes') ?? '') : undefined;
      await uploadDeps({ bucket: bucketShim(blobs()), dockerVersion, agentVersion, agentNotes: notes, fromDir: arg('from-dir') });
      return 0;
    }
    case 'backup': {
      if (cfg.adapter !== 'local') return usage('backup copies the local data dir (adapter local); Firebase has its own backups');
      const out = arg('out');
      if (!out) return usage('backup needs --out <dir>');
      const dst = path.resolve(out);
      mkdirSync(dst, { recursive: true });
      const L = await import('../adapters/local/index.js');
      await L.backupSqlite(cfg.dataDir, path.join(dst, 'db'));
      for (const d of ['blobs', 'blobmeta', 'secrets']) if (existsSync(path.join(cfg.dataDir, d))) cpSync(path.join(cfg.dataDir, d), path.join(dst, d), { recursive: true, preserveTimestamps: true });
      writeFileSync(path.join(dst, 'BACKUP.json'), JSON.stringify({ createdAt: new Date().toISOString(), from: cfg.dataDir }, null, 2) + '\n');
      console.log(JSON.stringify({ ok: true, out: dst }));
      return 0;
    }
    case 'migrate':
      // needs the Firebase Admin SDK + credentials helpers: run from the repo checkout (docs-v2/self-host.md)
      console.error('migrate runs from the repository checkout (needs the Firebase Admin SDK):\n  cd backend && npx tsx scripts/migrate-cloud.ts --from firebase --to local --project <id> --data-dir <opm-cloud data dir> [--dry-run]');
      return 2;
    default:
      return usage(`unknown command ${cmd}`);
  }
}
function usage(msg: string): number {
  console.error(`${msg}\n\n${HELP}`);
  return 2;
}
