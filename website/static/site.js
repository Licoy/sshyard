// SSHYard website: initial boot transition, interactive dot lattice,
// appearance, language, downloads for this device, the SSHYard v1.6.0
// interactive demo window, and section interactions. Zero dependencies;
// every page remains complete without JavaScript.
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
  function clamp(x, a, b) { return Math.max(a, Math.min(b, x)); }

  // ---------------------------------------------------------------- page boot veil

  var bootVeilDone = Promise.resolve();
  (function bootVeil() {
    if (!doc.classList.contains("booting")) return;
    var done = false, resolveDone;
    bootVeilDone = new Promise(function (r) { resolveDone = r; });
    function finish() {
      if (done) return;
      done = true;
      try { sessionStorage.setItem("sshyard-booted", "1"); } catch (e) {}
      doc.classList.add("boot-done");
      setTimeout(function () {
        doc.classList.remove("booting", "boot-done");
        resolveDone();
      }, 420);
    }
    var veil = document.querySelector("[data-boot]");
    if (veil) veil.addEventListener("click", finish);
    setTimeout(finish, 420);
    setTimeout(finish, 1200);
  })();

  // ---------------------------------------------------------------- toasts

  var toastStack = document.querySelector("[data-toasts]");
  function toast(text) {
    if (!toastStack || !text) return;
    var el = document.createElement("div");
    el.className = "toast-item";
    var dot = document.createElement("i");
    dot.className = "toast-dot";
    var span = document.createElement("span");
    span.textContent = text;
    el.append(dot, span);
    toastStack.appendChild(el);
    setTimeout(function () {
      el.classList.add("out");
      setTimeout(function () { el.remove(); }, 240);
    }, 2000);
  }

  // ---------------------------------------------------------------- sliding segmented pill

  function updateSegPill(seg) {
    if (!seg) return;
    var active = seg.querySelector('[aria-checked="true"], [aria-selected="true"], [aria-current]');
    if (!active || !active.offsetWidth) return;
    seg.style.setProperty("--px", active.offsetLeft + "px");
    seg.style.setProperty("--pw", active.offsetWidth + "px");
    seg.classList.add("has-pill");
  }
  function refreshAllSegs() { $$(".seg").forEach(updateSegPill); }
  window.addEventListener("resize", refreshAllSegs, { passive: true });
  requestAnimationFrame(refreshAllSegs);

  // ---------------------------------------------------------------- appearance

  var themeColors = { light: "#F5F5F7", dark: "#1E1E20" };
  var systemDark = window.matchMedia("(prefers-color-scheme: dark)");
  var themeListeners = [];

  function isDarkNow() {
    var attr = doc.getAttribute("data-theme");
    return attr === "dark" || (attr !== "light" && systemDark.matches);
  }
  function applyTheme(mode) {
    if (mode === "light" || mode === "dark") doc.setAttribute("data-theme", mode);
    else doc.removeAttribute("data-theme");
    $$("[data-theme-set]").forEach(function (b) {
      b.setAttribute("aria-checked", String(b.getAttribute("data-theme-set") === (mode || "auto")));
    });
    var dark = isDarkNow();
    $$('meta[name="theme-color"]').forEach(function (m) {
      if (mode === "light" || mode === "dark") m.setAttribute("content", themeColors[dark ? "dark" : "light"]);
      else m.setAttribute("content", m.media.indexOf("dark") >= 0 ? themeColors.dark : themeColors.light);
    });
    $$(".theme-switch").forEach(updateSegPill);
    themeListeners.forEach(function (fn) { fn(dark); });
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

  // ---------------------------------------------------------------- menus, scroll progress & spy

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
  var progressBar = document.querySelector("[data-scroll-progress]");
  var backTop = document.querySelector("[data-back-top]");
  var spyLinks = $$("[data-spy]");
  var spySections = spyLinks.map(function (a) { return document.getElementById(a.getAttribute("data-spy")); }).filter(Boolean);

  function onScroll() {
    var y = window.scrollY || 0;
    if (nav) {
      nav.classList.toggle("scrolled", y > 2);
      nav.style.setProperty("--nav-glass", clamp(y / 56, 0, 1).toFixed(4));
    }
    if (progressBar) {
      var max = Math.max(1, doc.scrollHeight - window.innerHeight);
      progressBar.style.setProperty("--scroll-p", clamp(y / max, 0, 1).toFixed(4));
    }
    if (backTop) backTop.hidden = y < 560;
    if (spySections.length) {
      var activeId = "";
      var line = y + 140;
      spySections.forEach(function (sec) {
        if (sec.offsetTop <= line) activeId = sec.id;
      });
      spyLinks.forEach(function (a) { a.classList.toggle("spy", a.getAttribute("data-spy") === activeId); });
    }
  }
  window.addEventListener("scroll", onScroll, { passive: true });
  onScroll();
  if (backTop) {
    backTop.addEventListener("click", function () {
      window.scrollTo({ top: 0, behavior: reduced.matches ? "auto" : "smooth" });
    });
  }

  // ---------------------------------------------------------------- hero interactive dot lattice (from internal/app/welcomefield.go)

  (function heroField() {
    var canvas = document.querySelector("[data-field]");
    var hero = document.querySelector(".hero");
    if (!canvas || !hero) return;
    var ctx = canvas.getContext("2d");
    if (!ctx) return;

    var spacing = 24, dotR = 1.35, reach = 9.5;
    var w = 0, h = 0, dpr = 1, cols = 0, rows = 0, ox = 0, oy = 0;
    var trail = [], rings = [], hover = null;
    var t0 = performance.now(), last = t0, drift = 0, active = true, raf = 0;
    var inkRGB = [103, 103, 108], glowRGB = [25, 132, 252], restAlpha = 0.20;

    function syncColors() {
      var dark = isDarkNow();
      inkRGB = dark ? [152, 152, 157] : [103, 103, 108];
      restAlpha = dark ? 0.24 : 0.20;
    }
    syncColors();
    themeListeners.push(function () { syncColors(); draw(performance.now()); });

    function resize() {
      var r = hero.getBoundingClientRect();
      w = Math.max(1, Math.floor(r.width));
      h = Math.max(1, Math.floor(r.height));
      dpr = Math.min(window.devicePixelRatio || 1, 2);
      canvas.width = Math.floor(w * dpr);
      canvas.height = Math.floor(h * dpr);
      cols = Math.ceil(w / spacing) + 1;
      rows = Math.ceil(h / spacing) + 1;
      ox = (w - cols * spacing) / 2;
      oy = (h - rows * spacing) / 2;
      draw(performance.now());
    }
    function hash(x, y) {
      var v = Math.sin(x * 127.1 + y * 311.7) * 43758.5453;
      return v - Math.floor(v);
    }
    function smoothstep(a, b, x) {
      var t = clamp((x - a) / (b - a), 0, 1);
      return t * t * (3 - 2 * t);
    }
    function draw(now) {
      var sec = (now - t0) / 1000;
      if (!reduced.matches) drift += Math.min((now - last) / 1000, 0.08);
      last = now;
      trail = trail.filter(function (m) { return sec - m.at < 1.35; });
      rings = rings.filter(function (r) { return sec - r.at < 1.75; });
      if (hover && sec - hover.at > 1.2) hover = null;

      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx.clearRect(0, 0, w, h);

      var marks = trail.slice();
      if (hover) {
        var hf = 1 - (sec - hover.at) / 1.2;
        marks.push({ x: hover.x, y: hover.y, w: 0.45 * hf * hf, r: 70 });
      }
      var tMarks = marks.map(function (m) {
        if (m.w != null) return m;
        var age = sec - m.at, fade = 1 - age / 1.35;
        return { x: m.x, y: m.y, w: m.f * fade * fade, r: 40 + 48 * age };
      });
      var aRings = rings.map(function (r) {
        var age = sec - r.at, fade = 1 - age / 1.75;
        return { x: r.x, y: r.y, rad: age * 420, spread: 16 + 38 * age, f: fade * fade };
      });

      for (var gy = 0; gy < rows; gy++) {
        var hy = oy + (gy + 0.5) * spacing;
        for (var gx = 0; gx < cols; gx++) {
          var hx = ox + (gx + 0.5) * spacing;
          var seed = hash(gx + 71, gy + 71);
          var turn = Math.sin(hx / 220 + drift * 0.28) * Math.cos(hy / 220 - drift * 0.21) * 6.283;
          var bob = drift * (0.35 + 0.3 * seed);
          var dx = (Math.cos(turn) * 0.32 + Math.sin(bob + seed * 6.28) * 0.14) * reach;
          var dy = (Math.sin(turn) * 0.32 + Math.cos(bob * 0.8 + seed * 9) * 0.14) * reach;
          var heat = 0;

          for (var i = 0; i < tMarks.length; i++) {
            var m = tMarks[i], ax = hx - m.x, ay = hy - m.y, d2 = ax * ax + ay * ay;
            if (d2 > m.r * m.r * 5) continue;
            var k = m.w * Math.exp(-d2 / (m.r * m.r));
            if (k < 0.005) continue;
            var d = Math.sqrt(d2) + 1;
            heat += k;
            dx += (ax / d) * k * reach;
            dy += (ay / d) * k * reach;
          }
          for (var j = 0; j < aRings.length; j++) {
            var rg = aRings[j], rx = hx - rg.x, ry = hy - rg.y, rd = Math.sqrt(rx * rx + ry * ry);
            var front = (rd - rg.rad) / rg.spread;
            if (front * front > 6) continue;
            var rk = rg.f * Math.exp(-front * front);
            heat += rk;
            dx += (rx / (rd + 1)) * rk * reach * 1.45;
            dy += (ry / (rd + 1)) * rk * reach * 1.45;
          }
          var len = Math.hypot(dx, dy);
          if (len > reach) { dx *= reach / len; dy *= reach / len; }
          var lit = Math.min(heat, 1);
          var swell = 0.5 + 0.5 * Math.sin(hx * 0.008 + hy * 0.005 + drift * 0.32) * Math.sin(-hx * 0.005 + hy * 0.01 - drift * 0.22);
          var a = (restAlpha * (0.45 + 0.55 * swell) * (1 - lit) + 0.62 * lit);
          var blend = smoothstep(0, 0.45, lit);
          var cr = Math.round(inkRGB[0] + (glowRGB[0] - inkRGB[0]) * blend);
          var cg = Math.round(inkRGB[1] + (glowRGB[1] - inkRGB[1]) * blend);
          var cb = Math.round(inkRGB[2] + (glowRGB[2] - inkRGB[2]) * blend);
          var rad = dotR * (1 + 0.72 * lit);
          ctx.fillStyle = "rgba(" + cr + "," + cg + "," + cb + "," + a.toFixed(3) + ")";
          ctx.beginPath();
          ctx.arc(hx + dx, hy + dy, rad, 0, 6.2832);
          ctx.fill();
        }
      }
    }
    function loop(now) {
      raf = 0;
      if (!active || document.hidden || reduced.matches) return;
      draw(now);
      raf = requestAnimationFrame(loop);
    }
    function kick() { if (!raf && active && !document.hidden && !reduced.matches) raf = requestAnimationFrame(loop); }

    document.addEventListener("pointermove", function (e) {
      if (reduced.matches || !active) return;
      if (e.target.closest && !e.target.closest(".hero, .nav")) return;
      var r = hero.getBoundingClientRect();
      var x = e.clientX - r.left, y = e.clientY - r.top;
      if (y < 0 || y > r.height) return;
      var sec = (performance.now() - t0) / 1000;
      hover = { x: x, y: y, at: sec };
      var prev = trail[trail.length - 1];
      if (!prev || Math.hypot(x - prev.x, y - prev.y) >= 16) {
        trail.push({ x: x, y: y, at: sec, f: 0.75 });
        if (trail.length > 20) trail.shift();
      }
      kick();
    }, { passive: true });

    hero.addEventListener("pointerdown", function (e) {
      if (reduced.matches || e.target.closest(".win, .nav, a, button, input")) return;
      var r = hero.getBoundingClientRect();
      rings.push({ x: e.clientX - r.left, y: e.clientY - r.top, at: (performance.now() - t0) / 1000 });
      if (rings.length > 4) rings.shift();
      kick();
    }, { passive: true });

    new IntersectionObserver(function (entries) {
      active = entries[0].isIntersecting;
      if (active) kick();
    }, { threshold: 0.02 }).observe(hero);
    document.addEventListener("visibilitychange", function () { if (!document.hidden) kick(); });
    window.addEventListener("resize", resize, { passive: true });
    resize();
    kick();
  })();

  // ---------------------------------------------------------------- language

  $$("[data-lang-pick]").forEach(function (a) {
    a.addEventListener("click", function () { store("sshyard-lang", a.getAttribute("data-lang-pick")); });
  });

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
    setTimeout(function () { banner.hidden = false; }, 1100);
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

  // ---------------------------------------------------------------- copy helper

  function copyText(text, onDone) {
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(text).then(onDone, function () {});
    } else {
      var ta = document.createElement("textarea");
      ta.value = text;
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      try { document.execCommand("copy"); onDone(); } catch (e) {}
      ta.remove();
    }
  }
  $$("[data-copy]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      copyText(btn.getAttribute("data-copy"), function () {
        btn.classList.add("done");
        btn.setAttribute("aria-label", data.copy.copied);
        btn.title = data.copy.copied;
        toast(data.copy.copied);
        setTimeout(function () {
          btn.classList.remove("done");
          btn.setAttribute("aria-label", data.copy.copy);
          btn.title = data.copy.copy;
        }, 1600);
      });
    });
  });

  // ---------------------------------------------------------------- reveal & spotlight

  var reveals = $$(".reveal");
  if ("IntersectionObserver" in window && !reduced.matches) {
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) {
        if (e.isIntersecting) { e.target.classList.add("in"); io.unobserve(e.target); }
      });
    }, { rootMargin: "0px 0px -6% 0px", threshold: 0.06 });
    reveals.forEach(function (el) { io.observe(el); });
  } else {
    reveals.forEach(function (el) { el.classList.add("in"); });
  }

  $$(".spot").forEach(function (el) {
    el.addEventListener("pointermove", function (e) {
      var r = el.getBoundingClientRect();
      el.style.setProperty("--mx", (e.clientX - r.left) + "px");
      el.style.setProperty("--my", (e.clientY - r.top) + "px");
    });
  });

  // ---------------------------------------------------------------- tabs helper

  function tabs(list, onSelect) {
    var items = $$('[role="tab"]', list);
    function select(i, focus) {
      items.forEach(function (t, j) {
        t.setAttribute("aria-selected", String(i === j));
        t.tabIndex = i === j ? 0 : -1;
      });
      updateSegPill(list);
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
    requestAnimationFrame(function () { updateSegPill(list); });
    return {
      select: select,
      count: items.length,
      current: function () { return items.findIndex(function (t) { return t.getAttribute("aria-selected") === "true"; }); },
    };
  }

  // ---------------------------------------------------------------- feature category filter

  (function featFilters() {
    var list = document.querySelector("[data-feat-filters]");
    var grid = document.querySelector("[data-feat-grid]");
    if (!list || !grid) return;
    var items = $$('[role="tab"]', list);
    var cards = $$(".feat", grid);
    tabs(list, function (i) {
      var cat = items[i].getAttribute("data-filter");
      grid.classList.toggle("filtered", cat !== "all");
      cards.forEach(function (card) {
        var show = cat === "all" || card.getAttribute("data-cat") === cat;
        card.classList.toggle("is-hidden", !show);
        if (show) {
          card.classList.add("in");
          card.classList.remove("pop-in");
          void card.offsetWidth;
          card.classList.add("pop-in");
        }
      });
    });
  })();

  // ---------------------------------------------------------------- screenshots tour & lightbox

  (function tour() {
    var root = document.querySelector(".tour");
    if (!root) return;
    var shots = $$(".shot", root);
    var list = root.querySelector(".tour-tabs");
    var ms = 7000;
    root.style.setProperty("--tour-ms", ms + "ms");
    shots.forEach(function (s, i) {
      if (i) s.setAttribute("aria-hidden", "true");
      var img = s.querySelector("img"), frame = s.querySelector(".shot-frame");
      if (img && frame) {
        if (img.complete && img.naturalWidth > 0) frame.classList.remove("loading");
        else img.addEventListener("load", function () { frame.classList.remove("loading"); }, { once: true });
      }
    });
    var t = tabs(list, function (i) {
      shots.forEach(function (s, j) {
        s.classList.toggle("on", i === j);
        if (i === j) s.removeAttribute("aria-hidden"); else s.setAttribute("aria-hidden", "true");
      });
      if (shots[i + 1]) { var img = shots[i + 1].querySelector("img"); if (img) img.loading = "eager"; }
    });
    var prevBtn = root.querySelector("[data-tour-prev]");
    var nextBtn = root.querySelector("[data-tour-next]");
    function step(delta) {
      t.select((t.current() + delta + t.count) % t.count, false);
      list.dispatchEvent(new CustomEvent("picked"));
    }
    if (prevBtn) prevBtn.addEventListener("click", function () { step(-1); });
    if (nextBtn) nextBtn.addEventListener("click", function () { step(1); });

    if (reduced.matches) return;
    var timer = null, visible = false, hover = false, stopped = false;
    function schedule() {
      clearTimeout(timer);
      root.classList.remove("playing");
      if (stopped || !visible || hover || document.hidden) { root.classList.toggle("paused", !stopped); return; }
      root.classList.remove("paused");
      void root.offsetWidth;
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

  (function lightbox() {
    var dlg = document.querySelector("[data-lightbox]");
    if (!dlg || typeof dlg.showModal !== "function") return;
    var img = dlg.querySelector("img");
    var imgs = $$("[data-zoom]");
    var cur = 0;
    function show(idx) {
      cur = (idx + imgs.length) % imgs.length;
      var z = imgs[cur];
      var src = (z.getAttribute("srcset") || "").split(",").pop().trim().split(" ")[0] || z.src;
      img.src = src;
      img.alt = z.alt;
    }
    imgs.forEach(function (z, idx) {
      z.tabIndex = 0;
      z.setAttribute("role", "button");
      function open() { show(idx); dlg.showModal(); }
      z.addEventListener("click", open);
      z.addEventListener("keydown", function (e) { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); open(); } });
    });
    var prev = dlg.querySelector("[data-lb-prev]");
    var next = dlg.querySelector("[data-lb-next]");
    if (prev) prev.addEventListener("click", function (e) { e.stopPropagation(); show(cur - 1); });
    if (next) next.addEventListener("click", function (e) { e.stopPropagation(); show(cur + 1); });
    dlg.addEventListener("keydown", function (e) {
      if (e.key === "ArrowLeft") { e.preventDefault(); show(cur - 1); }
      else if (e.key === "ArrowRight") { e.preventDefault(); show(cur + 1); }
    });
    dlg.addEventListener("click", function (e) { if (e.target === dlg || e.target === img) dlg.close(); });
  })();

  // ---------------------------------------------------------------- AI permission sandbox (3 modes × 3 risk levels)

  (function modes() {
    var list = document.querySelector(".mode-tabs");
    var card = document.querySelector("[data-approval]");
    if (!list || !card) return;
    var panels = $$(".mode-text");
    var keys = $$('[role="tab"]', list).map(function (b) { return b.getAttribute("data-mode"); });
    var scBtns = $$("[data-scenario]", card);
    var cmdEl = card.querySelector("[data-approval-cmd]");
    var outEl = card.querySelector("[data-approval-out]");
    var riskEl = card.querySelector("[data-risk-pill]");
    var revText = card.querySelector("[data-reviewed-text]");
    var scenarios = (data.aiDemo && data.aiDemo.scenarios) || [];
    var risks = (data.aiDemo && data.aiDemo.risks) || {};
    var scIdx = 1, timer = null;

    function step(s) { card.setAttribute("data-step", s); }
    function paintScenario() {
      var sc = scenarios[scIdx];
      if (!sc) return;
      card.setAttribute("data-risk", sc.risk);
      if (cmdEl) cmdEl.textContent = "$ " + sc.cmd;
      if (outEl) outEl.textContent = sc.out;
      if (riskEl && risks[sc.risk]) riskEl.textContent = risks[sc.risk];
      if (revText) revText.textContent = sc.auto;
      scBtns.forEach(function (b, i) { b.classList.toggle("on", i === scIdx); });
    }
    function play() {
      clearTimeout(timer);
      var m = card.getAttribute("data-mode");
      var sc = scenarios[scIdx] || { risk: "medium" };
      if (m === "ask") {
        step("wait");
      } else if (m === "review") {
        if (sc.risk === "readonly") {
          step("reviewed");
        } else if (sc.risk === "critical") {
          step("check");
          timer = setTimeout(function () { step("wait"); }, reduced.matches ? 0 : 750);
        } else {
          step("check");
          timer = setTimeout(function () { step("reviewed"); }, reduced.matches ? 0 : 1050);
        }
      } else {
        step("ran");
      }
    }
    tabs(list, function (i) {
      panels.forEach(function (p, j) { p.hidden = i !== j; });
      card.setAttribute("data-mode", keys[i]);
      play();
    });
    scBtns.forEach(function (b, i) {
      b.addEventListener("click", function () { scIdx = i; paintScenario(); play(); });
    });
    card.querySelector("[data-approval-allow]").addEventListener("click", function () { step("ok"); });
    card.querySelector("[data-approval-deny]").addEventListener("click", function () { step("denied"); });
    card.querySelector("[data-approval-replay]").addEventListener("click", play);
    paintScenario();
  })();

  // ---------------------------------------------------------------- command palette

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

  // ---------------------------------------------------------------- SSHYard v1.6.0 interactive demo window

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
    var winWorkspace = root.querySelector("[data-win-workspace]");
    var winSettings = root.querySelector("[data-win-settings-view]");
    var winBrand = root.querySelector("[data-win-brand]");
    var winBack = root.querySelector("[data-settings-back]");
    var winTabarea = root.querySelector("[data-tabarea]");
    var winSettingsBtn = root.querySelector("[data-win-settings]");
    var winToastEl = root.querySelector("[data-win-toast]");
    var metricPop = root.querySelector("[data-metric-pop]");
    var bootEl = root.querySelector("[data-win-boot]");

    function icon(name) {
      var n = tpl.querySelector('[data-i="' + name + '"] svg');
      return n ? n.cloneNode(true) : document.createTextNode("");
    }
    function fileIcon(name) {
      return (tpl.querySelector('svg[data-fi="' + name + '"]') || tpl.querySelector('svg[data-fi="file"]')).cloneNode(true);
    }
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

    var winToastTimer = 0;
    function winToast(msg, iconName) {
      if (!winToastEl) return;
      clearTimeout(winToastTimer);
      winToastEl.textContent = "";
      winToastEl.append(icon(iconName || "checkbox-circle-fill"), h("span", null, msg));
      winToastEl.hidden = false;
      winToastTimer = setTimeout(function () { winToastEl.hidden = true; }, 2400);
    }

    // ------------------------------------------------------------ privacy & terminal color schemes (internal/theme/schemes.go)

    var privacy = false;
    function maskAddr(s) {
      if (!privacy || !s) return s;
      return String(s).replace(/(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})/g, "$1.***.***.$4")
        .replace(/([a-z0-9-]+)\.([a-z0-9.-]+)/i, "$1.***");
    }
    function targetOf(x) {
      if (x.local) return "zsh · ~/sshyard";
      return x.user + "@" + maskAddr(x.addr);
    }

    var SCHEMES = [
      { id: "auto", name: T.settings_ui.scheme_auto, auto: true },
      { id: "dracula", name: "Dracula", bg: "#282A36", fg: "#F8F8F2", cur: "#F8F8F2", red: "#FF5555", green: "#50FA7B", yellow: "#F1FA8C", blue: "#BD93F9", purple: "#FF79C6", cyan: "#8BE9FD", dim: "#6272A4" },
      { id: "tokyo-night", name: "Tokyo Night", bg: "#1A1B26", fg: "#C0CAF5", cur: "#C0CAF5", red: "#F7768E", green: "#9ECE6A", yellow: "#E0AF68", blue: "#7AA2F7", purple: "#BB9AF7", cyan: "#7DCFFF", dim: "#565F89" },
      { id: "catppuccin-mocha", name: "Catppuccin Mocha", bg: "#1E1E2E", fg: "#CDD6F4", cur: "#F5E0DC", red: "#F38BA8", green: "#A6E3A1", yellow: "#F9E2AF", blue: "#89B4FA", purple: "#F5C2E7", cyan: "#94E2D5", dim: "#6C7086" },
      { id: "nord", name: "Nord", bg: "#2E3440", fg: "#D8DEE9", cur: "#D8DEE9", red: "#BF616A", green: "#A3BE8C", yellow: "#EBCB8B", blue: "#81A1C1", purple: "#B48EAD", cyan: "#88C0D0", dim: "#616E88" },
      { id: "one-dark", name: "One Dark", bg: "#282C34", fg: "#ABB2BF", cur: "#528BFF", red: "#E06C75", green: "#98C379", yellow: "#E5C07B", blue: "#61AFEF", purple: "#C678DD", cyan: "#56B6C2", dim: "#5C6370" },
      { id: "solarized-dark", name: "Solarized Dark", bg: "#002B36", fg: "#839496", cur: "#93A1A1", red: "#DC322F", green: "#859900", yellow: "#B58900", blue: "#268BD2", purple: "#D33682", cyan: "#2AA198", dim: "#586E75" },
      { id: "gruvbox-dark", name: "Gruvbox Dark", bg: "#282828", fg: "#EBDBB2", cur: "#EBDBB2", red: "#FB4934", green: "#B8BB26", yellow: "#FABD2F", blue: "#83A598", purple: "#D3869B", cyan: "#8EC07C", dim: "#928374" },
      { id: "monokai", name: "Monokai", bg: "#272822", fg: "#F8F8F2", cur: "#F8F8F0", red: "#F92672", green: "#A6E22E", yellow: "#F4BF75", blue: "#66D9EF", purple: "#AE81FF", cyan: "#A1EFE4", dim: "#75715E" },
      { id: "solarized-light", name: "Solarized Light", bg: "#FDF6E3", fg: "#657B83", cur: "#586E75", red: "#DC322F", green: "#859900", yellow: "#B58900", blue: "#268BD2", purple: "#D33682", cyan: "#2AA198", dim: "#93A1A1" },
      { id: "one-light", name: "One Light", bg: "#FAFAFA", fg: "#383A42", cur: "#526FFF", red: "#E45649", green: "#50A14F", yellow: "#C18401", blue: "#4078F2", purple: "#A626A4", cyan: "#0184BC", dim: "#A0A1A7" },
    ];
    var currentScheme = "auto";
    function applyScheme(id) {
      currentScheme = id;
      var sc = SCHEMES.filter(function (s) { return s.id === id; })[0];
      var props = ["--term-bg", "--term-fg", "--term-cursor", "--t-red", "--t-green", "--t-yellow", "--t-blue", "--t-purple", "--t-cyan", "--t-dim"];
      if (!sc || sc.auto) {
        props.forEach(function (p) { win.style.removeProperty(p); });
        win.removeAttribute("data-term-tone");
      } else {
        win.style.setProperty("--term-bg", sc.bg);
        win.style.setProperty("--term-fg", sc.fg);
        win.style.setProperty("--term-cursor", sc.cur);
        win.style.setProperty("--t-red", sc.red);
        win.style.setProperty("--t-green", sc.green);
        win.style.setProperty("--t-yellow", sc.yellow);
        win.style.setProperty("--t-blue", sc.blue);
        win.style.setProperty("--t-purple", sc.purple);
        win.style.setProperty("--t-cyan", sc.cyan);
        win.style.setProperty("--t-dim", sc.dim);
        var darkBg = sc.id !== "solarized-light" && sc.id !== "one-light";
        win.setAttribute("data-term-tone", darkBg ? "dark" : "light");
      }
      if (settingsOpen) renderSettings();
    }

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
      { id: "web-01", group: "prod", fav: true, icon: "server-line", tint: "green", user: "deploy", addr: "10.0.1.12", port: 22, jump: "bastion",
        os: "Ubuntu 24.04.1 LTS", kernel: "6.8.0-49-generic",
        cores: 2, mem: 1640, used: 1090, swap: 1024, disk: { size: "40G", used: "13G", avail: "25G", pct: 33 }, lat: 14, cpu: 3,
        up: "21 days,  3:02", load: [0.20, 0.10, 0.10], ifaces: ["eth0", "docker0", "br-3f61589b1e"],
        services: ["nginx", "php8.3-fpm", "ssh", "docker"],
        procs: [["www-data", 1412, "php-fpm8.3", 212, 1.4, 340], ["www-data", 905, "nginx", 48, 0.6, 120], ["root", 655, "dockerd", 41, 0.4, 80], ["redis", 1720, "redis-server", 19.6, 0.2, 42], ["root", 1, "systemd", 12, 0.0, 0]],
        containers: [
          { id: "3f2a9c1b7d40", image: "redis:7-alpine", cmd: "docker-entrypoint.sh", created: "2 weeks ago", status: "Up 2 weeks", running: true, ports: "6379/tcp", name: "app-redis",
            logs: "1:C 20 Nov 2026 09:12:04.112 * Ready to accept connections tcp\n1:M 20 Nov 2026 09:31:02.408 * 100 changes in 300 seconds. Saving...\n1:M 20 Nov 2026 09:31:02.419 * Background saving terminated with success" },
          { id: "9b1e04c2aa17", image: "prom/node-exporter:v1.8.2", cmd: "/bin/node_exporter", created: "2 weeks ago", status: "Up 2 weeks", running: true, ports: "9100/tcp", name: "node-exporter",
            logs: "ts=2026-11-20T09:00:01Z level=info msg=\"Starting node_exporter\" version=1.8.2\nts=2026-11-20T09:00:01Z level=info msg=\"Listening on\" address=[::]:9100" },
        ] },
      { id: "db-primary", group: "prod", fav: true, icon: "database-2-line", tint: "orange", user: "postgres", home: "/var/lib/postgresql", addr: "10.0.2.5", port: 22, jump: "bastion",
        os: "Debian GNU/Linux 12 (bookworm)", kernel: "6.1.0-27-amd64",
        cores: 4, mem: 7980, used: 4710, swap: 2048, disk: { size: "200G", used: "86G", avail: "104G", pct: 46 }, lat: 9, cpu: 12,
        up: "64 days, 11:47", load: [0.48, 0.52, 0.41], ifaces: ["eth0"],
        services: ["postgresql", "pgbouncer", "ssh"],
        procs: [["postgres", 812, "postgres", 2150, 8.4, 2400], ["postgres", 1530, "pgbouncer", 64, 1.1, 310], ["root", 702, "node_exporter", 22, 0.2, 18], ["root", 1, "systemd", 11, 0.0, 0]] },
      { id: "staging", group: "infra", fav: false, icon: "cloud-line", tint: "teal", user: "ubuntu", addr: "10.0.3.7", port: 22, jump: "",
        os: "Ubuntu 22.04.5 LTS", kernel: "5.15.0-126-generic",
        cores: 2, mem: 3890, used: 1420, swap: 0, disk: { size: "80G", used: "31G", avail: "46G", pct: 41 }, lat: 22, cpu: 6,
        up: "5 days,  7:15", load: [0.12, 0.08, 0.05], ifaces: ["eth0", "docker0"],
        services: ["nginx", "docker", "ssh", "redis-server"],
        procs: [["ubuntu", 2210, "node", 410, 3.2, 180], ["redis", 990, "redis-server", 96, 0.5, 65], ["root", 640, "dockerd", 58, 0.3, 40], ["root", 1, "systemd", 10, 0.0, 0]],
        containers: [
          { id: "c41d9e0f22ab", image: "app-web:staging", cmd: "node server.js", created: "5 days ago", status: "Up 5 days", running: true, ports: "3000->3000/tcp", name: "app-web",
            logs: "[info] app-web listening on :3000\n[http] GET /health 200 1.4ms" },
          { id: "71aa3be95c08", image: "postgres:16-alpine", cmd: "docker-entrypoint.sh", created: "5 days ago", status: "Up 5 days", running: true, ports: "5432/tcp", name: "app-db",
            logs: "LOG: database system is ready to accept connections" },
        ] },
      { id: "bastion", group: "infra", fav: false, icon: "shield-line", tint: "purple", user: "ops", addr: "203.0.113.10", port: 2222, jump: "",
        os: "Debian GNU/Linux 12 (bookworm)", kernel: "6.1.0-27-cloud-amd64",
        cores: 1, mem: 480, used: 160, swap: 0, disk: { size: "10G", used: "2.1G", avail: "7.4G", pct: 22 }, lat: 31, cpu: 1,
        up: "112 days,  2:09", load: [0.01, 0.02, 0.00], ifaces: ["ens3"],
        services: ["ssh", "fail2ban"],
        procs: [["root", 488, "fail2ban-server", 31, 0.2, 12], ["root", 512, "sshd", 9.1, 0.1, 4], ["root", 1, "systemd", 9, 0.0, 0]] },
    ];
    var byId = {};

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

    function initHost(x) {
      byId[x.id] = x;
      x.home = x.home || "/home/" + x.user;
      x.sessions = 0;
      x.coresPct = [];
      for (var c = 0; c < x.cores; c++) x.coresPct.push(clamp(x.cpu + (c % 2 ? 3 : -1), 1, 95));
      x.stat = { cpu: x.cpu, used: x.used, up: 437, down: 863, lat: x.lat, cpuHist: [], netHist: [] };
      for (var i = 0; i < 36; i++) { x.stat.cpuHist.push(x.cpu); x.stat.netHist.push(800); }
      x.fs = makeFS(x);
      x.filesCwd = x.home;
      x.log = h("div", "ai-log");
      return x;
    }

    // Local pretend filesystem for Dual-Pane SFTP (matching sftp-file-manager.webp)
    var localFS = D();
    put(localFS, "Users/dev/Projects/acme-app/deploy.sh", F("#!/usr/bin/env bash\nrsync -az ./dist/ deploy@web-01:/var/www/app/\n", { x: true }));
    put(localFS, "Users/dev/Projects/acme-app/nginx.conf", F("server {\n    listen 80;\n    root /var/www/app/public;\n}\n"));
    put(localFS, "Users/dev/Projects/acme-app/package.json", F('{\n  "name": "acme-app",\n  "version": "1.6.0"\n}\n'));
    put(localFS, "Users/dev/Projects/acme-app/README.md", F("# acme-app\n\nLocal workspace ready for SFTP upload.\n"));
    put(localFS, "Users/dev/.ssh/config", F("Host web-01\n    HostName 10.0.1.12\n    User deploy\n    ProxyJump bastion\n"));
    put(localFS, "Users/dev/.agents/skills/sshyard/SKILL.md", F("# SSHYard Skill\nConnected via local MCP endpoint.\n"));
    var localCwd = "/Users/dev/Projects/acme-app";

    function makeFS(x) {
      var t = D();
      ["bin", "boot", "dev", "opt", "proc", "root", "run", "srv", "tmp", "usr/bin", "usr/lib", "usr/local", "var/lib", "var/cache"].forEach(function (p) { put(t, p, D()); });
      put(t, "etc/hostname", F(x.id + "\n"));
      put(t, "etc/hosts", F("127.0.0.1 localhost\n" + x.addr + " " + x.id + "\n"));
      put(t, "etc/os-release", F('PRETTY_NAME="' + x.os + '"\nHOME_URL="https://www.' + (/Ubuntu/.test(x.os) ? "ubuntu.com" : "debian.org") + '/"\n'));
      put(t, "etc/ssh/sshd_config", F("Port " + (x.port || 22) + "\nPermitRootLogin no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\n"));
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
      } else if (x.id === "db-primary") {
        put(t, "etc/postgresql/16/main/postgresql.conf", F("listen_addresses = '10.0.2.5'\nmax_connections = 200\nshared_buffers = 2GB\nwal_level = replica\n"));
        put(t, "etc/postgresql/16/main/pg_hba.conf", F("# TYPE  DATABASE  USER  ADDRESS      METHOD\nhost    all       all   10.0.0.0/16  scram-sha-256\n"));
        put(t, "var/log/postgresql/postgresql-16-main.log", F("2026-11-20 09:00:00.012 UTC [812] LOG:  checkpoint starting: time\n2026-11-20 09:02:41.330 UTC [812] LOG:  checkpoint complete: wrote 1873 buffers (0.7%)\n"));
        put(t, x.home + "/16/main", D());
        put(t, x.home + "/backup.sh", F("#!/bin/sh\npg_dump -Fc app > /var/backups/app-$(date +%F).dump\n", { x: true }));
        put(t, "var/backups/app-2026-11-19.dump", F(null, { size: 48213004 }));
        put(t, "var/backups/app-2026-11-20.dump", F(null, { size: 48377196 }));
      } else if (x.id === "staging") {
        put(t, "etc/nginx/sites-enabled/staging.conf", F("server {\n    listen 80;\n    location / {\n        proxy_pass http://127.0.0.1:3000;\n    }\n}\n"));
        put(t, x.home + "/app/package.json", F('{\n  "name": "app-web",\n  "version": "2.4.0",\n  "scripts": { "start": "node server.js" }\n}\n'));
        put(t, x.home + "/app/server.js", F("import http from 'node:http';\n\nhttp.createServer((req, res) => res.end('ok')).listen(3000);\n"));
        put(t, x.home + "/app/docker-compose.yml", F("services:\n  web:\n    image: app-web:staging\n    ports: [\"3000:3000\"]\n  db:\n    image: postgres:16-alpine\n"));
        put(t, x.home + "/app/src/index.js", F("export * from './routes.js';\n"));
        put(t, x.home + "/app/README.md", F("# app-web\n\nStaging deploys from the main branch.\n"));
      } else if (x.id === "bastion") {
        put(t, x.home + "/.ssh/authorized_keys", F("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK7v… ops@laptop\n"));
        put(t, x.home + "/.ssh/config", F("Host *.internal\n    ProxyJump bastion\n"));
        put(t, x.home + "/README.md", F("# bastion\n\nJump host for 10.0.0.0/16. Use ProxyJump; nothing else runs here.\n"));
        put(t, "etc/fail2ban/jail.local", F("[sshd]\nenabled = true\nmaxretry = 5\nbantime = 1h\n"));
      } else {
        put(t, x.home + "/README.md", F("# " + x.id + "\n\nConnected via SSHYard.\n"));
      }
      return t;
    }

    HOSTS.forEach(initHost);
    var web = byId["web-01"];

    // Local shell pseudo-host
    var localHost = initHost({
      id: "local", local: true, icon: "terminal-window-line", tint: "blue", user: "dev", home: "/Users/dev", addr: "localhost", port: 0,
      os: "macOS 15.4 (arm64)", kernel: "Darwin 24.4.0", cores: 8, mem: 16384, used: 7200, swap: 0,
      disk: { size: "512G", used: "194G", avail: "318G", pct: 38 }, lat: 1, cpu: 4, up: "4 days, 12:18",
      load: [1.12, 1.04, 0.96], ifaces: ["en0"], services: [], procs: [["dev", 412, "sshyard", 48, 0.8, 12], ["dev", 901, "zsh", 6, 0.1, 0]],
    });

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
    function lookupTree(tree, path) {
      var node = tree;
      var parts = path.split("/").filter(Boolean);
      for (var i = 0; i < parts.length; i++) {
        if (!node.d || !Object.prototype.hasOwnProperty.call(node.c, parts[i])) return null;
        node = node.c[parts[i]];
      }
      return node;
    }
    function lookup(x, path) { return lookupTree(x.fs, path); }
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

    // ------------------------------------------------------------ terminal sessions

    var tabs = [];
    var current = null;
    var focused = false;
    var follow = true;
    var dualFiles = false;
    var broadcastSync = false;
    var broadcastSelected = {};
    var nextSid = 1;

    function tile(x, cls) {
      var t = h("span", "tile" + (cls ? " " + cls : ""), icon(x.icon || "server-line"));
      t.style.setProperty("--tint", "var(--tint-" + (x.tint || "blue") + ")");
      return t;
    }
    function meterCls(pct) { return pct >= 80 ? "m-danger" : pct >= 60 ? "m-warn" : "m-ok"; }

    function Session(x, el) {
      this.sid = nextSid++;
      this.host = x;
      this.cwd = x.home;
      this.old = x.home;
      this.hist = [];
      this.hi = 0;
      this.buf = "";
      this.caret = 0;
      this.busy = false;
      this.state = "connecting";
      this.finding = false;
      this.findQuery = "";
      this.findIdx = 0;
      if (el === false) { this.lines = []; this.state = "ready"; return; }
      this.el = el || h("section", "pane term", [h("div", "pane-head"), h("pre", "term-out"), h("div", "term-stats")]);
      this.el.setAttribute("data-pane", "");
      this.out = this.el.querySelector(".term-out");
      this.out.textContent = "";
      this.head = this.el.querySelector(".pane-head");
      this.findBar = h("div", "term-find");
      this.findBar.hidden = true;
      this.banner = h("div", "pane-banner");
      this.banner.hidden = true;
      this.el.insertBefore(this.findBar, this.out);
      this.el.insertBefore(this.banner, this.el.querySelector(".term-stats"));
      this.buildFind();
      this.buildHead();
      this.buildStats();
      var s = this;
      this.el.addEventListener("click", function (e) {
        if (e.target.closest(".pane-icons, .term-find, .term-stats, .pane-banner")) return;
        var sel = window.getSelection && window.getSelection();
        if (sel && !sel.isCollapsed && s.out.contains(sel.anchorNode)) return;
        focusSession(s, true);
      });
    }
    Session.prototype.buildFind = function () {
      var s = this;
      var q = h("input");
      q.type = "search";
      q.placeholder = T.find_placeholder;
      q.setAttribute("aria-label", T.find_placeholder);
      this.findInput = q;
      this.findCount = h("span", "term-find-count");
      q.addEventListener("input", function () { s.findQuery = q.value; s.findIdx = 0; s.applyFind(); });
      q.addEventListener("keydown", function (e) {
        if (e.key === "Escape") { s.toggleFind(false); focusSession(s, true); }
        else if (e.key === "Enter") { e.preventDefault(); s.stepFind(e.shiftKey ? -1 : 1); }
      });
      this.findBar.append(icon("search-line"), q, this.findCount,
        btn("arrow-up-s-line", "Previous", function () { s.stepFind(-1); }, "wbtn-xs"),
        btn("arrow-down-s-line", "Next", function () { s.stepFind(1); }, "wbtn-xs"),
        btn("close-line", T.tools.close, function () { s.toggleFind(false); }, "wbtn-xs"));
    };
    Session.prototype.toggleFind = function (on) {
      this.finding = on == null ? !this.finding : on;
      this.findBar.hidden = !this.finding;
      if (this.findBtn) this.findBtn.classList.toggle("on", this.finding);
      if (this.finding) { this.findInput.focus(); this.findInput.select(); this.applyFind(); }
      else { this.findQuery = ""; this.findInput.value = ""; this.clearFindMarks(); }
    };
    Session.prototype.clearFindMarks = function () {
      $$("mark.term-hit", this.out).forEach(function (m) { m.replaceWith(document.createTextNode(m.textContent)); });
      this.out.normalize();
    };
    Session.prototype.applyFind = function () {
      this.clearFindMarks();
      var q = this.findQuery.trim();
      if (!q) { this.findCount.textContent = ""; return; }
      var ql = q.toLowerCase(), hits = [];
      var walker = document.createTreeWalker(this.out, NodeFilter.SHOW_TEXT);
      var nodes = [], n;
      while ((n = walker.nextNode())) {
        if (this.live && this.live.contains(n)) continue;
        if (n.nodeValue.toLowerCase().indexOf(ql) >= 0) nodes.push(n);
      }
      nodes.forEach(function (tn) {
        var text = tn.nodeValue, low = text.toLowerCase(), frag = document.createDocumentFragment(), idx = 0, pos;
        while ((pos = low.indexOf(ql, idx)) >= 0) {
          if (pos > idx) frag.appendChild(document.createTextNode(text.slice(idx, pos)));
          var m = h("mark", "term-hit", text.slice(pos, pos + q.length));
          hits.push(m);
          frag.appendChild(m);
          idx = pos + q.length;
        }
        if (idx < text.length) frag.appendChild(document.createTextNode(text.slice(idx)));
        tn.parentNode.replaceChild(frag, tn);
      });
      this.findHits = hits;
      if (!hits.length) { this.findCount.textContent = T.find_none; return; }
      this.findIdx = (this.findIdx + hits.length) % hits.length;
      hits[this.findIdx].classList.add("cur");
      hits[this.findIdx].scrollIntoView({ block: "nearest" });
      this.findCount.textContent = (this.findIdx + 1) + "/" + hits.length;
    };
    Session.prototype.stepFind = function (d) {
      if (!this.findHits || !this.findHits.length) return;
      this.findHits[this.findIdx].classList.remove("cur");
      this.findIdx = (this.findIdx + d + this.findHits.length) % this.findHits.length;
      this.findHits[this.findIdx].classList.add("cur");
      this.findHits[this.findIdx].scrollIntoView({ block: "nearest" });
      this.findCount.textContent = (this.findIdx + 1) + "/" + this.findHits.length;
    };
    Session.prototype.buildHead = function () {
      var s = this, x = this.host;
      this.head.textContent = "";
      this.dot = h("i", "dot");
      this.targetEl = h("span", "muted", targetOf(x));
      this.syncMark = h("span", "pane-sync-ic", icon("keyboard-box-fill"));
      this.syncMark.hidden = true;
      var iconsRow = h("span", "pane-icons", [
        this.findBtn = btn("search-line", T.find, function () { focusSession(s, false); s.toggleFind(); }, "wbtn-xs"),
        x.local ? null : btn("folder-3-line", T.tools.files, function () { focusSession(s, false); x.filesCwd = s.cwd; x.preview = null; setTool("files", true); }, "wbtn-xs"),
        this.splitBtn = btn("layout-column-line", T.split, function () { split(s, "row"); }, "wbtn-xs"),
        this.splitDownBtn = btn("layout-row-line", T.split_down, function () { split(s, "col"); }, "wbtn-xs"),
        btn("close-line", T.close_pane, function () { closePane(s); }, "wbtn-xs"),
      ]);
      this.head.append(this.dot, tile(x, "tile-sm"), h("strong", null, x.local ? T.local_terminal : x.id),
        x.temp ? h("span", "pane-badge", T.temporary) : null, this.syncMark, this.targetEl, iconsRow);
    };
    Session.prototype.paintBanner = function () {
      var s = this, x = this.host;
      this.banner.textContent = "";
      this.banner.classList.remove("err");
      if (this.state === "connecting") {
        this.banner.hidden = false;
        this.banner.append(h("i", "ui-spin"), h("span", null, fill(T.connecting, { host: x.id })));
      } else if (this.state === "closed") {
        this.banner.hidden = false;
        this.banner.classList.add("err");
        this.banner.append(icon("error-warning-line"), h("span", null, T.disconnected),
          btn("restart-line", T.reconnect, function () { s.host.sessions++; connect(s, true); }, "wbtn-xs"));
      } else {
        this.banner.hidden = true;
      }
    };
    Session.prototype.buildStats = function () {
      var bar = this.el.querySelector(".term-stats");
      bar.textContent = "";
      if (this.host.local) { bar.hidden = true; return; }
      this.st = {};
      var s = this;
      function mkBtn(key, ic, popFn) {
        var b = h("b");
        var el = h(popFn ? "button" : "span", key === "lat" ? "lat" : null, [icon(ic), b]);
        if (popFn) {
          el.type = "button";
          el.addEventListener("click", function (e) { e.stopPropagation(); openMetricPop(el, s.host, key, popFn); });
        }
        s.st[key] = { wrap: el, val: b };
        bar.append(el);
      }
      mkBtn("cpu", "cpu-line", "cpu");
      mkBtn("mem", "ram-2-line", null);
      mkBtn("disk", "hard-drive-3-line", "disk");
      var upB = h("b"), dnB = h("b");
      var netEl = h("button", "m-net", [icon("arrow-up-line"), upB, icon("arrow-down-line"), dnB]);
      netEl.type = "button";
      netEl.addEventListener("click", function (e) { e.stopPropagation(); openMetricPop(netEl, s.host, "net", "net"); });
      s.st.net = { wrap: netEl, up: upB, down: dnB };
      bar.append(netEl);
      mkBtn("load", "speed-up-line", "load");
      mkBtn("lat", "pulse-line", null);
      this.paintStats();
    };
    Session.prototype.paintStats = function () {
      if (!this.st) return;
      var x = this.host, st = x.stat, on = this.state === "ready";
      var memPct = st.used / x.mem * 100;
      var ldPct = Math.round(((st.load || x.load)[0] / x.cores) * 100);
      this.st.cpu.val.textContent = on ? Math.round(st.cpu) + "%" : "–";
      this.st.cpu.wrap.className = on ? meterCls(st.cpu) : "m-dim";
      this.st.mem.val.textContent = on ? (st.used / 1024).toFixed(1) + "G/" + (x.mem / 1024).toFixed(1) + "G" : "–";
      this.st.mem.wrap.className = on ? meterCls(memPct) : "m-dim";
      this.st.disk.val.textContent = on ? x.disk.pct + "%" : "–";
      this.st.disk.wrap.className = on ? meterCls(x.disk.pct) : "m-dim";
      this.st.net.up.textContent = on ? rate(st.up) : "–";
      this.st.net.down.textContent = on ? rate(st.down) : "–";
      this.st.net.wrap.className = on ? "m-net" : "m-dim";
      this.st.load.val.textContent = on ? ldPct + "%" : "–";
      this.st.load.wrap.className = on ? meterCls(ldPct) : "m-dim";
      this.st.lat.val.textContent = on ? Math.round(st.lat) + " ms" : "–";
      this.st.lat.wrap.className = "lat " + (on ? (st.lat >= 100 ? "m-warn" : "m-ok") : "m-dim");
      this.el.classList.toggle("off", !on);
    };
    Session.prototype.promptSegs = function () {
      if (this.host.local) return [["dev@macbook", "t-cyan"], [":"], [tilde(this.host, this.cwd), "t-blue"], [" % "]];
      return [[this.host.user + "@" + this.host.id, "t-ok"], [":"], [tilde(this.host, this.cwd), "t-blue"], ["$ "]];
    };
    function segsNode(cls, segs) {
      var l = h("span", "tl" + (cls ? " " + cls : ""));
      segs.forEach(function (sg) { l.append(sg[1] ? h("span", sg[1], sg[0]) : sg[0]); });
      return l;
    }
    Session.prototype.print = function (line, cls) {
      if (this.lines) { this.lines.push(typeof line === "string" ? line : line.map(function (s) { return s[0]; }).join("")); return; }
      var l = segsNode(cls, typeof line === "string" ? [[line]] : line);
      if (this.live && this.live.parentNode) this.out.insertBefore(l, this.live); else this.out.appendChild(l);
      while (this.out.childNodes.length > 400) this.out.removeChild(this.out.firstChild);
      this.scroll();
    };
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

    // Metric popovers (CPU cores, Disk mounts, NIC rates, Load 1/5/15m)
    var popOwner = null;
    function closeMetricPop() {
      if (popOwner) popOwner.classList.remove("on");
      popOwner = null;
      if (metricPop) metricPop.hidden = true;
    }
    function openMetricPop(anchor, x, key) {
      if (!metricPop) return;
      if (popOwner === anchor && !metricPop.hidden) { closeMetricPop(); return; }
      closeMetricPop();
      popOwner = anchor;
      anchor.classList.add("on");
      metricPop.textContent = "";
      var M = T.monitor;
      if (key === "cpu") {
        metricPop.append(h("strong", null, M.cores_title));
        x.coresPct.forEach(function (pct, idx) {
          var barEl = h("span", "mpop-bar", h("i"));
          barEl.firstChild.style.width = clamp(pct, 2, 100) + "%";
          barEl.firstChild.style.background = pct >= 80 ? "var(--danger)" : pct >= 60 ? "var(--warning)" : "var(--success)";
          metricPop.append(h("div", "mpop-row", [h("span", null, "Core " + idx + "  " + Math.round(pct) + "%"), barEl]));
        });
      } else if (key === "disk") {
        metricPop.append(h("strong", null, M.disk),
          h("div", "mpop-row", [h("span", null, "/"), h("span", "t-ok", x.disk.used + " / " + x.disk.size + " (" + x.disk.pct + "%)")]),
          h("div", "mpop-row", [h("span", "muted", "/dev/shm"), h("span", "muted", "0M / " + Math.round(x.mem / 2) + "M (0%)")]));
      } else if (key === "net") {
        metricPop.append(h("strong", null, M.network));
        x.ifaces.forEach(function (ni, idx) {
          metricPop.append(h("div", "mpop-row", [h("span", null, ni),
            h("span", "t-blue", "↑" + (idx ? "0B/s" : rate(x.stat.up)) + " ↓" + (idx ? "0B/s" : rate(x.stat.down)))]));
        });
      } else if (key === "load") {
        var l = x.stat.load || x.load;
        metricPop.append(h("strong", null, M.load),
          h("div", "mpop-row", [h("span", "muted", "1 min"), h("span", null, l[0].toFixed(2))]),
          h("div", "mpop-row", [h("span", "muted", "5 min"), h("span", null, l[1].toFixed(2))]),
          h("div", "mpop-row", [h("span", "muted", "15 min"), h("span", null, l[2].toFixed(2))]));
      }
      metricPop.hidden = false;
      var wr = win.getBoundingClientRect(), ar = anchor.getBoundingClientRect();
      metricPop.style.left = clamp(ar.left - wr.left, 8, Math.max(8, wr.width - metricPop.offsetWidth - 12)) + "px";
      metricPop.style.bottom = (wr.bottom - ar.top + 6) + "px";
    }
    document.addEventListener("click", function (e) {
      if (metricPop && !metricPop.hidden && !metricPop.contains(e.target) && !e.target.closest(".term-stats button")) closeMetricPop();
    });

    function sess() { return current ? current.panes[current.focus] : null; }
    function allPanes() {
      var out = [];
      tabs.forEach(function (t) { t.panes.forEach(function (p) { out.push(p); }); });
      return out;
    }

    async function connect(s, again) {
      var x = s.host;
      s.state = "connecting";
      s.paintStats();
      s.paintBanner();
      paintChrome();
      if (!x.local) {
        s.print(fill(T.connecting, { host: x.id }), "t-dim");
        await sleep(again ? 520 : 480 + x.lat * 10);
      }
      if (s.closed) return;
      s.state = "ready";
      x.connectedAt = x.connectedAt || Date.now();
      if (x.local) {
        s.print("Last login: " + new Date().toUTCString().replace(/ GMT$/, "") + " on ttys004", "t-dim");
      } else {
        s.print("Welcome to " + x.os + " (GNU/Linux " + x.kernel + " x86_64)", "t-dim");
        s.print("Last login: " + new Date(Date.now() - 3600e3 * 19).toUTCString().replace(/ GMT$/, "") + " from 10.0.0.8", "t-dim");
      }
      s.paintStats();
      s.paintBanner();
      s.showLive();
      paintChrome();
      if (tool && sess() && sess().host === x) renderTool();
    }
    function disconnect(s) {
      s.state = "closed";
      s.host.sessions = Math.max(0, s.host.sessions - 1);
      s.hideLive();
      s.print("Connection to " + maskAddr(s.host.addr) + " closed.");
      s.paintStats();
      s.paintBanner();
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
        paintStatusCwd();
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
        var src = r.lines.map(textOf), text = src.length ? src.join("\n") + "\n" : "";
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
          return pad(r[0], 9) + lpad(r[1], 7) + lpad((r[4] || 0.1).toFixed(1), 5) + lpad((r[3] / x.mem * 100).toFixed(1), 5) +
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
        x.procs.forEach(function (r) { out.push(lpad(r[1], 7) + " " + pad(r[0], 9) + lpad((r[4] || 0.2).toFixed(1), 5) + lpad((r[3] / x.mem * 100).toFixed(1), 6) + " " + r[2]); });
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
        if (x.services.indexOf(unit) < 0) return { lines: [(sub === "status" ? "Unit " : "Failed to " + sub + " ") + unit + ".service" + (sub === "status" ? " could not be found." : ": Unit " + unit + ".service not found.")], code: 4 };
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
        if (sub === "ps") return table([["CONTAINER ID", "IMAGE", "STATUS", "PORTS", "NAMES"]].concat(x.containers.filter(function (ct) { return ct.running || c.argv.indexOf("-a") >= 0; }).map(function (ct) { return [ct.id, ct.image, ct.status, ct.ports, ct.name]; })));
        if (sub === "images") return table([["REPOSITORY", "TAG", "IMAGE ID", "SIZE"]].concat(x.containers.map(function (ct) { var i = ct.image.split(":"); return [i[0], i[1] || "latest", ct.id.slice(2, 14), "41.2MB"]; })));
        if (sub === "exec") return [["Attached to container " + (c.argv[2] || "shell") + " (demo shell ready)", "t-cyan"]];
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
          await sleep(k === 1 ? 120 : 480);
          if (c.s.abort) { c.print("^C"); break; }
          var t = base + Math.random() * base * 0.2;
          times.push(t);
          c.print("64 bytes from " + ip + ": icmp_seq=" + k + " ttl=" + (target === "localhost" ? 64 : 56) + " time=" + t.toFixed(t < 1 ? 3 : 1) + " ms");
        }
        var min = Math.min.apply(null, times), max = Math.max.apply(null, times), avg = times.reduce(function (a, b) { return a + b; }, 0) / (times.length || 1);
        c.print("");
        c.print("--- " + target + " ping statistics ---");
        c.print(times.length + " packets transmitted, " + times.length + " received, 0% packet loss");
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
          "2: " + c.x.ifaces[0] + ": <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 state UP", "    inet " + maskAddr(c.x.addr) + "/24 scope global " + c.x.ifaces[0]];
      },
      neofetch: function (c) {
        var x = c.x, st = x.stat, title = x.user + "@" + x.id;
        var rows = [["OS", x.os + " x86_64"], ["Kernel", x.kernel], ["Uptime", x.up.replace(/,\s+(\d+):(\d+)/, ", $1 hours, $2 mins")],
          ["Shell", x.local ? "zsh 5.9" : "bash 5.2.21"], ["CPU", "Intel Xeon (" + x.cores + ") @ 2.40GHz"], ["Memory", Math.round(st.used) + "MiB / " + x.mem + "MiB"]];
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
    async function submit(s, fromSync) {
      var line = s.buf;
      s.setBuf("");
      if (s.state === "closed") { s.host.sessions++; await connect(s, true); return; }
      if (s.state !== "ready" || s.busy) return;
      if (!fromSync && broadcastSync && broadcastSelected[s.sid]) {
        allPanes().forEach(function (other) {
          if (other !== s && broadcastSelected[other.sid] && other.state === "ready" && !other.busy) {
            other.setBuf(line);
            submit(other, true);
          }
        });
      }
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
    async function typeInto(s, text, fast) {
      while (s.busy || s.state === "connecting") await sleep(120);
      if (s.state !== "ready") return;
      function quick() { return typeof fast === "function" ? fast() : fast; }
      for (var i = 1; i <= text.length; i++) {
        if (quick()) { s.setBuf(text); break; }
        s.setBuf(text.slice(0, i));
        await sleep(24 + Math.random() * 44);
      }
      if (!quick()) await sleep(220);
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

    // ------------------------------------------------------------ tabs & welcome

    function newSession(x, el) {
      var s = new Session(x, el);
      x.sessions++;
      broadcastSelected[s.sid] = true;
      return s;
    }
    function openTab(x, quiet) {
      if (settingsOpen) toggleSettings(false);
      if (tabs.length >= 6) { activate(tabs[tabs.length - 1]); return tabs[tabs.length - 1]; }
      var s = newSession(x);
      var tab = { panes: [s], focus: 0, dir: "row" };
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
      termsEl.classList.toggle("vertical", !!(tab && tab.dir === "col"));
      if (tab) {
        tab.panes.forEach(function (s) { termsEl.appendChild(s.el); s.scroll(); });
        var s = sess();
        input.value = s.buf;
      } else {
        var wel = h("div", "win-welcome", [
          Object.assign(h("img"), { src: T.logo, width: 56, height: 56, alt: "" }),
          h("h3", null, T.welcome_title),
          h("p", null, T.welcome_hint),
          h("div", "win-welcome-acts", [
            (function () { var b = h("button", "btn btn-primary btn-sm", [icon("server-line"), h("span", null, "web-01")]); b.type = "button"; b.addEventListener("click", function () { openTab(web); }); return b; })(),
            (function () { var b = h("button", "btn btn-quiet btn-sm", [icon("flashlight-line"), h("span", null, T.quick_connect)]); b.type = "button"; b.addEventListener("click", function () { toggleQuick(true); }); return b; })(),
            (function () { var b = h("button", "btn btn-quiet btn-sm", [icon("terminal-window-line"), h("span", null, T.local_terminal)]); b.type = "button"; b.addEventListener("click", function () { openTab(localHost); }); return b; })(),
          ]),
        ]);
        termsEl.appendChild(wel);
      }
      paintChrome();
      renderTool();
    }
    function closeTab(tab) {
      var i = tabs.indexOf(tab);
      if (i < 0) return;
      tab.panes.forEach(function (s) { s.closed = true; delete broadcastSelected[s.sid]; if (s.state !== "closed") s.host.sessions = Math.max(0, s.host.sessions - 1); });
      tabs.splice(i, 1);
      if (tab === current) activate(tabs[Math.min(i, tabs.length - 1)] || null);
      else paintChrome();
    }
    function split(s, dir) {
      var tab = tabOf(s);
      if (!tab || tab.panes.length >= 2) return;
      tab.dir = dir || "row";
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
      delete broadcastSelected[s.sid];
      if (s.state !== "closed") s.host.sessions = Math.max(0, s.host.sessions - 1);
      tab.panes.splice(tab.panes.indexOf(s), 1);
      tab.focus = 0;
      tab.dir = "row";
      activate(tab);
    }
    function focusSession(s, keyboard) {
      var tab = tabOf(s);
      if (!tab) return;
      if (tab !== current) activate(tab);
      var prev = sess() && sess().host;
      tab.focus = tab.panes.indexOf(s);
      input.value = s.buf;
      paintFocus();
      paintStatusCwd();
      if (prev !== s.host) { paintChrome(); renderTool(); }
      if (keyboard) {
        if (story.running && s.host === web) story.fast = true;
        input.focus({ preventScroll: true });
        try { input.setSelectionRange(s.caret, s.caret); } catch (e) {}
      }
    }
    function paintFocus() {
      tabs.forEach(function (t) {
        t.panes.forEach(function (s, i) {
          var isFocused = t === current && i === t.focus;
          var synced = broadcastSync && !!broadcastSelected[s.sid];
          s.el.classList.toggle("focused", isFocused);
          s.el.classList.toggle("synced", synced);
          if (s.syncMark) s.syncMark.hidden = !synced;
        });
      });
      win.classList.toggle("typing", focused);
    }

    // ------------------------------------------------------------ sidebar, tabs, status bar, resizers

    var hostsWrap = root.querySelector("[data-hosts-wrap]");
    var hostsEmpty = root.querySelector("[data-hosts-empty]");
    var search = root.querySelector("[data-host-search]");
    var sbBar = root.querySelector("[data-sb-bar]");
    var sbSearchBox = root.querySelector("[data-sb-search-box]");
    var sbConns = root.querySelector("[data-sb-conns]");
    var sbCount = root.querySelector("[data-sb-count]");
    var sbShowBtn = root.querySelector("[data-sb-show]");
    var stHost = root.querySelector("[data-st-host]");
    var stTime = root.querySelector("[data-st-time] span");
    var stCwd = root.querySelector("[data-st-cwd]");
    var stSessions = root.querySelector("[data-st-sessions] span");
    var foldedGroups = {};
    var pinging = false;

    function openSbSearch(open) {
      sbBar.hidden = open;
      sbSearchBox.hidden = !open;
      if (open) { search.focus({ preventScroll: true }); }
      else { search.value = ""; paintHosts(); }
    }
    root.querySelector("[data-sb-search-open]").addEventListener("click", function () { openSbSearch(true); });
    root.querySelector("[data-sb-search-close]").addEventListener("click", function () { openSbSearch(false); });
    search.addEventListener("keydown", function (e) { if (e.key === "Escape") openSbSearch(false); });
    search.addEventListener("input", paintHosts);

    // Sidebar latency ping test with authentic loading state
    var pingBtn = root.querySelector("[data-sb-ping]");
    pingBtn.addEventListener("click", async function () {
      if (pinging) return;
      pinging = true;
      var ic = pingBtn.querySelector(".i");
      ic.classList.remove("spin"); void ic.offsetWidth; ic.classList.add("spin");
      pingBtn.classList.add("on");
      HOSTS.forEach(function (x) { x.testing = true; });
      paintHosts();
      for (var i = 0; i < HOSTS.length; i++) {
        await sleep(reduced.matches ? 40 : 180 + Math.random() * 140);
        HOSTS[i].testing = false;
        HOSTS[i].stat.lat = clamp(HOSTS[i].lat + (Math.random() - 0.5) * 6, 4, 85);
        paintHosts();
      }
      pinging = false;
      pingBtn.classList.remove("on");
    });

    // Sidebar hide / show
    function setSidebarHidden(hide) {
      win.classList.toggle("sb-hidden", hide);
      if (sbShowBtn) sbShowBtn.hidden = !hide;
    }
    root.querySelector("[data-sb-hide]").addEventListener("click", function () { setSidebarHidden(true); });
    sbShowBtn.addEventListener("click", function () { setSidebarHidden(false); });
    root.querySelector("[data-sb-add]").addEventListener("click", function () { toggleQuick(true); });

    // Draggable sidebar resizer & pane divider
    (function resizers() {
      var sbR = root.querySelector("[data-sb-resize]");
      var pR = root.querySelector("[data-pane-resize]");
      if (sbR) {
        sbR.addEventListener("pointerdown", function (e) {
          e.preventDefault();
          sbR.classList.add("dragging");
          var startX = e.clientX, startW = parseFloat(getComputedStyle(root).getPropertyValue("--sb-w")) || 214;
          function move(ev) { root.style.setProperty("--sb-w", clamp(startW + ev.clientX - startX, 168, 320) + "px"); }
          function up() { sbR.classList.remove("dragging"); window.removeEventListener("pointermove", move); window.removeEventListener("pointerup", up); }
          window.addEventListener("pointermove", move);
          window.addEventListener("pointerup", up);
        });
      }
      if (pR) {
        var mainEl = root.querySelector("[data-main]");
        pR.addEventListener("pointerdown", function (e) {
          e.preventDefault();
          pR.classList.add("dragging");
          function move(ev) {
            var r = mainEl.getBoundingClientRect();
            var ratio = clamp((ev.clientX - r.left) / Math.max(1, r.width), 0.32, 0.72);
            mainEl.style.setProperty("--term-flex", ratio.toFixed(3));
            mainEl.style.setProperty("--tool-flex", (1 - ratio).toFixed(3));
          }
          function up() { pR.classList.remove("dragging"); window.removeEventListener("pointermove", move); window.removeEventListener("pointerup", up); }
          window.addEventListener("pointermove", move);
          window.addEventListener("pointerup", up);
        });
      }
    })();

    function hostRow(x, onPick, on, inSidebar) {
      var av = h("span", "host-av", [tile(x)]);
      var st = hostConnState(x);
      if (st) av.append(h("i", "av-dot" + (st === "connecting" ? " connecting" : "")));
      var kids = [av, h("span", "host-name", x.id)];
      if (inSidebar) {
        var latTxt = x.testing ? "…" : Math.round(x.stat.lat) + " ms";
        kids.push(h("span", "host-lat" + (x.testing ? " testing" : x.stat.lat >= 80 ? " warn" : ""), latTxt));
      } else if (st) {
        kids.push(h("i", "dot" + (st === "connecting" ? " connecting" : "")));
      }
      var b = h("button", "host" + (on ? " on" : ""), kids);
      b.type = "button";
      b.addEventListener("click", function () { onPick(x); });
      var li = h("li", inSidebar ? "host-item" : null, b);
      if (inSidebar) {
        var acts = h("span", "host-acts", [
          btn("folder-3-line", T.tools.files, function (e) {
            e.stopPropagation();
            var t = tabs.filter(function (t2) { return t2.panes[0].host === x; })[0];
            if (t) activate(t); else openTab(x, true);
            setTool("files", true);
          }, "wbtn-xs"),
          btn("terminal-box-line", T.new_tab, function (e) { e.stopPropagation(); openTab(x); }, "wbtn-xs"),
        ]);
        li.append(acts);
      }
      return li;
    }
    function hostConnState(x) {
      var ps = allPanes().filter(function (p) { return p.host === x; });
      if (ps.some(function (p) { return p.state === "ready"; })) return "ready";
      if (ps.some(function (p) { return p.state === "connecting"; })) return "connecting";
      return null;
    }

    function paintStatusCwd() {
      var s = sess();
      if (!stCwd) return;
      stCwd.hidden = !s || s.state !== "ready";
      if (s) stCwd.querySelector("span").textContent = tilde(s.host, s.cwd);
    }

    function paintChrome() {
      var s = sess(), x = s && s.host;
      tabsEl.textContent = "";
      tabs.forEach(function (t) {
        var first = t.panes[0];
        var live = t.panes.some(function (p) { return p.state === "ready"; });
        var conn = t.panes.some(function (p) { return p.state === "connecting"; });
        var extra = (t.panes.length - 1) + (t === current && tool ? 1 : 0);
        var tabLabel = (first.host.local ? T.local_terminal : first.host.id) + (extra > 0 ? " +" + extra : "");
        var close = btn("close-line", T.close_tab, function (e) { e.stopPropagation(); closeTab(t); }, "wtab-close");
        var aiBusy = first.host.aiBusy || (story.running && first.host === web);
        var synced = broadcastSync && t.panes.some(function (p) { return broadcastSelected[p.sid]; });
        var el = h("div", "wtab" + (t === current ? " on" : ""), [
          tile(first.host),
          h("span", "wtab-name", tabLabel),
          h("i", "dot" + (live ? "" : conn ? " connecting" : " off")),
          aiBusy ? h("span", "wtab-mark pulse", icon("robot-2-line")) : null,
          synced ? h("span", "wtab-mark", icon("keyboard-box-fill")) : null,
          close,
        ]);
        el.setAttribute("role", "tab");
        el.setAttribute("aria-selected", String(t === current));
        el.tabIndex = 0;
        el.addEventListener("click", function () { if (settingsOpen) toggleSettings(false); if (t !== current) activate(t); });
        el.addEventListener("keydown", function (e) { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); activate(t); } });
        tabsEl.appendChild(el);
      });
      tabsEl.setAttribute("role", "tablist");
      var on = tabsEl.querySelector(".wtab.on");
      if (on) {
        var oL = on.offsetLeft, oR = oL + on.offsetWidth;
        if (oL < tabsEl.scrollLeft) tabsEl.scrollLeft = oL;
        else if (oR > tabsEl.scrollLeft + tabsEl.clientWidth) tabsEl.scrollLeft = oR - tabsEl.clientWidth;
      }
      paintHosts();
      tabs.forEach(function (t) {
        t.panes.forEach(function (p) {
          p.dot.className = "dot" + (p.state === "ready" ? "" : p.state === "connecting" ? " connecting" : " off");
          p.targetEl.textContent = targetOf(p.host);
          p.splitBtn.hidden = t.panes.length > 1;
          p.splitDownBtn.hidden = t.panes.length > 1;
        });
      });
      stHost.textContent = "";
      if (x) {
        stHost.append(
          h("i", "dot" + (s.state === "ready" ? "" : s.state === "connecting" ? " connecting" : " off")),
          h("b", null, x.local ? T.local_terminal : x.id),
          h("span", "muted", targetOf(x))
        );
      }
      var n = allPanes().filter(function (p) { return p.state !== "closed"; }).length;
      stSessions.textContent = n === 1 ? T.session_one : fill(T.session_many, { n: n });
      var uniqConns = {};
      allPanes().forEach(function (p) { if (p.state === "ready" && !p.host.local) uniqConns[p.host.id] = 1; });
      var nc = Object.keys(uniqConns).length;
      if (sbConns) {
        sbConns.classList.toggle("zero", nc === 0);
        sbConns.querySelector("span").textContent = nc === 1 ? T.connected_one : fill(T.connected_many, { n: nc });
      }
      if (sbCount) sbCount.textContent = T.hosts;
      paintTime();
      paintStatusCwd();
      paintFocus();
      $$("[data-rail]", root).forEach(function (b) { b.disabled = !current; });
      toolPane.hidden = !tool || !current;
      win.classList.toggle("has-tool", !toolPane.hidden);
      syncPresets();
    }

    function paintHosts() {
      var q = search.value.trim().toLowerCase(), x = sess() && sess().host;
      hostsWrap.textContent = "";
      var matched = HOSTS.filter(function (y) { return (y.id + " " + y.addr).toLowerCase().indexOf(q) >= 0; });
      if (!matched.length) {
        hostsWrap.append(hostsEmpty);
        hostsEmpty.hidden = false;
        return;
      }
      hostsEmpty.hidden = true;
      var groupKeys = ["prod", "infra"];
      groupKeys.forEach(function (gk) {
        var items = matched.filter(function (y) { return y.group === gk; });
        if (!items.length) return;
        var folded = !!foldedGroups[gk] && !q;
        var gBtn = h("button", "side-group" + (folded ? " folded" : ""), [icon("arrow-down-s-line"), h("span", null, T.groups[gk])]);
        gBtn.type = "button";
        gBtn.addEventListener("click", function () { foldedGroups[gk] = !foldedGroups[gk]; paintHosts(); });
        hostsWrap.append(gBtn);
        if (!folded) {
          var ul = h("ul");
          ul.setAttribute("role", "list");
          items.forEach(function (y) {
            ul.appendChild(hostRow(y, function () {
              if (settingsOpen) toggleSettings(false);
              var t = tabs.filter(function (t2) { return t2.panes[0].host === y; })[0];
              if (t) { activate(t); focusSession(sess(), true); } else openTab(y);
            }, y === x, true));
          });
          hostsWrap.append(ul);
        }
      });
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
      stTime.parentNode.hidden = !x || !x.connectedAt || x.local;
      if (x && x.connectedAt) stTime.textContent = since(x.connectedAt);
    }

    // + New Tab menu (with Quick Connect + Local Terminal + saved hosts)
    var menu = root.querySelector("[data-tab-menu]");
    var menuBtn = root.querySelector("[data-new-tab]");
    var menuSearch = root.querySelector("[data-tab-menu-search]");
    var menuQuick = root.querySelector("[data-tab-menu-quick]");
    var menuList = root.querySelector("[data-tab-menu-list]");
    function paintMenu() {
      var q = menuSearch.value.trim().toLowerCase();
      menuQuick.textContent = "";
      if (!q) {
        menuQuick.hidden = false;
        var qc = h("button", "host", [h("span", "tile", icon("flashlight-line")), h("span", "host-name", T.quick_connect)]);
        qc.firstChild.style.setProperty("--tint", "var(--accent)");
        qc.type = "button";
        qc.addEventListener("click", function () { toggleMenu(false); toggleQuick(true); });
        var lc = h("button", "host", [tile(localHost), h("span", "host-name", T.local_terminal)]);
        lc.type = "button";
        lc.addEventListener("click", function () { toggleMenu(false); openTab(localHost); });
        menuQuick.append(qc, lc);
      } else {
        menuQuick.hidden = true;
      }
      menuList.textContent = "";
      HOSTS.filter(function (y) { return (y.id + " " + y.addr).toLowerCase().indexOf(q) >= 0; }).forEach(function (y) {
        menuList.appendChild(hostRow(y, function () { toggleMenu(false); openTab(y); }, false, false));
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

    // Quick Connect popover
    var quickPop = root.querySelector("[data-quick-pop]");
    var quickForm = root.querySelector("[data-quick-form]");
    var quickInput = root.querySelector("[data-quick-input]");
    function toggleQuick(open) {
      quickPop.hidden = !open;
      if (open) { quickInput.value = ""; quickInput.focus({ preventScroll: true }); }
    }
    root.querySelector("[data-quick-open]").addEventListener("click", function (e) { e.stopPropagation(); toggleQuick(quickPop.hidden); });
    root.querySelector("[data-quick-close]").addEventListener("click", function () { toggleQuick(false); });
    quickInput.addEventListener("keydown", function (e) { if (e.key === "Escape") toggleQuick(false); });
    document.addEventListener("click", function (e) { if (!quickPop.hidden && !quickPop.contains(e.target) && !e.target.closest("[data-quick-open], [data-sb-add]")) toggleQuick(false); });
    quickForm.addEventListener("submit", function (e) {
      e.preventDefault();
      var raw = quickInput.value.trim() || "ops@edge-02";
      toggleQuick(false);
      var m = raw.match(/^(?:([^@\s]+)@)?([^:\s]+)(?::(\d+))?$/);
      var user = (m && m[1]) || "root", addr = (m && m[2]) || "edge-02", port = (m && +m[3]) || 22;
      var id = addr.split(".")[0] || "quick";
      var existing = byId[id];
      if (existing) { openTab(existing); return; }
      var hNew = initHost({
        id: id, temp: true, group: "infra", icon: "flashlight-line", tint: "blue", user: user, addr: /^\d+\./.test(addr) ? addr : "10.0.9." + (10 + HOSTS.length),
        port: port, jump: "", os: "Ubuntu 24.04.1 LTS", kernel: "6.8.0-49-generic", cores: 2, mem: 2048, used: 620, swap: 0,
        disk: { size: "30G", used: "8.2G", avail: "21G", pct: 28 }, lat: 18, cpu: 4, up: "1 day, 04:12", load: [0.08, 0.05, 0.03],
        ifaces: ["eth0"], services: ["ssh", "nginx"], procs: [[user, 312, "bash", 14, 0.2, 4], ["root", 1, "systemd", 10, 0.0, 0]],
      });
      openTab(hNew);
    });

    // Status bar buttons: local terminal, theme, privacy, updates
    root.querySelector("[data-st-local]").addEventListener("click", function () { openTab(localHost); });
    root.querySelector("[data-st-theme]").addEventListener("click", function () {
      var mode = isDarkNow() ? "light" : "dark";
      store("sshyard-theme", mode);
      applyTheme(mode);
    });
    var privacyBtn = root.querySelector("[data-st-privacy]");
    function setPrivacy(on) {
      privacy = on;
      privacyBtn.classList.toggle("on", privacy);
      privacyBtn.setAttribute("aria-pressed", String(privacy));
      paintChrome();
      if (tool === "monitor" || tool === "host") renderTool();
      if (settingsOpen) renderSettings();
      if (privacy) winToast(T.privacy_on, "eye-off-fill");
    }
    privacyBtn.addEventListener("click", function () { setPrivacy(!privacy); });

    root.querySelector("[data-win-update]").addEventListener("click", function () {
      winToast(fill(T.up_to_date, { version: "v" + (data.version || "1.6.0") }), "checkbox-circle-fill");
    });

    // ------------------------------------------------------------ in-window Settings page

    var settingsOpen = false;
    var settingsTab = "terminal";
    function toggleSettings(open) {
      settingsOpen = open == null ? !settingsOpen : open;
      closeMetricPop();
      toggleMenu(false);
      toggleQuick(false);
      winWorkspace.hidden = settingsOpen;
      winSettings.hidden = !settingsOpen;
      winBrand.hidden = settingsOpen;
      winBack.hidden = !settingsOpen;
      winTabarea.hidden = settingsOpen;
      winSettingsBtn.classList.toggle("on", settingsOpen);
      winSettingsBtn.classList.toggle("prominent", settingsOpen);
      winSettingsBtn.setAttribute("aria-pressed", String(settingsOpen));
      if (settingsOpen) renderSettings();
      syncPresets();
    }
    winSettingsBtn.addEventListener("click", function () { toggleSettings(); });
    winBack.addEventListener("click", function () { toggleSettings(false); });

    function renderSettings() {
      var S = T.settings_ui;
      winSettings.textContent = "";
      var navItems = [
        ["appearance", "palette-line", S.appearance],
        ["terminal", "terminal-box-line", S.terminal],
        ["ai", "robot-2-line", S.ai],
      ];
      var nav = h("nav", "set-nav", navItems.map(function (it) {
        var b = h("button", "set-tab" + (settingsTab === it[0] ? " on" : ""), [icon(it[1]), h("span", null, it[2])]);
        b.type = "button";
        b.addEventListener("click", function () { settingsTab = it[0]; renderSettings(); });
        return b;
      }));
      var body = h("div", "set-body");
      if (settingsTab === "terminal") {
        var grid = h("div", "scheme-grid", SCHEMES.map(function (sc) {
          var bg = sc.auto ? (isDarkNow() ? "#161618" : "#FFFFFF") : sc.bg;
          var fg = sc.auto ? (isDarkNow() ? "#D8DEE9" : "#1F2328") : sc.fg;
          var gr = sc.auto ? "#30D158" : sc.green;
          var bl = sc.auto ? "#5BA8FD" : sc.blue;
          var rd = sc.auto ? "#F0626A" : sc.red;
          var yl = sc.auto ? "#E9C46A" : sc.yellow;
          var prev = h("div", "scheme-prev", [
            h("div", null, [Object.assign(h("span", null, "deploy@web"), { style: "color:" + gr }), ":~ $ ls"]),
            h("div", "scheme-swatches", [rd, gr, yl, bl].map(function (c) { var sw = h("i"); sw.style.background = c; return sw; })),
          ]);
          prev.style.background = bg;
          prev.style.color = fg;
          var card = h("button", "scheme-card" + (currentScheme === sc.id ? " on" : ""), [
            prev,
            h("span", "scheme-name", [h("span", null, sc.name), currentScheme === sc.id ? icon("check-line") : null]),
          ]);
          card.type = "button";
          card.addEventListener("click", function () { applyScheme(sc.id); });
          return card;
        }));
        body.append(
          h("div", "set-head", [h("span", "tile tile-lg", icon("terminal-box-line")), h("div", null, [h("h3", null, S.scheme), h("p", null, S.scheme_hint)])]),
          h("div", "set-card", [grid])
        );
      } else if (settingsTab === "appearance") {
        var curTheme = store("sshyard-theme") || "auto";
        var seg = h("div", "seg", [["auto", data.theme.auto], ["light", data.theme.light], ["dark", data.theme.dark]].map(function (m) {
          var b = h("button", null, m[1]);
          b.type = "button";
          b.setAttribute("aria-selected", String(curTheme === m[0]));
          b.addEventListener("click", function () { store("sshyard-theme", m[0] === "auto" ? null : m[0]); applyTheme(m[0]); renderSettings(); });
          return b;
        }));
        var privToggle = h("button", "toggle");
        privToggle.type = "button";
        privToggle.setAttribute("role", "switch");
        privToggle.setAttribute("aria-checked", String(privacy));
        privToggle.addEventListener("click", function () { setPrivacy(!privacy); });
        body.append(
          h("div", "set-head", [h("span", "tile tile-lg", icon("palette-line")), h("div", null, [h("h3", null, S.appearance)])]),
          h("div", "set-card", [
            h("div", "set-row", [h("div", null, [h("strong", null, S.appearance)]), seg]),
            h("div", "set-row", [h("div", null, [h("strong", null, S.privacy_title), h("span", null, S.privacy_desc)]), privToggle]),
          ])
        );
      } else {
        var mSeg = h("div", "seg", MODES.map(function (mk, idx) {
          var b = h("button", null, T.modes[idx].label);
          b.type = "button";
          b.setAttribute("aria-selected", String(mode === mk));
          b.addEventListener("click", function () { mode = mk; paintMode(); renderSettings(); });
          return b;
        }));
        body.append(
          h("div", "set-head", [h("span", "tile tile-lg", icon("robot-2-line")), h("div", null, [h("h3", null, S.ai)])]),
          h("div", "set-card", [
            h("div", "set-row", [h("div", null, [h("strong", null, S.mode_title), h("span", null, S.mode_desc)]), mSeg]),
          ])
        );
      }
      winSettings.append(nav, body);
      requestAnimationFrame(function () { $$(".seg", winSettings).forEach(updateSegPill); });
    }

    // ------------------------------------------------------------ keyboard (with Broadcast Sync!)

    input.addEventListener("focus", function () { focused = true; paintFocus(); });
    input.addEventListener("blur", function () { focused = false; paintFocus(); });
    function syncFromInput() {
      var s = sess();
      if (!s) return;
      s.buf = input.value;
      s.caret = input.selectionStart == null ? input.value.length : input.selectionStart;
      s.render();
      if (broadcastSync && broadcastSelected[s.sid]) {
        allPanes().forEach(function (other) {
          if (other !== s && broadcastSelected[other.sid] && other.state === "ready" && !other.busy) {
            other.buf = s.buf;
            other.caret = s.caret;
            other.render();
          }
        });
      }
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

    // ------------------------------------------------------------ right rail tool panes

    var tool = "ai";
    var bodies = {};
    $$("[data-tool]", toolPane).forEach(function (b) { bodies[b.getAttribute("data-tool")] = b; });
    var toolIcon = toolPane.querySelector("[data-tool-icon]");
    var toolTitle = toolPane.querySelector("[data-tool-title]");
    var toolActions = toolPane.querySelector("[data-tool-actions]");
    var ICONS = {
      monitor: "pulse-fill", usage: "bar-chart-horizontal-fill", host: "information-fill",
      files: "folder-3-fill", docker: "ship-2-fill", ai: "robot-2-fill",
      snippets: "code-s-slash-line", broadcast: "broadcast-fill",
    };

    function setTool(kind, show) {
      if (settingsOpen) toggleSettings(false);
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
      toolTitle.textContent = (tool === "snippets" || tool === "broadcast") ? T.tools[tool] : T.tools[tool] + " · " + (x.local ? T.local_terminal : x.id);
      toolActions.textContent = "";
      if (tool === "monitor") renderMonitor(x, s);
      if (tool === "usage") renderUsage(x, s);
      if (tool === "host") renderHostInfo(x);
      if (tool === "files") renderFiles(x);
      if (tool === "docker") renderDocker(x, s);
      if (tool === "snippets") renderSnippets();
      if (tool === "broadcast") renderBroadcast();
      if (tool === "ai") renderAI(x);
    }

    // 1. System Monitor (colored tiles + per-core vertical bars + sparklines + loading state)
    var mon = null;
    function bar(pct) { var b = h("span", "mbar", h("i")); b.firstChild.style.width = clamp(pct, 0, 100) + "%"; return b; }
    function spark() {
      var svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
      svg.setAttribute("viewBox", "0 0 100 30");
      svg.setAttribute("preserveAspectRatio", "none");
      svg.setAttribute("class", "spark");
      svg.setAttribute("aria-hidden", "true");
      var area = document.createElementNS(svg.namespaceURI, "path"), line = document.createElementNS(svg.namespaceURI, "path");
      area.setAttribute("class", "spark-area");
      line.setAttribute("class", "spark-line");
      svg.append(area, line);
      svg.paint = function (vals, max) {
        var pts = vals.map(function (v, i) { return [(i / (vals.length - 1)) * 100, 29 - clamp(v / max, 0, 1) * 27]; });
        var d = pts.map(function (p, i) { return (i ? "L" : "M") + p[0].toFixed(1) + " " + p[1].toFixed(1); }).join("");
        line.setAttribute("d", d);
        area.setAttribute("d", d + "L100 30L0 30Z");
      };
      return svg;
    }
    function card(iconName, tint, title, aside, kids) {
      var ic = h("span", "mcard-icon", icon(iconName));
      if (tint) ic.style.setProperty("--ctint", tint);
      return h("div", "mcard", [h("div", "mcard-head", [ic, h("b", null, title), aside ? h("span", "muted", aside) : null])].concat(kids));
    }
    function kv(k, v) { var d = h("b", "mono", v); return [h("div", "kv", [h("span", "muted", k), d]), d]; }
    function renderMonitor(x, s) {
      var M = T.monitor, b = bodies.monitor;
      b.textContent = "";
      if (s && s.state === "connecting") {
        b.append(h("div", "panel-loading", [h("i", "ui-spin"), h("span", null, M.waiting)]));
        mon = null;
        return;
      }
      var lat = kv(M.latency, ""), conn = kv(M.connected, "");
      var cpuPct = h("b", "big"), cpuBar = bar(0), cpuSpark = spark(), load = h("span", "mono");
      var coreEls = [];
      var coreRow = h("div", "core-bars", x.coresPct.map(function () {
        var fillEl = h("i");
        coreEls.push(fillEl);
        return h("span", "core-col", fillEl);
      }));
      var memPct = h("b", "t-blue"), memBar = bar(0), memTxt = h("span", "mono muted");
      var swapPct = h("b", "t-blue"), swapBar = bar(0), swapTxt = h("span", "mono muted");
      var netSpark = spark(), nets = [];
      b.append(h("div", "mon", [
        card("server-line", "var(--tint-" + x.tint + ")", M.system, null, [h("div", "kv-grid", [kv(M.address, targetOf(x))[0], kv(M.os, x.os)[0], lat[0], conn[0]])]),
        card("cpu-line", "var(--tint-green)", "CPU", fill(M.cores, { n: x.cores }), [
          h("div", "cpu-row", [cpuPct, h("div", "cpu-meta", [h("span", "muted", M.average), cpuBar])]),
          coreRow, cpuSpark,
          h("div", "kv-row", [h("span", "muted", M.load), load])]),
        card("ram-2-line", "var(--tint-purple)", M.memory, null, [
          h("div", "kv-row", [h("span", null, M.physical), memPct]), memBar, memTxt,
          h("div", "kv-row", [h("span", null, M.swap), swapPct]), swapBar, swapTxt]),
        card("arrow-up-down-line", "var(--tint-teal)", M.network, null, [netSpark].concat(x.ifaces.map(function (n, i) {
          var u = h("span", "t-ok"), d = h("span", "t-blue");
          nets.push([u, d, i]);
          return h("div", "iface mono", [h("span", null, n), h("span", "rates", [icon("arrow-up-line"), u, icon("arrow-down-line"), d])]);
        }))),
        card("hard-drive-3-line", "var(--tint-orange)", M.disk, null, [h("div", "kv-row", [h("span", "mono", "/"), h("b", "t-blue", x.disk.pct + "%")]), bar(x.disk.pct), h("span", "mono muted", x.disk.used + " / " + x.disk.size)]),
      ]));
      mon = { x: x, paint: function () {
        var st = x.stat;
        lat[1].textContent = Math.round(st.lat) + " ms";
        conn[1].textContent = x.connectedAt ? since(x.connectedAt) : "–";
        cpuPct.textContent = Math.round(st.cpu) + "%";
        cpuBar.firstChild.style.width = st.cpu + "%";
        x.coresPct.forEach(function (p, i) { if (coreEls[i]) coreEls[i].style.height = clamp(p, 6, 100) + "%"; });
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

    // 2. Process Usage (usage.go: filter + sortable table)
    var usageFilter = "", usageSort = "mem", usageDesc = true;
    function renderUsage(x, s) {
      var U = T.usage, b = bodies.usage;
      b.textContent = "";
      if (s && s.state === "connecting") {
        b.append(h("div", "panel-loading", [h("i", "ui-spin"), h("span", null, T.monitor.waiting)]));
        return;
      }
      var qInput = h("input");
      qInput.type = "search";
      qInput.value = usageFilter;
      qInput.placeholder = U.filter;
      var countEl = h("span", "usage-count");
      var tbody = h("tbody");
      var cols = [["name", U.proc], ["pid", U.pid], ["cpu", U.cpu], ["mem", U.mem], ["disk", U.disk]];
      var thead = h("thead", null, h("tr", null, cols.map(function (c) {
        var th = h("th", usageSort === c[0] ? "sort" : null, c[1] + (usageSort === c[0] ? (usageDesc ? " ↓" : " ↑") : ""));
        th.addEventListener("click", function () {
          if (usageSort === c[0]) usageDesc = !usageDesc; else { usageSort = c[0]; usageDesc = c[0] !== "name"; }
          renderUsage(x, s);
        });
        return th;
      })));
      function paintRows() {
        var q = usageFilter.trim().toLowerCase();
        var list = x.procs.filter(function (p) { return !q || p[2].toLowerCase().indexOf(q) >= 0 || p[0].toLowerCase().indexOf(q) >= 0 || String(p[1]) === q; });
        list.sort(function (a, b2) {
          var d = usageSort === "name" ? a[2].localeCompare(b2[2])
            : usageSort === "pid" ? a[1] - b2[1]
            : usageSort === "cpu" ? (a[4] || 0) - (b2[4] || 0)
            : usageSort === "disk" ? (a[5] || 0) - (b2[5] || 0)
            : a[3] - b2[3];
          return usageDesc ? -d : d;
        });
        countEl.textContent = fill(U.count, { n: list.length });
        tbody.textContent = "";
        list.forEach(function (p) {
          tbody.append(h("tr", null, [
            h("td", null, p[2]),
            h("td", "muted", String(p[1])),
            h("td", null, (p[4] || 0.1).toFixed(1) + "%"),
            h("td", null, mib(p[3])),
            h("td", "muted", p[5] ? rate(p[5] * 1024) : "0B/s"),
          ]));
        });
      }
      qInput.addEventListener("input", function () { usageFilter = qInput.value; paintRows(); });
      paintRows();
      b.append(h("div", "usage-wrap", [
        h("div", "usage-tools", [h("label", "side-search", [icon("search-line"), qInput]), countEl]),
        h("div", "usage-table-wrap", h("table", "usage-table", [thead, tbody])),
      ]));
    }

    // 3. Host Details (rail.go:689)
    function renderHostInfo(x) {
      var H = T.host_info, b = bodies.host;
      b.textContent = "";
      var panesCount = allPanes().filter(function (p) { return p.host === x; }).length;
      var connBtn = h("button", "btn btn-primary btn-sm", H.connect);
      connBtn.type = "button";
      connBtn.addEventListener("click", function () { openTab(x); });
      var favBtn = btn(x.fav ? "star-fill" : "star-line", H.favorite, function () { x.fav = !x.fav; renderHostInfo(x); }, x.fav ? "on" : "");
      var filesBtn = btn("folder-3-line", T.tools.files, function () { setTool("files", true); });
      b.append(h("div", "mon", [
        h("div", "mcard host-hero", [
          h("div", "host-hero-top", [tile(x, "tile-lg"), h("div", null, [h("strong", null, x.local ? T.local_terminal : x.id), h("span", "muted small", targetOf(x))])]),
          h("div", "host-hero-acts", [connBtn, h("span", "spacer"), x.local ? null : filesBtn, x.local ? null : favBtn]),
        ]),
        card("global-line", "var(--tint-blue)", H.connection, null, [
          h("div", "kv-grid", [
            kv(T.monitor.address, maskAddr(x.addr))[0], kv(H.port, String(x.port || 22))[0],
            kv(H.user, x.user)[0], kv(H.group, T.groups[x.group] || "–")[0],
            kv(H.proxy, H.proxy_none)[0], kv(H.panes, String(panesCount))[0],
            kv(H.auth, H.auth_agent)[0], kv(H.jumps, x.jump || "–")[0],
          ]),
        ]),
      ]));
    }

    // 4. SFTP File Manager (single & dual-pane Local | Remote + loading bar!)
    var pick = root.querySelector("[data-file-pick]");
    var transfers = h("div", "transfers");
    function changed(x) { if (tool === "files" && sess() && sess().host === x) renderFiles(x); }
    function buildFileSide(opts) {
      var side = h("div", "files-side");
      var F_ = T.files;
      if (opts.head) {
        var hActs = h("span", "pane-icons", [
          btn("corner-left-up-line", F_.up, opts.onUp, "wbtn-xs"),
          btn("refresh-line", F_.refresh, opts.onRefresh, "wbtn-xs"),
        ]);
        side.append(h("div", "files-side-head", [
          h("span", "tool-icon", icon(opts.isLocal ? "computer-line" : "server-line")),
          h("span", null, opts.title),
          hActs,
        ]));
      }
      var crumbs = h("div", "crumbs-bar");
      var parts = opts.cwd.split("/").filter(Boolean);
      var rootBtn = h("button", "crumb", icon(opts.isLocal ? "computer-line" : "server-line"));
      rootBtn.type = "button";
      rootBtn.addEventListener("click", function () { opts.onNav("/"); });
      crumbs.append(rootBtn);
      parts.forEach(function (p, i) {
        var c = h("button", "crumb", p);
        c.type = "button";
        c.addEventListener("click", function () { opts.onNav("/" + parts.slice(0, i + 1).join("/")); });
        crumbs.append(icon("arrow-right-s-line"), c);
      });
      side.append(crumbs);
      var view = h("div", "files-view");
      side.append(view);
      var dir = lookupTree(opts.tree, opts.cwd);
      if (!dir || !dir.d) dir = opts.tree;
      if (opts.preview) {
        var f = lookupTree(opts.tree, opts.preview);
        var back = btn("arrow-left-line", F_.back, opts.onClosePreview, "wbtn-xs");
        var body = f ? bodyOf(f) : null;
        view.append(
          h("div", "preview-head", [back, fileIcon(fileIconName(baseOf(opts.preview), false)), h("b", "mono", baseOf(opts.preview)), h("span", "muted", human(f ? sizeOf(f) : 0))]),
          body == null ? h("p", "files-empty muted", F_.note) : h("pre", "preview", body.split("\n").slice(0, 200).join("\n"))
        );
      } else {
        var list = h("ul", "files-list");
        list.setAttribute("role", "list");
        var names = entries(dir).sort(function (a, b2) { return (dir.c[b2].d - dir.c[a].d) || 0; });
        names.forEach(function (n) {
          var node = dir.c[n];
          var row = h("button", "frow", [fileIcon(fileIconName(n, node.d)), h("span", "fname", n), h("span", "fsize", node.d ? "" : human(sizeOf(node)))]);
          row.type = "button";
          row.addEventListener("click", function () {
            var p = (opts.cwd === "/" ? "" : opts.cwd) + "/" + n;
            if (node.d) opts.onNav(p);
            else if (opts.isLocal) opts.onUploadLocal(n, node);
            else opts.onOpen(p);
          });
          list.append(h("li", null, row));
        });
        view.append(h("div", "files-head", [h("span", null, F_.name), h("span", null, F_.size)]),
          names.length ? list : h("p", "files-empty muted", F_.empty));
      }
      if (!opts.isLocal) view.append(h("div", "drop-hint", [icon("upload-2-line"), h("span", null, F_.drop)]));
      return side;
    }

    function flashFilesLoad(barEl) {
      if (!barEl) return;
      barEl.classList.add("busy");
      setTimeout(function () { barEl.classList.remove("busy"); }, reduced.matches ? 60 : 320);
    }

    function renderFiles(x, flash) {
      var F_ = T.files, b = bodies.files;
      toolActions.textContent = "";
      var dualBtn = btn(dualFiles ? "computer-fill" : "computer-line", F_.dual, function () { dualFiles = !dualFiles; renderFiles(x, true); }, "wbtn-xs" + (dualFiles ? " on" : ""));
      dualBtn.setAttribute("aria-pressed", String(dualFiles));
      var followBtn = btn(follow ? "footprint-fill" : "footprint-line", F_.follow, function () { follow = !follow; if (follow && sess()) { x.filesCwd = sess().cwd; x.preview = null; } renderFiles(x); }, "wbtn-xs" + (follow ? " on" : ""));
      followBtn.setAttribute("aria-pressed", String(follow));
      toolActions.append(
        dualBtn,
        followBtn,
        dualFiles ? null : btn("corner-left-up-line", F_.up, function () { x.preview = null; x.filesCwd = parentOf(x.filesCwd); renderFiles(x, true); }, "wbtn-xs"),
        btn("refresh-line", F_.refresh, function (e) { var i = e.currentTarget.querySelector(".i"); i.classList.remove("spin"); void i.offsetWidth; i.classList.add("spin"); renderFiles(x, true); }, "wbtn-xs"),
        btn("upload-2-line", F_.upload, function () { pick.click(); }, "wbtn-xs")
      );
      b.textContent = "";
      var progBar = h("div", "files-bar-prog");
      b.append(progBar);
      if (flash) flashFilesLoad(progBar);

      var splitEl = h("div", "files-split" + (dualFiles ? " dual" : ""));
      if (dualFiles) {
        splitEl.append(buildFileSide({
          isLocal: true, head: true, title: F_.local, tree: localFS, cwd: localCwd,
          onUp: function () { localCwd = parentOf(localCwd); renderFiles(x, true); },
          onRefresh: function () { renderFiles(x, true); },
          onNav: function (p) { localCwd = p; renderFiles(x, true); },
          onUploadLocal: function (name, node) {
            upload([{ name: name, size: sizeOf(node) || 1240, body: bodyOf(node) }]);
          },
        }));
      }
      splitEl.append(buildFileSide({
        isLocal: false, head: dualFiles, title: F_.remote, tree: x.fs, cwd: x.filesCwd, preview: x.preview,
        onUp: function () { x.preview = null; x.filesCwd = parentOf(x.filesCwd); renderFiles(x, true); },
        onRefresh: function () { renderFiles(x, true); },
        onNav: function (p) { x.preview = null; x.filesCwd = p; renderFiles(x, true); },
        onOpen: function (p) { x.preview = p; renderFiles(x, true); },
        onClosePreview: function () { x.preview = null; renderFiles(x); },
      }));
      b.append(splitEl, transfers);
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
        var ms = clamp((file.size || 2048) / 4096, 650, 2600), t0 = performance.now();
        (function frame(t) {
          var p = clamp((t - t0) / ms, 0, 1);
          prog.firstChild.style.width = p * 100 + "%";
          pct.textContent = Math.round(p * 100) + "%";
          if (p < 1) { requestAnimationFrame(frame); return; }
          put(x.fs, (dir === "/" ? "" : dir) + "/" + name, F(file.body != null ? file.body : null, { size: file.size }));
          label.textContent = fill(T.files.uploaded, { name: name });
          row.classList.add("done");
          changed(x);
          setTimeout(function () { row.remove(); }, 2400);
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

    // 5. Docker Manager (docker.go: Containers / Images, start/stop/restart, logs, exec shell)
    var dockerTab = 0, dockerLogsFor = null;
    function renderDocker(x, s, loading) {
      var DK = T.docker, b = bodies.docker;
      b.textContent = "";
      if (s && s.state === "connecting") {
        b.append(h("div", "panel-loading", [h("i", "ui-spin"), h("span", null, DK.loading)]));
        return;
      }
      if (!x.containers) {
        b.append(h("p", "files-empty muted", DK.empty));
        return;
      }
      if (loading) {
        b.append(h("div", "panel-loading", [h("i", "ui-spin"), h("span", null, DK.loading)]));
        setTimeout(function () { if (tool === "docker" && sess() && sess().host === x) renderDocker(x, s, false); }, reduced.matches ? 60 : 380);
        return;
      }
      if (dockerLogsFor && x.containers.indexOf(dockerLogsFor) >= 0) {
        var ct = dockerLogsFor;
        b.append(
          h("div", "preview-head", [
            btn("arrow-left-line", DK.back, function () { dockerLogsFor = null; renderDocker(x, s); }, "wbtn-xs"),
            h("b", "mono", ct.name),
            h("span", "muted", ct.image),
          ]),
          h("pre", "preview", ct.logs || "(no logs)")
        );
        return;
      }
      dockerLogsFor = null;
      var seg = h("div", "seg", [
        (function () { var t0 = h("button", null, fill(DK.containers, { n: x.containers.length })); t0.type = "button"; t0.setAttribute("aria-selected", String(dockerTab === 0)); t0.addEventListener("click", function () { dockerTab = 0; renderDocker(x, s); }); return t0; })(),
        (function () { var t1 = h("button", null, fill(DK.images, { n: x.containers.length })); t1.type = "button"; t1.setAttribute("aria-selected", String(dockerTab === 1)); t1.addEventListener("click", function () { dockerTab = 1; renderDocker(x, s); }); return t1; })(),
      ]);
      var refBtn = btn("refresh-line", T.files.refresh, function () { renderDocker(x, s, true); }, "wbtn-xs");
      var list = h("div", "docker-list");
      if (dockerTab === 0) {
        x.containers.forEach(function (ct) {
          var acts = h("span", "drow-acts", [
            btn(ct.running ? "stop-line" : "play-line", ct.running ? DK.stop : DK.start, function () {
              ct.running = !ct.running;
              ct.status = ct.running ? "Up just now" : "Exited (0) just now";
              renderDocker(x, s);
            }, "wbtn-xs"),
            btn("restart-line", DK.restart, function () { ct.running = true; ct.status = "Up 1 second"; renderDocker(x, s, true); }, "wbtn-xs"),
            btn("file-list-3-line", DK.logs, function () { dockerLogsFor = ct; renderDocker(x, s); }, "wbtn-xs"),
            btn("terminal-box-line", DK.shell, function () {
              var cur = sess();
              if (cur && cur.state === "ready") { typeInto(cur, "docker exec -it " + ct.name + " sh", reduced.matches); focusSession(cur, true); }
            }, "wbtn-xs"),
          ]);
          list.append(h("div", "drow", [
            h("div", "drow-info", [
              h("div", "drow-name", [h("i", "dot" + (ct.running ? "" : " off")), h("span", null, ct.name)]),
              h("div", "drow-meta", ct.image + " · " + ct.status),
            ]),
            acts,
          ]));
        });
      } else {
        x.containers.forEach(function (ct) {
          list.append(h("div", "drow", [
            h("div", "drow-info", [
              h("div", "drow-name", [icon("ship-2-fill"), h("span", null, ct.image)]),
              h("div", "drow-meta", ct.id.slice(0, 12) + " · 41.2 MB"),
            ]),
          ]));
        });
      }
      b.append(h("div", "docker-wrap", [h("div", "docker-bar", [seg, refBtn]), list]));
      requestAnimationFrame(function () { updateSegPill(seg); });
    }

    // 6. Snippets
    function renderSnippets() {
      var b = bodies.snippets;
      if (b.firstChild) return;
      var list = h("ul", "snips");
      list.setAttribute("role", "list");
      T.snippets.items.forEach(function (it) {
        list.append(h("li", "snip", [
          h("div", "snip-text", [h("b", null, it.name), h("code", null, it.cmd)]),
          h("span", "snip-acts", [
            btn("terminal-line", T.snippets.insert, function () { var s = sess(); if (s && s.state === "ready" && !s.busy) { s.setBuf(it.cmd); focusSession(s, true); } }, "wbtn-xs"),
            btn("play-circle-line", T.snippets.run, function () { var s = sess(); if (s) { if (story.running && s.host === web) story.fast = true; typeInto(s, it.cmd, reduced.matches); } }, "wbtn-xs"),
          ]),
        ]));
      });
      b.append(list);
    }

    // 7. Broadcast Commands (broadcast.go: multi-terminal send + live synced typing)
    function renderBroadcast() {
      var BC = T.broadcast, b = bodies.broadcast;
      b.textContent = "";
      var panes = allPanes().filter(function (p) { return p.state !== "closed"; });
      var syncBtn = h("button", "toggle");
      syncBtn.type = "button";
      syncBtn.setAttribute("role", "switch");
      syncBtn.setAttribute("aria-checked", String(broadcastSync));
      syncBtn.addEventListener("click", function () {
        broadcastSync = !broadcastSync;
        paintChrome();
        renderBroadcast();
      });
      var targetsEl = h("div", "bc-targets");
      var selCount = 0;
      panes.forEach(function (p) {
        var on = !!broadcastSelected[p.sid];
        if (on) selCount++;
        var cb = h("input");
        cb.type = "checkbox";
        cb.checked = on;
        cb.addEventListener("change", function () { broadcastSelected[p.sid] = cb.checked; paintChrome(); renderBroadcast(); });
        targetsEl.append(h("label", "bc-target", [cb, tile(p.host, "tile-sm"), h("strong", null, p.host.local ? T.local_terminal : p.host.id), h("span", "muted small", targetOf(p.host))]));
      });
      if (!panes.length) targetsEl.append(h("p", "muted small", BC.none));
      var cmdIn = h("input");
      cmdIn.type = "text";
      cmdIn.placeholder = BC.placeholder;
      cmdIn.value = "uptime";
      var sendBtn = h("button", "btn btn-primary btn-sm", BC.send);
      sendBtn.type = "submit";
      sendBtn.disabled = selCount === 0;
      var form = h("form", "bc-form", [cmdIn, h("div", "bc-form-foot", [h("span", "muted small", fill(BC.targets, { n: selCount, total: panes.length })), sendBtn])]);
      form.addEventListener("submit", function (e) {
        e.preventDefault();
        var cmd = cmdIn.value.trim();
        if (!cmd) return;
        var chosen = panes.filter(function (p) { return broadcastSelected[p.sid] && p.state === "ready"; });
        chosen.forEach(function (p) { typeInto(p, cmd, reduced.matches); });
        winToast(fill(BC.sent, { n: chosen.length }), "broadcast-fill");
        cmdIn.value = "";
      });
      b.append(h("div", "bc-wrap", [
        h("div", "bc-sync", [h("div", null, [h("strong", null, BC.sync), h("span", null, BC.sync_hint)]), syncBtn]),
        targetsEl,
        form,
      ]));
    }

    // 8. AI Assistant (aiview.go & toolcard.go & terminal-ai-assistant.webp)
    var aiBody = bodies.ai;
    var aiSugg = aiBody.querySelector("[data-ai-sugg]");
    var aiForm = aiBody.querySelector("[data-ai-form]");
    var aiInput = aiBody.querySelector("[data-ai-input]");
    var aiModeBtn = aiBody.querySelector("[data-ai-mode]");
    var aiFull = aiBody.querySelector("[data-ai-full]");
    var aiWorkingEl = aiBody.querySelector("[data-ai-working]");
    var aiModelBtn = aiBody.querySelector("[data-ai-model]");
    var aiEffortBtn = aiBody.querySelector("[data-ai-effort]");
    var aiInTermBtn = aiBody.querySelector("[data-ai-interm]");
    var aiAttachBtn = aiBody.querySelector("[data-ai-attach]");
    var aiStopBtn = aiBody.querySelector("[data-ai-stop]");
    var aiSendBtn = aiBody.querySelector("[data-ai-send]");
    var MODES = ["ask", "review", "full"];
    var MODE_ICONS = { ask: "shield-check-line", review: "eye-line", full: "lock-unlock-line" };
    var mode = "review";
    var aiBackendIdx = 0;
    var BACKENDS = [T.ai_ui.builtin, "Claude Code (ACP)", "Codex (ACP)"];
    var MODELS = [
      { id: "gpt-5.4", brand: "openai" },
      { id: "claude-sonnet-4", brand: "claude" },
      { id: "gemini-2.5-pro", brand: "gemini" },
      { id: "deepseek-r1", brand: "deepseek" },
    ];
    var modelIdx = 0, effortHigh = false, runInTerminal = false;

    function paintMode() {
      aiModeBtn.textContent = "";
      aiModeBtn.append(icon(MODE_ICONS[mode]), h("span", null, T.modes[MODES.indexOf(mode)].label), icon("arrow-down-s-line"));
      aiModeBtn.className = "chip chip-mode mode-" + mode;
      aiFull.hidden = mode !== "full";
    }
    function paintModel() {
      var m = MODELS[modelIdx];
      var br = T.brands && T.brands[m.brand];
      var imgs = $$("img", aiModelBtn);
      if (br && imgs.length >= 2) { imgs[0].src = br.light; imgs[1].src = br.dark; }
      var lbl = aiModelBtn.querySelector("[data-ai-model-name]");
      if (lbl) lbl.textContent = m.id;
    }
    aiModeBtn.addEventListener("click", function () { mode = MODES[(MODES.indexOf(mode) + 1) % MODES.length]; paintMode(); });
    aiModelBtn.addEventListener("click", function () { modelIdx = (modelIdx + 1) % MODELS.length; paintModel(); });
    aiEffortBtn.addEventListener("click", function () {
      effortHigh = !effortHigh;
      aiEffortBtn.querySelector("span").textContent = effortHigh ? T.ai_ui.effort_high : T.ai_ui.effort_auto;
    });
    aiInTermBtn.addEventListener("click", function () {
      runInTerminal = !runInTerminal;
      aiInTermBtn.classList.toggle("on", runInTerminal);
      aiInTermBtn.setAttribute("aria-pressed", String(runInTerminal));
    });
    aiAttachBtn.addEventListener("click", function () { pick.click(); });
    aiStopBtn.addEventListener("click", function () {
      var x = sess() && sess().host;
      if (!x) return;
      if (story.running && x === web) story.fast = true;
      x.aiStop = true;
      setAIWorking(x, false);
    });

    function setAIWorking(x, busy) {
      x.aiBusy = busy;
      if (sess() && sess().host === x && tool === "ai") {
        aiWorkingEl.hidden = !busy;
        aiStopBtn.hidden = !busy;
        aiSendBtn.hidden = busy;
      }
      paintChrome();
    }

    function renderAI(x) {
      var slot = aiBody.querySelector(".ai-log");
      if (slot !== x.log) slot.replaceWith(x.log);
      var backendChip = h("button", "ai-chip-head", [icon("robot-2-line"), h("span", null, BACKENDS[aiBackendIdx]), icon("arrow-down-s-line")]);
      backendChip.type = "button";
      backendChip.addEventListener("click", function () { aiBackendIdx = (aiBackendIdx + 1) % BACKENDS.length; renderAI(x); });
      toolActions.textContent = "";
      toolActions.append(
        backendChip,
        btn("chat-new-line", T.ai_ui.new_chat, function () { if (!x.aiBusy) { x.log.textContent = ""; renderAI(x); } }, "wbtn-xs"),
        x === web ? btn("restart-line", T.replay, replay, "wbtn-xs") : null
      );
      if (!x.log.firstChild) x.log.append(h("div", "ai-empty", [icon("robot-2-fill"), h("p", null, T.placeholder)]));
      aiSugg.hidden = story.running && x === web;
      aiWorkingEl.hidden = !x.aiBusy;
      aiStopBtn.hidden = !x.aiBusy;
      aiSendBtn.hidden = !!x.aiBusy;
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
      for (var i = 0; i < chars.length && !reduced.matches && !(fast && fast()) && !x.aiStop;) {
        i += cjk ? 1 + Math.floor(Math.random() * 2) : 2 + Math.floor(Math.random() * 4);
        p.textContent = chars.slice(0, i).join("");
        x.log.scrollTop = x.log.scrollHeight;
        await sleep(cjk ? 30 : 20);
      }
      p.textContent = text;
      x.log.scrollTop = x.log.scrollHeight;
    }

    // Collapsible Tool Card (toolcard.go)
    function toolCard(x, title, codes, openInit) {
      var meta = h("span", "muted"), state = h("span");
      var chev = h("span", null, icon(openInit ? "arrow-up-s-line" : "arrow-down-s-line"));
      var head = h("button", "tool-head", [chev, icon("tools-line"), h("b", null, title), meta, state]);
      head.type = "button";
      var det = h("div", "tool-det", codes.map(function (c) { return h("code", c[1] || null, c[0]); }));
      var foot = h("div", "tool-foot", [h("span", null, (x.local ? T.local_terminal : x.id)), h("span", "mono", "")]);
      det.append(foot);
      var el = h("div", "tool" + (openInit ? " open" : ""), [head, det]);
      head.addEventListener("click", function () {
        var open = el.classList.toggle("open");
        chev.textContent = "";
        chev.append(icon(open ? "arrow-up-s-line" : "arrow-down-s-line"));
      });
      logAdd(x, el);
      function scroll() { x.log.scrollTop = x.log.scrollHeight; }
      return {
        meta: function (t) { meta.textContent = t; },
        state: function (t, cls) { state.textContent = t; state.className = cls || ""; },
        done: function (exitCode, ms) { foot.lastChild.textContent = fill(T.ai_ui.exit, { code: exitCode }) + " · " + ms + " ms"; },
        out: function (lines) {
          lines.forEach(function (l) { det.insertBefore(h("code", l[1] || null, l[0]), foot); });
          scroll();
        },
        ask: function () {
          el.classList.add("open");
          chev.textContent = ""; chev.append(icon("arrow-up-s-line"));
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
        await sleep(reduced.matches ? 0 : 750);
        c.meta(T.tool_ok);
        return true;
      }
      c.meta(T.card.ran);
      return true;
    }

    function values(x) {
      var p = x.procs.slice().sort(function (a, b) { return b[3] - a[3]; });
      return { host: x.id, pct: x.disk.pct + "%", used: x.disk.used, size: x.disk.size, avail: x.disk.avail,
        top: p[0][2], topmem: mib(p[0][3]), second: (p[1] || p[0])[2], secondmem: mib((p[1] || p[0])[3]) };
    }

    function memTable(x) {
      var A = T.ai_ui, st = x.stat;
      var rows = [
        [A.table_total, (x.mem / 1024).toFixed(2) + " GiB"],
        [A.table_used, (st.used / 1024).toFixed(2) + " GiB (" + Math.round(st.used / x.mem * 100) + "%)"],
        [A.table_avail, mib(Math.max(0, x.mem - st.used))],
        [A.table_swap, x.swap ? (x.swap / 1024).toFixed(1) + " GiB" : "0 B"],
      ];
      return h("table", "ai-table", [
        h("thead", null, h("tr", null, [h("th", null, A.table_item), h("th", null, A.table_val)])),
        h("tbody", null, rows.map(function (r) { return h("tr", null, [h("td", null, r[0]), h("td", null, r[1])]); })),
      ]);
    }

    async function runAIQuery(x, qText, cmd, answer, showMemTable) {
      if (x.aiBusy) return;
      x.aiStop = false;
      setAIWorking(x, true);
      logAdd(x, h("div", "msg-user", h("span", null, qText)));
      await sleep(reduced.matches ? 0 : 280);
      if (x.aiStop) { setAIWorking(x, false); logAdd(x, h("p", "muted small", T.ai_ui.interrupted)); return; }
      var c = toolCard(x, T.tool_run, [[cmd]], true);
      if (!(await approve(c))) {
        c.state(T.rejected, "t-red");
        await streamText(x, T.denied);
        setAIWorking(x, false);
        return;
      }
      c.state(T.running, "t-blue");
      var tStart = performance.now();
      var headless = new Session(x, false);
      await runLine(headless, cmd);
      if (runInTerminal && sess() && sess().host === x && sess().state === "ready") {
        await typeInto(sess(), cmd, reduced.matches);
      } else {
        await sleep(reduced.matches ? 0 : 420);
      }
      c.out(headless.lines.slice(0, 5).map(function (l) { return [l, "t-dim"]; }));
      c.done(0, Math.max(38, Math.round(performance.now() - tStart)));
      c.state(T.tool_done, "t-ok");
      if (showMemTable) logAdd(x, memTable(x));
      await streamText(x, answer);
      setAIWorking(x, false);
    }

    aiSugg.addEventListener("click", function (e) {
      var b = e.target.closest("[data-q]");
      if (!b || !sess()) return;
      var i = +b.getAttribute("data-q"), sg = T.suggestions[i], x = sess().host;
      runAIQuery(x, sg.q, sg.cmd, fill(sg.a, values(x)), i === 1);
    });
    aiForm.addEventListener("submit", async function (e) {
      e.preventDefault();
      var text = aiInput.value.trim(), s = sess();
      if (!text || !s) return;
      aiInput.value = "";
      if (story.running && s.host === web) { story.fast = true; await story.done; }
      var x = s.host, low = text.toLowerCase();
      if (/disk|space|df|磁盘|磁碟|ディスク|디스크|空间|空間/.test(low)) {
        runAIQuery(x, text, T.suggestions[0].cmd, fill(T.suggestions[0].a, values(x)), false);
      } else if (/mem|ram|free|内存|記憶體|メモリ|메모리/.test(low)) {
        runAIQuery(x, text, T.suggestions[1].cmd, fill(T.suggestions[1].a, values(x)), true);
      } else if (/service|systemd|nginx|fail|服务|服務|サービス|서비스/.test(low)) {
        runAIQuery(x, text, T.suggestions[2].cmd, fill(T.suggestions[2].a, values(x)), false);
      } else if (/docker|container|容器|コンテナー|컨테이너/.test(low) && x.containers) {
        runAIQuery(x, text, "docker ps", x.containers.map(function (c) { return c.name + " (" + c.status + ")"; }).join(", "), false);
      } else if (/ip|net|网络|網路|ネットワーク|네트워크/.test(low)) {
        runAIQuery(x, text, "ip a", x.id + " · " + maskAddr(x.addr) + " (" + x.ifaces.join(", ") + ")", false);
      } else {
        logAdd(x, h("div", "msg-user", h("span", null, text)));
        streamText(x, T.free_text);
      }
    });

    // ------------------------------------------------------------ startup boot sequence & 502 story

    var story = { running: false, fast: false, done: Promise.resolve(), end: null };
    var visible = false;
    var bootDone = false;
    new IntersectionObserver(function (e) { visible = e[0].isIntersecting; }, { threshold: 0.15 }).observe(root);
    function storyWait(ms) { return story.fast ? Promise.resolve() : sleep(ms); }
    function onScreen() {
      return new Promise(function (resolve) {
        (function check() { if (story.fast || (visible && !document.hidden)) resolve(); else setTimeout(check, 250); })();
      });
    }
    function fast() { return story.fast; }

    var bootSkip = false;
    var bootSkipBtn = root.querySelector("[data-win-boot-skip]");
    if (bootSkipBtn) bootSkipBtn.addEventListener("click", function () { bootSkip = true; });
    if (bootEl) bootEl.addEventListener("click", function () { bootSkip = true; });

    async function playBoot() {
      if (!bootEl || reduced.matches) return;
      await bootVeilDone;
      bootSkip = false;
      bootEl.classList.remove("leaving");
      bootEl.hidden = false;
      var steps = $$("[data-step]", bootEl);
      steps.forEach(function (st) { st.className = "boot-step"; });
      for (var i = 0; i < steps.length; i++) {
        if (bootSkip) break;
        steps[i].classList.add("active");
        await sleep(340);
        steps[i].classList.remove("active");
        steps[i].classList.add("done");
      }
      if (!bootSkip) await sleep(180);
      bootEl.classList.add("leaving");
      await sleep(380);
      bootEl.hidden = true;
      bootEl.classList.remove("leaving");
      bootDone = true;
    }

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
      var t = tabs.filter(function (t2) { return t2.panes[0].host === web; })[0] || openTab(web, true);
      var s = t.panes[0];
      if (s.state === "closed") await submit(s);
      await onScreen();
      if (!bootDone) await playBoot();
      await storyWait(550);
      await typeInto(s, "curl -sI localhost | head -1", fast);
      await onScreen();
      await storyWait(550);
      logAdd(x, h("div", "msg-user", h("span", null, T.user_q)));
      setAIWorking(x, true);
      await storyWait(650);
      var steps = [
        [T.tool_run, [["systemctl is-active php8.3-fpm"]], [["failed", "t-red"]], null, 3, 62],
        null,
        [T.tool_edit, [["/etc/php/8.3/fpm/pool.d/www.conf"]], [["- listen = /run/php/php8.2-fpm.sock", "t-red"], ["+ listen = /run/php/php8.3-fpm.sock", "t-ok"]], function () { x.fixed = true; }, 0, 48],
        [T.tool_run, [["sudo systemctl restart php8.3-fpm"]], [], function () { x.broken = false; }, 0, 184],
      ];
      for (var i = 0; i < steps.length; i++) {
        await onScreen();
        if (!steps[i]) { await streamText(x, T.a1, fast); await storyWait(550); continue; }
        var c = toolCard(x, steps[i][0], steps[i][1], i < 3);
        c.meta(T.tool_ok);
        c.state(T.running, "t-blue");
        await storyWait(820);
        c.out(steps[i][2]);
        if (steps[i][3]) steps[i][3]();
        c.done(steps[i][4], steps[i][5]);
        c.state(T.tool_done, "t-ok");
        await storyWait(480);
      }
      await onScreen();
      await streamText(x, T.a2, fast);
      setAIWorking(x, false);
      await storyWait(650);
      await typeInto(s, "curl -sI localhost | head -1", fast);
      story.running = false;
      story.fast = false;
      story.end();
      if (sess() && sess().host === web && tool === "ai") aiSugg.hidden = false;
    }
    function replay() {
      if (story.running) return;
      var t = tabs.filter(function (t2) { return t2.panes[0].host === web; })[0];
      if (t) activate(t);
      setTool("ai", true);
      playStory();
    }

    // ------------------------------------------------------------ quick showcase presets below demo

    var presetBtns = $$("[data-preset]");
    function syncPresets() {
      presetBtns.forEach(function (b) {
        var k = b.getAttribute("data-preset");
        var on = (k === "schemes" && settingsOpen) || (k === "privacy" && privacy) ||
          (!settingsOpen && k === tool && k !== "privacy" && k !== "schemes" && k !== "reboot");
        b.classList.toggle("on", on);
      });
    }
    presetBtns.forEach(function (b) {
      b.addEventListener("click", function () {
        var k = b.getAttribute("data-preset");
        closeMetricPop();
        toggleMenu(false);
        toggleQuick(false);
        if (k === "reboot") {
          toggleSettings(false);
          bootDone = false;
          if (!story.running) replay(); else playBoot();
        } else if (k === "schemes") {
          settingsTab = "terminal";
          toggleSettings(true);
        } else if (k === "privacy") {
          setPrivacy(!privacy);
        } else if (k === "files") {
          dualFiles = true;
          if (!current) openTab(web);
          setTool("files", true);
        } else if (k === "docker") {
          if (!current || !sess() || !sess().host.containers) {
            var wt = tabs.filter(function (t2) { return t2.panes[0].host.containers; })[0];
            if (wt) activate(wt); else openTab(web);
          }
          setTool("docker", true);
        } else if (k === "ai") {
          if (!current) openTab(web);
          setTool("ai", true);
        } else {
          if (!current) openTab(web);
          setTool(k, true);
        }
        syncPresets();
      });
    });

    // ------------------------------------------------------------ live metrics ticker

    setInterval(function () {
      if (!visible || document.hidden) return;
      HOSTS.forEach(function (x) {
        var st = x.stat;
        st.cpu = clamp(st.cpu + (Math.random() - 0.47) * 5, Math.max(1, x.cpu - 3), x.cpu + 26);
        x.coresPct = x.coresPct.map(function (v) { return clamp(st.cpu + (Math.random() - 0.5) * 14, 1, 96); });
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
    first.paintBanner();
    tabs.push({ panes: [first], focus: 0, dir: "row" });
    var db = byId["db-primary"];
    db.connectedAt = Date.now() - 12 * 60e3;
    var second = newSession(db);
    tabs.push({ panes: [second], focus: 0, dir: "row" });
    connect(second);
    activate(tabs[0]);
    paintMode();
    paintModel();
    setTool("ai", true);
    playStory();

    if (paletteAdd) {
      var lines = {
        monitor: "pulse-line", usage: "bar-chart-horizontal-fill", host: "information-fill",
        files: "folder-3-line", docker: "ship-2-fill", ai: "robot-2-line",
        snippets: "code-s-slash-line", broadcast: "broadcast-fill",
      };
      var show = function () { root.scrollIntoView({ behavior: reduced.matches ? "auto" : "smooth", block: "center" }); };
      paletteAdd(data.palette.demo, Object.keys(lines).map(function (k) {
        return { icon: icon(lines[k]), label: fill(data.palette.open, { name: T.tools[k] }), run: function () { show(); setTool(k, true); } };
      }).concat([
        { icon: icon("palette-line"), label: T.settings_ui.scheme, run: function () { show(); settingsTab = "terminal"; toggleSettings(true); } },
        { icon: icon("eye-off-line"), label: T.privacy, run: function () { show(); setPrivacy(!privacy); } },
        { icon: icon("terminal-window-line"), label: T.local_terminal, run: function () { show(); openTab(localHost); } },
        { icon: icon("restart-line"), label: T.reboot, run: function () { show(); bootDone = false; if (!story.running) replay(); else playBoot(); } },
      ]).concat(HOSTS.map(function (x) {
        return { icon: icon(x.icon), label: fill(data.palette.connect, { host: x.id }), run: function () { show(); openTab(x); } };
      })));
    }
  })();

  // The demo window settles from a subtle tilt as it scrolls into place.
  (function tilt() {
    var demo = document.querySelector(".demo");
    if (!demo || reduced.matches) return;
    var raf = 0;
    function update() {
      raf = 0;
      var r = demo.getBoundingClientRect(), vh = window.innerHeight || 800;
      var p = clamp((vh - r.top) / (vh * 0.72), 0, 1);
      demo.style.setProperty("--tilt", (1 - p).toFixed(3));
    }
    function queue() { if (!raf) raf = requestAnimationFrame(update); }
    window.addEventListener("scroll", queue, { passive: true });
    window.addEventListener("resize", queue);
    update();
  })();

  // ---------------------------------------------------------------- enhanced AES-GCM vault lab

  (function seal() {
    var box = document.querySelector("[data-seal]");
    var c = window.crypto;
    if (!box || !c || !c.getRandomValues) return;
    box.hidden = false;
    var field = box.querySelector("[data-seal-input]");
    var plain = box.querySelector("[data-seal-plain]");
    var ivEl = box.querySelector("[data-seal-iv]");
    var ctEl = box.querySelector("[data-seal-ct]");
    var tagEl = box.querySelector("[data-seal-tag]");
    var bytes = box.querySelector("[data-seal-bytes]");
    var keyEl = box.querySelector("[data-seal-key]");
    var rekeyBtn = box.querySelector("[data-seal-rekey]");
    var copyBtn = box.querySelector("[data-seal-copy]");
    var presetBtns = $$("[data-seal-preset]", box);
    var key = null, seq = 0, anim = 0, lastHex = "";
    var subtle = c.subtle && window.isSecureContext ? c.subtle : null;

    function hex(a) { return Array.prototype.map.call(a, function (b) { return (b < 16 ? "0" : "") + b.toString(16); }).join(""); }
    function paintHex(fullHex) {
      lastHex = fullHex;
      var ivH = fullHex.slice(0, 24);
      var tagH = fullHex.slice(-32);
      var ctH = fullHex.slice(24, Math.max(24, fullHex.length - 32));
      if (ivEl) ivEl.textContent = ivH;
      if (ctEl) ctEl.textContent = ctH;
      if (tagEl) tagEl.textContent = tagH;
    }
    function scramble(text) {
      cancelAnimationFrame(anim);
      if (reduced.matches) { paintHex(text); return; }
      var t0 = performance.now(), digits = "0123456789abcdef";
      (function frame(t) {
        var p = Math.min(1, (t - t0) / 420), n = Math.floor(text.length * p), s = text.slice(0, n);
        for (var i = n; i < text.length; i++) s += digits[(Math.random() * 16) | 0];
        paintHex(s);
        if (p < 1) anim = requestAnimationFrame(frame);
      })(t0);
    }
    async function genKey() {
      var fp = hex(c.getRandomValues(new Uint8Array(6)));
      if (keyEl) keyEl.textContent = "256b·" + fp.slice(0, 4) + "…" + fp.slice(-4);
      if (subtle) {
        try { key = await subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt"]); } catch (e) { key = null; }
      }
      update();
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
    field.addEventListener("input", function () {
      presetBtns.forEach(function (b) { b.classList.toggle("on", b.getAttribute("data-seal-preset") === field.value); });
      update();
    });
    if (rekeyBtn) {
      rekeyBtn.addEventListener("click", function () {
        var ic = rekeyBtn.querySelector(".i");
        if (ic) { ic.classList.remove("spin"); void ic.offsetWidth; ic.classList.add("spin"); }
        genKey();
      });
    }
    if (copyBtn) {
      copyBtn.addEventListener("click", function () {
        if (!lastHex) return;
        copyText(lastHex, function () { toast(data.copy.copied); });
      });
    }
    presetBtns.forEach(function (b) {
      b.addEventListener("click", function () {
        field.value = b.getAttribute("data-seal-preset");
        presetBtns.forEach(function (x) { x.classList.toggle("on", x === b); });
        update();
      });
    });
    genKey();
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
