import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// Прокси на Control Plane: браузер обращается к /api на том же origin,
// поэтому CORS не требуется, а токен не уходит в сторонние домены.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      "/api": {
        target: process.env.CONTROL_URL ?? "https://api.nshno.tech",
        changeOrigin: true,
        secure: true,
        rewrite: (path) => path.replace(/^\/api/, ""),
      },
    },
  },
  build: {
    outDir: "dist",
    sourcemap: false,
  },
});
