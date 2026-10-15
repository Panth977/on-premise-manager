import type { ProposalStatus, StagingState } from '@opm/shared/types';
export type Tone = '' | 'ok' | 'warn' | 'bad' | 'info' | 'accent';

export const proposalTone: Record<ProposalStatus, Tone> = {
  published: 'info',
  staging: 'info',
  ready: 'accent',
  approved: 'warn',
  applying: 'warn',
  applied: 'ok',
  'failed-rolled-back': 'bad',
  stale: '',
  superseded: '',
};
export const stagingTone: Record<StagingState, Tone> = { pending: '', downloading: 'info', ready: 'ok', failed: 'bad' };
export function containerTone(state: string, ready: boolean): Tone {
  if (state === 'running') return ready ? 'ok' : 'warn';
  if (state === 'exited') return '';
  if (state === 'restarting' || state === 'created' || state === 'paused') return 'warn';
  return 'bad';
}
export function stepTone(state: string): Tone {
  return state === 'ok' ? 'ok' : state === 'failed' ? 'bad' : state === 'running' ? 'info' : '';
}
export function routeLabel(r: string | null | undefined, via?: string | null): string {
  switch (r) {
    case 'direct':
      return 'direct';
    case 'relay':
      return via ? `relay via ${via}` : 'relay';
    case 'tunnel':
      return 'browser tunnel';
    default:
      return 'none';
  }
}
