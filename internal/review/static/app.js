"use strict";

// photodupe UI: one page, five steps (choose, scan, results [+review], save,
// done), routed by location.hash so the browser back button works.

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

async function api(path, opts = {}) {
  if (opts.json !== undefined) {
    opts = { ...opts, method: opts.method || "POST", body: JSON.stringify(opts.json) };
  }
  const res = await fetch(path, opts);
  if (!res.ok) throw new Error(((await res.text()) || res.statusText).trim());
  const ct = res.headers.get("content-type") || "";
  return ct.includes("application/json") ? res.json() : res.text();
}

// ---------- formatting ----------
const esc = (s) =>
  String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const fmtInt = (n) => Number(n || 0).toLocaleString();
function fmtBytes(n) {
  n = Number(n || 0);
  if (n >= 1e9) return (n / 1e9).toFixed(1) + " GB";
  if (n >= 1e6) return (n / 1e6).toFixed(1) + " MB";
  if (n >= 1e3) return Math.round(n / 1e3) + " KB";
  return n + " B";
}
function fmtDate(s) {
  const d = new Date(String(s).replace(" ", "T"));
  if (isNaN(d)) return s || "";
  return d.toLocaleString(undefined, { day: "numeric", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit" });
}
function fmtDuration(sec) {
  sec = Math.max(1, Math.round(sec));
  if (sec < 60) return `About ${sec} sec left`;
  return `About ${Math.round(sec / 60)} min left`;
}
const plural = (n, one, many) => `${fmtInt(n)} ${n === 1 ? one : many || one + "s"}`;

const ICON = {
  check: '<svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><polyline points="20 6 9 17 4 12"/></svg>',
  alert: '<svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>',
  clock: '<svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/></svg>',
  chevron: '<svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><path d="m9 18 6-6-6-6"/></svg>',
  folder: '<svg class="icon" viewBox="0 0 24 24" aria-hidden="true"><path d="M4 20h16a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.93a2 2 0 0 1-1.66-.9l-.82-1.2A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13c0 1.1.9 2 2 2Z"/></svg>',
};

const LEVEL_LABEL = { strict: "Strict", standard: "Standard", loose: "Loose" };
const LEVEL_HELP = {
  strict: "Only near-identical copies: re-saves, resizes and compressed versions.",
  standard: "Also catches light edits and crops. Recommended for most libraries.",
  loose: "Also groups heavier edits and filters. Check the results before saving.",
  custom: "A custom threshold is set in data/config.json.",
};
const ORDER_LABEL = { resolution: "Highest resolution", filesize: "Largest file", earliest: "Earliest date" };
const PICK_REASON = { resolution: "highest resolution", filesize: "largest file", earliest: "earliest date" };

let toastTimer = null;
function toast(msg) {
  const t = $("#toast");
  t.textContent = msg;
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (t.hidden = true), 4000);
}

// ---------- state ----------
const state = {
  status: null,
  summary: null,
  clusters: null, // cached /api/clusters, enriched
  strictness: null,
  filter: "all",
  pos: 0, // index into the filtered review list
  undo: [],
  exportReport: null,
};

async function loadSummary() {
  state.summary = await api("/api/summary");
  return state.summary;
}
async function loadClusters() {
  const list = await api("/api/clusters");
  state.clusters = list.map(enrich);
  return state.clusters;
}
async function loadStrictness() {
  state.strictness = await api("/api/strictness");
  return state.strictness;
}
function invalidateResults() {
  state.summary = null;
  state.clusters = null;
  state.strictness = null;
}

// ---------- routing ----------
const VIEWS = ["choose", "scan", "results", "review", "save", "done"];
const STEP_OF = { choose: 0, scan: 1, results: 2, review: 2, save: 3, done: 3 };
let currentView = null;

function go(view) {
  if (location.hash === "#" + view) render(view);
  else location.hash = view;
}
window.addEventListener("hashchange", () => render(location.hash.slice(1)));

async function render(view) {
  if (!VIEWS.includes(view)) view = "choose";
  try {
    state.status = await api("/api/status");
  } catch (e) {
    toast("Can't reach photodupe. Is it still running?");
    return;
  }
  if (state.status.running && view !== "scan") return go("scan");
  if (["results", "review", "save"].includes(view)) {
    const sum = state.summary || (await loadSummary());
    if (!sum.has_results) return go("choose");
  }

  if (currentView === "scan" && view !== "scan") stopScanPoll();
  currentView = view;
  VIEWS.forEach((v) => ($("#view-" + v).hidden = v !== view));
  renderSteps(view);
  window.scrollTo(0, 0);

  const fn = { choose: showChoose, scan: showScan, results: showResults, review: showReview, save: showSave, done: showDone }[view];
  try {
    await fn();
  } catch (e) {
    toast(e.message);
  }
}

function renderSteps(view) {
  const cur = STEP_OF[view];
  const has = state.summary?.has_results;
  const running = state.status?.running;
  $$(".step").forEach((btn, i) => {
    btn.classList.toggle("current", i === cur);
    btn.classList.toggle("done", i < cur);
    if (i === cur) btn.setAttribute("aria-current", "step");
    else btn.removeAttribute("aria-current");
    const target = btn.dataset.step;
    let enabled = i !== cur;
    if (running) enabled = target === "scan" && i !== cur;
    else if (target === "scan") enabled = false;
    else if ((target === "results" || target === "save") && !has) enabled = false;
    btn.disabled = !enabled;
  });
}
$$(".step").forEach((btn) => btn.addEventListener("click", () => go(btn.dataset.step)));
document.addEventListener("click", (e) => {
  const t = e.target.closest("[data-go]");
  if (t) go(t.dataset.go);
});

// ---------- recent folders (per browser, best effort) ----------
function getRecent() {
  try {
    return JSON.parse(localStorage.getItem("photodupe.recent") || "[]");
  } catch {
    return [];
  }
}
function addRecent(path) {
  try {
    const list = [path, ...getRecent().filter((p) => p !== path)].slice(0, 5);
    localStorage.setItem("photodupe.recent", JSON.stringify(list));
  } catch {
    /* storage unavailable: skip */
  }
}

// ---------- 1. choose ----------
let inFolder = null; // last /api/folder result for the input field
let preLevel = "standard";
let inCheckTimer = null;
let inCheckSeq = 0;

async function showChoose() {
  const input = $("#in-path");
  const [cfg, strict, sum] = await Promise.all([api("/api/config"), loadStrictness(), state.summary || loadSummary()]);
  // Coming back here must never feel like starting over: offer the way back.
  $("#have-results").hidden = !sum.has_results;
  if (sum.has_results) {
    const bits = [sum.input_dir, plural(sum.groups, "group")];
    if (sum.decided) bits.push(`${plural(sum.decided, "choice")} saved`);
    $("#have-text").textContent = bits.join(" · ");
  }
  // Always start from the folder the saved results belong to, so a path typed
  // earlier and abandoned never lingers as a one-click "replace".
  if (cfg.input_dir || !input.value) input.value = cfg.input_dir || "";
  preLevel = strict.current === "custom" ? "standard" : strict.current;
  renderPreLevel(strict.levels);

  const recent = getRecent();
  $("#recent-wrap").hidden = recent.length === 0;
  $("#recent").innerHTML = recent.map((p) => `<button type="button" class="chip" title="${esc(p)}">${esc(p)}</button>`).join("");
  $$("#recent .chip").forEach((chip, i) =>
    chip.addEventListener("click", () => {
      input.value = recent[i];
      checkInput();
    })
  );
  checkInput();
}

function renderPreLevel(levels) {
  const seg = $("#pre-level");
  seg.innerHTML = levels
    .map((l) => `<button type="button" data-level="${l.name}" aria-pressed="${l.name === preLevel}">${LEVEL_LABEL[l.name] || l.name}</button>`)
    .join("");
  $$("button", seg).forEach((b) =>
    b.addEventListener("click", () => {
      preLevel = b.dataset.level;
      $$("button", seg).forEach((x) => x.setAttribute("aria-pressed", String(x === b)));
    })
  );
}

$("#in-path").addEventListener("input", () => {
  clearTimeout(inCheckTimer);
  inCheckTimer = setTimeout(checkInput, 350);
});
$("#in-path").addEventListener("keydown", (e) => {
  if (e.key === "Enter" && !$("#btn-find").disabled) $("#btn-find").click();
});

async function checkInput() {
  const path = $("#in-path").value.trim();
  const info = $("#in-info");
  const btn = $("#btn-find");
  $("#find-error").hidden = true;
  inFolder = null;
  btn.disabled = true;
  if (!path) {
    info.hidden = true;
    renderFindMode();
    return;
  }
  const seq = ++inCheckSeq;
  info.hidden = false;
  info.className = "info-strip";
  info.textContent = "Counting photos…";
  let f;
  try {
    f = await api("/api/folder?path=" + encodeURIComponent(path));
  } catch (e) {
    if (seq !== inCheckSeq) return;
    info.className = "info-strip warn";
    info.textContent = e.message;
    return;
  }
  if (seq !== inCheckSeq) return; // a newer check is in flight
  if (!f.exists || !f.is_dir) {
    info.className = "info-strip warn";
    info.textContent = f.exists ? "That's a file, not a folder." : "That folder doesn't exist.";
    return;
  }
  if (f.photos === 0) {
    info.className = "info-strip warn";
    info.textContent = "No photos found in this folder or its subfolders.";
    return;
  }
  inFolder = f;
  btn.disabled = false;
  renderFindMode();
  const parts = [
    `<strong>${ICON.check}${plural(f.photos, "photo")}${f.truncated ? "+" : ""}</strong>`,
    `<span>${fmtBytes(f.bytes)}</span>`,
  ];
  if (f.subfolders) parts.push(`<span>${plural(f.subfolders, "subfolder")}</span>`);
  if (f.heic) parts.push(`<span>${plural(f.heic, "iPhone (HEIC) photo")} included</span>`);
  info.innerHTML = parts.join("");
}

// findMode: "new" (no results yet), "rescan" (same folder as the saved
// results: decisions are kept), or "replace" (a different folder).
function findMode() {
  const sum = state.summary;
  if (!sum?.has_results || !inFolder) return "new";
  return inFolder.path === sum.input_dir ? "rescan" : "replace";
}

function renderFindMode() {
  const mode = findMode();
  const sum = state.summary;
  const note = $("#find-note");
  $("#btn-find-label").textContent = mode === "rescan" ? "Scan again" : "Find duplicates";
  note.hidden = mode === "new";
  note.className = mode === "replace" ? "small warn-text" : "muted small";
  if (mode === "rescan") note.textContent = "Only new or changed photos are read again. Your choices are kept.";
  if (mode === "replace") note.textContent = `This replaces your results for ${sum.input_dir}${sum.decided ? `, including ${plural(sum.decided, "saved choice")}` : ""}.`;
}

function confirmDialog({ title, text, yes }) {
  return new Promise((resolve) => {
    $("#confirm-title").textContent = title;
    $("#confirm-text").textContent = text;
    $("#confirm-yes").textContent = yes;
    const done = (v) => {
      hideModal("#confirm");
      $("#confirm-yes").onclick = $("#confirm-no").onclick = $("#confirm").onclick = null;
      confirmCancel = null;
      resolve(v);
    };
    $("#confirm-yes").onclick = () => done(true);
    $("#confirm-no").onclick = () => done(false);
    $("#confirm").onclick = (e) => e.target.id === "confirm" && done(false);
    confirmCancel = () => done(false);
    showModal("#confirm");
    $("#confirm-no").focus(); // the safe choice is the default
  });
}
let confirmCancel = null;

$("#btn-find").addEventListener("click", async () => {
  if (!inFolder) return;
  if (findMode() === "replace") {
    const sum = state.summary;
    const ok = await confirmDialog({
      title: "Start over with a different folder?",
      text: `Your results for ${sum.input_dir}${sum.decided ? ` and ${plural(sum.decided, "saved choice")}` : ""} will be replaced by a new scan of ${inFolder.path}.`,
      yes: "Replace my results",
    });
    if (!ok) return;
  }
  const btn = $("#btn-find");
  btn.disabled = true;
  try {
    const level = state.strictness?.levels.find((l) => l.name === preLevel);
    const cfg = { input_dir: inFolder.path };
    if (level) cfg.phash_threshold = level.threshold;
    await api("/api/config", { json: cfg });
    await api("/api/run", { method: "POST" });
    addRecent(inFolder.path);
    invalidateResults();
    go("scan");
  } catch (e) {
    $("#find-error").textContent = e.message;
    $("#find-error").hidden = false;
    btn.disabled = false;
  }
});

// ---------- 2. scan ----------
const PHASES = [
  { key: "scan", label: "Reading photos" },
  { key: "hash", label: "Comparing photos" },
  { key: "cluster", label: "Grouping duplicates" },
];
const PHASE_INDEX = { scan: 0, hash: 1, embed: 1, cluster: 2 };
let scanTimer = null;
let rate = { phase: null, t0: 0, d0: 0 };
let scanCounts = {};

function stopScanPoll() {
  clearInterval(scanTimer);
  scanTimer = null;
}

async function showScan() {
  const cfg = await api("/api/config");
  $("#scan-path").textContent = cfg.input_dir;
  $("#scan-error").hidden = true;
  $("#btn-see-results").hidden = true;
  scanCounts = {};
  stopScanPoll();
  scanTimer = setInterval(pollScan, 700);
  pollScan();
}

async function pollScan() {
  let s;
  try {
    s = await api("/api/status");
  } catch {
    return;
  }
  state.status = s;
  const active = PHASE_INDEX[s.phase] ?? -1;
  if (s.total > 0 && active >= 0) scanCounts[active] = { done: s.done, total: s.total };

  let eta = "";
  if (s.running && s.total > 0 && s.done > 0) {
    const now = performance.now();
    if (rate.phase !== s.phase) rate = { phase: s.phase, t0: now, d0: s.done };
    const dd = s.done - rate.d0;
    const dt = (now - rate.t0) / 1000;
    if (dd > 0 && dt > 1.5) eta = fmtDuration(((s.total - s.done) * dt) / dd);
  }

  const finished = !s.running;
  const ok = finished && !s.error;
  $("#scan-title").textContent = !finished ? "Looking for duplicates…" : ok ? "Scan complete" : "The scan stopped";
  $("#scan-eta").innerHTML = eta ? ICON.clock + esc(eta) : "";
  $("#scan-log").textContent = (s.log || []).join("\n");

  $("#phases").innerHTML = PHASES.map((p, i) => {
    const done = ok || (s.running && i < active) || (finished && !ok && i < active);
    const isActive = s.running && i === active;
    const c = scanCounts[i];
    let detail = "";
    if (isActive && c) detail = `<span class="mono">${fmtInt(c.done)} / ${fmtInt(c.total)}</span>`;
    else if (done && i === 0 && scanCounts[0]) detail = plural(scanCounts[0].total, "photo");
    else if (done && i === 2 && state.summary) detail = plural(state.summary.groups, "group");
    else if (!done && !isActive) detail = "Waiting";
    const pct = isActive && c && c.total ? Math.round((100 * c.done) / c.total) : 0;
    return `<li class="phase ${done ? "done" : isActive ? "active" : ""}">
      <div class="phase-head">
        <span class="phase-dot">${done ? ICON.check : i + 1}</span>
        <span class="phase-label">${p.label}</span>
        <span class="phase-detail">${detail}</span>
      </div>
      ${isActive ? `<div class="bar" role="progressbar" aria-label="${p.label}" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${pct}"><div class="bar-fill" style="width:${pct}%"></div></div>` : ""}
    </li>`;
  }).join("");

  if (finished) {
    stopScanPoll();
    renderSteps("scan");
    if (s.error) {
      $("#scan-error").innerHTML = `<strong>Something went wrong.</strong><span>${esc(s.error)}</span><button type="button" class="btn" data-go="choose">Back to folder choice</button>`;
      $("#scan-error").hidden = false;
    } else if (s.phase) {
      invalidateResults();
      await loadSummary();
      renderSteps("scan");
      if (!state.summary.has_results) {
        $("#scan-title").textContent = "No photos could be read";
        $("#scan-error").innerHTML = `<span>photodupe couldn't read any photos in this folder. Check "Show details" for the reason, then try another folder.</span><button type="button" class="btn" data-go="choose">Back to folder choice</button>`;
        $("#scan-error").hidden = false;
        return;
      }
      pollScanDetailOnly();
      $("#btn-see-results").hidden = false;
      $("#btn-see-results").focus();
    } else {
      go(state.summary?.has_results ? "results" : "choose"); // nothing ran
    }
  }
}
function pollScanDetailOnly() {
  const last = $$("#phases .phase-detail")[2];
  if (last && state.summary) last.textContent = plural(state.summary.groups, "group");
}
$("#btn-see-results").addEventListener("click", () => go("results"));

