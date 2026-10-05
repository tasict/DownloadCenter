/* Sign-in page. The browser signs in to QTS itself (same origin as /cgi-bin/authLogin.cgi) and sets NAS_SID like
   the QTS login page; the backend only ever sees that cookie, never a password, and QTS handles lockout.
   Branches follow the authLogin.cgi answers in QTS order. */
(function(){
	'use strict';
	var DC = window.DC, h = DC.h, add = DC.add, clear = DC.clear, icon = DC.icon, btn = DC.btn;
	var CGI = '/cgi-bin/authLogin.cgi';
	var QUESTIONS = {1:DC.t('What is your pet\'s name?'), 2:DC.t('What is your favorite sport?'), 3:DC.t('What is your favorite color?')};

	function b64(s){ return btoa(unescape(encodeURIComponent(s))); }
	function form(o){
		var out = [], k;
		for(k in o) if(o.hasOwnProperty(k) && o[k] !== undefined && o[k] !== null) out.push(encodeURIComponent(k) + '=' + encodeURIComponent(o[k]));
		return out.join('&');
	}
	/* Calls authLogin.cgi and returns the answer flattened to {element: text}. */
	function cgi(params, method, url){
		return new Promise(function(resolve, reject){
			var x = new XMLHttpRequest(), body = form(params);
			method = method || 'POST';
			x.open(method, (url || CGI) + (method === 'GET' ? '?' + body : ''), true);
			if(method === 'POST') x.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded; charset=UTF-8');
			x.timeout = 30000;
			x.onreadystatechange = function(){
				if(x.readyState !== 4) return;
				if(x.status !== 200){ reject({status:x.status}); return; }
				resolve(parse(x.responseText));
			};
			x.ontimeout = function(){ reject({status:0}); };
			x.send(method === 'POST' ? body : null);
		});
	}
	function parse(text){
		var out = {}, doc, all, i, el;
		try{ doc = new DOMParser().parseFromString(text, 'text/xml'); }catch(e){ return out; }
		all = doc.getElementsByTagName('*');
		for(i = 0; i < all.length; i++){
			el = all[i];
			if(el.children && el.children.length) continue;
			if(out[el.nodeName] === undefined || el.textContent) out[el.nodeName] = (el.textContent || '').replace(/^\s+|\s+$/g, '');
		}
		return out;
	}
	function setCookie(k, v){ document.cookie = k + '=' + encodeURIComponent(v) + '; path=/; SameSite=Lax'; }
	function delCookie(k){ document.cookie = k + '=; path=/; expires=Thu, 01 Jan 1970 00:00:00 GMT'; }
	function getCookie(k){
		var parts = document.cookie.split(/;\s*/), i, kv;
		for(i = 0; i < parts.length; i++){ kv = parts[i].split('='); if(kv[0] === k) return decodeURIComponent(kv.slice(1).join('=')); }
		return '';
	}
	DC.getCookie = getCookie;

	/* "Remember user name" keeps only the user name in this browser. */
	function loadUser(){ return DC.ls('dc-user') || ''; }
	function saveUser(u){ DC.ls('dc-user', u || null); }
	function cidKey(u){ return 'dc-cid-' + u; }
	function vtKey(u){ return 'dc-vt-' + u; }

	var app = document.getElementById('app');

	function show(msg){
		var R = DC.R;
		if(R.closeModal) R.closeModal();
		clear(app);
		app.className = 'app';
		if(DC.embedded){
			/* Inside the QTS desktop the QTS sign-in applies; never show our own page there. */
			app.appendChild(h('div', {'class':'login'}, h('div', {'class':'lbox'}, [
				h('div', {'class':'brand'}, [h('img', {'class':'mark', src:'img/logo.png', alt:''}), 'Download Center']),
				h('h1', {text:msg || DC.t('Your QTS sign-in has expired.')}),
				h('p', {'class':'lmsg', text:DC.t('Refresh, or sign in to QTS again and reopen.')}),
				btn('retry', DC.t('Refresh'), function(){ location.reload(); }, 'pri')
			])));
			return;
		}
		var user = h('input', {type:'text', id:'lUser', autocomplete:'username', value:loadUser(), autocapitalize:'off', spellcheck:'false', enterkeyhint:'next'});
		var pass = h('input', {type:'password', id:'lPass', autocomplete:'current-password', enterkeyhint:'go'});
		var rem = h('input', {type:'checkbox', id:'lRem', checked:!!loadUser()});
		var err = h('div', {'class':'lerr', role:'alert'});
		var box = h('div', {'class':'lbox'}), timer = null, trust = false;
		/* Kept only in this closure between the two 2-step requests; cleared on success, timeout or leaving. */
		var cred = null, info = {};
		function brand(){ return h('div', {'class':'brand'}, [h('img', {'class':'mark', src:'img/logo.png', alt:''}), 'Download Center']); }
		function stopTimer(){ clearInterval(timer); timer = null; }
		function forget(){ cred = null; pass.value = ''; }
		window.addEventListener('pagehide', forget);
		function countdown(el, secs){
			stopTimer();
			function upd(){
				el.textContent = DC.t('{time} left', {time:Math.floor(secs / 60) + ':' + DC.pad(secs % 60)});
				if(secs-- <= 0){ stopTimer(); forget(); step1(DC.t('The verification time has passed. Sign in again.')); }
			}
			upd(); timer = setInterval(function(){ if(!box.parentNode){ stopTimer(); return; } upd(); }, 1000);
		}
		function backLink(){ return h('button', {'class':'ib quiet lback', type:'button', onclick:function(){ forget(); step1(); }}, [icon('back'), DC.t('Use another account')]); }
		function trustBox(){
			return h('label', {'class':'lrem', 'for':'lTrust'}, [h('input', {type:'checkbox', id:'lTrust', checked:trust, onchange:function(){ trust = this.checked; }}), DC.t('Don\'t verify again on this device')]);
		}
		function screen(title, kids){ stopTimer(); err.textContent = ''; clear(box); add(box, [brand(), h('h1', {text:title})].concat(kids)); }
		function generic(){ err.textContent = DC.t('Invalid credentials, or the account is no longer valid.'); }

		function baseParams(extra){
			var u = cred.user, p = {user:u, pwd:b64(cred.pass), serviceKey:'1', client_app:'DownloadCenter', client_agent:navigator.userAgent, r:Math.random()}, cid = DC.ls(cidKey(u)), vt = DC.ls(vtKey(u)), k;
			if(cid) p.client_id = cid; else p.gen_client_id = '1';
			if(vt) p.vtoken = vt;
			if(extra) for(k in extra) p[k] = extra[k];
			return p;
		}
		/* Evaluates an answer in QTS order. onTwoStep handles a repeated 2-step answer (wrong code). */
		function evaluate(a, onTwoStep){
			if(a.client_id && cred) DC.ls(cidKey(cred.user), a.client_id);
			if(a.authPassed === '1' && a.authSid){
				if(a.vtoken && cred) DC.ls(vtKey(cred.user), a.vtoken);
				var name = a.username || (cred && cred.user) || '';
				setCookie('NAS_SID', a.authSid);
				setCookie('NAS_USER', name);
				setCookie('NAS_PW_STATUS', a.pw_status || '0');
				setCookie('home', '1');
				stopTimer(); forget();
				if(DC.boot) DC.boot(); else location.reload();
				return;
			}
			if(a.user_pw_expiry === '1' || a.pw_status === '1'){ forget(); stepExpired(); return; }
			if(a.need_2_step_verification === '1' || a.need_2sv === '1'){
				var k;
				for(k in a) info[k] = a[k];
				if(onTwoStep) onTwoStep(a); else stepCode();
				return;
			}
			if(a.force_2sv === '1'){ forget(); stepEnrol(); return; }
			generic();
		}
		function netErr(){ err.textContent = DC.t('Cannot connect to QTS. Try again later.'); }

		function step1(msg){
			stopTimer();
			var go = h('button', {'class':'ib btn pri', type:'submit'}, [icon('lock'), h('span', {text:DC.t('Sign in')})]);
			var eye = h('button', {'class':'ib sq leye', type:'button', 'aria-label':DC.t('Show password'), 'aria-pressed':'false', onclick:function(){
				var showPw = pass.type === 'password'; pass.type = showPw ? 'text' : 'password';
				eye.setAttribute('aria-pressed', showPw ? 'true' : 'false'); eye.setAttribute('aria-label', showPw ? DC.t('Hide password') : DC.t('Show password'));
				clear(eye).appendChild(icon(showPw ? 'eyeoff' : 'eye')); pass.focus();
			}}, icon('eye'));
			var warn = h('div', {'class':'lwarn', hidden:true});
			screen(DC.t('Sign in with your NAS account'), [
				warn,
				h('form', {onsubmit:function(e){
					e.preventDefault();
					var u = user.value.replace(/^\s+|\s+$/g, '');
					if(!u){ err.textContent = DC.t('Enter the user name.'); user.focus(); return; }
					if(!pass.value){ err.textContent = DC.t('Enter the password.'); pass.focus(); return; }
					saveUser(rem.checked ? u : '');
					cred = {user:u, pass:pass.value};
					info = {};
					DC.busy(go, true, DC.t('Signing in…')); err.textContent = '';
					cgi(baseParams()).then(function(a){ DC.busy(go, false); evaluate(a); if(a.authPassed !== '1') pass.select(); }, function(){ DC.busy(go, false); netErr(); });
				}}, [
					h('label', {'class':'lfield', 'for':'lUser'}, [h('span', {text:DC.t('Account')}), user]),
					h('label', {'class':'lfield', 'for':'lPass'}, [h('span', {text:DC.t('Password')}), h('span', {'class':'lpw'}, [pass, eye])]),
					h('label', {'class':'lrem', 'for':'lRem'}, [rem, DC.t('Remember user name')]),
					go, err
				]),
				h('div', {'class':'lnote'}, [h('div', {text:DC.t('Uses the same account as QTS. Opening from the QTS desktop signs you in directly. On a phone, you can add this page to the home screen.')}),
					h('div', {'class':'lutil'}, [DC.themeSeg ? DC.themeSeg() : null, DC.phoneButton ? DC.phoneButton(false) : null])])
			]);
			if(msg) err.textContent = msg;
			httpWarning(warn);
			(user.value ? pass : user).focus();
		}
		/* TOTP from an authenticator app; the same field also takes the code from the backup e-mail. */
		function stepCode(mailed){
			var len = +(info.security_code_length || 6) || 6;
			var code = h('input', {type:'text', id:'lCode', inputmode:'numeric', autocomplete:'one-time-code', maxlength:String(len), pattern:'[0-9]*', 'class':'code', enterkeyhint:'go',
				oninput:function(){ this.value = this.value.replace(/\D/g, '').slice(0, len); if(this.value.length === len && !go.disabled) submit(); }});
			var left = h('span', {'class':'num'}), go = h('button', {'class':'ib btn pri', type:'submit'}, [icon('lock'), h('span', {text:DC.t('Verify and sign in')})]);
			function submit(){
				if(!/^\d+$/.test(code.value) || code.value.length < 4){ err.textContent = DC.t('Enter the verification code.'); code.focus(); return; }
				DC.busy(go, true, DC.t('Verifying…'));
				cgi(baseParams({security_code:code.value, dont_verify_2sv_again:trust ? '1' : '0'})).then(function(a){
					DC.busy(go, false);
					evaluate(a, function(ans){
						err.textContent = ans.timestring ? DC.t('Incorrect verification code. Try again. The NAS time is now {time}; your authenticator\'s time must match it.', {time:ans.timestring.replace(/^\d+\/\d+\/\d+\s+/, '').replace(/:\d+$/, '')}) : DC.t('Incorrect verification code. Try again.');
						code.select();
					});
				}, function(){ DC.busy(go, false); netErr(); });
			}
			var f = h('form', {onsubmit:function(e){ e.preventDefault(); submit(); }}, [h('label', {'class':'lfield', 'for':'lCode'}, [h('span', {text:mailed ? DC.t('Code from the email') : DC.t('Verification code')}), code]), trustBox(), go, err]);
			screen(mailed ? DC.t('The verification code has been sent to this account\'s backup email.') : DC.t('This account has 2-step verification enabled. Enter the {n}-digit code from your authenticator.', {n:len}), [
				f,
				h('div', {'class':'lalt'}, [left, h('button', {'class':'ib linkish', type:'button', onclick:stepOther}, DC.t('Verify another way'))]),
				h('div', {'class':'lnote'}, backLink())
			]);
			countdown(left, mailed ? 300 : 180);
			code.focus();
		}
		/* Only the methods the account has (security_code_en, lost_phone). */
		function stepOther(){
			var opts = h('div', {'class':'lopts'}), lp = info.lost_phone, tooMany = lp === '-1' || (+info.emergency_try_limit > 0 && +info.emergency_try_count >= +info.emergency_try_limit);
			if(info.security_code_en !== '0') opts.appendChild(h('button', {'class':'ib lopt', type:'button', onclick:function(){ stepCode(); }}, [icon('lock'), h('span', null, [h('b', {text:DC.t('6-digit code from the authenticator')}), h('small', {text:DC.t('Google Authenticator, QNAP Authenticator, etc.')})]), icon('chev', 'chev')]));
			if(lp === '1') opts.appendChild(h('button', {'class':'ib lopt', type:'button', disabled:tooMany, onclick:function(e){
				var b = e.currentTarget; b.disabled = true;
				cgi(baseParams({send_mail:'1', q_lang:DC.lang || 'TCH'})).then(function(a){
					b.disabled = false;
					if(a.send_result === '1') stepCode(true);
					else if(a.send_result === '-1') err.textContent = DC.t('The NAS has no mail server configured, so the verification code cannot be sent.');
					else err.textContent = DC.t('Failed to send the verification code. Try again later.');
				}, function(){ b.disabled = false; netErr(); });
			}}, [icon('bell'), h('span', null, [h('b', {text:DC.t('Send code to backup email')}), h('small', {text:DC.t('Sent to the email address entered when setting up 2-step verification')})]), icon('chev', 'chev')]));
			if(lp === '2') opts.appendChild(h('button', {'class':'ib lopt', type:'button', disabled:tooMany, onclick:stepQuestion}, [icon('key'), h('span', null, [h('b', {text:DC.t('Answer security question')}), h('small', {text:DC.t('The question chosen when setting up 2-step verification')})]), icon('chev', 'chev')]));
			screen(DC.t('Choose another verification method'), [
				opts,
				tooMany ? h('p', {'class':'lmsg', text:DC.t('Too many attempts with the alternative verification method. Contact your administrator for help.')}) : null,
				err,
				h('div', {'class':'lnote'}, backLink())
			]);
		}
		function stepQuestion(){
			var ans = h('input', {type:'text', id:'lAns', autocomplete:'off', enterkeyhint:'go'}), left = h('span', {'class':'num'}), q = h('p', {'class':'lq', text:DC.t('Loading question…')});
			var go = h('button', {'class':'ib btn pri', type:'submit'}, [icon('lock'), h('span', {text:DC.t('Verify and sign in')})]);
			screen(DC.t('Answer the security question you set up.'), [
				h('form', {onsubmit:function(e){
					e.preventDefault();
					if(!ans.value){ err.textContent = DC.t('Enter the answer.'); ans.focus(); return; }
					DC.busy(go, true, DC.t('Verifying…'));
					cgi(baseParams({security_answer:ans.value, dont_verify_2sv_again:trust ? '1' : '0'})).then(function(a){
						DC.busy(go, false);
						if(a.authPassed === '1' || a.user_pw_expiry === '1' || a.pw_status === '1' || a.force_2sv === '1'){ evaluate(a); return; }
						if(a.lost_phone === '-1' || (+a.emergency_try_limit > 0 && +a.emergency_try_count >= +a.emergency_try_limit)){
							err.textContent = DC.t('Too many wrong answers to the alternative verification question. Contact your administrator for help.'); go.disabled = true; ans.disabled = true; return;
						}
						err.textContent = DC.t('Incorrect answer.'); ans.select();
					}, function(){ DC.busy(go, false); netErr(); });
				}}, [q, h('label', {'class':'lfield', 'for':'lAns'}, [h('span', {text:DC.t('Answer')}), ans]), trustBox(), go, err]),
				h('div', {'class':'lalt'}, [left, h('button', {'class':'ib linkish', type:'button', onclick:stepOther}, DC.t('Verify another way'))]),
				h('div', {'class':'lnote'}, backLink())
			]);
			countdown(left, 300);
			cgi(baseParams({get_question:'1'})).then(function(a){
				var n = a.security_question_no;
				/* A custom question is the user's own text: shown with textContent only. */
				q.textContent = n === '4' ? (a.security_question_text || DC.t('Custom question')) : (QUESTIONS[n] || DC.t('Security question'));
			}, function(){ q.textContent = DC.t('Cannot load the question.'); });
			ans.focus();
		}
		/* QTS answers these without a session; the fix happens in QTS, so the page says where and links there. */
		function stepExpired(){
			screen(DC.t('The password of this account has expired. Change it before signing in.'), [
				h('p', {'class':'lmsg', text:DC.t('Sign in to QTS and change your password as instructed, then come back here and sign in with the new password.')}),
				btn('popout', DC.t('Change password in QTS'), function(){ window.open('/cgi-bin/', '_blank', 'noopener'); }, 'pri'),
				h('div', {'class':'lnote'}, backLink())
			]);
		}
		function stepEnrol(){
			screen(DC.t('The administrator requires this account to use 2-step verification, but it has not been set up yet.'), [
				h('p', {'class':'lmsg', text:DC.t('Set up 2-step verification in the QTS desktop under “Options › Security” first, then come back here to sign in.')}),
				btn('popout', DC.t('Open QTS'), function(){ window.open('/cgi-bin/', '_blank', 'noopener'); }, 'pri'),
				h('div', {'class':'lnote'}, backLink())
			]);
		}
		app.appendChild(h('div', {'class':'login'}, box));
		step1(msg);
	}

	/* Plain http (not localhost): warn and offer the https address QTS publishes. */
	function httpWarning(el){
		if(location.protocol !== 'http:' || /^(localhost|127\.|\[::1\])/.test(location.hostname)) return;
		cgi({}, 'GET').then(function(a){
			var port = a.stunnelPort || '443';
			if(a.stunnelEnabled === '0') return;
			clear(el).hidden = false;
			add(el, [h('span', {text:DC.t('This connection is not encrypted. Your password will be sent in plain text.')}),
				h('button', {'class':'ib linkish', type:'button', onclick:function(){ location.href = 'https://' + location.hostname + (port === '443' ? '' : ':' + port) + location.pathname; }}, DC.t('Use encrypted connection'))]);
		}, function(){
			clear(el).hidden = false;
			el.appendChild(h('span', {text:DC.t('This connection is not encrypted. Your password will be sent in plain text.')}));
		});
	}

	/* Logout is QTS logout too: the session is shared with QTS in this browser. */
	function logout(){
		var sid = getCookie('NAS_SID');
		function done(){
			delCookie('NAS_SID'); delCookie('NAS_USER'); delCookie('NAS_PW_STATUS'); delCookie('home'); delCookie('QDS_SID');
			/* The next account signing in here starts on the task list, not on the page this one left open */
			try{ history.replaceState(null, '', location.pathname + location.search); }catch(e){}
			location.reload();
		}
		DC.api.post('logout', {}, {quiet:true}).then(null, function(){}).then(function(){
			if(!sid){ done(); return; }
			cgi({logout:'1', sid:sid}, 'GET', '/cgi-bin/authLogout.cgi').then(done, done);
		});
	}

	/* Keep the QTS session alive in a full browser tab, the way the QTS desktop does (it checks its sid with authLogin.cgi every 15 s).
	   Inside the QTS desktop the desktop already does this, so nothing runs there. A failed check means the session is gone: show the
	   login page right away instead of letting the next action fail half way. Browsers throttle timers in hidden tabs to about once a
	   minute, so the interval is 60 s plus an immediate check whenever the tab becomes visible again. */
	var kaTimer = null, kaBusy = false;
	function keepAliveCheck(){
		if(kaBusy) return;
		var sid = getCookie('NAS_SID');
		if(!sid){ stopKeepAlive(); if(DC.onSignedOut) DC.onSignedOut(); return; }
		kaBusy = true;
		cgi({sid:sid, r:String(Math.random())}, 'GET').then(function(a){
			kaBusy = false;
			if(a.authPassed !== '1'){ stopKeepAlive(); if(DC.onSignedOut) DC.onSignedOut(); }
		}, function(){ kaBusy = false; /* network hiccup or NAS busy: try again next round */ });
	}
	function onVisible(){ if(!document.hidden && kaTimer) keepAliveCheck(); }
	function startKeepAlive(){
		if(DC.embedded || kaTimer) return;
		kaTimer = setInterval(keepAliveCheck, 60000);
		document.addEventListener('visibilitychange', onVisible);
	}
	function stopKeepAlive(){
		if(kaTimer){ clearInterval(kaTimer); kaTimer = null; }
		document.removeEventListener('visibilitychange', onVisible);
	}

	DC.login = {show:show, logout:logout, parse:parse, keepAlive:startKeepAlive, stopKeepAlive:stopKeepAlive};
})();
