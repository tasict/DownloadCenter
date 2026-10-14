/* Download Center's "Add download" widget for Deskpane (shared/deskpane/widgets.json). URLs, magnet links and .torrent
   files dropped on it or pasted into it are added with the user's usual settings, as POST /tasks does them.

   Deskpane imports this module and runs it in its own page, with the user's QTS session, so it defines nothing global:
   one custom element, an open shadow root (Deskpane tells the widget's own controls from the rest of the widget through
   composedPath), names and messages only ever set as text. Deskpane needs a modern browser, so this file is modern
   JavaScript, unlike the web UI's ES5; it has no import or export, so `node --check` reads it as it is. */

const BASE = '/DownloadCenter/';
const TAG = 'downloadcenter-add';

/* ---------- language: the one chosen in QTS, like the web UI (js/i18n.js) ---------- */
const LANGS = ['TCH', 'SCH', 'ENG', 'JPN', 'KOR', 'GER', 'FRE', 'SPA', 'ITA', 'POR', 'RUS', 'DUT', 'THA'];
const BROWSER = [[/^zh-(tw|hk|mo|hant)/, 'TCH'], [/^zh/, 'SCH'], [/^de/, 'GER'], [/^es/, 'SPA'], [/^fr/, 'FRE'], [/^it/, 'ITA'], [/^ja/, 'JPN'],
  [/^ko/, 'KOR'], [/^ru/, 'RUS'], [/^nl/, 'DUT'], [/^th/, 'THA'], [/^pt/, 'POR']];

function cookie(name) {
  for (const part of document.cookie.split(/;\s*/)) {
    const i = part.indexOf('=');
    if (i > 0 && part.slice(0, i) === name) {
      try {
        return decodeURIComponent(part.slice(i + 1));
      } catch {
        return '';
      }
    }
  }
  return '';
}

function langCode() {
  const q = cookie('nas_lang');
  let code = q && q !== 'auto' ? q.toUpperCase() : '';
  if (!code) {
    const l = (navigator.language || 'en').toLowerCase();
    code = (BROWSER.find(([re]) => re.test(l)) || [null, 'ENG'])[1];
  }
  if (code === 'ESM') code = 'SPA';
  return LANGS.includes(code) ? code : 'ENG';
}

const LANG = langCode();
let dict = null;
/* One request per page, however many widgets and previews there are; the code is from the fixed list above */
const dictReady =
  LANG === 'ENG'
    ? Promise.resolve()
    : fetch(`${BASE}widgets/lang/${LANG}.json`, { credentials: 'same-origin' })
        .then((r) => (r.ok ? r.json() : null))
        .then((d) => {
          if (d && typeof d === 'object') dict = d;
        })
        .catch(() => {});

/* DC.t(key, {name: value}), as in the web UI: the key is the English text as a plain literal, collected by mklang */
const DC = {
  t(s, params) {
    let r = (dict && typeof dict[s] === 'string' && dict[s]) || s;
    if (params) r = r.replace(/\{(\w+)\}/g, (m, k) => (Object.prototype.hasOwnProperty.call(params, k) ? String(params[k]) : m));
    return r;
  },
};

