// Dry run of the cloud frontend flows against a deployment of the last run (e2e/run/state.json).
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import * as ui from './ui.js';
import { CFG } from './lib/env.js';
import { write } from './lib/report.js';
const st = JSON.parse(readFileSync(join(CFG.runDir, 'state.json'), 'utf8')) as { deploymentId: string };
const step = process.argv[2] ?? 'all';
try {
  if (step === 'all' || step === 'login') await ui.cloudLogin(CFG.frontendUrl, CFG.adminEmail, CFG.adminPassword);
  if (step === 'all' || step === 'tour') await ui.cloudTour(CFG.frontendUrl, st.deploymentId);
  if (step === 'all' || step === 'publish') await ui.cloudSetThemeAndPublish(CFG.frontendUrl, st.deploymentId, process.argv[3] ?? 'red', 'ui dry run');
} catch (e) {
  console.log('ERROR', e);
}
await ui.close();
console.log(write());
