"use strict";

const POLL_MS = 5000;
const FETCH_TIMEOUT_MS = 8000;

const instances = JSON.parse(document.getElementById("instances").textContent) || [];
const cards = new Map();
const wide = matchMedia("(min-width: 760px)"); // cards side by side, see app.css
const reducedMotion = matchMedia("(prefers-reduced-motion: reduce)");

// Numbers in English like the rest of the UI, so compact counts read 123K / 1.23M
// everywhere.
const num = new Intl.NumberFormat("en");
const compact = new Intl.NumberFormat("en", { notation: "compact", maximumSignificantDigits: 3 });

const fmtCount = (n) => (n < 10000 ? num.format(n) : compact.format(n));

function fmtPct(x) {
  if (!Number.isFinite(x)) return "–";
  const p = x * 100;
  return (p > 0 && p < 10 ? p.toFixed(1) : Math.round(p)) + "%";
}

function fmtLeft(sec) {
  sec = Math.max(0, Math.ceil(sec));
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = String(sec % 60).padStart(2, "0");
  return h ? `${h}:${String(m).padStart(2, "0")}:${s}` : `${m}:${s}`;
}

function fmtDuration(sec) {
  if (sec % 3600 === 0) return sec === 3600 ? "1 hour" : `${sec / 3600} hours`;
  return `${Math.round(sec / 60)} minutes`;
}

function h(tag, props, ...children) {
  const el = document.createElement(tag);
  Object.assign(el, props);
  el.append(...children);
  return el;
}

/* API */

class HTTPError extends Error {}

async function api(path, body) {
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), body === undefined ? FETCH_TIMEOUT_MS : 130000);
  let res;
  try {
    res = await fetch(path, {
      method: body === undefined ? "GET" : "POST",
      headers: body === undefined ? {} : { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: ctl.signal,
      cache: "no-store",
    });
    setOnline(true);
  } catch (err) {
    setOnline(false);
    throw err.name === "AbortError" ? new Error("Request timed out") : new Error("Can't reach blocky-ctrl");
  } finally {
    clearTimeout(timer);
  }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new HTTPError(data.error || `${res.status} ${res.statusText}`);
  return data;
}

function setOnline(ok) {
  document.getElementById("conn").hidden = ok;
}

/* Cards */

class Card {
  constructor(inst) {
    this.inst = inst;
    this.data = null; // last /api/instances/{id} response
    this.deadline = 0; // performance.now() at which a timed pause ends
    this.mutated = 0; // performance.now() of the last action, to drop older polls
    this.inflight = false;
    this.busy = false;

    this.el = document.getElementById("card").content.firstElementChild.cloneNode(true);
    this.el.id = "i-" + inst.id;
    const $ = (s) => this.el.querySelector(s);
    this.$ = $;
    $(".name").textContent = inst.name;
    $(".host").textContent = inst.host;
    $(".badge").hidden = !inst.readonly;
    $(".bars").append(...Array.from({ length: 24 }, () => h("span", {}, h("i"))));
    const body = $(".more-body");
    const toggle = $(".more-toggle");
    body.id = "more-" + inst.id;
    toggle.setAttribute("aria-controls", body.id);
    toggle.addEventListener("click", () => {
      // Side by side the cards share row heights, so one open Details would
      // stretch its neighbours; open and close them together there instead.
      const open = !this.detailsOpen;
      for (const c of wide.matches ? cards.values() : [this]) c.showDetails(open);
    });
    for (const btn of this.el.querySelectorAll("[data-act]")) {
      btn.addEventListener("click", () => this.maintain(btn));
    }
    this.glyph = $(".glyph");
    this.glyph.hidden = inst.readonly; // nothing to toggle
    this.glyph.addEventListener("click", () => {
      if (this.busy) return;
      if (this.kind === "on") sheet.open(this);
      else this.enable();
    });
    this.renderGlyph();
  }

  get groups() {
    return this.data?.stats?.groups.map((g) => g.name) ?? [];
  }

