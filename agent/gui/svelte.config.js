import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

// Output: the Go agent embeds ../internal/webui/dist (go:embed). OPM_GUI_OUT overrides it (mock build for e2e tests).
const out = process.env.OPM_GUI_OUT ?? '../internal/webui/dist';

/** @type {import('@sveltejs/kit').Config} */
export default {
  preprocess: vitePreprocess(),
  compilerOptions: { runes: true },
  kit: {
    adapter: adapter({ pages: out, assets: out, fallback: 'index.html', precompress: true, strict: true }),
    // GUI lives under /_opm/; hashed files under /_opm/assets/** (SPEC §7.1)
    paths: { base: '/_opm', relative: false },
    appDir: 'assets',
    // diff.js / http.js are not in the @opm/shared exports map
    // $opm = the shared form components of both GUIs (ui/, consumed as source — SPEC §21 #102)
    alias: { $shared: '../../shared/dist', $opm: '../../ui/src' },
    output: { bundleStrategy: 'single' },
    version: { pollInterval: 0 },
  },
};
