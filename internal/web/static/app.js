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
  const fetchOpt = Object.assign({ cache: "no-store" }, opt);
  let r=await fetch("/panel/api"+path, fetchOpt);
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

function renderSingleCard(d) {
  const p = d.profile || {};
  const isOnline = d.status === "online";
  const statusText = isOnline ? "在线" : "离线";
  const lastText = d.last_heartbeat ? new Date(d.last_heartbeat*1000).toLocaleString() : "";
  const archOs = (d.arch || d.os) ? `${esc(d.arch||"")}/${esc(d.os||"")} · ` : "";
  const cpuStr = p.cpu_cores ? `${p.cpu_cores}C · ${p.cpu_usage_percent ?? 0}%` : '-';
  const loadStr = p.load_1m !== undefined ? p.load_1m.toFixed(2) : '-';
  const memStr = p.mem_available ? `${p.mem_available} (${p.mem_percent}%)` : (p.mem_percent !== undefined ? `${p.mem_percent}%` : '-');
  const diskStr = p.disk_free ? `${p.disk_free} (${p.disk_percent}%)` : (p.disk_percent !== undefined ? `${p.disk_percent}%` : '-');
  const psiStr = p.io_psi !== undefined ? p.io_psi.toFixed(2) : '-';
  const dockerVal = p.docker ? 'YES' : 'NO';
  const dockerClass = p.docker ? 'yes' : 'no';

  return `
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
  `;
}

