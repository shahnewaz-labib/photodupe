"use strict";

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => Array.from(document.querySelectorAll(sel));

async function api(path, opts) {
  const res = await fetch(path, opts);
  if (!res.ok) throw new Error((await res.text()) || res.statusText);
  const ct = res.headers.get("content-type") || "";
  return ct.includes("application/json") ? res.json() : res.text();
}

// ---- tabs ----
$$(".tab").forEach((btn) => {
  btn.addEventListener("click", () => {
    $$(".tab").forEach((b) => b.classList.remove("active"));
    $$(".panel").forEach((p) => p.classList.remove("active"));
    btn.classList.add("active");
    $("#tab-" + btn.dataset.tab).classList.add("active");
    if (btn.dataset.tab === "review") loadClusters();
    if (btn.dataset.tab === "settings") loadConfig();
  });
});

// ---- folder browser ----
let browseTarget = null;
let browseCurrent = "";

$$("[data-browse]").forEach((btn) => {
  btn.addEventListener("click", () => {
    browseTarget = btn.dataset.browse;
    const start = $("#" + browseTarget).value.trim();
    openBrowser(start);
  });
});

async function openBrowser(path) {
  $("#browser").classList.remove("hidden");
  await listDir(path);
}

async function listDir(path) {
  const data = await api("/api/browse?path=" + encodeURIComponent(path || ""));
  browseCurrent = data.path;
  $("#browser-path").textContent = data.path;
  const ul = $("#browser-list");
  ul.innerHTML = "";
  (data.dirs || []).forEach((d) => {
    const li = document.createElement("li");
    li.textContent = "📁 " + d;
    li.addEventListener("click", () => listDir(joinPath(data.path, d)));
    ul.appendChild(li);
  });
  $("#browser-up").onclick = () => listDir(data.parent);
}

function joinPath(base, name) {
  return base.endsWith("/") ? base + name : base + "/" + name;
}

$("#browser-close").addEventListener("click", () => $("#browser").classList.add("hidden"));
$("#browser-use").addEventListener("click", () => {
  if (browseTarget) $("#" + browseTarget).value = browseCurrent;
  $("#browser").classList.add("hidden");
});

// ---- run + progress ----
let pollTimer = null;

$("#btn-start").addEventListener("click", async () => {
  const cfg = {
    input_dir: $("#input-dir").value.trim(),
    output_dir: $("#output-dir").value.trim(),
  };
  if (!cfg.input_dir) {
    $("#run-hint").textContent = "Set an input folder first.";
    return;
  }
  try {
    await api("/api/config", { method: "POST", body: JSON.stringify(cfg) });
    await api("/api/run", { method: "POST" });
    $("#run-hint").textContent = "";
    $("#progress").classList.remove("hidden");
    startPolling();
  } catch (e) {
    $("#run-hint").textContent = "Error: " + e.message;
  }
});

function startPolling() {
  if (pollTimer) clearInterval(pollTimer);
  pollTimer = setInterval(pollStatus, 1000);
  pollStatus();
}

async function pollStatus() {
  let s;
  try {
    s = await api("/api/status");
  } catch (e) {
    return;
  }
  $("#prog-phase").textContent = s.phase || "—";
  $("#prog-counts").textContent = s.total > 0 ? `${s.done} / ${s.total}` : "";
  const pct = s.total > 0 ? Math.round((100 * s.done) / s.total) : (s.running ? 0 : 100);
  $("#prog-fill").style.width = pct + "%";
  $("#prog-log").textContent = (s.log || []).join("\n");
  $("#btn-start").disabled = s.running;
  if (!s.running) {
    clearInterval(pollTimer);
    pollTimer = null;
    if (s.error) $("#run-hint").textContent = "Failed: " + s.error;
  }
}

// ---- review ----
async function loadClusters() {
  let clusters;
  try {
    clusters = await api("/api/clusters");
  } catch (e) {
    return;
  }
  const wrap = $("#clusters");
  wrap.innerHTML = "";
  $("#review-empty").style.display = clusters.length ? "none" : "block";

  clusters.forEach((c) => wrap.appendChild(renderCluster(c)));
}

