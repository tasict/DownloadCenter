// Download Center project site: language choice, the hero fragment bar and the download buttons from updates.json.
(function () {
  'use strict';

  // Google Analytics (GA4): visits, and which package and support links are clicked. Empty id = not loaded.
  var GA_ID = 'G-R03VP80PMS';
  if (GA_ID) {
    window.dataLayer = window.dataLayer || [];
    window.gtag = function () { window.dataLayer.push(arguments); };
    window.gtag('js', new Date());
    window.gtag('config', GA_ID);
    var ga = document.createElement('script');
    ga.async = true;
    ga.src = 'https://www.googletagmanager.com/gtag/js?id=' + encodeURIComponent(GA_ID);
    document.head.appendChild(ga);
    document.addEventListener('click', function (e) {
      var a = e.target.closest && e.target.closest('a');
      if (!a) return;
      if (a.classList.contains('arch')) {
        var m = /DownloadCenter_([^_]+)_([^.]+)\.qpkg/.exec(a.href);
        window.gtag('event', 'package_download', { version: m ? m[1] : '', arch: m ? m[2] : '' });
      } else if (/bobaboba\.me|paypal\.me/.test(a.href)) {
        window.gtag('event', 'support_click', { target: /paypal/.test(a.href) ? 'paypal' : 'boba' });
      } else if (/github\.com\/tasict\/DownloadCenter/.test(a.href)) {
        window.gtag('event', 'github_click');
      }
    });
  }

  // Remember an explicit language choice; both pages follow it instead of the browser language.
  var links = document.querySelectorAll('a[hreflang]');
  for (var i = 0; i < links.length; i++) {
    links[i].addEventListener('click', function () {
      try { localStorage.setItem('lang', this.getAttribute('hreflang')); } catch (e) { /* storage blocked */ }
    });
  }
  var langMenu = document.querySelector('.lang');
  document.addEventListener('click', function (e) {
    if (langMenu && langMenu.open && !langMenu.contains(e.target)) langMenu.open = false;
  });
  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape' && langMenu && langMenu.open) { langMenu.open = false; langMenu.querySelector('summary').focus(); }
  });

  // Fragment bar: pieces light in a scattered order, the percentage follows, then everything turns green.
  var frag = document.querySelector('[data-frag]');
  var still = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  if (frag && !still) {
    var cells = frag.querySelectorAll('.frag-cells i'), order = [], n = cells.length, seed = 20261002, k;
    for (k = 0; k < n; k++) order.push(k);
    for (k = n - 1; k > 0; k--) {
      seed = (seed * 16807) % 2147483647;
      var j = seed % (k + 1), t = order[k]; order[k] = order[j]; order[j] = t;
    }
    var step = 2600 / n;
    for (k = 0; k < n; k++) cells[order[k]].style.setProperty('--d', Math.round(250 + k * step) + 'ms');
    var pct = frag.querySelector('[data-pct]'), done = pct ? pct.textContent : '', start = Date.now() + 250;
    frag.classList.add('run');
    if (pct) {
      pct.textContent = '0%';
      var tick = function () {
        var p = Math.min(100, Math.floor((Date.now() - start) / 2600 * 100));
        if (p < 100) { pct.textContent = Math.max(0, p) + '%'; requestAnimationFrame(tick); }
        else setTimeout(function () { pct.textContent = done; }, 250);
      };
      requestAnimationFrame(tick);
    }
  }

  // Download buttons. updates.json is published next to the site by CI; until there is a release the page keeps its
  // "coming soon" text and the link to the Releases page.
  var box = document.querySelector('[data-release]');
  if (!box || !window.fetch) return;
  var lang = document.documentElement.lang || 'en';
  var T = JSON.parse(box.getAttribute('data-t') || '{}');
  var ARCH = [
    ['x86_64', T.x86_64 || 'Intel and AMD (x86_64)'],
    ['arm_64', T.arm_64 || 'ARM 64-bit (arm_64)'],
    ['arm-x41', T['arm-x41'] || 'ARMv7 (arm-x41)'],
    ['arm-x31', T['arm-x31'] || 'ARMv7 (arm-x31)']
  ];
  function el(tag, attrs, kids) {
    var e = document.createElement(tag), a;
    for (a in attrs || {}) if (Object.prototype.hasOwnProperty.call(attrs, a)) {
      if (a === 'text') e.textContent = attrs[a]; else e.setAttribute(a, attrs[a]);
    }
    (kids || []).forEach(function (c) { if (c) e.appendChild(typeof c === 'string' ? document.createTextNode(c) : c); });
    return e;
  }
  function fill(s, v) { return String(s).replace(/\{(\w+)\}/g, function (m, k) { return v.hasOwnProperty(k) ? v[k] : m; }); }
  function size(b) {
    if (!(b > 0)) return '';
    return b >= 1048576 ? (b / 1048576).toFixed(1) + ' MB' : Math.max(1, Math.round(b / 1024)) + ' KB';
  }
  function date(d) {
    var m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(d || '');
    if (!m) return d || '';
    try { return new Date(+m[1], +m[2] - 1, +m[3]).toLocaleDateString(lang, { year: 'numeric', month: 'long', day: 'numeric' }); }
    catch (e) { return d; }
  }
  function safeUrl(u) { return /^https:\/\/github\.com\//.test(u || '') ? u : null; }
  function svg() {
    var ns = 'http://www.w3.org/2000/svg', s = document.createElementNS(ns, 'svg'), p = document.createElementNS(ns, 'path');
    s.setAttribute('viewBox', '0 0 20 20'); s.setAttribute('width', '18'); s.setAttribute('height', '18'); s.setAttribute('aria-hidden', 'true');
    p.setAttribute('d', 'M10 3v10m0 0-4-4m4 4 4-4M4 16.5h12'); p.setAttribute('fill', 'none'); p.setAttribute('stroke', 'currentColor');
    p.setAttribute('stroke-width', '1.8'); p.setAttribute('stroke-linecap', 'round'); p.setAttribute('stroke-linejoin', 'round');
    s.appendChild(p);
    return s;
  }

  fetch(box.getAttribute('data-src'), { cache: 'no-cache' }).then(function (r) {
    if (!r.ok) throw new Error('status ' + r.status);
    return r.json();
  }).then(function (feed) {
    var list = (feed && feed.releases) || [], rel = null, pre = null, k;
    for (k = 0; k < list.length; k++) {
      if (feed.latest && list[k].version === feed.latest) rel = list[k];
      if (feed.latest_prerelease && list[k].version === feed.latest_prerelease) pre = list[k];
    }
    if (!rel) for (k = 0; k < list.length; k++) if (!list[k].prerelease) { rel = list[k]; break; }
    if (!rel || !rel.assets) return;
    var ul = el('ul', { 'class': 'arches' }), any = false;
    ARCH.forEach(function (a) {
      var as = rel.assets[a[0]], url = as && safeUrl(as.url);
      if (!url) return;
      any = true;
      ul.appendChild(el('li', null, [el('a', { 'class': 'arch', href: url, download: '' }, [
        el('b', null, [svg(), a[1]]),
        el('small', { 'class': 'num', text: size(as.size), title: as.name || '' })
      ])]));
    });
    if (!any) return;
    var body = [
      el('h3', { text: fill(T.version || 'Version {v}', { v: rel.version }) }),
      el('p', { 'class': 'rel-meta num', text: fill(T.released || 'Released {date}', { date: date(rel.date) }) }),
      ul
    ];
    var sums = safeUrl(rel.sums_url), relUrl = safeUrl(rel.url);
    if (sums || relUrl) {
      var p = el('p', { 'class': 'sums' });
      if (sums) p.appendChild(el('a', { href: sums, text: T.sums || 'SHA-256 checksums' }));
      if (sums && relUrl) p.appendChild(document.createTextNode('　'));
      if (relUrl) p.appendChild(el('a', { href: relUrl, text: T.page || 'Release page on GitHub' }));
      body.push(p);
    }
    if (rel.notes) {
      // Release notes are Markdown from the changelog; shown as plain text, never as HTML.
      body.push(el('details', null, [el('summary', { text: T.notes || 'What’s new' }), el('div', { 'class': 'notes', text: rel.notes })]));
    }
    if (pre && pre.version !== rel.version && safeUrl(pre.url)) {
      body.push(el('p', { 'class': 'sums' }, [el('a', { href: safeUrl(pre.url), text: fill(T.pre || 'Try the pre-release {v}', { v: pre.version }) })]));
    }
    while (box.firstChild) box.removeChild(box.firstChild);
    body.forEach(function (b) { box.appendChild(b); });
    var hero = document.querySelectorAll('[data-latest]');
    for (k = 0; k < hero.length; k++) hero[k].textContent = fill(hero[k].getAttribute('data-latest'), { v: rel.version });
  }).catch(function () { /* keep the "coming soon" fallback */ });
})();