async function loadDevices(){
  let all = await api("/devices");
  let ds = all.filter(d => !d.is_local);
  // 严格固定名称字典序排列，杜绝乱跳
  ds.sort((a, b) => String(a.name || "").localeCompare(String(b.name || "")));

  const container = $("#devices");
  const currentCardElements = container.querySelectorAll(".device-card[data-node]");
  const currentNodes = Array.from(currentCardElements).map(el => el.getAttribute("data-node"));
  const newNodes = ds.map(d => d.name);

  // 如果节点列表结构未变，采用就地局部更新，卡片物理位置丝毫不动
  const sameOrder = currentNodes.length === newNodes.length && currentNodes.every((n, i) => n === newNodes[i]);
  if (sameOrder) {
    ds.forEach(d => {
      const card = container.querySelector(`.device-card[data-node="${CSS.escape(d.name)}"]`);
      if (card) {
        const isOnline = d.status === "online";
        card.className = `device-card ${isOnline ? "online" : "offline"}`;
        card.innerHTML = renderSingleCard(d);
      }
    });
  } else {
    // 首次渲染或增删节点时渲染
    container.innerHTML = ds.map(d => {
      const isOnline = d.status === "online";
      return `<div class="device-card ${isOnline ? "online" : "offline"}" data-node="${esc(d.name)}">${renderSingleCard(d)}</div>`;
    }).join("");
  }

  // 保持控制台 select 选项
  const select = $("#device");
  const selectedVal = select.value;
  select.innerHTML = ds.map(d => `<option value="${esc(d.name)}" ${d.status==="offline" ? "disabled" : ""} ${d.name===selectedVal ? "selected" : ""}>${esc(d.name)}${d.status==="offline" ? " [离线]" : ""}</option>`).join("");
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

let mcpEditingId="";
function mcpFieldError(id,message){
  const el=$("#"+id); el.classList.add("mcp-field-error"); el.title=message;
  const status=$("#mcpStatus"); status.textContent=message; status.style.color="#e05252";
  el.focus();
}
function mcpClearErrors(){
  ["mcpId","mcpName","mcpCommand","mcpUrl","mcpArgs","mcpHeaders","mcpEnv"].forEach(id=>{const el=$("#"+id);if(el){el.classList.remove("mcp-field-error");el.removeAttribute("title")}});
  $("#mcpStatus").textContent=""; $("#mcpStatus").style.color="";
}
function parseMCPField(id,label,kind){
  const el=$("#"+id); let value;
  try{value=JSON.parse(el.value||"");}catch(e){mcpFieldError(id,label+" 必须是合法的 JSON "+kind+"，请检查格式。");throw e}
  if(kind==="数组" && !Array.isArray(value)){mcpFieldError(id,label+" 必须是合法的 JSON 数组，如 []。");throw Error("invalid "+id)}
  if(kind==="对象" && (value===null||Array.isArray(value)||typeof value!=="object")){mcpFieldError(id,label+" 必须是合法的 JSON 对象，如 {}。");throw Error("invalid "+id)}
  return value;
}
function showMCPToast(message){
  let t=$("#mcpToast"); if(!t){t=document.createElement("div");t.id="mcpToast";t.style.cssText="position:fixed;right:18px;top:18px;z-index:9999;padding:10px 14px;border:1px solid var(--border);border-radius:8px;background:var(--bg-sub);box-shadow:0 8px 30px rgba(0,0,0,.25)";document.body.appendChild(t)}
  t.textContent=message; t.hidden=false; clearTimeout(t._timer); t._timer=setTimeout(()=>t.hidden=true,2600);
}
async function loadMCPServices(){
  const services=await api("/mcp/services");
  const allDevices=await api("/devices");
  const devices=allDevices.filter(d=>!d.is_local);
  $("#mcpTestNode").innerHTML='<option value="local">Hub 本机</option>'+devices.filter(d=>d.status==="online").map(d=>'<option value="'+esc(d.name)+'">'+esc(d.name)+'（在线）</option>').join("");
  $("#mcpNodes").innerHTML=devices.map(d=>`<label><input type="checkbox" value="${esc(d.name)}">${esc(d.name)}</label>`).join("");
  $("#mcpList").innerHTML=services.map(s=>`<div class="mcp-item"><h3>${esc(s.name)} <small>${esc(s.id)}</small></h3><div class="mcp-meta">${esc(s.transport)} · ${s.scope==='custom'?'指定节点':'全部节点'} · ${s.enabled?'启用':'停用'}${s.url?' · '+esc(s.url):s.command?' · '+esc(s.command):''}</div><div class="mcp-actions"><button type="button" onclick="editMCP('${esc(s.id)}')">编辑</button><button type="button" onclick="deleteMCP('${esc(s.id)}')">删除</button></div></div>`).join("")||'<div class="mcp-meta">暂无 MCP 服务配置。</div>';
}
function resetMCPForm(){
  mcpEditingId=""; $("#mcpEditor").hidden=false; $("#mcpId").disabled=false; ["mcpId","mcpName","mcpCommand","mcpUrl"].forEach(id=>$("#"+id).value=""); $("#mcpTransport").value="stdio"; $("#mcpEnabled").value="1"; $("#mcpArgs").value="[]"; $("#mcpHeaders").value="{}"; $("#mcpEnv").value="{}"; $("#mcpScope").value="all"; $("#mcpStatus").textContent=""; $("#mcpTestResult").hidden=true; $("#mcpTestResult").textContent=""; $("#mcpNodes").querySelectorAll("input").forEach(x=>x.checked=false);
}
async function editMCP(id){
  const services=await api("/mcp/services"); const s=services.find(x=>x.id===id); if(!s)return; resetMCPForm(); mcpEditingId=id; $("#mcpId").value=s.id; $("#mcpId").disabled=true; $("#mcpName").value=s.name||""; $("#mcpTransport").value=s.transport; $("#mcpEnabled").value=s.enabled?"1":"0"; $("#mcpCommand").value=s.command||""; $("#mcpUrl").value=s.url||""; $("#mcpArgs").value=s.args_json||"[]"; $("#mcpHeaders").value=s.headers_json||"{}"; $("#mcpEnv").value=s.env_json||"{}"; $("#mcpScope").value=s.scope||"all"; let nodes=[];try{nodes=JSON.parse(s.target_nodes_json||"[]")}catch(e){};$("#mcpNodes").querySelectorAll("input").forEach(x=>x.checked=nodes.includes(x.value));
}
async function deleteMCP(id){if(!confirm(`确认删除 MCP 服务 [${id}]？`))return;try{await api("/mcp/service?id="+encodeURIComponent(id),{method:"DELETE"});await loadMCPServices()}catch(e){alert("删除失败: "+e.message)}}
$("#mcpNew").onclick=resetMCPForm; $("#mcpCancel").onclick=()=>$("#mcpEditor").hidden=true;
$("#mcpSave").onclick=async()=>{
  mcpClearErrors();
  try{
    const transport=$("#mcpTransport").value;
    const body={id:$("#mcpId").value.trim(),name:$("#mcpName").value.trim(),transport,command:$("#mcpCommand").value.trim(),url:$("#mcpUrl").value.trim(),scope:$("#mcpScope").value,enabled:$("#mcpEnabled").value==="1",args:parseMCPField("mcpArgs","Arguments","数组"),headers:parseMCPField("mcpHeaders","Headers","对象"),env:parseMCPField("mcpEnv","环境变量","对象"),target_nodes:[...$("#mcpNodes").querySelectorAll("input:checked")].map(x=>x.value)};
    if(!body.id){mcpFieldError("mcpId","服务标识不能为空。");return}
    if(!body.name){mcpFieldError("mcpName","显示名称不能为空。");return}
    if(transport==="stdio"&&!body.command){mcpFieldError("mcpCommand","stdio 模式必须填写 Command。");return}
    if(transport!=="stdio"&&!body.url){mcpFieldError("mcpUrl","HTTP/SSE 模式必须填写 URL。");return}
    await api("/mcp/services",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});
    showMCPToast("MCP ["+body.id+"] 已保存并同步到在线节点");
    $("#mcpEditor").hidden=true;
    await loadMCPServices();
  }catch(e){if(!$("#mcpStatus").textContent)$("#mcpStatus").textContent="保存失败: "+e.message}
};
$("#mcpTest").onclick=async()=>{
  mcpClearErrors();
  const result=$("#mcpTestResult"); result.hidden=false; result.className="mcp-test-result"; result.textContent="正在测试连通性…";
  try{
    const transport=$("#mcpTransport").value;
    const service={id:$("#mcpId").value.trim(),name:$("#mcpName").value.trim(),transport,command:$("#mcpCommand").value.trim(),url:$("#mcpUrl").value.trim(),args:parseMCPField("mcpArgs","Arguments","数组"),headers:parseMCPField("mcpHeaders","Headers","对象"),env:parseMCPField("mcpEnv","环境变量","对象")};
    if(transport==="stdio"&&!service.command){mcpFieldError("mcpCommand","stdio 模式必须填写 Command。");result.hidden=true;return}
    if(transport!=="stdio"&&!service.url){mcpFieldError("mcpUrl","HTTP/SSE 模式必须填写 URL。");result.hidden=true;return}
    let node=$("#mcpTestNode").value;
    if(node==="auto"||!node){const ds=(await api("/devices")).filter(d=>d.status==="online");node=ds.find(d=>!d.is_local)?.name||"local"}
    const x=await api("/mcp/test",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({device:node,service})});
    if(!x.success)throw Error((x.error&&x.error.message)||"连通失败");
    result.className="mcp-test-result ok";
    result.innerHTML="<strong>✓ 连通成功！</strong> "+esc(x.device)+" · "+esc(x.transport)+" · "+x.duration_ms+"ms · 检测到 "+x.tool_count+" 个可用工具";
    if(x.tool_count){result.innerHTML+='<ul class="mcp-test-tools">'+x.tools.map(t=>"<li><code>"+esc(t.name||"")+"</code>"+(t.description?" — "+esc(t.description):"")+"</li>").join("")+"</ul>"}
  }catch(e){
    result.className="mcp-test-result fail";
    result.innerHTML='<strong>✗ 连通失败</strong><div class="mcp-test-detail">'+esc(e.message)+"</div>";
  }
};
$("#mcpTransport").onchange=()=>{const stdio=$("#mcpTransport").value==="stdio";$("#mcpCommand").parentElement.style.display=stdio?"":"none";$("#mcpArgs").parentElement.style.display=stdio?"":"none";$("#mcpUrl").parentElement.style.display=stdio?"none":""};
loadMCPServices().catch(console.error);

