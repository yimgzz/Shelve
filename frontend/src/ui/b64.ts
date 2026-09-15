// ui/b64.ts — base64<->bytes helpers (Phase 4c task 1).
//
// The rpc transport carries terminal I/O as base64 strings (master plan §5):
// the engine emits `terminal:data` payloads as b64 (Go side encodes
// batched output), and `TerminalService.Write` expects b64 input. These
// two single helpers keep the encoding in one place for the data pump
// and the input path. atob()/btoa() operate on binary strings; we bridge
// them to Uint8Array (used by xterm's term.write and TextEncoder).

/** Decode a base64 string into a fresh Uint8Array. */
export function b64ToBytes(b64: string): Uint8Array {
    const bin = atob(b64);
    const bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) {
        bytes[i] = bin.charCodeAt(i);
    }
    return bytes;
}

/** Encode a Uint8Array into a base64 string (std alphabet, padded). */
export function bytesToB64(u8: Uint8Array): string {
    let bin = "";
    for (let i = 0; i < u8.length; i++) {
        bin += String.fromCharCode(u8[i]);
    }
    return btoa(bin);
}