function renderCluster(c) {
  const card = document.createElement("div");
  card.className = "cluster" + (c.decision.resolved ? " resolved" : "");

  const head = document.createElement("div");
  head.className = "cluster-head row spread";
  head.innerHTML = `<span><strong>${c.id}</strong>
    ${c.tiers.map((t) => `<span class="badge tier-${t}">Tier ${t}</span>`).join("")}
    <span class="hint">${c.members.length} images</span>
    ${c.decision.resolved ? '<span class="badge ok">resolved</span>' : ""}</span>`;
  card.appendChild(head);

  const skipLabel = document.createElement("label");
  skipLabel.className = "skip";
  const skip = document.createElement("input");
  skip.type = "checkbox";
  skip.checked = c.decision.skipped;
  skipLabel.appendChild(skip);
  skipLabel.append(" Not actually duplicates (keep all)");
  card.appendChild(skipLabel);

  const grid = document.createElement("div");
  grid.className = "grid";
  c.members.forEach((m) => {
    const cell = document.createElement("div");
    cell.className = "cell" + (m.keeper ? " keeper" : "");
    cell.innerHTML = `
      <a href="/api/image?id=${m.id}" target="_blank" title="open full image">
        <img loading="lazy" src="/api/image?id=${m.id}&thumb=1" alt="${m.filename}">
      </a>
      <label class="keep-radio">
        <input type="radio" name="keep-${c.id}" value="${m.id}" ${m.keeper ? "checked" : ""}>
        keep
      </label>
      <div class="meta">${m.filename}<br>${m.width}×${m.height} · ${fmtSize(m.file_size)}<br>${m.capture_time}</div>`;
    grid.appendChild(cell);
  });
  card.appendChild(grid);

  const save = document.createElement("button");
  save.className = "primary";
  save.textContent = "Save decision";
  save.addEventListener("click", async () => {
    const picked = card.querySelector(`input[name="keep-${c.id}"]:checked`);
    const dec = {
      keeper_id: picked ? picked.value : "0", // string: uint64 exceeds JS safe int
      skipped: skip.checked,
      resolved: true,
    };
    try {
      await api(`/api/clusters/${c.id}/decision`, { method: "POST", body: JSON.stringify(dec) });
      loadClusters();
    } catch (e) {
      alert("Save failed: " + e.message);
    }
  });
  card.appendChild(save);
  return card;
}

function fmtSize(n) {
  if (n > 1e6) return (n / 1e6).toFixed(1) + " MB";
  if (n > 1e3) return (n / 1e3).toFixed(0) + " KB";
  return n + " B";
}

$("#btn-refresh").addEventListener("click", loadClusters);

// ---- settings ----
async function loadConfig() {
  const c = await api("/api/config");
  $("#phash-enabled").checked = c.phash_enabled;
  $("#phash-threshold").value = c.phash_threshold;
  $("#phash-th-val").textContent = c.phash_threshold;
  $("#clip-enabled").checked = c.clip_enabled;
  $("#clip-threshold").value = c.clip_similarity_thresh;
  $("#clip-th-val").textContent = c.clip_similarity_thresh;
}

$("#phash-threshold").addEventListener("input", (e) => ($("#phash-th-val").textContent = e.target.value));
$("#clip-threshold").addEventListener("input", (e) => ($("#clip-th-val").textContent = e.target.value));

function currentParams() {
  return {
    phash_enabled: $("#phash-enabled").checked,
    phash_threshold: Number($("#phash-threshold").value),
    clip_enabled: $("#clip-enabled").checked,
    clip_similarity_thresh: Number($("#clip-threshold").value),
  };
}

$("#btn-save-config").addEventListener("click", async () => {
  await api("/api/config", { method: "POST", body: JSON.stringify(currentParams()) });
  $("#config-hint").textContent = "Saved.";
});

$("#btn-recluster").addEventListener("click", async () => {
  $("#config-hint").textContent = "Re-clustering…";
  try {
    await api("/api/config", { method: "POST", body: JSON.stringify(currentParams()) });
    const r = await api("/api/recluster", { method: "POST" });
    $("#config-hint").textContent = r.warning
      ? "⚠ " + r.warning
      : `Re-clustered (${r.clusters} clusters). See Review tab.`;
  } catch (e) {
    $("#config-hint").textContent = "Error: " + e.message;
  }
});

$("#btn-export").addEventListener("click", async () => {
  $("#export-hint").textContent = "Copying…";
  try {
    const rep = await api("/api/export", { method: "POST" });
    $("#export-hint").textContent =
      `Copied ${rep.copied}, skipped ${rep.skipped} dups, ${rep.collisions} renamed, ${(rep.errors || []).length} errors.`;
  } catch (e) {
    $("#export-hint").textContent = "Error: " + e.message;
  }
});

// resume progress if a run is already going
pollStatus().then(() => {
  api("/api/status").then((s) => {
    if (s.running) {
      $("#progress").classList.remove("hidden");
      startPolling();
    }
  });
});
