/* Add flow: one paste field (any kind of link), then a dialog that decides files, folders, account and auto-removal. */
(function(){
	'use strict';
	var DC = window.DC, h = DC.h, add = DC.add, clear = DC.clear, icon = DC.icon, ibtn = DC.ibtn, btn = DC.btn, R = DC.R;

	var KIND_ICON = {empty:'link', url:'link', mixed:'link', page:'link', magnet:'magnet', torrent:'torrent', bad:'error'};
	var KIND_TEXT = {url:DC.t('網址'), page:DC.t('網頁'), magnet:DC.t('磁力'), torrent:DC.t('種子檔')};
	var UNSUPPORTED = {'mega.nz':'MEGA', 'mega.co.nz':'MEGA'};

	function hostOf(u){ var m = /^[a-z]+:\/\/(?:[^@\/]*@)?(?:www\.)?([^\/:?#]+)/i.exec(u); return m ? m[1].toLowerCase() : ''; }
	function hostMatch(host, list){
		var i;
		for(i = 0; i < list.length; i++) if(host === list[i] || host.slice(-list[i].length - 1) === '.' + list[i]) return true;
		return false;
	}
	/* File-hosting link: {name, acct} where acct says whether the user has an account for it (unknown = true). */
	function hosterOf(u){
		var host = hostOf(u), k, svcs = (DC.S.me && DC.S.me.hosters) || [], i, s;
		if(!host) return null;
		for(k in UNSUPPORTED) if(host === k || host.slice(-k.length - 1) === '.' + k) return {name:UNSUPPORTED[k], acct:false, unsupported:true};
		for(i = 0; i < svcs.length; i++){
			s = svcs[i];
			if(s.hosts && hostMatch(host, s.hosts)) return {name:s.title || s.id, id:s.id, acct:true};
		}
		return null;
	}
	function nameFrom(u){
		var m = /[?&]dn=([^&]+)/.exec(u), p;
		try{
			if(m) return decodeURIComponent(m[1].replace(/\+/g, ' '));
			p = u.replace(/[?#].*$/, '').split('/');
			return decodeURIComponent(p[p.length - 1] || p[2] || u).replace(/\.torrent$/i, '');
		}catch(e){ return u; }
	}
	/* thunder:// (Xunlei), flashget:// and qqdl:// carry the real URL in base64; the server decodes them too. A URL that is not
	   UTF-8 (GBK file names) keeps its bytes, percent-encoded. */
	function unwrap(u){
		var i, m, b, s, head, tail, out;
		for(i = 0; i < 3; i++){
			m = /^(thunder|flashget|qqdl):\/\/([^&]+)/i.exec(u);
			if(!m) return u;
			head = {thunder:'AA', flashget:'[FLASHGET]'}[m[1].toLowerCase()] || '';
			tail = {thunder:'ZZ', flashget:'[FLASHGET]'}[m[1].toLowerCase()] || '';
			try{ b = window.atob(decodeURIComponent(m[2]).replace(/\/+$/, '').replace(/-/g, '+').replace(/_/g, '/')); }catch(e){ return u; }
			if(b.length < head.length + tail.length || b.slice(0, head.length) !== head || b.slice(b.length - tail.length) !== tail) return u;
			b = b.slice(head.length, b.length - tail.length).replace(/^\s+|\s+$/g, '');
			try{ s = decodeURIComponent(escape(b)); }catch(e2){
				out = [];
				for(m = 0; m < b.length; m++) out.push(b.charCodeAt(m) >= 0x80 ? '%' + b.charCodeAt(m).toString(16).toUpperCase() : b.charAt(m));
				s = out.join('');
			}
			if(!s) return u;
			u = s;
		}
		return u;
	}
	function detect(text){
		var re = /(magnet:\?[^\s"'<>]+|(?:https?|s?ftps?|scp|thunder|flashget|qqdl):\/\/[^\s"'<>]+)/ig, m, out = [], seen = {}, kinds = {}, keys = [], k, l, host;
		if(!text.replace(/\s+/g, '')) return {kind:'empty', items:[]};
		while((m = re.exec(text))){
			l = unwrap(m[1].replace(/[),.;。，、）」』]+$/, ''));
			if(seen[l]) continue;
			seen[l] = 1;
			host = /^magnet:/i.test(l) ? null : hosterOf(l);
			if(/^magnet:/i.test(l)) k = 'magnet';
			else if(/\.torrent(\?.*)?$/i.test(l)) k = 'torrent';
			else if(!host && /^https?:/i.test(l) && /(\/|\.html?|\.php|\.aspx?)$/i.test(l.replace(/[?#].*$/, ''))) k = 'page';
			else k = 'url';
			out.push({kind:k, text:l, name:nameFrom(l), host:host}); kinds[k] = 1;
		}
		if(!out.length) return {kind:'bad'};
		for(k in kinds) keys.push(k);
		return {kind:keys.length === 1 ? keys[0] : 'mixed', items:out, loose:text.replace(re, '').replace(/\s+/g, '').length > 0};
	}

	/* ---------- paste field: inline on wide screens, in a sheet on phones ---------- */
	/* on (optional) replaces the default handlers: {type(), enter(), files(fileList), paste()} */
	function pasteBox(sheet, on){
		var pb = {lastKind:null, sheet:sheet};
		on = on || {};
		pb.kind = h('div', {'class':'kind'}, icon('link'));
		pb.input = h('textarea', {'class':'addInput', rows:'1', 'aria-label':DC.t('要下載的網址或磁力連結'), placeholder:DC.t('貼上網址或磁力連結'), autocapitalize:'off', autocomplete:'off', spellcheck:'false', enterkeyhint:'go',
			oninput:function(){ onInput(pb); if(on.type) on.type(); }, onkeydown:function(e){ if(e.key === 'Enter' && !e.shiftKey){ e.preventDefault(); if(on.enter) on.enter(); else submit(pb); } },
			onpaste:function(){ if(on.paste) setTimeout(function(){ onInput(pb); on.paste(); }, 0); }});
		/* No accept filter: iOS greys out .torrent files it has no type for, so the extension is checked after picking. */
		pb.file = h('input', {type:'file', hidden:true, multiple:true, onchange:function(){
			var files = this.files;
			DC.track('add_pick');
			if(on.files) on.files(files); else openTorrents(files);
			this.value = '';
		}});
		pb.hint = h('div', {'class':'addHint'});
		return pb;
	}
	/* What this NAS can download, from the engines' capabilities (a missing engine drops its kinds) */
	function protocols(){
		var me = DC.S.me || {}, c = me.url_caps || {}, out = [];
		if(c.urls) out.push('HTTP', 'HTTPS');
		if(c.ftp) out.push('FTP', 'FTPS');
		if(c.sftp) out.push('SFTP');
		if(c.scp) out.push('SCP');
		if(me.bt_engine) out.push(DC.t('磁力連結'), DC.t('種子檔'));
		return out.join(DC.t('、'));
	}
	function hintIdle(pb){
		var list = protocols();
		clear(pb.hint).className = 'addHint';
		if(!pb.sheet) pb.hint.appendChild(h('span', {text:DC.t('可以貼一個或多個網址、磁力連結、一段含連結的文字或網頁網址，也可以把 .torrent 檔拖進視窗。')}));
		if(list) pb.hint.appendChild(h('span', {'class':'protos', text:DC.t('支援：{list}', {list:list})}));
	}
	function onInput(pb){
		var el = pb.input, d = detect(el.value), one, hint = pb.hint;
		el.style.height = 'auto'; el.style.height = Math.min(el.scrollHeight, pb.sheet ? 200 : 120) + 'px';
		if(pb.go) pb.go.disabled = d.kind === 'empty' || d.kind === 'bad';
		if(d.kind !== pb.lastKind){
			pb.lastKind = d.kind;
			pb.kind.className = 'kind k-' + d.kind;
			clear(pb.kind).appendChild(icon(KIND_ICON[d.kind], d.kind === 'empty' ? '' : 'play'));
		}
		if(d.kind === 'empty'){ hintIdle(pb); return; }
		hint.className = 'addHint' + (d.kind === 'bad' ? ' bad' : '');
		one = d.items && d.items.length === 1 ? d.items[0] : null;
		if(d.kind === 'bad') hint.textContent = DC.t('沒有找到可以下載的連結。支援：{list}。', {list:protocols()});
		else if(!one) hint.textContent = d.loose ? DC.t('從文字中找到 {n} 個連結，在下方勾選要下載的項目。', {n:d.items.length}) : DC.t('{n} 個連結，在下方勾選要下載的項目。', {n:d.items.length});
		else if(one.kind === 'page') hint.textContent = DC.t('這是網頁，下方會列出頁面裡的下載連結。');
		else if(one.kind === 'magnet') hint.textContent = DC.t('選好存放位置就能按「開始下載」；檔案清單取得後可以挑選要下載的檔案。');
		else if(one.kind === 'torrent') hint.textContent = DC.t('這是 .torrent 檔的網址，會先下載這個檔案。');
		else if(one.host) hint.textContent = one.host.unsupported ? DC.t('{service} 目前不支援。', {service:one.host.name}) : DC.t('會用你的 {service} 帳號登入後下載。', {service:one.host.name});
		else hint.textContent = DC.t('選好存放位置後按「開始下載」。');
	}
	function submit(pb){
		var d = detect(pb.input.value);
		if(d.kind === 'empty' || d.kind === 'bad'){ pb.input.focus(); return; }
		DC.track(d.loose && d.items.length > 1 ? 'add_text' : 'add_paste');
		if(d.loose && d.items.length > 1){
			/* Pasted text: let the server classify the links (file hosts, pages) */
			DC.api.post('tasks/extract', {text:pb.input.value}).then(function(r){
				var items = [], i, l;
				for(i = 0; i < (r.links || []).length; i++){
					l = r.links[i];
					items.push({kind:l.kind, text:l.url, name:l.name || nameFrom(l.url), host:l.hoster ? {name:l.hoster, acct:true} : (l.kind === 'magnet' ? null : hosterOf(l.url))});
				}
				openAdd(items.length ? items : d.items);
			}, function(){ openAdd(d.items); });
			return;
		}
		openAdd(d.items);
	}
	function openTorrents(fileList){
		var files = [], bad = [], i;
		for(i = 0; i < (fileList ? fileList.length : 0); i++){
			if(/\.torrent$/i.test(fileList[i].name)) files.push(fileList[i]);
			else bad.push(fileList[i].name);
		}
		if(bad.length && !files.length){ DC.toast(DC.t('{file} 不是 .torrent 檔', {file:bad[0]})); return; }
		if(!files.length) return;
		var items = [];
		for(i = 0; i < files.length; i++) items.push({kind:'torrentfile', file:files[i], text:files[i].name, name:files[i].name.replace(/\.torrent$/i, '')});
		openAdd(items);
	}

	/* ---------- folder picker: an inline panel under its button, so it works inside dialogs. A row takes that folder, the
	   arrow beside it opens it, the path on top leads back up; it opens on the folder that holds the current choice. A
	   folder that is gone gives way to the nearest one that still exists. opts: noFree (the caller shows free space
	   itself), noneLabel (the choice for no folder, with allowNone). ---------- */
	var FP_FILTER = 30, FP_MAX = 300;
	function parentOf(p){ return p && p.indexOf('/') >= 0 ? p.replace(/\/[^\/]*$/, '') : ''; }
	function folderPicker(id, initial, allowNone, onChange, opts){
		opts = opts || {};
		var fp = {value:initial || ''}, gen = 0, cur = null, busyT = null;
		var noneLabel = opts.noneLabel || DC.t('不移動（留在暫存位置）');
		var label = h('span', {'class':'fp-path'});
		var trigger = h('button', {'class':'ib fpick-btn', type:'button', id:id, 'aria-expanded':'false', 'aria-controls':id + 'Panel',
			onclick:function(){ if(panel.hidden) open(parentOf(fp.value), fp.value); else close(false); }}, [icon('folder'), label, icon('chev', 'chev')]);
		var back = ibtn('back', DC.t('上一層'), function(){ if(cur && cur.path) open(parentOf(cur.path), cur.path); });
		var crumbs = h('div', {'class':'fpcrumbs'});
		var note = h('p', {'class':'note warn', role:'status', hidden:true});
		var filter = h('input', {type:'search', 'class':'fpfilter', placeholder:DC.t('篩選資料夾'), 'aria-label':DC.t('篩選資料夾'), hidden:true,
			oninput:function(){ if(cur) fill(cur.r.folders || [], null); }});
		var list = h('div', {'class':'fplist'});
		var free = h('p', {'class':'note num fpfree', hidden:true});
		var msg = h('span', {'class':'fpmsg'});
		var noneB = allowNone ? h('button', {'class':'ib linkish', type:'button', onclick:function(){ set(''); }}, [icon('close'), noneLabel]) : null;
		var newB = h('button', {'class':'ib linkish', type:'button', hidden:true, onclick:function(){ newB.hidden = true; newRow.hidden = false; newIn.value = ''; newIn.focus(); }}, [icon('plus'), DC.t('新增資料夾')]);
		var useB = btn(null, '', function(){ if(cur && cur.path) set(cur.path); }, 'pri');
		useB.hidden = true;
		var newIn = h('input', {type:'text', maxlength:'255', placeholder:DC.t('新資料夾名稱'), 'aria-label':DC.t('新資料夾名稱'), autocapitalize:'off',
			onkeydown:function(e){ if(e.key === 'Enter'){ e.preventDefault(); create(); } }});
		var createB = btn(null, DC.t('建立'), function(){ create(); }, 'pri');
		var newRow = h('div', {'class':'fpnew', hidden:true}, [newIn, createB, btn(null, DC.t('取消'), function(){ hideNew(true); })]);
		var foot = h('div', {'class':'fpfoot'}, [noneB, newB, msg, useB]);
		var panel = h('div', {'class':'fpick', id:id + 'Panel', role:'group', 'aria-label':DC.t('選擇資料夾'), hidden:true}, [
			h('div', {'class':'fphead'}, [back, crumbs]), note, filter, list, free,
			foot, newRow]);

		function paint(){
			var v = fp.value, k = v.lastIndexOf('/');
			clear(label);
			trigger.title = v;
			if(!v){ label.className = 'fp-path none'; label.textContent = allowNone ? noneLabel : DC.t('請選擇'); return; }
			label.className = 'fp-path mono';
			if(k >= 0) label.appendChild(h('span', {'class':'fp-dir', text:v.slice(0, k + 1)}));
			label.appendChild(h('span', {'class':'fp-leaf', text:v.slice(k + 1)}));
		}
		function set(v){
			fp.value = v; paint(); close(true);
			if(onChange) onChange(v);
		}
		function close(focus){
			gen++; clearTimeout(busyT);
			panel.hidden = true; panel.classList.remove('busy');
			trigger.setAttribute('aria-expanded', 'false');
			hideNew(false);
			if(focus) trigger.focus();
		}
		function hideNew(focus){ newRow.hidden = true; newB.hidden = !canMake(); if(focus) newB.focus(); }
		function canMake(){ return !!(cur && cur.path && cur.r.choosable !== false && DC.isAdmin()); }
		/* path: the folder to show; focusPath: the row to focus (the folder we came from, or the current choice);
		   missing: a folder that turned out not to exist on the way here */
		function open(path, focusPath, missing){
			var my = ++gen;
			if(panel.hidden){ cur = null; panel.hidden = false; trigger.setAttribute('aria-expanded', 'true'); }
			clearTimeout(busyT);
			if(cur) busyT = setTimeout(function(){ panel.classList.add('busy'); }, 150);
			else{
				crumbsFor(path); back.hidden = !path;
				note.hidden = filter.hidden = free.hidden = foot.hidden = newRow.hidden = true;
				clear(list).appendChild(h('div', {'class':'loading'}, [icon('check'), DC.t('讀取中…')]));
			}
			panel.setAttribute('aria-busy', 'true');
			DC.api.get('folders', path ? {path:path} : null).then(function(r){
				if(my === gen) render(path, r, focusPath, missing);
			}, function(e){
				if(my !== gen) return;
				if(path && (e.code === 'folder_not_found' || e.code === 'folder_not_allowed')) open(parentOf(path), null, missing || path);
				else failed(path, e);
			});
		}
		function done(){ clearTimeout(busyT); panel.classList.remove('busy'); panel.removeAttribute('aria-busy'); }
		function render(path, r, focusPath, missing){
			var here = !!path && r.choosable !== false, target;
			done();
			cur = {path:path, r:r};
			crumbsFor(path);
			back.hidden = !path;
			note.hidden = !missing;
			note.textContent = missing ? DC.t('找不到「{path}」，改為顯示上一層。', {path:missing}) : '';
			filter.value = '';
			filter.hidden = (r.folders || []).length <= FP_FILTER;
			target = fill(r.folders || [], focusPath);
			free.hidden = !(path && r.free >= 0 && !opts.noFree);
			free.textContent = free.hidden ? '' : DC.t('還有 {size} 可用。', {size:DC.fsize(r.free)});
			useB.hidden = !here;
			useB.lastChild.textContent = here ? DC.t('使用「{name}」', {name:path.slice(path.lastIndexOf('/') + 1)}) : '';
			msg.textContent = !path ? '' : r.writable === false ? DC.t('這個資料夾無法寫入，請選擇其他資料夾。') : !here ? DC.t('請從下面選一個資料夾。') : '';
			hideNew(false);
			foot.hidden = !(noneB || here || msg.textContent || !newB.hidden);
			if(target){ target.focus({preventScroll:true}); if(target.scrollIntoView) target.scrollIntoView({block:'nearest'}); }
		}
		function failed(path, e){
			done();
			crumbsFor(path);
			back.hidden = !path;
			note.hidden = true; filter.hidden = true; free.hidden = true; useB.hidden = true; newB.hidden = true; newRow.hidden = true; msg.textContent = '';
			foot.hidden = !noneB;
			clear(list).appendChild(h('div', {'class':'fperr'}, [h('p', {'class':'note warn', text:DC.errText(e)}), btn('retry', DC.t('重試'), function(){ open(path, null); })]));
		}
		/* The rows matching the filter, at most FP_MAX of them; returns the row to focus (focusPath's, else the first) */
		function fill(fs, focusPath){
			var q = filter.value.replace(/^\s+|\s+$/g, '').toLowerCase(), hits = [], i, row, first = null, want = null;
			for(i = 0; i < fs.length; i++) if(!q || fs[i].name.toLowerCase().indexOf(q) >= 0) hits.push(fs[i]);
			clear(list);
			for(i = 0; i < hits.length && i < FP_MAX; i++){
				row = rowOf(hits[i]);
				list.appendChild(row);
				if(!first) first = row.firstChild;
				if(focusPath && hits[i].path === focusPath) want = row.firstChild;
			}
			if(hits.length > FP_MAX) list.appendChild(h('p', {'class':'note', text:DC.t('還有 {n} 個資料夾沒有列出，請用篩選縮小範圍。', {n:hits.length - FP_MAX})}));
			if(!hits.length) list.appendChild(h('p', {'class':'note', text:q ? DC.t('沒有符合的資料夾。') : DC.t('沒有子資料夾。')}));
			return want || first;
		}
		function rowOf(f){
			var sel = !!fp.value && f.path === fp.value, ok = f.choosable !== false, meta = '';
			if(f.writable === false) meta = DC.t('唯讀');
			else if(sel) meta = DC.t('目前選擇');
			else if(f.free >= 0) meta = DC.t('{size} 可用', {size:DC.fsize(f.free)});
			return h('div', {'class':'fprow' + (sel ? ' sel' : '') + (ok ? '' : ' ro')}, [
				h('button', {'class':'ib fpitem', type:'button', 'aria-current':sel ? 'true' : null, onclick:function(){ if(ok) set(f.path); else open(f.path, null); }},
					[icon(f.writable === false ? 'lock' : sel ? 'done' : 'folder'), h('span', {'class':'nm', text:f.name}), meta ? h('small', {'class':'num', text:meta}) : null]),
				h('button', {'class':'fpgo', type:'button', 'aria-label':DC.t('打開「{name}」', {name:f.name}), title:DC.t('打開「{name}」', {name:f.name}), onclick:function(){ open(f.path, null); }}, icon('chev'))
			]);
		}
		/* 共用資料夾 › Share › … › Parent › Folder: long paths keep their first folder and the last two */
		function crumbsFor(path){
			var segs = path ? path.split('/') : [], items = [], i;
			clear(crumbs);
			items.push(crumb(DC.t('共用資料夾'), '', segs[0] || null, !segs.length));
			for(i = 0; i < segs.length; i++){
				if(segs.length > 4 && i >= 1 && i < segs.length - 2){
					if(i === 1) items.push(h('span', {'class':'fpell', 'aria-hidden':'true', text:'…'}));
					continue;
				}
				items.push(crumb(segs[i], segs.slice(0, i + 1).join('/'), i + 1 < segs.length ? segs.slice(0, i + 2).join('/') : null, i === segs.length - 1));
			}
			for(i = 0; i < items.length; i++){
				if(i) crumbs.appendChild(h('span', {'class':'sep', 'aria-hidden':'true', text:'›'}));
				crumbs.appendChild(items[i]);
			}
		}
		function crumb(text, p, child, last){
			if(last) return h('b', {text:text, title:text, 'aria-current':'location'});
			return h('button', {type:'button', text:text, title:text, onclick:function(){ open(p, child); }});
		}
		function create(){
			var name = newIn.value.replace(/^\s+|\s+$/g, '');
			if(!name || !cur || !cur.path){ newIn.focus(); return; }
			DC.busy(createB, true);
			DC.api.post('folders', {path:cur.path, name:name}).then(function(res){ DC.busy(createB, false); set(res.path); },
				function(e){ DC.busy(createB, false); DC.toast(DC.errText(e)); newIn.focus(); });
		}
		/* Escape closes (and leaves the dialog open); arrows move between rows, → opens a folder, ← goes up */
		function keys(e){
			var t = e.target, rows, i;
			if(e.key === 'Escape'){
				if(panel.hidden) return;
				e.stopPropagation(); e.preventDefault();
				if(!newRow.hidden && newRow.contains(t)) hideNew(true);
				else close(true);
				return;
			}
			if(!t.classList || !t.classList.contains('fpitem')) return;
			rows = list.querySelectorAll('.fpitem');
			for(i = 0; i < rows.length && rows[i] !== t; i++){}
			if(e.key === 'ArrowDown' && i + 1 < rows.length){ e.preventDefault(); rows[i + 1].focus(); }
			else if(e.key === 'ArrowUp' && i > 0){ e.preventDefault(); rows[i - 1].focus(); }
			else if(e.key === 'ArrowRight'){ e.preventDefault(); t.nextSibling.click(); }
			else if(e.key === 'ArrowLeft' && cur && cur.path){ e.preventDefault(); open(parentOf(cur.path), cur.path); }
		}
		paint();
		fp.el = h('div', {'class':'fpwrap', onkeydown:keys}, [trigger, panel]);
		fp.set = function(v){ fp.value = v; paint(); };
		return fp;
	}
	DC.folderPicker = folderPicker;

	/* ---------- the add dialog ---------- */
	var STATUS_LABEL = {in_list:DC.t('已在清單中'), downloaded:DC.t('已下載過'), same_torrent:DC.t('已在清單中'), same_content:DC.t('內容相同的種子已在清單中')};
	function pickList(picks, onChange){
		var list = h('div', {'class':'items files'}), boxes = [], exts = {}, extList = [], i, tools = null, e;
		function set(fn){ for(var k = 0; k < picks.length; k++){ if(picks[k].blocked) continue; picks[k].sel = fn(picks[k]); boxes[k].checked = picks[k].sel; } onChange(); }
		for(i = 0; i < picks.length; i++){
			(function(pk, idx){
				var it = pk.it, x = DC.extOf(it.name), label;
				if(x && !exts[x]){ exts[x] = 1; extList.push(x); }
				var cb = h('input', {type:'checkbox', id:'pk' + idx, checked:pk.sel, disabled:pk.blocked, onchange:function(){ pk.sel = this.checked; onChange(); }});
				boxes.push(cb);
				if(pk.status && STATUS_LABEL[pk.status]) label = STATUS_LABEL[pk.status];
				else if(it.host) label = it.host.unsupported ? DC.t('{service} 不支援', {service:it.host.name}) : DC.t('{service} 帳號', {service:it.host.name});
				else if(it.kind === 'torrentfile') label = DC.t('種子檔');
				else if(it.kind === 'url' && hostOf(it.text)) label = /^(s?ftps?|scp):/i.test(it.text) ? it.text.split(':')[0].toUpperCase() + ' · ' + hostOf(it.text) : hostOf(it.text);
				else if(it.kind === 'torrent' && hostOf(it.text)) label = DC.t('{host} 的種子檔', {host:hostOf(it.text)});
				else label = KIND_TEXT[it.kind];
				list.appendChild(h('label', {'class':'item', 'for':'pk' + idx}, [cb, h('span', {text:it.name || it.text}), h('em', {'class':pk.status && pk.status !== 'new' || pk.blocked ? 'dup' : '', text:label})]));
			})(picks[i], i);
		}
		if(picks.length > 1){
			tools = h('div', {'class':'tools'}, h('button', {type:'button', onclick:function(){ set(function(pk){ return pk.it.kind !== 'page'; }); }}, DC.t('全選')));
			for(i = 0; i < extList.length && i < 6; i++){
				e = extList[i];
				tools.appendChild(h('button', {type:'button', onclick:(function(x){ return function(){ set(function(pk){ return DC.extOf(pk.it.name) === x; }); }; })(e)}, DC.t('只要 .{ext}', {ext:e})));
			}
			tools.appendChild(h('button', {type:'button', onclick:function(){ set(function(){ return false; }); }}, DC.t('全不選')));
		}
		return [tools, list];
	}
	function loading(text){ return h('div', {'class':'loading'}, [icon('check'), text]); }
	function torrentForm(files){
		var fd = new FormData(), i;
		for(i = 0; i < files.length; i++) fd.append('file', files[i]);
		return fd;
	}
	/* Same torrent already listed: fold the new trackers in instead of a second task. */
	function openMerge(it, taskName, doMerge){
		DC.modal(it.name || it.text, it.kind === 'magnet' ? 'magnet' : 'torrent', [
			h('p', {'class':'lead', text:DC.t('這個種子已經在清單中。')}),
			h('p', {'class':'note', text:DC.t('和「{task}」是同一個種子。新來源裡的 tracker 會併入既有任務，不會重複下載。', {task:taskName || DC.t('既有任務')})})
		], function(close){
			return [btn(null, DC.t('取消'), close), btn(null, DC.t('併入既有任務'), function(e){ DC.busy(e.currentTarget, true); doMerge(close); }, 'pri')];
		});
	}
	/* The add dialog. With opts.compose it is the whole add flow in one window: the paste field sits on top and the files,
	   links and options below follow what is pasted (re-rendered in place, no second window). Without it (dropped .torrent
	   files, page links) it opens straight on the given items. */
	function openAdd(items, pageUrl, opts){
		opts = opts || {};
		var compose = !!opts.compose;
		var me = DC.S.me, admin = DC.isAdmin(), defs = me.defaults || {};
		var one = null, single = null, files = null, picks = null, meta = null, free = -1, closed = false, contentOf = '', probeTimer = null, dupNote = false;
		var hasBt = false, hasUrl = false, gen = 0, title = '', start = null, typing = null;
		var body = h('div', {'class':'addbody'}), sum = h('div', {'class':'sum num'}), leadEl = h('p', {'class':'lead'});
		var lastFolder = DC.pref('last_folder') || defs.folder || '';
		var folder = admin ? folderPicker('addFolder', lastFolder, false, function(){ refreshFree(); }, {noFree:true}) : null;
		var move = admin ? folderPicker('addMove', defs.move_to || '', true) : null;
		var moveLabel = h('label', {'for':'addMove', text:DC.t('完成後移至')});
		/* Site account for URL downloads, as in the official dialog: automatic by host, none, a saved account, or typed in for this task only. */
		var manualOpt = h('option', {value:'manual', text:DC.t('手動輸入…')}), acctLoaded = false;
		var acct = h('select', {id:'addAcct', onchange:function(){ manual.hidden = this.value !== 'manual'; }}, [h('option', {value:'auto', text:DC.t('自動（依網站找預存帳號）')}), h('option', {value:'none', text:DC.t('不使用')}), manualOpt]);
		var acctLabel = h('label', {'for':'addAcct', text:DC.t('網站帳號')});
		var manual = h('div', {'class':'opts', hidden:true}, [
			h('label', {'for':'addUser', text:DC.t('帳號')}), h('input', {type:'text', id:'addUser', autocomplete:'off', autocapitalize:'off', spellcheck:'false'}),
			h('label', {'for':'addPass', text:DC.t('密碼')}), h('input', {type:'password', id:'addPass', autocomplete:'new-password'})]);
		/* Proxy for URL downloads: automatic (site rules, else the default), none, or a saved profile. Torrents use the profile set for
		   them in the settings; the row stays hidden when no profile exists and going direct is allowed. */
		var pxSel = h('select', {id:'addProxy'}), pxLabel = h('label', {'for':'addProxy', text:DC.t('代理')}), pxInfo = null;
		pxSel.hidden = pxLabel.hidden = true;
		function loadProxies(){
			if(pxInfo) return;
			pxInfo = {profiles:[], can_direct:true, loading:true};
			DC.api.get('proxies', null, {quiet:true}).then(function(r){
				var k, autoText;
				pxInfo = r;
				if(r.by_site) autoText = r['default'] ? DC.t('自動（依網站，否則用 {name}）', {name:r['default']}) : DC.t('自動（依網站，否則不使用）');
				else autoText = r['default'] ? DC.t('預設（{name}）', {name:r['default']}) : DC.t('預設（不使用）');
				clear(pxSel).appendChild(h('option', {value:'auto', text:autoText}));
				if(r.can_direct) pxSel.appendChild(h('option', {value:'none', text:DC.t('不使用代理')}));
				for(k = 0; k < (r.profiles || []).length; k++) pxSel.appendChild(h('option', {value:r.profiles[k].id, text:r.profiles[k].name}));
				syncOptions();
			}, function(){});
		}
		function proxyOffered(){ return !!(pxInfo && !pxInfo.loading && ((pxInfo.profiles || []).length || pxInfo['default'] || pxInfo.by_site || !pxInfo.can_direct)); }
		var seededOpt = h('option', {value:'seeded', text:DC.t('做種完成後移除')});
		var auto = h('select', {id:'addAuto'}, [h('option', {value:'', text:DC.t('保留在清單')}), h('option', {value:'completed', text:DC.t('下載完成後移除')}), seededOpt]);
		auto.value = defs.auto_remove || '';
		function loadAccounts(){
			if(acctLoaded) return;
			acctLoaded = true;
			DC.api.get('accounts', null, {quiet:true}).then(function(r){
				var a, k;
				for(k = 0; k < (r.accounts || []).length; k++){
					a = r.accounts[k];
					if(a.kind === 'site' && a.enabled) acct.insertBefore(h('option', {value:'id:' + a.id, text:DC.t('{site}（{user}）', {site:a.host, user:a.username})}), manualOpt);
				}
			}, function(){});
		}
		/* Options that depend on what is being added: the account only for URLs, the "seeded" choice for torrents */
		function syncOptions(){
			acct.hidden = acctLabel.hidden = !hasUrl;
			if(!hasUrl) manual.hidden = true; else { manual.hidden = acct.value !== 'manual'; loadAccounts(); loadProxies(); }
			pxSel.hidden = pxLabel.hidden = !hasUrl || !proxyOffered();
			seededOpt.hidden = seededOpt.disabled = !(hasBt || pageUrl);
			if(seededOpt.disabled && auto.value === 'seeded') auto.value = 'completed';
		}
		function where(){ return admin ? (folder.value || DC.t('暫存位置')) : (me.home_folder || 'home/Download'); }
		function freeText(){ return free >= 0 ? DC.t('{folder} 還有 {size} 可用。', {folder:where(), size:DC.fsize(free)}) : ''; }
		function refreshFree(){
			if(!admin){
				DC.api.get('stats', null, {quiet:true}).then(function(r){ if(r.folders && r.folders[0]){ free = r.folders[0].free; total(); } }, function(){});
				return;
			}
			if(!folder.value) return;
			DC.api.get('folders', {path:folder.value, free_only:'1'}, {quiet:true}).then(function(r){ if(r.free !== undefined){ free = r.free; total(); } }, function(e){
				/* The folder used last time is gone: back to the default one */
				if(e.code === 'folder_not_found' && defs.folder && folder.value !== defs.folder){ folder.set(defs.folder); refreshFree(); }
			});
		}
		function total(){
			var n = 0, sz = 0, j;
			if(picks){
				for(j = 0; j < picks.length; j++) if(picks[j].sel) n++;
				sum.textContent = DC.sentences(DC.t('已選 {n} / {total} 個。', {n:n, total:picks.length}), freeText());
				if(start) start.disabled = !n;
				return;
			}
			if(!files){ sum.textContent = one ? freeText() : ''; return; }
			for(j = 0; j < files.length; j++) if(files[j].sel){ n++; sz += files[j].size; }
			sum.textContent = DC.sentences(DC.t('已選 {n} / {total} 個檔案，共 {size}。', {n:n, total:files.length, size:DC.fsize(sz)}), freeText());
			if(start) start.disabled = !n;
		}
		function showFiles(){
			var flist = h('div', {'class':'items files'}), boxes = [], j;
			function setAll(fn){ for(var k = 0; k < files.length; k++){ files[k].sel = fn(files[k]); boxes[k].checked = files[k].sel; } total(); }
			for(j = 0; j < files.length; j++){
				(function(f, idx){
					var cb = h('input', {type:'checkbox', id:'af' + idx, checked:f.sel, onchange:function(){ f.sel = this.checked; total(); }});
					boxes.push(cb);
					flist.appendChild(h('label', {'class':'item', 'for':'af' + idx}, [cb, h('span', {text:f.path}), h('em', {'class':'num', text:DC.fsize(f.size)})]));
				})(files[j], j);
			}
			clear(body);
			if(dupNote) body.appendChild(h('p', {'class':'note warn', text:DC.t('目的地已有同名的檔案，可能已經下載過。')}));
			if(contentOf) body.appendChild(h('p', {'class':'note', text:DC.t('會作為既有任務的其他來源，下載到同一個資料夾。')}));
			if(files.length > 1) body.appendChild(h('div', {'class':'tools'}, [
				h('button', {type:'button', onclick:function(){ setAll(function(){ return true; }); }}, DC.t('全選')),
				h('button', {type:'button', onclick:function(){ setAll(function(f){ return /\.(mp4|mkv|avi|mov|m4v|webm|ts|wmv|flv)$/i.test(f.path); }); }}, DC.t('只要影片')),
				h('button', {type:'button', onclick:function(){ setAll(function(){ return false; }); }}, DC.t('全不選'))]));
			add(body, [flist, sum]);
			total();
		}
		function showPicks(){ clear(body); add(body, [pickList(picks, total), sum]); total(); }
		function useMeta(r){
			meta = r.torrent;
			if(r.free !== undefined && r.free >= 0) free = r.free;
			files = [];
			for(var k = 0; k < (meta.files || []).length; k++) files.push({index:meta.files[k].index, path:meta.files[k].path.replace(/^[^\/]+\//, ''), size:meta.files[k].size, sel:true});
			if(files.length === 1 && meta.files[0]) files[0].path = meta.files[0].path;
			showFiles();
		}
		function cancelProbe(){
			clearTimeout(probeTimer);
			if(one && one.kind === 'magnet' && !meta && probeTimer !== null) DC.api.post('tasks/probe', {magnet:one.text, cancel:true}, {quiet:true}).then(null, function(){});
			probeTimer = null;
		}
		function probeMagnet(my){
			DC.api.post('tasks/probe', {magnet:one.text, folder:admin ? folder.value : ''}).then(function(r){
				if(closed || my !== gen) return;
				if(r.state === 'ready'){ useMeta(r); return; }
				probeTimer = setTimeout(function(){ probeMagnet(my); }, 2000);
			}, function(e){
				if(closed || my !== gen) return;
				clear(body); body.appendChild(h('p', {'class':'note warn', text:DC.t('{error}。仍可以直接開始下載，檔案清單之後在任務詳細裡挑選。', {error:DC.errText(e)})}));
			});
		}
		function makePicks(list, statuses){
			var out = [], j, it, st, blocked;
			for(j = 0; j < list.length; j++){
				it = list[j]; st = statuses ? statuses[j] : null;
				blocked = !!(it.host && it.host.unsupported);
				out.push({it:it, status:st ? st.status : 'new', blocked:blocked, sel:it.kind !== 'page' && !blocked && (!st || st.status === 'new')});
			}
			return out;
		}
		function checkThen(list, done){
			var srcs = [], j, form, tf = list.length && list[0].kind === 'torrentfile';
			if(tf){
				/* Torrent files (only ever dropped or picked together) are checked in one multipart request */
				form = new FormData();
				for(j = 0; j < list.length; j++) form.append('file', list[j].file);
				if(admin && folder.value) form.append('folder', folder.value);
				DC.api.upload('tasks/check', form, {quiet:true}).then(function(r){ done(r.items || []); }, function(){ done(null); });
				return;
			}
			for(j = 0; j < list.length; j++) srcs.push(list[j].text);
			DC.api.post('tasks/check', {sources:srcs, folder:admin ? folder.value : ''}, {quiet:true}).then(function(r){ done(r.items || []); }, function(){ done(null); });
		}
		function loadTorrentFiles(my){
			var form = torrentForm([one.file]);
			if(admin && folder.value) form.append('folder', folder.value);
			DC.api.upload('tasks/probe', form).then(function(r){ if(!closed && my === gen) useMeta(r); }, function(e){ if(closed || my !== gen) return; clear(body); body.appendChild(h('p', {'class':'note warn', text:DC.errText(e)})); });
		}
		function setTitle(t){ var sp = box && box.querySelector('.mhead h2 span'); if(sp) sp.textContent = t; }

		/* (Re)fill everything that depends on the items. Late answers of an earlier load are dropped by the generation check. */
		function load(list, page){
			var my, i, n;
			cancelProbe();
			gen++; my = gen;
			items = list || []; pageUrl = page || null;
			single = !pageUrl && items.length === 1 ? items[0] : null;
			if(single && single.kind === 'page'){ pageUrl = single.text; items = []; single = null; }
			one = single && (single.kind === 'magnet' || single.kind === 'torrentfile') ? single : null;
			files = picks = meta = null; contentOf = ''; dupNote = false;
			hasBt = false; hasUrl = !!pageUrl;
			for(i = 0; i < items.length; i++){
				if(items[i].kind === 'magnet' || items[i].kind === 'torrentfile') hasBt = true;
				if((items[i].kind === 'url' || items[i].kind === 'torrent') && !items[i].host) hasUrl = true;
			}
			syncOptions();
			clear(body);
			if(start) start.disabled = true;
			n = items.length;
			title = pageUrl ? DC.t('網頁裡的連結') : one ? (one.name || one.text) : (n === 1 ? (items[0].name || items[0].text) : n ? DC.t('加入 {n} 個下載', {n:n}) : DC.t('加入下載'));
			if(compose) leadEl.textContent = n || pageUrl ? (pageUrl ? pageUrl : title) : '';
			else { setTitle(title); leadEl.textContent = pageUrl ? pageUrl : one ? (one.kind === 'magnet' ? DC.t('磁力連結') : DC.t('種子檔')) : (n > 1 ? DC.t('勾選要下載的項目，選好存放位置就開始。') : DC.t('選好存放位置就開始下載。')); }
			leadEl.className = 'lead' + (pageUrl ? ' mono' : '') + (compose ? ' cmp-what' : '');
			leadEl.hidden = compose && !(n || pageUrl);
			if(!n && !pageUrl){ total(); return; }
			if(pageUrl){
				body.appendChild(loading(DC.t('正在讀取頁面裡的連結…')));
				DC.api.post('tasks/extract', {url:pageUrl}).then(function(r){
					if(closed || my !== gen) return;
					var list2 = [], k, l;
					for(k = 0; k < (r.links || []).length; k++){ l = r.links[k]; list2.push({kind:l.kind, text:l.url, name:l.name || nameFrom(l.url), host:l.hoster ? {name:l.hoster, acct:true} : null}); }
					if(!list2.length){ clear(body); body.appendChild(h('p', {'class':'note', text:DC.t('這個網頁裡沒有找到下載連結。')})); return; }
					checkThen(list2, function(st){ if(closed || my !== gen) return; items = list2; picks = makePicks(list2, st); showPicks(); });
				}, function(e){ if(closed || my !== gen) return; clear(body); body.appendChild(h('p', {'class':'note warn', text:DC.errText(e)})); });
			}else if(one){
				body.appendChild(loading(one.kind === 'magnet' ? DC.t('正在檢查…') : DC.t('正在讀取種子檔…')));
				checkThen([one], function(st){
					if(closed || my !== gen) return;
					var s0 = st && st[0];
					if(s0 && (s0.status === 'same_torrent' || (s0.status === 'in_list' && s0.task_id))){
						var it = one;
						closeBox();
						openMerge(it, s0.task_name, function(closeMerge){
							DC.track('add_merge');
							var p = it.kind === 'magnet' ? DC.api.post('tasks', {source:it.text}) : DC.api.upload('tasks/torrent', torrentForm([it.file]));
							p.then(function(){ closeMerge(); DC.toast(DC.t('已併入「{task}」', {task:s0.task_name || DC.t('既有任務')})); DC.pollNow(); }, function(e){ closeMerge(); DC.toast(DC.errText(e)); });
						});
						return;
					}
					if(s0 && s0.status === 'same_content' && s0.task_id && one.kind === 'torrentfile'){
						var t = DC.task(s0.task_id);
						clear(body);
						body.appendChild(h('p', {'class':'note', text:DC.t('清單中的「{task}」檔案內容和這個種子相同。可以把它當作其他來源合併（同一時間只有一個來源在下載，停滯時自動切換），或仍然加入為新任務。', {task:t ? t.name : s0.task_id})}));
						body.appendChild(h('div', {'class':'srcs'}, [
							btn(null, DC.t('合併為其他來源'), function(){ contentOf = s0.task_id; loadTorrentFiles(my); }, 'pri'),
							btn(null, DC.t('仍要加入'), function(){ loadTorrentFiles(my); })]));
						return;
					}
					dupNote = !!(s0 && s0.status === 'downloaded');
					if(one.kind === 'magnet'){
						clear(body);
						if(dupNote) body.appendChild(h('p', {'class':'note warn', text:DC.t('目的地已有同名的檔案，可能已經下載過。')}));
						body.appendChild(loading(DC.t('正在取得檔案清單…')));
						body.appendChild(h('p', {'class':'note', text:DC.t('不用等：現在按「開始下載」就會放入下載佇列，取得檔案清單後可以在任務詳細的「檔案」挑選要下載的檔案。')}));
						if(start) start.disabled = false;
						probeTimer = 0;
						probeMagnet(my);
					}else loadTorrentFiles(my);
				});
			}else{
				body.appendChild(loading(DC.t('正在檢查是否重複…')));
				checkThen(items, function(st){ if(closed || my !== gen) return; picks = makePicks(items, st); showPicks(); });
			}
			refreshFree();
			total();
		}

		/* Compose mode: the paste field drives load() as the text changes */
		var pb = null, clip = !!(navigator.clipboard && navigator.clipboard.readText && window.isSecureContext);
		function abtn(name, text, label, fn){ return h('button', {'class':'ib abtn', type:'button', title:label, 'aria-label':label, onclick:fn}, [icon(name), h('span', {text:text})]); }
		function fromText(){
			clearTimeout(typing);
			var text = pb.input.value, d = detect(text);
			if(d.kind === 'empty' || d.kind === 'bad'){ load([]); return; }
			if(d.loose && d.items.length > 1){
				/* Pasted text: let the server classify the links (file hosts, pages) */
				var my = gen;
				DC.api.post('tasks/extract', {text:text}).then(function(r){
					if(closed || my !== gen || pb.input.value !== text) return;
					var list = [], i, l;
					for(i = 0; i < (r.links || []).length; i++){
						l = r.links[i];
						list.push({kind:l.kind, text:l.url, name:l.name || nameFrom(l.url), host:l.hoster ? {name:l.hoster, acct:true} : (l.kind === 'magnet' ? null : hosterOf(l.url))});
					}
					load(list.length ? list : d.items);
				}, function(){ if(!closed && pb.input.value === text) load(d.items); });
				return;
			}
			load(d.items);
		}
		if(compose){
			pb = pasteBox(!!opts.sheet, {
				type:function(){ clearTimeout(typing); typing = setTimeout(fromText, 450); },
				enter:fromText,
				paste:fromText,
				files:function(fl){
					var list = [], bad = [], i;
					for(i = 0; i < fl.length; i++){
						if(/\.torrent$/i.test(fl[i].name)) list.push({kind:'torrentfile', file:fl[i], text:fl[i].name, name:fl[i].name.replace(/\.torrent$/i, '')});
						else bad.push(fl[i].name);
					}
					if(!list.length){ if(bad.length) DC.toast(DC.t('{file} 不是 .torrent 檔', {file:bad[0]})); return; }
					pb.input.value = ''; onInput(pb);
					load(list);
				}
			});
		}
		var pasteRow = compose ? [
			h('div', {'class':'add add-cmp' + (opts.sheet ? ' add-sheet' : '')}, [pb.kind, pb.input,
				clip ? abtn('paste', DC.t('貼上'), DC.t('貼上剪貼簿'), function(){
					navigator.clipboard.readText().then(function(t){ DC.track('add_clip'); pb.input.value = t; onInput(pb); fromText(); pb.input.focus(); }, function(){ DC.toast(DC.t('無法讀取剪貼簿，請在輸入框貼上')); pb.input.focus(); });
				}) : null,
				abtn('torrent', '.torrent', DC.t('選擇 .torrent 檔'), function(){ pb.file.click(); }), pb.file]),
			pb.hint] : null;

		var box = null;
		var closeBox = DC.modal(compose ? DC.t('加入下載') : '', compose ? 'plus' : 'link', [
			pasteRow,
			leadEl,
			body,
			h('div', {'class':'opts'}, [
				admin ? [h('label', {'for':'addFolder', text:DC.t('暫存位置')}), folder.el, moveLabel, move.el]
					: [h('span', {'class':'unit', text:DC.t('存放位置')}), h('span', {'class':'mono', text:DC.t('{folder}（你的家目錄）', {folder:me.home_folder || 'home/Download'})})],
				acctLabel, acct,
				pxLabel, pxSel,
				h('label', {'for':'addAuto', text:DC.t('完成後')}), auto]),
			manual
		], function(close){
			start = btn(null, DC.t('開始下載'), function(){ go(close); }, 'pri');
			start.disabled = true;
			return [btn(null, DC.t('取消'), close), start];
		}, {nofocus:compose, wide:compose, onclose:function(){ closed = true; clearTimeout(typing); cancelProbe(); },
			guard:function(){ return !!((pb && pb.input.value.replace(/\s+/g, '')) || (items && items.length) || pageUrl); }});
		var boxes = document.querySelectorAll('.modal');
		box = boxes.length ? boxes[boxes.length - 1] : null;
		if(box && compose) box.classList.add('compose');

		function accountBody(){
			if(!hasUrl) return null;
			var v = acct.value;
			if(v === 'auto' || v === 'none') return {mode:v};
			if(v === 'manual') return {mode:'manual', user:DC.val('addUser'), pass:DC.val('addPass')};
			return {mode:'id', id:v.slice(3)};
		}
		function options(){
			var o = {auto_remove:auto.value};
			if(admin){ o.folder = folder.value; o.move_to = move.value; }
			var a = accountBody();
			if(a) o.account = a;
			if(hasUrl && !pxSel.hidden && pxSel.value && pxSel.value !== 'auto') o.proxy = pxSel.value;
			return o;
		}
		function finished(r, n){
			var ok = 0, merged = 0, dup = 0, errs = [], j, res = r.results || [];
			if(!res.length && r.id) res = [{id:r.id, name:r.name, merged:r.merged}];
			for(j = 0; j < res.length; j++){
				if(res[j].error){ if(res[j].error.code === 'duplicate') dup++; else errs.push(res[j].error.message); continue; }
				if(res[j].merged) merged++; else { ok++; DC.markFresh(res[j].id); }
			}
			if(admin && folder.value) DC.savePref('last_folder', folder.value);
			closeBox();
			if(DC.S.view !== 'tasks') DC.go('tasks');
			var parts = [];
			if(ok) parts.push(ok === 1 && n === 1 ? DC.t('已加入：{name}', {name:res[0].name || title}) : DC.t('已加入 {n} 個下載', {n:ok}));
			if(merged) parts.push(DC.t('併入 {n} 個既有任務', {n:merged}));
			if(dup) parts.push(DC.t('{n} 個已在清單中', {n:dup}));
			if(errs.length) parts.push(DC.t('{n} 個失敗：{error}', {n:errs.length, error:errs[0]}));
			DC.toast(parts.join(DC.t('，')) || DC.t('沒有加入任何下載'));
			if(ok === 1 && res.length === 1){
				var it0 = items && items.length === 1 ? items[0] : null;
				flyIn(res[0].name || title, it0 ? it0.kind : 'url', function(){ DC.bumpCount(); DC.pollNow(); });
			}else DC.pollNow();
		}
		function failed(e){ DC.busy(start, false); DC.toast(DC.errText(e)); }
		function go(){
			var o = options(), j, sel = [], list = [];
			if(hasUrl && acct.value === 'manual' && !DC.val('addUser')){ DC.toast(DC.t('請輸入網站帳號')); return; }
			if(admin && !o.folder){ DC.toast(DC.t('請選擇暫存位置')); return; }
			DC.busy(start, true, DC.t('加入中…'));
			if(one){
				for(j = 0; files && j < files.length; j++) if(files[j].sel) sel.push(files[j].index);
				if(one.kind === 'magnet'){
					o.source = one.text;
					/* Without the file list yet the whole torrent is queued; files can be picked in the task detail once the metadata arrives */
					if(files && sel.length < files.length) o.files = sel;
					DC.api.post('tasks', o).then(function(r){ finished(r, 1); }, failed);
				}else{
					var fd = torrentForm([one.file]);
					if(o.folder) fd.append('folder', o.folder);
					if(admin) fd.append('move_to', o.move_to || '');
					fd.append('auto_remove', o.auto_remove || '');
					if(files && sel.length < files.length) fd.append('files', sel.join(','));
					if(contentOf) fd.append('content_of', contentOf);
					DC.api.upload('tasks/torrent', fd).then(function(r){ finished(r, 1); }, failed);
				}
				return;
			}
			var tfiles = [];
			for(j = 0; picks && j < picks.length; j++){
				if(!picks[j].sel) continue;
				if(picks[j].it.kind === 'torrentfile') tfiles.push(picks[j].it.file);
				else list.push(picks[j].it.text);
			}
			var results = {results:[]}, pending = 0;
			function part(r){ var k, res = r.results || (r.id ? [{id:r.id, name:r.name, merged:r.merged}] : []); for(k = 0; k < res.length; k++) results.results.push(res[k]); }
			function partErr(e){ results.results.push({error:{code:e.code, message:e.message}}); }
			function step(){ if(--pending === 0) finished(results, list.length + tfiles.length); }
			if(list.length){ pending++; o.sources = list; DC.api.post('tasks', o).then(function(r){ part(r); step(); }, function(e){ if(e.body && e.body.error && e.code === 'duplicate') results.results.push({error:{code:'duplicate', message:e.message}}); else partErr(e); step(); }); }
			if(tfiles.length){
				pending++;
				var form = torrentForm(tfiles);
				if(o.folder) form.append('folder', o.folder);
				if(admin) form.append('move_to', o.move_to || '');
				form.append('auto_remove', o.auto_remove || '');
				DC.api.upload('tasks/torrent', form).then(function(r){ part(r); step(); }, function(e){ partErr(e); step(); });
			}
			if(!pending){ DC.busy(start, false); DC.toast(DC.t('請至少勾選一個項目')); }
		}

		if(compose){
			onInput(pb);
			if(opts.text){ pb.input.value = opts.text; onInput(pb); fromText(); } else load([]);
			setTimeout(function(){ if(!closed) pb.input.focus(); }, 60);
		}else load(items, pageUrl);
	}
	/* Opens the add window (desktop launcher, '/', the phone "+" and pasting a link on the task list all land here). */
	function compose(text){ openAdd([], null, {compose:true, sheet:DC.phone(), text:text || ''}); }
	function openPaste(){ compose(''); }

	/* Pasting a link anywhere on the task list opens the add window with it: no need to aim for a field first */
	document.addEventListener('paste', function(e){
		if(!DC.S.me || R.closeModal || DC.S.view !== 'tasks' || !DC.can('tasks:add')) return;
		var tag = (e.target.tagName || '').toLowerCase();
		if(tag === 'input' || tag === 'textarea' || tag === 'select' || e.target.isContentEditable) return;
		var text = e.clipboardData && e.clipboardData.getData('text');
		if(!text) return;
		var d = detect(text);
		if(d.kind === 'empty' || d.kind === 'bad') return;
		e.preventDefault();
		compose(text);
	});
	/* One orchestrated moment for an add: the panel folds, a chip with the task flies from the launcher into the queue, the row
	   lands with a bounce and the 全部 count pops. Without Web Animations or with reduced motion the row simply appears. */
	function flyIn(name, kind, done){
		var from = R.launch && R.launch.getBoundingClientRect(), to = R.list && R.list.getBoundingClientRect(), chip, reduce = false;
		try{ reduce = window.matchMedia('(prefers-reduced-motion: reduce)').matches; }catch(x){}
		if(!from || !to || reduce || !document.body.animate || DC.phone()){ done(); return; }
		chip = h('div', {'class':'fly', 'aria-hidden':'true'}, [icon(kind === 'magnet' ? 'magnet' : kind === 'torrent' ? 'torrent' : 'link'), h('span', {text:name})]);
		chip.style.left = from.left + 'px'; chip.style.top = from.top + 'px'; chip.style.maxWidth = Math.max(160, Math.min(360, to.width * .5)) + 'px';
		document.body.appendChild(chip);
		var dx = (to.left + 58) - from.left, dy = (to.top + 14) - from.top;
		var anim = chip.animate([
			{transform:'translate(0,0) scale(.9)', opacity:0},
			{transform:'translate(' + (dx * .25) + 'px,' + (dy * .1 - 18) + 'px) scale(1.04)', opacity:1, offset:.3},
			{transform:'translate(' + dx + 'px,' + dy + 'px) scale(1)', opacity:1, offset:.88},
			{transform:'translate(' + dx + 'px,' + dy + 'px) scale(.98)', opacity:0}
		], {duration:620, easing:'cubic-bezier(.3,.9,.3,1)', fill:'forwards'});
		var ended = false;
		function end(){ if(ended) return; ended = true; DC.remove(chip); done(); }
		anim.onfinish = end; setTimeout(end, 800);
	}

	DC.addFlow = {pasteBox:pasteBox, onInput:onInput, submit:submit, openPaste:openPaste, compose:compose, openTorrents:openTorrents, openAdd:openAdd, detect:detect};
})();
