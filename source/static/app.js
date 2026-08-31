let bandwidthChart = null;
let latencyChart = null;
let currentRange = "7d";
let currentAnalyticsProvider = "all";
let currentAnalyticsServer = "all";
let analyticsFiltersData = { providers: [], servers: [] };
let sseSource = null;

function init() {
  initTheme();
  initAuth();
  initTabs();
  initModals();
  initSettings();
  initAnalytics();
}

function initTheme() {
  const saved = localStorage.getItem("nettrack_theme") || "dark";
  document.documentElement.setAttribute("data-theme", saved);
  updateThemeIcon(saved);

  document.getElementById("btnThemeToggle").addEventListener("click", () => {
    const current = document.documentElement.getAttribute("data-theme");
    const next = current === "dark" ? "light" : "dark";
    document.documentElement.setAttribute("data-theme", next);
    localStorage.setItem("nettrack_theme", next);
    updateThemeIcon(next);
    updateChartsTheme();
  });
}

const SUN_SVG = `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-sun-icon lucide-sun"><circle cx="12" cy="12" r="4"/><path d="M12 2v2"/><path d="M12 20v2"/><path d="m4.93 4.93 1.41 1.41"/><path d="m17.66 17.66 1.41 1.41"/><path d="M2 12h2"/><path d="M20 12h2"/><path d="m6.34 17.66-1.41 1.41"/><path d="m19.07 4.93-1.41 1.41"/></svg>`;
const MOON_SVG = `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-moon-icon lucide-moon"><path d="M20.985 12.486a9 9 0 1 1-9.473-9.472c.405-.022.617.46.402.803a6 6 0 0 0 8.268 8.268c.344-.215.825-.004.803.401"/></svg>`;
const TRASH_SVG = `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-trash2-icon lucide-trash-2"><path d="M10 11v6"/><path d="M14 11v6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"/><path d="M3 6h18"/><path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>`;

function updateThemeIcon(theme) {
  const icon = document.getElementById("themeIcon");
  icon.innerHTML = theme === "dark" ? SUN_SVG : MOON_SVG;
}

async function initAuth() {
  const authModal = document.getElementById("authModal");
  const authForm = document.getElementById("authForm");
  const authError = document.getElementById("authError");
  const authPassword = document.getElementById("authPassword");
  const logoutBtn = document.getElementById("btnLogout");

  try {
    const res = await fetch("/api/auth/status");
    const data = await res.json();
    if (data.authenticated) {
      authModal.classList.remove("active");
      startApp();
    } else {
      authModal.classList.add("active");
    }
  } catch {
    authModal.classList.add("active");
  }

  authForm.addEventListener("submit", async (e) => {
    e.preventDefault();
    authError.style.display = "none";
    const password = authPassword.value;

    try {
      const res = await fetch("/api/auth/login", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ password }),
      });
      const data = await res.json();
      if (res.ok && data.authenticated) {
        authModal.classList.remove("active");
        authPassword.value = "";
        startApp();
      } else {
        authError.textContent = data.error || "Invalid password";
        authError.style.display = "block";
      }
    } catch {
      authError.textContent = "Authentication request failed";
      authError.style.display = "block";
    }
  });

  logoutBtn.addEventListener("click", async () => {
    try {
      await fetch("/api/auth/logout", { method: "POST" });
    } finally {
      if (sseSource) {
        sseSource.close();
        sseSource = null;
      }
      authModal.classList.add("active");
    }
  });
}

function startApp() {
  initSSE();
  loadHistory();
  loadAnalyticsFilters();
  loadAnalytics(currentRange);
  loadSettings();
}