// ---------- cluster helpers ----------
function enrich(c) {
  const m = c.members;
  const px = (x) => x.width * x.height;
  const maxPx = Math.max(...m.map(px));
  const maxSize = Math.max(...m.map((x) => x.file_size));
  const minDate = m.map((x) => x.capture_time).sort()[0];
  m.forEach((x) => {
    x.bestRes = px(x) === maxPx;
    x.bestSize = x.file_size === maxSize;
    x.bestDate = x.capture_time === minDate;
  });

  const sameDims = m.every((x) => x.width === m[0].width && x.height === m[0].height);
  const sameSize = sameDims && m.every((x) => x.file_size === m[0].file_size);
  let match, why;
  if (sameSize) {
    match = "Identical copies";
    why = `The same file appears ${m.length} times.`;
  } else if (sameDims) {
    match = "Same photo";
    why = `${m.length} copies at the same resolution, saved differently.`;
  } else {
    match = "Resized copies";
    const sizes = new Set(m.map((x) => `${x.width}x${x.height}`)).size;
    why = `The same photo saved at ${sizes} different sizes.`;
  }

  // Close call: the top two by resolution tie, and their files are within 10%.
  const byPx = [...m].sort((a, b) => px(b) - px(a) || b.file_size - a.file_size);
  let look = "";
  if (!sameSize && byPx.length > 1 && px(byPx[0]) === px(byPx[1]) && byPx[1].file_size >= 0.9 * byPx[0].file_size) {
    look = "Close call: the copies are nearly the same quality";
  }
  return { ...c, match, why, look };
}