/* ---------- symbols: Download Center's own (js/dom.js), constant markup ---------- */
const ICONS = {
  link: '<path class="a-l1" d="M10 13.5a4 4 0 0 0 5.7.3l2.8-2.8a4 4 0 0 0-5.7-5.7l-1.3 1.3"/><path class="a-l2" d="M14 10.5a4 4 0 0 0-5.7-.3L5.5 13a4 4 0 0 0 5.7 5.7l1.3-1.3"/>',
  magnet:
    '<g class="a-mag"><path d="M6 8v4.5a6 6 0 0 0 12 0V8"/><path d="M10 8v4.5a2 2 0 0 0 4 0V8"/><path d="M6 8h4M14 8h4"/><path d="M6 10.8h4M14 10.8h4"/></g><circle class="a-p a-p1" cx="8" cy="3.5" r=".95"/><circle class="a-p a-p2" cx="16" cy="3.5" r=".95"/><circle class="a-p a-p3" cx="12" cy="2.5" r=".95"/>',
  torrent: '<path d="M6 3.5h8l4.5 4.5v12.5H6z"/><path class="a-fold" d="M14 3.5V8h4.5"/><g class="a-bits"><path d="M9 12.5h2M13 12.5h2M9 15.5h2M13 15.5h2M9 18h2"/></g>',
  done: '<circle cx="12" cy="12" r="8.5"/><path class="a-tick" d="M8 12.4l2.8 2.8L16.2 9.6"/>',
  error: '<g class="a-shake"><path d="M10.3 4.3a2 2 0 0 1 3.4 0l7.4 12.8a2 2 0 0 1-1.7 3H4.6a2 2 0 0 1-1.7-3z"/><path d="M12 9.5v4"/><path d="M12 16.8h.01"/></g>',
  paste: '<rect x="5" y="4.5" width="14" height="16" rx="2"/><path d="M9 4.5V3.8a.8.8 0 0 1 .8-.8h4.4a.8.8 0 0 1 .8.8v.7"/><g class="a-dn"><path d="M12 9v6.5"/><path d="M9.5 13l2.5 2.5 2.5-2.5"/></g>',
  chev: '<path d="M9.5 5.5L16 12l-6.5 6.5"/>',
};

const SVG = 'http://www.w3.org/2000/svg';
function icon(name, cls) {
  const span = document.createElement('span');
  span.className = 'ic' + (cls ? ' ' + cls : '');
  span.setAttribute('aria-hidden', 'true');
  const svg = document.createElementNS(SVG, 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.innerHTML = ICONS[name]; // constant markup from the table above, never data
  span.appendChild(svg);
  return span;
}

/* el('div', {class: 'x', text: name, onclick: fn}, [children]) — text always as text */
function el(tag, attrs, kids) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === null || v === undefined || v === false) continue;
    if (k === 'text') n.textContent = v;
    else if (k.startsWith('on')) n.addEventListener(k.slice(2), v);
    else n.setAttribute(k, v === true ? '' : v);
  }
  for (const c of kids || []) if (c) n.appendChild(c);
  return n;
}

