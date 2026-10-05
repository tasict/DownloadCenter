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
	function boot(){ if(allowed()) load().then(lastResult, function(){}); }

	/* ---------- toolbar tag ---------- */
	function tag(){
		var v = U.view, el = R.upd;
		if(!el) return;
		clear(el);
		el.hidden = !(v && v.available && v.latest);
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

	function start(version){
		DC.api.post('update/install', {version:version}).then(function(v){ U.view = v; progress(version); }, function(err){ DC.toast(DC.errText(err)); });
	}

	var FAIL = {
		download:DC.t('Could not download the update file.'),
		signature:DC.t('The update file\'s signature did not verify; the update was stopped.'),
		hash:DC.t('The update file does not match the release list; the update was stopped.'),
		backup:DC.t('Could not back up the database; the update was stopped.'),
		install:DC.t('Could not start the installer.')
	};

	/* Progress: the download and checks come from dcd; once the installer runs dcd goes away, so the page waits for the
	   package to answer again with the new version and then reloads. */
	function progress(target){
		var phase = h('b', {'class':'up-phase', text:DC.t('Preparing…')}), fill = h('i'), sub = h('p', {'class':'note num'});
		var closeW = DC.modal(DC.t('Update to {version}', {version:target}), 'up', [
			h('div', {'class':'up-prog'}, [phase, h('div', {'class':'upbar', 'aria-hidden':'true'}, fill), sub]),
			h('p', {'class':'note', text:DC.t('Keep this page open during the update.')})
		], function(){ return []; }, {persist:true});
		var box = document.querySelectorAll('.modal'), t0 = Date.now(), wentDown = false;
		box = box[box.length - 1];
		function done(buttons){ var a = box && box.querySelector('.acts'); if(a){ clear(a); add(a, buttons); } }
		function failed(text, detail){
			phase.textContent = text; phase.className = 'up-phase bad';
			sub.textContent = detail || '';
			done([btn(null, DC.t('Close'), function(){ closeW(); load(); }, 'pri')]);
		}
		function job(){
			DC.api.get('update/job', null, {quiet:true}).then(function(j){
				if(j.phase === 'download'){
					phase.textContent = DC.t('Downloading…');
					if(j.total){ fill.style.width = Math.min(100, j.done * 100 / j.total).toFixed(1) + '%'; sub.textContent = DC.fsize(j.done) + ' / ' + DC.fsize(j.total); }
				}else if(j.phase === 'verify'){ phase.textContent = DC.t('Checking the signature and files…'); fill.style.width = '100%'; sub.textContent = ''; }
				else if(j.phase === 'backup') phase.textContent = DC.t('Backing up the database…');
				else if(j.phase === 'install'){ installing(); return; }
				else if(j.phase === 'failed'){ failed(FAIL[j.error] || DC.t('The update failed.'), j.detail); return; }
				setTimeout(job, 800);
			}, function(){ installing(); });
		}
		/* dcd stops during the install; ask the package's health check until the new version answers */
		function installing(){
			phase.textContent = DC.t('Installing; the package is restarting…'); fill.style.width = '100%'; sub.textContent = '';
			health(function(ver){
				if(ver === null){ wentDown = true; return again(); }
				if(ver === target){
					phase.textContent = DC.t('Updated to {version}; reloading the page…', {version:target});
					setTimeout(function(){ location.reload(); }, 1500);
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
				v.last && !v.last.ok && v.last.log ? h('pre', {'class':'relnotes mono', text:v.last.log}) : null
			]),
			DC.S.me.analytics ? sec('gauge', DC.t('Usage statistics'), null, [
				toggle('anOn', DC.t('Help improve Download Center'), DC.t('Sends anonymous usage statistics to Google Analytics once a day: version, model, which features are used and how often. No file names, links or accounts.'), DC.S.me.analytics.enabled, function(){
					var el = this;
					DC.api.put('analytics', {enabled:el.checked}).then(function(r){ DC.S.me.analytics = r; }, function(e){ el.checked = !el.checked; DC.toast(DC.errText(e)); });
				})
			]) : null,
			sec('link', DC.t('Links'), DC.t('The website, source code and problem reports are on GitHub; links open in a new tab.'), [
				ext(DC.t('Website'), DC.t('Features, installation and downloads'), DC.lang === 'TCH' ? SITE + 'zh-TW/' : SITE),
				ext(DC.t('Source code'), DC.t('Public on GitHub under the MIT License'), REPO),
				ext(DC.t('Releases'), DC.t('Notes and packages for every version; you can also download them manually here'), REPO + '/releases'),
				ext(DC.t('Report a problem'), DC.t('Found a problem or have a suggestion? Open an issue on GitHub and include version {version} ({arch})', {version:v.current, arch:v.arch || '?'}), REPO + '/issues')
			]),
			sec('files', DC.t('What\'s new'), null, [releases(v)])
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

	DC.update = {boot:boot, settings:settings, offer:function(){ if(U.view && U.view.latest) offer(U.view.latest); }, load:load};
})();
