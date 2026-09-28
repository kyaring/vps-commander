(() => {
  const root = document.querySelector('#content');
  const fmtBytes = n => { if (!Number.isFinite(n)) return '-'; const u=['B','KB','MB','GB','TB']; let i=0,v=n; while(v>=1024&&i<u.length-1){v/=1024;i++;} return `${v.toFixed(i?1:0)} ${u[i]}`; };
  const fmtRate = n => `${fmtBytes(n)}/s`;
  const fmtUp = n => { let s=Math.max(0,Math.floor(n||0)); const d=Math.floor(s/86400); s%=86400; const h=Math.floor(s/3600); s%=3600; const m=Math.floor(s/60); return d?`${d}天${h}小时`:h?`${h}小时${m}分`:`${m}分`; };
  const esc = s => String(s??'-').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  let mode=localStorage.getItem('vpc_probe_view')||'card';
  const setMode=m=>{mode=m;localStorage.setItem('vpc_probe_view',m);render(window.__data||[]);};
  document.querySelector('#cardBtn').onclick=()=>setMode('card'); document.querySelector('#compactBtn').onclick=()=>setMode('compact');
  const row=(d,card)=>{const p=d.profile||{}; return `<div class="${card?'card':'tr'}"><div class="name"><b>${esc(d.name)}</b><span class="${d.status==='online'?'on':'off'}">${d.status==='online'?'在线':'离线'}</span></div><span>Uptime ${fmtUp(p.uptime_seconds)}</span><span>Load ${[p.load_1m,p.load_5m,p.load_15m].map(x=>Number(x||0).toFixed(2)).join(' / ')}</span><span>CPU ${p.cpu_usage_percent??'-'}%</span><span>MEM ${p.mem_percent??'-'}%</span><span>↓ ${fmtRate(p.net_rx_rate)} ↑ ${fmtRate(p.net_tx_rate)}</span><span>流量 ↓ ${fmtBytes(p.net_rx_total)} ↑ ${fmtBytes(p.net_tx_total)}</span><span>Disk ${p.disk_free||'-'} ${p.disk_percent??'-'}%</span></div>`;};
  function render(data){document.querySelector('#cardBtn').classList.toggle('active',mode==='card');document.querySelector('#compactBtn').classList.toggle('active',mode==='compact');root.className=mode;root.innerHTML=mode==='card'?data.map(d=>row(d,true)).join(''):`<div class="table">${data.map(d=>row(d,false)).join('')}</div>`;}
  async function load(){try{const r=await fetch('/probe/summary',{cache:'no-store'});if(!r.ok)throw Error();window.__data=await r.json();render(window.__data);}catch(e){root.innerHTML='<p class="error">探针数据暂时不可用</p>';}}
  load();setInterval(load,5000);
})();
