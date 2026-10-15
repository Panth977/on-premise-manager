/** htmlCode cache of not-found pages (pages are immutable, so a fetched document never changes). */
import { api } from '../api';
const cache = new Map<string, Promise<string | null>>();
export function pageHtml(id: string): Promise<string | null> {
  let p = cache.get(id);
  if (!p) {
    p = api
      .getNotFoundPage({ notFoundPageId: id })
      .then((x) => x.htmlCode)
      .catch(() => null);
    cache.set(id, p);
  }
  return p;
}
export const STARTERS: { id: string; label: string; html: string }[] = [
  {
    id: 'simple',
    label: 'Simple',
    html: '<!doctype html>\n<html>\n<head>\n  <meta charset="utf-8">\n  <title>Not found</title>\n  <style>body{font-family:system-ui;text-align:center;padding:12vh 16px;color:#18181b}</style>\n</head>\n<body>\n  <h1>404</h1>\n  <p>This page does not exist.</p>\n</body>\n</html>\n',
  },
  {
    id: 'dark',
    label: 'Dark',
    html: '<!doctype html>\n<html>\n<head>\n  <meta charset="utf-8">\n  <title>Not found</title>\n  <style>body{margin:0;height:100vh;display:grid;place-items:center;font-family:system-ui;background:#0f172a;color:#e2e8f0}h1{font-size:96px;margin:0}p{opacity:.7}</style>\n</head>\n<body>\n  <div style="text-align:center"><h1>404</h1><p>Nothing here.</p></div>\n</body>\n</html>\n',
  },
  {
    id: 'maintenance',
    label: 'Maintenance (503)',
    html: '<!doctype html>\n<html>\n<head>\n  <meta charset="utf-8">\n  <title>Starting…</title>\n  <meta http-equiv="refresh" content="15">\n  <style>body{margin:0;height:100vh;display:grid;place-items:center;font-family:system-ui;background:#fffbeb;color:#78350f}h1{margin:0 0 8px}</style>\n</head>\n<body>\n  <div style="text-align:center"><h1>Starting up</h1><p>The service is starting. This page reloads every 15 seconds.</p></div>\n</body>\n</html>\n',
  },
];
