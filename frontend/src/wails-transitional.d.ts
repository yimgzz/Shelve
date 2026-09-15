// Transitional type shim — PHASE E2 ONLY, deleted in E3.
//
// The Wails-generated `frontend/bindings/` tree and the `@wailsio/runtime`
// package disappear in E1/E2, but the renderer source is only rewired to the
// new RPC client in E3 (roadmap: "frontend transport swap"). Until then these
// two ambient declarations keep `make lint`'s renderer `tsc` project green
// without touching a single renderer import or runtime behavior.
//
// E3 task 8 removes this file together with the last `bindings`/`@wailsio`
// import; the declarations then become dead and are deleted.
declare module "*/bindings/shelve/internal/wailsvc";
declare module "@wailsio/runtime";