const STYLE = `
:host { display: flex; position: relative; background: transparent; color: var(--dp-fg, #16161c); font-family: var(--dp-font, system-ui, sans-serif);
  font-size: 13px; line-height: 1.3; -webkit-tap-highlight-color: transparent; }
* { box-sizing: border-box; }
[hidden] { display: none !important; }
.w { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 8px; padding: var(--dp-widget-pad, 14px); }
.head { font-size: 11.5px; font-weight: 600; color: var(--dp-fg-2, rgb(22 22 28 / .62)); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.ic { display: inline-flex; width: 18px; height: 18px; flex: none; }
.ic svg { width: 100%; height: 100%; fill: none; stroke: currentColor; stroke-width: 1.7; stroke-linecap: round; stroke-linejoin: round; overflow: visible; }
.ic svg * { transform-box: view-box; }
.ic .a-p { fill: currentColor; stroke: none; opacity: 0; }
.glyphs { display: flex; gap: 6px; color: var(--dp-fg-2, rgb(22 22 28 / .62)); }
.glyphs .ic { width: 24px; height: 24px; }
.hint { color: var(--dp-fg-2, rgb(22 22 28 / .62)); font-size: 12.5px; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
.idle { display: grid; gap: 8px; align-content: start; }
form { display: flex; gap: 6px; margin: 0; }
input { flex: 1; min-width: 0; height: 30px; padding: 0 10px; border: 0; border-radius: 9px; background: var(--dp-fill-2, rgb(0 0 0 / .09));
  color: inherit; font: inherit; outline: none; }
input::placeholder { color: var(--dp-fg-3, rgb(22 22 28 / .38)); }
input:focus-visible, button:focus-visible { box-shadow: 0 0 0 2px var(--dp-accent, #2f7bff); }
button { font: inherit; color: inherit; border: 0; cursor: pointer; background: none; padding: 0; }
.add { height: 30px; padding: 0 14px; border-radius: 999px; background: var(--dp-accent, #2f7bff); color: #fff; font-weight: 600; flex: none; }
.add:disabled { opacity: .55; cursor: default; }
.paste { width: 30px; height: 30px; flex: none; display: grid; place-items: center; border-radius: 9px; background: var(--dp-fill-2, rgb(0 0 0 / .09)); }
.paste .ic { width: 17px; height: 17px; }
/* small: the symbols and the hint on top, the field at the bottom */
.w[data-size="s"] form { margin-top: auto; }
/* medium: the field first, the hint row under it, the results below */
.w[data-size="m"] input, .w[data-size="m"] .add, .w[data-size="m"] .paste { height: 34px; }
.w[data-size="m"] .paste { width: 34px; }
.w[data-size="m"] .idle { display: flex; align-items: center; gap: 8px; }
.w[data-size="m"] .glyphs { gap: 3px; flex: none; }
.w[data-size="m"] .glyphs .ic { width: 16px; height: 16px; }
.w[data-size="m"] .hint { font-size: 12px; -webkit-line-clamp: 1; }
.results { display: grid; gap: 2px; min-height: 0; }
.w[data-size="m"] .results:not(:empty) { border-top: 1px solid var(--dp-sep, rgb(0 0 0 / .09)); padding-top: 6px; }
.results:empty { display: none; }
.res { display: block; width: 100%; text-align: left; }
.res:disabled { cursor: default; }
.row { display: flex; align-items: center; gap: 7px; width: 100%; min-height: 24px; text-align: left; border-radius: 7px; }
.row .ic { width: 15px; height: 15px; }
.row .tx { flex: 1; min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.row .go { color: var(--dp-fg-3, rgb(22 22 28 / .38)); width: 13px; height: 13px; }
.res:not(:disabled):hover .tx, .res:not(:disabled):hover .what { text-decoration: underline; text-underline-offset: 2px; }
.ok > .ic:first-child, .ok.big .sym { color: var(--dp-success, #23a855); }
.bad > .ic:first-child, .bad.big .sym { color: var(--dp-danger, #e5413a); }
/* small: one result replaces the symbols and the hint for a while */
.big { display: grid; gap: 4px; align-content: start; width: 100%; text-align: left; }
.big .sym { width: 24px; height: 24px; }
.big .say { font-size: 15px; font-weight: 650; line-height: 1.2; }
.big .what { font-size: 12px; color: var(--dp-fg-2, rgb(22 22 28 / .62)); display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; word-break: break-word; }
.big.bad .what { -webkit-line-clamp: 3; }
/* the drop moment: an accent edge on the whole pane, Download Center's symbols at play */
.drop { position: absolute; inset: 0; display: none; flex-direction: column; justify-content: center; gap: 8px; padding: var(--dp-widget-pad, 14px);
  border-radius: var(--dp-r-sheet, 22px); box-shadow: inset 0 0 0 2px var(--dp-accent, #2f7bff);
  background: color-mix(in srgb, var(--dp-accent, #2f7bff) 12%, transparent); color: var(--dp-accent, #2f7bff); pointer-events: none; }
.drop .glyphs { color: inherit; }
.drop .glyphs .ic { width: 28px; height: 28px; }
.drop .say { font-size: 15px; font-weight: 650; }
:host([dropping]) .drop { display: flex; }
:host([dropping]) .w { visibility: hidden; }
:host([dropping]) .a-l1 { animation: link-a .5s cubic-bezier(.3,1.6,.5,1) infinite alternate; }
:host([dropping]) .a-l2 { animation: link-b .5s cubic-bezier(.3,1.6,.5,1) infinite alternate; }
.a-mag { transform-origin: 12px 13px; }
:host([dropping]) .a-mag { animation: wiggle .7s ease infinite; }
:host([dropping]) .a-p { animation: pull .7s ease-in infinite; }
:host([dropping]) .a-p2 { animation-delay: .12s; }
:host([dropping]) .a-p3 { animation-delay: .24s; }
.a-fold { transform-origin: 16px 6px; }
:host([dropping]) .a-fold { animation: fold .6s ease infinite; }
:host([dropping]) .a-bits { animation: hop .6s .1s ease infinite; }
.a-tick { stroke-dasharray: 14; animation: draw14 .6s .1s ease-out both; }
.a-shake { transform-origin: 12px 20px; animation: shake .6s ease both; }
@keyframes link-a { 0% { transform: translate(2.5px,-2.5px); } 60% { transform: translate(-.6px,.6px); } 100% { transform: none; } }
@keyframes link-b { 0% { transform: translate(-2.5px,2.5px); } 60% { transform: translate(.6px,-.6px); } 100% { transform: none; } }
@keyframes wiggle { 0%,100% { transform: none; } 25% { transform: rotate(-10deg); } 50% { transform: rotate(8deg); } 75% { transform: rotate(-4deg); } }
@keyframes pull { 0% { transform: translateY(-2px); opacity: 0; } 30% { opacity: 1; } 100% { transform: translateY(5px); opacity: 0; } }
@keyframes fold { 0%,100% { transform: none; } 45% { transform: rotate(-28deg) translate(-1px,1px); } }
@keyframes hop { 0%,100% { transform: none; } 45% { transform: translateY(-1.5px); } }
@keyframes draw14 { from { stroke-dashoffset: 14; } to { stroke-dashoffset: 0; } }
@keyframes shake { 0%,100% { transform: none; } 20% { transform: rotate(-12deg); } 40% { transform: rotate(10deg); } 60% { transform: rotate(-6deg); } 80% { transform: rotate(3deg); } }
@media (prefers-reduced-motion: reduce) { *, *::before, *::after { animation: none !important; } .a-tick { stroke-dasharray: none; } }
`;