function initTabs() {
  const tabs = document.querySelectorAll(".nav-tab");
  tabs.forEach((tab) => {
    tab.addEventListener("click", () => {
      tabs.forEach((t) => t.classList.remove("active"));
      tab.classList.add("active");

      const target = tab.getAttribute("data-tab");
      document.querySelectorAll(".tab-pane").forEach((pane) => {
        pane.classList.remove("active");
      });

      const activePane = document.getElementById(
        "tab" + target.charAt(0).toUpperCase() + target.slice(1),
      );
      if (activePane) {
        activePane.classList.add("active");
      }

      if (target === "dashboard") {
        loadHistory();
      } else if (target === "analytics") {
        loadAnalyticsFilters();
        loadAnalytics(currentRange);
      } else if (target === "settings") {
        loadSettings();
      }
    });
  });
}

function initSSE() {
  if (sseSource) {
    sseSource.close();
  }

  sseSource = new EventSource("/api/speedtest/events");

  sseSource.onmessage = (event) => {
    try {
      const data = JSON.parse(event.data);
      updateLiveProgress(data);
    } catch {}
  };

  sseSource.onerror = () => {
    setTimeout(() => {
      if (!sseSource || sseSource.readyState === EventSource.CLOSED) {
        initSSE();
      }
    }, 3000);
  };

  document
    .getElementById("btnCancelTest")
    .addEventListener("click", async () => {
      try {
        await fetch("/api/speedtest/cancel", { method: "POST" });
        showToast("Cancellation requested");
      } catch {}
    });
}

function updateLiveProgress(data) {
  const liveCard = document.getElementById("liveCard");
  const stage = data.stage;

  if (!stage || stage === "idle") {
    liveCard.classList.remove("active");
    return;
  }

  liveCard.classList.add("active");
  const stageName = stage.charAt(0).toUpperCase() + stage.slice(1);
  document.getElementById("liveStage").textContent = stageName;
  document.getElementById("liveServer").textContent =
    data.server_name || data.server_type || "";

  const bar = document.getElementById("liveProgressBar");
  const pct = Math.min(Math.max((data.progress || 0) * 100, 0), 100);
  bar.style.width = pct + "%";

  document.getElementById("liveDl").textContent = (data.download || 0).toFixed(
    2,
  );
  document.getElementById("liveUl").textContent = (data.upload || 0).toFixed(2);
  document.getElementById("livePing").textContent = (data.ping || 0).toFixed(1);
  document.getElementById("liveJitter").textContent = (
    data.jitter || 0
  ).toFixed(1);

  if (stage === "complete") {
    loadHistory();
    loadAnalyticsFilters();
    loadAnalytics(currentRange);
    setTimeout(() => {
      liveCard.classList.remove("active");
    }, 4000);
  } else if (stage === "error") {
    showToast("Test failed: " + (data.error || "unknown error"));
    setTimeout(() => {
      liveCard.classList.remove("active");
    }, 4000);
  }
}

function initModals() {
  const runModal = document.getElementById("runModal");
  const btnOpen = document.getElementById("btnOpenRunModal");
  const btnCancel = document.getElementById("btnCancelRunModal");
  const btnStart = document.getElementById("btnStartSpeedtest");
  const modalProvider = document.getElementById("modalProvider");
  const modalServer = document.getElementById("modalServer");

  btnOpen.addEventListener("click", () => {
    runModal.classList.add("active");
    loadServersForSelect(modalProvider.value, modalServer);
  });

  const closeModal = () => runModal.classList.remove("active");
  if (btnCancel) {
    btnCancel.addEventListener("click", closeModal);
  }

  modalProvider.addEventListener("change", () => {
    loadServersForSelect(modalProvider.value, modalServer);
  });

  btnStart.addEventListener("click", async () => {
    const provider = modalProvider.value;
    const server_id = modalServer.value;

    btnStart.disabled = true;
    try {
      const res = await fetch("/api/speedtest/run", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ provider, server_id }),
      });
      const data = await res.json();
      if (res.ok) {
        closeModal();
        showToast("Speedtest started");
      } else {
        showToast(data.error || "Failed to start test");
      }
    } catch {
      showToast("Network error starting test");
    } finally {
      btnStart.disabled = false;
    }
  });
}

