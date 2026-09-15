import { createHash } from "node:crypto";
import { defineConfig, type Plugin } from "vite";

// Content-Security-Policy (master plan §8.11; phase E3 task 7).
//
// The FOUC theme guard in index.html is an inline script, and the renderer is
// sandboxed with no Node access, so the script policy is locked to 'self'
// plus the SHA-256 of that inline guard instead of a blanket 'unsafe-inline'.
// Computing the hash here — over the FINAL HTML, after Vite has injected its
// own tags — means the hash can never drift: both `vite` (dev) and
// `vite build` get a correct policy without any hash bookkeeping, and the
// inline guard can be edited freely.
//
// connect-src must cover the token-gated loopback /rpc + /terminal sockets
// and the Vite HMR socket in dev. style-src keeps 'unsafe-inline' for Vite
// HMR and xterm's injected styles.
const CONNECT_SRC = "ws://127.0.0.1:* ws://localhost:* http://localhost:*";

function cspHashPlugin(): Plugin {
  return {
    name: "shelve:csp-inline-script-hash",
    transformIndexHtml: {
      order: "post",
      handler(html) {
        const hashes: string[] = [];
        const re = /<script([^>]*)>([\s\S]*?)<\/script>/gi;
        let match: RegExpExecArray | null;
        while ((match = re.exec(html)) !== null) {
          const attrs = match[1] ?? "";
          if (/\bsrc\s*=/i.test(attrs)) {
            continue; // external script: covered by 'self'
          }
          const code = match[2] ?? "";
          if (code.trim() === "") {
            continue;
          }
          const digest = createHash("sha256").update(code, "utf8").digest("base64");
          hashes.push(`'sha256-${digest}'`);
        }
        const policy = [
          "default-src 'self'",
          "img-src 'self' data:",
          "font-src 'self' data:",
          "style-src 'self' 'unsafe-inline'",
          `script-src ${["'self'", ...hashes].join(" ")}`,
          `connect-src ${CONNECT_SRC}`,
        ].join("; ");
        const meta = `<meta http-equiv="Content-Security-Policy" content="${policy}"/>`;
        return html.replace(/<head([^>]*)>/i, `<head$1>\n    ${meta}`);
      },
    },
  };
}

// https://vitejs.dev/config/
export default defineConfig({
  // E2-D3: relative asset URLs so `loadFile` (file://) resolves them in the
  // packaged app. The renderer is served through Electron, not a browser origin.
  base: "./",
  plugins: [cspHashPlugin()],
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
