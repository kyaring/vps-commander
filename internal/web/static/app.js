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
let auditTokens=[];
let knownTokenDevices=[];
let auditDeviceOptions=[];
let auditCursor=0;
let auditHasMore=false;
let auditActionOptions=new Set(["exec","file_read","file_write","file_list","file_info","file_search","edit_block","mcp_call","mcp_denied","scope_denied","device_denied","security_denied","rate_limited","list_processes","read_multiple_files","create_directory","move_file","token_create","token_update","token_rotate","token_revoke"]);

function refreshAuditFilterOptions(){
  const actor=$("#auditActor");
  if(actor){
    const selected=actor.value;
    const options=['<option value="">全部令牌</option>','<option value="panel">面板</option>','<option value="legacy">legacy</option>'];
    auditTokens.forEach(t=>{if(!t.is_legacy && t.name!=="legacy-env") options.push('<option value="'+esc(t.name)+'">'+esc(t.name)+(t.revoked_at?'（已吊销）':'')+'</option>')});
    actor.innerHTML=options.join("");
    if(Array.from(actor.options||[]).some(o=>o.value===selected)) actor.value=selected;
  }
  const device=$("#auditDevice");
  if(device){
    const selected=device.value;
    device.innerHTML='<option value="">全部设备</option>'+auditDeviceOptions.map(d=>'<option value="'+esc(d)+'">'+esc(d)+'</option>').join("");
    if(Array.from(device.options||[]).some(o=>o.value===selected)) device.value=selected;
  }
  const action=$("#auditAction");
  if(action){
    const selected=action.value;
    action.innerHTML='<option value="">全部动作</option>'+Array.from(auditActionOptions).sort().map(a=>'<option value="'+esc(a)+'">'+esc(a)+'</option>').join("");
    if(Array.from(action.options||[]).some(o=>o.value===selected)) action.value=selected;
  }
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
  knownTokenDevices=Array.from(new Set(all.map(d=>d.name).filter(Boolean))).sort();
  auditDeviceOptions=Array.from(new Set(all.map(d=>d.name).filter(Boolean))).sort();
  refreshAuditFilterOptions();
  let ds = all.filter(d => !d.is_local);
  // 严格固定名称字典序排列，杜绝乱跳
  ds.sort((a, b) => String(a.name || "").localeCompare(String(b.name || "")));

  const summary = $("#deviceSummary");
  const online = ds.filter(d => d.status === "online").length;
  const offline = ds.filter(d => d.status !== "online").map(d => d.name);
  if (summary) summary.innerHTML = `<strong>${ds.length} 台</strong>，<strong>${online} 在线</strong>，<strong class="${offline.length ? "offline-names" : ""}">${offline.length} 离线</strong>${offline.length ? ` · <span class="offline-names">${offline.map(esc).join("、")}</span>` : ""}`;
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

let tokenEditingId = "";
let createdToken = null;
let secretCopied = false;
const tokenTemplates = {
  read:["devices:read","files:read"],
  ops:["devices:read","files:read","exec","files:write","process","mcp:call"],
  all:["devices:read","files:read","exec","files:write","process","mcp:call","admin"]
};
function selectedTokenScopes(){return [...document.querySelectorAll("[data-token-scope]:checked")].map(x=>x.dataset.tokenScope)}
function setTokenScopes(scopes){document.querySelectorAll("[data-token-scope]").forEach(x=>x.checked=scopes.includes(x.dataset.tokenScope))}
function setTokenTemplate(template, confirmAdmin=true){
  if(template==="all" && confirmAdmin && !confirm("全部权限将包含 admin，可管理令牌、安全策略、MCP 和通知。确定要启用高权限模板吗？")){ $("#tokenTemplate").value="custom"; return false; }
  if(tokenTemplates[template])setTokenScopes(tokenTemplates[template]);
  return true;
}
$("#tokenTemplate").addEventListener("change",()=>setTokenTemplate($("#tokenTemplate").value));
document.querySelectorAll("[data-token-scope]").forEach(x=>x.addEventListener("change",()=>{if($("#tokenTemplate").value!=="custom")$("#tokenTemplate").value="custom"}));
$("[data-token-scope=admin]").addEventListener("change",event=>{if(event.target.checked&&!confirm("admin 是高危权限，允许管理令牌、安全策略、MCP 和通知。确认启用？")){event.target.checked=false;} });
function resetTokenForm(){
  tokenEditingId=""; $("#tokenName").value=""; $("#tokenNote").value=""; $("#tokenRate").value="10"; $("#tokenSessions").value="10"; $("#tokenTemplate").value="read"; setTokenScopes(tokenTemplates.read); $("#tokenCreate").textContent="创建命名令牌"; $("#tokenCancelEdit").hidden=true; $("#tokenFormStatus").textContent="";
  $("#tokenDevices").querySelectorAll("input").forEach(x=>x.checked=false);
}
function renderTokenDevices(devices=null){
  const selected=new Set(devices||[]); const box=$("#tokenDevices");
  const old=devices===null?Array.from(box.querySelectorAll("input:checked")).map(x=>x.value):[];
  const names=knownTokenDevices.length?knownTokenDevices:Array.from(auditDeviceOptions);
  box.innerHTML=names.map(name=>`<label><input type="checkbox" value="${esc(name)}" ${selected.has(name)||old.includes(name)?"checked":""}>${esc(name)}</label>`).join("")||'<span class="muted">暂无设备；留空表示全部设备。</span>';
}

async function loadOAuthAdmin(){
  const result=await Promise.all([api("/oauth/clients"),api("/oauth/grants")]);
  const clients=result[0]||[],grants=result[1]||[],now=Math.floor(Date.now()/1000);
  $("#oauthClients").innerHTML=clients.map(function(c){
    const disabled=!!c.disabled_at;
    return "<tr><td><strong>"+esc(c.client_name)+"</strong><div class=\"muted\"><code>"+esc(c.client_id)+"</code></div></td><td>"+esc((c.redirect_uris||[]).join("\n"))+"</td><td>"+esc(new Date(Number(c.created_at)*1000).toLocaleString())+"</td><td>"+(disabled?"已停用":"有效")+"</td><td>"+(disabled?"":("<button type=\"button\" data-oauth-client-disable=\""+esc(c.client_id)+"\">停用</button>"))+"</td></tr>";
  }).join("")||"<tr><td colspan=5>暂无 OAuth 客户端</td></tr>";
  $("#oauthGrants").innerHTML=grants.map(function(g){
    const revoked=!!g.revoked_at,expired=Number(g.refresh_expires_at)<=now;
    const status=revoked?"已撤销":(expired?"已过期":"有效");
    const op=!revoked&&!expired?"<button type=\"button\" data-oauth-grant-revoke=\""+esc(g.grant_id)+"\">撤销授权</button>":"";
    return "<tr><td>"+esc(g.client_name)+"<div class=\"muted\">"+esc(g.client_id)+"</div></td><td>"+esc(g.api_token_name)+"</td><td>"+esc((g.scopes||[]).join(", "))+"</td><td><code>"+esc(g.resource)+"</code></td><td>"+esc(new Date(Number(g.created_at)*1000).toLocaleString())+"<div class=\"muted\">到期："+esc(new Date(Number(g.refresh_expires_at)*1000).toLocaleString())+"</div></td><td>"+status+"</td><td>"+op+"</td></tr>";
  }).join("")||"<tr><td colspan=7>暂无授权记录</td></tr>";
}
$("#oauthClientCreate").onclick=async function(){
  const name=$("#oauthClientName").value.trim(),redirect_uris=$("#oauthRedirectURIs").value.split(/\r?\n/).map(function(x){return x.trim()}).filter(Boolean);
  if(!name||!redirect_uris.length){$("#oauthAdminStatus").textContent="请填写客户端名称和至少一个回调 URI。";return}
  if(!confirm("请确认回调 URI 已由你控制，且与客户端配置完全一致。继续创建？"))return;
  const b=$("#oauthClientCreate");b.disabled=true;
  try{
    const x=await api("/oauth/clients",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({name:name,redirect_uris:redirect_uris})});
    $("#oauthCreatedClientID").value=x.client_id;$("#oauthCreatedClientSecret").value=x.client_secret;$("#oauthClientSecretBox").hidden=false;
    $("#oauthClientName").value="";$("#oauthRedirectURIs").value="";$("#oauthAdminStatus").textContent="客户端已创建。Secret 仅显示本次，请先保存。";
    await loadOAuthAdmin();
  }catch(e){$("#oauthAdminStatus").textContent="创建失败："+e.message}
  finally{b.disabled=false}
};
$("#oauthClients").addEventListener("click",async function(e){
  const b=e.target.closest("[data-oauth-client-disable]");if(!b)return;
  const id=b.getAttribute("data-oauth-client-disable");
  if(!confirm("停用该 OAuth 客户端将撤销其所有授权，确定继续？"))return;
  try{await api("/oauth/client/disable?id="+encodeURIComponent(id),{method:"POST"});$("#oauthAdminStatus").textContent="客户端已停用。";await loadOAuthAdmin()}catch(err){$("#oauthAdminStatus").textContent="停用失败："+err.message}
});
$("#oauthGrants").addEventListener("click",async function(e){
  const b=e.target.closest("[data-oauth-grant-revoke]");if(!b)return;
  const id=b.getAttribute("data-oauth-grant-revoke");
  if(!confirm("确认撤销这条 OAuth 授权？现有访问令牌将立即失效。"))return;
  try{await api("/oauth/grant/revoke?id="+encodeURIComponent(id),{method:"POST"});$("#oauthAdminStatus").textContent="授权已撤销。";await loadOAuthAdmin()}catch(err){$("#oauthAdminStatus").textContent="撤销失败："+err.message}
});
$("#oauthRefresh").onclick=function(){loadOAuthAdmin().catch(function(e){$("#oauthAdminStatus").textContent="刷新失败："+e.message})};
$("#oauthHideSecret").onclick=function(){if(!confirm("确认已保存 Client ID 和 Client Secret？隐藏后 Secret 无法再次查看。"))return;$("#oauthCreatedClientSecret").value="";$("#oauthClientSecretBox").hidden=true};