async function loadServersForSelect(
  provider,
  selectElem,
  selectedValue = "auto",
) {
  selectElem.innerHTML = '<option value="auto">Auto / Nearest Server</option>';
  try {
    const res = await fetch(
      "/api/servers?type=" + encodeURIComponent(provider),
    );
    const servers = await res.json();
    if (Array.isArray(servers)) {
      servers.forEach((s) => {
        if (s.id !== "auto") {
          const opt = document.createElement("option");
          opt.value = s.id;
          opt.textContent = s.name || s.host || s.id;
          if (s.id === selectedValue) {
            opt.selected = true;
          }
          selectElem.appendChild(opt);
        }
      });
    }
  } catch {}
}

async function loadHistory() {
  const tbody = document.getElementById("historyTableBody");
  const emptyState = document.getElementById("historyEmpty");

  try {
    const res = await fetch("/api/speedtest/history?limit=100");
    const data = await res.json();

    if (!data.results || data.results.length === 0) {
      tbody.innerHTML = "";
      emptyState.style.display = "flex";
      return;
    }

    emptyState.style.display = "none";
    tbody.innerHTML = data.results
      .map((r) => {
        const date = new Date(r.timestamp * 1000).toLocaleString();
        const isFailed = r.status === "failed";
        const schedBadge = r.is_scheduled
          ? ' <span class="badge">Scheduled</span>'
          : "";
        const statusBadge = isFailed
          ? ' <span class="badge danger" title="' +
            escapeHtml(r.error || "Test failed") +
            '">Failed</span>'
          : "";
        const serverText =
          r.server_id &&
          r.server_id !== "auto" &&
          !r.server_name?.includes(r.server_id)
            ? `${r.server_name || r.server_host || "-"} (${r.server_id})`
            : r.server_name || r.server_host || "-";
        const dlText = isFailed
          ? '<span style="color: var(--text-muted);">-</span>'
          : `${r.download_mbps.toFixed(2)} Mbps`;
        const ulText = isFailed
          ? '<span style="color: var(--text-muted);">-</span>'
          : `${r.upload_mbps.toFixed(2)} Mbps`;
        const pingText = isFailed
          ? '<span style="color: var(--text-muted);">-</span>'
          : `${r.ping_ms.toFixed(1)} ms`;
        const jitterText = isFailed
          ? '<span style="color: var(--text-muted);">-</span>'
          : `${r.jitter_ms.toFixed(1)} ms`;

        return `
        <tr>
          <td class="font-mono">${date}</td>
          <td><span class="badge">${escapeHtml(r.provider)}</span>${schedBadge}${statusBadge}</td>
          <td>${escapeHtml(serverText)}</td>
          <td class="font-mono" style="font-weight: 600;">${dlText}</td>
          <td class="font-mono" style="font-weight: 600;">${ulText}</td>
          <td class="font-mono">${pingText}</td>
          <td class="font-mono">${jitterText}</td>
          <td class="font-mono">${r.duration_s.toFixed(1)}s</td>
          <td style="text-align: right;">
            <button class="btn icon-only sm danger" onclick="deleteHistoryItem('${r.id}')" title="Delete">${TRASH_SVG}</button>
          </td>
        </tr>
      `;
      })
      .join("");
  } catch {}
}

window.deleteHistoryItem = async function (id) {
  try {
    const res = await fetch(
      "/api/speedtest/history/" + encodeURIComponent(id),
      {
        method: "DELETE",
      },
    );
    if (res.ok) {
      loadHistory();
      loadAnalytics(currentRange);
      showToast("Entry deleted");
    }
  } catch {}
};

function initAnalytics() {
  const rangeBtns = document.querySelectorAll(".range-btn");
  rangeBtns.forEach((btn) => {
    btn.addEventListener("click", () => {
      rangeBtns.forEach((b) => b.classList.remove("active"));
      btn.classList.add("active");
      currentRange = btn.getAttribute("data-range");
      loadAnalytics(currentRange);
    });
  });

  const provFilter = document.getElementById("analyticsProviderFilter");
  const srvFilter = document.getElementById("analyticsServerFilter");

  if (provFilter) {
    provFilter.addEventListener("change", () => {
      currentAnalyticsProvider = provFilter.value;
      currentAnalyticsServer = "all";
      updateServerFilterDropdown();
      loadAnalytics(currentRange);
    });
  }

  if (srvFilter) {
    srvFilter.addEventListener("change", () => {
      currentAnalyticsServer = srvFilter.value;
      loadAnalytics(currentRange);
    });
  }
}

