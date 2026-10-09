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
  transport: 'mcp-sse', device: 'node-a', action: 'exec', command: 'echo <denied>',
  exit_code: 0, duration_ms: 2, result: 'denied', target_type: 'device', target: 'node-a', caller_ip: '127.0.0.1'
}, [{name:'deploy-token',is_legacy:false}]);
for (const text of ['deploy-token','mcp-sse','node-a','exec','echo &lt;denied&gt;','audit-result denied','device:node-a']) {
  assert.ok(row.includes(text), `audit row should display ${text}`);
}
assert.ok(!row.includes('echo <denied>'), 'command text must be HTML-escaped');
assert.equal(AuditUI.actorLabel({actor_name:'legacy-env',actor_type:'api_token'}, []), 'legacy');
assert.equal(AuditUI.actorLabel({actor_name:'panel:admin',actor_type:'panel',transport:'panel'}, []), '面板');
console.log('PASS audit-ui token filtering, date/result/transport/cursor query, added columns, denied styling, actor aliases, and HTML escaping');
