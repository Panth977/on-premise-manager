/**
 * The document triggers of OPM as data (name, kind, pattern, limits) — §21 #108. app.ts attaches the handlers; the
 * local DocStore uses the patterns to write outbox rows in the same transaction as the matching write (every process
 * that writes: opm-cloud, scripts, tests), the Firebase entry exports one Cloud Functions trigger per spec.
 */
import type { DocTrigger } from '../driver/index.js';

export type TriggerSpec = Omit<DocTrigger, 'handler'>;
export const TRIGGER_SPECS: TriggerSpec[] = [
  { name: 'importImage', kind: 'written', pattern: 'services/{serviceSlug}/images/{version}', timeoutSeconds: 540, memory: '1GiB' },
  { name: 'mirrorTemplateCommit', kind: 'created', pattern: 'templates/{templateId}/commits/{commitId}', timeoutSeconds: 540, memory: '256MiB' },
  { name: 'mirrorDeploymentCommit', kind: 'created', pattern: 'deployments/{deploymentId}/commits/{commitId}', timeoutSeconds: 540, memory: '256MiB' },
  { name: 'mirrorAgentCommit', kind: 'created', pattern: 'deployments/{deploymentId}/agentBranches/{siteSlug}/commits/{commitId}', timeoutSeconds: 540, memory: '256MiB' },
  { name: 'mirrorVolumeVersion', kind: 'created', pattern: 'deployments/{deploymentId}/volumeContent/{key}/versions/{versionId}', timeoutSeconds: 540, memory: '256MiB' },
  { name: 'mirrorQueue', kind: 'created', pattern: 'githubMirror/config/queue/{itemId}', timeoutSeconds: 540, memory: '1GiB' },
];