async function loadAnalyticsFilters() {
  try {
    const res = await fetch("/api/speedtest/filters");
    const data = await res.json();
    if (data && Array.isArray(data.providers)) {
      analyticsFiltersData = data;
      const provSelect = document.getElementById("analyticsProviderFilter");
      if (provSelect) {
        provSelect.innerHTML = '<option value="all">All Providers</option>';
        data.providers.forEach((p) => {
          const opt = document.createElement("option");
          opt.value = p;
          opt.textContent = p.charAt(0).toUpperCase() + p.slice(1);
          if (p === currentAnalyticsProvider) {
            opt.selected = true;
          }
          provSelect.appendChild(opt);
        });
      }
      updateServerFilterDropdown();
    }
  } catch {}
}

function updateServerFilterDropdown() {
  const srvSelect = document.getElementById("analyticsServerFilter");
  if (!srvSelect) return;

  srvSelect.innerHTML = '<option value="all">All Servers</option>';
  const filtered = (analyticsFiltersData.servers || []).filter((s) => {
    if (
      currentAnalyticsProvider !== "all" &&
      s.provider !== currentAnalyticsProvider
    ) {
      return false;
    }
    return true;
  });

  filtered.forEach((s) => {
    const opt = document.createElement("option");
    opt.value = s.id;
    opt.textContent = s.name || s.id;
    if (s.id === currentAnalyticsServer) {
      opt.selected = true;
    }
    srvSelect.appendChild(opt);
  });
}

async function loadAnalytics(range) {
  try {
    let url = "/api/speedtest/analytics?range=" + encodeURIComponent(range);
    if (currentAnalyticsProvider !== "all") {
      url += "&provider=" + encodeURIComponent(currentAnalyticsProvider);
    }
    if (currentAnalyticsServer !== "all") {
      url += "&server=" + encodeURIComponent(currentAnalyticsServer);
    }

    const res = await fetch(url);
    const data = await res.json();

    const dl = data.download || {};
    const ul = data.upload || {};
    const ping = data.ping || {};
    const jitter = data.jitter || {};

    document.getElementById("statAvgDl").textContent = dl.avg
      ? dl.avg.toFixed(2)
      : "-";
    document.getElementById("statPeakDl").textContent = dl.max
      ? dl.max.toFixed(2)
      : "-";
    document.getElementById("statP90Dl").textContent = dl.p90
      ? dl.p90.toFixed(2)
      : "-";
    document.getElementById("statMinDl").textContent = dl.min
      ? dl.min.toFixed(2)
      : "-";

    document.getElementById("statAvgUl").textContent = ul.avg
      ? ul.avg.toFixed(2)
      : "-";
    document.getElementById("statPeakUl").textContent = ul.max
      ? ul.max.toFixed(2)
      : "-";
    document.getElementById("statP90Ul").textContent = ul.p90
      ? ul.p90.toFixed(2)
      : "-";
    document.getElementById("statMinUl").textContent = ul.min
      ? ul.min.toFixed(2)
      : "-";

    document.getElementById("statAvgPing").textContent = ping.avg
      ? ping.avg.toFixed(1)
      : "-";
    document.getElementById("statMinPing").textContent = ping.min
      ? ping.min.toFixed(1)
      : "-";
    document.getElementById("statP90Ping").textContent = ping.p90
      ? ping.p90.toFixed(1)
      : "-";
    document.getElementById("statAvgJitter").textContent = jitter.avg
      ? jitter.avg.toFixed(1)
      : "-";

    document.getElementById("statSuccessRate").textContent =
      data.total_tests > 0 ? (data.success_rate || 0).toFixed(1) : "-";
    document.getElementById("statTotalTests").textContent = String(
      data.total_tests || 0,
    );
    document.getElementById("statFailedTests").textContent = String(
      data.failed_tests || 0,
    );
    document.getElementById("statTotalData").textContent =
      (data.total_data_gb || 0).toFixed(2) + " GB";

    renderCharts(data.points || []);
  } catch {}
}

