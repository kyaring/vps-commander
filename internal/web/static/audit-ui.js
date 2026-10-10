(function(root){
  function escapeHTML(value){
    return String(value??"").replace(/[&<>\"]/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;"}[c]));
  }
  function dateEpoch(value,endOfDay){
    if(!value)return "";
    const d=new Date(value+"T00:00:00");
    if(Number.isNaN(d.getTime()))return "";
    if(endOfDay)d.setHours(23,59,59,999);
    return String(Math.floor(d.getTime()/1000));
  }
  function buildAuditQuery(filters,cursor){
    const p=new URLSearchParams();
    p.set("limit","200");
    if(filters.actor)p.set("actor",filters.actor);
    if(filters.device)p.set("device",filters.device);
    if(filters.action)p.set("action",filters.action);
    if(filters.result)p.set("result",filters.result);
    if(filters.transport)p.set("transport",filters.transport);
    const from=dateEpoch(filters.from,false),to=dateEpoch(filters.to,true);
    if(from)p.set("from",from);
    if(to)p.set("to",to);
    if(cursor>0)p.set("cursor",String(cursor));
    return p;
  }
  function actorLabel(row,tokens){
    if(row.actor_type==="system"||row.transport==="internal")return "系统";
    if(row.transport==="panel"||row.actor_type==="panel")return "面板";
    if(["legacy","legacy-env","unknown(legacy)"].includes(row.actor_name))return "legacy";
    if(tokens.some(t=>t.is_legacy&&t.name===row.actor_name))return "legacy";
    return row.actor_name||"unknown(legacy)";
  }
  function oauthClientLabel(row,clients){
    if(!row.oauth_client_id)return "";
    const name=clients&&clients[row.oauth_client_id];
    return '<div class="audit-via" title="OAuth 客户端 ID：'+escapeHTML(row.oauth_client_id)+'">经 OAuth · '+escapeHTML(name||row.oauth_client_id)+'</div>';
  }
  // The target column was removed because it almost always repeats the device
  // or the command/path. Keep it visible whenever it carries extra information
  // (e.g. scope_denied target=admin, or a device target differing from 设备).
  function auditTargetExtra(row,command){
    const target=String(row.target||"");
    if(!target)return "";
    if(row.target_type==="device"&&target===String(row.device||""))return "";
    if(String(command||"").includes(target))return "";
    return (row.target_type?row.target_type+":":"")+target;
  }
  function renderAuditRow(row,tokens,clients){
    const result=row.result||((Number(row.exit_code)===0)?"ok":"error");
    // Device rows target the same node shown in the 设备 column, so only a
    // non-device target (e.g. a file path) is useful when no command exists.
    const fallback=row.target&&row.target_type!=="device"?row.target:"";
    const command=String(row.command||fallback||"");
    const extra=auditTargetExtra(row,command);
    return '<tr data-audit-id="'+escapeHTML(row.id)+'"><td>'+escapeHTML(new Date(Number(row.timestamp)*1000).toLocaleString())+'</td><td class="audit-actor">'+escapeHTML(actorLabel(row,tokens))+oauthClientLabel(row,clients)+'</td><td>'+escapeHTML(row.transport||"legacy")+'</td><td>'+escapeHTML(row.device||"—")+'</td><td>'+escapeHTML(row.action||"—")+'</td><td class="audit-command"><code class="audit-command-text" tabindex="0" role="button" aria-expanded="false" title="'+escapeHTML(command)+'">'+escapeHTML(command||"—")+'</code>'+(extra?'<div class="audit-via audit-target">目标：'+escapeHTML(extra)+'</div>':'')+'</td><td>'+escapeHTML(row.exit_code??"")+'</td><td>'+escapeHTML(row.duration_ms??0)+'ms</td><td><span class="audit-result '+escapeHTML(result)+'">'+escapeHTML(result)+'</span></td><td>'+escapeHTML(row.caller_ip||"")+'</td></tr>';
  }
  const api={buildAuditQuery,renderAuditRow,actorLabel};
  root.AuditUI=api;
  if(typeof module!=="undefined"&&module.exports)module.exports=api;
})(typeof globalThis!=="undefined"?globalThis:this);
