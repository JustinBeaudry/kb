// Polls kb until the owner decides this request, then returns the browser to
// the app. The request ID is the only thing read from the page.
(function () {
  "use strict";
  var main = document.getElementById("kb-request");
  var status = document.getElementById("kb-status");
  if (!main || !status) return;
  var id = main.getAttribute("data-request") || "";
  function say(text) { status.textContent = text; }
  function poll() {
    fetch("/oauth/requests/" + encodeURIComponent(id), { credentials: "same-origin", cache: "no-store" })
      .then(function (r) {
        if (r.status === 200) return r.json();
        if (r.status === 404) { say("This request is no longer active. Start again from your app."); return null; }
        return { status: "retry" };
      })
      .then(function (body) {
        if (!body) return;
        if (body.status === "done" && typeof body.redirect === "string") {
          say("Returning you to the app.");
          window.location.assign(body.redirect);
          return;
        }
        setTimeout(poll, 3000);
      })
      .catch(function () { setTimeout(poll, 5000); });
  }
  setTimeout(poll, 2000);
})();
