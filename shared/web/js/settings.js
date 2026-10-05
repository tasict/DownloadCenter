/* Settings: tabs on wide screens, an iOS-style section index on phones. Download, schedule and users live here;
   accounts, tokens, notifications and import in settings-more.js. */
(function(){
	'use strict';
	var DC = window.DC, h = DC.h, add = DC.add, clear = DC.clear, icon = DC.icon, ibtn = DC.ibtn, btn = DC.btn, R = DC.R;
	var field = DC.field, fieldDiv = DC.fieldDiv, toggle = DC.toggle, num = DC.num, sec = DC.sec;
	var SET_INFO = {
		dl:['down', DC.t('Folders, concurrent downloads, speed, torrents')], sched:['cal', DC.t('When to run at full speed, limited speed or pause')], users:['user', DC.t('Who can use Download Center')],
		acct:['key', DC.t('Accounts for sites that require sign-in and file-hosting services')], token:['ticket', DC.t('Let other apps use your downloads')], notify:['bell', DC.t('Telegram, Discord, Webhook')], import:['inbox', DC.t('Settings, tasks and site accounts')], about:['retry', DC.t('Version, updates and links')]
	};
	var importAvail = null, importAsk = false;
	/* After a successful import the section disappears (kept on screen until the next navigation, so the summary stays readable). */
	DC.importDone = function(){ importAvail = false; };
	/* The tab is shown once the backend says there is something to import; a reload on it waits for that answer. */
	function importKnown(avail){
		var S = DC.S;
		importAvail = avail; importAsk = false;
		if((avail || S.setTab === 'import') && S.view === 'settings' && !(S.dirty && S.dirty())) DC.renderView();
	}

	function tabs(){
		var admin = DC.isAdmin(), t;
		t = admin ? [['dl', DC.t('Downloads')], ['sched', DC.t('Schedule')], ['users', DC.t('Users')], ['acct', DC.t('Site accounts')], ['token', DC.t('Access tokens')], ['notify', DC.t('Notifications & integrations')]]
			: [['dl', DC.t('Downloads')], ['acct', DC.t('Site accounts')], ['token', DC.t('Access tokens')], ['notify', DC.t('Notifications & integrations')]];
		if(admin && importAvail) t.push(['import', DC.t('Import from official version')]);
		if(admin && DC.S.me.via === 'session') t.push(['about', DC.t('About and updates')]);
		if(DC.S.me.via === 'token') t = [['dl', DC.t('Downloads')]];
		return t;
	}
	function view(main){
		var S = DC.S, admin = DC.isAdmin(), list, tb, body, i, ok = false, label = '', ts, wait;
		if(admin && importAvail === null){
			importAvail = false; importAsk = true;
			DC.api.get('import', null, {quiet:true}).then(function(r){ importKnown(!!(r && r.available)); }, function(){ importKnown(false); });
		}
		S.dirty = null;
		ts = tabs();
		for(i = 0; i < ts.length; i++) if(ts[i][0] === S.setTab){ ok = true; label = ts[i][1]; }
		wait = !ok && importAsk && S.setTab === 'import';
		if(wait) label = DC.t('Import from official version');
		else if(!ok){ S.setTab = 'dl'; label = ts[0][1]; }
		/* Phone: seven tabs do not fit, so settings open on an index like the iOS Settings app and each section is its own page. */
		if(DC.phone() && !S.setOpen){
			list = h('div', {'class':'group setidx'});
			for(i = 0; i < ts.length; i++){
				list.appendChild(h('button', {'class':'ib srow t' + (i % 7), type:'button', onclick:(function(id){ return function(){ S.setTab = id; S.setOpen = true; DC.track('set_' + id); DC.renderView(); window.scrollTo(0, 0); }; })(ts[i][0])},
					[icon(SET_INFO[ts[i][0]][0]), h('span', null, [h('b', {text:ts[i][1]}), h('small', {text:SET_INFO[ts[i][0]][1]})]), icon('chev', 'chev')]));
			}
			add(main, [h('div', {'class':'lhead'}, [h('h1', {text:DC.t('Settings')}), admin ? null : h('span', {'class':'sub', text:DC.t('Affects only your own downloads')})]), list, foot()]);
			return;
		}
		tb = h('div', {'class':'tabs', role:'tablist'});
		for(i = 0; i < ts.length; i++){
			tb.appendChild(h('button', {'class':'tab' + (S.setTab === ts[i][0] ? ' on' : ''), type:'button', role:'tab', 'aria-selected':S.setTab === ts[i][0] ? 'true' : 'false',
				onclick:(function(id){ return function(){ if(S.setTab !== id) DC.leave(function(){ S.setTab = id; DC.track('set_' + id); DC.renderView(); }); }; })(ts[i][0])}, ts[i][1]));
		}
		body = h('div');
		if(wait) loadingInto(body);
		else if(S.setTab === 'dl') (admin ? setDownload : setDownloadUser)(body);
		else if(S.setTab === 'sched') setSchedule(body);
		else if(S.setTab === 'users') setUsers(body);
		else if(S.setTab === 'acct') DC.setMore.accounts(body);
		else if(S.setTab === 'token') DC.setMore.tokens(body);
		else if(S.setTab === 'notify') DC.setMore.notify(body);
		else if(S.setTab === 'about') DC.update.settings(body);
		else DC.setMore.importer(body);
		add(main, [
			DC.phone()
				? h('div', {'class':'lhead sub-page'}, [h('button', {'class':'ib linkish back', type:'button', onclick:function(){ DC.leave(function(){ S.setOpen = false; DC.renderView(); window.scrollTo(0, 0); }); }}, [icon('back'), DC.t('Settings')]), h('h1', {text:label})])
				: h('div', {'class':'lhead'}, [h('h1', {text:DC.t('Settings')}), admin ? null : h('span', {'class':'sub', text:DC.t('Affects only your own downloads')})]),
			h('div', {'class':'set'}, [tb, body]), foot()]);
	}
	function foot(){
		var v = DC.S.me && DC.S.me.nas && DC.S.me.nas.version;
		return h('footer', {'class':'foot'}, [h('span', {'class':'num', text:'Download Center' + (v ? ' ' + v : '')}),
			h('button', {type:'button', onclick:function(){ DC.showNotice(false); }}, DC.t('Terms of use')),
			h('a', {href:'docs/third-party-notices.txt', target:'_blank', rel:'noopener'}, DC.t('Licenses'))].concat(DC.supportLinks()));
	}
	function loadingInto(body){ body.appendChild(h('div', {'class':'loading'}, [icon('check'), DC.t('Loading…')])); }
	function errorInto(body, e){ clear(body); body.appendChild(h('p', {'class':'note warn', text:DC.errText(e)})); }
	DC.loadingInto = loadingInto; DC.errorInto = errorInto;
	/* Save bar of a settings page: it sticks to the bottom of the window and says so while something is changed, and the page
	   asks before it is left with unsaved changes (DC.leave). */
	function saveBar(onSave, onRevert){
		var dirty = false, el;
		var sb = {save:btn(null, DC.t('Save'), function(){ onSave(); }, 'pri')};
		el = sb.el = h('div', {'class':'savebar sticky'}, [h('span', {'class':'dirtynote', text:DC.t('Unsaved changes')}),
			h('span', {'class':'dirtybtn'}, btn(null, DC.t('Revert'), function(){ sb.clean(); onRevert(); })), sb.save]);
		sb.mark = function(){ if(dirty) return; dirty = true; el.classList.add('dirty'); };
		sb.clean = function(){ dirty = false; el.classList.remove('dirty'); };
		DC.S.dirty = function(){ return dirty && document.body.contains(el); };
		return sb;
	}

	/* ---------- Downloads (regular user) ---------- */
	function setDownloadUser(body){
		var me = DC.S.me, home = me.home_folder || 'home/Download';
		add(body, [
			sec('folder', DC.t('My folders'), null, [h('p', {'class':'note', text:DC.t('All downloaded files are saved to {home} in your home folder, and you own them. Until they finish, downloads stay in @DownloadCenterTemp inside it and only then move to {home}; torrents keep seeding from there.', {home:home})})])
		]);
	}

	/* ---------- Downloads (administrator) ---------- */
	var SEED_TIMES = [[-1, DC.t('No seeding')], [0, DC.t('No time limit (set share ratio to 0 to seed forever)')], [30, DC.t('30 minutes')], [60, DC.t('1 hour')], [180, DC.t('3 hours')], [360, DC.t('6 hours')], [720, DC.t('12 hours')], [1440, DC.t('1 day')], [2880, DC.t('2 days')], [4320, DC.t('3 days')], [10080, DC.t('1 week')], [20160, DC.t('2 weeks')]];
	var PEER_MODES = [[1, DC.t('Download Center (default)')], [2, 'Deluge 1.3.12'], [3, 'Transmission 2.94'], [4, 'uTorrent Mac 1.8.7'], [0, DC.t('Custom')]];
	/* A section whose intro has a short line and the rest behind Help */
	function secMore(iconName, title, intro, more, kids){
		var el = sec(iconName, title, null, kids);
		el.insertBefore(h('div', {'class':'secintro'}, [h('p', {text:intro}), h('details', null, [h('summary', {text:DC.t('More details')}), h('p', {text:more})])]), el.children[1]);
		return el;
	}
	function setDownload(body){
		loadingInto(body);
		DC.api.get('settings').then(function(r){ clear(body); downloadForm(body, r); }, function(e){ errorInto(body, e); });
	}
	function downloadForm(body, r){
		var s = r.settings, tor = s.torrent || {}, px = s.proxy || {}, eng = tor.engine || '', caps = (r.engines || {})[eng] || {}, ucaps = (r.engines || {}).builtin || {};
		var bar = saveBar(function(){ save(); }, function(){ downloadForm(clear(body), r); }), saveB = bar.save;
		var temp = DC.folderPicker('sTemp', s.temp_dir, false, function(){ bar.mark(); }), move = DC.folderPicker('sMove', s.move_dir, true, function(){ bar.mark(); });
		var portRes = h('span', {'class':'unit', role:'status'});
		var seedTime = DC.select('bTime', SEED_TIMES, String(tor.seed_time === undefined ? 0 : tor.seed_time));
		var known = false, k;
		for(k = 0; k < SEED_TIMES.length; k++) if(SEED_TIMES[k][0] === tor.seed_time) known = true;
		if(!known) seedTime.appendChild(h('option', {value:String(tor.seed_time), text:DC.t('{n} minutes', {n:tor.seed_time}), selected:true}));
		var peerCustom = h('div', {hidden:tor.peer_mode !== 0}, [
			field(DC.t('Client ID'), DC.t('Two letters'), h('input', {type:'text', id:'bPeerId', value:tor.peer_id || '', maxlength:'2', size:'3'}), 'bPeerId'),
			field(DC.t('Version'), null, h('input', {type:'text', id:'bPeerVer', value:tor.peer_version || '', size:'8'}), 'bPeerVer'),
			field('User agent', null, h('input', {type:'text', id:'bPeerAgent', value:tor.peer_agent || '', maxlength:'64'}), 'bPeerAgent')]);
		var peerSel = DC.select('bPeerMode', PEER_MODES, String(tor.peer_mode === undefined ? 1 : tor.peer_mode), function(){ peerCustom.hidden = this.value !== '0'; });
		function kind(id, t, help){
			return [field(t, help || null, num(id + 'Max', s[id] ? s[id].max_num : 1, DC.t('max'), {min:'1', max:'50'}), id + 'Max')];
		}
		function sv(key){ var k = key.split('.'); return s[k[0]] ? s[k[0]][k[1]] || 0 : 0; }
		function spIn(id, key, label){ var v = sv(key); return h('input', {type:'number', id:id, value:v ? String(v) : '', min:'0', inputmode:'numeric', placeholder:DC.t('No limit'), 'aria-label':label}); }
		function speedRow(label, a, ak, b, bk){
			return h('div', {'class':'sprow', role:'row'}, [h('span', {role:'rowheader', text:label}), spIn(a, ak, DC.t('{what}, normal', {what:label})), spIn(b, bk, DC.t('{what}, limited speed period', {what:label}))]);
		}
		add(body, [
			secMore('folder', DC.t('Folder'), DC.t('Locations preselected when adding downloads; you can still change them in the dialog.'), DC.t('Downloads in progress stay in @DownloadCenterTemp in the shared folder of the temporary location and move to “Move to when finished” only once complete, or to the temporary location if “Don\'t move” is chosen; torrents keep seeding from where they were moved. Regular users always save to home/Download in their own home folder.'), [
				fieldDiv(DC.t('Default temporary location'), null, temp.el),
				fieldDiv(DC.t('Default move-to folder when finished'), null, move.el)
			]),
			sec('done', DC.t('Finished tasks'), DC.t('Only removed from the list. Files are kept and the record stays in the history.'), [
				field(DC.t('When finished'), DC.t('Defaults for new tasks; can be changed when adding'), DC.select('sAuto', [['', DC.t('Keep in list')], ['completed', DC.t('Remove when downloaded')], ['seeded', DC.t('Remove when seeded (torrents)')]], s.auto_remove || ''), 'sAuto'),
				field(DC.t('Keep history for'), null, num('sHist', s.history_days || 90, DC.t('days'), {min:'1'}), 'sHist')
			]),
			sec('all', DC.t('Concurrent downloads'), DC.t('Extra tasks are queued and start in list order when a slot is free.'), [kind('bt', DC.t('Torrent'), eng ? null : DC.t('This NAS cannot download torrents (the BT engine is missing)')), kind('http', DC.t('URL'), ucaps.urls ? null : DC.t('This NAS cannot download this kind of URL (the download component dc-dl is unavailable)')), kind('ftp', DC.t('FTP/SFTP'), ucaps.ftp ? null : DC.t('This NAS cannot download this kind of URL (the download component dc-dl is unavailable)'))]),
			sec('gauge', DC.t('Speed'), DC.t('Values are in KB/s; leave empty for no limit. “Limited speed” periods in the schedule use the right-hand column.'), [
				h('div', {'class':'sptab', role:'table', 'aria-label':DC.t('Speed limits')}, [
					h('div', {'class':'sprow sphead', role:'row'}, [h('span'), h('span', {role:'columnheader', text:DC.t('Normal')}), h('span', {role:'columnheader', text:DC.t('Limited speed period')})]),
					speedRow(DC.t('Torrent download'), 'bDn', 'bt.max_down', 'bLDn', 'bt.limited_down'),
					speedRow(DC.t('Torrent upload'), 'bUp', 'bt.max_up', 'bLUp', 'bt.limited_up'),
					speedRow(DC.t('URL download'), 'hDn', 'http.max_down', 'hLDn', 'http.limited_down'),
					speedRow(DC.t('FTP download'), 'fDn', 'ftp.max_down', 'fLDn', 'ftp.limited_down')])
			]),
			sec('torrent', DC.t('Torrent'), null, [
				fieldDiv(DC.t('Incoming port'), caps.upnp ? null : DC.t('Forward these ports to the NAS on your router'), [h('input', {type:'number', id:'pFrom', value:String(tor.lt_port_from), min:'1024', max:'65535', 'aria-label':DC.t('Start port')}), '–', h('input', {type:'number', id:'pTo', value:String(tor.lt_port_to), min:'1024', max:'65535', 'aria-label':DC.t('End port')}),
					btn('link', DC.t('Test incoming port'), function(e){
						var b = e.currentTarget;
						/* Consent is asked for every test and never stored: the external service only learns the address for this one check */
						DC.confirm(DC.t('Allow an external service to test the incoming port?'), 'link', DC.t('The test sends the NAS\'s public IP and the saved incoming ports to an external checking service (ifconfig.co), which connects back from the internet to check them. It is allowed only for this test and revoked right after.'), DC.t('Allow and test'), function(){
						DC.busy(b, true); portRes.textContent = DC.t('Testing…');
						DC.api.post('settings/port-test', {consent:true}).then(function(res){
							DC.busy(b, false);
							var okP = [], noP = [], j, x;
							for(j = 0; j < (res.results || []).length; j++){ x = res.results[j]; if(x.reachable) okP.push(x.port); else noP.push(x.port); }
							portRes.textContent = DC.sentences(okP.length ? DC.t('{ports} can be reached from the internet.', {ports:okP.join(DC.t(', '))}) : '', noP.length ? (res.ip ? DC.t('{ports} not open. Forward these ports to the NAS ({ip}) on your router.', {ports:noP.join(DC.t(', ')), ip:res.ip}) : DC.t('{ports} not open. Forward these ports to the NAS on your router.', {ports:noP.join(DC.t(', '))})) : '');
						}, function(err){ DC.busy(b, false); portRes.textContent = DC.errText(err); });
						});
					})]),
				h('div', {'class':'inline'}, [portRes]),
				caps.upnp ? toggle('bUpnp', DC.t('Open incoming ports automatically'), DC.t('Ask the router to forward ports with UPnP/NAT-PMP. Not used when “Proxy only” is on'), !!tor.upnp) : null,
				toggle('bDht', 'DHT', DC.t('Find peers even without a tracker'), !!tor.dht),
				toggle('bLsd', DC.t('Local peer discovery'), DC.t('Share with peers on the same local network'), !!tor.lsd),
				toggle('bPex', DC.t('Peer exchange'), DC.t('Ask connected peers for more sources'), !!tor.pex),
				toggle('bEnc', DC.t('Encrypted connections only'), DC.t('Transfer only with peers that support encryption; there may be fewer sources'), !!tor.encrypt),
				fieldDiv(DC.t('Share ratio and seeding time'), DC.t('Seeding stops when either the share ratio or the seeding time is reached; a share ratio of 0 means the ratio is ignored'), [num('bRatio', tor.seed_ratio, DC.t('ratio'), {step:'0.1'}), h('span', {'class':'unit', text:DC.t('or')}), seedTime]),
				caps.global_conn_limit ? field(DC.t('Total connection limit'), null, num('bConn', tor.max_conn, DC.t('max')), 'bConn') : null,
				field(DC.t('Connection limit per torrent'), DC.t('0 means no limit'), num('bTConn', tor.torrent_max_conn, DC.t('max')), 'bTConn'),
				field(DC.t('Upload limit per torrent'), DC.t('0 means no limit'), num('bTUp', tor.torrent_max_up, 'KB/s'), 'bTUp'),
				field(DC.t('Client identity'), DC.t('Some private trackers accept only specific clients'), peerSel, 'bPeerMode'),
				peerCustom,
			]),
			proxySec(px, caps, ucaps),
			sec('inbox', DC.t('When space runs low'), null, [field(DC.t('Free space below'), DC.t('0 means no check. Below this, tasks downloading to this volume are paused and you are notified'), num('sDisk', s.disk_low_mb ? Math.round(s.disk_low_mb / 1024) : 0, 'GB'), 'sDisk')]),
			v4Sec(),
			bar.el
		]);
		/* Every edit on the page marks it changed, except the parts that act on their own (the folder browser, the
		   Download Station switch) */
		function changed(e){ var tg = e && e.target; if(tg && (tg.id === 'v4On' || (tg.closest && tg.closest('.fpick')))) return; bar.mark(); }
		/* Revert renders the form again into the same box: replace the listener rather than adding another */
		if(body._changed){ body.removeEventListener('input', body._changed); body.removeEventListener('change', body._changed); }
		body._changed = changed;
		body.addEventListener('input', changed);
		body.addEventListener('change', changed);
		function save(){
			var o = {
				temp_dir:temp.value, move_dir:move.value, auto_remove:DC.val('sAuto'), history_days:DC.ival('sHist'),
				http:{max_num:DC.ival('httpMax'), max_down:DC.ival('hDn'), limited_down:DC.ival('hLDn'), max_up:(s.http || {}).max_up || 0, limited_up:(s.http || {}).limited_up || 0},
				ftp:{max_num:DC.ival('ftpMax'), max_down:DC.ival('fDn'), limited_down:DC.ival('fLDn'), max_up:(s.ftp || {}).max_up || 0, limited_up:(s.ftp || {}).limited_up || 0},
				bt:{max_num:DC.ival('btMax'), max_down:DC.ival('bDn'), max_up:DC.ival('bUp'), limited_down:DC.ival('bLDn'), limited_up:DC.ival('bLUp')},
				torrent:{dht:DC.chk('bDht'), lsd:DC.chk('bLsd'), pex:DC.chk('bPex'), encrypt:DC.chk('bEnc'), seed_ratio:DC.fval('bRatio'), seed_time:parseInt(DC.val('bTime'), 10),
					torrent_max_conn:DC.ival('bTConn'), torrent_max_up:DC.ival('bTUp'), peer_mode:DC.ival('bPeerMode')},
				disk_low_mb:DC.ival('sDisk') * 1024
			};
			if(document.getElementById('bUpnp')) o.torrent.upnp = DC.chk('bUpnp');
			if(document.getElementById('bConn')) o.torrent.max_conn = DC.ival('bConn');
			if(o.torrent.peer_mode === 0){ o.torrent.peer_id = DC.val('bPeerId'); o.torrent.peer_version = DC.val('bPeerVer'); o.torrent.peer_agent = DC.val('bPeerAgent'); }
			o.torrent.lt_port_from = DC.ival('pFrom'); o.torrent.lt_port_to = DC.ival('pTo');
			if(o.torrent.peer_mode === 0 && !/^[a-zA-Z~]{2}$/.test(o.torrent.peer_id || '')){ DC.toast(DC.t('The client ID must be two letters')); return; }
			/* Only the choices: profiles are saved from their own window */
			o.proxy = pxState.collect();
			DC.busy(saveB, true, DC.t('Saving…'));
			DC.api.put('settings', o).then(function(res){
				DC.busy(saveB, false); DC.toast(DC.t('Settings saved'));
				bar.clean(); r = res; DC.pollStats(); DC.refreshDefaults();
			}, function(e){ DC.busy(saveB, false); DC.toast(DC.errText(e)); });
		}
		/* Proxy profiles: URL tasks pick one when they are added (or one is chosen by site), torrents share the profile chosen here.
		   Profiles are saved from their own window at once; the choices below are saved with the rest of the page. A task that is
		   given a proxy never falls back to a direct connection. c: torrent engine capabilities, u: URL engine capabilities. */
		var pxState;
		function proxySec(p, c, u){
			var wrap = h('div'), mem = {};
			pxState = {};
			function list(){ return p.profiles || []; }
			function byId(id){ var l = list(), i; for(i = 0; i < l.length; i++) if(l[i].id === id) return l[i]; return null; }
			function choices(){ var o = [['', DC.t('No proxy')]], l = list(), i; for(i = 0; i < l.length; i++) o.push([l[i].id, l[i].name]); return o; }
			function val(k, d){ return mem.hasOwnProperty(k) ? mem[k] : d; }
			function keep(){
				['pxDef:url_default', 'pxBt:bt', 'pxNotify:notify_profile'].forEach(function(x){ var k = x.split(':'); if(document.getElementById(k[0])) mem[k[1]] = DC.val(k[0]); });
				['pxReq:require_for_users', 'pxTrk:apply_trackers', 'pxPeers:apply_peers', 'pxOnly:force'].forEach(function(x){ var k = x.split(':'); if(document.getElementById(k[0])) mem[k[1]] = DC.chk(k[0]); });
			}
			function render(){
				var items = h('div'), l = list(), i, bt = byId(val('bt', p.bt || ''));
				if(!l.length) items.appendChild(DC.emptyAdd('link', DC.t('No proxies yet.'), DC.t('Add proxy'), function(){ keep(); editProfile(null); }));
				for(i = 0; i < l.length; i++) items.appendChild(profileRow(l[i]));
				clear(wrap).appendChild(sec('link', DC.t('Proxy server'), DC.t('Downloads connect through a proxy, so the other side sees the proxy\'s IP. When adding a URL task you can pick which proxy to use, or let it be chosen by site; torrents share the one set below. A task given a proxy never falls back to a direct connection.'), [
					items,
					l.length ? DC.addRow(DC.t('Add proxy'), function(){ keep(); editProfile(null); }) : null,
					l.length ? field(DC.t('Default for URL downloads'), DC.t('Used when a new task chooses “Automatic” and no site rule matches'), DC.select('pxDef', choices(), val('url_default', p.url_default || '')), 'pxDef') : null,
					l.length ? toggle('pxReq', DC.t('Regular users must use a proxy'), DC.t('Regular users cannot choose “No proxy”; when no proxy is available, their tasks stop instead of connecting directly'), val('require_for_users', !!p.require_for_users)) : null,
					l.length ? field(DC.t('Torrents use'), DC.t('All torrents on the NAS share one'), DC.select('pxBt', choices(), val('bt', p.bt || ''), function(){ keep(); render(); }), 'pxBt') : null,
					bt ? toggle('pxTrk', DC.t('Torrent trackers'), null, val('apply_trackers', p.apply_trackers !== false)) : null,
					bt && bt.type === 'socks5' && c.socks5_peers ? toggle('pxPeers', DC.t('Torrent connections'), DC.t('Connections for exchanging data with peers'), val('apply_peers', p.apply_peers !== false)) : null,
					bt ? toggle('pxOnly', DC.t('Proxy only'), DC.t('When the proxy cannot be reached, torrents stop instead of connecting directly, and UPnP is not used'), val('force', p.force !== false)) : null,
					l.length ? field(DC.t('Notifications and webhooks use'), null, DC.select('pxNotify', choices(), val('notify_profile', p.notify_profile || '')), 'pxNotify') : null
				]));
			}
			function profileRow(pr){
				var tags = [];
				if(pr.id === val('url_default', p.url_default)) tags.push(DC.t('URL default'));
				if(pr.id === val('bt', p.bt)) tags.push(DC.t('Torrent'));
				if(pr.for_users) tags.push(DC.t('Users can choose'));
				var pills = h('div', {'class':'pills'}), k;
				for(k = 0; k < tags.length; k++) pills.appendChild(h('span', {text:tags[k]}));
				return h('div', {'class':'lrow'}, [icon('link'),
					h('div', null, [h('b', {text:pr.name}), h('small', {'class':'mono', text:(pr.type === 'socks5' ? 'SOCKS5 ' : 'HTTP ') + pr.host + ':' + pr.port}), tags.length ? pills : null,
						pr.sites ? h('small', {text:DC.t('Used automatically for: {sites}', {sites:pr.sites})}) : null]),
					h('div', {'class':'acts2'}, [
					ibtn('edit', DC.t('Edit'), function(){ keep(); editProfile(pr); }),
					ibtn('trash', DC.t('Delete'), function(){
						keep();
						DC.confirm(DC.t('Delete proxy “{name}”?', {name:pr.name}), 'trash', DC.t('URL tasks using this proxy will stop until another proxy is chosen; they will not switch to a direct connection.'), DC.t('Delete'), function(){
							saveProfiles(list().filter(function(x){ return x.id !== pr.id; }), null, function(){ DC.toast(DC.t('Proxy deleted')); });
						}, true);
					})])
				]);
			}
			/* Profiles are stored at once (the passwords never come back from the server). */
			function saveProfiles(next, passwords, done){
				var body = {proxy:{profiles:next}};
				if(passwords) body.proxy_passwords = passwords;
				return DC.api.put('settings', body).then(function(res){
					p = (res.settings || {}).proxy || {};
					if(!byId(mem.url_default)) mem.url_default = p.url_default || '';
					if(!byId(mem.bt)) mem.bt = p.bt || '';
					if(!byId(mem.notify_profile)) mem.notify_profile = p.notify_profile || '';
					render();
					if(done) done();
				}, function(e){ DC.toast(DC.errText(e)); throw e; });
			}
			function editProfile(pr){
				var isNew = !pr, res = h('span', {'class':'unit', role:'status'});
				pr = pr || {id:'px' + Math.random().toString(36).slice(2, 10), name:'', type:'socks5', host:'', port:1080, user:'', remote_dns:true, sites:'', no_proxy:'localhost,127.0.0.0/8,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,169.254.0.0/16', for_users:false};
				var dnsRow = toggle('ppDns', DC.t('Resolve domain names through the proxy'), DC.t('Prevents the NAS\'s DNS lookups from revealing the sites you download from'), pr.remote_dns !== false);
				var type = DC.select('ppType', [['socks5', 'SOCKS5'], ['http', 'HTTP']], pr.type || 'socks5', function(){
					dnsRow.hidden = this.value !== 'socks5';
					var port = document.getElementById('ppPort');
					if(port && (port.value === '1080' || port.value === '3128')) port.value = this.value === 'socks5' ? '1080' : '3128';
				});
				dnsRow.hidden = type.value !== 'socks5';
				var form = DC.mform([
					field(DC.t('Name'), DC.t('Name shown when adding tasks'), h('input', {type:'text', id:'ppName', value:pr.name, maxlength:'40'}), 'ppName'),
					field(DC.t('Type'), null, type, 'ppType'),
					fieldDiv(DC.t('Server'), DC.t('Host name or IP, and port'), h('span', {'class':'hostport'}, [h('input', {type:'text', id:'ppHost', value:pr.host, placeholder:'vpn.example.net', autocapitalize:'off', spellcheck:'false', 'aria-label':DC.t('Server')}),
						h('span', {'class':'unit', 'aria-hidden':'true', text:':'}), h('input', {type:'number', id:'ppPort', value:String(pr.port || 1080), inputmode:'numeric', min:'1', max:'65535', 'aria-label':DC.t('Port')})])),
					field(DC.t('Account'), DC.t('Optional'), h('input', {type:'text', id:'ppUser', value:pr.user || '', autocomplete:'off', autocapitalize:'off'}), 'ppUser'),
					field(DC.t('Password'), pr.has_password ? DC.t('Saved; leave empty to keep it') : DC.t('Not shown again after saving'), h('input', {type:'password', id:'ppPass', autocomplete:'new-password'}), 'ppPass'),
					dnsRow,
					field(DC.t('Use automatically for sites'), DC.t('Comma-separated, e.g. example.com; tasks set to “Automatic” use this proxy for these sites'), h('input', {type:'text', id:'ppSites', value:pr.sites || '', autocapitalize:'off', spellcheck:'false'}), 'ppSites'),
					fieldDiv(DC.t('Bypass proxy for'), DC.t('Comma-separated hosts or networks that are reached directly'), h('textarea', {id:'ppSkip', rows:'3', 'class':'secretarea', value:pr.no_proxy || '', autocapitalize:'off', spellcheck:'false', 'aria-label':DC.t('Bypass proxy for')})),
					toggle('ppUsers', DC.t('Regular users can choose it'), DC.t('When off, only administrators can choose it when adding tasks'), !!pr.for_users),
					h('div', {'class':'inline'}, [btn('link', DC.t('Test proxy'), function(e){
						var b = e.currentTarget, body = {profile:pr.id, type:type.value, host:DC.val('ppHost'), port:DC.ival('ppPort'), user:DC.val('ppUser'), password:DC.val('ppPass')};
						if(!body.host){ res.textContent = DC.t('Enter the server.'); return; }
						DC.busy(b, true); res.textContent = DC.t('Testing…');
						DC.api.post('settings/proxy-test', body).then(function(x){
							DC.busy(b, false);
							if(!x.ok){ res.textContent = DC.t('Cannot connect: {error}', {error:x.error || ''}); return; }
							res.textContent = x.udp === false ? DC.t('Connected. The public IP through the proxy is {ip}. This proxy does not relay UDP, so DHT and UDP trackers are disabled for torrents.', {ip:x.ip || '?'}) : DC.t('Connected. The public IP through the proxy is {ip}.', {ip:x.ip || '?'});
						}, function(err){ DC.busy(b, false); res.textContent = DC.errText(err); });
					}), res])
				]);
				DC.modal(isNew ? DC.t('Add proxy') : DC.t('Edit proxy'), 'link', [form], function(close){
					var ok = btn(null, DC.t('Save'), function(){
						var next = {id:pr.id, name:DC.val('ppName'), type:type.value, host:DC.val('ppHost'), port:DC.ival('ppPort'), user:DC.val('ppUser'),
							remote_dns:DC.chk('ppDns'), sites:DC.val('ppSites'), no_proxy:DC.val('ppSkip'), for_users:DC.chk('ppUsers')}, pw = DC.val('ppPass'), l, i, pws = null;
						if(!next.host){ DC.toast(DC.t('Enter the proxy server')); return; }
						if(next.port < 1 || next.port > 65535){ DC.toast(DC.t('Invalid proxy server port')); return; }
						if(!next.name) next.name = next.host;
						l = list().slice();
						for(i = 0; i < l.length; i++) if(l[i].id === next.id) break;
						l[i] = next;
						if(pw){ pws = {}; pws[next.id] = pw; }
						DC.busy(ok, true, DC.t('Saving…'));
						saveProfiles(l, pws, function(){ close(); DC.toast(DC.t('Proxy saved')); }).then(null, function(){ DC.busy(ok, false); });
					}, 'pri');
					return [btn(null, DC.t('Cancel'), close), ok];
				});
			}
			pxState.collect = function(){
				keep();
				return {url_default:val('url_default', p.url_default || ''), require_for_users:val('require_for_users', !!p.require_for_users), bt:val('bt', p.bt || ''),
					apply_trackers:val('apply_trackers', p.apply_trackers !== false), apply_peers:val('apply_peers', p.apply_peers !== false), force:val('force', p.force !== false),
					notify_profile:val('notify_profile', p.notify_profile || '')};
			};
			render();
			return wrap;
		}
	}

	/* Download Station compatibility (Qget, Qfile, browser extensions): only when the official package is gone. */
	function v4Sec(){
		var box = h('div');
		DC.api.get('v4', null, {quiet:true}).then(function(r){
			var on = !!r.linked, can = !!r.can_link || on, note;
			if(r.official_installed && r.official_enabled) note = DC.t('The official Download Station is still in use. Disable or remove it before Download Center can take over /downloadstation.');
			else if(r.official_installed) note = DC.t('The official Download Station is disabled. When enabled, Qget, Qfile and browser extensions connect to Download Center instead; re-enabling the official version switches them back automatically.');
			else note = DC.t('When enabled, Qget, Qfile and browser extensions can connect to Download Center with their existing settings.');
			box.appendChild(sec('plug', DC.t('Download Station compatible (Qget/Qfile)'), null, [
				toggle('v4On', DC.t('Take over /downloadstation'), note, on, function(){
					var el = this, want = el.checked;
					el.disabled = true;
					DC.api.post('v4', {enable:want}).then(function(x){ el.disabled = false; el.checked = !!x.linked; DC.toast(x.linked ? DC.t('Took over /downloadstation') : DC.t('No longer taking over /downloadstation')); }, function(e){ el.disabled = false; el.checked = !want; DC.toast(DC.errText(e)); });
				}),
				r.path ? h('p', {'class':'note mono', text:r.path}) : null
			]));
			var cb = document.getElementById('v4On');
			if(cb && !can) cb.disabled = true;
		}, function(){});
		return box;
	}

	/* ---------- Schedule (7 x 24, Monday first; '1' full speed, '2' limited, '0' paused) ---------- */
	var DAYS = [DC.t('Mon'), DC.t('Tue'), DC.t('Wed'), DC.t('Thu'), DC.t('Fri'), DC.t('Sat'), DC.t('Sun')];
	var MODE = {'1':DC.t('Full speed'), '2':DC.t('Limited speed'), '0':DC.t('Pause')};
	function schedSummary(sched){
		var txt = [], lines = [], d, segs, parts, j, start = 0, prev = null, row, cur, i, n;
		for(d = 0; d < 7; d++){
			row = sched[d]; segs = []; cur = {m:row[0], a:0};
			for(i = 1; i <= 24; i++) if(i === 24 || row[i] !== cur.m){ segs.push({m:cur.m, a:cur.a, b:i}); if(i < 24) cur = {m:row[i], a:i}; }
			if(segs.length === 1) txt.push(DC.t('{mode} all day', {mode:MODE[segs[0].m]}));
			else{
				parts = [];
				for(j = 0; j < segs.length; j++) parts.push(DC.t('{from}–{to} {mode}', {from:DC.pad(segs[j].a) + ':00', to:DC.pad(segs[j].b) + ':00', mode:MODE[segs[j].m]}));
				txt.push(parts.join(DC.t('; ')));
			}
		}
		for(d = 0; d <= 7; d++){
			if(d === 7 || (prev !== null && txt[d] !== prev)){
				n = d - start;
				lines.push(DC.t('{days}: {plan}', {days:n === 1 ? DAYS[start] : (n === 2 ? DC.t('{a}, {b}', {a:DAYS[start], b:DAYS[d - 1]}) : DC.t('{a}–{b}', {a:DAYS[start], b:DAYS[d - 1]})), plan:prev}));
				start = d;
			}
			if(d < 7) prev = txt[d];
		}
		return lines;
	}
	DC.schedSummary = schedSummary;
	function setSchedule(body){
		loadingInto(body);
		Promise.all([DC.api.get('schedule'), DC.api.get('settings', null, {quiet:true}).then(null, function(){ return null; })]).then(function(rs){ clear(body); scheduleForm(body, rs[0], rs[1] && rs[1].settings); }, function(e){ errorInto(body, e); });
	}
	/* The speeds of the Limited speed periods, as set on the Downloads page (KB/s, 0 = no limit) */
	function limitText(st){
		function v(k, f){ var x = st[k] && st[k][f]; return x > 0 ? DC.fspeed(x * 1024) : DC.t('No limit'); }
		return DC.t('“Limited speed” periods: torrent download {bd}, upload {bu}; URL {http}; FTP {ftp}.', {bd:v('bt', 'limited_down'), bu:v('bt', 'limited_up'), http:v('http', 'limited_down'), ftp:v('ftp', 'limited_down')});
	}
	function scheduleForm(body, r, st){
		var sched = [], d, bar, ed, on = !!r.enabled;
		for(d = 0; d < 7; d++) sched.push((r.days && r.days[d] || '111111111111111111111111').split(''));
		bar = saveBar(function(){
			var days = [], k;
			for(k = 0; k < 7; k++) days.push(sched[k].join(''));
			DC.busy(bar.save, true);
			DC.api.put('schedule', {enabled:DC.chk('scOn'), days:days}).then(function(){ DC.busy(bar.save, false); bar.clean(); DC.toast(DC.t('Schedule saved')); DC.pollStats(); }, function(e){ DC.busy(bar.save, false); DC.toast(DC.errText(e)); });
		}, function(){ scheduleForm(clear(body), r, st); });
		ed = schedEditor(sched, function(){ bar.mark(); });
		ed.classList.toggle('off', !on);
		body.appendChild(sec('cal', DC.t('Weekly schedule'), DC.t('Pick a pen, then press and drag to paint. Click a weekday to paint the whole day, or a time to paint that hour for the whole week.'), [
			toggle('scOn', DC.t('Control speed by schedule'), DC.t('When off, always full speed; the schedule below has no effect'), on, function(){ ed.classList.toggle('off', !this.checked); bar.mark(); }),
			ed,
			st ? h('p', {'class':'note'}, [limitText(st) + ' ', h('button', {'class':'ib linkish', type:'button', onclick:function(){ DC.leave(function(){ DC.S.setTab = 'dl'; DC.renderView(); window.scrollTo(0, 0); }); }}, DC.t('Change limits'))]) : null,
			bar.el
		]));
	}
	function schedEditor(sched, onEdit){
		var wrap = h('div', {'class':'sched-ed'}), grid = h('div', {'class':'sgrid', role:'grid', 'aria-label':DC.t('Weekly schedule')}), sum = h('div', {'class':'ssum'}), cells = [], painting = false, brush = '1', d, hr, c;
		var brushes = h('div', {'class':'brushes'});
		[['1', DC.t('Full speed')], ['2', DC.t('Limited speed')], ['0', DC.t('Pause')]].forEach(function(b){
			brushes.appendChild(h('button', {'class':'brush' + (brush === b[0] ? ' on' : ''), type:'button', 'aria-pressed':brush === b[0] ? 'true' : 'false', onclick:function(){
				brush = b[0];
				var all = brushes.children, i; for(i = 0; i < all.length; i++){ all[i].className = 'brush'; all[i].setAttribute('aria-pressed', 'false'); }
				this.className = 'brush on'; this.setAttribute('aria-pressed', 'true');
			}}, [h('i', {'class':'m' + b[0]}), b[1]]));
		});
		function paint(dd, hh){ sched[dd][hh] = brush; cells[dd][hh].className = 'c m' + brush; cells[dd][hh].title = DAYS[dd] + ' ' + DC.pad(hh) + ':00 ' + MODE[brush]; if(onEdit) onEdit(); }
		function summary(){ clear(sum); var l = schedSummary(sched), i; for(i = 0; i < l.length; i++) sum.appendChild(h('div', {text:l[i]})); }
		grid.appendChild(h('span'));
		for(hr = 0; hr < 24; hr++) grid.appendChild(h('button', {'class':'hh num', type:'button', title:DC.t('{time} all week', {time:DC.pad(hr) + ':00'}), onclick:(function(hh){ return function(){ for(var k = 0; k < 7; k++) paint(k, hh); summary(); }; })(hr)}, hr % 3 === 0 ? DC.pad(hr) : ''));
		for(d = 0; d < 7; d++){
			cells.push([]);
			grid.appendChild(h('button', {'class':'dh', type:'button', title:DC.t('{day} all day', {day:DAYS[d]}), onclick:(function(dd){ return function(){ for(var k = 0; k < 24; k++) paint(dd, k); summary(); }; })(d)}, DAYS[d]));
			for(hr = 0; hr < 24; hr++){
				c = h('button', {'class':'c m' + sched[d][hr], type:'button', title:DAYS[d] + ' ' + DC.pad(hr) + ':00 ' + MODE[sched[d][hr]], 'aria-label':DAYS[d] + ' ' + DC.pad(hr) + ':00'});
				(function(dd, hh, el){
					el.addEventListener('pointerdown', function(e){ e.preventDefault(); painting = true; paint(dd, hh); });
					el.addEventListener('pointerenter', function(){ if(painting) paint(dd, hh); });
					el.addEventListener('keydown', function(e){ if(e.key === 'Enter' || e.key === ' '){ e.preventDefault(); paint(dd, hh); summary(); } });
				})(d, hr, c);
				cells[d].push(c);
				grid.appendChild(c);
			}
		}
		grid.addEventListener('pointermove', function(e){
			if(!painting || e.pointerType === 'mouse') return;
			var el = document.elementFromPoint(e.clientX, e.clientY);
			if(el && el.parentNode === grid && el.className.indexOf('c ') === 0){ var ev = document.createEvent('Event'); ev.initEvent('pointerenter', false, false); el.dispatchEvent(ev); }
		});
		R.paintEnd = function(){ if(painting){ painting = false; summary(); } };
		add(wrap, [brushes, h('div', {'class':'gridwrap'}, grid), sum]);
		summary();
		return wrap;
	}

	/* ---------- Users: who may use Download Center is set in QTS (application privilege); this page lists them ---------- */
	function setUsers(body){
		loadingInto(body);
		DC.api.get('users').then(function(r){ clear(body); usersForm(body, r); }, function(e){ errorInto(body, e); });
	}
	/* Inside the QTS desktop its Control Panel opens on Users; in a tab of its own the QTS desktop does, after signing in if
	   needed. QTS has no link to one account's application privileges, so Users is as far as it goes. */
	function openQtsUsers(){
		try{ if(DC.embedded && window.parent.os && window.parent.os.openApp){ window.parent.os.openApp('users'); return; } }catch(e){}
		window.open('/cgi-bin/main.html?a=users', '_blank', 'noopener');
	}
	function usersForm(body, r){
		var a = r.access || {}, members = (a.members || []).slice(), list = h('div'), i;
		members.sort(function(x, y){ return x.admin !== y.admin ? (x.admin ? -1 : 1) : (x.name < y.name ? -1 : x.name > y.name ? 1 : 0); });
		for(i = 0; i < members.length; i++){
			(function(u){
				list.appendChild(h('div', {'class':'lrow'}, [icon('user'), h('div', null, [h('b', {text:u.name}),
					h('div', {'class':'pills'}, [h('span', {text:u.admin ? DC.t('Administrator') : DC.t('Regular user')})]),
					h('small', {text:(u.admin ? DC.t('Sees and manages all downloads and settings') : DC.t('Sees only their own downloads; files are saved to home/Download')) +
						(u.last_login_at ? DC.t('. Last signed in {time}', {time:DC.ftime(u.last_login_at)}) : '')})])]));
			})(members[i]);
		}
		if(!members.length) list.appendChild(h('p', {'class':'note', text:DC.t('No account can use it yet.')}));
		body.appendChild(sec('user', DC.t('Users'), DC.t('Who can use Download Center is set in QTS: Control Panel › Privilege › Users, then tick Download Center under an account\'s “Edit Application Privilege”. QTS administrators can always use it and are its administrators here.'), [
			!a.available ? h('p', {'class':'note warn', text:DC.t('QTS on this NAS has no application privileges, so only QTS administrators can use Download Center.')}) :
				!a.registered ? h('p', {'class':'note warn', text:DC.t('Download Center is not registered with QTS application privileges yet; this happens automatically within a minute. Until then only administrators can use it.')}) : null,
			a.available ? DC.fieldDiv(DC.t('Permission'), DC.t('Allow or remove accounts in QTS, then come back here and refresh.'),
				[btn('popout', DC.t('Set in QTS'), openQtsUsers, 'pri'), ibtn('retry', DC.t('Refresh'), function(){ clear(body); setUsers(body); })]) : null,
			h('div', {'class':'sphead', text:DC.t('Accounts that can use it ({n})', {n:members.length})}),
			list,
			a.groups && a.groups.length ? h('p', {'class':'note', text:DC.t('QTS also allows the members of these groups: {groups}', {groups:a.groups.join(DC.t(', '))})}) : null,
			r.homes_enabled ? null : h('p', {'class':'note warn', text:DC.t('The QTS home folder service is not enabled: files of regular users are saved to Public.')})
		]));
	}

	DC.settings = {view:view};
})();
