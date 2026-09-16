// components/settings-dialog.ts — the Settings modal (Phase 4d task 1;
// master plan §6 Settings). Three groups: General (theme / auto-lock /
// SFTP browser / SFTP panel position), Terminal (font family / size /
// scrollback) and Files (text editor command). Theme applies LIVE via
// theme.applyTheme and reverts on Cancel; Terminal options are applied live
// to existing xterm instances on Save (TermPool.applySettings). Save writes
// the FULL settings object (including window geometry) through AppService.

import { AppService } from "../rpc";
import { openDialog } from "../ui/dialog";
import { applyTheme, type ThemeMode } from "../ui/theme";
import { familyDefaultVariant, variantById, variantsForFamily } from "../ui/themes";
import { store, normalizeSftpPanelSide, type Settings } from "../store";
import { TermPool } from "../terminal/xterm";
import { toast } from "./toasts";

// Guard so Ctrl+, cannot stack duplicate dialogs while one is open.
let settingsOpen = false;

/** A labelled single control inside a settings group. */
function settingField(labelText: string, control: HTMLElement, opts?: { hint?: string }): HTMLElement {
    const wrap = document.createElement("label");
    wrap.className = "settings-field";
    const span = document.createElement("span");
    span.className = "settings-label";
    span.textContent = labelText;
    wrap.append(span, control);
    if (opts?.hint) {
        const hint = document.createElement("div");
        hint.className = "hint";
        hint.textContent = opts.hint;
        wrap.appendChild(hint);
    }
    return wrap;
}

