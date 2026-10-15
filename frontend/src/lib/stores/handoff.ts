/**
 * In-memory hand-off of a just-created credential to the next page (SPEC-final §21 #84): after "Create deployment" the
 * Setup page shows the access key in RevealValue for 60 s. Module memory only — never the URL, localStorage or
 * sessionStorage; taken (cleared) by the first reader.
 */
import type { RevealResult } from '../ui/RevealValue.svelte';

let createdKey: { deploymentId: string; result: RevealResult; at: number } | null = null;

export function handOffAccessKey(deploymentId: string, result: RevealResult) {
  createdKey = { deploymentId, result, at: Date.now() };
}
/** the key handed off for this deployment (once), with the seconds already spent subtracted */
export function takeAccessKey(deploymentId: string): RevealResult | null {
  const k = createdKey;
  if (!k || k.deploymentId !== deploymentId) return null;
  createdKey = null;
  const total = k.result.hideAfterSec ?? 60;
  const left = Math.ceil(total - (Date.now() - k.at) / 1000);
  return left > 0 ? { value: k.result.value, hideAfterSec: left } : null;
}
