import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

// svelte-check (and editor tooling) prefers this over parsing vite.config.ts.
export default {
  preprocess: vitePreprocess()
};
