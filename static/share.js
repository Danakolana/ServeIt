(()=>{const S=ServeIt,t=S.$('#t'),L=S.$('#left');
let rem=+L.dataset.left,hard=+L.dataset.hard,t0=performance.now();const idle=+L.dataset.idle*60;
const tick=()=>{const s=Math.floor(rem-(performance.now()-t0)/1000);L.textContent=s>0?'Open for '+S.dur(s):'This link has expired'};
const touch=()=>{hard-=(performance.now()-t0)/1000;rem=Math.min(idle,hard);t0=performance.now();tick()};
tick();setInterval(tick,1000);
if(t){S.pick(t,{plan:t.dataset.dl,go:async(p,st)=>{await S.zip(t.dataset.dl,p,st);touch()}});
 t.addEventListener('click',e=>{if(e.target.closest('a[download]'))touch()})}
})();
