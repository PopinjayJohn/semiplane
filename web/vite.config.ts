import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';

// Thin slice: SPA builds to web/dist, then `make embed` copies it into the Go binary.
export default defineConfig({
  plugins: [svelte(), tailwindcss()],
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
