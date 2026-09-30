(() => {
  const root = document.querySelector('#content');
  const summaryEl = document.querySelector('#summaryText');
  const lastUpdatedEl = document.querySelector('#lastUpdated');

  const fmtBytes = n => {
    if (!Number.isFinite(n) || n <= 0) return '0 B';
    const u = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0, v = n;
    while (v >= 1024 && i < u.length - 1) {
      v /= 1024;
      i++;
    }
    return `${v.toFixed(i === 0 ? 0 : 1)} ${u[i]}`;
  };

  const fmtRate = n => `${fmtBytes(n)}/s`;

  const fmtUp = n => {
    let s = Math.max(0, Math.floor(n || 0));
    if (s <= 0) return '--';
    const d = Math.floor(s / 86400);
    s %= 86400;
    const h = Math.floor(s / 3600);
    s %= 3600;
    const m = Math.floor(s / 60);
    if (d > 0) return `${d}天 ${h}时`;
    if (h > 0) return `${h}小时 ${m}分`;
    return `${m} 分钟`;
  };

  const esc = s => String(s ?? '-').replace(/[&<>"']/g, c => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
  }[c]));

  const getFlag = (cc, country) => {
    if (!cc || typeof cc !== 'string' || cc.length !== 2) return '';
    const code = cc.toUpperCase();
    const flag = String.fromCodePoint(...[...code].map(c => 0x1F1E6 - 65 + c.charCodeAt(0)));
    const title = country ? `${country} (${code})` : code;
    return `<span class="flag-icon" title="${esc(title)}">${flag}</span>`;
  };

  const getStatusLevel = pct => {
    const val = Number(pct || 0);
    if (val >= 85) return 'danger';
    if (val >= 65) return 'warn';
    return 'normal';
  };

  let mode = localStorage.getItem('vpc_probe_view') || 'card';

  const setMode = m => {
    mode = m;
    localStorage.setItem('vpc_probe_view', m);
    render(window.__data || []);
  };

  document.querySelector('#cardBtn').onclick = () => setMode('card');
  document.querySelector('#compactBtn').onclick = () => setMode('compact');

  const renderCard = d => {
    const p = d.profile || {};
    const isOnline = d.status === 'online';
    const cpuPct = p.cpu_usage_percent != null ? Number(p.cpu_usage_percent) : 0;
    const memPct = p.mem_percent != null ? Number(p.mem_percent) : 0;
    const diskPct = p.disk_percent != null ? Number(p.disk_percent) : 0;
    const loadStr = [p.load_1m, p.load_5m, p.load_15m].map(x => Number(x || 0).toFixed(2)).join(', ');

    return `
      <div class="node-card">
        <div class="card-top">
          <div class="node-title">
            <div class="node-name-wrap">
              ${getFlag(d.country_code, d.country)}
              <span class="node-name">${esc(d.name)}</span>
              ${d.is_local ? '<span class="local-badge">本机</span>' : ''}
            </div>
            <div class="node-meta">
              <span class="meta-pill">${p.cpu_cores || 1} 核</span>
              <span class="meta-pill">${esc(d.os || 'Linux')}</span>
              <span>${fmtUp(p.uptime_seconds)}</span>
            </div>
          </div>
          <div class="status-badge ${isOnline ? 'online' : 'offline'}">
            <span class="dot"></span>
            ${isOnline ? '在线' : '离线'}
          </div>
        </div>

        <div class="metrics-bars">
          <div class="metric-item">
            <div class="metric-header">
              <span class="metric-label">CPU 负载</span>
              <span class="metric-value">${p.cpu_usage_percent != null ? `${cpuPct}%` : '--'}</span>
            </div>
            <div class="progress-track">
              <div class="progress-fill ${getStatusLevel(cpuPct)}" style="width: ${Math.min(100, Math.max(0, cpuPct))}%"></div>
            </div>
          </div>

          <div class="metric-item">
            <div class="metric-header">
              <span class="metric-label">内存 (空闲 ${esc(p.mem_available || '--')})</span>
              <span class="metric-value">${p.mem_percent != null ? `${memPct}%` : '--'}</span>
            </div>
            <div class="progress-track">
              <div class="progress-fill ${getStatusLevel(memPct)}" style="width: ${Math.min(100, Math.max(0, memPct))}%"></div>
            </div>
          </div>

          <div class="metric-item">
            <div class="metric-header">
              <span class="metric-label">磁盘 (空闲 ${esc(p.disk_free || '--')})</span>
              <span class="metric-value">${p.disk_percent != null ? `${diskPct}%` : '--'}</span>
            </div>
            <div class="progress-track">
              <div class="progress-fill ${getStatusLevel(diskPct)}" style="width: ${Math.min(100, Math.max(0, diskPct))}%"></div>
            </div>
          </div>
        </div>

        <div class="card-stats-grid">
          <div class="stat-box">
            <span class="stat-title">实时网络 (↓ / ↑)</span>
            <span class="stat-desc">
              <b class="traffic-in">↓ ${fmtRate(p.net_rx_rate)}</b> / 
              <b class="traffic-out">↑ ${fmtRate(p.net_tx_rate)}</b>
            </span>
          </div>
          <div class="stat-box">
            <span class="stat-title">总流量 (↓ / ↑)</span>
            <span class="stat-desc">
              ↓ ${fmtBytes(p.net_rx_total)} / ↑ ${fmtBytes(p.net_tx_total)}
            </span>
          </div>
          <div class="stat-box">
            <span class="stat-title">系统平均负载 (1/5/15m)</span>
            <span class="stat-desc">${loadStr}</span>
          </div>
          <div class="stat-box">
            <span class="stat-title">IO PSI / Docker</span>
            <span class="stat-desc">${p.io_psi != null ? Number(p.io_psi).toFixed(2) : '0.00'} / ${p.docker ? '有' : '无'}</span>
          </div>
        </div>
      </div>
    `;
  };

  const renderTable = list => {
    const rows = list.map(d => {
      const p = d.profile || {};
      const isOnline = d.status === 'online';
      const cpuPct = p.cpu_usage_percent != null ? Number(p.cpu_usage_percent) : 0;
      const memPct = p.mem_percent != null ? Number(p.mem_percent) : 0;
      const diskPct = p.disk_percent != null ? Number(p.disk_percent) : 0;
      const loadStr = [p.load_1m, p.load_5m, p.load_15m].map(x => Number(x || 0).toFixed(2)).join(' / ');

      return `
        <tr>
          <td>
            <div class="table-node">
              <span class="name">
                ${getFlag(d.country_code, d.country)}
                ${esc(d.name)}
                ${d.is_local ? '<span class="local-badge">本机</span>' : ''}
              </span>
              <span class="sub">${p.cpu_cores || 1}核 | ${esc(d.os || 'Linux')}</span>
            </div>
          </td>
          <td>
            <span class="status-badge ${isOnline ? 'online' : 'offline'}">
              <span class="dot"></span>
              ${isOnline ? '在线' : '离线'}
            </span>
          </td>
          <td class="col-mono">${fmtUp(p.uptime_seconds)}</td>
          <td class="col-mono">${loadStr}</td>
          <td>
            <div class="mini-bar-wrap">
              <div class="mini-bar">
                <div class="mini-bar-fill progress-fill ${getStatusLevel(cpuPct)}" style="width: ${Math.min(100, Math.max(0, cpuPct))}%"></div>
              </div>
              <span class="col-mono">${p.cpu_usage_percent != null ? `${cpuPct}%` : '--'}</span>
            </div>
          </td>
          <td>
            <div class="mini-bar-wrap">
              <div class="mini-bar">
                <div class="mini-bar-fill progress-fill ${getStatusLevel(memPct)}" style="width: ${Math.min(100, Math.max(0, memPct))}%"></div>
              </div>
              <span class="col-mono">${p.mem_percent != null ? `${memPct}%` : '--'}</span>
            </div>
          </td>
          <td class="col-mono">
            <span class="traffic-in">↓ ${fmtRate(p.net_rx_rate)}</span><br>
            <span class="traffic-out">↑ ${fmtRate(p.net_tx_rate)}</span>
          </td>
          <td class="col-mono">
            ↓ ${fmtBytes(p.net_rx_total)}<br>
            ↑ ${fmtBytes(p.net_tx_total)}
          </td>
          <td>
            <div class="mini-bar-wrap">
              <div class="mini-bar">
                <div class="mini-bar-fill progress-fill ${getStatusLevel(diskPct)}" style="width: ${Math.min(100, Math.max(0, diskPct))}%"></div>
              </div>
              <span class="col-mono">${p.disk_percent != null ? `${diskPct}%` : '--'}</span>
            </div>
          </td>
        </tr>
      `;
    }).join('');

    return `
      <div class="table-wrapper">
        <table class="probe-table">
          <thead>
            <tr>
              <th>节点名称</th>
              <th>状态</th>
              <th>运行时间</th>
              <th>负载 (1/5/15m)</th>
              <th>CPU</th>
              <th>内存</th>
              <th>实时网速</th>
              <th>累计流量</th>
              <th>磁盘</th>
            </tr>
          </thead>
          <tbody>
            ${rows}
          </tbody>
        </table>
      </div>
    `;
  };

  function render(data) {
    document.querySelector('#cardBtn').classList.toggle('active', mode === 'card');
    document.querySelector('#compactBtn').classList.toggle('active', mode === 'compact');

    const total = data.length;
    const onlineCount = data.filter(x => x.status === 'online').length;
    if (summaryEl) {
      summaryEl.textContent = `${onlineCount} / ${total} 节点在线`;
    }

    if (!data || data.length === 0) {
      root.innerHTML = '<div class="error-msg">暂无节点数据</div>';
      return;
    }

    if (mode === 'card') {
      root.className = 'card-view';
      root.innerHTML = `<div class="card-grid">${data.map(renderCard).join('')}</div>`;
    } else {
      root.className = 'table-view';
      root.innerHTML = renderTable(data);
    }
  }

  async function load() {
    try {
      const r = await fetch('/probe/summary', { cache: 'no-store' });
      if (!r.ok) throw Error();
      let rawData = await r.json();
      if (Array.isArray(rawData)) {
        rawData.sort((a, b) => {
          if (a.is_local && !b.is_local) return -1;
          if (!a.is_local && b.is_local) return 1;
          return String(a.name || "").localeCompare(String(b.name || ""));
        });
      }
      window.__data = rawData;
      render(window.__data);
      if (lastUpdatedEl) {
        const now = new Date();
        const timeStr = `${String(now.getHours()).padStart(2, '0')}:${String(now.getMinutes()).padStart(2, '0')}:${String(now.getSeconds()).padStart(2, '0')}`;
        lastUpdatedEl.textContent = `更新于 ${timeStr}`;
      }
    } catch (e) {
      root.innerHTML = '<div class="error-msg">探针数据暂时不可用，正在重试...</div>';
    }
  }

  load();
  setInterval(load, 5000);
})();