// pickReason names the criterion that decided our pick, walking the keep
// order the way the server's heuristic does.
function pickReason(c, order) {
  const sug = c.members.find((x) => x.id === c.suggested_id);
  if (!sug) return "";
  const val = {
    resolution: (x) => x.width * x.height,
    filesize: (x) => x.file_size,
    earliest: (x) => -new Date(x.capture_time.replace(" ", "T")).getTime(),
  };
  let pool = c.members;
  for (const k of order || []) {
    if (!val[k]) continue;
    const best = Math.max(...pool.map(val[k]));
    pool = pool.filter((x) => val[k](x) === best);
    if (pool.length === 1) return pool[0] === sug ? PICK_REASON[k] : "";
  }
  return "first by file path";
}

const isChanged = (c) => !c.decision.skipped && c.decision.keeper_id !== "0" && c.decision.keeper_id !== c.suggested_id;
const isKeptAll = (c) => c.decision.skipped;
const isApproved = (c) => !!c.decision.approved && !c.decision.skipped;
const keeperOf = (c) => (c.decision.skipped ? null : c.decision.keeper_id !== "0" && c.members.some((x) => x.id === c.decision.keeper_id) ? c.decision.keeper_id : c.suggested_id);
const thumbURL = (id) => `/api/image?id=${encodeURIComponent(id)}&thumb=1`;
const fullURL = (id) => `/api/image?id=${encodeURIComponent(id)}`;

