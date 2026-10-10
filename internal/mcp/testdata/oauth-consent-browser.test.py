#!/usr/bin/env python3
"""Real Hub + headless Chromium: approving the OAuth consent form must navigate to the client redirect URI.

Regression for v1.1.31, where the consent page CSP `form-action 'self'` made browsers block the
302 back to the client (e.g. chatgpt.com), so the flow hung after "approve".
Set VPC_HUB_BIN to test a prebuilt binary instead of building ./cmd/hub.
"""
import base64, hashlib, http.server, json, os, secrets, socket, subprocess, tempfile, threading, time, urllib.parse, urllib.request
from pathlib import Path
import websocket

REPO = Path(__file__).resolve().parents[3]
PASSWORD = "oauth-browser-test-password"
API_KEY = "oauth-browser-test-api-key"
PUBLIC = "https://hub.example.com"

def free_port():
    s = socket.socket(); s.bind(("127.0.0.1", 0)); p = s.getsockname()[1]; s.close(); return p

hits = []
class CB(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        hits.append(self.path); self.send_response(200); self.send_header("Content-Type", "text/plain"); self.end_headers(); self.wfile.write(b"callback ok")
    def log_message(self, *a): pass

with tempfile.TemporaryDirectory(prefix="vpc-oauth-browser-") as temp:
    temp = Path(temp)
    binary = Path(os.environ["VPC_HUB_BIN"]) if os.environ.get("VPC_HUB_BIN") else temp / "hub"
    if not os.environ.get("VPC_HUB_BIN"):
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/hub"], cwd=REPO, check=True)
    port, cb_port, cdp_port = free_port(), free_port(), free_port()
    base, redirect = f"http://127.0.0.1:{port}", f"http://127.0.0.1:{cb_port}/cb"
    cbsrv = http.server.HTTPServer(("127.0.0.1", cb_port), CB); threading.Thread(target=cbsrv.serve_forever, daemon=True).start()
    env = os.environ.copy()
    env.update({"VPS_COMMANDER_WEB_PASSWORD": PASSWORD, "VPS_COMMANDER_API_KEY": API_KEY,
                "VPS_COMMANDER_CLUSTER_SECRET": "oauth-browser-cluster", "VPS_COMMANDER_PUBLIC_URL": PUBLIC})
    log = open(temp / "hub.log", "w+")
    hub = subprocess.Popen([str(binary), "-addr", f"127.0.0.1:{port}", "-db", str(temp / "hub.db")], env=env, stdout=log, stderr=subprocess.STDOUT)
    chrome = None
    try:
        for _ in range(300):
            try: urllib.request.urlopen(base + "/healthz", timeout=1); break
            except Exception: time.sleep(.1)
        def admin(path, body):
            req = urllib.request.Request(base + path, data=json.dumps(body).encode(), headers={"Authorization": "Bearer " + API_KEY, "Content-Type": "application/json"})
            return json.load(urllib.request.urlopen(req, timeout=5))
        admin("/api/v1/tokens", {"name": "chatgpt-app", "scopes": ["devices:read", "mcp:call"]})
        client = admin("/api/v1/oauth/clients", {"name": "ChatGPT", "redirect_uris": [redirect]})
        verifier = secrets.token_urlsafe(48)
        challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).rstrip(b"=").decode()
        q = urllib.parse.urlencode({"response_type": "code", "client_id": client["client_id"], "redirect_uri": redirect, "state": "browser-state",
                                    "code_challenge": challenge, "code_challenge_method": "S256", "resource": PUBLIC + "/mcp", "scope": "devices:read mcp:call"})
        chrome = subprocess.Popen(["/usr/bin/chromium", "--headless", "--no-sandbox", "--disable-gpu", f"--remote-debugging-port={cdp_port}", f"--remote-allow-origins=http://127.0.0.1:{cdp_port}",
                                   f"--user-data-dir={temp / 'chromium'}", "about:blank"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        cdp = f"http://127.0.0.1:{cdp_port}"
        for _ in range(100):
            try: targets = json.load(urllib.request.urlopen(cdp + "/json", timeout=1)); break
            except Exception: time.sleep(.1)
        target = next(t for t in targets if t.get("type") == "page")
        ws = websocket.create_connection(target["webSocketDebuggerUrl"], timeout=8, origin=cdp)
        seq = [0]; console = []
        def cmd(method, params=None):
            seq[0] += 1; ws.send(json.dumps({"id": seq[0], "method": method, "params": params or {}}))
            while True:
                m = json.loads(ws.recv())
                if m.get("method") == "Runtime.consoleAPICalled" or m.get("method") == "Log.entryAdded": console.append(m)
                if m.get("id") == seq[0]: return m.get("result", {})
        def ev(expr, aw=False):
            return cmd("Runtime.evaluate", {"expression": expr, "awaitPromise": aw, "returnByValue": True}).get("result", {}).get("value")
        cmd("Page.enable"); cmd("Runtime.enable"); cmd("Log.enable")
        cmd("Page.navigate", {"url": base + "/panel/login"}); time.sleep(1)
        status = ev(f"fetch('/panel/login',{{method:'POST',headers:{{'Content-Type':'application/json'}},body:JSON.stringify({{password:{json.dumps(PASSWORD)}}})}}).then(r=>r.status)", True)
        assert status in (200, 204), f"panel login status {status}"
        cmd("Page.navigate", {"url": base + "/oauth/authorize?" + q}); time.sleep(1.5)
        assert ev("!!document.querySelector('button[value=approve]')"), "consent page did not render: " + str(ev("document.body.innerText"))
        print("PASS consent page rendered in Chromium")
        ev("document.querySelectorAll('input[name=scope]').forEach(c=>c.checked=true); document.querySelector('button[value=approve]').click(); true")
        landed = ""
        for _ in range(60):
            time.sleep(.1); landed = ev("location.href") or ""
            if landed.startswith(redirect): break
        cb = urllib.parse.urlparse(landed); params = urllib.parse.parse_qs(cb.query)
        assert landed.startswith(redirect) and params.get("code") and params.get("state") == ["browser-state"] and hits, \
            f"approve did not reach client redirect: href={landed} callback_hits={hits} logs={[c.get('params',{}).get('entry',{}).get('text') for c in console][-3:]}"
        print("PASS approve navigated to client redirect with code and state")
        # Complete the flow to show the delivered code is usable.
        tok = urllib.request.urlopen(urllib.request.Request(base + "/oauth/token", data=urllib.parse.urlencode({"grant_type": "authorization_code", "code": params["code"][0],
              "redirect_uri": redirect, "client_id": client["client_id"], "client_secret": client["client_secret"], "code_verifier": verifier}).encode()), timeout=5)
        assert json.load(tok).get("access_token", "").startswith("vpc_oat_"), "token exchange failed"
        print("PASS delivered code exchanged for an access token")
    finally:
        if chrome: chrome.kill()
        hub.kill(); cbsrv.shutdown()