  // kind is one of loading, error, on, off, partial.
  get kind() {
    const d = this.data;
    if (!d) return "loading";
    if (!d.status) return "error";
    if (d.status.enabled) return "on";
    const dg = d.status.disabledGroups ?? [];
    const all = this.groups;
    return dg.length && all.some((g) => !dg.includes(g)) ? "partial" : "off";
  }

  async refresh() {
    if (this.inflight) return;
    this.inflight = true;
    const started = performance.now();
    try {
      const d = await api(`/api/instances/${encodeURIComponent(this.inst.id)}`);
      if (started < this.mutated) return;
      this.apply(d);
    } catch (err) {
      if (started < this.mutated) return;
      if (err instanceof HTTPError) this.apply({ error: err.message });
      // Network trouble with blocky-ctrl itself: keep showing the last state.
    } finally {
      this.inflight = false;
    }
  }

  apply(d) {
    this.data = d;
    // Blocky rounds the remaining time down to whole seconds; count down from the
    // upper bound so the timer never runs out before blocky re-enables.
    const sec = d.status?.enabled === false ? d.status.autoEnableInSec ?? 0 : 0;
    this.deadline = sec > 0 ? performance.now() + (sec + 1) * 1000 : 0;
    this.render();
    tick.ensure();
  }

  applyStatus(status) {
    this.mutated = performance.now();
    this.apply({ ...this.data, status, error: undefined });
  }

  secondsLeft() {
    return this.deadline ? (this.deadline - performance.now()) / 1000 : 0;
  }

  render() {
    const { el, $, data: d } = this;
    const kind = this.kind;
    el.dataset.state = kind;
    el.setAttribute("aria-busy", String(kind === "loading"));
    this.renderGlyph();

    const err = $(".error-msg");
    err.hidden = kind !== "error";
    // The host is already on the card: "dial tcp 10.0.0.2:4000: connect: connection refused".
    if (kind === "error") err.textContent = d.error.replace(/^dial tcp \S+: (connect: )?/, "");
    const ok = Boolean(d?.status);
    $(".stats").hidden = $(".more").hidden = !ok;
    if (ok) this.renderStats(d.stats);
  }

  // pauseText describes a pause: "4:32", "Paused", "ads · 4:32" or "ads paused".
  pauseText() {
    const kind = this.kind;
    if (kind !== "off" && kind !== "partial") return "";
    const left = this.secondsLeft();
    const time = left > 0 ? fmtLeft(left) : "";
    if (kind === "off") return time || "Paused";
    const groups = this.data.status.disabledGroups.join(", ");
    return time ? `${groups} · ${time}` : `${groups} paused`;
  }

  // renderGlyph updates the pause/resume button, or on read-only instances the badge.
  renderGlyph() {
    const kind = this.kind;
    const text = this.pauseText();
    if (this.inst.readonly) {
      this.$(".badge").textContent = text ? `${this.inst.label} · ${text}` : this.inst.label;
      return;
    }
    const g = this.glyph;
    const label = g.querySelector(".glyph-text");
    const reshape = !label.textContent !== !text;
    const from = reshape ? g.offsetWidth : 0;
    label.textContent = text;
    if (reshape && from && !reducedMotion.matches) {
      g.animate([{ width: `${from}px` }, { width: `${g.offsetWidth}px` }], { duration: 300, easing: "cubic-bezier(0.32, 0.72, 0, 1)" });
    }

    const actionable = ["on", "off", "partial"].includes(kind);
    g.disabled = !actionable;
    g.setAttribute("aria-label", kind === "on" ? "Blocking. Pause blocking" : `${text}. Resume blocking`);
    if (actionable) g.removeAttribute("aria-hidden");
    else g.setAttribute("aria-hidden", "true");
    if (this.busy) g.setAttribute("aria-busy", "true");
    else g.removeAttribute("aria-busy");
  }