// ---------- 3. results ----------
let strictBusy = false;

async function showResults() {
  const [sum, clusters, strict] = await Promise.all([
    state.summary || loadSummary(),
    state.clusters || loadClusters(),
    state.strictness || loadStrictness(),
  ]);
  $("#res-path").textContent = `${sum.input_dir} · ${plural(sum.photos, "photo")}`;
  const none = sum.extra === 0;
  $("#res-title").textContent = none ? "No duplicates found" : `Found ${plural(sum.extra, "extra copy", "extra copies")}`;
  $("#res-lede").innerHTML = none
    ? "Every photo in this folder looks unique at this strictness."
    : `They sit in ${plural(sum.groups, "group")} of matching photos. Leaving them out makes your clean copy <strong>${fmtBytes(sum.extra_bytes)} smaller</strong>.`;

  $("#res-tiles").innerHTML = [
    tile("Photos scanned", fmtInt(sum.photos)),
    tile("Duplicate groups", fmtInt(sum.groups)),
    tile("Photos you'll keep", fmtInt(sum.keep)),
    tile("Space saved", fmtBytes(sum.extra_bytes), "", "accent"),
  ].join("");

  $("#res-next").hidden = none;
  $("#res-none").hidden = !none;
  $("#res-order").innerHTML = (sum.keep_order || [])
    .map((k, i) => `<span>${i + 1} · ${ORDER_LABEL[k] || k}</span>`)
    .join(ICON.chevron);

  const lookN = clusters.filter((c) => c.look).length;
  const reviewedTxt = sum.reviewed ? ` · ${fmtInt(sum.reviewed)} reviewed` : "";
  $("#res-look").innerHTML = lookN
    ? `${ICON.alert}Optional · ${plural(lookN, "group is", "groups are")} worth a look${reviewedTxt}`
    : `Optional${reviewedTxt}`;
  $("#res-look .icon")?.style.setProperty("stroke", "var(--warn-border)");

  const biggest = [...clusters].sort((a, b) => b.members.length - a.members.length).slice(0, 8);
  $("#res-biggest-wrap").hidden = biggest.length === 0;
  $("#btn-see-all").textContent = `See all ${fmtInt(clusters.length)}`;
  $("#res-biggest").innerHTML = biggest
    .map((c) => {
      const k = c.members.find((x) => x.id === (keeperOf(c) || c.suggested_id)) || c.members[0];
      return `<button type="button" class="thumb-card" data-cluster="${esc(c.id)}">
        <span class="ph"><img loading="lazy" src="${thumbURL(k.id)}" alt=""><span class="badge br">${c.members.length} copies</span></span>
        <span class="name" title="${esc(k.filename)}">${esc(k.filename)}</span>
      </button>`;
    })
    .join("");
  $$("#res-biggest .thumb-card").forEach((b) => b.addEventListener("click", () => openReviewAt(b.dataset.cluster)));

  renderStrictness(strict, sum);
}

function tile(label, value, sub = "", cls = "") {
  return `<div class="tile ${cls}"><div class="tile-label">${label}</div><div class="tile-value">${value}</div>${sub ? `<div class="tile-sub">${sub}</div>` : ""}</div>`;
}

function renderStrictness(strict, sum) {
  const seg = $("#strict-seg");
  seg.innerHTML = strict.levels
    .map((l) => `<button type="button" data-level="${l.name}" aria-pressed="${l.name === strict.current}" ${strictBusy ? "disabled" : ""}>${LEVEL_LABEL[l.name] || l.name}</button>`)
    .join("");
  $$("button", seg).forEach((b) => b.addEventListener("click", () => setLevel(b.dataset.level)));
  $("#strict-help").textContent = LEVEL_HELP[strict.current] || "";
  $("#strict-counts").innerHTML = strict.levels
    .map((l) => `<li class="${l.name === strict.current ? "on" : ""}"><span>${LEVEL_LABEL[l.name]}</span><span>${l.needs_rescan ? "needs re-scan" : plural(l.groups, "group")}</span></li>`)
    .join("");
  $("#strict-rescan").hidden = true;
  $("#strict-order").textContent = (sum.keep_order || []).map((k) => ORDER_LABEL[k] || k).join(", then ") + ".";
}

