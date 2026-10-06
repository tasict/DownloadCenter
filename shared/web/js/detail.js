/* Task inspector: overview, files, preview, peers and log. A right-hand glass drawer; a near full-screen sheet on phones. */
(function(){
	'use strict';
	var DC = window.DC, h = DC.h, add = DC.add, clear = DC.clear, icon = DC.icon, ibtn = DC.ibtn, btn = DC.btn;
	var D = {id:null, tab:'info', el:null, scrim:null, extra:null, peerTimer:null, subURL:null};

	/* The preview's subtitle track lives in a blob URL until the file, the tab or the drawer changes */
	function dropSubs(){
		if(D.subURL){ URL.revokeObjectURL(D.subURL); D.subURL = null; }
	}
	function close(){
		clearTimeout(D.peerTimer);
		dropSubs();
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
		head = h('div', {'class':'dr-head'}, [h('div', {'class':'grab', 'aria-hidden':'true'}), D.icon, h('div', null, [D.title, D.sub]), ibtn('close', DC.t('Close'), close)]);
		D.el = h('aside', {'class':'drawer', role:'dialog', 'aria-modal':'true', 'aria-label':t.name || t.source}, [
			head, D.tabs, D.body,
			h('div', {'class':'dr-foot'}, [D.act, D.folderBtn = btn('folder', DC.t('Open folder'), function(){ var x = task(); if(x) DC.openFolder(x); }),
				DC.can('tasks:remove') ? btn('trash', DC.t('Delete'), function(){ DC.askDelete([D.id]); }, 'dan') : null])
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
		D.sub.textContent = DC.t('{kind}, added by {owner}', {kind:DC.KIND_LABEL[t.kind] || '', owner:t.owner});
		if(D.title.textContent !== (t.name || t.source)) D.title.textContent = t.name || t.source;
		if(D.state === key) return;
		D.state = key;
		D.icon.className = 'sicon st-' + s;
		clear(D.icon).appendChild(icon(DC.ST[s].icon));
		/* The one thing most often done from the details: pause, resume or retry, as the first footer button. */
		clear(D.act);
		if(DC.can('tasks:control')){
			if(s === 'down' || s === 'wait' || s === 'check') D.act.appendChild(btn('hold', DC.t('Pause'), function(){ DC.act(t.id, 'pause'); }, 'pri'));
			else if(s === 'seed') D.act.appendChild(btn('hold', DC.t('Stop seeding'), function(){ DC.act(t.id, 'pause'); }, 'pri'));
			else if(s === 'pause') D.act.appendChild(btn('play', DC.t('Resume'), function(){ DC.act(t.id, 'resume'); }, 'pri'));
			else if(s === 'error') D.act.appendChild(btn('retry', DC.t('Retry'), function(){ DC.act(t.id, 'retry'); }, 'pri'));
		}
	}
	function renderTabs(){
		var tabs = [['info', DC.t('Overview')], ['files', DC.t('Files')], ['preview', DC.t('Preview')], ['peers', DC.t('Connections')], ['log', DC.t('Log')]], i;
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
		dropSubs();
		if(D.tab === 'info') info(t);
		else if(D.tab === 'files') filesTab(t);
		else if(D.tab === 'preview') previewTab(t);
		else if(D.tab === 'peers') peersTab(t);
		else logTab(t);
	}

	/* Another proxy for a URL task: a running download reconnects through it and continues where it was. */
	function changeProxy(t){
		DC.api.get('proxies').then(function(r){
			var opts = [['auto', r.by_site ? (r['default'] ? DC.t('Automatic (by site, otherwise {name})', {name:r['default']}) : DC.t('Automatic (by site, otherwise no proxy)')) : (r['default'] ? DC.t('Default ({name})', {name:r['default']}) : DC.t('Default (no proxy)'))]], k;
			if(r.can_direct) opts.push(['none', DC.t('No proxy')]);
			for(k = 0; k < (r.profiles || []).length; k++) opts.push([r.profiles[k].id, r.profiles[k].name]);
			var sel = DC.select('dpProxy', opts, t.proxy || 'auto');
			DC.modal(DC.t('Change proxy'), 'link', [h('p', {'class':'lead', text:DC.t('A running task reconnects and continues from what it has already downloaded.')}), DC.field(DC.t('Proxy'), null, sel, 'dpProxy')], function(close){
				var ok = btn(null, DC.t('Change'), function(){
					DC.busy(ok, true);
					DC.api.patch('tasks/' + t.id, {proxy:sel.value}).then(function(res){
						close();
						if(res.task) DC.upsertTask(res.task);
						DC.toast(DC.t('Proxy changed'));
						loadExtra();
					}, function(e){ DC.busy(ok, false); DC.toast(DC.errText(e)); });
				}, 'pri');
				return [btn(null, DC.t('Cancel'), close), ok];
			});
		}, function(e){ DC.toast(DC.errText(e)); });
	}

	function info(t){
		var s = DC.uiState(t), ex = D.extra || {}, srcs = ex.sources || [], srcText, i, admin = DC.isAdmin(), kv;
		if(srcs.length){
			srcText = [];
			for(i = 0; i < srcs.length; i++) srcText.push((srcs[i].active ? '● ' : '○ ') + (srcs[i].name || srcs[i].source) + (srcs[i].active ? DC.t(' (in use)') : ''));
			srcText = srcText.join('\n');
		}else srcText = t.source;
		var stateText = DC.ST[s].label;
		/* URL tasks: the proxy in use (resolved by the server for the detail view), changeable while not finished */
		var xt = ex.task || {}, pxText = null;
		if(t.proxy){
			if(!ex.task) pxText = '…';
			else if(xt.proxy_error) pxText = DC.t('Unavailable: {error}', {error:xt.proxy_error});
			else if(xt.proxy_name) pxText = t.proxy === 'auto' ? DC.t('{name} (automatic)', {name:xt.proxy_name}) : xt.proxy_name;
			else pxText = DC.t('No proxy');
		}
		if(t.state === 'metadata') stateText = DC.t('Getting file list');
		if(t.sched_paused) stateText += DC.t(' (paused by schedule)');
		if(t.wake_time && s === 'pause') stateText += DC.t(', resumes at {time}', {time:DC.fclock(t.wake_time)});
		if(t.error) stateText += DC.t(': {error}', {error:t.error.message});
		var inTemp = t.in_temp && s !== 'done' && s !== 'move';
		/* Paths break after a slash rather than mid-name, and can be copied */
		function path(text){
			var el = h('span', {'class':'mono pre path'}), parts = String(text || '').split('/'), k;
			for(k = 0; k < parts.length; k++){ if(k){ el.appendChild(document.createTextNode('/')); el.appendChild(h('wbr')); } el.appendChild(document.createTextNode(parts[k])); }
			return h('dd', {'class':'pathdd'}, [el, ibtn('copy', DC.t('Copy path'), function(){ DC.copyText(text, el); })]);
		}
		kv = h('dl', {'class':'kv num'}, [
			h('dt', {text:DC.t('Status')}), h('dd', {text:stateText}),
			h('dt', {text:DC.t('Progress')}), h('dd', {text:DC.t('{pct}%, {done} / {size}', {pct:Math.floor(t.progress || 0), done:DC.fsize(t.done), size:DC.fsize(t.size)})}),
			h('dt', {text:DC.t('Speed')}), h('dd', {text:DC.t('Download {down}, upload {up}', {down:DC.fspeed(t.down_rate), up:DC.fspeed(t.up_rate)})}),
			h('dt', {text:DC.t('Download time')}), h('dd', {text:DC.fdur(t.active_secs) + (s === 'down' && t.eta > 0 ? DC.t('; ') + DC.feta(t.eta) : '')}),
			t.proto === 'bt' ? h('dt', {text:DC.t('Share ratio')}) : null, t.proto === 'bt' ? h('dd', {text:DC.t('{ratio}, {size} uploaded', {ratio:(t.ratio || 0).toFixed(2), size:DC.fsize(t.up_total)})}) : null,
			t.proto === 'bt' ? h('dt', {text:DC.t('Connections')}) : null, t.proto === 'bt' ? h('dd', {text:DC.t('{peers} peers, {seeds} seeds', {peers:t.peers || 0, seeds:t.seeds || 0})}) : null,
			xt.private ? h('dt', {text:DC.t('Private torrent')}) : null, xt.private ? h('dd', {text:DC.t('Finds peers only through its tracker')}) : null,
			h('dt', {text:admin ? DC.t('Temporary location') : DC.t('Save to')}), path(t.folder),
			inTemp && t.location ? h('dt', {text:DC.t('Files being downloaded')}) : null, inTemp && t.location ? path(t.location) : null,
			t.move_to ? h('dt', {text:DC.t('Move to when finished')}) : null, t.move_to ? path(t.move_to) : null,
			s === 'done' || s === 'seed' && !t.in_temp ? h('dt', {text:DC.t('File location')}) : null, s === 'done' || s === 'seed' && !t.in_temp ? path(t.location) : null,
			h('dt', {text:srcs.length > 1 ? DC.t('Sources (merged)') : DC.t('Source')}), h('dd', {'class':'mono pre', text:srcText}),
			t.auto_remove ? h('dt', {text:DC.t('When finished')}) : null, t.auto_remove ? h('dd', {text:t.auto_remove === 'completed' ? DC.t('Remove from the list when the download finishes') : DC.t('Remove from the list when seeding finishes')}) : null,
			t.comment ? h('dt', {text:DC.t('Help')}) : null, t.comment ? h('dd', {text:t.comment}) : null,
			h('dt', {text:DC.t('Added on')}), h('dd', {text:DC.ftime(t.created_at)}),
			t.finished_at ? h('dt', {text:DC.t('Finished at')}) : null, t.finished_at ? h('dd', {text:DC.ftime(t.finished_at)}) : null,
			admin ? h('dt', {text:DC.t('Owner')}) : null, admin ? h('dd', {text:t.owner + (t.caller && t.caller !== 'Download Center' ? DC.t(' ({caller})', {caller:t.caller}) : '')}) : null,
			pxText ? h('dt', {text:DC.t('Proxy')}) : null,
			pxText ? h('dd', {'class':'inline'}, [h('span', {text:pxText}), DC.can('tasks:control') && s !== 'done' ? ibtn('edit', DC.t('Change proxy'), function(){ changeProxy(t); }) : null]) : null,
			admin && t.engine ? h('dt', {text:DC.t('Engine')}) : null, admin && t.engine ? h('dd', {text:t.engine}) : null
		]);
		clear(D.body);
		D.body.appendChild(kv);
		if(t.proto === 'bt' && t.files_total > 0){
			D.body.appendChild(h('div', {'class':'srcs'}, [h('a', {'class':'ib btn', href:DC.api.url('tasks/' + t.id + '/torrent'), download:(t.name || 'task') + '.torrent'}, [icon('torrent'), h('span', {text:DC.t('Download .torrent file')})])]));
		}
	}

	var PRIO = [[7, DC.t('High')], [4, DC.t('Medium')], [1, DC.t('Low')], [0, DC.t('Don\'t download')]];
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
		D.body.appendChild(h('div', {'class':'loading'}, [icon('check'), DC.t('Loading…')]));
		DC.api.get('tasks/' + t.id + '/files').then(function(r){
			if(D.tab !== 'files' || D.id !== t.id) return;
			var files = r.files || [], list = h('div', {'class':'items'}), dirty = false, save, levels = !!(DC.S.me.caps && DC.S.me.caps.file_priority_levels) && t.engine === DC.S.me.bt_engine, i;
			var editable = t.proto === 'bt' && DC.can('tasks:control') && t.state !== 'metadata', root = rootOf(files);
			clear(D.body);
			if(!files.length){ D.body.appendChild(h('p', {'class':'note', text:t.state === 'metadata' ? DC.t('Still getting the file list.') : DC.t('No file information yet.')})); return; }
			function mark(){ dirty = true; if(save) save.disabled = false; }
			for(i = 0; i < files.length; i++){
				(function(f, idx){
					var pct = f.size ? Math.floor(f.done * 100 / f.size) : 0, ctl;
					if(levels && editable){
						ctl = DC.select('fp' + idx, PRIO, String(f.priority >= 6 ? 7 : f.priority >= 3 ? 4 : f.priority > 0 ? 1 : 0), function(){ f.priority = +this.value; mark(); });
						list.appendChild(h('div', {'class':'item'}, [icon('files'), h('span', null, [shown(f.path.slice(root.length)), h('em', {'class':'num', text:' ' + DC.fsize(f.size) + DC.t(', {pct}%', {pct:pct})})]), ctl]));
					}else{
						list.appendChild(h('label', {'class':'item', 'for':'df' + idx}, [h('input', {type:'checkbox', id:'df' + idx, checked:f.priority > 0, disabled:!editable, onchange:function(){ f.priority = this.checked ? 1 : 0; mark(); }}),
							h('span', {text:shown(f.path.slice(root.length))}), h('em', {'class':'num', text:DC.fsize(f.size) + (f.done && f.priority > 0 ? DC.t(', {pct}%', {pct:pct}) : '')})]));
					}
				})(files[i], i);
			}
			add(D.body, [
				root ? h('p', {'class':'note mono', text:DC.t('Folder: {folder}', {folder:root.slice(0, -1)})}) : null,
				editable ? h('p', {'class':'note', text:levels ? DC.t('Files set to “Don\'t download” will no longer be downloaded. Parts already downloaded stay on the disk.') : DC.t('Unchecked files will no longer be downloaded. Parts already downloaded stay on the disk.')}) : null, list]);
			if(editable){
				save = btn(null, DC.t('Apply'), function(){
					var body, sel = [], k, n = 0;
					if(levels){ body = {}; for(k = 0; k < files.length; k++){ body[String(files[k].index)] = files[k].priority; if(files[k].priority > 0) n++; } }
					else{ for(k = 0; k < files.length; k++) if(files[k].priority > 0) sel.push(files[k].index); body = sel; n = sel.length; }
					if(!n){ DC.toast(DC.t('Select at least one file')); return; }
					DC.busy(save, true);
					DC.api.patch('tasks/' + t.id, {files:body}).then(function(res){ DC.busy(save, false); save.disabled = true; dirty = false; if(res.task) DC.upsertTask(res.task); DC.toast(DC.t('Files to download updated')); }, function(e){ DC.busy(save, false); DC.toast(DC.errText(e)); });
				}, 'pri');
				save.disabled = true;
				D.body.appendChild(h('div', {'class':'savebar'}, [save]));
			}
		}, function(e){ clear(D.body); D.body.appendChild(h('p', {'class':'note warn', text:DC.errText(e)})); });
	}

	function previewTab(t){
		clear(D.body);
		D.body.appendChild(h('div', {'class':'loading'}, [icon('check'), DC.t('Loading…')]));
		DC.api.get('tasks/' + t.id + '/preview-info').then(function(r){
			if(D.tab !== 'preview' || D.id !== t.id) return;
			var files = r.files || [], chooser = null, area = h('div'), i, best = 0;
			clear(D.body);
			if(!files.length){ D.body.appendChild(h('p', {'class':'note', text:DC.t('No files to preview yet.')})); return; }
			/* Default to the largest audio or video file, else to the largest file */
			best = -1;
			for(i = 0; i < files.length; i++) if((files[i].type === 'video' || files[i].type === 'audio') && (best < 0 || files[i].size > files[best].size)) best = i;
			if(best < 0){ best = 0; for(i = 1; i < files.length; i++) if(files[i].size > files[best].size) best = i; }
			if(files.length > 1){
				chooser = h('select', {id:'pvFile', 'aria-label':DC.t('File to preview'), onchange:function(){ show(files[+this.value]); }});
				for(i = 0; i < files.length; i++) chooser.appendChild(h('option', {value:String(i), text:shown(files[i].path)}));
				chooser.value = String(best);
				D.body.appendChild(h('div', {'class':'inline pvsel'}, chooser));
			}
			D.body.appendChild(area);
			if(r.sequential_supported && DC.can('tasks:control') && t.state !== 'done'){
				D.body.appendChild(h('label', {'class':'toggle', 'for':'pvStream'}, [h('input', {type:'checkbox', id:'pvStream', checked:!!r.sequential, onchange:function(){
					var on = this.checked;
					if(on) DC.track('stream_on');
					DC.api.patch('tasks/' + t.id, {sequential:on}).then(function(){ DC.toast(on ? DC.t('Download in order (watch while downloading)') : DC.t('Back to normal download')); }, function(e){ DC.toast(DC.errText(e)); });
				}}), h('span', null, [DC.t('Watch while downloading'), h('small', {text:DC.t('Download in order with the beginning and end first; the total download time may be longer')})])]));
			}
			/* The bar has two layers: the readable start of the file and everything downloaded so far. Files that cannot be
			   previewed at all only show how much is downloaded. */
			function bars(f, viewable){
				var c = f.size ? f.contiguous / f.size : 0, d = f.size ? Math.max(f.downloaded, f.contiguous) / f.size : 0, txt;
				if(viewable === false) return [h('div', {'class':'pvbar', 'aria-hidden':'true'}, [h('b', {style:'width:' + (d * 100).toFixed(1) + '%'})]),
					h('p', {'class':'sum num', text:DC.t('{pct}% downloaded.', {pct:Math.floor(d * 100)})})];
				txt = c >= 1 ? DC.t('Download finished; the whole file can be previewed.') : DC.sentences(DC.t('The first {pct}% ({size}) can be previewed.', {pct:Math.floor(c * 100), size:DC.fsize(f.contiguous)}), d > c + 0.001 ? DC.t('The other downloaded parts are not contiguous; wait for the gaps to fill.') : '');
				return [h('div', {'class':'pvbar', 'aria-hidden':'true'}, [h('i', {style:'width:' + (c * 100).toFixed(1) + '%'}), h('b', {style:'width:' + (d * 100).toFixed(1) + '%'})]),
					c < 1 ? h('div', {'class':'pvkey'}, [h('span', null, [h('i', {'class':'k1'}), DC.t('Previewable')]), h('span', null, [h('i', {'class':'k2'}), DC.t('Downloaded')])]) : null,
					h('p', {'class':'sum num', text:txt})];
			}
			/* A video with a same-name .srt/.vtt beside it gets a Subtitles switch. The file is rebuilt as clean WebVTT (DC.subs)
			   in a blob track; the switch and the player's own captions menu stay in step, and both choices are remembered. */
			function subtitles(video, sf){
				var name = shown(DC.baseName(sf.path)), on = DC.pref('subtitles') !== false, raw = null, guessed = false, track = null, box,
					input = h('input', {type:'checkbox', id:'pvSubs', checked:on, disabled:true, onchange:function(){
						on = this.checked;
						if(track) track.mode = on ? 'showing' : 'hidden';
						encRow.hidden = !(on && guessed);
						DC.savePref('subtitles', on);
					}}),
					note = h('small', {'class':'mono', text:name}),
					encRow = h('div', {'class':'pvenc', hidden:true});
				box = h('div', {'class':'pvsubs'}, [h('label', {'class':'toggle', 'for':'pvSubs'}, [input, h('span', null, [DC.t('Subtitles'), note])]), encRow]);
				add(area, box);
				function off(text){
					dropSubs();
					if(track && track.mode !== 'disabled') track.mode = 'disabled';
					clear(video);
					track = null; input.disabled = true; input.checked = false; encRow.hidden = true;
					note.className = ''; note.textContent = text;
				}
				function attach(d){
					var vtt = d && DC.subs.toVTT(d.text), el;
					if(!vtt){ off(DC.t('{name} could not be read as subtitles.', {name:name})); return false; }
					if(track) track.mode = 'disabled';
					dropSubs();
					clear(video);
					D.subURL = URL.createObjectURL(new Blob([vtt], {type:'text/vtt'}));
					el = h('track', {kind:'subtitles', label:DC.t('Subtitles'), src:D.subURL});
					video.appendChild(el);
					track = el.track;
					track.mode = on ? 'showing' : 'hidden';
					return true;
				}
				function encodings(enc){
					var names = {'utf-8':DC.t('Unicode (UTF-8)'), big5:DC.t('Traditional Chinese (Big5)'), gb18030:DC.t('Simplified Chinese (GB18030)'),
						shift_jis:DC.t('Japanese (Shift_JIS)'), 'euc-kr':DC.t('Korean (EUC-KR)'), 'windows-1252':DC.t('Western European (Windows-1252)')}, i,
						sel = h('select', {id:'pvEnc', onchange:function(){
							if(!attach(DC.subs.decodeAs(raw, this.value))) return;
							DC.savePref('subtitle_encoding', this.value);
							DC.track('subs_enc');
						}});
					for(i = 0; i < DC.subs.ENCODINGS.length; i++) sel.appendChild(h('option', {value:DC.subs.ENCODINGS[i], text:names[DC.subs.ENCODINGS[i]]}));
					sel.value = enc;
					add(encRow, [h('label', {'for':'pvEnc', text:DC.t('Text encoding')}), sel]);
				}
				/* The player's captions menu: follow it when it shows or hides our track */
				if(video.textTracks && video.textTracks.addEventListener) video.textTracks.addEventListener('change', function(){
					var s = !!track && track.mode === 'showing';
					if(!track || s === on) return;
					on = s; input.checked = s; encRow.hidden = !(on && guessed);
					DC.savePref('subtitles', on);
				});
				if(sf.contiguous < sf.size){ off(DC.t('Subtitles can be shown once {name} has finished downloading.', {name:name})); return; }
				DC.api.bytes('tasks/' + t.id + '/preview?file=' + sf.index, 4194304).then(function(buf){
					var d;
					if(!document.body.contains(box)) return;
					raw = buf;
					d = DC.subs.decode(buf, DC.pref('subtitle_encoding'), DC.lang);
					if(!attach(d)) return;
					guessed = d.guessed;
					if(guessed) encodings(d.enc);
					encRow.hidden = !(on && guessed);
					input.disabled = false;
					DC.track('preview_subs');
				}, function(){ if(document.body.contains(box)) off(DC.t('{name} could not be read as subtitles.', {name:name})); });
			}
			function show(f){
				var src = DC.api.url('tasks/' + t.id + '/preview', {file:f.index}), pre, media, sub;
				dropSubs();
				clear(area);
				if(f.contiguous <= 0){ add(area, [h('p', {'class':'note', text:DC.t('The beginning of this file has not been downloaded yet, so it cannot be previewed for now.')}), bars(f)]); return; }
				if((f.type === 'video' || f.type === 'audio') && f.playable){
					media = h(f.type === 'video' ? 'video' : 'audio', {'class':f.type === 'video' ? 'pvmedia' : 'pvaudio', controls:true, preload:'metadata', src:src, onplay:function(){ if(!this._played){ this._played = true; DC.track('preview_play'); } }});
					add(area, [media, bars(f)]);
					sub = DC.subs.match(files, f);
					if(sub) subtitles(media, sub);
				}else if(f.type === 'video' || f.type === 'audio'){
					add(area, [h('p', {'class':'note', text:DC.t('The browser cannot play this format directly. Download the downloaded part and play it on your computer.')}), bars(f),
						h('a', {'class':'ib btn', href:DC.api.url('tasks/' + t.id + '/preview', {file:f.index, download:1}), download:shown(DC.baseName(f.path))}, [icon('down'), h('span', {text:DC.t('Download previewable part')})])]);
				}else if(f.type === 'image'){
					add(area, [h('img', {'class':'pvimg', src:src, alt:shown(DC.baseName(f.path))}), bars(f)]);
				}else if(f.type === 'text'){
					pre = h('pre', {'class':'pvtext mono', text:DC.t('Loading…')});
					add(area, [pre, bars(f)]);
					DC.api.text('tasks/' + t.id + '/preview?file=' + f.index, 65536).then(function(txt){ pre.textContent = txt.length >= 65536 ? txt + '\n…' : txt; }, function(e){ pre.textContent = DC.errText(e); });
				}else if(f.type === 'archive' && f.playable){
					var list = h('div', {'class':'items'});
					add(area, [h('p', {'class':'note', text:DC.t('Here is what can be read so far.')}), list, bars(f)]);
					DC.api.get('tasks/' + t.id + '/archive', {file:f.index}).then(function(a){
						var k, es = a.entries || [];
						if(!es.length) list.appendChild(h('div', {'class':'item'}, [icon('files'), h('span', {text:DC.t('Cannot read the folder list yet.')}), h('em')]));
						for(k = 0; k < es.length && k < 500; k++) list.appendChild(h('div', {'class':'item'}, [icon(es[k].dir ? 'folder' : 'files'), h('span', {'class':'mono', text:es[k].name}), h('em', {'class':'num', text:es[k].dir ? '' : DC.fsize(es[k].size)})]));
					}, function(e){ list.appendChild(h('p', {'class':'note warn', text:DC.errText(e)})); });
				}else if(f.type === 'archive'){
					add(area, [h('p', {'class':'note', text:/\.(iso|img|dmg)(\.dsdownload)?$/i.test(f.path) ? DC.t('Disc images cannot be previewed. After the download finishes, you can mount them in File Station.') : DC.t('This type of archive cannot be previewed while downloading.')}), bars(f, false)]);
				}else add(area, [h('p', {'class':'note', text:DC.t('This type of file cannot be previewed.')}), bars(f, false)]);
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
		c.markText = svg('text', {'class':'mark', y:12, 'text-anchor':'end'}, DC.t('Recording started'));
		c.dP = svg('circle', {'class':'head d', r:3.5});
		c.uP = bt ? svg('circle', {'class':'head u', r:3.5}) : null;
		c.svg = svg('svg', {'class':'flowsvg', height:c.H, role:'img', 'aria-label':bt ? DC.t('Download and upload speed over the last two minutes') : DC.t('Download speed over the last two minutes')}, [
			svg('defs', null, [
				svg('linearGradient', {id:'dcFlowD', x1:0, y1:0, x2:0, y2:1}, [svg('stop', {offset:0, 'class':'g0'}), svg('stop', {offset:1, 'class':'g1'})]),
				bt ? svg('linearGradient', {id:'dcFlowU', x1:0, y1:1, x2:0, y2:0}, [svg('stop', {offset:0, 'class':'g0'}), svg('stop', {offset:1, 'class':'g1'})]) : null
			]),
			c.grid, c.mark, c.markText, c.s.dA, c.s.uA, c.s.dL, c.s.uL, c.dP, c.uP
		]);
		c.top = h('span', {'class':'flowlbl top num', 'aria-hidden':'true'});
		c.bot = bt ? h('span', {'class':'flowlbl bot num', 'aria-hidden':'true'}) : null;
		c.dn = readout('d', DC.t('Download speed'));
		c.up = bt ? readout('u', DC.t('Upload speed')) : null;
		c.el = h('div', {'class':'traffic'}, [
			h('div', {'class':'flowread'}, [c.dn.el, c.up ? c.up.el : null]),
			h('div', {'class':'flowplot'}, [c.svg, c.top, c.bot]),
			h('div', {'class':'flowaxis', 'aria-hidden':'true'}, [h('span', {text:DC.t('2 min ago')}), h('span', {text:DC.t('Now')})])
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
			list = h('div', {'class':'items peers'}, h('div', {'class':'item phead', 'aria-hidden':'true'}, [h('span'), h('span', {text:DC.t('Peer and client')}),
				h('em', null, [h('span', {text:'↓'}), h('span', {text:'↑'}), h('span', {text:DC.t('Progress')})])]));
			note = h('p', {'class':'note', text:DC.t('All peer graphs share one scale: download goes up, upload goes down.')});
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
				h('dt', {text:DC.t('Connected')}), h('dd', {text:DC.t('{n} users', {n:n})}),
				h('dt', {text:DC.t('Seeds')}), h('dd', {text:DC.t('{n}', {n:(x && x.seeds) || 0})}),
				tk.length ? h('dt', {text:DC.t('Extra trackers')}) : null, tk.length ? h('dd', {'class':'mono pre', text:tk.join('\n')}) : null
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
		if(!lines.length){ D.body.appendChild(h('p', {'class':'note', text:DC.t('No records yet.')})); return; }
		for(i = 0; i < lines.length; i++) ul.appendChild(h('li', null, [h('time', {text:DC.ftime(lines[i].time)}), h('span', {text:lines[i].msg})]));
		D.body.appendChild(ul);
	}

	DC.detail = {open:open, close:close, live:live, current:function(){ return D.id; }};
})();