  renderStats(s) {
    const { $ } = this;
    $(".f-queries").textContent = fmtCount(s.queries);
    $(".f-blocked").textContent = s.queries ? fmtPct(s.blocked / s.queries) : "–";
    $(".f-cached").textContent = s.queries ? fmtPct(s.cached / s.queries) : "–";
    $(".f-avg").replaceChildren(...(s.queries ? [num.format(s.avgMs), h("small", { textContent: " ms" })] : ["–"]));

    const max = Math.max(1, ...s.hours.map((x) => x.q));
    $(".bars").childNodes.forEach((bar, i) => {
      const hr = s.hours[i] ?? { q: 0, b: 0 };
      bar.classList.toggle("nodata", i < s.firstHour);
      bar.style.height = `${(hr.q / max) * 100}%`;
      bar.firstChild.style.height = hr.q ? `${(hr.b / hr.q) * 100}%` : "0";
    });

    // Blocky joins all of a client's names with commas; let them wrap there.
    const name = (s) => s.split(",").flatMap((part, i) => (i ? [",", h("wbr"), part] : [part]));
    const rows = (list, fmt) => list.length
      ? list.map((x) => h("li", {}, h("span", { className: "n" }, ...name(x.name)), h("span", { className: "c" }, fmt(x))))
      : [h("li", { className: "empty", textContent: "Nothing yet" })];
    $(".top-blocked .rank").replaceChildren(...rows(s.topBlocked, (x) => num.format(x.count)));
    $(".top-clients .rank").replaceChildren(...rows(s.topClients, (x) => num.format(x.count)));
    $(".groups-list .rank").replaceChildren(...rows(s.groups, (g) => {
      const c = h("span", {},
        h("span", { className: "allow", textContent: num.format(g.allowed) }), "/",
        h("span", { className: "deny", textContent: num.format(g.denied) }));
      c.setAttribute("aria-label", `${num.format(g.allowed)} allowed, ${num.format(g.denied)} denied`);
      return c;
    }));
  }

  get detailsOpen() {
    return this.$(".more-toggle").getAttribute("aria-expanded") === "true";
  }

  showDetails(open) {
    this.$(".more-toggle").setAttribute("aria-expanded", String(open));
    this.$(".more-body").hidden = !open;
  }

  setBusy(on) {
    this.busy = on;
    this.renderGlyph();
  }

  async enable() {
    this.setBusy(true);
    try {
      this.applyStatus(await api(`/api/instances/${encodeURIComponent(this.inst.id)}/enable`, {}));
      toast(this.label("Blocking resumed"));
    } catch (err) {
      toast(err.message, true);
    } finally {
      this.setBusy(false);
      settle();
    }
  }

  async disable(seconds, groups) {
    this.setBusy(true);
    try {
      this.applyStatus(await api(`/api/instances/${encodeURIComponent(this.inst.id)}/disable`, { seconds, groups }));
      const what = groups.length ? `${groups.join(", ")} paused` : "Blocking paused";
      toast(this.label(seconds ? `${what} for ${fmtDuration(seconds)}` : what));
    } catch (err) {
      toast(err.message, true);
    } finally {
      this.setBusy(false);
      settle();
    }
  }

  async maintain(btn) {
    const act = btn.dataset.act;
    btn.disabled = true;
    btn.setAttribute("aria-busy", "true");
    try {
      this.applyStatus(await api(`/api/instances/${encodeURIComponent(this.inst.id)}/${act}`, {}));
      toast(this.label(act === "refresh-lists" ? "Lists refreshed" : "Cache flushed"));
    } catch (err) {
      toast(err.message, true);
    } finally {
      btn.disabled = false;
      btn.removeAttribute("aria-busy");
      settle();
    }
  }

  label(msg) {
    return instances.length > 1 ? `${this.inst.name}: ${msg}` : msg;
  }
}

/* Polling */

function refreshAll() {
  for (const c of cards.values()) c.refresh();
}

let pollTimer = 0;
function schedule(ms = POLL_MS) {
  clearTimeout(pollTimer);
  if (document.visibilityState !== "visible") return;
  pollTimer = setTimeout(() => {
    refreshAll();
    schedule();
  }, ms);
}

// settle refreshes everything shortly after an action, once Redis has carried
// the new state to the read-only instances.
function settle() {
  schedule(700);
}

document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible") {
    refreshAll();
    schedule();
  } else {
    clearTimeout(pollTimer);
  }
});
window.addEventListener("online", () => schedule(0));
wide.addEventListener("change", () => {
  if (!wide.matches) return;
  const open = [...cards.values()].some((c) => c.detailsOpen);
  for (const c of cards.values()) c.showDetails(open);
});

