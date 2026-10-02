/* semiplane head resolver — UI §3.7 */
(function(){var d=document,e=d.documentElement,M=/^(auto|laptop|phone|tv)$/,T=/^(light|dark)$/,H=/SmartTV|Tizen|webOS|HbbTV|Web0S|NetCast|AppleTV/,S=/[;&\s]+/,q=new URLSearchParams(location.search).get("ui"),c=d.cookie.match(/(?:^|;\s*)sp_ui=([^;]*)/),p={theme:"auto"},i,x,f,o=1;if(c)for(i of c[1].split(S)){x=i.split("=");if(x.length>1)p[x[0]]=x[1]}p.theme=T.test(p.theme)?p.theme:"auto";f=M.test(q||"")?q:M.test(p.ui||"")?p.ui:"";if(!f&&!c&&H.test(navigator.userAgent))f="tv";function t(){var w=innerWidth,h=innerHeight;return f=="tv"?w>=2200?"tv-wide":"tv":f=="phone"||w<1024?h<=480?"compact-short":"compact":w<1280?"medium":w<1536?"large":"wide"}function a(){e.dataset.ui=t();e.dataset.theme=p.theme=="auto"?(matchMedia("(prefers-color-scheme:dark)").matches?"dark":"light"):p.theme;if(f&&o)d.cookie="sp_ui=theme="+p.theme+"&ui="+f+"; Path=/; Max-Age=31536000; SameSite=Lax"}e.spUI=function(u,c){if(M.test(u))f=u;T.test(c)&&(p.theme=c);a()};e.spTier=a;a();o=0})();
/*
 * Part 2 — the sheet re-parent. It cannot be part 1, and that is the whole
 * reason it is a second element rather than a second statement: at the moment
 * the resolver runs there is no document.body, so there is nowhere to move
 * anything to. The DOM does not exist yet, which is why the tier lives on the
 * document element (available immediately) and the movement waits for parse.
 *
 * Moving a node is not re-rendering it (UI §3.7's last task, UI §3.5's
 * "reflows with no modal, no reload, no loss of editor buffer"). Nothing is
 * fetched, nothing is rebuilt, and a moved panel keeps its scroll offset. The
 * recorded home is the parent and the next sibling rather than an index,
 * because a panel's recorded sibling is always a non-panel: in the shell the
 * order is header, nav, main, rail, footer, so each panel's next sibling is
 * the element after it. Focus is captured and reapplied because §7.9 requires
 * an orientation change to preserve it.
 */
(function(){function M(){var d=document,e=d.documentElement,h=d.createElement("div"),n=e.querySelectorAll("[role=navigation],[role=complementary]"),a=[];h.className="shell-sheets";d.body.append(h);n.forEach(function(x){a.push([x,x.parentNode,x.nextSibling])});return function(){var f=d.activeElement;e.spTier&&e.spTier();a.forEach(function(r){var s=e.dataset.ui.indexOf("compact")==0?h:r[1],q=s===h?null:r[2];if(r[0].parentNode!=s||r[0].nextSibling!=q)s.insertBefore(r[0],q)});if(f&&f.focus)f.focus()}}addEventListener("DOMContentLoaded",function(){var r=M();r();addEventListener("resize",r)})})();
