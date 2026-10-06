/* Subtitles for the video preview: find the subtitle file that belongs to a video, decode it and rebuild it as clean WebVTT.
   Plain functions without DOM access; the preview tab in detail.js does the rest. Subtitle files come from the network,
   so only timings and text survive, and the text keeps nothing but bold, italic and underline. */
(function(){
	'use strict';
	var DC = window.DC = window.DC || {};
	/* Text encodings offered when a file is not Unicode (TextDecoder labels) */
	var ENCODINGS = ['utf-8', 'big5', 'gb18030', 'shift_jis', 'euc-kr', 'windows-1252'];
	var BY_LANG = {TCH:'big5', SCH:'gb18030', JPN:'shift_jis', KOR:'euc-kr'};
	var MAX_CUES = 20000;
	var TIME = /^\s*((?:\d+:)?\d{1,2}:\d{1,2}(?:[,.]\d{1,3})?)\s*-->\s*((?:\d+:)?\d{1,2}:\d{1,2}(?:[,.]\d{1,3})?)(?:\s|$)/;
	var ENT = {amp:'&', lt:'<', gt:'>', nbsp:' ', lrm:'‎', rlm:'‏'};

	/* Path without the official package's unfinished-file suffix and without the extension; the directory stays. */
	function stem(p){
		var i, s;
		p = String(p || '').replace(/\.dsdownload$/i, '');
		i = p.lastIndexOf('.'); s = p.lastIndexOf('/');
		return i > s ? p.slice(0, i) : p;
	}
	function ext(p){
		var s = String(p || '').replace(/\.dsdownload$/i, ''), i = s.lastIndexOf('.');
		return i > s.lastIndexOf('/') ? s.slice(i + 1).toLowerCase() : '';
	}

	/* The subtitle file for a playable video among the task's files: same folder, same name in any case, .srt or .vtt.
	   An exact-case name wins, then .srt before .vtt. */
	function match(files, f){
		var base, low, i, c, e, s, rank, best = null, top = -1;
		if(!f || f.type !== 'video' || !f.playable) return null;
		base = stem(f.path); low = base.toLowerCase();
		for(i = 0; i < (files || []).length; i++){
			c = files[i];
			if(c === f || c.index === f.index) continue;
			e = ext(c.path);
			if(e !== 'srt' && e !== 'vtt') continue;
			s = stem(c.path);
			if(s.toLowerCase() !== low) continue;
			rank = (s === base ? 2 : 0) + (e === 'srt' ? 1 : 0);
			if(rank > top){ best = c; top = rank; }
		}
		return best;
	}

	function dec(enc, bytes, fatal){
		try{ return new TextDecoder(enc, {fatal:!!fatal}).decode(bytes); }catch(e){ return null; }
	}
	/* Bytes to text. A BOM or valid UTF-8 is certain; anything else is a guess (guessed: true, the tab then offers the
	   encoding menu): the user's last choice, the UI language's legacy encoding, the other East Asian ones, Windows-1252. */
	function decode(buf, preferred, lang){
		var b, enc, t, tries, i;
		if(typeof TextDecoder === 'undefined' || !buf) return null;
		b = new Uint8Array(buf);
		if(b.length >= 3 && b[0] === 0xEF && b[1] === 0xBB && b[2] === 0xBF) enc = 'utf-8';
		else if(b.length >= 2 && b[0] === 0xFF && b[1] === 0xFE) enc = 'utf-16le';
		else if(b.length >= 2 && b[0] === 0xFE && b[1] === 0xFF) enc = 'utf-16be';
		if(enc){ t = dec(enc, b, false); return t === null ? null : {text:t, enc:enc, guessed:false}; }
		t = dec('utf-8', b, true);
		if(t !== null) return {text:t, enc:'utf-8', guessed:false};
		tries = [];
		if(ENCODINGS.indexOf(preferred) > 0) tries.push(preferred);
		tries.push(BY_LANG[lang] || 'windows-1252', 'big5', 'gb18030', 'shift_jis', 'euc-kr');
		for(i = 0; i < tries.length; i++){
			if(tries.indexOf(tries[i]) < i) continue;
			t = dec(tries[i], b, true);
			if(t !== null) return {text:t, enc:tries[i], guessed:true};
		}
		return decodeAs(buf, 'windows-1252');
	}
	/* Bytes to text in the encoding the user chose; bytes that do not fit become U+FFFD. */
	function decodeAs(buf, enc){
		var t;
		if(typeof TextDecoder === 'undefined' || !buf || ENCODINGS.indexOf(enc) < 0) return null;
		t = dec(enc, new Uint8Array(buf), false);
		return t === null ? null : {text:t, enc:enc, guessed:true};
	}

	function pad(n, w){ n = String(n); while(n.length < w) n = '0' + n; return n; }
	function ms(s){
		var f = /^(?:(\d+):)?(\d{1,2}):(\d{1,2})(?:[,.](\d{1,3}))?$/.exec(s);
		return ((+(f[1] || 0) * 60 + +f[2]) * 60 + +f[3]) * 1000 + +((f[4] || '0') + '00').slice(0, 3);
	}
	function stamp(t){
		return pad(Math.floor(t / 3600000), 2) + ':' + pad(Math.floor(t / 60000) % 60, 2) + ':' + pad(Math.floor(t / 1000) % 60, 2) + '.' + pad(t % 1000, 3);
	}
	/* One line of cue text: drop {\…} overrides and every tag but b, i and u, then escape the rest so the cue parser
	   sees no markup of its own (WebVTT entities are decoded first so they are not escaped twice). */
	function clean(line, vtt){
		line = line.replace(/[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F]/g, '').replace(/\{\\[^}]*\}/g, '');
		line = line.replace(/<\s*(\/?)\s*([biu])\s*>/gi, function(m, c, t){ return '\u0001' + c + t.toLowerCase() + '\u0002'; });
		line = line.replace(/<[^>]*>/g, '');
		if(vtt) line = line.replace(/&(amp|lt|gt|nbsp|lrm|rlm);/g, function(m, n){ return ENT[n]; });
		line = line.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
		return line.replace(/\u0001(\/?)([biu])\u0002/g, '<$1$2>').replace(/^\s+|\s+$/g, '');
	}
	/* SRT or WebVTT text to WebVTT holding only timings and cleaned text; null when no cue is left. Headers, cue numbers
	   and identifiers, NOTE, STYLE and REGION blocks, cue settings and SRT coordinates are dropped. */
	function toVTT(text){
		var lines = String(text || '').replace(/^﻿/, '').replace(/\r\n?/g, '\n').split('\n'), out = [], cur = null, vtt, i, m, line;
		vtt = /^WEBVTT(?:[ \t]|$)/.test(lines[0] || '');
		function close(){
			var body = [], k, l;
			if(!cur || cur.b <= cur.a || out.length >= MAX_CUES) return;
			for(k = 0; k < cur.text.length; k++){ l = clean(cur.text[k], vtt); if(l) body.push(l); }
			if(body.length) out.push(stamp(cur.a) + ' --> ' + stamp(cur.b) + '\n' + body.join('\n'));
		}
		for(i = 0; i < lines.length; i++){
			line = lines[i];
			m = TIME.exec(line);
			if(m){
				/* SRT without blank lines between cues: the number of this cue ended up in the previous one */
				if(cur && cur.text.length && /^\s*\d+\s*$/.test(cur.text[cur.text.length - 1])) cur.text.pop();
				close();
				cur = {a:ms(m[1]), b:ms(m[2]), text:[]};
			}else if(/^\s*$/.test(line)){ close(); cur = null; }
			else if(cur) cur.text.push(line);
		}
		close();
		return out.length ? 'WEBVTT\n\n' + out.join('\n\n') + '\n' : null;
	}

	DC.subs = {ENCODINGS:ENCODINGS, match:match, decode:decode, decodeAs:decodeAs, toVTT:toVTT};
})();
