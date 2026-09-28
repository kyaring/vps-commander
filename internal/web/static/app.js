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
async function loadDevices(){
  let ds=await api("/devices");
  $("#devices").innerHTML=ds.map(d=>'<div class="device '+(d.status==="online"?"":"offline")+'"><b><i class="dot"></i>'+esc(d.name)+'</b><div class="status">'+esc(d.status)+' · '+(d.is_local?"local":"remote")+' · '+esc(d.arch||"")+'/'+esc(d.os||"")+'</div></div>').join("");
  $("#device").innerHTML=ds.map(d=>'<option>'+esc(d.name)+'</option>').join("")
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
