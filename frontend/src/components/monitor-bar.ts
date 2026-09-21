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

import { MonitorService } from "../rpc";
import { store, activeTab, type MonitorMetrics } from "../store";
import { toast } from "./toasts";

/** Snapshots older than this render dimmed placeholders. */
const STALE_MS = 10_000;

let host: HTMLElement | null = null;
let unsub: (() => void) | null = null;
/** tabID currently being monitored by the backend (Start already called). */
let monitored: string | null = null;
/** True while the Disk tooltip is open. */
let diskTooltipOpen = false;
/** Last known pointer position (document mousemove); -1 before the first
 *  move. Geometry-based hit-test for the rebuilt Disk item: on a 2 s refresh
 *  the hovered node is replaced, so neither mouseenter (never re-fires) nor
 *  :hover (may lag the insertion) can be relied on. */
let lastPX = -1;
let lastPY = -1;
/** Last known pointer position provably inside THIS window: any mousemove in
 *  this window sets it; blur/hidden clear it (while the window is in the
 *  background the pointer lives in another window, so the recorded position
 *  is stale and must not drive a reopen). */
let pointerInWindow = false;
/** Currently rendered Disk item + tooltip (null while the bar is empty or
 *  stale), so mousemove/blur decisions need no DOM query. */
let diskElRef: HTMLElement | null = null;
let diskTipRef: HTMLElement | null = null;

/** True when the last known pointer position is inside `rect`. No padding:
 *  this is meant to mirror the browser's own hit-testing, so the JS decision
 *  cannot disagree with mouseenter/mouseleave. (A padded test resurrected a
 *  closed tooltip from a stale position a few px past the item edge.) */
function isPointerInside(rect: DOMRect | null): boolean {
    if (!rect || lastPX < 0 || lastPY < 0) {
        return false;
    }
    return lastPX >= rect.left && lastPX <= rect.right && lastPY >= rect.top && lastPY <= rect.bottom;
}

/** Pointer over the live Disk item, or over the open tooltip (it floats
 *  above the item but is its DOM child, so scrolling the df listing must
 *  keep it open). Requires proven in-window pointer — see pointerInWindow. */
function pointerOverDisk(): boolean {
    if (!pointerInWindow || !diskElRef) {
        return false;
    }
    if (isPointerInside(diskElRef.getBoundingClientRect())) {
        return true;
    }
    return !!(diskTipRef && diskTooltipOpen && isPointerInside(diskTipRef.getBoundingClientRect()));
}

function hideDiskTip(): void {
    if (diskTipRef) {
        diskTipRef.style.display = "none";
    }
    diskTooltipOpen = false;
}

const pointerMove = (e: MouseEvent): void => {
    lastPX = e.clientX;
    lastPY = e.clientY;
    pointerInWindow = true;
    // The tooltip is anchored to the item, not the pointer, so every move
    // re-evaluates membership against the item's live rect. This closes a
    // tooltip that a metric rebuild re-opened after the pointer had already
    // left the item, and re-opens one if the pointer is back over the item
    // without a fresh mouseenter (a replaced node does not re-fire
    // mouseenter until the pointer moves again).
    if (!diskElRef || !diskTipRef) {
        return;
    }
    if (pointerOverDisk()) {
        showDiskTip();
    } else if (diskTooltipOpen) {
        hideDiskTip();
    }
};

/** Window lost focus: the pointer is now in another window (or off-screen)
 *  where no mouse events will reach us, so without this the open tooltip
 *  would sit there forever. Standard tooltip behavior: dismiss on blur. Also
 *  invalidate the recorded pointer position — it is about another window now,
 *  and trusting it would let the next metric rebuild re-open the tooltip from
 *  a stale position. */
const onWindowBlur = (): void => {
    pointerInWindow = false;
    if (diskTooltipOpen) {
        hideDiskTip();
    }
};
const onVisibilityChange = (): void => {
    if (document.visibilityState === "hidden") {
        pointerInWindow = false;
        if (diskTooltipOpen) {
            hideDiskTip();
        }
    }
};

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

/** Show + position the fixed Disk tooltip above `item`. Shared by the
 *  mouseenter handler, the geometry re-evaluation and the post-rebuild
 *  reopen so all paths agree. */
function positionTooltip(item: HTMLElement, tip: HTMLElement): void {
    const r = item.getBoundingClientRect();
    tip.style.display = "block";
    const h = tip.offsetHeight; // measurable now that it is displayed
    tip.style.position = "fixed";
    tip.style.left = `${Math.max(4, Math.min(r.left, window.innerWidth - 330))}px`;
    tip.style.top = `${Math.max(4, r.top - h - 6)}px`;
}

/** Last anchor geometry the open tooltip was positioned for; lets
 *  showDiskTip skip the (layout-forcing) reposition while the item and the
 *  pointer sit still. Reset on every rebuild (the tooltip node is new). */
let tipAnchorLeft = -1;
let tipAnchorTop = -1;

/** Open (or re-open) the Disk tooltip; idempotent. */
function showDiskTip(): void {
    if (!diskElRef || !diskTipRef) {
        return;
    }
    const r = diskElRef.getBoundingClientRect();
    if (diskTooltipOpen && r.left === tipAnchorLeft && r.top === tipAnchorTop) {
        return;
    }
    positionTooltip(diskElRef, diskTipRef);
    tipAnchorLeft = r.left;
    tipAnchorTop = r.top;
    diskTooltipOpen = true;
}

