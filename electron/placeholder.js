// E2 scaffold only (deleted in E3): show the resolved bridge address.
//
// Loaded as an external script (not inline) so the page CSP can be
// `script-src 'self'`; text is written with textContent, never innerHTML.
(function () {
    "use strict";

    var status = document.getElementById("status");
    var addr = document.getElementById("addr");

    // The window can exist before the Go backend finishes its handshake, so
    // keep polling with a small backoff until bridgeEndpoint() resolves.
    function attempt(delay) {
        window.shelve.bridgeEndpoint().then(function (endpoint) {
            document.title = "Shelve — bridge ready";
            status.firstChild.textContent = "bridge ready at ";
            addr.textContent = endpoint.addr;
        }).catch(function () {
            setTimeout(function () { attempt(Math.min(delay * 2, 1000)); }, delay);
        });
    }

    attempt(50);
})();
