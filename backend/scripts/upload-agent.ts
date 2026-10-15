/**
 * Upload built agent binaries to Storage (SPEC-final §2.3): agent/<version>/<os>-<arch>/opm-agent + .sha256 (hex) + .sig
 * (base64url Ed25519 signature of the lowercase sha256 hex string by the release key) and optionally agent/latest.
 *
 *   npx tsx scripts/upload-agent.ts --dir ../agent/dist --version 0.1.0 [--latest] [--project your-firebase-project] [--bucket b]
 *
 * --dir holds either files named opm-agent-<os>-<arch> (e.g. opm-agent-linux-arm64) or <os>-<arch>/opm-agent.
 * §21 #91: <dir>/release.json (make dist: {version, schema, minSchema, commit, builtAt}) → agent/<version>/release.json
 * (its version must equal --version; absent = an old build, schema unknown = legacy 1).
 * Release key: env OPM_RELEASE_PRIVATE_KEY (32-byte seed, base64) or ../.secrets/release-key.json (privateKeySeedB64).
 * Emulator: set FIREBASE_STORAGE_EMULATOR_HOST=127.0.0.1:9199 (and --project demo-opm).
 * Self-hosted opm-cloud (§21 #108): --adapter local --data-dir <opm-cloud data dir> writes the VM's BlobStore.
 */
import { existsSync, readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { arg, flag, init } from './_admin.ts';
import { uploadAgentDir } from '../functions/src/server/release.ts';
export { signHex, readReleaseMeta, findBinaries, uploadAgentDir } from '../functions/src/server/release.ts';

const here = dirname(fileURLToPath(import.meta.url));

export function releaseKey(): { seedB64: string } {
  if (process.env.OPM_RELEASE_PRIVATE_KEY) return { seedB64: process.env.OPM_RELEASE_PRIVATE_KEY };
  const f = resolve(here, '../../.secrets/release-key.json');
  if (!existsSync(f)) throw new Error('no release key: set OPM_RELEASE_PRIVATE_KEY or create .secrets/release-key.json');
  return { seedB64: JSON.parse(readFileSync(f, 'utf8')).privateKeySeedB64 };
}
if (import.meta.url === `file://${process.argv[1]}`) {
  const dir = arg('dir');
  const version = arg('version');
  if (!dir || !version || !/^[0-9A-Za-z._+-]{1,64}$/.test(version)) {
    console.error('usage: upload-agent --dir <dir> --version <v> [--latest] [--project <id>] [--bucket <name>] [--adapter local --data-dir <dir>]');
    process.exit(2);
  }
  const projectId = arg('project', process.env.GCLOUD_PROJECT ?? 'your-firebase-project')!;
  const { bucket } = init(projectId, arg('bucket'));
  let seedB64: string;
  try {
    seedB64 = releaseKey().seedB64;
    await uploadAgentDir({ bucket, dir: resolve(dir), version, latest: flag('latest'), seedB64 });
  } catch (e) {
    console.error((e as Error).message);
    process.exit(/no binaries/.test((e as Error).message) ? 1 : 2);
  }
  process.exit(0);
}
