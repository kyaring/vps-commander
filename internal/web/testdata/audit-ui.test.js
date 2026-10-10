'use strict';
const assert = require('node:assert/strict');
const AuditUI = require('../static/audit-ui.js');

const query = AuditUI.buildAuditQuery({
  actor: 'deploy-token', device: 'node-a', action: 'exec', result: 'denied',
  transport: 'mcp', from: '2026-10-01', to: '2026-10-09'
}, 987);
assert.equal(query.get('actor'), 'deploy-token', 'token filter must use the selected named token');
assert.equal(query.get('device'), 'node-a');
assert.equal(query.get('action'), 'exec');
assert.equal(query.get('result'), 'denied');
assert.equal(query.get('transport'), 'mcp');
assert.equal(query.get('cursor'), '987');
assert.ok(Number(query.get('from')) > 0 && Number(query.get('to')) > Number(query.get('from')));

const row = AuditUI.renderAuditRow({
  id: 9, timestamp: 1791500000, actor_name: 'deploy-token', actor_type: 'api_token',
  oauth_client_id: 'client-<test>', transport: 'mcp-sse', device: 'node-a', action: 'exec', command: 'echo <denied>',
  exit_code: 0, duration_ms: 2, result: 'denied', target_type: 'device', target: 'node-a', caller_ip: '127.0.0.1'
}, [{name:'deploy-token',is_legacy:false}], {'client-<test>':'Chat<GPT>'});
for (const text of ['deploy-token','经 OAuth · Chat&lt;GPT&gt;','title="OAuth 客户端 ID：client-&lt;test&gt;"','mcp-sse','node-a','exec','echo &lt;denied&gt;','audit-result denied']) {
  assert.ok(row.includes(text), `audit row should display ${text}`);
}
assert.ok(!row.includes('echo <denied>'), 'command text must be HTML-escaped');
assert.ok(row.includes('title="echo &lt;denied&gt;"'), 'tooltip must contain escaped full command');
assert.ok(row.includes('aria-expanded="false"') && row.includes('audit-command-text'), 'command must be keyboard-focusable and collapsible');
assert.ok(!row.includes('device:node-a'), 'redundant device target column must be gone');
assert.equal((row.match(/<td/g)||[]).length, 10, 'audit row must have 10 cells matching the header');
const plain = AuditUI.renderAuditRow({id:1,timestamp:1,actor_name:'hark',transport:'rest',device:'n',action:'exec',command:'ls',target_type:'device',target:'n'}, [], {});
assert.ok(!plain.includes('audit-via'), 'non-OAuth rows must not show an OAuth line');
const unknown = AuditUI.renderAuditRow({id:2,timestamp:1,actor_name:'x',oauth_client_id:'vpc_client_x',transport:'mcp-http',device:'n',action:'exec',command:'ls'}, [], {});
assert.ok(unknown.includes('经 OAuth · vpc_client_x'), 'unknown client falls back to client id');
const fileRow = AuditUI.renderAuditRow({id:3,timestamp:1,actor_name:'x',transport:'rest',device:'n',action:'file_read',command:'',target_type:'file',target:'/etc/os-release'}, [], {});
assert.ok(fileRow.includes('/etc/os-release'), 'file target shown in command/path when command is empty');
const scopeRow = AuditUI.renderAuditRow({id:4,timestamp:1,actor_name:'x',transport:'rest',device:'',action:'scope_denied',command:'/metrics',target_type:'scope',target:'admin',result:'denied'}, [], {});
assert.ok(scopeRow.includes('目标：scope:admin'), 'target that adds information must stay visible');
const mismatch = AuditUI.renderAuditRow({id:5,timestamp:1,actor_name:'x',transport:'rest',device:'node-a',action:'exec',command:'ls',target_type:'device',target:'node-b'}, [], {});
assert.ok(mismatch.includes('目标：device:node-b'), 'device target differing from device column must be shown');
assert.ok(!plain.includes('目标：') && !fileRow.includes('目标：'), 'redundant targets stay hidden');
assert.equal(AuditUI.actorLabel({actor_name:'legacy-env',actor_type:'api_token'}, []), 'legacy');
assert.equal(AuditUI.actorLabel({actor_name:'panel:admin',actor_type:'panel',transport:'panel'}, []), '面板');
assert.equal(AuditUI.actorLabel({actor_name:'system:notify',actor_type:'system',transport:'internal'}, []), '系统');
console.log('PASS audit-ui token filtering, date/result/transport/cursor query, merged OAuth client into token cell, dropped redundant target column, denied styling, actor aliases, and HTML escaping');
