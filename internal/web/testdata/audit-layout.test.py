#!/usr/bin/env python3
"""Use Chromium CDP device metrics to verify the actual audit panel at 1280/390/320 CSS px."""
import json
import os
import re
import subprocess
import tempfile
import time
import urllib.request
from pathlib import Path

import websocket

ROOT = Path(__file__).resolve().parent.parent / "static"
HTML = (ROOT / "index.html").read_text()
match = re.search(r'<article class="card wide audit-card">[\s\S]*?</article>', HTML)
assert match, "actual audit explorer markup must exist in index.html"

with tempfile.TemporaryDirectory(prefix="vpc-audit-cdp-") as temp:
    temp_path = Path(temp)
    harness = temp_path / "index.html"
    css = (ROOT / "app.css").as_uri()
    audit_js = (ROOT / "audit-ui.js").as_uri()
    harness.write_text(f'''<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="{css}"></head><body><main><section class="grid">{match.group(0)}</section></main><script src="{audit_js}"></script></body></html>''')
    profile = temp_path / "profile"
    proc = subprocess.Popen([
        "/usr/bin/chromium", "--headless", "--no-sandbox", "--disable-gpu",
        "--disable-dev-shm-usage", "--remote-allow-origins=*", "--remote-debugging-port=0",
        f"--user-data-dir={profile}", "about:blank"
    ], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        port_file = profile / "DevToolsActivePort"
        deadline = time.time() + 10
        while time.time() < deadline and not port_file.exists(): time.sleep(0.05)
        assert port_file.exists(), "Chromium did not expose CDP"
        port = int(port_file.read_text().splitlines()[0])
        base = f"http://127.0.0.1:{port}"
        with urllib.request.urlopen(base + "/json/version", timeout=2) as response:
            browser_ws = json.load(response)["webSocketDebuggerUrl"]
        ws = websocket.create_connection(browser_ws, timeout=5, origin=base)
        next_id = 0
        def command(method, params=None, session_id=None):
            global next_id
            next_id += 1
            msg = {"id": next_id, "method": method, "params": params or {}}
            if session_id: msg["sessionId"] = session_id
            ws.send(json.dumps(msg))
            while True:
                result = json.loads(ws.recv())
                if result.get("id") == next_id:
                    if "error" in result: raise RuntimeError(result["error"])
                    return result.get("result", {})
        target = command("Target.createTarget", {"url": "about:blank"})["targetId"]
        targets = []
        deadline = time.time() + 5
        while time.time() < deadline:
            with urllib.request.urlopen(base + "/json/list", timeout=2) as response:
                targets = json.load(response)
            found = next((t for t in targets if t.get("id") == target), None)
            if found: break
            time.sleep(0.05)
        assert found, "CDP page target was not created"
        page_ws = websocket.create_connection(found["webSocketDebuggerUrl"], timeout=5, origin=base)
        msg_id = 0
        def page_command(method, params=None):
            global msg_id
            msg_id += 1
            page_ws.send(json.dumps({"id": msg_id, "method": method, "params": params or {}}))
            while True:
                result = json.loads(page_ws.recv())
                if result.get("id") == msg_id:
                    if "error" in result: raise RuntimeError(result["error"])
                    return result.get("result", {})
        page_command("Page.enable")
        page_command("Runtime.enable")
        page_command("Page.navigate", {"url": harness.as_uri()})
        for width in (1280, 390, 320):
            page_command("Emulation.setDeviceMetricsOverride", {"width": width, "height": 1000, "deviceScaleFactor": 1, "mobile": True})
            time.sleep(0.25)
            expression = '''(() => { const card=document.querySelector('.audit-card'), wrap=document.querySelector('.audit-tablewrap'), table=document.querySelector('.audit-table'), filters=document.querySelector('.audit-filters'); return {viewport:innerWidth,documentWidth:document.documentElement.scrollWidth,cardWidth:card.getBoundingClientRect().width,cardScrollWidth:card.scrollWidth,wrapWidth:wrap.clientWidth,wrapScrollWidth:wrap.scrollWidth,tableWidth:table.scrollWidth,filterWidth:filters.clientWidth,filterChildren:[...filters.children].map(e=>({left:e.getBoundingClientRect().left,right:e.getBoundingClientRect().right,top:e.getBoundingClientRect().top,bottom:e.getBoundingClientRect().bottom}))}; })()'''
            evaluated = page_command("Runtime.evaluate", {"expression": expression, "returnByValue": True})
            metrics = evaluated["result"]["value"]
            assert metrics["viewport"] == width, f"viewport mismatch at {width}px: {metrics}"
            assert metrics["documentWidth"] <= width + 2, f"page overflows horizontally at {width}px: {metrics}"
            if width <= 450:
                assert metrics["wrapScrollWidth"] > metrics["wrapWidth"], f"audit table is not horizontally scrollable at {width}px: {metrics}"
            assert metrics["cardWidth"] <= width, f"audit card exceeds viewport at {width}px: {metrics}"
            children = metrics["filterChildren"]
            for i, first in enumerate(children):
                for second in children[i+1:]:
                    overlap = min(first["right"], second["right"]) - max(first["left"], second["left"]) > 1 and min(first["bottom"], second["bottom"]) - max(first["top"], second["top"]) > 1
                    assert not overlap, f"filter controls overlap at {width}px: {metrics}"
            print(f"PASS audit layout {width}px: viewport={metrics['viewport']}, card={metrics['cardWidth']:.0f}, tablewrap={metrics['wrapWidth']}, table={metrics['tableWidth']}")
        page_ws.close()
        ws.close()
    finally:
        proc.terminate()
        try: proc.wait(timeout=5)
        except subprocess.TimeoutExpired: proc.kill()
