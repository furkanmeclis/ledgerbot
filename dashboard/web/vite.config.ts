import path from 'node:path';
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

export default defineConfig({
  root: path.resolve(__dirname),
  plugins: [react(), tailwindcss()],
  resolve: { alias: { '@': path.resolve(__dirname, 'src') } },
  build: { outDir: path.resolve(__dirname, '..', 'dist'), emptyOutDir: true },
  server: {
    port: 5173,
    proxy: { '/api': 'http://localhost:3000', '/socket.io': { target: 'http://localhost:3000', ws: true } },
  },
});
