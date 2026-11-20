// SSHYard website: appearance, language, downloads for this device, the
// demo window and the small interactions. No dependencies; the pages work
// without it.
(function () {
  "use strict";

  var doc = document.documentElement;
  var data = {};
  try { data = JSON.parse(document.getElementById("site-data").textContent); } catch (e) {}
  var reduced = window.matchMedia("(prefers-reduced-motion: reduce)");

  function store(key, value) {
    try {
      if (value === undefined) return localStorage.getItem(key);
      if (value === null) localStorage.removeItem(key); else localStorage.setItem(key, value);
    } catch (e) { return null; }
  }
  function $$(sel, root) { return Array.prototype.slice.call((root || document).querySelectorAll(sel)); }
  function sleep(ms) { return new Promise(function (r) { setTimeout(r, ms); }); }

  // ---------------------------------------------------------------- appearance

  var themeColors = { light: "#F5F5F7", dark: "#1E1E20" };
  var systemDark = window.matchMedia("(prefers-color-scheme: dark)");

  function applyTheme(mode) {
    if (mode === "light" || mode === "dark") doc.setAttribute("data-theme", mode);
    else doc.removeAttribute("data-theme");
    $$("[data-theme-set]").forEach(function (b) {
      b.setAttribute("aria-checked", String(b.getAttribute("data-theme-set") === (mode || "auto")));
    });
    var dark = mode === "dark" || (mode !== "light" && systemDark.matches);
    $$('meta[name="theme-color"]').forEach(function (m) {
      if (mode === "light" || mode === "dark") m.setAttribute("content", themeColors[dark ? "dark" : "light"]);
      else m.setAttribute("content", m.media.indexOf("dark") >= 0 ? themeColors.dark : themeColors.light);
    });
  }
  applyTheme(store("sshyard-theme"));
  $$("[data-theme-set]").forEach(function (b) {
    b.addEventListener("click", function () {
      var mode = b.getAttribute("data-theme-set");
      store("sshyard-theme", mode === "auto" ? null : mode);
      applyTheme(mode);
    });
  });
  $$(".theme-switch").forEach(function (group) {
    group.addEventListener("keydown", function (e) {
      if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
      var items = $$("[data-theme-set]", group);
      var i = items.indexOf(document.activeElement);
      if (i < 0) return;
      var next = items[(i + (e.key === "ArrowRight" ? 1 : items.length - 1)) % items.length];
      next.focus();
      next.click();
      e.preventDefault();
    });
  });

  // ---------------------------------------------------------------- menus

  function closeDetails(except) {
    $$("details.lang[open], details.menu[open]").forEach(function (d) { if (d !== except) d.open = false; });
  }
  document.addEventListener("click", function (e) {
    var inside = e.target.closest && e.target.closest("details.lang, details.menu");
    closeDetails(inside);
    if (e.target.closest && e.target.closest(".menu-panel a")) closeDetails();
  });
  document.addEventListener("keydown", function (e) {
    if (e.key !== "Escape") return;
    var open = document.querySelector("details.lang[open], details.menu[open]");
    if (open) { open.open = false; open.querySelector("summary").focus(); }
  });

  var nav = document.querySelector(".nav");
  function onScroll() { if (nav) nav.classList.toggle("scrolled", window.scrollY > 4); }
  window.addEventListener("scroll", onScroll, { passive: true });
  onScroll();

  // ---------------------------------------------------------------- language

  $$("[data-lang-pick]").forEach(function (a) {
    a.addEventListener("click", function () { store("sshyard-lang", a.getAttribute("data-lang-pick")); });
  });

  // The <head> script of base.html matches the browser's language and, on a
  // first visit to an English page, already redirected. Elsewhere (a shared
  // link into another language) only suggest the visitor's own.
  (function langBanner() {
    var banner = document.querySelector(".langbanner");
    if (!banner || !data.banner || store("sshyard-lang")) return;
    var want = doc.getAttribute("data-pref-lang") || "en";
    if (want === data.lang || !data.banner[want]) return;
    var alt = document.querySelector('link[rel="alternate"][hreflang="' + want + '"]');
    if (!alt) return;
    var b = data.banner[want];
    banner.setAttribute("lang", want);
    banner.querySelector(".langbanner-text").textContent = b.text;
    var go = banner.querySelector(".langbanner-go");
    go.textContent = b.switch;
    go.href = new URL(alt.href).pathname + location.hash;
    go.addEventListener("click", function () { store("sshyard-lang", want); });
    var close = banner.querySelector(".langbanner-close");
    close.setAttribute("aria-label", b.dismiss);
    close.title = b.dismiss;
    close.addEventListener("click", function () { store("sshyard-lang", data.lang); banner.hidden = true; });
    setTimeout(function () { banner.hidden = false; }, 900);
  })();

  // ---------------------------------------------------------------- downloads

  function detectOS() {
    var ua = navigator.userAgent || "";
    var p = (navigator.userAgentData && navigator.userAgentData.platform) || navigator.platform || "";
    if (/iPhone|iPad|iPod|Android/i.test(ua) || (/Mac/i.test(p) && navigator.maxTouchPoints > 1)) return "";
    if (/Mac/i.test(p) || /Mac OS X/.test(ua)) return "macos";
    if (/Win/i.test(p) || /Windows/.test(ua)) return "windows";
    if (/Linux|X11|CrOS/i.test(p + " " + ua)) return "linux";
    return "";
  }

  // Apple Silicon unless the browser says Intel: high-entropy hints where
  // available, else the WebGL renderer (Safari reports "Apple GPU").
  // Windows and Linux are x64 unless the hints or the user agent say Arm
  // (x64 builds also run emulated on Windows on Arm).
  function arch(os) {
    var guess = os === "macos" ? function () {
      try {
        var gl = document.createElement("canvas").getContext("webgl");
        var ext = gl && gl.getExtension("WEBGL_debug_renderer_info");
        var r = ext ? gl.getParameter(ext.UNMASKED_RENDERER_WEBGL) : "";
        return /Intel|AMD|Radeon|NVIDIA/i.test(r) ? "amd64" : "arm64";
      } catch (e) { return "arm64"; }
    } : function () {
      return /aarch64|arm64|armv8/i.test(navigator.userAgent || "") ? "arm64" : "amd64";
    };
    var hints = navigator.userAgentData && navigator.userAgentData.getHighEntropyValues;
    if (!hints) return Promise.resolve(guess());
    return navigator.userAgentData.getHighEntropyValues(["architecture"]).then(function (v) {
      if (v.architecture === "x86") return "amd64";
      if (v.architecture === "arm") return "arm64";
      return guess();
    }, guess);
  }

  (function downloads() {
    var os = detectOS();
    if (!os || !data.downloads) return;
    doc.setAttribute("data-os", os);
    arch(os).then(function (a) {
      var arm = a === "arm64";
      var key = os === "linux" ? "linux"
        : os === "windows" ? (arm && data.downloads.win_setup_arm64 ? "win_setup_arm64" : "win_setup")
        : arm ? "mac_arm64" : "mac_amd64";
      $$("[data-dl-cta]").forEach(function (el) {
        var label = el.querySelector("[data-dl-label]");
        if (label && data.cta[os]) label.textContent = data.cta[os];
        if (key !== "linux") el.href = data.downloads[key].url; else el.href = data.linuxPage;
      });
      var meta = document.querySelector("[data-dl-meta]");
      if (meta && key !== "linux") {
        var d = data.downloads[key];
        meta.textContent = [data.labels[key], d.size].filter(Boolean).join(" · ");
      }
      // The visitor's architecture leads in their platform's card.
      $$('[data-platform="' + os + '"] .dl-files').forEach(function (list) {
        var chips = $$("[data-asset]", list);
        var mine = chips.filter(function (x) { return /_arm64$/.test(x.getAttribute("data-asset")) === arm; });
        if (!mine.length || mine[0] === chips[0]) return;
        mine.concat(chips.filter(function (x) { return mine.indexOf(x) < 0; })).forEach(function (x, i) {
          list.appendChild(x);
          x.className = "btn " + (i === 0 ? "btn-primary" : "btn-quiet");
        });
      });
    });
  })();

  // ---------------------------------------------------------------- copy

  $$("[data-copy]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var text = btn.getAttribute("data-copy");
      var done = function () {
        btn.classList.add("done");
        btn.setAttribute("aria-label", data.copy.copied);
        btn.title = data.copy.copied;
        setTimeout(function () {
          btn.classList.remove("done");
          btn.setAttribute("aria-label", data.copy.copy);
          btn.title = data.copy.copy;
        }, 1600);
      };
      if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(text).then(done, function () {});
      } else {
        var ta = document.createElement("textarea");
        ta.value = text;
        ta.style.position = "fixed";
        ta.style.opacity = "0";
        document.body.appendChild(ta);
        ta.select();
        try { document.execCommand("copy"); done(); } catch (e) {}
        ta.remove();
      }
    });
  });

  // ---------------------------------------------------------------- reveal

  var reveals = $$(".reveal");
  if ("IntersectionObserver" in window && !reduced.matches) {
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) {
        if (e.isIntersecting) { e.target.classList.add("in"); io.unobserve(e.target); }
      });
    }, { rootMargin: "0px 0px -8% 0px", threshold: 0.08 });
    reveals.forEach(function (el) { io.observe(el); });
  } else {
    reveals.forEach(function (el) { el.classList.add("in"); });
  }

  // ---------------------------------------------------------------- spotlight

  $$(".spot").forEach(function (el) {
    el.addEventListener("pointermove", function (e) {
      var r = el.getBoundingClientRect();
      el.style.setProperty("--mx", (e.clientX - r.left) + "px");
      el.style.setProperty("--my", (e.clientY - r.top) + "px");
    });
  });

  // ---------------------------------------------------------------- tabs

  // A tablist with roving focus; onSelect(index) shows the panel.
  function tabs(list, onSelect) {
    var items = $$('[role="tab"]', list);
    function select(i, focus) {
      items.forEach(function (t, j) {
        t.setAttribute("aria-selected", String(i === j));
        t.tabIndex = i === j ? 0 : -1;
      });
      if (focus) items[i].focus();
      onSelect(i);
    }
    items.forEach(function (t, i) {
      t.addEventListener("click", function () { select(i, false); list.dispatchEvent(new CustomEvent("picked")); });
    });
    list.addEventListener("keydown", function (e) {
      var i = items.indexOf(document.activeElement);
      if (i < 0) return;
      var n = { ArrowRight: i + 1, ArrowLeft: i - 1, Home: 0, End: items.length - 1 }[e.key];
      if (n === undefined) return;
      e.preventDefault();
      select((n + items.length) % items.length, true);
      list.dispatchEvent(new CustomEvent("picked"));
    });
    return { select: select, count: items.length, current: function () {
      return items.findIndex(function (t) { return t.getAttribute("aria-selected") === "true"; });
    } };
  }

  (function tour() {
    var root = document.querySelector(".tour");
    if (!root) return;
    var shots = $$(".shot", root);
    var list = root.querySelector(".tour-tabs");
    var ms = 7000;
    root.style.setProperty("--tour-ms", ms + "ms");
    shots.forEach(function (s, i) { if (i) s.setAttribute("aria-hidden", "true"); });
    var t = tabs(list, function (i) {
      shots.forEach(function (s, j) {
        s.classList.toggle("on", i === j);
        if (i === j) s.removeAttribute("aria-hidden"); else s.setAttribute("aria-hidden", "true");
      });
      if (shots[i + 1]) { var img = shots[i + 1].querySelector("img"); if (img) img.loading = "eager"; }
    });
    if (reduced.matches) return;
    var timer = null, visible = false, hover = false, stopped = false;
    function schedule() {
      clearTimeout(timer);
      root.classList.remove("playing");
      if (stopped || !visible || hover || document.hidden) { root.classList.toggle("paused", !stopped); return; }
      root.classList.remove("paused");
      void root.offsetWidth; // restart the progress animation
      root.classList.add("playing");
      timer = setTimeout(function () { t.select((t.current() + 1) % t.count, false); schedule(); }, ms);
    }
    list.addEventListener("picked", function () { stopped = true; schedule(); root.classList.remove("paused"); });
    var stage = root.querySelector(".tour-stage");
    stage.addEventListener("pointerenter", function () { hover = true; schedule(); });
    stage.addEventListener("pointerleave", function () { hover = false; schedule(); });
    document.addEventListener("visibilitychange", schedule);
    new IntersectionObserver(function (e) { visible = e[0].isIntersecting; schedule(); }, { threshold: 0.35 }).observe(stage);
  })();

  // The permission modes beside the approval card, which plays each one:
  // Ask waits for Allow or Deny, Review checks, Full access just runs.
  (function modes() {
    var list = document.querySelector(".mode-tabs");
    var card = document.querySelector("[data-approval]");
    if (!list || !card) return;
    var panels = $$(".mode-text");
    var keys = $$('[role="tab"]', list).map(function (b) { return b.getAttribute("data-mode"); });
    var timer = null;
    function step(s) { card.setAttribute("data-step", s); }
    function play() {
      clearTimeout(timer);
      var m = card.getAttribute("data-mode");
      if (m === "ask") step("wait");
      else if (m === "review") { step("check"); timer = setTimeout(function () { step("reviewed"); }, reduced.matches ? 0 : 1200); }
      else step("ran");
    }
    tabs(list, function (i) {
      panels.forEach(function (p, j) { p.hidden = i !== j; });
      card.setAttribute("data-mode", keys[i]);
      play();
    });
    card.querySelector("[data-approval-allow]").addEventListener("click", function () { step("ok"); });
    card.querySelector("[data-approval-deny]").addEventListener("click", function () { step("denied"); });
    card.querySelector("[data-approval-replay]").addEventListener("click", play);
  })();

  // ---------------------------------------------------------------- palette

  // The command palette (⌘K, Ctrl+K elsewhere): pages, appearance,
  // languages and, on the home page, the demo window's actions.
  var paletteAdd = null;
  (function palette() {
    var dlg = document.querySelector("[data-pal]");
    var buttons = $$("[data-palette]");
    if (!dlg || typeof dlg.showModal !== "function") {
      buttons.forEach(function (b) { b.hidden = true; });
      return;
    }
    var input = dlg.querySelector("[data-pal-input]");
    var list = dlg.querySelector("[data-pal-list]");
    var empty = dlg.querySelector("[data-pal-empty]");
    var mac = /Mac|iPhone|iPad/i.test((navigator.userAgentData && navigator.userAgentData.platform) || navigator.platform || navigator.userAgent);
    $$("[data-pal-kbd]").forEach(function (k) { k.textContent = mac ? "⌘K" : "Ctrl K"; });
    var active = -1, uid = 0;

    function options() { return $$('[role="option"]', list).filter(function (o) { return !o.hidden; }); }
    function mark(i, scroll) {
      var opts = options();
      active = opts.length ? (i + opts.length) % opts.length : -1;
      opts.forEach(function (o, j) { o.setAttribute("aria-selected", String(j === active)); });
      $$('[role="option"][hidden]', list).forEach(function (o) { o.setAttribute("aria-selected", "false"); });
      if (active < 0) { input.removeAttribute("aria-activedescendant"); return; }
      var o = opts[active];
      if (!o.id) o.id = "pal-" + (++uid);
      input.setAttribute("aria-activedescendant", o.id);
      if (scroll) o.scrollIntoView({ block: "nearest" });
    }
    function filter() {
      var q = input.value.trim().toLowerCase();
      var group = null, groupShown = 0, shown = 0;
      function close() { if (group) group.hidden = !groupShown; }
      Array.prototype.forEach.call(list.children, function (li) {
        if (li.classList.contains("pal-group")) { close(); group = li; groupShown = 0; return; }
        var hit = !q || li.textContent.toLowerCase().indexOf(q) >= 0;
        li.hidden = !hit;
        if (hit) { shown++; groupShown++; }
      });
      close();
      empty.hidden = shown > 0;
      mark(0, true);
    }
    function open() {
      if (dlg.open) return;
      closeDetails();
      input.value = "";
      filter();
      dlg.showModal();
      input.focus();
    }
    function run(li) {
      if (!li) return;
      dlg.close();
      var mode = li.getAttribute("data-pal-theme");
      if (mode) { store("sshyard-theme", mode === "auto" ? null : mode); applyTheme(mode); return; }
      if (li.hasAttribute("data-lang-pick")) store("sshyard-lang", li.getAttribute("data-lang-pick"));
      if (li._run) { li._run(); return; }
      var href = li.getAttribute("data-href");
      if (href) location.href = href;
    }
    buttons.forEach(function (b) { b.addEventListener("click", open); });
    document.addEventListener("keydown", function (e) {
      if ((mac ? e.metaKey : e.ctrlKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === "k") {
        e.preventDefault();
        if (dlg.open) dlg.close(); else open();
      }
    });
    input.addEventListener("input", filter);
    input.addEventListener("keydown", function (e) {
      if (e.key === "ArrowDown" || e.key === "ArrowUp") { e.preventDefault(); mark(active + (e.key === "ArrowDown" ? 1 : -1), true); }
      else if (e.key === "Enter" && !e.isComposing) { e.preventDefault(); run(options()[active]); }
    });
    list.addEventListener("click", function (e) { run(e.target.closest('[role="option"]')); });
    list.addEventListener("pointermove", function (e) {
      var o = e.target.closest('[role="option"]');
      var i = options().indexOf(o);
      if (i >= 0 && i !== active) mark(i, false);
    });
    dlg.addEventListener("click", function (e) { if (e.target === dlg) dlg.close(); });

    // A group of actions: [{icon: <svg>, label, run}].
    paletteAdd = function (title, items) {
      list.appendChild(Object.assign(document.createElement("li"), { className: "pal-group", textContent: title }))
        .setAttribute("role", "presentation");
      items.forEach(function (it) {
        var li = document.createElement("li");
        li.setAttribute("role", "option");
        li.setAttribute("aria-selected", "false");
        li.append(it.icon, Object.assign(document.createElement("span"), { textContent: it.label }));
        li._run = it.run;
        list.appendChild(li);
      });
    };
  })();

  // ---------------------------------------------------------------- demo

  // The demo window: a small model of the app with hosts, tabs, split
  // terminals over a pretend shell, and the right rail's panes (system
  // monitor, files, AI and snippets). Nothing here talks to a network.
  (function demo() {
    var root = document.querySelector("[data-demo]");
    if (!root || !data.demo) return;
    var T = data.demo;
    var tpl = root.querySelector("template[data-icons]").content;
    var win = root.querySelector(".win");
    var tabsEl = root.querySelector("[data-tabs]");
    var termsEl = root.querySelector("[data-terms]");
    var toolPane = root.querySelector("[data-tool-pane]");
    var input = root.querySelector("[data-term-input]");

    function icon(name) {
      var n = tpl.querySelector('[data-i="' + name + '"] svg');
      return n ? n.cloneNode(true) : document.createTextNode("");
    }
    function fileIcon(name) {
      return (tpl.querySelector('svg[data-fi="' + name + '"]') || tpl.querySelector('svg[data-fi="file"]')).cloneNode(true);
    }
    // h("span", "cls", [child, "text", ...]): text is always text, never markup.
    function h(tag, cls, kids) {
      var el = document.createElement(tag);
      if (cls) el.className = cls;
      if (kids != null) (Array.isArray(kids) ? kids : [kids]).forEach(function (k) { if (k != null) el.append(k); });
      return el;
    }
    function btn(name, label, onClick, cls) {
      var b = h("button", "wbtn" + (cls ? " " + cls : ""), icon(name));
      b.type = "button";
      b.setAttribute("aria-label", label);
      b.title = label;
      if (onClick) b.addEventListener("click", onClick);
      return b;
    }
    function fill(s, v) { return String(s).replace(/\{(\w+)\}/g, function (m, k) { return k in v ? v[k] : m; }); }
    function clamp(x, a, b) { return Math.max(a, Math.min(b, x)); }
    function pad(s, n) { s = String(s); return s.length >= n ? s : s + " ".repeat(n - s.length); }
    function lpad(s, n) { s = String(s); return s.length >= n ? s : " ".repeat(n - s.length) + s; }
    function mib(mb) { return mb >= 1024 ? (mb / 1024).toFixed(1) + " GiB" : Math.round(mb) + " MiB"; }
    function human(n) {
      if (n == null) return "";
      if (n < 1024) return n + " B";
      if (n < 1048576) return (n / 1024).toFixed(1) + " KB";
      return (n / 1048576).toFixed(1) + " MB";
    }
    function rate(b) { return b < 1024 ? Math.round(b) + "B/s" : (b / 1024).toFixed(1) + "K/s"; }

    // ------------------------------------------------------------ hosts

    var SERVICES = {
      nginx: "A high performance web server and a reverse proxy server",
      "php8.3-fpm": "The PHP 8.3 FastCGI Process Manager",
      ssh: "OpenBSD Secure Shell server",
      docker: "Docker Application Container Engine",
      postgresql: "PostgreSQL RDBMS",
      pgbouncer: "connection pooler for PostgreSQL",
      "redis-server": "Advanced key-value store",
      fail2ban: "Fail2Ban Service",
    };
    var HOSTS = [
      { id: "web-01", tint: "green", user: "deploy", addr: "10.0.1.12", os: "Ubuntu 24.04.1 LTS", kernel: "6.8.0-49-generic",
        cores: 2, mem: 1640, used: 1090, swap: 1024, disk: { size: "40G", used: "13G", avail: "25G", pct: 33 }, lat: 14, cpu: 3,
        up: "21 days,  3:02", load: [0.20, 0.10, 0.10], ifaces: ["eth0", "docker0", "br-3f61589b1e"],
        services: ["nginx", "php8.3-fpm", "ssh", "docker"],
        procs: [["www-data", 1412, "php-fpm8.3", 212], ["www-data", 905, "nginx", 48], ["root", 655, "dockerd", 41], ["root", 1, "systemd", 12], ["redis", 1720, "redis-server", 9.6]],
        containers: [["3f2a9c1b7d40", "redis:7-alpine", "\"docker-entrypoint.s…\"", "2 weeks ago", "Up 2 weeks", "6379/tcp", "app-redis"],
          ["9b1e04c2aa17", "prom/node-exporter:v1.8.2", "\"/bin/node_exporter\"", "2 weeks ago", "Up 2 weeks", "9100/tcp", "node-exporter"]] },
      { id: "db-primary", tint: "orange", user: "postgres", home: "/var/lib/postgresql", addr: "10.0.2.5", os: "Debian GNU/Linux 12 (bookworm)", kernel: "6.1.0-27-amd64",
        cores: 4, mem: 7980, used: 4710, swap: 2048, disk: { size: "200G", used: "86G", avail: "104G", pct: 46 }, lat: 9, cpu: 12,
        up: "64 days, 11:47", load: [0.48, 0.52, 0.41], ifaces: ["eth0"],
        services: ["postgresql", "pgbouncer", "ssh"],
        procs: [["postgres", 812, "postgres", 2150], ["postgres", 1530, "pgbouncer", 64], ["root", 702, "node_exporter", 22], ["root", 1, "systemd", 11]] },
      { id: "staging", tint: "teal", user: "ubuntu", addr: "10.0.3.7", os: "Ubuntu 22.04.5 LTS", kernel: "5.15.0-126-generic",
        cores: 2, mem: 3890, used: 1420, swap: 0, disk: { size: "80G", used: "31G", avail: "46G", pct: 41 }, lat: 22, cpu: 6,
        up: "5 days,  7:15", load: [0.12, 0.08, 0.05], ifaces: ["eth0", "docker0"],
        services: ["nginx", "docker", "ssh", "redis-server"],
        procs: [["ubuntu", 2210, "node", 410], ["redis", 990, "redis-server", 96], ["root", 640, "dockerd", 58], ["root", 1, "systemd", 10]],
        containers: [["c41d9e0f22ab", "app-web:staging", "\"node server.js\"", "5 days ago", "Up 5 days", "0.0.0.0:3000->3000/tcp", "app-web"],
          ["71aa3be95c08", "postgres:16-alpine", "\"docker-entrypoint.s…\"", "5 days ago", "Up 5 days", "5432/tcp", "app-db"]] },
      { id: "bastion", tint: "purple", user: "ops", addr: "203.0.113.10", os: "Debian GNU/Linux 12 (bookworm)", kernel: "6.1.0-27-cloud-amd64",
        cores: 1, mem: 480, used: 160, swap: 0, disk: { size: "10G", used: "2.1G", avail: "7.4G", pct: 22 }, lat: 31, cpu: 1,
        up: "112 days,  2:09", load: [0.01, 0.02, 0.00], ifaces: ["ens3"],
        services: ["ssh", "fail2ban"],
        procs: [["root", 488, "fail2ban-server", 31], ["root", 512, "sshd", 9.1], ["root", 1, "systemd", 9]] },
    ];
    var byId = {};
    HOSTS.forEach(function (x) {
      byId[x.id] = x;
      x.home = x.home || "/home/" + x.user;
      x.sessions = 0;
      x.stat = { cpu: x.cpu, used: x.used, up: 400, down: 900, lat: x.lat, cpuHist: [], netHist: [] };
      for (var i = 0; i < 40; i++) { x.stat.cpuHist.push(x.cpu); x.stat.netHist.push(0); }
      x.fs = makeFS(x);
      x.filesCwd = x.home;
    });
    var web = byId["web-01"];

    // ------------------------------------------------------------ files

    function D(c) { return { d: true, c: c || {} }; }
    function F(body, extra) { var f = { d: false, body: body }; if (extra) Object.assign(f, extra); return f; }
    function bodyOf(f) { return typeof f.body === "function" ? f.body() : f.body; }
    function sizeOf(f) { if (f.size != null) return f.size; var b = bodyOf(f); return b == null ? 0 : new Blob([b]).size; }
    function put(tree, path, node) {
      var parts = path.split("/").filter(Boolean), dir = tree;
      for (var i = 0; i < parts.length - 1; i++) {
        if (!dir.c[parts[i]]) dir.c[parts[i]] = D();
        dir = dir.c[parts[i]];
      }
      dir.c[parts[parts.length - 1]] = node;
    }
    function makeFS(x) {
      var t = D();
      ["bin", "boot", "dev", "opt", "proc", "root", "run", "srv", "tmp", "usr/bin", "usr/lib", "usr/local", "var/lib", "var/cache"].forEach(function (p) { put(t, p, D()); });
      put(t, "etc/hostname", F(x.id + "\n"));
      put(t, "etc/hosts", F("127.0.0.1 localhost\n" + x.addr + " " + x.id + "\n"));
      put(t, "etc/os-release", F('PRETTY_NAME="' + x.os + '"\nHOME_URL="https://www.' + (/Ubuntu/.test(x.os) ? "ubuntu.com" : "debian.org") + '/"\n'));
      put(t, "etc/ssh/sshd_config", F("Port 22\nPermitRootLogin no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\n"));
      put(t, "var/log/syslog", F("Nov 20 09:30:01 " + x.id + " CRON[20511]: (root) CMD (command -v debian-sa1 > /dev/null && debian-sa1 1 1)\nNov 20 09:35:12 " + x.id + " systemd[1]: Started session-412.scope - Session 412 of User " + x.user + ".\n"));
      put(t, "var/log/auth.log", F("Nov 20 09:35:12 " + x.id + " sshd[20644]: Accepted publickey for " + x.user + " from 10.0.0.8 port 52814 ssh2: ED25519 SHA256:q3W9…\n"));
      put(t, x.home + "/.bashrc", F("# ~/.bashrc\nexport EDITOR=vim\nalias ll='ls -alF'\nalias gs='git status'\n"));
      put(t, x.home + "/.profile", F("if [ -n \"$BASH_VERSION\" ]; then\n  . \"$HOME/.bashrc\"\nfi\n"));
      if (x.services.indexOf("nginx") >= 0) {
        put(t, "etc/nginx/nginx.conf", F("user www-data;\nworker_processes auto;\n\nevents {\n    worker_connections 768;\n}\n\nhttp {\n    sendfile on;\n    include /etc/nginx/sites-enabled/*;\n}\n"));
        put(t, "var/log/nginx/access.log", F('10.0.0.8 - - [20/Nov/2026:09:39:58 +0000] "GET / HTTP/1.1" 200 5120 "-" "curl/8.5.0"\n10.0.4.2 - - [20/Nov/2026:09:40:02 +0000] "GET /health HTTP/1.1" 200 2 "-" "kube-probe/1.31"\n'));
      }
      if (x.id === "web-01") {
        put(t, "etc/nginx/sites-enabled/app.conf", F("server {\n    listen 80;\n    root /var/www/app/public;\n    index index.php;\n\n    location ~ \\.php$ {\n        include fastcgi_params;\n        fastcgi_pass unix:/run/php/php8.3-fpm.sock;\n    }\n}\n"));
        put(t, "etc/php/8.3/fpm/pool.d/www.conf", F(function () {
          return "[www]\nuser = www-data\ngroup = www-data\nlisten = /run/php/php" + (x.fixed ? "8.3" : "8.2") + "-fpm.sock\npm = dynamic\npm.max_children = 8\n";
        }));
        put(t, "var/log/nginx/error.log", F("2026/11/20 09:31:07 [crit] 905#905: *118 connect() to unix:/run/php/php8.3-fpm.sock failed (2: No such file or directory) while connecting to upstream\n"));
        put(t, "var/www/app/public/index.php", F("<?php\n\nrequire __DIR__.'/../vendor/autoload.php';\n\n(require_once __DIR__.'/../bootstrap/app.php')\n    ->handleRequest(Illuminate\\Http\\Request::capture());\n"));
        put(t, "var/www/app/public/favicon.ico", F(null, { size: 15086 }));
        put(t, "var/www/app/composer.json", F('{\n    "name": "acme/app",\n    "require": {\n        "php": "^8.3",\n        "laravel/framework": "^12.0"\n    }\n}\n'));
        put(t, "var/www/app/composer.lock", F(null, { size: 301442 }));
        put(t, "var/www/app/storage/logs/app.log", F("[2026-11-20 09:31:05] production.INFO: Deployed release 2026-11-20-0930\n"));
        put(t, "var/www/app/README.md", F("# acme/app\n\nDeployed to /var/www/app by ~/deploy.sh.\n"));
        put(t, x.home + "/deploy.sh", F("#!/usr/bin/env bash\nset -euo pipefail\ncd /var/www/app\ngit pull --ff-only\ncomposer install --no-dev -o\nsudo systemctl reload php8.3-fpm\n", { x: true }));
        put(t, x.home + "/releases/2026-11-18-1410", D());
        put(t, x.home + "/releases/2026-11-20-0930", D());
      }
      if (x.id === "db-primary") {
        put(t, "etc/postgresql/16/main/postgresql.conf", F("listen_addresses = '10.0.2.5'\nmax_connections = 200\nshared_buffers = 2GB\nwal_level = replica\n"));
        put(t, "etc/postgresql/16/main/pg_hba.conf", F("# TYPE  DATABASE  USER  ADDRESS      METHOD\nhost    all       all   10.0.0.0/16  scram-sha-256\n"));
        put(t, "var/log/postgresql/postgresql-16-main.log", F("2026-11-20 09:00:00.012 UTC [812] LOG:  checkpoint starting: time\n2026-11-20 09:02:41.330 UTC [812] LOG:  checkpoint complete: wrote 1873 buffers (0.7%)\n"));
        put(t, x.home + "/16/main", D());
        put(t, x.home + "/backup.sh", F("#!/bin/sh\npg_dump -Fc app > /var/backups/app-$(date +%F).dump\n", { x: true }));
        put(t, "var/backups/app-2026-11-19.dump", F(null, { size: 48213004 }));
        put(t, "var/backups/app-2026-11-20.dump", F(null, { size: 48377196 }));
      }
      if (x.id === "staging") {
        put(t, "etc/nginx/sites-enabled/staging.conf", F("server {\n    listen 80;\n    location / {\n        proxy_pass http://127.0.0.1:3000;\n    }\n}\n"));
        put(t, x.home + "/app/package.json", F('{\n  "name": "app-web",\n  "version": "2.4.0",\n  "scripts": { "start": "node server.js" }\n}\n'));
        put(t, x.home + "/app/server.js", F("import http from 'node:http';\n\nhttp.createServer((req, res) => res.end('ok')).listen(3000);\n"));
        put(t, x.home + "/app/docker-compose.yml", F("services:\n  web:\n    image: app-web:staging\n    ports: [\"3000:3000\"]\n  db:\n    image: postgres:16-alpine\n"));
        put(t, x.home + "/app/src/index.js", F("export * from './routes.js';\n"));
        put(t, x.home + "/app/README.md", F("# app-web\n\nStaging deploys from the main branch.\n"));
      }
      if (x.id === "bastion") {
        put(t, x.home + "/.ssh/authorized_keys", F("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK7v… ops@laptop\n"));
        put(t, x.home + "/.ssh/config", F("Host *.internal\n    ProxyJump bastion\n"));
        put(t, x.home + "/README.md", F("# bastion\n\nJump host for 10.0.0.0/16. Use ProxyJump; nothing else runs here.\n"));
        put(t, "etc/fail2ban/jail.local", F("[sshd]\nenabled = true\nmaxretry = 5\nbantime = 1h\n"));
      }
      return t;
    }
    // An absolute, normalised path for p seen from cwd.
    function resolve(x, cwd, p) {
      if (!p || p === "~") return x.home;
      if (p.indexOf("~/") === 0) p = x.home + p.slice(1);
      var parts = (p[0] === "/" ? p : cwd + "/" + p).split("/"), out = [];
      parts.forEach(function (s) {
        if (!s || s === ".") return;
        if (s === "..") out.pop(); else out.push(s);
      });
      return "/" + out.join("/");
    }
    function lookup(x, path) {
      var node = x.fs;
      var parts = path.split("/").filter(Boolean);
      for (var i = 0; i < parts.length; i++) {
        if (!node.d || !Object.prototype.hasOwnProperty.call(node.c, parts[i])) return null;
        node = node.c[parts[i]];
      }
      return node;
    }
    function parentOf(path) { var i = path.lastIndexOf("/"); return i <= 0 ? "/" : path.slice(0, i); }
    function baseOf(path) { return path.slice(path.lastIndexOf("/") + 1); }
    function entries(dir) {
      return Object.keys(dir.c).sort(function (a, b) {
        return a.replace(/^\./, "").localeCompare(b.replace(/^\./, ""));
      });
    }
    function tilde(x, path) { return path === x.home ? "~" : path.indexOf(x.home + "/") === 0 ? "~" + path.slice(x.home.length) : path; }
    function owner(x, path) { return path.indexOf(x.home) === 0 ? x.user : path.indexOf("/var/www") === 0 ? "www-data" : "root"; }

    var DIR_ICONS = { etc: "folder-config", config: "folder-config", log: "folder-log", logs: "folder-log", public: "folder-public",
      src: "folder-src", tmp: "folder-temp", lib: "folder-lib", backups: "folder-database", postgresql: "folder-database" };
    var EXT_ICONS = { conf: "settings", cnf: "settings", ini: "settings", env: "settings", log: "log", php: "php", json: "json",
      sh: "console", bash: "console", md: "markdown", html: "html", htm: "html", yml: "yaml", yaml: "yaml", png: "image",
      jpg: "image", jpeg: "image", gif: "image", svg: "image", webp: "image", ico: "image", zip: "zip", gz: "zip", tgz: "zip",
      tar: "zip", sql: "database", dump: "database", db: "database", js: "javascript", mjs: "javascript", css: "css",
      pdf: "pdf", py: "python", go: "go", pem: "certificate", crt: "certificate", txt: "document", lock: "lock" };
    function fileIconName(name, dir) {
      if (dir) return DIR_ICONS[name] || "folder";
      if (/^(docker-compose\.ya?ml|Dockerfile)$/.test(name)) return "docker";
      if (name === "nginx.conf" || /\.conf$/.test(name) && /nginx|sites-/.test(name)) return "nginx";
      if (name[0] === "." && name.indexOf(".", 1) < 0) return "console";
      var ext = name.slice(name.lastIndexOf(".") + 1).toLowerCase();
      return name.indexOf(".") >= 0 && EXT_ICONS[ext] || "file";
    }

    // ------------------------------------------------------------ terminal

    var tabs = [];
    var current = null;
    var focused = false; // the hidden input has the keyboard
    var follow = true;

    function tile(x) { var t = h("span", "tile", x.id[0]); t.style.setProperty("--tint", "var(--tint-" + x.tint + ")"); return t; }

    // A terminal session; headless ones (the assistant's commands) only collect output.
    function Session(x, el) {
      this.host = x;
      this.cwd = x.home;
      this.old = x.home;
      this.hist = [];
      this.hi = 0;
      this.buf = "";
      this.caret = 0;
      this.busy = false;
      this.state = "connecting";
      if (el === false) { this.lines = []; this.state = "ready"; return; }
      this.el = el || h("section", "pane term", [h("div", "pane-head"), h("pre", "term-out"), h("div", "term-stats")]);
      this.el.setAttribute("data-pane", "");
      this.out = this.el.querySelector(".term-out");
      this.out.textContent = "";
      this.head = this.el.querySelector(".pane-head");
      this.buildHead();
      this.buildStats();
      var s = this;
      this.el.addEventListener("pointerdown", function () { s.pressed = true; });
      this.el.addEventListener("click", function (e) {
        if (e.target.closest(".pane-icons")) return;
        var sel = window.getSelection && window.getSelection();
        if (sel && !sel.isCollapsed && s.out.contains(sel.anchorNode)) return;
        focusSession(s, true);
      });
    }
    Session.prototype.buildHead = function () {
      var s = this, x = this.host;
      this.head.textContent = "";
      this.dot = h("i", "dot");
      this.head.append(this.dot, tile(x), h("strong", null, x.id), h("span", "muted", x.user + "@" + x.addr),
        h("span", "pane-icons", [
          btn("folder-3-line", T.tools.files, function () { focusSession(s, false); x.filesCwd = s.cwd; x.preview = null; setTool("files", true); }),
          this.splitBtn = btn("layout-column-line", T.split, function () { split(s); }),
          btn("close-line", T.close_pane, function () { closePane(s); }),
        ]));
    };
    Session.prototype.buildStats = function () {
      var bar = this.el.querySelector(".term-stats");
      bar.textContent = "";
      this.st = {};
      var s = this;
      [["cpu", "cpu-line"], ["mem", "ram-2-line"], ["disk", "hard-drive-2-line"], ["up", "arrow-up-line"], ["down", "arrow-down-line"], ["lat", "pulse-line"]].forEach(function (k) {
        var b = h("b");
        s.st[k[0]] = b;
        bar.append(h("span", k[0] === "lat" ? "lat" : null, [icon(k[1]), b]));
      });
      this.paintStats();
    };
    Session.prototype.paintStats = function () {
      if (!this.st) return;
      var x = this.host, st = x.stat, on = this.state === "ready";
      this.st.cpu.textContent = on ? Math.round(st.cpu) + "%" : "–";
      this.st.mem.textContent = on ? (st.used / 1024).toFixed(1) + "G/" + (x.mem / 1024).toFixed(1) + "G" : "–";
      this.st.disk.textContent = on ? x.disk.pct + "%" : "–";
      this.st.up.textContent = on ? rate(st.up) : "–";
      this.st.down.textContent = on ? rate(st.down) : "–";
      this.st.lat.textContent = on ? Math.round(st.lat) + " ms" : "–";
      this.el.classList.toggle("off", !on);
    };
    Session.prototype.promptSegs = function () {
      return [[this.host.user + "@" + this.host.id, "t-ok"], [":"], [tilde(this.host, this.cwd), "t-blue"], ["$ "]];
    };
    function segsNode(cls, segs) {
      var l = h("span", "tl" + (cls ? " " + cls : ""));
      segs.forEach(function (sg) { l.append(sg[1] ? h("span", sg[1], sg[0]) : sg[0]); });
      return l;
    }
    // A line is a string or a list of [text, class] segments.
    Session.prototype.print = function (line, cls) {
      if (this.lines) { this.lines.push(typeof line === "string" ? line : line.map(function (s) { return s[0]; }).join("")); return; }
      var l = segsNode(cls, typeof line === "string" ? [[line]] : line);
      if (this.live && this.live.parentNode) this.out.insertBefore(l, this.live); else this.out.appendChild(l);
      while (this.out.childNodes.length > 400) this.out.removeChild(this.out.firstChild);
      this.scroll();
    };
    // How many monospace cells fit on a line of this terminal.
    Session.prototype.cols = function () {
      if (this.lines || !this.out.clientWidth) return 80;
      var cs = getComputedStyle(this.out);
      return Math.floor((this.out.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight)) / (parseFloat(cs.fontSize) * 0.6));
    };
    Session.prototype.scroll = function () { this.out.scrollTop = this.out.scrollHeight; };
    Session.prototype.showLive = function () {
      if (this.lines) return;
      if (!this.live) {
        this.live = h("span", "tl live");
        this.livePrompt = h("span");
        this.liveA = h("span", "cmd");
        this.cursor = h("i", "cursor");
        this.liveB = h("span", "cmd");
        this.live.append(this.livePrompt, this.liveA, this.cursor, this.liveB);
      }
      this.livePrompt.textContent = "";
      this.livePrompt.append.apply(this.livePrompt, segsNode("", this.promptSegs()).childNodes);
      this.out.appendChild(this.live);
      this.render();
    };
    Session.prototype.hideLive = function () { if (this.live && this.live.parentNode) this.live.remove(); };
    Session.prototype.render = function () {
      if (!this.live) return;
      var c = clamp(this.caret, 0, this.buf.length);
      this.liveA.textContent = this.buf.slice(0, c);
      this.liveB.textContent = this.buf.slice(c);
      this.scroll();
    };
    Session.prototype.echo = function (line) { this.print(this.promptSegs().concat([[line, "cmd"]])); };
    Session.prototype.clear = function () {
      if (this.lines) { this.lines = []; return; }
      var live = this.live && this.live.parentNode ? this.live : null;
      this.out.textContent = "";
      if (live) this.out.appendChild(live);
    };
    Session.prototype.setBuf = function (text, caret) {
      this.buf = text;
      this.caret = caret == null ? text.length : caret;
      if (this === sess() && input.value !== text) { input.value = text; try { input.setSelectionRange(this.caret, this.caret); } catch (e) {} }
      this.render();
    };

    function sess() { return current ? current.panes[current.focus] : null; }

    async function connect(s, again) {
      var x = s.host;
      s.state = "connecting";
      s.paintStats();
      s.print(fill(T.connecting, { host: x.id }), "t-dim");
      await sleep(again ? 450 : 350 + x.lat * 12);
      if (s.closed) return;
      s.state = "ready";
      x.connectedAt = x.connectedAt || Date.now();
      s.print("Welcome to " + x.os + " (GNU/Linux " + x.kernel + " x86_64)", "t-dim");
      s.print("Last login: " + new Date(Date.now() - 3600e3 * 19).toUTCString().replace(/ GMT$/, "") + " from 10.0.0.8", "t-dim");
      s.paintStats();
      s.showLive();
      paintChrome();
    }
    function disconnect(s) {
      s.state = "closed";
      s.host.sessions--;
      s.hideLive();
      s.print("Connection to " + s.host.addr + " closed.");
      s.print(T.disconnected, "t-dim");
      s.paintStats();
      paintChrome();
    }

    // ------------------------------------------------------------ shell

    function tokens(text) {
      var out = [], cur = "", q = null, any = false;
      for (var i = 0; i < text.length; i++) {
        var c = text[i];
        if (q) { if (c === q) q = null; else cur += c; continue; }
        if (c === "'" || c === '"') { q = c; any = true; continue; }
        if (/\s/.test(c)) { if (cur || any) out.push(cur); cur = ""; any = false; continue; }
        cur += c;
      }
      if (cur || any) out.push(cur);
      return out;
    }
    function flags(argv) {
      var f = {}, rest = [];
      argv.forEach(function (a) {
        if (/^--/.test(a)) f[a.slice(2)] = true;
        else if (/^-[a-zA-Z]+$/.test(a)) a.slice(1).split("").forEach(function (c) { f[c] = true; });
        else rest.push(a);
      });
      return { f: f, args: rest };
    }
    function nArg(argv, dflt) {
      for (var i = 0; i < argv.length; i++) {
        if (argv[i] === "-n" && argv[i + 1]) return +argv[i + 1];
        if (/^-n?\d+$/.test(argv[i])) return +argv[i].replace(/\D/g, "");
      }
      return dflt;
    }
    function textOf(line) { return typeof line === "string" ? line : line.map(function (s) { return s[0]; }).join(""); }
    function readArgs(s, argv, name) {
      var files = argv.filter(function (a) { return a[0] !== "-" && !/^\d+$/.test(a); });
      var lines = [], err = [];
      files.forEach(function (a) {
        var p = resolve(s.host, s.cwd, a), n = lookup(s.host, p);
        if (!n) err.push(name + ": " + a + ": No such file or directory");
        else if (n.d) err.push(name + ": " + a + ": Is a directory");
        else { var b = bodyOf(n); lines = lines.concat(b == null ? [] : b.replace(/\n$/, "").split("\n")); }
      });
      return { files: files.length, lines: lines, err: err };
    }
    function table(rows) {
      var w = [];
      rows.forEach(function (r) { r.forEach(function (c, i) { w[i] = Math.max(w[i] || 0, String(c).length); }); });
      return rows.map(function (r) { return r.map(function (c, i) { return i === r.length - 1 ? c : pad(c, w[i] + 3); }).join("").replace(/\s+$/, ""); });
    }
    function nowUTC() { return new Date().toUTCString().slice(17, 25); }

    var COMMANDS = {
      help: function (c) {
        c.print(T.shell.help);
        var names = Object.keys(COMMANDS).filter(function (n) { return !ALIASES[n]; }).sort();
        var per = Math.max(1, Math.floor(c.s.cols() / 11));
        for (var i = 0; i < names.length; i += per) c.print(names.slice(i, i + per).map(function (n) { return pad(n, 11); }).join("").trim(), "t-blue");
        c.print(T.shell.keys, "t-dim");
      },
      clear: function (c) { c.s.clear(); },
      pwd: function (c) { return [c.s.cwd]; },
      whoami: function (c) { return [c.x.user]; },
      hostname: function (c) { return [c.x.id]; },
      id: function (c) { var u = c.x.user; return ["uid=1000(" + u + ") gid=1000(" + u + ") groups=1000(" + u + "),27(sudo)"]; },
      nproc: function (c) { return [String(c.x.cores)]; },
      uname: function (c) {
        return [c.argv.indexOf("-a") >= 0 ? "Linux " + c.x.id + " " + c.x.kernel + " #1 SMP PREEMPT_DYNAMIC x86_64 GNU/Linux" : c.argv.indexOf("-r") >= 0 ? c.x.kernel : "Linux"];
      },
      date: function () { return [new Date().toUTCString().replace(/^(\w+), (\d+) (\w+) (\d+) ([\d:]+) GMT$/, "$1 $3 $2 $5 UTC $4")]; },
      uptime: function (c) {
        var l = c.x.stat.load || c.x.load;
        return [" " + nowUTC() + " up " + c.x.up + ",  1 user,  load average: " + l.map(function (v) { return v.toFixed(2); }).join(", ")];
      },
      echo: function (c) { return [c.argv.join(" ")]; },
      history: function (c) { return c.s.hist.map(function (l, i) { return lpad(i + 1, 5) + "  " + l; }); },
      cd: function (c) {
        var a = c.argv[0];
        var p = a === "-" ? c.s.old : resolve(c.x, c.s.cwd, a);
        var n = lookup(c.x, p);
        if (!n) return { lines: ["bash: cd: " + a + ": No such file or directory"], code: 1 };
        if (!n.d) return { lines: ["bash: cd: " + a + ": Not a directory"], code: 1 };
        c.s.old = c.s.cwd;
        c.s.cwd = p;
        if (a === "-") c.print(tilde(c.x, p));
        if (follow && !c.s.lines && c.s === sess()) { c.x.filesCwd = p; c.x.preview = null; if (tool === "files") renderTool(); }
      },
      ls: function (c) {
        var o = flags(c.argv), long = o.f.l, all = o.f.a || o.f.A, out = [], code = 0;
        var paths = o.args.length ? o.args : ["."];
        paths.forEach(function (a, k) {
          var p = resolve(c.x, c.s.cwd, a), n = lookup(c.x, p);
          if (!n) { out.push("ls: cannot access '" + a + "': No such file or directory"); code = 2; return; }
          var items = n.d ? entries(n).filter(function (e) { return all || e[0] !== "."; }).map(function (e) { return [e, n.c[e], p + "/" + e]; }) : [[a, n, p]];
          if (n.d && o.f.a) items.unshift([".", n, p], ["..", lookup(c.x, parentOf(p)) || n, parentOf(p)]);
          if (paths.length > 1) { if (k) out.push(""); out.push(a + ":"); }
          function seg(e) { return [e[0], e[1].d ? "t-blue" : e[1].x ? "t-ok" : ""]; }
          if (!long) {
            var segs = [];
            items.forEach(function (e, i) { if (i) segs.push(["  "]); segs.push(seg(e)); });
            if (segs.length) out.push(segs);
            return;
          }
          if (n.d) out.push("total " + items.length * 4);
          items.forEach(function (e) {
            var who = owner(c.x, e[2]);
            var size = e[1].d ? 4096 : sizeOf(e[1]);
            out.push([[(e[1].d ? "drwxr-xr-x" : e[1].x ? "-rwxr-xr-x" : "-rw-r--r--") + " " + lpad(e[1].d ? 2 : 1, 2) + " " + pad(who, 8) + " " + pad(who, 8) + " " +
              lpad(o.f.h ? human(size).replace(/ B$/, "").replace(/\.\d KB$/, "K").replace(/ KB$/, "K").replace(/ MB$/, "M") : size, 8) + " Nov 20 09:31 "], seg(e)]);
          });
        });
        return { lines: out, code: code };
      },
      cat: function (c) {
        if (!c.argv.length) return c.stdin || [];
        var r = readArgs(c.s, c.argv, "cat");
        return { lines: r.err.concat(r.lines), code: r.err.length ? 1 : 0 };
      },
      head: function (c) { var r = readArgs(c.s, c.argv, "head"); return { lines: r.err.concat((r.files ? r.lines : c.stdin || []).slice(0, nArg(c.argv, 10))) }; },
      tail: function (c) { var r = readArgs(c.s, c.argv, "tail"), src = r.files ? r.lines : c.stdin || []; return { lines: r.err.concat(src.slice(-nArg(c.argv, 10))) }; },
      grep: function (c) {
        var o = flags(c.argv), pat = o.args[0] || "", re;
        try { re = new RegExp(pat, o.f.i ? "i" : ""); } catch (e) { return { lines: ["grep: Invalid regular expression"], code: 2 }; }
        var src = o.args.length > 1 ? readArgs(c.s, o.args.slice(1), "grep").lines : c.stdin || [];
        var hit = src.filter(function (l) { return re.test(textOf(l)) !== !!o.f.v; });
        return { lines: hit, code: hit.length ? 0 : 1 };
      },
      wc: function (c) {
        var o = flags(c.argv);
        var r = o.args.length ? readArgs(c.s, c.argv, "wc") : { lines: c.stdin || [], err: [] };
        if (r.err.length) return r.err;
        var src = r.lines.map(textOf);
        var text = src.length ? src.join("\n") + "\n" : "";
        var n = { l: src.length, w: text.split(/\s+/).filter(Boolean).length, c: new TextEncoder().encode(text).length };
        var pick = ["l", "w", "c"].filter(function (k) { return o.f[k]; });
        return [(pick.length ? pick : ["l", "w", "c"]).map(function (k) { return pick.length === 1 ? String(n[k]) : lpad(String(n[k]), 7); }).join(" ").trim()];
      },
      sort: function (c) { return (c.stdin || []).map(textOf).sort(); },
      mkdir: function (c) {
        var o = flags(c.argv), out = [];
        o.args.forEach(function (a) {
          var p = resolve(c.x, c.s.cwd, a);
          if (lookup(c.x, p)) { if (!o.f.p) out.push("mkdir: cannot create directory '" + a + "': File exists"); return; }
          if (!o.f.p && !lookup(c.x, parentOf(p))) { out.push("mkdir: cannot create directory '" + a + "': No such file or directory"); return; }
          put(c.x.fs, p, D());
        });
        changed(c.x);
        return { lines: out, code: out.length ? 1 : 0 };
      },
      touch: function (c) {
        c.argv.forEach(function (a) {
          var p = resolve(c.x, c.s.cwd, a);
          if (!lookup(c.x, p) && lookup(c.x, parentOf(p))) put(c.x.fs, p, F(""));
        });
        changed(c.x);
      },
      rm: function (c) {
        var o = flags(c.argv), out = [];
        o.args.forEach(function (a) {
          var p = resolve(c.x, c.s.cwd, a), n = lookup(c.x, p);
          if (p === "/") { out.push("rm: it is dangerous to operate recursively on '/'"); return; }
          if (!n) { if (!o.f.f) out.push("rm: cannot remove '" + a + "': No such file or directory"); return; }
          if (n.d && !o.f.r && !o.f.R) { out.push("rm: cannot remove '" + a + "': Is a directory"); return; }
          delete lookup(c.x, parentOf(p)).c[baseOf(p)];
        });
        changed(c.x);
        return { lines: out, code: out.length ? 1 : 0 };
      },
      df: function (c) {
        var d = c.x.disk, hm = c.argv.join(" ").indexOf("h") >= 0;
        var rows = [["Filesystem", "Size", "Used", "Avail", "Use%", "Mounted on"], ["/dev/vda1", d.size, d.used, d.avail, d.pct + "%", "/"]];
        if (!/\s\//.test(" " + c.argv.join(" "))) rows.push(["tmpfs", hm ? Math.round(c.x.mem / 2) + "M" : String(c.x.mem * 512), "0", hm ? Math.round(c.x.mem / 2) + "M" : String(c.x.mem * 512), "0%", "/dev/shm"]);
        if (!hm) rows[1] = rows[1].map(function (v, i) { return i >= 1 && i <= 3 ? String(Math.round(parseFloat(v) * 1048576)) : v; });
        return table(rows);
      },
      free: function (c) {
        var x = c.x, used = x.stat.used, cache = Math.round(x.mem * 0.38), free = Math.max(0, x.mem - used - cache);
        var g = function (mb) { return c.argv.indexOf("-h") >= 0 ? (mb >= 1024 ? (mb / 1024).toFixed(1) + "Gi" : mb ? Math.round(mb) + "Mi" : "0B") : String(Math.round(mb * 1024)); };
        var rows = [["", "total", "used", "free", "shared", "buff/cache", "available"],
          ["Mem:", g(x.mem), g(used), g(free), g(12), g(cache), g(x.mem - used)],
          ["Swap:", g(x.swap), g(0), g(x.swap)]];
        return rows.map(function (r) { return r.map(function (v, i) { return i ? lpad(v, 12) : pad(v, 4); }).join("").replace(/\s+$/, ""); });
      },
      ps: function (c) {
        var x = c.x, p = x.procs.slice().sort(function (a, b) { return b[3] - a[3]; });
        if (/comm,rss/.test(c.argv.join(" "))) return ["COMMAND           RSS"].concat(p.map(function (r) { return pad(r[2], 15) + lpad(Math.round(r[3] * 1024), 8); }));
        return ["USER         PID %CPU %MEM    VSZ   RSS TTY      STAT START   TIME COMMAND"].concat(p.map(function (r, i) {
          return pad(r[0], 9) + lpad(r[1], 7) + lpad((i ? 0.1 : x.stat.cpu / 3).toFixed(1), 5) + lpad((r[3] / x.mem * 100).toFixed(1), 5) +
            lpad(Math.round(r[3] * 1024 * 2.4), 7) + lpad(Math.round(r[3] * 1024), 6) + " ?        Ss   Nov02   " + lpad((i * 7 + 3) % 60, 2) + ":" + lpad(i * 13 % 60, 2).replace(/ /g, "0") + " " + r[2];
        }));
      },
      top: function (c) {
        var x = c.x, st = x.stat;
        var out = [
          "top - " + nowUTC() + " up " + x.up + ",  1 user,  load average: " + (st.load || x.load).map(function (v) { return v.toFixed(2); }).join(", "),
          "Tasks: " + (90 + x.procs.length * 7) + " total,   1 running, " + (89 + x.procs.length * 7) + " sleeping,   0 stopped,   0 zombie",
          "%Cpu(s): " + lpad(st.cpu.toFixed(1), 4) + " us,  0.7 sy,  0.0 ni, " + (99.3 - st.cpu).toFixed(1) + " id,  0.0 wa",
          "MiB Mem : " + lpad(x.mem.toFixed(1), 8) + " total, " + lpad((x.mem - st.used - x.mem * 0.38).toFixed(1), 8) + " free, " + lpad(st.used.toFixed(1), 8) + " used, " + lpad((x.mem * 0.38).toFixed(1), 8) + " buff/cache",
          "",
          [["    PID USER      %CPU  %MEM COMMAND", "t-inv"]],
        ];
        x.procs.forEach(function (r, i) { out.push(lpad(r[1], 7) + " " + pad(r[0], 9) + lpad((i ? 0.3 / (i + 1) : st.cpu / 2).toFixed(1), 5) + lpad((r[3] / x.mem * 100).toFixed(1), 6) + " " + r[2]); });
        return out;
      },
      systemctl: function (c) {
        var x = c.x, o = flags(c.argv), sub = o.args[0], unit = (o.args[1] || "").replace(/\.service$/, "");
        function state(u) { return u === "php8.3-fpm" && x.broken ? "failed" : (x.stopped || {})[u] ? "inactive" : "active"; }
        if (o.f.failed || sub === "--failed") {
          var bad = x.broken ? [[["● ", "t-red"], ["php8.3-fpm.service loaded failed failed " + SERVICES["php8.3-fpm"]]]] : [];
          return o.f["no-legend"] ? bad : bad.concat([bad.length + " loaded units listed."]);
        }
        if (!sub || sub === "list-units") return x.services.map(function (u) { return pad(u + ".service", 22) + " loaded " + pad(state(u), 8) + " " + (state(u) === "active" ? "running" : "dead   ") + " " + SERVICES[u]; });
        if (x.services.indexOf(unit) < 0) return { lines: [(sub === "status" ? "Unit " : "Failed to " + sub + " " ) + unit + ".service" + (sub === "status" ? " could not be found." : ": Unit " + unit + ".service not found.")], code: 4 };
        if (sub === "is-active") return { lines: [state(unit)], code: state(unit) === "active" ? 0 : 3 };
        if (sub === "status") {
          var st = state(unit), ok = st === "active", proc = x.procs.filter(function (r) { return r[2].indexOf(unit.split(/[.-]/)[0]) === 0; })[0];
          return { lines: [
            [["● ", ok ? "t-ok" : st === "failed" ? "t-red" : "t-dim"], [unit + ".service - " + SERVICES[unit]]],
            "     Loaded: loaded (/usr/lib/systemd/system/" + unit + ".service; enabled; preset: enabled)",
            [["     Active: "], [ok ? "active (running)" : st === "failed" ? "failed (Result: exit-code)" : "inactive (dead)", ok ? "t-ok" : st === "failed" ? "t-red" : ""], [" since Mon 2026-11-02 08:31:12 UTC; 2 weeks 4 days ago"]],
          ].concat(ok && proc ? ["   Main PID: " + proc[1] + " (" + proc[2] + ")", "     Memory: " + proc[3] + "M", "        CPU: 3min 12.408s"] : []), code: ok ? 0 : 3 };
        }
        if (/^(restart|start|reload)$/.test(sub)) {
          if (unit === "php8.3-fpm" && x.broken && !x.fixed) return { lines: ["Job for php8.3-fpm.service failed because the control process exited with error code."], code: 1 };
          if (unit === "php8.3-fpm") x.broken = false;
          if (x.stopped) delete x.stopped[unit];
          return [];
        }
        if (sub === "stop") { (x.stopped = x.stopped || {})[unit] = true; return []; }
        return { lines: ["Unknown command verb " + sub + "."], code: 1 };
      },
      docker: function (c) {
        var x = c.x;
        if (!x.containers) return { lines: ["bash: docker: command not found"], code: 127 };
        var sub = c.argv[0];
        if (sub === "ps") return table([["CONTAINER ID", "IMAGE", "COMMAND", "CREATED", "STATUS", "PORTS", "NAMES"]].concat(x.containers));
        if (sub === "images") return table([["REPOSITORY", "TAG", "IMAGE ID", "SIZE"]].concat(x.containers.map(function (r) { var i = r[1].split(":"); return [i[0], i[1] || "latest", r[0].slice(2, 14), "41.2MB"]; })));
        return { lines: [sub ? "docker: '" + sub + "' is not a docker command." : "Usage:  docker [OPTIONS] COMMAND"], code: 1 };
      },
      ping: async function (c) {
        var o = flags(c.argv.filter(function (a, i, all) { return a !== "-c" && all[i - 1] !== "-c"; }));
        var target = o.args[0];
        if (!target) return { lines: ["ping: usage error: Destination address required"], code: 1 };
        var n = nArg(c.argv.map(function (a) { return a === "-c" ? "-n" : a; }), 4);
        var hash = 0;
        for (var i = 0; i < target.length; i++) hash = (hash * 31 + target.charCodeAt(i)) >>> 0;
        var ip = /^\d+\.\d+\.\d+\.\d+$/.test(target) ? target : target === "localhost" ? "127.0.0.1" : [93 + hash % 90, hash >> 8 & 255, hash >> 16 & 255, 1 + (hash >> 24) % 254].join(".");
        var base = target === "localhost" ? 0.04 : 4 + hash % 30, times = [];
        c.print("PING " + target + " (" + ip + ") 56(84) bytes of data.");
        for (var k = 1; k <= Math.min(n, 20); k++) {
          await sleep(k === 1 ? 120 : 520);
          if (c.s.abort) { c.print("^C"); break; }
          var t = base + Math.random() * base * 0.2;
          times.push(t);
          c.print("64 bytes from " + ip + ": icmp_seq=" + k + " ttl=" + (target === "localhost" ? 64 : 56) + " time=" + t.toFixed(t < 1 ? 3 : 1) + " ms");
        }
        var min = Math.min.apply(null, times), max = Math.max.apply(null, times), avg = times.reduce(function (a, b) { return a + b; }, 0) / (times.length || 1);
        c.print("");
        c.print("--- " + target + " ping statistics ---");
        c.print(times.length + " packets transmitted, " + times.length + " received, 0% packet loss, time " + Math.round(times.length * 1001) + "ms");
        if (times.length) c.print("rtt min/avg/max/mdev = " + [min, avg, max, (max - min) / 2].map(function (v) { return v.toFixed(3); }).join("/") + " ms");
      },
      curl: function (c) {
        var x = c.x, o = flags(c.argv), url = o.args[0];
        if (!url) return { lines: ["curl: try 'curl --help' for more information"], code: 2 };
        var local = /^(https?:\/\/)?(localhost|127\.0\.0\.1)([:/]|$)/.test(url), headers = o.f.I || o.f.head;
        if (local && (x.services.indexOf("nginx") < 0 || (x.stopped || {}).nginx)) return { lines: ["curl: (7) Failed to connect to localhost port 80 after 0 ms: Couldn't connect to server"], code: 7 };
        var bad = local && x.broken;
        var head = [[[bad ? "HTTP/1.1 502 Bad Gateway" : local ? "HTTP/1.1 200 OK" : "HTTP/2 200", bad ? "t-red" : "t-ok"]],
          local ? "Server: nginx/1.24.0 (Ubuntu)" : "server: ECS (nyd/D11F)", "Date: " + new Date().toUTCString(), "Content-Type: text/html; charset=UTF-8"];
        if (headers) return head;
        return [bad ? "<html><head><title>502 Bad Gateway</title></head><body><center><h1>502 Bad Gateway</h1></center></body></html>" : "<!doctype html><html><head><title>" + (local ? "Acme" : url) + "</title></head><body>…</body></html>"];
      },
      ip: function (c) {
        return ["1: lo: <LOOPBACK,UP,LOWER_UP> mtu 65536 state UNKNOWN", "    inet 127.0.0.1/8 scope host lo",
          "2: " + c.x.ifaces[0] + ": <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 state UP", "    inet " + c.x.addr + "/24 brd " + c.x.addr.replace(/\d+$/, "255") + " scope global " + c.x.ifaces[0]];
      },
      neofetch: function (c) {
        var x = c.x, st = x.stat, title = x.user + "@" + x.id;
        var rows = [["OS", x.os + " x86_64"], ["Host", "KVM/QEMU Standard PC (Q35 + ICH9, 2009)"], ["Kernel", x.kernel], ["Uptime", x.up.replace(/,\s+(\d+):(\d+)/, ", $1 hours, $2 mins")],
          ["Shell", "bash 5.2.21"], ["CPU", "Intel Xeon (Icelake) (" + x.cores + ") @ 2.394GHz"], ["Memory", Math.round(st.used) + "MiB / " + x.mem + "MiB"]];
        var out = [[[title, "t-blue"]], "-".repeat(title.length)];
        rows.forEach(function (r) { out.push([[r[0], "t-blue"], [": " + r[1]]]); });
        out.push("");
        out.push(["t-red", "t-ok", "t-yellow", "t-blue", "t-purple", "t-cyan"].map(function (k) { return ["███", k]; }));
        return out;
      },
      exit: function (c) { if (c.s.lines) return; c.print("logout"); c.s.exiting = true; },
      vim: function (c) {
        var a = c.argv.filter(function (v) { return v[0] !== "-"; })[0];
        c.print(fill(T.shell.editor, { cmd: c.name }), "t-dim");
        var p = a ? resolve(c.x, c.s.cwd, a) : c.s.cwd, n = lookup(c.x, p);
        c.x.filesCwd = n && n.d ? p : parentOf(p);
        c.x.preview = n && !n.d ? p : null;
        setTool("files", true);
      },
      ssh: function (c) { c.print(T.shell.ssh, "t-dim"); },
      sudo: function () { return []; },
    };
    var ALIASES = { ll: "ls", htop: "top", fastfetch: "neofetch", logout: "exit", vi: "vim", nano: "vim", less: "cat", more: "cat", ifconfig: "ip" };
    Object.keys(ALIASES).forEach(function (a) { COMMANDS[a] = COMMANDS[ALIASES[a]]; });

    // Runs one command line: ; and && chains, | pipes and > or >> into a file.
    async function runLine(s, line) {
      var parts = line.split(/\s*(&&|;)\s*/), code = 0;
      for (var i = 0; i < parts.length; i += 2) {
        if (i && parts[i - 1] === "&&" && code) break;
        if (s.abort || s.exiting) break;
        if (parts[i].trim()) code = await runPipe(s, parts[i]);
      }
      return code;
    }
    async function runPipe(s, seg) {
      var redirect = null, m = seg.match(/\s*(>>?)\s*(\S+)\s*$/);
      if (m) { redirect = { append: m[1] === ">>", path: m[2] }; seg = seg.slice(0, m.index); }
      var stages = seg.split(/\s*\|\s*/), lines = null, code = 0;
      for (var i = 0; i < stages.length; i++) {
        var r = await runCmd(s, stages[i], lines);
        lines = r.lines;
        code = r.code;
      }
      if (redirect) {
        var p = resolve(s.host, s.cwd, redirect.path), n = lookup(s.host, p);
        if (n && n.d) { s.print("bash: " + redirect.path + ": Is a directory"); return 1; }
        if (!lookup(s.host, parentOf(p))) { s.print("bash: " + redirect.path + ": No such file or directory"); return 1; }
        var text = (lines || []).map(textOf).join("\n") + (lines && lines.length ? "\n" : "");
        put(s.host.fs, p, F((redirect.append && n ? bodyOf(n) || "" : "") + text));
        changed(s.host);
        return code;
      }
      (lines || []).forEach(function (l) { s.print(l); });
      return code;
    }
    async function runCmd(s, text, stdin) {
      var x = s.host;
      var argv = tokens(text).map(function (a) {
        return a.replace(/\$(\w+)|\$\{(\w+)\}/g, function (m, k1, k2) {
          var k = k1 || k2;
          return { USER: x.user, HOME: x.home, PWD: s.cwd, SHELL: "/bin/bash", HOSTNAME: x.id }[k] || "";
        });
      });
      while (argv[0] === "sudo" && argv.length > 1) argv.shift();
      if (!argv.length) return { lines: [], code: 0 };
      var name = argv.shift(), fn = COMMANDS[name];
      if (!fn) return { lines: ["bash: " + name + ": command not found"], code: 127 };
      var r = await fn({ s: s, x: x, argv: argv, name: name, stdin: stdin, print: function (l, cls) { s.print(l, cls); } });
      if (Array.isArray(r)) return { lines: r, code: 0 };
      return { lines: r ? r.lines : [], code: r ? r.code || 0 : 0 };
    }
    async function submit(s) {
      var line = s.buf;
      s.setBuf("");
      if (s.state === "closed") { s.closedRetry = true; s.host.sessions++; await connect(s, true); return; }
      if (s.state !== "ready" || s.busy) return;
      s.hideLive();
      s.echo(line);
      if (line.trim() && s.hist[s.hist.length - 1] !== line) s.hist.push(line);
      s.hi = s.hist.length;
      s.busy = true;
      s.abort = false;
      try { await runLine(s, line); } catch (e) { s.print(String(e)); }
      s.busy = false;
      s.abort = false;
      if (s.exiting) { s.exiting = false; disconnect(s); return; }
      if (s.state === "ready") s.showLive();
    }
    // Types text into a session as if at the keyboard, then runs it.
    async function typeInto(s, text, fast) {
      while (s.busy || s.state === "connecting") await sleep(120);
      if (s.state !== "ready") return;
      function quick() { return typeof fast === "function" ? fast() : fast; }
      for (var i = 1; i <= text.length; i++) {
        if (quick()) { s.setBuf(text); break; }
        s.setBuf(text.slice(0, i));
        await sleep(26 + Math.random() * 50);
      }
      if (!quick()) await sleep(240);
      await submit(s);
    }
    function complete(s) {
      var before = s.buf.slice(0, s.caret), after = s.buf.slice(s.caret);
      var word = before.match(/\S*$/)[0], lead = before.slice(0, before.length - word.length);
      var first = !lead.trim() || /(\||&&|;|sudo)\s*$/.test(lead);
      var cands;
      if (first) cands = Object.keys(COMMANDS).filter(function (n) { return n.indexOf(word) === 0; }).sort().map(function (n) { return n + " "; });
      else {
        var cut = word.lastIndexOf("/") + 1, dirPart = word.slice(0, cut), base = word.slice(cut);
        var dir = lookup(s.host, resolve(s.host, s.cwd, dirPart || "."));
        cands = dir && dir.d ? entries(dir).filter(function (n) { return n.indexOf(base) === 0 && (base[0] === "." || n[0] !== "."); })
          .map(function (n) { return dirPart + n + (dir.c[n].d ? "/" : " "); }) : [];
      }
      if (!cands.length) return;
      var common = cands.reduce(function (a, b) { var i = 0; while (i < a.length && a[i] === b[i]) i++; return a.slice(0, i); });
      if (cands.length === 1 || common.length > word.length) {
        var head = before.slice(0, before.length - word.length) + (cands.length === 1 ? cands[0] : common);
        s.setBuf(head + after, head.length);
        return;
      }
      s.echo(s.buf);
      s.print(cands.map(function (c) { return c.trim(); }).join("  "));
    }

    // ------------------------------------------------------------ tabs

    function newSession(x, el) {
      var s = new Session(x, el);
      x.sessions++;
      return s;
    }
    function openTab(x, quiet) {
      if (tabs.length >= 6) { activate(tabs[tabs.length - 1]); return tabs[tabs.length - 1]; }
      var s = newSession(x);
      var tab = { panes: [s], focus: 0 };
      tabs.push(tab);
      activate(tab);
      if (!quiet) focusSession(s, true);
      connect(s);
      return tab;
    }
    function tabOf(s) { return tabs.filter(function (t) { return t.panes.indexOf(s) >= 0; })[0]; }
    function activate(tab) {
      current = tab;
      termsEl.textContent = "";
      if (tab) {
        tab.panes.forEach(function (s) { termsEl.appendChild(s.el); s.scroll(); });
        var s = sess();
        input.value = s.buf;
      } else {
        termsEl.appendChild(h("div", "term-empty", [icon("server-line"), h("p", null, T.no_tabs)]));
      }
      paintChrome();
      renderTool();
    }
    function closeTab(tab) {
      var i = tabs.indexOf(tab);
      if (i < 0) return;
      tab.panes.forEach(function (s) { s.closed = true; if (s.state !== "closed") s.host.sessions--; });
      tabs.splice(i, 1);
      if (tab === current) activate(tabs[Math.min(i, tabs.length - 1)] || null);
      else paintChrome();
    }
    function split(s) {
      var tab = tabOf(s);
      if (!tab || tab.panes.length >= 2) return;
      var n = newSession(s.host);
      n.cwd = s.cwd;
      tab.panes.push(n);
      tab.focus = 1;
      activate(tab);
      focusSession(n, true);
      connect(n, true);
    }
    function closePane(s) {
      var tab = tabOf(s);
      if (!tab) return;
      if (tab.panes.length === 1) { closeTab(tab); return; }
      s.closed = true;
      if (s.state !== "closed") s.host.sessions--;
      tab.panes.splice(tab.panes.indexOf(s), 1);
      tab.focus = 0;
      activate(tab);
    }
    function focusSession(s, keyboard) {
      var tab = tabOf(s);
      if (!tab) return;
      if (tab !== current) activate(tab);
      var host = sess() && sess().host;
      tab.focus = tab.panes.indexOf(s);
      input.value = s.buf;
      paintFocus();
      if (host !== s.host) { paintChrome(); renderTool(); }
      if (keyboard) {
        if (story.running && s.host === web) story.fast = true;
        input.focus({ preventScroll: true });
        try { input.setSelectionRange(s.caret, s.caret); } catch (e) {}
      }
    }
    function paintFocus() {
      tabs.forEach(function (t) { t.panes.forEach(function (s, i) { s.el.classList.toggle("focused", t === current && i === t.focus); }); });
      win.classList.toggle("typing", focused);
    }

    // The tab strip, sidebar, status bar and rail.
    var hostsEl = root.querySelector("[data-hosts]");
    var hostsEmpty = root.querySelector("[data-hosts-empty]");
    var search = root.querySelector("[data-host-search]");
    var stHost = root.querySelector("[data-st-host]");
    var stTime = root.querySelector("[data-st-time] span");
    var stSessions = root.querySelector("[data-st-sessions] span");
    function hostRow(x, onPick, on) {
      var b = h("button", "host" + (on ? " on" : ""), [tile(x), h("span", "host-name", x.id), x.sessions > 0 ? h("i", "dot") : null]);
      b.type = "button";
      b.addEventListener("click", function () { onPick(x); });
      return h("li", null, b);
    }
    function paintChrome() {
      var s = sess(), x = s && s.host;
      tabsEl.textContent = "";
      tabs.forEach(function (t) {
        var first = t.panes[0], live = t.panes.some(function (p) { return p.state === "ready"; });
        var close = btn("close-line", T.close_tab, function (e) { e.stopPropagation(); closeTab(t); }, "wtab-close");
        var el = h("div", "wtab" + (t === current ? " on" : ""), [tile(first.host), h("span", "wtab-name", first.host.id + (t.panes.length > 1 ? " +" + (t.panes.length - 1) : "")), h("i", "dot" + (live ? "" : " off")), close]);
        el.setAttribute("role", "tab");
        el.setAttribute("aria-selected", String(t === current));
        el.tabIndex = 0;
        el.addEventListener("click", function () { if (t !== current) activate(t); });
        el.addEventListener("keydown", function (e) { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); activate(t); } });
        tabsEl.appendChild(el);
      });
      tabsEl.setAttribute("role", "tablist");
      var on = tabsEl.querySelector(".wtab.on");
      if (on) on.scrollIntoView({ block: "nearest", inline: "nearest" });
      paintHosts();
      tabs.forEach(function (t) { t.panes.forEach(function (p) { p.dot.classList.toggle("off", p.state !== "ready"); p.splitBtn.hidden = t.panes.length > 1; }); });
      stHost.textContent = "";
      if (x) stHost.append(h("i", "dot" + (s.state === "ready" ? "" : " off")), h("b", null, x.id), h("span", "muted", x.user + "@" + x.addr));
      var n = tabs.reduce(function (a, t) { return a + t.panes.filter(function (p) { return p.state !== "closed"; }).length; }, 0);
      stSessions.textContent = n === 1 ? T.session_one : fill(T.session_many, { n: n });
      paintTime();
      paintFocus();
      $$("[data-rail]", root).forEach(function (b) { b.disabled = !current; });
      toolPane.hidden = !tool || !current;
      win.classList.toggle("has-tool", !toolPane.hidden);
    }
    function paintHosts() {
      var q = search.value.trim().toLowerCase(), x = sess() && sess().host;
      hostsEl.textContent = "";
      var list = HOSTS.filter(function (y) { return (y.id + " " + y.addr).indexOf(q) >= 0; });
      list.forEach(function (y) {
        hostsEl.appendChild(hostRow(y, function () {
          var t = tabs.filter(function (t) { return t.panes[0].host === y; })[0];
          if (t) { activate(t); focusSession(sess(), true); } else openTab(y);
        }, y === x));
      });
      hostsEmpty.hidden = list.length > 0;
    }
    var units = {};
    function unit(u, n) {
      try {
        units[u] = units[u] || new Intl.NumberFormat(data.lang, { style: "unit", unit: u, unitDisplay: "long" });
        return units[u].format(n);
      } catch (e) { return n + (u === "minute" ? " min" : " s"); }
    }
    function since(t) {
      var sec = Math.max(1, Math.round((Date.now() - t) / 1000));
      return sec < 60 ? unit("second", sec) : sec < 3600 ? unit("minute", Math.floor(sec / 60)) : unit("hour", Math.floor(sec / 3600));
    }
    function paintTime() {
      var x = sess() && sess().host;
      stTime.parentNode.hidden = !x || !x.connectedAt;
      if (x && x.connectedAt) stTime.textContent = since(x.connectedAt);
    }
    search.addEventListener("input", paintHosts);

    // The + menu: a searchable list of hosts, each opened in a new tab.
    var menu = root.querySelector("[data-tab-menu]");
    var menuBtn = root.querySelector("[data-new-tab]");
    var menuSearch = root.querySelector("[data-tab-menu-search]");
    var menuList = root.querySelector("[data-tab-menu-list]");
    function paintMenu() {
      var q = menuSearch.value.trim().toLowerCase();
      menuList.textContent = "";
      HOSTS.filter(function (y) { return (y.id + " " + y.addr).indexOf(q) >= 0; }).forEach(function (y) {
        menuList.appendChild(hostRow(y, function () { toggleMenu(false); openTab(y); }));
      });
    }
    function toggleMenu(open) {
      menu.hidden = !open;
      menuBtn.setAttribute("aria-expanded", String(open));
      if (open) { menuSearch.value = ""; paintMenu(); menuSearch.focus({ preventScroll: true }); }
    }
    menuBtn.addEventListener("click", function (e) { e.stopPropagation(); toggleMenu(menu.hidden); });
    menuSearch.addEventListener("input", paintMenu);
    menuSearch.addEventListener("keydown", function (e) {
      if (e.key === "Escape") { toggleMenu(false); menuBtn.focus(); }
      if (e.key === "Enter") { var b = menuList.querySelector("button"); if (b) b.click(); }
    });
    document.addEventListener("click", function (e) { if (!menu.hidden && !menu.contains(e.target)) toggleMenu(false); });

    root.querySelector("[data-st-theme]").addEventListener("click", function () {
      var dark = doc.getAttribute("data-theme") === "dark" || (!doc.getAttribute("data-theme") && systemDark.matches);
      var mode = dark ? "light" : "dark";
      store("sshyard-theme", mode);
      applyTheme(mode);
    });

    // ------------------------------------------------------------ keyboard

    input.addEventListener("focus", function () { focused = true; paintFocus(); });
    input.addEventListener("blur", function () { focused = false; paintFocus(); });
    function syncFromInput() {
      var s = sess();
      if (!s) return;
      s.buf = input.value;
      s.caret = input.selectionStart == null ? input.value.length : input.selectionStart;
      s.render();
    }
    input.addEventListener("input", syncFromInput);
    input.addEventListener("keyup", syncFromInput);
    input.addEventListener("select", syncFromInput);
    input.addEventListener("keydown", function (e) {
      var s = sess();
      if (!s) return;
      if (story.running && s.host === web) story.fast = true;
      if (e.isComposing || e.keyCode === 229) return;
      var ctrl = e.ctrlKey && !e.metaKey && !e.altKey;
      if (e.key === "Enter") { e.preventDefault(); submit(s); return; }
      if (e.key === "Tab") { e.preventDefault(); complete(s); return; }
      if (e.key === "ArrowUp" || e.key === "ArrowDown") {
        e.preventDefault();
        if (!s.hist.length) return;
        s.hi = clamp(s.hi + (e.key === "ArrowUp" ? -1 : 1), 0, s.hist.length);
        s.setBuf(s.hist[s.hi] || "");
        return;
      }
      if (ctrl && e.key.toLowerCase() === "c") {
        if (window.getSelection && !window.getSelection().isCollapsed) return;
        e.preventDefault();
        if (s.busy) { s.abort = true; return; }
        s.hideLive();
        s.echo(s.buf + "^C");
        s.setBuf("");
        s.showLive();
        return;
      }
      if (ctrl && e.key.toLowerCase() === "l") { e.preventDefault(); s.clear(); return; }
      if (ctrl && e.key.toLowerCase() === "u") { e.preventDefault(); s.setBuf(s.buf.slice(s.caret), 0); return; }
      if (e.key === "Escape") { input.blur(); return; }
    });

    // ------------------------------------------------------------ panes

    var tool = "ai";
    var bodies = {};
    $$("[data-tool]", toolPane).forEach(function (b) { bodies[b.getAttribute("data-tool")] = b; });
    var toolIcon = toolPane.querySelector("[data-tool-icon]");
    var toolTitle = toolPane.querySelector("[data-tool-title]");
    var toolActions = toolPane.querySelector("[data-tool-actions]");
    var ICONS = { monitor: "pulse-fill", files: "folder-3-fill", ai: "robot-2-fill", snippets: "code-s-slash-line" };

    function setTool(kind, show) {
      tool = kind === tool && !show ? null : kind;
      $$("[data-rail]", root).forEach(function (b) {
        var on = b.getAttribute("data-rail") === tool;
        b.classList.toggle("on", on);
        b.setAttribute("aria-pressed", String(on));
      });
      paintChrome();
      renderTool();
    }
    $$("[data-rail]", root).forEach(function (b) {
      b.addEventListener("click", function () { setTool(b.getAttribute("data-rail")); });
    });
    toolPane.querySelector("[data-tool-close]").addEventListener("click", function () { setTool(tool); });

    function renderTool() {
      var s = sess();
      if (!tool || !s) return;
      var x = s.host;
      toolPane.setAttribute("data-kind", tool);
      Object.keys(bodies).forEach(function (k) { bodies[k].hidden = k !== tool; });
      toolIcon.textContent = "";
      toolIcon.appendChild(icon(ICONS[tool]));
      toolTitle.textContent = tool === "snippets" ? T.tools.snippets : T.tools[tool] + " · " + x.id;
      toolActions.textContent = "";
      if (tool === "monitor") renderMonitor(x);
      if (tool === "files") renderFiles(x);
      if (tool === "snippets") renderSnippets();
      if (tool === "ai") renderAI(x);
    }

    // System monitor -------------------------------------------------

    var mon = null;
    function bar(pct) { var b = h("span", "mbar", h("i")); b.firstChild.style.width = clamp(pct, 0, 100) + "%"; return b; }
    function spark() {
      var svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
      svg.setAttribute("viewBox", "0 0 100 32");
      svg.setAttribute("preserveAspectRatio", "none");
      svg.setAttribute("class", "spark");
      svg.setAttribute("aria-hidden", "true");
      var area = document.createElementNS(svg.namespaceURI, "path"), line = document.createElementNS(svg.namespaceURI, "path");
      area.setAttribute("class", "spark-area");
      line.setAttribute("class", "spark-line");
      svg.append(area, line);
      svg.paint = function (vals, max) {
        var pts = vals.map(function (v, i) { return [(i / (vals.length - 1)) * 100, 31 - clamp(v / max, 0, 1) * 29]; });
        var d = pts.map(function (p, i) { return (i ? "L" : "M") + p[0].toFixed(1) + " " + p[1].toFixed(1); }).join("");
        line.setAttribute("d", d);
        area.setAttribute("d", d + "L100 32L0 32Z");
      };
      return svg;
    }
    function card(iconName, title, aside, kids) {
      return h("div", "mcard", [h("div", "mcard-head", [h("span", "mcard-icon", icon(iconName)), h("b", null, title), aside ? h("span", "muted", aside) : null])].concat(kids));
    }
    function kv(k, v) { var d = h("b", "mono", v); return [h("div", "kv", [h("span", "muted", k), d]), d]; }
    function renderMonitor(x) {
      var M = T.monitor, b = bodies.monitor;
      b.textContent = "";
      var lat = kv(M.latency, ""), conn = kv(M.connected, "");
      var cpuPct = h("b", "big"), cpuBar = bar(0), cpuSpark = spark(), load = h("span", "mono");
      var memPct = h("b", "t-blue"), memBar = bar(0), memTxt = h("span", "mono muted");
      var swapPct = h("b", "t-blue"), swapBar = bar(0), swapTxt = h("span", "mono muted");
      var netSpark = spark(), nets = [];
      b.append(h("div", "mon", [
        card("server-line", M.system, null, [h("div", "kv-grid", [kv(M.address, x.user + "@" + x.addr)[0], kv(M.os, x.os)[0], lat[0], conn[0]])]),
        card("cpu-line", "CPU", fill(M.cores, { n: x.cores }), [
          h("div", "cpu-row", [cpuPct, h("span", "muted", M.average)]), cpuBar, cpuSpark,
          h("div", "kv-row", [h("span", "muted", M.load), load])]),
        card("ram-2-line", M.memory, null, [
          h("div", "kv-row", [h("span", null, M.physical), memPct]), memBar, memTxt,
          h("div", "kv-row", [h("span", null, M.swap), swapPct]), swapBar, swapTxt]),
        card("global-line", M.network, null, [netSpark].concat(x.ifaces.map(function (n, i) {
          var u = h("span", "t-ok"), d = h("span", "t-blue");
          nets.push([u, d, i]);
          return h("div", "iface mono", [h("span", null, n), h("span", "rates", [icon("arrow-up-line"), u, icon("arrow-down-line"), d])]);
        }))),
        card("hard-drive-2-line", M.disk, null, [h("div", "kv-row", [h("span", "mono", "/"), h("b", "t-blue", x.disk.pct + "%")]), bar(x.disk.pct), h("span", "mono muted", x.disk.used + " / " + x.disk.size)]),
      ]));
      mon = { x: x, paint: function () {
        var st = x.stat;
        lat[1].textContent = Math.round(st.lat) + " ms";
        conn[1].textContent = x.connectedAt ? since(x.connectedAt) : "–";
        cpuPct.textContent = Math.round(st.cpu) + "%";
        cpuBar.firstChild.style.width = st.cpu + "%";
        cpuSpark.paint(st.cpuHist, Math.min(100, Math.max(20, 1.5 * Math.max.apply(null, st.cpuHist))));
        load.textContent = (st.load || x.load).map(function (v) { return v.toFixed(2); }).join("  ");
        var mp = st.used / x.mem * 100;
        memPct.textContent = Math.round(mp) + "%";
        memBar.firstChild.style.width = mp + "%";
        memTxt.textContent = (st.used / 1024).toFixed(1) + "G / " + (x.mem / 1024).toFixed(1) + "G";
        swapPct.textContent = "0%";
        swapTxt.textContent = "0K / " + (x.swap ? (x.swap / 1024).toFixed(1) + "G" : "0B");
        netSpark.paint(st.netHist, Math.max.apply(null, st.netHist.concat([2048])));
        nets.forEach(function (n) { n[0].textContent = n[2] ? "0B/s" : rate(st.up); n[1].textContent = n[2] ? "0B/s" : rate(st.down); });
      } };
      mon.paint();
    }

    // Files ------------------------------------------------------------

    var pick = root.querySelector("[data-file-pick]");
    var transfers = h("div", "transfers");
    function changed(x) { if (tool === "files" && sess() && sess().host === x) renderFiles(x); }
    function renderFiles(x) {
      var F_ = T.files, b = bodies.files;
      toolActions.textContent = "";
      toolActions.append(
        btn("corner-left-up-line", F_.up, function () { x.preview = null; x.filesCwd = parentOf(x.filesCwd); renderFiles(x); }),
        btn("refresh-line", F_.refresh, function (e) { var i = e.currentTarget.querySelector(".i"); i.classList.remove("spin"); void i.offsetWidth; i.classList.add("spin"); renderFiles(x); }),
        (function () {
          var f = btn(follow ? "footprint-fill" : "footprint-line", F_.follow, function () { follow = !follow; if (follow) { x.filesCwd = sess().cwd; x.preview = null; } renderFiles(x); }, follow ? "on" : "");
          f.setAttribute("aria-pressed", String(follow));
          return f;
        })(),
        btn("upload-2-line", F_.upload, function () { pick.click(); }));
      b.textContent = "";
      var dir = lookup(x, x.filesCwd);
      if (!dir || !dir.d) { x.filesCwd = x.home; dir = lookup(x, x.home); }
      var crumbs = h("div", "crumbs-bar");
      var parts = x.filesCwd.split("/").filter(Boolean);
      var root_ = h("button", "crumb", icon("server-line"));
      root_.type = "button";
      root_.setAttribute("aria-label", "/");
      root_.addEventListener("click", function () { x.preview = null; x.filesCwd = "/"; renderFiles(x); });
      crumbs.append(root_);
      parts.forEach(function (p, i) {
        var c = h("button", "crumb", p);
        c.type = "button";
        c.addEventListener("click", function () { x.preview = null; x.filesCwd = "/" + parts.slice(0, i + 1).join("/"); renderFiles(x); });
        crumbs.append(icon("arrow-right-s-line"), c);
      });
      b.append(crumbs);
      var view = h("div", "files-view");
      b.append(view);
      if (x.preview) {
        var f = lookup(x, x.preview);
        var back = btn("arrow-left-line", F_.back, function () { x.preview = null; renderFiles(x); });
        var body = f ? bodyOf(f) : null;
        view.append(h("div", "preview-head", [back, fileIcon(fileIconName(baseOf(x.preview), false)), h("b", "mono", baseOf(x.preview)), h("span", "muted", human(f ? sizeOf(f) : 0))]),
          body == null ? h("p", "files-empty muted", F_.note) : h("pre", "preview", body.split("\n").slice(0, 200).join("\n")));
      } else {
        var list = h("ul", "files-list");
        list.setAttribute("role", "list");
        var names = entries(dir).sort(function (a, b2) { return (dir.c[b2].d - dir.c[a].d) || 0; });
        names.forEach(function (n) {
          var node = dir.c[n];
          var row = h("button", "frow", [fileIcon(fileIconName(n, node.d)), h("span", "fname", n), h("span", "fsize", node.d ? "" : human(sizeOf(node)))]);
          row.type = "button";
          row.addEventListener("click", function () {
            var p = (x.filesCwd === "/" ? "" : x.filesCwd) + "/" + n;
            if (node.d) x.filesCwd = p; else x.preview = p;
            renderFiles(x);
          });
          list.append(h("li", null, row));
        });
        view.append(h("div", "files-head", [h("span", null, F_.name), h("span", null, F_.size)]),
          names.length ? list : h("p", "files-empty muted", F_.empty));
      }
      view.append(h("div", "drop-hint", [icon("upload-2-line"), h("span", null, F_.drop)]));
      b.append(transfers);
    }
    function upload(files) {
      var s = sess();
      if (!s || !files || !files.length) return;
      var x = s.host, dir = x.filesCwd;
      if (tool !== "files") setTool("files", true);
      Array.prototype.slice.call(files, 0, 8).forEach(function (file) {
        var name = String(file.name).replace(/[\/\\]/g, "_") || "upload";
        var label = h("span", "tname", fill(T.files.uploading, { name: name }));
        var pct = h("span", "mono muted", "0%");
        var prog = bar(0);
        var row = h("div", "trow", [fileIcon(fileIconName(name, false)), h("div", "tmain", [label, prog]), pct]);
        transfers.append(row);
        var ms = clamp(file.size / 4096, 700, 3200), t0 = performance.now();
        (function frame(t) {
          var p = clamp((t - t0) / ms, 0, 1);
          prog.firstChild.style.width = p * 100 + "%";
          pct.textContent = Math.round(p * 100) + "%";
          if (p < 1) { requestAnimationFrame(frame); return; }
          put(x.fs, (dir === "/" ? "" : dir) + "/" + name, F(null, { size: file.size }));
          label.textContent = fill(T.files.uploaded, { name: name });
          row.classList.add("done");
          changed(x);
          setTimeout(function () { row.remove(); }, 2600);
        })(t0);
      });
      if (!transfers.querySelector(".tnote")) transfers.prepend(h("p", "tnote muted", T.files.note));
    }
    pick.addEventListener("change", function () { upload(pick.files); pick.value = ""; });
    var depth = 0;
    function hasFiles(e) { return e.dataTransfer && Array.prototype.indexOf.call(e.dataTransfer.types || [], "Files") >= 0; }
    bodies.files.addEventListener("dragenter", function (e) { if (!hasFiles(e)) return; e.preventDefault(); depth++; bodies.files.classList.add("dragging"); });
    bodies.files.addEventListener("dragover", function (e) { if (!hasFiles(e)) return; e.preventDefault(); e.dataTransfer.dropEffect = "copy"; });
    bodies.files.addEventListener("dragleave", function () { if (--depth <= 0) { depth = 0; bodies.files.classList.remove("dragging"); } });
    bodies.files.addEventListener("drop", function (e) {
      if (!hasFiles(e)) return;
      e.preventDefault();
      depth = 0;
      bodies.files.classList.remove("dragging");
      upload(e.dataTransfer.files);
    });

    // Snippets ---------------------------------------------------------

    function renderSnippets() {
      var b = bodies.snippets;
      if (b.firstChild) return;
      var list = h("ul", "snips");
      list.setAttribute("role", "list");
      T.snippets.items.forEach(function (it) {
        list.append(h("li", "snip", [h("div", "snip-text", [h("b", null, it.name), h("code", null, it.cmd)]),
          h("span", "snip-acts", [
            btn("terminal-line", T.snippets.insert, function () { var s = sess(); if (s && s.state === "ready" && !s.busy) { s.setBuf(it.cmd); focusSession(s, true); } }),
            btn("play-circle-line", T.snippets.run, function () { var s = sess(); if (s) { if (story.running && s.host === web) story.fast = true; typeInto(s, it.cmd, reduced.matches); } }),
          ])]));
      });
      b.append(list);
    }

    // AI ---------------------------------------------------------------

    var aiBody = bodies.ai;
    var aiSugg = aiBody.querySelector("[data-ai-sugg]");
    var aiForm = aiBody.querySelector("[data-ai-form]");
    var aiInput = aiBody.querySelector("[data-ai-input]");
    var aiModeBtn = aiBody.querySelector("[data-ai-mode]");
    var aiFull = aiBody.querySelector("[data-ai-full]");
    var MODES = ["ask", "review", "full"];
    var MODE_ICONS = { ask: "shield-check-line", review: "eye-line", full: "lock-unlock-line" };
    var mode = "review";
    HOSTS.forEach(function (x) { x.log = h("div", "ai-log"); });

    function paintMode() {
      aiModeBtn.textContent = "";
      aiModeBtn.append(icon(MODE_ICONS[mode]), h("span", null, T.modes[MODES.indexOf(mode)].label));
      aiModeBtn.className = "chip chip-mode mode-" + mode;
      aiFull.hidden = mode !== "full";
    }
    aiModeBtn.addEventListener("click", function () { mode = MODES[(MODES.indexOf(mode) + 1) % MODES.length]; paintMode(); });

    function renderAI(x) {
      var slot = aiBody.querySelector(".ai-log");
      if (slot !== x.log) slot.replaceWith(x.log);
      if (x === web) toolActions.append(btn("restart-line", T.replay, replay));
      if (!x.log.firstChild) x.log.append(h("div", "ai-empty", [icon("robot-2-fill"), h("p", null, T.placeholder)]));
      aiSugg.hidden = story.running && x === web;
      x.log.scrollTop = x.log.scrollHeight;
    }
    function logAdd(x, el) {
      var e = x.log.querySelector(".ai-empty");
      if (e) e.remove();
      x.log.append(el);
      if (!reduced.matches) { el.classList.add("entering"); void el.offsetWidth; el.classList.remove("entering"); }
      x.log.scrollTop = x.log.scrollHeight;
      return el;
    }
    async function streamText(x, text, fast) {
      var p = logAdd(x, h("p", "msg-ai"));
      var chars = Array.from(text), cjk = /[぀-ヿ㐀-鿿가-힣]/.test(text);
      for (var i = 0; i < chars.length && !reduced.matches && !(fast && fast());) {
        i += cjk ? 1 + Math.floor(Math.random() * 2) : 2 + Math.floor(Math.random() * 4);
        p.textContent = chars.slice(0, i).join("");
        x.log.scrollTop = x.log.scrollHeight;
        await sleep(cjk ? 34 : 22);
      }
      p.textContent = text;
      x.log.scrollTop = x.log.scrollHeight;
    }
    function toolCard(x, iconName, title, codes) {
      var meta = h("span", "muted"), state = h("span");
      var el = h("div", "tool", [h("div", "tool-head", [icon(iconName), h("b", null, title), meta, state])]
        .concat(codes.map(function (c) { return h("code", c[1] || null, c[0]); })));
      logAdd(x, el);
      function scroll() { x.log.scrollTop = x.log.scrollHeight; }
      return {
        meta: function (t) { meta.textContent = t; },
        state: function (t, cls) { state.textContent = t; state.className = cls || ""; },
        out: function (lines) { lines.forEach(function (l) { el.append(h("code", l[1] || null, l[0])); }); scroll(); },
        ask: function () {
          return new Promise(function (resolve) {
            var deny = h("button", "btn btn-quiet btn-sm", T.card.deny), allow = h("button", "btn btn-primary btn-sm", T.card.allow);
            var row = h("div", "tool-acts", [deny, allow]);
            deny.type = allow.type = "button";
            deny.addEventListener("click", function () { row.remove(); resolve(false); });
            allow.addEventListener("click", function () { row.remove(); resolve(true); });
            el.append(row);
            scroll();
          });
        },
      };
    }
    async function approve(c) {
      if (mode === "ask") {
        c.state(T.card.waiting, "muted");
        return c.ask();
      }
      if (mode === "review") {
        c.state(T.card.reviewing, "muted");
        await sleep(reduced.matches ? 0 : 900);
        c.meta(T.tool_ok);
        return true;
      }
      c.meta(T.card.ran);
      return true;
    }
    function values(x) {
      var p = x.procs.slice().sort(function (a, b) { return b[3] - a[3]; });
      return { host: x.id, pct: x.disk.pct + "%", used: x.disk.used, size: x.disk.size, avail: x.disk.avail,
        top: p[0][2], topmem: mib(p[0][3]), second: p[1][2], secondmem: mib(p[1][3]) };
    }
    async function ask(x, i) {
      if (x.aiBusy) return;
      x.aiBusy = true;
      var sg = T.suggestions[i];
      logAdd(x, h("div", "msg-user", h("span", null, sg.q)));
      await sleep(reduced.matches ? 0 : 350);
      var c = toolCard(x, "terminal-box-line", T.tool_run, [[sg.cmd]]);
      if (!(await approve(c))) {
        c.state(T.rejected, "t-red");
        await streamText(x, T.denied);
        x.aiBusy = false;
        return;
      }
      c.state(T.running, "muted");
      var headless = new Session(x, false);
      await runLine(headless, sg.cmd);
      await sleep(reduced.matches ? 0 : 500);
      c.out(headless.lines.slice(0, 5).map(function (l) { return [l, "t-dim"]; }));
      c.state(T.tool_done, "t-ok");
      await streamText(x, fill(sg.a, values(x)));
      x.aiBusy = false;
    }
    aiSugg.addEventListener("click", function (e) {
      var b = e.target.closest("[data-q]");
      if (b && sess()) ask(sess().host, +b.getAttribute("data-q"));
    });
    aiForm.addEventListener("submit", async function (e) {
      e.preventDefault();
      var text = aiInput.value.trim(), s = sess();
      if (!text || !s) return;
      aiInput.value = "";
      // A question waits for the story to finish, so the two never interleave.
      if (story.running && s.host === web) { story.fast = true; await story.done; }
      logAdd(s.host, h("div", "msg-user", h("span", null, text)));
      streamText(s.host, T.free_text);
    });

    // The story: nginx answers 502 until the assistant fixes php-fpm.
    var story = { running: false, fast: false, done: Promise.resolve(), end: null };
    var visible = false;
    new IntersectionObserver(function (e) { visible = e[0].isIntersecting; }, { threshold: 0.2 }).observe(root);
    function storyWait(ms) { return story.fast ? Promise.resolve() : sleep(ms); }
    function onScreen() {
      return new Promise(function (resolve) {
        (function check() { if (story.fast || (visible && !document.hidden)) resolve(); else setTimeout(check, 300); })();
      });
    }
    function fast() { return story.fast; }
    async function playStory() {
      if (story.running) return;
      story.running = true;
      story.fast = reduced.matches;
      story.done = new Promise(function (r) { story.end = r; });
      var x = web;
      x.broken = true;
      x.fixed = false;
      x.log.textContent = "";
      if (sess() && sess().host === x && tool === "ai") renderTool();
      var t = tabs.filter(function (t) { return t.panes[0].host === web; })[0] || openTab(web, true);
      var s = t.panes[0];
      if (s.state === "closed") await submit(s);
      await onScreen();
      await storyWait(900);
      await typeInto(s, "curl -sI localhost | head -1", fast);
      await onScreen();
      await storyWait(700);
      logAdd(x, h("div", "msg-user", h("span", null, T.user_q)));
      await storyWait(800);
      var steps = [
        ["terminal-box-line", T.tool_run, [["systemctl is-active php8.3-fpm"]], [["failed", "t-red"]]],
        null,
        ["file-edit-line", T.tool_edit, [["/etc/php/8.3/fpm/pool.d/www.conf"]], [["- listen = /run/php/php8.2-fpm.sock", "t-red"], ["+ listen = /run/php/php8.3-fpm.sock", "t-ok"]], function () { x.fixed = true; }],
        ["terminal-box-line", T.tool_run, [["sudo systemctl restart php8.3-fpm"]], [], function () { x.broken = false; }],
      ];
      for (var i = 0; i < steps.length; i++) {
        await onScreen();
        if (!steps[i]) { await streamText(x, T.a1, fast); await storyWait(700); continue; }
        var c = toolCard(x, steps[i][0], steps[i][1], steps[i][2]);
        c.meta(T.tool_ok);
        c.state(T.running, "muted");
        await storyWait(1000);
        c.out(steps[i][3]);
        if (steps[i][4]) steps[i][4]();
        c.state(T.tool_done, "t-ok");
        await storyWait(600);
      }
      await onScreen();
      await streamText(x, T.a2, fast);
      await storyWait(800);
      await typeInto(s, "curl -sI localhost | head -1", fast);
      story.running = false;
      story.fast = false;
      story.end();
      if (sess() && sess().host === web && tool === "ai") aiSugg.hidden = false;
    }
    function replay() {
      if (story.running) return;
      var t = tabs.filter(function (t) { return t.panes[0].host === web; })[0];
      if (t) activate(t);
      setTool("ai", true);
      playStory();
    }

    // ------------------------------------------------------------ live figures

    setInterval(function () {
      if (!visible || document.hidden) return;
      HOSTS.forEach(function (x) {
        var st = x.stat;
        st.cpu = clamp(st.cpu + (Math.random() - 0.47) * 5, Math.max(1, x.cpu - 3), x.cpu + 26);
        st.used = clamp(st.used + (Math.random() - 0.5) * x.mem * 0.012, x.used * 0.94, x.used * 1.06);
        st.lat = clamp(st.lat + (Math.random() - 0.5) * 4, x.lat * 0.7, x.lat * 1.8);
        st.up = 200 + Math.random() * 1100;
        st.down = 300 + Math.random() * (x.id === "db-primary" ? 9000 : 3200);
        st.load = x.load.map(function (v) { return Math.max(0, v + (Math.random() - 0.5) * 0.08); });
        st.cpuHist.push(st.cpu); st.cpuHist.shift();
        st.netHist.push(st.down); st.netHist.shift();
      });
      tabs.forEach(function (t) { t.panes.forEach(function (s) { s.paintStats(); }); });
      if (tool === "monitor" && mon && sess() && mon.x === sess().host) mon.paint();
      paintTime();
    }, 1500);

    // ------------------------------------------------------------ start

    var first = newSession(web, termsEl.querySelector("[data-pane]"));
    first.state = "ready";
    web.connectedAt = Date.now() - 4 * 60e3;
    first.print("Last login: Tue Nov  3 09:12:44 2026 from 10.0.0.8", "t-dim");
    first.echo("systemctl is-active nginx");
    first.print("active");
    first.showLive();
    first.paintStats();
    tabs.push({ panes: [first], focus: 0 });
    var db = byId["db-primary"];
    db.connectedAt = Date.now() - 12 * 60e3;
    var second = newSession(db);
    tabs.push({ panes: [second], focus: 0 });
    connect(second);
    activate(tabs[0]);
    paintMode();
    setTool("ai", true);
    playStory();

    if (paletteAdd) {
      var lines = { monitor: "pulse-line", files: "folder-3-line", ai: "robot-2-line", snippets: "code-s-slash-line" };
      var show = function () { root.scrollIntoView({ behavior: reduced.matches ? "auto" : "smooth", block: "center" }); };
      paletteAdd(data.palette.demo, Object.keys(lines).map(function (k) {
        return { icon: icon(lines[k]), label: fill(data.palette.open, { name: T.tools[k] }), run: function () { show(); setTool(k, true); } };
      }).concat(HOSTS.map(function (x) {
        return { icon: icon("terminal-box-line"), label: fill(data.palette.connect, { host: x.id }), run: function () { show(); openTab(x); } };
      })));
    }
  })();

  // The demo window settles from a tilt as it scrolls into place.
  (function tilt() {
    var demo = document.querySelector(".demo");
    if (!demo || reduced.matches) return;
    var raf = 0;
    function update() {
      raf = 0;
      var r = demo.getBoundingClientRect(), vh = window.innerHeight || 800;
      var p = clamp01((vh - r.top) / (vh * 0.75));
      demo.style.setProperty("--tilt", (1 - p).toFixed(3));
    }
    function clamp01(v) { return Math.max(0, Math.min(1, v)); }
    function queue() { if (!raf) raf = requestAnimationFrame(update); }
    window.addEventListener("scroll", queue, { passive: true });
    window.addEventListener("resize", queue);
    update();
  })();

  // ---------------------------------------------------------------- seal

  // Encrypts what is typed with a random AES-GCM key, as a picture of
  // what the local store does; nothing leaves the page.
  (function seal() {
    var box = document.querySelector("[data-seal]");
    var c = window.crypto;
    if (!box || !c || !c.getRandomValues) return;
    box.hidden = false;
    var field = box.querySelector("[data-seal-input]");
    var plain = box.querySelector("[data-seal-plain]");
    var out = box.querySelector("[data-seal-out]");
    var bytes = box.querySelector("[data-seal-bytes]");
    var key = null, seq = 0, anim = 0;
    var subtle = c.subtle && window.isSecureContext ? c.subtle : null;
    function hex(a) { return Array.prototype.map.call(a, function (b) { return (b < 16 ? "0" : "") + b.toString(16); }).join(""); }
    function scramble(text) {
      cancelAnimationFrame(anim);
      if (reduced.matches) { out.textContent = text; return; }
      var t0 = performance.now(), digits = "0123456789abcdef";
      (function frame(t) {
        var p = Math.min(1, (t - t0) / 420), n = Math.floor(text.length * p), s = text.slice(0, n);
        for (var i = n; i < text.length; i++) s += digits[(Math.random() * 16) | 0];
        out.textContent = s;
        if (p < 1) anim = requestAnimationFrame(frame);
      })(t0);
    }
    async function update() {
      var my = ++seq, text = field.value;
      plain.textContent = text;
      var data_ = new TextEncoder().encode(text), nonce = c.getRandomValues(new Uint8Array(12)), ct;
      try { ct = key ? new Uint8Array(await subtle.encrypt({ name: "AES-GCM", iv: nonce }, key, data_)) : null; } catch (e) { ct = null; }
      if (!ct) ct = c.getRandomValues(new Uint8Array(data_.length + 16));
      if (my !== seq) return;
      var all = new Uint8Array(nonce.length + ct.length);
      all.set(nonce);
      all.set(ct, nonce.length);
      bytes.textContent = "· " + bytes.getAttribute("data-tpl").replace("{n}", all.length);
      scramble(hex(all));
    }
    field.addEventListener("input", update);
    if (subtle) subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt"]).then(function (k) { key = k; update(); }, update);
    else update();
  })();

  // ---------------------------------------------------------------- lightbox

  (function lightbox() {
    var dlg = document.querySelector("[data-lightbox]");
    if (!dlg || typeof dlg.showModal !== "function") return;
    var img = dlg.querySelector("img");
    $$("[data-zoom]").forEach(function (z) {
      z.tabIndex = 0;
      z.setAttribute("role", "button");
      function open() {
        var src = (z.getAttribute("srcset") || "").split(",").pop().trim().split(" ")[0] || z.src;
        img.src = src;
        img.alt = z.alt;
        dlg.showModal();
      }
      z.addEventListener("click", open);
      z.addEventListener("keydown", function (e) { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); open(); } });
    });
    dlg.addEventListener("click", function (e) { if (e.target === dlg || e.target === img) dlg.close(); });
  })();

  // ---------------------------------------------------------------- 404

  (function notFound() {
    var el = document.getElementById("notfound-data");
    if (!el) return;
    var all = JSON.parse(el.textContent);
    var path = location.pathname.slice((data.base || "").length);
    var code = Object.keys(all).find(function (c) { return all[c].prefix && path.indexOf("/" + all[c].prefix + "/") === 0; }) || "en";
    var t = all[code];
    if (code !== "en") doc.setAttribute("lang", code);
    $$("[data-nf]").forEach(function (n) { n.textContent = t[n.getAttribute("data-nf")]; });
    var home = document.querySelector('[data-nf="home"]');
    if (home) home.href = t.href;
  })();
})();
