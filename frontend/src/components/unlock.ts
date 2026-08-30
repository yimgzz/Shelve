// components/unlock.ts — the master-password gate (master plan D3, §6).
// Two modes: first-run "create master password" (with confirm + a simple
// length/charset strength hint, no external libs) and "unlock". Wrong
// password shows an inline error + CSS shake. On success the vault
// emits vault:state-changed and main.ts swaps to the app shell.

import { VaultService } from "../../bindings/dummy-ssh-manager/internal/wailsvc";

export type UnlockMode = "create" | "locked";

const MIN_LENGTH = 8;

/** Weak heuristic strength estimate used for the create-mode hint only. */
function strength(text: string): { label: string; cls: string } {
    let score = 0;
    if (text.length >= MIN_LENGTH) {
        score += 1;
    }
    if (/[A-Z]/.test(text) && /[a-z]/.test(text)) {
        score += 1;
    }
    if (/\d/.test(text)) {
        score += 1;
    }
    if (/[^A-Za-z0-9]/.test(text)) {
        score += 1;
    }
    if (score < 2) {
        return { label: "Weak", cls: "weak" };
    }
    if (score < 4) {
        return { label: "Fair", cls: "fair" };
    }
    return { label: "Strong", cls: "strong" };
}

/** Render the unlock/create gate into the given root element. */
export function renderUnlockGate(root: HTMLElement, mode: UnlockMode): void {
    root.textContent = "";

    const screen = document.createElement("div");
    screen.className = "unlock-screen";

    const brand = document.createElement("div");
    brand.className = "unlock-brand";
    const title = document.createElement("h1");
    title.className = "unlock-title";
    title.textContent = "Dummy SSH Manager";
    const subtitle = document.createElement("p");
    subtitle.className = "unlock-subtitle";
    subtitle.textContent =
        mode === "create"
            ? "Create a master password to protect your sessions."
            : "Enter your master password to unlock your sessions.";
    brand.append(title, subtitle);
    screen.appendChild(brand);

    const card = document.createElement("form");
    card.className = "unlock-card";
    card.setAttribute("aria-label", mode === "create" ? "Create master password" : "Unlock");

    // Password field.
    const pwField = document.createElement("div");
    pwField.className = "field";
    const pwLabel = document.createElement("label");
    pwLabel.htmlFor = "dsm-master-pw";
    pwLabel.textContent = mode === "create" ? "Master password" : "Password";
    const pwInput = document.createElement("input");
    pwInput.id = "dsm-master-pw";
    pwInput.type = "password";
    pwInput.className = "input";
    pwInput.autocomplete = mode === "create" ? "new-password" : "current-password";
    pwField.append(pwLabel, pwInput);
    card.appendChild(pwField);

    let confirmInput: HTMLInputElement | null = null;
    let hintEl: HTMLDivElement | null = null;

    if (mode === "create") {
        const confirmField = document.createElement("div");
        confirmField.className = "field";
        const confirmLabel = document.createElement("label");
        confirmLabel.htmlFor = "dsm-master-pw-confirm";
        confirmLabel.textContent = "Confirm password";
        confirmInput = document.createElement("input");
        confirmInput.id = "dsm-master-pw-confirm";
        confirmInput.type = "password";
        confirmInput.className = "input";
        confirmInput.autocomplete = "new-password";
        confirmField.append(confirmLabel, confirmInput);
        card.appendChild(confirmField);

        hintEl = document.createElement("div");
        hintEl.className = "unlock-hint";
        hintEl.textContent = `At least ${MIN_LENGTH} characters.`;
        card.appendChild(hintEl);

        pwInput.addEventListener("input", () => {
            if (!hintEl || !confirmInput) {
                return;
            }
            const s = strength(pwInput.value);
            if (pwInput.value.length === 0) {
                hintEl.textContent = `At least ${MIN_LENGTH} characters.`;
                hintEl.style.color = "";
            } else {
                hintEl.textContent =
                    s.label === "Weak"
                        ? `Weak — use at least ${MIN_LENGTH} chars with mixed case, numbers or symbols.`
                        : s.label === "Fair"
                          ? "Fair — add symbols and mixed case for a stronger password."
                          : "Strong password.";
                hintEl.style.color = s.cls === "strong" ? "var(--ok)" : "var(--warn)";
            }
        });
    }

    const errorEl = document.createElement("div");
    errorEl.className = "unlock-error";
    card.appendChild(errorEl);

    const submit = document.createElement("button");
    submit.type = "submit";
    submit.className = "btn primary";
    submit.textContent = mode === "create" ? "Create vault" : "Unlock";
    card.appendChild(submit);

    const fail = (message: string) => {
        errorEl.textContent = message;
        card.classList.remove("shake");
        // Force reflow so the shake animation can restart.
        void card.offsetWidth;
        card.classList.add("shake");
        pwInput.select();
    };

    card.addEventListener("submit", (e) => {
        e.preventDefault();
        if (submit.disabled) {
            return;
        }
        const password = pwInput.value;
        errorEl.textContent = "";
        card.classList.remove("shake");

        if (password.length < MIN_LENGTH) {
            fail(`Master password must be at least ${MIN_LENGTH} characters.`);
            return;
        }
        if (mode === "create" && confirmInput && password !== confirmInput.value) {
            fail("Passwords do not match.");
            return;
        }

        submit.disabled = true;
        const call =
            mode === "create" ? VaultService.CreateVault(password) : VaultService.Unlock(password);
        void call
            .catch((err: unknown) => {
                submit.disabled = false;
                fail(String(err));
            })
            .finally(() => {
                // On success the vault:state-changed event drives the swap;
                // keep the button enabled until then.
                if (!submit.disabled) {
                    submit.disabled = false;
                }
            });
    });

    screen.appendChild(card);
    root.appendChild(screen);

    pwInput.focus();
}