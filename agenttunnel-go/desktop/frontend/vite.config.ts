import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The desktop UI is a plain React app. It reaches the daemon over the loopback
// WebSocket it is handed at startup, so there is no bindings codegen step.
export default defineConfig({
  server: {
    host: "127.0.0.1",
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  plugins: [react()],
});
