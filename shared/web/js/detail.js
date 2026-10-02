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
		var t = task(), tabs = [['info', DC.t('概要')], ['files', DC.t('檔案')], ['preview', DC.t('預覽')], ['peers', DC.t('連線')], ['log', DC.t('紀錄')]], i;
		clear(D.tabs);
		for(i = 0; i < tabs.length; i++){
			if(tabs[i][0] === 'peers' && t.proto !== 'bt') continue;
			D.tabs.appendChild(h('button', {'class':'tab' + (D.tab === tabs[i][0] ? ' on' : ''), role:'tab', type:'button', 'aria-selected':D.tab === tabs[i][0] ? 'true' : 'false',
				onclick:(function(id){ return function(){ D.tab = id; renderTabs(); renderBody(); if(id === 'log') loadExtra(); }; })(tabs[i][0])}, tabs[i][1]));
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
					add(area, [h(f.type === 'video' ? 'video' : 'audio', {'class':f.type === 'video' ? 'pvmedia' : 'pvaudio', controls:true, preload:'metadata', src:src}), bars(f)]);
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

	function peersTab(t){
		var id = t.id;
		function load(){
			DC.api.get('tasks/' + id + '/peers', null, {quiet:true}).then(function(r){
				if(D.tab !== 'peers' || D.id !== id) return;
				var ps = r.peers || [], list, i, p, tk = (D.extra && D.extra.trackers) || [];
				clear(D.body);
				D.body.appendChild(h('dl', {'class':'kv num'}, [
					h('dt', {text:DC.t('已連線')}), h('dd', {text:DC.t('{n} 位使用者', {n:ps.length})}),
					h('dt', {text:DC.t('完整來源')}), h('dd', {text:DC.t('{n} 個', {n:t.seeds || 0})}),
					tk.length ? h('dt', {text:DC.t('額外 tracker')}) : null, tk.length ? h('dd', {'class':'mono pre', text:tk.join('\n')}) : null
				]));
				if(ps.length){
					list = h('div', {'class':'items peers'}, h('div', {'class':'item phead', 'aria-hidden':'true'}, [h('span'), h('span', {text:DC.t('使用者與用戶端')}),
						h('em', null, [h('span', {text:'↓'}), h('span', {text:'↑'}), h('span', {text:DC.t('進度')})])]));
					for(i = 0; i < ps.length && i < 200; i++){
						p = ps[i];
						list.appendChild(h('div', {'class':'item'}, [icon(p.seeder ? 'seed' : 'user'), h('span', null, [h('b', {'class':'mono', text:p.ip}), p.client ? h('small', {text:p.client}) : null]),
							h('em', {'class':'num'}, [h('span', {text:DC.fspeed(p.down_rate)}), h('span', {text:DC.fspeed(p.up_rate)}), h('span', {text:Math.floor((p.progress || 0) * 100) + '%'})])]));
					}
					D.body.appendChild(list);
				}
				D.peerTimer = setTimeout(load, 3000);
			}, function(){ D.peerTimer = setTimeout(load, 5000); });
		}
		clear(D.body);
		D.body.appendChild(h('div', {'class':'loading'}, [icon('check'), DC.t('讀取中…')]));
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