async function setLevel(name) {
  const strict = state.strictness;
  if (!strict || strictBusy || name === strict.current) return;
  const level = strict.levels.find((l) => l.name === name);
  if (level.needs_rescan) {
    $$("#strict-seg button").forEach((b) => b.setAttribute("aria-pressed", String(b.dataset.level === name)));
    $("#strict-help").textContent = LEVEL_HELP[name];
    $("#strict-rescan").hidden = false;
    $("#btn-rescan").onclick = () => applyLevel(name);
    return;
  }
  applyLevel(name);
}

async function applyLevel(name) {
  strictBusy = true;
  $$("#strict-seg button").forEach((b) => (b.disabled = true));
  try {
    const r = await api("/api/strictness", { json: { name } });
    invalidateResults();
    if (r.rescan) return go("scan");
    await showResults();
    toast(`Switched to ${LEVEL_LABEL[name]}.`);
  } catch (e) {
    toast(e.message);
  } finally {
    strictBusy = false;
    $$("#strict-seg button").forEach((b) => (b.disabled = false));
  }
}

$("#btn-to-save").addEventListener("click", () => go("save"));
$("#btn-to-review").addEventListener("click", () => {
  const anyTodo = state.clusters?.some((c) => !c.decision.resolved);
  state.filter = anyTodo ? "todo" : "all";
  state.pos = 0;
  go("review");
});
$("#btn-see-all").addEventListener("click", () => {
  state.filter = "all";
  state.pos = 0;
  go("review");
});
function openReviewAt(clusterId) {
  state.filter = "all";
  state.pos = Math.max(0, (state.clusters || []).findIndex((c) => c.id === clusterId));
  go("review");
}

// ---------- 3b. review ----------
const FILTERS = [
  { key: "todo", label: "To review", test: (c) => !c.decision.resolved },
  { key: "look", label: "Worth a look", test: (c) => !!c.look },
  { key: "approved", label: "Approved", test: isApproved },
  { key: "declined", label: "Declined", test: isKeptAll },
  { key: "all", label: "All", test: () => true },
];
const filtered = () => (state.clusters || []).filter(FILTERS.find((f) => f.key === state.filter).test);
const currentCluster = () => filtered()[state.pos];

async function showReview() {
  if (!state.clusters) await loadClusters();
  if (!state.summary) await loadSummary();
  renderReview();
}

function renderReview() {
  const list = filtered();
  if (state.pos >= list.length) state.pos = Math.max(0, list.length - 1);
  const all = state.clusters;
  const reviewed = all.filter((c) => c.decision.resolved).length;

  $("#rv-tabs").innerHTML = FILTERS.map((f) => {
    const n = all.filter(f.test).length;
    return `<button type="button" role="tab" data-filter="${f.key}" aria-selected="${f.key === state.filter}">${f.label} · ${fmtInt(n)}</button>`;
  }).join("");
  $$("#rv-tabs button").forEach((b) =>
    b.addEventListener("click", () => {
      state.filter = b.dataset.filter;
      state.pos = 0;
      renderReview();
    })
  );

  $("#rv-reviewed").textContent = `${fmtInt(reviewed)} of ${fmtInt(all.length)} reviewed`;
  const approvedN = all.filter(isApproved).length;
  $("#clean-count").textContent = fmtInt(approvedN);
  $("#btn-clean").disabled = approvedN === 0;
  const pct = all.length ? (100 * reviewed) / all.length : 0;
  $("#rv-progress .bar-fill").style.width = pct + "%";
  $("#rv-progress").setAttribute("aria-valuenow", String(Math.round(pct)));

  const c = list[state.pos];
  $("#rv-main").hidden = !c;
  $("#rv-empty").hidden = !!c;
  $("#rv-title").textContent = c ? `Group ${fmtInt(state.pos + 1)} of ${fmtInt(list.length)}` : "Review groups";
  renderQueue(list);
  if (!c) return;

  const verdict = isApproved(c)
    ? `<span class="pill ok">${ICON.check}Approved</span>`
    : isKeptAll(c)
      ? '<span class="pill no">Declined, keeping all</span>'
      : "";
  $("#rv-why").innerHTML =
    verdict +
    `<span class="pill info">${esc(c.match)}</span><span>${esc(c.why)}</span>` +
    (c.look ? `<span class="pill warn">${ICON.alert}${esc(c.look)}</span>` : "");

  const keep = keeperOf(c);
  const reason = pickReason(c, state.summary?.keep_order);
  $("#rv-members").innerHTML = c.members
    .map((m, i) => {
      const isKeep = c.decision.skipped || m.id === keep;
      const cls = c.decision.skipped ? "member" : isKeep ? "member keep" : "member drop";
      const badge = isKeep ? `<span class="badge tl keep">${ICON.check}Keeping</span>` : `<span class="badge tl">Left out</span>`;
      const best = (b) => (b && c.members.length > 1 ? ICON.check.replace('aria-hidden="true"', 'role="img" aria-label="best"') : "");
      return `<button type="button" class="${cls}" data-id="${esc(m.id)}" aria-pressed="${isKeep}" aria-label="Keep ${esc(m.filename)}">
        <span class="ph contain"><img loading="lazy" src="${thumbURL(m.id)}" alt="">${badge}${i < 9 ? `<kbd>${i + 1}</kbd>` : ""}</span>
        <span class="name" title="${esc(m.filename)}">${esc(m.filename)}</span>
        <span class="folder" title="${esc(m.path)}">${esc(m.folder || "Top folder")}</span>
        <span class="facts">
          <span class="k">Size</span><span class="v">${m.width}×${m.height}${best(m.bestRes)}</span>
          <span class="k">File</span><span class="v">${fmtBytes(m.file_size)}${best(m.bestSize)}</span>
          <span class="k">Taken</span><span class="v">${esc(fmtDate(m.capture_time))}${best(m.bestDate)}</span>
        </span>
        ${m.id === c.suggested_id && !c.decision.skipped ? `<span class="pick">Our pick: ${esc(reason)}</span>` : ""}
      </button>`;
    })
    .join("");
  $$("#rv-members .member").forEach((b) => b.addEventListener("click", () => pickKeeper(b.dataset.id)));

  $("#btn-approve").setAttribute("aria-pressed", String(isApproved(c)));
  $("#btn-decline").setAttribute("aria-pressed", String(isKeptAll(c)));
  $("#btn-prev").disabled = state.pos === 0;
  $("#btn-next").disabled = state.pos >= list.length - 1;
}

