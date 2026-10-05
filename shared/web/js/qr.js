/* Open on phone: a QR code (ISO/IEC 18004) of the page's address, drawn here so nothing leaves the NAS. Byte mode (UTF-8),
   error correction level M, the smallest version that fits, the mask with the lowest penalty. It carries the address only,
   never a session: the phone signs in on its own. */
(function(){
	'use strict';
	var DC = window.DC, h = DC.h, btn = DC.btn, ibtn = DC.ibtn;

	/* Level M: error correction codewords per block and number of blocks, versions 1 to 40 (index 0 unused) */
	var ECC = [0, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26, 30, 22, 22, 24, 24, 28, 28, 26, 26, 26, 26, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28];
	var BLOCKS = [0, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16, 17, 17, 18, 20, 21, 23, 25, 26, 28, 29, 31, 33, 35, 37, 38, 40, 43, 45, 47, 49];

	/* GF(256) with the polynomial 0x11D */
	var EXP = [], LOG = [], i, v = 1;
	for(i = 0; i < 255; i++){ EXP[i] = v; LOG[v] = i; v <<= 1; if(v & 256) v ^= 0x11d; }
	function mul(a, b){ return a && b ? EXP[(LOG[a] + LOG[b]) % 255] : 0; }
	/* Reed-Solomon generator of degree n, highest power first */
	function generator(n){
		var g = [1], next, j, k;
		for(j = 0; j < n; j++){
			next = [];
			for(k = 0; k <= g.length; k++) next[k] = (k < g.length ? g[k] : 0) ^ (k > 0 ? mul(g[k - 1], EXP[j]) : 0);
			g = next;
		}
		return g;
	}
	function remainder(data, gen){
		var n = gen.length - 1, r = [], f, j, k;
		for(j = 0; j < n; j++) r.push(0);
		for(j = 0; j < data.length; j++){
			f = data[j] ^ r.shift();
			r.push(0);
			for(k = 0; k < n; k++) r[k] ^= mul(gen[k + 1], f);
		}
		return r;
	}
	/* Modules left for data and error correction once the patterns are drawn */
	function rawModules(ver){
		var r = (16 * ver + 128) * ver + 64, n;
		if(ver >= 2){
			n = Math.floor(ver / 7) + 2;
			r -= (25 * n - 10) * n - 55;
			if(ver >= 7) r -= 36;
		}
		return r;
	}
	function alignments(ver){
		if(ver === 1) return [];
		var n = Math.floor(ver / 7) + 2, step = ver === 32 ? 26 : Math.ceil((ver * 4 + 4) / (n * 2 - 2)) * 2, out = [6], p;
		for(p = ver * 4 + 10; out.length < n; p -= step) out.splice(1, 0, p);
		return out;
	}
	function utf8(s){
		var b = unescape(encodeURIComponent(s)), out = [], j;
		for(j = 0; j < b.length; j++) out.push(b.charCodeAt(j));
		return out;
	}
	function maskBit(mask, x, y){
		switch(mask){
		case 0: return (x + y) % 2 === 0;
		case 1: return y % 2 === 0;
		case 2: return x % 3 === 0;
		case 3: return (x + y) % 3 === 0;
		case 4: return (Math.floor(x / 3) + Math.floor(y / 2)) % 2 === 0;
		case 5: return x * y % 2 + x * y % 3 === 0;
		case 6: return (x * y % 2 + x * y % 3) % 2 === 0;
		default: return ((x + y) % 2 + x * y % 3) % 2 === 0;
		}
	}

	/* The modules as rows of booleans (true = dark), without the quiet zone; null when the text does not fit */
	function encode(text){
		var data = utf8(text), bits = [], words = [], ver, cap, size, m = [], fn = [], j, k, x, y, val;
		for(ver = 1; ver <= 40; ver++){
			cap = Math.floor(rawModules(ver) / 8) - ECC[ver] * BLOCKS[ver];
			if(4 + (ver < 10 ? 8 : 16) + data.length * 8 <= cap * 8) break;
		}
		if(ver > 40) return null;
		function put(value, len){ for(var b = len - 1; b >= 0; b--) bits.push((value >>> b) & 1); }
		put(4, 4);
		put(data.length, ver < 10 ? 8 : 16);
		for(j = 0; j < data.length; j++) put(data[j], 8);
		put(0, Math.min(4, cap * 8 - bits.length));
		put(0, (8 - bits.length % 8) % 8);
		for(val = 0xEC; bits.length < cap * 8; val ^= 0xEC ^ 0x11) put(val, 8);
		for(j = 0; j < bits.length; j += 8){
			for(val = 0, k = 0; k < 8; k++) val = (val << 1) | bits[j + k];
			words.push(val);
		}

		/* Split into blocks, add error correction, interleave */
		var total = Math.floor(rawModules(ver) / 8), nb = BLOCKS[ver], ecl = ECC[ver], shortLen = Math.floor(total / nb), nShort = nb - total % nb;
		var gen = generator(ecl), blocks = [], eccs = [], all = [], at = 0, len;
		for(j = 0; j < nb; j++){
			len = shortLen - ecl + (j < nShort ? 0 : 1);
			blocks.push(words.slice(at, at + len));
			eccs.push(remainder(blocks[j], gen));
			at += len;
		}
		for(k = 0; k <= shortLen - ecl; k++) for(j = 0; j < nb; j++) if(k < blocks[j].length) all.push(blocks[j][k]);
		for(k = 0; k < ecl; k++) for(j = 0; j < nb; j++) all.push(eccs[j][k]);

		/* Function patterns */
		size = ver * 4 + 17;
		for(y = 0; y < size; y++){ m.push([]); fn.push([]); for(x = 0; x < size; x++){ m[y].push(false); fn[y].push(false); } }
		function set(px, py, dark){ m[py][px] = dark; fn[py][px] = true; }
		for(j = 0; j < size; j++){ set(6, j, j % 2 === 0); set(j, 6, j % 2 === 0); }
		function finder(cx, cy){
			for(var dy = -4; dy <= 4; dy++) for(var dx = -4; dx <= 4; dx++){
				var d = Math.max(Math.abs(dx), Math.abs(dy));
				if(cx + dx >= 0 && cx + dx < size && cy + dy >= 0 && cy + dy < size) set(cx + dx, cy + dy, d !== 2 && d !== 4);
			}
		}
		finder(3, 3); finder(size - 4, 3); finder(3, size - 4);
		var ap = alignments(ver), last = ap.length - 1, a, b;
		for(a = 0; a <= last; a++) for(b = 0; b <= last; b++){
			if((a === 0 && b === 0) || (a === 0 && b === last) || (a === last && b === 0)) continue;
			for(y = -2; y <= 2; y++) for(x = -2; x <= 2; x++) set(ap[a] + x, ap[b] + y, Math.max(Math.abs(x), Math.abs(y)) !== 1);
		}
		function format(mask){
			var d = mask, r = d, fb, n;   /* level M is 00 */
			for(n = 0; n < 10; n++) r = (r << 1) ^ ((r >>> 9) * 0x537);
			fb = ((d << 10) | r) ^ 0x5412;
			function bit(q){ return ((fb >>> q) & 1) !== 0; }
			for(n = 0; n <= 5; n++) set(8, n, bit(n));
			set(8, 7, bit(6)); set(8, 8, bit(7)); set(7, 8, bit(8));
			for(n = 9; n < 15; n++) set(14 - n, 8, bit(n));
			for(n = 0; n < 8; n++) set(size - 1 - n, 8, bit(n));
			for(n = 8; n < 15; n++) set(8, size - 15 + n, bit(n));
			set(8, size - 8, true);
		}
		format(0);
		if(ver >= 7){
			var rem = ver, vb;
			for(j = 0; j < 12; j++) rem = (rem << 1) ^ ((rem >>> 11) * 0x1f25);
			vb = (ver << 12) | rem;
			for(j = 0; j < 18; j++){
				a = size - 11 + j % 3; b = Math.floor(j / 3);
				set(a, b, ((vb >>> j) & 1) !== 0); set(b, a, ((vb >>> j) & 1) !== 0);
			}
		}

		/* Data, in two-column strips from the bottom right, up and down in turn */
		var idx = 0, nbits = all.length * 8, right, vert, up;
		for(right = size - 1; right >= 1; right -= 2){
			if(right === 6) right = 5;
			up = ((right + 1) & 2) === 0;
			for(vert = 0; vert < size; vert++) for(j = 0; j < 2; j++){
				x = right - j; y = up ? size - 1 - vert : vert;
				if(!fn[y][x] && idx < nbits){ m[y][x] = ((all[idx >>> 3] >>> (7 - (idx & 7))) & 1) === 1; idx++; }
			}
		}

		/* The mask: any of the eight reads back; the penalty only picks the one easiest to scan */
		function apply(mask){ for(var py = 0; py < size; py++) for(var px = 0; px < size; px++) if(!fn[py][px] && maskBit(mask, px, py)) m[py][px] = !m[py][px]; }
		function penalty(){
			var p = 0, dark = 0, pass, s, t, run, prev, line, c;
			for(pass = 0; pass < 2; pass++) for(s = 0; s < size; s++){
				run = 0; prev = null; line = [];
				for(t = 0; t < size; t++){
					c = pass ? m[t][s] : m[s][t];
					line.push(c);
					if(c === prev){ run++; if(run === 5) p += 3; else if(run > 5) p++; }
					else { prev = c; run = 1; }
				}
				for(t = 0; t + 7 <= size; t++){
					if(line[t] && !line[t + 1] && line[t + 2] && line[t + 3] && line[t + 4] && !line[t + 5] && line[t + 6] &&
						((t >= 4 && !line[t - 1] && !line[t - 2] && !line[t - 3] && !line[t - 4]) || (t + 11 <= size && !line[t + 7] && !line[t + 8] && !line[t + 9] && !line[t + 10]))) p += 40;
				}
			}
			for(s = 0; s < size - 1; s++) for(t = 0; t < size - 1; t++){
				c = m[s][t];
				if(c === m[s][t + 1] && c === m[s + 1][t] && c === m[s + 1][t + 1]) p += 3;
			}
			for(s = 0; s < size; s++) for(t = 0; t < size; t++) if(m[s][t]) dark++;
			return p + Math.floor(Math.abs(dark * 20 - size * size * 10) / (size * size)) * 10;
		}
		var best = 0, bestP = Infinity, mk, pp;
		for(mk = 0; mk < 8; mk++){
			apply(mk); format(mk);
			pp = penalty();
			if(pp < bestP){ bestP = pp; best = mk; }
			apply(mk);
		}
		apply(best); format(best);
		return m;
	}
	DC.qrEncode = encode;

	/* An SVG of the code with a quiet zone of four modules; the path data is only numbers */
	var NS = 'http://www.w3.org/2000/svg';
	function svg(text, label){
		var q = encode(text), d = '', n, x, y, run, el, bg, path;
		if(!q) return null;
		n = q.length + 8;
		for(y = 0; y < q.length; y++){
			for(x = 0; x < q.length; x += run || 1){
				run = 0;
				while(x + run < q.length && q[y][x + run]) run++;
				if(run) d += 'M' + (x + 4) + ' ' + (y + 4) + 'h' + run + 'v1h-' + run + 'z';
			}
		}
		el = document.createElementNS(NS, 'svg');
		el.setAttribute('viewBox', '0 0 ' + n + ' ' + n);
		el.setAttribute('shape-rendering', 'crispEdges');
		el.setAttribute('role', 'img');
		el.setAttribute('aria-label', label);
		bg = document.createElementNS(NS, 'rect');
		bg.setAttribute('width', String(n)); bg.setAttribute('height', String(n)); bg.setAttribute('fill', '#FFFFFF');
		path = document.createElementNS(NS, 'path');
		path.setAttribute('d', d); path.setAttribute('fill', '#000000');
		el.appendChild(bg); el.appendChild(path);
		return el;
	}

	/* The page as a phone should open it: no query (the QTS desktop adds its window id there), and of the fragment only the
	   app's own view (#tasks/down, #settings/notify), so nothing else that happens to be in the address goes along */
	function pageAddress(){
		var view = /^#(tasks|settings)(\/[a-z]+)?$/.test(location.hash) ? location.hash : '';
		return location.protocol + '//' + location.host + location.pathname + view;
	}
	function localOnly(host){
		host = String(host || '').toLowerCase().replace(/^\[|\]$/g, '');
		return host === 'localhost' || /\.local$/.test(host) || /^(127|10)\./.test(host) || /^192\.168\./.test(host) || /^169\.254\./.test(host) ||
			/^172\.(1[6-9]|2\d|3[01])\./.test(host) || /^(::1$|f[cd][0-9a-f]{2}:|fe[89ab][0-9a-f]:)/.test(host);
	}
	DC.openOnPhone = function(){
		var url = pageAddress(), code = svg(url, DC.t('QR code of {url}', {url:url})), link = h('span', {'class':'mono', text:url});
		DC.track('open_on_phone');
		DC.modal(DC.t('Open on phone'), 'qr', [
			code ? h('div', {'class':'qrtile'}, code) : null,
			h('p', {'class':'lead qrlead', text:DC.t('Scan the code with your phone to open this page.')}),
			h('div', {'class':'qrurl'}, [link, ibtn('copy', DC.t('Copy link'), function(){ DC.copyText(url, link); })]),
			h('p', {'class':'note', text:DC.t('Only the address is in the code, not your sign-in. Sign in again on the phone.')}),
			localOnly(location.hostname) ? h('p', {'class':'note warn', text:DC.t('This address works only on the same network as this computer.')}) : null
		], function(close){ return [btn(null, DC.t('Done'), close, 'pri')]; });
	};
	/* The button for the sign-in page and Personal settings; CSS hides it on touch devices outside the QTS desktop */
	DC.phoneButton = function(cls, before){
		return h('button', {'class':'ib phoneb ' + cls, type:'button', onclick:function(){ if(before) before(); DC.openOnPhone(); }}, [DC.icon('qr'), h('span', {text:DC.t('Open on phone')})]);
	};
})();