async function provisionDeviceCredential(){
  const result=$("#deviceProvisionResult");
  const status=$("#deviceProvisionStatus");
  const name=($("#provisionDeviceName").value||"").trim();
  if(!name){status.textContent="请输入节点名称。";return}
  try{
    const x=await api("/security/device-credentials/provision",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({device:name})});
    $("#deviceProvisionToken").value=x.token||"";
    $("#deviceProvisionCommand").value=x.install_command||"";
    result.hidden=false;
    status.textContent=name+" 接入凭证已生成。安装时把 Token 填入 Agent Token。";
  }catch(e){status.textContent="生成失败: "+e.message}
}
$("#deviceProvisionNew").onclick=()=>{$("#deviceProvisionEditor").hidden=false;$("#deviceProvisionResult").hidden=true;$("#deviceProvisionStatus").textContent="";$("#provisionDeviceName").focus()};
$("#deviceProvisionCancel").onclick=()=>{$("#deviceProvisionEditor").hidden=true};
$("#deviceProvisionCreate").onclick=()=>provisionDeviceCredential();
$("#deviceProvisionToken").onclick=e=>e.target.select();
$("#deviceProvisionCommand").onclick=e=>e.target.select();

async function loadSecuritySettings(){const x=await api("/security/settings"),nodes=x.nodes||[],effective=x.effective||{},configured=new Map(nodes.map(n=>[n.node,n.mode])),devices=await api("/devices");$("#globalSecurity").value=x.global||"medium";$("#securityNodes").innerHTML=devices.map(d=>{const mode=configured.get(d.name)||"inherit",eff=effective[d.name]||x.global||"medium";return `<tr><td>${esc(d.name)}</td><td><select data-security-node="${esc(d.name)}"><option value="inherit" ${mode==='inherit'?'selected':''}>跟随全局</option><option value="low" ${mode==='low'?'selected':''}>低 · Low</option><option value="medium" ${mode==='medium'?'selected':''}>中 · Medium</option><option value="high" ${mode==='high'?'selected':''}>高 · High</option></select></td><td>${esc(eff)}</td><td><button type="button" onclick="saveNodeSecurity('${esc(d.name)}')">保存</button></td></tr>`}).join("")}
async function saveNodeSecurity(node){const el=document.querySelector(`[data-security-node="${CSS.escape(node)}"]`);if(!el)return;try{await api("/security/settings/update",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({node,mode:el.value})});$("#securityStatus").textContent=`${node} 策略已保存。`;await loadSecuritySettings()}catch(e){$("#securityStatus").textContent="保存失败: "+e.message}}
$("#saveGlobalSecurity").onclick=async()=>{try{await api("/security/settings/update",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({global:$("#globalSecurity").value})});$("#securityStatus").textContent="全局安全策略已保存。";await loadSecuritySettings()}catch(e){$("#securityStatus").textContent="保存失败: "+e.message}};$("#securityRefresh").onclick=()=>loadSecuritySettings().catch(console.error);loadSecuritySettings().catch(console.error);


