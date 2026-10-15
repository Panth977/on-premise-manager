// VM e2e only (OPM_TEST_HOOKS=1): merge the internal test-hook fields of githubMirror/config (JSON in env OPM_HOOKS)
// through the LOCAL ADAPTER of the bundled server — run INSIDE the opm-cloud container (`node -` on stdin), so the
// write takes the adapter's normal path (index columns, outbox, WAL + busy_timeout; safe next to the running server).
const { installPorts } = require('/opt/opm-cloud/dist/opm-cloud.cjs');
(async () => {
  const hooks = JSON.parse(process.env.OPM_HOOKS || '{}');
  const ports = await installPorts({ adapter: 'local', dataDir: process.env.OPM_DATA_DIR || '/data', publicUrl: process.env.OPM_PUBLIC_URL || 'http://localhost:8790' });
  await ports.store.collection('githubMirror').doc('config').set(hooks, { merge: true });
  const v = (await ports.store.collection('githubMirror').doc('config').get()).data() || {};
  console.log('hooks set', JSON.stringify({ _apiBase: v._apiBase, _scope: v._scope, _stubExporter: v._stubExporter }));
  await ports.close();
})().catch((e) => {
  console.error(e && e.stack ? e.stack : String(e));
  process.exit(1);
});
