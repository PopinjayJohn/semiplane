import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';
import path from 'node:path';

// Thin slice: SPA builds to web/dist, then `make embed` copies it into the Go binary.
export default defineConfig({
  plugins: [svelte(), tailwindcss()],
  resolve: {
    alias: [
      // Component tests resolve bare `svelte` through Node's ESM loader, and
      // the exports map's default condition is the SSR bundle — whose mount()
      // throws lifecycle_function_unavailable by design. Pin the bare
      // specifier to the client entry (subpaths like `svelte/internal/*`
      // keep resolving through the exports map — exact-match regex).
      {
        find: /^svelte$/,
        replacement: path.resolve(__dirname, 'node_modules/svelte/src/index-client.js'),
      },
    ],
  },
  test: {
    environment: 'jsdom', // TipTap Editor (ProseMirror) needs a DOM for round-trip tests
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true
  },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://localhost:8080',
      '/healthz': 'http://localhost:8080'
    }
  }
});
