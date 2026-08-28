import { AppService } from "../bindings/dummy-ssh-manager/internal/wailsvc";

document.addEventListener("DOMContentLoaded", async () => {
    const el = document.getElementById("version");
    if (!el) {
        return;
    }
    try {
        el.textContent = await AppService.GetVersion();
    } catch (err) {
        el.textContent = String(err);
    }
});