async function loadTokens(){
  const a=await api("/tokens"); auditTokens=a; refreshAuditFilterOptions();
  const wizard=$("#wizardToken"), previous=wizard?.value;
  if(wizard)wizard.innerHTML='<option value="">选择命名令牌</option>'+a.filter(x=>!x.is_legacy).map(x=>`<option value="${esc(x.id)}" ${x.revoked_at?'disabled':''}>${esc(x.name)} · vpc_${esc(x.prefix)}${x.revoked_at?'（已吊销）':''}</option>`).join("");
  if(wizard && a.some(x=>x.id===previous&&!x.revoked_at))wizard.value=previous;
  $("#tokens").innerHTML=a.map(x=>{
    const status=x.revoked_at?"已吊销":"有效";
    const devices=Array.isArray(x.devices)&&x.devices.length?x.devices.join(", "):"全部设备";
    const last=x.last_used_at?new Date(Number(x.last_used_at)*1000).toLocaleString()+` · ${x.last_used_ip||"IP 未知"}`:"从未使用";
    const ops=x.revoked_at?"":`<button type="button" onclick="editToken('${esc(x.id)}')">编辑</button> <button type="button" onclick="revokeToken('${esc(x.id)}')">吊销</button>`;
    return `<tr><td><strong>${esc(x.name)}</strong><div class="muted">${esc(x.note||"")}</div></td><td><code>vpc_${esc(x.prefix)}</code></td><td>${esc((x.scopes||[]).join(", "))}</td><td>${esc(devices)}</td><td>${Number(x.rate_rps)||10} rps / ${Number(x.max_sessions)||10}</td><td>${esc(last)}</td><td>${status}</td><td>${ops}</td></tr>`;
  }).join("");
}
async function editToken(id){
  const x=auditTokens.find(t=>t.id===id);if(!x)return;
  if(!clearCreatedSecret())return;
  tokenEditingId=id;$("#tokenName").value=x.name||"";$("#tokenNote").value=x.note||"";$("#tokenRate").value=x.rate_rps||10;$("#tokenSessions").value=x.max_sessions||10;$("#tokenTemplate").value="custom";setTokenScopes(x.scopes||[]);
  renderTokenDevices(x.devices||[]);$("#tokenCreate").textContent="保存令牌修改";$("#tokenCancelEdit").hidden=false;$("#tokenFormStatus").textContent=`正在编辑：${x.name}`;$("#tokenManagerCard").scrollIntoView({behavior:"smooth",block:"start"});
}
async function revokeToken(id){if(!confirm("确认吊销该令牌？"))return;await api("/tokens/revoke?id="+encodeURIComponent(id),{method:"POST"});await loadTokens()}
$("#tokenRefresh").onclick=async()=>{
  if(!clearCreatedSecret())return;
  const button=$("#tokenRefresh"),oldText=button.textContent;
  button.disabled=true;button.textContent="刷新中…";$("#tokenFormStatus").textContent="正在刷新令牌列表…";
  try{await loadTokens();const now=new Date();$("#tokenFormStatus").textContent="已刷新 "+[now.getHours(),now.getMinutes(),now.getSeconds()].map(v=>String(v).padStart(2,"0")).join(":");}
  catch(e){$("#tokenFormStatus").textContent="刷新失败："+e.message;}
  finally{button.disabled=false;button.textContent=oldText;}
};
$("#tokenCancelEdit").onclick=resetTokenForm;
$("#tokenCreate").onclick=async()=>{
  const name=$("#tokenName").value.trim(),scopes=selectedTokenScopes(),devices=[...$("#tokenDevices").querySelectorAll("input:checked")].map(x=>x.value);
  if(!name){$("#tokenFormStatus").textContent="请输入令牌名称。";return}
  if(!scopes.length){$("#tokenFormStatus").textContent="至少选择一个权限。";return}
  if(scopes.includes("admin")&&!confirm("即将创建或更新包含 admin 的高权限令牌。它可以管理令牌、安全策略、MCP 和通知。确定继续？"))return;
  const rate=Math.max(1,Math.min(10000,Number($("#tokenRate").value)||10)),sessions=Math.max(1,Math.min(1000,Number($("#tokenSessions").value)||10));
  const body={name,note:$("#tokenNote").value.trim(),scopes,devices,rate_rps:rate,max_sessions:sessions};
  try{
    const editingId=tokenEditingId;let newToken=null;
    if(editingId){await api("/tokens/update?id="+encodeURIComponent(editingId),{method:"PATCH",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});$("#tokenFormStatus").textContent="令牌配置已更新。";}
    else {
      if(!clearCreatedSecret())return;
      newToken=await api("/tokens/create",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(body)});
      showCreatedSecret(newToken,name,"manager");
      $("#wizardSecret").value=newToken.token;$("#wizardNewTokenName").value=name;
    }
    resetTokenForm();await loadTokens();
    if(newToken){$("#wizardToken").value=newToken.id;$("#wizardSecret").value=newToken.token;wizardSecretTokenID=newToken.id;wizardGenerate();}
  }catch(e){$("#tokenFormStatus").textContent="保存失败："+e.message}
};