function diskItem(m: MonitorMetrics | null): HTMLElement {
    const el = document.createElement("div");
    el.className = "mon-item";
    el.appendChild(labelSpan("Disk"));
    // The tooltip node is (re)created, so the position anchor resets.
    tipAnchorLeft = -1;
    tipAnchorTop = -1;
    if (!m) {
        // Stale placeholder: no tooltip, and detach the stale refs so no
        // geometry decision can linger on a removed node.
        el.appendChild(staleValue());
        diskElRef = null;
        diskTipRef = null;
        return el;
    }
    el.appendChild(gaugeFor(m.diskUsedPct));
    el.appendChild(valueSpan(`${Math.round(m.diskUsedPct)}%`));
    // Hover tooltip with the full df -h listing (plan P004 item 7). The
    // tooltip is position:fixed so it escapes the status band's
    // overflow:hidden clipping; JS positions it above the item on hover.
    // Visibility is a pure function of the pointer geometry (pointerOverDisk,
    // re-evaluated on every mousemove and on every bar rebuild), not a
    // mouseenter/leave latch: a metric rebuild replaces the hovered node —
    // mouseenter never re-fires for it, and a mouseleave fired by the removed
    // node must not be able to kill the decision either.
    el.classList.add("mon-disk");
    const tip = document.createElement("div");
    tip.className = "mon-tooltip";
    tip.textContent = m.dfText.trim() || "df unavailable";
    el.appendChild(tip);
    diskElRef = el;
    diskTipRef = tip;
    el.addEventListener("mouseenter", showDiskTip);
    return el;
}

// ------------------------------------------------------------------ render --

/** Change-detection key of the last DOM rebuild (see render). */
let lastRenderKey = "";

function render(): void {
    const el = host;
    if (!el) {
        return;
    }
    const st = store.getState();
    const tab = activeTab(st);
    const visible = st.settings.monitoringEnabled && !!tab && tab.state === "ready";
    const wantMonitored = visible && tab ? tab.id : null;

    // Backend lifecycle — only on a transition (plan P004 D2/D5). Runs
    // before the DOM guard so Start/Stop always follows the active tab.
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

    const m = tab ? st.monitor[tab.id] : undefined;
    const fresh = !!m && Date.now() - m.updatedAt < STALE_MS;

    // Change detection: rebuild the bar only when something it draws changed
    // — the visibility inputs (active tab id/state, monitoring setting) or
    // the metrics snapshot for that tab. Every metric event carries a fresh
    // updatedAt, which doubles as a monotonic change counter; the `fresh`
    // flag preserves the stale-dim flip even when the backend ticker has
    // stopped. Unrelated store churn (sftp progress, search, tree refresh,
    // tab status) must not wipe and rebuild the bar.
    const key = `${wantMonitored ?? ""}|${m?.updatedAt ?? -1}|${fresh}`;
    if (key === lastRenderKey) {
        return;
    }
    lastRenderKey = key;

    // Pointer geometry over the CURRENT disk item or its open tooltip,
    // captured BEFORE the wipe: the rebuild removes the hovered node (fires
    // its mouseleave and detaches it), so the decision must be made from
    // geometry, not node events. The tooltip floats above the item, so it is
    // checked too — the pointer may be there scrolling the df listing.
    const oldDisk = el.querySelector<HTMLElement>(".mon-disk");
    const oldTip = oldDisk?.querySelector<HTMLElement>(".mon-tooltip");
    // Gated by pointerInWindow: after a blur the recorded position belongs
    // to another window and must not drive a reopen on the next refresh.
    const pointerWasInsideOld =
        pointerInWindow &&
        (isPointerInside(oldDisk?.getBoundingClientRect() ?? null) ||
            (diskTooltipOpen && isPointerInside(oldTip?.getBoundingClientRect() ?? null)));
    el.textContent = "";

    if (!visible || !tab) {
        hideDiskTip();
        diskElRef = null;
        diskTipRef = null;
        return;
    }

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
    const diskEl = diskItem(data);
    el.appendChild(diskEl);
    // Re-evaluate the tooltip against the replacement node: a metric refresh
    // removes the hovered node mid-hover (its mouseenter can never re-fire,
    // and the mouseleave fired by the removed node must not be able to kill
    // the decision), so the answer comes from pointer geometry. The new item
    // is laid out at the same place as the old one; testing both the new and
    // the pre-wipe geometry covers the sub-pixel shift.
    const nowOver =
        (pointerInWindow && isPointerInside(diskElRef?.getBoundingClientRect() ?? null)) ||
        pointerWasInsideOld;
    if (nowOver) {
        showDiskTip();
    } else {
        hideDiskTip();
    }
}

/** Mount the monitor bar into `el` (the .status-band footer) and subscribe. */
export function renderMonitorBar(el: HTMLElement): void {
    host = el;
    document.removeEventListener("mousemove", pointerMove);
    document.addEventListener("mousemove", pointerMove);
    window.removeEventListener("blur", onWindowBlur);
    window.addEventListener("blur", onWindowBlur);
    document.removeEventListener("visibilitychange", onVisibilityChange);
    document.addEventListener("visibilitychange", onVisibilityChange);
    if (unsub) {
        unsub();
    }
    // A fresh mount must always rebuild once, even if the store contents
    // happen to equal the previous mount's last-rendered state.
    lastRenderKey = "";
    unsub = store.subscribe(render);
    render();
}