function renderQueue(list) {
  const next = list.slice(state.pos + 1, state.pos + 7);
  $("#rv-queue").innerHTML = next.length
    ? next
        .map((c, i) => {
          const k = keeperOf(c) || c.suggested_id;
          const chip = isApproved(c)
            ? '<span class="qchip ok">Approved</span>'
            : isKeptAll(c)
            ? '<span class="qchip">Declined</span>'
            : isChanged(c)
              ? '<span class="qchip ok">Your pick</span>'
              : c.look
                ? '<span class="qchip warn">Worth a look</span>'
                : '<span class="qchip">Our pick</span>';
          const name = c.members.find((x) => x.id === k)?.filename || c.members[0].filename;
          return `<li><button type="button" data-pos="${state.pos + 1 + i}">
            <span class="qthumb"><img loading="lazy" src="${thumbURL(k)}" alt=""></span>
            <span class="qtext"><span class="qname">${esc(name)}</span><span class="qsub">${c.members.length} copies</span></span>
            ${chip}
          </button></li>`;
        })
        .join("")
    : '<li class="muted">That\'s the last group.</li>';
  $$("#rv-queue button").forEach((b) =>
    b.addEventListener("click", () => {
      state.pos = Number(b.dataset.pos);
      renderReview();
    })
  );
}

async function saveDecision(c, dec, { record = true } = {}) {
  const prev = { ...c.decision };
  c.decision = { ...c.decision, ...dec };
  state.summary = null; // counts changed; refetch when needed
  try {
    await api(`/api/clusters/${encodeURIComponent(c.id)}/decision`, { json: c.decision });
    if (record) state.undo.push({ id: c.id, prev });
  } catch (e) {
    c.decision = prev;
    toast("Couldn't save: " + e.message);
  }
}

async function pickKeeper(id) {
  const c = currentCluster();
  if (!c) return;
  // keeper_id "0" means "use our pick", so a later change in keep order still
  // applies. Picking a keeper un-declines the group but doesn't approve it.
  const approved = isApproved(c);
  await saveDecision(c, { keeper_id: id === c.suggested_id ? "0" : id, skipped: false, approved, resolved: approved });
  renderReview();
}

async function approveGroup() {
  const c = currentCluster();
  if (!c) return;
  if (!isApproved(c)) await saveDecision(c, { skipped: false, approved: true, resolved: true });
  advanceFrom(c);
}

async function declineGroup() {
  const c = currentCluster();
  if (!c) return;
  if (!isKeptAll(c)) await saveDecision(c, { keeper_id: "0", skipped: true, approved: false, resolved: true });
  advanceFrom(c);
}

// advanceFrom moves past c after a verdict. If c just left the current filter
// (say, "To review"), the same position already shows the next group.
function advanceFrom(c) {
  const list = filtered();
  if (!list.includes(c)) {
    if (state.pos >= list.length && list.length) toast("That was the last group in this tab.");
  } else if (state.pos < list.length - 1) {
    state.pos++;
  } else {
    toast("That was the last group in this tab.");
  }
  renderReview();
}

function nextGroup() {
  if (state.pos < filtered().length - 1) state.pos++;
  renderReview();
}

function prevGroup() {
  if (state.pos > 0) state.pos--;
  renderReview();
}

async function undo() {
  const last = state.undo.pop();
  if (!last) return toast("Nothing to undo.");
  const c = state.clusters.find((x) => x.id === last.id);
  if (!c) return;
  await saveDecision(c, last.prev, { record: false });
  state.filter = "all";
  state.pos = state.clusters.indexOf(c);
  renderReview();
  toast("Undone.");
}

$("#btn-approve").addEventListener("click", approveGroup);
$("#btn-decline").addEventListener("click", declineGroup);
$("#btn-next").addEventListener("click", nextGroup);
$("#btn-prev").addEventListener("click", prevGroup);
$("#btn-compare").addEventListener("click", openCompare);
$("#btn-accept-rest").addEventListener("click", () => go("save"));

// ---------- clean up approved groups ----------
async function openClean() {
  if (!state.summary) await loadSummary(); // for the removed folder path
  const groups = (state.clusters || []).filter(isApproved);
  if (!groups.length) return;
  let n = 0;
  let bytes = 0;
  for (const c of groups) {
    const keep = keeperOf(c);
    for (const m of c.members) {
      if (m.id !== keep) {
        n++;
        bytes += m.file_size;
      }
    }
  }
  $("#clean-text").innerHTML = `Move <strong>${plural(n, "left-out copy", "left-out copies")}</strong> (${fmtBytes(bytes)}) from ${plural(groups.length, "approved group")} out of your photo folder and into:`;
  $("#clean-dest").textContent = state.summary?.removed_dir || "";
  $("#clean-failed").hidden = true;
  $("#clean-go").hidden = false;
  $("#clean-go").disabled = false;
  $("#clean-go").textContent = "Move them";
  $("#clean-cancel").textContent = "Cancel";
  showModal("#clean");
}

async function runClean() {
  const btn = $("#clean-go");
  btn.disabled = true;
  btn.textContent = "Moving…";
  let r;
  try {
    r = await api("/api/clean", { method: "POST" });
  } catch (e) {
    btn.disabled = false;
    btn.textContent = "Move them";
    return toast("Clean-up failed: " + e.message);
  }
  invalidateResults();
  state.undo = []; // undo can't bring moved files back
  await Promise.all([loadClusters(), loadSummary()]);
  renderReview();
  const msg = `Moved ${plural(r.moved, "copy", "copies")} (${fmtBytes(r.bytes)}) from ${plural(r.groups, "group")}.`;
  if (r.failed?.length) {
    $("#clean-text").textContent = `${msg} ${plural(r.failed.length, "file was", "files were")} left in place:`;
    $("#clean-failed").innerHTML = r.failed.map((f) => `<li>${esc(f)}</li>`).join("");
    $("#clean-failed").hidden = false;
    btn.hidden = true;
    $("#clean-cancel").textContent = "Close";
  } else {
    hideModal("#clean");
    toast(`${msg} They're in ${r.dest}.`);
  }
}