function currentAuditFilters(){
  return {actor:$("#auditActor").value,device:$("#auditDevice").value,action:$("#auditAction").value,result:$("#auditResult").value,transport:$("#auditTransport").value,from:$("#auditFrom").value,to:$("#auditTo").value};
}
async function loadAudits(reset=true){
  if(reset){auditCursor=0;auditHasMore=false;$("#audits").innerHTML="";$("#auditStatus").textContent="加载中…";}
  const params=AuditUI.buildAuditQuery(currentAuditFilters(),reset?0:auditCursor);
  const rows=await api("/audits?"+params.toString());
  rows.forEach(x=>{if(x.action)auditActionOptions.add(x.action);if(x.device&&!auditDeviceOptions.includes(x.device))auditDeviceOptions.push(x.device)});
  refreshAuditFilterOptions();
  $("#audits").insertAdjacentHTML("beforeend",rows.map(x=>AuditUI.renderAuditRow(x,auditTokens)).join(""));
  if(rows.length){auditCursor=Number(rows[rows.length-1].id)||0;}
  auditHasMore=rows.length===200;
  $("#auditLoadMore").disabled=!auditHasMore;
  $("#auditStatus").textContent=rows.length?`已加载 ${$("#audits").rows.length} 条${auditHasMore?'，可继续加载':''}`:"没有匹配的日志";
}

