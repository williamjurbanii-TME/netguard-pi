'use strict';

const app = document.getElementById('app');
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const fmt = n => Number(n || 0).toLocaleString();
const pct = (a, b) => b ? (a / b * 100).toFixed(1) + '%' : '0%';
const state = { devices: [], groups: [], catalog: null };
let timer = null;

async function api(path, opts = {}) {
  const r = await fetch('/api' + path, {
    method: opts.method || 'GET',
    headers: { 'Content-Type': 'application/json' },
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
  });
  if (!r.ok) throw new Error((await r.text()) || r.statusText);
  return r.status === 204 || r.status === 202 ? null : r.json();
}

function toast(msg) {
  const t = document.getElementById('toast');
  t.textContent = msg;
  t.classList.remove('hidden');
  clearTimeout(toast.t);
  toast.t = setTimeout(() => t.classList.add('hidden'), 2500);
}

function ago(ts) {
  if (!ts) return 'never';
  const s = Math.floor(Date.now() / 1000) - ts;
  if (s < 60) return 'just now';
  if (s < 3600) return Math.floor(s / 60) + 'm ago';
  if (s < 86400) return Math.floor(s / 3600) + 'h ago';
  return Math.floor(s / 86400) + 'd ago';
}

const timeStr = ts => new Date(ts * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
const devName = d => d ? (d.name || d.hostname || d.id) : '';
const deviceById = id => state.devices.find(d => d.id === id);

async function loadState() {
  [state.devices, state.groups, state.catalog] = await Promise.all([api('/devices'), api('/groups'), state.catalog || api('/catalog')]);
}

function openModal(html) {
  document.getElementById('modal-body').innerHTML = html;
  document.getElementById('modal').classList.remove('hidden');
}
function closeModal() { document.getElementById('modal').classList.add('hidden'); }
document.getElementById('modal').addEventListener('click', e => { if (e.target.id === 'modal') closeModal(); });

// ---------- shared widgets ----------

function chart(hourly) {
  const now = Math.floor(Date.now() / 3600000) * 3600;
  const byHour = new Map(hourly.map(h => [h.hour, h]));
  const cols = [];
  let max = 1;
  for (let i = 23; i >= 0; i--) {
    const t = now - i * 3600;
    const h = byHour.get(t) || { total: 0, blocked: 0 };
    cols.push({ t, total: h.total, blocked: h.blocked });
    max = Math.max(max, h.total);
  }
  const bars = cols.map(c => {
    const label = new Date(c.t * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    return `<div class="col" title="${label}: ${fmt(c.total)} queries, ${fmt(c.blocked)} blocked">
      <div class="b" style="height:${c.blocked / max * 100}%"></div>
      <div class="a" style="height:${(c.total - c.blocked) / max * 100}%"></div></div>`;
  }).join('');
  return `<div class="chart">${bars}</div><div class="chart-axis"><span>24h ago</span><span>now</span></div>`;
}

const legend = `<div class="legend"><span><i style="background:var(--bar)"></i>Allowed</span><span><i style="background:var(--bad)"></i>Blocked</span></div>`;

function domainList(items, groupId, action) {
  if (!items.length) return '<p class="muted">Nothing yet.</p>';
  const max = items[0].count || 1;
  return items.map(i => `<div class="bar-row">
      <span class="name" title="${esc(i.domain)}">${esc(i.domain)}</span>
      <span class="muted num">${fmt(i.count)}</span>
      ${groupId ? `<button class="link" data-rule="${action}" data-domain="${esc(i.domain)}" data-group="${groupId}">${action}</button>` : ''}
    </div><div style="height:3px;background:var(--border);border-radius:2px"><div style="height:3px;width:${i.count / max * 100}%;background:${action === 'allow' ? 'var(--bad)' : 'var(--bar)'};border-radius:2px"></div></div>`).join('');
}

async function addRule(groupId, domain, action) {
  const g = state.groups.find(g => g.id == groupId);
  await api(`/groups/${groupId}/rule`, { method: 'POST', body: { domain, action } });
  toast(`${action === 'block' ? 'Blocked' : 'Allowed'} ${domain} for group "${g ? g.name : groupId}"`);
  state.groups = await api('/groups');
}

document.addEventListener('click', async e => {
  const b = e.target.closest('[data-rule]');
  if (!b) return;
  try { await addRule(b.dataset.group, b.dataset.domain, b.dataset.rule); b.disabled = true; }
  catch (err) { toast(err.message); }
});

// ---------- pages ----------

const pages = {
  async overview() {
    const s = await api('/summary');
    app.innerHTML = `
      <h2>Overview</h2>
      <div class="cards">
        <div class="card"><div class="label">Queries (24h)</div><div class="value">${fmt(s.queries)}</div></div>
        <div class="card"><div class="label">Blocked (24h)</div><div class="value">${fmt(s.blocked)}</div></div>
        <div class="card"><div class="label">Percent blocked</div><div class="value">${pct(s.blocked, s.queries)}</div></div>
        <div class="card"><div class="label">Active devices</div><div class="value">${s.active_devices} <span class="muted small">/ ${s.devices}</span></div></div>
        <div class="card"><div class="label">Domains on blocklists</div><div class="value">${fmt(s.list_domains)}</div></div>
      </div>
      <div class="panel"><div class="toolbar"><h3>Queries, last 24 hours</h3><span class="spacer"></span>${legend}</div>${chart(s.hourly)}</div>
      <p class="muted small">History: ${fmt(s.history.queries)} lookups stored${s.history.oldest ? ' since ' + new Date(s.history.oldest * 1000).toLocaleDateString() : ''}
        · kept for ${s.retention_days} days · database ${(s.history.bytes / 1048576).toFixed(1)} MB · NetGuard ${esc(s.version)}</p>
      <div class="grid2">
        <div class="panel"><h3>Top blocked domains</h3>${domainList(s.top_blocked)}</div>
        <div class="panel"><h3>Top allowed domains</h3>${domainList(s.top_allowed)}</div>
      </div>
      <div class="panel" style="margin-top:12px">
        <div class="toolbar"><h3>Blocklists</h3><span class="spacer"></span><button id="refresh-lists">Refresh lists now</button></div>
        <table><tr><th>Name</th><th>Category</th><th class="num">Domains</th><th>Updated</th><th>Status</th></tr>
        ${(s.lists || []).map(l => `<tr><td>${esc(l.name)}</td><td>${esc(l.category)}</td><td class="num">${fmt(l.domains)}</td>
          <td class="muted">${l.updated && !l.updated.startsWith('0001') ? new Date(l.updated).toLocaleString() : 'never'}</td>
          <td>${l.error ? `<span class="badge blocked" title="${esc(l.error)}">error</span>` : '<span class="badge allowed">ok</span>'}</td></tr>`).join('')}
        </table>
      </div>`;
    document.getElementById('refresh-lists').onclick = async () => { await api('/lists/refresh', { method: 'POST' }); toast('Refreshing blocklists in the background…'); };
    return () => pages.overview();
  },

  async ads() {
    await loadState();
    const a = await api('/ads?hours=' + adsHours);
    const groupOf = id => state.groups.find(g => g.id === deviceById(id)?.group_id)?.name || 'Default';
    const ranges = [[24, 'Last 24 hours'], [168, 'Last 7 days'], [720, 'Last 30 days']];
    const maxDev = Math.max(1, ...a.devices.map(d => d.blocked + d.allowed));
    app.innerHTML = `
      <div class="toolbar"><h2 style="margin:0">Ads &amp; Tracking</h2><span class="spacer"></span>
        <select id="ads-range">${ranges.map(([h, l]) => `<option value="${h}" ${h === adsHours ? 'selected' : ''}>${l}</option>`).join('')}</select></div>
      <div class="cards">
        <div class="card"><div class="label">Ad &amp; tracking requests blocked</div><div class="value">${fmt(a.blocked)}</div></div>
        <div class="card"><div class="label">Share of all lookups</div><div class="value">${pct(a.blocked + a.allowed, a.queries)}</div></div>
        <div class="card"><div class="label">Seen but not blocked</div><div class="value" style="color:${a.allowed ? 'var(--warn)' : 'inherit'}">${fmt(a.allowed)}</div></div>
        <div class="card"><div class="label">Chattiest device</div><div class="value" style="font-size:18px">${a.devices[0] ? esc(devName(deviceById(a.devices[0].id)) || a.devices[0].id) : '—'}</div></div>
      </div>
      ${a.allowed ? `<div class="warn-box">${fmt(a.allowed)} ad/tracking requests were <b>not blocked</b> because those devices' groups don't block that category. Put TVs in the <b>TVs</b> group (or tick <b>smarttv</b> on their group) to block them.</div>` : ''}
      <div class="panel"><div class="toolbar"><h3>Over time</h3><span class="spacer"></span>
        <div class="legend"><span><i style="background:var(--bad)"></i>Blocked</span><span><i style="background:var(--warn)"></i>Seen, not blocked</span></div></div>
        ${adChart(a.series, a.bucket, a.hours)}</div>
      <div class="panel" style="margin-top:12px"><h3>By device</h3><table>
        <tr><th>Device</th><th class="hide-sm">Group</th><th class="num">Blocked</th><th class="num">Not blocked</th><th class="num hide-sm">% of its lookups</th><th style="width:25%" class="hide-sm"></th><th></th></tr>
        ${a.devices.map(d => `<tr>
          <td>${esc(devName(deviceById(d.id)) || d.id)}</td><td class="hide-sm muted">${esc(groupOf(d.id))}</td>
          <td class="num">${fmt(d.blocked)}</td><td class="num" style="color:${d.allowed ? 'var(--warn)' : 'inherit'}">${fmt(d.allowed)}</td>
          <td class="num hide-sm">${pct(d.blocked + d.allowed, d.queries)}</td>
          <td class="hide-sm"><div style="display:flex;height:8px;border-radius:4px;overflow:hidden;background:var(--border);width:${(d.blocked + d.allowed) / maxDev * 100}%">
            <div style="flex:${d.blocked};background:var(--bad)"></div><div style="flex:${d.allowed};background:var(--warn)"></div></div></td>
          <td><button class="link" data-ads-device="${esc(d.id)}">details</button></td></tr>`).join('') || '<tr><td colspan="7" class="muted">No ad or tracking requests yet.</td></tr>'}
      </table></div>
      <div class="panel" style="margin-top:12px"><h3>Who they're talking to</h3>${companyTable(a.companies)}</div>`;
    document.getElementById('ads-range').onchange = e => { adsHours = Number(e.target.value); pages.ads(); };
    app.querySelectorAll('[data-ads-device]').forEach(b => b.onclick = () => adsDetails(b.dataset.adsDevice));
    return null; // heavy queries: refresh on demand only
  },

  async devices() {
    await loadState();
    const groupOpts = sel => state.groups.map(g => `<option value="${g.id}" ${g.id === sel ? 'selected' : ''}>${esc(g.name)}</option>`).join('');
    app.innerHTML = `
      <div class="toolbar"><h2 style="margin:0">Devices</h2><span class="spacer"></span><button id="reload">Refresh</button></div>
      <div class="panel"><table>
        <tr><th>Device</th><th class="hide-sm">IP</th><th>Group</th><th class="num">Queries 24h</th><th class="num hide-sm">Blocked</th><th>Pause internet</th><th></th></tr>
        ${state.devices.map(d => `<tr data-id="${esc(d.id)}">
          <td><span class="dot ${d.online ? 'on' : ''}"></span><b>${esc(devName(d))}</b>
            ${d.private_mac ? '<span class="badge" title="This device uses a private (randomized) Wi-Fi address">private Wi-Fi address</span>' : ''}
            <div class="muted small">${d.hostname && d.name ? esc(d.hostname) + ' · ' : ''}${d.local_name ? `<span class="mono">${esc(d.local_name)}</span> · ` : ''}${d.identifiers.length > 1 ? d.identifiers.length + ' addresses · ' : ''}seen ${ago(d.last_seen)}</div>
            ${d.same_name_as.filter(o => (deviceById(o)?.first_seen ?? 0) <= d.first_seen).map(o => `<div class="small warn-text">Same name as <b>${esc(devName(deviceById(o)) || o)}</b> <button class="link" data-merge="${esc(d.id)}" data-into="${esc(o)}">merge into it</button></div>`).join('')}</td>
          <td class="hide-sm mono">${esc(d.last_ip)}${d.static_ip ? ` <span class="badge" title="DHCP reservation">${esc(d.static_ip)}</span>` : ''}</td>
          <td><select data-act="group">${groupOpts(d.group_id)}</select></td>
          <td class="num">${fmt(d.queries)}</td>
          <td class="num hide-sm">${fmt(d.blocked)} <span class="muted small">${pct(d.blocked, d.queries)}</span></td>
          <td><label class="switch"><input type="checkbox" data-act="pause" ${d.paused ? 'checked' : ''}><span></span></label></td>
          <td style="white-space:nowrap"><button data-act="details">Summary</button> <button data-act="edit">Edit</button></td>
        </tr>`).join('') || '<tr><td colspan="7" class="muted">No devices yet. Point a device\'s DNS at this machine to see it here.</td></tr>'}
      </table></div>`;
    document.getElementById('reload').onclick = () => pages.devices();
    app.querySelectorAll('[data-merge]').forEach(b => b.onclick = () => mergeDevice(b.dataset.merge, b.dataset.into));
    app.querySelectorAll('tr[data-id]').forEach(tr => {
      const id = tr.dataset.id, path = '/devices/' + encodeURIComponent(id);
      tr.querySelector('[data-act=group]').onchange = async e => {
        await api(path, { method: 'PUT', body: { group_id: Number(e.target.value) } });
        toast('Group updated');
      };
      tr.querySelector('[data-act=pause]').onchange = async e => {
        await api(path, { method: 'PUT', body: { paused: e.target.checked } });
        toast(e.target.checked ? 'Internet paused for this device' : 'Internet resumed');
      };
      tr.querySelector('[data-act=details]').onclick = () => deviceDetails(id);
      tr.querySelector('[data-act=edit]').onclick = () => editDevice(id);
    });
  },

  async groups() {
    await loadState();
    const render = () => {
      app.innerHTML = `
        <div class="toolbar"><h2 style="margin:0">Groups</h2><span class="spacer"></span><button class="primary" id="new-group">New group</button></div>
        <p class="muted">Each device belongs to one group. Unknown devices use <b>${esc(state.groups[0]?.name || 'Default')}</b>.</p>
        ${state.groups.map(groupForm).join('')}`;
      document.getElementById('new-group').onclick = () => {
        state.groups.push({ id: 0, name: '', categories: ['ads', 'malware'], services: [], custom_block: [], custom_allow: [], safe_search: false, block_doh: false });
        render();
      };
      app.querySelectorAll('form[data-gid]').forEach(f => {
        f.onsubmit = async e => {
          e.preventDefault();
          const id = Number(f.dataset.gid);
          const checked = name => [...f.querySelectorAll(`input[name=${name}]:checked`)].map(i => i.value);
          const lines = name => f.elements[name].value.split(/[\s,]+/).filter(Boolean);
          const body = {
            name: f.elements.name.value, categories: checked('cat'), services: checked('svc'),
            safe_search: f.elements.safe_search.checked, block_doh: f.elements.block_doh.checked,
            custom_block: lines('custom_block'), custom_allow: lines('custom_allow'),
          };
          try {
            await api(id ? '/groups/' + id : '/groups', { method: id ? 'PUT' : 'POST', body });
            toast('Group saved');
            pages.groups();
          } catch (err) { toast(err.message); }
        };
        const del = f.querySelector('[data-act=delete]');
        if (del) del.onclick = async () => {
          const id = Number(f.dataset.gid);
          if (!id) { state.groups = state.groups.filter(g => g.id !== 0); return render(); }
          if (!confirm('Delete this group? Its devices move to the default group.')) return;
          await api('/groups/' + id, { method: 'DELETE' });
          pages.groups();
        };
      });
    };
    render();
  },

  async log() {
    await loadState();
    const view = logView.mode;
    app.innerHTML = `
      <div class="toolbar"><h2 style="margin:0">Query Log</h2><span class="spacer"></span>
        <div class="seg"><button data-view="sites" class="${view === 'sites' ? 'active' : ''}" title="One line per website per device">Sites</button><button data-view="raw" class="${view === 'raw' ? 'active' : ''}" title="Every single DNS lookup">Every lookup</button></div></div>
      <div class="toolbar">
        <select id="f-device"><option value="">All devices</option>${state.devices.map(d => `<option value="${esc(d.id)}" ${d.id === logView.device ? 'selected' : ''}>${esc(devName(d))}</option>`).join('')}</select>
        ${view === 'sites'
          ? `<select id="f-hours">${[[1, 'Last hour'], [24, 'Last 24 hours'], [168, 'Last 7 days']].map(([h, l]) => `<option value="${h}" ${h === logView.hours ? 'selected' : ''}>${l}</option>`).join('')}</select>`
          : `<select id="f-status"><option value="">Any status</option>${['blocked', 'allowed', 'cached', 'safesearch', 'local', 'error'].map(s => `<option ${s === logView.status ? 'selected' : ''}>${s}</option>`).join('')}</select>`}
        <input id="f-search" placeholder="Search domain…" value="${esc(logView.search)}">
        ${view === 'sites' ? `
          <label class="chip"><input type="checkbox" id="f-flagged" ${logView.flagged ? 'checked' : ''}>Flagged only <span class="muted small">(adult, gambling, malware)</span></label>
          <label class="chip" title="Hide ads, trackers and system services (iCloud, Windows Update, content servers...)"><input type="checkbox" id="f-bg" ${logView.hideBackground ? 'checked' : ''}>Hide background</label>` : ''}
        <span class="spacer"></span>
        <label class="small muted" title="Updates pause while your mouse is over the table"><input type="checkbox" id="f-live" ${logView.live ? 'checked' : ''}> Live</label>
      </div>
      <div class="panel" id="log-panel"><table>${view === 'sites'
        ? '<thead><tr><th>Last seen</th><th>Device</th><th>Site</th><th class="num">Lookups</th><th>Status</th><th></th></tr></thead>'
        : '<thead><tr><th>Time</th><th>Device</th><th>Domain</th><th class="hide-sm">Type</th><th>Status</th><th></th></tr></thead>'}
        <tbody id="log-body"></tbody></table></div>`;

    let hovering = false;
    const panel = document.getElementById('log-panel');
    panel.onmouseenter = () => { hovering = true; };
    panel.onmouseleave = () => { hovering = false; };
    const val = id => document.getElementById(id)?.value ?? '';
    const read = () => {
      logView.device = val('f-device');
      logView.search = val('f-search');
      logView.live = document.getElementById('f-live').checked;
      if (view === 'sites') {
        logView.hours = Number(val('f-hours'));
        logView.flagged = document.getElementById('f-flagged').checked;
        logView.hideBackground = document.getElementById('f-bg').checked;
      } else {
        logView.status = val('f-status');
      }
      saveLogView();
    };
    const load = async () => {
      read();
      const body = document.getElementById('log-body');
      if (view === 'sites') {
        const q = new URLSearchParams({ device: logView.device, search: logView.search, hours: logView.hours,
          flagged: logView.flagged ? 1 : 0, hide_background: logView.hideBackground ? 1 : 0 });
        const rows = await api('/sites?' + q);
        body.innerHTML = rows.map(siteRow).join('') || '<tr><td colspan="6" class="muted">No sites match.</td></tr>';
        body.querySelectorAll('[data-drill]').forEach(b => b.onclick = () => {
          Object.assign(logView, { mode: 'raw', device: b.dataset.device, search: b.dataset.drill, status: '' });
          saveLogView();
          pages.log();
        });
      } else {
        const q = new URLSearchParams({ device: logView.device, status: logView.status, search: logView.search, limit: 300 });
        const rows = await api('/queries?' + q);
        body.innerHTML = rows.map(logRow).join('') || '<tr><td colspan="6" class="muted">No queries match.</td></tr>';
      }
    };
    app.querySelectorAll('[data-view]').forEach(b => b.onclick = () => { logView.mode = b.dataset.view; saveLogView(); pages.log(); });
    ['f-device', 'f-hours', 'f-status', 'f-flagged', 'f-bg', 'f-live'].forEach(id => {
      const el = document.getElementById(id);
      if (el) el.onchange = load;
    });
    let deb;
    document.getElementById('f-search').oninput = () => { clearTimeout(deb); deb = setTimeout(load, 300); };
    await load();
    let tick = 0;
    return () => {
      // Raw view refreshes every 3 s; the heavier Sites view every 15 s.
      if (!logView.live || hovering || (view === 'sites' && ++tick % 5 !== 0)) return;
      return load();
    };
  },

  async dhcp() {
    await loadState();
    const s = await api('/summary');
    const leases = await api('/leases');
    app.innerHTML = `
      <h2>DHCP Leases</h2>
      ${s.dhcp_enabled ? '' : `<div class="warn-box">DHCP is disabled. Enable it in <span class="mono">config.yaml</span> once your router's DHCP server is turned off (or on an isolated test network).</div>`}
      <div class="panel"><table>
        <tr><th>IP</th><th>MAC</th><th>Hostname</th><th>Device</th><th>Expires</th></tr>
        ${leases.map(l => `<tr><td class="mono">${esc(l.ip)}</td><td class="mono">${esc(l.mac)}</td><td>${esc(l.hostname)}</td>
          <td>${esc(devName(deviceById(l.mac)))}</td><td class="muted">${l.expires * 1000 < Date.now() ? 'expired' : new Date(l.expires * 1000).toLocaleString()}</td></tr>`).join('')
          || '<tr><td colspan="5" class="muted">No leases.</td></tr>'}
      </table></div>`;
  },
};

// Query Log view settings, remembered per browser.
const logView = Object.assign(
  { mode: 'sites', device: '', search: '', status: '', hours: 24, flagged: false, hideBackground: true, live: true },
  (() => { try { return JSON.parse(localStorage.getItem('ng.logView') || '{}'); } catch { return {}; } })());
function saveLogView() {
  try { localStorage.setItem('ng.logView', JSON.stringify(logView)); } catch { /* storage unavailable */ }
}

function when(ts) {
  const d = new Date(ts * 1000);
  return d.toDateString() === new Date().toDateString()
    ? d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
    : d.toLocaleDateString([], { month: 'short', day: 'numeric' }) + ' ' + d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

function siteRow(s) {
  const d = deviceById(s.device_id);
  const gid = d ? d.group_id : 1;
  const status = s.blocked === 0 ? 'allowed' : s.blocked === s.lookups ? 'blocked' : 'partly blocked';
  const badgeClass = status === 'partly blocked' ? 'safesearch' : status;
  const tags = s.reasons.filter(r => !s.flags.some(f => r.endsWith(':' + f))).map(r => r.replace(/^(list|seen):/, '')).join(', ');
  const action = status === 'blocked' ? 'allow' : 'block';
  return `<tr class="${s.flags.length ? 'flagged' : ''}">
    <td class="mono" title="first ${esc(new Date(s.first * 1000).toLocaleString())}">${when(s.last)}</td>
    <td>${esc(devName(d) || s.device_id)}</td>
    <td style="word-break:break-all"><b>${esc(s.site)}</b>${s.app ? ` <span class="badge">${esc(s.app)}</span>` : ''}${s.flags.map(f => ` <span class="badge flag">${esc(f)}</span>`).join('')}
      <div class="muted small mono">${s.hosts.map(esc).join(' · ')}</div></td>
    <td class="num">${fmt(s.lookups)}</td>
    <td><span class="badge ${badgeClass}">${status}</span> <span class="muted small">${esc(tags)}</span></td>
    <td style="white-space:nowrap"><button class="link" data-drill="${esc(s.site)}" data-device="${esc(s.device_id)}" title="Show every lookup">details</button>
      <button class="link" data-rule="${action}" data-domain="${esc(s.site)}" data-group="${gid}" title="${action} ${esc(s.site)} and its subdomains for this device's group">${action}</button></td>
  </tr>`;
}

function logRow(e) {
  const d = deviceById(e.device_id);
  const gid = d ? d.group_id : 1;
  const action = e.status === 'blocked' ? 'allow' : 'block';
  return `<tr>
    <td class="mono">${timeStr(e.ts)}</td>
    <td>${esc(devName(d) || e.device_id)}<div class="muted small mono">${esc(e.client_ip)}</div></td>
    <td style="word-break:break-all">${esc(e.domain)}</td>
    <td class="hide-sm muted">${esc(e.qtype)}</td>
    <td><span class="badge ${esc(e.status)}">${esc(e.status)}</span>${/^(list|seen):(adult|gambling|malware)$/.test(e.reason) ? ` <span class="badge flag">${esc(e.reason.split(':')[1])}</span>` : ` <span class="muted small">${esc(e.reason)}</span>`}</td>
    <td>${e.reason === 'paused' ? '' : `<button class="link" data-rule="${action}" data-domain="${esc(e.domain)}" data-group="${gid}">${action}</button>`}</td>
  </tr>`;
}

function groupForm(g) {
  const cats = state.catalog.categories, svcs = state.catalog.services;
  const devCount = state.devices.filter(d => d.group_id === g.id).length;
  const chip = (name, val, on, label = val) => `<label class="chip"><input type="checkbox" name="${name}" value="${esc(val)}" ${on ? 'checked' : ''}>${esc(label)}</label>`;
  const svcRows = state.catalog.service_categories.map(cat => {
    const inCat = svcs.filter(s => s.category === cat);
    return inCat.length ? `<div class="svc-row"><span class="svc-cat">${esc(cat)}</span><div class="chips">${inCat.map(s => chip('svc', s.key, g.services.includes(s.key), s.label)).join('')}</div></div>` : '';
  }).join('');
  return `<form class="panel" data-gid="${g.id}" style="margin-bottom:12px">
    <div class="toolbar">
      <input name="name" value="${esc(g.name)}" placeholder="Group name" required style="font-weight:600">
      <span class="muted small">${g.id ? devCount + ' device(s)' : 'new'}</span>
      <span class="spacer"></span>
      ${g.id === 1 ? '' : '<button type="button" class="danger" data-act="delete">Delete</button>'}
      <button class="primary">Save</button>
    </div>
    <div class="field"><label>Block categories (from blocklists)</label><div class="chips">${cats.map(c => chip('cat', c, g.categories.includes(c))).join('')}</div></div>
    <div class="field"><label>Block apps &amp; services</label>${svcRows}</div>
    <div class="field chips">
      <label class="chip"><input type="checkbox" name="safe_search" ${g.safe_search ? 'checked' : ''}>Force SafeSearch (Google, Bing, DuckDuckGo, YouTube Restricted)</label>
      <label class="chip"><input type="checkbox" name="block_doh" ${g.block_doh ? 'checked' : ''}>Block encrypted-DNS bypass (DoH)</label>
    </div>
    <div class="grid2">
      <div class="field"><label>Always block (one domain per line, subdomains included)</label><textarea name="custom_block">${esc(g.custom_block.join('\n'))}</textarea></div>
      <div class="field"><label>Always allow (overrides everything above)</label><textarea name="custom_allow">${esc(g.custom_allow.join('\n'))}</textarea></div>
    </div>
  </form>`;
}

let summaryHours = 24;

function appCard(a, groupId, groupName) {
  const state = a.blocked === a.lookups ? 'blocked' : a.restricted > 0 && a.blocked === 0 ? 'restricted' : a.blocked > 0 ? 'partly' : 'allowed';
  const text = {
    blocked: `Blocked · tried ${fmt(a.lookups)}×`,
    restricted: 'Allowed · Restricted Mode',
    partly: `Partly blocked · ${fmt(a.blocked)} of ${fmt(a.lookups)}`,
    allowed: `Used · ${fmt(a.lookups)} lookups`,
  }[state] + (a.blocked_by_rule && state !== 'blocked' ? ' · <b>now blocked</b>' : '');
  const btn = a.blocked_by_rule
    ? `<button class="link" data-svc="${esc(a.key)}" data-block="0" data-group="${groupId}" title="Unblock ${esc(a.label)} for the ${esc(groupName)} group">Unblock</button>`
    : `<button class="link danger" data-svc="${esc(a.key)}" data-block="1" data-group="${groupId}" title="Block ${esc(a.label)} for the ${esc(groupName)} group">Block</button>`;
  return `<div class="app-card ${state}"><div><b>${esc(a.label)}</b> <span class="muted small">${esc(a.category)}</span></div>
    <div class="small app-state">${text}</div><div>${btn}</div></div>`;
}

async function deviceDetails(id) {
  const d = deviceById(id);
  const enc = encodeURIComponent(id);
  const [sum, st, recent] = await Promise.all([
    api(`/devices/${enc}/summary?hours=${summaryHours}`),
    api(`/devices/${enc}/stats`),
    api('/queries?' + new URLSearchParams({ device: id, limit: 40 })),
  ]);
  const flaggedAllowed = sum.flagged.filter(f => f.blocked < f.lookups);
  const periods = [[24, 'Last 24 hours'], [168, 'Last 7 days']];
  openModal(`
    <div class="toolbar"><h2 style="margin:0">${esc(devName(d))}</h2><span class="badge">${esc(sum.group_name || 'Default')}</span>
      ${sum.paused ? '<span class="badge blocked">internet paused</span>' : ''}<span class="spacer"></span>
      <select id="sum-hours">${periods.map(([h, l]) => `<option value="${h}" ${h === summaryHours ? 'selected' : ''}>${l}</option>`).join('')}</select>
      <a class="small" href="#devices/${encodeURIComponent(id)}" title="Link to this summary (bookmark it)">link</a>
      <button onclick="closeModal()">Close</button></div>

    ${sum.flagged.length ? `<div class="alert ${flaggedAllowed.length ? 'bad' : ''}">
      <b>${flaggedAllowed.length ? '⚠ Visited' : 'Tried to visit'} ${sum.flagged.length} flagged site${sum.flagged.length > 1 ? 's' : ''}</b>${flaggedAllowed.length ? `: <b>${flaggedAllowed.length} not blocked</b> by the ${esc(sum.group_name)} group.` : ', all blocked.'}
      <div class="chips" style="margin-top:6px">${sum.flagged.map(f => `<span class="chip"><span class="badge flag">${esc(f.category)}</span> ${esc(f.site)}
        <span class="muted small">${f.blocked === f.lookups ? 'blocked' : 'allowed'}</span>
        ${f.blocked < f.lookups ? `<button class="link" data-rule="block" data-domain="${esc(f.site)}" data-group="${sum.group_id}">block</button>` : ''}</span>`).join('')}</div>
    </div>` : `<div class="alert good">No adult, gambling or malware sites in this period.</div>`}

    <h3 style="margin-top:16px">Apps &amp; services</h3>
    ${sum.apps.length ? `<div class="app-grid">${sum.apps.map(a => appCard(a, sum.group_id, sum.group_name)).join('')}</div>`
      : '<p class="muted">No recognised apps in this period.</p>'}

    <h3 style="margin-top:16px">Other sites</h3>
    ${sum.sites.length ? `<div class="chips">${sum.sites.map(s => `<span class="chip">${esc(s.site)} <span class="muted small">${fmt(s.lookups)}</span>
      ${s.blocked === s.lookups ? '<span class="badge blocked">blocked</span>'
        : `<button class="link" data-rule="block" data-domain="${esc(s.site)}" data-group="${sum.group_id}" title="Block ${esc(s.site)} for the ${esc(sum.group_name)} group">block</button>`}</span>`).join('')}</div>`
      : '<p class="muted">No other sites.</p>'}
    <p class="muted small">${fmt(sum.lookups)} lookups in total. ${fmt(sum.background)} background lookups (ads, trackers, system services) are hidden.
      Blocking here applies to everyone in the <b>${esc(sum.group_name)}</b> group.</p>

    <details style="margin-top:12px"><summary class="muted">Full activity: hourly chart, top domains, recent lookups</summary>
      <div class="panel" style="margin-top:8px"><div class="toolbar"><h3>Last 24 hours</h3><span class="spacer"></span>${legend}</div>${chart(st.hourly)}</div>
      <div class="grid2">
        <div class="panel"><h3>Most visited</h3>${domainList(st.top_allowed, d.group_id, 'block')}</div>
        <div class="panel"><h3>Most blocked</h3>${domainList(st.top_blocked, d.group_id, 'allow')}</div>
      </div>
      <div class="panel" style="margin-top:12px"><h3>Recent lookups</h3><table>${recent.map(logRow).join('')}</table></div>
    </details>`);

  document.getElementById('sum-hours').onchange = e => { summaryHours = Number(e.target.value); deviceDetails(id); };
  document.querySelectorAll('#modal-body [data-svc]').forEach(b => b.onclick = async () => {
    const block = b.dataset.block === '1';
    try {
      await api(`/groups/${b.dataset.group}/service`, { method: 'POST', body: { service: b.dataset.svc, block } });
      toast(`${block ? 'Blocked' : 'Unblocked'} for the ${sum.group_name} group`);
      state.groups = await api('/groups');
      deviceDetails(id);
    } catch (err) { toast(err.message); }
  });
}

function editDevice(id) {
  const d = deviceById(id);
  openModal(`
    <h2>Edit device</h2>
    <form id="edit-form">
      <div class="field"><label>Name</label><input name="name" value="${esc(d.name)}" placeholder="${esc(d.hostname || 'e.g. Emma\'s laptop')}" style="width:100%"></div>
      <div class="field"><label>Reserved IP (DHCP, optional)</label><input name="static_ip" value="${esc(d.static_ip)}" placeholder="192.168.50.20" style="width:100%"></div>
      <p class="muted small">Network name: ${esc(d.hostname || '—')}${d.hostname_source ? ` (from ${esc(d.hostname_source)})` : ''}
        ${d.local_name ? ` · reachable as <span class="mono">${esc(d.local_name)}</span>` : ''} · first seen ${ago(d.first_seen)}</p>
      <div class="field"><label>Addresses this device has used</label>
        <table>${d.identifiers.map(i => `<tr>
          <td class="mono">${esc(i.identifier)}</td>
          <td>${i.private ? '<span class="badge" title="Randomized private Wi-Fi address">private</span>' : ''}</td>
          <td class="mono muted">${esc(i.last_ip)}</td><td class="muted">${ago(i.last_seen)}</td>
          <td>${d.identifiers.length > 1 ? `<button type="button" class="link" data-detach="${esc(i.identifier)}" title="Split this address off into its own device">split off</button>` : ''}</td>
        </tr>`).join('')}</table></div>
      <div class="field"><label>Same physical device as another entry? Merge this one into it (keeps the other's name, group and rules)</label>
        <div class="toolbar" style="margin:0"><select id="merge-into"><option value="">Choose device…</option>
          ${state.devices.filter(o => o.id !== d.id).map(o => `<option value="${esc(o.id)}">${esc(devName(o))}</option>`).join('')}</select>
          <button type="button" id="merge-btn">Merge</button></div></div>
      <div class="toolbar"><button type="button" class="danger" id="del-dev">Forget device</button><span class="spacer"></span>
        <button type="button" onclick="closeModal()">Cancel</button><button class="primary">Save</button></div>
    </form>`);
  const f = document.getElementById('edit-form');
  document.getElementById('merge-btn').onclick = () => {
    const into = document.getElementById('merge-into').value;
    if (into) mergeDevice(id, into);
  };
  f.querySelectorAll('[data-detach]').forEach(b => b.onclick = async () => {
    try {
      await api('/identifiers/' + encodeURIComponent(b.dataset.detach), { method: 'DELETE' });
      toast('Address split off; it will appear as its own device on its next query');
      closeModal(); pages.devices();
    } catch (err) { toast(err.message); }
  });
  f.onsubmit = async e => {
    e.preventDefault();
    try {
      await api('/devices/' + encodeURIComponent(id), { method: 'PUT', body: { name: f.elements.name.value, static_ip: f.elements.static_ip.value } });
      closeModal(); pages.devices();
    } catch (err) { toast(err.message); }
  };
  document.getElementById('del-dev').onclick = async () => {
    if (!confirm('Forget this device? It will reappear (in the default group) next time it makes a query.')) return;
    await api('/devices/' + encodeURIComponent(id), { method: 'DELETE' });
    closeModal(); pages.devices();
  };
}

let adsHours = 168;

function adChart(series, bucket, hours) {
  const n = bucket === 3600 ? hours : Math.round(hours / 24);
  // Same bucketing as the server: floor((now + utcOffset) / bucket) * bucket - utcOffset.
  const off = bucket === 3600 ? 0 : -new Date().getTimezoneOffset() * 60;
  const now = Math.floor(Date.now() / 1000);
  const end = Math.floor((now + off) / bucket) * bucket - off;
  const byT = new Map(series.map(s => [s.t, s]));
  const cols = [];
  let max = 1;
  for (let i = n - 1; i >= 0; i--) {
    const t = end - i * bucket;
    const s = byT.get(t) || { blocked: 0, allowed: 0 };
    cols.push({ t, ...s });
    max = Math.max(max, s.blocked + s.allowed);
  }
  const label = t => bucket === 3600
    ? new Date(t * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
    : new Date(t * 1000).toLocaleDateString([], { weekday: 'short', month: 'short', day: 'numeric' });
  return `<div class="chart">${cols.map(c => `<div class="col" title="${label(c.t)}: ${fmt(c.blocked)} blocked, ${fmt(c.allowed)} not blocked">
      <div class="b" style="height:${c.blocked / max * 100}%"></div>
      <div style="background:var(--warn);height:${c.allowed / max * 100}%"></div></div>`).join('')}</div>
    <div class="chart-axis"><span>${label(cols[0].t)}</span><span>${bucket === 3600 ? 'now' : 'today'}</span></div>`;
}

function companyTable(companies) {
  if (!companies.length) return '<p class="muted">Nothing yet.</p>';
  return `<table><tr><th>Company / domain</th><th class="num">Blocked</th><th class="num">Not blocked</th><th class="hide-sm">Busiest hostnames</th><th>Devices</th></tr>
    ${companies.map(c => `<tr><td><b>${esc(c.company)}</b></td><td class="num">${fmt(c.blocked)}</td>
      <td class="num" style="color:${c.allowed ? 'var(--warn)' : 'inherit'}">${fmt(c.allowed)}</td>
      <td class="hide-sm mono" style="word-break:break-all">${c.domains.map(esc).join('<br>')}</td>
      <td class="small">${c.devices.map(id => esc(devName(deviceById(id)) || id)).join(', ')}</td></tr>`).join('')}</table>`;
}

async function adsDetails(id) {
  const d = deviceById(id);
  const a = await api('/ads?' + new URLSearchParams({ hours: adsHours, device: id }));
  openModal(`
    <div class="toolbar"><h2 style="margin:0">${esc(devName(d) || id)}: ads &amp; tracking</h2><span class="spacer"></span><button onclick="closeModal()">Close</button></div>
    <div class="cards">
      <div class="card"><div class="label">Blocked</div><div class="value">${fmt(a.blocked)}</div></div>
      <div class="card"><div class="label">Not blocked</div><div class="value">${fmt(a.allowed)}</div></div>
      <div class="card"><div class="label">Share of its lookups</div><div class="value">${pct(a.blocked + a.allowed, a.queries)}</div></div>
    </div>
    <div class="panel">${adChart(a.series, a.bucket, a.hours)}</div>
    <div class="panel" style="margin-top:12px"><h3>Who it's talking to</h3>${companyTable(a.companies)}</div>`);
}

async function mergeDevice(src, into) {
  const a = deviceById(src), b = deviceById(into);
  if (!confirm(`Merge "${devName(a)}" into "${devName(b)}"?\n\nAll of its addresses and history move to "${devName(b)}", which keeps its name, group and rules.`)) return;
  try {
    await api('/devices/' + encodeURIComponent(src) + '/merge', { method: 'POST', body: { into } });
    toast('Devices merged');
    closeModal(); pages.devices();
  } catch (err) { toast(err.message); }
}

// ---------- router ----------

async function route() {
  // "#devices/<id>" opens that device's summary (bookmarkable).
  const [page = 'overview', arg] = (location.hash.slice(1) || 'overview').split(/\/(.*)/s);
  document.querySelectorAll('nav a').forEach(a => a.classList.toggle('active', a.dataset.page === page));
  clearInterval(timer);
  closeModal();
  try {
    const refresh = await (pages[page] || pages.overview)();
    if (typeof refresh === 'function') {
      timer = setInterval(() => { if (document.getElementById('modal').classList.contains('hidden')) refresh(); }, page === 'log' ? 3000 : 15000);
    }
    if (page === 'devices' && arg) await deviceDetails(decodeURIComponent(arg));
  } catch (err) {
    app.innerHTML = `<div class="warn-box">Error: ${esc(err.message)}</div>`;
  }
}

window.addEventListener('hashchange', route);
route();
