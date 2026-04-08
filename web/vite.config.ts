import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import path from 'node:path';

// Vite config for the helmdeck management UI.
//
// Output goes to web/dist/ which is what the Go control plane
// embeds via //go:embed at compile time. The base path is "/" so
// the SPA can be mounted at the root and react-router-dom handles
// every client-side route.
//
// `proxy` lets `npm run dev` talk to a real control plane on the
// host (default :3000) without CORS gymnastics — set
// HELMDECK_API_URL when running the dev server against a non-default
// address (e.g. a remote staging instance).
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: process.env.HELMDECK_API_URL || 'http://localhost:3000',
        changeOrigin: true,
      },
      '/v1': {
        target: process.env.HELMDECK_API_URL || 'http://localhost:3000',
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    rollupOptions: {
      output: {
        manualChunks: {
          react: ['react', 'react-dom', 'react-router-dom'],
          query: ['@tanstack/react-query'],
        },
      },
    },
  },
});
