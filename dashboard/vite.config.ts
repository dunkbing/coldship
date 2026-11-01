import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The production build is embedded into the Go binary (internal/web/dist),
// which serves it on the same origin as the API.
export default defineConfig({
  plugins: [react()],
  build: { outDir: '../internal/web/dist', emptyOutDir: true },
  server: {
    proxy: { '/v1': process.env.HANGAR_URL ?? 'http://127.0.0.1:7070' },
  },
})
