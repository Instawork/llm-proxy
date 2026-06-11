import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

const apiTarget = process.env.VITE_API_PROXY_TARGET || "http://localhost:9002";

// redirectBareAdmin sends `/admin` (no trailing slash) to `/admin/` so the
// dev server doesn't show Vite's "did you mean /admin/" base-path notice.
function redirectBareAdmin() {
  return {
    name: "redirect-bare-admin",
    configureServer(server: { middlewares: { use: (fn: (req: any, res: any, next: () => void) => void) => void } }) {
      server.middlewares.use((req, res, next) => {
        const url = (req.url ?? "").split("?")[0];
        if (url === "/admin") {
          res.statusCode = 302;
          res.setHeader("Location", "/admin/");
          res.end();
          return;
        }
        next();
      });
    },
  };
}

export default defineConfig({
  plugins: [react(), tailwindcss(), redirectBareAdmin()],
  base: "/admin/",
  build: {
    outDir: "dist",
  },
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      "/admin/api": {
        target: apiTarget,
        changeOrigin: true,
      },
      "/admin/auth": {
        target: apiTarget,
        changeOrigin: true,
      },
    },
  },
});
