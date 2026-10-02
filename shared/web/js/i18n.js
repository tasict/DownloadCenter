/* UI language: the language chosen in QTS. QTS keeps it in the nas_lang cookie (set by its login page and desktop);
   without one QTS picks by the browser language, and so do we, with the same table. The UI's source strings are
   Traditional Chinese; other languages load a dictionary keyed by those strings (js/lang/<code>.js), and languages
   without one fall back to English. Loaded in <head>, before every other script. */
(function(){
	'use strict';
	var DC = window.DC = window.DC || {};
	var SUPPORTED = {TCH:1, SCH:1, ENG:1, JPN:1, KOR:1, GER:1, FRE:1, SPA:1, ITA:1, POR:1, RUS:1, DUT:1, THA:1};
	var ALIAS = {ESM:'SPA'};
	var LOCALE = {TCH:'zh-Hant-TW', SCH:'zh-Hans-CN', ENG:'en', JPN:'ja', KOR:'ko', GER:'de', FRE:'fr', SPA:'es', ITA:'it', POR:'pt', RUS:'ru', DUT:'nl', THA:'th'};
	function cookie(k){
		var parts = document.cookie.split(/;\s*/), i, kv;
		for(i = 0; i < parts.length; i++){ kv = parts[i].split('='); if(kv[0] === k) return decodeURIComponent(kv.slice(1).join('=')); }
		return '';
	}
	/* QTS's own browser-language table (qos-core-login.js) */
	function fromBrowser(){
		var l = (navigator.language || navigator.browserLanguage || 'en').toLowerCase();
		var table = [[/^zh-(tw|hk|mo|hant)/, 'TCH'], [/^zh/, 'SCH'], [/^cs/, 'CZE'], [/^da/, 'DAN'], [/^de/, 'GER'], [/^es-mx/, 'ESM'], [/^es/, 'SPA'],
			[/^fr/, 'FRE'], [/^it/, 'ITA'], [/^ja/, 'JPN'], [/^ko/, 'KOR'], [/^(nb|no|nn)/, 'NOR'], [/^pl/, 'POL'], [/^ru/, 'RUS'], [/^fi/, 'FIN'],
			[/^sv/, 'SWE'], [/^nl/, 'DUT'], [/^tr/, 'TUR'], [/^th/, 'THA'], [/^hu/, 'HUN'], [/^pt/, 'POR'], [/^el/, 'GRK'], [/^ro/, 'ROM']], i;
		for(i = 0; i < table.length; i++) if(table[i][0].test(l)) return table[i][1];
		return 'ENG';
	}
	var q = cookie('nas_lang'), code = q && q !== 'auto' ? q.toUpperCase() : fromBrowser();
	code = ALIAS[code] || code;
	if(!SUPPORTED[code]) code = 'ENG';
	DC.lang = code;
	DC.locale = LOCALE[code] || 'en';
	document.documentElement.setAttribute('lang', DC.locale);
	window.DC_DICT = null;
	/* The dictionary has to be in place before the UI scripts run: a parser-inserted, same-origin script (allowed by the CSP) */
	if(code !== 'TCH'){
		if(document.readyState === 'loading') document.write('<script src="js/lang/' + code + '.js"><\/script>');
		else{ var sc = document.createElement('script'); sc.src = 'js/lang/' + code + '.js'; document.head.appendChild(sc); }
	}

	/* DC.t('已加入 {n} 個下載', {n:3}): translate a source string, then fill {name} placeholders */
	DC.t = function(s, params){
		var d = window.DC_DICT, r = (d && d[s]) || s;
		if(params) r = r.replace(/\{(\w+)\}/g, function(m, k){ return params.hasOwnProperty(k) ? String(params[k]) : m; });
		return r;
	};
})();
