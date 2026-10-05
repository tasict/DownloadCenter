/* REST client for api/v1 (QTS session cookie; X-Requested-With on every state-changing request). */
(function(){
	'use strict';
	var DC = window.DC;
	var BASE = 'api/v1/';

	function qs(q){
		var out = [], k;
		if(!q) return '';
		for(k in q) if(q.hasOwnProperty(k) && q[k] !== undefined && q[k] !== null && q[k] !== '') out.push(encodeURIComponent(k) + '=' + encodeURIComponent(q[k]));
		return out.length ? '?' + out.join('&') : '';
	}

	/* Errors carry {status, code, message, body}; messages from the server are already in the UI language (X-DC-Lang). */
	function fail(status, body){
		var e = {status:status, code:'failed', message:DC.t('An error occurred. Try again later.'), body:body || {}};
		if(body && body.error){ e.code = body.error.code || e.code; e.message = body.error.message || e.message; }
		else if(status === 0){ e.code = 'network'; e.message = DC.t('Cannot connect to the NAS.'); }
		else if(status === 404){ e.code = 'not_found'; e.message = DC.t('Not found.'); }
		else if(status === 502 || status === 503){ e.code = 'unavailable'; e.message = DC.t('The Download Center service is not responding. Try again later.'); }
		return e;
	}

	function request(method, path, body, opts){
		opts = opts || {};
		return new Promise(function(resolve, reject){
			var x = new XMLHttpRequest();
			x.open(method, BASE + path, true);
			x.setRequestHeader('Accept', 'application/json');
			if(method !== 'GET') x.setRequestHeader('X-Requested-With', 'XMLHttpRequest');
			x.setRequestHeader('X-DC-Lang', DC.lang || 'ENG');
			x.timeout = opts.timeout || 120000;
			x.onreadystatechange = function(){
				if(x.readyState !== 4) return;
				var data = null;
				try{ data = x.responseText ? JSON.parse(x.responseText) : {}; }catch(e){ data = null; }
				if(x.status >= 200 && x.status < 300 && data !== null){ resolve(data); return; }
				var err = fail(x.status, data);
				if(!opts.quiet){
					if(err.code === 'not_signed_in' && DC.onSignedOut) DC.onSignedOut(err);
					else if(err.code === 'not_on_list' && DC.onNotOnList) DC.onNotOnList(err);
				}
				reject(err);
			};
			x.ontimeout = function(){ reject({status:0, code:'timeout', message:DC.t('The NAS did not respond in time.'), body:{}}); };
			if(body instanceof FormData) x.send(body);
			else if(body !== undefined && body !== null){ x.setRequestHeader('Content-Type', 'application/json'); x.send(JSON.stringify(body)); }
			else x.send();
		});
	}

	DC.api = {
		base:BASE,
		qs:qs,
		get:function(path, q, opts){ return request('GET', path + qs(q), null, opts); },
		post:function(path, body, opts){ return request('POST', path, body === undefined ? {} : body, opts); },
		put:function(path, body, opts){ return request('PUT', path, body, opts); },
		patch:function(path, body, opts){ return request('PATCH', path, body, opts); },
		del:function(path, q, opts){ return request('DELETE', path + qs(q), null, opts); },
		upload:function(path, form, opts){ return request('POST', path, form, opts); },
		url:function(path, q){ return BASE + path + qs(q); },
		/* Text body (preview of text files). */
		text:function(path, limit){
			return new Promise(function(resolve, reject){
				var x = new XMLHttpRequest();
				x.open('GET', BASE + path, true);
				x.setRequestHeader('X-DC-Lang', DC.lang || 'ENG');
				if(limit) x.setRequestHeader('Range', 'bytes=0-' + (limit - 1));
				x.onreadystatechange = function(){
					if(x.readyState !== 4) return;
					if(x.status >= 200 && x.status < 300) resolve(x.responseText);
					else{ var d = null; try{ d = JSON.parse(x.responseText); }catch(e){} reject(fail(x.status, d)); }
				};
				x.send();
			});
		}
	};

	/* Error text for a toast. */
	DC.errText = function(e){ return (e && e.message) || DC.t('An error occurred. Try again later.'); };
})();
