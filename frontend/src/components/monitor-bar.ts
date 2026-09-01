// components/monitor-bar.ts — the bottom system-monitor bar (plan P004).
// Replaces the Phase 4c status bar: renders hostname, CPU, RAM, network
// upload/download, uptime and disk (df) for the ACTIVE ready tab, left to
// right, in MobaXterm style. The user@host text and the port-forward
// summary are gone (owner decision).
//
// Backend lifecycle (plan P004 D2): only the active tab is monitored. This
// component calls MonitorService.Start when the active tab turns ready and
// MonitorService.Stop on switch/close/disable. Metrics arrive via the
// monitor:metrics event → store.monitor; the 2 s metric updates must never
// trigger Start/Stop churn, so transitions happen ONLY when the monitored
// tabID or the visibility flag changes.

import { MonitorService } from "../../bindings/shelve/internal/wailsvc";
import { store, type MonitorMetrics } from "../store";
import { toast } from "./toasts";

/** Snapshots older than this render dimmed placeholders. */
const STALE_MS = 10_000;

let host: HTMLElement | null = null;
let unsub: (() => void) | null = null;
/** tabID currently being monitored by the backend (Start already called). */
let monitored: string | null = null;

// ------------------------------------------------------------- formatters --

/** Binary-unit byte formatter: MB/GB/TB chosen dynamically (plan P004). */
function humanizeBytes(bytes: number): string {
    // NOTE: never use `1 << n` here — JS shifts truncate the count to 5 bits
    // (1 << 40 === 256). Exponentiation yields exact IEEE doubles at these
    // magnitudes.
    const MB = 2 ** 20;
    const GB = 2 ** 30;
    const TB = 2 ** 40;
    if (bytes >= TB) return `${(bytes / TB).toFixed(1)} TB`;
    if (bytes >= GB) return `${(bytes / GB).toFixed(1)} GB`;
    return `${(bytes / MB).toFixed(1)} MB`;
}

/** 3d 4h 12m / 2h 15m / 34m / 45s. */
function formatUptime(seconds: number): string {
    const d = Math.floor(seconds / 86400);
    const h = Math.floor((seconds % 86400) / 3600);
    const m = Math.floor((seconds % 3600) / 60);
    const s = Math.floor(seconds % 60);
    if (d > 0) return `${d}d ${h}h ${m}m`;
    if (h > 0) return `${h}h ${m}m`;
    if (m > 0) return `${m}m`;
    return `${s}s`;
}

/** Network speed with a binary unit: B/s, KB/s, MB/s, GB/s. */
function formatSpeed(bps: number): string {
    // See humanizeBytes: `**` instead of `<<` (JS shift-count truncation).
    const KB = 2 ** 10;
    const MB = 2 ** 20;
    const GB = 2 ** 30;
    if (bps >= GB) return `${(bps / GB).toFixed(1)} GB/s`;
    if (bps >= MB) return `${(bps / MB).toFixed(1)} MB/s`;
    if (bps >= KB) return `${(bps / KB).toFixed(1)} KB/s`;
    return `${Math.round(bps)} B/s`;
}

// -------------------------------------------------------------- builders --

function valueSpan(text: string, extraCls?: string): HTMLElement {
    const s = document.createElement("span");
    s.className = "mon-value" + (extraCls ? ` ${extraCls}` : "");
    s.textContent = text;
    return s;
}

function labelSpan(text: string): HTMLElement {
    const s = document.createElement("span");
    s.className = "mon-label";
    s.textContent = text;
    return s;
}

function monItem(...children: HTMLElement[]): HTMLElement {
    const el = document.createElement("div");
    el.className = "mon-item";
    el.append(...children);
    return el;
}

/** Thin 2 px gauge; warn ≥80 %, crit ≥95 %. */
function gaugeFor(pct: number): HTMLElement {
    const track = document.createElement("span");
    track.className = "mon-gauge";
    const fill = document.createElement("span");
    fill.className = "mon-gauge-fill";
    const clamped = Math.max(0, Math.min(100, pct));
    fill.style.width = `${clamped}%`;
    if (pct >= 95) fill.classList.add("crit");
    else if (pct >= 80) fill.classList.add("warn");
    track.appendChild(fill);
    return track;
}

function staleValue(): HTMLElement {
    return valueSpan("—", "stale");
}

function cpuItem(m: MonitorMetrics | null): HTMLElement {
    if (!m) {
        return monItem(labelSpan("CPU"), staleValue());
    }
    return monItem(labelSpan("CPU"), gaugeFor(m.cpuPercent), valueSpan(`${Math.round(m.cpuPercent)}%`));
}

