/* App shell: toolbar, filters, task list, polling and the event stream. Boots last. */
(function(){
	'use strict';
	var DC = window.DC, h = DC.h, add = DC.add, clear = DC.clear, icon = DC.icon, ibtn = DC.ibtn, btn = DC.btn, R = DC.R;
	var app = document.getElementById('app');

	/* ---------- state ---------- */
	var S = DC.S = {me:null, tasks:[], byId:{}, filter:'all', view:'tasks', sel:{}, picking:false, sort:'queue', dir:null, sortAt:0, rank:{},
		stats:null, setTab:'dl', setOpen:false, polling:null, statTimer:null, es:null, live:false};

	var ST = DC.ST = {
		down:{label:DC.t('下載中'), icon:'down'}, wait:{label:DC.t('等待中'), icon:'wait'}, pause:{label:DC.t('已暫停'), icon:'pause'},
		seed:{label:DC.t('做種中'), icon:'seed'}, done:{label:DC.t('已完成'), icon:'done'}, check:{label:DC.t('檢查中'), icon:'check'},
		move:{label:DC.t('搬移中'), icon:'move'}, error:{label:DC.t('錯誤'), icon:'error'}
	};
	var UI_STATE = {downloading:'down', metadata:'down', queued:'wait', paused:'pause', seeding:'seed', done:'done', checking:'check', moving:'move', error:'error'};
	DC.uiState = function(t){ return UI_STATE[t.state] || 'wait'; };
	var FILTERS = [
		{id:'all', label:DC.t('全部'), icon:'all', match:function(){ return true; }},
		{id:'down', label:DC.t('下載中'), icon:'down', match:function(t){ var s = DC.uiState(t); return s === 'down' || s === 'check' || s === 'move'; }},
		{id:'wait', label:DC.t('等待中'), icon:'wait', match:function(t){ return DC.uiState(t) === 'wait'; }},
		{id:'pause', label:DC.t('已暫停'), icon:'pause', match:function(t){ return DC.uiState(t) === 'pause'; }},
		{id:'seed', label:DC.t('做種中'), icon:'seed', match:function(t){ return DC.uiState(t) === 'seed'; }},
		{id:'done', label:DC.t('已完成'), icon:'done', match:function(t){ return DC.uiState(t) === 'done'; }},
		{id:'error', label:DC.t('錯誤'), icon:'error', match:function(t){ return DC.uiState(t) === 'error'; }}
	];
	var KIND_LABEL = DC.KIND_LABEL = {url:DC.t('網址'), torrent:DC.t('種子'), magnet:DC.t('磁力連結')};
	var SORTS = [['queue', DC.t('佇列順序')], ['status', DC.t('狀態')], ['progress', DC.t('下載進度')], ['eta', DC.t('剩餘時間')], ['elapsed', DC.t('已下載時間')]];
	var SORT_DIR = {status:1, progress:-1, eta:1, elapsed:-1};
	var STATUS_RANK = {error:0, down:1, check:2, move:3, seed:4, wait:5, pause:6, done:7};

	DC.isAdmin = function(){ return !!(S.me && S.me.admin); };
	DC.me = function(){ return S.me ? S.me.user : ''; };
	DC.can = function(scope){ var s = S.me && S.me.scopes, i; if(!s) return true; for(i = 0; i < s.length; i++) if(s[i] === scope) return true; return false; };

	/* ---------- preferences: server copy per user, browser copy for the next paint ---------- */
	DC.savePref = function(k, v){
		var o = {};
		o[k] = v;
		if(S.me){ S.me.prefs = S.me.prefs || {}; S.me.prefs[k] = v; }
		DC.api.put('me/prefs', o, {quiet:true}).then(null, function(){});
	};
	DC.pref = function(k){ return S.me && S.me.prefs ? S.me.prefs[k] : undefined; };

	/* ---------- appearance ---------- */
	var THEMES = [['light', DC.t('淺色'), 'sun'], ['dark', DC.t('深色'), 'moon'], ['auto', DC.t('自動'), 'auto']];
	function setThemeDOM(v){
		var root = document.documentElement;
		if(v !== 'light' && v !== 'dark') v = 'auto';
		if(v === 'auto') root.removeAttribute('data-theme'); else root.setAttribute('data-theme', v);
		return v;
	}
	function setGlassDOM(v){
		document.documentElement.style.setProperty('--glass-a', (0.3 + v * 0.0065).toFixed(3));
		/* Clear glass is also less blurred, so the level is visible over any content */
		document.documentElement.style.setProperty('--glass-blur', Math.round(4 + v * 0.36) + 'px');
	}
	function applyTheme(v, save){
		v = setThemeDOM(v);
		S.theme = v;
		DC.ls('dc-theme', v);
		if(save && S.me) DC.savePref('theme', v);
	}
	/* Glass translucency, like the iOS 27 slider: 0 = clearest, 100 = most frosted. */
	function applyGlass(v, save){
		S.glass = v;
		setGlassDOM(v);
		DC.ls('dc-glass', v);
		if(save && S.me) DC.savePref('glass', v);
	}
	DC.themeSeg = function(onPick, current){
		var seg = h('div', {'class':'seg', role:'group', 'aria-label':DC.t('外觀')}), i, cur = current || S.theme;
		for(i = 0; i < THEMES.length; i++){
			seg.appendChild(h('button', {'class':'ib', type:'button', 'aria-pressed':S.theme === THEMES[i][0] ? 'true' : 'false', title:THEMES[i][0] === 'auto' ? DC.t('跟著裝置的淺色或深色設定') : null,
				onclick:(function(v){ return function(){
					if(onPick){ setThemeDOM(v); onPick(v); } else applyTheme(v, true);
					var b = seg.children, k; for(k = 0; k < b.length; k++) b[k].setAttribute('aria-pressed', THEMES[k][0] === v ? 'true' : 'false');
				}; })(THEMES[i][0])}, [icon(THEMES[i][2]), THEMES[i][1]]));
		}
		return seg;
	};
	/* Glass level with a live preview: a sample panel over colourful content changes while dragging, and the dimming behind the
	   menu is lifted during the drag so the real sidebar and toolbar show the change too (over the plain page background alone
	   the difference is hard to see). */
	function glassSlider(onPick, current){
		var root = document.documentElement, peekTimer = null, cur = current === undefined ? S.glass : current;
		function peek(){ root.classList.add('glass-peek'); clearTimeout(peekTimer); peekTimer = setTimeout(function(){ root.classList.remove('glass-peek'); }, 900); }
		var pv = h('div', {'class':'glasspv', 'aria-hidden':'true'}, [
			h('div', {'class':'pv-bg'}, [h('b', {text:'ubuntu-24.04.3-desktop-amd64.iso'}), h('b', {text:'Sintel.mp4　62%　9.2 MB/s'}), h('b', {text:'LibreOffice_25.8.1_Linux.tar.gz'})]),
			h('div', {'class':'pv-glass'}, [icon('down'), h('span', {text:DC.t('玻璃預覽')})])]);
		var solid = false;
		try{ solid = window.matchMedia('(prefers-reduced-transparency: reduce)').matches || !(window.CSS && CSS.supports && (CSS.supports('backdrop-filter', 'blur(1px)') || CSS.supports('-webkit-backdrop-filter', 'blur(1px)'))); }catch(e){}
		return h('div', {'class':'glassbox'}, [pv, solid ? h('p', {'class':'note', text:DC.t('系統開啟了「降低透明度」或瀏覽器不支援模糊效果，玻璃固定為霧面。')}) : null, h('div', {'class':'glassrow'}, [h('span', {text:DC.t('透明')}), h('input', {type:'range', id:'glassRange', min:'0', max:'100', value:String(cur), 'aria-label':DC.t('玻璃透明度'),
			oninput:function(){ if(onPick){ setGlassDOM(+this.value); onPick(+this.value); } else applyGlass(+this.value, false); peek(); },
			onchange:function(){ if(onPick){ setGlassDOM(+this.value); onPick(+this.value); } else applyGlass(+this.value, true); peek(); }}), h('span', {text:DC.t('霧面')})])]);
	}

	/* ---------- boot ---------- */
	function stopLive(){
		S.live = false;
		clearTimeout(S.polling); clearTimeout(S.statTimer);
		if(S.es){ try{ S.es.close(); }catch(e){} S.es = null; }
		DC.login.stopKeepAlive();
	}
	DC.onSignedOut = function(){
		if(!S.live && S.me) return;
		var was = !!S.me;
		stopLive(); S.me = null;
		DC.login.show(was ? DC.t('登入已失效，請重新登入。') : null);
	};
	DC.onNotOnList = function(e){ stopLive(); S.me = null; DC.login.show(e.message); };
	DC.boot = function(){
		stopLive();
		DC.api.get('me', null, {quiet:true}).then(function(me){
			S.me = me;
			var p = me.prefs || {}, sv;
			if(p.theme && p.theme !== S.theme) applyTheme(p.theme, false);
			if(DC.embedded && p.glass !== undefined && +p.glass !== S.glass) applyGlass(+p.glass, false);
			sv = String(p.sort || DC.ls('dc-sort') || 'queue:').split(':');
			S.sort = sv[0] || 'queue'; S.dir = sv[1] ? +sv[1] : null;
			if(!SORT_DIR[S.sort] && S.sort !== 'queue') S.sort = 'queue';
			S.view = 'tasks'; S.filter = 'all'; S.sel = {};
			build(); renderNav(); renderView();
			S.live = true;
			poll(); pollStats(); stream();
			DC.login.keepAlive();
			loadPortrait();
			DC.track('session'); DC.track('lang_' + DC.lang); DC.track('theme_' + (S.theme || 'auto'));
			DC.track(DC.embedded ? 'layout_embedded' : DC.phone() ? 'layout_phone' : 'layout_desktop');
			if(DC.noticeDue()) DC.showNotice(true);
			DC.update.boot();
		}, function(e){
			if(e.code === 'not_on_list') DC.login.show(e.message);
			else if(e.code === 'not_signed_in') DC.login.show();
			else{
				clear(app);
				app.appendChild(h('div', {'class':'login'}, h('div', {'class':'lbox'}, [
					h('div', {'class':'brand'}, [h('img', {'class':'mark', src:'img/logo.png', alt:''}), 'Download Center']),
					h('h1', {text:DC.errText(e)}), btn('retry', DC.t('重試'), DC.boot, 'pri')])));
			}
		});
	};

	/* ---------- polling and the event stream ---------- */
	function poll(){
		clearTimeout(S.polling);
		if(!S.live) return;
		DC.api.get('tasks').then(function(r){
			setTasks(r.tasks || []);
			S.rates = {down:r.down_rate || 0, up:r.up_rate || 0};
			refresh();
		}, function(){}).then(function(){
			if(S.live) S.polling = setTimeout(poll, document.hidden ? 10000 : 2000);
		});
	}
	DC.pollNow = function(){ clearTimeout(S.polling); S.polling = setTimeout(poll, 150); };
	function pollStats(){
		clearTimeout(S.statTimer);
		if(!S.live) return;
		DC.api.get('stats').then(function(r){ S.stats = r; renderTop(); }, function(){}).then(function(){
			if(S.live) S.statTimer = setTimeout(pollStats, document.hidden ? 30000 : 5000);
		});
	}
	DC.pollStats = pollStats;
	var EVENT_TYPES = ['task.added', 'task.started', 'task.paused', 'task.resumed', 'task.completed', 'task.seeding_finished', 'task.moved', 'task.failed',
		'task.removed', 'task.merged', 'task.source_switched', 'queue.idle', 'disk.low', 'schedule.changed', 'engine.down', 'engine.up'];
	function stream(){
		if(!window.EventSource || S.es) return;
		try{ S.es = new EventSource(DC.api.url('events/stream')); }catch(e){ S.es = null; return; }
		var i;
		function on(e){
			var d = null;
			try{ d = JSON.parse(e.data); }catch(x){}
			DC.pollNow();
			if(!d) return;
			if(d.type === 'task.completed' && d.task) DC.toast(DC.t('下載完成：{name}', {name:d.task.name}));
			else if(d.type === 'task.failed' && d.task) DC.toast(DC.t('下載失敗：{name}', {name:d.task.name}));
			else if(d.type === 'task.removed' && d.task && d.data && d.data.auto) DC.toast(DC.t('已自動移除「{name}」，檔案保留', {name:d.task.name}));
			else if(d.type === 'disk.low') DC.toast(DC.t('剩餘空間不足，下載到 {folder} 的任務已暫停', {folder:(d.data && d.data.folder) || DC.t('這個資料夾')}));
			else if(d.type === 'schedule.changed') pollStats();
		}
		for(i = 0; i < EVENT_TYPES.length; i++) S.es.addEventListener(EVENT_TYPES[i], on);
		S.es.onerror = function(){ if(!S.live && S.es){ S.es.close(); S.es = null; } };
	}
	document.addEventListener('visibilitychange', function(){ if(!document.hidden && S.live){ poll(); pollStats(); } });

	function setTasks(list){
		var by = {}, i, t, old;
		for(i = 0; i < list.length; i++){
			t = list[i];
			old = S.byId[t.id];
			t._order = old ? old._order : pieceOrder(t.id);
			by[t.id] = t;
		}
		S.tasks = list; S.byId = by;
		for(i in S.sel) if(S.sel.hasOwnProperty(i) && !by[i]) delete S.sel[i];
	}
	/* The fragment bar of a torrent lights cells in a stable pseudo-random order per task. */
	function pieceOrder(id){
		var seed = parseInt(String(id).slice(0, 8), 16) % 2147483647 || 7, out = [], i;
		for(i = 0; i < 48; i++){ seed = (seed * 16807) % 2147483647; out.push((seed - 1) / 2147483646); }
		return out;
	}
	DC.task = function(id){ return S.byId[id] || null; };
	DC.upsertTask = function(t){
		if(!t) return;
		var old = S.byId[t.id], i;
		t._order = old ? old._order : pieceOrder(t.id);
		S.byId[t.id] = t;
		for(i = 0; i < S.tasks.length; i++) if(S.tasks[i].id === t.id){ S.tasks[i] = t; refresh(); return; }
		S.tasks.push(t); refresh();
	};

	/* ---------- schedule text in the toolbar ---------- */
	var MODE = {full:DC.t('全速'), limited:DC.t('限速'), off:DC.t('暫停')};
	DC.MODE = MODE;
	function schedText(){
		var sc = S.stats && S.stats.schedule, d, now, txt, days = [DC.t('週日'), DC.t('週一'), DC.t('週二'), DC.t('週三'), DC.t('週四'), DC.t('週五'), DC.t('週六')];
		if(!sc) return '';
		if(!sc.enabled) return DC.t('全速');
		txt = MODE[sc.mode] || sc.mode;
		if(sc.next_change){
			d = new Date(sc.next_change * 1000); now = new Date();
			var off = Math.round((new Date(d.getFullYear(), d.getMonth(), d.getDate()) - new Date(now.getFullYear(), now.getMonth(), now.getDate())) / 86400000);
			var at = DC.pad(d.getHours()) + ':00', nm = MODE[sc.next_mode] || '';
			if(off === 0) txt = DC.t('{mode}，{time} 起{next}', {mode:txt, time:at, next:nm});
			else if(off === 1) txt = DC.t('{mode}，明天 {time} 起{next}', {mode:txt, time:at, next:nm});
			else txt = DC.t('{mode}，{day} {time} 起{next}', {mode:txt, day:days[d.getDay()], time:at, next:nm});
		}
		return txt;
	}

	/* ---------- layout ---------- */
	function build(){
		var k;
		for(k in R) if(R.hasOwnProperty(k) && k !== 'closeModal') delete R[k];
		clear(app);
		R.spdDn = h('span', {'class':'spd num'});
		R.spdUp = h('span', {'class':'spd num'});
		/* Regular users cannot change the schedule: show it as plain status, not a button. */
		R.sched = DC.isAdmin() ? h('button', {'class':'ib quiet', type:'button', title:DC.t('修改排程'), onclick:function(){
			DC.leave(function(){ S.setTab = 'sched'; S.setOpen = true; go('settings'); });
		}}) : h('span', {'class':'quiet static', title:DC.t('排程')});
		R.upd = h('button', {'class':'ib quiet upd', type:'button', hidden:true, onclick:function(){ DC.update.offer(); }});
		R.user = h('button', {'class':'ib quiet who', type:'button', 'aria-label':DC.t('個人設定'), 'aria-haspopup':'dialog', onclick:userMenu}, [R.whoPic = h('span', {'class':'who-pic'}, S.portrait ? h('img', {src:S.portrait, alt:''}) : h('span', {'class':'who-av', 'aria-hidden':'true', text:(DC.me() || '?').charAt(0).toUpperCase()})), h('span', {'class':'who-n', text:DC.me()})]);
		R.top = h('header', {'class':'top'}, [
			h('div', {'class':'brand'}, [h('img', {'class':'mark', src:'img/logo.png', alt:''}), 'Download Center']),
			h('div', {'class':'top-end'}, [R.spdDn, R.spdUp, R.upd, R.sched, R.user, DC.embedded ? ibtn('popout', DC.t('在新分頁開啟'), function(){ window.open(location.href, '_blank', 'noopener'); }) : null])
		]);
		app.appendChild(R.top);
		R.nav = h('nav', {'class':'nav', 'aria-label':DC.t('任務篩選')});
		R.main = h('main');
		app.appendChild(h('div', {'class':'shell'}, [R.nav, R.main]));
		/* Phone: the two destinations and the add button float at the bottom, where the thumb is. */
		R.tabTasks = h('button', {'class':'ib tb', type:'button', onclick:function(){
			if(S.view === 'tasks'){ window.scrollTo(0, 0); return; }
			DC.leave(function(){ S.sel = {}; S.picking = false; go('tasks'); });
		}}, [icon('down', 'still'), h('span', {text:DC.t('任務')})]);
		R.tabSet = h('button', {'class':'ib tb', type:'button', onclick:function(){ DC.leave(function(){ S.setOpen = false; go('settings'); }); }}, [icon('gear'), h('span', {text:DC.t('設定')})]);
		app.appendChild(h('nav', {'class':'tabbar', 'aria-label':DC.t('主要')}, [
			h('div', {'class':'tabcap'}, [R.tabTasks, R.tabSet]),
			DC.can('tasks:add') ? h('button', {'class':'ib fab', type:'button', 'aria-label':DC.t('加入下載'), title:DC.t('加入下載'), onclick:function(){ DC.addFlow.openPaste(); }}, icon('plus')) : null
		]));
		setTopHeight();
		S.wasPhone = DC.phone();
		renderTop();
	}
	function setTopHeight(){ if(R.top) document.documentElement.style.setProperty('--toph', R.top.offsetHeight + 'px'); }
	function onResize(){
		var p;
		setTopHeight();
		if(!S.me || !R.main) return;
		p = DC.phone();
		if(p !== S.wasPhone){ S.wasPhone = p; renderNav(); renderView(); }
	}
	window.addEventListener('resize', onResize);
	/* iOS keeps fixed sheets under the on-screen keyboard; lift them by the part of the viewport it covers. */
	if(window.visualViewport){
		(function(){
			var vv = window.visualViewport;
			function kb(){ document.documentElement.style.setProperty('--kb', Math.max(0, Math.round(window.innerHeight - vv.height - vv.offsetTop)) + 'px'); }
			vv.addEventListener('resize', kb); vv.addEventListener('scroll', kb);
		})();
	}
	/* Personal menu: a popover under the avatar on wide screens, a sheet on phones. Appearance applies and is kept at once
	   (it is a preference, not a form). Signing out is a quiet row of its own, never a footer button, so Enter or a stray
	   click cannot end the QTS session, and it asks once more. */
	/* The profile picture set in QTS (served through the backend so no session id ends up in an image URL). Without one, the
	   toolbar and the menu show the initial. */
	function loadPortrait(){
		if(!S.me || S.me.via !== 'session') return;
		var url = DC.api.url('me/portrait') + '?u=' + encodeURIComponent(S.me.user), img = new Image();
		img.onload = function(){
			if(!img.naturalWidth) return;
			S.portrait = url;
			if(R.whoPic){ clear(R.whoPic); R.whoPic.appendChild(h('img', {src:url, alt:''})); }
		};
		img.src = url;
	}
	function userMenu(){
		var me = S.me, closeFn;
		var initial = (me.user || '?').charAt(0).toUpperCase();
		var who = h('div', {'class':'me-id'}, [S.portrait ? h('img', {'class':'me-av', src:S.portrait, alt:''}) : h('span', {'class':'me-av', 'aria-hidden':'true', text:initial}), h('div', null, [
			h('b', {text:me.user}),
			h('span', {'class':'note', text:me.admin ? DC.t('系統管理者') : DC.t('一般使用者')}),
			me.nas && me.nas.hostname ? h('span', {'class':'note', text:DC.t('NAS：{host}', {host:me.nas.hostname})}) : null])]);
		var out = DC.embedded
			? h('div', {'class':'me-out'}, [h('button', {'class':'ib linkish', type:'button', onclick:function(){ closeFn(); window.open(location.href, '_blank', 'noopener'); }}, [icon('popout'), DC.t('在新分頁開啟')]),
				h('span', {'class':'note', text:DC.t('在 QTS 桌面裡沿用 QTS 的登入狀態，要登出請從 QTS 登出。')})])
			: h('div', {'class':'me-out'}, [h('button', {'class':'ib linkish out', type:'button', onclick:function(){
					closeFn();
					DC.confirm(DC.t('登出 Download Center？'), 'lock', DC.t('登入狀態和 QTS 共用，這個瀏覽器上的 QTS 也會一起登出。'), DC.t('登出'), function(){ stopLive(); DC.login.logout(); }, true);
				}}, [icon('lock'), DC.t('登出')]),
				h('span', {'class':'note', text:DC.t('也會登出這個瀏覽器上的 QTS。')})]);
		closeFn = DC.modal(DC.t('個人設定'), 'user', [
			who,
			me.role === 'admin' && !me.qts_admin ? h('p', {'class':'note warn', text:DC.t('清單上你是系統管理者，但這個帳號不是 QTS 管理員，所以目前只有一般使用者的權限。')}) : null,
			h('h3', {'class':'me-h', text:DC.t('外觀')}),
			DC.themeSeg(),
			/* Glass level only inside the QTS desktop, where the window sits over the desktop wallpaper; a full tab keeps the default */
			DC.embedded ? [h('h3', {'class':'me-h', text:DC.t('玻璃')}), glassSlider()] : null,
			out,
			h('div', {'class':'foot me-foot'}, [h('span', {'class':'num', text:'Download Center ' + ((me.nas && me.nas.version) || '')})].concat(DC.supportLinks()))
		], function(close){
			return DC.phone() ? [btn(null, DC.t('完成'), close, 'pri')] : [];
		}, {anchor:R.user});
	}
	function renderTop(){
		if(!R.spdDn) return;
		var dn = (S.rates && S.rates.down) || (S.stats && S.stats.down_rate) || 0, up = (S.rates && S.rates.up) || (S.stats && S.stats.up_rate) || 0, txt;
		var pd = DC.fspeedParts(dn), pu = DC.fspeedParts(up);
		clear(R.spdDn); add(R.spdDn, ['↓ ' + pd[0], h('small', {text:pd[1]})]);
		clear(R.spdUp); add(R.spdUp, ['↑ ' + pu[0], h('small', {text:pu[1]})]);
		txt = schedText();
		if(R.schedText !== txt){ R.schedText = txt; clear(R.sched); if(txt) add(R.sched, [icon('cal'), h('span', {'class':'ell', text:txt})]); R.sched.hidden = !txt; }
	}
	function renderNav(){
		clear(R.nav); R.counts = {}; R.navBtn = {};
		var i, f, c;
		app.className = 'app v-' + S.view;
		R.tabTasks.classList.toggle('on', S.view === 'tasks');
		R.tabSet.classList.toggle('on', S.view === 'settings');
		R.tabTasks.setAttribute('aria-current', S.view === 'tasks' ? 'page' : 'false');
		R.tabSet.setAttribute('aria-current', S.view === 'settings' ? 'page' : 'false');
		for(i = 0; i < FILTERS.length; i++){
			f = FILTERS[i];
			c = h('span', {'class':'cnt num'});
			R.counts[f.id] = c;
			R.navBtn[f.id] = h('button', {'class':'ib nv st-' + f.id + (S.view === 'tasks' && S.filter === f.id ? ' on' : ''), type:'button',
				onclick:(function(id){ return function(){ DC.leave(function(){ S.filter = id; S.sel = {}; S.picking = false; go('tasks'); }); }; })(f.id)}, [icon(f.icon, 'still'), h('span', {text:f.label}), c]);
			R.nav.appendChild(R.navBtn[f.id]);
		}
		R.nav.appendChild(h('div', {'class':'gap'}));
		R.nav.appendChild(h('button', {'class':'ib nv nv-set' + (S.view === 'settings' ? ' on' : ''), type:'button', onclick:function(){ DC.leave(function(){ go('settings'); }); }}, [icon('gear'), h('span', {text:DC.t('設定')})]));
		renderCounts();
	}
	/* Empty filters are hidden from the phone's filter strip so the useful ones fit without scrolling. */
	function renderCounts(){
		var i, j, n;
		if(!R.counts) return;
		for(i = 0; i < FILTERS.length; i++){
			n = 0;
			for(j = 0; j < S.tasks.length; j++) if(FILTERS[i].match(S.tasks[j])) n++;
			R.counts[FILTERS[i].id].textContent = n || '';
			R.navBtn[FILTERS[i].id].classList.toggle('zero', !n && FILTERS[i].id !== 'all');
		}
	}
	function go(view){ S.dirty = null; S.view = view; DC.detail.close(); renderNav(); renderView(); window.scrollTo(0, 0); }
	DC.go = go;
	/* A settings page with unsaved changes sets S.dirty; moving away from it asks first. */
	DC.leave = function(fn){
		if(!S.dirty || !S.dirty()){ S.dirty = null; fn(); return; }
		DC.confirm(DC.t('放棄未儲存的變更？'), 'gear', DC.t('這一頁有修改還沒儲存，離開後會還原。'), DC.t('放棄變更'), function(){ S.dirty = null; fn(); }, true);
	};
	window.addEventListener('beforeunload', function(e){ if(S.dirty && S.dirty()){ e.preventDefault(); e.returnValue = ''; } });
	function renderView(){
		clear(R.main); R.rows = {}; R.list = null; R.paintEnd = null;
		if(S.view === 'tasks') viewTasks();
		else DC.settings.view(R.main);
	}
	DC.renderView = function(){ renderView(); };

	/* ---------- tasks view ---------- */
	function viewTasks(){
		R.lhead = h('div', {'class':'lhead'});
		R.list = h('div', {'class':'list', role:'list'});
		R.launch = null;
		if(DC.can('tasks:add')){
			/* One pill opens the add window; '/' and pasting a link on the task list do the same */
			R.launch = h('button', {'class':'launch', type:'button', 'aria-haspopup':'dialog', title:DC.t('加入下載（/）'), onclick:function(){ DC.addFlow.compose(); }}, [
				h('span', {'class':'lk', 'aria-hidden':'true'}, [icon('link'), icon('magnet'), icon('torrent')]),
				h('span', {'class':'lplus'}, icon('plus')), h('span', {'class':'ltxt', text:DC.t('加入下載')})]);
		}
		add(R.main, [R.lhead, R.list]);
		renderList();
	}
	function filterById(id){ for(var i = 0; i < FILTERS.length; i++) if(FILTERS[i].id === id) return FILTERS[i]; return FILTERS[0]; }
	function indexOf(id){ for(var i = 0; i < S.tasks.length; i++) if(S.tasks[i].id === id) return i; return -1; }
	function sortVal(t){
		var s = DC.uiState(t);
		if(S.sort === 'status') return STATUS_RANK[s];
		if(S.sort === 'progress') return t.progress;
		if(S.sort === 'eta') return s === 'down' && t.eta > 0 ? t.eta : Infinity;
		return t.active_secs || 0;
	}
	/* Live values change every second, so a sorted list is re-ordered at most every 10 s to keep rows from jumping under the pointer. */
	function visible(){
		var f = filterById(S.filter), out = [], i, now = Date.now(), dir;
		for(i = 0; i < S.tasks.length; i++) if(f.match(S.tasks[i])) out.push(S.tasks[i]);
		if(S.sort === 'queue') return out;
		if(!S.sortAt || now - S.sortAt > 10000){
			dir = S.dir || SORT_DIR[S.sort];
			out.sort(function(a, b){
				var x = sortVal(a), y = sortVal(b);
				if(x === Infinity || y === Infinity){ if(x === y) return indexOf(a.id) - indexOf(b.id); return x === Infinity ? 1 : -1; }
				return x === y ? indexOf(a.id) - indexOf(b.id) : (x < y ? -dir : dir);
			});
			S.rank = {};
			for(i = 0; i < out.length; i++) S.rank[out[i].id] = i;
			S.sortAt = now;
			return out;
		}
		out.sort(function(a, b){
			var x = S.rank[a.id], y = S.rank[b.id];
			if(x === undefined) x = 1e6 + indexOf(a.id);
			if(y === undefined) y = 1e6 + indexOf(b.id);
			return x - y;
		});
		return out;
	}
	function setSort(key, dir){
		if(key !== S.sort) DC.track('sort_' + key);
		S.sort = key; S.dir = dir || null; S.sortAt = 0;
		var v = key + ':' + (dir || '');
		DC.ls('dc-sort', v);
		DC.savePref('sort', v);
		renderList();
	}
	function selIds(){ var out = [], k; for(k in S.sel) if(S.sel.hasOwnProperty(k) && S.sel[k]) out.push(k); return out; }
	function keyOf(v){ var a = [], i; for(i = 0; i < v.length; i++) a.push(v[i].id); return a.join(','); }
	function endPick(){ S.sel = {}; S.picking = false; renderList(); }
	/* Selecting: wide screens show checkboxes on hover and a bar in place of the title. Phones enter it with 選取 or a long press;
	   the bar moves to the bottom with labelled buttons, and the title row offers 全選 and 完成. */
	function renderHead(){
		clear(R.lhead);
		var ids = selIds(), f = filterById(S.filter), v = visible(), picking = ids.length > 0 || !!S.picking, none = !ids.length, all, i, sel, dir;
		R.list.classList.toggle('selecting', picking);
		app.classList.toggle('picking', picking);
		function bb(name, label, fn, cls){ return h('button', {'class':'ib sq bb' + (cls ? ' ' + cls : ''), type:'button', 'aria-label':label, title:label, disabled:none, onclick:fn}, [icon(name), h('span', {text:label})]); }
		if(picking){
			all = v.length > 0 && ids.length === v.length;
			R.lhead.appendChild(h('div', {'class':'selhead'}, [
				h('button', {'class':'ib linkish', type:'button', onclick:function(){ S.sel = {}; if(!all) for(var k = 0; k < v.length; k++) S.sel[v[k].id] = true; S.picking = true; renderList(); }}, all ? DC.t('全不選') : DC.t('全選')),
				h('b', {'class':'num', text:none ? DC.t('選取任務') : DC.t('已選 {n} 個', {n:ids.length})}),
				h('button', {'class':'ib linkish done-b', type:'button', onclick:endPick}, DC.t('完成'))
			]));
			R.lhead.appendChild(h('div', {'class':'bulk', role:'toolbar', 'aria-label':DC.t('批次操作')}, [
				h('b', {text:DC.t('已選 {n} 個', {n:ids.length})}),
				DC.can('tasks:control') ? bb('play', DC.t('開始'), function(){ bulk('resume'); }) : null,
				DC.can('tasks:control') ? bb('hold', DC.t('暫停'), function(){ bulk('pause'); }) : null,
				S.sort === 'queue' && DC.can('tasks:control') ? bb('up', DC.t('上移'), function(){ bulk('up'); }) : null,
				S.sort === 'queue' && DC.can('tasks:control') ? bb('dn', DC.t('下移'), function(){ bulk('down'); }) : null,
				DC.can('tasks:remove') ? bb('trash', DC.t('刪除'), function(){ askDelete(ids); }, 'dan') : null,
				h('span', {'class':'bx'}, ibtn('close', DC.t('取消選取'), endPick))
			]));
			return;
		}
		sel = h('select', {id:'sortKey', 'aria-label':DC.t('排序方式'), onchange:function(){ setSort(this.value, null); }});
		dir = S.dir || SORT_DIR[S.sort];
		for(i = 0; i < SORTS.length; i++) sel.appendChild(h('option', {value:SORTS[i][0], text:SORTS[i][1]}));
		sel.value = S.sort;
		add(R.lhead, [h('h1', {text:f.label}), h('span', {'class':'sub num', text:DC.t('{n} 個任務', {n:v.length})}),
			h('div', {'class':'sorter'}, [h('label', {'for':'sortKey', text:DC.t('排序')}), sel,
				S.sort === 'queue' ? null : ibtn(dir > 0 ? 'up' : 'dn', dir > 0 ? (S.sort === 'status' ? DC.t('需要注意的在前，按一下反過來') : DC.t('由小到大，按一下改為由大到小')) : (S.sort === 'status' ? DC.t('已完成的在前，按一下反過來') : DC.t('由大到小，按一下改為由小到大')), function(){ setSort(S.sort, -dir); }),
				v.length ? h('button', {'class':'ib pickbtn', type:'button', onclick:function(){ S.picking = true; DC.track('pick_mode'); renderList(); }}, DC.t('選取')) : null]),
			R.launch]);
	}
	/* Keeps existing row nodes so state icons only replay when the state really changes. */
	function renderList(){
		if(!R.list) return;
		var v = visible(), old = R.rows || {}, keep = {}, i, k, f, r, el, ref;
		renderHead();
		R.visKey = keyOf(v);
		if(!v.length){
			clear(R.list); R.rows = {};
			f = filterById(S.filter);
			R.list.appendChild(h('div', {'class':'empty'}, S.filter === 'all'
				? [icon('down'), h('b', {text:DC.t('還沒有下載')}), DC.phone() ? DC.t('點右下角的 + 貼上網址，或選擇 .torrent 檔。') : DC.t('按「加入下載」貼上網址，或直接把 .torrent 檔拖進這個視窗。')]
				: [icon(f.icon), h('b', {text:DC.t('沒有{filter}的任務', {filter:f.label})})]));
			return;
		}
		if(R.list.querySelector('.empty')) clear(R.list);
		R.rows = {};
		for(i = 0; i < v.length; i++){
			r = old[v[i].id];
			if(r){ R.rows[v[i].id] = r; r.cb.checked = !!S.sel[v[i].id]; r.el.classList.toggle('sel', r.cb.checked); updateRow(v[i]); }
			else buildRow(v[i]);
			keep[v[i].id] = 1;
		}
		for(k in old) if(old.hasOwnProperty(k) && !keep[k] && old[k].el.parentNode === R.list) R.list.removeChild(old[k].el);
		ref = R.list.firstChild;
		for(i = 0; i < v.length; i++){
			el = R.rows[v[i].id].el;
			if(el === ref) ref = ref.nextSibling;
			else R.list.insertBefore(el, ref);
		}
	}
	DC.renderList = function(){ renderList(); };
	function buildRow(t){
		var r = {id:t.id}, i, press;
		function toggleSel(on){ S.sel[r.id] = on; r.cb.checked = on; r.el.classList.toggle('sel', on); renderHead(); }
		r.cb = h('input', {type:'checkbox', 'aria-label':DC.t('選取 {name}', {name:t.name}), checked:!!S.sel[t.id], onclick:function(e){ e.stopPropagation(); }, onchange:function(){ toggleSel(this.checked); }});
		r.ic = h('span', {'class':'sicon'});
		r.bar = h('div', {'class':'bar'});
		r.nameEl = h('b', {text:t.name || t.source});
		r.meta = h('div', {'class':'meta num'});
		r.act = h('div', {'class':'ract'});
		r.el = h('div', {'class':'row', role:'listitem', tabindex:'0',
			onclick:function(){ if(r.long){ r.long = false; return; } if((S.picking || selIds().length) && DC.phone()){ toggleSel(!r.cb.checked); return; } DC.detail.open(r.id); },
			onkeydown:function(e){ if(e.target !== r.el) return; if(e.key === 'Enter') DC.detail.open(r.id); if(e.key === ' '){ e.preventDefault(); toggleSel(!r.cb.checked); } },
			oncontextmenu:function(e){ if(DC.phone()) e.preventDefault(); },
			ontouchstart:function(){ press = setTimeout(function(){ r.long = true; if(DC.phone()){ S.picking = true; DC.track('pick_mode'); } toggleSel(!r.cb.checked); }, 500); },
			ontouchend:function(){ clearTimeout(press); }, ontouchmove:function(){ clearTimeout(press); }},
			[r.cb, r.ic, h('div', {'class':'rmain'}, [
				h('div', {'class':'rname'}, [r.nameEl, DC.isAdmin() && t.owner !== DC.me() ? h('span', {'class':'owner', text:t.owner}) : null]),
				r.bar, r.meta]), r.act]);
		if(S.sel[t.id]) r.el.classList.add('sel');
		if(S.fresh && S.fresh[t.id]){ r.el.classList.add('arrive'); delete S.fresh[t.id]; }
		R.rows[t.id] = r;
		updateRow(t);
		return r.el;
	}
	/* Opens File Station on the folder the task's data is in right now (the server looks at the disk: the temporary folder
	   while downloading, the destination once moved), with the file selected for single-file tasks.
	   Inside the QTS desktop this is the desktop's own openApp message (the protocol of QMessageClient.js, as the official
	   Download Station used it); in a plain tab the desktop is opened with the same app and config in its URL (the QTS login
	   page passes a= and c= through to main.html the same way). File Station paths are share-relative with a leading slash. */
	DC.openFolder = function(t){
		DC.track('open_folder');
		var framed = DC.embedded && window.parent && window.parent !== window, w = null;
		/* A tab opened when the answer arrives would count as a popup: open it now and point it there afterwards */
		if(!framed) w = window.open('', '_blank');
		DC.api.get('tasks/' + encodeURIComponent(t.id) + '/folder').then(function(r){
			var cfg = {path:'/' + String(r.path || '').replace(/^\/+/, '')}, wid, msg, url;
			if(r.file) cfg.file = r.file;
			if(framed){
				wid = (/[?&]windowId=([^&#]*)/.exec(location.href) || [])[1] || '';
				msg = {CATEGORY:'QTS_DESKTOP', TYPE:'function', FN:'openApp', OPTION:{appId:'fileExplorer', config:cfg}, APP_ID:wid, CALLBACK:'fn_dc' + new Date().getTime()};
				try{ window.parent.postMessage(JSON.stringify(msg), location.protocol + '//' + location.host); return; }catch(e){}
			}
			url = '/cgi-bin/main.html?a=fileExplorer&c=' + encodeURIComponent(JSON.stringify(cfg));
			if(w){ w.opener = null; w.location.href = url; }
			else window.open(url, '_blank', 'noopener');
		}, function(){ if(w) w.close(); });
	};
	function updateRow(t){
		var r = R.rows[t.id], i, on, cells, frac, pct, s = DC.uiState(t), key;
		if(!r) return;
		if(r.nameEl.textContent !== (t.name || t.source)) r.nameEl.textContent = t.name || t.source;
		/* Bar kind can change when a magnet turns into a torrent with files */
		if(r.barKind !== t.proto){
			clear(r.bar); r.pieces = null; r.fill = null;
			if(t.proto === 'bt'){
				r.pieces = h('div', {'class':'pieces', 'aria-hidden':'true'});
				for(i = 0; i < 48; i++) r.pieces.appendChild(h('i'));
				r.bar.appendChild(r.pieces);
			}else{ r.fill = h('i'); r.bar.appendChild(h('div', {'class':'flow', 'aria-hidden':'true'}, r.fill)); }
			r.barKind = t.proto; r.primed = false;
		}
		key = s + '|' + (t.user_paused ? 1 : 0);
		if(r.state !== key){
			r.el.className = r.el.className.replace(/\bst-\w+/g, '').replace(/\s+$/, '') + ' st-' + s;
			clear(r.ic).appendChild(icon(ST[s].icon));
			r.ic.title = ST[s].label;
			clear(r.act);
			if(DC.can('tasks:control')){
				if(s === 'down' || s === 'wait' || s === 'seed' || s === 'check') r.act.appendChild(ibtn('hold', s === 'seed' ? DC.t('停止做種') : DC.t('暫停'), function(){ act(r.id, 'pause'); }));
				if(s === 'pause') r.act.appendChild(ibtn('play', DC.t('繼續'), function(){ act(r.id, 'resume'); }));
				if(s === 'error') r.act.appendChild(ibtn('retry', DC.t('重試'), function(){ act(r.id, 'retry'); }));
			}
			if(s === 'done') r.act.appendChild(ibtn('folder', DC.t('開啟資料夾'), function(){ DC.openFolder(S.byId[r.id] || t); }));
			r.act.appendChild(ibtn('more', DC.t('詳細資訊'), function(){ DC.detail.open(r.id); })).className += ' more-b';
			r.state = key;
		}
		frac = Math.max(0, Math.min(1, (t.progress || 0) / 100));
		if(s === 'done') frac = 1;
		if(r.pieces){
			cells = r.pieces.children;
			for(i = 0; i < 48; i++){
				on = (t._order && t._order[i] < frac) || frac >= 1;
				if(on && cells[i].className.indexOf('on') < 0) cells[i].className = r.primed ? 'on new' : 'on';
				else if(!on && cells[i].className) cells[i].className = '';
				else if(cells[i].className === 'on new') cells[i].className = 'on';
			}
			r.primed = true;
		}else r.fill.style.width = (frac * 100).toFixed(1) + '%';
		clear(r.meta);
		/* Plain strings would merge into one text node; every piece gets its own span so the gap separates them */
		function meta(kids){ for(var j = 0; j < kids.length; j++) if(kids[j] !== null && kids[j] !== undefined && kids[j] !== '') r.meta.appendChild(typeof kids[j] === 'string' ? h('span', {text:kids[j]}) : kids[j]); }
		pct = Math.floor(t.progress || 0) + '%';
		if(t.state === 'metadata') meta([DC.t('正在取得檔案清單…'), t.peers ? DC.t('{n} 位使用者', {n:t.peers}) : null]);
		else if(s === 'down') meta([h('span', {'class':'em', text:pct}), h('span', {'class':'opt', text:DC.fsize(t.done) + ' / ' + DC.fsize(t.size)}), DC.fspeed(t.down_rate), DC.feta(t.eta)]);
		else if(S.sort === 'progress' && s !== 'done' && s !== 'error') r.meta.appendChild(h('span', {'class':'em', text:pct}));
		else if(s === 'wait') meta([t.sched_paused ? DC.t('排程暫停中') : DC.t('排隊中'), t.size ? DC.fsize(t.size) : null]);
		else if(s === 'pause') meta([t.wake_time ? DC.t('{time} 自動繼續', {time:DC.fclock(t.wake_time)}) : DC.t('暫停於 {pct}', {pct:pct}), t.size ? DC.fsize(t.size) : null]);
		else if(s === 'seed') meta([h('span', {'class':'em', text:DC.t('分享率 {ratio}', {ratio:(t.ratio || 0).toFixed(2)})}), DC.t('上傳 {speed}', {speed:DC.fspeed(t.up_rate)}), DC.fsize(t.size)]);
		else if(s === 'done') meta([DC.fsize(t.size), h('span', {'class':'opt', text:t.location || t.folder})]);
		else if(s === 'check') meta([DC.t('檢查已下載的部分 {pct}', {pct:pct}), DC.fsize(t.size)]);
		else if(s === 'move') meta([DC.t('移到 {folder}…', {folder:t.move_to || t.folder}), DC.fsize(t.size)]);
		else meta([h('span', {'class':'err', text:(t.error && t.error.message) || DC.t('發生錯誤')})]);
		if(S.sort === 'elapsed') r.meta.appendChild(h('span', {text:DC.t('已下載 {time}', {time:DC.fdur(t.active_secs)})}));
	}
	function refresh(){
		renderTop();
		if(!R.counts) return;
		renderCounts();
		if(R.list){
			var v = visible(), i;
			if(keyOf(v) !== R.visKey) renderList();
			else for(i = 0; i < v.length; i++) updateRow(v[i]);
		}
		if(DC.detail.current()) DC.detail.live();
	}
	DC.refresh = refresh;

	/* ---------- actions ---------- */
	function act(id, what){
		var p = what === 'pause' ? DC.api.post('tasks/' + id + '/pause', {}) : what === 'retry' ? DC.api.post('tasks/' + id + '/retry', {}) : DC.api.post('tasks/' + id + '/resume', {});
		return p.then(function(r){ if(r.task) DC.upsertTask(r.task); DC.pollNow(); }, function(e){ DC.toast(DC.errText(e)); });
	}
	DC.act = act;
	function bulk(what){
		DC.track('bulk');
		var ids = selIds(), body = {ids:ids, action:what};
		if(!ids.length) return;
		/* Moving several keeps their order: send them in queue order */
		ids.sort(function(a, b){ return indexOf(a) - indexOf(b); });
		DC.api.post('tasks/bulk', body).then(function(r){
			if(what === 'up' || what === 'down'){ DC.pollNow(); return; }
			DC.toast(what === 'resume' ? DC.t('已開始 {n} 個任務', {n:r.count || ids.length}) : DC.t('已暫停 {n} 個任務', {n:r.count || ids.length}));
			DC.pollNow();
		}, function(e){ DC.toast(DC.errText(e)); });
	}
	/* Delete with an in-page dialog; files are kept unless asked, and kept-file deletes can be undone for 6 seconds. */
	function askDelete(ids){
		var cb = h('input', {type:'checkbox', id:'delFiles'}), tempUrl = false, i, t;
		for(i = 0; i < ids.length; i++){ t = S.byId[ids[i]]; if(t && t.proto !== 'bt' && DC.uiState(t) !== 'done') tempUrl = true; }
		DC.modal(DC.t('刪除 {n} 個任務', {n:ids.length}), 'trash', [
			h('p', {'class':'lead', text:tempUrl ? DC.t('任務會從清單移除。還沒下載完的網址任務，暫存檔會一起刪除。') : DC.t('任務會從清單移除，已下載的檔案預設保留。')}),
			DC.can('files:delete') ? h('label', {'class':'toggle', 'for':'delFiles'}, [cb, h('span', null, [DC.t('同時刪除已下載的檔案'), h('small', {text:DC.t('刪除後無法復原')})])]) : null
		], function(close){
			return [btn(null, DC.t('取消'), close), btn(null, DC.t('刪除'), function(e){
				var files = cb.checked, b = e.currentTarget;
				DC.busy(b, true);
				DC.api.post('tasks/bulk', {ids:ids, action:'remove', delete_files:files}).then(function(r){
					close(); DC.detail.close();
					S.sel = {}; S.picking = false;
					var i;
					for(i = 0; i < ids.length; i++) delete S.byId[ids[i]];
					S.tasks = S.tasks.filter(function(t){ return ids.indexOf(t.id) < 0; });
					renderList(); renderCounts();
					DC.toast(files ? DC.t('已刪除 {n} 個任務和檔案', {n:r.count === undefined ? ids.length : r.count}) : DC.t('已刪除 {n} 個任務', {n:r.count === undefined ? ids.length : r.count}), files ? null : {label:DC.t('復原'), fn:function(){
						var n = 0, k, ps = [];
						for(k = 0; k < ids.length; k++) ps.push(DC.api.post('tasks/' + ids[k] + '/undo', {}, {quiet:true}).then(function(){ n++; }, function(){}));
						Promise.all(ps).then(function(){ DC.toast(n ? DC.t('已復原') : DC.t('已無法復原')); DC.pollNow(); });
					}});
					DC.pollNow();
				}, function(err){ DC.busy(b, false); DC.toast(DC.errText(err)); });
			}, 'dan pri')];
		});
	}
	DC.askDelete = askDelete;
	DC.markFresh = function(id){ S.fresh = S.fresh || {}; S.fresh[id] = true; };
	/* The sidebar count of 全部 pops when a task lands in the queue */
	DC.bumpCount = function(){ var c = R.counts && R.counts.all; if(!c) return; c.classList.remove('bump'); void c.offsetWidth; c.classList.add('bump'); };

	/* ---------- global: drag-and-drop, shortcuts ---------- */
	var dragDepth = 0;
	function hasFiles(e){ var t = e.dataTransfer && e.dataTransfer.types, i; if(!t) return false; for(i = 0; i < t.length; i++) if(t[i] === 'Files') return true; return false; }
	window.addEventListener('dragenter', function(e){
		if(!S.me || !hasFiles(e) || !DC.can('tasks:add')) return;
		dragDepth++;
		if(!R.dropEl){ R.dropEl = h('div', {'class':'drop'}, h('div', null, [icon('torrent', 'play'), h('div', {text:DC.t('放開以加入種子')})])); DC.layer().appendChild(R.dropEl); }
	});
	window.addEventListener('dragleave', function(){ dragDepth = Math.max(0, dragDepth - 1); if(!dragDepth && R.dropEl){ DC.remove(R.dropEl); R.dropEl = null; } });
	window.addEventListener('dragover', function(e){ if(hasFiles(e)) e.preventDefault(); });
	window.addEventListener('drop', function(e){
		if(!hasFiles(e)) return;
		e.preventDefault(); dragDepth = 0;
		if(R.dropEl){ DC.remove(R.dropEl); R.dropEl = null; }
		if(!S.me || !DC.can('tasks:add')) return;
		DC.track('add_drop');
		DC.addFlow.openTorrents(e.dataTransfer.files);
	});
	document.addEventListener('pointerup', function(){ if(R.paintEnd) R.paintEnd(); });
	document.addEventListener('keydown', function(e){
		var tag = (e.target.tagName || '').toLowerCase();
		if(e.key === 'Escape'){ if(R.dismissModal) R.dismissModal(); else if(R.closeModal) R.closeModal(); else DC.detail.close(); }
		if(S.me && e.key === '/' && tag !== 'input' && tag !== 'textarea' && tag !== 'select' && DC.can('tasks:add')){
			e.preventDefault();
			if(S.view !== 'tasks') go('tasks');
			DC.addFlow.compose();
		}
	});

	applyTheme(DC.ls('dc-theme') || 'auto', false);
	applyGlass(!DC.embedded || DC.ls('dc-glass') === null ? 55 : +DC.ls('dc-glass'), false);
	DC.boot();
})();
