import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// The build lands in the Go embed directory so `go build` ships the UI.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../internal/transport/static/dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 2000,
  },
  server: {
    port: 5173,
    proxy: {
      '/v1': { target: process.env.LAGENT_API ?? 'http://localhost:8080', changeOrigin: false },
    },
  },
});