$("#refresh").onclick=loadDevices;
$("#auditRefresh").onclick=()=>loadAudits(true);
$("#auditLoadMore").onclick=()=>{if(auditHasMore)loadAudits(false).catch(e=>$("#auditStatus").textContent=e.message)};
$("#auditExport").onclick=()=>{const params=AuditUI.buildAuditQuery(currentAuditFilters(),0);location.href="/panel/api/audits/export.csv?"+params.toString()};
$("#audits").addEventListener("click",event=>{const code=event.target.closest(".audit-command-text");if(!code)return;const expanded=code.classList.toggle("expanded");code.setAttribute("aria-expanded",String(expanded));});
$("#audits").addEventListener("keydown",event=>{const code=event.target.closest(".audit-command-text");if(!code||!(event.key==="Enter"||event.key===" "))return;event.preventDefault();const expanded=code.classList.toggle("expanded");code.setAttribute("aria-expanded",String(expanded));});
["#auditActor","#auditDevice","#auditAction","#auditResult","#auditTransport","#auditFrom","#auditTo"].forEach(id=>$(id).addEventListener("change",()=>loadAudits(true).catch(e=>$("#auditStatus").textContent=e.message)));
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
let wizardSecretTokenID="";
let wizardConfigGenerated=false;
const wizardPromptText="你是我的私有 VPS 运维助手。执行操作前先调用 list_devices 确认目标设备和状态；只使用当前令牌允许的权限；高风险操作或破坏性操作必须先向用户确认；不要猜测设备名、命令结果或成功状态。";
function wizardSelectedToken(){return auditTokens.find(t=>t.id===$("#wizardToken").value&&!t.revoked_at)}
function wizardGenerate(force=false){
  const selectedToken=wizardSelectedToken();
  if(force&&!selectedToken){$("#wizardTokenStatus").textContent="请先选择或新建一个有效的命名令牌。";return false;}
  if(force)wizardConfigGenerated=true;
  const client=$("#wizardClient").value, secret=$("#wizardSecret").value.trim()||"<在此填入命名令牌密钥>", origin=location.origin;
  $("#wizardGPT").hidden=!wizardConfigGenerated||client!=="gpt";$("#wizardMCP").hidden=!wizardConfigGenerated||client!=="mcp";$("#wizardREST").hidden=!wizardConfigGenerated||client!=="rest";
  $("#wizardSchema").value=origin+"/openapi.json";$("#wizardPrompt").value=wizardPromptText;
  $("#wizardHTTPURL").value=origin+"/mcp";$("#wizardSSEURL").value=origin+"/mcp/sse";
  $("#wizardMCPJSON").value=JSON.stringify({mcpServers:{"vps-commander":{type:"http",url:origin+"/mcp",headers:{Authorization:"Bearer "+secret}},"vps-commander-sse":{type:"sse",url:origin+"/mcp/sse",headers:{Authorization:"Bearer "+secret}}}},null,2);
  $("#wizardCurl").value=`curl -fsS '${origin}/api/v1/devices' -H 'Authorization: Bearer ${secret}'`;
  const selected=wizardSelectedToken();
  $("#wizardTokenStatus").textContent=selected?( $("#wizardSecret").value.trim()?`已选择命名令牌：${selected.name}`:`已选择命名令牌：${selected.name}。请粘贴该令牌的完整密钥；Hub 无法恢复旧密钥。`):"请先选择一个命名令牌；已有令牌需粘贴创建时保存的完整密钥。";
}
$("#wizardGenerate").onclick=()=>wizardGenerate(true);
$("#wizardAuth").addEventListener("change",()=>{$("#wizardAuthHint").textContent=$("#wizardAuth").value==="apikey"?"在 GPT Actions 的 Authentication 中选择 API Key，将 Auth Type 设为 Bearer，再粘贴命名令牌。":"在 GPT Actions 的 Authentication 中选择 Bearer，并粘贴命名令牌。"});
$("#wizardClient").addEventListener("change",wizardGenerate);
$("#wizardToken").addEventListener("change",()=>{if(wizardSecretTokenID!==$("#wizardToken").value){$("#wizardSecret").value="";wizardSecretTokenID="";}const t=wizardSelectedToken();$("#wizardTokenStatus").textContent=t?`已选择：${t.name}（前缀 vpc_${t.prefix}）。请确认输入的是该令牌的完整密钥。`:"请选择一个有效的命名令牌。";wizardGenerate()});
$("#wizardSecret").addEventListener("input",()=>{wizardSecretTokenID=$("#wizardToken").value;wizardGenerate()});
function showCreatedSecret(token,name,surface){
  createdToken={id:token.id,token:token.token,name};secretCopied=false;
  const manager=surface==="manager";
  $("#newTokenBox").hidden=!manager;$("#wizardNewTokenBox").hidden=manager;
  $("#newTokenName").textContent=name;$("#newTokenValue").value=manager?token.token:"";
  $("#wizardCreatedTokenName").textContent=name;$("#wizardNewTokenValue").value=manager?"":token.token;
}
function clearCreatedSecret(){
  if(createdToken&&!secretCopied&&!confirm("密钥尚未复制，刷新后将无法再查看，确定继续？"))return false;
  const oldID=createdToken?.id||"";
  $("#newTokenBox").hidden=true;$("#newTokenValue").value="";$("#newTokenName").textContent="";
  $("#wizardNewTokenBox").hidden=true;$("#wizardNewTokenValue").value="";$("#wizardCreatedTokenName").textContent="";
  if(oldID&&wizardSecretTokenID===oldID){$("#wizardSecret").value="";wizardSecretTokenID="";wizardGenerate();}
  createdToken=null;secretCopied=false;
  return true;
}
$("#newTokenValue").addEventListener("click",event=>event.target.select());
$("#wizardNewTokenValue").addEventListener("click",event=>event.target.select());
$("#hideNewToken").onclick=()=>clearCreatedSecret();
$("#wizardHideNewToken").onclick=()=>clearCreatedSecret();
document.querySelectorAll("[data-copy]").forEach(button=>button.addEventListener("click",async()=>{
  const el=$("#"+button.dataset.copy);if(!el)return;
  const originalText=button.dataset.originalText??button.textContent;button.dataset.originalText=originalText;
  const value="value" in el?el.value:el.textContent;
  let copied=false;
  try{if(!navigator.clipboard?.writeText)throw Error("Clipboard API unavailable");await navigator.clipboard.writeText(value);copied=true;}catch(_){
    try{
      if(typeof el.select==="function"){el.focus();el.select();}
      else{const range=document.createRange();range.selectNodeContents(el);const selection=getSelection();selection.removeAllRanges();selection.addRange(range);}
      copied=document.execCommand("copy");
    }catch(__){copied=false;}
  }
  if(copied){button.textContent="已复制";if(createdToken&&value&&value===createdToken.token&&(el.id==="newTokenValue"||el.id==="wizardNewTokenValue"))secretCopied=true;}
  else{
    if(typeof el.select==="function"){el.focus();el.select();}else{const range=document.createRange();range.selectNodeContents(el);const selection=getSelection();selection.removeAllRanges();selection.addRange(range);}
    const message="复制失败，请按 Ctrl/长按复制（内容已选中）。";
    const status=button.closest("#aiWizardCard")?$("#wizardTokenStatus"):$("#tokenFormStatus");
    if(status)status.textContent=message;
  }
  setTimeout(()=>{button.textContent=originalText;delete button.dataset.originalText;},1500);
}));
$("#wizardCreateToken").onclick=async()=>{
  const name=$("#wizardNewTokenName").value.trim();if(!name){$("#wizardTokenStatus").textContent="请先输入新令牌名称。";return}
  if(!clearCreatedSecret())return;
  const template=$("#wizardTokenTemplate").value,scopes=tokenTemplates[template]||tokenTemplates.read;
  try{const x=await api("/tokens/create",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({name,note:"由 AI 接入向导创建",scopes,rate_rps:10,max_sessions:10})});showCreatedSecret(x,name,"wizard");$("#wizardSecret").value=x.token;wizardSecretTokenID=x.id;await loadTokens();$("#wizardToken").value=x.id;$("#wizardTokenStatus").textContent=`命名令牌已创建（${template==="ops"?"运维":"只读"}）。完整密钥仅显示在当前页面，请妥善保存。`;wizardGenerate();}catch(e){$("#wizardTokenStatus").textContent="创建失败："+e.message}
};
$("#wizardTest").onclick=async()=>{
  const secret=$("#wizardSecret").value.trim(),result=$("#wizardTestResult");
  if(!wizardConfigGenerated){result.textContent="请先完成第 3 步：生成配置。";result.className="test-fail";return}
  if(!wizardSelectedToken()){result.textContent="失败：请先选择有效的命名令牌。";result.className="test-fail";return}
  if(!secret){result.textContent="失败：Hub 不保存完整密钥，请粘贴令牌密钥后重试。";result.className="test-fail";return}
  result.textContent="测试中…";result.className="";
  try{
    const client=$("#wizardClient").value;
    if(client==="mcp"){
      const headers={Authorization:"Bearer "+secret,"Content-Type":"application/json",Accept:"application/json, text/event-stream"};
      const rpc=async(payload,sessionID="")=>{const h={...headers};if(sessionID)h["Mcp-Session-Id"]=sessionID;const r=await fetch(location.origin+"/mcp",{method:"POST",headers:h,body:JSON.stringify(payload),cache:"no-store"});const body=await r.text();if(!r.ok)throw Error(`MCP HTTP ${r.status}: ${body.slice(0,240)}`);if(!body.trim())return null;let data;try{data=JSON.parse(body)}catch(e){throw Error("MCP 返回了非 JSON 响应："+body.slice(0,160))}if(data.error)throw Error(`MCP ${data.error.code}: ${data.error.message}`);return {data,sessionID:r.headers.get("Mcp-Session-Id")||sessionID}};
      const init=await rpc({jsonrpc:"2.0",id:1,method:"initialize",params:{protocolVersion:"2025-03-26",capabilities:{},clientInfo:{name:"vps-commander-panel-wizard",version:"1.0"}}});
      if(!init?.data?.result)throw Error("MCP initialize 未返回 result");
      const sessionID=init.sessionID||"";
      await rpc({jsonrpc:"2.0",method:"notifications/initialized"},sessionID);
      const listed=await rpc({jsonrpc:"2.0",id:2,method:"tools/list",params:{}},sessionID);
      const tools=listed?.data?.result?.tools;if(!Array.isArray(tools))throw Error("MCP tools/list 未返回工具列表");
      result.style.whiteSpace="pre-wrap";result.textContent=`成功：MCP initialize + tools/list 完成，可用工具 ${tools.length} 个。`+(tools.length?"\n"+tools.map(t=>`• ${t.name}${t.description?" — "+t.description:""}`).join("\n"):"\n此令牌当前没有可用工具。");result.className="test-ok";
    }else{
      const r=await fetch(location.origin+"/api/v1/devices",{headers:{Authorization:"Bearer "+secret},cache:"no-store"});const body=await r.text();if(!r.ok)throw Error(`HTTP ${r.status}: ${body.slice(0,240)}`);const data=JSON.parse(body);result.textContent=`成功：list_devices 返回 ${Array.isArray(data)?data.length:(data.devices||[]).length} 台设备。`;result.className="test-ok";
    }
  }catch(e){result.textContent="失败："+e.message;result.className="test-fail"}
};
$("#confirmNamedTokenUse").addEventListener("change",()=>{$("#rotateKey").disabled=!$("#confirmNamedTokenUse").checked});
$("#rotateKey").onclick=async()=>{
  if(!$("#confirmNamedTokenUse").checked){alert("请先确认已切换到命名令牌并验证连通。 ");return}
  if(!confirm("轮换 API Key 会让所有使用 legacy Key 的客户端立刻失效。请先将当前客户端切换到命名令牌。确定已完成切换？"))return;
  if(prompt("二次确认：输入 ROTATE 才会继续。")!=="ROTATE")return;
  try{const x=await api("/key/rotate",{method:"POST"});$("#newKey").textContent="新 API Key（仅本次显示）：\n"+x.api_key;}catch(e){$("#newKey").textContent=e.message}
};
$("#logout").onclick=async()=>{
  await fetch("/panel/logout",{method:"POST"});
  location.href="/panel/login"
};


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
$("#saveGlobalSecurity").onclick=async()=>{try{await api("/security/settings/update",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({global:$("#globalSecurity").value})});$("#securityStatus").textContent="全局安全策略已保存。";await loadSecuritySettings()}catch(e){$("#securityStatus").textContent="保存失败: "+e.message}};$("#securityRefresh").onclick=()=>loadSecuritySettings().catch(console.error);


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
function stopDevicePolling() {
  if (devicePollTimer) clearInterval(devicePollTimer);
  devicePollTimer = null;
}
function startDevicePolling() {
  stopDevicePolling();
  if (document.hidden || !["devices","console"].includes(currentPanelTab)) return;
  devicePollTimer = setInterval(() => loadDevices().catch(console.error), 5000);
}

