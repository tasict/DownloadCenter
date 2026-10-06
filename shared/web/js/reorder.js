/* Reordering the queue. A task is dragged by its handle, by the row itself with a mouse, or with the keyboard on the handle;
   Alt+arrows on a row move it one place. While it is in hand a slot shows where it lands and what that does to the download
   slots. Dropping moves it next to the visible task there: the server takes that neighbour as the anchor, so filters and the
   tasks of other users this account does not see cannot put it in the wrong place. The toast reports what the server says
   started or went back to waiting, with Undo. Loaded before app.js, which calls in through DC.reorder. */
(function(){
	'use strict';
	var DC = window.DC, h = DC.h, add = DC.add, clear = DC.clear, icon = DC.icon;
	var drag = null, dragged = false, liveEl = null, pending = 0, gen = 0;

	function S(){ return DC.S; }
	function short(n){ n = String(n || ''); return n.length > 24 ? n.slice(0, 22) + '…' : n; }
	function nameOf(t){ return t.name || t.source || ''; }
	function has(list, id){ return list.indexOf(id) >= 0; }
	function anySel(){ var k, sel = S().sel; for(k in sel) if(sel.hasOwnProperty(k) && sel[k]) return true; return false; }
	/* Phones drag only in selection mode, where a press on the row selects instead */
	function canDrag(t){
		var s = S();
		return !!t && s.view === 'tasks' && s.sort === 'queue' && DC.can('tasks:control') && (!DC.phone() || s.picking || anySel());
	}

	/* ---------- what a move does ---------- */
	function holds(t){ var s = DC.uiState(t); return (s === 'down' || s === 'check') && !t.user_paused; }
	function eligible(t){ var s = DC.uiState(t); return (s === 'down' || s === 'check' || s === 'wait') && !t.user_paused; }
	/* Who would have a download slot in this order: per type, the first tasks that may download, as many as hold a slot now.
	   The server also counts tasks this account does not see, so the guess never hands out more slots than are in use. */
	function slots(list){
		var n = {}, used = {}, out = {}, all = S().tasks, i, t;
		for(i = 0; i < all.length; i++) if(holds(all[i])) n[all[i].proto] = (n[all[i].proto] || 0) + 1;
		for(i = 0; i < list.length; i++){
			t = list[i];
			if(!eligible(t) || (used[t.proto] || 0) >= (n[t.proto] || 0)) continue;
			used[t.proto] = (used[t.proto] || 0) + 1;
			out[t.id] = true;
		}
		return out;
	}
	/* The queue as this account sees it, with ids (in queue order) moved next to the anchor */
	function arrange(ids, anchor, after){
		var all = S().tasks, moving = [], rest = [], i, k;
		for(i = 0; i < all.length; i++) (has(ids, all[i].id) ? moving : rest).push(all[i]);
		k = rest.length;
		for(i = 0; i < rest.length; i++) if(rest[i].id === anchor){ k = after ? i + 1 : i; break; }
		return rest.slice(0, k).concat(moving, rest.slice(k));
	}
	function sameOrder(list){
		var all = S().tasks, i;
		for(i = 0; i < all.length; i++) if(all[i] !== list[i]) return false;
		return true;
	}
	function waitText(proto, n){
		if(proto === 'bt') return DC.t('Queued: #{n} in the torrent queue', {n:n});
		if(proto === 'ftp') return DC.t('Queued: #{n} in the FTP queue', {n:n});
		return DC.t('Queued: #{n} in the URL queue', {n:n});
	}
	function dropRankText(proto, n){
		if(proto === 'bt') return DC.t('Drop to wait as #{n} in the torrent queue', {n:n});
		if(proto === 'ftp') return DC.t('Drop to wait as #{n} in the FTP queue', {n:n});
		return DC.t('Drop to wait as #{n} in the URL queue', {n:n});
	}
	/* Place among the tasks of its type waiting in this order. Tasks this account does not see may wait ahead of it: the
	   server's rank of the waiting task before it says how many. */
	function rankIn(next, now, t){
		var n = 0, prev = null, seen = 0, all = S().tasks, i, x;
		for(i = 0; i < next.length; i++){
			x = next[i];
			if(x.proto !== t.proto || !eligible(x) || now[x.id]) continue;
			n++;
			if(x === t) break;
			prev = x;
		}
		if(!prev || !prev.queue_rank) return n;
		for(i = 0; i < all.length; i++){
			x = all[i];
			if(x.proto === prev.proto && x.state === 'queued' && !x.user_paused) seen++;
			if(x === prev) break;
		}
		return n + Math.max(0, prev.queue_rank - seen);
	}
	/* The note on the slot: what dropping the tasks here does. Names only when this account sees every task; otherwise the
	   task that gives up or takes over the slot may be one it does not see. */
	function consequence(ids, anchor, after){
		var cur = slots(S().tasks), next = arrange(ids, anchor, after), now = slots(next), started = [], stopped = [], i, t, other,
			named = !!(S().me && S().me.tasks === 'all'), lead = DC.task(ids[0]), mine = {go:false, yield:false}, c = {next:next}, st;
		for(i = 0; i < next.length; i++){
			t = next[i];
			if(now[t.id] && !cur[t.id]){ started.push(t); if(has(ids, t.id)) mine.go = true; }
			if(cur[t.id] && !now[t.id]){ stopped.push(t); if(has(ids, t.id)) mine.yield = true; }
		}
		if(ids.length > 1){
			c.tone = mine.go ? 'go' : mine.yield ? 'yield' : '';
			c.icon = mine.go ? 'play' : mine.yield ? 'wait' : 'grip';
			c.text = started.length || stopped.length ? DC.t('Dropping here starts {a} and makes {b} wait', {a:started.length, b:stopped.length}) : DC.t('Move {n} tasks here', {n:ids.length});
			return c;
		}
		c.tone = ''; c.icon = 'wait';
		st = lead ? DC.uiState(lead) : 'pause';
		if(st === 'pause'){ c.icon = 'pause'; c.text = DC.t('Stays paused; when resumed it queues from here'); }
		else if(st === 'error'){ c.icon = 'error'; c.text = DC.t('Drop to put it here; when retried it queues from here'); }
		/* Finished, seeding and moving tasks never take a slot: the move only changes where they are in the list */
		else if(st === 'done' || st === 'seed' || st === 'move'){ c.icon = DC.ST[st].icon; c.text = DC.t('Drop to put it here; it does not take a download slot'); }
		else if(mine.go){
			other = stopped[0];
			c.tone = 'go'; c.icon = 'play';
			c.text = named && other ? DC.t('Drop to start downloading now; “{name}” will wait', {name:short(nameOf(other))}) : DC.t('Drop to start downloading now; another task will wait');
		}else if(mine.yield){
			other = started[0];
			c.tone = 'yield';
			c.text = named && other ? DC.t('Drop to wait; “{name}” starts downloading', {name:short(nameOf(other))}) : DC.t('Drop to wait; another task starts downloading');
		}else if(now[lead.id]){ c.icon = 'down'; c.text = DC.t('Drop to keep downloading'); }
		else c.text = dropRankText(lead.proto, rankIn(next, now, lead));
		return c;
	}

	/* ---------- moving ---------- */
	/* Dragging one of several selected tasks moves all of them, in their queue order */
	function moveIds(id){
		var s = S(), out = [], i, t;
		if(s.sel[id]){
			for(i = 0; i < s.tasks.length; i++){ t = s.tasks[i]; if(s.sel[t.id] && DC.R.rows && DC.R.rows[t.id]) out.push(t.id); }
			if(out.length > 1 && has(out, id)) return out;
		}
		return [id];
	}
	/* How to put the tasks back: each before the task that followed it, or after the one before it at the end */
	function undoPlan(ids){
		var all = S().tasks, steps = [], order = [], i, j, nxt;
		for(i = 0; i < all.length; i++){
			order.push(all[i].id);
			if(!has(ids, all[i].id)) continue;
			nxt = null;
			for(j = i + 1; j < all.length; j++) if(!has(ids, all[j].id)){ nxt = all[j].id; break; }
			if(nxt) steps.push({id:all[i].id, at:{before:nxt}});
			else if(i > 0) steps.push({id:all[i].id, at:{after:all[i - 1].id}});
		}
		return {steps:steps, order:order};
	}
	function begin(){ pending++; gen++; }
	function end(){ pending--; gen++; DC.pollNow(); }
	function flash(ids){
		var i, r;
		for(i = 0; i < ids.length; i++){
			r = DC.R.rows && DC.R.rows[ids[i]];
			if(!r) continue;
			r.el.classList.remove('arrive'); void r.el.offsetWidth; r.el.classList.add('arrive');
			(function(el){ setTimeout(function(){ el.classList.remove('arrive'); }, 1700); })(r.el);
		}
	}
	/* Shown at once; the server's answer says what really started or stopped */
	function move(ids, anchor, after){
		var plan = undoPlan(ids), c = consequence(ids, anchor, after), at = after ? {after:anchor} : {before:anchor}, p, body;
		DC.track('queue_move');
		S().tasks = c.next;
		begin();
		DC.renderList();
		flash(ids);
		say(c.text);
		if(ids.length > 1){
			body = {ids:ids, action:'move'};
			if(after) body.after = anchor; else body.before = anchor;
			p = DC.api.post('tasks/bulk', body);
		}else p = DC.api.patch('tasks/' + encodeURIComponent(ids[0]), {position:at});
		p.then(function(r){ end(); report(r || {}, ids, plan); }, function(e){ end(); DC.toast(DC.errText(e)); });
	}
	function report(r, ids, plan){
		var st = r.started || [], sp = r.stopped || [], msg;
		if(st.length === 1 && sp.length === 1) msg = DC.t('“{a}” started downloading; “{b}” is now waiting', {a:short(st[0].name), b:short(sp[0].name)});
		else if(st.length && sp.length) msg = DC.t('{a} started downloading, {b} now waiting', {a:st.length, b:sp.length});
		else if(st.length === 1) msg = DC.t('“{name}” started downloading', {name:short(st[0].name)});
		else if(sp.length === 1) msg = DC.t('“{name}” is now waiting', {name:short(sp[0].name)});
		else if(st.length) msg = DC.t('{n} tasks started downloading', {n:st.length});
		else if(sp.length) msg = DC.t('{n} tasks now waiting', {n:sp.length});
		else msg = ids.length > 1 ? DC.t('Moved {n} tasks', {n:ids.length}) : DC.t('Order changed');
		DC.toast(msg, {label:DC.t('Undo'), fn:function(){ restore(plan); }});
	}
	function restore(plan){
		var s = S(), back = [], chain = Promise.resolve(), i;
		for(i = 0; i < plan.order.length; i++) if(s.byId[plan.order[i]]) back.push(s.byId[plan.order[i]]);
		for(i = 0; i < s.tasks.length; i++) if(!has(plan.order, s.tasks[i].id)) back.push(s.tasks[i]);
		s.tasks = back;
		begin();
		DC.renderList();
		plan.steps.forEach(function(st){
			chain = chain.then(function(){ return DC.api.patch('tasks/' + encodeURIComponent(st.id), {position:st.at}, {quiet:true}); });
		});
		chain.then(function(){ DC.toast(DC.t('Restored')); say(DC.t('Restored')); }, function(e){ DC.toast(DC.errText(e)); }).then(end);
	}

	/* ---------- dragging ---------- */
	function say(text){
		if(!liveEl || !liveEl.parentNode){ liveEl = h('div', {'class':'dq-sr', 'aria-live':'polite'}); document.body.appendChild(liveEl); }
		liveEl.textContent = '';
		setTimeout(function(){ if(liveEl) liveEl.textContent = text; }, 30);
	}
	/* The rows still in the list, in order */
	function rowsLeft(){
		var out = [], kids = DC.R.list ? DC.R.list.children : [], i;
		for(i = 0; i < kids.length; i++) if(kids[i].classList.contains('row') && !kids[i].classList.contains('dq-hide')) out.push(kids[i]);
		return out;
	}
	function start(id, e, keyboard, fromY){
		var R = DC.R, ids = moveIds(id), src = R.rows && R.rows[id] && R.rows[id].el, rect, lr, rows, k = 0, i, copy, d;
		if(!src || !R.list || drag) return;
		rect = src.getBoundingClientRect(); lr = R.list.getBoundingClientRect();
		/* The slot starts where the task was: before the first row that followed it */
		rows = rowsLeft();
		for(i = 0; i < rows.length; i++){ if(rows[i] === src) break; if(!has(ids, rows[i].getAttribute('data-id'))) k++; }
		d = drag = {ids:ids, lead:id, k:-1, keyboard:keyboard, y:0};
		d.slot = h('div', {'class':'dq-slot', 'aria-hidden':keyboard ? null : 'true', tabindex:keyboard ? '-1' : null}, h('span', {'class':'dq-note'}));
		d.slot.style.minHeight = Math.max(52, rect.height - 8) + 'px';
		if(!keyboard){
			copy = src.cloneNode(true);
			copy.removeAttribute('tabindex');
			copy.classList.remove('arrive');
			d.ghost = h('div', {'class':'dq-ghost', 'aria-hidden':'true'}, [h('div', {'class':R.list.className}, copy), ids.length > 1 ? h('span', {'class':'dq-count num', text:String(ids.length)}) : null]);
			d.offset = (fromY === undefined ? e.clientY : fromY) - rect.top;
			d.ghost.style.left = lr.left + 'px';
			d.ghost.style.width = lr.width + 'px';
			d.ghost.style.top = rect.top + 'px';
			DC.layer().appendChild(d.ghost);
			try{ window.getSelection().removeAllRanges(); }catch(x){}
		}
		R.list.classList.add('dq-on');
		for(i = 0; i < ids.length; i++) if(R.rows[ids[i]]) R.rows[ids[i]].el.classList.add('dq-hide');
		if(keyboard){
			d.slot.addEventListener('keydown', keyMove);
			/* Moving the slot takes the focus away for a moment; clicking elsewhere puts the task back */
			d.slot.addEventListener('blur', function(){ setTimeout(function(){ if(drag === d && document.activeElement !== d.slot) finish(false); }, 0); });
			place(k);
			d.slot.focus();
			say(DC.sentences(DC.t('Picked up “{name}”. Use the up and down arrow keys to move it, Space to drop, Escape to cancel.', {name:short(nameOf(DC.task(id) || {}))}), d.c ? d.c.text : ''));
			return;
		}
		d.pid = e.pointerId;
		try{ R.list.setPointerCapture(e.pointerId); }catch(x){}
		d.onMove = function(ev){ if(ev.pointerId !== d.pid) return; ev.preventDefault(); track(ev.clientY); };
		d.onUp = function(ev){ if(ev.pointerId === d.pid) finish(true); };
		d.onCancel = function(ev){ if(ev.pointerId === d.pid) finish(false); };
		d.onKey = function(ev){ if(ev.key !== 'Escape') return; ev.preventDefault(); ev.stopPropagation(); finish(false); };
		document.addEventListener('pointermove', d.onMove);
		document.addEventListener('pointerup', d.onUp);
		document.addEventListener('pointercancel', d.onCancel);
		document.addEventListener('keydown', d.onKey, true);
		track(e.clientY);
		autoScroll();
	}
	function track(y){
		var d = drag, rows = rowsLeft(), k = rows.length, lr = DC.R.list.getBoundingClientRect(), i, r;
		d.y = y;
		for(i = 0; i < rows.length; i++){ r = rows[i].getBoundingClientRect(); if(y < r.top + r.height / 2){ k = i; break; } }
		d.ghost.style.top = Math.max(lr.top - 8, Math.min(lr.bottom - 30, y - d.offset)) + 'px';
		place(k);
	}
	/* Moves the slot to before row k (after the last row when k is past the end) and says what dropping there does */
	function place(k){
		var d = drag, rows = rowsLeft(), tops = [], note = d.slot.firstChild, i, el, dy;
		k = Math.max(0, Math.min(rows.length, k));
		if(k === d.k) return;
		/* Rows slide to their new places from where they were */
		for(i = 0; i < rows.length; i++) tops.push(rows[i].getBoundingClientRect().top);
		d.k = k;
		DC.R.list.insertBefore(d.slot, rows[k] || null);
		if(!rows.length){ d.anchor = null; d.c = null; return; }
		d.anchor = (rows[k] || rows[rows.length - 1]).getAttribute('data-id');
		d.after = k >= rows.length;
		d.c = consequence(d.ids, d.anchor, d.after);
		d.slot.className = 'dq-slot' + (d.c.tone ? ' ' + d.c.tone : '');
		clear(note);
		add(note, [icon(d.c.icon), h('span', {text:d.c.text})]);
		if(d.keyboard) say(d.c.text);
		for(i = 0; i < rows.length; i++){
			el = rows[i];
			dy = tops[i] - el.getBoundingClientRect().top;
			if(!dy) continue;
			el.style.transition = 'none';
			el.style.transform = 'translateY(' + dy + 'px)';
			void el.offsetWidth;
			el.style.transition = 'transform .18s cubic-bezier(.2,1,.3,1)';
			el.style.transform = '';
		}
	}
	/* Near the top or bottom edge the page scrolls, faster the closer the pointer is */
	function autoScroll(){
		var d = drag, top = 72, bottom = window.innerHeight - (DC.phone() ? 110 : 64), v = 0;
		if(!d || d.keyboard) return;
		if(d.y < top) v = -Math.min(16, Math.ceil((top - d.y) / 4));
		else if(d.y > bottom) v = Math.min(16, Math.ceil((d.y - bottom) / 4));
		if(v){ window.scrollBy(0, v); track(d.y); }
		d.raf = window.requestAnimationFrame(autoScroll);
	}
	function finish(drop){
		var d = drag, R = DC.R, i, el, next;
		if(!d) return;
		drag = null;
		if(!d.keyboard){
			document.removeEventListener('pointermove', d.onMove);
			document.removeEventListener('pointerup', d.onUp);
			document.removeEventListener('pointercancel', d.onCancel);
			document.removeEventListener('keydown', d.onKey, true);
			if(d.raf) window.cancelAnimationFrame(d.raf);
			try{ R.list.releasePointerCapture(d.pid); }catch(x){}
			/* The click that ends a drag must not open the task */
			dragged = true;
			setTimeout(function(){ dragged = false; }, 0);
		}
		DC.remove(d.ghost);
		DC.remove(d.slot);
		if(R.list){
			R.list.classList.remove('dq-on');
			for(i = 0; i < R.list.children.length; i++){ el = R.list.children[i]; el.classList.remove('dq-hide'); el.style.transition = ''; el.style.transform = ''; }
		}
		next = drop && d.anchor ? arrange(d.ids, d.anchor, d.after) : null;
		if(!next || sameOrder(next)){
			DC.renderList();
			if(!drop) say(DC.t('Cancelled; the order did not change'));
		}else move(d.ids, d.anchor, d.after);
		if(d.keyboard) refocus(d.lead);
	}
	function refocus(id){
		var r = DC.R.rows && DC.R.rows[id], g;
		if(!r) return;
		g = r.el.querySelector('.grip');
		(g || r.el).focus();
	}
	/* Keyboard: Space or Enter on the handle picks the task up, arrows move the slot, Space or Enter drops, Escape cancels */
	function keyMove(e){
		var d = drag, n;
		if(!d) return;
		n = rowsLeft().length;
		if(e.key === 'ArrowUp' || e.key === 'ArrowDown') place(d.k + (e.key === 'ArrowUp' ? -1 : 1));
		else if(e.key === 'Home' || e.key === 'End') place(e.key === 'Home' ? 0 : n);
		else if(e.key === ' ' || e.key === 'Enter') finish(true);
		else if(e.key === 'Escape' || e.key === 'Tab') finish(false);
		else return;
		e.preventDefault();
		e.stopPropagation();
		if(drag) drag.slot.focus();
	}

	DC.reorder = {
		/* The handle for a row; app.js adds it to the row's actions for tasks that can move */
		grip:function(t){
			return h('button', {'class':'ib sq grip', type:'button', 'aria-label':DC.t('Change the order of “{name}”', {name:nameOf(t)}), title:DC.t('Drag to change the order'),
				onpointerdown:function(e){ if(e.button || !canDrag(DC.task(t.id))) return; e.preventDefault(); e.stopPropagation(); start(t.id, e, false); },
				ontouchstart:function(e){ e.stopPropagation(); },
				onkeydown:function(e){
					if(drag || (e.key !== ' ' && e.key !== 'Enter') || !canDrag(DC.task(t.id))) return;
					e.preventDefault(); e.stopPropagation();
					start(t.id, null, true);
				},
				onclick:function(e){ e.stopPropagation(); }}, icon('grip'));
		},
		/* Every task can move; its place only matters while it waits for a download slot */
		movable:function(){ return DC.can('tasks:control'); },
		/* A mouse press on the row: the drag starts once it has moved 6 px, so a click still opens the task */
		rowDown:function(id, e){
			var sx = e.clientX, sy = e.clientY;
			if(drag || e.pointerType !== 'mouse' || e.button !== 0 || !canDrag(DC.task(id))) return;
			if(e.target.closest && e.target.closest('button,input,a,select,textarea')) return;
			function mv(ev){ if(Math.abs(ev.clientX - sx) + Math.abs(ev.clientY - sy) > 6){ off(); start(id, ev, false, sy); } }
			function off(){ document.removeEventListener('pointermove', mv); document.removeEventListener('pointerup', off); document.removeEventListener('pointercancel', off); }
			document.addEventListener('pointermove', mv);
			document.addEventListener('pointerup', off);
			document.addEventListener('pointercancel', off);
		},
		/* Alt+Up / Alt+Down on a row: before the visible row above, or after the one below */
		step:function(id, dir){
			var ids, vis, i, first = -1, last = -1, anchor = null;
			if(drag || !canDrag(DC.task(id))) return false;
			ids = moveIds(id); vis = DC.visibleTasks();
			for(i = 0; i < vis.length; i++) if(has(ids, vis[i].id)){ if(first < 0) first = i; last = i; }
			if(dir < 0){ for(i = first - 1; i >= 0; i--) if(!has(ids, vis[i].id)){ anchor = vis[i].id; break; } }
			else for(i = last + 1; i < vis.length; i++) if(!has(ids, vis[i].id)){ anchor = vis[i].id; break; }
			if(anchor) move(ids, anchor, dir > 0);
			if(DC.R.rows && DC.R.rows[id]) DC.R.rows[id].el.focus();
			return true;
		},
		waitText:waitText,
		active:function(){ return !!drag; },
		cancel:function(){ if(drag) finish(false); },
		justDragged:function(){ return dragged; },
		/* Task lists that were asked for before a move was answered still have the old order */
		gen:function(){ return gen; },
		pending:function(){ return pending > 0; }
	};
})();