let webhookEditingId = "";

async function loadWebhooks() {
  const list = await api("/notifications/webhooks");
  const el = $("#webhookList");
  if (!el) return;
  if (!list || list.length === 0) {
    el.innerHTML = '<div class="mcp-meta">暂无 Webhook 配置。</div>';
    return;
  }
  el.innerHTML = list.map(x => `
    <div class="mcp-item">
      <h3>${esc(x.name)} <small>${esc(x.id)}</small></h3>
      <div class="mcp-meta">
        ${esc(x.type)} · ${x.enabled ? '<span style="color:var(--dot-online)">启用</span>' : '<span style="color:var(--text-muted)">停用</span>'}<br>
        <code>${esc(x.url)}</code>
      </div>
      <div class="mcp-actions">
        <button type="button" onclick="editWebhook('${esc(x.id)}')">编辑</button>
        <button type="button" onclick="deleteWebhook('${esc(x.id)}')">删除</button>
      </div>
    </div>
  `).join("");
}

function resetWebhookForm() {
  webhookEditingId = "";
  const editor = $("#webhookEditor");
  if (!editor) return;
  editor.hidden = false;
  $("#webhookId").disabled = false;
  $("#webhookId").value = "";
  $("#webhookName").value = "";
  $("#webhookType").value = "wecom";
  $("#webhookEnabled").value = "1";
  $("#webhookUrl").value = "";
  $("#webhookConfig").value = "{}";
  $("#webhookStatus").textContent = "";
}

