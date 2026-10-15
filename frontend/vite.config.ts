import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [sveltekit()],
  server: { port: 5173, strictPort: false, fs: { allow: ['..'] } },
  preview: { port: 4173 },
  // ../ui (shared forms, §21 #102) imports @opm/shared by subpath: pre-bundle them too (no reload mid-test) and keep ONE
  // svelte runtime for files outside this package
  resolve: { dedupe: ['svelte'] },
  optimizeDeps: {
    include: ['@opm/shared', '@opm/shared/expr', '@opm/shared/validate', '@opm/shared/edge-resolve', '@opm/shared/nginx-import', '@opm/shared/devtools', '@opm/shared/icons', '@opm/shared/volume-content', '@opm/shared/branch', '@opm/shared/pending'],
  },
  build: { chunkSizeWarningLimit: 1500 },
});