// tick updates the pause countdowns once a second while any is running.
const tick = {
  timer: 0,
  ensure() {
    if (this.timer || ![...cards.values()].some((c) => c.deadline)) return;
    this.timer = setInterval(() => this.run(), 1000);
  },
  run() {
    let any = false;
    for (const c of cards.values()) {
      if (!c.deadline) continue;
      any = true;
      if (c.secondsLeft() <= 0) {
        c.deadline = 0;
        c.refresh();
      } else {
        c.renderGlyph();
      }
    }
    if (!any) {
      clearInterval(this.timer);
      this.timer = 0;
    }
  },
};

/* Pause sheet */

const sheet = {
  el: document.getElementById("sheet"),
  card: null,
  selected: new Set(),

  init() {
    const el = this.el;
    this.chips = el.querySelector(".chips");
    el.addEventListener("click", (e) => {
      if (e.target === el) this.close(); // backdrop
    });
    el.addEventListener("cancel", (e) => {
      e.preventDefault(); // Esc: animate out like the other ways to close
      this.close();
    });
    for (const btn of el.querySelectorAll("[data-sec]")) {
      btn.addEventListener("click", () => {
        const card = this.card;
        const groups = [...this.selected];
        this.close();
        card.disable(Number(btn.dataset.sec), groups);
      });
    }
  },

  open(card) {
    this.card = card;
    this.selected.clear();
    document.getElementById("sheet-sub").textContent = instances.length > 1 ? card.inst.name : "";
    const groups = card.groups;
    document.getElementById("sheet-groups").hidden = groups.length < 2;
    this.chips.replaceChildren(
      ...["", ...groups].map((g) => {
        const chip = h("button", { type: "button", className: "chip", textContent: g || "All groups" });
        chip.dataset.group = g;
        chip.addEventListener("click", () => this.toggle(g));
        return chip;
      }),
    );
    this.renderChips();
    clearTimeout(this.closing);
    if (!this.el.open) this.el.showModal();
    getComputedStyle(this.el).translate; // commit the hidden position so .shown transitions from it
    this.el.classList.add("shown");
  },

  close() {
    if (!this.el.classList.contains("shown")) return;
    this.el.classList.remove("shown");
    this.card = null;
    const done = () => {
      clearTimeout(this.closing);
      this.el.removeEventListener("transitionend", onEnd);
      if (!this.el.classList.contains("shown")) this.el.close();
    };
    // The sheet's movement is the longest transition: translate as a bottom
    // sheet, scale as a centered dialog.
    const onEnd = (e) => e.target === this.el && !e.pseudoElement && /^(translate|scale)$/.test(e.propertyName) && done();
    this.el.addEventListener("transitionend", onEnd);
    this.closing = setTimeout(done, 500); // no transitionend with reduced motion
  },

  toggle(g) {
    if (!g) this.selected.clear();
    else if (!this.selected.delete(g)) this.selected.add(g);
    this.renderChips();
  },

  renderChips() {
    for (const chip of this.chips.children) {
      const g = chip.dataset.group;
      chip.setAttribute("aria-pressed", String(g ? this.selected.has(g) : this.selected.size === 0));
    }
  },
};

/* Toasts */

function toast(msg, isErr = false) {
  const box = document.getElementById("toasts");
  const t = h("div", { className: isErr ? "toast err" : "toast", textContent: msg });
  t.setAttribute("role", isErr ? "alert" : "status");
  box.append(t);
  while (box.children.length > 3) box.firstChild.remove();
  setTimeout(() => {
    t.classList.add("gone");
    setTimeout(() => t.remove(), 400);
  }, isErr ? 5000 : 2600);
}

/* Boot */

// Lets :active styles show on iOS.
document.addEventListener("touchstart", () => {}, { passive: true });

sheet.init();
const list = document.getElementById("instances-list");
for (const inst of instances) {
  const c = new Card(inst);
  cards.set(inst.id, c);
  list.append(c.el);
}
refreshAll();
schedule();
