/**
 * Structured logger (one JSON line per entry, `severity` + `message` + fields) — the format Cloud Logging parses and
 * the same lines firebase-functions' logger writes, so Cloud Functions logs are unchanged; on a VM they go to stdout /
 * stderr (journald / docker logs).
 */
type Fields = Record<string, unknown> | undefined;
function out(severity: 'DEBUG' | 'INFO' | 'WARNING' | 'ERROR', message: string, fields: Fields) {
  let line: string;
  try {
    line = JSON.stringify({ ...(fields ?? {}), severity, message });
  } catch {
    line = JSON.stringify({ severity, message, fields: String(fields) });
  }
  if (severity === 'ERROR' || severity === 'WARNING') process.stderr.write(line + '\n');
  else process.stdout.write(line + '\n');
}
export const logger = {
  debug: (m: string, f?: Record<string, unknown>) => {
    if (process.env.OPM_LOG_DEBUG === '1') out('DEBUG', m, f);
  },
  info: (m: string, f?: Record<string, unknown>) => out('INFO', m, f),
  log: (m: string, f?: Record<string, unknown>) => out('INFO', m, f),
  warn: (m: string, f?: Record<string, unknown>) => out('WARNING', m, f),
  error: (m: string, f?: Record<string, unknown>) => out('ERROR', m, f),
};