async function editWebhook(id) {
  const list = await api("/notifications/webhooks");
  const item = list.find(x => x.id === id);
  if (!item) return;
  resetWebhookForm();
  webhookEditingId = id;
  $("#webhookId").value = item.id;
  $("#webhookId").disabled = true;
  $("#webhookName").value = item.name || "";
  $("#webhookType").value = item.type || "wecom";
  $("#webhookEnabled").value = item.enabled ? "1" : "0";
  $("#webhookUrl").value = item.url || "";
  $("#webhookConfig").value = item.config_json || "{}";
}

async function deleteWebhook(id) {
  if (!confirm(`确认删除 Webhook [${id}]？`)) return;
  try {
    await api("/notifications/webhook/delete?id=" + encodeURIComponent(id), { method: "DELETE" });
    await loadWebhooks();
  } catch (e) {
    alert("删除失败: " + e.message);
  }
}

if ($("#webhookNew")) $("#webhookNew").onclick = resetWebhookForm;
if ($("#webhookCancel")) $("#webhookCancel").onclick = () => {
  const editor = $("#webhookEditor");
  if (editor) editor.hidden = true;
};

if ($("#webhookSave")) $("#webhookSave").onclick = async () => {
  try {
    const id = $("#webhookId").value.trim();
    const name = $("#webhookName").value.trim();
    const url = $("#webhookUrl").value.trim();
    if (!id) throw Error("标识 (ID) 不能为空");
    if (!name) throw Error("名称不能为空");
    if (!url) throw Error("Webhook URL 不能为空");

    let cfg = {};
    try {
      cfg = JSON.parse($("#webhookConfig").value || "{}");
    } catch (e) {
      throw Error("配置 JSON 格式不正确");
    }

    const body = {
      id: id,
      name: name,
      type: $("#webhookType").value,
      url: url,
      config_json: JSON.stringify(cfg),
      enabled: $("#webhookEnabled").value === "1"
    };

    await api("/notifications/webhook/update", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body)
    });

    $("#webhookStatus").textContent = "Webhook [" + id + "] 已保存。";
    const editor = $("#webhookEditor");
    if (editor) editor.hidden = true;
    await loadWebhooks();
  } catch (e) {
    $("#webhookStatus").textContent = "保存失败: " + e.message;
  }
};

if ($("#webhookTest")) $("#webhookTest").onclick = async () => {
  try {
    const id = $("#webhookId").value.trim();
    const name = $("#webhookName").value.trim();
    const url = $("#webhookUrl").value.trim();
    if (!url) throw Error("Webhook URL 不能为空");

    let cfg = {};
    try {
      cfg = JSON.parse($("#webhookConfig").value || "{}");
    } catch (e) {
      throw Error("配置 JSON 格式不正确");
    }

    const body = {
      id: id || "test",
      name: name || "测试目标",
      type: $("#webhookType").value,
      url: url,
      config_json: JSON.stringify(cfg),
      enabled: true
    };

    $("#webhookStatus").textContent = "正在发送测试消息...";
    await api("/notifications/webhook/test", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body)
    });
    $("#webhookStatus").textContent = "✓ 测试消息发送成功！";
  } catch (e) {
    $("#webhookStatus").textContent = "✗ 测试失败: " + e.message;
  }
};

if ($("#notificationRefresh")) $("#notificationRefresh").onclick = () => loadWebhooks().catch(console.error);

// 开启 5 秒平滑实时自动轮询
let devicePollTimer = null;
function startDevicePolling() {
  if (devicePollTimer) clearInterval(devicePollTimer);
  devicePollTimer = setInterval(() => {
    if (!document.hidden) {
      loadDevices().catch(console.error);
    }
  }, 5000);
}
startDevicePolling();

document.addEventListener("visibilitychange", () => {
  if (!document.hidden) {
    loadDevices().catch(console.error);
  }
});
loadWebhooks().catch(console.error);
