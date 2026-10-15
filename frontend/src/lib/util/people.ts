import { catalog } from '../stores/catalog.svelte';
import { session } from '../session.svelte';
/** uid → account name (cloud actors); emails and unknown values are returned as they are. */
export function personName(uidOrEmail: string | null | undefined): string {
  if (!uidOrEmail) return 'system';
  if (uidOrEmail === 'bootstrap') return 'bootstrap';
  const a = catalog.accounts.find((x) => x.uid === uidOrEmail);
  if (a) return a.name;
  // without accounts.read (or before the catalog loaded) at least the signed-in person resolves
  if (session.account?.uid === uidOrEmail) return session.account.name;
  return uidOrEmail;
}
export const siteName = (s: string) => (s === '_cloud' ? 'cloud' : s);
