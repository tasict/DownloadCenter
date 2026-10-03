/* Task inspector: overview, files, preview, peers and log. A right-hand glass drawer; a near full-screen sheet on phones. */
(function(){
	'use strict';
	var DC = window.DC, h = DC.h, add = DC.add, clear = DC.clear, icon = DC.icon, ibtn = DC.ibtn, btn = DC.btn;
	var D = {id:null, tab:'info', el:null, scrim:null, extra:null, peerTimer:null};

	function close(){
		clearTimeout(D.peerTimer);
		if(D.el){ DC.remove(D.el); DC.remove(D.scrim); D.el = null; }
		document.documentElement.classList.remove('dr-dock');
		D.id = null;
	}
	/* Wide windows keep the inspector beside the list (no dimming, other rows stay clickable); narrower ones overlay it.
	   1320 keeps the list wider than the 860px sidebar breakpoint once the 440px for the inspector is taken off. */
	function wide(){ return document.body.clientWidth >= 1320; }
	function dock(){
		if(!D.el) return;
		var on = wide();
		document.documentElement.classList.toggle('dr-dock', on);
		D.scrim.hidden = on;
		D.el.setAttribute('aria-modal', on ? 'false' : 'true');
	}
	window.addEventListener('resize', dock);
	function task(){ return D.id ? DC.task(D.id) : null; }
	function open(id){
		var t = DC.task(id), head, again = !!D.el && wide();
		if(!t) return;
		close();
		D.id = id; D.tab = 'info'; D.extra = null; D.state = null;
		D.scrim = h('div', {'class':'scrim', onclick:close});
		D.tabs = h('div', {'class':'tabs', role:'tablist'});
		D.body = h('div', {'class':'dr-body'});
		D.icon = h('span', {'class':'sicon'});
		D.title = h('b', {text:t.name || t.source});
		D.sub = h('small');
		D.act = h('span', {'class':'dr-act'});
		head = h('div', {'class':'dr-head'}, [h('div', {'class':'grab', 'aria-hidden':'true'}), D.icon, h('div', null, [D.title, D.sub]), ibtn('close', DC.t('關閉'), close)]);
		D.el = h('aside', {'class':'drawer', role:'dialog', 'aria-modal':'true', 'aria-label':t.name || t.source}, [
			head, D.tabs, D.body,
			h('div', {'class':'dr-foot'}, [D.act, D.folderBtn = btn('folder', DC.t('開啟資料夾'), function(){ var x = task(); if(x) DC.openFolder(x); }),
				DC.can('tasks:remove') ? btn('trash', DC.t('刪除'), function(){ DC.askDelete([D.id]); }, 'dan') : null])
		]);
		DC.pullToClose(D.el, head, close);
		/* Switching rows in the docked inspector replaces it in place instead of sliding in again */
		if(again) D.el.classList.add('still');
		DC.layer().appendChild(D.scrim); DC.layer().appendChild(D.el);
		dock();
		header(t); renderTabs(); renderBody();
		DC.track('details');
		loadExtra();
		if(!again) D.el.querySelector('.dr-head .ib').focus();
	}
	function loadExtra(){
		var id = D.id;
		DC.api.get('tasks/' + id, null, {quiet:true}).then(function(r){
			if(D.id !== id) return;
			D.extra = r;
			if(r.task) DC.upsertTask(r.task);
			if(D.tab === 'info' || D.tab === 'log') renderBody();
		}, function(){});
	}
	function header(t){
		var s = DC.uiState(t), key = s + '|' + (t.user_paused ? 1 : 0) + '|' + t.state;
		D.sub.textContent = DC.t('{kind}，{owner} 加入', {kind:DC.KIND_LABEL[t.kind] || '', owner:t.owner});
		if(D.title.textContent !== (t.name || t.source)) D.title.textContent = t.name || t.source;
		if(D.state === key) return;
		D.state = key;
		D.icon.className = 'sicon st-' + s;
		clear(D.icon).appendChild(icon(DC.ST[s].icon));
		/* The one thing most often done from the details: pause, resume or retry, as the first footer button. */
		clear(D.act);
		if(DC.can('tasks:control')){
			if(s === 'down' || s === 'wait' || s === 'check') D.act.appendChild(btn('hold', DC.t('暫停'), function(){ DC.act(t.id, 'pause'); }, 'pri'));
			else if(s === 'seed') D.act.appendChild(btn('hold', DC.t('停止做種'), function(){ DC.act(t.id, 'pause'); }, 'pri'));
			else if(s === 'pause') D.act.appendChild(btn('play', DC.t('繼續'), function(){ DC.act(t.id, 'resume'); }, 'pri'));
			else if(s === 'error') D.act.appendChild(btn('retry', DC.t('重試'), function(){ DC.act(t.id, 'retry'); }, 'pri'));
		}
	}
	function renderTabs(){
		var tabs = [['info', DC.t('概要')], ['files', DC.t('檔案')], ['preview', DC.t('預覽')], ['peers', DC.t('連線')], ['log', DC.t('紀錄')]], i;
		clear(D.tabs);
		for(i = 0; i < tabs.length; i++){
			D.tabs.appendChild(h('button', {'class':'tab' + (D.tab === tabs[i][0] ? ' on' : ''), role:'tab', type:'button', 'aria-selected':D.tab === tabs[i][0] ? 'true' : 'false',
				onclick:(function(id){ return function(){ D.tab = id; DC.track('tab_' + id); renderTabs(); renderBody(); if(id === 'log') loadExtra(); }; })(tabs[i][0])}, tabs[i][1]));
		}
	}
	/* Called on every list refresh: live tabs re-render, interactive ones keep their state. */
	function live(){
		var t = task();
		if(!t){ if(D.id && !DC.task(D.id)) close(); return; }
		header(t);
		if(D.tab === 'info') renderBody();
	}
	function renderBody(){
		var t = task();
		if(!t) return;
		clearTimeout(D.peerTimer);
		if(D.tab === 'info') info(t);
		else if(D.tab === 'files') filesTab(t);
		else if(D.tab === 'preview') previewTab(t);
		else if(D.tab === 'peers') peersTab(t);
		else logTab(t);
	}

	/* Another proxy for a URL task: a running download reconnects through it and continues where it was. */
	function changeProxy(t){
		DC.api.get('proxies').then(function(r){
			var opts = [['auto', r.by_site ? (r['default'] ? DC.t('自動（依網站，否則用 {name}）', {name:r['default']}) : DC.t('自動（依網站，否則不使用）')) : (r['default'] ? DC.t('預設（{name}）', {name:r['default']}) : DC.t('預設（不使用）'))]], k;
			if(r.can_direct) opts.push(['none', DC.t('不使用代理')]);
			for(k = 0; k < (r.profiles || []).length; k++) opts.push([r.profiles[k].id, r.profiles[k].name]);
			var sel = DC.select('dpProxy', opts, t.proxy || 'auto');
			DC.modal(DC.t('更改代理'), 'link', [h('p', {'class':'lead', text:DC.t('下載中的任務會重新連線，從已下載的進度接續。')}), DC.field(DC.t('代理'), null, sel, 'dpProxy')], function(close){
				var ok = btn(null, DC.t('更改'), function(){
					DC.busy(ok, true);
					DC.api.patch('tasks/' + t.id, {proxy:sel.value}).then(function(res){
						close();
						if(res.task) DC.upsertTask(res.task);
						DC.toast(DC.t('已更改代理'));
						loadExtra();
					}, function(e){ DC.busy(ok, false); DC.toast(DC.errText(e)); });
				}, 'pri');
				return [btn(null, DC.t('取消'), close), ok];
			});
		}, function(e){ DC.toast(DC.errText(e)); });
	}

	function info(t){
		var s = DC.uiState(t), ex = D.extra || {}, srcs = ex.sources || [], srcText, i, admin = DC.isAdmin(), kv;
		if(srcs.length){
			srcText = [];
			for(i = 0; i < srcs.length; i++) srcText.push((srcs[i].active ? '● ' : '○ ') + (srcs[i].name || srcs[i].source) + (srcs[i].active ? DC.t('（使用中）') : ''));
			srcText = srcText.join('\n');
		}else srcText = t.source;
		var stateText = DC.ST[s].label;
		/* URL tasks: the proxy in use (resolved by the server for the detail view), changeable while not finished */
		var xt = ex.task || {}, pxText = null;
		if(t.proxy){
			if(!ex.task) pxText = '…';
			else if(xt.proxy_error) pxText = DC.t('無法使用：{error}', {error:xt.proxy_error});
			else if(xt.proxy_name) pxText = t.proxy === 'auto' ? DC.t('{name}（自動）', {name:xt.proxy_name}) : xt.proxy_name;
			else pxText = DC.t('不使用代理');
		}
		if(t.state === 'metadata') stateText = DC.t('取得檔案清單中');
		if(t.sched_paused) stateText += DC.t('（排程暫停）');
		if(t.wake_time && s === 'pause') stateText += DC.t('，{time} 自動繼續', {time:DC.fclock(t.wake_time)});
		if(t.error) stateText += DC.t('：{error}', {error:t.error.message});
		var inTemp = t.proto !== 'bt' && s !== 'done' && s !== 'move';
		/* Paths break after a slash rather than mid-name, and can be copied */
		function path(text){
			var el = h('span', {'class':'mono pre path'}), parts = String(text || '').split('/'), k;
			for(k = 0; k < parts.length; k++){ if(k){ el.appendChild(document.createTextNode('/')); el.appendChild(h('wbr')); } el.appendChild(document.createTextNode(parts[k])); }
			return h('dd', {'class':'pathdd'}, [el, ibtn('copy', DC.t('複製路徑'), function(){ DC.copyText(text, el); })]);
		}
		kv = h('dl', {'class':'kv num'}, [
			h('dt', {text:DC.t('狀態')}), h('dd', {text:stateText}),
			h('dt', {text:DC.t('進度')}), h('dd', {text:DC.t('{pct}%，{done} / {size}', {pct:Math.floor(t.progress || 0), done:DC.fsize(t.done), size:DC.fsize(t.size)})}),
			h('dt', {text:DC.t('速度')}), h('dd', {text:DC.t('下載 {down}，上傳 {up}', {down:DC.fspeed(t.down_rate), up:DC.fspeed(t.up_rate)})}),
			h('dt', {text:DC.t('已下載時間')}), h('dd', {text:DC.fdur(t.active_secs) + (s === 'down' && t.eta > 0 ? DC.t('，') + DC.feta(t.eta) : '')}),
			t.proto === 'bt' ? h('dt', {text:DC.t('分享率')}) : null, t.proto === 'bt' ? h('dd', {text:DC.t('{ratio}，已上傳 {size}', {ratio:(t.ratio || 0).toFixed(2), size:DC.fsize(t.up_total)})}) : null,
			t.proto === 'bt' ? h('dt', {text:DC.t('連線')}) : null, t.proto === 'bt' ? h('dd', {text:DC.t('{peers} 位使用者，{seeds} 個完整來源', {peers:t.peers || 0, seeds:t.seeds || 0})}) : null,
			h('dt', {text:admin ? DC.t('暫存位置') : DC.t('存放位置')}), path(t.folder),
			inTemp && t.location ? h('dt', {text:DC.t('下載中的檔案')}) : null, inTemp && t.location ? path(t.location) : null,
			t.move_to ? h('dt', {text:t.proto === 'bt' ? DC.t('做種結束後移至') : DC.t('完成後移至')}) : null, t.move_to ? path(t.move_to) : null,
			s === 'done' ? h('dt', {text:DC.t('檔案位置')}) : null, s === 'done' ? path(t.location) : null,
			h('dt', {text:srcs.length > 1 ? DC.t('來源（已合併）') : DC.t('來源')}), h('dd', {'class':'mono pre', text:srcText}),
			t.auto_remove ? h('dt', {text:DC.t('完成後')}) : null, t.auto_remove ? h('dd', {text:t.auto_remove === 'completed' ? DC.t('下載完成後從清單移除') : DC.t('做種完成後從清單移除')}) : null,
			t.comment ? h('dt', {text:DC.t('說明')}) : null, t.comment ? h('dd', {text:t.comment}) : null,
			h('dt', {text:DC.t('加入時間')}), h('dd', {text:DC.ftime(t.created_at)}),
			t.finished_at ? h('dt', {text:DC.t('完成時間')}) : null, t.finished_at ? h('dd', {text:DC.ftime(t.finished_at)}) : null,
			admin ? h('dt', {text:DC.t('擁有者')}) : null, admin ? h('dd', {text:t.owner + (t.caller && t.caller !== 'Download Center' ? DC.t('（{caller}）', {caller:t.caller}) : '')}) : null,
			pxText ? h('dt', {text:DC.t('代理')}) : null,
			pxText ? h('dd', {'class':'inline'}, [h('span', {text:pxText}), DC.can('tasks:control') && s !== 'done' ? ibtn('edit', DC.t('更改代理'), function(){ changeProxy(t); }) : null]) : null,
			admin && t.engine ? h('dt', {text:DC.t('引擎')}) : null, admin && t.engine ? h('dd', {text:t.engine}) : null
		]);
		clear(D.body);
		D.body.appendChild(kv);
		if(t.proto === 'bt' && t.files_total > 0){
			D.body.appendChild(h('div', {'class':'srcs'}, [h('a', {'class':'ib btn', href:DC.api.url('tasks/' + t.id + '/torrent'), download:(t.name || 'task') + '.torrent'}, [icon('torrent'), h('span', {text:DC.t('下載 .torrent 檔')})])]));
		}
	}

	var PRIO = [[7, DC.t('高')], [4, DC.t('中')], [1, DC.t('低')], [0, DC.t('不下載')]];
	/* Imported tasks keep the official package's unfinished-file suffix on disk; it is not part of the name */
	function shown(p){ return String(p || '').replace(/\.dsdownload$/i, ''); }
	/* A multi-file torrent puts everything under one folder: list the files relative to it */
	function rootOf(files){
		var first = /^([^\/]+)\//.exec(files.length > 1 ? files[0].path : ''), i;
		if(!first) return '';
		for(i = 1; i < files.length; i++) if(files[i].path.indexOf(first[0]) !== 0) return '';
		return first[0];
	}
	function filesTab(t){
		clear(D.body);
		D.body.appendChild(h('div', {'class':'loading'}, [icon('check'), DC.t('讀取中…')]));
		DC.api.get('tasks/' + t.id + '/files').then(function(r){
			if(D.tab !== 'files' || D.id !== t.id) return;
			var files = r.files || [], list = h('div', {'class':'items'}), dirty = false, save, levels = !!(DC.S.me.caps && DC.S.me.caps.file_priority_levels) && t.engine === DC.S.me.bt_engine, i;
			var editable = t.proto === 'bt' && DC.can('tasks:control') && t.state !== 'metadata', root = rootOf(files);
			clear(D.body);
			if(!files.length){ D.body.appendChild(h('p', {'class':'note', text:t.state === 'metadata' ? DC.t('還在取得檔案清單。') : DC.t('還沒有檔案資訊。')})); return; }
			function mark(){ dirty = true; if(save) save.disabled = false; }
			for(i = 0; i < files.length; i++){
				(function(f, idx){
					var pct = f.size ? Math.floor(f.done * 100 / f.size) : 0, ctl;
					if(levels && editable){
						ctl = DC.select('fp' + idx, PRIO, String(f.priority >= 6 ? 7 : f.priority >= 3 ? 4 : f.priority > 0 ? 1 : 0), function(){ f.priority = +this.value; mark(); });
						list.appendChild(h('div', {'class':'item'}, [icon('files'), h('span', null, [shown(f.path.slice(root.length)), h('em', {'class':'num', text:' ' + DC.fsize(f.size) + DC.t('，{pct}%', {pct:pct})})]), ctl]));
					}else{
						list.appendChild(h('label', {'class':'item', 'for':'df' + idx}, [h('input', {type:'checkbox', id:'df' + idx, checked:f.priority > 0, disabled:!editable, onchange:function(){ f.priority = this.checked ? 1 : 0; mark(); }}),
							h('span', {text:shown(f.path.slice(root.length))}), h('em', {'class':'num', text:DC.fsize(f.size) + (f.done && f.priority > 0 ? DC.t('，{pct}%', {pct:pct}) : '')})]));
					}
				})(files[i], i);
			}
			add(D.body, [
				root ? h('p', {'class':'note mono', text:DC.t('資料夾：{folder}', {folder:root.slice(0, -1)})}) : null,
				editable ? h('p', {'class':'note', text:levels ? DC.t('設為「不下載」的檔案不會再下載，已下載的部分留在磁碟上。') : DC.t('取消勾選的檔案不會再下載，已下載的部分留在磁碟上。')}) : null, list]);
			if(editable){
				save = btn(null, DC.t('套用'), function(){
					var body, sel = [], k, n = 0;
					if(levels){ body = {}; for(k = 0; k < files.length; k++){ body[String(files[k].index)] = files[k].priority; if(files[k].priority > 0) n++; } }
					else{ for(k = 0; k < files.length; k++) if(files[k].priority > 0) sel.push(files[k].index); body = sel; n = sel.length; }
					if(!n){ DC.toast(DC.t('至少要選一個檔案')); return; }
					DC.busy(save, true);
					DC.api.patch('tasks/' + t.id, {files:body}).then(function(res){ DC.busy(save, false); save.disabled = true; dirty = false; if(res.task) DC.upsertTask(res.task); DC.toast(DC.t('已更新要下載的檔案')); }, function(e){ DC.busy(save, false); DC.toast(DC.errText(e)); });
				}, 'pri');
				save.disabled = true;
				D.body.appendChild(h('div', {'class':'savebar'}, [save]));
			}
		}, function(e){ clear(D.body); D.body.appendChild(h('p', {'class':'note warn', text:DC.errText(e)})); });
	}

	function previewTab(t){
		clear(D.body);
		D.body.appendChild(h('div', {'class':'loading'}, [icon('check'), DC.t('讀取中…')]));
		DC.api.get('tasks/' + t.id + '/preview-info').then(function(r){
			if(D.tab !== 'preview' || D.id !== t.id) return;
			var files = r.files || [], chooser = null, area = h('div'), i, best = 0;
			clear(D.body);
			if(!files.length){ D.body.appendChild(h('p', {'class':'note', text:DC.t('還沒有可以預覽的檔案。')})); return; }
			/* Default to the largest media file */
			for(i = 0; i < files.length; i++) if((files[i].type === 'video' || files[i].type === 'audio') && files[i].size > files[best].size) best = i;
			if(files[best].type !== 'video' && files[best].type !== 'audio') for(i = 0; i < files.length; i++) if(files[i].size > files[best].size) best = i;
			if(files.length > 1){
				chooser = h('select', {id:'pvFile', 'aria-label':DC.t('要預覽的檔案'), onchange:function(){ show(files[+this.value]); }});
				for(i = 0; i < files.length; i++) chooser.appendChild(h('option', {value:String(i), text:shown(files[i].path)}));
				chooser.value = String(best);
				D.body.appendChild(h('div', {'class':'inline pvsel'}, chooser));
			}
			D.body.appendChild(area);
			if(r.sequential_supported && DC.can('tasks:control') && t.state !== 'done'){
				D.body.appendChild(h('label', {'class':'toggle', 'for':'pvStream'}, [h('input', {type:'checkbox', id:'pvStream', checked:!!r.sequential, onchange:function(){
					var on = this.checked;
					if(on) DC.track('stream_on');
					DC.api.patch('tasks/' + t.id, {sequential:on}).then(function(){ DC.toast(on ? DC.t('改為依序下載（邊下邊看）') : DC.t('改回一般下載')); }, function(e){ DC.toast(DC.errText(e)); });
				}}), h('span', null, [DC.t('邊下邊看'), h('small', {text:DC.t('改為依序下載並優先抓頭尾，總下載時間可能變長')})])]));
			}
			/* The bar has two layers: the readable start of the file and everything downloaded so far. Files that cannot be
			   previewed at all only show how much is downloaded. */
			function bars(f, viewable){
				var c = f.size ? f.contiguous / f.size : 0, d = f.size ? Math.max(f.downloaded, f.contiguous) / f.size : 0, txt;
				if(viewable === false) return [h('div', {'class':'pvbar', 'aria-hidden':'true'}, [h('b', {style:'width:' + (d * 100).toFixed(1) + '%'})]),
					h('p', {'class':'sum num', text:DC.t('已下載 {pct}%。', {pct:Math.floor(d * 100)})})];
				txt = c >= 1 ? DC.t('已下載完成，可以完整預覽。') : DC.sentences(DC.t('可以預覽開頭連續的 {pct}%（{size}）。', {pct:Math.floor(c * 100), size:DC.fsize(f.contiguous)}), d > c + 0.001 ? DC.t('其他已下載的片段不連續，要等中間補齊。') : '');
				return [h('div', {'class':'pvbar', 'aria-hidden':'true'}, [h('i', {style:'width:' + (c * 100).toFixed(1) + '%'}), h('b', {style:'width:' + (d * 100).toFixed(1) + '%'})]),
					c < 1 ? h('div', {'class':'pvkey'}, [h('span', null, [h('i', {'class':'k1'}), DC.t('可預覽')]), h('span', null, [h('i', {'class':'k2'}), DC.t('已下載')])]) : null,
					h('p', {'class':'sum num', text:txt})];
			}
			function show(f){
				var src = DC.api.url('tasks/' + t.id + '/preview', {file:f.index}), pre;
				clear(area);
				if(f.contiguous <= 0){ add(area, [h('p', {'class':'note', text:DC.t('這個檔案的開頭還沒下載，暫時無法預覽。')}), bars(f)]); return; }
				if((f.type === 'video' || f.type === 'audio') && f.playable){
					add(area, [h(f.type === 'video' ? 'video' : 'audio', {'class':f.type === 'video' ? 'pvmedia' : 'pvaudio', controls:true, preload:'metadata', src:src, onplay:function(){ if(!this._played){ this._played = true; DC.track('preview_play'); } }}), bars(f)]);
				}else if(f.type === 'video' || f.type === 'audio'){
					add(area, [h('p', {'class':'note', text:DC.t('瀏覽器無法直接播放這種格式，可以下載已下載的片段在電腦上播放。')}), bars(f),
						h('a', {'class':'ib btn', href:DC.api.url('tasks/' + t.id + '/preview', {file:f.index, download:1}), download:shown(DC.baseName(f.path))}, [icon('down'), h('span', {text:DC.t('下載預覽片段')})])]);
				}else if(f.type === 'image'){
					add(area, [h('img', {'class':'pvimg', src:src, alt:shown(DC.baseName(f.path))}), bars(f)]);
				}else if(f.type === 'text'){
					pre = h('pre', {'class':'pvtext mono', text:DC.t('讀取中…')});
					add(area, [pre, bars(f)]);
					DC.api.text('tasks/' + t.id + '/preview?file=' + f.index, 65536).then(function(txt){ pre.textContent = txt.length >= 65536 ? txt + '\n…' : txt; }, function(e){ pre.textContent = DC.errText(e); });
				}else if(f.type === 'archive' && f.playable){
					var list = h('div', {'class':'items'});
					add(area, [h('p', {'class':'note', text:DC.t('以下是目前讀得到的內容。')}), list, bars(f)]);
					DC.api.get('tasks/' + t.id + '/archive', {file:f.index}).then(function(a){
						var k, es = a.entries || [];
						if(!es.length) list.appendChild(h('div', {'class':'item'}, [icon('files'), h('span', {text:DC.t('還讀不到目錄。')}), h('em')]));
						for(k = 0; k < es.length && k < 500; k++) list.appendChild(h('div', {'class':'item'}, [icon(es[k].dir ? 'folder' : 'files'), h('span', {'class':'mono', text:es[k].name}), h('em', {'class':'num', text:es[k].dir ? '' : DC.fsize(es[k].size)})]));
					}, function(e){ list.appendChild(h('p', {'class':'note warn', text:DC.errText(e)})); });
				}else if(f.type === 'archive'){
					add(area, [h('p', {'class':'note', text:/\.(iso|img|dmg)(\.dsdownload)?$/i.test(f.path) ? DC.t('光碟映像檔無法預覽。下載完成後可以在 File Station 掛載。') : DC.t('這種壓縮檔無法在下載中預覽。')}), bars(f, false)]);
				}else add(area, [h('p', {'class':'note', text:DC.t('這種檔案無法預覽。')}), bars(f, false)]);
			}
			show(files[best]);
		}, function(e){ clear(D.body); D.body.appendChild(h('p', {'class':'note warn', text:DC.errText(e)})); });
	}

	/* ---------- connections: live traffic, recorded from the moment the tab opens ----------
	   Download rises above the middle line and upload hangs below it, each half with its own labelled scale.
	   Torrents also get one strip per peer; all strips share one scale so an idle peer reads as flat. */
	var NS = 'http://www.w3.org/2000/svg', WIN = 120000, POLL = 1000, MAXPEERS = 200;
	var UNITS = [['KB', 1024], ['MB', 1048576], ['GB', 1073741824]], STEPS = [1, 2, 5, 10, 20, 50, 100, 200, 500];
	function svg(tag, attrs, kids){
		var el = document.createElementNS(NS, tag), k;
		for(k in attrs) if(attrs.hasOwnProperty(k) && attrs[k] !== null && attrs[k] !== undefined) el.setAttribute(k, attrs[k]);
		return add(el, kids);
	}
	/* The scale only moves between round values, so it holds still while the speed wobbles */
	function nice(v, floor){
		var i, j, m;
		v = Math.max(v, floor);
		for(i = 0; i < UNITS.length; i++){
			m = v / UNITS[i][1];
			for(j = 0; j < STEPS.length; j++) if(m <= STEPS[j]) return {v:STEPS[j] * UNITS[i][1], label:STEPS[j] + ' ' + UNITS[i][0] + '/s'};
		}
		return {v:v, label:DC.fspeed(v)};
	}
	function peak(list, key){ var m = 0, i; for(i = 0; i < list.length; i++) if(list[i][key] > m) m = list[i][key]; return m; }
	/* Line and area paths of one series; a gap (hidden page, failed request) breaks the line instead of bridging it */
	function trace(list, key, x, y, base){
		var line = '', area = '', run = [], i;
		function flush(){
			var s, k;
			if(!run.length) return;
			s = 'M' + run[0][0] + ' ' + run[0][1];
			for(k = 1; k < run.length; k++) s += 'L' + run[k][0] + ' ' + run[k][1];
			line += s;
			area += s + 'L' + run[run.length - 1][0] + ' ' + base + 'L' + run[0][0] + ' ' + base + 'Z';
			run = [];
		}
		for(i = 0; i < list.length; i++){
			if(i && list[i].t - list[i - 1].t > POLL * 2.5) flush();
			run.push([x(list[i].t).toFixed(1), y(list[i][key]).toFixed(1)]);
		}
		flush();
		return {line:line, area:area};
	}
	/* Draws samples into a series set {dA, dL, uA, uL}; returns the y functions for the head dots */
	function plot(s, list, W, H, pad, sd, su, last){
		var mid = su ? H / 2 : H - 1, half = mid - pad, r;
		function x(t){ return W - (last - t) / WIN * W; }
		function yd(v){ return mid - Math.min(v / sd.v, 1) * half; }
		function yu(v){ return mid + Math.min(v / su.v, 1) * half; }
		r = trace(list, 'd', x, yd, mid);
		s.dA.setAttribute('d', r.area); s.dL.setAttribute('d', r.line);
		if(su){
			r = trace(list, 'u', x, yu, mid);
			s.uA.setAttribute('d', r.area); s.uL.setAttribute('d', r.line);
		}
		return {x:x, yd:yd, yu:yu, mid:mid, half:half};
	}
	function series(bt, grad){
		return {
			dA:svg('path', {'class':'area d', fill:grad ? 'url(#dcFlowD)' : null}), dL:svg('path', {'class':'line d'}),
			uA:bt ? svg('path', {'class':'area u', fill:grad ? 'url(#dcFlowU)' : null}) : null, uL:bt ? svg('path', {'class':'line u'}) : null
		};
	}
	function readout(cls, label){
		var v = h('b', {'class':'rdv num'});
		return {v:v, el:h('div', {'class':'rd ' + cls}, [h('span', {'class':'rdl'}, [h('i', {'aria-hidden':'true'}), label]), v])};
	}
	function setSpeed(el, b){
		var p = DC.fspeedParts(b);
		clear(el); add(el, [p[0], h('small', {text:p[1]})]);
	}
	function flowChart(bt){
		var c = {bt:bt, H:bt ? 132 : 96};
		c.s = series(bt, true);
		c.grid = svg('g', {'class':'grid'});
		c.mark = svg('line', {'class':'mark', y1:0, y2:c.H});
		c.markText = svg('text', {'class':'mark', y:12, 'text-anchor':'end'}, DC.t('開始記錄'));
		c.dP = svg('circle', {'class':'head d', r:3.5});
		c.uP = bt ? svg('circle', {'class':'head u', r:3.5}) : null;
		c.svg = svg('svg', {'class':'flowsvg', height:c.H, role:'img', 'aria-label':bt ? DC.t('最近兩分鐘的下載與上傳速度') : DC.t('最近兩分鐘的下載速度')}, [
			svg('defs', null, [
				svg('linearGradient', {id:'dcFlowD', x1:0, y1:0, x2:0, y2:1}, [svg('stop', {offset:0, 'class':'g0'}), svg('stop', {offset:1, 'class':'g1'})]),
				bt ? svg('linearGradient', {id:'dcFlowU', x1:0, y1:1, x2:0, y2:0}, [svg('stop', {offset:0, 'class':'g0'}), svg('stop', {offset:1, 'class':'g1'})]) : null
			]),
			c.grid, c.mark, c.markText, c.s.dA, c.s.uA, c.s.dL, c.s.uL, c.dP, c.uP
		]);
		c.top = h('span', {'class':'flowlbl top num', 'aria-hidden':'true'});
		c.bot = bt ? h('span', {'class':'flowlbl bot num', 'aria-hidden':'true'}) : null;
		c.dn = readout('d', DC.t('下載速度'));
		c.up = bt ? readout('u', DC.t('上傳速度')) : null;
		c.el = h('div', {'class':'flow'}, [
			h('div', {'class':'flowread'}, [c.dn.el, c.up ? c.up.el : null]),
			h('div', {'class':'flowplot'}, [c.svg, c.top, c.bot]),
			h('div', {'class':'flowaxis', 'aria-hidden':'true'}, [h('span', {text:DC.t('2 分鐘前')}), h('span', {text:DC.t('現在')})])
		]);
		return c;
	}
	function drawFlow(c, list, t0){
		var W = c.svg.getBoundingClientRect().width, H = c.H, last, p, sd, su = null, g, xs, cur;
		if(!W || !list.length) return;
		last = list[list.length - 1].t; cur = list[list.length - 1];
		sd = nice(peak(list, 'd'), 64 * 1024);
		if(c.bt) su = nice(peak(list, 'u'), 64 * 1024);
		c.svg.setAttribute('viewBox', '0 0 ' + W + ' ' + H);
		p = plot(c.s, list, W - 4, H, 8, sd, su, last);
		clear(c.grid);
		g = function(y, cls){ c.grid.appendChild(svg('line', {'class':cls, x1:0, x2:W, y1:y, y2:y})); };
		g(Math.round(p.mid - p.half) + 0.5, 'cap');
		g(Math.round(p.mid) - 0.5, 'base');
		if(su) g(Math.round(p.mid + p.half) - 0.5, 'cap');
		c.dP.setAttribute('cx', W - 4); c.dP.setAttribute('cy', p.yd(cur.d));
		if(su){ c.uP.setAttribute('cx', W - 4); c.uP.setAttribute('cy', p.yu(cur.u)); }
		/* Where recording began: everything left of it is before the tab was opened */
		xs = Math.round(p.x(t0)) + 0.5;
		c.mark.setAttribute('x1', xs); c.mark.setAttribute('x2', xs);
		c.mark.style.display = xs > 0 ? '' : 'none';
		c.markText.setAttribute('x', xs - 6);
		c.markText.style.display = xs > 96 ? '' : 'none';
		c.top.textContent = sd.label;
		if(su) c.bot.textContent = su.label;
		setSpeed(c.dn.v, cur.d);
		if(c.up) setSpeed(c.up.v, cur.u);
	}

	function peersTab(t){
		var id = t.id, bt = t.proto === 'bt', gen = D.flowGen = (D.flowGen || 0) + 1, t0 = Date.now();
		var chart = flowChart(bt), samples = [], kvBox = h('div'), list = null, note = null, rows = {}, order = [], sorted = false;
		clear(D.body);
		add(D.body, [chart.el, kvBox]);
		if(bt){
			list = h('div', {'class':'items peers'}, h('div', {'class':'item phead', 'aria-hidden':'true'}, [h('span'), h('span', {text:DC.t('使用者與用戶端')}),
				h('em', null, [h('span', {text:'↓'}), h('span', {text:'↑'}), h('span', {text:DC.t('進度')})])]));
			note = h('p', {'class':'note', text:DC.t('每位使用者的線圖共用同一個刻度，下載往上、上傳往下。')});
			list.hidden = note.hidden = true;
			add(D.body, [list, note]);
		}
		function current(){ return D.tab === 'peers' && D.id === id && D.flowGen === gen; }
		function trim(a, now){ while(a.length && a[0].t < now - WIN - POLL * 3) a.shift(); }
		function peerRow(){
			var r = {list:[], seed:null, s:series(true, false)};
			r.ip = h('b', {'class':'mono'}); r.client = h('small');
			r.dn = h('span'); r.up = h('span'); r.pct = h('span');
			r.mid = svg('line', {'class':'mid', x1:0, y1:12, y2:12});
			r.svg = svg('svg', {'class':'spark', height:24, 'aria-hidden':'true'}, [r.mid, r.s.dA, r.s.uA, r.s.dL, r.s.uL]);
			r.ic = icon('user');
			r.el = h('div', {'class':'item'}, [r.ic, h('span', null, [r.ip, r.client]), h('em', {'class':'num'}, [r.dn, r.up, r.pct]), r.svg]);
			return r;
		}
		function updPeers(ps, now){
			var seen = {}, i, k, p, r, all = [], sd, su, W;
			ps = ps.slice();
			/* The first answer is ordered by traffic; later peers join at the end so rows never jump while being read */
			if(!sorted){ ps.sort(function(a, b){ return (b.down_rate + b.up_rate) - (a.down_rate + a.up_rate); }); sorted = true; }
			for(i = 0; i < ps.length; i++){
				p = ps[i]; k = p.ip + ':' + p.port;
				if(seen[k]) continue;
				seen[k] = true;
				r = rows[k];
				if(!r){
					if(order.length >= MAXPEERS) continue;
					r = rows[k] = peerRow(); order.push(k); list.appendChild(r.el);
					r.ip.textContent = p.ip;
				}
				if(r.seed !== !!p.seeder){
					r.seed = !!p.seeder;
					var ic = icon(r.seed ? 'seed' : 'user');
					r.el.replaceChild(ic, r.ic); r.ic = ic;
				}
				if(r.client.textContent !== (p.client || '')) r.client.textContent = p.client || '';
				r.dn.textContent = DC.fspeed(p.down_rate); r.up.textContent = DC.fspeed(p.up_rate);
				r.pct.textContent = Math.floor((p.progress || 0) * 100) + '%';
				r.list.push({t:now, d:p.down_rate || 0, u:p.up_rate || 0});
				trim(r.list, now);
			}
			for(i = order.length - 1; i >= 0; i--){
				if(seen[order[i]]) continue;
				DC.remove(rows[order[i]].el); delete rows[order[i]]; order.splice(i, 1);
			}
			list.hidden = note.hidden = !order.length;
			if(!order.length) return;
			for(i = 0; i < order.length; i++) all = all.concat(rows[order[i]].list);
			sd = nice(peak(all, 'd'), 16 * 1024); su = nice(peak(all, 'u'), 16 * 1024);
			W = rows[order[0]].svg.getBoundingClientRect().width;
			if(!W) return;
			for(i = 0; i < order.length; i++){
				r = rows[order[i]];
				r.svg.setAttribute('viewBox', '0 0 ' + W + ' 24');
				r.mid.setAttribute('x2', W);
				plot(r.s, r.list, W, 24, 1, sd, su, now);
			}
		}
		function updKv(n){
			var x = task(), tk = (D.extra && D.extra.trackers) || [];
			clear(kvBox);
			if(!bt) return;
			kvBox.appendChild(h('dl', {'class':'kv num'}, [
				h('dt', {text:DC.t('已連線')}), h('dd', {text:DC.t('{n} 位使用者', {n:n})}),
				h('dt', {text:DC.t('完整來源')}), h('dd', {text:DC.t('{n} 個', {n:(x && x.seeds) || 0})}),
				tk.length ? h('dt', {text:DC.t('額外 tracker')}) : null, tk.length ? h('dd', {'class':'mono pre', text:tk.join('\n')}) : null
			]));
		}
		function load(){
			if(!current()) return;
			/* Nothing is sampled while the page is hidden; the chart shows that stretch as a gap */
			if(document.hidden){ D.peerTimer = setTimeout(load, POLL); return; }
			var asked = Date.now();
			DC.api.get('tasks/' + id + '/peers', null, {quiet:true}).then(function(r){
				if(!current()) return;
				var now = Date.now(), ps = r.peers || [];
				samples.push({t:now, d:r.down_rate || 0, u:r.up_rate || 0});
				trim(samples, now);
				if(bt){ updKv(ps.length); updPeers(ps, now); }
				drawFlow(chart, samples, t0);
				D.peerTimer = setTimeout(load, Math.max(250, POLL - (Date.now() - asked)));
			}, function(){ if(current()) D.peerTimer = setTimeout(load, POLL * 3); });
		}
		updKv(t.peers || 0);
		load();
	}

	function logTab(t){
		var lines = (D.extra && D.extra.log) || [], ul = h('ul', {'class':'log num'}), i;
		clear(D.body);
		if(!lines.length){ D.body.appendChild(h('p', {'class':'note', text:DC.t('還沒有紀錄。')})); return; }
		for(i = 0; i < lines.length; i++) ul.appendChild(h('li', null, [h('time', {text:DC.ftime(lines[i].time)}), h('span', {text:lines[i].msg})]));
		D.body.appendChild(ul);
	}

	DC.detail = {open:open, close:close, live:live, current:function(){ return D.id; }};
})();
