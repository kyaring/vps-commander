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
    if(row.transport==="panel"||row.actor_type==="panel")return "面板";
    if(["legacy","legacy-env","unknown(legacy)"].includes(row.actor_name))return "legacy";
    if(tokens.some(t=>t.is_legacy&&t.name===row.actor_name))return "legacy";
    return row.actor_name||"unknown(legacy)";
  }
  function renderAuditRow(row,tokens){
    const target=row.target_type&&row.target?row.target_type+":"+row.target:"—";
    const result=row.result||((Number(row.exit_code)===0)?"ok":"error");
    const command=String(row.command||"");
    return '<tr data-audit-id="'+escapeHTML(row.id)+'"><td>'+escapeHTML(new Date(Number(row.timestamp)*1000).toLocaleString())+'</td><td>'+escapeHTML(actorLabel(row,tokens))+'</td><td>'+escapeHTML(row.transport||"legacy")+'</td><td>'+escapeHTML(row.device||"—")+'</td><td>'+escapeHTML(row.action||"—")+'</td><td class="audit-command"><code class="audit-command-text" tabindex="0" role="button" aria-expanded="false" title="'+escapeHTML(command)+'">'+escapeHTML(command)+'</code></td><td>'+escapeHTML(row.exit_code??"")+'</td><td>'+escapeHTML(row.duration_ms??0)+'ms</td><td><span class="audit-result '+escapeHTML(result)+'">'+escapeHTML(result)+'</span></td><td>'+escapeHTML(target)+'</td><td>'+escapeHTML(row.caller_ip||"")+'</td></tr>';
  }
  const api={buildAuditQuery,renderAuditRow,actorLabel};
  root.AuditUI=api;
  if(typeof module!=="undefined"&&module.exports)module.exports=api;
})(typeof globalThis!=="undefined"?globalThis:this);
