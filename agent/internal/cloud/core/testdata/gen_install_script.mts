/** Regenerates install_script.tmpl from routes/install-script.ts. Run with tsx from backend/, output on stdout. */
import { renderInstallScript } from '../../../../../backend/src/routes/install-script.ts';
process.stdout.write(renderInstallScript(new Proxy({}, { get: (_t, k) => `@@OPM:${String(k)}@@` }) as never));