/** Effective family for a mode (system resolves via OS preference). */
function effectiveFamily(mode: string): "light" | "dark" {
   if (mode === "light") return "light";
   if (mode === "dark") return "dark";
   return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

/** Open the Settings dialog. No-op if one is already open. */
export function openSettingsDialog(): void {
    if (settingsOpen) {
        return;
    }
    settingsOpen = true;

    const current = store.getState().settings;
    // Theme snapshot for the live-apply revert on Cancel.
    const openTheme = current.theme;
    // Variant snapshot; "" means family default (resolved at apply time).
    const openVariant = current.themeVariant || "";
    // Live-selected variant; defaults to the persisted value or the family
    // default for the initial mode.
    let selVariant = current.themeVariant || familyDefaultVariant(effectiveFamily(current.theme));

    const body = document.createElement("div");
    body.className = "settings-body";

    // --------------------------------------------------------- General ---
    const general = document.createElement("section");
    general.className = "settings-group";
    const generalTitle = document.createElement("h3");
    generalTitle.textContent = "General";
    general.appendChild(generalTitle);

    const themeName = `theme-${Math.random().toString(36).slice(2)}`;
    const themeRadios = document.createElement("div");
    themeRadios.className = "auth-radios";
    const themeModes: ThemeMode[] = ["system", "light", "dark"];
    const themeRadioEls: Array<{ rb: HTMLInputElement; el: HTMLLabelElement }> = [];
    for (const mode of themeModes) {
        const rb = document.createElement("input");
        rb.type = "radio";
        rb.name = themeName;
        rb.value = mode;
        rb.checked = current.theme === mode;
        const label = document.createElement("label");
        label.className = "radio";
        const span = document.createElement("span");
        span.textContent = mode.charAt(0).toUpperCase() + mode.slice(1);
        label.append(rb, span);
        rb.addEventListener("change", () => {
            if (rb.checked) {
                // Keep the selected variant if it belongs to this mode's
                // family; otherwise fall back to the family default.
                const fam = effectiveFamily(mode);
                if (!variantsForFamily(fam).some((v) => v.id === selVariant)) {
                    selVariant = familyDefaultVariant(fam);
                }
                applyTheme(mode, selVariant);
                populateVariantSelect(mode);
            }
        });
        themeRadios.appendChild(label);
        themeRadioEls.push({ rb, el: label });
    }
    general.appendChild(settingField("Theme", themeRadios));

    // Variant selector — live-applied with the mode radios (plan P001 §4.4).
    const variantSelect = document.createElement("select");
    variantSelect.className = "input";
    const populateVariantSelect = (mode: ThemeMode): void => {
        variantSelect.replaceChildren();
        if (mode === "system") {
            for (const fam of ["dark", "light"] as const) {
                const group = document.createElement("optgroup");
                group.label = fam === "dark" ? "Dark" : "Light";
                for (const v of variantsForFamily(fam)) {
                    const opt = document.createElement("option");
                    opt.value = v.id;
                    opt.textContent = v.label;
                    opt.selected = v.id === selVariant;
                    group.appendChild(opt);
                }
                variantSelect.appendChild(group);
            }
        } else {
            for (const v of variantsForFamily(effectiveFamily(mode))) {
                const opt = document.createElement("option");
                opt.value = v.id;
                opt.textContent = v.label;
                opt.selected = v.id === selVariant;
                variantSelect.appendChild(opt);
            }
        }
    };
    variantSelect.addEventListener("change", () => {
        selVariant = variantSelect.value;
        let selectedMode = (themeRadioEls.find((t) => t.rb.checked)?.rb.value ?? current.theme) as ThemeMode;
        // A variant names a concrete family. When it disagrees with the current
        // mode's effective family (e.g. picking a dark palette while mode is
        // "system" on a light OS), switch the mode radio to the variant's family
        // so the selection applies live AND persists as a legal mode+variant pair
        // (plan P001 §4.1: the variant must belong to the active family).
        const vfam = variantById(selVariant)?.family;
        if (vfam && effectiveFamily(selectedMode) !== vfam) {
            const target = themeRadioEls.find((t) => t.rb.value === vfam);
            if (target) {
                target.rb.checked = true;
                selectedMode = vfam;
                populateVariantSelect(vfam);
            }
        }
        applyTheme(selectedMode, selVariant);
    });
    populateVariantSelect(current.theme as ThemeMode);
    general.appendChild(
        settingField("Variant", variantSelect, {
            hint: "Concrete palette within the chosen theme family.",
        }),
    );

    const autoLock = document.createElement("input");
    autoLock.type = "number";
    autoLock.className = "input";
    autoLock.min = "0";
    autoLock.step = "1";
    autoLock.value = String(current.autoLockMinutes);
    general.appendChild(
        settingField("Auto-lock (minutes)", autoLock, { hint: "0 = off; locks the vault after inactivity." }),
    );

    const sftpCheck = document.createElement("input");
    sftpCheck.type = "checkbox";
    sftpCheck.className = "settings-check";
    sftpCheck.checked = current.sftpBrowserEnabled;
    const sftpWrap = settingField("SFTP browser", sftpCheck, {
        hint: "Available for the active session when a tab is ready.",
    });
    sftpWrap.classList.add("settings-check-wrap");
    general.appendChild(sftpWrap);

    // SFTP panel position (plan sftp-panel-side): the dock side of the browser.
    // Docked left it replaces the session tree in the left column (toggle at
    // the top of the column); docked right it gets its own column.
    const sftpSide = document.createElement("select");
    sftpSide.className = "input";
    for (const [value, label] of [["left", "Left"], ["right", "Right"]] as const) {
        const opt = document.createElement("option");
        opt.value = value;
        opt.textContent = label;
        opt.selected = current.sftpPanelSide === value;
        sftpSide.appendChild(opt);
    }
    general.appendChild(
        settingField("SFTP panel position", sftpSide, {
            hint: "Where the SFTP browser is docked; on the left it replaces the session tree (toggle at the top of the column).",
        }),
    );

    // Plan P004: bottom-bar system monitor (hostname/CPU/RAM/net/uptime/df).
    const monCheck = document.createElement("input");
    monCheck.type = "checkbox";
    monCheck.className = "settings-check";
    monCheck.checked = current.monitoringEnabled;
    const monWrap = settingField("System monitoring", monCheck, {
        hint: "Shows hostname, CPU, RAM, network, uptime and disk usage under the terminal.",
    });
    monWrap.classList.add("settings-check-wrap");
    general.appendChild(monWrap);

    const sftpPath = document.createElement("input");
    sftpPath.type = "text";
    sftpPath.className = "input mono";
    sftpPath.autocomplete = "off";
    sftpPath.spellcheck = false;
    sftpPath.value = current.sftpInitialPath;
    general.appendChild(
        settingField("SFTP default path", sftpPath, {
            hint: "Directory the SFTP browser opens in by default (~ = remote home).",
        }),
    );

    body.appendChild(general);

    // ------------------------------------------------------- Terminal ---
    const terminal = document.createElement("section");
    terminal.className = "settings-group";
    const terminalTitle = document.createElement("h3");
    terminalTitle.textContent = "Terminal";
    terminal.appendChild(terminalTitle);

    const fontFamily = document.createElement("input");
    fontFamily.type = "text";
    fontFamily.className = "input mono";
    fontFamily.autocomplete = "off";
    fontFamily.spellcheck = false;
    fontFamily.value = current.terminal.fontFamily;
    terminal.appendChild(settingField("Font family", fontFamily));

    const fontSize = document.createElement("input");
    fontSize.type = "number";
    fontSize.className = "input";
    fontSize.min = "8";
    fontSize.max = "24";
    fontSize.step = "1";
    fontSize.value = String(current.terminal.fontSize);
    terminal.appendChild(settingField("Font size (8–24)", fontSize));

    const scrollback = document.createElement("input");
    scrollback.type = "number";
    scrollback.className = "input";
    scrollback.min = "500";
    scrollback.max = "100000";
    scrollback.step = "100";
    scrollback.value = String(current.terminal.scrollback);
    terminal.appendChild(settingField("Scrollback (500–100000)", scrollback));

    body.appendChild(terminal);

    // ----------------------------------------------------------- Files ---
    const files = document.createElement("section");
    files.className = "settings-group";
    const filesTitle = document.createElement("h3");
    filesTitle.textContent = "Files";
    files.appendChild(filesTitle);

    const editor = document.createElement("input");
    editor.type = "text";
    editor.className = "input mono";
    editor.autocomplete = "off";
    editor.spellcheck = false;
    editor.value = current.textEditorCommand;
    files.appendChild(
        settingField("Text editor command", editor, {
            hint: "Used to edit remote text files (SFTP).",
        }),
    );

    body.appendChild(files);

    // ---------------------------------------------------------- Footer ---
    const footer = document.createElement("div");
    const cancelBtn = document.createElement("button");
    cancelBtn.type = "button";
    cancelBtn.className = "btn";
    cancelBtn.textContent = "Cancel";
    const saveBtn = document.createElement("button");
    saveBtn.type = "button";
    saveBtn.className = "btn primary";
    saveBtn.textContent = "Save";
    footer.append(cancelBtn, saveBtn);

    const dialog = openDialog<boolean>({
        title: "Settings",
        body,
        footer,
        backdropClose: true,
        width: 460,
    });

    // Revert the live-applied theme on any non-save close (Esc/backdrop/×/Cancel).
    void dialog.done.then((saved) => {
        settingsOpen = false;
        if (!saved) {
            applyTheme(openTheme as ThemeMode, openVariant);
        }
    });

    cancelBtn.addEventListener("click", () => dialog.close(false));

    saveBtn.addEventListener("click", async () => {
        saveBtn.disabled = true;
        try {
            const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));
            const full: Settings = {
                ...current,
                theme: themeRadioEls.find((t) => t.rb.checked)?.rb.value ?? current.theme,
                themeVariant: selVariant,
                autoLockMinutes: clamp(Math.floor(Number(autoLock.value) || 0), 0, 60 * 24),
                sftpBrowserEnabled: sftpCheck.checked,
                sftpPanelSide: normalizeSftpPanelSide(sftpSide.value),
                monitoringEnabled: monCheck.checked,
                terminal: {
                    fontFamily: fontFamily.value.trim() || "monospace",
                    fontSize: clamp(Math.round(Number(fontSize.value) || 13), 8, 24),
                    scrollback: clamp(Math.round(Number(scrollback.value) || 10000), 500, 100000),
                },
                textEditorCommand: editor.value.trim() || "xdg-open",
                sftpInitialPath: sftpPath.value.trim() || "~",
                window: { ...current.window },
            };
            store.set({ settings: full });
            // Live-apply terminal options to existing instances (Save path).
            TermPool.applySettings(full.terminal);
            await AppService.SaveSettings(full);
            toast("info", "Settings saved");
            dialog.close(true);
        } catch (err) {
            toast("error", String(err));
            saveBtn.disabled = false;
        }
    });

    void dialog.done;
}