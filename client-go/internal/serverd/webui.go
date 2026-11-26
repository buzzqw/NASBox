package serverd

// webPage è l'interfaccia web: una sola pagina senza dipendenze esterne.
// Tutto ciò che arriva dal server entra nel DOM con textContent, mai con
// innerHTML, così un nome di file non può iniettare codice.
const webPage = `<!doctype html>
<html lang="it"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>NASBox</title>
<style>
:root{--bg:#f6f8fb;--card:#fff;--fg:#0f172a;--mut:#64748b;--line:#e2e8f0;--pri:#2563eb;--ok:#059669;--err:#dc2626;--hover:#eef3fb}
@media(prefers-color-scheme:dark){:root{--bg:#0f172a;--card:#1e293b;--fg:#e8edf3;--mut:#94a3b8;--line:#334155;--pri:#60a5fa;--ok:#34d399;--err:#f87171;--hover:#273449}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.45 system-ui,-apple-system,Segoe UI,sans-serif}
header{display:flex;gap:.75rem;align-items:center;padding:.7rem 1rem;background:var(--card);border-bottom:1px solid var(--line);position:sticky;top:0;z-index:5}
header h1{font-size:1.1rem;margin:0;white-space:nowrap}
header input[type=search]{flex:1;min-width:0;padding:.5rem .7rem;border:1px solid var(--line);border-radius:8px;background:var(--bg);color:var(--fg)}
button,.btn{font:inherit;border:1px solid var(--line);background:var(--card);color:var(--fg);padding:.45rem .8rem;border-radius:8px;cursor:pointer}
button:hover,.btn:hover{background:var(--hover)}
.primary{background:var(--pri);border-color:var(--pri);color:#fff}.primary:hover{filter:brightness(1.08);background:var(--pri)}
main{max-width:64rem;margin:0 auto;padding:1rem}
#crumbs{display:flex;flex-wrap:wrap;gap:.25rem;align-items:center;margin:.2rem 0 .8rem}
#crumbs a{color:var(--pri);cursor:pointer;text-decoration:none;padding:.1rem .3rem;border-radius:6px}
#crumbs a:hover{background:var(--hover)}#crumbs span{color:var(--mut)}
.card{background:var(--card);border:1px solid var(--line);border-radius:12px;overflow:hidden}
.row{display:grid;grid-template-columns:2rem 1fr 6rem 8rem auto;gap:.5rem;align-items:center;padding:.55rem .8rem;border-bottom:1px solid var(--line)}
.row:last-child{border-bottom:0}.row:hover{background:var(--hover)}
.row .name{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;cursor:pointer}
.row .meta{color:var(--mut);font-size:.85rem;text-align:right}
.row .acts{display:flex;gap:.3rem}.row .acts button{padding:.2rem .5rem;font-size:.85rem}
.empty{padding:2.5rem 1rem;text-align:center;color:var(--mut)}
#drop{position:fixed;inset:0;background:rgba(37,99,235,.15);border:3px dashed var(--pri);display:none;align-items:center;justify-content:center;font-size:1.4rem;z-index:20;pointer-events:none}
#toast{position:fixed;bottom:1rem;left:50%;transform:translateX(-50%);background:var(--fg);color:var(--bg);padding:.6rem 1rem;border-radius:10px;display:none;z-index:30;max-width:90vw}
#up{position:fixed;right:1rem;bottom:1rem;width:20rem;max-width:calc(100vw - 2rem);display:none;z-index:25;padding:.8rem}
#up .bar{height:6px;background:var(--line);border-radius:4px;overflow:hidden;margin-top:.4rem}#up .bar i{display:block;height:100%;width:0;background:var(--pri)}
dialog{border:1px solid var(--line);border-radius:12px;background:var(--card);color:var(--fg);max-width:min(60rem,94vw);width:100%;padding:1rem}
dialog::backdrop{background:rgba(0,0,0,.5)}
dialog img,dialog video,dialog iframe{max-width:100%;max-height:70vh;display:block;margin:.6rem auto}dialog iframe{width:100%;height:70vh;border:0}
.note{color:var(--mut);font-size:.85rem;margin:.6rem 0}
@media(max-width:640px){.row{grid-template-columns:2rem 1fr auto}.row .meta{display:none}}
#tabs{display:flex;gap:.4rem;padding:.5rem 1rem}
#tabs button[aria-current=true]{border-color:var(--pri);color:var(--pri)}
.statgrid{display:grid;grid-template-columns:repeat(auto-fit,minmax(15rem,1fr));gap:.7rem;padding:0 1rem 1rem}
.statcard{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:.8rem}
.statcard h3{margin:0 0 .5rem;font-size:.95rem}
.statcard .big{font-size:1.35rem;font-weight:600}
.statcard p{margin:.3rem 0;color:var(--mut);font-size:.85rem}
canvas.spark{width:100%;height:52px}
.bar{height:8px;background:var(--hover);border-radius:6px;overflow:hidden;margin:.35rem 0}.bar i{display:block;height:100%;background:var(--pri)}
</style></head><body>
<header><h1>NASBox</h1>
<input id="q" type="search" placeholder="Cerca per nome…" aria-label="Cerca">
<button id="linksBtn" style="display:none">Link condivisi</button>
<label id="upLabel" class="btn primary" style="display:none">Carica<input id="pick" type="file" multiple hidden></label></header>
<nav id="tabs"><button data-page="file" aria-current="true">File</button><button data-page="stato">Stato</button></nav>
<main><section id="filesPage"><div id="crumbs"></div><div class="card" id="list"></div>
<p class="note" id="foot"></p></section>
<section id="statusPage" style="display:none"><div class="statgrid">
<div class="statcard"><h3>NAS</h3><div class="big" id="stLock"></div><p id="stQueue"></p><canvas class="spark" id="sparkLoad"></canvas></div>
<div class="statcard"><h3>Deduplica chunk</h3><div class="big" id="stDedup"></div><p id="stDedupBytes"></p><div class="bar"><i id="stDedupBar"></i></div></div>
<div class="statcard"><h3>Manifest</h3><div id="stDelta"></div><div class="bar"><i id="stDeltaBar"></i></div></div>
<div class="statcard"><h3>Lock per dispositivo</h3><div id="stLocks"></div></div>
<div class="statcard"><h3>Integrità chunk</h3><div class="big" id="stFsck"></div><p id="stFsckDetail"></p></div>
<div class="statcard"><h3>Fasi lato NAS (secondi totali)</h3><div id="stPhases"></div></div>
</div></section></main>
<div id="drop">Rilascia i file per caricarli qui</div>
<div id="up" class="card"><div id="upt"></div><div class="bar"><i id="upb"></i></div></div>
<div id="toast"></div>
<dialog id="dlg"><div style="display:flex;justify-content:space-between;gap:1rem"><strong id="dt"></strong><button id="dc">Chiudi</button></div><div id="db"></div></dialog>
<script>
(function(){
var cwd="",writable=false,canShare=false,maxUp=0,mode="dir",page="file",metricHistory=[];
var $=function(id){return document.getElementById(id)};
function el(tag,text,cls){var e=document.createElement(tag);if(text!=null)e.textContent=text;if(cls)e.className=cls;return e}
function fmtSize(n){n=+n;if(n<1024)return n+" B";var u=["KB","MB","GB","TB"],i=-1;do{n/=1024;i++}while(n>=1024&&i<3);return n.toFixed(n<10?1:0)+" "+u[i]}
function fmtDate(t){if(!+t)return"";var d=new Date(t*1000);return d.toLocaleDateString()+" "+d.toLocaleTimeString([],{hour:"2-digit",minute:"2-digit"})}
function toast(msg){var t=$("toast");t.textContent=msg;t.style.display="block";clearTimeout(toast.t);toast.t=setTimeout(function(){t.style.display="none"},4500)}
function api(path,opts){return fetch(path,opts).then(function(r){if(!r.ok)return r.text().then(function(m){throw new Error(m.trim()||r.status)});return r.json()})}
function num(v){return +(v||0)}
function showPage(p){page=p;$("filesPage").style.display=p==="file"?"":"none";$("statusPage").style.display=p==="stato"?"":"none";
 var tabs=document.querySelectorAll("#tabs button");for(var i=0;i<tabs.length;i++)tabs[i].setAttribute("aria-current",tabs[i].getAttribute("data-page")===p?"true":"false");
 if(p==="stato")loadMetrics()}
function pct(a,b){return b>0?Math.min(100,Math.round(a*100/b)):0}
function loadMetrics(){api("/api/metrics").then(function(m){metricHistory.push(num(m.LOAD_1));if(metricHistory.length>120)metricHistory.shift();renderStatus(m)}).catch(function(e){toast(e.message)})}
function renderStatus(m){
 var held=m.NASBOX_LOCK_HELD==="true";$("stLock").textContent=held?"Lock occupato":"Libero";
 $("stLock").style.color=held?"var(--err)":"var(--ok)";
 $("stQueue").textContent="Coda: "+(m.NASBOX_QUEUE_COUNT||0)+" · fase: "+(m.NASBOX_LOCK_PHASE||"—")+" · revisione: "+(m.MANIFEST_BYTES?"attiva":"—");
 var hits=num(m.CHUNK_HAS_HITS),miss=num(m.CHUNK_HAS_MISSES),digests=hits+miss;
 $("stDedup").textContent=(m.CHUNK_HAS_HIT_RATE_PERCENT||"0")+"%";
 $("stDedupBytes").textContent=fmtSize(num(m.CHUNK_HAS_BYTES_SAVED))+" risparmiati su "+digests+" digest";
 $("stDedupBar").style.width=pct(hits,digests)+"%";
 var dbytes=num(m.MANIFEST_DELTA_BYTES),fbytes=num(m.MANIFEST_FULL_BYTES);
 $("stDelta").textContent="";
 ["Richieste delta: "+num(m.MANIFEST_DELTA_REQUESTS),"Fallback completi: "+num(m.MANIFEST_DELTA_FULL_FALLBACKS),"Render riutilizzati: "+num(m.MANIFEST_RENDER_CACHE_HITS),"Byte risparmiati: "+fmtSize(num(m.MANIFEST_DELTA_BYTES_SAVED))].forEach(function(t){$("stDelta").appendChild(el("p",t))});
 $("stDeltaBar").style.width=pct(fbytes-dbytes,fbytes)+"%";
 $("stLocks").textContent="";
 var devices=[];try{devices=JSON.parse(m.LOCK_DEVICE_STATS_JSON||"[]")}catch(e){}
 if(!devices.length)$("stLocks").appendChild(el("p","Nessun lease registrato"));
 devices.forEach(function(d){$("stLocks").appendChild(el("p",d.device+": "+d.grants+" lease, "+d.wait_s+"s attesa, "+d.hold_s+"s occupazione"))});
 var ok=m.CHUNK_FSCK_LAST_OK==="1";$("stFsck").textContent=num(m.CHUNK_FSCK_RUNS)?"Ultimo controllo: "+(ok?"ok":"problemi"):"Mai eseguito";
 $("stFsck").style.color=ok?"var(--ok)":"var(--err)";
 $("stFsckDetail").textContent=fmtDate(num(m.CHUNK_FSCK_LAST_TS))+" · corrotti "+num(m.CHUNK_FSCK_LAST_CORRUPT)+" · meta rotti "+num(m.CHUNK_FSCK_LAST_METAS_BROKEN);
 $("stPhases").textContent="";
 var phases=(m.PERF_PHASES||"").split(",").filter(function(x){return !!x});
 phases.forEach(function(name){var sec=num(m["PERF_"+name+"_SECONDS_TOTAL"]),calls=num(m["PERF_"+name+"_CALLS"]);
  $("stPhases").appendChild(el("p",name+": "+sec.toFixed(2)+"s su "+calls+" chiamate"))});
 drawSpark($("sparkLoad"),metricHistory,2)
}
function drawSpark(canvas,values,max){if(!canvas||!canvas.getContext)return;var g=canvas.getContext("2d"),w=canvas.width=canvas.clientWidth||300,h=canvas.height=52;
 g.clearRect(0,0,w,h);if(values.length<2)return;g.strokeStyle=getComputedStyle(canvas).color||"#2563eb";g.beginPath();
 for(var i=0;i<values.length;i++){var x=i*w/(values.length-1),y=h-Math.min(1,values[i]/max)*(h-4)-2;if(i)g.lineTo(x,y);else g.moveTo(x,y)}g.stroke()}
function enc(p){return encodeURIComponent(p)}
function join(a,b){return a?a+"/"+b:b}
function ext(n){var i=n.lastIndexOf(".");return i<0?"":n.slice(i+1).toLowerCase()}
function kindOf(n){var e=ext(n);if(["jpg","jpeg","png","gif","webp"].indexOf(e)>=0)return"img";if(e==="pdf")return"pdf";if(["mp4","webm"].indexOf(e)>=0)return"video";if(["mp3","ogg","m4a","flac","wav"].indexOf(e)>=0)return"audio";if(["txt","md"].indexOf(e)>=0)return"txt";return""}
function crumbs(){var c=$("crumbs");c.textContent="";var home=el("a","NASBox");home.onclick=function(){go("")};c.appendChild(home);var acc="";
 cwd.split("/").filter(Boolean).forEach(function(p){acc=join(acc,p);c.appendChild(el("span","/"));var a=el("a",p),t=acc;a.onclick=function(){go(t)};c.appendChild(a)})}
function render(items,searching){var l=$("list");l.textContent="";
 if(!items.length){l.appendChild(el("div",searching?"Nessun risultato.":"Cartella vuota."+(writable?" Trascina qui dei file per caricarli.":""),"empty"));return}
 items.forEach(function(it){var r=el("div",null,"row"),dir=it.kind==="DIR";
  r.appendChild(el("span",dir?"📁":"📄"));
  var n=el("div",searching?it.path:it.name,"name");n.title=it.path||it.name;
  var full=it.path||join(cwd,it.name);
  n.onclick=function(){if(dir)go(full);else open(full,it.name)};r.appendChild(n);
  r.appendChild(el("div",dir?"":fmtSize(it.size),"meta"));r.appendChild(el("div",fmtDate(it.mtime),"meta"));
  var a=el("div",null,"acts");
  if(!dir){var d=el("button","Scarica");d.onclick=function(){location.href="/api/download?path="+enc(full)};a.appendChild(d);
   var v=el("button","Versioni");v.onclick=function(){versions(full)};a.appendChild(v);
   if(canShare){var sh=el("button","Condividi");sh.onclick=function(){shareDialog(full,it.name||full)};a.appendChild(sh)}}
  r.appendChild(a);l.appendChild(r)})}
function sortItems(a){return a.slice().sort(function(x,y){if(x.kind!==y.kind)return x.kind==="DIR"?-1:1;return x.name.toLowerCase()<y.name.toLowerCase()?-1:1})}
function go(path){mode="dir";cwd=path;showPage("file");$("q").value="";crumbs();
 api("/api/browse?path="+enc(path)).then(function(a){render(sortItems(a.map(function(e){return{name:e.name,kind:e.kind,size:e.size,mtime:e.mtime}})),false);$("foot").textContent=a.length+" elementi"}).catch(function(e){render([],false);toast(e.message)})}
function open(path,name){var k=kindOf(name),u="/api/download?inline=1&path="+enc(path);if(!k){location.href="/api/download?path="+enc(path);return}
 var b=$("db");b.textContent="";$("dt").textContent=name;var m;
 if(k==="img"){m=el("img");m.src=u;m.alt=name}else if(k==="pdf"||k==="txt"){m=el("iframe");m.src=u}else{m=el(k);m.controls=true;m.src=u}
 b.appendChild(m);var dl=el("button","Scarica","primary");dl.onclick=function(){location.href="/api/download?path="+enc(path)};b.appendChild(dl);$("dlg").showModal()}
function versions(path){api("/api/versions?path="+enc(path)).then(function(v){var b=$("db");b.textContent="";$("dt").textContent="Versioni di "+path.split("/").pop();
 if(!v.length)b.appendChild(el("p","Nessuna versione precedente.","note"));
 v.forEach(function(x){var r=el("div",null,"row");r.style.gridTemplateColumns="1fr 6rem auto";r.appendChild(el("div",x.suffix.replace("--"," ").replace(/-(\d\d)-(\d\d)-(\d\d)/,":$1:$2:$3")));r.appendChild(el("div",fmtSize(x.size),"meta"));
  var d=el("button","Scarica");d.onclick=function(){location.href="/api/download?path="+enc(x.path)};r.appendChild(d);b.appendChild(r)});
 b.appendChild(el("p","Per ripristinare una versione usa l'app NASBox sul PC.","note"));$("dlg").showModal()}).catch(function(e){toast(e.message)})}
function postJSON(url,method){return api(url,{method:method,headers:{"X-NASBox-Web":"1"}})}
function expiryText(t){return t?"scade il "+new Date(t*1000).toLocaleDateString():"non scade"}
function shareDialog(path,name){var b=$("db");b.textContent="";$("dt").textContent="Condividi "+name;
 b.appendChild(el("p","Chiunque abbia il link potrà scaricare questo file. Scegli per quanto tempo:","note"));
 var row=el("div");row.style.cssText="display:flex;gap:.5rem;flex-wrap:wrap";b.appendChild(row);
 [["1 giorno","1"],["7 giorni","7"],["30 giorni","30"],["Senza scadenza","0"]].forEach(function(o){var x=el("button",o[0]);
  x.onclick=function(){postJSON("/api/share?path="+enc(path)+"&days="+o[1],"POST").then(function(r){shown(r)}).catch(function(e){toast(e.message)})};row.appendChild(x)});
 $("dlg").showModal();
 function shown(r){b.textContent="";b.appendChild(el("p","Link creato ("+expiryText(r.expires)+"):","note"));
  var inp=el("input");inp.value=r.url;inp.readOnly=true;inp.style.cssText="width:100%;padding:.5rem;border:1px solid var(--line);border-radius:8px;background:var(--bg);color:var(--fg)";b.appendChild(inp);inp.select();
  var cp=el("button","Copia il link","primary");cp.style.marginTop=".6rem";cp.onclick=function(){inp.select();if(navigator.clipboard&&navigator.clipboard.writeText){navigator.clipboard.writeText(r.url).then(function(){toast("Link copiato")},function(){document.execCommand("copy");toast("Link copiato")})}else{document.execCommand("copy");toast("Link copiato")}};b.appendChild(cp)}}
function linksDialog(){api("/api/share").then(function(list){var b=$("db");b.textContent="";$("dt").textContent="Link condivisi";
 if(!list.length)b.appendChild(el("p","Nessun link attivo.","note"));
 list.forEach(function(l){var r=el("div",null,"row");r.style.gridTemplateColumns="1fr auto auto";
  var n=el("div",l.path+" · "+expiryText(l.expires)+" · "+l.downloads+" download","name");n.title=l.url;r.appendChild(n);
  var c=el("button","Copia");c.onclick=function(){if(navigator.clipboard)navigator.clipboard.writeText(l.url).then(function(){toast("Link copiato")})};r.appendChild(c);
  var d=el("button","Revoca");d.onclick=function(){postJSON("/api/share?token="+enc(l.token),"DELETE").then(function(){linksDialog()}).catch(function(e){toast(e.message)})};r.appendChild(d);b.appendChild(r)});
 $("dlg").showModal()}).catch(function(e){toast(e.message)})}
$("linksBtn").onclick=linksDialog;
var st;$("q").oninput=function(){clearTimeout(st);var q=this.value.trim();if(q.length<2){if(mode==="search")go(cwd);return}
 st=setTimeout(function(){mode="search";api("/api/search?q="+enc(q)).then(function(r){render(r.hits,true);$("foot").textContent=r.hits.length+" risultati"+(r.truncated?" (ricerca troncata, affina i termini)":"")}).catch(function(e){toast(e.message)})},300)};
function upload(files){if(!writable){toast("Caricamento non abilitato sul server.");return}
 files=Array.prototype.slice.call(files);if(!files.length)return;
 var total=files.reduce(function(s,f){return s+f.size},0);if(maxUp&&total>maxUp){toast("Troppo grande: massimo "+fmtSize(maxUp)+" per richiesta.");return}
 var fd=new FormData();files.forEach(function(f){fd.append("file",f,f.name)});
 var x=new XMLHttpRequest();x.open("POST","/api/upload?path="+enc(cwd));x.setRequestHeader("X-NASBox-Web","1");
 var box=$("up");box.style.display="block";$("upt").textContent="Caricamento di "+files.length+" file…";$("upb").style.width="0";
 x.upload.onprogress=function(e){if(e.lengthComputable)$("upb").style.width=(100*e.loaded/e.total)+"%"};
 x.onload=function(){box.style.display="none";if(x.status===200){var r=JSON.parse(x.responseText);toast(r.saved.length+" file caricati");go(cwd)}else toast("Errore: "+(x.responseText||x.status).trim())};
 x.onerror=function(){box.style.display="none";toast("Connessione interrotta")};x.send(fd)}
$("pick").onchange=function(){upload(this.files);this.value=""};
var depth=0;window.addEventListener("dragenter",function(e){if(!writable)return;e.preventDefault();depth++;$("drop").style.display="flex"});
window.addEventListener("dragleave",function(){depth=Math.max(0,depth-1);if(!depth)$("drop").style.display="none"});
window.addEventListener("dragover",function(e){e.preventDefault()});
window.addEventListener("drop",function(e){e.preventDefault();depth=0;$("drop").style.display="none";if(e.dataTransfer&&e.dataTransfer.files.length)upload(e.dataTransfer.files)});
$("dc").onclick=function(){$("dlg").close();$("db").textContent=""};$("dlg").addEventListener("close",function(){$("db").textContent=""});
var tabs=document.querySelectorAll("#tabs button");for(var ti=0;ti<tabs.length;ti++)tabs[ti].onclick=function(){showPage(this.getAttribute("data-page"))};
setInterval(function(){if(page==="stato")loadMetrics()},5000);
api("/api/info").then(function(i){writable=!!i.writable;canShare=!!i.can_share;maxUp=i.max_upload_bytes||0;if(canShare)$("linksBtn").style.display="";if(writable)$("upLabel").style.display="";else $("foot").textContent="Sola lettura (imposta WEB_PASSWORD per caricare file).";go("")}).catch(function(e){toast(e.message);go("")});
})();
</script></body></html>`
