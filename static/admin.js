(()=>{const S=ServeIt,$=S.$;let list=[],t0=performance.now(),cur='';
const left=s=>Math.max(0,Math.floor(s.RemainingSeconds-(performance.now()-t0)/1000));
const row=(s,i)=>`<article class="share"><div class="share-top"><div class="meta"><b class="badge ${s.Status}" data-i="${i}">${s.Status==='active'?S.dur(left(s))+' left':s.Status}</b><span>${s.PageViews} views</span><span>${s.Downloads} downloads</span></div><div class="acts"><button class="btn sm ghost" data-a="copy" data-i="${i}">Copy</button><button class="btn sm ghost" data-a="qr" data-i="${i}">QR code</button>${s.Status==='active'?`<button class="btn sm ghost bad" data-a="rev" data-i="${i}">Revoke</button>`:''}</div></div><div class="share-url"><span>Guest link</span><a class="url" href="${S.esc(s.URL)}" target="_blank" rel="noreferrer">${S.esc(s.URL)}</a></div><p class="items" dir="auto"><span>Shared items</span>${s.Items.map(f=>S.esc(f.Path)).join(', ')}</p></article>`;
async function load(){
 try{const r=await fetch('/api/admin/shares');if(!r.ok)throw 0;list=await r.json();t0=performance.now();
  $('#shares').innerHTML=list.length?list.map(row).join(''):'<p class="empty">No links yet. Pick files above and create one.</p>'}
 catch{$('#shares').innerHTML='<p class="empty">Could not load links.</p>'}}
const dlg=$('#qr'),qc=$('#qc');
function qr(s){cur=s.URL;$('#qi').src=s.QRDataURL;$('#qu').textContent=s.URL;qc.textContent='Copy link';dlg.showModal()}
qc.onclick=async()=>{try{await S.copy(cur);qc.textContent='Copied'}catch{qc.textContent='Copy failed'}};
$('#qx').onclick=()=>dlg.close();dlg.onclick=e=>{if(e.target===dlg)dlg.close()};
$('#shares').onclick=async e=>{const b=e.target.closest('button');if(!b)return;const s=list[b.dataset.i],a=b.dataset.a;
 if(a==='copy'){try{await S.copy(s.URL);S.toast('Link copied')}catch{S.toast('Copy failed. Select the link and copy it.',1)}}
 if(a==='qr')qr(s);
 if(a==='rev'&&confirm('Revoke this link now? People using it lose access.')){
  const r=await fetch('/api/admin/shares/'+encodeURIComponent(s.ID),{method:'DELETE'});
  if(r.ok){S.toast('Link revoked');load()}else S.toast('Could not revoke the link',1)}};
setInterval(()=>S.$$('.badge.active').forEach(b=>{const n=left(list[b.dataset.i]);b.textContent=n?S.dur(n)+' left':'expired'}),1000);
setInterval(load,15000);load();
const t=$('#t');
if(t)S.pick(t,{key:'serveit-share-selection',go:async(paths,toast,clear)=>{
 try{const r=await fetch('/api/admin/shares',{method:'POST',headers:S.J,body:JSON.stringify({items:paths,idleTimeoutMinutes:+$('#idle').value,maxLifetimeMinutes:+$('#max').value})});
  const j=await r.json();if(!r.ok)throw new Error(j.error||'Could not create the link');
  clear();toast('Link created');qr(j);load()}catch(e){toast(e.message,1)}}});
})();