$("#btn-clean").addEventListener("click", openClean);
$("#clean-go").addEventListener("click", runClean);
$("#clean-cancel").addEventListener("click", () => hideModal("#clean"));
$("#clean-close").addEventListener("click", () => hideModal("#clean"));
$("#clean").addEventListener("click", (e) => {
  if (e.target.id === "clean") hideModal("#clean");
});

// ---------- compare ----------
function openCompare() {
  const c = currentCluster();
  if (!c) return;
  const keep = keeperOf(c);
  $("#compare-grid").innerHTML = c.members
    .map(
      (m, i) => `<div class="compare-cell ${m.id === keep ? "keep" : ""}">
        <span class="ph"><img src="${fullURL(m.id)}" alt="${esc(m.filename)}"></span>
        <span class="mono small">${esc(m.filename)}</span>
        <span class="muted small">${m.width}×${m.height} · ${fmtBytes(m.file_size)} · ${esc(fmtDate(m.capture_time))}</span>
        <button type="button" class="btn ${m.id === keep ? "btn-primary" : ""}" data-id="${esc(m.id)}">${m.id === keep ? "Keeping this one" : "Keep this one"} <kbd>${i + 1}</kbd></button>
      </div>`
    )
    .join("");
  $$("#compare-grid button").forEach((b) =>
    b.addEventListener("click", async () => {
      await pickKeeper(b.dataset.id);
      openCompare();
    })
  );
  showModal("#compare");
}

let lastFocus = null;
function showModal(sel) {
  lastFocus = document.activeElement;
  $(sel).hidden = false;
  $(sel + " .btn-icon")?.focus();
}
function hideModal(sel) {
  $(sel).hidden = true;
  lastFocus?.focus?.();
}
$("#compare-close").addEventListener("click", () => hideModal("#compare"));
$("#compare").addEventListener("click", (e) => {
  if (e.target.id === "compare") hideModal("#compare");
});

// ---------- keyboard ----------
document.addEventListener("keydown", (e) => {
  if (e.ctrlKey || e.metaKey || e.altKey) return;
  if (e.key === "Escape") {
    if (!$("#confirm").hidden) confirmCancel?.();
    else if (!$("#clean").hidden) hideModal("#clean");
    else if (!$("#compare").hidden) hideModal("#compare");
    else if (!$("#browser").hidden) hideModal("#browser");
    return;
  }
  if (currentView !== "review" || !$("#browser").hidden || !$("#clean").hidden) return;
  if (e.target.closest("input, textarea, select")) return;
  const comparing = !$("#compare").hidden;
  const c = currentCluster();
  if (!c) return;

  if (/^[1-9]$/.test(e.key)) {
    const m = c.members[Number(e.key) - 1];
    if (m) {
      e.preventDefault();
      pickKeeper(m.id).then(() => comparing && openCompare());
    }
    return;
  }
  if (comparing) {
    if (e.key === " ") {
      e.preventDefault();
      hideModal("#compare");
    }
    return;
  }
  const key = e.key.toLowerCase();
  if (key === " ") {
    e.preventDefault();
    openCompare();
  } else if (key === "arrowright") {
    e.preventDefault();
    nextGroup();
  } else if (key === "arrowleft") {
    e.preventDefault();
    prevGroup();
  } else if (key === "a") {
    e.preventDefault();
    approveGroup();
  } else if (key === "d" || key === "n") {
    declineGroup();
  } else if (key === "z") {
    undo();
  }
});

// ---------- 4. save ----------
let outFolder = null;
let outCheckTimer = null;
let outCheckSeq = 0;
let copyTimer = null;

async function showSave() {
  const sum = await loadSummary();
  $("#save-form").hidden = false;
  $("#save-progress").hidden = true;
  $("#save-error").hidden = true;

  const input = $("#out-path");
  if (!input.value) input.value = sum.output_dir || (sum.input_dir ? sum.input_dir.replace(/[\\/]+$/, "") + " (clean)" : "");

  $("#save-tiles").innerHTML = [
    tile("Copied to the new folder", plural(sum.keep, "photo"), fmtBytes(sum.keep_bytes), "accent"),
    tile("Left out (extra copies)", plural(sum.extra, "photo"), fmtBytes(sum.extra_bytes), "flat"),
  ].join("");
  $("#save-breakdown").innerHTML = [
    ["Groups using our pick", sum.auto],
    ["Groups where you chose the keeper", sum.changed],
    ['Groups you marked "not duplicates" (all kept)', sum.kept_all],
  ]
    .map(([k, v]) => `<li><span>${k}</span><span class="v">${fmtInt(v)}</span></li>`)
    .join("");
  $("#btn-copy-label").textContent = `Copy ${plural(sum.keep, "photo")}`;
  checkOutput();
}

$("#out-path").addEventListener("input", () => {
  clearTimeout(outCheckTimer);
  outCheckTimer = setTimeout(checkOutput, 350);
});

async function checkOutput() {
  const path = $("#out-path").value.trim();
  const st = $("#out-status");
  const btn = $("#btn-copy");
  outFolder = null;
  btn.disabled = true;
  st.className = "with-icon";
  if (!path) {
    st.textContent = "Choose where to save the copies.";
    return;
  }
  const seq = ++outCheckSeq;
  let f;
  try {
    f = await api("/api/folder?role=output&path=" + encodeURIComponent(path));
  } catch (e) {
    if (seq === outCheckSeq) {
      st.className = "with-icon error-text";
      st.textContent = e.message;
    }
    return;
  }
  if (seq !== outCheckSeq) return;
  const okLine = (txt) => {
    st.className = "with-icon";
    st.style.color = "var(--fg-accent-strong)";
    st.innerHTML = ICON.check + esc(txt);
  };
  st.style.color = "";
  if (f.conflict) {
    st.className = "with-icon error-text";
    st.innerHTML = ICON.alert + esc(friendlyConflict(f.conflict));
    return;
  }
  if (f.exists && !f.is_dir) {
    st.className = "with-icon error-text";
    st.innerHTML = ICON.alert + "That's a file, not a folder.";
    return;
  }
  outFolder = f;
  btn.disabled = false;
  if (!f.exists) okLine("New folder. It will be created.");
  else if (f.entries === 0) okLine("Empty folder.");
  else {
    st.className = "with-icon";
    st.style.color = "var(--warn-fg)";
    st.innerHTML = ICON.alert + esc(`This folder already has ${plural(f.entries, "item")}. Copies are added next to them.`);
  }
}

