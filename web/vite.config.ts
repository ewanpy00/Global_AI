import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

const api = process.env.FRESHGATE_API ?? "http://localhost:8080";

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/api": { target: api, changeOrigin: true },
      "/v1": { target: api, changeOrigin: true },
    },
  },
});
