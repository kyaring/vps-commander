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
        page_command("Page.navigate", {"url": base + "/panel"})
        deadline = time.time() + 15
        while time.time() < deadline:
            state = evaluate("({path:location.pathname,rows:document.querySelectorAll('#audits tr').length})")
            if state.get("path") == "/panel" and state.get("rows", 0) >= 2: break
            time.sleep(.2)
        assert state.get("path") == "/panel" and state.get("rows", 0) >= 2, f"real panel did not render seeded rows: {state}"
        assert evaluate("[...document.querySelectorAll('#audits tr')].some(r=>r.textContent.includes('scope_denied')&&r.textContent.includes('denied'))"), "denied row is missing"
        assert evaluate("[...document.querySelectorAll('#audits tr')].some(r=>r.querySelector('.audit-command-text')?.title.length===512)"), "512-character tooltip is missing"
        for width in (1280, 390, 320):
            page_command("Emulation.setDeviceMetricsOverride", {"width": width, "height": 1000,
                         "deviceScaleFactor": 1, "mobile": width <= 450})
            time.sleep(.25)
            metrics = evaluate("""(() => { const card=document.querySelector('.audit-card'),wrap=document.querySelector('.audit-tablewrap'),table=document.querySelector('.audit-table'),filters=document.querySelector('.audit-filters'),rows=[...document.querySelectorAll('#audits tr')],longRow=rows.find(r=>r.querySelector('.audit-command-text')?.title.length===512),normalRow=rows.find(r=>r!==longRow);return {viewport:innerWidth,documentWidth:document.documentElement.scrollWidth,cardWidth:card.getBoundingClientRect().width,wrapWidth:wrap.clientWidth,wrapScrollWidth:wrap.scrollWidth,tableWidth:table.scrollWidth,filters:[...filters.children].map(e=>({l:e.getBoundingClientRect().left,r:e.getBoundingClientRect().right,t:e.getBoundingClientRect().top,b:e.getBoundingClientRect().bottom})),longHeight:longRow?.getBoundingClientRect().height,normalHeight:normalRow?.getBoundingClientRect().height};})()""")
            assert metrics["viewport"] == width, f"viewport mismatch: {metrics}"
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
