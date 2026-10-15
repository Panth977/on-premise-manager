import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

const outDir = process.env.OPM_BUILD_DIR || 'build';

/** @type {import('@sveltejs/kit').Config} */
const config = {
  preprocess: vitePreprocess(),
  compilerOptions: { runes: true },
  kit: {
    // SPA: every route is rendered in the browser; Firebase Hosting rewrites ** → /index.html
    adapter: adapter({ pages: outDir, assets: outDir, fallback: 'index.html', precompress: false, strict: true }),
    // $opm = the shared form components of both GUIs (ui/, consumed as source — SPEC §21 #102)
    alias: { $ui: 'src/lib/ui', $cfg: 'src/lib/config-editor', $opm: '../ui/src' },
  },
};
export default config;
