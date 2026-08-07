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
    failedPolls: 0,
    localTime: null,
    volume: null
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

  // The status endpoint reports the PC's time as RFC3339 with the PC's offset.
  // Slicing the wall-clock part and appending "Z" gives a Date whose UTC fields
  // hold the PC's reading, which makes date arithmetic possible without the
  // browser's timezone ever being consulted.
  function pcWallDate(iso) {
    return new Date(iso.slice(0, 19) + "Z");
  }

  function pcWallInput(d) {
    return d.toISOString().slice(0, 16);
  }

  function pcOffsetMinutes(iso) {
    var m = /([+-])(\d{2}):(\d{2})$/.exec(iso);
    if (!m) return 0;
    var mins = parseInt(m[2], 10) * 60 + parseInt(m[3], 10);
    return m[1] === "-" ? -mins : mins;
  }

  function formatWait(seconds) {
    if (seconds < 60) return seconds + "s";
    var mins = Math.round(seconds / 60);
    if (mins < 60) return mins + "m";
    var h = Math.floor(mins / 60);
    var m = mins % 60;
    return m === 0 ? h + "h" : h + "h " + m + "m";
  }

  // Sliced as text, like every other reading of the PC's clock in this file.
  // Parsing through Date() and reformatting with toLocaleTimeString(), as this
  // used to, renders the instant in the BROWSER's zone while the "local" label
  // implies the PC's — correct only when the two happen to agree.
  function formatClock(iso) {
    if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}/.test(iso || "")) return "";
    return iso.slice(11, 16) + " local";
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
    var text = label + " in " + formatWait(left);
    // Past a minute the countdown alone stops being useful, so name the hour it
    // lands on. The wall-clock characters are taken from the server's string as
    // text: passing it through Date() would re-read it in the browser's
    // timezone, which is the one thing the design rules out. A schedule can
    // reach days out, so a bare time is ambiguous the same way the missed
    // banner's is — only drop the date when it is unmistakably today, compared
    // as text against the PC's own idea of "today".
    if (left >= 60 && state.pending.firesAtLocal) {
      var sameDay = state.localTime && state.localTime.slice(0, 10) === state.pending.firesAtLocal.slice(0, 10);
      var when = sameDay ? state.pending.firesAtLocal.slice(11, 16) : state.pending.firesAtLocal.slice(0, 16).replace("T", " ");
      text += " · at " + when;
    }
    el("pending-text").textContent = text;
    if (left === 0) state.firedAction = state.pending.action;
  }

  function applyStatus(data) {
    state.failedPolls = 0;
    setReachability("online", "online");

    el("hostname").textContent = data.hostname;
    el("os").textContent = data.os;
    el("uptime").textContent = formatUptime(data.uptimeSeconds);
    state.localTime = data.localTime;
    el("localtime").textContent = formatClock(data.localTime);

    document.querySelectorAll(".action").forEach(function (b) {
      if (b.dataset.action === "sleep") b.disabled = !data.capabilities.sleep;
      if (b.dataset.action === "hibernate") b.disabled = !data.capabilities.hibernate;
    });

    // The status poll only says whether anyone is signed in; the level itself
    // is never polled. Coming back from unavailable is the moment to re-read it.
    if (data.audioAvailable) {
      // Stop re-reading once the ceiling is hit. Every attempt spawns a helper
      // process on the PC and writes a log line, and a device that has failed
      // this many times running — no default endpoint, a bad HRESULT — is not
      // going to start working on the next poll three seconds from now.
      if (slider.disabled && volumeFailures < maxVolumeFailures) loadVolume();
    } else {
      // Nobody signed in is a different situation, not a repeat of the same
      // failure, so clear the count: signing back in gets a fresh set of
      // attempts and the feature recovers on its own.
      volumeFailures = 0;
      if (!slider.disabled) setVolumeEnabled(false, "Nobody is signed in at the PC");
    }

    // A successful poll proves the PC is reachable, so any locally recorded
    // "this action fired" marker is stale — the countdown may have elapsed
    // here while the action was aborted from another device, or a different
    // action may have been scheduled since. The one exception is a genuine
    // in-flight execution: a shutdown that really is going down will stop
    // answering polls shortly, and that is when "PC is offline" is correct.
    if (data.state !== "executing") {
      state.firedAction = null;
    }

    if (data.state === "pending" && data.pending) {
      // Adopting the server's pending action is what makes an action scheduled
      // or aborted on another device show up here.
      state.pending = {
        id: data.pending.id,
        action: data.pending.action,
        firesAtMs: Date.now() + data.pending.remainingSeconds * 1000,
        firesAtLocal: data.pending.firesAtLocal
      };
    } else {
      state.pending = null;
    }

    var missed = el("missed");
    if (data.missed) {
      var mLabel = LABELS[data.missed.action] || data.missed.action;
      // A miss can span days once the machine has been asleep or the service
      // down, so a bare time is ambiguous — only drop the date when it is
      // unmistakably today. The comparison is on the date characters as text,
      // the same rule as everywhere else the PC's clock is read, so the PC's
      // own idea of "today" decides rather than the browser's. Without a
      // reading of the PC's clock yet, showing the date is the safer default.
      var sameDay = state.localTime && state.localTime.slice(0, 10) === data.missed.wasDueAt.slice(0, 10);
      var when = sameDay ? data.missed.wasDueAt.slice(11, 16) : data.missed.wasDueAt.slice(0, 16).replace("T", " ");
      el("missed-text").textContent =
        mLabel + " was due at " + when + " and was skipped: the PC was off or asleep.";
      missed.classList.remove("hidden");
    } else {
      missed.classList.add("hidden");
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

  function selectedWhen() {
    var checked = document.querySelector('input[name="when"]:checked');
    return checked ? checked.value : "now";
  }

  // #when-in-value's max is only correct for whichever unit is selected: 10080
  // minutes and 168 hours are both the same 7-day server limit
  // (action.MaxHorizon), but the HTML max="10080" written into the template is
  // right for minutes only. Left unsynced, "hours" would let the browser's own
  // constraint validation wave through a value the server still refuses.
  function syncWhenInMax() {
    var value = el("when-in-value");
    var max = el("when-in-unit").value === "60" ? 10080 : 168;
    value.max = max;
    if (parseInt(value.value, 10) > max) value.value = String(max);
  }

  // Reset to Now, bound the picker to the PC's clock, and say so when the phone
  // holding the browser disagrees with the machine about what time it is.
  function resetWhen() {
    document.querySelector('input[name="when"][value="now"]').checked = true;
    el("when-in-value").value = "1";
    el("when-in-unit").value = "3600";
    syncWhenInMax();
    el("when-now-label").textContent =
      defaultDelay > 0 ? "Now (" + defaultDelay + "s countdown)" : "Now";

    var note = el("tz-note");
    var at = el("when-at");
    if (!state.localTime) {
      at.value = "";
      at.removeAttribute("min");
      at.removeAttribute("max");
      note.classList.add("hidden");
      return;
    }

    var pcNow = pcWallDate(state.localTime);
    // The picker's granularity is minutes, so its min must be rounded UP to
    // the next whole minute rather than truncated down to the current one:
    // the current minute is already partway elapsed, and by the time the
    // server parses that value back it is in the past, which it rejects.
    var pcMinMinute = new Date(Math.ceil(pcNow.getTime() / 60000) * 60000);
    at.min = pcWallInput(pcMinMinute);
    at.max = pcWallInput(new Date(pcNow.getTime() + 7 * 86400000));
    at.value = pcWallInput(new Date(pcNow.getTime() + 3600000));

    var pcOffset = pcOffsetMinutes(state.localTime);
    var browserOffset = -new Date().getTimezoneOffset();
    if (pcOffset === browserOffset) {
      note.classList.add("hidden");
      return;
    }
    var browserNow = new Date();
    note.textContent =
      "Times are the PC's clock, which reads " + at.min.slice(11) +
      ". Your device reads " +
      String(browserNow.getHours()).padStart(2, "0") + ":" +
      String(browserNow.getMinutes()).padStart(2, "0") + ".";
    note.classList.remove("hidden");
  }

  function whenPayload() {
    switch (selectedWhen()) {
      case "in":
        var n = parseInt(el("when-in-value").value, 10);
        if (!(n > 0)) throw new Error("Enter how long to wait.");
        var seconds = n * parseInt(el("when-in-unit").value, 10);
        // The max attribute tracks the unit (see syncWhenInMax), but an
        // attribute is only ever a suggestion to the browser, not a guarantee —
        // so the same 7-day cap the server enforces (action.MaxHorizon) is
        // checked again here, worded in what the operator typed rather than
        // the API's raw seconds.
        if (seconds > 604800) throw new Error("A schedule can reach at most 7 days ahead.");
        return { delaySeconds: seconds };
      case "at":
        var at = el("when-at").value;
        if (!at) throw new Error("Pick a date and time.");
        // With no step attribute a datetime-local input's own granularity is
        // already minutes; slicing to 16 characters is defensive, not a
        // workaround for anything the control actually emits.
        return { at: at.slice(0, 16) };
      default:
        return {};
    }
  }

  el("when-in-unit").addEventListener("change", syncWhenInMax);

  // A <label> wrapping the radio and the row's other controls only forwards a
  // click or keystroke to the radio when the target is the label's own text;
  // per the HTML spec a label's default activation behaviour does nothing for
  // events aimed at an interactive descendant. So typing in the number input
  // or the datetime picker, or opening the unit select, leaves "Now" checked
  // while the operator believes they picked "In" or "At" — and the action
  // fires on the short countdown instead of the deferred time they set.
  // Binding input/focus on each control and checking that row's radio closes
  // the gap the label's own behaviour leaves open.
  function checkWhenRow(value) {
    var radio = document.querySelector('input[name="when"][value="' + value + '"]');
    if (radio) radio.checked = true;
  }
  ["when-in-value", "when-in-unit"].forEach(function (id) {
    el(id).addEventListener("input", function () { checkWhenRow("in"); });
    el(id).addEventListener("focus", function () { checkWhenRow("in"); });
  });
  el("when-at").addEventListener("input", function () { checkWhenRow("at"); });
  el("when-at").addEventListener("focus", function () { checkWhenRow("at"); });

  var dialog = el("confirm");
  var chosenAction = null;

  document.querySelectorAll(".action").forEach(function (button) {
    button.addEventListener("click", function () {
      chosenAction = button.dataset.action;
      var label = LABELS[chosenAction] || chosenAction;
      el("confirm-title").textContent = label + " this PC?";
      el("graceful").checked = false;
      resetWhen();
      // Browsers only began clearing returnValue on showModal() in 2023
      // (Chrome 119, Firefox 121, Safari 17.4). On anything older it persists,
      // so a previous "confirm" would still be there after dismissing this
      // dialog with Escape — scheduling a forced shutdown nobody confirmed.
      dialog.returnValue = "";
      dialog.showModal();
    });
  });

  dialog.addEventListener("close", async function () {
    if (dialog.returnValue !== "confirm" || !chosenAction) return;
    // Unchecked "close apps gracefully" means force, which is the default.
    var force = !el("graceful").checked;
    try {
      showError("");
      var payload = whenPayload();
      payload.action = chosenAction;
      payload.force = force;
      var data = await post("/api/action", payload);
      state.firedAction = null;
      state.pending = {
        id: data.id,
        action: chosenAction,
        firesAtMs: Date.now() + data.remainingSeconds * 1000,
        firesAtLocal: data.firesAtLocal
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

  el("dismiss").addEventListener("click", async function () {
    try {
      await post("/api/dismiss", {});
      el("missed").classList.add("hidden");
    } catch (e) {
      showError(e.message);
    }
  });

  // ---- volume -------------------------------------------------------------

  var volumeRow = el("volume-row");
  var muteButton = el("volume-mute");
  var slider = el("volume-slider");
  var readout = el("volume-readout");

  function renderVolume() {
    if (!state.volume) {
      slider.value = 0;
      readout.textContent = "—";
      muteButton.textContent = "🔊";
      muteButton.setAttribute("aria-pressed", "false");
      muteButton.setAttribute("aria-label", "Mute");
      return;
    }
    slider.value = state.volume.level;
    readout.textContent = state.volume.level + "%";
    muteButton.textContent = state.volume.muted ? "🔇" : "🔊";
    muteButton.setAttribute("aria-pressed", state.volume.muted ? "true" : "false");
    muteButton.setAttribute("aria-label", state.volume.muted ? "Unmute" : "Mute");
  }

  // One load at a time, and not forever. The status poll re-triggers loadVolume
  // whenever the controls are disabled, and every call is a process spawn on
  // the PC, so an in-flight call must not be doubled and a persistent failure
  // must not become an unbounded retry loop.
  var volumeLoading = false;
  var volumeFailures = 0;
  var maxVolumeFailures = 3;

  // reason is the server's own explanation, which differs by case: nobody
  // signed in, a COM failure, an unsupported platform. Asserting one of those
  // for all of them tells the operator something that may simply be untrue.
  function setVolumeEnabled(enabled, reason) {
    slider.disabled = !enabled;
    muteButton.disabled = !enabled;
    volumeRow.classList.toggle("disabled", !enabled);
    var title = enabled ? "" : (reason || "Volume is unavailable");
    slider.title = title;
    muteButton.title = title;
    if (!enabled) {
      state.volume = null;
      renderVolume();
    }
  }

  async function loadVolume() {
    // One at a time: the status poll re-triggers this whenever the controls are
    // disabled, and each call is a process spawn on the PC.
    if (volumeLoading) return;
    volumeLoading = true;
    try {
      var res = await fetch("/api/volume", { headers: { Accept: "application/json" } });
      if (res.status === 401) {
        window.location.href = "/login";
        return;
      }
      if (!res.ok) {
        // 503 means nobody is signed in or the platform has no audio support;
        // anything else is a real failure. In every case the honest thing is to
        // stop claiming a level and to repeat the server's own reason rather
        // than inventing one.
        var reason = "";
        try {
          var body = await res.json();
          reason = body.error || "";
        } catch (e) { /* no body, or not JSON */ }
        volumeFailures += 1;
        setVolumeEnabled(false, reason);
        return;
      }
      state.volume = await res.json();
      // A load that worked clears the count, so a device that recovers gets its
      // retries back.
      volumeFailures = 0;
      setVolumeEnabled(true);
      renderVolume();
    } catch (e) {
      // The fetch itself failed, so there is no server message to quote.
      volumeFailures += 1;
      setVolumeEnabled(false, "");
    } finally {
      volumeLoading = false;
    }
  }

  async function sendVolume(payload) {
    try {
      showError("");
      state.volume = await post("/api/volume", payload);
      // A change that took is stronger evidence than a read that worked, so it
      // clears the failure count too.
      volumeFailures = 0;
      setVolumeEnabled(true);
      renderVolume();
    } catch (e) {
      showError(e.message);
      // Put the controls back where the server last said they were, rather
      // than leaving the slider showing a change that did not take.
      renderVolume();
    }
  }

  // Live feedback while dragging, but only one request when the drag ends.
  slider.addEventListener("input", function () {
    readout.textContent = slider.value + "%";
  });
  slider.addEventListener("change", function () {
    sendVolume({ level: parseInt(slider.value, 10) });
  });

  muteButton.addEventListener("click", function () {
    var muted = state.volume ? !state.volume.muted : true;
    sendVolume({ muted: muted });
  });

  loadVolume();

  setInterval(render, 250);
  setInterval(poll, 3000);
  poll();
})();
