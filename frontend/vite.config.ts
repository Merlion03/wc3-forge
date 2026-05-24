import {defineConfig} from 'vite'
import {svelte} from '@sveltejs/vite-plugin-svelte'

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [svelte()],
  resolve: {
    alias: {
      // mdx-m3-viewer's ModelViewer extends Node's EventEmitter. Without a
      // polyfill Vite leaves `require('events')` as a bare specifier and
      // the bundled code crashes with "Class extends value undefined".
      // The `events` npm package is the standard browser-side polyfill.
      events: 'events',
    },
  },
  optimizeDeps: {
    include: ['mdx-m3-viewer', 'events'],
  },
  build: {
    commonjsOptions: {
      transformMixedEsModules: true,
    },
  },
})