function memItem(m: MonitorMetrics | null): HTMLElement {
    if (!m) {
        return monItem(labelSpan("RAM"), staleValue());
    }
    const usedPct = m.memTotalBytes > 0 ? (100 * m.memUsedBytes) / m.memTotalBytes : 0;
    return monItem(
        labelSpan("RAM"),
        gaugeFor(usedPct),
        valueSpan(`${humanizeBytes(m.memUsedBytes)} / ${humanizeBytes(m.memTotalBytes)}`),
    );
}

function netItem(kind: "Up" | "Down", bps: number | null): HTMLElement {
    const el = monItem(labelSpan(kind), bps == null ? staleValue() : valueSpan(`${kind === "Up" ? "↑" : "↓"} ${formatSpeed(bps)}`));
    el.title = `${kind === "Up" ? "Upload" : "Download"} speed (all non-loopback interfaces)`;
    return el;
}

function uptimeItem(m: MonitorMetrics | null): HTMLElement {
    return monItem(labelSpan("Uptime"), m ? valueSpan(formatUptime(m.uptimeSeconds)) : staleValue());
}

function diskItem(m: MonitorMetrics | null): HTMLElement {
    const el = document.createElement("div");
    el.className = "mon-item";
    el.appendChild(labelSpan("Disk"));
    if (!m) {
        el.appendChild(staleValue());
        return el;
    }
    el.appendChild(gaugeFor(m.diskUsedPct));
    el.appendChild(valueSpan(`${Math.round(m.diskUsedPct)}%`));
    // Hover tooltip with the full df -h listing (plan P004 item 7). The
    // tooltip is position:fixed so it escapes the status band's
    // overflow:hidden clipping; JS positions it above the item on hover.
    el.classList.add("mon-disk");
    const tip = document.createElement("div");
    tip.className = "mon-tooltip";
    tip.textContent = m.dfText.trim() || "df unavailable";
    el.appendChild(tip);
    const showTip = () => {
        const r = el.getBoundingClientRect();
        tip.style.display = "block";
        const h = tip.offsetHeight; // measurable now that it is displayed
        tip.style.position = "fixed";
        tip.style.left = `${Math.max(4, Math.min(r.left, window.innerWidth - 330))}px`;
        tip.style.top = `${Math.max(4, r.top - h - 6)}px`;
    };
    const hideTip = () => {
        tip.style.display = "none";
    };
    el.addEventListener("mouseenter", showTip);
    el.addEventListener("mouseleave", hideTip);
    return el;
}

// ------------------------------------------------------------------ render --

function render(): void {
    const el = host;
    if (!el) {
        return;
    }
    el.textContent = "";
    const st = store.getState();
    const tab = st.tabs.find((t) => t.id === st.activeTabID);
    const visible = st.settings.monitoringEnabled && !!tab && tab.state === "ready";
    const wantMonitored = visible && tab ? tab.id : null;

    // Backend lifecycle — only on a transition (plan P004 D2/D5).
    if (wantMonitored !== monitored) {
        if (monitored) {
            // Stop is cleanup-only; failures are irrelevant (tab likely
            // already gone).
            void MonitorService.Stop(monitored).catch(() => undefined);
        }
        monitored = wantMonitored;
        if (monitored) {
            void MonitorService.Start(monitored).catch((err) => toast("error", String(err)));
        }
    }
    if (!visible || !tab) {
        return;
    }

    const m = st.monitor[tab.id];
    const fresh = !!m && Date.now() - m.updatedAt < STALE_MS;
    const data = fresh ? m : null;

    // 1. Hostname.
    const hostEl = document.createElement("span");
    hostEl.className = "mon-hostname";
    hostEl.textContent = data?.hostname || "—";
    if (!data) {
        hostEl.classList.add("stale");
    }
    el.appendChild(hostEl);

    // 2–7. CPU, RAM, Up, Down, Uptime, Disk (left → right).
    el.appendChild(cpuItem(data));
    el.appendChild(memItem(data));
    el.appendChild(netItem("Up", data ? data.netUpBps : null));
    el.appendChild(netItem("Down", data ? data.netDownBps : null));
    el.appendChild(uptimeItem(data));
    el.appendChild(diskItem(data));
}

/** Mount the monitor bar into `el` (the .status-band footer) and subscribe. */
export function renderMonitorBar(el: HTMLElement): void {
    host = el;
    if (unsub) {
        unsub();
    }
    unsub = store.subscribe(render);
    render();
}