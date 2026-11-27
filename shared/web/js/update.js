/* Updates, for administrators signed in to the UI: a quiet toolbar tag when a newer release is out, the update window with
   its progress, and the About and updates settings page (the notes of the running and the latest release; only newer ones install,
   going back is deliberately not offered). dcd fetches the release index, checks the signature and runs the installer; the
   page only asks and shows. */
(function(){
	'use strict';
	var DC = window.DC, h = DC.h, add = DC.add, clear = DC.clear, icon = DC.icon, btn = DC.btn, R = DC.R;
	var U = {view:null};
	/* The project's pages, linked from the About and updates page; the site has an English and a Traditional Chinese edition */
	var SITE = 'https://tasict.github.io/DownloadCenter/', REPO = 'https://github.com/tasict/DownloadCenter';

	function allowed(){ return DC.isAdmin() && DC.S.me && DC.S.me.via === 'session'; }
	function load(){ return DC.api.get('update', null, {quiet:true}).then(function(v){ U.view = v; tag(); return v; }); }
	/* The update runs on the NAS whether or not a page watches it: a page opened meanwhile picks up its progress */
	function running(v){ var p = v && v.job && v.job.phase; return p === 'download' || p === 'verify' || p === 'backup' || p === 'install'; }
	function boot(){
		if(!allowed()) return;
		load().then(function(v){
			if(running(v)) progress(v.job.target);
			else stopped(v);
			lastResult(v);
		}, function(){});
	}

	/* ---------- toolbar tag ---------- */
	function tag(){
		var v = U.view, el = R.upd;
		if(!el) return;
		clear(el);
		el.hidden = !(v && v.available && v.latest) || running(v);
		if(el.hidden) return;
		el.title = DC.t('Version {version} is available', {version:v.latest.version});
		add(el, [icon('up'), h('span', {text:DC.t('New version {version}', {version:v.latest.version})})]);
	}

	/* The outcome of the last update, once per browser */
	function lastResult(v){
		var l = v && v.last, seen = +(DC.ls('dc-update-seen') || 0);
		if(!l || l.at <= seen) return;
		DC.ls('dc-update-seen', l.at);
		DC.toast(l.ok ? DC.t('Updated to {version}', {version:l.to}) : DC.t('The update to {version} did not complete; still {from}', {version:l.to, from:l.from}), null, 9000);
	}

	function sizeText(e){ return e.size ? DC.fsize(e.size) : ''; }

	/* ---------- the update window ---------- */
	function offer(e){
		var v = U.view || {}, isLatest = v.latest && v.latest.version === e.version;
		DC.modal(DC.t('Download Center {version}', {version:e.version}), 'up', [
			h('p', {'class':'lead num', text:[e.date, e.prerelease ? DC.t('Pre-release') : '', sizeText(e)].filter(function(x){ return !!x; }).join(DC.t('; '))}),
			e.notes ? h('pre', {'class':'relnotes', text:e.notes}) : h('p', {'class':'note', text:DC.t('This version has no release notes.')}),
			h('p', {'class':'note', text:DC.t('The package restarts during the update: downloads pause briefly and then continue. The database is backed up first.')})
		], function(close){
			return [
				isLatest ? h('button', {'class':'ib linkish skipv', type:'button', onclick:function(){
					close();
					DC.api.put('update/settings', {skip:e.version}).then(function(nv){ U.view = nv; tag(); DC.toast(DC.t('You won\'t be reminded of {version} again', {version:e.version})); }, function(err){ DC.toast(DC.errText(err)); });
				}}, DC.t('Skip this version')) : null,
				btn(null, DC.t('Later'), close),
				btn(null, DC.t('Update now'), function(){ close(); start(e.version); }, 'pri')
			];
		});
	}

	/* An update that stopped before installing, once per browser: the page may have been closed while it ran */
	function stopped(v){
		var j = v && v.job, seen = +(DC.ls('dc-update-failed-seen') || 0);
		if(!j || j.phase !== 'failed' || !j.at || j.at <= seen) return;
		DC.ls('dc-update-failed-seen', j.at);
		DC.toast(failText(j), null, 9000);
	}
	function failText(j){ return DC.sentences(DC.t('The update to {version} did not finish.', {version:j.target}), FAIL[j.error] || DC.t('The update failed.')); }

	function start(version){
		DC.api.post('update/install', {version:version}).then(function(v){ U.view = v; tag(); progress(version); }, function(err){ DC.toast(DC.errText(err)); });
	}

	var FAIL = {
		download:DC.t('Could not download the update file.'),
		signature:DC.t('The update file\'s signature did not verify; the update was stopped.'),
		hash:DC.t('The update file does not match the release list; the update was stopped.'),
		backup:DC.t('Could not back up the database; the update was stopped.'),
		install:DC.t('Could not start the installer.')
	};

	/* The steps of an update and the symbol each one moves in the middle of the ring */
	var STEPS = [['download', 'down', DC.t('Download')], ['verify', 'check', DC.t('Verify files')], ['backup', 'copy', DC.t('Back up')], ['install', 'retry', DC.t('Install and restart')]];
	var RING = 2 * Math.PI * 26, SVGNS = 'http://www.w3.org/2000/svg';
	function svgEl(tag, attrs){ var el = document.createElementNS(SVGNS, tag), k; for(k in attrs) el.setAttribute(k, attrs[k]); return el; }
	/* m:ss, or h:mm:ss past an hour */
	function clock(sec){
		var x = Math.max(0, Math.floor(sec)), hh = Math.floor(x / 3600), mm = Math.floor(x % 3600 / 60);
		return (hh ? hh + ':' + DC.pad(mm) : mm) + ':' + DC.pad(x % 60);
	}

	/* Progress: the download and checks come from dcd; once the installer runs dcd goes away, so the page waits for the
	   package to answer again with the new version and then reloads. The window shows where the update is: a ring around
	   the step's symbol (the downloaded share while downloading, a turning arc otherwise; green with a check when it worked,
	   red when it failed), the four steps, and the time since the update started, measured by the NAS and counted on here
	   while the package restarts. */
	function progress(target){
		var phase = h('b', {'class':'up-phase', role:'status', text:DC.t('Preparing…')}), sub = h('p', {'class':'note num'}), time = h('p', {'class':'up-time num'});
		var arc = svgEl('circle', {'class':'up-arc', cx:'30', cy:'30', r:'26', 'stroke-dasharray':RING.toFixed(2), 'stroke-dashoffset':RING.toFixed(2)});
		var ring = svgEl('svg', {'class':'up-ring', viewBox:'0 0 60 60', 'aria-hidden':'true'});
		var mid = h('span', {'class':'up-mid'}), badge = h('div', {'class':'up-badge spin'}, [ring, mid]);
		var list = h('ol', {'class':'up-steps'}), items = [], i;
		ring.appendChild(svgEl('circle', {'class':'up-track', cx:'30', cy:'30', r:'26'}));
		ring.appendChild(arc);
		for(i = 0; i < STEPS.length; i++){ items.push(h('li', null, [h('span', {'class':'up-dot', 'aria-hidden':'true'}), h('span', {text:STEPS[i][2]})])); list.appendChild(items[i]); }
		var closeW = DC.modal(DC.t('Update to {version}', {version:target}), 'up', [
			h('div', {'class':'up-prog'}, [badge, phase, sub, list, time]),
			h('p', {'class':'note', text:DC.t('You can close this page: the update carries on on the NAS, and the result shows the next time you open Download Center.')})
		], function(){ return []; }, {persist:true, onclose:function(){ clearInterval(tick); U.busy = false; }});
		var box = document.querySelectorAll('.modal'), t0 = Date.now(), wentDown = false, step = -1, base = 0, baseAt = Date.now(), counting = true;
		var tick = setInterval(showTime, 1000);
		U.busy = true;
		box = box[box.length - 1];
		function showTime(){ if(counting) time.textContent = DC.t('Elapsed {time}', {time:clock(base + (Date.now() - baseAt) / 1000)}); }
		/* The NAS says how long the update has run; locally it counts on from there */
		function elapsed(j){ if(j && typeof j.elapsed === 'number'){ base = j.elapsed; baseAt = Date.now(); } showTime(); }
		/* n: the step in progress (STEPS.length: all done); state: on (default) or bad */
		function setStep(n, state){
			var k;
			if(n !== step && n < STEPS.length){
				clear(mid).appendChild(icon(STEPS[n][1]));
				badge.className = 'up-badge spin ph-' + STEPS[n][0];
			}
			step = n;
			for(k = 0; k < items.length; k++) items[k].className = k < n ? 'done' : k === n ? (state || 'on') : '';
		}
		/* share: the downloaded part (0..1), or null for a turning arc */
		function share(x){
			badge.classList.toggle('spin', x === null);
			arc.setAttribute('stroke-dashoffset', (x === null ? RING * .72 : RING * (1 - x)).toFixed(2));
		}
		function done(buttons){ var a = box && box.querySelector('.acts'); if(a){ clear(a); add(a, buttons); } }
		function failed(text, detail){
			counting = false; clearInterval(tick);
			phase.textContent = text; phase.className = 'up-phase bad';
			sub.textContent = detail || '';
			setStep(Math.max(step, 0), 'bad');
			badge.className = 'up-badge bad'; share(1); badge.classList.remove('spin');
			clear(mid).appendChild(icon('error'));
			done([btn(null, DC.t('Close'), function(){ closeW(); load(); }, 'pri')]);
		}
		function job(){
			DC.api.get('update/job', null, {quiet:true}).then(function(j){
				elapsed(j);
				if(j.phase === 'download'){
					setStep(0);
					phase.textContent = DC.t('Downloading…');
					if(j.total){ share(Math.min(1, j.done / j.total)); sub.textContent = DC.fsize(j.done) + ' / ' + DC.fsize(j.total); }
					else share(null);
				}else if(j.phase === 'verify'){ setStep(1); share(null); phase.textContent = DC.t('Checking the signature and files…'); sub.textContent = ''; }
				else if(j.phase === 'backup'){ setStep(2); share(null); phase.textContent = DC.t('Backing up the database…'); sub.textContent = ''; }
				else if(j.phase === 'install' || !j.phase){ installing(); return; }
				else if(j.phase === 'failed'){ DC.ls('dc-update-failed-seen', j.at || 0); failed(FAIL[j.error] || DC.t('The update failed.'), j.detail); return; }
				setTimeout(job, 800);
			}, function(){
				/* No progress: dcd may still be there (the network hiccuped) or it is restarting for the install */
				health(function(ver){ if(ver === null) installing(); else setTimeout(job, 1500); });
			});
		}
		/* dcd stops during the install; ask the package's health check until the new version answers */
		function installing(){
			setStep(3); share(null);
			phase.textContent = DC.t('Installing; the package is restarting…'); sub.textContent = '';
			health(function(ver){
				if(ver === null){ wentDown = true; return again(); }
				if(ver === target){
					counting = false; clearInterval(tick);
					setStep(4);
					badge.className = 'up-badge ok'; share(1);
					clear(mid).appendChild(icon('done'));
					phase.textContent = DC.t('Updated to {version}; reloading the page…', {version:target});
					setTimeout(function(){ location.reload(); }, 1800);
					return;
				}
				/* The old version answers again after it was down: the installer did not complete */
				if(wentDown && Date.now() - t0 > 20000) return failed(DC.t('The installation did not complete; still {version}.', {version:ver}), DC.t('Details are in the package\'s data/logs/update.log.'));
				again();
			});
		}
		function again(){
			if(Date.now() - t0 > 8 * 60 * 1000) return failed(DC.t('This is taking too long. Reload the page later.'), DC.t('Details are in the package\'s data/logs/update.log.'));
			setTimeout(installing, 2000);
		}
		setStep(0); share(null); showTime();
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

	/* ---------- settings: About and updates ---------- */
	function settings(body){
		DC.loadingInto(body);
		DC.api.get('update').then(function(v){ U.view = v; tag(); clear(body); render(body, v); }, function(e){ DC.errorInto(body, e); });
	}

	function setPref(body, o){
		DC.api.put('update/settings', o).then(function(v){ U.view = v; tag(); clear(body); render(body, v); }, function(e){ DC.toast(DC.errText(e)); });
	}

	function render(body, v){
		var sec = DC.sec, toggle = DC.toggle, checking = false, checkB;
		var when = v.checked_at ? DC.t('Last checked: {time}', {time:DC.ftime(v.checked_at)}) : DC.t('No check for updates yet');
		checkB = btn('retry', DC.t('Check now'), function(){
			if(checking) return;
			checking = true; DC.busy(checkB, true, DC.t('Checking for updates…'));
			DC.api.post('update/check', {}).then(function(nv){ U.view = nv; tag(); clear(body); render(body, nv); DC.toast(nv.available ? DC.t('Version {version} is available', {version:nv.latest.version}) : DC.t('You\'re up to date')); },
				function(e){ checking = false; DC.busy(checkB, false); DC.toast(DC.errText(e)); });
		});
		add(body, [
			sec('retry', DC.t('Current version'), null, [
				DC.fieldDiv('Download Center ' + v.current, DC.t('Architecture {arch}', {arch:v.arch || '?'}), v.available ? btn('up', DC.t('Update to {version}', {version:v.latest.version}), function(){ offer(v.latest); }, 'pri') : h('span', {'class':'unit', text:v.releases.length ? DC.t('You\'re up to date') : ''})),
				DC.fieldDiv(DC.t('Check for updates'), v.check_error ? DC.t('{when}; could not connect: {error}', {when:when, error:v.check_error}) : when, checkB),
				toggle('upAuto', DC.t('Check for updates automatically'), DC.t('Asks GitHub every 12 hours and shows new versions in the toolbar; nothing is installed automatically'), v.auto_check, function(){ setPref(body, {auto_check:this.checked}); }),
				toggle('upPre', DC.t('Include pre-releases'), DC.t('Pre-releases may still have problems; turn this on only on a NAS you use for testing'), v.prerelease, function(){ setPref(body, {prerelease:this.checked}); }),
				v.feed ? h('p', {'class':'note warn mono', text:DC.t('Using a custom update source: {url}', {url:v.feed})}) : null,
				v.last ? h('p', {'class':'note' + (v.last.ok ? '' : ' warn'), text:v.last.ok ? DC.t('{time}: updated from {from} to {version}.', {time:DC.ftime(v.last.at), from:v.last.from, version:v.last.to}) : DC.t('{time}: the update to {version} did not complete; still {from}.', {time:DC.ftime(v.last.at), from:v.last.from, version:v.last.to})}) : null,
				v.last && !v.last.ok && v.last.log ? h('pre', {'class':'relnotes mono', text:v.last.log}) : null,
				v.job && v.job.phase === 'failed' ? h('p', {'class':'note warn', text:failText(v.job)}) : null,
				v.job && v.job.phase === 'failed' && v.job.detail ? h('pre', {'class':'relnotes mono', text:v.job.detail}) : null
			]),
			DC.S.me.analytics ? sec('gauge', DC.t('Usage statistics'), null, [
				toggle('anOn', DC.t('Help improve Download Center'), DC.t('Sends anonymous usage statistics to Google Analytics once a day: version, model, which features are used and how often. No file names, links or accounts.'), DC.S.me.analytics.enabled, function(){
					var el = this;
					DC.api.put('analytics', {enabled:el.checked}).then(function(r){ DC.S.me.analytics = r; }, function(e){ el.checked = !el.checked; DC.toast(DC.errText(e)); });
				})
			]) : null,
			problems(),
			sec('link', DC.t('Links'), DC.t('The website, source code and problem reports are on GitHub; links open in a new tab.'), [
				ext(DC.t('Website'), DC.t('Features, installation and downloads'), DC.lang === 'TCH' ? SITE + 'zh-TW/' : SITE),
				ext(DC.t('Source code'), DC.t('Public on GitHub under the MIT License'), REPO),
				ext(DC.t('Releases'), DC.t('Notes and packages for every version; you can also download them manually here'), REPO + '/releases'),
				ext(DC.t('Report a problem'), DC.t('Found a problem or have a suggestion? Open an issue on GitHub and include version {version} ({arch})', {version:v.current, arch:v.arch || '?'}), REPO + '/issues')
			]),
			sec('files', DC.t('What\'s new'), null, [releases(v)])
		]);
	}

	/* Problem reports: the logs of the background programs (the server leaves out what was downloaded and account names), and
	   the task behind a diagnostic code that such a file shows */
	function problems(){
		var input = h('input', {type:'text', id:'diagCode', maxlength:8, autocapitalize:'off', autocomplete:'off', spellcheck:'false', 'class':'mono', placeholder:'3f2a91c0'});
		function find(){
			var code = input.value.replace(/\s+/g, '').toLowerCase();
			if(!/^[0-9a-f]{8}$/.test(code)){ DC.toast(DC.t('A diagnostic code has 8 characters, 0–9 and a–f.')); return; }
			DC.track('diag_find');
			DC.api.get('diagnostics/code/' + code).then(function(r){
				if(r.removed_at) DC.toast(DC.t('This task was removed from the list on {time}.', {time:DC.ftime(r.removed_at)}));
				else location.hash = '#task/' + r.id;
			}, function(e){ DC.toast(e.status === 404 ? DC.t('No task has this diagnostic code.') : DC.errText(e)); });
		}
		input.onkeydown = function(ev){ if(ev.key === 'Enter'){ ev.preventDefault(); find(); } };
		return DC.sec('error', DC.t('Problem reports'), DC.t('The logs of Download Center’s background programs, mainly their errors, to attach to a problem report. The file leaves out what was downloaded and account names; tasks appear as diagnostic codes.'), [
			DC.fieldDiv(DC.t('Logs'), DC.t('The last 2 MB of each log, within the task history'),
				h('a', {'class':'ib btn', href:DC.api.url('diagnostics/logs'), download:'', onclick:function(){ DC.track('diag_logs'); }}, [icon('down'), h('span', {text:DC.t('Download logs')})])),
			DC.fieldDiv(DC.t('Find a task'), DC.t('The diagnostic code of a task, as the logs show it (also in the task’s details)'), [input, btn('search', DC.t('Find'), find)])
		]);
	}

	function ext(label, help, url){
		return DC.fieldDiv(label, help, h('a', {'class':'linkish', href:url, target:'_blank', rel:'noopener noreferrer'}, [icon('popout'), DC.t('Open')]));
	}

	/* The running release and the latest one: the release Update to offers, else the newest published one (a development
	   build can be newer than all of them). Every other release is on the releases page. */
	function shown(v){
		var out = [], latest = v.latest ? v.latest.version : v.releases[0].version, i, e;
		for(i = 0; i < v.releases.length; i++){
			e = v.releases[i];
			if(e.relation === 'current' || e.version === latest) out.push(e);
		}
		return out;
	}

	function releases(v){
		var list = h('div'), rel, i;
		if(!v.releases.length){ list.appendChild(h('p', {'class':'note', text:v.checked_at ? DC.t('No versions have been released yet.') : DC.t('Click “Check now” to get the version list.')})); return list; }
		rel = shown(v);
		for(i = 0; i < rel.length; i++){
			(function(e){
				var pills = h('div', {'class':'pills'}, [e.relation === 'current' ? h('span', {text:DC.t('Current version')}) : null, e.prerelease ? h('span', {'class':'warn', text:DC.t('Pre-release')}) : null]);
				var act = null;
				if(e.relation === 'newer') act = e.installable ? btn(null, DC.t('Update'), function(){ offer(e); }, 'pri') : h('small', {'class':'unit', text:DC.t('No package for this NAS')});
				list.appendChild(h('div', {'class':'lrow relrow'}, [icon(e.relation === 'newer' ? 'up' : e.relation === 'older' ? 'dn' : 'done'), h('div', null, [
					h('b', {'class':'num', text:e.version}), pills,
					h('small', {'class':'num', text:[e.date, sizeText(e)].filter(function(x){ return !!x; }).join(DC.t('; '))}),
					e.notes ? h('pre', {'class':'relnotes', text:e.notes}) : h('p', {'class':'note', text:DC.t('This version has no release notes.')})
				]), h('div', {'class':'acts2'}, act)]));
			})(rel[i]);
		}
		return list;
	}

	/* busy: the update window is open (it explains a restart itself); health: the version the package answers with, or null */
	DC.update = {boot:boot, settings:settings, offer:function(){ if(U.view && U.view.latest) offer(U.view.latest); }, load:load,
		busy:function(){ return !!U.busy; }, health:health};
})();
