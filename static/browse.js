(()=>{const S=ServeIt,t=S.$('#t');
if(t)S.pick(t,{plan:'/api/download',go:(p,st)=>S.zip('/api/download',p,st)});
const q=S.$('#q'),f=S.$('#found'),d=S.$('#dirs'),n=S.$('#none');let tm,ac;
q.addEventListener('input',()=>{clearTimeout(tm);if(ac)ac.abort();const v=q.value.trim();
 if(!v){f.hidden=n.hidden=true;d.hidden=false;return}
 tm=setTimeout(async()=>{ac=new AbortController();
  try{const r=await fetch('/search?q='+encodeURIComponent(v),{signal:ac.signal});if(!r.ok)throw 0;const a=await r.json()||[];
   d.hidden=true;f.hidden=!a.length;n.hidden=!!a.length;n.textContent='Nothing matches.';
   f.tBodies[0].innerHTML=a.map(i=>`<tr><td class="nm"><a dir="auto" href="/${S.rel(i.relPath)}${i.isDir?'/':''}"${i.isDir?'':' download'}>${S.ic(i.isDir?'dir':'file')}${S.esc(i.relPath)}</a></td><td class="num">${i.isDir?'–':S.size(i.size)}</td></tr>`).join('')}
  catch(e){if(e.name!=='AbortError'){d.hidden=true;f.hidden=true;n.hidden=false;n.textContent='Search failed. Try again.'}}},220)});
})();
