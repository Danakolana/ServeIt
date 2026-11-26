(()=>{
const $=(s,r=document)=>r.querySelector(s),$$=(s,r=document)=>[...r.querySelectorAll(s)];
const esc=s=>String(s).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const size=n=>{if(n<1024)return n+' B';const u=['KB','MB','GB','TB'];let i=-1;do{n/=1024;i++}while(n>=1024&&i<3);return n.toFixed(1)+' '+u[i]};
const dur=s=>s<60?s+'s':s<3600?Math.floor(s/60)+'m '+s%60+'s':Math.floor(s/3600)+'h '+Math.floor(s%3600/60)+'m';
const rel=p=>p.split('/').map(encodeURIComponent).join('/');
const ic=n=>`<svg class="ic${n==='dir'?' dir':''}"><use href="#i-${n}"/></svg>`;
const J={'Content-Type':'application/json'};
const S='fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round"';
document.body.insertAdjacentHTML('afterbegin',`<svg style="position:absolute;width:0;height:0" aria-hidden="true"><symbol id="i-dir" viewBox="0 0 24 24"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" fill="currentColor"/></symbol><symbol id="i-file" viewBox="0 0 24 24"><path d="M6 3h8l5 5v13H6z" ${S} stroke-width="1.7"/></symbol><symbol id="i-mark" viewBox="0 0 24 24"><path d="M5 12h14M13 6l6 6-6 6" ${S} stroke-width="2.5"/></symbol><symbol id="i-find" viewBox="0 0 24 24"><circle cx="11" cy="11" r="6.5" ${S} stroke-width="2"/><path d="m16 16 4 4" ${S} stroke-width="2"/></symbol></svg>`);
let tt;
function toast(m,bad){let e=$('#toast');if(!e){e=document.createElement('div');e.id='toast';e.setAttribute('role','status');document.body.append(e)}
 e.textContent=m;e.className=(m?'on ':'')+(bad?'bad':'');clearTimeout(tt);if(m&&!/…$/.test(m))tt=setTimeout(()=>e.className='',4000)}
async function copy(s){
 if(!s)throw new Error('Copy failed');
 if(navigator.clipboard&&window.isSecureContext){try{await navigator.clipboard.writeText(s);return}catch{}}
 const previous=document.activeElement,a=document.createElement('textarea');a.value=s;a.setAttribute('readonly','');a.setAttribute('aria-hidden','true');a.style.cssText='position:fixed;top:0;left:-9999px;width:1px;height:1px;opacity:.01;pointer-events:none';document.body.append(a);a.focus();a.select();a.setSelectionRange(0,s.length);
 let ok=false;try{ok=document.execCommand('copy')}catch{}a.remove();if(previous&&typeof previous.focus==='function')previous.focus();if(!ok)throw new Error('Copy failed')
}
async function zip(url,paths,st){
 st('Preparing…');
 const post=q=>fetch(url+q,{method:'POST',headers:J,body:JSON.stringify({paths})});
 try{
  let h;
  if(window.showSaveFilePicker){try{h=await showSaveFilePicker({suggestedName:'ServeIt.zip'})}catch(e){if(e.name==='AbortError')return st('')}}
  if(h){const r=await post('');if(!r.ok)throw new Error((await r.json()).error||'Download failed');
   st('Downloading…');const w=await h.createWritable();
   await r.body.pipeTo(w).catch(()=>{throw new Error('Download failed. A selected file could not be read.')});return st('Saved')}
  const r=await post('?check=1'),b=await r.json();if(!r.ok)throw new Error(b.error||'Download failed');
  const f=document.createElement('form');f.method='POST';f.action=url;
  paths.forEach(p=>{const i=document.createElement('input');i.type='hidden';i.name='path';i.value=p;f.append(i)});
  document.body.append(f);f.submit();setTimeout(()=>f.remove(),1500);st('Download started')
 }catch(e){st(e.message||'Download failed',1)}
}
function pick(t,o){
 const body=t.tBodies[0],all=$('#all',t),dock=$('#dock'),sum=$('#sum'),go=$('#go');
 let saved=[];try{if(o.key)saved=JSON.parse(sessionStorage.getItem(o.key)||'[]')}catch{}
 const sel=new Set(saved),picks=()=>$$('.pick',t);let tok=0;
 const hs=$$('th[data-k]',t);let k='name',d=1;
 const sort=()=>{[...body.rows].sort((a,b)=>{const A=a.dataset,B=b.dataset;
   if(k==='name'){if(A.dir!==B.dir)return A.dir==='1'?-1:1;return A.name.localeCompare(B.name,undefined,{sensitivity:'base',numeric:true})*d}
   return A[k]!==B[k]?(A[k]-B[k])*d:A.name.localeCompare(B.name)}).forEach(r=>body.append(r));
  hs.forEach(h=>h.setAttribute('aria-sort',h.dataset.k===k?(d>0?'ascending':'descending'):'none'))};
 hs.forEach(h=>$('.sort',h).onclick=()=>{d=k===h.dataset.k?-d:1;k=h.dataset.k;sort()});
 async function upd(){
  const n=++tok;
  picks().forEach(p=>{p.checked=sel.has(p.value);p.closest('tr').classList.toggle('sel',p.checked)});
  try{if(o.key)sessionStorage.setItem(o.key,JSON.stringify([...sel]))}catch{}
  if(all)all.checked=picks().every(p=>p.checked);
  dock.classList.toggle('on',sel.size>0);
  sum.textContent=sel.size+' selected';go.disabled=false;
  if(!sel.size||!o.plan)return;
  const rows=picks().filter(p=>p.checked).map(p=>p.closest('tr')),bytes=rows.reduce((s,r)=>s+ +r.dataset.size,0);
  if(!rows.some(r=>r.dataset.dir==='1')){sum.textContent+=' · '+size(bytes);return}
  sum.textContent+=' · counting…';go.disabled=true;
  try{const r=await fetch(o.plan+'?plan=1',{method:'POST',headers:J,body:JSON.stringify({paths:[...sel]})}),j=await r.json();
   if(n!==tok)return;if(!r.ok)throw new Error(j.error);
   sum.textContent=`${sel.size} selected · ${j.files} files · ${size(j.bytes)}`;go.disabled=false}
  catch(e){if(n===tok){sum.textContent=sel.size+' selected';go.disabled=false;toast(e.message||'Could not measure the selection',1)}}
 }
 t.addEventListener('change',e=>{const c=e.target;
  if(c.id==='all')picks().forEach(p=>c.checked?sel.add(p.value):sel.delete(p.value));
  else if(c.classList.contains('pick'))c.checked?sel.add(c.value):sel.delete(c.value);else return;upd()});
 $('#clr').onclick=()=>{sel.clear();upd()};
 go.onclick=async()=>{go.disabled=true;await o.go([...sel],toast,()=>{sel.clear();upd()});go.disabled=false};
 sort();upd();
}
window.ServeIt={$,$$,esc,size,dur,rel,ic,J,toast,copy,zip,pick};
})();
