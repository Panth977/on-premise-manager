/**
 * Upload the pinned install dependencies (SPEC-final §19.1) to Storage and write deps/manifest.json (DepsManifest):
 *   deps/docker/<ver>/<amd64|arm64>/docker-<ver>.tgz — the OFFICIAL static Docker bundle
 *     https://download.docker.com/linux/static/stable/<x86_64|aarch64>/docker-<ver>.tgz (never "latest": exact version)
 *   minDockerApi — the lowest Docker Engine API the agent negotiates (1.41 = Docker 20.10; the cap 1.47 is in install.ts)
 *   agent.version — agent/latest at upload time (or --agent-version); the install script embeds the exact version + sha256
 *   agent.notes (§21 #90, optional, ≤ 8000 chars) — release notes shown on the agent GUI Agent page + cloud Setup:
 *     --agent-notes "text" | --agent-notes-file <path>; neither → the notes already in the manifest are kept when the
 *     agent version is unchanged, else null. --agent-notes "" clears them.
 *
 *   npx tsx scripts/upload-deps.ts [--docker-version 28.5.2] [--agent-version <v>] [--agent-notes "text" | --agent-notes-file f]
 *                                  [--bucket b]
 *   Emulator: FIREBASE_STORAGE_EMULATOR_HOST=127.0.0.1:9199 --project demo-opm  (or --from-dir <dir> with docker-<ver>-<arch>.tgz
 *   files for offline tests)
 * Idempotent: a file already in Storage with the same sha256 is not uploaded again.
 */
import { readFileSync } from 'node:fs';
import { arg, init } from './_admin.ts';
import { uploadDeps, DOCKER_VERSION } from '../functions/src/server/release.ts';
export { DOCKER_VERSION, MIN_DOCKER_API, AGENT_NOTES_MAX, resolveAgentNotes, depsPath, uploadDeps } from '../functions/src/server/release.ts';

if (import.meta.url === `file://${process.argv[1]}`) {
  const projectId = arg('project', 'local')!;
  const { bucket } = init(projectId, arg('bucket'));
  const dockerVersion = arg('docker-version', DOCKER_VERSION)!;
  if (!/^\d+\.\d+\.\d+$/.test(dockerVersion)) {
    console.error('--docker-version must be an exact version (e.g. 28.5.2), never "latest"');
    process.exit(2);
  }
  let agentVersion = arg('agent-version') ?? null;
  if (!agentVersion) {
    const [ex] = await bucket.file('agent/latest').exists();
    agentVersion = ex ? (await bucket.file('agent/latest').download())[0].toString('utf8').trim() || null : null;
  }
  const notesFile = arg('agent-notes-file');
  let agentNotes = process.argv.some((a) => a === '--agent-notes' || a.startsWith('--agent-notes=')) ? (arg('agent-notes') ?? '') : undefined;
  if (notesFile !== undefined) {
    if (agentNotes !== undefined) {
      console.error('use either --agent-notes or --agent-notes-file, not both');
      process.exit(2);
    }
    agentNotes = readFileSync(notesFile, 'utf8');
  }
  try {
    await uploadDeps({ bucket, dockerVersion, agentVersion, agentNotes, fromDir: arg('from-dir') });
  } catch (e) {
    console.error((e as Error).message);
    process.exit(2);
  }
  process.exit(0);
}
