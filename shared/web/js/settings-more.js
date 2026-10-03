/* Settings sections: site and file-hosting accounts, access tokens, notifications and integration, import from the official package. */
(function(){
	'use strict';
	var DC = window.DC, h = DC.h, add = DC.add, clear = DC.clear, icon = DC.icon, ibtn = DC.ibtn, btn = DC.btn;
	var field = DC.field, fieldDiv = DC.fieldDiv, toggle = DC.toggle, sec = DC.sec;

	/* ---------- 網站帳號 ---------- */
	var HOSTERS_DEFAULT = [
		{id:'1fichier', title:'1fichier', secret_label:DC.t('API 金鑰')},
		{id:'rapidgator', title:'Rapidgator', secret_label:DC.t('密碼'), needs_username:true},
		{id:'realdebrid', title:'Real-Debrid', secret_label:DC.t('API 權杖')},
		{id:'alldebrid', title:'AllDebrid', secret_label:DC.t('API 金鑰')},
		{id:'cookies', title:DC.t('其他網站（Cookie）'), secret_label:DC.t('cookies.txt 內容'), needs_host:true}
	];
	function accountInfo(a){
		var i = a.info || {}, out = [];
		if(i.error) return DC.t('驗證失敗：{error}', {error:i.error});
		if(i.plan) out.push(i.plan);
		if(i.premium === false) out.push(DC.t('免費帳號'));
		if(i.expires_at) out.push(DC.t('到期 {date}', {date:DC.fdate(i.expires_at)}));
		if(i.traffic_left !== undefined && i.traffic_left !== null && i.traffic_left >= 0) out.push(DC.t('今天還能下載 {size}', {size:DC.fsize(i.traffic_left)}));
		if(!out.length && i.verified_at) out.push(DC.t('已驗證'));
		return out.join(DC.t('，'));
	}
	function accounts(body){
		DC.loadingInto(body);
		DC.api.get('accounts').then(function(r){ clear(body); accountsForm(body, r); }, function(e){ DC.errorInto(body, e); });
	}
	function accountsForm(body, r){
		var all = r.accounts || [], svcs = (r.services && r.services.length) ? r.services : HOSTERS_DEFAULT, site = [], hosts = [], i, list = h('div'), hlist = h('div');
		function reload(){ accounts(clear(body)); }
		for(i = 0; i < all.length; i++) (all[i].kind === 'site' ? site : hosts).push(all[i]);
		function svcById(id){ for(var k = 0; k < svcs.length; k++) if(svcs[k].id === id) return svcs[k]; return {id:id, title:id}; }
		function row(a, isSite){
			var en = h('input', {type:'checkbox', id:'acEn' + a.id, checked:a.enabled, 'aria-label':DC.t('啟用'), onchange:function(){
				var on = this.checked;
				DC.api.patch('accounts/' + a.id, {enabled:on}).then(function(){ DC.toast(on ? DC.t('已啟用') : DC.t('已停用')); }, function(e){ DC.toast(DC.errText(e)); });
			}});
			var info = isSite ? DC.t('{host}，帳號 {user}', {host:a.host, user:a.username}) : accountInfo(a);
			return h('div', {'class':'lrow'}, [icon('key'), h('div', null, [h('b', {'class':isSite ? 'mono' : '', text:isSite ? a.host : (a.username || a.host ? DC.t('{service}：{account}', {service:svcById(a.kind).title, account:a.username || a.host}) : svcById(a.kind).title)}),
					h('small', {text:isSite ? DC.t('帳號 {user}', {user:a.username}) : info || DC.t('尚未驗證')}),
					h('label', {'class':'toggle', 'for':'acEn' + a.id}, [en, h('span', {text:DC.t('啟用')})])]),
				h('div', {'class':'acts2'}, [
					isSite ? null : ibtn('retry', DC.t('重新驗證'), function(){
						DC.api.post('accounts/' + a.id + '/verify', {}).then(function(x){ var t = accountInfo(x.account || {}); DC.toast(t || DC.t('已驗證')); reload(); }, function(e){ DC.toast(DC.errText(e)); });
					}),
					ibtn('edit', DC.t('編輯'), function(){ if(isSite) siteForm(a); else hostForm(a); }),
					ibtn('trash', DC.t('刪除'), function(){
						DC.confirm(DC.t('刪除帳號'), 'key', DC.t('刪除後，下載這個網站的檔案時不會再帶入這組帳號。'), DC.t('刪除'), function(){
							DC.api.del('accounts/' + a.id).then(function(){ DC.toast(DC.t('已刪除帳號')); reload(); }, function(e){ DC.toast(DC.errText(e)); });
						}, true);
					})])]);
		}
		/* One window adds and edits a site account; the password field left empty keeps the stored one. */
		function siteForm(a){
			var isNew = !a;
			a = a || {host:'', username:''};
			DC.modal(isNew ? DC.t('加入帳號') : DC.t('編輯帳號'), 'key', [
				h('p', {'class':'lead', text:DC.t('下載這個網站的檔案時會自動帶入帳號密碼。')}),
				DC.mform([
					field(DC.t('網站'), DC.t('主機名稱，例如 ftp.example.org'), h('input', {type:'text', id:'aHost', value:a.host, autocapitalize:'off', spellcheck:'false'}), 'aHost'),
					field(DC.t('帳號'), null, h('input', {type:'text', id:'aUser', value:a.username, autocapitalize:'off', autocomplete:'off'}), 'aUser'),
					field(DC.t('密碼'), isNew ? DC.t('儲存後不會再顯示') : DC.t('已儲存；留空表示不變'), h('input', {type:'password', id:'aPass', autocomplete:'new-password'}), 'aPass')
				])
			], function(close){
				var ok = btn(null, isNew ? DC.t('加入') : DC.t('儲存'), function(){
					var o = {host:DC.val('aHost'), username:DC.val('aUser'), secret:DC.val('aPass')};
					if(!o.host || !o.username){ DC.toast(DC.t('請填網站與帳號')); return; }
					DC.busy(ok, true);
					(isNew ? DC.api.post('accounts', {kind:'site', host:o.host, username:o.username, secret:o.secret, enabled:true}) : DC.api.patch('accounts/' + a.id, o)).then(function(){
						close(); DC.toast(isNew ? DC.t('已加入帳號') : DC.t('已儲存帳號')); reload();
					}, function(e){ DC.busy(ok, false); DC.toast(DC.errText(e)); });
				}, 'pri');
				return [btn(null, DC.t('取消'), close), ok];
			}, {nofocus:true});
			document.getElementById(isNew ? 'aHost' : 'aPass').focus();
		}
		/* File-hosting accounts: the service decides the fields; saving verifies the account. The service of an existing account
		   cannot change (delete it and add another instead). */
		function hostForm(a){
			var isNew = !a, fields = h('div');
			var svcOpts = [], k;
			for(k = 0; k < svcs.length; k++) svcOpts.push([svcs[k].id, svcs[k].title]);
			var svc = DC.select('hSvc', svcOpts, isNew ? svcs[0].id : a.kind, function(){ fill(this.value); });
			if(!isNew) svc.disabled = true;
			function fill(id){
				var sv = svcById(id), label = sv.secret_label || DC.t('API 金鑰');
				clear(fields);
				if(sv.needs_host || id === 'cookies') fields.appendChild(field(DC.t('網站'), DC.t('主機名稱，例如 example-host.com'), h('input', {type:'text', id:'hHost', value:a ? a.host || '' : '', autocapitalize:'off', spellcheck:'false'}), 'hHost'));
				if(sv.needs_username) fields.appendChild(field(DC.t('帳號'), null, h('input', {type:'text', id:'hUser', value:a ? a.username || '' : '', autocapitalize:'off', autocomplete:'off'}), 'hUser'));
				fields.appendChild(id === 'cookies'
					? fieldDiv(label, isNew ? DC.t('貼上 cookies.txt 的內容') : DC.t('已儲存；留空表示不變'), h('textarea', {id:'hSecret', rows:'4', 'aria-label':label, 'class':'secretarea'}))
					: field(label, isNew ? DC.t('儲存後不會再顯示') : DC.t('已儲存；留空表示不變'), h('input', {type:'password', id:'hSecret', autocomplete:'new-password'}), 'hSecret'));
			}
			fill(svc.value);
			DC.modal(isNew ? DC.t('加入免空帳號') : DC.t('編輯免空帳號'), 'key', [
				h('p', {'class':'lead', text:DC.t('貼上這個網站的連結時，會用你的帳號取得下載位址。帳號只屬於你。')}),
				DC.mform([field(DC.t('免空服務'), null, svc, 'hSvc'), fields]),
				h('p', {'class':'note', text:DC.t('MEGA 使用自己的加密方式，目前不支援。需要輸入驗證碼或等待倒數的免費下載也不支援。')})
			], function(close){
				var ok = btn(null, isNew ? DC.t('驗證並加入') : DC.t('儲存並驗證'), function(){
					var sv = svcById(svc.value), o = {host:DC.val('hHost'), username:DC.val('hUser'), secret:DC.val('hSecret')};
					if(isNew && !o.secret){ DC.toast(DC.t('請填{field}', {field:sv.secret_label || DC.t('API 金鑰')})); return; }
					if(document.getElementById('hHost') && !o.host){ DC.toast(DC.t('請填網站')); return; }
					DC.busy(ok, true, DC.t('驗證中…'));
					var done = function(x){ var t = accountInfo((x && x.account) || {}); close(); DC.toast(t || (isNew ? DC.t('已加入') : DC.t('已儲存帳號'))); reload(); };
					var fail = function(e){ DC.busy(ok, false); DC.toast(DC.errText(e)); };
					if(isNew) DC.api.post('accounts', {kind:sv.id, host:o.host, username:o.username, secret:o.secret, enabled:true}).then(done, fail);
					else DC.api.patch('accounts/' + a.id, o).then(function(){ return DC.api.post('accounts/' + a.id + '/verify', {}); }).then(done, fail);
				}, 'pri');
				return [btn(null, DC.t('取消'), close), ok];
			}, {nofocus:true});
			if(isNew) svc.focus(); else if(document.getElementById('hSecret')) document.getElementById('hSecret').focus();
		}
		for(i = 0; i < site.length; i++) list.appendChild(row(site[i], true));
		if(!site.length) list.appendChild(DC.emptyAdd('key', DC.t('還沒有帳號。'), DC.t('加入帳號'), function(){ siteForm(null); }));
		for(i = 0; i < hosts.length; i++) hlist.appendChild(row(hosts[i], false));
		if(!hosts.length) hlist.appendChild(DC.emptyAdd('key', DC.t('還沒有免空帳號。'), DC.t('加入免空帳號'), function(){ hostForm(null); }));
		body.appendChild(sec('key', DC.t('帳號密碼（HTTP / FTP）'), DC.t('下載這些網站的檔案時會自動帶入帳號密碼。密碼儲存後不會再顯示。'), [list,
			site.length ? DC.addRow(DC.t('加入帳號'), function(){ siteForm(null); }) : null]));
		body.appendChild(sec('key', DC.t('免空帳號'), DC.t('貼上這些網站的連結時，會用你的帳號登入後取得下載位址。帳號只屬於你，其他使用者看不到，也不能用。'), [hlist,
			hosts.length ? DC.addRow(DC.t('加入免空帳號'), function(){ hostForm(null); }) : null]));
	}

	/* ---------- 存取權杖 ---------- */
	var SCOPES = [
		['tasks:read', DC.t('查看任務')], ['tasks:add', DC.t('加入下載')], ['tasks:control', DC.t('開始、暫停、排序、選檔')], ['tasks:remove', DC.t('移除任務（保留檔案）')],
		['files:delete', DC.t('移除任務並刪除檔案')], ['stats:read', DC.t('查看速度與空間')], ['events:read', DC.t('接收事件')], ['notify:manage', DC.t('管理我的通知')],
		['settings:read', DC.t('讀取設定')], ['settings:write', DC.t('修改設定與排程')]
	];
	var PRESETS = {read:['tasks:read', 'stats:read', 'events:read'], add:['tasks:read', 'tasks:add', 'stats:read', 'events:read'], full:['tasks:read', 'tasks:add', 'tasks:control', 'tasks:remove', 'stats:read', 'events:read']};
	function scopeLabel(s){ for(var i = 0; i < SCOPES.length; i++) if(SCOPES[i][0] === s) return SCOPES[i][1]; return s; }
	var PRESET_LABEL = {read:DC.t('唯讀'), add:DC.t('加入下載'), full:DC.t('完整控制（不含刪檔）')};
	function presetOfScopes(sc){
		var p, k, same;
		for(p in PRESETS) if(PRESETS.hasOwnProperty(p)){
			same = PRESETS[p].length === sc.length;
			for(k = 0; same && k < sc.length; k++) if(PRESETS[p].indexOf(sc[k]) < 0) same = false;
			if(same) return p;
		}
		return '';
	}
	function reveal(value, regen){
		var code = h('code', {text:value});
		DC.modal(regen ? DC.t('已重新產生權杖') : DC.t('權杖已建立'), 'ticket', [
			h('p', {'class':'lead', text:regen ? DC.t('請現在複製。關掉這個視窗後就看不到完整權杖了，舊的權杖已失效。') : DC.t('請現在複製。關掉這個視窗後就看不到完整權杖了。')}),
			h('div', {'class':'secret'}, [code, btn('copy', DC.t('複製'), function(){ DC.copyText(value, code); })]),
			h('p', {'class':'note', text:DC.t('使用方式：在請求加上 Authorization: Bearer <權杖>。')})
		], function(close){ return [btn(null, DC.t('我已複製'), close, 'pri')]; });
	}
	function tokens(body){
		DC.loadingInto(body);
		DC.api.get('tokens').then(function(r){ clear(body); tokensForm(body, r); }, function(e){ DC.errorInto(body, e); });
	}
	function tokensForm(body, r){
		var admin = DC.isAdmin(), list = h('div'), toks = r.tokens || [];
		function reload(){ tokens(clear(body)); }
		function render(){
			clear(list);
			if(!toks.length){ list.appendChild(DC.emptyAdd('ticket', DC.t('還沒有權杖。'), DC.t('建立權杖'), function(){ tokenForm(null); })); return; }
			for(var j = 0; j < toks.length; j++){
				(function(t){
					var pills = h('div', {'class':'pills'}), all = h('div', {'class':'pills', hidden:true}), sc = t.scopes || [], pre = presetOfScopes(sc), k, more;
					pills.appendChild(h('span', {text:pre ? PRESET_LABEL[pre] : DC.t('自訂 {n} 項權限', {n:sc.length})}));
					if(sc.indexOf('files:delete') >= 0) pills.appendChild(h('span', {'class':'warn', text:scopeLabel('files:delete')}));
					for(k = 0; k < sc.length; k++) all.appendChild(h('span', {'class':sc[k] === 'files:delete' ? 'warn' : '', text:scopeLabel(sc[k])}));
					more = h('button', {'class':'ib linkish pmore', type:'button', 'aria-expanded':'false', onclick:function(){ all.hidden = !all.hidden; more.setAttribute('aria-expanded', all.hidden ? 'false' : 'true'); more.textContent = all.hidden ? DC.t('顯示權限') : DC.t('隱藏權限'); }}, DC.t('顯示權限'));
					pills.appendChild(more);
					if(t.tasks === 'all') pills.appendChild(h('span', {text:DC.t('所有人的任務')}));
					if(t.folders && t.folders.length) pills.appendChild(h('span', {text:DC.t('只限 {list}', {list:t.folders.join(DC.t('、'))})}));
					if(t.ip_allow && t.ip_allow.length) pills.appendChild(h('span', {text:DC.t('只限 {list}', {list:t.ip_allow.join(DC.t('、'))})}));
					if(t.sources && t.sources.length) pills.appendChild(h('span', {text:DC.t('來源：{list}', {list:t.sources.join(DC.t('、'))})}));
					list.appendChild(h('div', {'class':'lrow'}, [icon('ticket'), h('div', null, [h('b', {text:t.name}), h('small', {'class':'mono', text:'dct_' + String(t.id).replace(/^tok_/, '') + '_…'}), pills, all,
						h('small', {text:DC.t('到期：{expires}。最近使用：{used}', {expires:t.expires_at ? DC.fdate(t.expires_at) : DC.t('永不過期'),
							used:t.last_used_at ? DC.t('{time}，{ip}', {time:DC.ftime(t.last_used_at), ip:t.last_used_ip}) + (t.use_count ? DC.t('（{n} 次）', {n:t.use_count}) : '') : DC.t('尚未使用')})})]),
						h('div', {'class':'acts2'}, [ibtn('edit', DC.t('編輯'), function(){ tokenForm(t); }), ibtn('retry', DC.t('重新產生'), function(){
							DC.confirm(DC.t('重新產生「{name}」', {name:t.name}), 'ticket', DC.t('舊的權杖會立刻失效，使用它的程式要換成新的權杖。'), DC.t('重新產生'), function(){
								DC.api.post('tokens/' + t.id + '/regenerate', {}).then(function(x){ reveal(x.value, true); }, function(e){ DC.toast(DC.errText(e)); });
							}, true);
						}), ibtn('trash', DC.t('撤銷'), function(){
							DC.confirm(DC.t('撤銷「{name}」', {name:t.name}), 'ticket', DC.t('使用這個權杖的程式會立刻無法連線。'), DC.t('撤銷'), function(){
								DC.api.del('tokens/' + t.id).then(function(){ DC.toast(DC.t('已撤銷 {name}', {name:t.name})); reload(); }, function(e){ DC.toast(DC.errText(e)); });
							}, true);
						})])]));
				})(toks[j]);
			}
		}
		/* One window creates and edits a token. Editing changes what the token may do; the token itself stays valid. */
		function tokenForm(t){
			var isNew = !t, k, folderLim = null;
			t = t || {name:'', scopes:PRESETS.add, tasks:'own', folders:[], sources:[], ip_allow:[], rate_limit:120};
			var name = h('input', {type:'text', id:'tName', value:t.name, placeholder:DC.t('例如 Home Assistant')});
			var boxes = {}, scopeWrap = h('div', {'class':'scopes'});
			for(k = 0; k < SCOPES.length; k++){
				if(!admin && SCOPES[k][0].indexOf('settings:') === 0) continue;
				(function(sc){
					var id = 'sc_' + sc[0].replace(':', '_');
					boxes[sc[0]] = h('input', {type:'checkbox', id:id, checked:(t.scopes || []).indexOf(sc[0]) >= 0});
					scopeWrap.appendChild(h('label', {'class':'toggle', 'for':id}, [boxes[sc[0]], h('span', null, [sc[1], sc[0] === 'files:delete' ? h('small', {text:DC.t('危險：會刪除 NAS 上的檔案')}) : h('small', {'class':'mono', text:sc[0]})])]));
				})(SCOPES[k]);
			}
			function applyPreset(p){ for(var key in boxes) if(boxes.hasOwnProperty(key)) boxes[key].checked = PRESETS[p].indexOf(key) >= 0; }
			function presetOf(){
				var p, key, same;
				for(p in PRESETS) if(PRESETS.hasOwnProperty(p)){
					same = true;
					for(key in boxes) if(boxes.hasOwnProperty(key) && boxes[key].checked !== (PRESETS[p].indexOf(key) >= 0)) same = false;
					if(same) return p;
				}
				return 'custom';
			}
			var presetSel = DC.select('tPreset', [['read', DC.t('唯讀')], ['add', DC.t('加入下載')], ['full', DC.t('完整控制（不含刪檔）')], ['custom', DC.t('自訂')]], presetOf(), function(){ if(this.value !== 'custom') applyPreset(this.value); });
			for(k in boxes) if(boxes.hasOwnProperty(k)) boxes[k].onchange = function(){ presetSel.value = presetOf(); };
			var scopeSel = DC.select('tTasks', [['own', DC.t('只有我的任務')], admin ? ['all', DC.t('所有人的任務')] : null], t.tasks === 'all' ? 'all' : 'own');
			if(admin) folderLim = DC.folderPicker('tFolder', (t.folders || [])[0] || '', true, null, {noneLabel:DC.t('不限'), noFree:true});
			var expOpts = [['30', DC.t('30 天')], ['90', DC.t('90 天')], ['365', DC.t('一年')], ['0', DC.t('永不過期')]];
			if(!isNew) expOpts.unshift(['keep', t.expires_at ? DC.t('維持目前的期限（{date}）', {date:DC.fdate(t.expires_at)}) : DC.t('維持目前的期限（永不過期）')]);
			var exp = DC.select('tExp', expOpts, isNew ? '90' : 'keep');
			var ip = h('input', {type:'text', id:'tIp', value:(t.ip_allow || []).join(', '), placeholder:DC.t('192.168.1.0/24，留空不限'), autocapitalize:'off'});
			var rate = DC.num('tRate', t.rate_limit || 120, DC.t('次／分鐘'), {min:'1'});
			var srcs0 = t.sources || [], has = function(x){ return !srcs0.length || srcs0.indexOf(x) >= 0; };
			var srcWrap = h('span', {'class':'inline'}, [
				toggle('tsUrl', DC.t('網址'), null, has('url')), toggle('tsTor', DC.t('種子檔'), null, has('torrent')), toggle('tsMag', DC.t('磁力連結'), null, has('magnet'))]);
			DC.modal(isNew ? DC.t('建立權杖') : DC.t('編輯權杖'), 'ticket', [
				h('p', {'class':'lead', text:DC.t('權杖能做的事不會超過你的帳號。帳號停用或在 QTS 取消 Download Center 的使用權限時，權杖一起失效。')}),
				DC.mform([
					field(DC.t('名稱'), DC.t('日後辨認是哪個程式在用'), name, 'tName'),
					field(DC.t('權限'), null, presetSel, 'tPreset'), scopeWrap
				]),
				DC.mform([
					admin ? field(DC.t('可操作的任務'), null, scopeSel, 'tTasks') : null,
					admin ? fieldDiv(DC.t('可存放的資料夾'), DC.t('加入下載時只能存到這個資料夾或它底下的資料夾'), folderLim.el) : null,
					fieldDiv(DC.t('可加入的來源'), null, srcWrap),
					field(DC.t('允許的來源 IP'), DC.t('逗號分隔'), ip, 'tIp'),
					field(DC.t('速率上限'), null, rate, 'tRate'),
					field(DC.t('有效期限'), null, exp, 'tExp')
				])
			], function(close){
				var ok = btn(null, isNew ? DC.t('建立權杖') : DC.t('儲存'), function(){
					var sc = [], key, srcs = [], body2;
					for(key in boxes) if(boxes.hasOwnProperty(key) && boxes[key].checked) sc.push(key);
					if(!name.value){ name.focus(); DC.toast(DC.t('請替權杖取個名稱')); return; }
					if(!sc.length){ DC.toast(DC.t('至少要選一項權限')); return; }
					if(DC.chk('tsUrl')) srcs.push('url');
					if(DC.chk('tsTor')) srcs.push('torrent');
					if(DC.chk('tsMag')) srcs.push('magnet');
					if(!srcs.length){ DC.toast(DC.t('至少要允許一種來源')); return; }
					body2 = {name:name.value, scopes:sc, tasks:scopeSel.value, rate_limit:DC.ival('tRate') || 120,
						ip_allow:ip.value ? ip.value.split(/[\s,]+/).filter(function(x){ return !!x; }) : [], sources:srcs.length === 3 ? [] : srcs,
						folders:folderLim && folderLim.value ? [folderLim.value] : []};
					if(exp.value !== 'keep') body2.expires_days = +exp.value;
					DC.busy(ok, true);
					if(isNew) DC.api.post('tokens', body2).then(function(x){ close(); reload(); reveal(x.value, false); }, function(err){ DC.busy(ok, false); DC.toast(DC.errText(err)); });
					else DC.api.patch('tokens/' + t.id, body2).then(function(){ close(); DC.toast(DC.t('已儲存權杖')); reload(); }, function(err){ DC.busy(ok, false); DC.toast(DC.errText(err)); });
				}, 'pri');
				return [btn(null, DC.t('取消'), close), ok];
			}, {nofocus:true, wide:true});
			name.focus();
		}
		render();
		add(body, [
			sec('ticket', DC.t('存取權杖'), DC.t('讓其他程式（Home Assistant、自己寫的機器人、書籤小工具）用你的身分操作下載。每個程式用一個權杖，不用時撤銷即可。'), [
				list, toks.length ? DC.addRow(DC.t('建立權杖'), function(){ tokenForm(null); }) : null
			]),
			devSec()
		]);
	}

	/* Everything a developer needs in one place (the 存取權杖 page). The integration guide ships with the package as plain text. */
	function devSec(){
		return sec('plug', DC.t('給開發者'), DC.t('自己做 Bot 或整合時，用存取權杖呼叫 API，或訂閱事件。'), [
			h('dl', {'class':'kv'}, [h('dt', {text:DC.t('API 位址')}), h('dd', {'class':'mono', text:location.protocol + '//' + location.host + location.pathname + 'api/v1/'}),
				h('dt', {text:DC.t('驗證')}), h('dd', {'class':'mono', text:'Authorization: Bearer dct_…'}),
				h('dt', {text:DC.t('聊天指令')}), h('dd', {'class':'mono', text:'POST /api/v1/commands {"text":"/list"}'}),
				h('dt', {text:DC.t('事件串流')}), h('dd', {'class':'mono', text:'GET /api/v1/events/stream'}),
				h('dt', {text:DC.t('Webhook 簽章')}), h('dd', {'class':'mono', text:'X-DC-Signature: t=…,v1=…'}),
				h('dt', {text:DC.t('文件')}), h('dd', null, h('a', {'class':'linkish', href:'docs/integration.txt', target:'_blank', rel:'noopener'}, [icon('popout'), DC.t('整合說明（INTEGRATION.md）')]))])
		]);
	}

	/* ---------- 通知與整合 ---------- */
	var EVENT_LABEL = {
		'task.added':DC.t('加入'), 'task.started':DC.t('開始'), 'task.completed':DC.t('完成'), 'task.seeding_finished':DC.t('做種結束'), 'task.moved':DC.t('已搬移'), 'task.failed':DC.t('失敗'),
		'task.removed':DC.t('已移除'), 'task.paused':DC.t('暫停'), 'task.resumed':DC.t('繼續'), 'task.merged':DC.t('合併來源'), 'task.source_switched':DC.t('切換來源'), 'queue.idle':DC.t('全部下載完成'),
		'disk.low':DC.t('空間不足'), 'schedule.changed':DC.t('排程切換'), 'account.expiring':DC.t('免空帳號將到期'), 'engine.down':DC.t('引擎停止'), 'engine.up':DC.t('引擎恢復'),
		'security.token_created':DC.t('建立權杖'), 'security.token_rejected':DC.t('權杖遭拒')
	};
	function opsHelp(s){
		if(s.ops === 'yes') return DC.t('連結過帳號的人可以在頻道裡用指令查詢、加入、暫停下載');
		if(s.ops === 'https') return DC.t('需要 NAS 能從外部用 HTTPS 連到，才能把訊息送進來');
		if(s.ops === 'commands') return DC.t('事件會送到你的網址；要操作下載，由你的服務呼叫 /api/v1/commands');
		return DC.t('這個服務只能接收通知');
	}
	function canOperate(s){ return s.ops === 'yes' || s.ops === 'https'; }
	function pairDialog(c, p){
		var left = Math.max(0, (p.expires_at || 0) - Math.floor(Date.now() / 1000)), timer = h('small', {'class':'num'}), iv, cmd = p.command || ('/link ' + p.code);
		function upd(){ timer.textContent = left > 0 ? DC.t('剩 {min} 分 {sec} 秒有效', {min:Math.floor(left / 60), sec:DC.pad(left % 60)}) : DC.t('已過期，請重新產生'); }
		upd(); iv = setInterval(function(){ left--; upd(); if(left <= 0) clearInterval(iv); }, 1000);
		DC.modal(DC.t('連結到「{name}」', {name:c.name}), 'chat', [
			h('p', {'class':'lead', text:DC.t('在這個頻道對 Bot 傳送下面這行，之後就能用 {user} 的身分在頻道裡操作下載。', {user:DC.me()})}),
			h('div', {'class':'code6 num', text:cmd}), timer,
			h('p', {'class':'note', text:DC.t('指令：/list、/add <網址>、/pause <編號>、/resume <編號>、/speed、/help')})
		], function(close){ return [btn('copy', DC.t('複製'), function(){ DC.copyText(cmd); }), btn(null, DC.t('完成'), function(){ clearInterval(iv); close(); }, 'pri')]; }, {onclose:function(){ clearInterval(iv); }});
	}
	function notify(body){
		DC.loadingInto(body);
		DC.api.get('channels', null, {quiet:true}).then(function(r){ clear(body); notifyForm(body, r); }, function(e){
			clear(body);
			if(e.status === 404) body.appendChild(sec('bell', DC.t('通知與聊天 Bot'), null, [h('p', {'class':'note', text:DC.t('這個版本還沒有通知功能。')})]));
			else DC.errorInto(body, e);
		});
	}
	function notifyForm(body, r){
		var admin = DC.isAdmin(), list = h('div'), chans = r.channels || [], svcs = r.services || [], evs = r.events || [], adapters = r.adapters || [];
		function reload(){ notify(clear(body)); }
		function svcById(id){ for(var i = 0; i < svcs.length; i++) if(svcs[i].id === id) return svcs[i]; return {id:id, title:id, fields:[], ops:'no'}; }
		function render(){
			clear(list);
			if(!chans.length){ list.appendChild(svcs.length ? DC.emptyAdd('bell', DC.t('還沒有頻道。'), DC.t('新增頻道'), function(){ channelForm(null); }) : h('p', {'class':'note', text:DC.t('還沒有頻道。')})); return; }
			for(var j = 0; j < chans.length; j++){
				(function(c, idx){
					var s = svcById(c.service), can = canOperate(s), names = [], k;
					for(k = 0; k < (c.events || []).length; k++) names.push(EVENT_LABEL[c.events[k]] || c.events[k]);
					var opBox = h('input', {type:'checkbox', id:'op' + idx, checked:!!c.operate, disabled:!can, onchange:function(){
						var on = this.checked;
						DC.api.patch('channels/' + c.id, {operate:on}).then(function(x){
							DC.toast(on ? DC.t('「{name}」可以操作下載了', {name:c.name}) : DC.t('「{name}」改為只接收通知', {name:c.name}));
							if(on && x.pair) pairDialog(c, x.pair);
							reload();
						}, function(e){ DC.toast(DC.errText(e)); reload(); });
					}});
					var enBox = h('input', {type:'checkbox', id:'chen' + idx, checked:c.enabled !== false, onchange:function(){
						var on = this.checked;
						DC.api.patch('channels/' + c.id, {enabled:on}).then(function(){ DC.toast(on ? DC.t('已啟用「{name}」', {name:c.name}) : DC.t('已停用「{name}」', {name:c.name})); }, function(e){ DC.toast(DC.errText(e)); });
					}});
					var extra = [];
					if(c.quiet) extra.push(DC.t('{hours} 不通知', {hours:c.quiet}));
					if(c.digest) extra.push(c.digest >= 1440 ? DC.t('每天彙整一次') : DC.t('每 {n} 分鐘彙整一次', {n:c.digest}));
					if(c.fail_count) extra.push(DC.t('連續失敗 {n} 次', {n:c.fail_count}));
					if(admin && c.scope === 'all') extra.push(DC.t('所有人的任務'));
					list.appendChild(h('div', {'class':'lrow'}, [icon(s.id === 'webhook' ? 'plug' : (s.ops === 'no' ? 'bell' : 'chat')), h('div', null, [
						h('b', {text:DC.t('{service}：{name}', {service:s.title, name:c.name})}),
						h('small', {text:extra.length ? DC.t('通知：{events}。{extra}', {events:names.length ? names.join(DC.t('、')) : DC.t('不通知'), extra:extra.join(DC.t('，'))}) : DC.t('通知：{events}', {events:names.length ? names.join(DC.t('、')) : DC.t('不通知')})}),
						h('label', {'class':'toggle', 'for':'chen' + idx}, [enBox, h('span', {text:DC.t('啟用')})]),
						s.ops !== 'no' && s.ops !== 'commands' ? h('label', {'class':'toggle', 'for':'op' + idx}, [opBox, h('span', null, [DC.t('在頻道裡操作下載'), h('small', {text:c.operate ? DC.t('已連結 {n} 位使用者', {n:c.linked || 0}) : opsHelp(s)})])]) : h('small', {text:opsHelp(s)}),
						c.operate ? h('button', {'class':'ib linkish', type:'button', onclick:function(){
							DC.api.post('channels/' + c.id + '/pair', {}).then(function(p){ pairDialog(c, p); }, function(e){ DC.toast(DC.errText(e)); });
						}}, [icon('chat'), DC.t('連結我的帳號')]) : null,
						c.operate && c.linked ? h('button', {'class':'ib linkish', type:'button', onclick:function(){ showLinks(c); }}, [icon('user'), DC.t('已連結的帳號')]) : null
					]), h('div', {'class':'acts2'}, [
						ibtn('edit', DC.t('編輯'), function(){ channelForm(c); }),
						ibtn('bell', DC.t('傳送測試通知'), function(){
							DC.api.post('channels/' + c.id + '/test', {}).then(function(x){ DC.toast(x.ok === false ? DC.t('測試失敗：{error}', {error:x.error || ''}) : DC.t('已傳送測試通知')); }, function(e){ DC.toast(DC.errText(e)); });
						}),
						ibtn('files', DC.t('投遞紀錄'), function(){ showDeliveries(c); }),
						ibtn('trash', DC.t('刪除'), function(){
							DC.confirm(DC.t('刪除「{name}」', {name:c.name}), 'bell', DC.t('這個頻道不會再收到通知，已連結的聊天帳號也會解除。'), DC.t('刪除'), function(){
								DC.api.del('channels/' + c.id).then(function(){ DC.toast(DC.t('已刪除「{name}」', {name:c.name})); reload(); }, function(e){ DC.toast(DC.errText(e)); });
							}, true);
						})])]));
				})(chans[j], j);
			}
		}
		function showDeliveries(c){
			var box = h('div', null, h('div', {'class':'loading'}, [icon('check'), DC.t('讀取中…')]));
			DC.modal(DC.t('投遞紀錄：{name}', {name:c.name}), 'files', [box], function(close){ return [btn(null, DC.t('關閉'), close, 'pri')]; });
			DC.api.get('channels/' + c.id + '/deliveries').then(function(x){
				var ds = x.deliveries || [], ul = h('ul', {'class':'log num'}), i, d;
				clear(box);
				if(!ds.length){ box.appendChild(h('p', {'class':'note', text:DC.t('還沒有投遞紀錄。')})); return; }
				for(i = 0; i < ds.length; i++){
					d = ds[i];
					ul.appendChild(h('li', null, [h('time', {text:DC.ftime(d.time)}), h('span', {text:DC.t('{event}：{result}', {event:EVENT_LABEL[d.event_type] || d.event_type || '', result:
						(d.status === 'ok' || d.status === 'delivered' ? DC.t('成功') : d.status === 'pending' ? DC.t('等待重試') : DC.t('失敗')) + (d.http_status ? DC.t('（HTTP {code}）', {code:d.http_status}) : '') +
						(d.duration_ms ? DC.t('，') + d.duration_ms + ' ms' : '') + (d.attempt > 1 ? DC.t('，第 {n} 次', {n:d.attempt}) : '') + (d.error ? DC.t('，') + d.error : '')})})]));
				}
				box.appendChild(ul);
			}, function(e){ clear(box); box.appendChild(h('p', {'class':'note warn', text:DC.errText(e)})); });
		}
		function showLinks(c){
			var box = h('div');
			function load(){
				clear(box);
				DC.api.get('channels/' + c.id + '/links').then(function(x){
					var ls = x.links || [], i;
					if(!ls.length){ box.appendChild(h('p', {'class':'note', text:DC.t('沒有已連結的帳號。')})); return; }
					for(i = 0; i < ls.length; i++){
						(function(l){
							box.appendChild(h('div', {'class':'lrow'}, [icon('user'), h('div', null, [h('b', {text:l.qts_user}), h('small', {'class':'mono', text:DC.t('{user}，{time}', {user:l.chat_user, time:DC.ftime(l.created_at)})})]),
								h('div', {'class':'acts2'}, (admin || l.qts_user === DC.me()) ? ibtn('trash', DC.t('解除連結'), function(){
									DC.api.del('channels/' + c.id + '/links/' + encodeURIComponent(l.chat_user)).then(function(){ DC.toast(DC.t('已解除連結')); load(); }, function(e){ DC.toast(DC.errText(e)); });
								}) : null)]));
						})(ls[i]);
					}
				}, function(e){ box.appendChild(h('p', {'class':'note warn', text:DC.errText(e)})); });
			}
			load();
			DC.modal(DC.t('已連結的帳號：{name}', {name:c.name}), 'user', [box], function(close){ return [btn(null, DC.t('關閉'), close, 'pri')]; });
		}
		/* One window adds and edits a channel. Adding starts with the service (and can go back to it); editing keeps the service.
		   Secret fields left empty keep what is stored. */
		function channelForm(c){
			var isNew = !c, step = h('div'), ok = null, close = null, collect = null;
			function svcIcon(sv){ return icon(sv.id === 'webhook' ? 'plug' : (sv.ops === 'no' ? 'bell' : 'chat')); }
			/* A coloured initial tells the services apart at a glance (no third-party logos) */
			function svcBadge(sv){ var t = String(sv.title || sv.id), hue = 0, i; for(i = 0; i < t.length; i++) hue = (hue * 31 + t.charCodeAt(i)) % 360; return h('span', {'class':'svc-badge', 'aria-hidden':'true', style:'--hue:' + hue, text:t.charAt(0).toUpperCase()}); }
			function pick(){
				var grid = h('div', {'class':'svcs'}), k;
				for(k = 0; k < svcs.length; k++){
					(function(sv){
						grid.appendChild(h('button', {'class':'ib svc', type:'button', onclick:function(){ details(sv); }}, [svcBadge(sv), h('span', null, [h('b', {text:sv.title}), h('small', {text:sv.note || opsHelp(sv).replace(/^這個服務/, ''), title:sv.note || opsHelp(sv)})]), canOperate(sv) ? h('span', {'class':'svc-ops', title:DC.t('可以在頻道裡操作下載')}, icon('chat')) : null]));
					})(svcs[k]);
				}
				clear(step);
				add(step, [h('p', {'class':'lead', text:DC.t('選擇要送到哪裡。')}), grid]);
				collect = null;
				if(ok) ok.hidden = true;
			}
			function details(sv){
				var evWrap = h('div', {'class':'inline'}), m, fd, inputs = {}, cfg0 = (c && c.config) || {}, set0 = (c && c.secret_fields_set) || [];
				var n = h('input', {type:'text', id:'chName', value:c ? c.name : '', placeholder:DC.t('例如 家裡群組')});
				var ops = h('input', {type:'checkbox', id:'chOps', disabled:!canOperate(sv), checked:c ? !!c.operate : sv.ops === 'yes'});
				var evList = evs.length ? evs : ['task.added', 'task.completed', 'task.failed', 'task.seeding_finished', 'disk.low'];
				for(m = 0; m < evList.length; m++){
					if(!admin && /^(engine|security)\./.test(evList[m])) continue;
					evWrap.appendChild(h('label', {'class':'toggle', 'for':'ev' + m}, [h('input', {type:'checkbox', id:'ev' + m, 'data-ev':evList[m],
						checked:c ? (c.events || []).indexOf(evList[m]) >= 0 : (evList[m] === 'task.completed' || evList[m] === 'task.failed' || evList[m] === 'disk.low')}), h('span', {text:EVENT_LABEL[evList[m]] || evList[m]})]));
				}
				var conn = [];
				for(m = 0; m < (sv.fields || []).length; m++){
					fd = sv.fields[m];
					var secret = fd.type === 'secret' || fd.type === 'password', stored = secret && set0.indexOf(fd.key) >= 0;
					inputs[fd.key] = fd.type === 'textarea' ? h('textarea', {id:'chf_' + fd.key, rows:'4', 'class':'secretarea'})
						: h('input', {type:secret ? 'password' : (fd.type === 'number' ? 'number' : 'text'), id:'chf_' + fd.key, autocomplete:secret ? 'new-password' : 'off', autocapitalize:'off', spellcheck:'false'});
					if(!secret && cfg0[fd.key] !== undefined) inputs[fd.key].value = cfg0[fd.key];
					conn.push((fd.type === 'textarea' ? fieldDiv : field)(fd.required && !stored ? fd.label : DC.t('{field}（選填）', {field:fd.label}), stored ? DC.t('已儲存；留空表示不變') : (fd.help || null), inputs[fd.key], 'chf_' + fd.key));
				}
				var scope = admin ? DC.select('chScope', [['own', DC.t('只有我的任務')], ['all', DC.t('所有人的任務')]], c && c.scope === 'all' ? 'all' : 'own') : null;
				var quiet = h('input', {type:'text', id:'chQuiet', value:c ? c.quiet || '' : '', placeholder:DC.t('22:00-08:00，留空不設')});
				var digest = DC.select('chDigest', [['0', DC.t('每個事件都通知')], ['30', DC.t('每 30 分鐘彙整一次')], ['1440', DC.t('每天一次')]], String(c ? c.digest || 0 : 0));
				clear(step);
				add(step, [
					isNew ? h('div', {'class':'lrow svcpick'}, [svcIcon(sv), h('div', null, [h('b', {text:sv.title}), h('small', {text:opsHelp(sv)})]),
						h('div', {'class':'acts2'}, [btn(null, DC.t('換一個服務'), pick)])]) : null,
					DC.mform([field(DC.t('名稱'), null, n, 'chName')].concat(conn)),
					DC.mform([
						fieldDiv(DC.t('要通知的事件'), null, evWrap),
						sv.ops !== 'no' && sv.ops !== 'commands' ? h('label', {'class':'toggle', 'for':'chOps'}, [ops, h('span', null, [DC.t('在頻道裡操作下載'), h('small', {text:opsHelp(sv)})])]) : null,
						scope ? field(DC.t('範圍'), null, scope, 'chScope') : null,
						field(DC.t('勿擾時段'), DC.t('這段時間的通知會在結束後一次送出'), quiet, 'chQuiet'),
						field(DC.t('彙整'), DC.t('一次加入很多任務時避免洗版'), digest, 'chDigest')
					]),
					sv.ops === 'no' || sv.ops === 'commands' ? h('p', {'class':'note', text:opsHelp(sv)}) : null,
					sv.id === 'webhook' ? h('p', {'class':'note', text:DC.t('每個請求都帶 X-DC-Signature（HMAC-SHA256）。失敗時會在 1 分鐘到 6 小時內重試 5 次。')}) : null,
					sv.id === 'line' ? h('p', {'class':'note', text:DC.t('LINE Notify 已停止服務，這裡使用 Messaging API。')}) : null
				]);
				if(ok) ok.hidden = false;
				collect = function(){
					var ev = [], q, cfg = {}, key, boxes = evWrap.querySelectorAll('input');
					for(q = 0; q < boxes.length; q++) if(boxes[q].checked) ev.push(boxes[q].getAttribute('data-ev'));
					for(q = 0; q < (sv.fields || []).length; q++){
						key = sv.fields[q].key; cfg[key] = inputs[key].value;
						if(sv.fields[q].required && !cfg[key] && !(set0.indexOf(key) >= 0)){ inputs[key].focus(); DC.toast(DC.t('請填{field}', {field:sv.fields[q].label})); return null; }
					}
					return {service:sv.id, name:n.value || sv.title, config:cfg, events:ev, operate:ops.checked && canOperate(sv), scope:scope ? scope.value : 'own', quiet:quiet.value, digest:+digest.value};
				};
				n.focus();
			}
			DC.modal(isNew ? DC.t('新增頻道') : DC.t('編輯頻道'), 'bell', [step], function(cl){
				close = cl;
				ok = btn(null, isNew ? DC.t('儲存頻道') : DC.t('儲存'), function(){
					var o = collect && collect();
					if(!o) return;
					DC.busy(ok, true);
					if(isNew){
						o.enabled = true;
						DC.api.post('channels', o).then(function(x){
							close(); reload();
							if(x.pair) pairDialog(x.channel || {name:o.name}, x.pair); else DC.toast(DC.t('已加入「{name}」', {name:o.name}));
						}, function(err){ DC.busy(ok, false); DC.toast(DC.errText(err)); });
					}else{
						delete o.service;
						DC.api.patch('channels/' + c.id, o).then(function(x){
							close(); reload();
							if(x && x.pair) pairDialog(c, x.pair); else DC.toast(DC.t('已儲存「{name}」', {name:o.name}));
						}, function(err){ DC.busy(ok, false); DC.toast(DC.errText(err)); });
					}
				}, 'pri');
				return [btn(null, DC.t('取消'), cl), ok];
			}, {nofocus:true, wide:true});
			if(isNew) pick(); else details(svcById(c.service));
		}
		function adapterSec(){
			if(!admin) return null;
			var al = h('div'), i;
			function importForm(){
				var ta = h('textarea', {id:'adJson', rows:'10', 'class':'secretarea mono', placeholder:'{"adapter": "mattermost", "title": "Mattermost", "fields": [...], "request": {...}}'});
				DC.modal(DC.t('匯入轉接器'), 'inbox', [
					h('p', {'class':'lead', text:DC.t('用 JSON 描述一個 HTTP 請求，就能接上新的服務（Mattermost、Teams、企業微信…）。轉接器不能執行程式碼。')}), ta
				], function(close){
					var ok = btn(null, DC.t('匯入轉接器'), function(){
						var m;
						try{ m = JSON.parse(ta.value); }catch(e){ DC.toast(DC.t('JSON 格式不正確')); return; }
						DC.busy(ok, true);
						DC.api.post('adapters', {manifest:m}).then(function(){ close(); DC.toast(DC.t('已匯入轉接器')); reload(); }, function(e){ DC.busy(ok, false); DC.toast(DC.errText(e)); });
					}, 'pri');
					return [btn(null, DC.t('取消'), close), ok];
				}, {nofocus:true, wide:true});
				ta.focus();
			}
			for(i = 0; i < adapters.length; i++){
				(function(ad){
					al.appendChild(h('div', {'class':'lrow'}, [icon('plug'), h('div', null, [h('b', {text:ad.title || ad.id}), h('small', {'class':'mono', text:ad.id})]),
						h('div', {'class':'acts2'}, ibtn('trash', DC.t('刪除轉接器'), function(){
							DC.api.del('adapters/' + encodeURIComponent(ad.id)).then(function(){ DC.toast(DC.t('已刪除轉接器')); reload(); }, function(e){ DC.toast(DC.errText(e)); });
						}))]));
				})(adapters[i]);
			}
			if(!adapters.length) al.appendChild(DC.emptyAdd('plug', DC.t('還沒有轉接器。'), DC.t('匯入轉接器'), importForm));
			return sec('inbox', DC.t('轉接器'), DC.t('用 JSON 描述一個 HTTP 請求，就能接上新的服務（Mattermost、Teams、企業微信…）。轉接器不能執行程式碼。'), [al,
				adapters.length ? DC.addRow(DC.t('匯入轉接器'), importForm) : null]);
		}
		render();
		add(body, [
			sec('bell', DC.t('通知與聊天 Bot'), DC.t('每個頻道都會在你選的事件發生時通知你。支援的服務（Telegram，以及能從外部 HTTPS 連到時的 LINE）可以再打開「在頻道裡操作下載」，用指令查詢、加入、暫停下載。'), [list, svcs.length && chans.length ? DC.addRow(DC.t('新增頻道'), function(){ channelForm(null); }) : null]),
			adapterSec(),
			h('p', {'class':'note'}, [DC.t('API、事件串流與 Webhook 簽章的說明在「存取權杖」。') + ' ', h('button', {'class':'ib linkish', type:'button', onclick:function(){ DC.S.setTab = 'token'; DC.S.setOpen = true; DC.renderView(); window.scrollTo(0, 0); }}, DC.t('前往'))])
		]);
	}

	/* ---------- 從官方版匯入 ---------- */
	function importer(body){
		DC.loadingInto(body);
		DC.api.get('import', null, {quiet:true}).then(function(r){ clear(body); importForm(body, r); }, function(e){
			clear(body);
			body.appendChild(sec('inbox', DC.t('從官方 Download Station 匯入'), null, [h('p', {'class':'note', text:e.status === 404 ? DC.t('這個版本還沒有匯入功能。') : DC.errText(e)})]));
		});
	}
	function importForm(body, r){
		var vols = r.volumes || [], unfinished = 0, completed = 0, accts = 0, i, missing = r.users_missing || [], off = r.official || {}, running = !!(off.running || off.enabled), res = h('div', {role:'status'}), go;
		for(i = 0; i < vols.length; i++){ unfinished += vols[i].unfinished || 0; completed += vols[i].completed || 0; accts += vols[i].accounts || 0; }
		if(!r.available){ body.appendChild(sec('inbox', DC.t('從官方 Download Station 匯入'), null, [h('p', {'class':'note', text:DC.t('沒有找到官方 Download Station 的資料。')})])); return; }
		var userBoxes = h('div');
		for(i = 0; i < missing.length; i++) userBoxes.appendChild(toggle('imU' + i, DC.t('開放 {name} 使用 Download Center', {name:missing[i]}), DC.t('他在官方版有任務，但在 QTS 還沒有 Download Center 的使用權限'), true));
		body.appendChild(sec('inbox', DC.t('從官方 Download Station 匯入'), DC.t('匯入只讀取官方資料，不會修改或刪除它，可以重複執行。'), [
			h('ol', {'class':'steps'}, [h('li', {'class':running ? 'on' : '', text:DC.t('1 停止官方版')}), h('li', {'class':running ? '' : 'on', text:DC.t('2 選擇項目並匯入')}), h('li', {text:DC.t('3 驗證')})]),
			running ? h('div', null, [
				h('p', {'class':'note warn', text:DC.t('官方 Download Station 還在執行。匯入前要先停止它，避免兩邊同時寫同一個暫存檔。')}),
				h('div', {'class':'savebar'}, [btn(null, DC.t('停止官方 Download Station'), function(e){
					var b = e.currentTarget;
					DC.confirm(DC.t('停止官方 Download Station'), 'inbox', DC.t('官方版會停止，並在 App Center 停用（開機時不會自動啟動，避免兩邊同時下載同一個任務）。之後可以在 App Center 重新啟用。'), DC.t('停止'), function(){
						DC.busy(b, true);
						DC.api.post('import/stop-official', {}).then(function(){ DC.toast(DC.t('已停止官方 Download Station')); importer(clear(body)); }, function(err){ DC.busy(b, false); DC.toast(DC.errText(err)); });
					}, true);
				}, 'dan')])
			]) : null,
			toggle('imCfg', DC.t('設定'), r.settings && r.settings.found ? DC.t('資料夾、同時下載數、速限、種子設定、排程') : DC.t('找不到官方的設定檔'), !!(r.settings && r.settings.found)),
			toggle('imRun', DC.t('{n} 個未完成的任務', {n:unfinished}), DC.t('種子任務會重新檢查已下載的部分，大檔案需要一些時間；網址任務從已下載的大小續傳'), unfinished > 0),
			toggle('imDone', DC.t('{n} 筆已完成紀錄', {n:completed}), DC.t('只匯入紀錄，不重新做種'), false),
			toggle('imAcct', DC.t('{n} 組網站帳號', {n:accts}), null, accts > 0),
			userBoxes,
			r.last ? h('p', {'class':'note', text:r.last.summary ? DC.t('上次匯入：{time}。{summary}', {time:DC.ftime(r.last.time), summary:summaryText(r.last.summary)}) : DC.t('上次匯入：{time}', {time:DC.ftime(r.last.time)})}) : null,
			res,
			h('div', {'class':'savebar'}, [go = btn('inbox', DC.t('匯入'), function(){
				var add2 = [], k;
				for(k = 0; k < missing.length; k++) if(DC.chk('imU' + k)) add2.push(missing[k]);
				runImport({settings:DC.chk('imCfg'), unfinished:DC.chk('imRun'), completed:DC.chk('imDone'), accounts:DC.chk('imAcct'), add_users:add2});
			}, 'pri')])
		]));
		if(running) go.disabled = true;
	}
	/* Import progress: a window that shows the import is running (the request can take a while with many tasks), then the
	   result, and reloads the page so the imported tasks, settings and accounts show everywhere. A timeout reloads too: the
	   import keeps running on the NAS and the reloaded page shows where it got to. */
	function runImport(req){
		var steps = [DC.t('讀取官方設定與任務…'), DC.t('加入未完成的任務…'), DC.t('匯入網站帳號與紀錄…'), DC.t('準備重新檢查已下載的資料…')], si = 0, t0 = new Date().getTime(), done = false, timer, closeW;
		var stepEl = h('b', {'class':'imp-step', text:steps[0]}), timeEl = h('span', {'class':'note num', text:DC.t('0 秒')});
		var area = h('div', {'class':'imp', role:'status', 'aria-live':'polite'}, [
			h('div', {'class':'imp-orb', 'aria-hidden':'true'}, [h('i'), h('i'), h('i'), icon('inbox')]),
			stepEl, h('div', {'class':'imp-bar', 'aria-hidden':'true'}, h('span')), timeEl,
			h('p', {'class':'note', text:DC.t('官方的資料只會被讀取，不會修改。關掉這個視窗不會中斷匯入。')})]);
		var acts = h('div');
		closeW = DC.modal(DC.t('正在匯入'), 'inbox', [area], function(close){ return [acts]; }, {nofocus:true, onclose:function(){ clearInterval(timer); }});
		timer = setInterval(function(){
			var sec = Math.round((new Date().getTime() - t0) / 1000);
			timeEl.textContent = DC.t('{n} 秒', {n:sec});
			if(sec % 3 === 0 && si < steps.length - 1){ si++; stepEl.textContent = steps[si]; }
		}, 1000);
		function reloadIn(n, lead){
			var left = n, note = h('p', {'class':'note num'});
			function tick(){ note.textContent = lead + DC.t('{n} 秒後重新整理頁面。', {n:left}); if(left-- <= 0) location.reload(); }
			area.appendChild(note); tick();
			clearInterval(timer); timer = setInterval(tick, 1000);
			clear(acts); acts.appendChild(btn(null, DC.t('立即重新整理'), function(){ location.reload(); }, 'pri'));
		}
		DC.api.post('import', req, {timeout:600000, quiet:true}).then(function(x){
			done = true;
			var sm = x.summary || {}, w = sm.warnings || [], j;
			clear(area); area.className = 'imp ok';
			area.appendChild(h('div', {'class':'imp-orb', 'aria-hidden':'true'}, icon('done', 'play')));
			area.appendChild(h('b', {'class':'imp-step', text:DC.t('匯入完成')}));
			area.appendChild(h('p', {'class':'note', text:summaryText(sm) || DC.t('沒有需要匯入的項目。')}));
			for(j = 0; j < w.length; j++) area.appendChild(h('p', {'class':'note warn', text:w[j]}));
			if(DC.importDone) DC.importDone();
			reloadIn(w.length ? 8 : 3, '');
		}, function(e){
			done = true;
			if(e.code === 'timeout' || e.code === 'network'){
				clear(area); area.className = 'imp';
				area.appendChild(h('b', {'class':'imp-step', text:DC.t('匯入還在 NAS 上進行')}));
				area.appendChild(h('p', {'class':'note', text:DC.t('頁面等太久沒有收到回應，匯入不會因此中斷。重新整理後可以看到目前匯入的結果。')}));
				reloadIn(5, '');
				return;
			}
			clearInterval(timer);
			clear(area); area.className = 'imp bad';
			area.appendChild(h('div', {'class':'imp-orb', 'aria-hidden':'true'}, icon('error', 'play')));
			area.appendChild(h('b', {'class':'imp-step', text:DC.t('沒有匯入')}));
			area.appendChild(h('p', {'class':'note', text:DC.errText(e)}));
			clear(acts); acts.appendChild(btn(null, DC.t('關閉'), closeW, 'pri'));
		});
	}
	/* Field names of the importer's Summary (daemon/internal/importer) */
	function summaryText(sm){
		var parts = [];
		if(sm.settings) parts.push(DC.t('設定已匯入'));
		if(sm.tasks_added) parts.push(DC.t('加入 {n} 個任務', {n:sm.tasks_added}));
		if(sm.history_added) parts.push(DC.t('{n} 筆完成紀錄', {n:sm.history_added}));
		if(sm.accounts_added) parts.push(DC.t('{n} 組網站帳號', {n:sm.accounts_added}));
		if(sm.users_added) parts.push(DC.t('開放 {n} 位使用者使用', {n:sm.users_added}));
		if(sm.tasks_skipped) parts.push(DC.t('略過 {n} 個已存在的任務', {n:sm.tasks_skipped}));
		return parts.join(DC.t('，'));
	}

	DC.setMore = {accounts:accounts, tokens:tokens, notify:notify, importer:importer};
})();
