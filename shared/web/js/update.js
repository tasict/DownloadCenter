/* Updates, for administrators signed in to the UI: a quiet toolbar tag when a newer release is out, the update window with
   its progress, and the 關於與更新 settings page (every release, going back to an older one). dcd fetches the release
   index, checks the signature and runs the installer; the page only asks and shows. */
(function(){
	'use strict';
	var DC = window.DC, h = DC.h, add = DC.add, clear = DC.clear, icon = DC.icon, btn = DC.btn, R = DC.R;
	var U = {view:null};

	function allowed(){ return DC.isAdmin() && DC.S.me && DC.S.me.via === 'session'; }
	function load(){ return DC.api.get('update', null, {quiet:true}).then(function(v){ U.view = v; tag(); return v; }); }
	function boot(){ if(allowed()) load().then(lastResult, function(){}); }

	/* ---------- toolbar tag ---------- */
	function tag(){
		var v = U.view, el = R.upd;
		if(!el) return;
		clear(el);
		el.hidden = !(v && v.available && v.latest);
		if(el.hidden) return;
		el.title = DC.t('有新版本 {version}', {version:v.latest.version});
		add(el, [icon('up'), h('span', {text:DC.t('新版本 {version}', {version:v.latest.version})})]);
	}

	/* The outcome of the last update, once per browser */
	function lastResult(v){
		var l = v && v.last, seen = +(DC.ls('dc-update-seen') || 0);
		if(!l || l.at <= seen) return;
		DC.ls('dc-update-seen', l.at);
		DC.toast(l.ok ? DC.t('已更新到 {version}', {version:l.to}) : DC.t('更新到 {version} 沒有完成，仍是 {from}', {version:l.to, from:l.from}), null, 9000);
	}

	function sizeText(e){ return e.size ? DC.fsize(e.size) : ''; }

	/* ---------- the update window ---------- */
	function offer(e){
		var v = U.view || {}, isLatest = v.latest && v.latest.version === e.version;
		DC.modal(DC.t('Download Center {version}', {version:e.version}), 'up', [
			h('p', {'class':'lead num', text:[e.date, e.prerelease ? DC.t('測試版') : '', sizeText(e)].filter(function(x){ return !!x; }).join(DC.t('，'))}),
			e.notes ? h('pre', {'class':'relnotes', text:e.notes}) : h('p', {'class':'note', text:DC.t('這個版本沒有更新說明。')}),
			h('p', {'class':'note', text:DC.t('更新時套件會重新啟動，下載會暫停一下，之後自動接續。更新前會先備份資料庫。')})
		], function(close){
			return [
				isLatest ? h('button', {'class':'ib linkish skipv', type:'button', onclick:function(){
					close();
					DC.api.put('update/settings', {skip:e.version}).then(function(nv){ U.view = nv; tag(); DC.toast(DC.t('不會再提示 {version}', {version:e.version})); }, function(err){ DC.toast(DC.errText(err)); });
				}}, DC.t('略過這個版本')) : null,
				btn(null, DC.t('稍後'), close),
				btn(null, DC.t('立即更新'), function(){ close(); start(e.version, false); }, 'pri')
			];
		});
	}

	function goBack(e){
		var can = !e.needs_restore || e.backup_at, text;
		if(!e.needs_restore) text = DC.t('目前的資料庫會先備份，降版後繼續使用。');
		else if(e.backup_at) text = DC.t('這一版使用較舊的資料庫格式，會改用 {time} 的資料庫備份；之後新增或變更的任務與設定都會消失。', {time:DC.ftime(e.backup_at)});
		else text = DC.t('這一版使用較舊的資料庫格式，但找不到當時的資料庫備份，所以無法降回這一版。');
		DC.modal(DC.t('降回 {version}？', {version:e.version}), 'dn', [
			h('p', {'class':'lead', text:text}),
			e.notes ? h('pre', {'class':'relnotes', text:e.notes}) : null
		], function(close){
			var ok = btn(null, DC.t('降回這一版'), function(){ close(); start(e.version, !!e.needs_restore); }, 'dan pri');
			ok.disabled = !can;
			return [btn(null, DC.t('取消'), close), ok];
		});
	}

	function start(version, restore){
		DC.api.post('update/install', {version:version, restore:restore}).then(function(v){ U.view = v; progress(version); }, function(err){ DC.toast(DC.errText(err)); });
	}

	var FAIL = {
		download:DC.t('無法下載更新檔。'),
		signature:DC.t('更新檔的簽章驗證失敗，已停止更新。'),
		hash:DC.t('更新檔的內容和發布清單不符，已停止更新。'),
		backup:DC.t('無法備份資料庫，已停止更新。'),
		install:DC.t('無法啟動安裝程式。')
	};

	/* Progress: the download and checks come from dcd; once the installer runs dcd goes away, so the page waits for the
	   package to answer again with the new version and then reloads. */
	function progress(target){
		var phase = h('b', {'class':'up-phase', text:DC.t('準備中…')}), fill = h('i'), sub = h('p', {'class':'note num'});
		var closeW = DC.modal(DC.t('更新到 {version}', {version:target}), 'up', [
			h('div', {'class':'up-prog'}, [phase, h('div', {'class':'upbar', 'aria-hidden':'true'}, fill), sub]),
			h('p', {'class':'note', text:DC.t('更新期間請不要關閉這個頁面。')})
		], function(){ return []; }, {persist:true});
		var box = document.querySelectorAll('.modal'), t0 = Date.now(), wentDown = false;
		box = box[box.length - 1];
		function done(buttons){ var a = box && box.querySelector('.acts'); if(a){ clear(a); add(a, buttons); } }
		function failed(text, detail){
			phase.textContent = text; phase.className = 'up-phase bad';
			sub.textContent = detail || '';
			done([btn(null, DC.t('關閉'), function(){ closeW(); load(); }, 'pri')]);
		}
		function job(){
			DC.api.get('update/job', null, {quiet:true}).then(function(j){
				if(j.phase === 'download'){
					phase.textContent = DC.t('下載中…');
					if(j.total){ fill.style.width = Math.min(100, j.done * 100 / j.total).toFixed(1) + '%'; sub.textContent = DC.fsize(j.done) + ' / ' + DC.fsize(j.total); }
				}else if(j.phase === 'verify'){ phase.textContent = DC.t('檢查簽章與檔案…'); fill.style.width = '100%'; sub.textContent = ''; }
				else if(j.phase === 'backup') phase.textContent = DC.t('備份資料庫…');
				else if(j.phase === 'install'){ installing(); return; }
				else if(j.phase === 'failed'){ failed(FAIL[j.error] || DC.t('更新失敗。'), j.detail); return; }
				setTimeout(job, 800);
			}, function(){ installing(); });
		}
		/* dcd stops during the install; ask the package's health check until the new version answers */
		function installing(){
			phase.textContent = DC.t('安裝中，套件正在重新啟動…'); fill.style.width = '100%'; sub.textContent = '';
			health(function(ver){
				if(ver === null){ wentDown = true; return again(); }
				if(ver === target){
					phase.textContent = DC.t('已更新到 {version}，重新整理頁面…', {version:target});
					setTimeout(function(){ location.reload(); }, 1500);
					return;
				}
				/* The old version answers again after it was down: the installer did not complete */
				if(wentDown && Date.now() - t0 > 20000) return failed(DC.t('安裝沒有完成，仍是 {version}。', {version:ver}), DC.t('詳細記錄在套件的 data/logs/update.log。'));
				again();
			});
		}
		function again(){
			if(Date.now() - t0 > 8 * 60 * 1000) return failed(DC.t('等太久還沒有完成。請稍後重新整理頁面。'), DC.t('詳細記錄在套件的 data/logs/update.log。'));
			setTimeout(installing, 2000);
		}
		job();
	}

	function health(cb){
		var x = new XMLHttpRequest();
		x.open('GET', 'healthz?t=' + Date.now(), true);
		x.timeout = 4000;
		x.onload = function(){ var d = null; try{ d = JSON.parse(x.responseText); }catch(e){} cb(x.status === 200 && d && d.version ? d.version : null); };
		x.onerror = x.ontimeout = function(){ cb(null); };
		x.send();
	}

	/* ---------- settings: 關於與更新 ---------- */
	function settings(body){
		DC.loadingInto(body);
		DC.api.get('update').then(function(v){ U.view = v; tag(); clear(body); render(body, v); }, function(e){ DC.errorInto(body, e); });
	}

	function setPref(body, o){
		DC.api.put('update/settings', o).then(function(v){ U.view = v; tag(); clear(body); render(body, v); }, function(e){ DC.toast(DC.errText(e)); });
	}

	function render(body, v){
		var sec = DC.sec, toggle = DC.toggle, checking = false, checkB;
		var when = v.checked_at ? DC.t('上次檢查：{time}', {time:DC.ftime(v.checked_at)}) : DC.t('還沒有檢查過更新');
		checkB = btn('retry', DC.t('立即檢查'), function(){
			if(checking) return;
			checking = true; DC.busy(checkB, true, DC.t('檢查中…'));
			DC.api.post('update/check', {}).then(function(nv){ U.view = nv; tag(); clear(body); render(body, nv); DC.toast(nv.available ? DC.t('有新版本 {version}', {version:nv.latest.version}) : DC.t('已經是最新版本')); },
				function(e){ checking = false; DC.busy(checkB, false); DC.toast(DC.errText(e)); });
		});
		add(body, [
			sec('retry', DC.t('目前版本'), null, [
				DC.fieldDiv('Download Center ' + v.current, DC.t('架構 {arch}', {arch:v.arch || '?'}), v.available ? btn('up', DC.t('更新到 {version}', {version:v.latest.version}), function(){ offer(v.latest); }, 'pri') : h('span', {'class':'unit', text:v.releases.length ? DC.t('已經是最新版本') : ''})),
				DC.fieldDiv(DC.t('檢查更新'), v.check_error ? DC.t('{when}，無法連線：{error}', {when:when, error:v.check_error}) : when, checkB),
				toggle('upAuto', DC.t('自動檢查更新'), DC.t('每 12 小時向 GitHub 查一次，有新版本時在工具列提示；不會自動安裝'), v.auto_check, function(){ setPref(body, {auto_check:this.checked}); }),
				toggle('upPre', DC.t('包含測試版'), DC.t('測試版可能還有問題，建議只在測試用的 NAS 開啟'), v.prerelease, function(){ setPref(body, {prerelease:this.checked}); }),
				v.feed ? h('p', {'class':'note warn mono', text:DC.t('使用自訂的更新來源：{url}', {url:v.feed})}) : null,
				v.last ? h('p', {'class':'note' + (v.last.ok ? '' : ' warn'), text:v.last.ok ? DC.t('{time} 從 {from} 更新到 {version}。', {time:DC.ftime(v.last.at), from:v.last.from, version:v.last.to}) : DC.t('{time} 更新到 {version} 沒有完成，仍是 {from}。', {time:DC.ftime(v.last.at), from:v.last.from, version:v.last.to})}) : null,
				v.last && !v.last.ok && v.last.log ? h('pre', {'class':'relnotes mono', text:v.last.log}) : null
			]),
			sec('files', DC.t('所有版本'), DC.t('可以更新，也可以降回較舊的版本。每次更新前都會先備份資料庫。'), [releases(v)])
		]);
	}

	function releases(v){
		var list = h('div'), i;
		if(!v.releases.length){ list.appendChild(h('p', {'class':'note', text:v.checked_at ? DC.t('還沒有發布的版本。') : DC.t('按「立即檢查」取得版本資訊。')})); return list; }
		for(i = 0; i < v.releases.length; i++){
			(function(e){
				var pills = h('div', {'class':'pills'}, [e.relation === 'current' ? h('span', {text:DC.t('目前版本')}) : null, e.prerelease ? h('span', {'class':'warn', text:DC.t('測試版')}) : null]);
				var act = null;
				if(e.relation !== 'current'){
					if(!e.installable) act = h('small', {'class':'unit', text:DC.t('沒有適合這台 NAS 的套件')});
					else if(e.relation === 'newer') act = btn(null, DC.t('更新'), function(){ offer(e); }, 'pri');
					else act = btn(null, DC.t('降回這一版'), function(){ goBack(e); });
				}
				list.appendChild(h('div', {'class':'lrow relrow'}, [icon(e.relation === 'newer' ? 'up' : e.relation === 'older' ? 'dn' : 'done'), h('div', null, [
					h('b', {'class':'num', text:e.version}), pills,
					h('small', {'class':'num', text:[e.date, sizeText(e)].filter(function(x){ return !!x; }).join(DC.t('，'))}),
					e.notes ? h('details', {'class':'relmore'}, [h('summary', {text:DC.t('更新內容')}), h('pre', {'class':'relnotes', text:e.notes})]) : null
				]), h('div', {'class':'acts2'}, act)]));
			})(v.releases[i]);
		}
		return list;
	}

	DC.update = {boot:boot, settings:settings, offer:function(){ if(U.view && U.view.latest) offer(U.view.latest); }, load:load};
})();