function friendlyConflict(msg) {
  if (msg.includes("must differ")) return "Pick a different folder from the one you scanned.";
  if (msg.includes("ancestor")) return "This folder contains your photo folder. Pick a folder outside it.";
  if (msg.includes("empty")) return "Choose where to save the copies.";
  return msg;
}

$("#btn-copy").addEventListener("click", async () => {
  if (!outFolder) return;
  $("#btn-copy").disabled = true;
  $("#save-error").hidden = true;
  try {
    await api("/api/config", { json: { output_dir: outFolder.path } });
    await api("/api/export", { method: "POST" });
  } catch (e) {
    $("#save-error").textContent = friendlyConflict(e.message);
    $("#save-error").hidden = false;
    $("#btn-copy").disabled = false;
    return;
  }
  $("#save-form").hidden = true;
  $("#save-progress").hidden = false;
  const t0 = performance.now();
  clearInterval(copyTimer);
  copyTimer = setInterval(async () => {
    let st;
    try {
      st = await api("/api/export/status");
    } catch {
      return;
    }
    const pct = st.total ? (100 * st.done) / st.total : 0;
    $("#copy-count").textContent = st.total ? `${fmtInt(st.done)} / ${fmtInt(st.total)}` : "Starting…";
    $("#copy-bar .bar-fill").style.width = pct + "%";
    const dt = (performance.now() - t0) / 1000;
    $("#copy-eta").textContent = st.done > 0 && dt > 1.5 ? fmtDuration(((st.total - st.done) * dt) / st.done) : "";
    if (!st.running) {
      clearInterval(copyTimer);
      if (st.error) {
        $("#save-progress").hidden = true;
        $("#save-form").hidden = false;
        $("#save-error").textContent = friendlyConflict(st.error);
        $("#save-error").hidden = false;
        $("#btn-copy").disabled = false;
        return;
      }
      state.exportReport = { ...st, seconds: dt };
      go("done");
    }
  }, 400);
});

// ---------- done ----------
async function showDone() {
  let rep = state.exportReport;
  if (!rep) {
    const st = await api("/api/export/status");
    if (!st.report) return go(state.summary?.has_results ? "save" : "choose");
    rep = st;
  }
  const sum = state.summary || (await loadSummary());
  const r = rep.report;
  const errs = r.errors || [];
  $("#done-title").textContent = errs.length ? "Copied, with a few problems" : "Your clean copy is ready";
  $("#done-text").innerHTML = `${plural(r.copied, "photo")} copied to <span class="mono">${esc(rep.output)}</span>. ${plural(r.skipped, "extra copy", "extra copies")} left out${sum.extra_bytes ? `, saving ${fmtBytes(sum.extra_bytes)}` : ""}.`;
  const stats = [plural(errs.length, "error"), `${plural(r.collisions, "file")} renamed`];
  if (rep.seconds) stats.push(`Took ${rep.seconds < 60 ? Math.max(1, Math.round(rep.seconds)) + " s" : Math.round(rep.seconds / 60) + " min"}`);
  $("#done-stats").innerHTML = stats.map((s) => `<span>${esc(s)}</span>`).join("");
  $("#done-errors").hidden = errs.length === 0;
  $("#done-errors-log").textContent = errs.join("\n");
}

$("#btn-open").addEventListener("click", async () => {
  try {
    await api("/api/open", { method: "POST" });
  } catch (e) {
    toast(e.message);
  }
});

// ---------- folder picker ----------
let browseTarget = null;
let browseCurrent = "";

$$("[data-browse]").forEach((btn) =>
  btn.addEventListener("click", () => {
    browseTarget = btn.dataset.browse;
    showModal("#browser");
    listDir($("#" + browseTarget).value.trim());
  })
);

async function listDir(path) {
  let data;
  try {
    data = await api("/api/browse?path=" + encodeURIComponent(path || ""));
  } catch {
    // Path doesn't exist (e.g. a new output folder): start from its parent, else home.
    const parent = path ? path.replace(/[\\/][^\\/]*[\\/]?$/, "") : "";
    if (parent && parent !== path) return listDir(parent);
    if (path) return listDir("");
    return;
  }
  browseCurrent = data.path;
  $("#browser-path").textContent = data.path;
  const ul = $("#browser-list");
  ul.innerHTML = (data.dirs || []).length
    ? data.dirs.map((d) => `<li><button type="button">${ICON.folder}<span>${esc(d)}</span></button></li>`).join("")
    : '<li class="empty">No subfolders here.</li>';
  $$("button", ul).forEach((b, i) => b.addEventListener("click", () => listDir(joinPath(data.path, data.dirs[i]))));
  $("#browser-up").onclick = () => listDir(data.parent);
  $("#browser-up").disabled = data.parent === data.path;
}

function joinPath(base, name) {
  const sep = base.includes("\\") && !base.includes("/") ? "\\" : "/";
  return base.endsWith(sep) ? base + name : base + sep + name;
}

$("#browser-close").addEventListener("click", () => hideModal("#browser"));
$("#browser").addEventListener("click", (e) => {
  if (e.target.id === "browser") hideModal("#browser");
});
$("#browser-use").addEventListener("click", () => {
  if (browseTarget) {
    const input = $("#" + browseTarget);
    input.value = browseCurrent;
    input.dispatchEvent(new Event("input"));
    if (browseTarget === "in-path") checkInput();
    else checkOutput();
  }
  hideModal("#browser");
});

// ---------- boot ----------
(async function boot() {
  const want = location.hash.slice(1);
  if (VIEWS.includes(want)) return render(want);
  try {
    const [st, sum] = await Promise.all([api("/api/status"), loadSummary()]);
    go(st.running ? "scan" : sum.has_results ? "results" : "choose");
  } catch {
    go("choose");
  }
})();