function renderCharts(points) {
  const isDark = document.documentElement.getAttribute("data-theme") === "dark";
  const fg = isDark ? "#f2f4f8" : "#0d0e11";
  const fgMuted = isDark ? "#62687a" : "#8c93a2";
  const gridColor = isDark
    ? "rgba(255, 255, 255, 0.06)"
    : "rgba(0, 0, 0, 0.06)";

  const timestamps = points.map((p) => p.timestamp);
  const dlData = points.map((p) => p.download);
  const ulData = points.map((p) => p.upload);
  const pingData = points.map((p) => p.ping);
  const jitterData = points.map((p) => p.jitter);

  const bwContainer = document.getElementById("bandwidthChart");
  const latContainer = document.getElementById("latencyChart");

  const getWidth = (elem) =>
    elem.parentElement.clientWidth || elem.clientWidth || 600;

  const bwData = [timestamps, dlData, ulData];
  const latData = [timestamps, pingData, jitterData];

  const commonAxes = [
    {
      stroke: fgMuted,
      grid: { stroke: gridColor, width: 1 },
      ticks: { stroke: gridColor, width: 1 },
      font: '11px "JetBrains Mono"',
      values: (self, splits) =>
        splits.map((ts) => {
          const d = new Date(ts * 1000);
          return `${d.getMonth() + 1}/${d.getDate()} ${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
        }),
    },
    {
      stroke: fgMuted,
      grid: { stroke: gridColor, width: 1 },
      ticks: { stroke: gridColor, width: 1 },
      font: '11px "JetBrains Mono"',
    },
  ];

  const bwOpts = {
    width: getWidth(bwContainer),
    height: 240,
    cursor: { drag: { x: false, y: false } },
    legend: { show: true },
    scales: { x: { time: true }, y: { auto: true } },
    axes: commonAxes,
    series: [
      {},
      {
        label: "Download (Mbps)",
        stroke: fg,
        width: 2,
        fill: isDark ? "rgba(255, 255, 255, 0.06)" : "rgba(0, 0, 0, 0.06)",
        points: { show: points.length <= 50, size: 4 },
      },
      {
        label: "Upload (Mbps)",
        stroke: fgMuted,
        width: 2,
        dash: [4, 4],
        points: { show: points.length <= 50, size: 4 },
      },
    ],
  };

  const latOpts = {
    width: getWidth(latContainer),
    height: 240,
    cursor: { drag: { x: false, y: false } },
    legend: { show: true },
    scales: { x: { time: true }, y: { auto: true } },
    axes: commonAxes,
    series: [
      {},
      {
        label: "Ping (ms)",
        stroke: fg,
        width: 2,
        fill: isDark ? "rgba(255, 255, 255, 0.06)" : "rgba(0, 0, 0, 0.06)",
        points: { show: points.length <= 50, size: 4 },
      },
      {
        label: "Jitter (ms)",
        stroke: fgMuted,
        width: 2,
        dash: [4, 4],
        points: { show: points.length <= 50, size: 4 },
      },
    ],
  };

  if (bandwidthChart) {
    bandwidthChart.destroy();
    bandwidthChart = null;
  }
  bwContainer.innerHTML = "";
  bandwidthChart = new uPlot(bwOpts, bwData, bwContainer);

  if (latencyChart) {
    latencyChart.destroy();
    latencyChart = null;
  }
  latContainer.innerHTML = "";
  latencyChart = new uPlot(latOpts, latData, latContainer);
}

function updateChartsTheme() {
  if (bandwidthChart && latencyChart) {
    loadAnalytics(currentRange);
  }
}

window.addEventListener("resize", () => {
  if (bandwidthChart && document.getElementById("bandwidthChart")) {
    const bwElem = document.getElementById("bandwidthChart");
    bandwidthChart.setSize({
      width: bwElem.parentElement.clientWidth || 600,
      height: 240,
    });
  }
  if (latencyChart && document.getElementById("latencyChart")) {
    const latElem = document.getElementById("latencyChart");
    latencyChart.setSize({
      width: latElem.parentElement.clientWidth || 600,
      height: 240,
    });
  }
});

function initSettings() {
  const cronEnabled = document.getElementById("settingCronEnabled");
  const cronPreset = document.getElementById("settingCronPreset");
  const cronExpr = document.getElementById("settingCronExpr");
  const groupCustomCron = document.getElementById("groupCustomCron");
  const provider = document.getElementById("settingProvider");
  const server = document.getElementById("settingServer");
  const retention = document.getElementById("settingRetention");
  const btnSave = document.getElementById("btnSaveSettings");

  cronPreset.addEventListener("change", () => {
    if (cronPreset.value === "custom") {
      groupCustomCron.style.display = "flex";
    } else {
      groupCustomCron.style.display = "none";
      cronExpr.value = cronPreset.value;
    }
  });

  provider.addEventListener("change", () => {
    loadServersForSelect(provider.value, server);
  });

  btnSave.addEventListener("click", async () => {
    const expr =
      cronPreset.value === "custom" ? cronExpr.value : cronPreset.value;
    const body = {
      cron_enabled: cronEnabled.checked ? "true" : "false",
      cron_expression: expr,
      cron_provider: provider.value,
      cron_server_id: server.value,
      retention_days: retention.value,
    };

    btnSave.disabled = true;
    try {
      const res = await fetch("/api/settings", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      const data = await res.json();
      if (res.ok) {
        showToast("Settings saved");
      } else {
        showToast(data.error || "Failed to save settings");
      }
    } catch {
      showToast("Network error saving settings");
    } finally {
      btnSave.disabled = false;
    }
  });
}

async function loadSettings() {
  const cronEnabled = document.getElementById("settingCronEnabled");
  const cronPreset = document.getElementById("settingCronPreset");
  const cronExpr = document.getElementById("settingCronExpr");
  const groupCustomCron = document.getElementById("groupCustomCron");
  const provider = document.getElementById("settingProvider");
  const server = document.getElementById("settingServer");
  const retention = document.getElementById("settingRetention");

  try {
    const res = await fetch("/api/settings");
    const data = await res.json();

    cronEnabled.checked = data.cron_enabled === "true";
    const expr = data.cron_expression || "0 */6 * * *";
    cronExpr.value = expr;

    let matchedPreset = false;
    for (let i = 0; i < cronPreset.options.length; i++) {
      if (cronPreset.options[i].value === expr) {
        cronPreset.selectedIndex = i;
        matchedPreset = true;
        break;
      }
    }

    if (!matchedPreset) {
      cronPreset.value = "custom";
      groupCustomCron.style.display = "flex";
    } else {
      groupCustomCron.style.display = "none";
    }

    provider.value = data.cron_provider || "cloudflare";
    retention.value = data.retention_days || "0";

    await loadServersForSelect(
      provider.value,
      server,
      data.cron_server_id || "auto",
    );
  } catch {}
}

function showToast(message) {
  if (!message) return;
  const str = String(message);
  const formatted = str.charAt(0).toUpperCase() + str.slice(1);
  const toast = document.getElementById("toast");
  toast.textContent = formatted;
  toast.classList.add("show");
  clearTimeout(window.toastTimer);
  window.toastTimer = setTimeout(() => {
    toast.classList.remove("show");
  }, 3000);
}

function escapeHtml(str) {
  if (!str) return "";
  return str
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#039;");
}

document.addEventListener("DOMContentLoaded", init);