/* What a drop or a paste may carry: download links only */
const LINK = /^(https?|ftps?|sftp|scp|magnet|thunder|flashget|qqdl):/i;
/* Deskpane's own drags (desktop icons) are not ours */
const DESKPANE_DRAG = 'application/x-deskpane-items';
const RESULT_MS = 8000;

function linksIn(text) {
  return String(text || '')
    .split(/\s+/)
    .map((s) => s.trim())
    .filter((s) => s && !s.startsWith('#') && LINK.test(s));
}

/* An API call as the web UI makes it: the session cookie, the CSRF header, the UI language */
async function api(path, body) {
  const headers = { 'X-Requested-With': 'XMLHttpRequest', 'X-DC-Lang': LANG, 'X-DC-Via': 'widget', Accept: 'application/json' };
  let data = body;
  if (!(body instanceof FormData)) {
    headers['Content-Type'] = 'application/json';
    data = JSON.stringify(body);
  }
  let res;
  try {
    res = await fetch(BASE + 'api/v1/' + path, { method: 'POST', credentials: 'same-origin', headers, body: data });
  } catch {
    return { status: 0, body: { error: { code: 'network', message: DC.t('Cannot connect to the NAS.') } } };
  }
  let json = null;
  try {
    json = await res.json();
  } catch {
    /* not JSON: a proxy or QTS error page */
  }
  if (!json || typeof json !== 'object') json = { error: { code: 'failed', message: DC.t('An error occurred. Try again later.') } };
  return { status: res.status, body: json };
}

class DownloadCenterAdd extends HTMLElement {
  #ctx = null;
  #shape = '';
  #results = []; // newest first: {level: 'ok'|'bad', say, what, row, id, title}
  #timer = 0;
  #busy = false;
  #drag = 0;
  #els = null;

