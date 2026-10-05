/* DOM helpers, the icon set, formatters, toasts and dialogs shared by every view (window.DC). */
(function(){
	'use strict';
	var DC = window.DC = window.DC || {};
	/* index.html has a static boot line; show it in the UI language (dom.js runs right after that markup) */
	(function(){ var b = document.querySelector('#app .boot'); if(b && DC.t) b.textContent = DC.t('載入中…'); })();

	/* ---------- icon set (constant markup, never built from data) ---------- */
	var ICONS = {
		down:'<path class="a-tray" d="M4 15v3a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-3"/><g class="a-drop"><path d="M12 3.5v11"/><path d="M7.5 10l4.5 4.5 4.5-4.5"/></g>',
		wait:'<circle cx="12" cy="12" r="8.5"/><path class="a-hand" d="M12 12V7"/><path class="a-hand2" d="M12 12h3.5"/>',
		pause:'<path class="a-l" d="M9 6.5v11"/><path class="a-r" d="M15 6.5v11"/>',
		seed:'<circle class="a-ring" cx="12" cy="10" r="3"/><circle class="a-ring a-ring2" cx="12" cy="10" r="3"/><path d="M12 20.5v-8"/><g class="a-leaf"><path d="M12 13.5c0-3.2 2.2-5.5 5.5-5.5 0 3.2-2.2 5.5-5.5 5.5z"/><path d="M12 15.5c0-2.6-1.8-4.3-4.5-4.3 0 2.6 1.8 4.3 4.5 4.3z"/></g><path d="M7.5 20.5h9"/>',
		done:'<circle cx="12" cy="12" r="8.5"/><path class="a-tick" d="M8 12.4l2.8 2.8L16.2 9.6"/>',
		check:'<path class="a-cells" d="M4 20h16"/><g class="a-lens"><circle cx="11" cy="10" r="5"/><path d="M14.6 13.6L18 17"/></g>',
		move:'<path d="M3 8.5a2 2 0 0 1 2-2h3.8l2 2H19a2 2 0 0 1 2 2V18a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><g class="a-slide"><path d="M7 14h7"/><path d="M11.5 11.5L14 14l-2.5 2.5"/></g>',
		error:'<g class="a-shake"><path d="M10.3 4.3a2 2 0 0 1 3.4 0l7.4 12.8a2 2 0 0 1-1.7 3H4.6a2 2 0 0 1-1.7-3z"/><path d="M12 9.5v4"/><path d="M12 16.8h.01"/></g>',
		plus:'<g class="a-rot"><path d="M12 5v14"/><path d="M5 12h14"/></g>',
		link:'<path class="a-l1" d="M10 13.5a4 4 0 0 0 5.7.3l2.8-2.8a4 4 0 0 0-5.7-5.7l-1.3 1.3"/><path class="a-l2" d="M14 10.5a4 4 0 0 0-5.7-.3L5.5 13a4 4 0 0 0 5.7 5.7l1.3-1.3"/>',
		magnet:'<g class="a-mag"><path d="M6 8v4.5a6 6 0 0 0 12 0V8"/><path d="M10 8v4.5a2 2 0 0 0 4 0V8"/><path d="M6 8h4M14 8h4"/><path d="M6 10.8h4M14 10.8h4"/></g><circle class="a-p a-p1" cx="8" cy="3.5" r=".95"/><circle class="a-p a-p2" cx="16" cy="3.5" r=".95"/><circle class="a-p a-p3" cx="12" cy="2.5" r=".95"/>',
		torrent:'<path d="M6 3.5h8l4.5 4.5v12.5H6z"/><path class="a-fold" d="M14 3.5V8h4.5"/><g class="a-bits"><path d="M9 12.5h2M13 12.5h2M9 15.5h2M13 15.5h2M9 18h2"/></g>',
		play:'<path class="a-nudge" d="M8 5.8v12.4a.8.8 0 0 0 1.2.7l9.6-6.2a.8.8 0 0 0 0-1.4L9.2 5.1a.8.8 0 0 0-1.2.7z"/>',
		hold:'<g class="a-squash"><path d="M9 6.5v11"/><path d="M15 6.5v11"/></g>',
		trash:'<g class="a-lid"><path d="M4 7h16"/><path d="M9.5 7V4.5h5V7"/></g><path d="M6.5 7l.9 12.2a1 1 0 0 0 1 .8h7.2a1 1 0 0 0 1-.8L17.5 7"/><path d="M10 11v5.5M14 11v5.5"/>',
		up:'<g class="a-up"><path d="M12 19V5.5"/><path d="M6.5 11L12 5.5 17.5 11"/></g>',
		dn:'<g class="a-dn"><path d="M12 5v13.5"/><path d="M6.5 13L12 18.5 17.5 13"/></g>',
		folder:'<path d="M3 10.2V7a2 2 0 0 1 2-2h3.8l2 2H19a2 2 0 0 1 2 2v1.2"/><path class="a-flap" d="M3 10.2h18l-1.4 8.1a2 2 0 0 1-2 1.7H6.4a2 2 0 0 1-2-1.7z"/>',
		cal:'<rect x="3.5" y="5" width="17" height="15.5" rx="2"/><path d="M3.5 9.5h17"/><path d="M8 3v4M16 3v4"/><g class="a-page"><path d="M7.5 13h2M11 13h2M14.5 13h2M7.5 16.5h2M11 16.5h2"/></g>',
		gauge:'<path d="M3.8 17a8.5 8.5 0 1 1 16.4 0"/><path class="a-needle" d="M12 16.5l3.5-5"/><circle class="dot" cx="12" cy="16.5" r="1.3"/>',
		key:'<g class="a-key"><circle cx="8" cy="15.5" r="4"/><path d="M10.9 12.6L19 4.5"/><path d="M16 7.5l2 2"/><path d="M13.8 9.7l1.5 1.5"/></g>',
		gear:'<g class="a-gear"><circle cx="12" cy="12" r="6.5"/><circle cx="12" cy="12" r="2.4"/><path d="M12 2.8v2.7M12 18.5v2.7M2.8 12h2.7M18.5 12h2.7M5.5 5.5l1.9 1.9M16.6 16.6l1.9 1.9M5.5 18.5l1.9-1.9M16.6 7.4l1.9-1.9"/></g>',
		files:'<path d="M11 6.5h9M11 12h9M11 17.5h9"/><path class="a-c1" d="M3.8 6.6l1.5 1.5 2.7-3"/><path class="a-c2" d="M3.8 12.1l1.5 1.5 2.7-3"/><path class="a-c3" d="M3.8 17.6l1.5 1.5 2.7-3"/>',
		more:'<circle class="a-d1 dot" cx="6" cy="12" r="1.4"/><circle class="a-d2 dot" cx="12" cy="12" r="1.4"/><circle class="a-d3 dot" cx="18" cy="12" r="1.4"/>',
		user:'<g class="a-nod"><circle cx="12" cy="8.5" r="3.8"/></g><path d="M4.5 20a7.5 7.5 0 0 1 15 0"/>',
		close:'<g class="a-rot"><path d="M6.5 6.5l11 11M17.5 6.5l-11 11"/></g>',
		inbox:'<path d="M4 14.5V18a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-3.5"/><path d="M8 4h8"/><g class="a-dn"><path d="M12 7v8.5"/><path d="M8.5 12l3.5 3.5 3.5-3.5"/></g>',
		retry:'<g class="a-spin1"><path d="M19.5 12a7.5 7.5 0 1 1-2.2-5.3"/><path d="M19.5 4.5v4h-4"/></g>',
		all:'<path d="M12 4l8.5 4.5L12 13 3.5 8.5z"/><path class="a-s2" d="M3.5 12.5L12 17l8.5-4.5"/><path class="a-s3" d="M3.5 16.5L12 21l8.5-4.5"/>',
		bell:'<g class="a-bell"><path d="M12 4.5a5.5 5.5 0 0 0-5.5 5.5v4.2L5 16.5h14l-1.5-2.3V10A5.5 5.5 0 0 0 12 4.5z"/><path d="M12 3v1.5"/></g><path class="a-clap" d="M10.2 19a2 2 0 0 0 3.6 0"/>',
		chat:'<path d="M5 5h14a1.5 1.5 0 0 1 1.5 1.5v8A1.5 1.5 0 0 1 19 16h-9l-4.5 3.5V16H5a1.5 1.5 0 0 1-1.5-1.5v-8A1.5 1.5 0 0 1 5 5z"/><circle class="a-d1 dot" cx="8.5" cy="10.6" r="1.1"/><circle class="a-d2 dot" cx="12" cy="10.6" r="1.1"/><circle class="a-d3 dot" cx="15.5" cy="10.6" r="1.1"/>',
		plug:'<g class="a-plug"><path d="M9 3v4M15 3v4"/><path d="M7 7h10v3a5 5 0 0 1-10 0z"/></g><path d="M12 15v6"/>',
		sparkle:'<g class="a-sparkle"><path d="M10.5 4c.7 4.3 2.4 6 6.5 6.5-4.1.5-5.8 2.2-6.5 6.5-.7-4.3-2.4-6-6.5-6.5 4.1-.5 5.8-2.2 6.5-6.5z"/></g><path class="a-tw" d="M18 14.5v5M15.5 17h5"/>',
		ticket:'<g class="a-ticket"><path d="M4 7.5A1.5 1.5 0 0 1 5.5 6h13A1.5 1.5 0 0 1 20 7.5V10a2 2 0 0 0 0 4v2.5a1.5 1.5 0 0 1-1.5 1.5h-13A1.5 1.5 0 0 1 4 16.5V14a2 2 0 0 0 0-4z"/><path d="M14.5 6.5v11" stroke-dasharray="1.5 2"/><path d="M7.5 10.2h3.5M7.5 13.8h4.5"/></g>',
		copy:'<path d="M8.5 8V5.5A1.5 1.5 0 0 1 10 4h8.5A1.5 1.5 0 0 1 20 5.5V14a1.5 1.5 0 0 1-1.5 1.5H16"/><rect class="a-front" x="4" y="8.5" width="11.5" height="11.5" rx="1.5"/>',
		sun:'<circle cx="12" cy="12" r="4"/><g class="a-rays"><path d="M12 2.5v2M12 19.5v2M2.5 12h2M19.5 12h2M5.3 5.3l1.4 1.4M17.3 17.3l1.4 1.4M5.3 18.7l1.4-1.4M17.3 6.7l1.4-1.4"/></g>',
		moon:'<g class="a-moon"><path d="M19.5 14.5A8 8 0 0 1 9.5 4.5a8 8 0 1 0 10 10z"/></g><path class="a-star" d="M17.5 3.5v3M16 5h3"/>',
		auto:'<circle cx="12" cy="12" r="8.5"/><path class="a-half dot" d="M12 3.5a8.5 8.5 0 0 1 0 17z"/>',
		popout:'<path d="M18.5 13.5v4a2 2 0 0 1-2 2h-10a2 2 0 0 1-2-2v-10a2 2 0 0 1 2-2h4"/><g class="a-pop"><path d="M14 4.5h5.5V10"/><path d="M19.5 4.5L11 13"/></g>',
		lock:'<rect x="5" y="10.5" width="14" height="10" rx="1.5"/><path class="a-shackle" d="M8 10.5V8a4 4 0 0 1 8 0v2.5"/><path d="M12 14.5v2.5"/>',
		chev:'<path d="M9.5 5.5L16 12l-6.5 6.5"/>',
		back:'<path d="M15 5L8 12l7 7"/>',
		eye:'<path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z"/><circle cx="12" cy="12" r="3"/>',
		eyeoff:'<path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z"/><circle cx="12" cy="12" r="3"/><path d="M4 4l16 16"/>',
		edit:'<g class="a-pen"><path d="M15.5 5.5l3 3L9 18l-4 1 1-4z"/><path d="M13.5 7.5l3 3"/></g>',
		paste:'<rect x="5" y="4.5" width="14" height="16" rx="2"/><path d="M9 4.5V3.8a.8.8 0 0 1 .8-.8h4.4a.8.8 0 0 1 .8.8v.7"/><g class="a-dn"><path d="M12 9v6.5"/><path d="M9.5 13l2.5 2.5 2.5-2.5"/></g>'
	};
	DC.ICONS = ICONS;

	/* ---------- tiny DOM helpers: data only ever goes into textContent, value or attributes ---------- */
	function h(tag, attrs, kids){
		var el = document.createElement(tag), k, v;
		if(attrs){
			for(k in attrs){
				if(!attrs.hasOwnProperty(k)) continue;
				v = attrs[k];
				if(v === null || v === undefined || v === false) continue;
				if(k === 'class') el.className = v;
				else if(k === 'text') el.textContent = v;
				else if(k === 'value') el.value = v;
				else if(k === 'checked') el.checked = !!v;
				else if(k === 'style') el.style.cssText = v;
				else if(k.indexOf('on') === 0 && typeof v === 'function') el.addEventListener(k.slice(2), v);
				else if(v === true) el.setAttribute(k, '');
				else el.setAttribute(k, v);
			}
		}
		add(el, kids);
		return el;
	}
	function add(el, kids){
		var i, c;
		if(kids === null || kids === undefined) return el;
		if(!(kids instanceof Array)) kids = [kids];
		for(i = 0; i < kids.length; i++){
			c = kids[i];
			if(c === null || c === undefined || c === false) continue;
			if(c instanceof Array){ add(el, c); continue; }
			el.appendChild(typeof c === 'string' || typeof c === 'number' ? document.createTextNode(String(c)) : c);
		}
		return el;
	}
	function clear(el){ while(el.firstChild) el.removeChild(el.firstChild); return el; }
	function remove(el){ if(el && el.parentNode) el.parentNode.removeChild(el); }
	function icon(name, cls){
		var s = h('span', {'class':'ic' + (cls ? ' ' + cls : ''), 'aria-hidden':'true'});
		s.innerHTML = '<svg viewBox="0 0 24 24">' + (ICONS[name] || ICONS.more) + '</svg>';
		return s;
	}
	function ibtn(name, label, fn){
		return h('button', {'class':'ib sq', type:'button', 'aria-label':label, title:label, onclick:function(e){ e.stopPropagation(); fn(e); }}, icon(name));
	}
	function btn(name, label, fn, cls){
		return h('button', {'class':'ib btn' + (cls ? ' ' + cls : ''), type:'button', onclick:fn}, [name ? icon(name) : null, h('span', {text:label})]);
	}
	function busy(b, on, label){
		if(!b) return;
		if(on){ b._label = b.lastChild ? b.lastChild.textContent : ''; b.disabled = true; if(label && b.lastChild) b.lastChild.textContent = label; }
		else{ b.disabled = false; if(b._label !== undefined && b.lastChild) b.lastChild.textContent = b._label; }
	}
	DC.h = h; DC.add = add; DC.clear = clear; DC.remove = remove; DC.icon = icon; DC.ibtn = ibtn; DC.btn = btn; DC.busy = busy;

	/* ---------- formatting ---------- */
	var KB = 1024, MB = KB * 1024, GB = MB * 1024, TB = GB * 1024;
	function fsize(b){
		b = +b || 0;
		if(b >= TB) return (b / TB).toFixed(2) + ' TB';
		if(b >= GB) return (b / GB).toFixed(b >= 10 * GB ? 1 : 2) + ' GB';
		if(b >= MB) return (b / MB).toFixed(b >= 100 * MB ? 0 : 1) + ' MB';
		if(b >= KB) return Math.round(b / KB) + ' KB';
		return b + ' B';
	}
	function fspeed(b){ return b > 0 ? fsize(b) + '/s' : '0 KB/s'; }
	/* [number, unit] for the toolbar, where the unit is set smaller */
	function fspeedParts(b){ var t = fspeed(b), i = t.lastIndexOf(' '); return [t.slice(0, i), t.slice(i + 1)]; }
	function feta(s){
		if(!isFinite(s) || s <= 0) return '';
		s = Math.round(s);
		if(s < 60) return DC.t('剩 {s} 秒', {s:s});
		if(s < 3600) return DC.t('剩 {m} 分', {m:Math.floor(s / 60)});
		if(s < 86400 * 2) return DC.t('剩 {h} 小時 {m} 分', {h:Math.floor(s / 3600), m:Math.floor((s % 3600) / 60)});
		/* At a few bytes a second the estimate runs into years; past a month it says nothing useful */
		if(s > 86400 * 30) return DC.t('剩超過 30 天');
		return DC.t('剩 {d} 天', {d:Math.floor(s / 86400)});
	}
	function fdur(s){
		s = +s || 0;
		if(s < 60) return DC.t('不到 1 分');
		if(s < 3600) return DC.t('{m} 分', {m:Math.floor(s / 60)});
		return DC.t('{h} 小時 {m} 分', {h:Math.floor(s / 3600), m:Math.floor((s % 3600) / 60)});
	}
	function pad(n){ return (n < 10 ? '0' : '') + n; }
	function ftime(unix){
		if(!unix) return '';
		var d = new Date(unix * 1000), now = new Date(), y = new Date(now.getTime() - 86400000), hm = pad(d.getHours()) + ':' + pad(d.getMinutes());
		if(d.toDateString() === now.toDateString()) return DC.t('今天 {time}', {time:hm});
		if(d.toDateString() === y.toDateString()) return DC.t('昨天 {time}', {time:hm});
		return d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate()) + ' ' + hm;
	}
	function fdate(unix){
		if(!unix) return '';
		var d = new Date(unix * 1000);
		return d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate());
	}
	function fclock(unix){ var d = new Date(unix * 1000); return pad(d.getHours()) + ':' + pad(d.getMinutes()); }
	function extOf(n){ var m = /\.(tar\.(?:gz|xz|bz2|zst)|[a-z0-9]{1,5})$/i.exec(n || ''); return m ? m[1].toLowerCase() : ''; }
	function baseName(p){ var a = String(p || '').split('/'); return a[a.length - 1] || p; }
	/* Joins translated sentences: Chinese and Japanese run on after 。！？, other languages need a space between them */
	DC.sentences = function(){
		var out = '', i, s;
		for(i = 0; i < arguments.length; i++){ s = arguments[i]; if(!s) continue; if(out && !/[。！？]$/.test(out)) out += ' '; out += s; }
		return out;
	};
	DC.KB = KB; DC.MB = MB; DC.GB = GB;
	DC.fsize = fsize; DC.fspeed = fspeed; DC.fspeedParts = fspeedParts; DC.feta = feta; DC.fdur = fdur; DC.pad = pad; DC.ftime = ftime; DC.fdate = fdate; DC.fclock = fclock; DC.extOf = extOf; DC.baseName = baseName;

	/* ---------- browser storage: per-viewer conveniences only ---------- */
	DC.ls = function(k, v){
		try{
			if(arguments.length === 1) return localStorage.getItem(k);
			if(v === null) localStorage.removeItem(k); else localStorage.setItem(k, String(v));
		}catch(e){}
		return null;
	};

	/* ---------- layout queries ---------- */
	DC.layer = function(){ return document.body; };
	/* The layout breakpoint is a container query on the box the app lives in; JS asks the same box. */
	DC.phone = function(){ return document.body.clientWidth <= 560; };
	DC.embedded = (function(){ try{ return window.self !== window.top; }catch(e){ return true; } })();

	/* ---------- toast ---------- */
	var toastEl = null, toastTimer = null;
	DC.toast = function(msg, action, ms){
		remove(toastEl);
		clearTimeout(toastTimer);
		var el = toastEl = h('div', {'class':'toast', role:'status'}, [h('span', {text:msg}), action ? h('button', {type:'button', onclick:function(){ remove(el); action.fn(); }}, action.label) : null]);
		DC.layer().appendChild(el);
		toastTimer = setTimeout(function(){ remove(el); }, ms || 6000);
	};

	/* ---------- dialogs ---------- */
	DC.R = {};
	/* opts: wide, nofocus, onclose, guard() (true while the dialog holds input that a stray click or Escape must not throw
	   away: it then only nudges, and 取消 still closes), persist (closes only through its own buttons), anchor (an element: on wide screens the dialog opens as a popover
	   under it, without dimming). A dialog whose main button is destructive starts with 取消 focused, so Enter cannot delete. */
	DC.modal = function(title, iconName, kids, acts, opts){
		opts = opts || {};
		var scrim = h('div', {'class':'scrim'}), box, b, onclose = opts.onclose, nudgeT;
		if(DC.R.closeModal) DC.R.closeModal();
		function close(){
			remove(scrim); remove(box);
			if(DC.R.closeModal === close){ DC.R.closeModal = null; DC.R.dismissModal = null; }
			if(onclose){ var f = onclose; onclose = null; f(); }
		}
		function dismiss(){
			if(opts.persist || (opts.guard && opts.guard())){
				box.classList.remove('nudge'); void box.offsetWidth; box.classList.add('nudge');
				clearTimeout(nudgeT); nudgeT = setTimeout(function(){ box.classList.remove('nudge'); }, 500);
				if(!opts.persist) DC.toast(DC.t('輸入的內容還沒加入。要放棄請按「取消」。'));
				return false;
			}
			close();
			return true;
		}
		var head = h('div', {'class':'mhead'}, [h('div', {'class':'grab', 'aria-hidden':'true'}), h('h2', null, [iconName ? icon(iconName, 'play') : null, h('span', {text:title})])]);
		/* Title and actions stay put; only the middle scrolls, so the main button is reachable however long the list is. */
		box = h('div', {'class':'modal' + (opts.wide ? ' wide' : ''), role:'dialog', 'aria-modal':'true', 'aria-label':title}, [head, h('div', {'class':'mbody'}, kids), h('div', {'class':'acts'}, acts(close))]);
		DC.pullToClose(box, head, dismiss);
		if(opts.anchor && !DC.phone()){
			var ar = opts.anchor.getBoundingClientRect();
			box.classList.add('pop'); scrim.classList.add('clear');
			box.style.top = Math.round(ar.bottom + 8) + 'px';
			box.style.right = Math.max(8, Math.round(document.documentElement.clientWidth - ar.right)) + 'px';
		}else DC.dragByHead(box, head);
		scrim.onclick = dismiss;
		DC.layer().appendChild(scrim); DC.layer().appendChild(box);
		DC.R.closeModal = close; DC.R.dismissModal = dismiss;
		b = box.querySelectorAll('.acts .btn');
		if(b.length && !opts.nofocus) (b[b.length - 1].className.indexOf('dan') >= 0 ? b[0] : b[b.length - 1]).focus();
		return close;
	};
	/* Wide screens: a dialog can be moved by its title bar (to see the list behind it, e.g. while choosing files). The whole
	   dialog stays inside the window (8px margin), also when the browser or QTS window is resized; a dialog taller or wider
	   than the window keeps its top-left edge visible. A double click on the title puts it back in the middle. Phones keep
	   pull-to-close. */
	DC.dragByHead = function(box, head){
		var M = 8, sx = 0, sy = 0, ox = 0, oy = 0, dx = 0, dy = 0, base = null, id = null;
		function range(lo, hi){ return hi < lo ? [lo, lo] : [lo, hi]; }
		/* base = the dialog's rectangle without our offset, so limits do not depend on where it was dragged before */
		function measure(){ var r = box.getBoundingClientRect(); base = {left:r.left - dx, right:r.right - dx, top:r.top - dy, bottom:r.bottom - dy}; }
		function place(x, y){
			var W = window.innerWidth, H = window.innerHeight, rx, ry;
			if(!base) measure();
			rx = range(M - base.left, W - M - base.right);
			ry = range(M - base.top, H - M - base.bottom);
			dx = Math.max(rx[0], Math.min(rx[1], x));
			dy = Math.max(ry[0], Math.min(ry[1], y));
			box.style.translate = dx + 'px ' + dy + 'px';
		}
		head.addEventListener('pointerdown', function(e){
			if(DC.phone() || e.button !== 0 || (e.target.closest && e.target.closest('button,a,input,select,textarea'))) return;
			id = e.pointerId; sx = e.clientX; sy = e.clientY; ox = dx; oy = dy;
			measure();
			box.classList.add('dragging');
			try{ head.setPointerCapture(id); }catch(x){}
			e.preventDefault();
		});
		head.addEventListener('pointermove', function(e){
			if(id === null || e.pointerId !== id) return;
			place(ox + e.clientX - sx, oy + e.clientY - sy);
		});
		function end(e){ if(id === null || (e && e.pointerId !== id)) return; id = null; box.classList.remove('dragging'); }
		head.addEventListener('pointerup', end);
		head.addEventListener('pointercancel', end);
		head.addEventListener('dblclick', function(e){ if(DC.phone() || (e.target.closest && e.target.closest('button'))) return; dx = dy = 0; box.style.translate = ''; });
		/* Resizing the window (or the content growing) must not leave a moved dialog outside: re-apply the limits */
		function refit(){
			if(!box.parentNode){ window.removeEventListener('resize', refit); return; }
			if(dx || dy){ measure(); place(dx, dy); }
		}
		window.addEventListener('resize', refit);
		if(window.ResizeObserver){ var ro = new ResizeObserver(function(){ if(!box.parentNode){ ro.disconnect(); return; } refit(); }); ro.observe(box); }
	};
	/* Phone sheets: drag the handle or title down to dismiss, like an iOS sheet. */
	DC.pullToClose = function(box, head, close){
		var y0 = null, dy = 0;
		head.addEventListener('touchstart', function(e){ if(!DC.phone()) return; y0 = e.touches[0].clientY; dy = 0; box.style.transition = 'none'; });
		head.addEventListener('touchmove', function(e){
			if(y0 === null) return;
			dy = Math.max(0, e.touches[0].clientY - y0);
			box.style.transform = 'translateY(' + dy + 'px)';
			e.preventDefault();
		}, {passive:false});
		head.addEventListener('touchend', function(){
			if(y0 === null) return;
			y0 = null;
			box.style.transition = 'transform .25s cubic-bezier(.2,1,.3,1)';
			if(dy > 90 && close() !== false) return;
			box.style.transform = '';
		});
	};
	/* A simple confirm dialog; fn runs on the confirm button. */
	DC.confirm = function(title, iconName, text, okLabel, fn, danger){
		return DC.modal(title, iconName, [h('p', {'class':'lead', text:text})], function(close){
			return [btn(null, DC.t('取消'), close), btn(null, okLabel, function(){ close(); fn(); }, danger ? 'dan pri' : 'pri')];
		});
	};

	/* ---------- supporting the project: only where people go on purpose (their menu, the foot of settings), never a prompt ---------- */
	DC.SUPPORT = {boba:'https://tasict.bobaboba.me', paypal:'https://paypal.me/tasict'};
	DC.supportLinks = function(){
		return [
			h('a', {'class':'boba', href:DC.SUPPORT.boba, target:'_blank', rel:'noopener noreferrer', title:DC.t('珍奶贊助直接刷卡，不需要 PayPal 帳號。')}, [h('img', {src:'img/boba.png', alt:''}), h('span', {text:DC.t('請我喝珍奶')})]),
			h('a', {'class':'paypal', href:DC.SUPPORT.paypal, target:'_blank', rel:'noopener noreferrer'}, DC.t('用 PayPal 贊助'))
		];
	};

	/* ---------- usage statistics: how often parts of the UI are used, counted here and handed to the daemon about once a
	   minute, which keeps only names it knows and adds them to its daily anonymous report (off: it drops them) ---------- */
	var TRACK = {}, trackN = 0, trackTimer = null;
	DC.track = function(key){
		TRACK[key] = (TRACK[key] || 0) + 1; trackN++;
		if(!trackTimer) trackTimer = setTimeout(DC.trackFlush, 60000);
	};
	DC.trackFlush = function(){
		var body;
		clearTimeout(trackTimer); trackTimer = null;
		if(!trackN || !DC.S || !DC.S.me || DC.S.me.via !== 'session' || !window.fetch) return;
		body = JSON.stringify({counts:TRACK}); TRACK = {}; trackN = 0;
		try{
			fetch('api/v1/analytics/ui', {method:'POST', credentials:'same-origin', keepalive:true, body:body,
				headers:{'Content-Type':'application/json', 'X-Requested-With':'XMLHttpRequest', 'X-DC-Lang':DC.lang || 'TCH'}})['catch'](function(){});
		}catch(e){}
	};
	document.addEventListener('visibilitychange', function(){ if(document.visibilityState === 'hidden') DC.trackFlush(); });

	/* ---------- usage notice: shown once per account on first use (pref "notice"), readable again from the settings footer ---------- */
	var NOTICE_VERSION = 2;
	DC.noticeDue = function(){ var me = DC.S && DC.S.me; return !!(me && me.via !== 'token' && !(me.prefs && me.prefs.notice >= NOTICE_VERSION)); };
	/* One page, two parts: what the user is responsible for when downloading, and what the anonymous statistics carry.
	   Administrators decide about the statistics right here (the switch starts on, so agreeing is all it takes);
	   regular users see the same facts and who decides. */
	DC.showNotice = function(first){
		var items = [
			DC.t('Download Center 只依照你提供的網址、種子檔或磁力連結下載檔案，不提供、搜尋或推薦任何內容。'),
			DC.t('請只下載你有權取得的內容。未經授權下載或散布受著作權保護的軟體、影音或其他作品，可能觸犯你所在地的法律。'),
			DC.t('使用 BitTorrent 時，你在下載的同時也會把檔案分享給其他使用者，他們看得到你的 IP 位址。'),
			DC.t('你要為下載的內容和使用方式負責；在法律允許的範圍內，開發者不對使用本軟體造成的損失或法律責任負責。'),
			DC.t('Download Center 是獨立開發的軟體，與 QNAP 無關，也未經 QNAP 認可。')
		], list = h('ul', {'class':'notice'}), i, me = DC.S && DC.S.me, stats = me && me.admin && me.analytics, sw = null, head;
		for(i = 0; i < items.length; i++) list.appendChild(h('li', {text:items[i]}));
		function column(cls, iconName, title, lines){
			var ul = h('ul'), k;
			for(k = 0; k < lines.length; k++) ul.appendChild(h('li', {text:lines[k]}));
			return h('div', {'class':'ntcol ' + cls}, [h('b', null, [icon(iconName), title]), ul]);
		}
		if(stats){
			sw = DC.toggle('ntStats', DC.t('協助改善 Download Center'), DC.t('每天一次傳給 Google Analytics，隨時可以在「設定 › 關於與更新」關閉。'), stats.enabled || !stats.asked, first ? null : function(){
				var el = this;
				DC.api.put('analytics', {enabled:el.checked}).then(function(r){ DC.S.me.analytics = r; }, function(e){ el.checked = !el.checked; DC.toast(DC.errText(e)); });
			});
			head = sw;
		}else head = h('p', {'class':'ntwho', text:DC.t('Download Center 可以每天一次把匿名的使用統計傳給 Google Analytics，是否傳送由系統管理者決定。')});
		return DC.modal(DC.t('使用聲明'), 'files', [
			h('p', {'class':'lead', text:DC.t('使用 Download Center 前，請先看過這兩件事。')}),
			h('h3', {'class':'nthead', text:DC.t('下載的內容')}), list,
			h('h3', {'class':'nthead', text:DC.t('使用統計')}),
			h('div', {'class':'ntstats'}, [head, h('div', {'class':'ntsplit'}, [
				column('yes', 'done', DC.t('會傳送'), [DC.t('版本、架構與 NAS 機型'), DC.t('用到哪些功能、各用了幾次'), DC.t('任務數量、完成與失敗次數')]),
				column('no', 'close', DC.t('不會傳送'), [DC.t('檔名、網址與下載的內容'), DC.t('帳號、密碼與權杖'), DC.t('NAS 名稱與資料夾路徑')])
			])])
		], function(close){
			return first ? [btn(null, DC.t('我了解並同意'), function(){
				DC.savePref('notice', NOTICE_VERSION);
				if(sw) DC.api.put('analytics', {enabled:DC.chk('ntStats')}).then(function(r){ if(DC.S.me) DC.S.me.analytics = r; }, function(){});
				close();
			}, 'pri')] : [btn(null, DC.t('關閉'), close, 'pri')];
		}, {persist:!!first});
	};

	/* ---------- settings form pieces ---------- */
	DC.field = function(label, help, ctl, id){ return h('label', {'class':'field', 'for':id || null}, [h('span', null, [label, help ? h('small', {text:help}) : null]), h('span', {'class':'inline'}, ctl)]); };
	DC.fieldDiv = function(label, help, ctl){ return h('div', {'class':'field'}, [h('span', null, [label, help ? h('small', {text:help}) : null]), h('span', {'class':'inline'}, ctl)]); };
	DC.toggle = function(id, label, help, on, onchange){ return h('label', {'class':'toggle', 'for':id}, [h('input', {type:'checkbox', id:id, checked:on, onchange:onchange || null}), h('span', null, [label, help ? h('small', {text:help}) : null])]); };
	DC.num = function(id, val, unit, attrs){
		var a = {type:'number', id:id, value:String(val === undefined || val === null ? 0 : val), min:'0', inputmode:'decimal'}, k;
		if(attrs) for(k in attrs) a[k] = attrs[k];
		return [h('input', a), unit ? h('span', {'class':'unit', text:unit}) : null];
	};
	DC.sec = function(iconName, title, intro, kids){ return h('section', {'class':'sec'}, [h('h3', null, [icon(iconName), title]), intro ? h('p', {text:intro}) : null, h('div', {'class':'group'}, kids)]); };
	/* Settings lists all work alike: adding and editing happen in a window. An empty list puts the way to add the first entry
	   in the middle of its card; a list with entries gets the add button under it, on the right. mform groups the fields of
	   such a window. */
	DC.emptyAdd = function(iconName, text, label, fn){ return h('div', {'class':'empty-add'}, [icon(iconName), h('p', {text:text}), btn('plus', label, fn, 'pri')]); };
	DC.addRow = function(label, fn){ return h('div', {'class':'inline addrow'}, [btn('plus', label, fn)]); };
	DC.mform = function(kids){ return h('div', {'class':'group mform'}, kids); };
	DC.val = function(id){ var el = document.getElementById(id); return el ? el.value : ''; };
	DC.ival = function(id){ var v = parseInt(DC.val(id), 10); return isNaN(v) ? 0 : v; };
	DC.fval = function(id){ var v = parseFloat(DC.val(id)); return isNaN(v) ? 0 : v; };
	DC.chk = function(id){ var el = document.getElementById(id); return !!(el && el.checked); };
	DC.select = function(id, opts, value, onchange){
		var s = h('select', {id:id, onchange:onchange || null}), i;
		for(i = 0; i < opts.length; i++) if(opts[i]) s.appendChild(h('option', {value:opts[i][0], text:opts[i][1]}));
		if(value !== undefined && value !== null) s.value = value;
		return s;
	};
	DC.copyText = function(text, el){
		function fallback(){
			if(el){ var r = document.createRange(), s = window.getSelection(); r.selectNodeContents(el); s.removeAllRanges(); s.addRange(r); }
			DC.toast(DC.t('已選取，請手動複製'));
		}
		try{
			if(navigator.clipboard && window.isSecureContext) navigator.clipboard.writeText(text).then(function(){ DC.toast(DC.t('已複製')); }, fallback);
			else fallback();
		}catch(e){ fallback(); }
	};
})();
