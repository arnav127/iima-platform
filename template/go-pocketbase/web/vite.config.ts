import { defineConfig } from 'vite';
import preact from '@preact/preset-vite';
import { fileURLToPath } from 'node:url';

// The build lands in backend/pb_public, which the backend serves.
// VITE_BASE: the app's path on the platform, e.g. /myapp/ (from app.toml).
const base = (process.env.VITE_BASE || '/').replace(/\/?$/, '/');

export default defineConfig({
  base,
  root: fileURLToPath(new URL('.', import.meta.url)),
  plugins: [preact()],
  build: {
    outDir: fileURLToPath(new URL('../backend/pb_public', import.meta.url)),
    emptyOutDir: true,
    target: 'es2020',
  },
  server: {
    port: 5173,
    // Dev: the backend runs at the root (go run . serve), so strip the base before proxying /api.
    proxy: { [`${base}api`]: { target: 'http://127.0.0.1:8090', rewrite: (p) => p.slice(base.length - 1) } },
  },
});
