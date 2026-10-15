import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

// VITE_AGENT_URL=http://127.0.0.1:7080 npm run dev → proxies /_opm/api to a real agent.
const agent = process.env.VITE_AGENT_URL;

export default defineConfig({
  plugins: [sveltekit()],
  // literal true/false so the production bundle drops the mock agent completely (dead-code elimination)
  define: { __OPM_MOCK__: JSON.stringify(process.env.VITE_MOCK === '1') },
  // ../../ui = shared form components (§21 #102), one svelte runtime for files outside this package
  resolve: { dedupe: ['svelte'] },
  server: { fs: { allow: ['.', '../../ui', '../../shared'] }, ...(agent ? { proxy: { '/_opm/api': { target: agent, changeOrigin: false } } } : {}) },
  build: { target: 'es2022', cssMinify: true, reportCompressedSize: true, chunkSizeWarningLimit: 600 },
});
