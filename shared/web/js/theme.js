/* Applies the saved theme, and the saved glass level inside the QTS desktop, before the first paint (loaded in <head>). */
(function(){
	'use strict';
	var root = document.documentElement, v, g;
	try{ v = localStorage.getItem('dc-theme'); g = localStorage.getItem('dc-glass'); }catch(e){}
	if(v === 'light' || v === 'dark') root.setAttribute('data-theme', v);
	var embedded = true;
	try{ embedded = window.self !== window.top; }catch(e){}
	if(embedded) root.className += ' embedded';
	/* The glass level is adjustable only inside the QTS desktop; a full tab always uses the default */
	if(embedded && g !== null && g !== undefined && !isNaN(+g)){ root.style.setProperty('--glass-a', (0.3 + (+g) * 0.0065).toFixed(3)); root.style.setProperty('--glass-blur', Math.round(4 + (+g) * 0.36) + 'px'); }
})();