document.addEventListener("visibilitychange", () => {
  if (document.hidden) {
    stopDevicePolling();
  } else {
    if (["devices","console"].includes(currentPanelTab)) loadDevices().catch(console.error);
    startDevicePolling();
  }
});


const validPanelTabs=["devices","console","access","security","logs"];
let currentPanelTab="";
const loadedPanelTabs=new Set();
async function activatePanelTab(tab, force=false){
  if(!validPanelTabs.includes(tab))tab="devices";
  if(currentPanelTab!==tab||force){currentPanelTab=tab;document.querySelectorAll("[data-tab]").forEach(b=>{const active=b.dataset.tab===tab;b.classList.toggle("active",active);b.setAttribute("aria-selected",String(active));b.tabIndex=active?0:-1});document.querySelectorAll("[data-tab-panel]").forEach(p=>{const active=p.dataset.tabPanel===tab;p.classList.toggle("active",active);p.hidden=!active});}
  if(location.hash!=="#"+tab)history.replaceState(null,"",location.pathname+location.search+"#"+tab);
  if(!force&&loadedPanelTabs.has(tab)){startDevicePolling();return}
  try{
    if(tab==="devices"||tab==="console"){await loadDevices();if(tab==="devices")renderTokenDevices();}
    if(tab==="access"){await loadDevices();await loadTokens();renderTokenDevices();await loadMCPServices();await loadOAuthAdmin();wizardGenerate();}
    if(tab==="security"){await Promise.all([loadSecuritySettings(),loadWebhooks()]);}
    if(tab==="logs"){await loadTokens();await loadAudits(true);}
    loadedPanelTabs.add(tab);
  }catch(e){console.error(e);const status=$("#tokenFormStatus");if(status&&tab==="access")status.textContent="加载失败："+e.message;const auditStatus=$("#auditStatus");if(auditStatus&&tab==="logs")auditStatus.textContent="加载失败："+e.message;}
  startDevicePolling();
}
document.querySelectorAll("[data-tab]").forEach(button=>button.addEventListener("click",()=>activatePanelTab(button.dataset.tab)));
document.addEventListener("keydown",event=>{if(!event.target.closest(".panel-tabs"))return;if(event.key!=="ArrowLeft"&&event.key!=="ArrowRight")return;event.preventDefault();const i=validPanelTabs.indexOf(currentPanelTab),next=(i+(event.key==="ArrowRight"?1:validPanelTabs.length-1))%validPanelTabs.length;activatePanelTab(validPanelTabs[next]);document.querySelector(`[data-tab="${validPanelTabs[next]}"]`)?.focus()});
window.addEventListener("hashchange",()=>activatePanelTab(location.hash.slice(1)||"devices"));
const deviceDetails=$("#deviceBoardDetails"),deviceToggle=$("#deviceBoardToggle");
function setDeviceBoardExpanded(expanded){deviceDetails.hidden=!expanded;deviceToggle.setAttribute("aria-expanded",String(expanded));deviceToggle.textContent=expanded?"收起列表":"展开列表";try{localStorage.setItem("vpc_device_board_expanded",expanded?"1":"0")}catch(e){}}
let boardExpanded=false;try{boardExpanded=localStorage.getItem("vpc_device_board_expanded")==="1"}catch(e){}
setDeviceBoardExpanded(boardExpanded);deviceToggle.addEventListener("click",()=>setDeviceBoardExpanded(deviceDetails.hidden));
setTokenTemplate("read",false);
$("#addDeviceTop").addEventListener("click",()=>{activatePanelTab("devices");$("#deviceProvisionNew").click();setTimeout(()=>$("#provisionDeviceName").focus(),0)});
activatePanelTab(location.hash.slice(1)||"devices",true);
