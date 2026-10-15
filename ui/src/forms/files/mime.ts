/** Mime suggestions of the file definition form (both GUIs, §21 #102). */
export const MIME_OPTIONS: { value: string; sub: string }[] = [
  { value: 'text/plain', sub: 'plain text, .txt / .env / .conf' },
  { value: 'application/json', sub: '.json' },
  { value: 'text/yaml', sub: '.yaml / .yml' },
  { value: 'application/toml', sub: '.toml' },
  { value: 'text/html', sub: '.html' },
  { value: 'text/css', sub: '.css' },
  { value: 'text/csv', sub: '.csv' },
  { value: 'text/markdown', sub: '.md' },
  { value: 'application/javascript', sub: '.js / .mjs' },
  { value: 'application/typescript', sub: '.ts' },
  { value: 'application/xml', sub: '.xml' },
  { value: 'application/x-pem-file', sub: 'certificates / keys (.pem, .crt)' },
  { value: 'application/x-sh', sub: 'shell script' },
  { value: 'application/sql', sub: '.sql' },
  { value: 'application/octet-stream', sub: 'any binary' },
  { value: 'application/zip', sub: '.zip' },
  { value: 'application/gzip', sub: '.gz / .tgz' },
  { value: 'image/png', sub: '.png' },
  { value: 'image/jpeg', sub: '.jpg' },
  { value: 'image/svg+xml', sub: '.svg' },
  { value: 'image/x-icon', sub: '.ico' },
  { value: 'font/woff2', sub: '.woff2' },
];
/** a text editor makes sense for these */
export function isTextMime(m: string): boolean {
  return m.startsWith('text/') || ['application/json', 'application/toml', 'application/xml', 'application/x-pem-file', 'application/javascript', 'application/typescript', 'application/x-sh', 'application/sql', 'image/svg+xml'].includes(m);
}
