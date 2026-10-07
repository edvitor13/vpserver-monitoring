/* VPServer — interface do painel. JS puro + uPlot, sem build.
   Todo texto vindo do servidor passa por esc() antes de ir para o HTML. */
'use strict';
(() => {
  // ------------------------------------------------------------------ utilidades
  const $ = (s, el = document) => el.querySelector(s);
  const $$ = (s, el = document) => [...el.querySelectorAll(s)];
  const ESC = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
  const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ESC[c]);
  const NF = [0, 1, 2].map((d) => new Intl.NumberFormat('pt-BR', { maximumFractionDigits: d }));
  const num = (v, d = 1) => (v == null || isNaN(v) ? '—' : NF[d].format(v));
  const pad = (n) => String(n).padStart(2, '0');
  const store = {
    get(k, def) { try { const v = localStorage.getItem('vpmon-' + k); return v == null ? def : JSON.parse(v); } catch { return def; } },
    set(k, v) { try { localStorage.setItem('vpmon-' + k, JSON.stringify(v)); } catch { /* sem storage */ } },
  };

  function scaled(v, k, units) {
    if (v == null || isNaN(v)) return '—';
    let i = 0;
    while (Math.abs(v) >= k && i < units.length - 1) { v /= k; i++; }
    const d = i === 0 || Math.abs(v) >= 100 ? 0 : Math.abs(v) >= 10 ? 1 : 2;
    return num(v, d) + ' ' + units[i];
  }
  // memória e disco: base 1024 (como o `free -h`); banda: base 1000 (como a Oracle cobra)
  const bytes = (v) => scaled(v, 1024, ['B', 'KB', 'MB', 'GB', 'TB']);
  const data = (v) => scaled(v, 1000, ['B', 'KB', 'MB', 'GB', 'TB', 'PB']);
  const rate = (v) => data(v) + '/s';
  const pct = (v) => (v == null || isNaN(v) ? '—' : num(v, v > 0 && v < 10 ? 1 : 0) + '%');
  function dur(s) {
    s = Math.max(0, Math.floor(s || 0));
    const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
    if (d) return `${d} d ${h} h`;
    if (h) return `${h} h ${m} min`;
    return `${m} min`;
  }
  function ago(ts) {
    if (!ts) return '—';
    const s = Date.now() / 1000 - ts;
    if (s < 60) return 'agora';
    if (s < 3600) return `há ${Math.floor(s / 60)} min`;
    if (s < 86400 * 2) return `há ${Math.floor(s / 3600)} h`;
    return `há ${Math.floor(s / 86400)} dias`;
  }
  const dt = (ts) => { const d = new Date(ts * 1000); return `${pad(d.getDate())}/${pad(d.getMonth() + 1)} ${pad(d.getHours())}:${pad(d.getMinutes())}`; };
  const hms = (ms) => { const d = new Date(ms); return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`; };
  const cssVar = (n) => getComputedStyle(document.documentElement).getPropertyValue(n).trim();
  function colorOf(idx) {
    if (idx >= 0 && idx < 8) return cssVar('--s' + (idx + 1));
    return cssVar(idx === -1 ? '--s-sys' : idx === -2 ? '--s-kernel' : '--s-other');
  }
  function mix(a, b, t) {
    const p = (h) => (/^#[0-9a-f]{6}$/i.test(h) ? [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16)) : null);
    const x = p(a), y = p(b);
    if (!x || !y) return a;
    return '#' + x.map((v, i) => Math.round(v * (1 - t) + y[i] * t).toString(16).padStart(2, '0')).join('');
  }
  const level = (p, warn, crit) => (p >= crit ? 'crit' : p >= warn ? 'warn' : '');

  // ------------------------------------------------------------------ ícones
  const P = {
    logo: '<polyline points="2 13 6 13 9 5 14 19 17 10 19 14 22 14"/>',
    cpu: '<rect x="4" y="4" width="16" height="16" rx="2"/><rect x="9" y="9" width="6" height="6"/><path d="M9 1v3M15 1v3M9 20v3M15 20v3M20 9h3M20 14h3M1 9h3M1 14h3"/>',
    mem: '<rect x="2" y="6" width="20" height="12" rx="2"/><path d="M6 10v4M10 10v4M14 10v4M18 10v4"/>',
    disk: '<path d="M22 12H2"/><path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z"/><path d="M6 16h.01M10 16h.01"/>',
    net: '<path d="M7 17V5M3 9l4-4 4 4M17 7v12M13 15l4 4 4-4"/>',
    up: '<path d="M12 19V5M5 12l7-7 7 7"/>',
    load: '<path d="M12 14l4-4"/><path d="M3.3 19a10 10 0 1 1 17.4 0"/>',
    ok: '<polyline points="20 6 9 17 4 12"/>',
    warn: '<path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z"/><path d="M12 9v4M12 17h.01"/>',
    crit: '<polygon points="7.86 2 16.14 2 22 7.86 22 16.14 16.14 22 7.86 22 2 16.14 2 7.86 7.86 2"/><path d="M15 9l-6 6M9 9l6 6"/>',
    info: '<circle cx="12" cy="12" r="10"/><path d="M12 16v-4M12 8h.01"/>',
    pause: '<circle cx="12" cy="12" r="10"/><path d="M10 15V9M14 15V9"/>',
    sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>',
    moon: '<path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/>',
    logout: '<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><path d="M21 12H9"/>',
    x: '<path d="M18 6 6 18M6 6l12 12"/>',
    logs: '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/><path d="M8 13h8M8 17h6"/>',
    refresh: '<polyline points="23 4 23 10 17 10"/><path d="M20.5 15a9 9 0 1 1-2.1-9.4L23 10"/>',
    clock: '<circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/>',
    key: '<circle cx="7.5" cy="15.5" r="5.5"/><path d="m21 2-9.6 9.6M15.5 7.5l3 3L22 7l-3-3"/>',
    gear: '<path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/>',
    search: '<circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/>',
    spark: '<path d="M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9z"/><path d="M19 15l.8 2.2L22 18l-2.2.8L19 21l-.8-2.2L16 18l2.2-.8z"/>',
    send: '<path d="M22 2 11 13"/><path d="M22 2 15 22l-4-9-9-4z"/>',
    stop: '<rect x="6" y="6" width="12" height="12" rx="2"/>',
    grid: '<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>',
    box: '<path d="M21 8 12 3 3 8v8l9 5 9-5z"/><path d="m3 8 9 5 9-5M12 13v8"/>',
    term: '<polyline points="4 17 10 11 4 5"/><path d="M12 19h8"/>',
    server: '<rect x="2" y="3" width="20" height="8" rx="2"/><rect x="2" y="13" width="20" height="8" rx="2"/><path d="M6 7h.01M6 17h.01"/>',
    bell: '<path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9"/><path d="M10.3 21a1.94 1.94 0 0 0 3.4 0"/>',
    more: '<circle cx="5" cy="12" r="1.5"/><circle cx="12" cy="12" r="1.5"/><circle cx="19" cy="12" r="1.5"/>',
    whats: '<path d="M21 11.5a8.4 8.4 0 0 1-12.4 7.4L3 21l2.1-5.4A8.4 8.4 0 1 1 21 11.5z"/>',
    users: '<path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 0 0-3-3.87M16 3.13a4 4 0 0 1 0 7.75"/>',
    phone: '<rect x="6" y="2" width="12" height="20" rx="2"/><path d="M11 18h2"/>',
    lock: '<rect x="4" y="11" width="16" height="10" rx="2"/><path d="M8 11V7a4 4 0 0 1 8 0v4"/>',
    qr: '<rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><path d="M14 14h3v3h-3zM20 14v.01M14 20h.01M17 20h4v-3"/>',
    plus: '<path d="M12 5v14M5 12h14"/>',
    play: '<polygon points="6 4 20 12 6 20 6 4"/>',
    user: '<circle cx="12" cy="8" r="4"/><path d="M4 21v-1a6 6 0 0 1 6-6h4a6 6 0 0 1 6 6v1"/>',
  };
  const icon = (n, cls = 'i') => `<svg class="${cls}" viewBox="0 0 24 24" aria-hidden="true">${P[n] || ''}</svg>`;
  const STATUS = { ok: ['ok', 'OK'], warn: ['warn', 'Atenção'], crit: ['crit', 'Crítico'], info: ['info', 'Info'], off: ['pause', 'Parado'] };
  function badge(lvl, label) {
    const k = lvl === 'stopped' ? 'off' : lvl;
    const [ic, def] = STATUS[k] || STATUS.info;
    return `<span class="badge ${k}">${icon(ic)}${esc(label || def)}</span>`;
  }
  function containerBadge(c) {
    if (!c) return '';
    if (c.state === 'running') {
      if (c.health === 'unhealthy') return badge('crit', 'Unhealthy');
      if (c.health === 'starting') return badge('info', 'Iniciando');
      return badge('ok', c.health === 'healthy' ? 'Saudável' : 'Rodando');
    }
    if (c.state === 'restarting') return badge('crit', 'Reiniciando');
    if (c.state === 'paused') return badge('off', 'Pausado');
    return badge('off', 'Parado');
  }
  function appBadge(a) {
    switch (a.status) {
      case 'crit': return badge('crit', `${a.unhealthy} com problema`);
      case 'warn': return badge('warn', `${a.running}/${a.total} no ar`);
      case 'stopped': return badge('off', `0/${a.total} no ar`);
      case 'paused': return badge('info', 'Pausada');
      default: return a.total ? badge('ok', `${a.running}/${a.total} no ar`) : '';
    }
  }
  // de onde vem o número de instâncias (ex.: "api ×2")
  const apiTitle = (a) => (a.components || []).filter((c) => c.api).map((c) => (c.count > 1 ? `${c.name} ×${c.count}` : c.name)).join(', ');
  const diskOf = (d) => (d && d.total ? bytes(d.total) : '—');
  function diskParts(d) {
    if (!d || !d.total) return 'medindo…';
    return [['imagens', d.images], ['volumes', d.volumes], ['logs', d.logs], ['camada dos contêineres', d.layer]]
      .filter(([, v]) => v > 0).map(([l, v]) => `${l} ${bytes(v)}`).join(' · ');
  }
  const swatch = (color) => `<i class="swatch" style="background:${esc(color)}"></i>`;

  // ------------------------------------------------------------------ API
  async function api(path, opts = {}) {
    const headers = { 'X-Requested-With': 'vpmon' };
    if (opts.body) headers['Content-Type'] = 'application/json';
    const r = await fetch(path, { credentials: 'same-origin', ...opts, headers });
    const j = await r.json().catch(() => ({}));
    if (r.status === 401 && path !== '/api/login') {
      showLogin();
      throw new Error('login');
    }
    if (r.status === 403 && j.error && j.error.code === 'password_change_required') {
      showSetup();
      throw new Error('login');
    }
    if (!r.ok) throw new Error((j.error && j.error.message) || `Erro ${r.status}`);
    return j;
  }
  let toastT;
  function toast(msg) {
    let t = $('.toast');
    if (!t) { t = document.createElement('div'); t.className = 'toast'; t.setAttribute('role', 'status'); document.body.append(t); }
    t.textContent = msg;
    clearTimeout(toastT);
    toastT = setTimeout(() => t.remove(), 3500);
  }

  // ------------------------------------------------------------------ estado
  const S = {
    ov: null,
    tab: 'overview',
    range: store.get('range', '1h'),
    cleanup: [], // charts e timers da aba atual
    drawer: null,
    logs: Object.assign({ c: '*', tail: 300, q: '', errors: false, live: false, wrap: true }, store.get('logs', {})),
    procSort: 'cpu',
    lastOk: 0,
  };
  const RANGES = [['1h', '1 h'], ['6h', '6 h'], ['24h', '24 h'], ['7d', '7 dias'], ['30d', '30 dias'], ['1y', '1 ano']];
  const refreshFor = (r) => (r === '1h' ? 30000 : r === '6h' ? 60000 : 120000);
  // atualização periódica da aba atual; com a aba do navegador escondida, não baixa nada
  function later(fn, ms) { const id = setInterval(() => { if (!document.hidden) fn(); }, ms); S.cleanup.push(() => clearInterval(id)); }
  function teardown() { S.cleanup.splice(0).forEach((f) => { try { f(); } catch { /* ignora */ } }); }

  // ------------------------------------------------------------------ gráficos (uPlot)
  function xTicks(u, splits, ai, space, incr) {
    return splits.map((ts) => {
      const d = new Date(ts * 1000);
      if (incr >= 86400 || (d.getHours() === 0 && d.getMinutes() === 0 && incr >= 3600)) return `${pad(d.getDate())}/${pad(d.getMonth() + 1)}`;
      if (incr < 60) return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
      return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
    });
  }
  function stackCols(cols) {
    const n = cols.length ? cols[0].length : 0;
    const acc = new Array(n).fill(0);
    const empty = new Array(n).fill(true);
    cols.forEach((c) => c.forEach((v, i) => { if (v != null) empty[i] = false; }));
    return cols.map((c) => c.map((v, i) => (empty[i] ? null : (acc[i] += v || 0))));
  }

  /* cfg: { series: [{label, color}], fmt(v), stacked, fill, height, softMax }
     Empilhado: as séries vêm de baixo para cima; são desenhadas de cima para
     baixo, cada uma preenchendo até o zero, e a de baixo cobre a de cima. */
  function makeChart(el, cfg) {
    let u = null, raw = null, sig = '', lastT = null;
    const lastIdx = (col) => { for (let i = col.length - 1; i >= 0; i--) if (col[i] != null) return i; return null; };
    const H = cfg.height || (window.innerWidth < 560 ? 170 : 200);
    function build(t, cols) {
      const surface = cssVar('--surface'), muted = cssVar('--muted'), grid = cssVar('--grid');
      const order = cfg.series.map((_, i) => i);
      if (cfg.stacked) order.reverse();
      const series = [{ value: (_, ts) => (ts == null ? (lastT ? 'agora · ' + dt(lastT) : '—') : dt(ts)) }];
      for (const i of order) {
        const s = cfg.series[i];
        series.push({
          label: s.label,
          stroke: s.color,
          width: cfg.stacked ? 1.5 : 2,
          fill: cfg.stacked ? mix(s.color, surface, 0.42) : cfg.fill ? s.color + '1f' : undefined,
          points: { show: false },
          value: (_, v, si, idx) => {
            if (!raw) return '—';
            const col = raw[order[si - 1]];
            const i = idx == null ? lastIdx(col) : idx;
            const r = i == null ? null : col[i];
            return r == null ? '—' : cfg.fmt(r);
          },
        });
      }
      const data = [t, ...order.map((i) => cols[i])];
      u = new uPlot({
        width: Math.max(200, el.clientWidth), height: H, series,
        scales: { x: { time: true }, y: { range: (_, mn, mx) => [0, Math.max(cfg.softMax || 0, (mx || 0) * 1.12) || 1] } },
        axes: [
          { stroke: muted, grid: { stroke: grid, width: 1 }, ticks: { show: false }, values: xTicks, font: '11px system-ui', space: 64 },
          { stroke: muted, grid: { stroke: grid, width: 1 }, ticks: { show: false }, values: (_, v) => v.map((x) => cfg.fmt(x)), size: 62, font: '11px system-ui', space: 34 },
        ],
        cursor: { drag: { x: false, y: false }, points: { size: 8, width: 2, stroke: surface } },
        legend: { live: true },
      }, data, el);
    }
    const ro = new ResizeObserver(() => u && u.setSize({ width: Math.max(200, el.clientWidth), height: H }));
    ro.observe(el);
    const api = {
      set(t, cols) {
        raw = cols;
        lastT = t.length ? t[t.length - 1] : null;
        const plotted = cfg.stacked ? stackCols(cols) : cols;
        const s = cfg.series.map((x) => x.label + x.color).join('|');
        if (!t.length) {
          if (u) { u.destroy(); u = null; }
          el.innerHTML = '<div class="chart-empty">Coletando dados…</div>';
          sig = '';
          return;
        }
        if (u && s === sig) {
          const order = cfg.series.map((_, i) => i);
          if (cfg.stacked) order.reverse();
          u.setData([t, ...order.map((i) => plotted[i])]);
          return;
        }
        if (u) u.destroy();
        el.innerHTML = '';
        sig = s;
        build(t, plotted);
      },
      setSeries(series) { cfg.series = series; },
      destroy() { ro.disconnect(); if (u) u.destroy(); u = null; },
    };
    S.cleanup.push(api.destroy);
    return api;
  }

  function rangeSeg(cur, act = 'range', opts = RANGES) {
    return `<div class="seg" role="group" aria-label="Período">${opts.map(([v, l]) =>
      `<button type="button" data-act="${act}" data-v="${v}" aria-pressed="${v === cur}">${l}</button>`).join('')}</div>`;
  }
  function chartCard(id, title, sub) {
    return `<div class="card chart-card"><h3>${esc(title)}</h3><div class="sub">${esc(sub)}</div><div class="chart" id="${id}"><div class="chart-empty"><div class="spinner"></div></div></div></div>`;
  }

  // ------------------------------------------------------------------ login
  function showLogin(msg) {
    teardown();
    closeDrawer();
    S.ov = null;
    clearTimeout(pollT);
    $('#app').innerHTML = `
      <div class="login"><div class="card login-card">
        <div class="brand"><div class="brand-logo">${icon('logo')}</div>
          <div class="brand-txt"><div class="brand-name">VPServer</div><div class="brand-sub">Monitoramento do servidor</div></div></div>
        <form id="login-form" autocomplete="on">
          <div class="field"><label for="lu">Usuário</label><input class="input" id="lu" name="username" autocomplete="username" required></div>
          <div class="field"><label for="lp">Senha</label><input class="input" id="lp" name="password" type="password" autocomplete="current-password" required></div>
          <div class="form-err" id="login-err" role="alert">${esc(msg || '')}</div>
          <button class="btn primary" type="submit">Entrar</button>
        </form>
      </div></div>`;
    $('#lu').focus();
    $('#login-form').addEventListener('submit', async (e) => {
      e.preventDefault();
      const btn = $('#login-form button');
      btn.disabled = true;
      try {
        const j = await api('/api/login', { method: 'POST', body: JSON.stringify({ user: $('#lu').value.trim(), password: $('#lp').value }) });
        if (j.mustChange) showSetup($('#lp').value);
        else start();
      } catch (err) {
        $('#login-err').textContent = err.message;
        btn.disabled = false;
        $('#lp').select();
      }
    });
  }

  // ------------------------------------------------------------------ casca
  // [chave, nome, ícone, nome curto (celular)]
  const TABS = [['overview', 'Visão geral', 'grid', 'Início'], ['infos', 'Infos', 'info', 'Infos'],
    ['apps', 'Aplicações', 'box', 'Apps'], ['traffic', 'Banda', 'net', 'Banda'], ['logs', 'Logs', 'term', 'Logs'],
    ['system', 'Sistema', 'server', 'Sistema'], ['limits', 'Limites', 'load', 'Limites'],
    ['ai', 'IA', 'spark', 'IA'], ['notify', 'Notificações', 'bell', 'Avisos']]; // IA e WhatsApp juntas, no fim
  const BNAV = ['overview', 'apps', 'infos', 'ai']; // no celular, o resto fica em "Mais"
  // o que o usuário logado pode (a API recusa do mesmo jeito; aqui só some da tela)
  const can = {
    admin: () => !!(S.me && S.me.admin),
    act: () => !!(S.me && (S.me.admin || S.me.actions)),
    manage: () => !!(S.me && (S.me.admin || S.me.manage)),
  };
  const roleText = (u) => (u.admin ? 'Administrador'
    : [u.actions && 'Ações nas apps', u.manage && 'Gerencia usuários'].filter(Boolean).join(' · ') || 'Só leitura');
  const visibleTabs = () => TABS.filter(([k]) => k !== 'notify' || can.admin());
  const tabHref = (k) => `#/${k === 'overview' ? '' : k}`;
  const countHTML = (k) => (k === 'infos' ? '<span class="count infos-count" hidden></span>' : '');
  function renderShell() {
    $('#app').innerHTML = `
      <header class="top"><div class="top-in">
        <div class="bar">
          <a class="brand" href="#/" style="text-decoration:none;color:inherit">
            <div class="brand-logo">${icon('logo')}</div>
            <div class="brand-txt"><div class="brand-name">VPServer</div><div class="brand-sub" id="srv-sub">carregando…</div></div>
          </a>
          <div class="bar-actions">
            <span id="hdr-status"></span>
            <span class="updated" id="hdr-upd"></span>
            <button class="who" type="button" data-act="settings" title="${esc(roleText(S.me || {}))} · Minha conta">${icon('user')}<span>${esc((S.me || {}).user || '')}</span></button>
            <button class="icon-btn" type="button" data-act="theme" aria-label="Trocar tema" title="Trocar tema">${icon(isDark() ? 'sun' : 'moon')}</button>
            <button class="icon-btn" type="button" data-act="settings" aria-label="Configurações" title="Configurações">${icon('gear')}</button>
            <button class="icon-btn" type="button" data-act="logout" aria-label="Sair" title="Sair">${icon('logout')}</button>
          </div>
        </div>
        <nav class="tabs" aria-label="Seções">${visibleTabs().map(([k, l, ic]) => `<a class="tab" href="${tabHref(k)}" data-tab="${k}">${icon(ic)}${l}${countHTML(k)}</a>`).join('')}</nav>
      </div></header>
      <main id="view"></main>
      <nav class="bnav" aria-label="Seções">${BNAV.map((k) => { const [, , ic, short] = TABS.find((t) => t[0] === k); return `<a class="bn" href="${tabHref(k)}" data-tab="${k}">${icon(ic)}<span>${short}</span>${countHTML(k)}</a>`; }).join('')}
        <button class="bn" type="button" data-act="more" id="bn-more">${icon('more')}<span>Mais</span></button></nav>`;
  }
  function isDark() {
    const t = document.documentElement.getAttribute('data-theme');
    return t ? t === 'dark' : matchMedia('(prefers-color-scheme: dark)').matches;
  }
  function renderHeader() {
    const o = S.ov;
    if (!o || !$('#srv-sub')) return;
    const s = o.server;
    const c = s.cloud || {};
    const where = c.provider === 'oracle' ? `Oracle ${c.regionName} · ${String(c.shape).replace('VM.Standard.', '')} ${num(c.ocpus, 0)} OCPU/${num(c.memGb, 0)} GB` : s.arch;
    $('#srv-sub').textContent = [s.name || s.hostname, where, s.os].filter(Boolean).join(' · ');
    const crit = o.alerts.filter((a) => a.level === 'crit').length;
    const warn = o.alerts.filter((a) => a.level === 'warn').length;
    $('#hdr-status').innerHTML = `<a href="#/infos" class="plain">${crit ? badge('crit', crit === 1 ? '1 urgente' : `${crit} urgentes`)
      : warn ? badge('warn', warn === 1 ? '1 alerta' : `${warn} alertas`) : badge('ok', 'Tudo certo')}</a>`;
    $$('.infos-count').forEach((cnt) => {
      cnt.hidden = !o.alerts.length;
      cnt.textContent = o.alerts.length;
      cnt.className = 'count infos-count ' + (crit ? 'crit' : warn ? 'warn' : 'info');
      cnt.title = `${crit} urgente(s), ${warn} alerta(s), ${o.alerts.length - crit - warn} informação(ões)`;
    });
    $('#hdr-upd').textContent = o.updated ? `atualizado ${hms(o.updated * 1000)}` : '';
    markTabs();
  }
  function markTabs() {
    $$('.tab, .bn[data-tab], .sheet-item[data-tab]').forEach((t) => t.setAttribute('aria-current', t.dataset.tab === S.tab ? 'page' : 'false'));
    const more = $('#bn-more');
    if (more) more.setAttribute('aria-current', BNAV.includes(S.tab) ? 'false' : 'page');
  }
  // "Mais" no celular: as outras seções, Configurações, tema e sair
  function openMore() {
    closeDrawer();
    const scrim = document.createElement('div');
    scrim.className = 'scrim';
    scrim.dataset.act = 'close';
    const sh = document.createElement('div');
    sh.className = 'sheet';
    sh.setAttribute('role', 'dialog');
    sh.setAttribute('aria-modal', 'true');
    sh.setAttribute('aria-label', 'Mais seções');
    sh.innerHTML = `<div class="sheet-grip"></div>
      <div class="sheet-who">${icon('user')}<span><b>${esc(S.me.user)}</b> · ${esc(roleText(S.me))}</span></div><div class="sheet-grid">
      ${visibleTabs().filter(([k]) => !BNAV.includes(k)).map(([k, l, ic]) => `<a class="sheet-item" href="${tabHref(k)}" data-tab="${k}">${icon(ic)}<span>${l}</span></a>`).join('')}
      </div><div class="sheet-sep"></div><div class="sheet-grid">
      <button class="sheet-item" type="button" data-act="settings">${icon('gear')}<span>Configurações</span></button>
      <button class="sheet-item" type="button" data-act="theme">${icon(isDark() ? 'sun' : 'moon')}<span>${isDark() ? 'Tema claro' : 'Tema escuro'}</span></button>
      <button class="sheet-item" type="button" data-act="logout">${icon('logout')}<span>Sair</span></button></div>`;
    document.body.append(scrim, sh);
    S.drawer = { update() {}, reload() {}, destroy() {} };
    markTabs();
  }

  // ------------------------------------------------------------------ roteamento
  function parseHash() {
    const h = location.hash.replace(/^#\/?/, '');
    const [tab, q] = h.split('?');
    return { tab: visibleTabs().some(([k]) => k === tab) ? tab : 'overview', params: new URLSearchParams(q || '') };
  }
  function route() {
    const { tab, params } = parseHash();
    teardown();
    stopWA();
    S.tab = tab;
    renderHeader();
    const v = $('#view');
    if (!v) return;
    window.scrollTo(0, 0);
    VIEWS[tab].mount(v, params);
    if (S.ov && VIEWS[tab].update) VIEWS[tab].update();
  }

  // ------------------------------------------------------------------ polling da visão geral
  let pollT;
  async function poll() {
    clearTimeout(pollT);
    if (document.hidden) return;
    try {
      S.ov = await api('/api/overview');
      S.lastOk = Date.now();
      renderHeader();
      const v = VIEWS[S.tab];
      if (v.update) v.update();
      if (S.drawer) S.drawer.update();
    } catch (e) {
      if (e.message === 'login') return;
      if (Date.now() - S.lastOk > 15000) toast('Sem conexão com o painel — tentando de novo…');
    }
    pollT = setTimeout(poll, 5000);
  }
  document.addEventListener('visibilitychange', () => { if (!document.hidden && $('#view')) poll(); });

  // ------------------------------------------------------------------ peças comuns
  function kpi(ic, label, value, sub, meter) {
    return `<div class="card kpi"><div class="kpi-label">${icon(ic)}${esc(label)}</div>
      <div class="kpi-value num">${value}</div>
      ${meter ? `<div class="meter"><i class="${meter[1] || ''}" style="width:${Math.min(100, Math.max(0, meter[0])).toFixed(1)}%"></i></div>` : ''}
      <div class="kpi-sub">${sub}</div></div>`;
  }
  function alertsHTML(alerts, emptyText = 'Tudo certo no servidor.') {
    if (!alerts.length) {
      return `<div class="alert ok"><div class="ic">${icon('ok')}</div><div class="alert-body"><div class="alert-t">${esc(emptyText)}</div>
        <div class="alert-d">Nenhum alerta de CPU, memória, disco, contêineres ou limites do plano grátis.</div></div></div>`;
    }
    const area = { host: 'Servidor', app: 'Aplicação', limite: 'Plano grátis', monitor: 'Monitor', disco: 'Disco' };
    const lbl = { crit: 'Urgente', warn: 'Alerta', info: 'Info' };
    return alerts.map((a) => `<div class="alert ${a.level}${a.target ? ' clickable' : ''}" ${a.target ? `data-unit="${esc(a.target)}"` : ''}>
      <div class="ic">${icon(a.level)}</div><div class="alert-body">
      <div class="alert-meta">${lbl[a.level]} · ${area[a.area] || ''}</div>
      <div class="alert-t">${esc(a.title)}</div>${a.detail ? `<div class="alert-d">${esc(a.detail)}</div>` : ''}
      ${a.items && a.items.length ? `<ul class="alert-items">${a.items.map((i) => `<li>${esc(i)}</li>`).join('')}</ul>` : ''}
      ${a.action ? `<div class="alert-action"><span class="m-l">O que fazer</span> <code>${esc(a.action)}</code></div>` : ''}</div></div>`).join('');
  }
  const appsOf = () => (S.ov ? S.ov.apps : []);
  function findUnit(key) {
    for (const a of appsOf()) for (const u of a.units) if (u.key === key) return { u, a };
    return null;
  }

  // ------------------------------------------------------------------ aba: visão geral
  const overview = {
    mount(v) {
      v.innerHTML = `<div class="page">
        <section class="alerts" id="ov-alerts"></section>
        <section class="kpis k6" id="ov-kpis"></section>
        <section class="card" id="ov-share"></section>
        <section>
          <div class="section-h"><div><h2>Histórico</h2><p>Médias por intervalo. Toque no gráfico para ver os valores.</p></div>${rangeSeg(S.range)}</div>
          <div class="charts c2">
            ${chartCard('ch-cpu', 'CPU', '% da máquina inteira, por tipo de uso')}
            ${chartCard('ch-mem', 'Memória', 'Em uso (sem cache) e cache do sistema')}
            ${chartCard('ch-acpu', 'CPU por aplicação', 'Quem usou o processador')}
            ${chartCard('ch-amem', 'Memória por aplicação', 'Quem ocupou a RAM')}
            ${chartCard('ch-net', 'Rede do servidor', 'Placa principal: o que a Oracle mede')}
            ${chartCard('ch-disk', 'Disco', 'Leitura e escrita')}
          </div>
        </section></div>`;
      const s = (k) => cssVar(k);
      const C = {
        cpu: makeChart($('#ch-cpu'), { stacked: true, fmt: pct, softMax: 10, series: [
          { label: 'Usuário', color: s('--s1') }, { label: 'Sistema', color: s('--s2') },
          { label: 'Espera de disco', color: s('--s3') }, { label: 'Steal (Oracle)', color: s('--s4') }] }),
        mem: makeChart($('#ch-mem'), { fmt: bytes, series: [{ label: 'Em uso', color: s('--s1') }, { label: 'Cache', color: s('--s3') }] }),
        net: makeChart($('#ch-net'), { fmt: rate, series: [{ label: 'Saída (envio)', color: s('--s2') }, { label: 'Entrada', color: s('--s1') }] }),
        disk: makeChart($('#ch-disk'), { fmt: rate, series: [{ label: 'Leitura', color: s('--s1') }, { label: 'Escrita', color: s('--s2') }] }),
        acpu: makeChart($('#ch-acpu'), { stacked: true, fmt: pct, softMax: 10, series: [] }),
        amem: makeChart($('#ch-amem'), { stacked: true, fmt: bytes, series: [] }),
      };
      const load = async () => {
        const r = S.range;
        try {
          const [h, ac, am] = await Promise.all([
            api(`/api/history/host?range=${r}&f=user,system,iowait,steal,mem,cache,tx,rx,rd,wr`),
            api(`/api/history/apps?range=${r}&f=cpu`),
            api(`/api/history/apps?range=${r}&f=mem`),
          ]);
          const c = h.s;
          C.cpu.set(h.t, [c.user, c.system, c.iowait, c.steal]);
          C.mem.set(h.t, [c.mem, c.cache]);
          C.net.set(h.t, [c.tx, c.rx]);
          C.disk.set(h.t, [c.rd, c.wr]);
          for (const [ch, res] of [[C.acpu, ac], [C.amem, am]]) {
            ch.setSeries(res.series.map((x) => ({ label: x.name, color: colorOf(x.color) })));
            ch.set(res.t, res.series.map((x) => x.v));
          }
        } catch (e) { if (e.message !== 'login') toast(e.message); }
      };
      load();
      later(load, refreshFor(S.range));
      overview.reload = () => { teardown(); route(); };
    },
    update() {
      const o = S.ov, h = o.host, t = o.traffic;
      const hot = o.alerts.filter((a) => a.level !== 'info');
      const nInfo = o.alerts.length - hot.length;
      $('#ov-alerts').innerHTML = hot.length ? alertsHTML(hot) + (nInfo ? `<a class="more-infos" href="#/infos">+ ${nInfo} informaç${nInfo === 1 ? 'ão' : 'ões'} na aba Infos →</a>` : '')
        : `<a class="alert ok slim plain" href="#/infos"><div class="ic">${icon('ok')}</div><div class="alert-body"><div class="alert-t">Nada urgente no servidor.</div>
          <div class="alert-d">${nInfo ? `${nInfo} informaç${nInfo === 1 ? 'ão' : 'ões'} na aba Infos (cache, logs, limites…) →` : 'Nenhum alerta nem informação pendente.'}</div></div></a>`;
      const memP = (h.memUsed / h.memTotal) * 100;
      const fsP = (h.fsUsed / h.fsTotal) * 100;
      const egP = (t.month.tx / t.limitBytes) * 100;
      $('#ov-kpis').innerHTML = [
        kpi('cpu', 'CPU', pct(h.cpu), `usuário ${pct(h.cpuUser)} · sistema ${pct(h.cpuSystem)} · steal ${pct(h.cpuSteal)}`, [h.cpu, level(h.cpu, 75, 90)]),
        kpi('mem', 'Memória em uso', `${bytes(h.memUsed)} <small>de ${bytes(h.memTotal)}</small>`, `+ ${bytes(memCache(h))} de cache (liberável) · livre ${bytes(h.memFree)}`, [memP, level(memP, 88, 95)]),
        kpi('disk', 'Disco', `${bytes(h.fsUsed)} <small>de ${bytes(h.fsTotal)}</small>`, o.storage.measured ? `${bytes(o.storage.buildCache + o.storage.unusedImages)} disso é cache do Docker (liberável) · livre ${bytes(h.fsAvail)}` : `livre ${bytes(h.fsAvail)} · E/S ${rate(h.diskRead + h.diskWrite)}`, [fsP, level(fsP, 80, 90)]),
        kpi('net', 'Rede agora', `↑ ${rate(h.netTx)}`, `↓ ${rate(h.netRx)} · placa ${esc(h.iface || '—')}`),
        kpi('up', 'Saída no mês', data(t.month.tx), `de 10 TB grátis · projeção ${t.projectedTx ? data(t.projectedTx) : '—'}`, [egP, level(egP, 75, 90)]),
        kpi('load', 'Carga', `${num(h.load1, 2)} <small>/ ${h.cores} núcleos</small>`, `5 min ${num(h.load5, 2)} · 15 min ${num(h.load15, 2)} · ligado há ${dur(h.uptime)}`),
      ].join('');
      $('#ov-share').innerHTML = shareHTML(o);
    },
  };

  // cache de memória que o kernel devolve quando precisa = disponível − livre
  const memCache = (h) => Math.max(0, h.memTotal - h.memUsed - h.memFree);
  function shareHTML(o) {
    const h = o.host, st = o.storage;
    const apps = o.apps.filter((a) => a.cpu > 0 || a.mem > 0 || a.total || (a.disk && a.disk.total));
    const seg = (val, total, color, title, cls = '') => {
      const w = (val / total) * 100;
      return w < 0.25 ? '' : `<i class="${cls}" style="width:${w.toFixed(2)}%;${color ? `background:${color}` : ''}" title="${esc(title)}"></i>`;
    };
    const cores = h.cores || 1;
    const real = apps.filter((a) => a.kind !== 'kernel');
    const diskApps = apps.filter((a) => a.disk && a.disk.total);
    const cacheDisk = st.buildCache + st.unusedImages;
    return `<div class="card-h"><div><h2>Quem está consumindo agora</h2>
        <div class="muted" style="font-size:.82rem;margin-top:2px">Cada barra é a máquina inteira. <b class="ink2">Listrado = cache</b>, que o sistema libera quando precisa; o fim vazio é o que está livre.</div></div></div>
      <div class="share">
        <div class="share-row"><span class="share-label">CPU</span><div class="share-bar">${apps.map((a) => seg(a.cpu, 100, colorOf(a.color), a.name)).join('')}</div>
          <span class="share-total num">${pct(h.cpu)} de ${cores} núcl.</span></div>
        <div class="share-row"><span class="share-label">Memória</span><div class="share-bar">${apps.map((a) => seg(a.mem, h.memTotal, colorOf(a.color), a.name)).join('')}${seg(memCache(h), h.memTotal, '', 'Cache de memória (liberável)', 'cache')}</div>
          <span class="share-total num">${bytes(h.memUsed)} + ${bytes(memCache(h))} cache</span></div>
        ${st.measured ? `<div class="share-row"><span class="share-label">Disco</span><div class="share-bar">${diskApps.map((a) => seg(a.disk.total, h.fsTotal, colorOf(a.color), a.name)).join('')}${seg(st.other, h.fsTotal, cssVar('--s-sys'), 'Sistema e outros')}${seg(cacheDisk, h.fsTotal, '', 'Cache do Docker (liberável)', 'cache')}</div>
          <span class="share-total num">${bytes(h.fsUsed)} · ${bytes(cacheDisk)} cache</span></div>` : ''}
      </div>
      <ul class="legend">${apps.map((a) => `<li>${swatch(colorOf(a.color))}<span class="n">${esc(a.name)}</span>
        <span class="v">${pct(a.cpu)} · ${bytes(a.mem)}${a.disk && a.disk.total ? ' · ' + bytes(a.disk.total) : ''}</span></li>`).join('')}
        ${st.measured ? `<li>${swatch(cssVar('--s-sys'))}<span class="n">Sistema e outros (disco)</span><span class="v">${bytes(st.other)}</span></li>` : ''}
        <li><i class="swatch cache"></i><span class="n">Cache de memória (liberável)</span><span class="v">${bytes(memCache(h))}</span></li>
        ${st.measured ? `<li><i class="swatch cache"></i><span class="n">Cache do Docker (liberável)</span><span class="v">${bytes(cacheDisk)}</span></li>` : ''}</ul>
      <div class="table-wrap" style="margin-top:14px"><table>
        <thead><tr><th>Aplicação</th><th>Containers</th><th class="r hide-sm" title="Quantas instâncias da API de cada app estão rodando">Instâncias</th><th class="r">CPU</th><th class="r">Memória</th><th class="r">Disco</th><th class="r hide-sm">Rede ↑ agora</th><th class="r hide-sm">Saída hoje</th><th class="r">Saída no mês</th></tr></thead>
        <tbody>${real.map((a) => { const sys = a.kind === 'system'; return `<tr class="clickable" data-unit="app:${esc(a.key)}">
          <td><div class="cell-name">${swatch(colorOf(a.color))}<span>${esc(a.name)}</span></div></td>
          <td>${sys ? `<span class="muted">${a.units.length} serviços</span>` : appBadge(a)}</td>
          <td class="r hide-sm" title="${esc(apiTitle(a))}">${sys || !a.api ? '<span class="muted">—</span>' : a.api}</td>
          <td class="r">${pct(a.cpu)}</td><td class="r">${bytes(a.mem)}</td>
          <td class="r">${sys ? '—' : diskOf(a.disk)}</td>
          <td class="r hide-sm">${sys ? '—' : rate(a.netTx)}</td>
          <td class="r hide-sm">${sys ? '—' : data(a.today.tx)}</td>
          <td class="r">${sys ? '—' : data(a.month.tx)}</td></tr>`; }).join('')}</tbody></table></div>`;
  }

  // ------------------------------------------------------------------ aba: aplicações
  const appsView = {
    mount(v) {
      v.innerHTML = `<div class="page"><div class="section-h"><div><h2>Aplicações</h2>
        <p>Descobertas sozinhas: cada projeto do Docker Compose é uma aplicação; serviços do Linux ficam em "Sistema".</p></div></div>
        <div class="grid g2" id="apps-grid"></div></div>`;
    },
    update() {
      const all = appsOf();
      $('#apps-grid').innerHTML = all.map((a) => {
        if (a.kind === 'kernel') {
          return `<div class="card app-card"><div class="card-h"><h2>${swatch(colorOf(a.color))}${esc(a.name)}</h2></div>
            <div class="app-metrics"><div><div class="m-l">CPU</div><div class="m-v">${pct(a.cpu)}</div></div>
            <div><div class="m-l">Memória</div><div class="m-v">${bytes(a.mem)}</div></div></div>
            <div class="note">O que o servidor gasta e não pertence a nenhum contêiner ou serviço: memória do kernel, buffers de rede, tabelas de página — normal ficar em algumas centenas de MB. Picos de CPU aqui costumam ser processos de vida curta (um <code>docker run</code>, um build) que começam e terminam entre duas leituras.</div></div>`;
        }
        const sys = a.kind === 'system';
        const units = sys ? a.units.filter((u) => u.cpu > 0.05 || u.mem > 8 * 1048576) : a.units;
        const hidden = a.units.length - units.length;
        return `<div class="card app-card">
          <div class="card-h"><h2 class="clickable" data-unit="app:${esc(a.key)}" style="cursor:pointer">${swatch(colorOf(a.color))}<span>${esc(a.name)}</span></h2>
            ${sys ? '' : `<div class="card-h-r">${appBadge(a)}${pauseBtns(a)}</div>`}</div>
          <div class="app-metrics">
            <div><div class="m-l">CPU</div><div class="m-v">${pct(a.cpu)}</div></div>
            <div><div class="m-l">Memória</div><div class="m-v">${bytes(a.mem)}</div></div>
            ${sys ? `<div><div class="m-l">Disco E/S</div><div class="m-v">${rate(a.ioRead + a.ioWrite)}</div></div><div><div class="m-l">Serviços</div><div class="m-v">${a.units.length}</div></div>`
              : `<div><div class="m-l">Disco</div><div class="m-v">${diskOf(a.disk)}</div></div>
            <div><div class="m-l">Saída no mês</div><div class="m-v">${data(a.month.tx)}</div></div>`}
          </div>
          ${sys ? '' : `<div class="app-facts"><div><span class="m-l">Instâncias</span> <b>${a.api || '—'}</b> <span class="muted">${a.api ? `(${esc(apiTitle(a))})` : ''}</span></div>
            <div><span class="m-l">Disco</span> ${diskParts(a.disk)}</div></div>`}
          <div class="rows">${units.map(unitRow).join('')}</div>
          ${hidden > 0 ? `<div class="muted" style="font-size:.78rem;padding-top:8px">+ ${hidden} serviços parados ou quase sem consumo</div>` : ''}
        </div>`;
      }).join('');
    },
  };
  // Pausar/Retomar: só apps do Docker, nunca o próprio painel
  function pauseBtns(a, cls = 'sm') {
    if (!can.act() || a.self || (a.kind !== 'compose' && a.kind !== 'standalone')) return '';
    const b = (p, ic, l) => `<button class="btn ${cls}" type="button" data-act="pause" data-v="${esc(a.key)}" data-pause="${p}">${icon(ic)}${l}</button>`;
    return (a.paused ? b(0, 'play', 'Retomar') : '') + (a.running ? b(1, 'pause', 'Pausar') : '');
  }
  // janela "tem certeza?" (fica por cima de gaveta e modal); resolve true/false
  function confirmDialog({ title, body, ok, danger }) {
    return new Promise((resolve) => {
      const scrim = document.createElement('div');
      scrim.className = 'cf-scrim';
      const m = document.createElement('div');
      m.className = 'cf-modal';
      m.innerHTML = `<div class="card cf-card" role="alertdialog" aria-modal="true" aria-labelledby="cf-t" aria-describedby="cf-b">
        <h2 id="cf-t">${esc(title)}</h2><div id="cf-b" class="cf-b">${body}</div>
        <div class="controls cf-actions"><button class="btn" type="button" data-cf="0">Cancelar</button>
        <button class="btn ${danger ? 'danger' : 'primary'}" type="button" data-cf="1">${esc(ok)}</button></div></div>`;
      document.body.append(scrim, m);
      let open = true;
      const finish = (v) => {
        if (!open) return;
        open = false;
        scrim.remove();
        m.remove();
        window.removeEventListener('keydown', onKey, true);
        resolve(v);
      };
      const onKey = (e) => { if (e.key === 'Escape') { e.stopPropagation(); finish(false); } };
      window.addEventListener('keydown', onKey, true);
      scrim.addEventListener('click', () => finish(false));
      m.addEventListener('click', (e) => {
        const b = e.target.closest('[data-cf]');
        if (b) finish(b.dataset.cf === '1');
        else if (e.target === m) finish(false);
      });
      $('[data-cf="0"]', m).focus();
    });
  }
  async function togglePause(key, pause) {
    const a = appsOf().find((x) => x.key === key);
    if (!a) return;
    const list = a.units.filter((u) => u.container && u.container.state === (pause ? 'running' : 'paused')).map((u) => u.container.name);
    const names = `<b>${list.map(esc).join(', ')}</b>`;
    const ok = await confirmDialog(pause ? {
      title: `Pausar ${a.name}?`, ok: 'Pausar', danger: true,
      body: `<p>${list.length === 1 ? 'Este contêiner vai ficar congelado' : `Estes ${list.length} contêineres vão ficar congelados`}: ${names}.</p>
        <ul class="cf-list"><li>O site ou a API dessa aplicação <b>para de responder</b> até você retomar.</li>
        <li>Para de usar CPU; a memória continua ocupada e nada é perdido.</li>
        <li>Um deploy dessa app ou um reinício do servidor desfaz a pausa.</li></ul>`,
    } : {
      title: `Retomar ${a.name}?`, ok: 'Retomar',
      body: `<p>${list.length === 1 ? 'O contêiner' : 'Os contêineres'} ${names} ${list.length === 1 ? 'volta' : 'voltam'} a rodar exatamente de onde ${list.length === 1 ? 'parou' : 'pararam'}.</p>`,
    });
    if (!ok) return;
    try {
      const j = await api('/api/apps/pause', { method: 'POST', body: JSON.stringify({ app: key, pause }) });
      toast(j.warning ? `Feito em parte: ${j.warning}` : `${a.name} ${pause ? 'pausada' : 'retomada'}.`);
      poll();
    } catch (ex) { if (ex.message !== 'login') toast(ex.message); }
  }
  function unitRow(u) {
    const c = u.container;
    const sub = c ? [c.status || c.state, c.image].filter(Boolean).join(' · ') : (u.desc || '');
    const lim = u.memLimit ? ` <span class="muted">/ ${bytes(u.memLimit)}</span>` : '';
    return `<div class="row" data-unit="${esc(u.key)}" role="button" tabindex="0">
      <div class="row-main"><div class="row-name"><span>${esc(u.name)}</span>${c ? containerBadge(c) : ''}</div>${sub ? `<div class="row-sub">${esc(sub)}</div>` : ''}</div>
      <div class="row-stats"><span>CPU <b>${pct(u.cpu)}</b></span><span>RAM <b>${bytes(u.mem)}</b>${lim}</span>${u.disk ? `<span>Disco <b>${bytes(u.disk.total)}</b></span>` : ''}${u.hasNet ? `<span>↑ <b>${rate(u.netTx)}</b></span>` : ''}</div>
    </div>`;
  }

  // ------------------------------------------------------------------ gaveta de detalhe (contêiner, serviço ou app)
  function closeDrawer() {
    stopWA();
    if (!S.drawer) return;
    S.drawer.destroy();
    S.drawer = null;
    $$('.scrim, .drawer, .modal, .sheet').forEach((e) => e.remove());
    document.body.style.overflow = '';
  }
  function openUnit(key) {
    closeDrawer();
    const isApp = key.startsWith('app:');
    const scrim = document.createElement('div');
    scrim.className = 'scrim';
    scrim.dataset.act = 'close';
    const d = document.createElement('aside');
    d.className = 'drawer';
    d.setAttribute('role', 'dialog');
    d.setAttribute('aria-modal', 'true');
    d.innerHTML = `<div class="drawer-h"><h2 id="dr-title"></h2><button class="icon-btn" data-act="close" aria-label="Fechar">${icon('x')}</button></div>
      <div class="drawer-b"><div id="dr-info"></div>
      <div class="section-h" style="margin:0"><h3>Histórico</h3>${rangeSeg(S.dRange || '1h', 'drange')}</div>
      ${chartCard('dr-cpu', 'CPU', '% da máquina inteira')}
      ${chartCard('dr-mem', 'Memória', 'Em uso, sem cache inativo')}
      ${chartCard('dr-net', 'Rede', 'Bytes por segundo')}
      ${chartCard('dr-io', 'Disco', 'Leitura e escrita')}
      <div id="dr-logs"></div></div>`;
    document.body.append(scrim, d);
    document.body.style.overflow = 'hidden';
    const local = [];
    const mk = (id, cfg) => { const before = S.cleanup.length; const c = makeChart($('#' + id, d), cfg); S.cleanup.splice(before); local.push(c.destroy); return c; };
    const s = (k) => cssVar(k);
    const C = {
      cpu: mk('dr-cpu', { fmt: pct, fill: true, series: [{ label: 'CPU', color: s('--s1') }] }),
      mem: mk('dr-mem', { fmt: bytes, fill: true, series: [{ label: 'Memória', color: s('--s3') }] }),
      net: mk('dr-net', { fmt: rate, series: [{ label: 'Saída', color: s('--s2') }, { label: 'Entrada', color: s('--s1') }] }),
      io: mk('dr-io', { fmt: rate, series: [{ label: 'Leitura', color: s('--s1') }, { label: 'Escrita', color: s('--s2') }] }),
    };
    const load = async () => {
      try {
        const r = await api(`/api/history/unit?key=${encodeURIComponent(key)}&range=${S.dRange || '1h'}`);
        C.cpu.set(r.t, [r.s.cpu]);
        C.mem.set(r.t, [r.s.mem]);
        C.net.set(r.t, [r.s.tx, r.s.rx]);
        C.io.set(r.t, [r.s.rd, r.s.wr]);
      } catch (e) {
        if (e.message !== 'login') ['dr-cpu', 'dr-mem', 'dr-net', 'dr-io'].forEach((id) => { $('#' + id, d).innerHTML = `<div class="chart-empty">${esc(e.message)}</div>`; });
      }
    };
    const iv = setInterval(load, 15000);
    local.push(() => clearInterval(iv));
    let logsLoaded = false;
    const info = () => {
      if (isApp) {
        const a = appsOf().find((x) => 'app:' + x.key === key);
        if (!a) return;
        $('#dr-title', d).innerHTML = `${swatch(colorOf(a.color))} ${esc(a.name)}`;
        $('#dr-info', d).innerHTML = `<div class="card"><div class="kv k3">
          ${kv('CPU', pct(a.cpu))}${kv('Memória', bytes(a.mem))}${kv('Contêineres', a.total ? `${a.running} de ${a.total} rodando${a.paused ? ` · ${a.paused} pausado(s)` : ''}` : '—')}
          ${kv('Rede ↑ agora', rate(a.netTx))}${kv('Saída hoje', data(a.today.tx))}${kv('Saída no mês', data(a.month.tx))}
          ${kv('Entrada no mês', data(a.month.rx))}${kv('Disco E/S', rate(a.ioRead + a.ioWrite))}${kv('Erros no log (1 h)', num(a.logErrors1h, 0))}
          ${a.kind !== 'system' ? kv('Disco ocupado', diskOf(a.disk)) + kv('Instâncias da API', `${a.api || '—'} <small class="muted">${a.api ? esc(apiTitle(a)) : ''}</small>`) : ''}
          </div>${a.kind !== 'system' && a.disk && a.disk.total ? `<div class="muted" style="font-size:.8rem;margin-top:10px">Disco: ${diskParts(a.disk)}. Imagem usada por mais de uma app é dividida entre elas.</div>` : ''}
          ${pauseBtns(a, '') ? `<div class="controls" style="margin-top:12px">${pauseBtns(a, '')}</div>` : ''}</div><div class="card"><h3 style="margin-bottom:6px">Componentes</h3><div class="rows">${a.units.map(unitRow).join('')}</div></div>`;
        return;
      }
      const f = findUnit(key);
      if (!f) { $('#dr-info', d).innerHTML = '<div class="note">Esta unidade não está mais rodando.</div>'; return; }
      const { u, a } = f, c = u.container;
      $('#dr-title', d).textContent = u.name;
      const limCPU = u.cpuLimit ? ` de ${num(u.cpuLimit, 2)}` : '';
      $('#dr-info', d).innerHTML = `<div class="card">
        <div class="chips" style="margin-bottom:12px">${c ? containerBadge(c) : ''}<span class="chip">${swatch(colorOf(a.color))}<span>${esc(a.name)}</span></span>
          ${c ? `<span class="chip"><span>${esc(c.image)}</span></span>${(c.ports || []).map((p) => `<span class="chip"><span>${esc(p)}</span></span>`).join('')}` : ''}</div>
        ${u.desc ? `<p class="ink2" style="margin:0 0 12px;font-size:.86rem">${esc(u.desc)}</p>` : ''}
        ${c ? `<p class="muted" style="margin:0 0 12px;font-size:.8rem">${esc(c.status)} · criado ${ago(c.created)}${c.service ? ` · serviço "${esc(c.service)}"` : ''}</p>` : ''}
        <div class="kv k3">
          ${kv('CPU', `${pct(u.cpu)} <small class="muted">(${num(u.cpuCores, 2)}${limCPU} núcl.)</small>`)}
          ${kv('Memória', bytes(u.mem) + (u.memLimit ? ` <small class="muted">de ${bytes(u.memLimit)}</small>` : ' <small class="muted">sem limite</small>'))}
          ${kv('Processos', num(u.pids, 0))}
          ${u.hasNet ? kv('Rede agora', `↑ ${rate(u.netTx)} ↓ ${rate(u.netRx)}`) : ''}
          ${u.hasNet ? kv('Hoje', `↑ ${data(u.today.tx)} ↓ ${data(u.today.rx)}`) : ''}
          ${u.hasNet ? kv('No mês', `↑ ${data(u.month.tx)} ↓ ${data(u.month.rx)}`) : ''}
          ${kv('Disco E/S', `${rate(u.ioRead)} lendo · ${rate(u.ioWrite)} gravando`)}
          ${u.cpuLimit ? kv('Segurado pelo limite', pct(u.throttled)) : ''}
          ${c ? kv('Log (1 h)', `${num(u.logLines1h, 0)} linhas · ${num(u.logErrors1h, 0)} erros`) : ''}
          ${u.disk ? kv('Disco ocupado', `${bytes(u.disk.total)} <small class="muted">(${[['volumes', u.disk.volumes], ['logs', u.disk.logs], ['camada', u.disk.layer]].filter(([, v]) => v > 0).map(([l, v]) => `${l} ${bytes(v)}`).join(' · ') || 'quase nada'})</small>`) : ''}
          ${u.imageSize ? kv('Imagem', `${bytes(u.imageSize)}${u.imageUses > 1 ? ` <small class="muted">(compartilhada por ${u.imageUses} contêineres)</small>` : ''}`) : ''}
        </div></div>`;
      if (c && !logsLoaded) {
        logsLoaded = true;
        api('/api/logs/targets').then((ts) => {
          const t = ts.find((x) => x.name === u.name);
          const errs = t ? t.lastErrors : [];
          $('#dr-logs', d).innerHTML = `<div class="card"><div class="card-h"><h3>Últimos erros no log</h3>
            <a class="btn" href="#/logs?c=${encodeURIComponent(u.name)}" data-act="close">${icon('logs')}Abrir logs</a></div>
            ${errs.length ? `<div class="logbox wrap" style="height:auto;max-height:320px">${errs.map((l) => logLine(l, false)).join('')}</div>`
              : '<div class="muted" style="font-size:.85rem">Nenhuma linha de erro nas últimas 24 h.</div>'}</div>`;
        }).catch(() => {});
      }
    };
    S.drawer = { update: info, destroy: () => local.forEach((f) => f()), reload: () => load() };
    info();
    load();
    $('[data-act="close"]', d).focus();
  }
  const kv = (l, v) => `<div><div class="m-l">${esc(l)}</div><div class="m-v">${v}</div></div>`;

  // ------------------------------------------------------------------ acesso e IA (primeiro acesso e Configurações)
  function accessFormHTML(user, setup) {
    return `<form id="acc-form" autocomplete="on" class="stack">
      <div class="field"><label for="acc-u">Usuário</label><input class="input" id="acc-u" name="username" autocomplete="username" value="${esc(user)}" required minlength="3" maxlength="32"></div>
      <div class="field"><label for="acc-c">Senha atual</label><input class="input" id="acc-c" type="password" autocomplete="current-password" required></div>
      <div class="field"><label for="acc-n">Nova senha</label><input class="input" id="acc-n" type="password" autocomplete="new-password" minlength="10" required></div>
      <div class="field"><label for="acc-r">Repita a nova senha</label><input class="input" id="acc-r" type="password" autocomplete="new-password" minlength="10" required></div>
      <div class="muted" style="font-size:.78rem">Pelo menos 10 caracteres (uma frase com espaços vale). Usuário: 3 a 32 letras, números, ponto, hífen ou _.</div>
      <div class="form-err" id="acc-err" role="alert"></div>
      <button class="btn primary" type="submit">${setup ? 'Salvar e continuar' : 'Salvar'}</button>
    </form>`;
  }
  function bindAccess(prefill, onDone) {
    if (prefill) $('#acc-c').value = prefill;
    ($('#acc-c').value ? $('#acc-n') : $('#acc-c')).focus();
    $('#acc-form').addEventListener('submit', async (e) => {
      e.preventDefault();
      const err = $('#acc-err'), btn = $('#acc-form button[type=submit]');
      const nw = $('#acc-n').value;
      if (nw !== $('#acc-r').value) { err.textContent = 'As duas novas senhas não são iguais.'; $('#acc-r').select(); return; }
      if (nw.length < 10) { err.textContent = 'A nova senha precisa ter pelo menos 10 caracteres.'; return; }
      btn.disabled = true;
      err.textContent = '';
      try {
        const j = await api('/api/password', { method: 'POST', body: JSON.stringify({ current: $('#acc-c').value, new: nw, user: $('#acc-u').value.trim() }) });
        onDone(j);
      } catch (ex) {
        if (ex.message !== 'login') err.textContent = ex.message;
        btn.disabled = false;
      }
    });
  }
  function aiStatus(v) {
    if (!v.enabled) return badge('off', 'IA desligada');
    return `${badge('ok', 'IA ligada')} <span class="muted" style="font-size:.82rem">${esc(v.model)} · chave ${esc(v.key)} · ${v.source === 'panel' ? 'configurada aqui' : 'vinda do .env do servidor'}</span>`;
  }
  function aiFormHTML(v, setup) {
    const models = [['deepseek-chat', 'deepseek-chat — rápido (recomendado)'], ['deepseek-reasoner', 'deepseek-reasoner — pensa mais, mais lento']];
    if (v.model && !models.some(([m]) => m === v.model)) models.push([v.model, v.model]);
    return `<form id="ai-form" class="stack" autocomplete="off">
      <div id="ai-st">${aiStatus(v)}</div>
      <p class="muted" style="margin:0;font-size:.84rem">A aba <b>IA</b> responde perguntas sobre o servidor usando a DeepSeek. Crie uma chave em
        <a href="https://platform.deepseek.com/api_keys" target="_blank" rel="noopener noreferrer">platform.deepseek.com</a> → API keys.
        Ela fica só no servidor (nunca volta para o navegador) e é testada antes de salvar.</p>
      <div class="field"><label for="ai-k">Chave da DeepSeek</label><input class="input" id="ai-k" type="password" placeholder="${v.source === 'panel' ? 'cole uma nova para trocar (vazio = manter)' : 'sk-...'}" spellcheck="false"></div>
      <div class="field"><label for="ai-m">Modelo</label><select class="input" id="ai-m">${models.map(([m, l]) => `<option value="${esc(m)}"${m === (v.model || 'deepseek-chat') ? ' selected' : ''}>${esc(l)}</option>`).join('')}</select></div>
      <div class="form-err" id="ai-err" role="alert"></div><div class="form-ok" id="ai-ok" role="status"></div>
      <div class="controls">
        <button class="btn primary" type="submit">Testar e salvar</button>
        ${v.enabled && !setup ? '<button class="btn" type="button" id="ai-test">Testar conexão</button>' : ''}
        ${v.source === 'panel' && !setup ? '<button class="btn" type="button" id="ai-rm">Remover chave</button>' : ''}
        ${setup ? '<button class="btn" type="button" id="ai-skip">Pular por enquanto</button>' : ''}
      </div>
    </form>`;
  }
  function bindAI(onSaved, onSkip) {
    const err = $('#ai-err'), ok = $('#ai-ok');
    const run = async (body, msg) => {
      err.textContent = ''; ok.textContent = 'Falando com a DeepSeek…';
      $$('#ai-form button').forEach((b) => { b.disabled = true; });
      try {
        const j = await api('/api/settings/ai', { method: 'POST', body: JSON.stringify(body) });
        ok.textContent = msg;
        $('#ai-st').innerHTML = aiStatus(j.ai);
        $('#ai-k').value = '';
        if (onSaved && body.action !== 'test') onSaved(j.ai);
      } catch (ex) {
        ok.textContent = '';
        if (ex.message !== 'login') err.textContent = ex.message;
      } finally {
        $$('#ai-form button').forEach((b) => { b.disabled = false; });
      }
    };
    $('#ai-form').addEventListener('submit', (e) => {
      e.preventDefault();
      run({ action: 'save', key: $('#ai-k').value.trim(), model: $('#ai-m').value }, 'Chave testada e salva. A aba IA já pode ser usada.');
    });
    const t = $('#ai-test');
    if (t) t.addEventListener('click', () => run({ action: 'test' }, 'Conexão com a DeepSeek funcionando.'));
    const rm = $('#ai-rm');
    if (rm) rm.addEventListener('click', () => {
      if (rm.dataset.sure !== '1') { rm.dataset.sure = '1'; rm.textContent = 'Confirmar remoção'; return; }
      run({ action: 'remove' }, 'Chave removida da tela.');
    });
    const sk = $('#ai-skip');
    if (sk) sk.addEventListener('click', onSkip);
  }

  // Primeiro acesso: com a senha inicial, só dá para criar o acesso e (opcional) ligar a IA.
  async function showSetup(prefill) {
    teardown();
    closeDrawer();
    clearTimeout(pollT);
    let me = {};
    try { me = await api('/api/me'); } catch { return; }
    S.me = me;
    const steps = me.admin ? ['Acesso', 'IA (opcional)', 'WhatsApp (opcional)'] : ['Crie sua senha'];
    const shell = (step, body) => `<div class="login"><div class="card login-card settings-card">
      <div class="brand"><div class="brand-logo">${icon('logo')}</div>
        <div class="brand-txt"><div class="brand-name">VPServer</div><div class="brand-sub">Primeiro acesso</div></div></div>
      <div class="steps">${steps.map((l, i) => `<span class="${step === i + 1 ? 'on' : step > i + 1 ? 'done' : ''}">${i + 1} · ${l}</span>`).join('')}</div>
      ${body}</div></div>`;
    const intro = me.passwordSource === 'env'
      ? 'O painel está com a senha inicial. Escolha o usuário e uma senha nova para continuar — até lá, nenhum dado do servidor aparece.'
      : 'Sua senha é provisória (quem cadastrou você a recebeu). Crie a sua para continuar — até lá, nenhum dado do servidor aparece.';
    $('#app').innerHTML = shell(1, `<h2 style="margin:14px 0 4px">Crie o seu acesso</h2>
      <p class="muted" style="margin:0 0 14px;font-size:.86rem">${esc(intro)}</p>
      ${accessFormHTML(me.user || 'admin', true)}`);
    bindAccess(prefill, async () => {
      if (!me.admin) { start(); return; } // IA e WhatsApp são do administrador
      let st = { ai: { enabled: false } };
      try { st = await api('/api/settings'); } catch { /* segue */ }
      $('#app').innerHTML = shell(2, `<h2 style="margin:14px 0 4px">Quer usar a IA?</h2>${aiFormHTML(st.ai, true)}
        <p class="muted" style="margin:10px 0 0;font-size:.8rem">Dá para configurar depois em <b>Configurações</b> (ícone de engrenagem no topo).</p>`);
      bindAI(() => setTimeout(waStep, 900), waStep);
      $('#ai-k').focus();
    });
    // passo 3: conectar o WhatsApp das notificações (se estiver instalado)
    async function waStep() {
      let j;
      try { j = await api('/api/notify'); } catch { start(); return; }
      if (!j.notify.installed) { start(); return; }
      const render = (n) => {
        S.nt = n;
        const st = n.status;
        const own = st.state === 'open' && st.number && !n.config.recipients.length;
        $('#app').innerHTML = shell(3, `<h2 style="margin:14px 0 4px">Avisos pelo WhatsApp?</h2>
          <p class="muted" style="margin:0 0 12px;font-size:.86rem">O painel pode mandar alertas (app caiu, disco enchendo, limites do plano grátis) e um resumo diário pelo WhatsApp.</p>
          <div id="wa-box">${waBoxHTML(n)}</div>
          ${own ? `<form class="stack" id="su-to" style="margin-top:14px"><div class="field"><label for="su-num">Mandar os avisos para (com DDI)</label>
            <input class="input" id="su-num" inputmode="tel" value="${esc(fmtPhone(st.number))}"></div>
            <div class="form-err" id="su-err" role="alert"></div><button class="btn primary" type="submit">Salvar e concluir</button></form>` : ''}
          <div class="controls" style="margin-top:14px"><button class="btn${own ? '' : ' primary'}" type="button" id="su-done">${st.state === 'open' ? 'Concluir' : 'Pular por enquanto'}</button></div>
          <p class="muted" style="margin:10px 0 0;font-size:.8rem">O que avisar e para quem fica na aba <b>Notificações</b>.</p>`);
        bindWA($('#wa-box'), (x) => render(x.notify));
        $('#su-done').addEventListener('click', () => { stopWA(); start(); });
        const f = $('#su-to');
        if (f) f.addEventListener('submit', async (e) => {
          e.preventDefault();
          try {
            await api('/api/notify/config', { method: 'POST', body: JSON.stringify({ ...n.config, recipients: [{ id: $('#su-num').value, name: 'Eu' }], panelUrl: location.origin }) });
            stopWA();
            start();
          } catch (ex) { if (ex.message !== 'login') $('#su-err').textContent = ex.message; }
        });
      };
      render(j.notify);
    }
  }

  // Configurações: Minha conta (todos), Usuários (quem gerencia), IA e WhatsApp (administradores).
  async function openSettings(tab = 'acesso') {
    closeDrawer();
    let me = {}, st = { ai: { enabled: false } };
    try {
      me = await api('/api/me');
      S.me = me;
      if (me.admin) st = await api('/api/settings');
    } catch { return; }
    const tabs = [['acesso', 'Minha conta', true], ['usuarios', 'Usuários', can.manage()], ['ia', 'IA', can.admin()], ['whatsapp', 'WhatsApp', can.admin()]]
      .filter(([, , ok]) => ok);
    if (!tabs.some(([k]) => k === tab)) tab = 'acesso';
    const scrim = document.createElement('div');
    scrim.className = 'scrim';
    scrim.dataset.act = 'close';
    const m = document.createElement('div');
    m.className = 'modal';
    m.innerHTML = `<div class="card login-card settings-card" role="dialog" aria-modal="true" aria-labelledby="st-t">
      <div class="card-h"><div><h2 id="st-t">${icon('gear')}Configurações</h2>
        <div class="muted st-who">${icon('user')}${esc(me.user)} · ${esc(roleText(me))}</div></div>
        <button class="icon-btn" type="button" data-act="close" aria-label="Fechar">${icon('x')}</button></div>
      ${tabs.length > 1 ? `<div class="seg" role="tablist" style="margin-bottom:14px">
        ${tabs.map(([k, l]) => `<button type="button" data-act="stab" data-v="${k}" aria-pressed="${tab === k}">${l}</button>`).join('')}</div>` : ''}
      <div id="st-body"></div></div>`;
    document.body.append(scrim, m);
    S.drawer = { update() {}, reload() {}, destroy() {} };
    const show = (t) => {
      $$('[data-act="stab"]', m).forEach((b) => b.setAttribute('aria-pressed', b.dataset.v === t));
      const body = $('#st-body', m);
      stopWA();
      body.onclick = body.onchange = null;
      if (t === 'usuarios') {
        usersPanel(body);
      } else if (t === 'ia') {
        body.innerHTML = aiFormHTML(st.ai, false);
        bindAI((v) => { st.ai = v; }, null);
      } else if (t === 'whatsapp') {
        body.innerHTML = '<div class="loading"><div class="spinner"></div></div>';
        const render = (n) => {
          body.innerHTML = `<p class="muted" style="margin:0 0 12px;font-size:.84rem">Conexão do WhatsApp que manda os avisos do painel.</p>
            <div id="wa-box">${waBoxHTML(n)}</div>
            <p style="margin:14px 0 0;font-size:.86rem"><a href="#/notify">Escolher para quem e o que avisar →</a></p>`;
          bindWA($('#wa-box', body), (x) => render(x.notify));
        };
        api('/api/notify').then((j) => render(j.notify)).catch((ex) => { if (ex.message !== 'login') body.innerHTML = `<div class="form-err">${esc(ex.message)}</div>`; });
      } else {
        const origin = me.passwordSource === 'panel' ? `Senha trocada pelo painel em ${dt(me.passwordChanged)}.` : 'Hoje vale a senha definida no .env do servidor.';
        body.innerHTML = `<p class="muted" style="margin:0 0 12px;font-size:.84rem">${esc(origin)} Ao salvar, as outras sessões (outros aparelhos) saem.</p>${accessFormHTML(me.user, false)}`;
        bindAccess('', (j) => { closeDrawer(); toast(`Acesso salvo (usuário ${j.user}). Os outros aparelhos vão precisar entrar de novo.`); });
      }
    };
    S.settingsTab = show;
    show(tab);
  }

  // ------------------------------------------------------------------ usuários (Configurações → Usuários)
  function permBoxes(u) {
    const me = S.me;
    const box = (k, label, hint, ok) => `<label class="perm${ok ? '' : ' locked'}"><input type="checkbox" class="sw" data-perm="${k}"
      ${u[k] || (k !== 'admin' && u.admin) ? 'checked' : ''} ${ok && !(k !== 'admin' && u.admin) ? '' : 'disabled'}>
      <span><b>${label}</b><small>${hint}</small></span></label>`;
    return `<div class="perms">
      ${box('admin', 'Administrador', 'Pode tudo: ações nas apps, usuários, IA e WhatsApp.', me.admin)}
      ${box('actions', 'Pausar e retomar aplicações', 'Os botões Pausar/Retomar da aba Aplicações.', me.admin || me.actions)}
      ${box('manage', 'Criar e gerenciar usuários', 'Sem mexer em administradores; só concede o que tem.', true)}</div>`;
  }
  const readPerms = (root) => Object.fromEntries($$('[data-perm]', root).map((c) => [c.dataset.perm, c.checked]));
  const passBox = (name, pass) => `<div class="passbox" role="status"><div><b>Senha provisória de ${esc(name)}</b></div>
    <div class="passline"><code class="pass">${esc(pass)}</code><button class="btn sm" type="button" data-copy="${esc(pass)}">Copiar</button></div>
    <div class="muted">Passe só para essa pessoa. No primeiro acesso ela cria a própria senha (esta não aparece de novo).</div></div>`;
  function usersPanel(body) {
    const load = async (flash) => {
      let j;
      try { j = await api('/api/users'); } catch (ex) {
        if (ex.message !== 'login') body.innerHTML = `<div class="form-err">${esc(ex.message)}</div>`;
        return;
      }
      const me = S.me;
      const editable = (u) => u.name !== me.user && (me.admin || !u.admin);
      body.innerHTML = `${flash || ''}<div class="ulist">${j.users.map((u) => `<div class="urow" data-user="${esc(u.name)}">
          <div class="urow-h"><div class="urow-n"><span><b>${esc(u.name)}</b>${u.name === me.user ? ' <span class="muted">(você)</span>' : ''}</span>
            <small>${esc(roleText(u))}${u.mustChange ? ' · senha provisória' : ''} · ${u.lastLogin ? `último acesso ${dt(u.lastLogin)}` : 'nunca entrou'}</small></div>
            ${editable(u) ? `<div class="controls"><button class="btn sm" type="button" data-u="edit">Permissões</button>
              <button class="btn sm" type="button" data-u="reset">Nova senha</button>
              <button class="btn sm" type="button" data-u="del" aria-label="Remover ${esc(u.name)}">Remover</button></div>` : ''}</div>
          ${editable(u) ? `<div class="urow-edit" hidden>${permBoxes(u)}<div class="controls"><button class="btn primary sm" type="button" data-u="save">Salvar permissões</button></div></div>` : ''}
        </div>`).join('')}</div>
        <form class="stack unew" id="unew" autocomplete="off"><h3>${icon('plus')}Novo usuário</h3>
          <div class="field"><label for="un-name">Nome de usuário</label><input class="input" id="un-name" minlength="3" maxlength="32" required placeholder="ex.: maria" autocapitalize="off" spellcheck="false"></div>
          ${permBoxes({})}
          <div class="form-err" id="un-err" role="alert"></div>
          <button class="btn primary" type="submit">Criar usuário</button>
          <p class="muted" style="margin:0;font-size:.8rem">Sem nenhuma permissão marcada, a pessoa só vê (dados, logs e o chat com a IA).
            O painel gera uma senha provisória; a pessoa cria a dela no primeiro acesso.</p></form>`;
      $('#unew', body).addEventListener('submit', async (e) => {
        e.preventDefault();
        const name = $('#un-name', body).value.trim();
        try {
          const r = await api('/api/users', { method: 'POST', body: JSON.stringify({ name, ...readPerms($('#unew', body)) }) });
          load(passBox(r.user.name, r.password));
        } catch (ex) { if (ex.message !== 'login') $('#un-err', body).textContent = ex.message; }
      });
    };
    body.onchange = (e) => { // administrador pode tudo: marca e trava as outras
      const c = e.target.closest('[data-perm="admin"]');
      if (!c) return;
      $$('[data-perm]:not([data-perm="admin"])', c.closest('.perms')).forEach((o) => {
        o.checked = c.checked || o.checked;
        o.disabled = c.checked || (o.dataset.perm === 'actions' && !can.act());
      });
    };
    body.onclick = async (e) => {
      const cp = e.target.closest('[data-copy]');
      if (cp) {
        try { await navigator.clipboard.writeText(cp.dataset.copy); toast('Senha copiada.'); } catch { toast('Copie a senha manualmente.'); }
        return;
      }
      const b = e.target.closest('[data-u]');
      if (!b) return;
      const row = b.closest('.urow');
      const name = row.dataset.user;
      const post = async (path, extra) => api(path, { method: 'POST', body: JSON.stringify({ name, ...extra }) });
      try {
        if (b.dataset.u === 'edit') { $('.urow-edit', row).hidden = !$('.urow-edit', row).hidden; return; }
        if (b.dataset.u === 'save') { await post('/api/users/update', readPerms(row)); toast(`Permissões de ${name} salvas. As sessões dele(a) saíram.`); load(); return; }
        if (b.dataset.u === 'reset') {
          if (!await confirmDialog({ title: `Nova senha para ${name}?`, ok: 'Gerar senha',
            body: `<p>O painel gera uma senha provisória e ${esc(name)} sai de todos os aparelhos. No próximo acesso, cria a própria senha.</p>` })) return;
          const r = await post('/api/users/reset');
          load(passBox(name, r.password));
          return;
        }
        if (b.dataset.u === 'del') {
          if (!await confirmDialog({ title: `Remover ${name}?`, ok: 'Remover', danger: true,
            body: `<p>${esc(name)} deixa de acessar o painel na hora (as sessões abertas caem). Dá para cadastrar de novo depois.</p>` })) return;
          await post('/api/users/delete');
          toast(`${name} removido(a).`);
          load();
        }
      } catch (ex) { if (ex.message !== 'login') toast(ex.message); }
    };
    body.innerHTML = '<div class="loading"><div class="spinner"></div></div>';
    load();
  }

  // ------------------------------------------------------------------ aba: banda
  const trafficView = {
    mount(v) {
      v.innerHTML = `<div class="page" id="tr"><div class="loading"><div class="spinner"></div></div></div>`;
      const tr = { range: '24h', chart: null, days: null, sel: null };
      const loadChart = async () => {
        try {
          const h = await api(`/api/history/host?range=${tr.range}&f=tx,rx`);
          tr.chart && tr.chart.set(h.t, [h.s.tx, h.s.rx]);
        } catch { /* toast já cuida */ }
      };
      const load = async () => {
        let t;
        try { t = await api('/api/traffic'); } catch (e) { if (e.message !== 'login') toast(e.message); return; }
        const first = !tr.chart;
        if (first) {
          $('#tr').innerHTML = `
            <section class="card" id="tr-hero"></section>
            <section class="kpis" id="tr-kpis"></section>
            <section class="card"><div class="card-h"><div><h2>Saída por dia</h2><div class="muted" style="font-size:.82rem">Últimos 30 dias · toque numa barra</div></div></div>
              <div id="tr-days"></div></section>
            <section class="card chart-card"><div class="card-h"><div><h3>Velocidade da rede</h3><div class="sub" style="margin:0">Placa principal do servidor</div></div>${rangeSeg(tr.range, 'trange', RANGES.slice(0, 5))}</div>
              <div class="chart" id="tr-chart"></div></section>
            <section class="card" id="tr-apps"></section>
            <section class="card" id="tr-cts"></section>
            <section class="card" id="tr-months"></section>
            <section class="note"><b>Como a banda é medida.</b> O total do servidor vem da placa de rede principal (o mesmo que a Oracle mede para o limite de 10 TB de saída). Por aplicação, soma-se o tráfego da rede de cada contêiner — isso inclui conversas internas (ex.: API ↔ banco, app ↔ túnel), então a soma das apps é maior que o total real. Quem fala com a internet são as portas de entrada: túneis da Cloudflare (cloudflared) ou um proxy com portas abertas (Caddy, Nginx…).</section>`;
          tr.chart = makeChart($('#tr-chart'), { fmt: rate, series: [{ label: 'Saída', color: cssVar('--s2') }, { label: 'Entrada', color: cssVar('--s1') }] });
          trafficView.setRange = (r) => { tr.range = r; $$('[data-act="trange"]').forEach((b) => b.setAttribute('aria-pressed', b.dataset.v === r)); loadChart(); };
          loadChart();
        }
        const s = t.summary;
        const egP = (s.month.tx / s.limitBytes) * 100;
        $('#tr-hero').innerHTML = `<div class="card-h"><h2>Saída para a internet neste mês</h2>${badge(egP >= 90 ? 'crit' : egP >= 75 ? 'warn' : 'ok', `${pct(egP)} do grátis`)}</div>
          <div class="hero num">${data(s.month.tx)}</div>
          <div class="meter thick" style="margin:12px 0 8px"><i class="${level(egP, 75, 90)}" style="width:${Math.min(100, egP).toFixed(2)}%"></i></div>
          <div class="muted" style="font-size:.85rem">Limite grátis da Oracle: <b class="ink2">10 TB/mês</b> · projeção para o mês: <b class="ink2">${s.projectedTx ? data(s.projectedTx) : 'calculando…'}</b> · entrada no mês: ${data(s.month.rx)} (entrada não é cobrada)</div>`;
        $('#tr-kpis').innerHTML = [
          kpi('up', 'Hoje', `↑ ${data(s.today.tx)}`, `↓ ${data(s.today.rx)} recebidos`),
          kpi('clock', 'Ontem', `↑ ${data(s.yesterday.tx)}`, `↓ ${data(s.yesterday.rx)} recebidos`),
          kpi('net', 'Mês passado', `↑ ${data(s.lastMonth.tx)}`, `↓ ${data(s.lastMonth.rx)} recebidos`),
          kpi('net', 'Desde o boot', `↑ ${data(s.sinceBoot.tx)}`, `↓ ${data(s.sinceBoot.rx)} · contador da placa ${esc(s.iface)}`),
        ].join('');
        tr.days = t.days.slice(-30);
        const maxD = Math.max(1, ...tr.days.map((d) => d.tx));
        $('#tr-days').innerHTML = `<div class="bars">${tr.days.map((d, i) => `<div data-act="day" data-i="${i}" title="${esc(d.d)}"><i style="height:${((d.tx / maxD) * 100).toFixed(1)}%"></i></div>`).join('')}</div>
          <div class="bars-x"><span>${esc(tr.days[0].d.slice(8))}/${esc(tr.days[0].d.slice(5, 7))}</span><span>hoje</span></div>
          <div class="tip" id="tr-tip">Maior dia: ${data(maxD)}</div>`;
        trafficView.day = (i) => {
          const d = tr.days[i];
          $$('.bars > div').forEach((b) => b.classList.toggle('on', b.dataset.i == i));
          $('#tr-tip').innerHTML = `<b>${esc(d.d.slice(8))}/${esc(d.d.slice(5, 7))}</b> · saída ${data(d.tx)} · entrada ${data(d.rx)}`;
        };
        const rowsT = (list, withApp) => list.map((x) => `<tr class="clickable" data-unit="${esc(withApp ? x.key : 'app:' + x.app)}">
          <td><div class="cell-name">${swatch(colorOf(x.color))}<span>${esc(x.name)}</span></div></td>
          <td class="r">${data(x.today.tx)}</td><td class="r">${data(x.month.tx)}</td><td class="r">${data(x.month.rx)}</td><td class="r">${data(x.lastMonth.tx)}</td></tr>`).join('');
        const head = '<thead><tr><th>Nome</th><th class="r">Saída hoje</th><th class="r">Saída no mês</th><th class="r">Entrada no mês</th><th class="r">Saída mês passado</th></tr></thead>';
        $('#tr-apps').innerHTML = `<div class="card-h"><h2>Por aplicação</h2></div>${t.apps.length ? `<div class="table-wrap"><table>${head}<tbody>${rowsT(t.apps, false)}</tbody></table></div>` : '<div class="empty">Ainda sem dados — a contagem começa quando o monitor sobe.</div>'}`;
        $('#tr-cts').innerHTML = `<div class="card-h"><h2>Por contêiner</h2></div>${t.containers.length ? `<div class="table-wrap"><table>${head}<tbody>${rowsT(t.containers, true)}</tbody></table></div>` : '<div class="empty">Ainda sem dados.</div>'}`;
        $('#tr-months').innerHTML = `<div class="card-h"><h2>Meses</h2><span class="muted" style="font-size:.8rem">contando desde ${dt(s.since)}</span></div>
          <div class="table-wrap"><table><thead><tr><th>Mês</th><th class="r">Saída</th><th class="r">Entrada</th><th class="r">% do grátis</th></tr></thead>
          <tbody>${t.months.slice().reverse().filter((m) => m.tx || m.rx).map((m) => `<tr><td>${esc(m.d.slice(5))}/${esc(m.d.slice(0, 4))}</td>
          <td class="r">${data(m.tx)}</td><td class="r">${data(m.rx)}</td><td class="r">${pct((m.tx / s.limitBytes) * 100)}</td></tr>`).join('') || '<tr><td colspan="4" class="muted">Ainda sem dados.</td></tr>'}</tbody></table></div>`;
      };
      load();
      later(load, 30000);
      later(loadChart, 60000);
    },
  };

  // ------------------------------------------------------------------ aba: logs
  function logLine(l, showC) {
    const t = l.t ? hms(l.t) : '';
    return `<div class="ln${l.e ? ' err' : ''}${l.s === 'err' ? ' stderr' : ''}"><span class="t">${t}</span>${showC ? `<span class="c" style="color:${esc(l.color || 'inherit')}">${esc(l.c)}</span>` : ''}<span class="m">${esc(l.m)}</span></div>`;
  }
  const logsView = {
    mount(v, params) {
      const L = S.logs;
      if (params.get('c')) L.c = params.get('c');
      v.innerHTML = `<div class="page">
        <div class="section-h"><div><h2>Logs dos contêineres</h2><p>Lidos na hora pelo Docker (só leitura). Linhas que parecem erro ficam em destaque.</p></div></div>
        <section class="card">
          <div class="controls" style="margin-bottom:12px">
            <div class="field" style="flex:1 1 220px"><label for="lg-c">Contêiner</label><select class="input" id="lg-c"></select></div>
            <div class="field"><label for="lg-n">Linhas</label><select class="input" id="lg-n">${[100, 300, 1000, 2000].map((n) => `<option value="${n}"${n === L.tail ? ' selected' : ''}>${n}</option>`).join('')}</select></div>
            <div class="field" style="flex:1 1 180px"><label for="lg-q">Filtrar texto</label><input class="input" id="lg-q" type="search" placeholder="ex.: 500, timeout" value="${esc(L.q)}"></div>
            <div class="field"><label>&nbsp;</label><div class="controls">
              <label class="toggle"><input type="checkbox" id="lg-e"${L.errors ? ' checked' : ''}>Só erros</label>
              <label class="toggle"><input type="checkbox" id="lg-l"${L.live ? ' checked' : ''}>Ao vivo</label>
              <label class="toggle"><input type="checkbox" id="lg-w"${L.wrap ? ' checked' : ''}>Quebrar linhas</label>
              <button class="btn" type="button" id="lg-r">${icon('refresh')}Atualizar</button></div></div>
          </div>
          <div class="logbox${L.wrap ? ' wrap' : ''}" id="lg-box" tabindex="0"><div class="loading"><div class="spinner"></div></div></div>
          <div class="muted" style="font-size:.78rem;margin-top:8px" id="lg-meta"></div>
        </section>
        <section class="card" id="lg-act"></section></div>`;
      let lines = [], liveT = null;
      const save = () => store.set('logs', { c: L.c, tail: L.tail, q: L.q, errors: L.errors, live: L.live, wrap: L.wrap });
      const draw = (stick) => {
        const box = $('#lg-box');
        if (!box) return;
        const atBottom = stick || box.scrollHeight - box.scrollTop - box.clientHeight < 40;
        const q = L.q.toLowerCase();
        const shown = q ? lines.filter((l) => l.m.toLowerCase().includes(q) || (l.c || '').toLowerCase().includes(q)) : lines;
        box.innerHTML = shown.length ? shown.map((l) => logLine(l, L.c === '*')).join('') : '<div class="empty">Nenhuma linha.</div>';
        $('#lg-meta').textContent = `${shown.length} de ${lines.length} linhas · ${lines.filter((l) => l.e).length} parecem erro · lido às ${hms(Date.now())}`;
        if (atBottom) box.scrollTop = box.scrollHeight;
      };
      const colors = {};
      const fetchLogs = async (stick) => {
        try {
          const r = await api(`/api/logs?c=${encodeURIComponent(L.c)}&tail=${L.tail}${L.errors ? '&errors=1' : ''}`);
          lines = r.lines.map((l) => Object.assign(l, { color: colors[l.c] }));
          draw(stick);
        } catch (e) {
          if (e.message !== 'login') $('#lg-box').innerHTML = `<div class="empty">${esc(e.message)}</div>`;
        }
      };
      const setLive = () => {
        clearInterval(liveT);
        if (L.live) liveT = setInterval(() => fetchLogs(false), 4000);
      };
      S.cleanup.push(() => clearInterval(liveT));
      const loadTargets = async () => {
        let ts;
        try { ts = await api('/api/logs/targets'); } catch { return; }
        const byApp = {};
        ts.forEach((t) => { (byApp[t.appName] = byApp[t.appName] || []).push(t); });
        ts.forEach((t) => { colors[t.name] = colorOf(t.color); });
        const sel = $('#lg-c');
        if (sel && !sel.options.length) {
          sel.innerHTML = `<option value="*">Todos os contêineres (misturados)</option>` + Object.entries(byApp).map(([app, list]) =>
            `<optgroup label="${esc(app)}">${list.map((t) => `<option value="${esc(t.name)}">${esc(t.name)}${t.state !== 'running' ? ' (parado)' : ''}</option>`).join('')}</optgroup>`).join('');
          sel.value = ts.some((t) => t.name === L.c) ? L.c : '*';
          L.c = sel.value;
          fetchLogs(true);
        }
        $('#lg-act').innerHTML = `<div class="card-h"><div><h2>Atividade de log</h2><div class="muted" style="font-size:.82rem">Contada a cada 5 min. Toque para abrir.</div></div></div>
          <div class="table-wrap"><table><thead><tr><th>Contêiner</th><th>Aplicação</th><th class="r">Linhas (1 h)</th><th class="r">Erros (1 h)</th><th class="r">Erros (24 h)</th></tr></thead>
          <tbody>${ts.slice().sort((a, b) => b.errors24h - a.errors24h || b.lines1h - a.lines1h).map((t) => `<tr class="clickable" data-act="logpick" data-v="${esc(t.name)}">
          <td><div class="cell-name">${swatch(colors[t.name] || cssVar('--s-other'))}<span>${esc(t.name)}</span></div></td><td class="ink2">${esc(t.appName)}</td>
          <td class="r">${num(t.lines1h, 0)}</td><td class="r">${t.errors1h ? `<b>${num(t.errors1h, 0)}</b>` : '0'}</td><td class="r">${num(t.errors24h, 0)}</td></tr>`).join('')}</tbody></table></div>`;
      };
      loadTargets();
      later(loadTargets, 60000);
      setLive();
      logsView.pick = (name) => { $('#lg-c').value = name; L.c = name; save(); fetchLogs(true); window.scrollTo({ top: 0, behavior: 'smooth' }); };
      $('#lg-c').addEventListener('change', (e) => { L.c = e.target.value; save(); fetchLogs(true); });
      $('#lg-n').addEventListener('change', (e) => { L.tail = +e.target.value; save(); fetchLogs(true); });
      $('#lg-e').addEventListener('change', (e) => { L.errors = e.target.checked; save(); fetchLogs(true); });
      $('#lg-l').addEventListener('change', (e) => { L.live = e.target.checked; save(); setLive(); if (L.live) fetchLogs(true); });
      $('#lg-w').addEventListener('change', (e) => { L.wrap = e.target.checked; save(); $('#lg-box').classList.toggle('wrap', L.wrap); });
      $('#lg-r').addEventListener('click', () => fetchLogs(true));
      let qT;
      $('#lg-q').addEventListener('input', (e) => { clearTimeout(qT); qT = setTimeout(() => { L.q = e.target.value; save(); draw(false); }, 150); });
    },
  };

  // ------------------------------------------------------------------ aba: sistema
  const systemView = {
    mount(v) {
      v.innerHTML = `<div class="page" id="sy"><div class="loading"><div class="spinner"></div></div></div>`;
      const load = async () => {
        let s;
        try { s = await api('/api/system'); } catch (e) { if (e.message !== 'login') toast(e.message); return; }
        const h = s.host, i = s.info, sv = s.server;
        const boot = h.t - h.uptime;
        const psi = (label, v10, v60, tip) => `<div style="display:grid;gap:4px"><div style="display:flex;justify-content:space-between;font-size:.85rem"><span>${label}</span><span class="num">${pct(v10)} <span class="muted">(1 min: ${pct(v60)})</span></span></div>
          <div class="meter"><i class="${level(v10, 10, 30)}" style="width:${Math.min(100, v10)}%"></i></div><div class="muted" style="font-size:.76rem">${tip}</div></div>`;
        const procs = S.procSort === 'cpu' ? s.byCpu : s.byMem;
        const svcs = s.services.filter((x) => x.cpu > 0.01 || x.mem > 4 * 1048576);
        const d = s.disk || {};
        $('#sy').innerHTML = `
          <section class="grid g2">
            <div class="card"><div class="card-h"><h2>Servidor</h2>${badge('ok', 'No ar há ' + dur(h.uptime))}</div><div class="kv">
              ${kv('Nome', esc(sv.hostname || '—'))}${kv('Sistema', esc(sv.os || '—'))}${kv('Kernel', esc(sv.kernel || '—'))}${kv('Arquitetura', esc(sv.arch || '—'))}
              ${kv('Núcleos', `${h.cores} vCPU`)}${kv('Memória', bytes(h.memTotal))}${kv('Swap', h.swapTotal ? `${bytes(h.swapUsed)} de ${bytes(h.swapTotal)}` : 'sem swap')}${kv('Disco (volume)', bytes(sv.diskSize))}
              ${kv('Docker', esc(sv.docker || '—'))}${kv('Ligado desde', dt(boot))}${kv('Processos', `${num(h.procs, 0)} (${num(h.procsRunning, 0)} rodando)`)}${kv('Conexões TCP', `${num(h.tcp, 0)} abertas · ${num(h.tcpTimeWait, 0)} em espera`)}
              ${kv('Contêineres', i.containers ? `${i.running} rodando · ${i.stopped} parados` : '—')}${kv('Imagens', num(i.images, 0))}
              ${kv('OOM kills (boot)', num(h.oomKills, 0))}${kv('Versão do painel', esc(s.version || '—'))}
              ${sv.cloud && sv.cloud.provider === 'oracle' ? kv('Nuvem', `Oracle · ${esc(sv.cloud.regionName)}`) + kv('Shape', `${esc(sv.cloud.shape)} <small class="muted">${num(sv.cloud.ocpus, 0)} OCPU · ${num(sv.cloud.memGb, 0)} GB · ${num(sv.cloud.netGbps, 0)} Gbps</small>`) : ''}
            </div></div>
            <div class="card"><div class="card-h"><div><h2>Pressão do sistema</h2><div class="muted" style="font-size:.82rem">% do tempo em que algo ficou esperando (PSI do kernel). Perto de 0 = folgado.</div></div></div>
              <div style="display:grid;gap:14px">
                ${psi('CPU', h.psi.cpu10, h.psi.cpu60, 'Tarefas esperando a vez no processador.')}
                ${psi('Memória', h.psi.mem10, h.psi.mem60, 'Tempo perdido recuperando RAM (cache, swap). Subiu = falta memória.')}
                ${psi('Disco', h.psi.io10, h.psi.io60, 'Tarefas esperando leitura/escrita no disco.')}
              </div>
              <h3 style="margin:18px 0 8px">CPU por núcleo</h3>
              <div style="display:grid;gap:8px">${(h.perCpu || []).map((c, k) => `<div class="share-row" style="grid-template-columns:70px minmax(0,1fr) 52px"><span class="share-label">Núcleo ${k}</span>
                <div class="meter thick"><i class="${level(c, 75, 90)}" style="width:${Math.min(100, c).toFixed(1)}%"></i></div><span class="share-total num">${pct(c)}</span></div>`).join('')}</div>
            </div>
          </section>
          <section class="card"><div class="card-h"><div><h2>Processos</h2><div class="muted" style="font-size:.82rem">Os 15 que mais gastam agora, com a aplicação dona.${s.warming ? ' Medindo — o % de CPU aparece na próxima leitura.' : ''}</div></div>
            <div class="seg"><button type="button" data-act="psort" data-v="cpu" aria-pressed="${S.procSort === 'cpu'}">Por CPU</button><button type="button" data-act="psort" data-v="mem" aria-pressed="${S.procSort === 'mem'}">Por memória</button></div></div>
            <div class="table-wrap"><table><thead><tr><th>Processo</th><th>De quem</th><th class="r">CPU</th><th class="r">Memória</th><th class="r">Threads</th><th class="r">PID</th></tr></thead>
            <tbody>${procs.map((p) => `<tr><td class="mono">${esc(p.name)}</td><td class="ink2">${esc(p.unitName || '—')}</td><td class="r">${pct(p.cpu)}</td><td class="r">${bytes(p.rss)}</td><td class="r">${num(p.threads, 0)}</td><td class="r muted">${p.pid}</td></tr>`).join('')}</tbody></table></div></section>
          <section class="card"><div class="card-h"><div><h2>Serviços do Linux</h2><div class="muted" style="font-size:.82rem">Fora dos contêineres: Docker, agentes da Oracle, runners do GitHub, SSH…</div></div></div>
            <div class="table-wrap"><table><thead><tr><th>Serviço</th><th class="r">CPU</th><th class="r">Memória</th><th class="r">Disco E/S</th></tr></thead>
            <tbody>${svcs.map((x) => `<tr class="clickable" data-unit="${esc(x.key)}"><td><div>${esc(x.name)}</div>${x.desc ? `<div class="cell-sub">${esc(x.desc)}</div>` : ''}</td>
            <td class="r">${pct(x.cpu)}</td><td class="r">${bytes(x.mem)}</td><td class="r">${rate(x.ioRead + x.ioWrite)}</td></tr>`).join('')}</tbody></table></div></section>
          <section class="grid g2">
            <div class="card"><div class="card-h"><div><h2>Disco do Docker</h2><div class="muted" style="font-size:.82rem">${d.t ? 'medido ' + ago(d.t) + ' (a cada 30 min)' : 'medindo…'}</div></div></div>
              ${d.t ? `<div class="kv">${kv('Imagens', `${bytes(d.imagesSize)} <small class="muted">(${num(d.imagesCount, 0)})</small>`)}${kv('Imagens sem uso', bytes(d.imagesUnused))}
              ${kv('Volumes', bytes(d.volumesSize))}${kv('Contêineres (camada gravável)', bytes(d.containersSize))}${kv('Cache de build (liberável)', `<b>${bytes(d.buildCacheSize)}</b>`)}${kv('Logs dos contêineres', S.ov && S.ov.storage.logsKnown ? bytes(S.ov.storage.logs) : '—')}</div>
              <h3 style="margin:16px 0 4px">Volumes</h3><div class="table-wrap"><table><tbody>${(d.volumes || []).map((x) => `<tr><td class="mono" style="font-size:.8rem">${esc(x.name)}</td><td class="r">${bytes(x.size)}</td></tr>`).join('')}</tbody></table></div>
              <h3 style="margin:16px 0 4px">Maiores imagens</h3><div class="table-wrap"><table><tbody>${(d.images || []).slice(0, 8).map((x) => `<tr><td class="mono" style="font-size:.8rem">${esc(x.tags.join(', '))}</td><td class="r">${x.containers ? '' : '<span class="muted">sem uso · </span>'}${bytes(x.size)}</td></tr>`).join('')}</tbody></table></div>` : '<div class="loading"><div class="spinner"></div></div>'}
            </div>
            <div class="card"><div class="card-h"><div><h2>Eventos dos contêineres</h2><div class="muted" style="font-size:.82rem">Subidas, quedas, reinícios e healthchecks.</div></div></div>
              ${s.events.length ? `<div class="table-wrap"><table><tbody>${s.events.slice(0, 60).map((e) => `<tr><td class="muted num" style="white-space:nowrap">${dt(e.t)}</td><td>${esc(e.container)}</td><td>${eventLabel(e)}</td></tr>`).join('')}</tbody></table></div>` : '<div class="empty">Nenhum evento nas últimas 24 h.</div>'}
            </div>
          </section>`;
      };
      systemView.reload = load;
      load();
      later(load, 5000);
    },
  };
  function eventLabel(e) {
    const a = e.action;
    if (a === 'die') return e.requested ? badge('off', 'parou (deploy/stop)') : e.exitCode && e.exitCode !== '0' ? badge('crit', `caiu (código ${e.exitCode})`) : badge('off', 'parou');
    if (a === 'oom' || a === 'oom_kill_host') return badge('crit', 'sem memória (OOM)');
    if (a === 'start') return badge('ok', 'subiu');
    if (a === 'restart') return badge('warn', 'reiniciou');
    if (a.startsWith('health:')) return a.includes('unhealthy') ? badge('crit', 'unhealthy') : badge('ok', a.replace('health:', '').trim());
    const map = { create: 'criado', destroy: 'removido', kill: 'sinal enviado', stop: 'parado', pause: 'pausado', unpause: 'retomado', rename: 'renomeado', update: 'atualizado' };
    return `<span class="ink2">${esc(map[a] || a)}</span>`;
  }

  // ------------------------------------------------------------------ aba: limites
  const limitsView = {
    mount(v) {
      v.innerHTML = `<div class="page"><div class="section-h"><div><h2>Limites do plano grátis</h2>
        <p id="lm-sub">Carregando…</p></div></div>
        <section class="alerts" id="lm-alerts"></section>
        <section class="grid g2" id="lm-items"></section>
        <section class="card" id="lm-reclaim"></section>
        <section class="card"><h2 style="margin-bottom:10px">Cloudflare (plano Free)</h2><div class="kv">
          ${kv('Banda dos túneis', 'sem limite')}${kv('Upload por requisição', 'até 100 MB')}${kv('Resposta mais lenta', 'cai em 100 s')}${kv('Custo', 'R$ 0')}</div>
          <div class="note" style="margin-top:12px">Os sites saem pelo túnel (cloudflared), então o tráfego para os visitantes passa pela Cloudflare — mas sai do servidor do mesmo jeito e conta para os 10 TB da Oracle.</div></section></div>`;
    },
    update() {
      const o = S.ov, L = o.limits;
      const sub = {
        a1: `VM ${L.shape} em ${L.region}, no plano Always Free da Oracle (Ampere A1). Os limites da Oracle são da conta inteira; aqui aparece o que esta VM usa.`,
        micro: `VM ${L.shape} em ${L.region}, no plano Always Free da Oracle (AMD Micro).`,
        paid: `VM ${L.shape} em ${L.region}: este shape não é Always Free (é cobrado). Aqui ficam o disco e a saída de dados.`,
      }[L.kind] || 'Este servidor não parece ser uma VM da Oracle: aqui ficam só o disco e a saída de dados.';
      $('#lm-sub').textContent = sub;
      $('#lm-reclaim').classList.toggle('hidden', L.kind !== 'a1' && L.kind !== 'micro');
      $('#lm-alerts').innerHTML = alertsHTML(o.alerts.filter((a) => a.area === 'limite'), 'Nenhum limite do plano grátis em risco.');
      const fmt = (it, val) => (it.unit === 'bytes' ? (it.key === 'egress' ? data(val) : bytes(val)) : it.unit === 'ocpu' ? `${num(val, 0)} OCPU` : it.unit === 'vm' ? `${num(val, 0)} VM` : `${num(val, 0)} GB`);
      $('#lm-items').innerHTML = L.items.map((it) => {
        const p = it.limit ? (it.used / it.limit) * 100 : 0;
        const lv = it.level === 'crit' ? 'crit' : it.level === 'warn' ? 'warn' : 'ok';
        return `<div class="card"><div class="card-h"><h3>${esc(it.label)}</h3>${badge(lv, pct(p))}</div>
          <div class="kpi-value num" style="margin-bottom:8px">${fmt(it, it.used)} <small>de ${fmt(it, it.limit)}</small></div>
          <div class="meter thick"><i class="${it.level === 'crit' ? 'crit' : it.level === 'warn' ? 'warn' : ''}" style="width:${Math.min(100, p).toFixed(1)}%"></i></div>
          <div class="muted" style="font-size:.8rem;margin-top:8px">${esc(it.note)}</div></div>`;
      }).join('');
      const r = L.reclaim;
      const bar = (label, val, tip) => `<div style="display:grid;gap:4px"><div style="display:flex;justify-content:space-between;font-size:.85rem"><span>${label}</span><span class="num"><b>${pct(val)}</b> <span class="muted">· regra: abaixo de 20%</span></span></div>
        <div class="meter thick" style="position:relative"><i class="${val < 20 ? 'warn' : ''}" style="width:${Math.min(100, val).toFixed(1)}%"></i></div><div class="muted" style="font-size:.76rem">${tip}</div></div>`;
      const verdict = !r.ready ? badge('info', `coletando · ${num(r.dataDays, 1)} de 7 dias`)
        : r.idle ? badge('warn', 'parece ociosa') : badge('ok', 'não ociosa');
      $('#lm-reclaim').innerHTML = `<div class="card-h"><div><h2>Risco de a Oracle recuperar a VM por ociosidade</h2>
        <div class="muted" style="font-size:.82rem">Regra do Always Free: se em 7 dias a CPU (percentil 95), a rede e a memória ficarem <b>todas</b> abaixo de 20%, a Oracle pode desligar e recuperar a instância.</div></div>${verdict}</div>
        <div style="display:grid;gap:14px">
          ${bar('CPU — percentil 95 em 7 dias', r.cpuP95, '95% do tempo a CPU ficou abaixo disso.')}
          ${bar('Memória — média em 7 dias', r.memAvg, 'Memória em uso (sem cache) sobre o total.')}
          ${bar('Rede — média em 7 dias', r.netAvg, 'Tráfego sobre a banda do shape (informada pela Oracle).')}
        </div>
        <div class="note" style="margin-top:14px">${r.applies === 'no' ? 'Configurado como conta <b>Pay As You Go</b>: essa regra não se aplica.'
          : r.applies === 'yes' ? 'Configurado como conta <b>Always Free</b>: a regra se aplica. Basta <b>um</b> dos três ficar acima de 20% para não contar como ociosa.'
            : 'Não sei se a conta é Always Free ou Pay As You Go (contas pagas não sofrem essa recuperação). Ajuste <b>VPMON_ALWAYS_FREE</b> no .env do painel. Basta <b>um</b> dos três acima de 20% para a VM não contar como ociosa.'}
          Estimativa feita com médias de 5 min; a Oracle mede com o próprio agente.</div>`;
    },
  };

  // ------------------------------------------------------------------ aba: infos (urgente, alertas, informações)
  const infosView = {
    mount(v) {
      v.innerHTML = `<div class="page"><div class="section-h"><div><h2>Infos</h2>
        <p>Tudo que o painel percebeu, do mais urgente ao só-para-saber. Atualiza sozinho a cada 5 s.</p></div></div>
        <div id="if-body"><div class="loading"><div class="spinner"></div></div></div></div>`;
    },
    update() {
      const al = S.ov.alerts;
      const sec = (lvl, title, empty) => {
        const list = al.filter((a) => a.level === lvl);
        return `<section><div class="section-h" style="margin-bottom:8px"><h2>${title} <span class="count ${list.length ? lvl : 'info'}">${list.length}</span></h2></div>
          <div class="alerts">${list.length ? alertsHTML(list) : `<div class="empty card">${empty}</div>`}</div></section>`;
      };
      $('#if-body').innerHTML = `<div class="page">
        ${sec('crit', 'Urgente', 'Nada urgente agora.')}
        ${sec('warn', 'Alertas', 'Nenhum alerta.')}
        ${sec('info', 'Informações', 'Nada a observar.')}</div>`;
    },
  };

  // ------------------------------------------------------------------ markdown (seguro: tudo passa por esc antes)
  function md(src) {
    const lines = String(src || '').replace(/\r/g, '').split('\n');
    const inline = (s) => esc(s)
      .replace(/`([^`]+)`/g, '<code>$1</code>')
      .replace(/\*\*([^*]+)\*\*/g, '<b>$1</b>')
      .replace(/(^|[\s(])\*([^*\s][^*]*?)\*(?=[\s).,;:!?]|$)/g, '$1<i>$2</i>');
    const cells = (l) => l.trim().replace(/^\|/, '').replace(/\|$/, '').split('|').map((c) => c.trim());
    let out = '', i = 0;
    while (i < lines.length) {
      const l = lines[i];
      if (/^\s*```/.test(l)) {
        const buf = [];
        i++;
        while (i < lines.length && !/^\s*```/.test(lines[i])) buf.push(lines[i++]);
        i++;
        out += `<pre><code>${esc(buf.join('\n'))}</code></pre>`;
      } else if (/^\s*\|.*\|\s*$/.test(l) && i + 1 < lines.length && /^\s*\|?\s*:?-{2,}/.test(lines[i + 1])) {
        const head = cells(l);
        i += 2;
        const rows = [];
        while (i < lines.length && /^\s*\|.*\|\s*$/.test(lines[i])) rows.push(cells(lines[i++]));
        out += `<div class="table-wrap"><table><thead><tr>${head.map((h) => `<th>${inline(h)}</th>`).join('')}</tr></thead><tbody>${rows.map((r) => `<tr>${r.map((c) => `<td>${inline(c)}</td>`).join('')}</tr>`).join('')}</tbody></table></div>`;
      } else if (/^#{1,4}\s/.test(l)) {
        out += `<h4>${inline(l.replace(/^#+\s*/, ''))}</h4>`;
        i++;
      } else if (/^\s*[-*•]\s+/.test(l)) {
        const items = [];
        while (i < lines.length && /^\s*[-*•]\s+/.test(lines[i])) items.push(lines[i++].replace(/^\s*[-*•]\s+/, ''));
        out += `<ul>${items.map((x) => `<li>${inline(x)}</li>`).join('')}</ul>`;
      } else if (/^\s*\d+[.)]\s+/.test(l)) {
        const items = [];
        while (i < lines.length && /^\s*\d+[.)]\s+/.test(lines[i])) items.push(lines[i++].replace(/^\s*\d+[.)]\s+/, ''));
        out += `<ol>${items.map((x) => `<li>${inline(x)}</li>`).join('')}</ol>`;
      } else if (!l.trim()) {
        i++;
      } else {
        const buf = [];
        while (i < lines.length && lines[i].trim() && !/^\s*(```|#{1,4}\s|[-*•]\s+|\d+[.)]\s+|\|)/.test(lines[i])) buf.push(lines[i++]);
        if (!buf.length) buf.push(lines[i++]);
        out += `<p>${buf.map(inline).join('<br>')}</p>`;
      }
    }
    return out;
  }

  // ------------------------------------------------------------------ aba: IA (chat com a DeepSeek)
  const SUGGEST = [
    'Como está o servidor agora? Tem algo preocupante?',
    'Qual app mais usou CPU e memória nas últimas 24 h?',
    'Quanto de banda cada app enviou este mês?',
    'Tem erros importantes nos logs da última hora?',
    'O que dá para limpar no disco com segurança?',
    'Estou perto da regra de VM ociosa da Oracle?',
  ];
  const aiView = {
    mount(v) {
      S.chat = store.get('chat', []);
      v.innerHTML = `<div class="page">
        <div class="section-h"><div><h2>${icon('spark')} Pergunte sobre o servidor</h2>
          <p id="ai-sub">Carregando…</p></div>
          <button class="btn" type="button" data-act="chat-new">Nova conversa</button></div>
        <section class="card chat">
          <div class="chat-log" id="chat-log" aria-live="polite"></div>
          <form class="chat-form" id="chat-form">
            <textarea class="input" id="chat-in" rows="1" placeholder="Pergunte algo… ex.: quem mais usou CPU hoje?" aria-label="Pergunta"></textarea>
            <button class="btn primary" type="submit" id="chat-send" aria-label="Enviar">${icon('send')}<span>Enviar</span></button>
          </form>
          <div class="chat-note">As perguntas e os dados que a IA consulta vão para a DeepSeek. Nos logs, tokens, senhas e e-mails seguem mascarados e o fim dos IPs é escondido.</div>
        </section></div>`;
      api('/api/chat/status').then((st) => {
        S.chatOn = st.enabled;
        $('#ai-sub').innerHTML = st.enabled
          ? esc(`DeepSeek (${st.model}) com acesso só de leitura ao estado atual, ao histórico, à banda, aos logs, aos processos e aos eventos.`)
          : can.admin() ? `A IA está desligada. <a href="#" data-act="settings" data-v="ia">Ponha a chave da DeepSeek em Configurações → IA</a>.`
            : 'A IA está desligada. Um administrador pode ligar em Configurações → IA.';
        aiView.render();
      }).catch(() => {});
      const ta = $('#chat-in');
      const grow = () => { ta.style.height = 'auto'; ta.style.height = Math.min(160, ta.scrollHeight) + 'px'; };
      ta.addEventListener('input', grow);
      ta.addEventListener('keydown', (e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); $('#chat-form').requestSubmit(); } });
      $('#chat-form').addEventListener('submit', (e) => {
        e.preventDefault();
        if (S.chatBusy) { S.chatAbort && S.chatAbort.abort(); return; }
        const q = ta.value.trim();
        if (!q) return;
        ta.value = '';
        grow();
        aiView.ask(q);
      });
      S.cleanup.push(() => { S.chatAbort && S.chatAbort.abort(); });
      aiView.render();
      setTimeout(() => ta.focus(), 50);
    },
    save() { store.set('chat', (S.chat || []).slice(-40).map(({ role, content, tools, meta, error }) => ({ role, content, tools, meta, error }))); },
    render() {
      const log = $('#chat-log');
      if (!log) return;
      const near = log.scrollHeight - log.scrollTop - log.clientHeight < 80;
      if (!S.chat.length) {
        log.innerHTML = `<div class="chat-empty">${icon('spark')}<div><b>O que você quer saber do servidor?</b><br><span class="muted">A IA lê os dados do painel e explica com números.</span></div>
          <div class="suggest">${SUGGEST.map((s) => `<button type="button" class="chip-btn" data-act="chat-ask" data-v="${esc(s)}" ${S.chatOn === false ? 'disabled' : ''}>${esc(s)}</button>`).join('')}</div></div>`;
      } else {
        log.innerHTML = S.chat.map((m, idx) => m.role === 'user'
          ? `<div class="msg user">${esc(m.content)}</div>`
          : `<div class="msg ai">${(m.tools || []).length ? `<div class="tools">${m.tools.map((t) => `<span class="tool-chip">${icon('search')}${esc(t)}</span>`).join('')}</div>` : ''}
              <div class="md">${md(m.content)}${m.streaming ? '<span class="caret"></span>' : ''}</div>
              ${m.error ? `<div class="alert crit slim"><div class="ic">${icon('crit')}</div><div class="alert-body"><div class="alert-t">${esc(m.error)}</div></div></div>` : ''}
              ${m.meta ? `<div class="msg-meta">${esc(m.meta)}${idx === S.chat.length - 1 && !m.streaming ? '' : ''}</div>` : ''}</div>`).join('');
      }
      if (near || S.chatBusy) log.scrollTop = log.scrollHeight;
      const btn = $('#chat-send');
      if (btn) {
        btn.innerHTML = S.chatBusy ? `${icon('stop')}<span>Parar</span>` : `${icon('send')}<span>Enviar</span>`;
        btn.disabled = S.chatOn === false;
      }
    },
    async ask(q) {
      if (S.chatBusy || S.chatOn === false) return;
      const history = S.chat.filter((m) => m.content && !m.error).map((m) => ({ role: m.role, content: m.content }));
      history.push({ role: 'user', content: q });
      S.chat.push({ role: 'user', content: q });
      const msg = { role: 'assistant', content: '', tools: [], streaming: true };
      S.chat.push(msg);
      S.chatBusy = true;
      S.chatAbort = new AbortController();
      aiView.render();
      let raf = 0;
      const paint = () => { if (!raf) raf = requestAnimationFrame(() => { raf = 0; aiView.render(); }); };
      const t0 = Date.now();
      try {
        const r = await fetch('/api/chat', {
          method: 'POST', credentials: 'same-origin', signal: S.chatAbort.signal,
          headers: { 'X-Requested-With': 'vpmon', 'Content-Type': 'application/json' },
          body: JSON.stringify({ messages: history }),
        });
        if (r.status === 401) { showLogin(); return; }
        if (!r.ok) {
          const j = await r.json().catch(() => ({}));
          throw new Error((j.error && j.error.message) || `Erro ${r.status}`);
        }
        const reader = r.body.getReader();
        const dec = new TextDecoder();
        let buf = '';
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          buf += dec.decode(value, { stream: true });
          let k;
          while ((k = buf.indexOf('\n\n')) >= 0) {
            const block = buf.slice(0, k);
            buf = buf.slice(k + 2);
            let ev = 'message', data = '';
            for (const line of block.split('\n')) {
              if (line.startsWith('event:')) ev = line.slice(6).trim();
              else if (line.startsWith('data:')) data += line.slice(5).trim();
            }
            if (!data) continue;
            const d = JSON.parse(data);
            if (ev === 'delta') msg.content += d.text;
            else if (ev === 'tool') {
              msg.tools.push(d.label);
              if (msg.content && !msg.content.endsWith('\n')) msg.content += '\n\n';
            } else if (ev === 'error') msg.error = d.message;
            else if (ev === 'done') {
              const u = d.usage || {};
              const tok = (u.prompt_tokens || 0) + (u.completion_tokens || 0);
              msg.meta = `${d.model} · ${num(tok / 1000, 1)} mil tokens · ${num((Date.now() - t0) / 1000, 0)} s`;
            }
            paint();
          }
        }
      } catch (e) {
        if (e.name === 'AbortError') msg.error = msg.content ? '' : 'Pergunta cancelada.';
        else msg.error = e.message;
        if (e.name === 'AbortError' && msg.content) msg.meta = 'interrompida';
      } finally {
        msg.streaming = false;
        if (!msg.content && !msg.error) msg.error = 'A IA não respondeu nada. Tente de novo.';
        S.chatBusy = false;
        S.chatAbort = null;
        aiView.save();
        aiView.render();
      }
    },
  };

  // ------------------------------------------------------------------ WhatsApp: conexão (aba Notificações, Configurações e primeiro acesso)
  let waTimer = null, waQRTimer = null;
  function stopWA() { clearInterval(waTimer); clearInterval(waQRTimer); waTimer = waQRTimer = null; }
  function fmtPhone(n) {
    const d = String(n || '');
    if (d.endsWith('@g.us')) return 'grupo';
    if (d.startsWith('55') && (d.length === 12 || d.length === 13)) return `+55 ${d.slice(2, 4)} ${d.slice(4, -4)}-${d.slice(-4)}`;
    return d ? '+' + d : '';
  }
  // *negrito* e _itálico_ do WhatsApp (tudo escapado antes)
  const waText = (t) => esc(t).replace(/\*([^*\n]+)\*/g, '<b>$1</b>').replace(/(^|[\s(])_([^_\n]+)_(?=$|[\s).,!?:])/gm, '$1<i>$2</i>').replace(/\n/g, '<br>');
  function waStatusHTML(n) {
    if (!n.installed) return badge('off', 'WhatsApp não instalado');
    const st = n.status;
    if (!st.service) return badge('crit', 'Serviço fora do ar');
    if (st.state === 'open') return `${badge('ok', 'Conectado')} <span class="muted wa-who">${esc([st.name, fmtPhone(st.number)].filter(Boolean).join(' · '))}</span>`;
    if (st.state === 'connecting') return badge('warn', 'Esperando o celular');
    return badge('off', 'Desconectado');
  }
  function waBoxHTML(n) {
    if (!n.installed) {
      return `<div class="note">O WhatsApp das notificações não está rodando neste servidor. Para ligar, no <code>.env</code> do painel ponha
        <code>COMPOSE_PROFILES=whatsapp</code> (as chaves <code>VPMON_WA_KEY</code> e <code>VPMON_WA_DB_PASSWORD</code> o <code>vpmon init</code> já gera)
        e rode <code>docker compose up -d</code>. Usa ~250 MB de RAM.</div>`;
    }
    const st = n.status;
    if (!st.service) {
      return `<div class="wa-row">${waStatusHTML(n)}</div><div class="note" style="margin-top:10px">A Evolution (contêiner <b>vpserver-whatsapp</b>) não respondeu${st.error ? `: ${esc(st.error)}` : ''}.
        Logo depois de subir ela leva cerca de 1 minuto. Veja em Aplicações → vpserver-monitoring.</div>`;
    }
    if (st.state === 'open') {
      return `<div class="wa-row"><span class="wa-st">${waStatusHTML(n)}</span><button class="btn" type="button" data-wa="logout">Desconectar</button></div>`;
    }
    return `<div class="wa-row">${waStatusHTML(n)}</div>
      <div id="wa-qr" class="wa-qr">
        <p class="muted" style="margin:0;font-size:.84rem">Dica: conecte um número só para os avisos (um chip ou um WhatsApp Business de outro número) e mande para o seu.
          Mensagem do seu próprio número para você mesmo chega sem tocar.</p>
        <div class="controls"><button class="btn primary" type="button" data-wa="qr">${icon('qr')}Mostrar QR code</button>
        <button class="btn" type="button" data-wa="code">${icon('phone')}Conectar com código</button></div>
      </div>`;
  }
  // liga os botões da caixa de conexão; onChange(resposta de /api/notify) quando conecta ou desconecta
  function bindWA(box, onChange) {
    let finished = false;
    const done = async () => {
      if (finished) return;
      finished = true;
      stopWA();
      toast('WhatsApp conectado.');
      try { onChange(await api('/api/notify')); } catch { /* segue */ }
    };
    const watch = () => {
      clearInterval(waTimer);
      waTimer = setInterval(async () => {
        if (document.hidden) return;
        try { const j = await api('/api/notify'); if (j.notify.status.state === 'open') done(); } catch { /* tenta de novo */ }
      }, 3000);
    };
    const showQR = async () => {
      const area = $('#wa-qr', box);
      if (!area) return;
      stopWA();
      area.innerHTML = '<div class="wa-qrbox"><div class="spinner"></div></div>';
      const load = async () => {
        try {
          const qr = await api('/api/notify/connect', { method: 'POST', body: '{}' });
          if (qr.connected) { done(); return; }
          if (!$('#wa-qr', box)) { stopWA(); return; }
          $('#wa-qr', box).innerHTML = `<div class="wa-qrbox">${qr.image ? `<img alt="QR code para conectar o WhatsApp" src="${esc(qr.image)}">` : '<div class="spinner"></div>'}</div>
            <ol class="howto"><li>No celular: WhatsApp → <b>Aparelhos conectados</b> → <b>Conectar aparelho</b>.</li>
            <li>Aponte a câmera para o código. Ele muda a cada ~20 s e se atualiza sozinho aqui.</li></ol>
            <button class="btn" type="button" data-wa="code">${icon('phone')}Estou no celular: prefiro um código</button>`;
        } catch (ex) {
          stopWA();
          if (ex.message !== 'login' && $('#wa-qr', box)) $('#wa-qr', box).innerHTML = `<div class="form-err">${esc(ex.message)}</div><button class="btn" type="button" data-wa="qr">Tentar de novo</button>`;
        }
      };
      await load();
      clearInterval(waQRTimer);
      waQRTimer = setInterval(() => { if (!document.hidden) load(); }, 25000);
      watch();
    };
    const showCode = () => {
      const area = $('#wa-qr', box);
      if (!area) return;
      stopWA();
      area.innerHTML = `<form class="stack" id="wa-code-f"><div class="field"><label for="wa-num">Número do WhatsApp que vai mandar os avisos (com DDI)</label>
        <input class="input" id="wa-num" inputmode="tel" autocomplete="tel" placeholder="+55 11 91234-5678" value="+55 "></div>
        <div class="form-err" id="wa-err" role="alert"></div>
        <div class="controls"><button class="btn primary" type="submit">Gerar código</button><button class="btn" type="button" data-wa="qr">${icon('qr')}Usar QR code</button></div></form>`;
      const inp = $('#wa-num', area);
      inp.focus();
      inp.setSelectionRange(inp.value.length, inp.value.length);
      $('#wa-code-f', area).addEventListener('submit', async (e) => {
        e.preventDefault();
        const btn = $('#wa-code-f button[type=submit]', area);
        btn.disabled = true;
        $('#wa-err', area).textContent = '';
        try {
          const qr = await api('/api/notify/connect', { method: 'POST', body: JSON.stringify({ number: inp.value }) });
          if (qr.connected) { done(); return; }
          if (!qr.pairingCode) throw new Error('A Evolution não devolveu o código. Espere alguns segundos e tente de novo.');
          const c = qr.pairingCode;
          area.innerHTML = `<div class="wa-code num" aria-label="Código de pareamento">${esc(c.slice(0, 4))}-${esc(c.slice(4))}</div>
            <ol class="howto"><li>No celular: WhatsApp → <b>Aparelhos conectados</b> → <b>Conectar aparelho</b>.</li>
            <li>Toque em <b>Conectar com número de telefone</b> e digite o código acima.</li></ol>
            <button class="btn" type="button" data-wa="qr">${icon('qr')}Usar QR code</button>`;
          watch();
        } catch (ex) {
          if (ex.message !== 'login') $('#wa-err', area).textContent = ex.message;
          btn.disabled = false;
        }
      });
    };
    box.onclick = async (e) => {
      const b = e.target.closest('[data-wa]');
      if (!b) return;
      const act = b.dataset.wa;
      if (act === 'qr') showQR();
      else if (act === 'code') showCode();
      else if (act === 'logout') {
        if (b.dataset.sure !== '1') { b.dataset.sure = '1'; b.textContent = 'Confirmar: desconectar'; return; }
        b.disabled = true;
        try {
          await api('/api/notify/logout', { method: 'POST', body: '{}' });
          toast('WhatsApp desconectado.');
          onChange(await api('/api/notify'));
        } catch (ex) { if (ex.message !== 'login') toast(ex.message); b.disabled = false; }
      }
    };
  }

  // ------------------------------------------------------------------ aba: notificações
  const NT_GROUPS = [
    ['alertas', 'Alertas', 'Saem quando o problema começa (depois de durar alguns minutos, para não avisar pico de segundos), quando piora e, se quiser, quando resolve.'],
    ['mudancas', 'Mudanças e segurança', ''],
    ['resumos', 'Resumos', 'Saem no horário abaixo, sempre de um período fechado: ontem, os últimos 7 dias, o mês anterior.'],
    ['ia', 'Análises com IA', 'A IA lê os dados do painel (só leitura, com logs mascarados) e manda uma análise curta. Usa a sua chave da DeepSeek.'],
  ];
  const NT_STATUS = { sent: ['ok', 'Enviada'], partial: ['warn', 'Parcial'], failed: ['crit', 'Não saiu'], held: ['info', 'Segurada'], skipped: ['off', 'Ignorada'] };
  const notifyView = {
    mount(v) {
      v.innerHTML = `<div class="page"><div class="section-h"><div><h2>${icon('bell')} Notificações pelo WhatsApp</h2>
        <p>Alertas, resumos e análises do servidor no seu WhatsApp. Tudo se configura aqui e salva sozinho.</p></div>
        <span class="muted nt-saved" id="nt-saved" role="status"></span></div>
        <div id="nt-body"><div class="loading"><div class="spinner"></div></div></div></div>`;
      notifyView.listen($('#nt-body'));
      notifyView.load();
    },
    async load() {
      try {
        const j = await api('/api/notify');
        S.nt = j.notify;
        S.ntAI = j.ai;
        notifyView.render();
      } catch (e) {
        if (e.message !== 'login' && $('#nt-body')) $('#nt-body').innerHTML = `<div class="card empty">${esc(e.message)}</div>`;
      }
    },
    render() {
      const body = $('#nt-body');
      if (!body || !S.nt) return;
      const n = S.nt, c = n.config, st = n.status;
      const kinds = (g) => n.catalog.filter((k) => k.group === g);
      const locked = (k) => k.ai && !S.ntAI;
      const opt = (k) => `<label class="opt${locked(k) ? ' locked' : ''}">
        <input class="sw" type="checkbox" data-ev="${esc(k.key)}" ${c.events[k.key] && !locked(k) ? 'checked' : ''} ${locked(k) ? 'disabled' : ''}>
        <span class="opt-t">${esc(k.label)}</span><span class="opt-d">${esc(k.desc)}</span></label>`;
      const warnings = [];
      if (n.installed && st.state !== 'open') warnings.push('O WhatsApp não está conectado: nada sai até conectar.');
      if (!c.recipients.length) warnings.push('Ninguém em "Para quem enviar": nada sai até adicionar um número ou grupo.');
      if (!c.enabled) warnings.push('As notificações estão desligadas.');
      if (n.pending && warnings.length) warnings.push(`${n.pending} alerta(s) esperando para sair.`);
      body.innerHTML = `<div class="page">
        ${warnings.length ? `<div class="alert warn slim"><div class="ic">${icon('warn')}</div><div class="alert-body"><div class="alert-t">${warnings.map(esc).join(' ')}</div></div></div>` : ''}
        <div class="grid g2">
          <section class="card"><div class="card-h"><h2>${icon('whats')}Conexão</h2></div><div id="wa-box">${waBoxHTML(n)}</div></section>
          <section class="card"><div class="card-h"><h2>${icon('users')}Para quem enviar</h2>
            <label class="sw-l"><input class="sw" type="checkbox" id="nt-on" ${c.enabled ? 'checked' : ''}><span>${c.enabled ? 'Ligadas' : 'Desligadas'}</span></label></div>
            <div id="nt-rcpts"></div>
            <form class="nt-add" id="nt-add" autocomplete="off">
              <div class="field"><label for="nt-name">Nome (opcional)</label><input class="input" id="nt-name" maxlength="40" placeholder="Ex.: Ana"></div>
              <div class="field"><label for="nt-num">WhatsApp com DDI</label><input class="input" id="nt-num" inputmode="tel" placeholder="+55 11 91234-5678"></div>
              <button class="btn" type="submit">${icon('plus')}Adicionar</button>
            </form>
            <div class="controls" style="margin-top:10px">
              ${st.state === 'open' && st.number ? `<button class="btn" type="button" id="nt-me">${icon('phone')}Este número (${esc(fmtPhone(st.number))})</button>` : ''}
              ${st.state === 'open' ? `<button class="btn" type="button" id="nt-groups">${icon('users')}Escolher um grupo</button>` : ''}
            </div>
            <div id="nt-glist"></div></section>
        </div>
        <section class="card"><div class="card-h"><h2>${icon('bell')}O que avisar</h2></div>
          ${NT_GROUPS.map(([g, title, desc]) => `<div class="nt-group"><h3>${title}</h3>${desc ? `<p class="muted">${esc(desc)}</p>` : ''}
            ${g === 'ia' && !S.ntAI ? `<div class="lock-note">${icon('lock')}<span>Para usar as análises, configure a IA (chave da DeepSeek).</span>
              <button class="btn" type="button" data-act="settings" data-v="ia">Configurar a IA</button></div>` : ''}
            <div class="opts">${kinds(g).map(opt).join('')}</div></div>`).join('')}
        </section>
        <section class="card"><div class="card-h"><h2>${icon('clock')}Horários</h2></div>
          <div class="nt-times">
            <div class="field"><label for="nt-daily">Resumos e análises saem às</label><input class="input" type="time" id="nt-daily" value="${esc(c.dailyAt)}"></div>
            <div class="nt-quiet"><label class="sw-l"><input class="sw" type="checkbox" id="nt-quiet" ${c.quiet ? 'checked' : ''}><span>Horário de silêncio</span></label>
              <div class="nt-range"><div class="field"><label for="nt-qf">das</label><input class="input" type="time" id="nt-qf" value="${esc(c.quietFrom)}" ${c.quiet ? '' : 'disabled'}></div>
              <div class="field"><label for="nt-qt">às</label><input class="input" type="time" id="nt-qt" value="${esc(c.quietTo)}" ${c.quiet ? '' : 'disabled'}></div></div></div>
          </div>
          <p class="muted" style="margin:10px 0 0;font-size:.8rem">No silêncio, só o urgente (app caiu, servidor no limite, segurança) sai na hora; o resto chega numa mensagem só quando o silêncio acaba.
            Fuso do painel: ${esc(n.tz)} (<code>VPMON_TZ</code>).${n.held ? ` Agora há ${n.held} mensagem(ns) segurada(s).` : ''}</p>
        </section>
        <section class="card"><div class="card-h"><h2>${icon('send')}Enviar agora</h2></div>
          <div class="controls">
            <button class="btn" type="button" data-send="test">Mensagem de teste</button>
            <button class="btn" type="button" data-send="daily">Resumo de ontem</button>
            <button class="btn" type="button" data-send="weekly">Resumo da semana</button>
            <button class="btn" type="button" data-send="monthly">Fechamento do mês</button>
            ${['ai_daily', 'ai_weekly', 'ai_logs'].map((k) => { const kk = n.catalog.find((x) => x.key === k); return `<button class="btn" type="button" data-send="${k}" ${S.ntAI ? '' : 'disabled title="Configure a IA para usar"'}>${icon(S.ntAI ? 'spark' : 'lock')}${esc(kk ? kk.label.replace(' no resumo diário', '') : k)}</button>`; }).join('')}
          </div>
          ${n.running.length ? `<p class="muted" style="margin:10px 0 0;font-size:.8rem"><span class="spinner inline"></span> A IA está montando ${n.running.length} análise(s)…</p>` : ''}
        </section>
        <section class="card"><div class="card-h"><h2>${icon('logs')}Últimas mensagens</h2><button class="btn" type="button" id="nt-reload">${icon('refresh')}Atualizar</button></div>
          <div class="nlog">${n.log.length ? n.log.map((e) => {
            const [lv, label] = NT_STATUS[e.status] || NT_STATUS.sent;
            return `<details class="nl"><summary><span class="nl-time">${dt(e.t)}</span>${badge(lv, label)}<span class="nl-title">${esc(e.title || e.kind)}</span>
              ${e.error ? `<span class="nl-err">${esc(e.error)}</span>` : ''}</summary>${e.text ? `<div class="wa-msg">${waText(e.text)}</div>` : ''}</details>`;
          }).join('') : '<div class="empty">Nenhuma mensagem ainda.</div>'}</div></section>
      </div>`;
      notifyView.renderRecipients();
      notifyView.bind();
      if (n.running.length) {
        clearTimeout(notifyView.rt);
        notifyView.rt = setTimeout(() => { if (S.tab === 'notify') notifyView.load(); }, 10000);
      }
    },
    renderRecipients() {
      const el = $('#nt-rcpts');
      if (!el) return;
      const rs = S.nt.config.recipients;
      el.innerHTML = rs.length ? `<div class="rcpts">${rs.map((r, i) => `<div class="rcpt">${icon(r.id.endsWith('@g.us') ? 'users' : 'phone')}
        <div class="rcpt-n"><b>${esc(r.name || (r.id.endsWith('@g.us') ? 'Grupo' : 'Sem nome'))}</b><span class="muted">${esc(r.id.endsWith('@g.us') ? 'grupo do WhatsApp' : fmtPhone(r.id))}</span></div>
        <button class="icon-btn" type="button" data-rm="${i}" aria-label="Remover ${esc(r.name || r.id)}" title="Remover">${icon('x')}</button></div>`).join('')}</div>`
        : '<div class="empty" style="padding:12px 0">Ninguém ainda. Adicione o seu WhatsApp abaixo.</div>';
    },
    // a cada desenho: a caixa de conexão e o formulário (que são recriados)
    bind() {
      const body = $('#nt-body');
      bindWA($('#wa-box', body), (j) => { S.nt = j.notify; notifyView.render(); });
      $('#nt-add').addEventListener('submit', (e) => {
        e.preventDefault();
        const num = $('#nt-num').value.trim();
        if (!num) { $('#nt-num').focus(); return; }
        notifyView.addRecipient(num, $('#nt-name').value.trim());
        $('#nt-num').value = '';
        $('#nt-name').value = '';
      });
    },
    // uma vez, na montagem: mudanças e cliques dentro da aba
    listen(body) {
      body.addEventListener('change', (e) => {
        const t = e.target;
        if (t.dataset.ev) { S.nt.config.events[t.dataset.ev] = t.checked; notifyView.save(); return; }
        if (t.id === 'nt-on') { S.nt.config.enabled = t.checked; t.nextElementSibling.textContent = t.checked ? 'Ligadas' : 'Desligadas'; notifyView.save(); return; }
        if (t.id === 'nt-quiet') { S.nt.config.quiet = t.checked; $('#nt-qf').disabled = $('#nt-qt').disabled = !t.checked; notifyView.save(); return; }
        const times = { 'nt-daily': 'dailyAt', 'nt-qf': 'quietFrom', 'nt-qt': 'quietTo' };
        if (times[t.id] && t.value) { S.nt.config[times[t.id]] = t.value; notifyView.save(); }
      });
      body.addEventListener('click', async (e) => {
        const rm = e.target.closest('[data-rm]');
        if (rm) { S.nt.config.recipients.splice(+rm.dataset.rm, 1); notifyView.renderRecipients(); notifyView.save(true); return; }
        const pick = e.target.closest('[data-group]');
        if (pick) { notifyView.addRecipient(pick.dataset.group, pick.dataset.name); $('#nt-glist').innerHTML = ''; return; }
        const send = e.target.closest('[data-send]');
        if (send) {
          send.disabled = true;
          try {
            const j = await api('/api/notify/send', { method: 'POST', body: JSON.stringify({ kind: send.dataset.send }) });
            toast(j.message);
          } catch (ex) { if (ex.message !== 'login') toast(ex.message); }
          send.disabled = false;
          setTimeout(() => { if (S.tab === 'notify') notifyView.load(); }, 1200);
          return;
        }
        if (e.target.closest('#nt-reload')) { notifyView.load(); return; }
        if (e.target.closest('#nt-me')) { notifyView.addRecipient(S.nt.status.number, 'Eu'); return; }
        if (e.target.closest('#nt-groups')) {
          const gl = $('#nt-glist');
          gl.innerHTML = '<div class="loading"><div class="spinner"></div></div>';
          try {
            const j = await api('/api/notify/groups');
            gl.innerHTML = j.groups.length ? `<div class="glist">${j.groups.sort((a, b) => a.subject.localeCompare(b.subject)).map((g) =>
              `<button class="chip-btn" type="button" data-group="${esc(g.id)}" data-name="${esc(g.subject)}">${esc(g.subject)} <span class="muted">· ${g.size}</span></button>`).join('')}</div>`
              : '<div class="empty" style="padding:12px 0">Este número não participa de nenhum grupo.</div>';
          } catch (ex) { gl.innerHTML = ex.message === 'login' ? '' : `<div class="form-err">${esc(ex.message)}</div>`; }
        }
      });
    },
    addRecipient(id, name) {
      if (S.nt.config.recipients.length >= 10) { toast('No máximo 10 destinos.'); return; }
      S.nt.config.recipients.push({ id: String(id), name: name || '' });
      notifyView.save(true);
    },
    // salva (com uma pausa curta para juntar cliques); now = já, e redesenha os destinos
    save(now) {
      clearTimeout(notifyView.t);
      const el = $('#nt-saved');
      if (el) el.textContent = 'Salvando…';
      notifyView.t = setTimeout(async () => {
        try {
          const j = await api('/api/notify/config', { method: 'POST', body: JSON.stringify({ ...S.nt.config, panelUrl: location.origin }) });
          S.nt.config = j.config;
          if ($('#nt-saved')) $('#nt-saved').textContent = `Salvo às ${hms(Date.now()).slice(0, 5)}`;
          notifyView.renderRecipients();
        } catch (ex) {
          if (ex.message === 'login') return;
          if ($('#nt-saved')) $('#nt-saved').textContent = '';
          toast(ex.message);
          notifyView.load(); // volta ao que está salvo
        }
      }, now ? 0 : 450);
    },
  };

  const VIEWS = { overview, infos: infosView, ai: aiView, apps: appsView, traffic: trafficView, logs: logsView, system: systemView, limits: limitsView, notify: notifyView };

  // ------------------------------------------------------------------ eventos globais
  document.addEventListener('click', async (e) => {
    const act = e.target.closest('[data-act]');
    const unit = e.target.closest('[data-unit]');
    if (act) {
      const a = act.dataset.act, v = act.dataset.v;
      if (a === 'range') { S.range = v; store.set('range', v); route(); return; }
      if (a === 'drange') { S.dRange = v; $$('[data-act="drange"]').forEach((b) => b.setAttribute('aria-pressed', b.dataset.v === v)); S.drawer && S.drawer.reload(); return; }
      if (a === 'trange') { trafficView.setRange && trafficView.setRange(v); return; }
      if (a === 'day') { trafficView.day(+act.dataset.i); return; }
      if (a === 'close') { closeDrawer(); return; }
      if (a === 'logpick') { logsView.pick(v); return; }
      if (a === 'psort') { S.procSort = v; systemView.reload && systemView.reload(); return; }
      if (a === 'more') { openMore(); return; }
      if (a === 'pause') { togglePause(v, act.dataset.pause === '1'); return; }
      if (a === 'theme') {
        if (act.closest('.sheet')) closeDrawer();
        const next = isDark() ? 'light' : 'dark';
        document.documentElement.setAttribute('data-theme', next);
        store.set('theme', next);
        try { localStorage.setItem('vpmon-theme', next); } catch { /* sem storage */ }
        $$('.icon-btn[data-act="theme"]').forEach((b) => { b.innerHTML = icon(isDark() ? 'sun' : 'moon'); });
        const reopen = S.drawer && $('.drawer') ? S.drawerKey : null;
        route();
        if (reopen) openUnit(reopen);
        return;
      }
      if (a === 'settings') { e.preventDefault(); openSettings(v || 'acesso'); return; }
      if (a === 'stab') { S.settingsTab && S.settingsTab(v); return; }
      if (a === 'chat-ask') { aiView.ask(v); return; }
      if (a === 'chat-new') { if (S.chatAbort) S.chatAbort.abort(); S.chat = []; aiView.save(); aiView.render(); const ta = $('#chat-in'); if (ta) ta.focus(); return; }
      if (a === 'logout') {
        try { await api('/api/logout', { method: 'POST', body: '{}' }); } catch { /* sai mesmo assim */ }
        showLogin();
        return;
      }
    }
    if (unit) { S.drawerKey = unit.dataset.unit; openUnit(unit.dataset.unit); }
  });
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') closeDrawer();
    if ((e.key === 'Enter' || e.key === ' ') && e.target.matches('.row[data-unit]')) { e.preventDefault(); e.target.click(); }
  });
  window.addEventListener('hashchange', () => { closeDrawer(); route(); });

  // ------------------------------------------------------------------ início
  async function start() {
    try { S.me = await api('/api/me'); } catch { return; }
    renderShell();
    route();
    await poll();
  }
  async function boot() {
    if (!window.uPlot) { await new Promise((r) => window.addEventListener('load', r, { once: true })); }
    let me;
    try { me = await api('/api/me'); } catch (e) { if (e.message !== 'login') showLogin('Não consegui falar com o painel.'); return; }
    if (me.mustChange) showSetup();
    else start();
  }
  boot();
})();
