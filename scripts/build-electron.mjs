// Bundles the Electron main + preload entry points with esbuild (E2-D2).
//
// One small dependency, no bundler framework: main/preload are TypeScript
// bundled to CommonJS so the sandboxed preload stays a single self-contained
// file, and `electron` stays external (provided by the runtime).
//
// Node target: Electron 42.10.0 bundles Node.js 24.18.1
// (https://raw.githubusercontent.com/electron/electron/v42.10.0/DEPS).
import { build } from "esbuild";
import { rm } from "node:fs/promises";

const OUTDIR = "dist-electron";
const NODE_TARGET = "node24";

await rm(OUTDIR, { recursive: true, force: true });

await build({
    entryPoints: ["electron/main.ts", "electron/preload.ts"],
    outdir: OUTDIR,
    outExtension: { ".js": ".cjs" },
    bundle: true,
    platform: "node",
    format: "cjs",
    target: NODE_TARGET,
    // The Electron runtime provides `electron`; everything else is bundled.
    external: ["electron"],
    sourcemap: false,
    logLevel: "info",
});
