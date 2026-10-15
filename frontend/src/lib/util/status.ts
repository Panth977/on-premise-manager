import type { ImportStatus, ProposalStatus, StagingState, TransferStatus } from '@opm/shared';

export type Tone = 'green' | 'amber' | 'red' | 'blue' | 'violet' | 'gray' | 'accent';

export const PROPOSAL_TONE: Record<ProposalStatus, Tone> = {
  published: 'blue',
  staging: 'blue',
  ready: 'violet',
  approved: 'accent',
  applying: 'amber',
  applied: 'green',
  'failed-rolled-back': 'red',
  stale: 'amber',
  superseded: 'gray',
};
export const IMPORT_TONE: Record<ImportStatus, Tone> = { pending: 'gray', copying: 'blue', ready: 'green', failed: 'red' };
export const STAGING_TONE: Record<StagingState, Tone> = { pending: 'gray', downloading: 'blue', ready: 'green', failed: 'red' };
export const TRANSFER_TONE: Record<TransferStatus, Tone> = { uploading: 'blue', ready: 'violet', 'saved-on-server': 'green', downloaded: 'green', expired: 'gray' };
export const TOKEN_TONE: Record<'active' | 'used' | 'expired' | 'revoked', Tone> = { active: 'green', used: 'blue', expired: 'gray', revoked: 'red' };

export function toneOf(kind: string, value: string): Tone {
  const maps: Record<string, Record<string, Tone>> = { proposal: PROPOSAL_TONE, import: IMPORT_TONE, staging: STAGING_TONE, transfer: TRANSFER_TONE, token: TOKEN_TONE };
  return maps[kind]?.[value] ?? 'gray';
}
