(function () {
  "use strict";

  var body = document.body;
  var csrf = body.dataset.csrf;
  var defaultDelay = parseInt(body.dataset.delay, 10) || 0;

  var LABELS = {
    shutdown: "Shut down",
    restart: "Restart",
    sleep: "Sleep",
    hibernate: "Hibernate"
  };

  function el(id) { return document.getElementById(id); }

  var state = {
    pending: null,     // { id, action, firesAtMs }
    firedAction: null, // the action whose countdown reached zero
    failedPolls: 0
  };

  async function post(path, payload) {
    var res = await fetch(path, {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
      body: JSON.stringify(payload)
    });
    if (res.status === 401) {
      window.location.href = "/login";
      throw new Error("signed out");
    }
    var data = {};
    try { data = await res.json(); } catch (e) { /* empty body is fine */ }
    if (!res.ok) throw new Error(data.error || "Request failed (" + res.status + ")");
    return data;
  }

  function showError(message) {
    var node = el("error");
    node.textContent = message || "";
    node.classList.toggle("hidden", !message);
  }

  function setReachability(cls, text) {
    var badge = el("reachability");
    badge.className = "badge " + cls;
    badge.textContent = text;
  }

  function formatUptime(seconds) {
    var d = Math.floor(seconds / 86400);
    var h = Math.floor((seconds % 86400) / 3600);
    var m = Math.floor((seconds % 3600) / 60);
    if (d > 0) return "up " + d + "d " + h + "h";
    if (h > 0) return "up " + h + "h " + m + "m";
    return "up " + m + "m";
  }

  function formatClock(iso) {
    var t = new Date(iso);
    if (isNaN(t.getTime())) return "";
    return t.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) + " local";
  }

  // The countdown is rendered from a locally computed deadline so it ticks
  // smoothly between the 3 second polls.
  function render() {
    var box = el("pending");
    if (!state.pending) {
      box.classList.add("hidden");
      return;
    }
    box.classList.remove("hidden");
    var left = Math.max(0, Math.round((state.pending.firesAtMs - Date.now()) / 1000));
    var label = LABELS[state.pending.action] || state.pending.action;
    el("pending-text").textContent = label + " in " + left + "s";
    if (left === 0) state.firedAction = state.pending.action;
  }

  function applyStatus(data) {
    state.failedPolls = 0;
    setReachability("online", "online");

    el("hostname").textContent = data.hostname;
    el("os").textContent = data.os;
    el("uptime").textContent = formatUptime(data.uptimeSeconds);
    el("localtime").textContent = formatClock(data.localTime);

    document.querySelectorAll(".action").forEach(function (b) {
      if (b.dataset.action === "sleep") b.disabled = !data.capabilities.sleep;
      if (b.dataset.action === "hibernate") b.disabled = !data.capabilities.hibernate;
    });

    if (data.state === "pending" && data.pending) {
      // Adopting the server's pending action is what makes an action scheduled
      // or aborted on another device show up here.
      state.pending = {
        id: data.pending.id,
        action: data.pending.action,
        firesAtMs: Date.now() + data.pending.remainingSeconds * 1000
      };
    } else {
      state.pending = null;
    }

    showError(data.state === "failed" ? data.error : "");
    render();
  }

  async function poll() {
    try {
      var res = await fetch("/api/status", { headers: { Accept: "application/json" } });
      if (res.status === 401) {
        window.location.href = "/login";
        return;
      }
      if (!res.ok) throw new Error("status " + res.status);
      applyStatus(await res.json());
    } catch (e) {
      state.failedPolls += 1;
      if (state.failedPolls < 2) return;
      // Once a shutdown or restart has fired, an unreachable PC is the
      // confirmation rather than an error.
      if (state.firedAction === "shutdown" || state.firedAction === "restart") {
        setReachability("offline", "PC is offline");
        state.pending = null;
        el("pending").classList.add("hidden");
      } else {
        setReachability("offline", "Cannot reach PC");
      }
    }
  }

  var dialog = el("confirm");
  var chosenAction = null;

  document.querySelectorAll(".action").forEach(function (button) {
    button.addEventListener("click", function () {
      chosenAction = button.dataset.action;
      var label = LABELS[chosenAction] || chosenAction;
      var suffix = defaultDelay > 0 ? " this PC in " + defaultDelay + "s?" : " this PC?";
      el("confirm-title").textContent = label + suffix;
      el("graceful").checked = false;
      dialog.showModal();
    });
  });

  dialog.addEventListener("close", async function () {
    if (dialog.returnValue !== "confirm" || !chosenAction) return;
    // Unchecked "close apps gracefully" means force, which is the default.
    var force = !el("graceful").checked;
    try {
      showError("");
      var data = await post("/api/action", { action: chosenAction, force: force });
      state.firedAction = null;
      state.pending = {
        id: data.id,
        action: chosenAction,
        firesAtMs: Date.now() + data.remainingSeconds * 1000
      };
      render();
    } catch (e) {
      showError(e.message);
    }
  });

  el("abort").addEventListener("click", async function () {
    if (!state.pending) return;
    try {
      await post("/api/abort", { id: state.pending.id });
      state.pending = null;
      state.firedAction = null;
      showError("");
      render();
    } catch (e) {
      showError(e.message);
    }
  });

  setInterval(render, 250);
  setInterval(poll, 3000);
  poll();
})();
