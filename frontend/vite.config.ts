import path from "node:path";
import { defineConfig, type Plugin } from "vite";

// TRANSITIONAL (phase E2 only): the Wails-generated bindings and the
// @wailsio/runtime package were removed in E1/E2, but the renderer sources are
// only rewired to the local RPC client in E3. This plugin rewrites the two
// removed specifiers to a throwing stub so `vite build` still produces
// frontend/dist for E2; the E2 window loads electron/placeholder.html and never
// runs this bundle. E3 deletes this plugin and frontend/stubs/wails.ts.
const WAILS_STUB = path.resolve(import.meta.dirname, "stubs/wails.ts");
const WAILS_BINDINGS = /(^|\/)bindings\/shelve\/internal\/wailsvc$/;

function wailsTransitionalStub(): Plugin {
  return {
    name: "shelve:wails-transitional-stub",
    enforce: "pre",
    resolveId(source) {
      if (source === "@wailsio/runtime" || WAILS_BINDINGS.test(source)) {
        return WAILS_STUB;
      }
      return null;
    },
  };
}

// https://vitejs.dev/config/
export default defineConfig({
  // E2-D3: relative asset URLs so `loadFile` (file://) resolves them in the
  // packaged app. The renderer is served through Electron, not a browser origin.
  base: "./",
  plugins: [wailsTransitionalStub()],
  server: {
    // The Electron shell (dev) loads http://127.0.0.1:5173 via SHELVE_DEV_URL.
    host: "127.0.0.1",
    port: 5173,
    strictPort: true,
  },
  build: {
    outDir: "dist",
    // Aligned with the pinned Electron 42.10.0 Chromium (148.0.7778.280).
    target: "chrome148",
  },
});
