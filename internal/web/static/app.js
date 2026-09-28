const $=s=>document.querySelector(s);
(function(){
  const b=$("#themeToggle");
  if(!b)return;
  b.onclick=()=>{
    const current=document.documentElement.dataset.theme||"dark";
    const next=current==="light"?"dark":"light";
    document.documentElement.dataset.theme=next;
    localStorage.setItem("vpc_theme",next);
  };
})();

async function api(path,opt={}){
  let r=await fetch("/panel/api"+path,opt);
  if(r.status===401){location.href="/panel/login";throw Error("unauthorized")}
  if(!r.ok)throw Error(await r.text());
  return r.json()
}

function esc(v){
  return String(v??"").replace(/[&<>"]/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;"}[c]))
}

async function removeDevice(name){
  if(!confirm(`确认注销并踢出节点 [${name}] 吗？\n注销后该节点将被断开并禁止再次连入。`)) return;
  try{
    await api("/devices?name="+encodeURIComponent(name), {method: "DELETE"});
    await loadDevices();
  }catch(e){
    alert("注销失败: "+e.message);
  }
}

async function loadDevices(){
  let all=await api("/devices");
  let ds=all.filter(d=>!d.is_local);
  $("#devices").innerHTML=ds.map(d=>{
    const p = d.profile || {};
    const role = p.role || "APPLICATION";
    const isOnline = d.status === "online";
    const statusText = isOnline ? "在线" : "离线";\n    const lastText = d.last_heartbeat ? new Date(d.last_heartbeat*1000).toLocaleString() : "";
    const archOs = (d.arch || d.os) ? `${esc(d.arch||"")}/${esc(d.os||"")} · ` : "";
    const cpuStr = p.cpu_cores ? `${p.cpu_cores}C · ${p.cpu_usage_percent ?? 0}%` : '-';
    const loadStr = p.load_1m !== undefined ? p.load_1m.toFixed(2) : '-';
    const memStr = p.mem_available ? `${p.mem_available} (${p.mem_percent}%)` : (p.mem_percent !== undefined ? `${p.mem_percent}%` : '-');
    const diskStr = p.disk_free ? `${p.disk_free} (${p.disk_percent}%)` : (p.disk_percent !== undefined ? `${p.disk_percent}%` : '-');
    const psiStr = p.io_psi !== undefined ? p.io_psi.toFixed(2) : '-';
    const dockerVal = p.docker ? 'YES' : 'NO';
    const dockerClass = p.docker ? 'yes' : 'no';

    return `
      <div class="device-card ${isOnline ? "online" : "offline"}">
        <div class="v1-head">
          <div class="v1-row1">
            <div class="v1-title">
              <span class="dot"></span>
              <strong class="v1-name" title="${esc(d.name)}">${esc(d.name)}</strong>
            </div>
            <button class="btn-revoke" onclick="removeDevice('${esc(d.name)}')">注销</button>
          </div>
          <div class="v1-row2">${archOs}${statusText}${isOnline ? "" : (lastText ? " · 最后活跃: "+lastText : "")}</div>
        </div>
        <div class="profile-grid">
          <div class="p-item"><span class="p-label">CPU</span><span class="p-val">${cpuStr}</span></div>
          <div class="p-item"><span class="p-label">LOAD</span><span class="p-val">${loadStr}</span></div>
          <div class="p-item"><span class="p-label">MEM</span><span class="p-val">${memStr}</span></div>
          <div class="p-item"><span class="p-label">DISK</span><span class="p-val">${diskStr}</span></div>
          <div class="p-item"><span class="p-label">IO PSI</span><span class="p-val">${psiStr}</span></div>
          <div class="p-item"><span class="p-label">DOCKER</span><span class="p-val badge-docker-text ${dockerClass}">${dockerVal}</span></div>
        </div>
      </div>
    `;
  }).join("");

  $("#device").innerHTML=ds.map(d=>`<option value="${esc(d.name)}" ${d.status==="offline" ? "disabled" : ""}>${esc(d.name)}${d.status==="offline" ? " [离线]" : ""}</option>`).join("");
}

async function loadAudits(){
  let a=await api("/audits?limit=100");
  $("#audits").innerHTML=a.map(x=>'<tr><td>'+new Date(x.timestamp*1000).toLocaleString()+'</td><td>'+esc(x.device)+'</td><td>'+esc(x.action)+'</td><td><code>'+esc(x.command)+'</code></td><td>'+x.exit_code+'</td><td>'+x.duration_ms+'ms</td><td>'+esc(x.caller_ip)+'</td></tr>').join("")
}

$("#refresh").onclick=loadDevices;
$("#auditRefresh").onclick=loadAudits;
$("#run").onclick=async()=>{
  let o=$("#output");
  o.textContent="执行中…";
  try{
    let x=await api("/exec",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({device:$("#device").value,command:$("#command").value,timeout:30})});
    o.textContent=`exit_code: ${x.exit_code}\n\nstdout:\n${x.stdout}\n\nstderr:\n${x.stderr}\nduration: ${x.duration_ms}ms`
  }catch(e){
    o.textContent=e.message
  }
};
$("#copySchema").onclick=()=>navigator.clipboard.writeText(location.origin+"/openapi.json");
$("#copyPrompt").onclick=()=>navigator.clipboard.writeText("你是我的私有 VPS 运维助手。执行操作前确认目标设备；优先使用 list_devices 确认节点状态；高风险操作先向用户确认。");
$("#rotateKey").onclick=async()=>{
  if(!confirm("轮换后旧 API Key 会立即失效，确认继续？"))return;
  try{
    let x=await api("/key/rotate",{method:"POST"});
    $("#newKey").textContent="新 API Key（仅本次显示）：\n"+x.api_key
  }catch(e){
    $("#newKey").textContent=e.message
  }
};
$("#logout").onclick=async()=>{
  await fetch("/panel/logout",{method:"POST"});
  location.href="/panel/login"
};
loadDevices().then(loadAudits).catch(console.error);
