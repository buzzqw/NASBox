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
</style></head><body>
<header><h1>NASBox</h1>
<input id="q" type="search" placeholder="Cerca per nome…" aria-label="Cerca">
<label id="upLabel" class="btn primary" style="display:none">Carica<input id="pick" type="file" multiple hidden></label></header>
<main><div id="crumbs"></div><div class="card" id="list"></div>
<p class="note" id="foot"></p></main>
<div id="drop">Rilascia i file per caricarli qui</div>
<div id="up" class="card"><div id="upt"></div><div class="bar"><i id="upb"></i></div></div>
<div id="toast"></div>
<dialog id="dlg"><div style="display:flex;justify-content:space-between;gap:1rem"><strong id="dt"></strong><button id="dc">Chiudi</button></div><div id="db"></div></dialog>
<script>
(function(){
var cwd="",writable=false,maxUp=0,mode="dir";
var $=function(id){return document.getElementById(id)};
function el(tag,text,cls){var e=document.createElement(tag);if(text!=null)e.textContent=text;if(cls)e.className=cls;return e}
function fmtSize(n){n=+n;if(n<1024)return n+" B";var u=["KB","MB","GB","TB"],i=-1;do{n/=1024;i++}while(n>=1024&&i<3);return n.toFixed(n<10?1:0)+" "+u[i]}
function fmtDate(t){if(!+t)return"";var d=new Date(t*1000);return d.toLocaleDateString()+" "+d.toLocaleTimeString([],{hour:"2-digit",minute:"2-digit"})}
function toast(msg){var t=$("toast");t.textContent=msg;t.style.display="block";clearTimeout(toast.t);toast.t=setTimeout(function(){t.style.display="none"},4500)}
function api(path,opts){return fetch(path,opts).then(function(r){if(!r.ok)return r.text().then(function(m){throw new Error(m.trim()||r.status)});return r.json()})}
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
   var v=el("button","Versioni");v.onclick=function(){versions(full)};a.appendChild(v)}
  r.appendChild(a);l.appendChild(r)})}
function sortItems(a){return a.slice().sort(function(x,y){if(x.kind!==y.kind)return x.kind==="DIR"?-1:1;return x.name.toLowerCase()<y.name.toLowerCase()?-1:1})}
function go(path){mode="dir";cwd=path;$("q").value="";crumbs();
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
api("/api/info").then(function(i){writable=!!i.writable;maxUp=i.max_upload_bytes||0;if(writable)$("upLabel").style.display="";else $("foot").textContent="Sola lettura (imposta WEB_PASSWORD per caricare file).";go("")}).catch(function(e){toast(e.message);go("")});
})();
</script></body></html>`
