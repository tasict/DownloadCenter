/* Torrent search: one field, the sources answering one by one, results that open the usual add window. The page keeps its
   state (keyword, filters, results) while the user is elsewhere in the app; the DOM is built again each time it is shown. */
(function(){
	'use strict';
	var DC = window.DC, h = DC.h, add = DC.add, clear = DC.clear, icon = DC.icon, ibtn = DC.ibtn, btn = DC.btn;

	var CATS = [['all', DC.t('All')], ['video', DC.t('Video')], ['audio', DC.t('Music')], ['software', DC.t('Software')], ['books', DC.t('Books')], ['other', DC.t('Other')]];
	var SORTS = [['relevance', DC.t('Relevance')], ['seeders', DC.t('Seeders')], ['size', DC.t('Size')], ['date', DC.t('Date published')], ['name', DC.t('Name')]];
	var SORT_DIR = {seeders:-1, size:-1, date:-1, name:1};
	/* Whether a result can be downloaded, in the signal of the task's source status */
	var HEALTH = {
		good:{icon:'waves', label:DC.t('Well seeded')}, web:{icon:'webseed', label:DC.t('Direct download source')},
		few:{icon:'waves', label:DC.t('Few seeders')}, none:{icon:'waves-off', label:DC.t('No seeders')}, unk:{icon:'seed', label:DC.t('Seeders unknown')}
	};
	var PAGE = 50, POLL = 600, KEEP = 29 * 60 * 1000, RECENT = 8;

	/* State of the page (survives leaving it), U the nodes of the page shown now */
	var S = fresh(null), U = {};
	function fresh(user){ return {user:user, q:'', cat:'all', sort:null, dir:null, only:null, open:{}, busy:{}, added:{}, shown:PAGE, job:null, err:null, timer:null, gen:0, sources:null, why:null}; }

	function me(){ return DC.S && DC.S.me; }
	/* Some source can be searched now */
	function ready(){ var m = me(); return !!(m && m.search && m.search.available && DC.can('search:use')); }
	/* The page is offered: to everyone while a source can be searched, to administrators always (it leads them to the settings) */
	function entry(){ var m = me(); return !!(m && m.search && DC.can('search:use') && (m.search.available || m.admin)); }
	function trim(s){ return String(s || '').replace(/^\s+|\s+$/g, ''); }
	function chars(s){ return s.replace(/[\uD800-\uDBFF][\uDC00-\uDFFF]/g, '_').length; }
	function shown(){ return !!(U.main && document.body.contains(U.main) && DC.S.view === 'search'); }
	function catLabel(c){ for(var i = 0; i < CATS.length; i++) if(CATS[i][0] === c) return CATS[i][1]; return c; }
	function srcName(id){ var l = S.job ? S.job.sources : [], i; for(i = 0; i < l.length; i++) if(l[i].id === id) return l[i].name; return id; }

	/* ---------- recent searches: only in this browser, per account ---------- */
	function recentKey(){ return 'dc-search-recent:' + (S.user || ''); }
	function recents(){ var a = null; try{ a = JSON.parse(DC.ls(recentKey()) || '[]'); }catch(e){} return a instanceof Array ? a.slice(0, RECENT) : []; }
	function remember(q){
		var a = recents(), out = [q], i;
		for(i = 0; i < a.length && out.length < RECENT; i++) if(typeof a[i] === 'string' && a[i].toLowerCase() !== q.toLowerCase()) out.push(a[i]);
		DC.ls(recentKey(), JSON.stringify(out));
	}

	/* ---------- the field: words search, a link opens the add window ---------- */
	function linkIn(v){ var d = DC.addFlow.detect(v); return d.kind !== 'empty' && d.kind !== 'bad'; }
	function form(){
		if(U.form) return;
		U.lead = h('span', {'class':'sr-lead', 'aria-hidden':'true'});
		U.input = h('input', {type:'text', 'class':'sr-input', id:'srQ', placeholder:DC.t('Search for torrents'), 'aria-label':DC.t('Search for torrents'), 'aria-describedby':'srHint',
			autocomplete:'off', autocapitalize:'off', spellcheck:'false', enterkeyhint:'search', inputmode:'search', oninput:mode,
			onkeydown:function(e){ if(e.key === 'Escape' && U.input.value){ e.stopPropagation(); U.input.value = ''; mode(); } }});
		U.clearB = ibtn('close', DC.t('Clear the field'), function(){ U.input.value = ''; mode(); U.input.focus(); });
		U.clearB.className += ' sr-clear';
		U.goB = h('button', {'class':'ib btn pri sr-go', type:'submit'}, h('span', {text:DC.t('Search')}));
		U.form = h('form', {'class':'sr-box', role:'search', autocomplete:'off', novalidate:true, onsubmit:function(e){ e.preventDefault(); submit(); }}, [U.lead, U.input, U.clearB, U.goB]);
		U.hint = h('div', {'class':'sr-hint', id:'srHint'});
		U.link = null;
		mode();
	}
	function mode(){
		var v = U.input.value, t = trim(v), link = linkIn(v), n = chars(t), canAdd = DC.can('tasks:add');
		U.clearB.hidden = !v;
		if(link !== U.link){
			U.link = link;
			U.lead.className = 'sr-lead' + (link ? ' k-link' : '');
			clear(U.lead).appendChild(icon(link ? 'link' : 'search', link ? 'play' : ''));
		}
		U.goB.lastChild.textContent = link ? DC.t('Add') : DC.t('Search');
		U.goB.disabled = link ? !canAdd : n < 2 || n > 200 || !ready();
		U.hint.className = 'sr-hint';
		if(link) U.hint.textContent = canAdd ? DC.t('This is a link: Add opens the add window with it.') : DC.t('This account cannot add downloads.');
		else if(n > 200){ U.hint.className += ' bad'; U.hint.textContent = DC.t('Search for 2 to 200 characters.'); }
		else if(n >= 2 && ready()) U.hint.textContent = DC.t('Press Enter to search torrents for “{text}”.', {text:t});
		else U.hint.textContent = DC.t('Type a few words to search. A pasted link is added as a download.');
	}
	function submit(){
		var v = U.input.value, t = trim(v), n = chars(t);
		if(linkIn(v)){ if(DC.can('tasks:add')) DC.addFlow.compose(v); return; }
		if(n < 2 || n > 200 || !ready()){ U.input.focus(); return; }
		run(t);
	}

	/* ---------- searching: start, poll every 600 ms until every source has answered, stop ---------- */
	function run(q){
		var my = ++S.gen, cat = S.cat;
		clearTimeout(S.timer); S.timer = null;
		S.q = q; S.err = null;
		remember(q);
		DC.track('search_run');
		if(U.goB) DC.busy(U.goB, true);
		DC.api.post('search', {q:q, category:cat}).then(function(r){
			if(my !== S.gen) return;
			if(U.goB){ DC.busy(U.goB, false); mode(); }
			S.job = {id:r.id, q:q, category:cat, done:false, sources:[], results:[], seen:{}};
			S.only = null; S.open = {}; S.busy = {}; S.added = {}; S.shown = PAGE; S.why = null;
			if(shown()){ paint(); if(DC.phone()) U.input.blur(); }
			poll(my, 0);
		}, function(e){
			if(my !== S.gen) return;
			if(U.goB){ DC.busy(U.goB, false); mode(); }
			if(e.code === 'search_no_sources' && cat === 'all') setAvailable(false);
			S.err = e.code === 'rate_limited' ? DC.t('Too many searches in a minute. Wait a moment and try again.')
				: e.code === 'search_no_sources' && cat !== 'all' ? DC.t('No search source has results in “{category}”.', {category:catLabel(cat)}) : DC.errText(e);
			if(shown()) paint();
			/* The search shown before goes on */
			if(S.job && !S.job.done) poll(my, 0);
		});
	}
	/* S.inflight is the generation of the request under way, so a page shown again does not start a second chain */
	function poll(my, fails){
		clearTimeout(S.timer); S.timer = null;
		var job = S.job;
		S.inflight = my;
		DC.api.get('search/' + encodeURIComponent(job.id)).then(function(v){
			if(S.inflight === my) S.inflight = null;
			if(my !== S.gen || job !== S.job) return;
			job.sources = v.sources || []; job.results = v.results || []; job.done = !!v.done;
			if(job.done) job.doneAt = Date.now();
			else S.timer = setTimeout(function(){ poll(my, 0); }, POLL);
			if(shown()) update();
		}, function(e){
			if(S.inflight === my) S.inflight = null;
			if(my !== S.gen || job !== S.job) return;
			/* The NAS out of reach for a moment: try again; gone (a newer search of this account, or expired): say so */
			if((e.code === 'network' || e.code === 'timeout' || e.code === 'unavailable') && fails < 5){ S.timer = setTimeout(function(){ poll(my, fails + 1); }, 2000); return; }
			job.done = true; job.lost = e.code === 'not_found' ? DC.t('This search ended: a newer search of this account replaced it, or it expired. Search again.') : DC.errText(e);
			if(shown()) update();
		});
	}
	function stopSearch(){
		var job = S.job;
		if(!job || job.done) return;
		DC.api.del('search/' + encodeURIComponent(job.id)).then(function(){ if(S.inflight !== S.gen){ clearTimeout(S.timer); poll(S.gen, 0); } }, function(e){ DC.toast(DC.errText(e)); });
	}
	/* A search answered more than half an hour ago is gone on the NAS: its results cannot be added any more */
	function expire(){ if(S.job && S.job.done && S.job.doneAt && Date.now() - S.job.doneAt > KEEP){ S.job = null; S.why = null; } }

	/* ---------- results: filtered by source and sorted here (the data is in hand), relevance takes the sources in turn. The
	   category is the sources' to apply: one without categories labels everything "other", so filtering again here would hide
	   its results. ---------- */
	function keyOf(r){ return r.infohash ? 'h' + r.infohash : r.ref ? 'r' + r.ref : 't' + (r.sources || [])[0] + '|' + r.title; }
	function anySeeders(){ var rs = S.job ? S.job.results : [], i; for(i = 0; i < rs.length; i++) if(rs[i].seeders !== undefined && rs[i].seeders !== null) return true; return false; }
	function sortKey(){ return S.sort || (anySeeders() ? 'seeders' : 'relevance'); }
	function sortDir(){ return S.dir || SORT_DIR[sortKey()] || 1; }
	function visible(){
		var j = S.job, rs = j ? j.results : [], at = {}, n = {}, out = [], i, r, s0, key = sortKey(), dir = sortDir();
		if(!j) return out;
		for(i = 0; i < j.sources.length; i++) at[j.sources[i].id] = i;
		for(i = 0; i < rs.length; i++){
			r = rs[i]; s0 = (r.sources || [])[0];
			n[s0] = n[s0] || 0;
			r._rank = n[s0]++; r._src = at[s0] || 0;
			if(S.only && (r.sources || []).indexOf(S.only) < 0) continue;
			out.push(r);
		}
		function rel(a, b){ return a._rank - b._rank || a._src - b._src; }
		function val(r){
			if(key === 'seeders') return r.seeders;
			if(key === 'size') return r.size;
			if(key === 'date') return r.date || undefined;
			return undefined;
		}
		out.sort(function(a, b){
			var x, y;
			if(key === 'relevance') return rel(a, b);
			if(key === 'name') return (String(a.title).localeCompare(String(b.title), undefined, {numeric:true, sensitivity:'base'}) * dir) || rel(a, b);
			x = val(a); y = val(b);
			/* Unknown values stay last, whichever the direction */
			if(x === undefined || x === null || y === undefined || y === null){ if((x === undefined || x === null) && (y === undefined || y === null)) return rel(a, b); return x === undefined || x === null ? 1 : -1; }
			return x === y ? rel(a, b) : (x < y ? -dir : dir);
		});
		return out;
	}
	function health(r){
		if(r.seeders >= 10) return 'good';
		if(r.webseed) return 'web';
		if(r.seeders > 0) return 'few';
		if(r.seeders === 0) return 'none';
		return 'unk';
	}
	/* The task this result is in the list as: its infohash, or what adding it gave a moment ago (before the list has it) */
	function taskOf(key, r){
		var a = S.added[key];
		if(a && (DC.task(a.id) || Date.now() - a.at < 8000)) return a.id;
		return r.infohash && DC.task(r.infohash) ? r.infohash : '';
	}

	/* ---------- the page ---------- */
	function view(main, slot){
		var phone = DC.phone();
		form(); expire(); mode();
		U.main = main; U.rows = {}; U.chipEls = {}; U.painted = false; U.hsub = null;
		U.seg = h('div', {'class':'seg sr-seg', role:'group', 'aria-label':DC.t('Category')});
		CATS.forEach(function(c){
			U.seg.appendChild(h('button', {type:'button', 'aria-pressed':S.cat === c[0] ? 'true' : 'false', onclick:function(){ setCat(c[0]); }}, c[1]));
		});
		U.idle = h('div', {'class':'sr-idle'});
		U.chips = h('div', {'class':'sr-chips', role:'group', 'aria-label':DC.t('Search sources'), 'aria-live':'polite'});
		U.why = h('p', {'class':'note sr-why', role:'status', hidden:true});
		U.head = h('div', {'class':'sr-head'});
		U.prog = h('div', {'class':'sr-prog', 'aria-hidden':'true'});
		U.list = h('div', {'class':'list sr-list', role:'list', 'aria-label':DC.t('Search results')});
		U.more = h('div', {'class':'sr-morebar'});
		U.err = h('p', {'class':'note warn sr-err', role:'alert', hidden:true});
		U.res = h('div', {'class':'sr-res'}, [U.chips, U.why, U.head, U.prog, U.list, U.more]);
		if(phone) slot.appendChild(U.form);
		add(main, [phone ? null : h('div', {'class':'sr-top'}, [U.form, U.hint]), h('div', {'class':'sr-bar'}, U.seg), U.err, U.idle, U.res,
			h('p', {'class':'sr-foot', text:DC.t('Search sources are third-party sites. Download only what you have the right to download.')})]);
		paint();
		/* Picks up a search that was still running when the page was left or the session restarted */
		if(S.job && !S.job.done && !S.timer && S.inflight !== S.gen) poll(S.gen, 0);
		focus();
	}
	/* The field gets the focus; on a phone that has results to show, the keyboard would cover them */
	function focus(){
		if(!U.input || !document.body.contains(U.input)) return;
		if(DC.phone() && S.job && S.job.results.length) return;
		try{ U.input.focus({preventScroll:true}); }catch(e){ U.input.focus(); }
	}
	/* Which part of the page shows: no source yet, nothing searched yet, or the search */
	function paint(){
		if(!U.main) return;
		var admin = DC.isAdmin(), none = !ready();
		U.err.hidden = !S.err; U.err.textContent = S.err || '';
		U.seg.hidden = none;
		clear(U.idle);
		U.idle.hidden = !!S.job && !none;
		U.res.hidden = !S.job || none;
		if(none){
			U.idle.appendChild(h('div', {'class':'empty sr-empty'}, [icon('search'), h('b', {text:DC.t('No search source is turned on yet')}),
				h('p', {text:admin ? DC.t('Turn on a built-in source or add your own in Settings › Search.') : DC.t('Ask your administrator to turn on a search source.')}),
				admin ? btn('gear', DC.t('Open Settings › Search'), function(){ DC.leave(function(){ DC.S.setTab = 'search'; DC.S.setOpen = true; DC.go('settings'); }); }, 'pri') : null]));
			return;
		}
		if(!S.job){ idle(); return; }
		U.painted = false;
		clear(U.list); clear(U.chips); U.rows = {}; U.chipEls = {};
		update();
		U.painted = true;
	}
	function idle(){
		var list = recents(), box = h('div', {'class':'sr-recent'}), i;
		U.idle.appendChild(h('p', {'class':'sr-lede', text:DC.t('Search the torrent sources set up on this NAS. Results open the usual add window, where you choose files and folders.')}));
		if(S.sources === null){
			S.sources = [];
			DC.api.get('search/sources', null, {quiet:true}).then(function(r){ S.sources = r.sources || []; if(shown() && !S.job){ clear(U.idle); idle(); } }, function(){});
		}
		if(S.sources.length) U.idle.appendChild(h('p', {'class':'sr-lede sr-srcs', text:DC.t('Searches {list}.', {list:S.sources.map(function(s){ return s.name; }).join(DC.t(', '))})}));
		if(!list.length) return;
		for(i = 0; i < list.length; i++){
			(function(q){ box.appendChild(h('button', {'class':'sr-rq', type:'button', onclick:function(){ U.input.value = q; mode(); run(q); }}, [icon('search'), h('span', {text:q})])); })(list[i]);
		}
		U.idle.appendChild(h('div', {'class':'sr-rhead'}, [h('h2', {text:DC.t('Recent searches')}),
			h('button', {'class':'ib linkish', type:'button', onclick:function(){ DC.ls(recentKey(), null); clear(U.idle); idle(); }}, DC.t('Clear recent searches'))]));
		U.idle.appendChild(box);
		U.idle.appendChild(h('p', {'class':'note', text:DC.t('Recent searches are kept only in this browser.')}));
	}
	function setCat(c){
		if(c === S.cat) return;
		S.cat = c;
		DC.track('search_filter');
		[].forEach.call(U.seg.children, function(b, i){ b.setAttribute('aria-pressed', CATS[i][0] === c ? 'true' : 'false'); });
		/* The sources are asked again for the category chosen */
		if(S.job && S.job.category !== c) run(S.job.q);
	}
	/* Rows are kept between polls and only change when their result does. New results go to their place in the order while
	   the row under the pointer (or with the focus) stays where it is on the screen, whatever changes above it. */
	function update(){
		if(!U.main || !S.job) return;
		var an = anchor(), top0 = an ? an.getBoundingClientRect().top : 0, d;
		chips(); head(); rows();
		if(an && an.parentNode){ d = an.getBoundingClientRect().top - top0; if(d) window.scrollBy(0, d); }
	}

	/* Source chips: a spinner while asked, then a dot and the count, or orange with the reason; a done chip filters */
	function chips(){
		var j = S.job, i, s, c, k, why = null;
		for(i = 0; i < j.sources.length; i++){
			s = j.sources[i]; c = U.chipEls[s.id];
			if(!c){
				c = U.chipEls[s.id] = {mark:h('span', {'class':'sr-mark'}), n:h('span', {'class':'sr-n num'})};
				c.el = h('button', {'class':'sr-chip', type:'button', onclick:(function(id){ return function(){ chipClick(id); }; })(s.id)}, [c.mark, h('span', {'class':'sr-cn', text:s.name}), c.n]);
				U.chips.appendChild(c.el);
			}
			k = [s.state, s.count, s.error, S.only === s.id, S.why === s.id].join('|');
			if(c.key === k) continue;
			c.key = k;
			c.el.className = 'sr-chip ' + (s.state === 'running' ? 'run' : s.state === 'done' ? 'ok' : s.state === 'failed' ? 'bad' : 'off');
			clear(c.mark).className = 'sr-mark';
			if(s.state === 'running') c.mark.className += ' spinner';
			c.n.textContent = s.state === 'done' ? String(s.count) : s.state === 'failed' ? (s.error || DC.t('Failed')) : s.state === 'cancelled' ? DC.t('Stopped') : '';
			c.n.className = 'sr-n' + (s.state === 'done' ? ' num' : ' sr-reason');
			c.el.removeAttribute('aria-pressed'); c.el.removeAttribute('aria-expanded'); c.el.removeAttribute('aria-disabled');
			if(s.state === 'done'){ c.el.setAttribute('aria-pressed', S.only === s.id ? 'true' : 'false'); if(!s.count) c.el.setAttribute('aria-disabled', 'true'); }
			else if(s.state === 'failed') c.el.setAttribute('aria-expanded', S.why === s.id ? 'true' : 'false');
			else c.el.setAttribute('aria-disabled', 'true');
			c.el.title = s.state === 'failed' ? DC.t('{source}: {reason}', {source:s.name, reason:s.error || DC.t('Failed')}) : '';
		}
		for(i = 0; i < j.sources.length; i++) if(j.sources[i].id === S.why && j.sources[i].state === 'failed') why = j.sources[i];
		U.why.hidden = !why;
		U.why.textContent = why ? DC.t('{source}: {reason}', {source:why.name, reason:why.error || DC.t('Failed')}) : '';
	}
	function chipClick(id){
		var s = null, i;
		for(i = 0; i < S.job.sources.length; i++) if(S.job.sources[i].id === id) s = S.job.sources[i];
		if(!s) return;
		if(s.state === 'failed'){ S.why = S.why === id ? null : id; chips(); return; }
		if(s.state !== 'done' || (!s.count && S.only !== id)) return;
		S.only = S.only === id ? null : id;
		DC.track('search_filter');
		update();
	}
	/* The heading is built once per page and only its texts change, so an open sort menu survives the polls */
	function headBuild(){
		U.hsub = h('span', {'class':'sub num'});
		U.hall = h('button', {'class':'ib linkish', type:'button', onclick:function(){ S.only = null; update(); }}, DC.t('Show all sources'));
		U.hstop = h('button', {'class':'ib pickbtn sr-stop', type:'button', onclick:stopSearch}, DC.t('Stop'));
		U.hsel = h('select', {id:'srSort', onchange:function(){ S.sort = this.value; S.dir = null; DC.track('search_sort'); update(); }});
		SORTS.forEach(function(s){ U.hsel.appendChild(h('option', {value:s[0], text:s[1]})); });
		U.hdir = ibtn('up', '', function(){ S.dir = -sortDir(); DC.track('search_sort'); update(); });
		U.hdirK = '';
		add(clear(U.head), [h('h2', {text:DC.t('Results')}), U.hsub, U.hall,
			h('div', {'class':'sorter'}, [U.hstop, h('label', {'for':'srSort', text:DC.t('Sort')}), U.hsel, U.hdir])]);
	}
	function head(){
		var j = S.job, n = visible().length, dir = sortDir(), key = sortKey(), running = !j.done, tip = dirTip(key, dir), dk = dir > 0 ? 'up' : 'dn';
		if(!U.hsub) headBuild();
		U.hsub.textContent = S.only ? DC.t('{n} from {source}', {n:n, source:srcName(S.only)}) : running ? DC.t('{n} so far', {n:n}) : DC.t('{n} found', {n:n});
		U.hall.hidden = !S.only;
		U.hstop.hidden = !running;
		if(U.hsel.value !== key) U.hsel.value = key;
		U.hdir.hidden = key === 'relevance';
		if(U.hdirK !== dk){ U.hdirK = dk; clear(U.hdir).appendChild(icon(dk)); }
		U.hdir.title = tip; U.hdir.setAttribute('aria-label', tip);
		U.prog.classList.toggle('on', running);
	}
	function dirTip(key, dir){
		if(key === 'date') return dir > 0 ? DC.t('Oldest first; click for newest first') : DC.t('Newest first; click for oldest first');
		if(key === 'name') return dir > 0 ? DC.t('A to Z; click for Z to A') : DC.t('Z to A; click for A to Z');
		return dir > 0 ? DC.t('Smallest first; click for largest first') : DC.t('Largest first; click for smallest first');
	}

	function anchor(){
		if(!U.list) return null;
		var el = U.list.querySelector('.sr-row:hover'), a = document.activeElement;
		if(!el && a && U.list.contains(a)) el = a.closest('.sr-row');
		return el;
	}
	function rows(){
		var j = S.job, v = visible(), lim = Math.min(v.length, S.shown), keep = {}, i, k, ref, row, el, empty;
		for(i = 0; i < lim; i++){
			k = keyOf(v[i]);
			row = U.rows[k] || build(k, U.painted);
			fill(row, v[i]);
			keep[k] = 1;
		}
		for(k in U.rows) if(U.rows.hasOwnProperty(k) && !keep[k]){ DC.remove(U.rows[k].el); delete U.rows[k]; }
		empty = U.list.querySelector('.empty, .loading');
		if(empty && lim) DC.remove(empty);
		ref = U.list.firstChild;
		for(i = 0; i < lim; i++){
			el = U.rows[keyOf(v[i])].el;
			if(el === ref) ref = ref.nextSibling;
			else U.list.insertBefore(el, ref);
		}
		clear(U.more);
		if(v.length > lim) U.more.appendChild(btn(null, DC.t('Show {n} more', {n:Math.min(PAGE, v.length - lim)}), function(){ S.shown += PAGE; rows(); }));
		if(lim) return;
		clear(U.list);
		if(!j.done) U.list.appendChild(h('div', {'class':'loading'}, [icon('check'), j.sources.length ? DC.t('Searching {n} sources…', {n:j.sources.length}) : DC.t('Searching…')]));
		else U.list.appendChild(emptyState(j));
	}
	function emptyState(j){
		var failed = 0, i;
		if(j.lost) return h('div', {'class':'empty'}, [icon('search'), h('b', {text:j.lost})]);
		for(i = 0; i < j.sources.length; i++) if(j.sources[i].state === 'failed') failed++;
		if(j.sources.length && failed === j.sources.length) return h('div', {'class':'empty'}, [icon('search'), h('b', {text:DC.t('No source could be searched')}), DC.t('The reasons are on the sources above. Try again later.')]);
		return h('div', {'class':'empty'}, [icon('search'), h('b', {text:DC.t('Nothing found for “{q}”.', {q:j.q})}), DC.t('Try fewer words or another category.')]);
	}
	function build(k, fresh){
		var row = {key:k};
		row.sig = h('span', {'class':'sr-sig', role:'img'});
		row.title = h('span', {'class':'sr-title'});
		row.meta = h('span', {'class':'meta sr-meta'});
		row.main = h('button', {'class':'sr-main', type:'button', 'aria-expanded':'false'}, [row.title, row.meta]);
		row.act = h('span', {'class':'sr-act'});
		/* A click anywhere but the button opens the details under the row; selecting text in them does not close them (the
		   buttons stop their own clicks: the row is redrawn under them) */
		/* A new row fades in once; moving it later in the order must not play that again */
		row.el = h('div', {'class':'sr-row' + (fresh ? ' sr-in' : ''), role:'listitem', onanimationend:function(){ row.el.classList.remove('sr-in'); }, onclick:function(e){
			if(e.target.closest && e.target.closest('.sr-more,.sr-act')) return;
			S.open[k] = !S.open[k];
			fill(row, row.r, true);
		}}, [row.sig, row.main, row.act]);
		U.rows[k] = row;
		return row;
	}
	function fill(row, r, force){
		var k = row.key, hs = health(r), tid = taskOf(k, r), canAdd = DC.can('tasks:add'), busy = !!S.busy[k], open = !!S.open[k], sig, names = [], i, L = HEALTH[hs];
		row.r = r;
		for(i = 0; i < (r.sources || []).length; i++) names.push(srcName(r.sources[i]));
		sig = [hs, r.title, r.seeders, r.leechers, r.size, r.date, names.join(','), tid, busy, open, canAdd, r.category, r.page].join('|');
		if(row.sigKey === sig && !force) return;
		row.sigKey = sig;
		/* The state colour is set on the row: the signal and the seeders in the details line share it */
		row.el.className = 'sr-row s-' + hs + (open ? ' open' : '') + (row.el.classList.contains('sr-in') ? ' sr-in' : '');
		clear(row.sig).appendChild(icon(L.icon));
		row.sig.setAttribute('aria-label', L.label); row.sig.title = L.label;
		row.title.textContent = r.title;
		row.main.setAttribute('aria-expanded', open ? 'true' : 'false');
		clear(row.meta);
		function m(cls, text){ if(text) row.meta.appendChild(h('span', {'class':cls || null, text:text})); }
		if(r.seeders !== undefined && r.seeders !== null){
			m('sr-h num', DC.t('{n} seeders', {n:r.seeders}));
			if(r.leechers !== undefined && r.leechers !== null) m('num', DC.t('{n} downloading', {n:r.leechers}));
		}else if(hs === 'web') m('sr-h', L.label);
		if(r.size !== undefined && r.size !== null) m('num', DC.fsize(r.size));
		if(r.date) m('num', DC.fdate(r.date));
		m(null, names.join(DC.t(', ')));
		if(tid) row.meta.appendChild(h('span', {'class':'tag sr-have', text:DC.t('In the list')}));
		clear(row.act);
		if(tid) row.act.appendChild(h('button', {'class':'ib sr-addb have', type:'button', 'aria-label':DC.t('In the list. Open the task details'), title:DC.t('Open the task details'),
			onclick:function(e){ e.stopPropagation(); if(DC.task(tid)) DC.detail.open(tid); else DC.pollNow(); }}, icon('done')));
		else if(canAdd) row.act.appendChild(h('button', {'class':'ib sr-addb' + (busy ? ' busy' : ''), type:'button', disabled:busy, 'aria-busy':busy ? 'true' : null,
			'aria-label':DC.t('Add “{name}”', {name:r.title}), title:DC.t('Add download'), onclick:function(e){ e.stopPropagation(); addResult(row); }}, busy ? h('span', {'class':'spinner'}) : icon('plus')));
		if(row.more){ DC.remove(row.more); row.more = null; }
		if(open) row.el.appendChild(row.more = more(r, names));
	}
	/* The details under a row: everything known about it, the infohash to copy, the source's own page */
	function more(r, names){
		var dl = h('dl'), box = h('div', {'class':'sr-more'}, dl), code;
		function kv(label, dd){ add(dl, [h('dt', {text:label}), dd]); }
		kv(DC.t('Name'), h('dd', {text:r.title}));
		kv(DC.t('Found by'), h('dd', {text:names.join(DC.t(', '))}));
		if(r.category) kv(DC.t('Category'), h('dd', {text:catLabel(r.category)}));
		if(r.date) kv(DC.t('Published'), h('dd', {'class':'num', text:DC.ftime(r.date)}));
		if(r.infohash){
			code = h('span', {'class':'mono', text:r.infohash});
			kv(DC.t('Info hash'), h('dd', {'class':'sr-hash'}, [code, ibtn('copy', DC.t('Copy info hash'), function(){ DC.copyText(r.infohash, code); })]));
		}
		if(/^https?:\/\//i.test(r.page || '')) box.appendChild(h('a', {'class':'linkish', href:r.page, target:'_blank', rel:'noopener noreferrer'}, [icon('popout'), DC.t('Open source page')]));
		return box;
	}

	/* ---------- adding: a magnet link as if pasted, else the .torrent through the NAS as if chosen ---------- */
	function torrentName(r){
		var n = String(r.title || '').replace(/[\\\/:*?"<>|\u0000-\u001f]+/g, ' ').replace(/\s+/g, ' ').replace(/^\s+|\s+$/g, '').slice(0, 120);
		return (n || 'download') + '.torrent';
	}
	function addResult(row){
		var r = row.r, k = row.key, job = S.job;
		if(S.busy[k] || !job) return;
		var opts = {stay:true, onAdded:function(res){
			for(var i = 0; i < (res || []).length; i++) if(res[i].id && !res[i].error){ S.added[k] = {id:res[i].id, at:Date.now()}; break; }
			if(U.rows[k]) fill(U.rows[k], U.rows[k].r);
		}};
		DC.track('search_add');
		if(r.magnet){ DC.addFlow.openMagnet(r.magnet, r.title, opts); return; }
		if(!r.ref) return;
		S.busy[k] = true; fill(row, r);
		DC.api.file('search/' + encodeURIComponent(job.id) + '/torrent/' + encodeURIComponent(r.ref)).then(function(f){
			var o = null;
			S.busy[k] = false;
			if(U.rows[k]) fill(U.rows[k], U.rows[k].r);
			if(/json/i.test(f.type)){
				try{ o = JSON.parse(new TextDecoder('utf-8').decode(new Uint8Array(f.data))); }catch(x){}
				if(o && typeof o.magnet === 'string' && /^magnet:\?/i.test(o.magnet)) DC.addFlow.openMagnet(o.magnet, r.title, opts);
				else DC.toast(DC.t('This result cannot be added.'));
				return;
			}
			DC.addFlow.openTorrentData(f.data, torrentName(r), r.title, opts);
		}, function(e){
			S.busy[k] = false;
			if(U.rows[k]) fill(U.rows[k], U.rows[k].r);
			/* not_found: the search expired, or its source was switched off or is no longer offered to this account */
			DC.toast(e.code === 'not_found' ? DC.t('These results are no longer available: the search ended or its source was switched off. Search again.')
				: e.code === 'rate_limited' ? DC.t('Two .torrent files are already being fetched for you. Wait for them, then try again.') : DC.errText(e));
		});
	}

	/* ---------- outside the page ---------- */
	/* The task list changed: results that are in it now say so */
	function tasksChanged(){
		if(!shown() || !S.job) return;
		for(var k in U.rows) if(U.rows.hasOwnProperty(k)) fill(U.rows[k], U.rows[k].r);
	}
	/* From the add window: go to the page with these words and search */
	function open(text){
		DC.leave(function(){
			if(DC.S.view !== 'search') DC.go('search');
			form();
			U.input.value = text; mode();
			if(ready()) run(trim(text));
		});
	}
	/* Signed in (again): another account starts from nothing */
	function begin(user){
		if(S.user === user) return;
		stop();
		S = fresh(user);
		U = {};
	}
	function stop(){ clearTimeout(S.timer); S.timer = null; S.gen++; }

	/* Settings › Search changed which sources can be searched */
	function setAvailable(on){
		var m = me();
		if(!m || !m.search || !!m.search.available === !!on) return;
		m.search.available = !!on;
		S.sources = null;
		DC.renderNav();
	}

	DC.search = {view:view, focus:focus, open:open, begin:begin, stop:stop, entry:entry, ready:ready, tasksChanged:tasksChanged, setAvailable:setAvailable};
})();
