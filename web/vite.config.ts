import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    target: 'es2022',
  },
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      '/api':      { target: 'http://localhost:8080', changeOrigin: false },
      '/ws':       { target: 'ws://localhost:8080',   ws: true, changeOrigin: false },
      '/sse':      { target: 'http://localhost:8080', changeOrigin: false },
      '/internal': { target: 'http://localhost:8080', changeOrigin: false },
    },
  },
})