  constructor() {
    super();
    const root = this.attachShadow({ mode: 'open' });
    root.appendChild(el('style', { text: STYLE }));
    this.addEventListener('dragenter', this.#dragEnter);
    this.addEventListener('dragover', this.#dragOver);
    this.addEventListener('dragleave', this.#dragLeave);
    this.addEventListener('drop', this.#onDrop);
  }

  set ctx(ctx) {
    this.#ctx = ctx;
    /* Deskpane sends a new ctx whenever anything changes, visibility included: rebuild only when the layout changes,
       so a half-typed URL and the results stay */
    const shape = `${ctx.size}|${ctx.shell}|${ctx.preview ? 1 : 0}`;
    if (shape === this.#shape) return;
    this.#shape = shape;
    const typed = this.#els ? this.#els.input.value : '';
    dictReady.then(() => {
      if (this.#ctx === ctx || this.#shape === shape) this.#build(typed);
    });
  }

  get ctx() {
    return this.#ctx;
  }

  disconnectedCallback() {
    clearTimeout(this.#timer);
  }

  get #size() {
    return this.#ctx && this.#ctx.size === 'm' ? 'm' : 's';
  }

  get #phone() {
    return !!this.#ctx && this.#ctx.shell === 'phone';
  }

  #build(typed) {
    const root = this.shadowRoot;
    while (root.childNodes.length > 1) root.removeChild(root.lastChild);
    const s = this.#size, phone = this.#phone, preview = !!(this.#ctx && this.#ctx.preview);
    /* Holding the field on a phone (for the system's paste menu) must not pick the widget up */
    const keep = (e) => e.stopPropagation();
    const input = el('input', {
      type: 'text', inputmode: 'url', enterkeyhint: 'go', autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false',
      'aria-label': DC.t('Paste URLs or magnet links'), placeholder: s === 'm' ? DC.t('Paste URLs or magnet links') : DC.t('Paste a URL'),
      disabled: preview, onpointerdown: keep, onpaste: (e) => this.#onPaste(e),
    });
    input.value = typed || '';
    const add = s === 'm' ? el('button', { class: 'add', type: 'submit', text: DC.t('Add'), disabled: preview, onpointerdown: keep }) : null;
    const canPaste = phone && !!(navigator.clipboard && navigator.clipboard.readText) && window.isSecureContext;
    const paste = canPaste
      ? el('button', { class: 'paste', type: 'button', title: DC.t('Paste from clipboard'), 'aria-label': DC.t('Paste from clipboard'), disabled: preview, onpointerdown: keep, onclick: () => this.#fromClipboard() }, [icon('paste')])
      : null;
    const form = el('form', { onsubmit: (e) => { e.preventDefault(); this.#submit(); } }, [input, paste, add]);
    const glyphs = () => el('span', { class: 'glyphs' }, [icon('link'), icon('magnet'), icon('torrent')]);
    let hint;
    if (phone) hint = s === 'm' ? DC.t('Paste URLs or magnet links to add them') : DC.t('Paste a URL to add it');
    else hint = s === 'm' ? DC.t('Or drop URLs and .torrent files here') : DC.t('Drop a URL or .torrent file here');
    const idle = el('div', { class: 'idle' }, [glyphs(), el('span', { class: 'hint', text: hint })]);
    const results = el('div', { class: 'results', role: 'status', 'aria-live': 'polite' });
    const w = el('div', { class: 'w', 'data-size': s }, s === 'm'
      ? [el('div', { class: 'head', text: DC.t('Add download') }), form, idle, results]
      : [el('div', { class: 'head', text: DC.t('Add download') }), idle, results, form]);
    const drop = el('div', { class: 'drop', 'aria-hidden': 'true' }, [glyphs(), el('span', { class: 'say', text: DC.t('Drop to add') })]);
    root.append(w, drop);
    this.#els = { input, add, paste, idle, results };
    this.#render();
  }

  /* The results: in small, the newest one in place of the symbols and the hint; in medium, the newest two as rows */
  #render() {
    const e = this.#els;
    if (!e) return;
    e.results.replaceChildren();
    if (this.#size === 's') {
      const r = this.#results[0];
      e.idle.hidden = !!r;
      if (r) {
        const sym = icon(r.level === 'ok' ? 'done' : 'error', 'sym');
        e.results.appendChild(this.#target(r, el('span', { class: 'big ' + r.level }, [sym, el('span', { class: 'say', text: r.say }), r.what ? el('span', { class: 'what', text: r.what }) : null])));
      }
    } else {
      for (const r of this.#results.slice(0, 2))
        e.results.appendChild(this.#target(r, el('span', { class: 'row ' + r.level }, [icon(r.level === 'ok' ? 'done' : 'error'), el('span', { class: 'tx', text: r.row || r.say }), r.id ? icon('chev', 'go') : null])));
    }
    e.input.disabled = this.#busy || !!(this.#ctx && this.#ctx.preview);
    if (e.add) {
      e.add.disabled = e.input.disabled;
      e.add.textContent = this.#busy ? DC.t('Adding…') : DC.t('Add');
    }
  }

  /* A result is a button that opens its task, so Deskpane leaves the click to the widget */
  #target(r, inner) {
    const b = el('button', { type: 'button', class: 'res', disabled: !r.id, title: r.title || null, onpointerdown: (ev) => ev.stopPropagation() }, [inner]);
    if (r.id) b.addEventListener('click', () => this.#ctx && this.#ctx.open(`${BASE}#task/${r.id}`));
    return b;
  }

  #show(list) {
    this.#results = list.concat(this.#results).slice(0, 2);
    this.#render();
    clearTimeout(this.#timer);
    if (this.#size === 's')
      this.#timer = setTimeout(() => {
        this.#results = [];
        this.#render();
      }, RESULT_MS);
  }

  /* A paste keeps its line breaks (an <input> would join the lines into one long string) */
  #onPaste(e) {
    const text = e.clipboardData && e.clipboardData.getData('text');
    if (!text || !/\s/.test(text.trim())) return;
    e.preventDefault();
    const inp = this.#els.input;
    const v = inp.value, a = inp.selectionStart ?? v.length, b = inp.selectionEnd ?? v.length;
    inp.value = v.slice(0, a) + text.trim().replace(/\s+/g, ' ') + v.slice(b);
  }

  async #fromClipboard() {
    let text = '';
    try {
      text = await navigator.clipboard.readText();
    } catch {
      this.#show([{ level: 'bad', say: DC.t('Cannot read the clipboard. Paste into the input box instead') }]);
      return;
    }
    const links = linksIn(text);
    if (!links.length) {
      this.#show([{ level: 'bad', say: DC.t('There is no URL on the clipboard') }]);
      return;
    }
    this.#addLinks(links);
  }

  #submit() {
    const links = linksIn(this.#els.input.value);
    if (!links.length) {
      if (this.#els.input.value.trim()) this.#show([{ level: 'bad', say: DC.t('This type of URL is not supported') }]);
      return;
    }
    this.#addLinks(links, true);
  }

  get #start() {
    return !this.#ctx || this.#ctx.config.start !== false;
  }

  async #addLinks(links, fromField) {
    if (this.#busy) return;
    this.#busy = true;
    this.#render();
    const one = links.length === 1;
    const { status, body } = await api('tasks', one ? { source: links[0], start: this.#start } : { sources: links, start: this.#start });
    this.#busy = false;
    if (fromField && (status === 200 || status === 409)) this.#els.input.value = '';
    if (one || !Array.isArray(body.results)) this.#show([this.#single(status, body, links[0])]);
    else this.#show(this.#many(body.results));
  }

  async #addTorrents(files) {
    if (this.#busy) return;
    this.#busy = true;
    this.#render();
    const each = [];
    for (const f of files) {
      const fd = new FormData();
      fd.append('file', f, f.name);
      fd.append('start', this.#start ? '1' : '0');
      const { status, body } = await api('tasks/torrent', fd);
      each.push(this.#single(status, body, f.name));
    }
    this.#busy = false;
    if (each.length === 1) {
      this.#show(each);
      return;
    }
    const added = each.filter((r) => r.added).length;
    const failed = each.filter((r) => r.level === 'bad');
    this.#show([{ level: added ? 'ok' : 'bad', say: added ? DC.t('{n} downloads added', { n: added }) : DC.t('No downloads added') }].concat(failed.slice(0, 1)));
  }

  /* One source: 200 {id, name, merged}, 409 {error: duplicate, id}, or another error */
  #single(status, body, source) {
    const name = (body && body.name) || source;
    if (status === 200 && body.id) return { level: 'ok', added: true, say: DC.t('Added'), what: name, row: DC.t('Added: {name}', { name }), id: body.id };
    const err = body.error || {};
    if (err.code === 'duplicate' && body.id) return { level: 'ok', say: DC.t('Already in the list'), what: name, row: DC.t('Already in the list: {name}', { name }), id: body.id };
    return { level: 'bad', say: err.message || DC.t('An error occurred. Try again later.'), title: source };
  }

  /* Several sources: 200 {results: [{source, id, name, merged, error}]} */
  #many(results) {
    const ok = results.filter((r) => !r.error).length;
    const dup = results.filter((r) => r.error && r.error.code === 'duplicate').length;
    const failed = results.filter((r) => r.error && r.error.code !== 'duplicate');
    let say = DC.t('No downloads added');
    if (ok) say = DC.t('{n} downloads added', { n: ok });
    else if (dup) say = DC.t('{n} already in the list', { n: dup });
    const out = [{ level: ok || dup ? 'ok' : 'bad', say }];
    for (const r of failed.slice(0, 1)) out.push({ level: 'bad', say: r.error.message || DC.t('An error occurred. Try again later.'), title: r.source });
    return out;
  }

  /* ---------- drag and drop ---------- */
  #accepts(e) {
    const types = Array.from((e.dataTransfer && e.dataTransfer.types) || []);
    if (!this.#ctx || this.#ctx.preview || this.#busy || types.includes(DESKPANE_DRAG)) return false;
    return types.includes('Files') || types.includes('text/uri-list') || types.includes('text/plain');
  }

  #dragEnter = (e) => {
    if (!this.#accepts(e)) return;
    e.preventDefault();
    this.#drag++;
    this.setAttribute('dropping', '');
  };

  #dragOver = (e) => {
    if (!this.#accepts(e)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'copy';
    this.setAttribute('dropping', '');
  };

  #dragLeave = () => {
    this.#drag = Math.max(0, this.#drag - 1);
    if (!this.#drag) this.removeAttribute('dropping');
  };

  #onDrop = (e) => {
    const ok = this.#accepts(e);
    this.#drag = 0;
    this.removeAttribute('dropping');
    if (!ok) return;
    e.preventDefault();
    e.stopPropagation();
    const dt = e.dataTransfer;
    const files = Array.from(dt.files || []);
    if (files.length) {
      const torrents = files.filter((f) => /\.torrent$/i.test(f.name));
      if (!torrents.length) {
        this.#show([{ level: 'bad', say: DC.t('Only .torrent files can be added here') }]);
        return;
      }
      this.#addTorrents(torrents);
      return;
    }
    const links = linksIn(dt.getData('text/uri-list') || dt.getData('text/plain'));
    if (!links.length) {
      this.#show([{ level: 'bad', say: DC.t('There is no URL in what you dropped') }]);
      return;
    }
    this.#addLinks(links);
  };
}

if (!customElements.get(TAG)) customElements.define(TAG, DownloadCenterAdd);
