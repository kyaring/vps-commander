#!/usr/bin/env python3
"""Launch a temporary real Hub and test its authenticated audit panel with Chromium CDP."""
import json, os, socket, sqlite3, subprocess, tempfile, time, urllib.request
from pathlib import Path
import websocket

REPO = Path(__file__).resolve().parents[3]
PASSWORD = "layout-test-panel-password"

with tempfile.TemporaryDirectory(prefix="vpc-real-audit-layout-") as temp:
    temp = Path(temp)
    binary, db, profile = temp / "hub", temp / "hub.db", temp / "chromium"
    subprocess.run(["go", "build", "-o", str(binary), "./cmd/hub"], cwd=REPO, check=True)
    sock = socket.socket(); sock.bind(("127.0.0.1", 0)); port = sock.getsockname()[1]; sock.close()
    base = f"http://127.0.0.1:{port}"
    env = os.environ.copy()
    env.update({"VPS_COMMANDER_WEB_PASSWORD": PASSWORD, "VPS_COMMANDER_API_KEY": "layout-test-api-key",
                "VPS_COMMANDER_LEGACY_KEY": "off", "VPS_COMMANDER_CLUSTER_SECRET": "layout-test-cluster-secret"})
    log = open(temp / "hub.log", "w+")
    hub = subprocess.Popen([str(binary), "-addr", f"127.0.0.1:{port}", "-db", str(db)],
                           cwd=REPO, env=env, stdout=log, stderr=subprocess.STDOUT)
    chrome = None
    try:
        deadline = time.time() + 30
        while time.time() < deadline:
            if hub.poll() is not None: raise AssertionError("temporary Hub exited")
            try:
                with urllib.request.urlopen(base + "/healthz", timeout=1): break
            except Exception: time.sleep(.1)
        else: raise AssertionError("temporary Hub did not start")
        long_command = ("sudo /usr/bin/env " + "verylongsegment_" * 50 + " <audit-command>")[:512]
        conn = sqlite3.connect(db, timeout=5); conn.execute("PRAGMA busy_timeout=5000")
        rows = [
            (int(time.time()), "127.0.0.1", "layout-node", "exec", long_command, 0, 1, "", "api_token", "named-layout", "named-layout-token", "mcp-http", "device", "layout-node", "ok", "", ""),
            (int(time.time()) + 1, "127.0.0.1", "layout-node", "scope_denied", "write /tmp/protected", 403, 0, "permission denied", "api_token", "named-layout", "named-layout-token", "rest", "device", "layout-node", "denied", "", ""),
        ]
        conn.executemany("INSERT INTO audit_logs(timestamp,caller_ip,device,action,command,exit_code,duration_ms,error_msg,actor_type,actor_id,actor_name,transport,target_type,target,result,request_id,session_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", rows)
        conn.commit(); conn.close()
        chrome = subprocess.Popen(["/usr/bin/chromium", "--headless", "--no-sandbox", "--disable-gpu",
                                   "--disable-dev-shm-usage", "--remote-allow-origins=*", "--remote-debugging-port=0",
                                   f"--user-data-dir={profile}", "about:blank"],
                                  stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        port_file = profile / "DevToolsActivePort"; deadline = time.time() + 15
        while time.time() < deadline and not port_file.exists(): time.sleep(.05)
        assert port_file.exists(), "Chromium did not expose CDP"
        cdp_port = int(port_file.read_text().splitlines()[0]); cdp = f"http://127.0.0.1:{cdp_port}"
        with urllib.request.urlopen(cdp + "/json/version", timeout=2) as response:
            browser_ws = json.load(response)["webSocketDebuggerUrl"]
        browser = websocket.create_connection(browser_ws, timeout=8, origin=cdp)
        browser_seq = 0
        def browser_command(method, params=None):
            nonlocal_browser = None
            global browser_seq
            browser_seq += 1; current = browser_seq
            browser.send(json.dumps({"id": current, "method": method, "params": params or {}}))
            while True:
                result = json.loads(browser.recv())
                if result.get("id") == current:
                    if "error" in result: raise RuntimeError(result["error"])
                    return result.get("result", {})
        target_id = browser_command("Target.createTarget", {"url": base + "/panel/login"})["targetId"]
        deadline = time.time() + 8; target = None
        while time.time() < deadline:
            with urllib.request.urlopen(cdp + "/json/list", timeout=2) as response: targets = json.load(response)
            target = next((item for item in targets if item.get("id") == target_id), None)
            if target: break
            time.sleep(.05)
        assert target, "could not find real Hub page"
        page = websocket.create_connection(target["webSocketDebuggerUrl"], timeout=8, origin=cdp)
        page_seq = 0
        def page_command(method, params=None):
            nonlocal_page = None
            global page_seq
            page_seq += 1; current = page_seq
            page.send(json.dumps({"id": current, "method": method, "params": params or {}}))
            while True:
                result = json.loads(page.recv())
                if result.get("id") == current:
                    if "error" in result: raise RuntimeError(result["error"])
                    return result.get("result", {})
        def evaluate(expression, await_promise=False):
            result = page_command("Runtime.evaluate", {"expression": expression, "returnByValue": True,
                                                        "awaitPromise": await_promise, "userGesture": True})["result"]
            if "exceptionDetails" in result: raise AssertionError(result["exceptionDetails"])
            return result.get("value")
        page_command("Page.enable"); page_command("Runtime.enable"); page_command("Network.enable")
        page_command("Page.navigate", {"url": base + "/panel/login"}); time.sleep(.2)
        login = evaluate("fetch('/panel/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({password:" + json.dumps(PASSWORD) + "})}).then(r=>r.status)", True)
        assert login == 204, f"panel login failed: {login}"
        page_command("Page.navigate", {"url": base + "/panel#logs"})
        deadline = time.time() + 15
        while time.time() < deadline:
            state = evaluate("({path:location.pathname,rows:document.querySelectorAll('#audits tr').length})")
            if state.get("path") == "/panel" and state.get("rows", 0) >= 2: break
            time.sleep(.2)
        assert state.get("path") == "/panel" and state.get("rows", 0) >= 2, f"real panel did not render seeded rows: {state}"
        assert evaluate("location.hash==='#logs' && document.querySelector('[data-tab=logs]').classList.contains('active')"), "direct #logs link did not select logs tab"
        assert evaluate("[...document.querySelectorAll('#audits tr')].some(r=>r.textContent.includes('scope_denied')&&r.textContent.includes('denied'))"), "denied row is missing"
        assert evaluate("[...document.querySelectorAll('#audits tr')].some(r=>r.querySelector('.audit-command-text')?.title.length===512)"), "512-character tooltip is missing"
        for width in (1280, 390, 320):
            page_command("Emulation.setDeviceMetricsOverride", {"width": width, "height": 1000,
                         "deviceScaleFactor": 1, "mobile": width <= 450})
            time.sleep(.25)
            metrics = evaluate("""(() => { const card=document.querySelector('.audit-card'),wrap=document.querySelector('.audit-tablewrap'),table=document.querySelector('.audit-table'),filters=document.querySelector('.audit-filters'),rows=[...document.querySelectorAll('#audits tr')],longRow=rows.find(r=>r.querySelector('.audit-command-text')?.title.length===512),normalRow=rows.find(r=>r!==longRow);return {viewport:innerWidth,documentWidth:document.documentElement.scrollWidth,cardWidth:card.getBoundingClientRect().width,wrapWidth:wrap.clientWidth,wrapScrollWidth:wrap.scrollWidth,tableWidth:table.scrollWidth,filters:[...filters.children].map(e=>({l:e.getBoundingClientRect().left,r:e.getBoundingClientRect().right,t:e.getBoundingClientRect().top,b:e.getBoundingClientRect().bottom})),longHeight:longRow?.getBoundingClientRect().height,normalHeight:normalRow?.getBoundingClientRect().height};})()""")
            assert metrics["viewport"] == width, f"viewport mismatch: {metrics}"
            nav_metrics=evaluate("(()=>{const n=document.querySelector('.panel-tabs');return {width:n.clientWidth,scroll:n.scrollWidth,nowrap:[...n.children].every(b=>getComputedStyle(b).whiteSpace==='nowrap')}})()")
            if width <= 390: assert nav_metrics["scroll"] > nav_metrics["width"] and nav_metrics["nowrap"], f"narrow tabs must scroll without wrapping: {nav_metrics}"
            assert metrics["documentWidth"] <= width, f"document overflows at {width}px: {metrics}"
            assert metrics["cardWidth"] <= width, f"card overflows at {width}px: {metrics}"
            assert metrics["wrapScrollWidth"] > metrics["wrapWidth"], f"table cannot scroll at {width}px: {metrics}"
            children = metrics["filters"]
            for i, a in enumerate(children):
                for b in children[i+1:]:
                    overlap = min(a["r"],b["r"])-max(a["l"],b["l"])>1 and min(a["b"],b["b"])-max(a["t"],b["t"])>1
                    assert not overlap, f"filters overlap at {width}px: {metrics}"
            assert metrics["normalHeight"] and metrics["longHeight"] <= metrics["normalHeight"] * 3.2, f"collapsed command too tall: {metrics}"
            if width == 1280:
                before = evaluate("[...document.querySelectorAll('.audit-command-text')].find(e=>e.title.length===512).getBoundingClientRect().height")
                expanded = evaluate("(()=>{const e=[...document.querySelectorAll('.audit-command-text')].find(e=>e.title.length===512);e.click();return {height:e.getBoundingClientRect().height,text:e.textContent.length,expanded:e.getAttribute('aria-expanded')}})()")
                assert expanded["expanded"] == "true" and expanded["text"] == 512 and expanded["height"] > before, f"expand failed: {expanded}"
                evaluate("[...document.querySelectorAll('.audit-command-text')].find(e=>e.title.length===512).click()")
            print(f"PASS real Hub audit layout {width}px: document={metrics['documentWidth']}, table={metrics['tableWidth']}, wrap={metrics['wrapWidth']}/{metrics['wrapScrollWidth']}, command={metrics['longHeight']:.0f}px")
            for tab_name in ("devices","console","access","security","logs"):
                evaluate("document.querySelector('[data-tab="+json.dumps(tab_name)+"]').click()")
                time.sleep(.2)
                panel_metrics=evaluate("(()=>({doc:document.documentElement.scrollWidth,viewport:innerWidth,active:document.querySelector('[data-tab].active')?.dataset.tab,tabScroll:document.querySelector('.panel-tabs').scrollWidth,tabWidth:document.querySelector('.panel-tabs').clientWidth}))()")
                assert panel_metrics["doc"]<=width and panel_metrics["active"]==tab_name, f"tab {tab_name} overflows or did not activate at {width}px: {panel_metrics}"
            evaluate("document.querySelector('[data-tab=logs]').click()")
        # Verify tab switching and persistence through reload.
        evaluate("document.querySelector('[data-tab=access]').click()")
        assert evaluate("location.hash==='#access' && !document.querySelector('[data-tab-panel=access]').hidden"), "access tab did not switch"
        evaluate("document.querySelector('[data-tab=console]').click()")
        assert evaluate("location.hash==='#console' && !document.querySelector('[data-tab-panel=console]').hidden"), "console tab did not switch"
        page_command("Page.reload", {})
        time.sleep(.4)
        deadline = time.time() + 12
        while time.time() < deadline:
            try:
                if evaluate("location.hash==='#console' && document.querySelector('[data-tab=console]').classList.contains('active')"): break
            except Exception: pass
            time.sleep(.1)
        assert evaluate("location.hash==='#console' && document.querySelector('[data-tab=console]').classList.contains('active')"), "refresh did not preserve selected tab"
        evaluate("document.querySelector('[data-tab=security]').click()")
        assert evaluate("document.querySelector('#rotateKey').disabled && document.querySelector('.warning-text').textContent.includes('legacy Key')"), "legacy key rotation must warn and require named-token confirmation"
        evaluate("document.querySelector('[data-tab=access]').click()")
        deadline = time.time() + 15
        while time.time() < deadline:
            if evaluate("document.querySelector('#tokenDevices input')"): break
            time.sleep(.1)
        # The ops template grants the six non-admin operational scopes.
        evaluate("(()=>{const t=document.querySelector('#tokenTemplate');t.value='ops';t.dispatchEvent(new Event('change',{bubbles:true}))})()")
        scopes = evaluate("[...document.querySelectorAll('[data-token-scope]:checked')].map(x=>x.dataset.tokenScope).sort()")
        assert scopes == ["devices:read","exec","files:read","files:write","mcp:call","process"], f"ops template scopes wrong: {scopes}"
        evaluate("(()=>{window.__confirmCalls=0;window.confirm=()=>{window.__confirmCalls++;return window.__confirmCalls===1};document.querySelector('#tokenName').value='must-not-create-admin';const t=document.querySelector('#tokenTemplate');t.value='all';t.dispatchEvent(new Event('change',{bubbles:true}));document.querySelector('#tokenCreate').click()})()")
        assert evaluate("window.__confirmCalls===2 && document.querySelector('[data-token-scope=admin]').checked"), "admin create did not require two confirmations"
        # Create a real named token through the wizard and test it against the temporary Hub.
        assert evaluate("document.querySelector('#wizardTokenTemplate').value") == "read", "wizard token template must default to read-only"
        evaluate("document.querySelector('#tokenTemplate').value='read';document.querySelector('#tokenTemplate').dispatchEvent(new Event('change',{bubbles:true}));document.querySelector('#wizardNewTokenName').value='layout-wizard-token';document.querySelector('#wizardCreateToken').click()")
        deadline = time.time() + 15
        while time.time() < deadline:
            created = evaluate("({secret:document.querySelector('#wizardSecret').value,id:document.querySelector('#wizardToken').value,selected:document.querySelector('#wizardToken').selectedOptions[0]?.textContent||''})")
            if created.get("secret","").startswith("vpc_") and created.get("id"): break
            time.sleep(.1)
        assert created.get("secret","").startswith("vpc_") and "layout-wizard-token" in created.get("selected",""), f"wizard did not create/select token: {created}"
        evaluate("document.querySelector('#wizardClient').value='mcp';document.querySelector('#wizardClient').dispatchEvent(new Event('change',{bubbles:true}));document.querySelector('#wizardGenerate').click()")
        config = evaluate("JSON.parse(document.querySelector('#wizardMCPJSON').value)")
        assert config["mcpServers"]["vps-commander"]["type"] == "http" and config["mcpServers"]["vps-commander"]["url"].endswith("/mcp"), f"bad Streamable HTTP config: {config}"
        assert config["mcpServers"]["vps-commander-sse"]["type"] == "sse" and config["mcpServers"]["vps-commander-sse"]["url"].endswith("/mcp/sse"), f"bad SSE config: {config}"
        evaluate("document.querySelector('#wizardTest').click()")
        deadline = time.time() + 10
        while time.time() < deadline:
            if evaluate("document.querySelector('#wizardTestResult').classList.contains('test-ok') || document.querySelector('#wizardTestResult').classList.contains('test-fail')"): break
            time.sleep(.1)
        assert evaluate("document.querySelector('#wizardTestResult').classList.contains('test-ok')"), "wizard MCP initialize/tools-list test failed: " + str(evaluate("document.querySelector('#wizardTestResult').textContent"))
        mcp_test_text = evaluate("document.querySelector('#wizardTestResult').textContent")
        assert "initialize + tools/list" in mcp_test_text and "list_devices" in mcp_test_text, f"MCP wizard did not show available tools: {mcp_test_text}"
        token = created["secret"]
        # Verify the wizard can also create an operational token with the requested scopes.
        evaluate("document.querySelector('#wizardTokenTemplate').value='ops';document.querySelector('#wizardNewTokenName').value='layout-wizard-ops-token';document.querySelector('#wizardCreateToken').click()")
        deadline = time.time() + 15
        while time.time() < deadline:
            wizard_scopes = evaluate("Array.from(document.querySelectorAll('#tokens tr')).find(r=>r.textContent.includes('layout-wizard-ops-token'))?.cells[2]?.textContent.split(', ').sort()||[]")
            if wizard_scopes: break
            time.sleep(.1)
        assert sorted(wizard_scopes) == ["devices:read","exec","files:read","files:write","mcp:call","process"], f"wizard ops template scopes wrong: {wizard_scopes}"
        rest = subprocess.run(["curl","-fsS",base+"/api/v1/devices","-H",f"Authorization: Bearer {token}"],capture_output=True,text=True,timeout=8,check=True)
        assert isinstance(json.loads(rest.stdout),list), "curl REST list_devices did not return a JSON array"
        rpc = json.dumps({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"panel-layout-test","version":"1"}}})
        streamable = subprocess.run(["curl","-fsS",base+"/mcp","-H",f"Authorization: Bearer {token}","-H","Content-Type: application/json","-H","Accept: application/json","-d",rpc],capture_output=True,text=True,timeout=8,check=True)
        assert "result" in json.loads(streamable.stdout), f"Streamable HTTP initialize failed: {streamable.stdout[:300]}"
        sse = subprocess.run(["curl","-sS","--max-time","1",base+"/mcp/sse","-H",f"Authorization: Bearer {token}"],capture_output=True,text=True,timeout=4)
        assert "event: endpoint" in sse.stdout and "/mcp/message?sessionId=" in sse.stdout, f"SSE endpoint failed: {sse.stdout[:300]}"
        print("PASS tab switching/hash reload, token templates/admin confirmation, wizard connectivity, generated MCP config, curl REST, Streamable HTTP initialize, and SSE endpoint")
        # Device dashboard defaults to a collapsed summary and persists expansion.
        evaluate("document.querySelector('[data-tab=devices]').click()")
        deadline = time.time() + 10
        while time.time() < deadline:
            summary = evaluate("document.querySelector('#deviceSummary').textContent")
            if "台" in summary: break
            time.sleep(.1)
        assert evaluate("document.querySelector('#deviceBoardDetails').hidden"), "device board should start collapsed"
        evaluate("document.querySelector('#deviceBoardToggle').click()")
        assert evaluate("!document.querySelector('#deviceBoardDetails').hidden && localStorage.getItem('vpc_device_board_expanded')==='1'"), "device board expansion was not persisted"
        page_command("Page.reload", {}); time.sleep(.5)
        deadline=time.time()+10
        while time.time()<deadline:
            try:
                if evaluate("location.hash==='#devices' && !document.querySelector('#deviceBoardDetails').hidden"): break
            except Exception: pass
            time.sleep(.1)
        assert evaluate("location.hash==='#devices' && !document.querySelector('#deviceBoardDetails').hidden"), "device board expansion did not survive refresh"
        # Create an ops token scoped to one real device and verify allowed/denied exec.
        evaluate("document.querySelector('[data-tab=access]').click()")
        deadline = time.time() + 12
        while time.time() < deadline:
            if evaluate("document.querySelector('#tokenDevices input')"): break
            time.sleep(.1)
        device_name = evaluate("document.querySelector('#tokenDevices input').value")
        evaluate("(()=>{document.querySelector('#tokenName').value='layout-ops-token';document.querySelector('#tokenNote').value='temporary end-to-end acceptance';const t=document.querySelector('#tokenTemplate');t.value='ops';t.dispatchEvent(new Event('change',{bubbles:true}));document.querySelectorAll('#tokenDevices input').forEach((e,i)=>e.checked=i===0);document.querySelector('#tokenCreate').click()})()")
        deadline = time.time() + 12
        while time.time() < deadline:
            ops_secret = evaluate("document.querySelector('#wizardSecret').value")
            if ops_secret.startswith("vpc_") and "layout-ops-token" in evaluate("document.querySelector('#wizardToken').selectedOptions[0]?.textContent||''"): break
            time.sleep(.1)
        assert ops_secret.startswith("vpc_"), "ops token was not created from the token form"
        ops_info=evaluate("(()=>{const t=auditTokens.find(t=>t.name==='layout-ops-token');return {devices:t?.devices,scopes:t?.scopes,name:t?.name}})()")
        assert device_name in (ops_info.get("devices") or []), f"ops token whitelist was not saved: selected={device_name} token={ops_info}"
        policy_status=evaluate("fetch('/panel/api/security/settings/update',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({global:'high'})}).then(r=>r.status)",True)
        assert policy_status==200, f"temporary Hub could not enable high security for exec acceptance: {policy_status}"
        allowed_body = json.dumps({"device":device_name,"command":"echo whitelist-ok","timeout":5})
        allowed = subprocess.run(["curl","-sS","-w","\n%{http_code}",base+"/api/v1/exec","-H",f"Authorization: Bearer {ops_secret}","-H","Content-Type: application/json","-d",allowed_body],capture_output=True,text=True,timeout=8,check=False)
        allowed_body_text, allowed_status = allowed.stdout.rsplit("\n",1)
        assert allowed_status=="200", f"exec on whitelisted device failed: HTTP {allowed_status}: {allowed_body_text[:300]} {allowed.stderr[:200]}"
        assert json.loads(allowed_body_text).get("exit_code")==0, f"exec on whitelisted device failed: {allowed_body_text[:300]}"
        denied_body = json.dumps({"device":"device-not-in-whitelist","command":"echo must-not-run","timeout":5})
        denied = subprocess.run(["curl","-sS","-w","\n%{http_code}",base+"/api/v1/exec","-H",f"Authorization: Bearer {ops_secret}","-H","Content-Type: application/json","-d",denied_body],capture_output=True,text=True,timeout=8)
        denied_body_text, denied_status = denied.stdout.rsplit("\n",1)
        assert denied_status=="403", f"exec on unlisted device should be denied: HTTP {denied_status}: {denied_body_text} {denied.stderr}"
        audited = 0; deadline=time.time()+4
        while time.time()<deadline and audited<1:
            conn = sqlite3.connect(db, timeout=5)
            audited = conn.execute("SELECT count(*) FROM audit_logs WHERE action='device_denied' AND device='device-not-in-whitelist'").fetchone()[0]
            conn.close()
            if audited<1: time.sleep(.1)
        if audited < 1:
            conn=sqlite3.connect(db,timeout=5); recent=conn.execute("SELECT action,device,result,error_msg FROM audit_logs ORDER BY id DESC LIMIT 8").fetchall(); conn.close()
            raise AssertionError(f"denied device access was not audited; response={denied_body_text}; recent={recent}")
        evaluate("document.querySelector('#tokenRefresh').click()")
        deadline=time.time()+8
        while time.time()<deadline:
            token_row=evaluate("[...document.querySelectorAll('#tokens tr')].find(r=>r.textContent.includes('layout-ops-token'))?.textContent||''")
            if token_row and "从未使用" not in token_row: break
            time.sleep(.1)
        assert token_row and "从未使用" not in token_row and "127.0.0.1" in token_row, f"recent-use timestamp/IP not rendered: {token_row}"
        token_id=evaluate("auditTokens.find(t=>t.name==='layout-ops-token')?.id")
        evaluate("editToken("+json.dumps(token_id)+")")
        evaluate("document.querySelector('#tokenRate').value='20';document.querySelector('#tokenCreate').click()")
        deadline=time.time()+8
        while time.time()<deadline:
            token_row=evaluate("[...document.querySelectorAll('#tokens tr')].find(r=>r.textContent.includes('layout-ops-token'))?.textContent||''")
            if "20 rps" in token_row: break
            time.sleep(.1)
        assert "20 rps" in token_row, f"editing token limit did not persist through PATCH: {token_row}"
        print("PASS device board collapse persistence, ops-token whitelist, allowed/denied exec audit, recent-use IP/time, and token PATCH editing")

        page.close(); browser.close()
    finally:
        if chrome:
            chrome.terminate()
            try: chrome.wait(timeout=5)
            except subprocess.TimeoutExpired: chrome.kill()
        hub.terminate()
        try: hub.wait(timeout=8)
        except subprocess.TimeoutExpired: hub.kill()
        log.close()
