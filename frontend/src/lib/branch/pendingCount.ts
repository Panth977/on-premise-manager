/** §21 #107 "N not committed" of a template / deployment in the lists = its stored local draft (IndexedDB, this account). */
import { draftState } from './drafts.svelte';
export const pendingCountOf = (branchKey: string): number => draftState.counts[branchKey] ?? 0;
