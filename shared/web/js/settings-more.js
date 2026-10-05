/* Settings sections: site and file-hosting accounts, access tokens, notifications and integration, import from the official package. */
(function(){
	'use strict';
	var DC = window.DC, h = DC.h, add = DC.add, clear = DC.clear, icon = DC.icon, ibtn = DC.ibtn, btn = DC.btn;
	var field = DC.field, fieldDiv = DC.fieldDiv, toggle = DC.toggle, sec = DC.sec;

	/* ---------- Site accounts ---------- */
	var HOSTERS_DEFAULT = [
		{id:'1fichier', title:'1fichier', secret_label:DC.t('API key')},
		{id:'rapidgator', title:'Rapidgator', secret_label:DC.t('Password'), needs_username:true},
		{id:'realdebrid', title:'Real-Debrid', secret_label:DC.t('API token')},
		{id:'alldebrid', title:'AllDebrid', secret_label:DC.t('API key')},
		{id:'cookies', title:DC.t('Other sites (cookies)'), secret_label:DC.t('cookies.txt content'), needs_host:true}
	];
	function accountInfo(a){
		var i = a.info || {}, out = [];
		if(i.error) return DC.t('Verification failed: {error}', {error:i.error});
		if(i.plan) out.push(i.plan);
		if(i.premium === false) out.push(DC.t('Free account'));
		if(i.expires_at) out.push(DC.t('Expires {date}', {date:DC.fdate(i.expires_at)}));
		if(i.traffic_left !== undefined && i.traffic_left !== null && i.traffic_left >= 0) out.push(DC.t('{size} left to download today', {size:DC.fsize(i.traffic_left)}));
		if(!out.length && i.verified_at) out.push(DC.t('Verified'));
		return out.join(DC.t('; '));
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
			var en = h('input', {type:'checkbox', id:'acEn' + a.id, checked:a.enabled, 'aria-label':DC.t('Enable'), onchange:function(){
				var on = this.checked;
				DC.api.patch('accounts/' + a.id, {enabled:on}).then(function(){ DC.toast(on ? DC.t('Enabled') : DC.t('Disabled')); }, function(e){ DC.toast(DC.errText(e)); });
			}});
			var info = isSite ? DC.t('{host}, account {user}', {host:a.host, user:a.username}) : accountInfo(a);
			return h('div', {'class':'lrow'}, [icon('key'), h('div', null, [h('b', {'class':isSite ? 'mono' : '', text:isSite ? a.host : (a.username || a.host ? DC.t('{service}: {account}', {service:svcById(a.kind).title, account:a.username || a.host}) : svcById(a.kind).title)}),
					h('small', {text:isSite ? DC.t('Account {user}', {user:a.username}) : info || DC.t('Not verified')}),
					h('label', {'class':'toggle', 'for':'acEn' + a.id}, [en, h('span', {text:DC.t('Enable')})])]),
				h('div', {'class':'acts2'}, [
					isSite ? null : ibtn('retry', DC.t('Verify again'), function(){
						DC.api.post('accounts/' + a.id + '/verify', {}).then(function(x){ var t = accountInfo(x.account || {}); DC.toast(t || DC.t('Verified')); reload(); }, function(e){ DC.toast(DC.errText(e)); });
					}),
					ibtn('edit', DC.t('Edit'), function(){ if(isSite) siteForm(a); else hostForm(a); }),
					ibtn('trash', DC.t('Delete'), function(){
						DC.confirm(DC.t('Delete account'), 'key', DC.t('After deletion, this account is no longer used when downloading files from this site.'), DC.t('Delete'), function(){
							DC.api.del('accounts/' + a.id).then(function(){ DC.toast(DC.t('Account deleted')); reload(); }, function(e){ DC.toast(DC.errText(e)); });
						}, true);
					})])]);
		}
		/* One window adds and edits a site account; the password field left empty keeps the stored one. */
		function siteForm(a){
			var isNew = !a;
			a = a || {host:'', username:''};
			DC.modal(isNew ? DC.t('Add account') : DC.t('Edit account'), 'key', [
				h('p', {'class':'lead', text:DC.t('Downloads from this site sign in with this account automatically.')}),
				DC.mform([
					field(DC.t('Site'), DC.t('Host name, e.g. ftp.example.org'), h('input', {type:'text', id:'aHost', value:a.host, autocapitalize:'off', spellcheck:'false'}), 'aHost'),
					field(DC.t('Account'), null, h('input', {type:'text', id:'aUser', value:a.username, autocapitalize:'off', autocomplete:'off'}), 'aUser'),
					field(DC.t('Password'), isNew ? DC.t('Not shown again after saving') : DC.t('Saved; leave empty to keep it'), h('input', {type:'password', id:'aPass', autocomplete:'new-password'}), 'aPass')
				])
			], function(close){
				var ok = btn(null, isNew ? DC.t('Add') : DC.t('Save'), function(){
					var o = {host:DC.val('aHost'), username:DC.val('aUser'), secret:DC.val('aPass')};
					if(!o.host || !o.username){ DC.toast(DC.t('Enter the site and account')); return; }
					DC.busy(ok, true);
					(isNew ? DC.api.post('accounts', {kind:'site', host:o.host, username:o.username, secret:o.secret, enabled:true}) : DC.api.patch('accounts/' + a.id, o)).then(function(){
						close(); DC.toast(isNew ? DC.t('Account added') : DC.t('Account saved')); reload();
					}, function(e){ DC.busy(ok, false); DC.toast(DC.errText(e)); });
				}, 'pri');
				return [btn(null, DC.t('Cancel'), close), ok];
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
				var sv = svcById(id), label = sv.secret_label || DC.t('API key');
				clear(fields);
				if(sv.needs_host || id === 'cookies') fields.appendChild(field(DC.t('Site'), DC.t('Host name, e.g. example-host.com'), h('input', {type:'text', id:'hHost', value:a ? a.host || '' : '', autocapitalize:'off', spellcheck:'false'}), 'hHost'));
				if(sv.needs_username) fields.appendChild(field(DC.t('Account'), null, h('input', {type:'text', id:'hUser', value:a ? a.username || '' : '', autocapitalize:'off', autocomplete:'off'}), 'hUser'));
				fields.appendChild(id === 'cookies'
					? fieldDiv(label, isNew ? DC.t('Paste the content of cookies.txt') : DC.t('Saved; leave empty to keep it'), h('textarea', {id:'hSecret', rows:'4', 'aria-label':label, 'class':'secretarea'}))
					: field(label, isNew ? DC.t('Not shown again after saving') : DC.t('Saved; leave empty to keep it'), h('input', {type:'password', id:'hSecret', autocomplete:'new-password'}), 'hSecret'));
			}
			fill(svc.value);
			DC.modal(isNew ? DC.t('Add file-hosting account') : DC.t('Edit file-hosting account'), 'key', [
				h('p', {'class':'lead', text:DC.t('Links from this site are opened with your account to get the download. The account is yours only.')}),
				DC.mform([field(DC.t('File-hosting services'), null, svc, 'hSvc'), fields]),
				h('p', {'class':'note', text:DC.t('MEGA uses its own encryption and is not supported yet. Free downloads that require a captcha or a countdown are not supported either.')})
			], function(close){
				var ok = btn(null, isNew ? DC.t('Verify and add') : DC.t('Save and verify'), function(){
					var sv = svcById(svc.value), o = {host:DC.val('hHost'), username:DC.val('hUser'), secret:DC.val('hSecret')};
					if(isNew && !o.secret){ DC.toast(DC.t('Enter {field}', {field:sv.secret_label || DC.t('API key')})); return; }
					if(document.getElementById('hHost') && !o.host){ DC.toast(DC.t('Enter the site')); return; }
					DC.busy(ok, true, DC.t('Verifying…'));
					var done = function(x){ var t = accountInfo((x && x.account) || {}); close(); DC.toast(t || (isNew ? DC.t('Added') : DC.t('Account saved'))); reload(); };
					var fail = function(e){ DC.busy(ok, false); DC.toast(DC.errText(e)); };
					if(isNew) DC.api.post('accounts', {kind:sv.id, host:o.host, username:o.username, secret:o.secret, enabled:true}).then(done, fail);
					else DC.api.patch('accounts/' + a.id, o).then(function(){ return DC.api.post('accounts/' + a.id + '/verify', {}); }).then(done, fail);
				}, 'pri');
				return [btn(null, DC.t('Cancel'), close), ok];
			}, {nofocus:true});
			if(isNew) svc.focus(); else if(document.getElementById('hSecret')) document.getElementById('hSecret').focus();
		}
		for(i = 0; i < site.length; i++) list.appendChild(row(site[i], true));
		if(!site.length) list.appendChild(DC.emptyAdd('key', DC.t('No accounts yet.'), DC.t('Add account'), function(){ siteForm(null); }));
		for(i = 0; i < hosts.length; i++) hlist.appendChild(row(hosts[i], false));
		if(!hosts.length) hlist.appendChild(DC.emptyAdd('key', DC.t('No file-hosting accounts yet.'), DC.t('Add file-hosting account'), function(){ hostForm(null); }));
		body.appendChild(sec('key', DC.t('User name and password (HTTP / FTP)'), DC.t('The user name and password are filled in automatically when downloading files from these sites. Passwords are not shown again after saving.'), [list,
			site.length ? DC.addRow(DC.t('Add account'), function(){ siteForm(null); }) : null]));
		body.appendChild(sec('key', DC.t('File-hosting accounts'), DC.t('When you paste links from these sites, your account is used to sign in and get the download address. The account belongs to you only; other users cannot see or use it.'), [hlist,
			hosts.length ? DC.addRow(DC.t('Add file-hosting account'), function(){ hostForm(null); }) : null]));
	}

	/* ---------- Access tokens ---------- */
	var SCOPES = [
		['tasks:read', DC.t('View tasks')], ['tasks:add', DC.t('Add download')], ['tasks:control', DC.t('Start, pause, sort, select files')], ['tasks:remove', DC.t('Remove tasks (keep files)')],
		['files:delete', DC.t('Remove tasks and delete files')], ['stats:read', DC.t('View speed and space')], ['events:read', DC.t('Receive events')], ['notify:manage', DC.t('Manage my notifications')],
		['settings:read', DC.t('Read settings')], ['settings:write', DC.t('Edit settings and schedule')]
	];
	var PRESETS = {read:['tasks:read', 'stats:read', 'events:read'], add:['tasks:read', 'tasks:add', 'stats:read', 'events:read'], full:['tasks:read', 'tasks:add', 'tasks:control', 'tasks:remove', 'stats:read', 'events:read']};
	function scopeLabel(s){ for(var i = 0; i < SCOPES.length; i++) if(SCOPES[i][0] === s) return SCOPES[i][1]; return s; }
	var PRESET_LABEL = {read:DC.t('Read only'), add:DC.t('Add download'), full:DC.t('Full control (no file deletion)')};
	function presetOfScopes(sc){
		var p, k, same;
		for(p in PRESETS) if(PRESETS.hasOwnProperty(p)){
			same = PRESETS[p].length === sc.length;
			for(k = 0; same && k < sc.length; k++) if(PRESETS[p].indexOf(sc[k]) < 0) same = false;
			if(same) return p;
		}
		return '';
	}
	/* AI agents: the skill file ships under docs/skill/; one shell line installs it for the chosen agent and, with a
	   token, writes ~/.config/download-center/config (address and token) for it. */
	var agentKind = 'claude';
	function agentBase(){ return location.protocol + '//' + location.host + location.pathname.replace(/[^\/]*$/, '').replace(/\/+$/, ''); }
	function shq(s){ return "'" + String(s).replace(/'/g, "'\\''") + "'"; }
	function agentCmd(token){
		var dir = (agentKind === 'agents' ? '~/.agents/skills' : '~/.claude/skills') + '/download-center', base = agentBase();
		var cmd = 'mkdir -p ' + dir + (token ? ' ~/.config/download-center' : '') + ' && curl -fsSL ' + shq(base + '/docs/skill/SKILL.md') + ' -o ' + dir + '/SKILL.md';
		if(token) cmd += " && (umask 077; printf 'DC_URL=%s\\nDC_TOKEN=%s\\n' " + shq(base) + ' ' + shq(token) + ' > ~/.config/download-center/config)';
		return cmd;
	}
	/* The agent picker and the command to copy ({field, cmd}); token = null installs or updates the skill alone. */
	function agentInstall(id, token){
		var code = h('code', {text:agentCmd(token)});
		var sel = DC.select(id, [['claude', 'Claude Code'], ['agents', DC.t('Codex, Gemini CLI and others')]], agentKind, function(){ agentKind = this.value; code.textContent = agentCmd(token); });
		return {field:field(DC.t('AI Agent'), null, sel, id), cmd:h('div', {'class':'secret cmd'}, [code, btn('copy', DC.t('Copy'), function(){ DC.copyText(code.textContent, code); })])};
	}
	function reveal(value, regen, agent){
		var code = h('code', {text:value}), inst = agentInstall('tAgent', value);
		DC.modal(regen ? DC.t('Token regenerated') : DC.t('Token created'), 'ticket', [
			h('p', {'class':'lead', text:regen ? DC.t('Copy it now. The full token cannot be shown again after this window closes. The old token no longer works.') : DC.t('Copy it now. The full token cannot be shown again after this window closes.')}),
			h('div', {'class':'secret'}, [code, btn('copy', DC.t('Copy'), function(){ DC.copyText(value, code); })]),
			h('p', {'class':'note', text:DC.t('Usage: add Authorization: Bearer <token> to the request.')}),
			h('details', {'class':'agentuse', open:!!agent}, [h('summary', {text:DC.t('For AI agents')}),
				h('p', {'class':'note', text:DC.t('Run this command in a terminal on the computer where the AI agent runs. It installs the skill file and saves this NAS’s address and the token. On Windows, use Git Bash or WSL.')}),
				DC.mform([inst.field]), inst.cmd])
		], function(close){ return [btn(null, DC.t('I\'ve copied it'), close, 'pri')]; });
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
			if(!toks.length){ list.appendChild(DC.emptyAdd('ticket', DC.t('No tokens yet.'), DC.t('Create token'), function(){ tokenForm(null); })); return; }
			for(var j = 0; j < toks.length; j++){
				(function(t){
					var pills = h('div', {'class':'pills'}), all = h('div', {'class':'pills', hidden:true}), sc = t.scopes || [], pre = presetOfScopes(sc), k, more;
					pills.appendChild(h('span', {text:pre ? PRESET_LABEL[pre] : DC.t('Custom: {n} permissions', {n:sc.length})}));
					if(sc.indexOf('files:delete') >= 0) pills.appendChild(h('span', {'class':'warn', text:scopeLabel('files:delete')}));
					for(k = 0; k < sc.length; k++) all.appendChild(h('span', {'class':sc[k] === 'files:delete' ? 'warn' : '', text:scopeLabel(sc[k])}));
					more = h('button', {'class':'ib linkish pmore', type:'button', 'aria-expanded':'false', onclick:function(){ all.hidden = !all.hidden; more.setAttribute('aria-expanded', all.hidden ? 'false' : 'true'); more.textContent = all.hidden ? DC.t('Show permissions') : DC.t('Hide permissions'); }}, DC.t('Show permissions'));
					pills.appendChild(more);
					if(t.tasks === 'all') pills.appendChild(h('span', {text:DC.t('Everyone\'s tasks')}));
					if(t.folders && t.folders.length) pills.appendChild(h('span', {text:DC.t('Only {list}', {list:t.folders.join(DC.t(', '))})}));
					if(t.ip_allow && t.ip_allow.length) pills.appendChild(h('span', {text:DC.t('Only {list}', {list:t.ip_allow.join(DC.t(', '))})}));
					if(t.sources && t.sources.length) pills.appendChild(h('span', {text:DC.t('Sources: {list}', {list:t.sources.join(DC.t(', '))})}));
					list.appendChild(h('div', {'class':'lrow'}, [icon('ticket'), h('div', null, [h('b', {text:t.name}), h('small', {'class':'mono', text:'dct_' + String(t.id).replace(/^tok_/, '') + '_…'}), pills, all,
						h('small', {text:DC.t('Expires: {expires}. Last used: {used}', {expires:t.expires_at ? DC.fdate(t.expires_at) : DC.t('Never expires'),
							used:t.last_used_at ? DC.t('{time}, {ip}', {time:DC.ftime(t.last_used_at), ip:t.last_used_ip}) + (t.use_count ? DC.t(' ({n} times)', {n:t.use_count}) : '') : DC.t('Not used yet')})})]),
						h('div', {'class':'acts2'}, [ibtn('edit', DC.t('Edit'), function(){ tokenForm(t); }), ibtn('retry', DC.t('Regenerate'), function(){
							DC.confirm(DC.t('Regenerate “{name}”', {name:t.name}), 'ticket', DC.t('The old token stops working immediately. Apps using it must switch to the new token.'), DC.t('Regenerate'), function(){
								DC.api.post('tokens/' + t.id + '/regenerate', {}).then(function(x){ reveal(x.value, true); }, function(e){ DC.toast(DC.errText(e)); });
							}, true);
						}), ibtn('trash', DC.t('Revoke'), function(){
							DC.confirm(DC.t('Revoke “{name}”', {name:t.name}), 'ticket', DC.t('Apps using this token will lose access immediately.'), DC.t('Revoke'), function(){
								DC.api.del('tokens/' + t.id).then(function(){ DC.toast(DC.t('{name} revoked', {name:t.name})); reload(); }, function(e){ DC.toast(DC.errText(e)); });
							}, true);
						})])]));
				})(toks[j]);
			}
		}
		/* One window creates and edits a token. Editing changes what the token may do; the token itself stays valid. */
		function tokenForm(t, agent){
			var isNew = !t, k, folderLim = null;
			t = t || {name:agent ? DC.t('AI Agent') : '', scopes:agent ? PRESETS.full : PRESETS.add, tasks:'own', folders:[], sources:[], ip_allow:[], rate_limit:120};
			var name = h('input', {type:'text', id:'tName', value:t.name, placeholder:DC.t('e.g. Home Assistant')});
			var boxes = {}, scopeWrap = h('div', {'class':'scopes'});
			for(k = 0; k < SCOPES.length; k++){
				if(!admin && SCOPES[k][0].indexOf('settings:') === 0) continue;
				(function(sc){
					var id = 'sc_' + sc[0].replace(':', '_');
					boxes[sc[0]] = h('input', {type:'checkbox', id:id, checked:(t.scopes || []).indexOf(sc[0]) >= 0});
					scopeWrap.appendChild(h('label', {'class':'toggle', 'for':id}, [boxes[sc[0]], h('span', null, [sc[1], sc[0] === 'files:delete' ? h('small', {text:DC.t('Danger: deletes files on the NAS')}) : h('small', {'class':'mono', text:sc[0]})])]));
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
			var presetSel = DC.select('tPreset', [['read', DC.t('Read only')], ['add', DC.t('Add download')], ['full', DC.t('Full control (no file deletion)')], ['custom', DC.t('Custom')]], presetOf(), function(){ if(this.value !== 'custom') applyPreset(this.value); });
			for(k in boxes) if(boxes.hasOwnProperty(k)) boxes[k].onchange = function(){ presetSel.value = presetOf(); };
			var scopeSel = DC.select('tTasks', [['own', DC.t('Only my tasks')], admin ? ['all', DC.t('Everyone\'s tasks')] : null], t.tasks === 'all' ? 'all' : 'own');
			if(admin) folderLim = DC.folderPicker('tFolder', (t.folders || [])[0] || '', true, null, {noneLabel:DC.t('No limit'), noFree:true});
			var expOpts = [['30', DC.t('30 days')], ['90', DC.t('90 days')], ['365', DC.t('1 year')], ['0', DC.t('Never expires')]];
			if(!isNew) expOpts.unshift(['keep', t.expires_at ? DC.t('Keep the current expiry ({date})', {date:DC.fdate(t.expires_at)}) : DC.t('Keep the current expiry (never)')]);
			var exp = DC.select('tExp', expOpts, isNew ? '90' : 'keep');
			var ip = h('input', {type:'text', id:'tIp', value:(t.ip_allow || []).join(', '), placeholder:DC.t('192.168.1.0/24; leave empty for no restriction'), autocapitalize:'off'});
			var rate = DC.num('tRate', t.rate_limit || 120, DC.t('per minute'), {min:'1'});
			var srcs0 = t.sources || [], has = function(x){ return !srcs0.length || srcs0.indexOf(x) >= 0; };
			var srcWrap = h('span', {'class':'inline'}, [
				toggle('tsUrl', DC.t('URL'), null, has('url')), toggle('tsTor', DC.t('Torrent file'), null, has('torrent')), toggle('tsMag', DC.t('Magnet link'), null, has('magnet'))]);
			DC.modal(isNew ? DC.t('Create token') : DC.t('Edit token'), 'ticket', [
				h('p', {'class':'lead', text:DC.t('A token can never do more than your account. It stops working when your account is disabled or your permission to use Download Center is removed in QTS.')}),
				DC.mform([
					field(DC.t('Name'), DC.t('Helps you tell later which app is using it'), name, 'tName'),
					field(DC.t('Permissions'), null, presetSel, 'tPreset'), scopeWrap
				]),
				DC.mform([
					admin ? field(DC.t('Tasks it can manage'), null, scopeSel, 'tTasks') : null,
					admin ? fieldDiv(DC.t('Allowed folders'), DC.t('Downloads can only be saved to this folder or the folders inside it'), folderLim.el) : null,
					fieldDiv(DC.t('Allowed sources'), null, srcWrap),
					field(DC.t('Allowed source IPs'), DC.t('Comma-separated'), ip, 'tIp'),
					field(DC.t('Rate limit'), null, rate, 'tRate'),
					field(DC.t('Expiration'), null, exp, 'tExp')
				])
			], function(close){
				var ok = btn(null, isNew ? DC.t('Create token') : DC.t('Save'), function(){
					var sc = [], key, srcs = [], body2;
					for(key in boxes) if(boxes.hasOwnProperty(key) && boxes[key].checked) sc.push(key);
					if(!name.value){ name.focus(); DC.toast(DC.t('Give the token a name')); return; }
					if(!sc.length){ DC.toast(DC.t('Select at least one permission')); return; }
					if(DC.chk('tsUrl')) srcs.push('url');
					if(DC.chk('tsTor')) srcs.push('torrent');
					if(DC.chk('tsMag')) srcs.push('magnet');
					if(!srcs.length){ DC.toast(DC.t('Allow at least one source')); return; }
					body2 = {name:name.value, scopes:sc, tasks:scopeSel.value, rate_limit:DC.ival('tRate') || 120,
						ip_allow:ip.value ? ip.value.split(/[\s,]+/).filter(function(x){ return !!x; }) : [], sources:srcs.length === 3 ? [] : srcs,
						folders:folderLim && folderLim.value ? [folderLim.value] : []};
					if(exp.value !== 'keep') body2.expires_days = +exp.value;
					DC.busy(ok, true);
					if(isNew) DC.api.post('tokens', body2).then(function(x){ close(); reload(); reveal(x.value, false, agent); }, function(err){ DC.busy(ok, false); DC.toast(DC.errText(err)); });
					else DC.api.patch('tokens/' + t.id, body2).then(function(){ close(); DC.toast(DC.t('Token saved')); reload(); }, function(err){ DC.busy(ok, false); DC.toast(DC.errText(err)); });
				}, 'pri');
				return [btn(null, DC.t('Cancel'), close), ok];
			}, {nofocus:true, wide:true});
			name.focus();
		}
		render();
		add(body, [
			sec('ticket', DC.t('Access tokens'), DC.t('Let other apps (Home Assistant, your own bots, bookmarklets) control downloads as you. Use one token per app and revoke it when no longer needed.'), [
				list, toks.length ? DC.addRow(DC.t('Create token'), function(){ tokenForm(null); }) : null
			]),
			agentSec(function(){ tokenForm(null, true); }),
			devSec()
		]);
	}

	/* AI agents (Claude Code, Codex, Gemini CLI ...) use the REST API through the skill file and a token of their own. */
	function agentSec(create){
		var inst = agentInstall('aAgent', null);
		function step(n, title, text, act){
			return h('div', {'class':'lrow'}, [h('span', {'class':'stepno', text:String(n)}), h('div', null, [h('b', {text:title}), h('small', {text:text})]), act || null]);
		}
		return sec('sparkle', DC.t('AI Agent'), DC.t('With the skill file installed and a token of their own, AI agents such as Claude Code, Codex and Gemini CLI can look up, add and pause downloads for you.'), [
			step(1, DC.t('Create token'), DC.t('For the AI agent alone. It can do no more than your account and cannot delete files by default. When the token expires or is regenerated, run the new setup command.'), btn('plus', DC.t('Create token'), create, 'pri')),
			step(2, DC.t('Run the setup command on your computer'), DC.t('After the token is created, a one-line command is shown. Run it in a terminal on the computer where the AI agent runs. It installs the skill file and saves this NAS’s address and the token to ~/.config/download-center/config.')),
			step(3, DC.t('Start using it'), DC.t('Start a new conversation and just say “download this link to the NAS”, “how far along are my downloads?” or “pause all downloads”.')),
			h('details', {'class':'agentuse'}, [h('summary', {text:DC.t('Install or update the skill by hand')}),
				h('p', {'class':'note', text:DC.t('After updating Download Center, run this command to update the skill file. The token settings stay as they are.')}),
				inst.field, inst.cmd,
				h('p', {'class':'inline agentlinks'}, [h('a', {'class':'linkish', href:'docs/skill/SKILL.md', download:'SKILL.md'}, [icon('down'), DC.t('Download the skill file (SKILL.md)')]),
					h('a', {'class':'linkish', href:'docs/ai-agent.txt', target:'_blank', rel:'noopener'}, [icon('popout'), DC.t('AI agent guide (AI-AGENT.md)')])])])
		]);
	}

	/* Everything a developer needs in one place (the Access tokens page). The integration guide ships with the package as plain text. */
	function devSec(){
		return sec('plug', DC.t('For developers'), DC.t('Use access tokens to call the API or subscribe to events when building your own bot or integration.'), [
			h('dl', {'class':'kv'}, [h('dt', {text:DC.t('API address')}), h('dd', {'class':'mono', text:location.protocol + '//' + location.host + location.pathname + 'api/v1/'}),
				h('dt', {text:DC.t('Verify')}), h('dd', {'class':'mono', text:'Authorization: Bearer dct_…'}),
				h('dt', {text:DC.t('Chat commands')}), h('dd', {'class':'mono', text:'POST /api/v1/commands {"text":"/list"}'}),
				h('dt', {text:DC.t('Event stream')}), h('dd', {'class':'mono', text:'GET /api/v1/events/stream'}),
				h('dt', {text:DC.t('Webhook signature')}), h('dd', {'class':'mono', text:'X-DC-Signature: t=…,v1=…'}),
				h('dt', {text:DC.t('Documentation')}), h('dd', null, h('a', {'class':'linkish', href:'docs/integration.txt', target:'_blank', rel:'noopener'}, [icon('popout'), DC.t('Integration guide (INTEGRATION.md)')]))])
		]);
	}

	/* ---------- Notifications & integrations ---------- */
	var EVENT_LABEL = {
		'task.added':DC.t('Add'), 'task.started':DC.t('Start'), 'task.completed':DC.t('Done'), 'task.seeding_finished':DC.t('Seeding finished'), 'task.moved':DC.t('Moved'), 'task.failed':DC.t('Failed'),
		'task.removed':DC.t('Removed'), 'task.paused':DC.t('Pause'), 'task.resumed':DC.t('Resume'), 'task.merged':DC.t('Merge sources'), 'task.source_switched':DC.t('Switch source'), 'queue.idle':DC.t('All downloads finished'),
		'disk.low':DC.t('Not enough space'), 'schedule.changed':DC.t('Schedule change'), 'account.expiring':DC.t('File-hosting account expiring'), 'engine.down':DC.t('Engine stopped'), 'engine.up':DC.t('Engine recovered'),
		'security.token_created':DC.t('Create token'), 'security.token_rejected':DC.t('Token rejected')
	};
	function opsHelp(s){
		if(s.ops === 'yes') return DC.t('People who have linked their accounts can check, add and pause downloads with commands in the channel');
		if(s.ops === 'https') return DC.t('The NAS must be reachable from outside over HTTPS for messages to come in');
		if(s.ops === 'commands') return DC.t('Events are sent to your URL; to control downloads, your service calls /api/v1/commands');
		return DC.t('This service can only receive notifications');
	}
	function canOperate(s){ return s.ops === 'yes' || s.ops === 'https'; }
	/* A webhook's signing secret, when the server made it: shown once, like a token */
	function secretDialog(value){
		var code = h('code', {text:value});
		DC.modal(DC.t('Webhook signing secret'), 'plug', [
			h('p', {'class':'lead', text:DC.t('Copy it now: it is shown only once. Your service uses it to check the X-DC-Signature of every request.')}),
			h('div', {'class':'secret'}, [code, btn('copy', DC.t('Copy'), function(){ DC.copyText(value, code); })])
		], function(close){ return [btn(null, DC.t('I\'ve copied it'), close, 'pri')]; });
	}
	function pairDialog(c, p){
		var left = Math.max(0, (p.expires_at || 0) - Math.floor(Date.now() / 1000)), timer = h('small', {'class':'num'}), iv, cmd = p.command || ('/link ' + p.code);
		function upd(){ timer.textContent = left > 0 ? DC.t('Valid for {min} min {sec} s', {min:Math.floor(left / 60), sec:DC.pad(left % 60)}) : DC.t('Expired. Generate a new one'); }
		upd(); iv = setInterval(function(){ left--; upd(); if(left <= 0) clearInterval(iv); }, 1000);
		DC.modal(DC.t('Link to “{name}”', {name:c.name}), 'chat', [
			h('p', {'class':'lead', text:DC.t('Send the line below to the bot in this channel. After that, you can control downloads in the channel as {user}.', {user:DC.me()})}),
			h('div', {'class':'code6 num', text:cmd}), timer,
			h('p', {'class':'note', text:DC.t('Commands: /list, /add <URL>, /pause <number>, /resume <number>, /speed, /help')})
		], function(close){ return [btn('copy', DC.t('Copy'), function(){ DC.copyText(cmd); }), btn(null, DC.t('Done'), function(){ clearInterval(iv); close(); }, 'pri')]; }, {onclose:function(){ clearInterval(iv); }});
	}
	function notify(body){
		DC.loadingInto(body);
		DC.api.get('channels', null, {quiet:true}).then(function(r){ clear(body); notifyForm(body, r); }, function(e){
			clear(body);
			if(e.status === 404) body.appendChild(sec('bell', DC.t('Notifications and chat bots'), null, [h('p', {'class':'note', text:DC.t('This version does not support notifications yet.')})]));
			else DC.errorInto(body, e);
		});
	}
	function notifyForm(body, r){
		var admin = DC.isAdmin(), list = h('div'), chans = r.channels || [], svcs = r.services || [], evs = r.events || [], adapters = r.adapters || [];
		function reload(){ notify(clear(body)); }
		function svcById(id){ for(var i = 0; i < svcs.length; i++) if(svcs[i].id === id) return svcs[i]; return {id:id, title:id, fields:[], ops:'no'}; }
		function render(){
			clear(list);
			if(!chans.length){ list.appendChild(svcs.length ? DC.emptyAdd('bell', DC.t('No channels yet.'), DC.t('Add channel'), function(){ channelForm(null); }) : h('p', {'class':'note', text:DC.t('No channels yet.')})); return; }
			for(var j = 0; j < chans.length; j++){
				(function(c, idx){
					var s = svcById(c.service), can = canOperate(s), names = [], k;
					for(k = 0; k < (c.events || []).length; k++) names.push(EVENT_LABEL[c.events[k]] || c.events[k]);
					var opBox = h('input', {type:'checkbox', id:'op' + idx, checked:!!c.operate, disabled:!can, onchange:function(){
						var on = this.checked;
						DC.api.patch('channels/' + c.id, {operate:on}).then(function(x){
							DC.toast(on ? DC.t('“{name}” can now control downloads', {name:c.name}) : DC.t('“{name}” now only receives notifications', {name:c.name}));
							if(on && x.pair) pairDialog(c, x.pair);
							reload();
						}, function(e){ DC.toast(DC.errText(e)); reload(); });
					}});
					var enBox = h('input', {type:'checkbox', id:'chen' + idx, checked:c.enabled !== false, onchange:function(){
						var on = this.checked;
						DC.api.patch('channels/' + c.id, {enabled:on}).then(function(){ DC.toast(on ? DC.t('“{name}” enabled', {name:c.name}) : DC.t('“{name}” disabled', {name:c.name})); }, function(e){ DC.toast(DC.errText(e)); });
					}});
					var extra = [];
					if(c.quiet) extra.push(DC.t('No notifications {hours}', {hours:c.quiet}));
					if(c.digest) extra.push(c.digest >= 1440 ? DC.t('Daily digest') : DC.t('Digest every {n} minutes', {n:c.digest}));
					if(c.fail_count) extra.push(DC.t('Failed {n} times in a row', {n:c.fail_count}));
					if(admin && c.scope === 'all') extra.push(DC.t('Everyone\'s tasks'));
					list.appendChild(h('div', {'class':'lrow'}, [icon(s.id === 'webhook' ? 'plug' : (s.ops === 'no' ? 'bell' : 'chat')), h('div', null, [
						h('b', {text:DC.t('{service}: {name}', {service:s.title, name:c.name})}),
						h('small', {text:extra.length ? DC.t('Notify: {events}. {extra}', {events:names.length ? names.join(DC.t(', ')) : DC.t('Don\'t notify'), extra:extra.join(DC.t('; '))}) : DC.t('Notify: {events}', {events:names.length ? names.join(DC.t(', ')) : DC.t('Don\'t notify')})}),
						h('label', {'class':'toggle', 'for':'chen' + idx}, [enBox, h('span', {text:DC.t('Enable')})]),
						s.ops !== 'no' && s.ops !== 'commands' ? h('label', {'class':'toggle', 'for':'op' + idx}, [opBox, h('span', null, [DC.t('Control downloads in the channel'), h('small', {text:c.operate ? DC.t('{n} users linked', {n:c.linked || 0}) : opsHelp(s)})])]) : h('small', {text:opsHelp(s)}),
						c.operate ? h('button', {'class':'ib linkish', type:'button', onclick:function(){
							DC.api.post('channels/' + c.id + '/pair', {}).then(function(p){ pairDialog(c, p); }, function(e){ DC.toast(DC.errText(e)); });
						}}, [icon('chat'), DC.t('Link my account')]) : null,
						c.operate && c.linked ? h('button', {'class':'ib linkish', type:'button', onclick:function(){ showLinks(c); }}, [icon('user'), DC.t('Linked accounts')]) : null
					]), h('div', {'class':'acts2'}, [
						ibtn('edit', DC.t('Edit'), function(){ channelForm(c); }),
						ibtn('bell', DC.t('Send test notification'), function(){
							DC.api.post('channels/' + c.id + '/test', {}).then(function(x){ DC.toast(x.ok === false ? DC.t('Test failed: {error}', {error:x.error || ''}) : DC.t('Test notification sent')); }, function(e){ DC.toast(DC.errText(e)); });
						}),
						ibtn('files', DC.t('Delivery log'), function(){ showDeliveries(c); }),
						ibtn('trash', DC.t('Delete'), function(){
							DC.confirm(DC.t('Delete “{name}”', {name:c.name}), 'bell', DC.t('This channel will no longer receive notifications, and linked chat accounts will be unlinked.'), DC.t('Delete'), function(){
								DC.api.del('channels/' + c.id).then(function(){ DC.toast(DC.t('“{name}” deleted', {name:c.name})); reload(); }, function(e){ DC.toast(DC.errText(e)); });
							}, true);
						})])]));
				})(chans[j], j);
			}
		}
		function showDeliveries(c){
			var box = h('div', null, h('div', {'class':'loading'}, [icon('check'), DC.t('Loading…')]));
			DC.modal(DC.t('Delivery log: {name}', {name:c.name}), 'files', [box], function(close){ return [btn(null, DC.t('Close'), close, 'pri')]; });
			DC.api.get('channels/' + c.id + '/deliveries').then(function(x){
				var ds = x.deliveries || [], ul = h('ul', {'class':'log num'}), i, d;
				clear(box);
				if(!ds.length){ box.appendChild(h('p', {'class':'note', text:DC.t('No deliveries yet.')})); return; }
				for(i = 0; i < ds.length; i++){
					d = ds[i];
					ul.appendChild(h('li', null, [h('time', {text:DC.ftime(d.time)}), h('span', {text:DC.t('{event}: {result}', {event:EVENT_LABEL[d.event_type] || d.event_type || '', result:
						(d.status === 'ok' || d.status === 'delivered' ? DC.t('Succeeded') : d.status === 'pending' ? DC.t('Waiting to retry') : DC.t('Failed')) + (d.http_status ? DC.t(' (HTTP {code})', {code:d.http_status}) : '') +
						(d.duration_ms ? DC.t('; ') + d.duration_ms + ' ms' : '') + (d.attempt > 1 ? DC.t(', attempt {n}', {n:d.attempt}) : '') + (d.error ? DC.t('; ') + d.error : '')})})]));
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
					if(!ls.length){ box.appendChild(h('p', {'class':'note', text:DC.t('No linked accounts.')})); return; }
					for(i = 0; i < ls.length; i++){
						(function(l){
							box.appendChild(h('div', {'class':'lrow'}, [icon('user'), h('div', null, [h('b', {text:l.qts_user}), h('small', {'class':'mono', text:DC.t('{user}, {time}', {user:l.chat_user, time:DC.ftime(l.created_at)})})]),
								h('div', {'class':'acts2'}, (admin || l.qts_user === DC.me()) ? ibtn('trash', DC.t('Unlink'), function(){
									DC.api.del('channels/' + c.id + '/links/' + encodeURIComponent(l.chat_user)).then(function(){ DC.toast(DC.t('Unlinked')); load(); }, function(e){ DC.toast(DC.errText(e)); });
								}) : null)]));
						})(ls[i]);
					}
				}, function(e){ box.appendChild(h('p', {'class':'note warn', text:DC.errText(e)})); });
			}
			load();
			DC.modal(DC.t('Linked accounts: {name}', {name:c.name}), 'user', [box], function(close){ return [btn(null, DC.t('Close'), close, 'pri')]; });
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
						grid.appendChild(h('button', {'class':'ib svc', type:'button', onclick:function(){ details(sv); }}, [svcBadge(sv), h('span', null, [h('b', {text:sv.title}), h('small', {text:sv.note || opsHelp(sv).replace(/^這個服務/, ''), title:sv.note || opsHelp(sv)})]), canOperate(sv) ? h('span', {'class':'svc-ops', title:DC.t('Downloads can be controlled from the channel')}, icon('chat')) : null]));
					})(svcs[k]);
				}
				clear(step);
				add(step, [h('p', {'class':'lead', text:DC.t('Choose where to send.')}), grid]);
				collect = null;
				if(ok) ok.hidden = true;
			}
			function details(sv){
				var evWrap = h('div', {'class':'inline'}), m, fd, inputs = {}, cfg0 = (c && c.config) || {}, set0 = (c && c.secret_fields_set) || [];
				var n = h('input', {type:'text', id:'chName', value:c ? c.name : '', placeholder:DC.t('e.g. Family group')});
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
					conn.push((fd.type === 'textarea' ? fieldDiv : field)(fd.required && !stored ? fd.label : DC.t('{field} (optional)', {field:fd.label}), stored ? DC.t('Saved; leave empty to keep it') : (fd.help || null), inputs[fd.key], 'chf_' + fd.key));
				}
				var scope = admin ? DC.select('chScope', [['own', DC.t('Only my tasks')], ['all', DC.t('Everyone\'s tasks')]], c && c.scope === 'all' ? 'all' : 'own') : null;
				var quiet = h('input', {type:'text', id:'chQuiet', value:c ? c.quiet || '' : '', placeholder:DC.t('22:00-08:00; leave empty for none')});
				var digest = DC.select('chDigest', [['0', DC.t('Notify for every event')], ['30', DC.t('Digest every 30 minutes')], ['1440', DC.t('Once a day')]], String(c ? c.digest || 0 : 0));
				clear(step);
				add(step, [
					isNew ? h('div', {'class':'lrow svcpick'}, [svcIcon(sv), h('div', null, [h('b', {text:sv.title}), h('small', {text:opsHelp(sv)})]),
						h('div', {'class':'acts2'}, [btn(null, DC.t('Choose another service'), pick)])]) : null,
					DC.mform([field(DC.t('Name'), null, n, 'chName')].concat(conn)),
					DC.mform([
						fieldDiv(DC.t('Events to notify'), null, evWrap),
						sv.ops !== 'no' && sv.ops !== 'commands' ? h('label', {'class':'toggle', 'for':'chOps'}, [ops, h('span', null, [DC.t('Control downloads in the channel'), h('small', {text:opsHelp(sv)})])]) : null,
						scope ? field(DC.t('Scope'), null, scope, 'chScope') : null,
						field(DC.t('Quiet hours'), DC.t('Notifications during this period are sent together when it ends'), quiet, 'chQuiet'),
						field(DC.t('Digest'), DC.t('Avoids flooding when many tasks are added at once'), digest, 'chDigest')
					]),
					sv.ops === 'no' || sv.ops === 'commands' ? h('p', {'class':'note', text:opsHelp(sv)}) : null,
					sv.id === 'webhook' ? h('p', {'class':'note', text:DC.t('Every request carries X-DC-Signature (HMAC-SHA256). Failed deliveries are retried 5 times over 1 minute to 6 hours.')}) : null,
					sv.id === 'line' ? h('p', {'class':'note', text:DC.t('LINE Notify has been discontinued; the Messaging API is used here.')}) : null
				]);
				if(ok) ok.hidden = false;
				collect = function(){
					var ev = [], q, cfg = {}, key, boxes = evWrap.querySelectorAll('input');
					for(q = 0; q < boxes.length; q++) if(boxes[q].checked) ev.push(boxes[q].getAttribute('data-ev'));
					for(q = 0; q < (sv.fields || []).length; q++){
						key = sv.fields[q].key; cfg[key] = inputs[key].value;
						if(sv.fields[q].required && !cfg[key] && !(set0.indexOf(key) >= 0)){ inputs[key].focus(); DC.toast(DC.t('Enter {field}', {field:sv.fields[q].label})); return null; }
					}
					return {service:sv.id, name:n.value || sv.title, config:cfg, events:ev, operate:ops.checked && canOperate(sv), scope:scope ? scope.value : 'own', quiet:quiet.value, digest:+digest.value};
				};
				n.focus();
			}
			DC.modal(isNew ? DC.t('Add channel') : DC.t('Edit channel'), 'bell', [step], function(cl){
				close = cl;
				ok = btn(null, isNew ? DC.t('Save channel') : DC.t('Save'), function(){
					var o = collect && collect();
					if(!o) return;
					DC.busy(ok, true);
					if(isNew){
						o.enabled = true;
						DC.api.post('channels', o).then(function(x){
							close(); reload();
							if(x.secret && o.service === 'webhook' && !(o.config && o.config.secret)) secretDialog(x.secret);
							else if(x.pair) pairDialog(x.channel || {name:o.name}, x.pair); else DC.toast(DC.t('“{name}” added', {name:o.name}));
						}, function(err){ DC.busy(ok, false); DC.toast(DC.errText(err)); });
					}else{
						delete o.service;
						DC.api.patch('channels/' + c.id, o).then(function(x){
							close(); reload();
							if(x && x.pair) pairDialog(c, x.pair); else DC.toast(DC.t('Saved “{name}”', {name:o.name}));
						}, function(err){ DC.busy(ok, false); DC.toast(DC.errText(err)); });
					}
				}, 'pri');
				return [btn(null, DC.t('Cancel'), cl), ok];
			}, {nofocus:true, wide:true});
			if(isNew) pick(); else details(svcById(c.service));
		}
		function adapterSec(){
			if(!admin) return null;
			var al = h('div'), i;
			function importForm(){
				var ta = h('textarea', {id:'adJson', rows:'10', 'class':'secretarea mono', placeholder:'{"adapter": "mattermost", "title": "Mattermost", "fields": [...], "request": {...}}'});
				DC.modal(DC.t('Import adapter'), 'inbox', [
					h('p', {'class':'lead', text:DC.t('Describe an HTTP request in JSON to connect a new service (Mattermost, Teams, WeCom…). Adapters cannot run code.')}), ta
				], function(close){
					var ok = btn(null, DC.t('Import adapter'), function(){
						var m;
						try{ m = JSON.parse(ta.value); }catch(e){ DC.toast(DC.t('Invalid JSON format')); return; }
						DC.busy(ok, true);
						DC.api.post('adapters', {manifest:m}).then(function(){ close(); DC.toast(DC.t('Adapter imported')); reload(); }, function(e){ DC.busy(ok, false); DC.toast(DC.errText(e)); });
					}, 'pri');
					return [btn(null, DC.t('Cancel'), close), ok];
				}, {nofocus:true, wide:true});
				ta.focus();
			}
			for(i = 0; i < adapters.length; i++){
				(function(ad){
					al.appendChild(h('div', {'class':'lrow'}, [icon('plug'), h('div', null, [h('b', {text:ad.title || ad.id}), h('small', {'class':'mono', text:ad.id})]),
						h('div', {'class':'acts2'}, ibtn('trash', DC.t('Delete adapter'), function(){
							DC.api.del('adapters/' + encodeURIComponent(ad.id)).then(function(){ DC.toast(DC.t('Adapter deleted')); reload(); }, function(e){ DC.toast(DC.errText(e)); });
						}))]));
				})(adapters[i]);
			}
			if(!adapters.length) al.appendChild(DC.emptyAdd('plug', DC.t('No adapters yet.'), DC.t('Import adapter'), importForm));
			return sec('inbox', DC.t('Adapters'), DC.t('Describe an HTTP request in JSON to connect a new service (Mattermost, Teams, WeCom…). Adapters cannot run code.'), [al,
				adapters.length ? DC.addRow(DC.t('Import adapter'), importForm) : null]);
		}
		render();
		add(body, [
			sec('bell', DC.t('Notifications and chat bots'), DC.t('Each channel notifies you when the events you choose happen. Supported services (Telegram, and LINE when reachable from outside over HTTPS) can also turn on “Control downloads in the channel” to check, add and pause downloads with commands.'), [list, svcs.length && chans.length ? DC.addRow(DC.t('Add channel'), function(){ channelForm(null); }) : null]),
			admin ? publicSec() : null,
			adapterSec(),
			h('p', {'class':'note'}, [DC.t('The API, event stream and webhook signatures are described under “Access tokens”.') + ' ', h('button', {'class':'ib linkish', type:'button', onclick:function(){ DC.S.setTab = 'token'; DC.S.setOpen = true; DC.renderView(); window.scrollTo(0, 0); }}, DC.t('Go there'))])
		]);
	}

	/* Administrators: the public HTTPS address of the package (setting external_url), used for links in notifications and
	   for LINE's webhook. Saved on its own, like the other choices of this page. */
	function publicSec(){
		var inp = h('input', {type:'url', id:'sExtUrl', placeholder:'https://nas.example.com', autocapitalize:'off', spellcheck:'false'}), save;
		DC.api.get('settings', null, {quiet:true}).then(function(r){ inp.value = (r.settings || {}).external_url || ''; }, function(){});
		save = btn(null, DC.t('Save'), function(){
			var v = inp.value.trim().replace(/\/+$/, '').replace(/\/downloadcenter$/i, '');
			if(v && !/^https:\/\/[^\/\s]+/i.test(v)){ inp.focus(); DC.toast(DC.t('Enter an address that starts with https://')); return; }
			DC.busy(save, true);
			DC.api.put('settings', {external_url:v}).then(function(){ DC.busy(save, false); inp.value = v; DC.toast(DC.t('Settings saved')); }, function(e){ DC.busy(save, false); DC.toast(DC.errText(e)); });
		});
		return sec('link', DC.t('Address from outside'), DC.t('The HTTPS address of this NAS from the internet, for example https://nas.example.com. Links in notifications use it, and LINE needs it to control downloads in a chat.'), [
			field(DC.t('Address'), null, [inp, save], 'sExtUrl')
		]);
	}

	/* ---------- Import from official version ---------- */
	function importer(body){
		DC.loadingInto(body);
		DC.api.get('import', null, {quiet:true}).then(function(r){ clear(body); importForm(body, r); }, function(e){
			clear(body);
			body.appendChild(sec('inbox', DC.t('Import from the official Download Station'), null, [h('p', {'class':'note', text:e.status === 404 ? DC.t('This version does not support import yet.') : DC.errText(e)})]));
		});
	}
	function importForm(body, r){
		var vols = r.volumes || [], unfinished = 0, completed = 0, accts = 0, i, missing = r.users_missing || [], off = r.official || {}, running = !!(off.running || off.enabled), res = h('div', {role:'status'}), go;
		for(i = 0; i < vols.length; i++){ unfinished += vols[i].unfinished || 0; completed += vols[i].completed || 0; accts += vols[i].accounts || 0; }
		if(!r.available){ body.appendChild(sec('inbox', DC.t('Import from the official Download Station'), null, [h('p', {'class':'note', text:DC.t('No official Download Station data found.')})])); return; }
		var userBoxes = h('div');
		for(i = 0; i < missing.length; i++) userBoxes.appendChild(toggle('imU' + i, DC.t('Allow {name} to use Download Center', {name:missing[i]}), DC.t('They have tasks in the official version but no permission to use Download Center in QTS yet'), true));
		body.appendChild(sec('inbox', DC.t('Import from the official Download Station'), DC.t('Import only reads the official data and never changes or deletes it.'), [
			h('ol', {'class':'steps'}, [h('li', {'class':running ? 'on' : '', text:DC.t('1 Stop official version')}), h('li', {'class':running ? '' : 'on', text:DC.t('2 Select items and import')}), h('li', {text:DC.t('3 Verify')})]),
			running ? h('div', null, [
				h('p', {'class':'note warn', text:DC.t('The official Download Station is still running. Stop it before importing so that both don\'t write to the same temporary files.')}),
				h('div', {'class':'savebar'}, [btn(null, DC.t('Stop the official Download Station'), function(e){
					var b = e.currentTarget;
					DC.confirm(DC.t('Stop the official Download Station'), 'inbox', DC.t('The official version will be stopped and disabled in App Center (it won\'t start at boot, so both don\'t download the same task). You can enable it again in App Center later.'), DC.t('Stop'), function(){
						DC.busy(b, true);
						DC.api.post('import/stop-official', {}).then(function(){ DC.toast(DC.t('Official Download Station stopped')); importer(clear(body)); }, function(err){ DC.busy(b, false); DC.toast(DC.errText(err)); });
					}, true);
				}, 'dan')])
			]) : null,
			toggle('imCfg', DC.t('Settings'), r.settings && r.settings.found ? DC.t('Folders, concurrent downloads, speed limits, torrent settings, schedule') : DC.t('Official settings file not found'), !!(r.settings && r.settings.found)),
			toggle('imRun', DC.t('{n} unfinished tasks', {n:unfinished}), DC.t('Torrent tasks recheck the downloaded data, which takes a while for large files; URL tasks resume from the downloaded size'), unfinished > 0),
			toggle('imDone', DC.t('{n} finished records', {n:completed}), DC.t('Import records only, don\'t seed again'), false),
			toggle('imAcct', DC.t('{n} site accounts', {n:accts}), null, accts > 0),
			userBoxes,
			r.last ? h('p', {'class':'note', text:r.last.summary ? DC.t('Last import: {time}. {summary}', {time:DC.ftime(r.last.time), summary:summaryText(r.last.summary)}) : DC.t('Last import: {time}', {time:DC.ftime(r.last.time)})}) : null,
			res,
			h('div', {'class':'savebar'}, [go = btn('inbox', DC.t('Import'), function(){
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
		var steps = [DC.t('Reading official settings and tasks…'), DC.t('Add unfinished tasks…'), DC.t('Import site accounts and records…'), DC.t('Preparing to recheck downloaded data…')], si = 0, t0 = new Date().getTime(), done = false, timer, closeW;
		var stepEl = h('b', {'class':'imp-step', text:steps[0]}), timeEl = h('span', {'class':'note num', text:DC.t('0 s')});
		var area = h('div', {'class':'imp', role:'status', 'aria-live':'polite'}, [
			h('div', {'class':'imp-orb', 'aria-hidden':'true'}, [h('i'), h('i'), h('i'), icon('inbox')]),
			stepEl, h('div', {'class':'imp-bar', 'aria-hidden':'true'}, h('span')), timeEl,
			h('p', {'class':'note', text:DC.t('The official data is only read, never changed. Closing this window does not stop the import.')})]);
		var acts = h('div');
		closeW = DC.modal(DC.t('Importing'), 'inbox', [area], function(close){ return [acts]; }, {nofocus:true, onclose:function(){ clearInterval(timer); }});
		timer = setInterval(function(){
			var sec = Math.round((new Date().getTime() - t0) / 1000);
			timeEl.textContent = DC.t('{n} s', {n:sec});
			if(sec % 3 === 0 && si < steps.length - 1){ si++; stepEl.textContent = steps[si]; }
		}, 1000);
		function reloadIn(n, lead){
			var left = n, note = h('p', {'class':'note num'});
			function tick(){ note.textContent = lead + DC.t('Refreshing the page in {n} s.', {n:left}); if(left-- <= 0) location.reload(); }
			area.appendChild(note); tick();
			clearInterval(timer); timer = setInterval(tick, 1000);
			clear(acts); acts.appendChild(btn(null, DC.t('Refresh now'), function(){ location.reload(); }, 'pri'));
		}
		DC.api.post('import', req, {timeout:600000, quiet:true}).then(function(x){
			done = true;
			var sm = x.summary || {}, w = sm.warnings || [], j;
			clear(area); area.className = 'imp ok';
			area.appendChild(h('div', {'class':'imp-orb', 'aria-hidden':'true'}, icon('done', 'play')));
			area.appendChild(h('b', {'class':'imp-step', text:DC.t('Import finished')}));
			area.appendChild(h('p', {'class':'note', text:summaryText(sm) || DC.t('Nothing to import.')}));
			for(j = 0; j < w.length; j++) area.appendChild(h('p', {'class':'note warn', text:w[j]}));
			if(DC.importDone) DC.importDone();
			reloadIn(w.length ? 8 : 3, '');
		}, function(e){
			done = true;
			if(e.code === 'timeout' || e.code === 'network'){
				clear(area); area.className = 'imp';
				area.appendChild(h('b', {'class':'imp-step', text:DC.t('Import is still running on the NAS')}));
				area.appendChild(h('p', {'class':'note', text:DC.t('The page waited too long for a response; the import is not interrupted. Refresh to see the current import results.')}));
				reloadIn(5, '');
				return;
			}
			clearInterval(timer);
			clear(area); area.className = 'imp bad';
			area.appendChild(h('div', {'class':'imp-orb', 'aria-hidden':'true'}, icon('error', 'play')));
			area.appendChild(h('b', {'class':'imp-step', text:DC.t('Not imported')}));
			area.appendChild(h('p', {'class':'note', text:DC.errText(e)}));
			clear(acts); acts.appendChild(btn(null, DC.t('Close'), closeW, 'pri'));
		});
	}
	/* Field names of the importer's Summary (daemon/internal/importer) */
	function summaryText(sm){
		var parts = [];
		if(sm.settings) parts.push(DC.t('Settings imported'));
		if(sm.tasks_added) parts.push(DC.t('Add {n} tasks', {n:sm.tasks_added}));
		if(sm.history_added) parts.push(DC.t('{n} completed records', {n:sm.history_added}));
		if(sm.accounts_added) parts.push(DC.t('{n} site accounts', {n:sm.accounts_added}));
		if(sm.users_added) parts.push(DC.t('Allowed {n} users', {n:sm.users_added}));
		if(sm.tasks_skipped) parts.push(DC.t('Skipped {n} existing tasks', {n:sm.tasks_skipped}));
		return parts.join(DC.t('; '));
	}

	/* First use next to the official package: an administrator is asked once (pref "import_offer") whether to bring its data
	   over, and is taken to the import page or told where to find it later. /me only hints (checked at start-up);
	   GET /import decides, so nothing is offered once something has been imported. */
	function offerImport(){
		var me = DC.S.me;
		if(!me || !me.admin || me.via !== 'session' || !me.import_available || DC.pref('import_offer')) return;
		DC.api.get('import', null, {quiet:true}).then(function(r){
			var vols = r.volumes || [], unfinished = 0, completed = 0, accts = 0, i, off = r.official || {};
			if(!r.available) return;
			for(i = 0; i < vols.length; i++){ unfinished += vols[i].unfinished || 0; completed += vols[i].completed || 0; accts += vols[i].accounts || 0; }
			var where = DC.t('Settings') + ' › ' + DC.t('Import from official version');
			DC.modal(DC.t('Import from Download Station?'), 'inbox', [
				h('p', {'class':'lead', text:DC.t('Download Station is installed on this NAS. Download Center can bring over its settings, tasks and site accounts. The import only reads its data and never changes it.')}),
				h('p', {'class':'note', text:DC.t('Found: {unfinished} unfinished tasks, {completed} finished records, {accounts} site accounts.', {unfinished:unfinished, completed:completed, accounts:accts})}),
				off.running || off.enabled ? h('p', {'class':'note warn', text:DC.t('Download Station is still running. The import page shows you how to stop it first.')}) : null
			], function(close){
				return [btn(null, DC.t('Not now'), function(){
					DC.savePref('import_offer', 1);
					close();
					DC.modal(DC.t('You can import later'), 'inbox', [
						h('p', {'class':'lead', text:DC.t('Whenever you are ready, open {place}. It stays there until you have imported.', {place:where})})
					], function(close2){ return [btn(null, DC.t('Close'), close2, 'pri')]; });
				}), btn(null, DC.t('Import now'), function(){
					DC.savePref('import_offer', 1);
					close();
					DC.leave(function(){ DC.S.setTab = 'import'; DC.S.setOpen = true; DC.go('settings'); window.scrollTo(0, 0); });
				}, 'pri')];
			}, {persist:true});
		}, function(){});
	}
	DC.offerImport = offerImport;

	DC.setMore = {accounts:accounts, tokens:tokens, notify:notify, importer:importer};
})();
