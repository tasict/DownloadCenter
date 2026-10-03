/* Settings: tabs on wide screens, an iOS-style section index on phones. Download, schedule and users live here;
   accounts, tokens, notifications and import in settings-more.js. */
(function(){
	'use strict';
	var DC = window.DC, h = DC.h, add = DC.add, clear = DC.clear, icon = DC.icon, ibtn = DC.ibtn, btn = DC.btn, R = DC.R;
	var field = DC.field, fieldDiv = DC.fieldDiv, toggle = DC.toggle, num = DC.num, sec = DC.sec;
	var SET_INFO = {
		dl:['down', DC.t('資料夾、同時下載數、速度、種子')], sched:['cal', DC.t('什麼時候全速、限速或暫停')], users:['user', DC.t('誰可以使用 Download Center')],
		acct:['key', DC.t('需要登入的網站與免空帳號')], token:['ticket', DC.t('讓其他程式使用你的下載')], notify:['bell', DC.t('Telegram、Discord、Webhook')], import:['inbox', DC.t('設定、任務與網站帳號')], about:['retry', DC.t('版本、更新與舊版本')]
	};
	var importAvail = null;
	/* After a successful import the section disappears (kept on screen until the next navigation, so the summary stays readable). */
	DC.importDone = function(){ importAvail = false; };

	function tabs(){
		var admin = DC.isAdmin(), t;
		t = admin ? [['dl', DC.t('下載')], ['sched', DC.t('排程')], ['users', DC.t('使用者')], ['acct', DC.t('網站帳號')], ['token', DC.t('存取權杖')], ['notify', DC.t('通知與整合')]]
			: [['dl', DC.t('下載')], ['acct', DC.t('網站帳號')], ['token', DC.t('存取權杖')], ['notify', DC.t('通知與整合')]];
		if(admin && importAvail) t.push(['import', DC.t('從官方版匯入')]);
		if(admin && DC.S.me.via === 'session') t.push(['about', DC.t('關於與更新')]);
		if(DC.S.me.via === 'token') t = [['dl', DC.t('下載')]];
		return t;
	}
	function view(main){
		var S = DC.S, admin = DC.isAdmin(), list, tb, body, i, ok = false, label = '', ts;
		if(admin && importAvail === null){
			importAvail = false;
			DC.api.get('import', null, {quiet:true}).then(function(r){ importAvail = !!(r && r.available); if(importAvail && S.view === 'settings' && !(S.dirty && S.dirty())) DC.renderView(); }, function(){ importAvail = false; });
		}
		S.dirty = null;
		ts = tabs();
		for(i = 0; i < ts.length; i++) if(ts[i][0] === S.setTab){ ok = true; label = ts[i][1]; }
		if(!ok){ S.setTab = 'dl'; label = ts[0][1]; }
		/* Phone: seven tabs do not fit, so settings open on an index like the iOS Settings app and each section is its own page. */
		if(DC.phone() && !S.setOpen){
			list = h('div', {'class':'group setidx'});
			for(i = 0; i < ts.length; i++){
				list.appendChild(h('button', {'class':'ib srow t' + (i % 7), type:'button', onclick:(function(id){ return function(){ S.setTab = id; S.setOpen = true; DC.track('set_' + id); DC.renderView(); window.scrollTo(0, 0); }; })(ts[i][0])},
					[icon(SET_INFO[ts[i][0]][0]), h('span', null, [h('b', {text:ts[i][1]}), h('small', {text:SET_INFO[ts[i][0]][1]})]), icon('chev', 'chev')]));
			}
			add(main, [h('div', {'class':'lhead'}, [h('h1', {text:DC.t('設定')}), admin ? null : h('span', {'class':'sub', text:DC.t('只會影響你自己的下載')})]), list, foot()]);
			return;
		}
		tb = h('div', {'class':'tabs', role:'tablist'});
		for(i = 0; i < ts.length; i++){
			tb.appendChild(h('button', {'class':'tab' + (S.setTab === ts[i][0] ? ' on' : ''), type:'button', role:'tab', 'aria-selected':S.setTab === ts[i][0] ? 'true' : 'false',
				onclick:(function(id){ return function(){ if(S.setTab !== id) DC.leave(function(){ S.setTab = id; DC.track('set_' + id); DC.renderView(); }); }; })(ts[i][0])}, ts[i][1]));
		}
		body = h('div');
		if(S.setTab === 'dl') (admin ? setDownload : setDownloadUser)(body);
		else if(S.setTab === 'sched') setSchedule(body);
		else if(S.setTab === 'users') setUsers(body);
		else if(S.setTab === 'acct') DC.setMore.accounts(body);
		else if(S.setTab === 'token') DC.setMore.tokens(body);
		else if(S.setTab === 'notify') DC.setMore.notify(body);
		else if(S.setTab === 'about') DC.update.settings(body);
		else DC.setMore.importer(body);
		add(main, [
			DC.phone()
				? h('div', {'class':'lhead sub-page'}, [h('button', {'class':'ib linkish back', type:'button', onclick:function(){ DC.leave(function(){ S.setOpen = false; DC.renderView(); window.scrollTo(0, 0); }); }}, [icon('back'), DC.t('設定')]), h('h1', {text:label})])
				: h('div', {'class':'lhead'}, [h('h1', {text:DC.t('設定')}), admin ? null : h('span', {'class':'sub', text:DC.t('只會影響你自己的下載')})]),
			h('div', {'class':'set'}, [tb, body]), foot()]);
	}
	function foot(){
		var v = DC.S.me && DC.S.me.nas && DC.S.me.nas.version;
		return h('footer', {'class':'foot'}, [h('span', {'class':'num', text:'Download Center' + (v ? ' ' + v : '')}),
			h('button', {type:'button', onclick:function(){ DC.showNotice(false); }}, DC.t('使用聲明')),
			h('a', {href:'docs/third-party-notices.txt', target:'_blank', rel:'noopener'}, DC.t('授權資訊'))].concat(DC.supportLinks()));
	}
	function loadingInto(body){ body.appendChild(h('div', {'class':'loading'}, [icon('check'), DC.t('讀取中…')])); }
	function errorInto(body, e){ clear(body); body.appendChild(h('p', {'class':'note warn', text:DC.errText(e)})); }
	DC.loadingInto = loadingInto; DC.errorInto = errorInto;
	/* Save bar of a settings page: it sticks to the bottom of the window and says so while something is changed, and the page
	   asks before it is left with unsaved changes (DC.leave). */
	function saveBar(onSave, onRevert){
		var dirty = false, el;
		var sb = {save:btn(null, DC.t('儲存'), function(){ onSave(); }, 'pri')};
		el = sb.el = h('div', {'class':'savebar sticky'}, [h('span', {'class':'dirtynote', text:DC.t('有未儲存的變更')}),
			h('span', {'class':'dirtybtn'}, btn(null, DC.t('還原'), function(){ sb.clean(); onRevert(); })), sb.save]);
		sb.mark = function(){ if(dirty) return; dirty = true; el.classList.add('dirty'); };
		sb.clean = function(){ dirty = false; el.classList.remove('dirty'); };
		DC.S.dirty = function(){ return dirty && document.body.contains(el); };
		return sb;
	}

	/* ---------- 下載 (regular user) ---------- */
	function setDownloadUser(body){
		var me = DC.S.me, home = me.home_folder || 'home/Download';
		add(body, [
			sec('folder', DC.t('我的資料夾'), null, [h('p', {'class':'note', text:DC.t('下載的檔案都會存到你家目錄的 {home}，檔案擁有者是你。網址下載完成前會先放在其中的 @DownloadCenterTemp；種子直接下載到 {home} 並在那裡做種。', {home:home})})])
		]);
	}

	/* ---------- 下載 (administrator) ---------- */
	var SEED_TIMES = [[-1, DC.t('不做種')], [0, DC.t('不限時間（分享率設 0 則一直做種）')], [30, DC.t('30 分鐘')], [60, DC.t('1 小時')], [180, DC.t('3 小時')], [360, DC.t('6 小時')], [720, DC.t('12 小時')], [1440, DC.t('1 天')], [2880, DC.t('2 天')], [4320, DC.t('3 天')], [10080, DC.t('1 週')], [20160, DC.t('2 週')]];
	var PEER_MODES = [[1, DC.t('Download Center（預設）')], [2, 'Deluge 1.3.12'], [3, 'Transmission 2.94'], [4, 'uTorrent Mac 1.8.7'], [0, DC.t('自訂')]];
	/* A section whose intro has a short line and the rest behind 說明 */
	function secMore(iconName, title, intro, more, kids){
		var el = sec(iconName, title, null, kids);
		el.insertBefore(h('div', {'class':'secintro'}, [h('p', {text:intro}), h('details', null, [h('summary', {text:DC.t('詳細說明')}), h('p', {text:more})])]), el.children[1]);
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
		if(!known) seedTime.appendChild(h('option', {value:String(tor.seed_time), text:DC.t('{n} 分鐘', {n:tor.seed_time}), selected:true}));
		var peerCustom = h('div', {hidden:tor.peer_mode !== 0}, [
			field(DC.t('用戶端代號'), DC.t('兩個英文字母'), h('input', {type:'text', id:'bPeerId', value:tor.peer_id || '', maxlength:'2', size:'3'}), 'bPeerId'),
			field(DC.t('版本'), null, h('input', {type:'text', id:'bPeerVer', value:tor.peer_version || '', size:'8'}), 'bPeerVer'),
			field('User agent', null, h('input', {type:'text', id:'bPeerAgent', value:tor.peer_agent || '', maxlength:'64'}), 'bPeerAgent')]);
		var peerSel = DC.select('bPeerMode', PEER_MODES, String(tor.peer_mode === undefined ? 1 : tor.peer_mode), function(){ peerCustom.hidden = this.value !== '0'; });
		function kind(id, t, help){
			return [field(t, help || null, num(id + 'Max', s[id] ? s[id].max_num : 1, DC.t('個'), {min:'1', max:'50'}), id + 'Max')];
		}
		function sv(key){ var k = key.split('.'); return s[k[0]] ? s[k[0]][k[1]] || 0 : 0; }
		function spIn(id, key, label){ var v = sv(key); return h('input', {type:'number', id:id, value:v ? String(v) : '', min:'0', inputmode:'numeric', placeholder:DC.t('不限'), 'aria-label':label}); }
		function speedRow(label, a, ak, b, bk){
			return h('div', {'class':'sprow', role:'row'}, [h('span', {role:'rowheader', text:label}), spIn(a, ak, DC.t('{what}，平時', {what:label})), spIn(b, bk, DC.t('{what}，限速時段', {what:label}))]);
		}
		add(body, [
			secMore('folder', DC.t('資料夾'), DC.t('加入下載時預先選好的位置，仍可在對話框中更改。'), DC.t('網址下載先放在暫存位置所在共用資料夾的 @DownloadCenterTemp，完成後移到「完成後移至」，選「不移動」就移到暫存位置；種子直接下載到暫存位置並在那裡做種，做種結束才移。一般使用者固定存到各自家目錄的 home/Download。'), [
				fieldDiv(DC.t('預設暫存位置'), null, temp.el),
				fieldDiv(DC.t('預設完成後移至'), DC.t('種子在做種結束後才移'), move.el)
			]),
			sec('done', DC.t('完成的任務'), DC.t('只從清單移除，檔案會保留，紀錄留在歷史中。'), [
				field(DC.t('完成後'), DC.t('新任務的預設值，加入時可以更改'), DC.select('sAuto', [['', DC.t('保留在清單')], ['completed', DC.t('下載完成後移除')], ['seeded', DC.t('做種完成後移除（種子）')]], s.auto_remove || ''), 'sAuto'),
				field(DC.t('歷史紀錄保留'), null, num('sHist', s.history_days || 90, DC.t('天'), {min:'1'}), 'sHist')
			]),
			sec('all', DC.t('同時下載'), DC.t('超過的任務會排隊，有空位時依清單順序開始。'), [kind('bt', DC.t('種子'), eng ? null : DC.t('這台 NAS 無法下載種子（缺少 BT 引擎）')), kind('http', DC.t('網址'), ucaps.urls ? null : DC.t('這台 NAS 無法下載這種網址（下載元件 dc-dl 無法使用）')), kind('ftp', DC.t('FTP／SFTP'), ucaps.ftp ? null : DC.t('這台 NAS 無法下載這種網址（下載元件 dc-dl 無法使用）'))]),
			sec('gauge', DC.t('速度'), DC.t('單位是 KB/s，留空代表不限速。排程裡的「限速」時段使用右欄的數值。'), [
				h('div', {'class':'sptab', role:'table', 'aria-label':DC.t('速度上限')}, [
					h('div', {'class':'sprow sphead', role:'row'}, [h('span'), h('span', {role:'columnheader', text:DC.t('平時')}), h('span', {role:'columnheader', text:DC.t('限速時段')})]),
					speedRow(DC.t('種子下載'), 'bDn', 'bt.max_down', 'bLDn', 'bt.limited_down'),
					speedRow(DC.t('種子上傳'), 'bUp', 'bt.max_up', 'bLUp', 'bt.limited_up'),
					speedRow(DC.t('網址下載'), 'hDn', 'http.max_down', 'hLDn', 'http.limited_down'),
					speedRow(DC.t('FTP 下載'), 'fDn', 'ftp.max_down', 'fLDn', 'ftp.limited_down')])
			]),
			sec('torrent', DC.t('種子'), null, [
				fieldDiv(DC.t('連入埠'), caps.upnp ? null : DC.t('請在路由器把這些埠轉到 NAS'), [h('input', {type:'number', id:'pFrom', value:String(tor.lt_port_from), min:'1024', max:'65535', 'aria-label':DC.t('起始埠')}), '–', h('input', {type:'number', id:'pTo', value:String(tor.lt_port_to), min:'1024', max:'65535', 'aria-label':DC.t('結束埠')}),
					btn('link', DC.t('測試連入埠'), function(e){
						var b = e.currentTarget;
						/* Consent is asked for every test and never stored: the external service only learns the address for this one check */
						DC.confirm(DC.t('允許外部服務測試連入埠？'), 'link', DC.t('測試會把 NAS 的對外 IP 與目前儲存的連入埠告訴外部檢測服務（ifconfig.co），由它從網際網路連回來檢查。只在這次測試使用，測完就不再允許。'), DC.t('允許並測試'), function(){
						DC.busy(b, true); portRes.textContent = DC.t('測試中…');
						DC.api.post('settings/port-test', {consent:true}).then(function(res){
							DC.busy(b, false);
							var okP = [], noP = [], j, x;
							for(j = 0; j < (res.results || []).length; j++){ x = res.results[j]; if(x.reachable) okP.push(x.port); else noP.push(x.port); }
							portRes.textContent = DC.sentences(okP.length ? DC.t('{ports} 可以從網際網路連入。', {ports:okP.join(DC.t('、'))}) : '', noP.length ? (res.ip ? DC.t('{ports} 沒有開放，請在路由器把這些埠轉到 NAS（{ip}）。', {ports:noP.join(DC.t('、')), ip:res.ip}) : DC.t('{ports} 沒有開放，請在路由器把這些埠轉到 NAS。', {ports:noP.join(DC.t('、'))})) : '');
						}, function(err){ DC.busy(b, false); portRes.textContent = DC.errText(err); });
						});
					})]),
				h('div', {'class':'inline'}, [portRes]),
				caps.upnp ? toggle('bUpnp', DC.t('自動開放連入埠'), DC.t('用 UPnP／NAT-PMP 請路由器轉埠。開了「只經過代理」時不會使用'), !!tor.upnp) : null,
				toggle('bDht', 'DHT', DC.t('沒有 tracker 也能找到其他使用者'), !!tor.dht),
				toggle('bLsd', DC.t('區域網路探索'), DC.t('同一個區域網路裡互相分享'), !!tor.lsd),
				toggle('bPex', DC.t('交換使用者名單'), DC.t('向已連上的使用者打聽更多來源'), !!tor.pex),
				toggle('bEnc', DC.t('只用加密連線'), DC.t('只和支援加密的使用者傳輸，來源可能變少'), !!tor.encrypt),
				fieldDiv(DC.t('分享率與做種時間'), DC.t('達到分享率或做種時間其中一個就停止做種；分享率填 0 代表不看分享率'), [num('bRatio', tor.seed_ratio, DC.t('倍分享率'), {step:'0.1'}), h('span', {'class':'unit', text:DC.t('或')}), seedTime]),
				caps.global_conn_limit ? field(DC.t('全部連線上限'), null, num('bConn', tor.max_conn, DC.t('個')), 'bConn') : null,
				field(DC.t('每個種子的連線上限'), DC.t('0 代表不限'), num('bTConn', tor.torrent_max_conn, DC.t('個')), 'bTConn'),
				field(DC.t('每個種子的上傳上限'), DC.t('0 代表不限'), num('bTUp', tor.torrent_max_up, 'KB/s'), 'bTUp'),
				field(DC.t('用戶端身分'), DC.t('部分私人 tracker 只接受特定用戶端'), peerSel, 'bPeerMode'),
				peerCustom,
			]),
			proxySec(px, caps, ucaps),
			sec('inbox', DC.t('空間不足時'), null, [field(DC.t('剩餘空間低於'), DC.t('0 代表不檢查。低於時暫停下載到這個磁碟區的任務並通知'), num('sDisk', s.disk_low_mb ? Math.round(s.disk_low_mb / 1024) : 0, 'GB'), 'sDisk')]),
			v4Sec(),
			bar.el
		]);
		/* Every edit on the page marks it changed, except the parts that act on their own (the folder browser, the
		   Download Station switch) */
		function changed(e){ var tg = e && e.target; if(tg && (tg.id === 'v4On' || (tg.closest && tg.closest('.fpick')))) return; bar.mark(); }
		/* 還原 renders the form again into the same box: replace the listener rather than adding another */
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
			if(o.torrent.peer_mode === 0 && !/^[a-zA-Z~]{2}$/.test(o.torrent.peer_id || '')){ DC.toast(DC.t('用戶端代號要是兩個英文字母')); return; }
			/* Only the choices: profiles are saved from their own window */
			o.proxy = pxState.collect();
			DC.busy(saveB, true, DC.t('儲存中…'));
			DC.api.put('settings', o).then(function(res){
				DC.busy(saveB, false); DC.toast(DC.t('已儲存設定'));
				bar.clean(); r = res; DC.pollStats();
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
			function choices(){ var o = [['', DC.t('不使用代理')]], l = list(), i; for(i = 0; i < l.length; i++) o.push([l[i].id, l[i].name]); return o; }
			function val(k, d){ return mem.hasOwnProperty(k) ? mem[k] : d; }
			function keep(){
				['pxDef:url_default', 'pxBt:bt', 'pxNotify:notify_profile'].forEach(function(x){ var k = x.split(':'); if(document.getElementById(k[0])) mem[k[1]] = DC.val(k[0]); });
				['pxReq:require_for_users', 'pxTrk:apply_trackers', 'pxPeers:apply_peers', 'pxOnly:force'].forEach(function(x){ var k = x.split(':'); if(document.getElementById(k[0])) mem[k[1]] = DC.chk(k[0]); });
			}
			function render(){
				var items = h('div'), l = list(), i, bt = byId(val('bt', p.bt || ''));
				if(!l.length) items.appendChild(DC.emptyAdd('link', DC.t('還沒有代理設定。'), DC.t('新增代理'), function(){ keep(); editProfile(null); }));
				for(i = 0; i < l.length; i++) items.appendChild(profileRow(l[i]));
				clear(wrap).appendChild(sec('link', DC.t('代理伺服器'), DC.t('下載經過代理連線，對方看到的是代理的 IP。新增網址任務時可以選用哪一組，或依網站自動選；種子共用下面指定的一組。指定了代理就不會改走直接連線。'), [
					items,
					l.length ? DC.addRow(DC.t('新增代理'), function(){ keep(); editProfile(null); }) : null,
					l.length ? field(DC.t('網址下載預設'), DC.t('新增任務選「自動」且沒有網站規則符合時使用'), DC.select('pxDef', choices(), val('url_default', p.url_default || '')), 'pxDef') : null,
					l.length ? toggle('pxReq', DC.t('一般使用者必須使用代理'), DC.t('一般使用者不能選「不使用代理」；沒有可用的代理時任務會停住，不會直接連線'), val('require_for_users', !!p.require_for_users)) : null,
					l.length ? field(DC.t('種子使用'), DC.t('整台 NAS 的種子共用一組'), DC.select('pxBt', choices(), val('bt', p.bt || ''), function(){ keep(); render(); }), 'pxBt') : null,
					bt ? toggle('pxTrk', DC.t('種子 tracker'), null, val('apply_trackers', p.apply_trackers !== false)) : null,
					bt && bt.type === 'socks5' && c.socks5_peers ? toggle('pxPeers', DC.t('種子連線'), DC.t('和其他使用者傳資料的連線'), val('apply_peers', p.apply_peers !== false)) : null,
					bt ? toggle('pxOnly', DC.t('只經過代理'), DC.t('代理連不上時種子停住，不會改用直接連線，也不使用 UPnP'), val('force', p.force !== false)) : null,
					l.length ? field(DC.t('通知與 Webhook 使用'), null, DC.select('pxNotify', choices(), val('notify_profile', p.notify_profile || '')), 'pxNotify') : null
				]));
			}
			function profileRow(pr){
				var tags = [];
				if(pr.id === val('url_default', p.url_default)) tags.push(DC.t('網址預設'));
				if(pr.id === val('bt', p.bt)) tags.push(DC.t('種子'));
				if(pr.for_users) tags.push(DC.t('使用者可選'));
				var pills = h('div', {'class':'pills'}), k;
				for(k = 0; k < tags.length; k++) pills.appendChild(h('span', {text:tags[k]}));
				return h('div', {'class':'lrow'}, [icon('link'),
					h('div', null, [h('b', {text:pr.name}), h('small', {'class':'mono', text:(pr.type === 'socks5' ? 'SOCKS5 ' : 'HTTP ') + pr.host + ':' + pr.port}), tags.length ? pills : null,
						pr.sites ? h('small', {text:DC.t('自動套用：{sites}', {sites:pr.sites})}) : null]),
					h('div', {'class':'acts2'}, [
					ibtn('edit', DC.t('編輯'), function(){ keep(); editProfile(pr); }),
					ibtn('trash', DC.t('刪除'), function(){
						keep();
						DC.confirm(DC.t('刪除代理「{name}」？', {name:pr.name}), 'trash', DC.t('使用這組代理的網址任務會停住，直到改選其他代理；不會改走直接連線。'), DC.t('刪除'), function(){
							saveProfiles(list().filter(function(x){ return x.id !== pr.id; }), null, function(){ DC.toast(DC.t('已刪除代理')); });
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
				var dnsRow = toggle('ppDns', DC.t('由代理解析網域名稱'), DC.t('避免 NAS 的 DNS 查詢洩漏要下載的網站'), pr.remote_dns !== false);
				var type = DC.select('ppType', [['socks5', 'SOCKS5'], ['http', 'HTTP']], pr.type || 'socks5', function(){
					dnsRow.hidden = this.value !== 'socks5';
					var port = document.getElementById('ppPort');
					if(port && (port.value === '1080' || port.value === '3128')) port.value = this.value === 'socks5' ? '1080' : '3128';
				});
				dnsRow.hidden = type.value !== 'socks5';
				var form = DC.mform([
					field(DC.t('名稱'), DC.t('新增任務時顯示的名稱'), h('input', {type:'text', id:'ppName', value:pr.name, maxlength:'40'}), 'ppName'),
					field(DC.t('類型'), null, type, 'ppType'),
					fieldDiv(DC.t('伺服器'), DC.t('主機名稱或 IP，以及埠'), h('span', {'class':'hostport'}, [h('input', {type:'text', id:'ppHost', value:pr.host, placeholder:'vpn.example.net', autocapitalize:'off', spellcheck:'false', 'aria-label':DC.t('伺服器')}),
						h('span', {'class':'unit', 'aria-hidden':'true', text:':'}), h('input', {type:'number', id:'ppPort', value:String(pr.port || 1080), inputmode:'numeric', min:'1', max:'65535', 'aria-label':DC.t('埠')})])),
					field(DC.t('帳號'), DC.t('選填'), h('input', {type:'text', id:'ppUser', value:pr.user || '', autocomplete:'off', autocapitalize:'off'}), 'ppUser'),
					field(DC.t('密碼'), pr.has_password ? DC.t('已儲存；留空表示不變') : DC.t('儲存後不會再顯示'), h('input', {type:'password', id:'ppPass', autocomplete:'new-password'}), 'ppPass'),
					dnsRow,
					field(DC.t('自動套用的網站'), DC.t('逗號分隔，例如 example.com；任務選「自動」時，這些網站用這組代理'), h('input', {type:'text', id:'ppSites', value:pr.sites || '', autocapitalize:'off', spellcheck:'false'}), 'ppSites'),
					fieldDiv(DC.t('不經代理的位址'), DC.t('逗號分隔，這些主機或網段直接連線'), h('textarea', {id:'ppSkip', rows:'3', 'class':'secretarea', value:pr.no_proxy || '', autocapitalize:'off', spellcheck:'false', 'aria-label':DC.t('不經代理的位址')})),
					toggle('ppUsers', DC.t('一般使用者可以選'), DC.t('關閉時只有系統管理者能在新增任務時選這組'), !!pr.for_users),
					h('div', {'class':'inline'}, [btn('link', DC.t('測試代理'), function(e){
						var b = e.currentTarget, body = {profile:pr.id, type:type.value, host:DC.val('ppHost'), port:DC.ival('ppPort'), user:DC.val('ppUser'), password:DC.val('ppPass')};
						if(!body.host){ res.textContent = DC.t('請填伺服器。'); return; }
						DC.busy(b, true); res.textContent = DC.t('測試中…');
						DC.api.post('settings/proxy-test', body).then(function(x){
							DC.busy(b, false);
							if(!x.ok){ res.textContent = DC.t('連不上：{error}', {error:x.error || ''}); return; }
							res.textContent = x.udp === false ? DC.t('連得上。經過代理的對外 IP 是 {ip}。這個代理不轉送 UDP，種子的 DHT 與 UDP tracker 會停用。', {ip:x.ip || '?'}) : DC.t('連得上。經過代理的對外 IP 是 {ip}。', {ip:x.ip || '?'});
						}, function(err){ DC.busy(b, false); res.textContent = DC.errText(err); });
					}), res])
				]);
				DC.modal(isNew ? DC.t('新增代理') : DC.t('編輯代理'), 'link', [form], function(close){
					var ok = btn(null, DC.t('儲存'), function(){
						var next = {id:pr.id, name:DC.val('ppName'), type:type.value, host:DC.val('ppHost'), port:DC.ival('ppPort'), user:DC.val('ppUser'),
							remote_dns:DC.chk('ppDns'), sites:DC.val('ppSites'), no_proxy:DC.val('ppSkip'), for_users:DC.chk('ppUsers')}, pw = DC.val('ppPass'), l, i, pws = null;
						if(!next.host){ DC.toast(DC.t('請填代理伺服器')); return; }
						if(next.port < 1 || next.port > 65535){ DC.toast(DC.t('代理伺服器的埠不正確')); return; }
						if(!next.name) next.name = next.host;
						l = list().slice();
						for(i = 0; i < l.length; i++) if(l[i].id === next.id) break;
						l[i] = next;
						if(pw){ pws = {}; pws[next.id] = pw; }
						DC.busy(ok, true, DC.t('儲存中…'));
						saveProfiles(l, pws, function(){ close(); DC.toast(DC.t('已儲存代理')); }).then(null, function(){ DC.busy(ok, false); });
					}, 'pri');
					return [btn(null, DC.t('取消'), close), ok];
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
			if(r.official_installed && r.official_enabled) note = DC.t('官方 Download Station 還在使用中。停用或移除它之後，這裡才能接手 /downloadstation。');
			else if(r.official_installed) note = DC.t('官方 Download Station 已停用。啟用後 Qget、Qfile 與瀏覽器外掛會改連 Download Center；重新啟用官方版時它會自己改回。');
			else note = DC.t('啟用後 Qget、Qfile 與瀏覽器外掛可以用原本的設定連到 Download Center。');
			box.appendChild(sec('plug', DC.t('Download Station 相容（Qget／Qfile）'), null, [
				toggle('v4On', DC.t('接手 /downloadstation'), note, on, function(){
					var el = this, want = el.checked;
					el.disabled = true;
					DC.api.post('v4', {enable:want}).then(function(x){ el.disabled = false; el.checked = !!x.linked; DC.toast(x.linked ? DC.t('已接手 /downloadstation') : DC.t('已停止接手 /downloadstation')); }, function(e){ el.disabled = false; el.checked = !want; DC.toast(DC.errText(e)); });
				}),
				r.path ? h('p', {'class':'note mono', text:r.path}) : null
			]));
			var cb = document.getElementById('v4On');
			if(cb && !can) cb.disabled = true;
		}, function(){});
		return box;
	}

	/* ---------- 排程 (7 x 24, Monday first; '1' full speed, '2' limited, '0' paused) ---------- */
	var DAYS = [DC.t('週一'), DC.t('週二'), DC.t('週三'), DC.t('週四'), DC.t('週五'), DC.t('週六'), DC.t('週日')];
	var MODE = {'1':DC.t('全速'), '2':DC.t('限速'), '0':DC.t('暫停')};
	function schedSummary(sched){
		var txt = [], lines = [], d, segs, parts, j, start = 0, prev = null, row, cur, i, n;
		for(d = 0; d < 7; d++){
			row = sched[d]; segs = []; cur = {m:row[0], a:0};
			for(i = 1; i <= 24; i++) if(i === 24 || row[i] !== cur.m){ segs.push({m:cur.m, a:cur.a, b:i}); if(i < 24) cur = {m:row[i], a:i}; }
			if(segs.length === 1) txt.push(DC.t('全天{mode}', {mode:MODE[segs[0].m]}));
			else{
				parts = [];
				for(j = 0; j < segs.length; j++) parts.push(DC.t('{from}–{to} {mode}', {from:DC.pad(segs[j].a) + ':00', to:DC.pad(segs[j].b) + ':00', mode:MODE[segs[j].m]}));
				txt.push(parts.join(DC.t('，')));
			}
		}
		for(d = 0; d <= 7; d++){
			if(d === 7 || (prev !== null && txt[d] !== prev)){
				n = d - start;
				lines.push(DC.t('{days}：{plan}', {days:n === 1 ? DAYS[start] : (n === 2 ? DC.t('{a}、{b}', {a:DAYS[start], b:DAYS[d - 1]}) : DC.t('{a}至{b}', {a:DAYS[start], b:DAYS[d - 1]})), plan:prev}));
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
	/* The speeds of the 限速 periods, as set on the 下載 page (KB/s, 0 = no limit) */
	function limitText(st){
		function v(k, f){ var x = st[k] && st[k][f]; return x > 0 ? DC.fspeed(x * 1024) : DC.t('不限'); }
		return DC.t('「限速」時段：種子下載 {bd}、上傳 {bu}，網址 {http}，FTP {ftp}。', {bd:v('bt', 'limited_down'), bu:v('bt', 'limited_up'), http:v('http', 'limited_down'), ftp:v('ftp', 'limited_down')});
	}
	function scheduleForm(body, r, st){
		var sched = [], d, bar, ed, on = !!r.enabled;
		for(d = 0; d < 7; d++) sched.push((r.days && r.days[d] || '111111111111111111111111').split(''));
		bar = saveBar(function(){
			var days = [], k;
			for(k = 0; k < 7; k++) days.push(sched[k].join(''));
			DC.busy(bar.save, true);
			DC.api.put('schedule', {enabled:DC.chk('scOn'), days:days}).then(function(){ DC.busy(bar.save, false); bar.clean(); DC.toast(DC.t('已儲存排程')); DC.pollStats(); }, function(e){ DC.busy(bar.save, false); DC.toast(DC.errText(e)); });
		}, function(){ scheduleForm(clear(body), r, st); });
		ed = schedEditor(sched, function(){ bar.mark(); });
		ed.classList.toggle('off', !on);
		body.appendChild(sec('cal', DC.t('每週排程'), DC.t('選一支筆，按住拖曳塗色。點星期幾塗滿整天，點時間塗滿整週的那個小時。'), [
			toggle('scOn', DC.t('依排程控制速度'), DC.t('關閉時一律全速，下面的排程不會生效'), on, function(){ ed.classList.toggle('off', !this.checked); bar.mark(); }),
			ed,
			st ? h('p', {'class':'note'}, [limitText(st) + ' ', h('button', {'class':'ib linkish', type:'button', onclick:function(){ DC.leave(function(){ DC.S.setTab = 'dl'; DC.renderView(); window.scrollTo(0, 0); }); }}, DC.t('修改限速'))]) : null,
			bar.el
		]));
	}
	function schedEditor(sched, onEdit){
		var wrap = h('div', {'class':'sched-ed'}), grid = h('div', {'class':'sgrid', role:'grid', 'aria-label':DC.t('每週排程')}), sum = h('div', {'class':'ssum'}), cells = [], painting = false, brush = '1', d, hr, c;
		var brushes = h('div', {'class':'brushes'});
		[['1', DC.t('全速')], ['2', DC.t('限速')], ['0', DC.t('暫停')]].forEach(function(b){
			brushes.appendChild(h('button', {'class':'brush' + (brush === b[0] ? ' on' : ''), type:'button', 'aria-pressed':brush === b[0] ? 'true' : 'false', onclick:function(){
				brush = b[0];
				var all = brushes.children, i; for(i = 0; i < all.length; i++){ all[i].className = 'brush'; all[i].setAttribute('aria-pressed', 'false'); }
				this.className = 'brush on'; this.setAttribute('aria-pressed', 'true');
			}}, [h('i', {'class':'m' + b[0]}), b[1]]));
		});
		function paint(dd, hh){ sched[dd][hh] = brush; cells[dd][hh].className = 'c m' + brush; cells[dd][hh].title = DAYS[dd] + ' ' + DC.pad(hh) + ':00 ' + MODE[brush]; if(onEdit) onEdit(); }
		function summary(){ clear(sum); var l = schedSummary(sched), i; for(i = 0; i < l.length; i++) sum.appendChild(h('div', {text:l[i]})); }
		grid.appendChild(h('span'));
		for(hr = 0; hr < 24; hr++) grid.appendChild(h('button', {'class':'hh num', type:'button', title:DC.t('整週的 {time}', {time:DC.pad(hr) + ':00'}), onclick:(function(hh){ return function(){ for(var k = 0; k < 7; k++) paint(k, hh); summary(); }; })(hr)}, hr % 3 === 0 ? DC.pad(hr) : ''));
		for(d = 0; d < 7; d++){
			cells.push([]);
			grid.appendChild(h('button', {'class':'dh', type:'button', title:DC.t('{day}整天', {day:DAYS[d]}), onclick:(function(dd){ return function(){ for(var k = 0; k < 24; k++) paint(dd, k); summary(); }; })(d)}, DAYS[d]));
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

	/* ---------- 使用者 ---------- */
	function setUsers(body){
		loadingInto(body);
		DC.api.get('users').then(function(r){ clear(body); usersForm(body, r); }, function(e){ errorInto(body, e); });
	}
	function usersForm(body, r){
		var users = r.users || [], accts = r.accounts || [], list = h('div'), addBox = h('div');
		function reload(){ DC.api.get('users').then(function(x){ r = x; users = x.users || []; accts = x.accounts || []; render(); }, function(e){ DC.toast(DC.errText(e)); }); }
		function isQtsAdmin(n){ for(var i = 0; i < accts.length; i++) if(accts[i].name === n) return !!accts[i].admin; return false; }
		function roleText(u){
			var qa = isQtsAdmin(u.name);
			return u.role === 'admin' ? (qa ? DC.t('看得到並管理所有下載與設定') : DC.t('清單上是系統管理者，但不是 QTS 管理員，目前只有一般使用者的權限'))
				: (qa ? DC.t('只看得到自己的下載，檔案存到 home/Download') : DC.t('只看得到自己的下載，檔案存到 home/Download。不是 QTS 管理員，不能設為系統管理者'));
		}
		/* QTS accounts not on the list yet */
		function candidates(){
			var out = [], j, k, taken;
			for(k = 0; k < accts.length; k++){
				taken = false;
				for(j = 0; j < users.length; j++) if(users[j].name === accts[k].name) taken = true;
				if(!taken) out.push(accts[k]);
			}
			return out;
		}
		function roleOptions(name){ var o = [['user', DC.t('一般使用者')]]; if(isQtsAdmin(name)) o.unshift(['admin', DC.t('系統管理者')]); return o; }
		/* Adding picks a QTS account and a role; editing changes the role. Administrators must be QTS administrators. */
		function userForm(u){
			var isNew = !u, cands = candidates(), pick = null, role, k, opts = [];
			if(isNew){
				for(k = 0; k < cands.length; k++) opts.push([cands[k].name, cands[k].admin ? DC.t('{user}（QTS 管理員）', {user:cands[k].name}) : cands[k].name]);
				pick = DC.select('uPick', opts, opts[0][0], function(){ fillRole(this.value); });
			}
			var roleBox = h('span', {'class':'inline'});
			function fillRole(name){
				clear(roleBox);
				role = DC.select('uRole', roleOptions(name), u && u.role === 'admin' && isQtsAdmin(name) ? 'admin' : 'user');
				roleBox.appendChild(role);
			}
			fillRole(isNew ? pick.value : u.name);
			DC.modal(isNew ? DC.t('加入使用者') : DC.t('編輯 {user}', {user:u.name}), 'user', [
				h('p', {'class':'lead', text:isNew ? DC.t('選擇要讓哪個 QTS 帳號登入 Download Center。') : roleText(u)}),
				DC.mform([
					isNew ? field(DC.t('QTS 帳號'), null, pick, 'uPick') : null,
					fieldDiv(DC.t('角色'), DC.t('系統管理者看得到所有下載與設定；一般使用者只看得到自己的下載'), roleBox)
				]),
				h('p', {'class':'note', text:DC.t('系統管理者只能指定給 QTS administrators 群組的成員，因為系統管理者可以把檔案存到 NAS 上任何共用資料夾。')})
			], function(close){
				var ok = btn(null, isNew ? DC.t('加入') : DC.t('儲存'), function(){
					var n = isNew ? pick.value : u.name, v = role.value;
					DC.busy(ok, true);
					(isNew ? DC.api.post('users', {name:n, role:v}) : DC.api.patch('users/' + encodeURIComponent(n), {role:v})).then(function(){
						close();
						DC.toast(isNew ? DC.t('已加入 {user}', {user:n}) : (v === 'admin' ? DC.t('{user} 改為系統管理者', {user:n}) : DC.t('{user} 改為一般使用者', {user:n})));
						reload();
					}, function(e){ DC.busy(ok, false); DC.toast(DC.errText(e)); });
				}, 'pri');
				return [btn(null, DC.t('取消'), close), ok];
			}, {nofocus:true});
			(pick || role).focus();
		}
		function render(){
			var j;
			clear(list);
			for(j = 0; j < users.length; j++){
				(function(u){
					list.appendChild(h('div', {'class':'lrow'}, [icon('user'), h('div', null, [h('b', {text:u.name}),
						h('div', {'class':'pills'}, [h('span', {text:u.role === 'admin' ? DC.t('系統管理者') : DC.t('一般使用者')})]),
						h('small', {text:roleText(u) + (u.last_login_at ? DC.t('。最近登入 {time}', {time:DC.ftime(u.last_login_at)}) : '')})]),
						h('div', {'class':'acts2'}, [
							ibtn('edit', DC.t('編輯'), function(){ userForm(u); }),
							u.name === DC.me() ? null : ibtn('trash', DC.t('移除 {user}', {user:u.name}), function(){
								DC.confirm(DC.t('移除 {user}', {user:u.name}), 'user', DC.t('{user} 將無法再登入 Download Center，他的任務仍會保留。', {user:u.name}), DC.t('移除'), function(){
									DC.api.del('users/' + encodeURIComponent(u.name)).then(function(){ DC.toast(DC.t('已移除 {user}，他的任務仍保留', {user:u.name})); reload(); }, function(e){ DC.toast(DC.errText(e)); });
								}, true);
							})])]));
				})(users[j]);
			}
			clear(addBox);
			if(candidates().length) addBox.appendChild(DC.addRow(DC.t('加入使用者'), function(){ userForm(null); }));
			else addBox.appendChild(h('p', {'class':'note', text:DC.t('所有 QTS 帳號都已經在清單上。')}));
		}
		render();
		body.appendChild(sec('user', DC.t('使用者'), DC.t('只有清單上的帳號能登入 Download Center。系統管理者看得到所有下載；一般使用者只看得到自己的下載，檔案固定存到各自家目錄的 home/Download。'), [
			list, addBox,
			r.homes_enabled ? null : h('p', {'class':'note warn', text:DC.t('QTS 的家目錄服務沒有啟用：一般使用者的檔案會存到 Public。')})
		]));
	}

	DC.settings = {view:view};
})();
