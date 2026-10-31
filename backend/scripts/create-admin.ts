/**
 * Bootstrap the first admin (SPEC-final §5.1): Firebase Auth user + accounts/{uid} with fullAdminRules(), createdBy "bootstrap".
 *   Emulator: FIREBASE_AUTH_EMULATOR_HOST=127.0.0.1:9099 FIRESTORE_EMULATOR_HOST=127.0.0.1:8085 \
 *             npx tsx scripts/create-admin.ts --project demo-opm --email admin@opm.test --password secret123 --name Admin
 *   Prod (ADC): npx tsx scripts/create-admin.ts --email admin@opm.test --out ../.secrets/admin.json
 * Without --password a strong random password is generated. Idempotent: an existing user gets the new password and
 * the account doc is (re)written with full admin rules. --out writes { email, password, uid, projectId } (0600).
 */
import { randomBytes } from 'node:crypto';
import { writeFileSync, mkdirSync } from 'node:fs';
import { dirname } from 'node:path';
import { fullAdminRules, type AccountDoc } from '@opm/shared';
import { arg, init } from './_admin.ts';

export async function createAdmin(o: { projectId: string; email: string; password: string; name: string }) {
  const { auth, db } = init(o.projectId);
  const email = o.email.trim().toLowerCase();
  let uid: string;
  try {
    const u = await auth.getUserByEmail(email);
    uid = u.uid;
    await auth.updateUser(uid, { password: o.password, displayName: o.name, disabled: false });
  } catch (e) {
    if ((e as { code?: string }).code !== 'auth/user-not-found') throw e;
    const u = await auth.createUser({ email, password: o.password, displayName: o.name, emailVerified: true });
    uid = u.uid;
  }
  const ref = db.collection('accounts').doc(uid);
  const cur = (await ref.get()).data() as AccountDoc | undefined;
  const account: AccountDoc = {
    uid,
    img: cur?.img ?? null,
    name: o.name,
    email,
    phone: cur?.phone ?? null,
    access: fullAdminRules(),
    disabled: false,
    createdAt: cur?.createdAt ?? new Date().toISOString(),
    createdBy: cur?.createdBy ?? 'bootstrap',
    lastLoginAt: cur?.lastLoginAt ?? null,
    // §21 #84: the bootstrap password is not stored in Firestore (it is in --out / printed); the password changed → nothing to reveal
    lastSetPassword: null,
    lastResetLink: null,
  };
  await ref.set(account);
  return { uid, email };
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const projectId = arg('project', 'local')!;
  const email = arg('email');
  if (!email) {
    console.error('usage: create-admin --email <email> [--password <pw>] [--name <name>] [--project <id>] [--out <file>]');
    process.exit(2);
  }
  const password = arg('password') ?? randomBytes(18).toString('base64url') + 'aA1!';
  const name = arg('name', 'Admin')!;
  const r = await createAdmin({ projectId, email, password, name });
  const out = arg('out');
  if (out) {
    mkdirSync(dirname(out), { recursive: true });
    writeFileSync(out, JSON.stringify({ email: r.email, password, uid: r.uid, projectId }, null, 2) + '\n', { mode: 0o600 });
  }
  console.log(JSON.stringify({ ok: true, uid: r.uid, email: r.email, projectId, ...(out ? { written: out } : { password }) }));
  process.exit(0);
}
