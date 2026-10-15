import type { HandleClientError } from '@sveltejs/kit';

/**
 * Unexpected client errors: log them (with the route) and show a readable message on the error page.
 * A chunk that cannot be loaded (a new deploy replaced the files) → reload once to get the new index.html.
 */
export const handleError: HandleClientError = ({ error, event, status, message }) => {
  const msg = error instanceof Error ? error.message : String(error);
  if (/dynamically imported module|Importing a module script failed|error loading dynamically/i.test(msg)) {
    try {
      if (!sessionStorage.getItem('opm.reloaded')) {
        sessionStorage.setItem('opm.reloaded', '1');
        location.reload();
      }
    } catch {
      /* ignore */
    }
  }
  console.error('[opm] unexpected error', status, event.url.pathname, error);
  return { message: error instanceof Error ? error.message : message };
};
