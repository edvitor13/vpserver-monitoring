/* VPServer — interface do painel. JS puro + uPlot, sem build.
   Todo texto vindo do servidor passa por esc() antes de ir para o HTML. */
'use strict';
(() => {
  // ------------------------------------------------------------------ utilidades
  const $ = (s, el = document) => el.querySelector(s);
  const $$ = (s, el = document) => [...el.querySelectorAll(s)];
  const ESC = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
  const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ESC[c]);
  const store = {
    get(k, def) { try { const v = localStorage.getItem('vpmon-' + k); return v == null ? def : JSON.parse(v); } catch { return def; } },
    set(k, v) { try { localStorage.setItem('vpmon-' + k, JSON.stringify(v)); } catch { /* sem storage */ } },
  };

  // ------------------------------------------------------------------ idioma
  // O texto da tela está em português no código, sempre dentro de T('…'). Os outros
  // idiomas são catálogos em i18n/<idioma>.js (carregados antes deste arquivo), com a
  // frase em português como chave; {0}, {1}… são os valores, na ordem do idioma.
  // Sem tradução, fica o português. Primeiro acesso: o idioma do navegador.
  const LANGS = { 'pt-BR': 'Português', en: 'English' };
  function pickLang() {
    const saved = store.get('lang', '');
    if (LANGS[saved]) return saved;
    for (const l of navigator.languages || [navigator.language || '']) {
      if (/^pt\b/i.test(l)) return 'pt-BR';
      if (/^en\b/i.test(l)) return 'en';
    }
    return 'en';
  }
  const LANG = pickLang();
  const CAT = (window.VPMON_I18N || {})[LANG] || null;
  const LOCALE = LANG === 'en' ? 'en-US' : 'pt-BR';
  document.documentElement.lang = LANG;
  function T(s, vals) {
    const out = (CAT && CAT[s]) || s;
    return vals ? out.replace(/\{(\d+)\}/g, (m, i) => (vals[i] !== undefined ? vals[i] : m)) : out;
  }
  const langSegHTML = (save) => `<div class="seg" role="group" aria-label="Idioma · Language">${Object.entries(LANGS).map(([k, l]) => `<button type="button"
    data-act="lang" data-v="${k}" data-save="${save ? 1 : 0}" aria-pressed="${k === LANG}" lang="${k}">${l}</button>`).join('')}</div>`;
  // na tela de login: uma caixa; em Configurações → Minha conta: uma seção como as outras
  const langPickHTML = (save) => `<div class="lang-pick"><span class="lang-l">${icon('globe')}Idioma · Language</span>${langSegHTML(save)}</div>`;
  const langSectionHTML = () => `<section class="tf-section first"><h3>${icon('globe')}Idioma · Language</h3><div>${langSegHTML(true)}</div></section>`;
  // o idioma salvo na conta vale nos aparelhos que ainda não escolheram um
  function followAccountLang(me) {
    if (!me || !LANGS[me.lang] || me.lang === LANG || store.get('lang', '')) return false;
    store.set('lang', me.lang);
    location.reload();
    return true;
  }
  // troca o idioma (escolha explícita: vale neste aparelho e, logado, na conta)
  async function setLang(l, save) {
    if (!LANGS[l]) return;
    store.set('lang', l);
    if (save) await api('/api/lang', { method: 'POST', body: JSON.stringify({ lang: l }) }).catch(() => {});
    location.reload();
  }

  const NF = [0, 1, 2].map((d) => new Intl.NumberFormat(LOCALE, { maximumFractionDigits: d }));
  const num = (v, d = 1) => (v == null || isNaN(v) ? '—' : NF[d].format(v));
  const pad = (n) => String(n).padStart(2, '0');
  // dia e mês no jeito do idioma (31/12 ou 12/31)
  const dm = (d) => (LANG === 'en' ? `${pad(d.getMonth() + 1)}/${pad(d.getDate())}` : `${pad(d.getDate())}/${pad(d.getMonth() + 1)}`);

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
    if (s < 60) return T('agora');
    if (s < 3600) return `${T('há {0} min', [Math.floor(s / 60)])}`;
    if (s < 86400 * 2) return `${T('há {0} h', [Math.floor(s / 3600)])}`;
    return `${T('há {0} dias', [Math.floor(s / 86400)])}`;
  }
  const dt = (ts) => { const d = new Date(ts * 1000); return `${dm(d)} ${pad(d.getHours())}:${pad(d.getMinutes())}`; };
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
    shield: '<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/><path d="m9 12 2 2 4-4"/>',
    install: '<path d="M12 3v12M7 10l5 5 5-5"/><path d="M5 21h14"/>',
    broom: '<path d="m13 11 8-8"/><path d="M14.6 12.6c.8.8.9 2.1.2 3L10 22l-8-8 6.4-4.8c.9-.7 2.2-.6 3 .2z"/><path d="m6.8 10.4 6.8 6.8"/><path d="m5 17 1.5-1.5"/>',
    chev: '<polyline points="6 9 12 15 18 9"/>',
    db: '<ellipse cx="12" cy="5" rx="8" ry="3"/><path d="M4 5v6c0 1.7 3.6 3 8 3s8-1.3 8-3V5"/><path d="M4 11v6c0 1.7 3.6 3 8 3s8-1.3 8-3v-6"/>',
    upload: '<path d="M12 21V9M7 14l5-5 5 5"/><path d="M5 3h14"/>',
    globe: '<circle cx="12" cy="12" r="10"/><path d="M2 12h20"/><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/>',
    shell: '<rect x="2" y="4" width="20" height="16" rx="2"/><path d="m6 9 3 3-3 3M12 15h6"/>',
    eye: '<path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/>',
    eyeoff: '<path d="M17.9 17.9A10 10 0 0 1 12 20c-7 0-11-8-11-8a18.4 18.4 0 0 1 5.1-5.9"/><path d="M9.9 4.2A9 9 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.2 3.2"/><path d="m1 1 22 22"/><path d="M14.1 14.1a3 3 0 1 1-4.2-4.2"/>',
    layers: '<path d="m12 2 10 5-10 5L2 7z"/><path d="m2 17 10 5 10-5"/><path d="m2 12 10 5 10-5"/>',
    link: '<path d="M10 13a5 5 0 0 0 7.5.5l3-3a5 5 0 0 0-7-7l-1.7 1.7"/><path d="M14 11a5 5 0 0 0-7.5-.5l-3 3a5 5 0 0 0 7 7l1.7-1.7"/>',
    ext: '<path d="M15 3h6v6"/><path d="M10 14 21 3"/><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/>',
  };
  const icon = (n, cls = 'i') => `<svg class="${cls}" viewBox="0 0 24 24" aria-hidden="true">${P[n] || ''}</svg>`;
  const STATUS = { ok: ['ok', 'OK'], warn: ['warn', T('Atenção')], crit: ['crit', T('Crítico')], info: ['info', T('Info')], off: ['pause', T('Parado')] };
  function badge(lvl, label) {
    const k = lvl === 'stopped' ? 'off' : lvl;
    const [ic, def] = STATUS[k] || STATUS.info;
    return `<span class="badge ${k}">${icon(ic)}${esc(label || def)}</span>`;
  }
  function containerBadge(c) {
    if (!c) return '';
    if (c.state === 'running') {
      if (c.health === 'unhealthy') return badge('crit', T('Unhealthy'));
      if (c.health === 'starting') return badge('info', T('Iniciando'));
      return badge('ok', c.health === 'healthy' ? T('Saudável') : T('Rodando'));
    }
    if (c.state === 'restarting') return badge('crit', T('Reiniciando'));
    if (c.state === 'paused') return badge('off', T('Pausado'));
    return badge('off', T('Parado'));
  }
  function appBadge(a) {
    switch (a.status) {
      case 'crit': return badge('crit', `${T('{0} com problema', [a.unhealthy])}`);
      case 'warn': return badge('warn', `${T('{0}/{1} no ar', [a.running, a.total])}`);
      case 'stopped': return badge('off', `${T('0/{0} no ar', [a.total])}`);
      case 'paused': return badge('info', T('Pausada'));
      default: return a.total ? badge('ok', `${T('{0}/{1} no ar', [a.running, a.total])}`) : '';
    }
  }
  // de onde vem o número de instâncias (ex.: "api ×2")
  const apiTitle = (a) => (a.components || []).filter((c) => c.api).map((c) => (c.count > 1 ? `${c.name} ×${c.count}` : c.name)).join(', ');
  const diskOf = (d) => (d && d.total ? bytes(d.total) : '—');
  function diskParts(d) {
    if (!d || !d.total) return T('medindo…');
    return [[T('imagens'), d.images], ['volumes', d.volumes], ['logs', d.logs], [T('camada dos contêineres'), d.layer]]
      .filter(([, v]) => v > 0).map(([l, v]) => `${l} ${bytes(v)}`).join(' · ');
  }
  const swatch = (color) => `<i class="swatch" style="background:${esc(color)}"></i>`;

  // Chaves e tokens: escondidos por padrão, com o olhinho para ver.
  const eyeBtn = (what) => `<button class="eye" type="button" data-eye aria-pressed="false" aria-label="${T('Mostrar {0}', [what])}" title="${T('Mostrar')}">${icon('eye')}</button>`;
  const dots = (v) => '•'.repeat(Math.min(32, Math.max(12, String(v).length)));
  // campo (tipo senha) com o olhinho; attrs vão como estão (já escapados por quem chama)
  const secretInput = (attrs, what) => `<div class="secret-in"><input class="input" type="password" ${attrs}>${eyeBtn(what)}</div>`;
  // segredo mostrado uma vez: pontinhos, olhinho para revelar; o Copiar copia sem revelar
  const secretOut = (value, what) => `<span class="secret-out"><code class="pass" data-secret="${esc(value)}">${dots(value)}</code>${eyeBtn(what)}
    <button class="btn sm" type="button" data-copy="${esc(value)}">${T('Copiar')}</button></span>`;
  function toggleEye(btn) {
    const on = btn.getAttribute('aria-pressed') !== 'true';
    const what = (btn.getAttribute('aria-label') || '').replace(/^(Mostrar|Esconder) /, '');
    btn.setAttribute('aria-pressed', on);
    btn.setAttribute('aria-label', `${on ? T('Esconder') : T('Mostrar')} ${what}`);
    btn.title = on ? T('Esconder') : T('Mostrar');
    btn.innerHTML = icon(on ? 'eyeoff' : 'eye');
    const box = btn.parentElement;
    const input = $('input', box);
    if (input) input.type = on ? 'text' : 'password';
    const out = $('[data-secret]', box);
    if (out) out.textContent = on ? out.dataset.secret : dots(out.dataset.secret);
  }

  // ------------------------------------------------------------------ versão (tela aberta há tempos x painel atualizado)
  const APP_VERSION = (document.querySelector('meta[name="vpmon-version"]') || {}).content || '';
  const APP_COMMIT = (document.querySelector('meta[name="vpmon-commit"]') || {}).content || '';
  const APP_BUILT = (document.querySelector('meta[name="vpmon-built"]') || {}).content || '';
  const RELEASES = 'https://github.com/edvitor13/vpserver-monitoring/releases';
  // "v1.4.0" para versão lançada; o resto (dev, 1.4.0-dev.abc1234) como veio
  const verLabel = () => (/^\d+\.\d+\.\d+$/.test(APP_VERSION) ? 'v' + APP_VERSION : APP_VERSION || 'dev');
  function verDate() {
    const d = APP_BUILT ? new Date(APP_BUILT) : null;
    if (!d || isNaN(d)) return '';
    return `${T('{0}/{1}/{2} às {3}:{4}', [pad(d.getDate()), pad(d.getMonth() + 1), d.getFullYear(), pad(d.getHours()), pad(d.getMinutes())])}`;
  }
  // rodapé: a versão; passando o mouse (ou tocando) aparece a data e o commit
  function versionHTML() {
    const when = verDate();
    const tip = [when ? `${T('Versão de {0}', [when])}` : T('Versão sem data (compilada fora do CI)'), APP_COMMIT && `commit ${APP_COMMIT}`].filter(Boolean).join(' · ');
    const tag = /^\d+\.\d+\.\d+$/.test(APP_VERSION) ? `${RELEASES}/tag/v${APP_VERSION}` : RELEASES;
    return `<span class="ver-wrap"><button class="ver" type="button" aria-describedby="ver-tip">VPServer ${esc(verLabel())}</button>
      <span class="ver-tip" id="ver-tip" role="tooltip">${esc(tip)}</span></span>
      <a class="ver-new" href="${esc(tag)}" target="_blank" rel="noopener noreferrer">${T('Novidades')}</a>`;
  }
  let newVersion = '';
  // o servidor manda a versão dele em toda resposta; se a tela é de outra, ela
  // se atualiza: sozinha quando não há sessão (login), com aviso dentro do painel
  function checkVersion(server, status) {
    if (!server || !APP_VERSION || server === APP_VERSION || newVersion === server) return;
    newVersion = server;
    let tried = '';
    try { tried = sessionStorage.getItem('vpmon-reloaded-for') || ''; } catch { /* sem storage */ }
    if ((!S.me || status === 401) && tried !== server) { // nada a perder: recarrega (uma vez por versão)
      try { sessionStorage.setItem('vpmon-reloaded-for', server); } catch { /* sem storage */ }
      location.reload();
      return;
    }
    if (!$('.update-bar')) {
      const bar = document.createElement('div');
      bar.className = 'update-bar';
      bar.setAttribute('role', 'status');
      bar.innerHTML = `<span>${icon('refresh')}${T('Nova versão do painel disponível.')}</span><button class="btn sm primary" type="button" data-act="update">${T('Atualizar')}</button>`;
      document.body.append(bar);
    }
  }

  // fora do painel (login) a página não chama a API sozinha: confere a versão no /healthz
  function pingVersion() {
    fetch('/healthz', { cache: 'no-store' }).then((r) => checkVersion(r.headers.get('X-VPMon-Version'), 0)).catch(() => {});
  }

  // ------------------------------------------------------------------ instalar como app (PWA)
  let installEvt = null; // Android/Chrome/Edge: o navegador oferece a instalação
  window.addEventListener('beforeinstallprompt', (e) => { e.preventDefault(); installEvt = e; });
  window.addEventListener('appinstalled', () => { installEvt = null; $$('.install-only').forEach((x) => x.remove()); toast(T('Pronto: o VPServer está instalado como app.')); });
  const isStandalone = () => matchMedia('(display-mode: standalone)').matches || navigator.standalone === true;
  const isIOS = () => /iphone|ipad|ipod/i.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);
  const isMobile = () => matchMedia('(max-width: 720px), (pointer: coarse)').matches;
  // oferece quando ainda não está instalado: no celular sempre (com o jeito de cada um), no computador se o navegador deixar
  const canInstall = () => !isStandalone() && (isMobile() || !!installEvt);
  async function installApp() {
    if (installEvt) {
      installEvt.prompt();
      const r = await installEvt.userChoice.catch(() => ({}));
      installEvt = null;
      if (r.outcome !== 'accepted') toast(T('Instalação cancelada. Dá para instalar depois pelo menu Mais.'));
      return;
    }
    const steps = isIOS()
      ? `<li>${T('No <b>Safari</b>, toque em <b>Compartilhar</b> (o quadrado com a seta para cima).')}</li>
         <li>${T('Escolha <b>Adicionar à Tela de Início</b> e toque em <b>Adicionar</b>.')}</li>`
      : `<li>${T('No menu do navegador (<b>⋮</b> no Chrome), toque em <b>Instalar app</b> ou <b>Adicionar à tela inicial</b>.')}</li>
         <li>${T('Confirme em <b>Instalar</b>.')}</li>`;
    await confirmDialog({ title: T('Instalar o VPServer como app'), ok: T('Entendi'),
      body: `<ol class="howto">${steps}<li>${T('Pronto: o painel abre em tela cheia, com ícone próprio. Quando houver versão nova, ele avisa e atualiza.')}</li></ol>` });
  }
  // aviso único no celular, depois do login
  function offerInstall() {
    let seen = false;
    try { seen = localStorage.getItem('vpmon-install-offered') === '1'; } catch { /* sem storage */ }
    if (seen || !isMobile() || !canInstall() || $('.install-bar')) return;
    const bar = document.createElement('div');
    bar.className = 'install-bar install-only';
    bar.setAttribute('role', 'status');
    bar.innerHTML = `<span>${icon('install')}${T('Instale o painel como app no celular.')}</span>
      <button class="btn sm primary" type="button" data-act="install">${T('Instalar')}</button>
      <button class="icon-btn" type="button" data-act="install-later" aria-label="${T('Agora não')}">${icon('x')}</button>`;
    document.body.append(bar);
  }
  const closeInstallBar = () => {
    try { localStorage.setItem('vpmon-install-offered', '1'); } catch { /* sem storage */ }
    $$('.install-bar').forEach((x) => x.remove());
  };
  if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('/sw.js', { updateViaCache: 'none' }).catch(() => { /* sem SW: só não oferece instalar no Chrome */ });
  }

  // ------------------------------------------------------------------ API
  // vendo outro servidor (pelo painel central): só estes caminhos vão para ele
  const REMOTE_GET = new Set(['/api/overview', '/api/history/host', '/api/history/apps', '/api/history/unit', '/api/traffic',
    '/api/system', '/api/cleanup', '/api/logs/targets', '/api/logs']);
  const REMOTE_POST = new Set(['/api/apps/pause', '/api/cleanup/run', '/api/cleanup/auto']);
  function remotePath(path, method) {
    if (!S.remote) return path;
    const p = path.split('?')[0];
    if ((method === 'GET' && REMOTE_GET.has(p)) || (method === 'POST' && REMOTE_POST.has(p))) {
      return `/api/fleet/view/${encodeURIComponent(S.remote.id)}${path}`;
    }
    return path;
  }
  async function api(path, opts = {}) {
    const headers = { 'X-Requested-With': 'vpmon', 'X-VPMon-Lang': LANG }; // o servidor responde no idioma da tela
    if (opts.body) headers['Content-Type'] = 'application/json';
    const r = await fetch(remotePath(path, (opts.method || 'GET').toUpperCase()), { credentials: 'same-origin', ...opts, headers });
    checkVersion(r.headers.get('X-VPMon-Version'), r.status);
    const j = await r.json().catch(() => ({}));
    if (r.status === 401 && !path.startsWith('/api/login')) {
      showLogin();
      throw new Error('login');
    }
    if (r.status === 403 && j.error && j.error.code === 'password_change_required') {
      showSetup();
      throw new Error('login');
    }
    if (!r.ok) {
      const e = new Error((j.error && j.error.message) || `${T('Erro {0}', [r.status])}`);
      e.code = j.error && j.error.code; // estável: a tela decide por ele (ex.: ssh_locked)
      throw e;
    }
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
  function savedRemote() {
    try { return JSON.parse(sessionStorage.getItem('vpmon-remote') || 'null'); } catch { return null; }
  }
  const S = {
    ov: null,
    remote: savedRemote(), // {id, name, logs, control}: vendo um servidor conectado
    fleet: null, // /api/fleet/servers: este painel e os conectados
    tab: 'overview',
    range: store.get('range', '1h'),
    cleanup: [], // charts e timers da aba atual
    drawer: null,
    logs: Object.assign({ c: '*', tail: 300, q: '', errors: false, live: false, wrap: true }, store.get('logs', {})),
    procSort: 'cpu',
    lastOk: 0,
  };
  const RANGES = [['1h', '1 h'], ['6h', '6 h'], ['24h', '24 h'], ['7d', T('7 dias')], ['30d', T('30 dias')], ['1y', T('1 ano')]];
  const refreshFor = (r) => (r === '1h' ? 30000 : r === '6h' ? 60000 : 120000);
  // atualização periódica da aba atual; com a aba do navegador escondida, não baixa nada
  function later(fn, ms) { const id = setInterval(() => { if (!document.hidden) fn(); }, ms); S.cleanup.push(() => clearInterval(id)); }
  function teardown() { S.cleanup.splice(0).forEach((f) => { try { f(); } catch { /* ignora */ } }); }

  // ------------------------------------------------------------------ gráficos (uPlot)
  function xTicks(u, splits, ai, space, incr) {
    return splits.map((ts) => {
      const d = new Date(ts * 1000);
      if (incr >= 86400 || (d.getHours() === 0 && d.getMinutes() === 0 && incr >= 3600)) return dm(d);
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
      const series = [{ value: (_, ts) => (ts == null ? (lastT ? T('agora · ') + dt(lastT) : '—') : dt(ts)) }];
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
          el.innerHTML = `<div class="chart-empty">${T('Coletando dados…')}</div>`;
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
    return `<div class="seg" role="group" aria-label="${T('Período')}">${opts.map(([v, l]) =>
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
    S.me = null;
    S.sh = S.shInfo = null; // o chat de SSH é de quem estava logado
    pingVersion();
    clearTimeout(pollT);
    $('#app').innerHTML = `
      <div class="login"><div class="card login-card">
        <div class="brand"><div class="brand-logo">${icon('logo')}</div>
          <div class="brand-txt"><div class="brand-name">VPServer</div><div class="brand-sub">${T('Monitoramento do servidor')}</div></div></div>
        <form id="login-form" autocomplete="on">
          <div class="field"><label for="lu">${T('Usuário')}</label><input class="input" id="lu" name="username" autocomplete="username" required></div>
          <div class="field"><label for="lp">${T('Senha')}</label><input class="input" id="lp" name="password" type="password" autocomplete="current-password" required></div>
          <div class="form-err" id="login-err" role="alert">${esc(msg || '')}</div>
          <button class="btn primary" type="submit">${T('Entrar')}</button>
        </form>
        ${langPickHTML(false)}
      </div><footer class="foot foot-login">${versionHTML()}</footer></div>`;
    $('#lu').focus();
    $('#login-form').addEventListener('submit', async (e) => {
      e.preventDefault();
      const btn = $('#login-form button');
      btn.disabled = true;
      try {
        const j = await api('/api/login', { method: 'POST', body: JSON.stringify({ user: $('#lu').value.trim(), password: $('#lp').value }) });
        if (j.need2fa) { show2FAStep(j.ticket, j.user, $('#lp').value); return; }
        if (j.mustChange) showSetup($('#lp').value);
        else start();
      } catch (err) {
        $('#login-err').textContent = err.message;
        btn.disabled = false;
        $('#lp').select();
      }
    });
  }

  // segundo passo do login: o código do app autenticador (ou um de recuperação)
  function show2FAStep(ticket, user, typedPass) {
    const card = $('.login-card');
    let recovery = false;
    const render = () => {
      $('#login-form').outerHTML = `<form id="login-form" autocomplete="off">
        <div><h2 class="tf-title">${icon('shield')}${T('Verificação em duas etapas')}</h2>
          <p class="muted tf-sub">${recovery ? `${T('Digite um dos códigos de recuperação de <b>{0}</b> (cada um vale uma vez).', [esc(user)])}`
            : `${T('Abra o app autenticador e digite o código de 6 dígitos de <b>VPServer</b> para <b>{0}</b>.', [esc(user)])}`}</p></div>
        <div class="field"><label for="tf-code">${recovery ? T('Código de recuperação') : T('Código do app')}</label>
          <input class="input code-input" id="tf-code" ${recovery ? 'placeholder="xxxx-xxxx" autocapitalize="off" spellcheck="false"' : 'inputmode="numeric" autocomplete="one-time-code" placeholder="000 000"'} maxlength="12" required></div>
        <label class="sw-l"><input class="sw" type="checkbox" id="tf-rem"><span>${T('Lembrar este aparelho por 30 dias')}</span></label>
        <div class="form-err" id="login-err" role="alert"></div>
        <button class="btn primary" type="submit">${T('Entrar')}</button>
        <div class="tf-links"><button class="linkish" type="button" id="tf-alt">${recovery ? T('Usar o código do app') : T('Perdeu o celular? Usar um código de recuperação')}</button>
          <button class="linkish" type="button" id="tf-back">${T('Voltar')}</button></div></form>`;
      $('#tf-code').focus();
      $('#tf-alt').addEventListener('click', () => { recovery = !recovery; render(); });
      $('#tf-back').addEventListener('click', () => showLogin());
      $('#login-form').addEventListener('submit', async (e) => {
        e.preventDefault();
        const btn = $('#login-form button[type=submit]');
        btn.disabled = true;
        try {
          const j = await api('/api/login/2fa', { method: 'POST', body: JSON.stringify({ ticket, code: $('#tf-code').value, remember: $('#tf-rem').checked }) });
          if (j.recoveryUsed) toast(`${T('Código de recuperação usado. Sobram {0}; gere novos em Configurações → Minha conta.', [j.recoveryLeft])}`);
          if (j.mustChange) showSetup(typedPass);
          else start();
        } catch (ex) {
          if (/acabou/.test(ex.message)) { showLogin(ex.message); return; }
          $('#login-err').textContent = ex.message;
          btn.disabled = false;
          $('#tf-code').select();
        }
      });
    };
    if (card) render();
  }

  // ------------------------------------------------------------------ verificação em duas etapas (ativar, códigos, desligar)
  let qrLib;
  function loadQR() { // qrcode-generator (MIT), só quando precisa
    if (window.qrcode) return Promise.resolve(window.qrcode);
    if (!qrLib) {
      qrLib = new Promise((resolve, reject) => {
        const sc = document.createElement('script');
        sc.src = 'qrcode.js';
        sc.onload = () => resolve(window.qrcode);
        sc.onerror = () => { qrLib = null; reject(new Error(T('não consegui carregar o gerador de QR'))); };
        document.head.append(sc);
      });
    }
    return qrLib;
  }
  async function qrSVG(text) {
    const qr = await loadQR();
    const q = qr(0, 'M');
    q.addData(text);
    q.make();
    const n = q.getModuleCount(), m = 2;
    let d = '';
    for (let r = 0; r < n; r++) for (let c = 0; c < n; c++) if (q.isDark(r, c)) d += `M${c + m} ${r + m}h1v1h-1z`;
    return `<svg class="qr-svg" viewBox="0 0 ${n + 2 * m} ${n + 2 * m}" role="img" aria-label="${T('QR code para o app autenticador')}" shape-rendering="crispEdges">
      <rect width="100%" height="100%" fill="#fff"/><path d="${d}" fill="#000"/></svg>`;
  }
  // códigos de recuperação: mostrados uma vez, com copiar e baixar
  function recoveryHTML(codes) {
    return `<div class="tf-codes"><p>${T('<b>Guarde estes códigos de recuperação.</b> Cada um entra uma vez se você perder o celular; eles não aparecem de novo.')}</p>
      <ol class="codes">${codes.map((c) => `<li><code>${esc(c)}</code></li>`).join('')}</ol>
      <div class="controls"><button class="btn sm" type="button" data-copy="${esc(codes.join('\n'))}">${T('Copiar')}</button>
        <button class="btn sm" type="button" data-download="${esc(codes.join('\n'))}">${T('Baixar (.txt)')}</button></div>
      <label class="sw-l"><input class="sw" type="checkbox" id="tf-saved"><span>${T('Guardei os códigos num lugar seguro')}</span></label>
      <button class="btn primary" type="button" id="tf-done" disabled>${T('Concluir')}</button></div>`;
  }
  function bindRecovery(root, onDone) {
    $('#tf-saved', root).addEventListener('change', (e) => { $('#tf-done', root).disabled = !e.target.checked; });
    $('#tf-done', root).addEventListener('click', onDone);
  }
  // ativação: QR (ou chave), confirmar com um código, códigos de recuperação
  async function twoFAEnroll(root, onDone) {
    root.innerHTML = '<div class="loading"><div class="spinner"></div></div>';
    let st;
    try { st = await api('/api/2fa/setup', { method: 'POST', body: '{}' }); } catch (ex) {
      if (ex.message !== 'login') root.innerHTML = `<div class="form-err">${esc(ex.message)}</div>`;
      return;
    }
    const svg = await qrSVG(st.uri).catch(() => `<div class="note">${T('Não deu para desenhar o QR: use a chave abaixo.')}</div>`);
    root.innerHTML = `<div class="tf-enroll">
      <ol class="howto"><li>${T('Instale um app autenticador no celular: Google Authenticator, Microsoft Authenticator, Authy, 1Password ou Aegis.')}</li>
        <li>${T('No app, adicione uma conta e <b>escaneie o QR code</b>.')}</li><li>${T('Digite o código de 6 dígitos que o app mostrar.')}</li></ol>
      <div class="tf-qr">${svg}</div>
      <div class="tf-alt"><a class="btn sm" href="${esc(st.uri)}">${icon('phone')}${T('Abrir no app (pelo celular)')}</a>
        <details><summary>${T('Não consegue escanear? Digite a chave no app')}</summary>
          <div class="passline"><code class="tf-key">${esc(st.secret)}</code><button class="btn sm" type="button" data-copy="${esc(st.secret.replace(/ /g, ''))}">${T('Copiar')}</button></div></details></div>
      <form class="stack" id="tf-en" autocomplete="off"><div class="field"><label for="tf-c">${T('Código do app')}</label>
        <input class="input code-input" id="tf-c" inputmode="numeric" autocomplete="one-time-code" placeholder="000 000" maxlength="7" required></div>
        <div class="form-err" id="tf-err" role="alert"></div><button class="btn primary" type="submit">${T('Ativar')}</button></form></div>`;
    $('#tf-c', root).focus();
    $('#tf-en', root).addEventListener('submit', async (e) => {
      e.preventDefault();
      try {
        const j = await api('/api/2fa/enable', { method: 'POST', body: JSON.stringify({ code: $('#tf-c', root).value }) });
        toast(T('Verificação em duas etapas ligada.'));
        root.innerHTML = recoveryHTML(j.codes);
        bindRecovery(root, onDone);
      } catch (ex) { if (ex.message !== 'login') $('#tf-err', root).textContent = ex.message; }
    });
  }
  // seção "Verificação em duas etapas" de Minha conta
  async function renderTwoFA(el) {
    let me;
    try { me = await api('/api/me'); S.me = me; } catch { return; }
    const t = me.twoFA || {};
    const head = `<h3>${icon('shield')}${T('Verificação em duas etapas')}</h3>`;
    if (!t.enabled) {
      el.innerHTML = `${head}<p class="muted">${T('{0} Recomendado: além da senha, o painel pede um código do app autenticador do celular. Sem API externa nem custo.', [badge('off', T('Desligada'))])}</p>
        <button class="btn primary" type="button" id="tf-on">${icon('shield')}${T('Ativar')}</button>`;
      $('#tf-on', el).addEventListener('click', () => twoFAEnroll(el, () => renderTwoFA(el)));
      return;
    }
    el.innerHTML = `${head}<p class="muted">${T('{0} desde {1} · {2} código(s) de recuperação sobrando.', [badge('ok', T('Ligada')), dt(t.since), t.recoveryLeft])}</p>
      <div class="controls"><button class="btn sm" type="button" id="tf-new">${T('Novos códigos de recuperação')}</button>
        <button class="btn sm" type="button" id="tf-off">${T('Desligar')}</button></div><div id="tf-act"></div>`;
    const act = $('#tf-act', el);
    $('#tf-new', el).addEventListener('click', () => {
      act.innerHTML = `<form class="stack" id="tf-nf"><div class="field"><label for="tf-nc">${T('Código do app')}</label>
        <input class="input code-input" id="tf-nc" inputmode="numeric" autocomplete="one-time-code" maxlength="7" required></div>
        <div class="form-err" id="tf-err" role="alert"></div><button class="btn primary" type="submit">${T('Gerar novos códigos')}</button></form>`;
      $('#tf-nc', act).focus();
      $('#tf-nf', act).addEventListener('submit', async (e) => {
        e.preventDefault();
        try {
          const j = await api('/api/2fa/recovery', { method: 'POST', body: JSON.stringify({ code: $('#tf-nc', act).value }) });
          act.innerHTML = recoveryHTML(j.codes);
          bindRecovery(act, () => renderTwoFA(el));
        } catch (ex) { if (ex.message !== 'login') $('#tf-err', act).textContent = ex.message; }
      });
    });
    $('#tf-off', el).addEventListener('click', () => {
      act.innerHTML = `<form class="stack" id="tf-of"><p class="muted" style="margin:0">${T('Para desligar, confirme com a senha e um código do app (ou de recuperação).')}</p>
        <div class="field"><label for="tf-op">${T('Senha')}</label><input class="input" id="tf-op" type="password" autocomplete="current-password" required></div>
        <div class="field"><label for="tf-oc">${T('Código')}</label><input class="input code-input" id="tf-oc" autocomplete="one-time-code" maxlength="12" required></div>
        <div class="form-err" id="tf-err" role="alert"></div><button class="btn danger" type="submit">${T('Desligar a verificação')}</button></form>`;
      $('#tf-op', act).focus();
      $('#tf-of', act).addEventListener('submit', async (e) => {
        e.preventDefault();
        try {
          await api('/api/2fa/disable', { method: 'POST', body: JSON.stringify({ password: $('#tf-op', act).value, code: $('#tf-oc', act).value }) });
          toast(T('Verificação em duas etapas desligada.'));
          renderTwoFA(el);
        } catch (ex) { if (ex.message !== 'login') $('#tf-err', act).textContent = ex.message; }
      });
    });
  }
  // recomendação única depois do login, para quem ainda não ligou
  async function recommend2FA() {
    const ok = await confirmDialog({ title: T('Proteja o painel com a verificação em duas etapas'), ok: T('Ativar agora'),
      body: `<p>${T('Além da senha, o painel passa a pedir um código de 6 dígitos do app autenticador do seu celular (Google Authenticator, Authy, 1Password...). Se alguém descobrir a senha, ainda não entra.')}</p><p>${T('Leva um minuto. Dá para ligar ou desligar depois em <b>Configurações → Minha conta</b>.')}</p>` });
    api('/api/2fa/dismiss', { method: 'POST', body: '{}' }).catch(() => {});
    if (ok) openSettings('acesso', { enroll: true });
    else {
      toast(T('Tudo bem. Dá para ativar depois em Configurações → Minha conta.'));
      setTimeout(offerInstall, 4000); // um aviso por vez
    }
  }

  // ------------------------------------------------------------------ casca
  // [chave, nome, ícone, nome curto (celular)]
  const TABS = [['overview', T('Visão geral'), 'grid', T('Início')], ['servers', T('Servidores'), 'layers', T('Servidores')], ['infos', T('Infos'), 'info', T('Infos')],
    ['apps', T('Aplicações'), 'box', T('Apps')], ['traffic', T('Banda'), 'net', T('Banda')], ['logs', T('Logs'), 'term', T('Logs')],
    ['system', T('Sistema'), 'server', T('Sistema')], ['limits', T('Limites'), 'load', T('Limites')],
    ['cleanup', T('Limpeza'), 'broom', T('Limpeza')], ['backups', T('Backups'), 'db', T('Backups')], ['ssh', 'SSH', 'shell', 'SSH'], ['users', T('Usuários'), 'users', T('Usuários')],
    ['ai', T('IA'), 'spark', T('IA')], ['notify', T('Notificações'), 'bell', T('Avisos')]]; // IA e WhatsApp juntas, no fim
  // Menu de cima (computador): as seções do dia a dia soltas e o resto em grupos com menu
  // suspenso. O "Mais" do celular usa os mesmos grupos. [chave, nome, ícone, seções]
  const NAV = ['overview', 'infos', 'apps', 'logs',
    ['res', T('Recursos'), 'cpu', ['traffic', 'system', 'limits']],
    ['ops', T('Manutenção'), 'broom', ['cleanup', 'backups']],
    'ssh', 'ai',
    ['adm', T('Administração'), 'gear', ['servers', 'users', 'notify']]];
  const TAB_DESC = {
    traffic: T('Tráfego por app e do mês'), system: T('Processos, disco e Docker'), limits: T('Cotas do plano grátis'),
    cleanup: T('Cache, imagens e logs do Docker'), backups: T('Bancos para o R2 ou S3'), ssh: T('Comandos no servidor, em chat'),
    servers: T('Outros painéis conectados'), users: T('Quem entra e o que pode'), notify: T('Avisos pelo WhatsApp'),
  };
  // o menu com só o que esta pessoa vê: grupo vazio some, grupo de uma seção vira seção solta
  function navItems() {
    const vis = visibleTabs();
    const byKey = new Map(vis.map((t) => [t[0], t]));
    const used = new Set();
    const out = [];
    for (const n of NAV) {
      if (typeof n === 'string') {
        if (byKey.has(n)) { out.push({ tab: byKey.get(n) }); used.add(n); }
        continue;
      }
      const [g, label, ic, keys] = n;
      const items = keys.filter((k) => byKey.has(k)).map((k) => byKey.get(k));
      items.forEach(([k]) => used.add(k));
      if (items.length === 1) out.push({ tab: items[0] });
      else if (items.length) out.push({ group: g, label, icon: ic, items });
    }
    vis.filter(([k]) => !used.has(k)).forEach((t) => out.push({ tab: t })); // seção nova fora do NAV: solta, no fim
    return out;
  }
  const groupOf = (tab) => { const n = NAV.find((x) => typeof x !== 'string' && x[3].includes(tab)); return n ? n[0] : ''; };
  const BNAV = ['overview', 'apps', 'infos', 'ai']; // no celular, o resto fica em "Mais"
  // só as visíveis (em outro servidor não há IA): completa com Servidores/Banda
  const bnavTabs = () => {
    const vis = visibleTabs().map(([k]) => k);
    const out = BNAV.filter((k) => vis.includes(k));
    for (const k of ['servers', 'traffic', 'logs']) if (out.length < 4 && vis.includes(k) && !out.includes(k)) out.push(k);
    return out;
  };
  // o que o usuário logado pode (a API recusa do mesmo jeito; aqui só some da tela)
  const can = {
    admin: () => !!(S.me && S.me.admin),
    // em outro servidor, ações só com o controle total liberado por ele
    act: () => !!(S.me && (S.me.admin || S.me.actions)) && (!S.remote || S.remote.control),
    manage: () => !!(S.me && (S.me.admin || S.me.manage)),
    clean: () => !!(S.me && (S.me.admin || S.me.clean)) && (!S.remote || S.remote.control),
  };
  const roleText = (u) => (u.admin ? T('Administrador')
    : [u.actions && T('Ações nas apps'), u.manage && T('Gerencia usuários'), u.clean && T('Limpa o disco')].filter(Boolean).join(' · ') || T('Só leitura'));
  // em outro servidor: tudo dele, menos o que nunca vai à distância (usuários, IA, WhatsApp)
  const REMOTE_TABS = new Set(['overview', 'servers', 'infos', 'apps', 'traffic', 'logs', 'system', 'limits', 'cleanup']);
  const visibleTabs = () => TABS.filter(([k]) => (k !== 'notify' || can.admin()) && (k !== 'backups' || can.admin()) && (k !== 'ssh' || can.admin()) && (k !== 'users' || can.manage())
    && (!S.remote || (REMOTE_TABS.has(k) && (k !== 'logs' || S.remote.logs || S.remote.control))));
  const tabHref = (k) => `#/${k === 'overview' ? '' : k}`;
  const countHTML = (k) => (k === 'infos' ? '<span class="count infos-count" hidden></span>' : '');
  // Menu de cima ou o de baixo (celular): o de baixo vale até 720 px e também quando as
  // seções não cabem na largura (a medida é do menu de cima de verdade, com as seções que
  // esta pessoa vê).
  function fitNav() {
    const root = document.documentElement;
    const tabs = $('.top .tabs');
    if (!tabs) { root.classList.remove('nav-compact'); return; }
    const was = root.classList.contains('nav-compact');
    let compact = window.innerWidth <= 720;
    if (!compact) {
      root.classList.remove('nav-compact');
      compact = tabs.scrollWidth > tabs.clientWidth + 1;
    }
    root.classList.toggle('nav-compact', compact);
    if (compact !== was && $('.nav-menu')) closeDrawer();
  }
  let fitT = 0;
  window.addEventListener('resize', () => { clearTimeout(fitT); fitT = setTimeout(fitNav, 60); });
  if (document.fonts && document.fonts.ready) document.fonts.ready.then(fitNav);
  function renderShell() {
    setTimeout(() => { renderPill(); renderStrip(); fitNav(); }, 0);
    $('#app').innerHTML = `
      <header class="top"><div class="top-in">
        <div class="bar">
          <a class="brand" href="#/" style="text-decoration:none;color:inherit">
            <div class="brand-logo">${icon('logo')}</div>
            <div class="brand-txt"><div class="brand-name">VPServer</div><div class="brand-sub" id="srv-sub">${T('carregando…')}</div></div>
          </a>
          <span id="srv-pill-wrap"></span>
          <div class="bar-actions">
            <span id="hdr-status"></span>
            <span class="updated" id="hdr-upd"></span>
            <button class="who" type="button" data-act="settings" title="${T('{0} · Minha conta', [esc(roleText(S.me || {}))])}">${icon('user')}<span>${esc((S.me || {}).user || '')}</span></button>
            <button class="icon-btn" type="button" data-act="theme" aria-label="${T('Trocar tema')}" title="${T('Trocar tema')}">${icon(isDark() ? 'sun' : 'moon')}</button>
            <button class="icon-btn" type="button" data-act="settings" aria-label="${T('Configurações')}" title="${T('Configurações')}">${icon('gear')}</button>
            <button class="icon-btn" type="button" data-act="logout" aria-label="${T('Sair')}" title="${T('Sair')}">${icon('logout')}</button>
          </div>
        </div>
        <nav class="tabs" aria-label="${T('Seções')}">${navItems().map((n) => (n.tab
          ? `<a class="tab" href="${tabHref(n.tab[0])}" data-tab="${n.tab[0]}">${icon(n.tab[2])}${n.tab[1]}${countHTML(n.tab[0])}</a>`
          : `<button class="tab tab-group" type="button" data-act="nav-menu" data-group="${n.group}" aria-haspopup="menu" aria-expanded="false">${icon(n.icon)}${n.label}<span class="tab-chev">${icon('chev')}</span></button>`)).join('')}</nav>
      </div><div class="remote-strip" id="remote-strip" hidden></div></header>
      <main id="view"></main>
      <footer class="foot">${versionHTML()}</footer>
      <nav class="bnav" aria-label="${T('Seções')}">${bnavTabs().map((k) => { const [, , ic, short] = TABS.find((t) => t[0] === k); return `<a class="bn" href="${tabHref(k)}" data-tab="${k}">${icon(ic)}<span>${short}</span>${countHTML(k)}</a>`; }).join('')}
        <button class="bn" type="button" data-act="more" id="bn-more">${icon('more')}<span>${T('Mais')}</span></button></nav>`;
  }
  // ------------------------------------------------------------------ servidores (trocar dentro do painel central)
  const localName = () => (S.fleet && S.fleet.self && S.fleet.self.name) || (!S.remote && S.ov && S.ov.server.name) || T('este servidor');
  const others = () => ((S.fleet && S.fleet.servers) || []);
  function renderPill() {
    const wrap = $('#srv-pill-wrap');
    if (!wrap) return;
    const r = S.remote;
    const show = !!r || others().length > 0;
    $('.bar') && $('.bar').classList.toggle('has-pill', show);
    if (!show) { wrap.innerHTML = ''; return; }
    const kind = r ? (r.control ? T('conectado · controle total') : T('conectado · só ver')) : T('este servidor');
    wrap.innerHTML = `<button class="srv-pill${r ? ' remote' : ''}" type="button" data-act="servers-menu" aria-haspopup="true" title="${T('Trocar de servidor')}">
      ${icon('server')}<span class="srv-pill-t"><b>${esc(r ? r.name : localName())}</b><small>${kind}</small></span>${icon('chev')}</button>`;
  }
  function renderStrip(err) {
    const el = $('#remote-strip');
    if (!el) return;
    el.hidden = !S.remote;
    if (!S.remote) { el.innerHTML = ''; return; }
    el.classList.toggle('err', !!err);
    el.innerHTML = `<div class="remote-strip-in">${icon(err ? 'warn' : 'layers')}<span>${err ? esc(err)
      : `${T('Vendo <b>{0}</b> pelo painel central · {1}', [esc(S.remote.name), S.remote.control ? T('controle total') : T('só ver')])}`}</span>
      <button type="button" data-act="pick-server" data-v="">${T('Voltar para {0}', [esc(localName())])}</button></div>`;
  }
  async function loadFleet() {
    try { S.fleet = await api('/api/fleet/servers'); } catch (e) { if (e.message === 'login') throw e; S.fleet = { servers: [] }; }
    S.fleetAt = Date.now();
    // o servidor que estava aberto deixou de compartilhar: volta para este
    if (S.remote) {
      const t = others().find((x) => x.id === S.remote.id);
      if (!t || !t.viewable) { setRemote(null); toast(T('O servidor que estava aberto não está mais compartilhado: voltei para este.')); }
      else setRemote({ id: t.id, name: t.name, logs: t.logs, control: t.control }, true);
    }
  }
  function setRemote(r, quiet) {
    S.remote = r;
    try { r ? sessionStorage.setItem('vpmon-remote', JSON.stringify(r)) : sessionStorage.removeItem('vpmon-remote'); } catch { /* sem storage */ }
    if (quiet) return;
    S.ov = null;
    S.lastOk = Date.now();
  }
  // trocar de servidor: id vazio = este painel
  function pickServer(id) {
    closeDrawer();
    let next = null;
    if (id) {
      const t = others().find((x) => x.id === id);
      if (!t || !t.viewable) { toast(T('Esse servidor não está compartilhado ou não está conectado agora.')); return; }
      next = { id: t.id, name: t.name, logs: t.logs, control: t.control };
    }
    if ((S.remote && S.remote.id) === (next && next.id)) return;
    setRemote(next);
    renderShell();
    if (!visibleTabs().some(([k]) => k === S.tab)) location.hash = '#/';
    route();
    poll();
    toast(next ? `${T('Vendo {0}{1}.', [next.name, next.control ? T(' (controle total)') : T(' (só ver)')])}` : `${T('De volta a {0}.', [localName()])}`);
  }
  function srvState(rep, t) {
    if (t && !t.online) return ['off', t.lastSeen ? T('sem notícias') : T('nunca conectou')];
    if (t && !t.viewable) return ['off', T('não compartilha a tela')];
    if (!rep) return ['off', ''];
    if (rep.crit) return ['crit', rep.crit === 1 ? T('1 urgente') : `${T('{0} urgentes', [rep.crit])}`];
    if (rep.warn) return ['warn', rep.warn === 1 ? T('1 alerta') : `${T('{0} alertas', [rep.warn])}`];
    return ['ok', T('tudo certo')];
  }
  async function openServersMenu(anchor) {
    closeDrawer();
    await loadFleet().catch(() => {});
    renderPill();
    const btn = $('.srv-pill') || anchor;
    const rect = btn.getBoundingClientRect();
    const scrim = document.createElement('div');
    scrim.className = 'scrim-clear';
    scrim.dataset.act = 'close';
    const m = document.createElement('div');
    m.className = 'srv-menu';
    m.setAttribute('role', 'menu');
    const item = (id, name, rep, t, on) => {
      const [lv, txt] = srvState(rep, t);
      const off = t && !t.viewable;
      return `<button class="srv-item${on ? ' on' : ''}" type="button" role="menuitem" data-act="pick-server" data-v="${esc(id)}" ${off ? 'disabled' : ''}>
        <i class="dot ${lv}"></i><span class="srv-item-t"><b>${esc(name)}</b><small>${esc([t ? (t.control ? T('controle total') : t.viewable ? T('só ver') : '') : T('este servidor'), txt].filter(Boolean).join(' · '))}</small></span>
        ${on ? icon('ok') : ''}</button>`;
    };
    m.innerHTML = `<div class="srv-menu-h">${T('Servidores')}</div>
      ${item('', localName(), S.fleet && S.fleet.self, null, !S.remote)}
      ${others().map((t) => item(t.id, t.name, t.report, t, S.remote && S.remote.id === t.id)).join('')}
      <a class="srv-menu-foot" href="#/servers" data-act="close">${T('Gerenciar conexões →')}</a>`;
    document.body.append(scrim, m);
    const w = Math.min(340, window.innerWidth - 24);
    m.style.top = `${Math.round(rect.bottom + 6)}px`;
    m.style.left = `${Math.round(Math.max(12, Math.min(rect.left, window.innerWidth - w - 12)))}px`;
    S.drawer = { update() {}, reload() {}, destroy() {} };
    $('.srv-item.on', m)?.focus();
  }
  // menu suspenso de um grupo do menu de cima (mesma camada do seletor de servidores)
  function openNavMenu(btn) {
    closeDrawer();
    const n = navItems().find((x) => x.group === btn.dataset.group);
    if (!n) return;
    const rect = btn.getBoundingClientRect();
    const scrim = document.createElement('div');
    scrim.className = 'scrim-clear';
    scrim.dataset.act = 'close';
    const m = document.createElement('div');
    m.className = 'srv-menu nav-menu';
    m.setAttribute('role', 'menu');
    m.setAttribute('aria-label', n.label);
    m.dataset.group = n.group;
    m.innerHTML = n.items.map(([k, l, ic]) => `<a class="srv-item nav-item${k === S.tab ? ' on' : ''}" role="menuitem" href="${tabHref(k)}" data-act="close">
      ${icon(ic)}<span class="srv-item-t"><b>${esc(l)}</b><small>${esc(TAB_DESC[k] || '')}</small></span>${k === S.tab ? icon('ok') : ''}</a>`).join('');
    document.body.append(scrim, m);
    const w = Math.min(300, window.innerWidth - 24);
    m.style.width = `${w}px`;
    m.style.top = `${Math.round(rect.bottom + 4)}px`;
    m.style.left = `${Math.round(Math.max(12, Math.min(rect.left, window.innerWidth - w - 12)))}px`;
    btn.setAttribute('aria-expanded', 'true');
    S.drawer = { update() {}, reload() {}, destroy() { btn.setAttribute('aria-expanded', 'false'); } };
    const items = $$('.nav-item', m);
    m.addEventListener('keydown', (e) => {
      const i = items.indexOf(document.activeElement);
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        items[(i + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length].focus();
      } else if (e.key === 'Tab') closeDrawer();
    });
    (items.find((a) => a.classList.contains('on')) || items[0]).focus();
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
    const where = c.provider === 'oracle' ? `${T('Oracle {0} · {1} {2} OCPU/{3} GB', [c.regionName, String(c.shape).replace('VM.Standard.', ''), num(c.ocpus, 0), num(c.memGb, 0)])}` : s.arch;
    $('#srv-sub').textContent = [s.name || s.hostname, where, s.os].filter(Boolean).join(' · ');
    const crit = o.alerts.filter((a) => a.level === 'crit').length;
    const warn = o.alerts.filter((a) => a.level === 'warn').length;
    $('#hdr-status').innerHTML = `<a href="#/infos" class="plain">${crit ? badge('crit', crit === 1 ? T('1 urgente') : `${T('{0} urgentes', [crit])}`)
      : warn ? badge('warn', warn === 1 ? T('1 alerta') : `${T('{0} alertas', [warn])}`) : badge('ok', T('Tudo certo'))}</a>`;
    $$('.infos-count').forEach((cnt) => {
      cnt.hidden = !o.alerts.length;
      cnt.textContent = o.alerts.length;
      cnt.className = 'count infos-count ' + (crit ? 'crit' : warn ? 'warn' : 'info');
      cnt.title = `${T('{0} urgente(s), {1} alerta(s), {2} informação(ões)', [crit, warn, o.alerts.length - crit - warn])}`;
    });
    $('#hdr-upd').textContent = o.updated ? `${T('atualizado {0}', [hms(o.updated * 1000)])}` : '';
    renderPill();
    markTabs();
    fitNav();
  }
  function markTabs() {
    $$('.tab, .bn[data-tab], .sheet-item[data-tab]').forEach((t) => t.setAttribute('aria-current', t.dataset.tab === S.tab ? 'page' : 'false'));
    $$('.tab-group').forEach((b) => b.setAttribute('aria-current', b.dataset.group === groupOf(S.tab) ? 'page' : 'false'));
    const more = $('#bn-more');
    if (more) more.setAttribute('aria-current', bnavTabs().includes(S.tab) ? 'false' : 'page');
  }
  // seções do "Mais": as soltas que não estão no menu de baixo e, em seguida, os grupos
  function moreSections() {
    const bn = bnavTabs();
    const cell = ([k, l, ic]) => `<a class="sheet-item" href="${tabHref(k)}" data-tab="${k}">${icon(ic)}<span>${l}</span></a>`;
    const loose = [];
    const groups = [];
    for (const n of navItems()) {
      if (n.tab) { if (!bn.includes(n.tab[0])) loose.push(n.tab); continue; }
      const items = n.items.filter(([k]) => !bn.includes(k));
      if (items.length) groups.push(`<div class="sheet-h">${esc(n.label)}</div><div class="sheet-grid">${items.map(cell).join('')}</div>`);
    }
    return (loose.length ? `<div class="sheet-grid">${loose.map(cell).join('')}</div>` : '') + groups.join('');
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
    sh.setAttribute('aria-label', T('Mais seções'));
    sh.innerHTML = `<div class="sheet-grip"></div>
      <div class="sheet-who">${icon('user')}<span><b>${esc(S.me.user)}</b> · ${esc(roleText(S.me))}</span></div>${moreSections()}
      <div class="sheet-sep"></div><div class="sheet-grid">
      ${canInstall() ? `<button class="sheet-item install-only" type="button" data-act="install">${icon('install')}<span>${T('Instalar app')}</span></button>` : ''}
      <button class="sheet-item" type="button" data-act="settings">${icon('gear')}<span>${T('Configurações')}</span></button>
      <button class="sheet-item" type="button" data-act="theme">${icon(isDark() ? 'sun' : 'moon')}<span>${isDark() ? T('Tema claro') : T('Tema escuro')}</span></button>
      <button class="sheet-item" type="button" data-act="logout">${icon('logout')}<span>${T('Sair')}</span></button></div>`;
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
      renderStrip();
      renderHeader();
      const v = VIEWS[S.tab];
      if (v.update) v.update();
      if (S.drawer) S.drawer.update();
    } catch (e) {
      if (e.message === 'login') return;
      if (S.remote) renderStrip(`${S.remote.name}: ${e.message}`);
      else if (Date.now() - S.lastOk > 15000) toast(T('Sem conexão com o painel — tentando de novo…'));
    }
    pollT = setTimeout(poll, 5000);
  }
  document.addEventListener('visibilitychange', () => {
    if (document.hidden) return;
    if ($('#view')) poll(); // painel: a própria consulta traz a versão
    else pingVersion(); // login e primeiro acesso
  });

  // ------------------------------------------------------------------ peças comuns
  function kpi(ic, label, value, sub, meter) {
    return `<div class="card kpi"><div class="kpi-label">${icon(ic)}${esc(label)}</div>
      <div class="kpi-value num">${value}</div>
      ${meter ? `<div class="meter"><i class="${meter[1] || ''}" style="width:${Math.min(100, Math.max(0, meter[0])).toFixed(1)}%"></i></div>` : ''}
      <div class="kpi-sub">${sub}</div></div>`;
  }
  function alertsHTML(alerts, emptyText = T('Tudo certo no servidor.')) {
    if (!alerts.length) {
      return `<div class="alert ok"><div class="ic">${icon('ok')}</div><div class="alert-body"><div class="alert-t">${esc(emptyText)}</div>
        <div class="alert-d">${T('Nenhum alerta de CPU, memória, disco, contêineres ou limites do plano grátis.')}</div></div></div>`;
    }
    const area = { host: T('Servidor'), app: T('Aplicação'), limite: T('Plano grátis'), monitor: T('Monitor'), disco: T('Disco') };
    const lbl = { crit: T('Urgente'), warn: T('Alerta'), info: T('Info') };
    return alerts.map((a) => `<div class="alert ${a.level}${a.target ? ' clickable' : ''}" ${a.target ? `data-unit="${esc(a.target)}"` : ''}>
      <div class="ic">${icon(a.level)}</div><div class="alert-body">
      <div class="alert-meta">${lbl[a.level]} · ${area[a.area] || ''}</div>
      <div class="alert-t">${esc(a.title)}</div>${a.detail ? `<div class="alert-d">${esc(a.detail)}</div>` : ''}
      ${a.items && a.items.length ? `<ul class="alert-items">${a.items.map((i) => `<li>${esc(i)}</li>`).join('')}</ul>` : ''}
      ${a.action ? `<div class="alert-action"><span class="m-l">${T('O que fazer')}</span> <code>${esc(a.action)}</code></div>` : ''}</div></div>`).join('');
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
          <div class="section-h"><div><h2>${T('Histórico')}</h2><p>${T('Médias por intervalo. Toque no gráfico para ver os valores.')}</p></div>${rangeSeg(S.range)}</div>
          <div class="charts c2">
            ${chartCard('ch-cpu', 'CPU', T('% da máquina inteira, por tipo de uso'))}
            ${chartCard('ch-mem', T('Memória'), T('Em uso (sem cache) e cache do sistema'))}
            ${chartCard('ch-acpu', T('CPU por aplicação'), T('Quem usou o processador'))}
            ${chartCard('ch-amem', T('Memória por aplicação'), T('Quem ocupou a RAM'))}
            ${chartCard('ch-net', T('Rede do servidor'), T('Placa principal: o que a Oracle mede'))}
            ${chartCard('ch-disk', T('Disco'), T('Leitura e escrita'))}
          </div>
        </section></div>`;
      const s = (k) => cssVar(k);
      const C = {
        cpu: makeChart($('#ch-cpu'), { stacked: true, fmt: pct, softMax: 10, series: [
          { label: T('Usuário'), color: s('--s1') }, { label: T('Sistema'), color: s('--s2') },
          { label: T('Espera de disco'), color: s('--s3') }, { label: T('Steal (Oracle)'), color: s('--s4') }] }),
        mem: makeChart($('#ch-mem'), { fmt: bytes, series: [{ label: T('Em uso'), color: s('--s1') }, { label: T('Cache'), color: s('--s3') }] }),
        net: makeChart($('#ch-net'), { fmt: rate, series: [{ label: T('Saída (envio)'), color: s('--s2') }, { label: T('Entrada'), color: s('--s1') }] }),
        disk: makeChart($('#ch-disk'), { fmt: rate, series: [{ label: T('Leitura'), color: s('--s1') }, { label: T('Escrita'), color: s('--s2') }] }),
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
      $('#ov-alerts').innerHTML = hot.length ? alertsHTML(hot) + (nInfo ? `<a class="more-infos" href="#/infos">${nInfo === 1 ? T('+ 1 informação na aba Infos →') : T('+ {0} informações na aba Infos →', [nInfo])}</a>` : '')
        : `<a class="alert ok slim plain" href="#/infos"><div class="ic">${icon('ok')}</div><div class="alert-body"><div class="alert-t">${T('Nada urgente no servidor.')}</div>
          <div class="alert-d">${nInfo ? (nInfo === 1 ? T('1 informação na aba Infos (cache, logs, limites…) →') : T('{0} informações na aba Infos (cache, logs, limites…) →', [nInfo])) : T('Nenhum alerta nem informação pendente.')}</div></div></a>`;
      const memP = (h.memUsed / h.memTotal) * 100;
      const fsP = (h.fsUsed / h.fsTotal) * 100;
      const egP = (t.month.tx / t.limitBytes) * 100;
      $('#ov-kpis').innerHTML = [
        kpi('cpu', 'CPU', pct(h.cpu), `${T('usuário {0} · sistema {1} · steal {2}', [pct(h.cpuUser), pct(h.cpuSystem), pct(h.cpuSteal)])}`, [h.cpu, level(h.cpu, 75, 90)]),
        kpi('mem', T('Memória em uso'), `${bytes(h.memUsed)} <small>${T('de {0}', [bytes(h.memTotal)])}</small>`, `${T('+ {0} de cache (liberável) · livre {1}', [bytes(memCache(h)), bytes(h.memFree)])}`, [memP, level(memP, 88, 95)]),
        kpi('disk', T('Disco'), `${bytes(h.fsUsed)} <small>${T('de {0}', [bytes(h.fsTotal)])}</small>`, o.storage.measured ? `${T('{0} disso é cache do Docker (liberável) · livre {1}', [bytes(o.storage.buildCache + o.storage.unusedImages), bytes(h.fsAvail)])}` : `${T('livre {0} · E/S {1}', [bytes(h.fsAvail), rate(h.diskRead + h.diskWrite)])}`, [fsP, level(fsP, 80, 90)]),
        kpi('net', T('Rede agora'), `↑ ${rate(h.netTx)}`, `${T('↓ {0} · placa {1}', [rate(h.netRx), esc(h.iface || '—')])}`),
        kpi('up', T('Saída no mês'), data(t.month.tx), `${T('de 10 TB grátis · projeção {0}', [t.projectedTx ? data(t.projectedTx) : '—'])}`, [egP, level(egP, 75, 90)]),
        kpi('load', T('Carga'), `${num(h.load1, 2)} <small>${T('/ {0} núcleos', [h.cores])}</small>`, `${T('5 min {0} · 15 min {1} · ligado há {2}', [num(h.load5, 2), num(h.load15, 2), dur(h.uptime)])}`),
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
    return `<div class="card-h"><div><h2>${T('Quem está consumindo agora')}</h2>
        <div class="muted" style="font-size:.82rem;margin-top:2px">${T('Cada barra é a máquina inteira. <b class="ink2">Listrado = cache</b>, que o sistema libera quando precisa; o fim vazio é o que está livre.')}</div></div></div>
      <div class="share">
        <div class="share-row"><span class="share-label">CPU</span><div class="share-bar">${apps.map((a) => seg(a.cpu, 100, colorOf(a.color), a.name)).join('')}</div>
          <span class="share-total num">${T('{0} de {1} núcl.', [pct(h.cpu), cores])}</span></div>
        <div class="share-row"><span class="share-label">${T('Memória')}</span><div class="share-bar">${apps.map((a) => seg(a.mem, h.memTotal, colorOf(a.color), a.name)).join('')}${seg(memCache(h), h.memTotal, '', T('Cache de memória (liberável)'), 'cache')}</div>
          <span class="share-total num">${T('{0} + {1} cache', [bytes(h.memUsed), bytes(memCache(h))])}</span></div>
        ${st.measured ? `<div class="share-row"><span class="share-label">${T('Disco')}</span><div class="share-bar">${diskApps.map((a) => seg(a.disk.total, h.fsTotal, colorOf(a.color), a.name)).join('')}${seg(st.other, h.fsTotal, cssVar('--s-sys'), T('Sistema e outros'))}${seg(cacheDisk, h.fsTotal, '', T('Cache do Docker (liberável)'), 'cache')}</div>
          <span class="share-total num">${T('{0} · {1} cache', [bytes(h.fsUsed), bytes(cacheDisk)])}</span></div>` : ''}
      </div>
      <ul class="legend">${apps.map((a) => `<li>${swatch(colorOf(a.color))}<span class="n">${esc(a.name)}</span>
        <span class="v">${pct(a.cpu)} · ${bytes(a.mem)}${a.disk && a.disk.total ? ' · ' + bytes(a.disk.total) : ''}</span></li>`).join('')}
        ${st.measured ? `<li>${swatch(cssVar('--s-sys'))}<span class="n">${T('Sistema e outros (disco)')}</span><span class="v">${bytes(st.other)}</span></li>` : ''}
        <li><i class="swatch cache"></i><span class="n">${T('Cache de memória (liberável)')}</span><span class="v">${bytes(memCache(h))}</span></li>
        ${st.measured ? `<li><i class="swatch cache"></i><span class="n">${T('Cache do Docker (liberável)')}</span><span class="v">${bytes(cacheDisk)}</span></li>` : ''}</ul>
      <div class="table-wrap" style="margin-top:14px"><table>
        <thead><tr><th>${T('Aplicação')}</th><th>${T('Containers')}</th><th class="r hide-sm" title="${T('Quantas instâncias da API de cada app estão rodando')}">${T('Instâncias')}</th><th class="r">CPU</th><th class="r">${T('Memória')}</th><th class="r">${T('Disco')}</th><th class="r hide-sm">${T('Rede ↑ agora')}</th><th class="r hide-sm">${T('Saída hoje')}</th><th class="r">${T('Saída no mês')}</th></tr></thead>
        <tbody>${real.map((a) => { const sys = a.kind === 'system'; return `<tr class="clickable" data-unit="app:${esc(a.key)}">
          <td><div class="cell-name">${swatch(colorOf(a.color))}<span>${esc(a.name)}</span></div></td>
          <td>${sys ? `<span class="muted">${T('{0} serviços', [a.units.length])}</span>` : appBadge(a)}</td>
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
      v.innerHTML = `<div class="page"><div class="section-h"><div><h2>${T('Aplicações')}</h2>
        <p>${T('Descobertas sozinhas: cada projeto do Docker Compose é uma aplicação; serviços do Linux ficam em "Sistema".')}</p></div></div>
        <div class="grid g2" id="apps-grid"></div></div>`;
    },
    update() {
      const all = appsOf();
      $('#apps-grid').innerHTML = all.map((a) => {
        if (a.kind === 'kernel') {
          return `<div class="card app-card"><div class="card-h"><h2>${swatch(colorOf(a.color))}${esc(a.name)}</h2></div>
            <div class="app-metrics"><div><div class="m-l">CPU</div><div class="m-v">${pct(a.cpu)}</div></div>
            <div><div class="m-l">${T('Memória')}</div><div class="m-v">${bytes(a.mem)}</div></div></div>
            <div class="note">${T('O que o servidor gasta e não pertence a nenhum contêiner ou serviço: memória do kernel, buffers de rede, tabelas de página — normal ficar em algumas centenas de MB. Picos de CPU aqui costumam ser processos de vida curta (um <code>docker run</code>, um build) que começam e terminam entre duas leituras.')}</div></div>`;
        }
        const sys = a.kind === 'system';
        const units = sys ? a.units.filter((u) => u.cpu > 0.05 || u.mem > 8 * 1048576) : a.units;
        const hidden = a.units.length - units.length;
        return `<div class="card app-card">
          <div class="card-h"><h2 class="clickable" data-unit="app:${esc(a.key)}" style="cursor:pointer">${swatch(colorOf(a.color))}<span>${esc(a.name)}</span></h2>
            ${sys ? '' : `<div class="card-h-r">${appBadge(a)}${pauseBtns(a)}</div>`}</div>
          <div class="app-metrics">
            <div><div class="m-l">CPU</div><div class="m-v">${pct(a.cpu)}</div></div>
            <div><div class="m-l">${T('Memória')}</div><div class="m-v">${bytes(a.mem)}</div></div>
            ${sys ? `<div><div class="m-l">${T('Disco E/S')}</div><div class="m-v">${rate(a.ioRead + a.ioWrite)}</div></div><div><div class="m-l">${T('Serviços')}</div><div class="m-v">${a.units.length}</div></div>`
              : `<div><div class="m-l">${T('Disco')}</div><div class="m-v">${diskOf(a.disk)}</div></div>
            <div><div class="m-l">${T('Saída no mês')}</div><div class="m-v">${data(a.month.tx)}</div></div>`}
          </div>
          ${sys ? '' : `<div class="app-facts"><div><span class="m-l">${T('Instâncias')}</span> <b>${a.api || '—'}</b> <span class="muted">${a.api ? `(${esc(apiTitle(a))})` : ''}</span></div>
            <div><span class="m-l">${T('Disco')}</span> ${diskParts(a.disk)}</div></div>`}
          <div class="rows">${units.map(unitRow).join('')}</div>
          ${hidden > 0 ? `<div class="muted" style="font-size:.78rem;padding-top:8px">${T('+ {0} serviços parados ou quase sem consumo', [hidden])}</div>` : ''}
        </div>`;
      }).join('');
    },
  };
  // Pausar/Retomar: só apps do Docker, nunca o próprio painel
  function pauseBtns(a, cls = 'sm') {
    if (!can.act() || a.self || (a.kind !== 'compose' && a.kind !== 'standalone')) return '';
    const b = (p, ic, l) => `<button class="btn ${cls}" type="button" data-act="pause" data-v="${esc(a.key)}" data-pause="${p}">${icon(ic)}${l}</button>`;
    return (a.paused ? b(0, 'play', T('Retomar')) : '') + (a.running ? b(1, 'pause', T('Pausar')) : '');
  }
  // janela "tem certeza?" (fica por cima de gaveta e modal); resolve true/false
  function confirmDialog({ title, body, ok, danger }) {
    return new Promise((resolve) => {
      const scrim = document.createElement('div');
      scrim.className = 'cf-scrim';
      const m = document.createElement('div');
      m.className = 'cf-modal';
      m.innerHTML = `<div class="card cf-card" role="alertdialog" aria-modal="true" aria-labelledby="cf-t" aria-describedby="cf-b">
        <h2 id="cf-t">${esc(title)}</h2><div id="cf-b" class="cf-b">${S.remote ? `<p class="cf-where">${icon('server')}<span>${T('No servidor <b>{0}</b>, pelo painel central.', [esc(S.remote.name)])}</span></p>` : ''}${body}</div>
        <div class="controls cf-actions"><button class="btn" type="button" data-cf="0">${T('Cancelar')}</button>
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
      title: `${T('Pausar {0}?', [a.name])}`, ok: T('Pausar'), danger: true,
      body: `<p>${list.length === 1 ? T('Este contêiner vai ficar congelado') : `${T('Estes {0} contêineres vão ficar congelados', [list.length])}`}: ${names}.</p>
        <ul class="cf-list"><li>${T('O site ou a API dessa aplicação <b>para de responder</b> até você retomar.')}</li>
        <li>${T('Para de usar CPU; a memória continua ocupada e nada é perdido.')}</li>
        <li>${T('Um deploy dessa app ou um reinício do servidor desfaz a pausa.')}</li></ul>`,
    } : {
      title: `${T('Retomar {0}?', [a.name])}`, ok: T('Retomar'),
      body: `<p>${list.length === 1 ? T('O contêiner {0} volta a rodar exatamente de onde parou.', [names]) : T('Os contêineres {0} voltam a rodar exatamente de onde pararam.', [names])}</p>`,
    });
    if (!ok) return;
    try {
      const j = await api('/api/apps/pause', { method: 'POST', body: JSON.stringify({ app: key, pause }) });
      toast(j.warning ? `${T('Feito em parte: {0}', [j.warning])}` : `${a.name} ${pause ? 'pausada' : 'retomada'}.`);
      poll();
    } catch (ex) { if (ex.message !== 'login') toast(ex.message); }
  }
  function unitRow(u) {
    const c = u.container;
    const sub = c ? [c.status || c.state, c.image].filter(Boolean).join(' · ') : (u.desc || '');
    const lim = u.memLimit ? ` <span class="muted">/ ${bytes(u.memLimit)}</span>` : '';
    return `<div class="row" data-unit="${esc(u.key)}" role="button" tabindex="0">
      <div class="row-main"><div class="row-name"><span>${esc(u.name)}</span>${c ? containerBadge(c) : ''}</div>${sub ? `<div class="row-sub">${esc(sub)}</div>` : ''}</div>
      <div class="row-stats"><span>CPU <b>${pct(u.cpu)}</b></span><span>RAM <b>${bytes(u.mem)}</b>${lim}</span>${u.disk ? `<span>${T('Disco <b>{0}</b>', [bytes(u.disk.total)])}</span>` : ''}${u.hasNet ? `<span>↑ <b>${rate(u.netTx)}</b></span>` : ''}</div>
    </div>`;
  }

  // ------------------------------------------------------------------ gaveta de detalhe (contêiner, serviço ou app)
  function closeDrawer() {
    stopWA();
    // o seletor de servidores sai sempre (a camada transparente dele cobre a tela toda)
    $$('.scrim-clear, .srv-menu').forEach((e) => e.remove());
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
    d.innerHTML = `<div class="drawer-h"><h2 id="dr-title"></h2><button class="icon-btn" data-act="close" aria-label="${T('Fechar')}">${icon('x')}</button></div>
      <div class="drawer-b"><div id="dr-info"></div>
      <div class="section-h" style="margin:0"><h3>${T('Histórico')}</h3>${rangeSeg(S.dRange || '1h', 'drange')}</div>
      ${chartCard('dr-cpu', 'CPU', T('% da máquina inteira'))}
      ${chartCard('dr-mem', T('Memória'), T('Em uso, sem cache inativo'))}
      ${chartCard('dr-net', T('Rede'), T('Bytes por segundo'))}
      ${chartCard('dr-io', T('Disco'), T('Leitura e escrita'))}
      <div id="dr-logs"></div></div>`;
    document.body.append(scrim, d);
    document.body.style.overflow = 'hidden';
    const local = [];
    const mk = (id, cfg) => { const before = S.cleanup.length; const c = makeChart($('#' + id, d), cfg); S.cleanup.splice(before); local.push(c.destroy); return c; };
    const s = (k) => cssVar(k);
    const C = {
      cpu: mk('dr-cpu', { fmt: pct, fill: true, series: [{ label: 'CPU', color: s('--s1') }] }),
      mem: mk('dr-mem', { fmt: bytes, fill: true, series: [{ label: T('Memória'), color: s('--s3') }] }),
      net: mk('dr-net', { fmt: rate, series: [{ label: T('Saída'), color: s('--s2') }, { label: T('Entrada'), color: s('--s1') }] }),
      io: mk('dr-io', { fmt: rate, series: [{ label: T('Leitura'), color: s('--s1') }, { label: T('Escrita'), color: s('--s2') }] }),
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
          ${kv('CPU', pct(a.cpu))}${kv(T('Memória'), bytes(a.mem))}${kv(T('Contêineres'), a.total ? `${T('{0} de {1} rodando{2}', [a.running, a.total, a.paused ? ` ${T('· {0} pausado(s)', [a.paused])}` : ''])}` : '—')}
          ${kv(T('Rede ↑ agora'), rate(a.netTx))}${kv(T('Saída hoje'), data(a.today.tx))}${kv(T('Saída no mês'), data(a.month.tx))}
          ${kv(T('Entrada no mês'), data(a.month.rx))}${kv(T('Disco E/S'), rate(a.ioRead + a.ioWrite))}${kv(T('Erros no log (1 h)'), num(a.logErrors1h, 0))}
          ${a.kind !== 'system' ? kv(T('Disco ocupado'), diskOf(a.disk)) + kv(T('Instâncias da API'), `${a.api || '—'} <small class="muted">${a.api ? esc(apiTitle(a)) : ''}</small>`) : ''}
          </div>${a.kind !== 'system' && a.disk && a.disk.total ? `<div class="muted" style="font-size:.8rem;margin-top:10px">${T('Disco: {0}. Imagem usada por mais de uma app é dividida entre elas.', [diskParts(a.disk)])}</div>` : ''}
          ${pauseBtns(a, '') ? `<div class="controls" style="margin-top:12px">${pauseBtns(a, '')}</div>` : ''}</div><div class="card"><h3 style="margin-bottom:6px">${T('Componentes')}</h3><div class="rows">${a.units.map(unitRow).join('')}</div></div>`;
        return;
      }
      const f = findUnit(key);
      if (!f) { $('#dr-info', d).innerHTML = `<div class="note">${T('Esta unidade não está mais rodando.')}</div>`; return; }
      const { u, a } = f, c = u.container;
      $('#dr-title', d).textContent = u.name;
      const limCPU = u.cpuLimit ? ` ${T('de {0}', [num(u.cpuLimit, 2)])}` : '';
      $('#dr-info', d).innerHTML = `<div class="card">
        <div class="chips" style="margin-bottom:12px">${c ? containerBadge(c) : ''}<span class="chip">${swatch(colorOf(a.color))}<span>${esc(a.name)}</span></span>
          ${c ? `<span class="chip"><span>${esc(c.image)}</span></span>${(c.ports || []).map((p) => `<span class="chip"><span>${esc(p)}</span></span>`).join('')}` : ''}</div>
        ${u.desc ? `<p class="ink2" style="margin:0 0 12px;font-size:.86rem">${esc(u.desc)}</p>` : ''}
        ${c ? `<p class="muted" style="margin:0 0 12px;font-size:.8rem">${T('{0} · criado {1}{2}', [esc(c.status), ago(c.created), c.service ? ` ${T('· serviço "{0}"', [esc(c.service)])}` : ''])}</p>` : ''}
        <div class="kv k3">
          ${kv('CPU', `${pct(u.cpu)} <small class="muted">${T('({0}{1} núcl.)', [num(u.cpuCores, 2), limCPU])}</small>`)}
          ${kv(T('Memória'), bytes(u.mem) + (u.memLimit ? ` <small class="muted">${T('de {0}', [bytes(u.memLimit)])}</small>` : ` <small class="muted">${T('sem limite')}</small>`))}
          ${kv(T('Processos'), num(u.pids, 0))}
          ${u.hasNet ? kv(T('Rede agora'), `↑ ${rate(u.netTx)} ↓ ${rate(u.netRx)}`) : ''}
          ${u.hasNet ? kv(T('Hoje'), `↑ ${data(u.today.tx)} ↓ ${data(u.today.rx)}`) : ''}
          ${u.hasNet ? kv(T('No mês'), `↑ ${data(u.month.tx)} ↓ ${data(u.month.rx)}`) : ''}
          ${kv(T('Disco E/S'), `${T('{0} lendo · {1} gravando', [rate(u.ioRead), rate(u.ioWrite)])}`)}
          ${u.cpuLimit ? kv(T('Segurado pelo limite'), pct(u.throttled)) : ''}
          ${c ? kv(T('Log (1 h)'), `${T('{0} linhas · {1} erros', [num(u.logLines1h, 0), num(u.logErrors1h, 0)])}`) : ''}
          ${u.disk ? kv(T('Disco ocupado'), `${bytes(u.disk.total)} <small class="muted">(${[['volumes', u.disk.volumes], ['logs', u.disk.logs], [T('camada'), u.disk.layer]].filter(([, v]) => v > 0).map(([l, v]) => `${l} ${bytes(v)}`).join(' · ') || T('quase nada')})</small>`) : ''}
          ${u.imageSize ? kv(T('Imagem'), `${bytes(u.imageSize)}${u.imageUses > 1 ? ` <small class="muted">${T('(compartilhada por {0} contêineres)', [u.imageUses])}</small>` : ''}`) : ''}
        </div></div>`;
      if (c && !logsLoaded) {
        logsLoaded = true;
        api('/api/logs/targets').then((ts) => {
          const t = ts.find((x) => x.name === u.name);
          const errs = t ? t.lastErrors : [];
          $('#dr-logs', d).innerHTML = `<div class="card"><div class="card-h"><h3>${T('Últimos erros no log')}</h3>
            <a class="btn" href="#/logs?c=${encodeURIComponent(u.name)}" data-act="close">${icon('logs')}${T('Abrir logs')}</a></div>
            ${errs.length ? `<div class="logbox wrap" style="height:auto;max-height:320px">${errs.map((l) => logLine(l, false)).join('')}</div>`
              : `<div class="muted" style="font-size:.85rem">${T('Nenhuma linha de erro nas últimas 24 h.')}</div>`}</div>`;
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
      <div class="field"><label for="acc-u">${T('Usuário')}</label><input class="input" id="acc-u" name="username" autocomplete="username" value="${esc(user)}" required minlength="3" maxlength="32"></div>
      <div class="field"><label for="acc-c">${T('Senha atual')}</label><input class="input" id="acc-c" type="password" autocomplete="current-password" required></div>
      <div class="field"><label for="acc-n">${T('Nova senha')}</label><input class="input" id="acc-n" type="password" autocomplete="new-password" minlength="10" required></div>
      <div class="field"><label for="acc-r">${T('Repita a nova senha')}</label><input class="input" id="acc-r" type="password" autocomplete="new-password" minlength="10" required></div>
      <div class="muted" style="font-size:.78rem">${T('Pelo menos 10 caracteres (uma frase com espaços vale). Usuário: 3 a 32 letras, números, ponto, hífen ou _.')}</div>
      <div class="form-err" id="acc-err" role="alert"></div>
      <div class="controls"><button class="btn primary" type="submit">${setup ? T('Salvar e continuar') : T('Salvar')}</button>
        ${setup ? '' : `<button class="btn" type="button" id="acc-cancel">${T('Cancelar')}</button>`}</div>
    </form>`;
  }
  function bindAccess(prefill, onDone) {
    if (prefill) $('#acc-c').value = prefill;
    ($('#acc-c').value ? $('#acc-n') : $('#acc-c')).focus();
    $('#acc-form').addEventListener('submit', async (e) => {
      e.preventDefault();
      const err = $('#acc-err'), btn = $('#acc-form button[type=submit]');
      const nw = $('#acc-n').value;
      if (nw !== $('#acc-r').value) { err.textContent = T('As duas novas senhas não são iguais.'); $('#acc-r').select(); return; }
      if (nw.length < 10) { err.textContent = T('A nova senha precisa ter pelo menos 10 caracteres.'); return; }
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
    if (!v.enabled) return badge('off', T('IA desligada'));
    return `${badge('ok', T('IA ligada'))} <span class="muted" style="font-size:.82rem">${T('{0} · chave {1} · {2}', [esc(v.model), esc(v.key), v.source === 'panel' ? T('configurada aqui') : T('vinda do .env do servidor')])}</span>`;
  }
  function aiFormHTML(v, setup) {
    const models = [['deepseek-chat', T('deepseek-chat — rápido (recomendado)')], ['deepseek-reasoner', T('deepseek-reasoner — pensa mais, mais lento')]];
    if (v.model && !models.some(([m]) => m === v.model)) models.push([v.model, v.model]);
    return `<form id="ai-form" class="stack" autocomplete="off">
      <div id="ai-st">${aiStatus(v)}</div>
      <p class="muted" style="margin:0;font-size:.84rem">${T('A aba <b>IA</b> responde perguntas sobre o servidor usando a DeepSeek. Crie uma chave em')}
        <a href="https://platform.deepseek.com/api_keys" target="_blank" rel="noopener noreferrer">platform.deepseek.com</a> ${T('→ API keys. Ela fica só no servidor (nunca volta para o navegador) e é testada antes de salvar.')}</p>
      <div class="field"><label for="ai-k">${T('Chave da DeepSeek')}</label>${secretInput(`id="ai-k" placeholder="${v.source === 'panel' ? T('cole uma nova para trocar (vazio = manter)') : 'sk-...'}" spellcheck="false" autocomplete="off"`, T('a chave'))}</div>
      <div class="field"><label for="ai-m">${T('Modelo')}</label><select class="input" id="ai-m">${models.map(([m, l]) => `<option value="${esc(m)}"${m === (v.model || 'deepseek-chat') ? ' selected' : ''}>${esc(l)}</option>`).join('')}</select></div>
      <div class="form-err" id="ai-err" role="alert"></div><div class="form-ok" id="ai-ok" role="status"></div>
      <div class="controls">
        <button class="btn primary" type="submit">${T('Testar e salvar')}</button>
        ${v.enabled && !setup ? `<button class="btn" type="button" id="ai-test">${T('Testar conexão')}</button>` : ''}
        ${v.source === 'panel' && !setup ? `<button class="btn" type="button" id="ai-rm">${T('Remover chave')}</button>` : ''}
        ${setup ? `<button class="btn" type="button" id="ai-skip">${T('Pular por enquanto')}</button>` : ''}
      </div>
    </form>`;
  }
  function bindAI(onSaved, onSkip) {
    const err = $('#ai-err'), ok = $('#ai-ok');
    const run = async (body, msg) => {
      err.textContent = ''; ok.textContent = T('Falando com a DeepSeek…');
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
      run({ action: 'save', key: $('#ai-k').value.trim(), model: $('#ai-m').value }, T('Chave testada e salva. A aba IA já pode ser usada.'));
    });
    const t = $('#ai-test');
    if (t) t.addEventListener('click', () => run({ action: 'test' }, T('Conexão com a DeepSeek funcionando.')));
    const rm = $('#ai-rm');
    if (rm) rm.addEventListener('click', () => {
      if (rm.dataset.sure !== '1') { rm.dataset.sure = '1'; rm.textContent = T('Confirmar remoção'); return; }
      run({ action: 'remove' }, T('Chave removida da tela.'));
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
    const steps = me.admin ? [T('Acesso'), T('Duas etapas'), T('IA'), T('WhatsApp')] : [T('Crie sua senha'), T('Duas etapas')];
    const shell = (step, body) => `<div class="login"><div class="card login-card settings-card">
      <div class="brand"><div class="brand-logo">${icon('logo')}</div>
        <div class="brand-txt"><div class="brand-name">VPServer</div><div class="brand-sub">${T('Primeiro acesso')}</div></div></div>
      <div class="steps">${steps.map((l, i) => `<span class="${step === i + 1 ? 'on' : step > i + 1 ? 'done' : ''}">${i + 1} · ${l}</span>`).join('')}</div>
      ${body}</div></div>`;
    const intro = me.passwordSource === 'env'
      ? T('O painel está com a senha inicial. Escolha o usuário e uma senha nova para continuar — até lá, nenhum dado do servidor aparece.')
      : T('Sua senha é provisória (quem cadastrou você a recebeu). Crie a sua para continuar — até lá, nenhum dado do servidor aparece.');
    $('#app').innerHTML = shell(1, `<h2 style="margin:14px 0 4px">${T('Crie o seu acesso')}</h2>
      <p class="muted" style="margin:0 0 14px;font-size:.86rem">${esc(intro)}</p>
      ${accessFormHTML(me.user || 'admin', true)}`);
    bindAccess(prefill, () => tfStep());
    // passo 2: verificação em duas etapas (recomendado)
    function tfStep() {
      if ((me.twoFA || {}).enabled) { me.admin ? aiStep() : start(); return; } // já ligado (só trocou a senha)
      $('#app').innerHTML = shell(2, `<h2 style="margin:14px 0 4px">${icon('shield')} ${T('Verificação em duas etapas')} <span class="badge ok">${T('Recomendado')}</span></h2>
        <p class="muted" style="margin:0 0 12px;font-size:.86rem">${T('Além da senha, o painel pede um código do app autenticador do celular. Se alguém descobrir a senha, ainda não entra.')}</p>
        <div id="tf-box"><button class="btn primary" type="button" id="tf-go">${icon('shield')}${T('Ativar agora')}</button></div>
        <div class="controls" style="margin-top:12px"><button class="btn" type="button" id="tf-skip">${T('Pular por enquanto')}</button></div>
        <p class="muted" style="margin:10px 0 0;font-size:.8rem">${T('Dá para ligar ou desligar depois em <b>Configurações → Minha conta</b>.')}</p>`);
      const next = () => (me.admin ? aiStep() : start()); // IA e WhatsApp são do administrador
      $('#tf-go').addEventListener('click', () => { $('#tf-skip').closest('.controls').hidden = true; twoFAEnroll($('#tf-box'), next); });
      $('#tf-skip').addEventListener('click', () => { api('/api/2fa/dismiss', { method: 'POST', body: '{}' }).catch(() => {}); next(); });
    }
    async function aiStep() {
      let st = { ai: { enabled: false } };
      try { st = await api('/api/settings'); } catch { /* segue */ }
      $('#app').innerHTML = shell(3, `<h2 style="margin:14px 0 4px">${T('Quer usar a IA?')}</h2>${aiFormHTML(st.ai, true)}
        <p class="muted" style="margin:10px 0 0;font-size:.8rem">${T('Dá para configurar depois em <b>Configurações</b> (ícone de engrenagem no topo).')}</p>`);
      bindAI(() => setTimeout(waStep, 900), waStep);
      $('#ai-k').focus();
    }
    // passo 3: conectar o WhatsApp das notificações (se estiver instalado)
    async function waStep() {
      let j;
      try { j = await api('/api/notify'); } catch { start(); return; }
      if (!j.notify.installed) { start(); return; }
      const render = (n) => {
        S.nt = n;
        const st = n.status;
        const own = st.state === 'open' && st.number && !n.config.recipients.length;
        $('#app').innerHTML = shell(4, `<h2 style="margin:14px 0 4px">${T('Avisos pelo WhatsApp?')}</h2>
          <p class="muted" style="margin:0 0 12px;font-size:.86rem">${T('O painel pode mandar alertas (app caiu, disco enchendo, limites do plano grátis) e um resumo diário pelo WhatsApp.')}</p>
          <div id="wa-box">${waBoxHTML(n)}</div>
          ${own ? `<form class="stack" id="su-to" style="margin-top:14px"><div class="field"><label for="su-num">${T('Mandar os avisos para (com DDI)')}</label>
            <input class="input" id="su-num" inputmode="tel" value="${esc(fmtPhone(st.number))}"></div>
            <div class="form-err" id="su-err" role="alert"></div><button class="btn primary" type="submit">${T('Salvar e concluir')}</button></form>` : ''}
          <div class="controls" style="margin-top:14px"><button class="btn${own ? '' : ' primary'}" type="button" id="su-done">${st.state === 'open' ? T('Concluir') : T('Pular por enquanto')}</button></div>
          <p class="muted" style="margin:10px 0 0;font-size:.8rem">${T('O que avisar e para quem fica na aba <b>Notificações</b>.')}</p>`);
        bindWA($('#wa-box'), (x) => render(x.notify));
        $('#su-done').addEventListener('click', () => { stopWA(); start(); });
        const f = $('#su-to');
        if (f) f.addEventListener('submit', async (e) => {
          e.preventDefault();
          try {
            await api('/api/notify/config', { method: 'POST', body: JSON.stringify({ ...n.config, recipients: [{ id: $('#su-num').value, name: T('Eu') }], panelUrl: location.origin }) });
            stopWA();
            start();
          } catch (ex) { if (ex.message !== 'login') $('#su-err').textContent = ex.message; }
        });
      };
      render(j.notify);
    }
  }

  // Configurações: Minha conta (todos), Usuários (quem gerencia), IA e WhatsApp (administradores).
  async function openSettings(tab = 'acesso', opts = {}) {
    closeDrawer();
    let me = {}, st = { ai: { enabled: false } };
    try {
      me = await api('/api/me');
      S.me = me;
      if (me.admin) st = await api('/api/settings');
    } catch { return; }
    if (tab === 'usuarios') { location.hash = '#/users'; return; } // virou a aba Usuários
    const tabs = [['acesso', T('Minha conta'), true], ['ia', T('IA'), can.admin()], ['whatsapp', T('WhatsApp'), can.admin()]]
      .filter(([, , ok]) => ok);
    if (!tabs.some(([k]) => k === tab)) tab = 'acesso';
    const scrim = document.createElement('div');
    scrim.className = 'scrim';
    scrim.dataset.act = 'close';
    const m = document.createElement('div');
    m.className = 'modal';
    m.innerHTML = `<div class="card login-card settings-card" role="dialog" aria-modal="true" aria-labelledby="st-t">
      <button class="icon-btn st-close" type="button" data-act="close" aria-label="${T('Fechar')}">${icon('x')}</button>
      <div class="card-h"><div><h2 id="st-t">${icon('gear')}${T('Configurações')}</h2>
        <div class="muted st-who">${icon('user')}${esc(me.user)} · ${esc(roleText(me))} · ${esc(verLabel())}${verDate() ? ` ${T('de {0}', [esc(verDate())])}` : ''}</div></div></div>
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
      if (t === 'ia') {
        body.innerHTML = aiFormHTML(st.ai, false);
        bindAI((v) => { st.ai = v; }, null);
      } else if (t === 'whatsapp') {
        body.innerHTML = '<div class="loading"><div class="spinner"></div></div>';
        const render = (n) => {
          body.innerHTML = `<p class="muted" style="margin:0 0 12px;font-size:.84rem">${T('Conexão do WhatsApp que manda os avisos do painel.')}</p>
            <div id="wa-box">${waBoxHTML(n)}</div>
            <p style="margin:14px 0 0;font-size:.86rem"><a href="#/notify">${T('Escolher para quem e o que avisar →')}</a></p>`;
          bindWA($('#wa-box', body), (x) => render(x.notify));
        };
        api('/api/notify').then((j) => render(j.notify)).catch((ex) => { if (ex.message !== 'login') body.innerHTML = `<div class="form-err">${esc(ex.message)}</div>`; });
      } else {
        const origin = me.passwordSource === 'panel' ? `${T('Senha trocada pelo painel em {0}.', [dt(me.passwordChanged)])}` : T('Hoje vale a senha definida no .env do servidor.');
        body.innerHTML = `${langSectionHTML()}<section class="tf-section" id="pw-sec"></section>
          <section class="tf-section" id="tf-sec"></section>
          ${canInstall() ? `<section class="tf-section install-only"><h3>${icon('install')}${T('App no celular')}</h3>
            <p class="muted">${T('Instale o painel como app: ícone na tela inicial, abre em tela cheia e se atualiza sozinho quando sai versão nova.')}</p>
            <div><button class="btn" type="button" data-act="install">${icon('install')}${T('Instalar app')}</button></div></section>` : ''}`;
        // senha e usuário: um resumo e o botão; o formulário só abre quando a pessoa pede
        const pw = $('#pw-sec', body);
        const closedPw = () => {
          pw.innerHTML = `<h3>${icon('key')}${T('Senha e usuário')}</h3>
            <p class="muted"><span>${T('Usuário <b>{0}</b> · {1}', [esc(me.user), esc(origin)])}</span></p>
            <div><button class="btn" type="button" id="pw-open">${icon('key')}${T('Trocar senha ou usuário')}</button></div>`;
          $('#pw-open', pw).addEventListener('click', openPw);
        };
        const openPw = () => {
          pw.innerHTML = `<h3>${icon('key')}${T('Trocar senha ou usuário')}</h3>
            <p class="muted">${T('Ao salvar, as outras sessões (outros aparelhos) saem.')}</p>${accessFormHTML(me.user, false)}`;
          bindAccess('', (j) => { closeDrawer(); toast(`${T('Acesso salvo (usuário {0}). Os outros aparelhos vão precisar entrar de novo.', [j.user])}`); });
          $('#acc-cancel', pw).addEventListener('click', () => { closedPw(); $('#pw-open', pw).focus(); });
        };
        closedPw();
        const sec = $('#tf-sec', body);
        if (opts.enroll && !(me.twoFA || {}).enabled) {
          opts.enroll = false;
          sec.scrollIntoView({ block: 'start' });
          twoFAEnroll(sec, () => renderTwoFA(sec));
        } else renderTwoFA(sec);
      }
    };
    S.settingsTab = show;
    show(tab);
  }

  // ------------------------------------------------------------------ usuários (aba Usuários)
  function permBoxes(u) {
    const me = S.me;
    const box = (k, label, hint, ok) => `<label class="perm${ok ? '' : ' locked'}"><input type="checkbox" class="sw" data-perm="${k}"
      ${u[k] || (k !== 'admin' && u.admin) ? 'checked' : ''} ${ok && !(k !== 'admin' && u.admin) ? '' : 'disabled'}>
      <span><b>${label}</b><small>${hint}</small></span></label>`;
    return `<div class="perms">
      ${box('admin', T('Administrador'), T('Pode tudo: ações nas apps, usuários, IA e WhatsApp.'), me.admin)}
      ${box('actions', T('Pausar e retomar aplicações'), T('Os botões Pausar/Retomar da aba Aplicações.'), me.admin || me.actions)}
      ${box('manage', T('Criar e gerenciar usuários'), T('Sem mexer em administradores; só concede o que tem.'), true)}
      ${box('clean', T('Limpar o disco'), T('A aba Limpeza: cache de build, imagens sem nome, logs e a limpeza automática.'), me.admin || me.clean)}</div>`;
  }
  const readPerms = (root) => Object.fromEntries($$('[data-perm]', root).map((c) => [c.dataset.perm, c.checked]));
  const passBox = (name, pass) => `<div class="passbox" role="status"><div>${T('<b>Senha provisória de {0}</b>', [esc(name)])}</div>
    <div class="passline">${secretOut(pass, T('a senha'))}</div>
    <div class="muted">${T('Passe só para essa pessoa. No primeiro acesso ela cria a própria senha (esta não aparece de novo).')}</div></div>`;
  function usersPanel(body) {
    const load = async (flash) => {
      let j;
      try { j = await api('/api/users'); } catch (ex) {
        if (ex.message !== 'login') body.innerHTML = `<div class="form-err">${esc(ex.message)}</div>`;
        return;
      }
      const me = S.me;
      const editable = (u) => u.name !== me.user && (me.admin || !u.admin);
      body.innerHTML = `${flash || ''}<div class="us-grid"><section class="card"><div class="card-h"><h2>${icon('users')}${T('Quem tem acesso')}</h2><span class="muted">${j.users.length}</span></div>
        <div class="ulist">${j.users.map((u) => `<div class="urow" data-user="${esc(u.name)}">
          <div class="urow-h"><div class="urow-n"><span><b>${esc(u.name)}</b>${u.name === me.user ? ` <span class="muted">${T('(você)')}</span>` : ''}</span>
            <small>${esc(roleText(u))}${u.twoFA ? T(' · 2FA ligado') : ''}${u.mustChange ? T(' · senha provisória') : ''} · ${u.lastLogin ? `${T('último acesso {0}', [dt(u.lastLogin)])}` : T('nunca entrou')}</small></div>
            ${editable(u) ? `<div class="controls"><button class="btn sm" type="button" data-u="edit">${T('Permissões')}</button>
              <button class="btn sm" type="button" data-u="reset">${T('Nova senha')}</button>
              ${u.twoFA ? `<button class="btn sm" type="button" data-u="tfoff">${T('Desligar 2FA')}</button>` : ''}
              <button class="btn sm" type="button" data-u="del" aria-label="${T('Remover {0}', [esc(u.name)])}">${T('Remover')}</button></div>` : ''}</div>
          ${editable(u) ? `<div class="urow-edit" hidden>${permBoxes(u)}<div class="controls"><button class="btn primary sm" type="button" data-u="save">${T('Salvar permissões')}</button></div></div>` : ''}
        </div>`).join('')}</div></section>
        <section class="card"><form class="stack" id="unew" autocomplete="off"><h2 class="us-h">${icon('plus')}${T('Novo usuário')}</h2>
          <div class="field"><label for="un-name">${T('Nome de usuário')}</label><input class="input" id="un-name" minlength="3" maxlength="32" required placeholder="${T('ex.: maria')}" autocapitalize="off" spellcheck="false"></div>
          ${permBoxes({})}
          <div class="form-err" id="un-err" role="alert"></div>
          <button class="btn primary" type="submit">${T('Criar usuário')}</button>
          <p class="muted" style="margin:0;font-size:.8rem">${T('Sem nenhuma permissão marcada, a pessoa só vê (dados, logs e o chat com a IA). O painel gera uma senha provisória; a pessoa cria a dela no primeiro acesso.')}</p></form></section></div>`;
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
        o.disabled = c.checked || (o.dataset.perm === 'actions' && !can.act()) || (o.dataset.perm === 'clean' && !can.clean());
      });
    };
    body.onclick = async (e) => {
      const b = e.target.closest('[data-u]');
      if (!b) return;
      const row = b.closest('.urow');
      const name = row.dataset.user;
      const post = async (path, extra) => api(path, { method: 'POST', body: JSON.stringify({ name, ...extra }) });
      try {
        if (b.dataset.u === 'edit') { $('.urow-edit', row).hidden = !$('.urow-edit', row).hidden; return; }
        if (b.dataset.u === 'save') { await post('/api/users/update', readPerms(row)); toast(`${T('Permissões de {0} salvas. As sessões dele(a) saíram.', [name])}`); load(); return; }
        if (b.dataset.u === 'reset') {
          if (!await confirmDialog({ title: `${T('Nova senha para {0}?', [name])}`, ok: T('Gerar senha'),
            body: `<p>${T('O painel gera uma senha provisória e {0} sai de todos os aparelhos. No próximo acesso, cria a própria senha.', [esc(name)])}</p>` })) return;
          const r = await post('/api/users/reset');
          load(passBox(name, r.password));
          return;
        }
        if (b.dataset.u === 'tfoff') {
          if (!await confirmDialog({ title: `${T('Desligar o 2FA de {0}?', [name])}`, ok: T('Desligar'), danger: true,
            body: `<p>${T('Use quando a pessoa perdeu o celular. {0} passa a entrar só com a senha até ligar de novo em Minha conta.', [esc(name)])}</p>` })) return;
          await post('/api/users/2fa-off');
          toast(`${T('2FA de {0} desligado.', [name])}`);
          load();
          return;
        }
        if (b.dataset.u === 'del') {
          if (!await confirmDialog({ title: `${T('Remover {0}?', [name])}`, ok: T('Remover'), danger: true,
            body: `<p>${T('{0} deixa de acessar o painel na hora (as sessões abertas caem). Dá para cadastrar de novo depois.', [esc(name)])}</p>` })) return;
          await post('/api/users/delete');
          toast(`${T('{0} removido(a).', [name])}`);
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
            <section class="card"><div class="card-h"><div><h2>${T('Saída por dia')}</h2><div class="muted" style="font-size:.82rem">${T('Últimos 30 dias · toque numa barra')}</div></div></div>
              <div id="tr-days"></div></section>
            <section class="card chart-card"><div class="card-h"><div><h3>${T('Velocidade da rede')}</h3><div class="sub" style="margin:0">${T('Placa principal do servidor')}</div></div>${rangeSeg(tr.range, 'trange', RANGES.slice(0, 5))}</div>
              <div class="chart" id="tr-chart"></div></section>
            <section class="card" id="tr-apps"></section>
            <section class="card" id="tr-cts"></section>
            <section class="card" id="tr-months"></section>
            <section class="note">${T('<b>Como a banda é medida.</b> O total do servidor vem da placa de rede principal (o mesmo que a Oracle mede para o limite de 10 TB de saída). Por aplicação, soma-se o tráfego da rede de cada contêiner — isso inclui conversas internas (ex.: API ↔ banco, app ↔ túnel), então a soma das apps é maior que o total real. Quem fala com a internet são as portas de entrada: túneis da Cloudflare (cloudflared) ou um proxy com portas abertas (Caddy, Nginx…).')}</section>`;
          tr.chart = makeChart($('#tr-chart'), { fmt: rate, series: [{ label: T('Saída'), color: cssVar('--s2') }, { label: T('Entrada'), color: cssVar('--s1') }] });
          trafficView.setRange = (r) => { tr.range = r; $$('[data-act="trange"]').forEach((b) => b.setAttribute('aria-pressed', b.dataset.v === r)); loadChart(); };
          loadChart();
        }
        const s = t.summary;
        const egP = (s.month.tx / s.limitBytes) * 100;
        $('#tr-hero').innerHTML = `<div class="card-h"><h2>${T('Saída para a internet neste mês')}</h2>${badge(egP >= 90 ? 'crit' : egP >= 75 ? 'warn' : 'ok', `${T('{0} do grátis', [pct(egP)])}`)}</div>
          <div class="hero num">${data(s.month.tx)}</div>
          <div class="meter thick" style="margin:12px 0 8px"><i class="${level(egP, 75, 90)}" style="width:${Math.min(100, egP).toFixed(2)}%"></i></div>
          <div class="muted" style="font-size:.85rem">${T('Limite grátis da Oracle: <b class="ink2">10 TB/mês</b> · projeção para o mês: <b class="ink2">{0}</b> · entrada no mês: {1} (entrada não é cobrada)', [s.projectedTx ? data(s.projectedTx) : T('calculando…'), data(s.month.rx)])}</div>`;
        $('#tr-kpis').innerHTML = [
          kpi('up', T('Hoje'), `↑ ${data(s.today.tx)}`, `${T('↓ {0} recebidos', [data(s.today.rx)])}`),
          kpi('clock', T('Ontem'), `↑ ${data(s.yesterday.tx)}`, `${T('↓ {0} recebidos', [data(s.yesterday.rx)])}`),
          kpi('net', T('Mês passado'), `↑ ${data(s.lastMonth.tx)}`, `${T('↓ {0} recebidos', [data(s.lastMonth.rx)])}`),
          kpi('net', T('Desde o boot'), `↑ ${data(s.sinceBoot.tx)}`, `${T('↓ {0} · contador da placa {1}', [data(s.sinceBoot.rx), esc(s.iface)])}`),
        ].join('');
        tr.days = t.days.slice(-30);
        const maxD = Math.max(1, ...tr.days.map((d) => d.tx));
        $('#tr-days').innerHTML = `<div class="bars">${tr.days.map((d, i) => `<div data-act="day" data-i="${i}" title="${esc(d.d)}"><i style="height:${((d.tx / maxD) * 100).toFixed(1)}%"></i></div>`).join('')}</div>
          <div class="bars-x"><span>${esc(tr.days[0].d.slice(8))}/${esc(tr.days[0].d.slice(5, 7))}</span><span>${T('hoje')}</span></div>
          <div class="tip" id="tr-tip">${T('Maior dia: {0}', [data(maxD)])}</div>`;
        trafficView.day = (i) => {
          const d = tr.days[i];
          $$('.bars > div').forEach((b) => b.classList.toggle('on', b.dataset.i == i));
          $('#tr-tip').innerHTML = `${T('<b>{0}/{1}</b> · saída {2} · entrada {3}', [esc(d.d.slice(8)), esc(d.d.slice(5, 7)), data(d.tx), data(d.rx)])}`;
        };
        const rowsT = (list, withApp) => list.map((x) => `<tr class="clickable" data-unit="${esc(withApp ? x.key : 'app:' + x.app)}">
          <td><div class="cell-name">${swatch(colorOf(x.color))}<span>${esc(x.name)}</span></div></td>
          <td class="r">${data(x.today.tx)}</td><td class="r">${data(x.month.tx)}</td><td class="r">${data(x.month.rx)}</td><td class="r">${data(x.lastMonth.tx)}</td></tr>`).join('');
        const head = `<thead><tr><th>${T('Nome')}</th><th class="r">${T('Saída hoje')}</th><th class="r">${T('Saída no mês')}</th><th class="r">${T('Entrada no mês')}</th><th class="r">${T('Saída mês passado')}</th></tr></thead>`;
        $('#tr-apps').innerHTML = `<div class="card-h"><h2>${T('Por aplicação')}</h2></div>${t.apps.length ? `<div class="table-wrap"><table>${head}<tbody>${rowsT(t.apps, false)}</tbody></table></div>` : `<div class="empty">${T('Ainda sem dados — a contagem começa quando o monitor sobe.')}</div>`}`;
        $('#tr-cts').innerHTML = `<div class="card-h"><h2>${T('Por contêiner')}</h2></div>${t.containers.length ? `<div class="table-wrap"><table>${head}<tbody>${rowsT(t.containers, true)}</tbody></table></div>` : `<div class="empty">${T('Ainda sem dados.')}</div>`}`;
        $('#tr-months').innerHTML = `<div class="card-h"><h2>${T('Meses')}</h2><span class="muted" style="font-size:.8rem">${T('contando desde {0}', [dt(s.since)])}</span></div>
          <div class="table-wrap"><table><thead><tr><th>${T('Mês')}</th><th class="r">${T('Saída')}</th><th class="r">${T('Entrada')}</th><th class="r">${T('% do grátis')}</th></tr></thead>
          <tbody>${t.months.slice().reverse().filter((m) => m.tx || m.rx).map((m) => `<tr><td>${esc(m.d.slice(5))}/${esc(m.d.slice(0, 4))}</td>
          <td class="r">${data(m.tx)}</td><td class="r">${data(m.rx)}</td><td class="r">${pct((m.tx / s.limitBytes) * 100)}</td></tr>`).join('') || `<tr><td colspan="4" class="muted">${T('Ainda sem dados.')}</td></tr>`}</tbody></table></div>`;
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
        <div class="section-h"><div><h2>${T('Logs dos contêineres')}</h2><p>${T('Lidos na hora pelo Docker (só leitura). Linhas que parecem erro ficam em destaque.')}</p></div></div>
        <section class="card">
          <div class="controls" style="margin-bottom:12px">
            <div class="field" style="flex:1 1 220px"><label for="lg-c">${T('Contêiner')}</label><select class="input" id="lg-c"></select></div>
            <div class="field"><label for="lg-n">${T('Linhas')}</label><select class="input" id="lg-n">${[100, 300, 1000, 2000].map((n) => `<option value="${n}"${n === L.tail ? ' selected' : ''}>${n}</option>`).join('')}</select></div>
            <div class="field" style="flex:1 1 180px"><label for="lg-q">${T('Filtrar texto')}</label><input class="input" id="lg-q" type="search" placeholder="${T('ex.: 500, timeout')}" value="${esc(L.q)}"></div>
            <div class="field"><label>&nbsp;</label><div class="controls">
              <label class="toggle"><input type="checkbox" id="lg-e"${L.errors ? ' checked' : ''}>${T('Só erros')}</label>
              <label class="toggle"><input type="checkbox" id="lg-l"${L.live ? ' checked' : ''}>${T('Ao vivo')}</label>
              <label class="toggle"><input type="checkbox" id="lg-w"${L.wrap ? ' checked' : ''}>${T('Quebrar linhas')}</label>
              <button class="btn" type="button" id="lg-r">${icon('refresh')}${T('Atualizar')}</button></div></div>
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
        box.innerHTML = shown.length ? shown.map((l) => logLine(l, L.c === '*')).join('') : `<div class="empty">${T('Nenhuma linha.')}</div>`;
        $('#lg-meta').textContent = `${T('{0} de {1} linhas · {2} parecem erro · lido às {3}', [shown.length, lines.length, lines.filter((l) => l.e).length, hms(Date.now())])}`;
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
          sel.innerHTML = `<option value="*">${T('Todos os contêineres (misturados)')}</option>` + Object.entries(byApp).map(([app, list]) =>
            `<optgroup label="${esc(app)}">${list.map((t) => `<option value="${esc(t.name)}">${esc(t.name)}${t.state !== 'running' ? T(' (parado)') : ''}</option>`).join('')}</optgroup>`).join('');
          sel.value = ts.some((t) => t.name === L.c) ? L.c : '*';
          L.c = sel.value;
          fetchLogs(true);
        }
        $('#lg-act').innerHTML = `<div class="card-h"><div><h2>${T('Atividade de log')}</h2><div class="muted" style="font-size:.82rem">${T('Contada a cada 5 min. Toque para abrir.')}</div></div></div>
          <div class="table-wrap"><table><thead><tr><th>${T('Contêiner')}</th><th>${T('Aplicação')}</th><th class="r">${T('Linhas (1 h)')}</th><th class="r">${T('Erros (1 h)')}</th><th class="r">${T('Erros (24 h)')}</th></tr></thead>
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
        const psi = (label, v10, v60, tip) => `<div style="display:grid;gap:4px"><div style="display:flex;justify-content:space-between;font-size:.85rem"><span>${label}</span><span class="num">${pct(v10)} <span class="muted">${T('(1 min: {0})', [pct(v60)])}</span></span></div>
          <div class="meter"><i class="${level(v10, 10, 30)}" style="width:${Math.min(100, v10)}%"></i></div><div class="muted" style="font-size:.76rem">${tip}</div></div>`;
        const procs = S.procSort === 'cpu' ? s.byCpu : s.byMem;
        const svcs = s.services.filter((x) => x.cpu > 0.01 || x.mem > 4 * 1048576);
        const d = s.disk || {};
        $('#sy').innerHTML = `
          <section class="grid g2">
            <div class="card"><div class="card-h"><h2>${T('Servidor')}</h2>${badge('ok', T('No ar há {0}', [dur(h.uptime)]))}</div><div class="kv">
              ${kv(T('Nome'), esc(sv.hostname || '—'))}${kv(T('Sistema'), esc(sv.os || '—'))}${kv(T('Kernel'), esc(sv.kernel || '—'))}${kv(T('Arquitetura'), esc(sv.arch || '—'))}
              ${kv(T('Núcleos'), `${h.cores} vCPU`)}${kv(T('Memória'), bytes(h.memTotal))}${kv(T('Swap'), h.swapTotal ? `${T('{0} de {1}', [bytes(h.swapUsed), bytes(h.swapTotal)])}` : T('sem swap'))}${kv(T('Disco (volume)'), bytes(sv.diskSize))}
              ${kv(T('Docker'), esc(sv.docker || '—'))}${kv(T('Ligado desde'), dt(boot))}${kv(T('Processos'), `${T('{0} ({1} rodando)', [num(h.procs, 0), num(h.procsRunning, 0)])}`)}${kv(T('Conexões TCP'), `${T('{0} abertas · {1} em espera', [num(h.tcp, 0), num(h.tcpTimeWait, 0)])}`)}
              ${kv(T('Contêineres'), i.containers ? `${T('{0} rodando · {1} parados', [i.running, i.stopped])}` : '—')}${kv(T('Imagens'), num(i.images, 0))}
              ${kv(T('OOM kills (boot)'), num(h.oomKills, 0))}${kv(T('Versão do painel'), esc(s.version || '—'))}
              ${sv.cloud && sv.cloud.provider === 'oracle' ? kv(T('Nuvem'), `${T('Oracle · {0}', [esc(sv.cloud.regionName)])}`) + kv(T('Shape'), `${esc(sv.cloud.shape)} <small class="muted">${T('{0} OCPU · {1} GB · {2} Gbps', [num(sv.cloud.ocpus, 0), num(sv.cloud.memGb, 0), num(sv.cloud.netGbps, 0)])}</small>`) : ''}
            </div></div>
            <div class="card"><div class="card-h"><div><h2>${T('Pressão do sistema')}</h2><div class="muted" style="font-size:.82rem">${T('% do tempo em que algo ficou esperando (PSI do kernel). Perto de 0 = folgado.')}</div></div></div>
              <div style="display:grid;gap:14px">
                ${psi('CPU', h.psi.cpu10, h.psi.cpu60, T('Tarefas esperando a vez no processador.'))}
                ${psi(T('Memória'), h.psi.mem10, h.psi.mem60, T('Tempo perdido recuperando RAM (cache, swap). Subiu = falta memória.'))}
                ${psi(T('Disco'), h.psi.io10, h.psi.io60, T('Tarefas esperando leitura/escrita no disco.'))}
              </div>
              <h3 style="margin:18px 0 8px">${T('CPU por núcleo')}</h3>
              <div style="display:grid;gap:8px">${(h.perCpu || []).map((c, k) => `<div class="share-row" style="grid-template-columns:70px minmax(0,1fr) 52px"><span class="share-label">${T('Núcleo {0}', [k])}</span>
                <div class="meter thick"><i class="${level(c, 75, 90)}" style="width:${Math.min(100, c).toFixed(1)}%"></i></div><span class="share-total num">${pct(c)}</span></div>`).join('')}</div>
            </div>
          </section>
          <section class="card"><div class="card-h"><div><h2>${T('Processos')}</h2><div class="muted" style="font-size:.82rem">${T('Os 15 que mais gastam agora, com a aplicação dona.{0}', [s.warming ? T(' Medindo — o % de CPU aparece na próxima leitura.') : ''])}</div></div>
            <div class="seg"><button type="button" data-act="psort" data-v="cpu" aria-pressed="${S.procSort === 'cpu'}">${T('Por CPU')}</button><button type="button" data-act="psort" data-v="mem" aria-pressed="${S.procSort === 'mem'}">${T('Por memória')}</button></div></div>
            <div class="table-wrap"><table><thead><tr><th>${T('Processo')}</th><th>${T('De quem')}</th><th class="r">CPU</th><th class="r">${T('Memória')}</th><th class="r">${T('Threads')}</th><th class="r">PID</th></tr></thead>
            <tbody>${procs.map((p) => `<tr><td class="mono">${esc(p.name)}</td><td class="ink2">${esc(p.unitName || '—')}</td><td class="r">${pct(p.cpu)}</td><td class="r">${bytes(p.rss)}</td><td class="r">${num(p.threads, 0)}</td><td class="r muted">${p.pid}</td></tr>`).join('')}</tbody></table></div></section>
          <section class="card"><div class="card-h"><div><h2>${T('Serviços do Linux')}</h2><div class="muted" style="font-size:.82rem">${T('Fora dos contêineres: Docker, agentes da Oracle, runners do GitHub, SSH…')}</div></div></div>
            <div class="table-wrap"><table><thead><tr><th>${T('Serviço')}</th><th class="r">CPU</th><th class="r">${T('Memória')}</th><th class="r">${T('Disco E/S')}</th></tr></thead>
            <tbody>${svcs.map((x) => `<tr class="clickable" data-unit="${esc(x.key)}"><td><div>${esc(x.name)}</div>${x.desc ? `<div class="cell-sub">${esc(x.desc)}</div>` : ''}</td>
            <td class="r">${pct(x.cpu)}</td><td class="r">${bytes(x.mem)}</td><td class="r">${rate(x.ioRead + x.ioWrite)}</td></tr>`).join('')}</tbody></table></div></section>
          <section class="grid g2">
            <div class="card"><div class="card-h"><div><h2>${T('Disco do Docker')}</h2><div class="muted" style="font-size:.82rem">${d.t ? T('medido {0} (a cada 30 min)', [ago(d.t)]) : T('medindo…')}</div></div></div>
              ${d.t ? `<div class="kv">${kv(T('Imagens'), `${bytes(d.imagesSize)} <small class="muted">(${num(d.imagesCount, 0)})</small>`)}${kv(T('Imagens sem uso'), bytes(d.imagesUnused))}
              ${kv(T('Volumes'), bytes(d.volumesSize))}${kv(T('Contêineres (camada gravável)'), bytes(d.containersSize))}${kv(T('Cache de build (liberável)'), `<b>${bytes(d.buildCacheSize)}</b>`)}${kv(T('Logs dos contêineres'), S.ov && S.ov.storage.logsKnown ? bytes(S.ov.storage.logs) : '—')}</div>
              <h3 style="margin:16px 0 4px">${T('Volumes')}</h3><div class="table-wrap"><table><tbody>${(d.volumes || []).map((x) => `<tr><td class="mono" style="font-size:.8rem">${esc(x.name)}</td><td class="r">${bytes(x.size)}</td></tr>`).join('')}</tbody></table></div>
              <h3 style="margin:16px 0 4px">${T('Maiores imagens')}</h3><div class="table-wrap"><table><tbody>${(d.images || []).slice(0, 8).map((x) => `<tr><td class="mono" style="font-size:.8rem">${esc(x.tags.join(', '))}</td><td class="r">${x.containers ? '' : `<span class="muted">${T('sem uso ·')} </span>`}${bytes(x.size)}</td></tr>`).join('')}</tbody></table></div>` : '<div class="loading"><div class="spinner"></div></div>'}
            </div>
            <div class="card"><div class="card-h"><div><h2>${T('Eventos dos contêineres')}</h2><div class="muted" style="font-size:.82rem">${T('Subidas, quedas, reinícios e healthchecks.')}</div></div></div>
              ${s.events.length ? `<div class="table-wrap"><table><tbody>${s.events.slice(0, 60).map((e) => `<tr><td class="muted num" style="white-space:nowrap">${dt(e.t)}</td><td>${esc(e.container)}</td><td>${eventLabel(e)}</td></tr>`).join('')}</tbody></table></div>` : `<div class="empty">${T('Nenhum evento nas últimas 24 h.')}</div>`}
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
    if (a === 'die') return e.requested ? badge('off', T('parou (deploy/stop)')) : e.exitCode && e.exitCode !== '0' ? badge('crit', `${T('caiu (código {0})', [e.exitCode])}`) : badge('off', 'parou');
    if (a === 'oom' || a === 'oom_kill_host') return badge('crit', T('sem memória (OOM)'));
    if (a === 'start') return badge('ok', T('subiu'));
    if (a === 'restart') return badge('warn', T('reiniciou'));
    if (a.startsWith('health:')) return a.includes('unhealthy') ? badge('crit', 'unhealthy') : badge('ok', a.replace('health:', '').trim());
    const map = { create: T('criado'), destroy: T('removido'), kill: T('sinal enviado'), stop: T('parado'), pause: T('pausado'), unpause: T('retomado'), rename: T('renomeado'), update: T('atualizado') };
    return `<span class="ink2">${esc(map[a] || a)}</span>`;
  }

  // ------------------------------------------------------------------ aba: limites
  const limitsView = {
    mount(v) {
      v.innerHTML = `<div class="page"><div class="section-h"><div><h2>${T('Limites do plano grátis')}</h2>
        <p id="lm-sub">${T('Carregando…')}</p></div></div>
        <section class="alerts" id="lm-alerts"></section>
        <section class="grid g2" id="lm-items"></section>
        <section class="card" id="lm-reclaim"></section>
        <section class="card"><h2 style="margin-bottom:10px">${T('Cloudflare (plano Free)')}</h2><div class="kv">
          ${kv(T('Banda dos túneis'), T('sem limite'))}${kv(T('Upload por requisição'), T('até 100 MB'))}${kv(T('Resposta mais lenta'), T('cai em 100 s'))}${kv(T('Custo'), 'R$ 0')}</div>
          <div class="note" style="margin-top:12px">${T('Os sites saem pelo túnel (cloudflared), então o tráfego para os visitantes passa pela Cloudflare — mas sai do servidor do mesmo jeito e conta para os 10 TB da Oracle.')}</div></section></div>`;
    },
    update() {
      const o = S.ov, L = o.limits;
      const sub = {
        a1: `${T('VM {0} em {1}, no plano Always Free da Oracle (Ampere A1). Os limites da Oracle são da conta inteira; aqui aparece o que esta VM usa.', [L.shape, L.region])}`,
        micro: `${T('VM {0} em {1}, no plano Always Free da Oracle (AMD Micro).', [L.shape, L.region])}`,
        paid: `${T('VM {0} em {1}: este shape não é Always Free (é cobrado). Aqui ficam o disco e a saída de dados.', [L.shape, L.region])}`,
      }[L.kind] || T('Este servidor não parece ser uma VM da Oracle: aqui ficam só o disco e a saída de dados.');
      $('#lm-sub').textContent = sub;
      $('#lm-reclaim').classList.toggle('hidden', L.kind !== 'a1' && L.kind !== 'micro');
      $('#lm-alerts').innerHTML = alertsHTML(o.alerts.filter((a) => a.area === 'limite'), T('Nenhum limite do plano grátis em risco.'));
      const fmt = (it, val) => (it.unit === 'bytes' ? (it.key === 'egress' ? data(val) : bytes(val)) : it.unit === 'ocpu' ? `${num(val, 0)} OCPU` : it.unit === 'vm' ? `${num(val, 0)} VM` : `${num(val, 0)} GB`);
      $('#lm-items').innerHTML = L.items.map((it) => {
        const p = it.limit ? (it.used / it.limit) * 100 : 0;
        const lv = it.level === 'crit' ? 'crit' : it.level === 'warn' ? 'warn' : 'ok';
        return `<div class="card"><div class="card-h"><h3>${esc(it.label)}</h3>${badge(lv, pct(p))}</div>
          <div class="kpi-value num" style="margin-bottom:8px">${fmt(it, it.used)} <small>${T('de {0}', [fmt(it, it.limit)])}</small></div>
          <div class="meter thick"><i class="${it.level === 'crit' ? 'crit' : it.level === 'warn' ? 'warn' : ''}" style="width:${Math.min(100, p).toFixed(1)}%"></i></div>
          <div class="muted" style="font-size:.8rem;margin-top:8px">${esc(it.note)}</div></div>`;
      }).join('');
      const r = L.reclaim;
      const bar = (label, val, tip) => `<div style="display:grid;gap:4px"><div style="display:flex;justify-content:space-between;font-size:.85rem"><span>${label}</span><span class="num"><b>${pct(val)}</b> <span class="muted">${T('· regra: abaixo de 20%')}</span></span></div>
        <div class="meter thick" style="position:relative"><i class="${val < 20 ? 'warn' : ''}" style="width:${Math.min(100, val).toFixed(1)}%"></i></div><div class="muted" style="font-size:.76rem">${tip}</div></div>`;
      const verdict = !r.ready ? badge('info', `${T('coletando · {0} de 7 dias', [num(r.dataDays, 1)])}`)
        : r.idle ? badge('warn', T('parece ociosa')) : badge('ok', T('não ociosa'));
      $('#lm-reclaim').innerHTML = `<div class="card-h"><div><h2>${T('Risco de a Oracle recuperar a VM por ociosidade')}</h2>
        <div class="muted" style="font-size:.82rem">${T('Regra do Always Free: se em 7 dias a CPU (percentil 95), a rede e a memória ficarem <b>todas</b> abaixo de 20%, a Oracle pode desligar e recuperar a instância.')}</div></div>${verdict}</div>
        <div style="display:grid;gap:14px">
          ${bar(T('CPU — percentil 95 em 7 dias'), r.cpuP95, T('95% do tempo a CPU ficou abaixo disso.'))}
          ${bar(T('Memória — média em 7 dias'), r.memAvg, T('Memória em uso (sem cache) sobre o total.'))}
          ${bar(T('Rede — média em 7 dias'), r.netAvg, T('Tráfego sobre a banda do shape (informada pela Oracle).'))}
        </div>
        <div class="note" style="margin-top:14px">${T('{0} Estimativa feita com médias de 5 min; a Oracle mede com o próprio agente.', [r.applies === 'no' ? `${T('Configurado como conta <b>Pay As You Go</b>: essa regra não se aplica.')}`
          : r.applies === 'yes' ? `${T('Configurado como conta <b>Always Free</b>: a regra se aplica. Basta <b>um</b> dos três ficar acima de 20% para não contar como ociosa.')}`
            : `${T('Não sei se a conta é Always Free ou Pay As You Go (contas pagas não sofrem essa recuperação). Ajuste <b>VPMON_ALWAYS_FREE</b> no .env do painel. Basta <b>um</b> dos três acima de 20% para a VM não contar como ociosa.')}`])}</div>`;
    },
  };

  // ------------------------------------------------------------------ aba: infos (urgente, alertas, informações)
  const infosView = {
    mount(v) {
      v.innerHTML = `<div class="page"><div class="section-h"><div><h2>${T('Infos')}</h2>
        <p>${T('Tudo que o painel percebeu, do mais urgente ao só-para-saber. Atualiza sozinho a cada 5 s.')}</p></div></div>
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
        ${sec('crit', T('Urgente'), T('Nada urgente agora.'))}
        ${sec('warn', T('Alertas'), T('Nenhum alerta.'))}
        ${sec('info', T('Informações'), T('Nada a observar.'))}</div>`;
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
    T('Como está o servidor agora? Tem algo preocupante?'),
    T('Qual app mais usou CPU e memória nas últimas 24 h?'),
    T('Quanto de banda cada app enviou este mês?'),
    T('Tem erros importantes nos logs da última hora?'),
    T('O que dá para limpar no disco com segurança?'),
    T('Estou perto da regra de VM ociosa da Oracle?'),
  ];
  const aiView = {
    mount(v) {
      S.chat = store.get('chat', []);
      v.innerHTML = `<div class="page">
        <div class="section-h"><div><h2>${icon('spark')} ${T('Pergunte sobre o servidor')}</h2>
          <p id="ai-sub">${T('Carregando…')}</p></div>
          <button class="btn" type="button" data-act="chat-new">${T('Nova conversa')}</button></div>
        <section class="card chat">
          <div class="chat-log" id="chat-log" aria-live="polite"></div>
          <form class="chat-form" id="chat-form">
            <textarea class="input" id="chat-in" rows="1" placeholder="${T('Pergunte algo… ex.: quem mais usou CPU hoje?')}" aria-label="${T('Pergunta')}"></textarea>
            <button class="btn primary" type="submit" id="chat-send" aria-label="${T('Enviar')}">${icon('send')}<span>${T('Enviar')}</span></button>
          </form>
          <div class="chat-note">${T('As perguntas e os dados que a IA consulta vão para a DeepSeek. Nos logs, tokens, senhas e e-mails seguem mascarados e o fim dos IPs é escondido.')}</div>
        </section></div>`;
      api('/api/chat/status').then((st) => {
        S.chatOn = st.enabled;
        $('#ai-sub').innerHTML = st.enabled
          ? esc(`${T('DeepSeek ({0}) com acesso só de leitura ao estado atual, ao histórico, à banda, aos logs, aos processos e aos eventos.', [st.model])}`)
          : can.admin() ? `${T('A IA está desligada.')} <a href="#" data-act="settings" data-v="ia">${T('Ponha a chave da DeepSeek em Configurações → IA')}</a>.`
            : T('A IA está desligada. Um administrador pode ligar em Configurações → IA.');
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
        log.innerHTML = `<div class="chat-empty">${icon('spark')}<div>${T('<b>O que você quer saber do servidor?</b><br>')}<span class="muted">${T('A IA lê os dados do painel e explica com números.')}</span></div>
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
        btn.innerHTML = S.chatBusy ? `${icon('stop')}<span>${T('Parar')}</span>` : `${icon('send')}<span>${T('Enviar')}</span>`;
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
          headers: { 'X-Requested-With': 'vpmon', 'X-VPMon-Lang': LANG, 'Content-Type': 'application/json' },
          body: JSON.stringify({ messages: history }),
        });
        if (r.status === 401) { showLogin(); return; }
        if (!r.ok) {
          const j = await r.json().catch(() => ({}));
          throw new Error((j.error && j.error.message) || `${T('Erro {0}', [r.status])}`);
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
              msg.meta = `${T('{0} · {1} mil tokens · {2} s', [d.model, num(tok / 1000, 1), num((Date.now() - t0) / 1000, 0)])}`;
            }
            paint();
          }
        }
      } catch (e) {
        if (e.name === 'AbortError') msg.error = msg.content ? '' : T('Pergunta cancelada.');
        else msg.error = e.message;
        if (e.name === 'AbortError' && msg.content) msg.meta = T('interrompida');
      } finally {
        msg.streaming = false;
        if (!msg.content && !msg.error) msg.error = T('A IA não respondeu nada. Tente de novo.');
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
    if (d.endsWith('@g.us')) return T('grupo');
    if (d.startsWith('55') && (d.length === 12 || d.length === 13)) return `+55 ${d.slice(2, 4)} ${d.slice(4, -4)}-${d.slice(-4)}`;
    return d ? '+' + d : '';
  }
  // *negrito* e _itálico_ do WhatsApp (tudo escapado antes)
  const waText = (t) => esc(t).replace(/\*([^*\n]+)\*/g, '<b>$1</b>').replace(/(^|[\s(])_([^_\n]+)_(?=$|[\s).,!?:])/gm, '$1<i>$2</i>').replace(/\n/g, '<br>');
  function waStatusHTML(n) {
    if (!n.installed) return badge('off', T('WhatsApp não instalado'));
    const st = n.status;
    if (!st.service) return badge('crit', T('Serviço fora do ar'));
    if (st.state === 'open') return `${badge('ok', T('Conectado'))} <span class="muted wa-who">${esc([st.name, fmtPhone(st.number)].filter(Boolean).join(' · '))}</span>`;
    if (st.state === 'connecting') return badge('warn', T('Esperando o celular'));
    return badge('off', T('Desconectado'));
  }
  function waBoxHTML(n) {
    if (!n.installed) {
      return `<div class="note">${T('O WhatsApp das notificações não está rodando neste servidor. Para ligar, no <code>.env</code> do painel ponha <code>COMPOSE_PROFILES=whatsapp</code> (as chaves <code>VPMON_WA_KEY</code> e <code>VPMON_WA_DB_PASSWORD</code> o <code>vpmon init</code> já gera) e rode <code>docker compose up -d</code>. Usa ~250 MB de RAM.')}</div>`;
    }
    const st = n.status;
    if (!st.service) {
      return `<div class="wa-row">${waStatusHTML(n)}</div><div class="note" style="margin-top:10px">${T('A Evolution (contêiner <b>vpserver-whatsapp</b>) não respondeu{0}. Logo depois de subir ela leva cerca de 1 minuto. Veja em Aplicações → vpserver-monitoring.', [st.error ? `: ${esc(st.error)}` : ''])}</div>`;
    }
    if (st.state === 'open') {
      return `<div class="wa-row"><span class="wa-st">${waStatusHTML(n)}</span><button class="btn" type="button" data-wa="logout">${T('Desconectar')}</button></div>`;
    }
    return `<div class="wa-row">${waStatusHTML(n)}</div>
      <div id="wa-qr" class="wa-qr">
        <p class="muted" style="margin:0;font-size:.84rem">${T('Dica: conecte um número só para os avisos (um chip ou um WhatsApp Business de outro número) e mande para o seu. Mensagem do seu próprio número para você mesmo chega sem tocar.')}</p>
        <div class="controls"><button class="btn primary" type="button" data-wa="qr">${icon('qr')}${T('Mostrar QR code')}</button>
        <button class="btn" type="button" data-wa="code">${icon('phone')}${T('Conectar com código')}</button></div>
      </div>`;
  }
  // liga os botões da caixa de conexão; onChange(resposta de /api/notify) quando conecta ou desconecta
  function bindWA(box, onChange) {
    let finished = false;
    const done = async () => {
      if (finished) return;
      finished = true;
      stopWA();
      toast(T('WhatsApp conectado.'));
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
          $('#wa-qr', box).innerHTML = `<div class="wa-qrbox">${qr.image ? `<img alt="${T('QR code para conectar o WhatsApp')}" src="${esc(qr.image)}">` : '<div class="spinner"></div>'}</div>
            <ol class="howto"><li>${T('No celular: WhatsApp → <b>Aparelhos conectados</b> → <b>Conectar aparelho</b>.')}</li>
            <li>${T('Aponte a câmera para o código. Ele muda a cada ~20 s e se atualiza sozinho aqui.')}</li></ol>
            <button class="btn" type="button" data-wa="code">${icon('phone')}${T('Estou no celular: prefiro um código')}</button>`;
        } catch (ex) {
          stopWA();
          if (ex.message !== 'login' && $('#wa-qr', box)) $('#wa-qr', box).innerHTML = `<div class="form-err">${esc(ex.message)}</div><button class="btn" type="button" data-wa="qr">${T('Tentar de novo')}</button>`;
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
      area.innerHTML = `<form class="stack" id="wa-code-f"><div class="field"><label for="wa-num">${T('Número do WhatsApp que vai mandar os avisos (com DDI)')}</label>
        <input class="input" id="wa-num" inputmode="tel" autocomplete="tel" placeholder="+55 11 91234-5678" value="+55 "></div>
        <div class="form-err" id="wa-err" role="alert"></div>
        <div class="controls"><button class="btn primary" type="submit">${T('Gerar código')}</button><button class="btn" type="button" data-wa="qr">${icon('qr')}${T('Usar QR code')}</button></div></form>`;
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
          if (!qr.pairingCode) throw new Error(T('A Evolution não devolveu o código. Espere alguns segundos e tente de novo.'));
          const c = qr.pairingCode;
          area.innerHTML = `<div class="wa-code num" aria-label="${T('Código de pareamento')}">${esc(c.slice(0, 4))}-${esc(c.slice(4))}</div>
            <ol class="howto"><li>${T('No celular: WhatsApp → <b>Aparelhos conectados</b> → <b>Conectar aparelho</b>.')}</li>
            <li>${T('Toque em <b>Conectar com número de telefone</b> e digite o código acima.')}</li></ol>
            <button class="btn" type="button" data-wa="qr">${icon('qr')}${T('Usar QR code')}</button>`;
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
        if (b.dataset.sure !== '1') { b.dataset.sure = '1'; b.textContent = T('Confirmar: desconectar'); return; }
        b.disabled = true;
        try {
          await api('/api/notify/logout', { method: 'POST', body: '{}' });
          toast(T('WhatsApp desconectado.'));
          onChange(await api('/api/notify'));
        } catch (ex) { if (ex.message !== 'login') toast(ex.message); b.disabled = false; }
      }
    };
  }

  // ------------------------------------------------------------------ aba: notificações
  const NT_GROUPS = [
    ['alertas', T('Alertas'), T('Saem quando o problema começa (depois de durar alguns minutos, para não avisar pico de segundos), quando piora e, se quiser, quando resolve.')],
    ['mudancas', T('Mudanças e segurança'), ''],
    ['resumos', T('Resumos'), T('Saem no horário abaixo, sempre de um período fechado: ontem, os últimos 7 dias, o mês anterior.')],
    ['ia', T('Análises com IA'), T('A IA lê os dados do painel (só leitura, com logs mascarados) e manda uma análise curta. Usa a sua chave da DeepSeek.')],
  ];
  const NT_STATUS = { sent: ['ok', T('Enviada')], partial: ['warn', T('Parcial')], failed: ['crit', T('Não saiu')], held: ['info', T('Segurada')], skipped: ['off', T('Ignorada')] };
  const notifyView = {
    mount(v) {
      v.innerHTML = `<div class="page"><div class="section-h"><div><h2>${icon('bell')} ${T('Notificações pelo WhatsApp')}</h2>
        <p>${T('Alertas, resumos e análises do servidor no seu WhatsApp. Tudo se configura aqui e salva sozinho.')}</p></div>
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
      if (!n.relay && n.installed && st.state !== 'open') warnings.push(T('O WhatsApp não está conectado: nada sai até conectar.'));
      if (!n.relay && !c.recipients.length) warnings.push(T('Ninguém em "Para quem enviar": nada sai até adicionar um número ou grupo.'));
      if (!c.enabled) warnings.push(T('As notificações estão desligadas.'));
      if (n.pending && warnings.length) warnings.push(`${T('{0} alerta(s) esperando para sair.', [n.pending])}`);
      body.innerHTML = `<div class="page">
        ${warnings.length ? `<div class="alert warn slim"><div class="ic">${icon('warn')}</div><div class="alert-body"><div class="alert-t">${warnings.map(esc).join(' ')}</div></div></div>` : ''}
        ${n.relay ? `<section class="card"><div class="card-h"><h2>${icon('link')}${T('Pelo WhatsApp do painel central')}</h2>
            <label class="sw-l"><input class="sw" type="checkbox" id="nt-on" ${c.enabled ? 'checked' : ''}><span>${c.enabled ? T('Ligadas') : T('Desligadas')}</span></label></div>
          <p class="muted" style="margin:0">${T('Os avisos deste servidor saem pelo WhatsApp do painel central <b>{0}</b>, para os destinos de lá (com o nome deste servidor no fim). Aqui você escolhe o que avisar e os horários.', [esc(n.relay)])} <a href="#/servers">${T('Conexão com o central →')}</a></p></section>` : ''}
        <div class="grid g2"${n.relay ? ' hidden' : ''}>
          <section class="card"><div class="card-h"><h2>${icon('whats')}${T('Conexão')}</h2></div><div id="wa-box">${waBoxHTML(n)}</div></section>
          <section class="card"><div class="card-h"><h2>${icon('users')}${T('Para quem enviar')}</h2>
            ${n.relay ? '' : `<label class="sw-l"><input class="sw" type="checkbox" id="nt-on" ${c.enabled ? 'checked' : ''}><span>${c.enabled ? T('Ligadas') : T('Desligadas')}</span></label>`}</div>
            <div id="nt-rcpts"></div>
            <form class="nt-add" id="nt-add" autocomplete="off">
              <div class="field"><label for="nt-name">${T('Nome (opcional)')}</label><input class="input" id="nt-name" maxlength="40" placeholder="${T('Ex.: Ana')}"></div>
              <div class="field"><label for="nt-num">${T('WhatsApp com DDI')}</label><input class="input" id="nt-num" inputmode="tel" placeholder="+55 11 91234-5678"></div>
              <button class="btn" type="submit">${icon('plus')}${T('Adicionar')}</button>
            </form>
            <div class="controls" style="margin-top:10px">
              ${st.state === 'open' && st.number ? `<button class="btn" type="button" id="nt-me">${icon('phone')}${T('Este número ({0})', [esc(fmtPhone(st.number))])}</button>` : ''}
              ${st.state === 'open' ? `<button class="btn" type="button" id="nt-groups">${icon('users')}${T('Escolher um grupo')}</button>` : ''}
            </div>
            <div id="nt-glist"></div></section>
        </div>
        <section class="card"><div class="card-h"><h2>${icon('bell')}${T('O que avisar')}</h2></div>
          ${NT_GROUPS.map(([g, title, desc]) => `<div class="nt-group"><h3>${title}</h3>${desc ? `<p class="muted">${esc(desc)}</p>` : ''}
            ${g === 'ia' && !S.ntAI ? `<div class="lock-note">${icon('lock')}<span>${T('Para usar as análises, configure a IA (chave da DeepSeek).')}</span>
              <button class="btn" type="button" data-act="settings" data-v="ia">${T('Configurar a IA')}</button></div>` : ''}
            <div class="opts">${kinds(g).map(opt).join('')}</div></div>`).join('')}
        </section>
        <section class="card"><div class="card-h"><h2>${icon('clock')}${T('Horários e limite')}</h2></div>
          <div class="nt-times">
            <div class="field"><label for="nt-daily">${T('Resumos e análises saem às')}</label><input class="input" type="time" id="nt-daily" value="${esc(c.dailyAt)}"></div>
            <div class="field"><label for="nt-max">${T('Máximo de mensagens por hora')}</label><input class="input nt-max" type="number" id="nt-max" min="5" max="500" value="${c.maxPerHour || 30}"></div>
            <div class="nt-quiet"><label class="sw-l"><input class="sw" type="checkbox" id="nt-quiet" ${c.quiet ? 'checked' : ''}><span>${T('Horário de silêncio')}</span></label>
              <div class="nt-range"><div class="field"><label for="nt-qf">${T('das')}</label><input class="input" type="time" id="nt-qf" value="${esc(c.quietFrom)}" ${c.quiet ? '' : 'disabled'}></div>
              <div class="field"><label for="nt-qt">${T('às')}</label><input class="input" type="time" id="nt-qt" value="${esc(c.quietTo)}" ${c.quiet ? '' : 'disabled'}></div></div></div>
          </div>
          <p class="muted" style="margin:10px 0 0;font-size:.8rem">${T('No silêncio, só o urgente (app caiu, servidor no limite, segurança) sai na hora; o resto chega numa mensagem só quando o silêncio acaba. Fuso do painel: {0} (<code>VPMON_TZ</code>).{1} O máximo por hora (padrão 30) vale para tudo que sai por este WhatsApp, inclusive os avisos dos servidores conectados; muito acima disso aumenta o risco de o WhatsApp bloquear o número.', [esc(n.tz), n.held ? ` ${T('Agora há {0} mensagem(ns) segurada(s).', [n.held])}` : ''])}</p>
        </section>
        <section class="card"><div class="card-h"><h2>${icon('send')}${T('Enviar agora')}</h2></div>
          <div class="controls">
            <button class="btn" type="button" data-send="test">${T('Mensagem de teste')}</button>
            <button class="btn" type="button" data-send="daily">${T('Resumo de ontem')}</button>
            <button class="btn" type="button" data-send="weekly">${T('Resumo da semana')}</button>
            <button class="btn" type="button" data-send="monthly">${T('Fechamento do mês')}</button>
            ${['ai_daily', 'ai_weekly', 'ai_logs'].map((k) => { const kk = n.catalog.find((x) => x.key === k); return `<button class="btn" type="button" data-send="${k}" ${S.ntAI ? '' : `disabled title="${T('Configure a IA para usar')}"`}>${icon(S.ntAI ? 'spark' : 'lock')}${esc(kk ? kk.label.replace(' no resumo diário', '') : k)}</button>`; }).join('')}
          </div>
          ${n.running.length ? `<p class="muted" style="margin:10px 0 0;font-size:.8rem"><span class="spinner inline"></span> ${T('A IA está montando {0} análise(s)…', [n.running.length])}</p>` : ''}
        </section>
        <section class="card"><div class="card-h"><h2>${icon('logs')}${T('Últimas mensagens')}</h2><button class="btn" type="button" id="nt-reload">${icon('refresh')}${T('Atualizar')}</button></div>
          <div class="nlog">${n.log.length ? n.log.map((e) => {
            const [lv, label] = NT_STATUS[e.status] || NT_STATUS.sent;
            return `<details class="nl"><summary><span class="nl-time">${dt(e.t)}</span>${badge(lv, label)}<span class="nl-title">${esc(e.title || e.kind)}</span>
              ${e.error ? `<span class="nl-err">${esc(e.error)}</span>` : ''}</summary>${e.text ? `<div class="wa-msg">${waText(e.text)}</div>` : ''}</details>`;
          }).join('') : `<div class="empty">${T('Nenhuma mensagem ainda.')}</div>`}</div></section>
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
        <div class="rcpt-n"><b>${esc(r.name || (r.id.endsWith('@g.us') ? T('Grupo') : T('Sem nome')))}</b><span class="muted">${esc(r.id.endsWith('@g.us') ? T('grupo do WhatsApp') : fmtPhone(r.id))}</span></div>
        <button class="icon-btn" type="button" data-rm="${i}" aria-label="${T('Remover {0}', [esc(r.name || r.id)])}" title="${T('Remover')}">${icon('x')}</button></div>`).join('')}</div>`
        : `<div class="empty" style="padding:12px 0">${T('Ninguém ainda. Adicione o seu WhatsApp abaixo.')}</div>`;
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
        if (t.id === 'nt-on') { S.nt.config.enabled = t.checked; t.nextElementSibling.textContent = t.checked ? T('Ligadas') : T('Desligadas'); notifyView.save(); return; }
        if (t.id === 'nt-quiet') { S.nt.config.quiet = t.checked; $('#nt-qf').disabled = $('#nt-qt').disabled = !t.checked; notifyView.save(); return; }
        if (t.id === 'nt-max') {
          const v = +t.value;
          if (v >= 5 && v <= 500) { S.nt.config.maxPerHour = v; notifyView.save(); } else { toast(T('Use de 5 a 500 mensagens por hora.')); t.value = S.nt.config.maxPerHour || 30; }
          return;
        }
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
        if (e.target.closest('#nt-me')) { notifyView.addRecipient(S.nt.status.number, T('Eu')); return; }
        if (e.target.closest('#nt-groups')) {
          const gl = $('#nt-glist');
          gl.innerHTML = '<div class="loading"><div class="spinner"></div></div>';
          try {
            const j = await api('/api/notify/groups');
            gl.innerHTML = j.groups.length ? `<div class="glist">${j.groups.sort((a, b) => a.subject.localeCompare(b.subject)).map((g) =>
              `<button class="chip-btn" type="button" data-group="${esc(g.id)}" data-name="${esc(g.subject)}">${esc(g.subject)} <span class="muted">· ${g.size}</span></button>`).join('')}</div>`
              : `<div class="empty" style="padding:12px 0">${T('Este número não participa de nenhum grupo.')}</div>`;
          } catch (ex) { gl.innerHTML = ex.message === 'login' ? '' : `<div class="form-err">${esc(ex.message)}</div>`; }
        }
      });
    },
    addRecipient(id, name) {
      if (S.nt.config.recipients.length >= 10) { toast(T('No máximo 10 destinos.')); return; }
      S.nt.config.recipients.push({ id: String(id), name: name || '' });
      notifyView.save(true);
    },
    // salva (com uma pausa curta para juntar cliques); now = já, e redesenha os destinos
    save(now) {
      clearTimeout(notifyView.t);
      const el = $('#nt-saved');
      if (el) el.textContent = T('Salvando…');
      notifyView.t = setTimeout(async () => {
        try {
          const j = await api('/api/notify/config', { method: 'POST', body: JSON.stringify({ ...S.nt.config, panelUrl: location.origin }) });
          S.nt.config = j.config;
          if ($('#nt-saved')) $('#nt-saved').textContent = `${T('Salvo às {0}', [hms(Date.now()).slice(0, 5)])}`;
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

  // ------------------------------------------------------------------ aba: servidores (painéis conectados)
  const FL_WA = { own: T('WhatsApp próprio'), central: T('WhatsApp do central'), off: T('Sem WhatsApp') };
  function srvMeter(label, p, text) {
    return `<div class="srv-m"><div class="srv-m-h"><span>${label}</span><b class="num">${text}</b></div>
      <div class="meter"><i class="${level(p, 75, 90)}" style="width:${Math.min(100, Math.max(0, p || 0)).toFixed(1)}%"></i></div></div>`;
  }
  // r = resumo do servidor (falta se ainda não conectou); t = token (falta = este painel)
  function srvCard(r, t) {
    const self = !t;
    const name = self ? (r.name || T('Este servidor')) : t.name;
    if (!self && !r) {
      return `<section class="card srv-card srv-wait"><div class="card-h"><h3>${icon('server')}${esc(name)}</h3>${badge('info', T('Aguardando conexão'))}</div>
        <p class="muted">${T('O token foi gerado {0}, mas o servidor ainda não mandou notícias. No painel dele: <b>Servidores → Conectar a um painel central</b>, com o endereço deste painel e o token.', [t.created ? `${T('em {0}', [dt(t.created)])}` : ''])}</p></section>`;
    }
    const silent = !self && !t.online;
    const health = silent ? badge('crit', T('Sem notícias'))
      : r.crit ? badge('crit', r.crit === 1 ? T('1 urgente') : `${T('{0} urgentes', [r.crit])}`)
        : r.warn ? badge('warn', r.warn === 1 ? T('1 alerta') : `${T('{0} alertas', [r.warn])}`) : badge('ok', T('Tudo certo'));
    const sub = [!self && r.name && r.name !== name ? r.name : '', r.where, r.os, r.uptime ? `${T('ligado há {0}', [dur(r.uptime)])}` : ''].filter(Boolean);
    const egP = r.egressLimit ? (r.egressMonth / r.egressLimit) * 100 : 0;
    return `<section class="card srv-card${silent ? ' srv-silent' : ''}${self ? ' srv-self' : ''}">
      <div class="card-h"><div class="srv-n"><h3>${icon(self ? 'grid' : 'server')}${esc(name)}${self ? ` <span class="muted srv-tag">${T('este painel')}</span>` : ''}</h3>
        ${sub.length ? `<div class="muted srv-sub">${esc(sub.join(' · '))}</div>` : ''}</div>${health}</div>
      ${silent ? `<div class="srv-down">${icon('crit')}<span>${T('Sem notícias desde {0} ({1}). Ele pode ter caído, perdido a internet ou ficado sem o painel.', [dt(t.lastSeen), ago(t.lastSeen)])}</span></div>` : ''}
      <div class="srv-meters${silent ? ' srv-old' : ''}">
        ${srvMeter('CPU', r.cpu, pct(r.cpu))}
        ${srvMeter(T('Memória'), r.memTotal ? (r.memUsed / r.memTotal) * 100 : 0, `${T('{0} de {1}', [bytes(r.memUsed), bytes(r.memTotal)])}`)}
        ${srvMeter(T('Disco'), r.diskTotal ? (r.diskUsed / r.diskTotal) * 100 : 0, `${T('{0} de {1}', [bytes(r.diskUsed), bytes(r.diskTotal)])}`)}
      </div>
      <div class="srv-facts">
        <span>${icon('box')}${r.appsTotal ? `${T('{0} de {1} apps no ar', [r.appsUp, r.appsTotal])}` : T('Nenhuma app')}</span>
        <span title="${T('Saída para a internet neste mês')}">${icon('up')}${T('{0} no mês{1}', [data(r.egressMonth), egP >= 75 ? ` ${T('({0} do limite)', [pct(egP)])}` : ''])}</span>
        <span>${icon('whats')}${FL_WA[r.whatsapp] || FL_WA.off}</span>
      </div>
      ${(r.top || []).length ? `<ul class="srv-alerts">${r.top.map((a) => `<li>${esc(a)}</li>`).join('')}</ul>`
        : silent ? '' : `<div class="muted srv-none">${icon('ok')}${T('Nenhum alerta.')}</div>`}
      <div class="srv-foot"><span class="muted">${self ? `${T('versão {0}', [esc(r.version || '?')])}` : `${T('versão {0} · notícia {1}', [esc(r.version || '?'), ago(t.lastSeen)])}`}
          ${!self ? ` · ${t.control ? T('controle total') : t.viewable ? T('dá para ver aqui') : T('não compartilha a tela')}` : ''}</span>
        <span class="controls">${!self && t.viewable && !(S.remote && S.remote.id === t.id) ? `<button class="btn sm primary" type="button" data-act="pick-server" data-v="${esc(t.id)}">${icon('eye')}${T('Ver aqui')}</button>` : ''}
        ${self && S.remote ? `<button class="btn sm" type="button" data-act="pick-server" data-v="">${icon('eye')}${T('Ver aqui')}</button>` : ''}
        ${!self && r.panelUrl ? `<a class="btn sm" href="${esc(r.panelUrl)}" target="_blank" rel="noopener noreferrer">${icon('ext')}${T('Abrir painel')}</a>` : ''}</span></div>
    </section>`;
  }
  const SHARE_RISK = {
    view: [T('Deixar o painel central ver este servidor?'), T('Mostrar no central'),
      `<p>${T('Quem entra no painel central passa a ver, na tela de lá, a visão geral, as apps, a banda, o sistema e a limpeza deste servidor (só leitura).')}</p><p>${T('Os logs e as ações continuam fechados até você liberar. Dá para desligar quando quiser; vale na hora.')}</p>`],
    logs: [T('Incluir os logs?'), T('Incluir os logs'),
      `<p>${T('Quem entra no painel central passa a ler os logs das apps deste servidor. <b>Log pode ter dado sensível</b> (tokens em URL, e-mails, erros com dados).')}</p>`],
    control: [T('Liberar o controle total?'), T('Liberar o controle total'),
      `<p>${T('Quem entra no painel central <b>com as permissões de lá</b> (Ações nas apps, Limpar o disco) passa a <b>pausar e retomar apps e limpar o disco deste servidor</b>, além de ver tudo, inclusive os logs.')}</p><p>${T('Se o painel central for invadido, essas ações ficam expostas também aqui. Usuários, senhas, IA, WhatsApp e esta conexão continuam só neste painel. Cada ação fica registrada como "fulano (pelo painel central)".')}</p>`],
  };
  const serversView = {
    mount(v) {
      v.innerHTML = `<div class="page"><div class="section-h"><div><h2>${icon('layers')} ${T('Servidores')}</h2>
        <p>${T('Este painel e os servidores conectados a ele, num lugar só. Cada um manda um resumo por minuto; quem para de mandar vira alerta aqui.')}</p></div></div>
        <div id="fl-body"><div class="loading"><div class="spinner"></div></div></div></div>`;
      serversView.at = 0;
      serversView.secret = '';
      serversView.editing = null;
      serversView.listen($('#fl-body'));
      serversView.load();
    },
    update() { if (Date.now() - serversView.at > 15000) serversView.load(); },
    // o que o painel central pode ver e fazer neste servidor
    async share(input) {
      const cl = S.flAdmin.client;
      const next = { view: cl.shareView, logs: cl.shareLogs, control: cl.shareControl };
      next[input.dataset.share] = input.checked;
      if (!next.view) next.logs = next.control = false;
      if (input.checked) {
        const [title, ok, body] = SHARE_RISK[input.dataset.share];
        if (!await confirmDialog({ title, ok, body, danger: input.dataset.share === 'control' })) { input.checked = false; return; }
      }
      try {
        const j = await api('/api/fleet/share', { method: 'POST', body: JSON.stringify(next) });
        S.flAdmin.client = j.client;
        toast(j.client.shareControl ? T('O painel central tem controle total deste servidor.') : j.client.shareView
          ? `${T('O painel central pode ver este servidor{0}.', [j.client.shareLogs ? T(', com os logs') : ''])}` : T('O painel central não vê mais este servidor.'));
      } catch (ex) { if (ex.message !== 'login') toast(ex.message); }
      serversView.load();
    },
    async load() {
      serversView.at = Date.now();
      try {
        const j = await api('/api/fleet/servers');
        S.fl = j;
        S.fleet = j; // o cabeçalho e a Visão geral usam a mesma lista
        S.fleetAt = Date.now();
        renderPill();
        if (can.admin()) S.flAdmin = await api('/api/fleet');
        serversView.render();
      } catch (e) {
        if (e.message !== 'login' && $('#fl-body')) $('#fl-body').innerHTML = `<div class="card empty">${esc(e.message)}</div>`;
      }
    },
    render() {
      const body = $('#fl-body');
      if (!body || !S.fl) return;
      if (body.contains(document.activeElement) && document.activeElement.matches('input')) return; // não apaga o que a pessoa digita
      const j = S.fl, cl = j.client || {};
      const others = j.servers || [];
      const silent = others.filter((t) => t.lastSeen && !t.online).length;
      body.innerHTML = `
        ${cl.connected ? `<div class="alert ${cl.ok ? 'info' : 'warn'} slim"><div class="ic">${icon(cl.ok ? 'link' : 'warn')}</div><div class="alert-body"><div class="alert-t">
          ${cl.ok ? `${T('Este servidor manda notícias para o painel central <b>{0}</b>.', [esc(cl.central || cl.url)])}` : `${T('Este servidor não conseguiu falar com o painel central: {0}.', [esc(cl.error || T('sem resposta'))])}`}
          <a href="${esc(cl.url)}" target="_blank" rel="noopener noreferrer">${T('Abrir o central')}</a></div></div></div>` : ''}
        ${others.length ? `<p class="muted fl-count">${T('{0} servidor(es) conectado(s) a este painel{1}.', [others.length, silent ? ` ${T('· <b class="crit-t">{0} sem notícias</b>', [silent])}` : ''])}</p>` : ''}
        <div class="srv-grid">${srvCard(j.self)}${others.map((t) => srvCard(t.report, t)).join('')}</div>
        ${!others.length && !cl.connected ? `<div class="note">${T('<b>Tem outros servidores com o VPServer?</b> Conecte-os a este painel para ver todos aqui e, se quiser, mandar os avisos deles pelo WhatsApp daqui. {0}', [can.admin() ? T('Gere um token logo abaixo e cole no painel do outro servidor.') : T('Peça a um administrador.')])}</div>` : ''}
        ${can.admin() && S.flAdmin ? serversView.adminHTML() : ''}`;
    },
    adminHTML() {
      const a = S.flAdmin, cl = a.client || {};
      const tokens = a.tokens || [];
      const tokRow = (t) => `<div class="urow" data-tok="${esc(t.id)}" data-name="${esc(t.name)}"><div class="urow-h">
        <div class="urow-n"><span><b>${esc(t.name)}</b> ${t.lastSeen ? (t.online ? badge('ok', T('conectado')) : badge('crit', T('sem notícias'))) : badge('info', T('nunca conectou'))}</span>
          <small>${T('{0}criado {1}{2}{3}{4}', [t.whatsapp ? `${T('WhatsApp daqui: {0} de {1} na última hora ·', [t.relayed, t.relayLimit])} ` : T('sem o WhatsApp daqui · '), t.created ? `${T('em {0}', [dt(t.created)])}` : '', t.createdBy ? ` ${T('por {0}', [esc(t.createdBy)])}` : '', t.lastSeen ? ` ${T('· última notícia {0}', [ago(t.lastSeen)])}` : '', t.lastIp ? ` · IP ${esc(t.lastIp)}` : ''])}</small></div>
        <div class="controls"><button class="btn sm" type="button" data-fl="edit">${T('Ajustar')}</button><button class="btn sm" type="button" data-fl="revoke">${T('Revogar')}</button></div></div>
        <div class="urow-edit" ${serversView.editing === t.id ? '' : 'hidden'}>
          <label class="perm"><input type="checkbox" class="sw" data-tw ${t.whatsapp ? 'checked' : ''}><span>${T('<b>Pode usar o WhatsApp deste painel</b>')}
            <small>${T('Os avisos dele saem pelo WhatsApp daqui, só para os destinos daqui.')}</small></span></label>
          <div class="field fl-limit"><label>${T('Limite de avisos dele por hora')}</label>
            <div class="cl-inline"><input class="input" type="number" data-tl min="1" max="500" value="${t.relayLimit}"><span>${T('por hora')}</span></div>
            <small class="muted">${T('Também conta no limite geral do WhatsApp daqui (Notificações). Muito acima de 30 por hora aumenta o risco de o WhatsApp bloquear o número.')}</small></div>
          <div class="controls"><button class="btn primary sm" type="button" data-fl="save">${T('Salvar')}</button></div></div></div>`;
      return `<div class="grid g2 fl-admin">
        <section class="card"><div class="card-h"><h2>${icon('key')}${T('Conectados a este painel')}</h2></div>
          <p class="muted fl-hint">${T('Um token para cada servidor que vai mandar notícias para cá. No painel do outro servidor, em <b>Servidores → Conectar a um painel central</b>, use o endereço <code>{0}</code> e o token.', [esc(location.origin)])}</p>
          <div id="fl-new-secret">${serversView.secret || ''}</div>
          ${tokens.length ? `<div class="ulist">${tokens.map(tokRow).join('')}</div>` : ''}
          <form class="stack unew" id="fl-new" autocomplete="off"><h3>${icon('plus')}${T('Gerar token')}</h3>
            <div class="field"><label for="fl-name">${T('Nome do servidor')}</label><input class="input" id="fl-name" maxlength="40" required placeholder="${T('ex.: loja')}"></div>
            <label class="perm"><input type="checkbox" class="sw" id="fl-wa"><span>${T('<b>Pode usar o WhatsApp deste painel</b>')}
              <small>${T('Os avisos dele saem pelo WhatsApp daqui, só para os destinos daqui (com o nome do servidor no fim), até o limite abaixo. Dá para mudar depois em "Ajustar".')}</small></span></label>
            <div class="field fl-limit"><label for="fl-limit">${T('Limite de avisos dele por hora')}</label>
              <div class="cl-inline"><input class="input" type="number" id="fl-limit" min="1" max="500" value="30"><span>${T('por hora')}</span></div></div>
            <div class="form-err" id="fl-err" role="alert"></div>
            <button class="btn primary" type="submit">${T('Gerar token')}</button></form></section>
        <section class="card"><div class="card-h"><h2>${icon('link')}${T('Este servidor num painel central')}</h2></div>
          ${cl.connected ? `
            <div class="fl-st"><div>${T('{0} ao painel <b>{1}</b>', [cl.ok ? badge('ok', T('Conectado')) : badge('crit', T('Com erro')), esc(cl.central || '')])}</div>
              <div class="muted"><a href="${esc(cl.url)}" target="_blank" rel="noopener noreferrer">${esc(cl.url)}</a> ${T('· desde {0}{1}', [dt(cl.since), cl.lastAt ? ` ${T('· último resumo {0}', [ago(cl.lastAt)])}` : ''])}</div>
              ${cl.ok ? '' : `<div class="form-err">${esc(cl.error || '')}</div>`}</div>
            <label class="perm${cl.canWhatsApp ? '' : ' locked'}"><input type="checkbox" class="sw" id="fl-usewa" ${cl.useWhatsApp ? 'checked' : ''} ${cl.canWhatsApp ? '' : 'disabled'}>
              <span>${T('<b>Mandar os avisos daqui pelo WhatsApp do central</b>')}
              <small>${cl.canWhatsApp ? T('O que avisar e os horários continuam na aba Notificações daqui; os destinos são os do central.')
                : T('O token deste servidor não pode usar o WhatsApp do central. Para isso, gere outro lá com essa opção marcada e conecte de novo.')}</small></span></label>
            ${cl.useWhatsApp && !cl.centralWhatsApp ? `<div class="form-err">${T('O WhatsApp do central não está pronto (desconectado, notificações desligadas ou sem destinos): os avisos daqui não saem por ele até resolver lá.')}</div>` : ''}
            <div class="fl-share"><h3>${icon('eye')}${T('O que o painel central pode ver e fazer aqui')}</h3>
              <label class="perm"><input type="checkbox" class="sw" data-share="view" ${cl.shareView ? 'checked' : ''}>
                <span>${T('<b>Deixar o central ver este servidor</b>')}<small>${T('Visão geral, apps, banda, sistema e limpeza, na tela do central, só leitura. {0}', [cl.shareView ? (cl.listening ? T('Conectado agora.') : T('Conectando…')) : ''])}</small></span></label>
              <label class="perm${cl.shareView ? '' : ' locked'}"><input type="checkbox" class="sw" data-share="logs" ${cl.shareLogs ? 'checked' : ''} ${cl.shareView && !cl.shareControl ? '' : 'disabled'}>
                <span>${T('<b>Incluir os logs</b>')}<small>${T('O que as apps escrevem no log (pode ter dado sensível).')}</small></span></label>
              <label class="perm${cl.shareView ? '' : ' locked'}"><input type="checkbox" class="sw" data-share="control" ${cl.shareControl ? 'checked' : ''} ${cl.shareView ? '' : 'disabled'}>
                <span>${T('<b>Controle total</b>')}<small>${T('Quem tem as permissões no central também pausa/retoma apps e limpa o disco daqui (inclui os logs). Usuários, senhas, IA, WhatsApp e esta conexão continuam só aqui.')}</small></span></label>
            </div>
            <div class="controls"><button class="btn" type="button" data-fl="disconnect">${T('Desconectar')}</button></div>`
          : `<p class="muted fl-hint">${T('Para ver este servidor no painel central de outro servidor: gere um token lá (Servidores → Gerar token) e cole aqui. Este painel passa a mandar um resumo por minuto (CPU, memória, disco, apps, alertas e banda). O central não consegue mexer em nada aqui.')}</p>
            <form class="stack" id="fl-connect" autocomplete="off">
              <div class="field"><label for="fl-url">${T('Endereço do painel central')}</label><input class="input" id="fl-url" required placeholder="${T('https://painel.exemplo.com')}" autocapitalize="off" spellcheck="false"></div>
              <div class="field"><label for="fl-tok">${T('Token')}</label>${secretInput('id="fl-tok" required placeholder="vps_…" autocapitalize="off" spellcheck="false" autocomplete="off"', T('o token'))}</div>
              <div class="form-err" id="fl-cerr" role="alert"></div>
              <button class="btn primary" type="submit">${icon('link')}${T('Conectar')}</button></form>`}
        </section></div>`;
    },
    listen(body) {
      body.addEventListener('submit', async (e) => {
        e.preventDefault();
        const f = e.target;
        const btn = $('button[type="submit"]', f);
        btn.disabled = true;
        try {
          if (f.id === 'fl-new') {
            const name = $('#fl-name').value.trim();
            const r = await api('/api/fleet/tokens', { method: 'POST', body: JSON.stringify({ name, whatsapp: $('#fl-wa').checked, relayLimit: +$('#fl-limit').value || 0 }) });
            document.activeElement.blur();
            await serversView.load();
            serversView.secret = `<div class="passbox" role="status"><div>${T('<b>Token de {0}</b>', [esc(r.token.name)])}</div>
              <div class="passline">${secretOut(r.secret, T('o token'))}</div>
              <div class="muted">${T('Copie agora: ele não aparece de novo. No painel de {0}: Servidores → Conectar a um painel central, com o endereço <code>{1}</code> e este token.', [esc(r.token.name), esc(location.origin)])}</div></div>`;
            serversView.render();
          } else if (f.id === 'fl-connect') {
            const r = await api('/api/fleet/connect', { method: 'POST', body: JSON.stringify({ url: $('#fl-url').value, token: $('#fl-tok').value }) });
            document.activeElement.blur();
            toast(`${T('Conectado ao painel central {0}.', [r.client.central || r.client.url])}`);
            serversView.load();
          }
        } catch (ex) {
          if (ex.message !== 'login') $(f.id === 'fl-new' ? '#fl-err' : '#fl-cerr').textContent = ex.message;
        }
        btn.disabled = false;
      });
      body.addEventListener('change', async (e) => {
        if (e.target.dataset.share) { serversView.share(e.target); return; }
        if (e.target.id !== 'fl-usewa') return;
        const on = e.target.checked;
        if (on && !await confirmDialog({ title: T('Mandar os avisos pelo WhatsApp do central?'), ok: T('Usar o do central'),
          body: `<p>${T('Os avisos deste servidor passam a sair pelo WhatsApp do painel central, para os destinos de lá (não os daqui), com o nome do servidor no fim.')}</p>
            <p>${T('O WhatsApp deste servidor, se houver, deixa de ser usado enquanto isso estiver ligado. Se o central sair do ar, os avisos daqui não chegam (o central acusa o silêncio do mesmo jeito).')}</p>` })) { e.target.checked = false; return; }
        try {
          await api('/api/fleet/whatsapp', { method: 'POST', body: JSON.stringify({ on }) });
          toast(on ? T('Os avisos daqui saem pelo WhatsApp do central.') : T('Os avisos daqui voltam a usar o WhatsApp deste servidor.'));
        } catch (ex) { if (ex.message !== 'login') toast(ex.message); }
        serversView.load();
      });
      body.addEventListener('click', async (e) => {
        const b = e.target.closest('[data-fl]');
        if (!b) return;
        try {
          if (b.dataset.fl === 'edit') {
            const id = b.closest('[data-tok]').dataset.tok;
            serversView.editing = serversView.editing === id ? null : id;
            serversView.render();
            return;
          }
          if (b.dataset.fl === 'save') {
            const row = b.closest('[data-tok]');
            const tok = (S.flAdmin.tokens || []).find((x) => x.id === row.dataset.tok) || {};
            const whatsapp = $('[data-tw]', row).checked, limit = +$('[data-tl]', row).value;
            if (!(limit >= 1 && limit <= 500)) { toast(T('O limite vai de 1 a 500 por hora.')); return; }
            if (whatsapp && !tok.whatsapp && !await confirmDialog({ title: `${T('{0} pode usar o WhatsApp daqui?', [row.dataset.name])}`, ok: T('Liberar'),
              body: `<p>${T('Os avisos de {0} passam a sair pelo WhatsApp deste painel, <b>para os destinos daqui</b>, até {1} por hora (com o nome do servidor no fim).', [esc(row.dataset.name), limit])}</p>
                <p>${T('Ele precisa ligar "Mandar os avisos daqui pelo WhatsApp do central" no painel dele. Vale em até 1 minuto, sem trocar o token.')}</p>` })) return;
            const j = await api('/api/fleet/tokens/update', { method: 'POST', body: JSON.stringify({ id: row.dataset.tok, whatsapp, relayLimit: limit }) });
            serversView.editing = null;
            toast(j.token.whatsapp ? `${T('{0}: até {1} avisos por hora pelo WhatsApp daqui.', [j.token.name, j.token.relayLimit])}` : `${T('{0} não usa mais o WhatsApp daqui.', [j.token.name])}`);
            serversView.load();
            return;
          }
          if (b.dataset.fl === 'revoke') {
            const row = b.closest('[data-tok]');
            const name = row.dataset.name;
            if (!await confirmDialog({ title: `${T('Revogar o token de {0}?', [name])}`, ok: T('Revogar'), danger: true,
              body: `<p>${T('{0} para na hora de mandar notícias e avisos para cá: o card some e não há alerta de silêncio.', [esc(name)])}</p>
                <p>${T('Nada muda no servidor {0} em si. Para conectar de novo, gere outro token.', [esc(name)])}</p>` })) return;
            await api('/api/fleet/tokens/revoke', { method: 'POST', body: JSON.stringify({ id: row.dataset.tok }) });
            toast(`${T('Token de {0} revogado.', [name])}`);
          } else if (b.dataset.fl === 'disconnect') {
            if (!await confirmDialog({ title: T('Desconectar do painel central?'), ok: T('Desconectar'), danger: true,
              body: `<p>${T('Este servidor para de mandar o resumo{0}. O central fica sabendo e mostra o card como "aguardando conexão", sem alerta.', [S.flAdmin.client.useWhatsApp ? T(' e de usar o WhatsApp do central para os avisos (voltam a depender do WhatsApp daqui)') : ''])}</p><p>${T('O token continua valendo lá até alguém revogar.')}</p>` })) return;
            await api('/api/fleet/disconnect', { method: 'POST', body: '{}' });
            toast(T('Desconectado do painel central.'));
          }
          serversView.load();
        } catch (ex) { if (ex.message !== 'login') toast(ex.message); }
      });
    },
  };

  // ------------------------------------------------------------------ aba: limpeza do disco
  const CL = {
    build_cache: { name: T('Cache de build do Docker'), icon: 'box',
      desc: T('Sobras dos builds de imagens (camadas intermediárias) que nenhum build está usando.'),
      cons: T('O próximo build de cada app demora mais, porque o Docker refaz o cache. Nada que está rodando muda.') },
    dangling: { name: T('Imagens sem nome'), icon: 'layers',
      desc: `${T('Versões antigas que ficaram sem nome (')}<none>${T(') depois de builds e atualizações. Nenhum contêiner usa: o Docker não apaga imagem em uso, nem de contêiner parado.')}`,
      cons: T('Não dá mais para voltar a essas versões antigas sem baixar ou buildar de novo. As apps no ar continuam iguais.') },
    logs: { name: T('Logs dos contêineres'), icon: 'logs',
      desc: T('O que as apps escreveram no log (o que aparece na aba Logs e no docker logs). Escolha de quais apps.'),
      cons: T('O histórico de logs das apps escolhidas some. Elas continuam rodando e o que escreverem daqui para frente continua sendo guardado.') },
  };
  const clPct = (d) => (d.fsTotal ? (d.fsUsed / d.fsTotal) * 100 : 0);
  // logs agrupados por app: [{app, name, size, ids, cts}]
  function clLogApps(d) {
    const by = new Map();
    for (const l of d.logs) {
      const g = by.get(l.app) || { app: l.app, name: l.appName || l.app, size: 0, ids: [], cts: [] };
      g.size += l.size;
      g.ids.push(l.id);
      g.cts.push(l.name);
      by.set(l.app, g);
    }
    return [...by.values()].sort((a, b) => b.size - a.size);
  }
  const cleanupView = {
    mount(v) {
      v.innerHTML = `<div class="page"><div class="section-h"><div><h2>${icon('broom')} ${T('Limpeza do disco')}</h2>
        <p>${T('Libera espaço só com o que não afeta as aplicações. Vale para o servidor todo (o Docker é um só), inclusive apps que não são deste painel.')}</p></div></div>
        <div id="cl-body"><div class="loading"><div class="spinner"></div></div></div></div>`;
      cleanupView.sel = { build: false, dangling: false, apps: new Set() };
      cleanupView.listen($('#cl-body'));
      cleanupView.load();
    },
    async load() {
      clearTimeout(cleanupView.t);
      try {
        const j = await api('/api/cleanup');
        const was = S.cl && S.cl.running;
        S.cl = j.cleanup;
        S.canClean = j.canClean;
        if (was && !S.cl.running && S.cl.runs[0]) {
          const r = S.cl.runs[0];
          toast(`${T('Limpeza concluída: {0} liberados.', [bytes(r.freed)])}`);
          cleanupView.sel = { build: false, dangling: false, apps: new Set() };
        }
        cleanupView.render();
        if (S.cl.running) cleanupView.t = setTimeout(() => { if (S.tab === 'cleanup') cleanupView.load(); }, 2000);
      } catch (e) {
        if (e.message !== 'login' && $('#cl-body')) $('#cl-body').innerHTML = `<div class="card empty">${esc(e.message)}</div>`;
      }
    },
    // o que está marcado: {build, dangling, logs: [ids], size, apps: [nomes]}
    picked() {
      const c = S.cl, d = c.disk, sel = cleanupView.sel;
      const apps = c.logsHelper ? clLogApps(d).filter((g) => sel.apps.has(g.app)) : [];
      return {
        build: sel.build && d.buildCache > 0, dangling: sel.dangling && d.danglingCount > 0,
        logs: apps.flatMap((g) => g.ids), appNames: apps.map((g) => g.name),
        size: (sel.build ? d.buildCache : 0) + (sel.dangling ? d.danglingSize : 0) + apps.reduce((s, g) => s + g.size, 0),
      };
    },
    render() {
      const body = $('#cl-body');
      if (!body || !S.cl) return;
      if (body.contains(document.activeElement) && document.activeElement.matches('input[type="number"]')) return;
      const c = S.cl, d = c.disk, sel = cleanupView.sel, can = S.canClean && !c.running;
      const p = clPct(d);
      const apps = clLogApps(d);
      const logsTotal = apps.reduce((s, g) => s + g.size, 0);
      const freeable = d.buildCache + d.danglingSize + logsTotal;
      const item = (key, checked, size, extra, disabledWhy, sub) => {
        const it = CL[key];
        const off = !can || !!disabledWhy;
        return `<div class="cl-item${checked && !off ? ' on' : ''}"><label class="cl-head">
          <input type="checkbox" class="sw" data-cl="${key}" ${checked && !off ? 'checked' : ''} ${off ? 'disabled' : ''}>
          <span class="cl-t">${icon(it.icon)}<b>${it.name}</b></span><span class="cl-size"><b class="num">${size}</b>${sub ? `<small>${sub}</small>` : ''}</span></label>
          <p class="muted cl-d">${esc(it.desc)}</p>
          <p class="cl-cons">${icon('warn')}<span>${esc(it.cons)}</span></p>
          ${disabledWhy ? `<p class="muted cl-why">${esc(disabledWhy)}</p>` : ''}${extra || ''}</div>`;
      };
      const logsList = apps.length ? `<div class="cl-apps">${apps.map((g) => `<label class="cl-app">
          <input type="checkbox" data-clapp="${esc(g.app)}" ${sel.apps.has(g.app) ? 'checked' : ''} ${!can || !c.logsHelper ? 'disabled' : ''}>
          <span class="cl-app-n"><b>${esc(g.name)}</b><small class="muted">${esc(g.cts.join(', '))}</small></span><span class="num">${bytes(g.size)}</span></label>`).join('')}</div>` : '';
      const pk = cleanupView.picked();
      const runs = c.runs || [];
      body.innerHTML = `
        ${c.running ? `<div class="alert info slim"><div class="ic"><span class="spinner inline"></span></div><div class="alert-body"><div class="alert-t">${T('Limpando… pode levar alguns minutos. A tela atualiza sozinha.')}</div></div></div>` : ''}
        ${!S.canClean ? `<div class="alert info slim"><div class="ic">${icon('lock')}</div><div class="alert-body"><div class="alert-t">${T('Você pode ver, mas limpar exige a permissão <b>Limpar o disco</b>. Peça a um administrador.')}</div></div></div>` : ''}
        <section class="card cl-hero"><div class="card-h"><h2>${icon('disk')}${T('Disco do servidor')}</h2>${badge(p >= 90 ? 'crit' : p >= 80 ? 'warn' : 'ok', pct(p))}</div>
          <div class="hero num">${bytes(d.fsUsed)} <span class="muted" style="font-size:1rem;font-weight:500">${T('de {0}', [bytes(d.fsTotal)])}</span></div>
          <div class="meter thick" style="margin:10px 0 8px"><i class="${level(p, 80, 90)}" style="width:${Math.min(100, p).toFixed(1)}%"></i></div>
          <div class="muted" style="font-size:.86rem">${T('Dá para liberar com segurança: <b class="ink2">{0}</b>{1}', [bytes(freeable), d.measured ? ` ${T('· Docker medido às {0}', [hms(d.measured * 1000).slice(0, 5)])}` : T(' · medindo o Docker…')])}</div></section>
        <section class="card"><div class="card-h"><h2>${icon('broom')}${T('O que dá para limpar')}</h2></div>
          <div class="cl-items">
            ${item('build_cache', sel.build, bytes(d.buildCache), '', d.buildCache ? '' : T('Nada para limpar agora.'))}
            ${item('dangling', sel.dangling, bytes(d.danglingSize), '', d.danglingCount ? '' : T('Nenhuma imagem sem nome agora.'),
              d.danglingCount ? (d.danglingCount === 1 ? T('1 imagem') : `${T('{0} imagens', [d.danglingCount])}`) : '')}
            ${item('logs', sel.apps.size > 0, bytes(logsTotal), logsList,
              !c.logsHelper ? T('O ajudante que limpa os logs (vpserver-cleaner) não está rodando. Ele sobe junto com o painel a partir desta versão: no servidor, docker compose up -d.')
                : !d.logsKnown ? T('O tamanho dos logs ainda não foi medido (o vpserver-sizer mede a cada 5 min).') : apps.length ? '' : T('Nenhum log para limpar.'))}
          </div>
          <div class="cl-go"><span class="muted">${pk.size ? `${T('Selecionado: <b class="ink2">{0}</b>', [bytes(pk.size)])}` : T('Marque o que quer limpar.')}</span>
            <button class="btn primary" type="button" data-cla="run" ${can && pk.size ? '' : 'disabled'}>${icon('broom')}${T('Limpar selecionados')}</button></div>
        </section>
        <div class="grid g2">
          ${cleanupView.autoHTML(c)}
          <section class="card"><div class="card-h"><h2>${icon('shield')}${T('Nunca é limpo')}</h2></div>
            <ul class="cl-never">
              <li>${T('<b>Volumes</b>: os dados das apps (bancos, uploads, sessões).')}</li>
              <li>${T('<b>Contêineres</b>, nem os parados ou pausados.')}</li>
              <li>${T('<b>Imagens com nome ou em uso</b>, nem as de contêiner parado.')}</li>
              <li>${T('<b>Redes</b> do Docker.')}</li>
              <li>${T('Arquivos fora do Docker (sistema, /var/log, pastas das apps): ficam com você no servidor.')}</li>
            </ul>
            <p class="muted" style="margin:8px 0 0;font-size:.82rem">${T('Por isso nenhuma app para ou perde dados com a limpeza.')}</p></section>
        </div>
        <section class="card"><div class="card-h"><h2>${icon('clock')}${T('Histórico')}</h2></div>
          ${runs.length ? `<div class="nlog">${runs.map((r) => `<details class="nl"><summary><span class="nl-time">${dt(r.t)}</span>
            ${badge(r.steps.some((s) => s.error) ? 'warn' : 'ok', r.by ? r.by : T('automática'))}
            <span class="nl-title">${T('{0} liberados · disco {1} → {2}', [bytes(r.freed), pct(r.before), pct(r.after)])}</span></summary>
            <div class="cl-steps">${r.note ? `<p class="muted">${esc(r.note)}</p>` : ''}${r.steps.map((s) => `<div>${esc(CL[s.item] ? CL[s.item].name : s.item)}${s.names && s.names.length ? ` (${esc(s.names.join(', '))})` : ''}:
              ${s.error ? `<span class="nl-err">${T('falhou: {0}', [esc(s.error)])}</span>` : `<b>${bytes(s.freed)}</b>${s.item === 'dangling' ? ` ${T('· {0} imagem(ns)', [s.removed])}` : ''}`}</div>`).join('')}</div></details>`).join('')}</div>`
            : `<div class="empty">${T('Nenhuma limpeza ainda.')}</div>`}</section>`;
    },
    autoHTML(c) {
      const a = c.auto, can = S.canClean;
      const dis = can ? '' : 'disabled';
      return `<section class="card"><div class="card-h"><h2>${icon('refresh')}${T('Limpeza automática')}</h2>
          <label class="sw-l"><input class="sw" type="checkbox" id="cl-auto-on" ${a.enabled ? 'checked' : ''} ${dis}><span>${a.enabled ? T('Ligada') : T('Desligada')}</span></label></div>
        <form class="stack" id="cl-auto" autocomplete="off">
          <div class="field cl-thr"><label for="cl-thr">${T('Limpar quando o disco passar de')}</label>
            <div class="cl-inline"><input class="input" type="number" id="cl-thr" min="50" max="98" value="${a.threshold}" ${dis}><span>%</span></div></div>
          <div class="cl-checks">
            <label><input type="checkbox" id="cl-a-build" ${a.buildCache ? 'checked' : ''} ${dis}> ${T('Cache de build')}</label>
            <label><input type="checkbox" id="cl-a-dang" ${a.dangling ? 'checked' : ''} ${dis}> ${T('Imagens sem nome')}</label>
            <label><input type="checkbox" id="cl-a-logs" ${a.logs ? 'checked' : ''} ${dis}> ${T('Logs maiores que')}
              <input class="input cl-mb" type="number" id="cl-a-mb" min="10" value="${a.logsOverMb}" ${dis}> ${T('MB (por contêiner)')}</label>
          </div>
          <p class="muted" style="margin:0;font-size:.8rem">${T('No máximo uma vez a cada 6 h. Cada limpeza fica no histórico e avisa pelo WhatsApp (tipo "Limpeza do disco" em Notificações).{0}', [c.lastAuto ? ` ${T('Última automática: {0}.', [dt(c.lastAuto)])}` : ''])}</p>
          <div class="form-err" id="cl-auto-err" role="alert"></div>
          ${can ? `<div><button class="btn" type="submit">${T('Salvar')}</button></div>` : ''}
        </form></section>`;
    },
    listen(body) {
      body.addEventListener('change', (e) => {
        const t = e.target, sel = cleanupView.sel;
        if (t.dataset.cl === 'build_cache') sel.build = t.checked;
        else if (t.dataset.cl === 'dangling') sel.dangling = t.checked;
        else if (t.dataset.cl === 'logs') { // marca/desmarca todas as apps
          sel.apps = new Set(t.checked ? clLogApps(S.cl.disk).map((g) => g.app) : []);
        } else if (t.dataset.clapp) {
          if (t.checked) sel.apps.add(t.dataset.clapp); else sel.apps.delete(t.dataset.clapp);
        } else if (t.id === 'cl-auto-on') {
          cleanupView.saveAuto(t.checked);
          return;
        } else return;
        cleanupView.render();
      });
      body.addEventListener('submit', (e) => {
        if (e.target.id !== 'cl-auto') return;
        e.preventDefault();
        cleanupView.saveAuto($('#cl-auto-on').checked);
      });
      body.addEventListener('click', async (e) => {
        if (!e.target.closest('[data-cla="run"]')) return;
        const pk = cleanupView.picked();
        if (!pk.size) return;
        const lines = [];
        if (pk.build) lines.push(['build_cache', CL.build_cache.name]);
        if (pk.dangling) lines.push(['dangling', CL.dangling.name]);
        if (pk.logs.length) lines.push(['logs', `${T('Logs de {0}', [pk.appNames.join(', ')])}`]);
        const ok = await confirmDialog({ title: `${T('Limpar {0} do disco?', [bytes(pk.size)])}`, ok: T('Limpar'), danger: true,
          body: `<p>${T('Não dá para desfazer. O que acontece:')}</p><ul class="cl-confirm">${lines.map(([k, t]) => `<li><b>${esc(t)}</b>: ${esc(CL[k].cons)}</li>`).join('')}</ul>
            <p>${T('Nenhuma app para: volumes, contêineres, redes e imagens em uso não são tocados. Vale para o servidor todo.')}</p>` });
        if (!ok) return;
        try {
          await api('/api/cleanup/run', { method: 'POST', body: JSON.stringify({ buildCache: pk.build, dangling: pk.dangling, logs: pk.logs, confirm: true }) });
          toast(T('Limpando… pode levar alguns minutos.'));
        } catch (ex) { if (ex.message !== 'login') toast(ex.message); }
        cleanupView.load();
      });
    },
    async saveAuto(on) {
      const a = { enabled: on, threshold: +$('#cl-thr').value, buildCache: $('#cl-a-build').checked, dangling: $('#cl-a-dang').checked,
        logs: $('#cl-a-logs').checked, logsOverMb: +$('#cl-a-mb').value };
      if (on && !S.cl.auto.enabled) {
        const what = [a.buildCache && T('o cache de build'), a.dangling && T('as imagens sem nome'), a.logs && `${T('os logs maiores que {0} MB', [a.logsOverMb])}`].filter(Boolean);
        const ok = await confirmDialog({ title: T('Ligar a limpeza automática?'), ok: T('Ligar'),
          body: `<p>${T('Quando o disco passar de <b>{0}%</b>, o painel limpa sozinho {1}, no máximo uma vez a cada 6 h.', [a.threshold, esc(what.join(', ') || T('o que estiver marcado'))])}</p>
            <ul class="cl-confirm">${[a.buildCache && 'build_cache', a.dangling && 'dangling', a.logs && 'logs'].filter(Boolean).map((k) => `<li><b>${CL[k].name}</b>: ${esc(CL[k].cons)}</li>`).join('')}</ul>
            <p>${T('Nenhuma app para. Cada limpeza fica no histórico e avisa pelo WhatsApp.')}</p>` });
        if (!ok) { $('#cl-auto-on').checked = false; return; }
      }
      try {
        const j = await api('/api/cleanup/auto', { method: 'POST', body: JSON.stringify(a) });
        S.cl.auto = j.auto;
        toast(j.auto.enabled ? `${T('Limpeza automática ligada (acima de {0}%).', [j.auto.threshold])}` : T('Limpeza automática desligada.'));
        document.activeElement.blur();
        cleanupView.render();
      } catch (ex) {
        if (ex.message === 'login') return;
        if ($('#cl-auto-err')) $('#cl-auto-err').textContent = ex.message;
        $('#cl-auto-on').checked = S.cl.auto.enabled;
      }
    },
  };

  // ------------------------------------------------------------------ aba: usuários
  const usersView = {
    mount(v) {
      v.innerHTML = `<div class="page"><div class="section-h"><div><h2>${icon('users')} ${T('Usuários')}</h2>
        <p>${T('Quem entra no painel e o que cada um pode fazer. Sem permissão marcada, a pessoa só vê (dados, logs e o chat com a IA).')}</p></div></div>
        <div id="us-body"></div></div>`;
      usersPanel($('#us-body'));
    },
  };

  // ------------------------------------------------------------------ aba: backups dos bancos (só administradores)
  const BK_EVERY = { '1h': T('A cada hora'), '6h': T('A cada 6 horas'), '24h': T('Uma vez por dia') };
  const BK_TIER = { hourly: T('horário'), daily: T('diário'), monthly: T('mensal') };
  const bkEngine = (k) => ((S.bk && S.bk.engines) || []).find((e) => e.key === k) || { name: k, restore: [] };
  const backupsView = {
    mount(v) {
      v.innerHTML = `<div class="page"><div class="section-h"><div><h2>${icon('db')} ${T('Backups dos bancos')}</h2>
        <p>${T('O painel tira o dump de cada banco escolhido, cifra com a sua chave e manda para o seu bucket (Cloudflare R2 ou outro compatível com S3).')}</p></div></div>
        <div id="bk-body"><div class="loading"><div class="spinner"></div></div></div></div>`;
      backupsView.files = {};
      backupsView.priv = '';
      backupsView.listen($('#bk-body'));
      backupsView.load();
    },
    async load() {
      clearTimeout(backupsView.t);
      try {
        const j = await api('/api/backup');
        S.bk = j.backup;
        backupsView.render();
        if (S.bk.running || S.bk.queue.length) backupsView.t = setTimeout(() => { if (S.tab === 'backups') backupsView.load(); }, 3000);
      } catch (e) {
        if (e.message !== 'login' && $('#bk-body')) $('#bk-body').innerHTML = `<div class="card empty">${esc(e.message)}</div>`;
      }
    },
    render() {
      const body = $('#bk-body');
      if (!body || !S.bk) return;
      if (body.contains(document.activeElement) && document.activeElement.matches('input, select')) return;
      const b = S.bk, st = b.storage;
      const step = (ok, n, t) => `<li class="${ok ? 'done' : ''}">${icon(ok ? 'ok' : 'info')}<span><b>${n}.</b> ${t}</span></li>`;
      body.innerHTML = `
        ${backupsView.usageHTML()}
        ${!b.configured ? `<section class="card"><div class="card-h"><h2>${icon('info')}${T('Para começar')}</h2></div><ol class="bk-steps">
          ${step(st.hasSecret, 1, T('Armazenamento: o bucket e as chaves de acesso (o painel testa antes de salvar).'))}
          ${step(!!b.publicKey, 2, T('Chave de criptografia: gere aqui (a privada fica com você) ou cole uma pública.'))}
          ${step(b.dbs.some((d) => d.enabled), 3, T('Ligue os bancos que quer copiar, com a frequência de cada um.'))}</ol></section>` : ''}
        ${backupsView.priv ? backupsView.privHTML() : ''}
        <div class="grid g2">
          ${backupsView.storageHTML(st)}
          ${backupsView.keyHTML()}
        </div>
        ${backupsView.dbsHTML()}
        <div class="grid g2">
          ${backupsView.retentionHTML()}
          ${backupsView.restoreHTML()}
        </div>
        ${backupsView.historyHTML()}
        <section class="note">${T('<b>Como o painel faz o dump.</b> Ele roda um comando fixo dentro do contêiner do banco (<code>docker exec</code>) usando o usuário e a senha que já estão nas variáveis do contêiner, cifra a saída na hora com a chave pública e só então grava num arquivo temporário e envia. O servidor nunca guarda a chave privada: nem ele nem o bucket conseguem abrir os backups. Para isso o proxy do Docker deixa o painel rodar <code>exec</code>, o que também permitiria, com o painel invadido, rodar comandos nos contêineres.')}</section>`;
    },
    // quanto o bucket ocupa (e, no R2, quanto dos 10 GB grátis)
    usageHTML() {
      const u = S.bk.usage || {}, lim = S.bk.freeLimit, st = S.bk.storage;
      if (!st.hasSecret || !u.at) return '';
      const p = lim ? (u.bytes / lim) * 100 : 0;
      const pp = p > 0 && p < 0.1 ? '<0,1%' : pct(p);
      return `<section class="card bk-usage"><div class="card-h"><h2>${icon('disk')}${T('Bucket {0}', [esc(st.bucket)])}</h2>
          ${lim ? badge(p >= 90 ? 'crit' : p >= 75 ? 'warn' : 'ok', `${T('{0} do grátis', [pp])}`) : ''}</div>
        <div class="bk-usage-n"><b class="num">${bytes(u.bytes)}</b><span class="muted">${lim ? `${T('de {0} grátis do R2', [bytes(lim)])}` : T('ocupados')}</span></div>
        ${lim ? `<div class="meter thick"><i class="${level(p, 75, 90)}" style="width:${Math.max(p > 0 ? 0.5 : 0, Math.min(100, p)).toFixed(2)}%"></i></div>` : ''}
        <div class="muted bk-usage-sub">${T('{0} arquivo(s) no bucket · medido {1}{2} {3}', [u.objects, ago(u.at), u.error ? ` · <span class="bk-err">${T('não consegui medir: {0}', [esc(u.error)])}</span>` : '', lim ? T('· a cota grátis é da conta inteira: outros buckets dela também contam') : ''])}</div></section>`;
    },
    storageHTML(st) {
      return `<section class="card"><div class="card-h"><h2>${icon('upload')}${T('Armazenamento')}</h2>${st.hasSecret ? badge('ok', T('testado')) : badge('info', T('falta configurar'))}</div>
        <form class="stack" id="bk-storage" autocomplete="off">
          <div class="field"><label for="bk-ep">${T('Endpoint (S3)')}</label><input class="input" id="bk-ep" required value="${esc(st.endpoint)}" placeholder="${T('https://<id-da-conta>.r2.cloudflarestorage.com')}" spellcheck="false" autocapitalize="off"></div>
          <div class="bk-two">
            <div class="field"><label for="bk-bucket">${T('Bucket')}</label><input class="input" id="bk-bucket" required value="${esc(st.bucket)}" placeholder="${T('meus-backups')}" spellcheck="false" autocapitalize="off"></div>
            <div class="field"><label for="bk-region">${T('Região')}</label><input class="input" id="bk-region" value="${esc(st.region || 'auto')}" spellcheck="false" autocapitalize="off"></div>
          </div>
          <div class="field"><label for="bk-ak">${T('Access Key ID')}</label>${secretInput(`id="bk-ak" required value="${esc(st.accessKey)}" spellcheck="false" autocapitalize="off" autocomplete="off"`, T('a Access Key ID'))}</div>
          <div class="field"><label for="bk-sk">${T('Secret Access Key')}</label>${secretInput(`id="bk-sk" ${st.hasSecret ? `placeholder="${T('guardada (deixe vazio para manter)')}"` : 'required'} autocomplete="new-password"`, T('a Secret Access Key'))}</div>
          <div class="field"><label for="bk-prefix">${T('Pasta no bucket')}</label><input class="input" id="bk-prefix" value="${esc(S.bk.prefix || 'vpserver')}" spellcheck="false" autocapitalize="off">
            <small class="muted">${T('Os arquivos ficam em {0}/{1}/&lt;banco&gt;/: dá para vários servidores dividirem um bucket.', [esc(S.bk.prefix || 'vpserver'), esc(S.bk.server)])}</small></div>
          <div class="form-err" id="bk-st-err" role="alert"></div>
          <div><button class="btn primary" type="submit">${T('Testar e salvar')}</button></div>
          <details class="bk-help"><summary>${T('Como criar no Cloudflare R2')}</summary>
            <ol><li>${T('No painel da Cloudflare: <b>R2</b> → <b>Create bucket</b> (o nome vai em "Bucket").')}</li>
              <li>${T('Em <b>R2</b> → <b>Manage R2 API Tokens</b> → <b>Create API token</b>: permissão <b>Object Read &amp; Write</b>, só neste bucket.')}</li>
              <li>${T('Copie a <b>Access Key ID</b>, a <b>Secret Access Key</b> e o endpoint <code>https://&lt;id-da-conta&gt;.r2.cloudflarestorage.com</code>. Região: <code>auto</code>.')}</li>
              <li>${T('O painel testa gravando, conferindo, listando e apagando um arquivo pequeno.')}</li></ol></details>
        </form></section>`;
    },
    keyHTML() {
      const pub = S.bk.publicKey;
      return `<section class="card"><div class="card-h"><h2>${icon('key')}${T('Chave de criptografia')}</h2>${pub ? badge('ok', T('configurada')) : badge('info', T('falta configurar'))}</div>
        ${pub ? `<p class="muted bk-p">${T('Os backups são cifrados para esta chave pública. Só a chave privada correspondente, que fica com você, abre os arquivos.')}</p>
          <div class="bk-key"><code>${esc(pub)}</code><button class="btn sm" type="button" data-copy="${esc(pub)}">${T('Copiar')}</button></div>
          <div class="controls"><button class="btn" type="button" data-bk="key-new">${T('Gerar outra')}</button><button class="btn" type="button" data-bk="key-paste">${T('Usar outra pública')}</button></div>`
        : `<p class="muted bk-p">${T('Formato <b>age</b> (abre com a ferramenta oficial <code>age</code>). O servidor guarda só a chave pública.')}</p>
          <div class="controls"><button class="btn primary" type="button" data-bk="key-new">${icon('key')}${T('Gerar par de chaves')}</button><button class="btn" type="button" data-bk="key-paste">${T('Já tenho uma chave pública')}</button></div>`}
        <form class="stack" id="bk-pub" hidden autocomplete="off"><div class="field"><label for="bk-pubin">${T('Chave pública (age1…)')}</label>
          <input class="input" id="bk-pubin" placeholder="age1…" spellcheck="false" autocapitalize="off"></div>
          <div><button class="btn primary" type="submit">${T('Usar esta chave')}</button></div></form></section>`;
    },
    privHTML() {
      const k = backupsView.priv;
      return `<section class="card bk-priv" role="alert"><div class="card-h"><h2>${icon('warn')}${T('Guarde a chave privada agora')}</h2></div>
        <p>${T('Esta é a <b>única vez</b> que ela aparece: o servidor não guarda. <b>Sem ela, nenhum backup abre.</b> Guarde no gerenciador de senhas (ou num arquivo seguro, fora do servidor) e teste uma restauração.')}</p>
        <div class="bk-key">${secretOut(k, T('a chave privada'))}</div>
        <div class="controls"><button class="btn" type="button" data-bk="key-download">${icon('install')}${T('Baixar chave.txt')}</button>
          <button class="btn primary" type="button" data-bk="key-saved">${T('Já guardei')}</button></div></section>`;
    },
    dbsHTML() {
      const b = S.bk;
      const row = (d) => {
        const eng = bkEngine(d.engine);
        const last = d.lastRun ? (d.lastErr
          ? `<span class="bk-err">${icon('crit')}${T('Falhou {0}: {1}', [ago(d.lastRun), esc(d.lastErr)])}</span>`
          : `<span>${icon('ok')}${T('Último {0} · {1}', [ago(d.lastRun), bytes(d.lastSize)])}</span>`) : `<span class="muted">${T('Ainda não rodou.')}</span>`;
        const running = b.running === d.id ? `<span class="spinner inline"></span> ${T('fazendo agora…')}` : b.queue.includes(d.id) ? T('na fila…') : '';
        const files = backupsView.files[d.id];
        return `<div class="bk-db${d.enabled ? ' on' : ''}" data-db="${esc(d.id)}">
          <div class="bk-db-h"><label class="bk-db-n"><input type="checkbox" class="sw" data-bkon ${d.enabled ? 'checked' : ''} ${d.found ? '' : 'disabled'}>
            <span><span class="bk-db-t"><b>${esc(d.appName || d.app || d.id)}</b> <span class="muted">· ${esc(d.service || d.container)}</span></span>
              <small class="muted">${esc(eng.name)} · ${d.found ? `${esc(d.container)} (${esc(d.state)})` : T('contêiner não encontrado agora')}</small></span></label>
            <span class="bk-tag">${esc(eng.name)}</span></div>
          <div class="bk-db-cfg">
            <div class="field"><label>${T('Frequência')}</label><select class="input" data-bkevery>${Object.entries(BK_EVERY).map(([k, l]) => `<option value="${k}" ${d.every === k ? 'selected' : ''}>${l}</option>`).join('')}</select></div>
            <div class="field"><label>${T('Banco (opcional)')}</label><input class="input" data-bkdb value="${esc(d.database || '')}" placeholder="${T('o padrão do contêiner')}" spellcheck="false" autocapitalize="off"></div>
          </div>
          <div class="bk-db-st">${last}${d.enabled && d.next ? `<span class="muted">${T('próximo: {0}', [dt(d.next)])}</span>` : ''}${running ? `<span>${running}</span>` : ''}</div>
          <div class="controls"><button class="btn sm" type="button" data-bk="run" ${d.enabled && d.found ? '' : 'disabled'}>${icon('upload')}${T('Fazer agora')}</button>
            <button class="btn sm" type="button" data-bk="files" ${b.configured ? '' : 'disabled'}>${icon('logs')}${T('Arquivos')}</button></div>
          ${files ? `<div class="bk-files">${files.loading ? '<div class="loading"><div class="spinner"></div></div>' : files.error ? `<div class="form-err">${esc(files.error)}</div>`
            : files.list.length ? files.list.map((o) => {
              const tier = (o.key.match(/\/(hourly|daily|monthly)\//) || [])[1];
              return `<div class="bk-file"><span><b>${dt(Date.parse(o.modified) / 1000)}</b> <span class="muted">${BK_TIER[tier] || ''} · ${bytes(o.size)}</span>
                <small class="muted">${esc(o.key.split('/').pop())}</small></span>
                <a class="btn sm" href="/api/backup/download?key=${encodeURIComponent(o.key)}" download>${icon('install')}${T('Baixar')}</a></div>`;
            }).join('') : `<div class="muted">${T('Nenhum arquivo no bucket ainda.')}</div>`}</div>` : ''}
        </div>`;
      };
      return `<section class="card"><div class="card-h"><h2>${icon('db')}${T('Bancos encontrados')}</h2></div>
        ${b.dbs.length ? `<div class="bk-dbs">${b.dbs.map(row).join('')}</div>`
          : `<div class="empty">${T('Nenhum banco nos contêineres (o painel reconhece PostgreSQL, MySQL/MariaDB, MongoDB e Redis/Valkey pela imagem).')}</div>`}
        ${!b.configured ? `<p class="muted bk-p">${T('Configure o armazenamento e a chave para ligar os backups.')}</p>` : ''}</section>`;
    },
    retentionHTML() {
      const r = S.bk.retention;
      return `<section class="card"><div class="card-h"><h2>${icon('clock')}${T('Retenção')}</h2></div>
        <form class="stack" id="bk-ret" autocomplete="off">
          <div class="bk-three">
            <div class="field"><label for="bk-rh">${T('Horários')}</label><div class="cl-inline"><input class="input" type="number" id="bk-rh" min="1" max="30" value="${r.hourly}"><span>${T('dias')}</span></div></div>
            <div class="field"><label for="bk-rd">${T('Diários')}</label><div class="cl-inline"><input class="input" type="number" id="bk-rd" min="1" max="365" value="${r.daily}"><span>${T('dias')}</span></div></div>
            <div class="field"><label for="bk-rm">${T('Mensais')}</label><div class="cl-inline"><input class="input" type="number" id="bk-rm" min="1" max="3650" value="${r.monthly}"><span>${T('dias')}</span></div></div>
          </div>
          <div class="field"><label for="bk-at">${T('O backup do dia (diário/mensal) é o primeiro depois das')}</label>
            <div class="cl-inline"><input class="input" type="number" id="bk-at" min="0" max="23" value="${r.dailyAt}"><span>h (UTC)</span></div></div>
          <label class="perm"><input type="checkbox" class="sw" id="bk-bybucket" ${r.byBucket ? 'checked' : ''}><span>${T('<b>Deixar a retenção para as regras do bucket</b>')}
            <small>${T('O painel não apaga nada; configure as regras de ciclo de vida no provedor (no R2: Settings → Object lifecycle rules, por prefixo).')}</small></span></label>
          <div class="form-err" id="bk-ret-err" role="alert"></div>
          <div><button class="btn" type="submit">${T('Salvar')}</button></div></form></section>`;
    },
    restoreHTML() {
      const used = [...new Set(S.bk.dbs.map((d) => d.engine))];
      const engs = S.bk.engines.filter((e) => !used.length || used.includes(e.key));
      return `<section class="card"><div class="card-h"><h2>${icon('refresh')}${T('Como restaurar')}</h2></div>
        <p class="muted bk-p">${T('Baixe o arquivo (em "Arquivos") e, no computador onde está a chave privada, use a ferramenta')}
          <a href="https://age-encryption.org" target="_blank" rel="noopener noreferrer">age</a>${T('. Teste uma restauração de vez em quando, num banco descartável.')}</p>
        ${engs.map((e) => `<div class="bk-restore"><b>${esc(e.name)}</b>${e.restore.map((l) => `<pre>${esc(l)}</pre>`).join('')}</div>`).join('')}</section>`;
    },
    historyHTML() {
      const runs = S.bk.runs;
      return `<section class="card"><div class="card-h"><h2>${icon('logs')}${T('Histórico')}</h2></div>
        ${runs.length ? `<div class="nlog">${runs.map((r) => `<details class="nl"><summary><span class="nl-time">${dt(r.t)}</span>
          ${badge(r.error ? 'crit' : 'ok', r.error ? T('falhou') : BK_TIER[r.tier] || 'ok')}<span class="nl-title">${esc(r.target)}${r.error ? '' : ` ${T('· {0} em {1} s', [bytes(r.size), r.seconds])}`}</span>
          ${r.error ? `<span class="nl-err">${esc(r.error)}</span>` : ''}</summary>
          <div class="cl-steps">${r.key ? `<div>${T('Arquivo: <code>{0}</code>', [esc(r.key)])}</div>` : ''}<div>${r.by ? `${T('Pedido por {0}', [esc(r.by)])}` : T('Agendado')}${r.plain ? ` ${T('· dump de {0}', [bytes(r.plain)])}` : ''}</div>
            ${r.pruned ? `<div>${T('{0} arquivo(s) antigo(s) apagado(s) pela retenção.', [r.pruned])}</div>` : ''}${r.pruneErr ? `<div class="nl-err">${T('Retenção: {0}', [esc(r.pruneErr)])}</div>` : ''}</div></details>`).join('')}</div>`
          : `<div class="empty">${T('Nenhum backup ainda.')}</div>`}</section>`;
    },
    async saveTarget(row, enabled, confirm) {
      const id = row.dataset.db;
      const d = S.bk.dbs.find((x) => x.id === id) || {};
      const every = $('[data-bkevery]', row).value, database = $('[data-bkdb]', row).value.trim();
      if (enabled && !d.enabled) {
        const eng = bkEngine(d.engine);
        const ok = await confirmDialog({ title: `${T('Ligar o backup de {0}?', [d.appName || id])}`, ok: T('Ligar o backup'),
          body: `<p>${T('{0}, o painel vai rodar o dump do <b>{1}</b> dentro do contêiner <b>{2}</b> (<code>docker exec</code>), com as credenciais das variáveis dele{3}.', [esc(BK_EVERY[every]), esc(eng.name), esc(d.container), database ? `${T(', banco <b>{0}</b>', [esc(database)])}` : ''])}</p>
            <p>${T('O dump sai cifrado para o bucket. Enquanto roda, o banco tem a carga de uma leitura completa; em bancos grandes, prefira uma frequência menor.')}</p>` });
        if (!ok) { $('[data-bkon]', row).checked = false; return; }
        confirm = true;
      }
      try {
        await api('/api/backup/target', { method: 'POST', body: JSON.stringify({ id, enabled, every, database, confirm: !!confirm || d.enabled }) });
        toast(enabled ? `${T('Backup de {0}: {1}.', [d.appName || id, BK_EVERY[every].toLowerCase()])}` : `${T('Backup de {0} desligado.', [d.appName || id])}`);
      } catch (ex) { if (ex.message !== 'login') toast(ex.message); }
      document.activeElement && document.activeElement.blur();
      backupsView.load();
    },
    listen(body) {
      body.addEventListener('submit', async (e) => {
        e.preventDefault();
        const f = e.target, btn = $('button[type="submit"]', f);
        btn.disabled = true;
        try {
          if (f.id === 'bk-storage') {
            $('#bk-st-err').textContent = '';
            btn.textContent = T('Testando…');
            const r = await api('/api/backup/storage', { method: 'POST', body: JSON.stringify({ endpoint: $('#bk-ep').value, bucket: $('#bk-bucket').value,
              region: $('#bk-region').value, accessKey: $('#bk-ak').value, secretKey: $('#bk-sk').value, prefix: $('#bk-prefix').value }) });
            toast(`${T('Armazenamento testado e salvo (bucket {0}).', [r.storage.bucket])}`);
          } else if (f.id === 'bk-pub') {
            if (S.bk.publicKey && !await confirmDialog({ title: T('Trocar a chave pública?'), ok: T('Trocar'), danger: true,
              body: `<p>${T('Os próximos backups passam a ser cifrados para a chave nova. <b>Os que já estão no bucket continuam abrindo só com a chave privada antiga</b>: guarde as duas.')}</p>` })) { btn.disabled = false; return; }
            await api('/api/backup/key', { method: 'POST', body: JSON.stringify({ publicKey: $('#bk-pubin').value, confirm: true }) });
            toast(T('Chave pública salva.'));
          } else if (f.id === 'bk-ret') {
            await api('/api/backup/retention', { method: 'POST', body: JSON.stringify({ hourly: +$('#bk-rh').value, daily: +$('#bk-rd').value,
              monthly: +$('#bk-rm').value, dailyAt: +$('#bk-at').value, byBucket: $('#bk-bybucket').checked }) });
            toast(T('Retenção salva.'));
          }
          document.activeElement && document.activeElement.blur();
          backupsView.load();
        } catch (ex) {
          if (ex.message === 'login') return;
          const err = $(f.id === 'bk-storage' ? '#bk-st-err' : f.id === 'bk-ret' ? '#bk-ret-err' : '#bk-st-err');
          if (f.id === 'bk-pub') toast(ex.message); else if (err) err.textContent = ex.message;
        }
        btn.disabled = false;
        if (f.id === 'bk-storage') btn.textContent = T('Testar e salvar');
      });
      body.addEventListener('change', (e) => {
        const row = e.target.closest('[data-db]');
        if (!row) return;
        if (e.target.matches('[data-bkon]')) backupsView.saveTarget(row, e.target.checked);
        else if (e.target.matches('[data-bkevery], [data-bkdb]')) {
          const d = S.bk.dbs.find((x) => x.id === row.dataset.db);
          if (d && d.enabled) backupsView.saveTarget(row, true, true);
        }
      });
      body.addEventListener('click', async (e) => {
        const b = e.target.closest('[data-bk]');
        if (!b) return;
        const act = b.dataset.bk, row = b.closest('[data-db]');
        try {
          if (act === 'key-paste') { const f = $('#bk-pub'); f.hidden = !f.hidden; if (!f.hidden) $('#bk-pubin').focus(); return; }
          if (act === 'key-new') {
            if (S.bk.publicKey && !await confirmDialog({ title: T('Gerar outra chave?'), ok: T('Gerar outra'), danger: true,
              body: `<p>${T('Os próximos backups passam a ser cifrados para a chave nova. <b>Os que já estão no bucket continuam abrindo só com a chave privada antiga</b>: guarde as duas.')}</p>` })) return;
            const r = await api('/api/backup/key', { method: 'POST', body: JSON.stringify({ generate: true, confirm: true }) });
            backupsView.priv = r.privateKey;
            await backupsView.load();
            window.scrollTo(0, 0);
            return;
          }
          if (act === 'key-download') {
            const a = document.createElement('a');
            a.href = URL.createObjectURL(new Blob([`${T('# chave privada dos backups do VPServer ({0})', [S.bk.server])}\n# public key: ${S.bk.publicKey}\n${backupsView.priv}\n`], { type: 'text/plain' }));
            a.download = T('chave-backups.txt');
            a.click();
            setTimeout(() => URL.revokeObjectURL(a.href), 1000);
            return;
          }
          if (act === 'key-saved') {
            if (!await confirmDialog({ title: T('Guardou a chave privada?'), ok: T('Guardei'), body: `<p>${T('Depois de fechar, ela não aparece mais. Sem ela, nenhum backup abre.')}</p>` })) return;
            backupsView.priv = '';
            backupsView.render();
            return;
          }
          if (act === 'run') {
            const d = S.bk.dbs.find((x) => x.id === row.dataset.db);
            if (!await confirmDialog({ title: `${T('Fazer o backup de {0} agora?', [d.appName || d.id])}`, ok: T('Fazer agora'),
              body: `<p>${T('Roda o dump dentro do contêiner <b>{0}</b> agora e envia ao bucket (fora da agenda).', [esc(d.container)])}</p>` })) return;
            await api('/api/backup/run', { method: 'POST', body: JSON.stringify({ id: d.id, confirm: true }) });
            toast(T('Backup na fila.'));
            backupsView.load();
            return;
          }
          if (act === 'files') {
            const id = row.dataset.db;
            if (backupsView.files[id] && !backupsView.files[id].loading) { delete backupsView.files[id]; backupsView.render(); return; }
            backupsView.files[id] = { loading: true };
            backupsView.render();
            try {
              const j = await api(`/api/backup/objects?id=${encodeURIComponent(id)}`);
              backupsView.files[id] = { list: j.objects || [] };
            } catch (ex) { backupsView.files[id] = { list: [], error: ex.message }; }
            backupsView.render();
          }
        } catch (ex) { if (ex.message !== 'login') toast(ex.message); }
      });
    },
  };

  // ------------------------------------------------------------------ aba: SSH (o shell do servidor em forma de chat)
  // Cada mensagem é um comando; a saída chega aos pedaços (long-poll). Programas
  // de tela cheia não funcionam (não há terminal de verdade): a tela avisa antes.
  const SSH_FULL = /^\s*(sudo\s+)?(vi|vim|nvim|nano|emacs|pico|top|htop|btop|less|more|watch|mc|tmux|screen|nmtui|ssh)(\s|$)/;
  const SSH_ALT = {
    top: 'top -bn1 | head -20', htop: 'top -bn1 | head -20', btop: 'top -bn1 | head -20', less: T('cat ARQUIVO ou tail -n 100 ARQUIVO'), more: T('cat ARQUIVO'),
    watch: T('rodar o comando de novo'), vi: T("sed -i 's/velho/novo/' ARQUIVO (ou peça para a IA)"), vim: T("sed -i 's/velho/novo/' ARQUIVO (ou peça para a IA)"),
    nvim: T("sed -i 's/velho/novo/' ARQUIVO"), nano: T("sed -i 's/velho/novo/' ARQUIVO (ou peça para a IA)"), emacs: T("sed -i 's/velho/novo/' ARQUIVO"),
    pico: T("sed -i 's/velho/novo/' ARQUIVO"), mc: T('ls -la e cp/mv'), tmux: '—', screen: '—', nmtui: 'nmcli', ssh: '—',
  };
  const SSH_COMMON = [
    ['df -h', T('espaço em disco')], ['free -h', T('memória')], ['uptime', T('tempo ligado e carga')],
    ['top -bn1 | head -20', T('quem mais usa CPU')], ['ps aux --sort=-%mem | head -15', T('quem mais usa memória')],
    ['du -sh * 2>/dev/null | sort -h | tail -15', T('o que mais ocupa nesta pasta')], ['ls -la', T('arquivos desta pasta')],
    ['sudo docker ps', T('contêineres rodando')], ['sudo docker ps -a', T('todos os contêineres')], ['sudo docker stats --no-stream', T('uso de cada contêiner agora')],
    ['sudo docker logs --tail 100 ', T('fim do log de um contêiner')], ['sudo docker compose ls', T('projetos do Compose')],
    ['sudo systemctl status ', T('estado de um serviço')], ['sudo journalctl -n 100 --no-pager', T('fim do log do sistema')],
    ['ss -tlnp', T('portas abertas')], ['ip -br a', T('endereços de rede')], ['cat /etc/os-release', T('versão do sistema')],
    ['sudo apt list --upgradable', T('atualizações disponíveis')], ['tail -n 100 ', T('fim de um arquivo')], ['sudo -i', T('virar root (pede a senha)')],
  ];
  const SSH_START = ['df -h', 'free -h', 'sudo docker ps', 'uptime'];
  const sshKey = (sid, id) => `${sid}:${id}`;
  // texto de terminal → texto puro: tira cores e outras sequências, e o \r sem \n recomeça a linha (barras de progresso)
  function termText(s) {
    s = String(s || '').replace(/\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)/g, '').replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, '').replace(/\x1b[@-Z\\-_]/g, '').replace(/\r+\n/g, '\n');
    s = s.split('\n').map((l) => { const i = l.lastIndexOf('\r'); return i >= 0 ? l.slice(i + 1) : l; }).join('\n');
    return s.replace(/[\x00-\x08\x0b\x0c\x0e-\x1d\x7f]/g, '');
  }
  // o que a pessoa digitou num programa rodando vem entre \x1e e \x1f
  const outHTML = (s) => termText(s).split(/(\x1e[^\x1f]*\x1f)/).map((p) => (p.startsWith('\x1e')
    ? `<span class="sb-in">› ${esc(p.slice(1, -1))}</span>` : esc(p))).join('');
  const plainOut = (s) => termText(s).replace(/\x1e[^\x1f]*\x1f/g, '');
  // pedido de senha no fim da saída de quem está rodando (sudo, su, ssh...)
  const askingPassword = (out) => /(password|senha|passphrase|contraseña)[^\n]{0,80}:\s*$/i.test(termText(out).split('\n').pop() || '');
  async function readSSE(r, on) {
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
        let ev = 'message', d = '';
        for (const line of block.split('\n')) {
          if (line.startsWith('event:')) ev = line.slice(6).trim();
          else if (line.startsWith('data:')) d += line.slice(5).trim();
        }
        if (d) on(ev, JSON.parse(d));
      }
    }
  }
  const sshView = {
    st() {
      if (!S.sh) {
        S.sh = { sid: '', ver: -1, lastId: 0, state: null, blocks: new Map(), items: [], nodes: new Map(), hist: [], hi: -1, draft: '',
          mode: 'cmd', pw: false, pwDismiss: 0, aiBusy: false, abort: null, loop: 0, n: 1, sugg: [], sel: -1,
          wrap: store.get('ssh-wrap', true) };
      }
      return S.sh;
    },
    mount(v) {
      sshView.st();
      v.innerHTML = '<div class="page" id="ssh-page"><div class="loading"><div class="spinner"></div></div></div>';
      const page = $('#ssh-page');
      page.addEventListener('click', sshView.onClick);
      page.addEventListener('submit', sshView.onSubmit);
      S.cleanup.push(() => sshView.stop());
      sshView.load();
    },
    stop() {
      const sh = sshView.st();
      sh.loop++;
      if (sh.pollAbort) sh.pollAbort.abort();
      if (sh.abort) sh.abort.abort();
    },
    async load() {
      const page = $('#ssh-page');
      if (!page) return;
      let j;
      try { j = await api('/api/ssh'); } catch (e) {
        if (e.message !== 'login') page.innerHTML = `<div class="alert crit"><div class="ic">${icon('crit')}</div><div class="alert-body"><div class="alert-t">${esc(e.message)}</div></div></div>`;
        return;
      }
      S.shInfo = j;
      const sh = sshView.st();
      sh.stop = false;
      if (!j.available) { page.innerHTML = `<div class="empty">${T('O SSH pela tela não está disponível neste painel.')}</div>`; return; }
      const head = `<div class="section-h"><div><h2>${icon('shell')} ${T('SSH do servidor')}</h2>
        <p>${T('Comandos no servidor por um chat: cada mensagem é um comando. Só administradores com 2FA, e abrir a sessão pede o código do app.')}</p></div></div>`;
      if (!j.twoFA) {
        page.innerHTML = `${head}<section class="card"><div class="card-h"><h2>${icon('shield')}${T('Ligue o 2FA para usar o SSH')}</h2></div>
          <p>${T('O SSH pela tela só abre para quem tem a verificação em duas etapas ligada: além da senha, abrir a sessão pede o código do app de autenticação.')}</p>
          <div class="controls"><button class="btn primary" type="button" data-act="settings">${icon('shield')}${T('Ligar em Minha conta')}</button></div></section>`;
        return;
      }
      if (!j.ssh.enabled) {
        page.innerHTML = head + sshView.setupHTML(j);
        sshView.probe(false);
        return;
      }
      if (!j.unlocked) {
        page.innerHTML = head + sshView.lockedHTML(j);
        setTimeout(() => { const c = $('#ssh-code'); if (c) c.focus(); }, 50);
        return;
      }
      sh.hist = (j.history || []).slice();
      sshView.chat();
    },
    footHTML(v) {
      return `<div class="ssh-foot muted">${T('Ligado por {0} {1} · <code>{2}@{3}</code> · chave do servidor <code class="ssh-fp">{4}</code> ·', [esc(v.enabledBy || '—'), v.enabledAt ? `${T('em {0}', [dt(v.enabledAt)])}` : '', esc(v.user), esc(v.host), esc(v.hostKey || '—')])} <button class="linkish" type="button" data-sa="log">${T('Registro')}</button>
        · <button class="linkish danger-t" type="button" data-sa="disable">${T('Desligar o SSH')}</button></div>`;
    },
    setupHTML(j) {
      const v = j.ssh;
      return `<section class="card ssh-setup">
        <div class="card-h"><h2>${icon('shell')}${T('Ativar o SSH pela tela')}</h2>${badge('off', T('desligado'))}</div>
        <p>${T('Ligado, o chat roda comandos no servidor como o usuário <code>{0}</code>, que vira root com <code>sudo</code> e a senha dele. Abrir a sessão pede o código do app; ela fecha depois de {1} min parada; cada comando fica no registro (sem a saída e sem senhas).', [esc(v.user), j.idleMinutes])}</p>
        <div class="alert warn slim"><div class="ic">${icon('warn')}</div><div class="alert-body"><div class="alert-t">${T('É acesso de verdade ao servidor')}</div>
          <div class="alert-d">${T('Um comando errado pode derrubar as aplicações ou apagar dados. Ligue só se for usar; dá para desligar a qualquer hora.')}</div></div></div>
        <ol class="ssh-steps">
          <li><h3>${T('1. Prepare o servidor (uma vez)')}</h3>
            <p>${T('Entre no servidor pelo seu SSH de sempre e rode o comando abaixo. Ele cria o usuário <code>{0}</code>, põe no grupo do sudo, autoriza a chave do painel só vinda da rede do contêiner (<code>{1}</code>), sem redirecionar portas, e no fim pede a senha que o sudo vai usar.', [esc(v.user), esc(v.nets.join(', '))])}</p>
            <div class="ssh-code"><pre>${esc(v.setup)}</pre><button class="btn sm" type="button" data-copy="${esc(v.setup)}">${T('Copiar')}</button></div>
            <details class="ssh-adv"><summary>${T('Usuário, endereço e porta')}</summary>
              <form id="ssh-target" class="ssh-target">
                <label class="field"><span>${T('Usuário no servidor')}</span><input class="input" id="ssh-t-user" value="${esc(v.user)}" autocapitalize="off" spellcheck="false"></label>
                <label class="field"><span>${T('Endereço (visto do contêiner)')}</span><input class="input" id="ssh-t-host" value="${esc(v.host)}" autocapitalize="off" spellcheck="false"></label>
                <label class="field"><span>${T('Porta')}</span><input class="input" id="ssh-t-port" type="number" min="1" max="65535" value="${v.port}"></label>
                <div class="controls"><button class="btn" type="submit">${T('Salvar')}</button>
                  <button class="btn" type="button" data-sa="newkey">${icon('key')}${T('Gerar outra chave do painel')}</button></div>
              </form>
              <p class="muted">${T('O padrão <code>host.docker.internal</code> é o próprio servidor visto de dentro do contêiner. Chave pública do painel:')}</p>
              <div class="ssh-code"><pre>${esc(v.publicKey)}</pre></div></details></li>
          <li><h3>${T('2. Confira')}</h3><div id="ssh-probe"><div class="loading"><div class="spinner"></div></div></div>
            <div class="controls"><button class="btn" type="button" data-sa="probe">${icon('refresh')}${T('Verificar (entra com a chave do painel)')}</button></div></li>
          <li><h3>${T('3. Ative')}</h3><p>${T('Digite o código do app de autenticação: o SSH liga e o chat já abre.')}</p>
            <form id="ssh-enable" class="ssh-codeform"><input class="input ssh-code-in" id="ssh-code" inputmode="numeric" autocomplete="one-time-code" maxlength="6" placeholder="123456" aria-label="${T('Código do app')}">
              <button class="btn primary" type="submit" id="ssh-enable-btn">${icon('shell')}${T('Ativar e abrir')}</button></form></li>
        </ol></section>`;
    },
    // login: entra com a chave (só quando a pessoa pede, depois do passo 1); sem ele, só
    // confere se o sshd responde (abrir a tela não vira tentativa de login falha)
    async probe(login) {
      const box = $('#ssh-probe');
      if (!box) return;
      box.innerHTML = '<div class="loading"><div class="spinner"></div></div>';
      let p;
      try { p = (await api(`/api/ssh/probe${login ? '?login=1' : ''}`)).probe; } catch (e) { box.innerHTML = `<div class="bk-err">${esc(e.message)}</div>`; return; }
      if (!$('#ssh-probe')) return;
      const row = (ok, t, d) => `<li class="${ok === null ? 'wait' : ok ? 'ok' : 'no'}">${icon(ok === null ? 'clock' : ok ? 'ok' : 'x')}<div><b>${t}</b>${d ? `<small>${d}</small>` : ''}</div></li>`;
      const reach = row(p.reachable, T('O servidor SSH responde'), p.reachable ? `${T('chave do servidor <code>{0}</code>{1}', [esc(p.hostKey), p.hostKeyNew ? ` ${T('<b class="danger-t">— diferente da aceita antes</b>')}` : ''])}` : esc(p.error || ''));
      box.innerHTML = login ? `<ul class="ssh-checks">${reach}
        ${row(p.authorized, T('A chave do painel entra'), p.authorized ? '' : p.reachable ? T('Rode o comando do passo 1 (ou confira usuário e porta).') : '')}
        ${row(p.sudo, T('O usuário está no grupo do sudo'), p.authorized ? `${T('grupos: {0}', [esc(p.groups || '—')])}` : '')}</ul>
        ${p.authorized && !p.sudo ? `<p class="muted">${T('Sem o grupo do sudo, o chat funciona, mas não vira root.')}</p>` : ''}`
        : `<ul class="ssh-checks">${reach}${row(null, T('A chave do painel entra'), T('Depois de rodar o comando do passo 1, clique em Verificar.'))}</ul>`;
    },
    lockedHTML(j) {
      const v = j.ssh;
      const open = (v.shells || []).length ? `<p class="muted">${T('Com sessão aberta agora: {0}.', [v.shells.map(esc).join(', ')])}</p>` : '';
      return `<section class="card ssh-lock"><div class="ssh-lock-in">${icon('lock')}<h2>${T('Sessão de SSH fechada')}</h2>
        <p>${T('Digite o código do app de autenticação para abrir. A sessão vale neste navegador e fecha sozinha depois de {0} min parada.', [j.idleMinutes])}</p>
        <form id="ssh-unlock" class="ssh-codeform"><input class="input ssh-code-in" id="ssh-code" inputmode="numeric" autocomplete="one-time-code" maxlength="6" placeholder="123456" aria-label="${T('Código do app')}">
          <button class="btn primary" type="submit">${icon('shell')}${T('Abrir')}</button></form>${open}</div></section>${sshView.footHTML(v)}`;
    },
    // --- chat ---------------------------------------------------------------------------
    chat() {
      const sh = sshView.st();
      const j = S.shInfo;
      const ai = j.ai;
      if (!ai && sh.mode === 'ai') sh.mode = 'cmd';
      $('#ssh-page').innerHTML = `
        <div class="ssh-bar"><span class="ssh-who" id="ssh-who"></span><code class="ssh-cwd" id="ssh-cwd"></code>
          <span class="ssh-bar-acts"><button class="btn sm" type="button" data-sa="wrap" aria-pressed="${sh.wrap}" title="${T('Quebrar linhas longas da saída')}">${T('Quebrar linhas')}</button>
          <button class="btn sm" type="button" data-sa="log">${T('Registro')}</button>
          <button class="btn sm" type="button" data-sa="end">${icon('logout')}${T('Encerrar')}</button></span></div>
        <section class="card ssh-chat${sh.wrap ? ' wrap' : ''}" id="ssh-chat">
          <div class="ssh-log" id="ssh-log" aria-live="polite"></div>
          <div class="ssh-sugg" id="ssh-sugg" role="listbox" hidden></div>
          <div class="ssh-keys" role="toolbar" aria-label="${T('Teclas')}">
            <button type="button" data-k="ctrl-c" title="${T('Interrompe o comando')}">Ctrl+C</button>
            <button type="button" data-k="ctrl-d" title="${T('Fim da entrada')}">Ctrl+D</button>
            <button type="button" data-k="tab" title="${T('Completar')}">Tab</button>
            <button type="button" data-k="up" aria-label="${T('Comando anterior')}">↑</button>
            <button type="button" data-k="down" aria-label="${T('Próximo comando')}">↓</button>
            <button type="button" data-k="esc">Esc</button>
            <button type="button" data-k="pw" title="${T('Digitar uma senha (não aparece nem vai para o registro)')}">${icon('key')}${T('Senha')}</button>
            <button type="button" data-k="sudo" id="ssh-sudo">sudo -i</button>
            <button type="button" data-k="clear" title="${T('Limpa a tela (o servidor não muda)')}">${T('Limpar tela')}</button>
          </div>
          <form class="ssh-form" id="ssh-form">
            <button class="ssh-mode" type="button" data-k="mode" id="ssh-mode" ${ai ? '' : `disabled title="${T('Ponha a chave da IA em Configurações → IA')}"`}></button>
            <textarea class="input" id="ssh-in" rows="1" autocapitalize="off" autocomplete="off" autocorrect="off" spellcheck="false" aria-label="${T('Comando')}"></textarea>
            <input class="input" id="ssh-pw" type="password" autocomplete="off" hidden aria-label="${T('Senha')}">
            <button class="btn primary" type="submit" id="ssh-send" aria-label="${T('Enviar')}">${icon('send')}</button>
          </form>
        </section>
        ${sshView.footHTML(j.ssh)}
        <div class="chat-note">${T('Tab completa comandos e arquivos; ↑ e ↓ trazem os anteriores; Ctrl+C interrompe. Programas de tela cheia (vim, top, less) não funcionam aqui. {0}', [ai ? T('A IA (botão $/IA) sugere comandos e explica saídas; ela só vê o que você mandar, com senhas e tokens mascarados, e nunca roda nada sozinha.') : ''])}</div>`;
      const ta = $('#ssh-in');
      ta.addEventListener('input', () => { sshView.grow(); sh.hi = -1; sshView.suggest(); });
      ta.addEventListener('keydown', sshView.onKey);
      $('#ssh-pw').addEventListener('keydown', (e) => { if (e.key === 'Escape') { sh.pw = false; sh.pwDismiss = sshView.lastBlock() ? sshView.lastBlock().to : 0; sshView.inputs(); } });
      $('#ssh-sugg').addEventListener('mousedown', (e) => e.preventDefault()); // não tira o foco do campo
      sh.nodes = new Map();
      sh.items.forEach((it) => { it.dirty = true; });
      sshView.paint();
      sshView.inputs();
      sshView.poll();
      setTimeout(() => ta.focus(), 50);
    },
    lastBlock() { const sh = sshView.st(); return sh.blocks.get(sshKey(sh.sid, sh.lastId)); },
    busy() { const sh = sshView.st(); return !!(sh.state && sh.state.busy); },
    root() { const sh = sshView.st(); return !!(sh.state && sh.state.uid === 0); },
    grow() {
      const ta = $('#ssh-in');
      if (!ta) return;
      ta.style.height = 'auto';
      ta.style.height = Math.min(180, ta.scrollHeight) + 'px';
      ta.style.overflowY = ta.scrollHeight > 180 ? 'auto' : 'hidden';
    },
    async poll() {
      const sh = sshView.st();
      const me = ++sh.loop;
      let fails = 0;
      while (sh.loop === me && $('#ssh-log')) {
        sh.pollAbort = new AbortController();
        const lb = sshView.lastBlock();
        try {
          const j = await api(`/api/ssh/poll?s=${encodeURIComponent(sh.sid)}&v=${sh.ver}&b=${sh.lastId}&o=${lb ? lb.to : 0}`, { signal: sh.pollAbort.signal });
          if (sh.loop !== me) return;
          fails = 0;
          sshView.apply(j);
        } catch (e) {
          if (sh.loop !== me || e.name === 'AbortError' || e.message === 'login') return;
          if (e.code === 'ssh_locked' || e.code === 'ssh_disabled' || e.code === 'ssh_2fa_required') { sshView.load(); return; }
          fails++;
          await new Promise((r) => setTimeout(r, Math.min(15000, 1500 * fails)));
        }
        if (sh.state && sh.state.closed) {
          sshView.closed(sh.state.closed);
          return;
        }
      }
    },
    apply(j) {
      const sh = sshView.st();
      const st = j.state;
      if (st.session && st.session !== sh.sid) {
        if (sh.sid && sh.items.length) sh.items.push({ k: 'i' + sh.n++, kind: 'i', text: T('Sessão nova.'), dirty: true });
        sh.sid = st.session;
        sh.lastId = 0;
        sh.closedShown = false;
        sh.items.forEach((x) => { if (x.reopen) { x.reopen = false; x.dirty = true; } });
      }
      for (const bv of j.blocks) {
        const key = sshKey(sh.sid, bv.id);
        let b = sh.blocks.get(key);
        if (!b) {
          b = { ...bv };
          sh.blocks.set(key, b);
          sh.items.push({ k: 'b' + key, kind: 'b', key, dirty: true });
        } else {
          const out = bv.from === b.to ? b.out + bv.out : bv.out;
          Object.assign(b, bv, { out });
        }
        if (b.out.length > 200000) { b.out = b.out.slice(-150000); b.cut = true; }
        const it = sh.items.find((x) => x.key === key);
        if (it) it.dirty = true;
        sh.lastId = Math.max(sh.lastId, bv.id);
      }
      const wasBusy = sshView.busy();
      sh.state = st;
      sh.ver = st.ver;
      if (wasBusy && !st.busy) { sh.pw = false; sh.pwDismiss = 0; }
      const lb = sshView.lastBlock();
      if (lb && st.busy && !sh.pw && askingPassword(lb.out) && lb.to !== sh.pwDismiss) { sh.pw = true; }
      if (sh.blocks.size > 300) { // memória: só as últimas
        const keep = new Set(sh.items.filter((x) => x.kind === 'b').slice(-120).map((x) => x.key));
        for (const k of sh.blocks.keys()) if (!keep.has(k)) sh.blocks.delete(k);
        sh.items = sh.items.filter((x) => x.kind !== 'b' || keep.has(x.key));
      }
      sshView.paint();
      sshView.inputs();
    },
    closed(reason) {
      const sh = sshView.st();
      if (sh.closedShown) return;
      sh.closedShown = true;
      sh.items.push({ k: 'i' + sh.n++, kind: 'i', text: `${T('A sessão terminou ({0}).', [reason])}`, reopen: true, dirty: true });
      sshView.paint();
      sshView.inputs();
    },
    blockHTML(b) {
      const sh = sshView.st();
      const running = !b.end && sshView.busy() && b === sshView.lastBlock();
      const status = running ? `<span class="sb-st run"><span class="spinner"></span>${T('rodando')}</span>`
        : b.code == null ? `<span class="sb-st off">${T('interrompido')}</span>`
          : b.code === 0 ? `<span class="sb-st ok" title="${T('código de saída 0')}">${icon('ok')}0</span>`
            : `<span class="sb-st bad" title="${T('código de saída {0}', [b.code])}">${icon('x')}${b.code}</span>`;
      const took = b.end ? ` · ${b.end - b.start < 1 ? '<1 s' : b.end - b.start < 120 ? `${b.end - b.start} s` : dur(b.end - b.start)}` : '';
      const out = plainOut(b.out).trim() ? `<pre class="sb-out">${b.base || b.cut ? `<span class="sb-cut">${T('… começo cortado (o painel guarda só o fim de saídas grandes)')}</span>\n` : ''}${outHTML(b.out)}</pre>`
        : running ? '' : `<div class="sb-none">${T('sem saída')}</div>`;
      const acts = running ? '' : `<div class="sb-acts">
        ${plainOut(b.out).trim() ? `<button class="linkish" type="button" data-sa="copy" data-key="${esc(sshKey(sh.sid, b.id))}">${T('Copiar saída')}</button>` : ''}
        <button class="linkish" type="button" data-sa="again" data-key="${esc(sshKey(sh.sid, b.id))}">${T('Rodar de novo')}</button>
        ${S.shInfo && S.shInfo.ai ? `<button class="linkish" type="button" data-sa="explain" data-key="${esc(sshKey(sh.sid, b.id))}">${icon('spark')}${T('Explicar')}</button>` : ''}</div>`;
      return `<div class="sb-h"><span class="sb-p${b.uid === 0 ? ' root' : ''}">${b.uid === 0 ? '#' : '$'}</span><code class="sb-cmd">${esc(b.cmd)}</code>${status}</div>
        <div class="sb-meta">${esc(b.by)} · ${hms(b.start * 1000)} · ${esc(b.cwd || '')}${b.note ? ` · ${esc(b.note)}` : ''}${took}</div>${out}${acts}`;
    },
    aiHTML(m) {
      return `<div class="msg user ssh-q">${icon('spark')}<span>${esc(m.q)}</span></div>
        <div class="msg ai"><div class="md">${md(m.content)}${m.streaming ? '<span class="caret"></span>' : ''}</div>
        ${m.error ? `<div class="alert crit slim"><div class="ic">${icon('crit')}</div><div class="alert-body"><div class="alert-t">${esc(m.error)}</div></div></div>` : ''}
        ${m.meta ? `<div class="msg-meta">${esc(m.meta)}</div>` : ''}</div>`;
    },
    emptyHTML() {
      return `<div class="ssh-empty">${icon('shell')}<div>${T('<b>Cada mensagem é um comando no servidor.</b><br>')}
        <span class="muted">${T('A pasta e as variáveis continuam de um comando para o outro. Para virar root: <code>sudo -i</code> (pede a senha).')}</span></div>
        <div class="suggest">${SSH_START.map((c) => `<button type="button" class="chip-btn mono" data-sa="fill" data-cmd="${esc(c)}">${esc(c)}</button>`).join('')}</div></div>`;
    },
    paint() {
      const sh = sshView.st();
      const log = $('#ssh-log');
      if (!log) return;
      const near = log.scrollHeight - log.scrollTop - log.clientHeight < 140;
      if (!sh.items.length) {
        log.innerHTML = sshView.emptyHTML();
        sh.nodes = new Map();
        return;
      }
      const empty = $('.ssh-empty', log);
      if (empty) empty.remove();
      let prev = null;
      for (const it of sh.items) {
        let node = sh.nodes.get(it.k);
        if (!node) {
          node = document.createElement('div');
          node.dataset.key = it.k;
          sh.nodes.set(it.k, node);
          it.dirty = true;
          if (prev) prev.after(node); else log.prepend(node);
        }
        if (it.dirty) {
          it.dirty = false;
          if (it.kind === 'b') {
            const b = sh.blocks.get(it.key);
            if (!b) continue;
            const pre = $('.sb-out', node);
            const keepScroll = pre && pre.scrollHeight - pre.scrollTop - pre.clientHeight < 40;
            node.className = 'sb';
            node.innerHTML = sshView.blockHTML(b);
            const npre = $('.sb-out', node);
            if (npre && (keepScroll || !pre)) npre.scrollTop = npre.scrollHeight;
          } else if (it.kind === 'a') {
            node.className = 'ssh-ai';
            node.innerHTML = sshView.aiHTML(it);
            if (!it.streaming) {
              $$('.md pre', node).forEach((pre) => {
                const cmd = pre.textContent.replace(/^\s*\$\s+/, '').trim();
                if (!cmd) return;
                const bar = document.createElement('div');
                bar.className = 'sa-run';
                bar.innerHTML = `<button class="btn sm primary" type="button" data-sa="run-ai">${icon('play')}${T('Rodar')}</button><button class="btn sm" type="button" data-sa="edit-ai">${T('Editar')}</button>`;
                bar.dataset.cmd = cmd;
                pre.after(bar);
              });
            }
          } else {
            node.className = 'ssh-info';
            node.innerHTML = `${icon('info')}<span>${esc(it.text)}</span>${it.reopen ? `<button class="btn sm primary" type="button" data-sa="reopen">${T('Abrir de novo')}</button>` : ''}`;
          }
        }
        prev = node;
      }
      for (const [k, node] of sh.nodes) if (!sh.items.some((x) => x.k === k)) { node.remove(); sh.nodes.delete(k); }
      if (near) log.scrollTop = log.scrollHeight;
    },
    // cabeçalho, campo (comando, entrada ou senha) e teclas conforme o estado
    inputs() {
      const sh = sshView.st();
      const st = sh.state || {};
      const ta = $('#ssh-in'), pw = $('#ssh-pw');
      if (!ta) return;
      const v = S.shInfo.ssh;
      const root = st.uid === 0;
      $('#ssh-who').innerHTML = `${icon('shell')}<b>${root ? 'root' : esc(v.user)}</b>${root ? ` <span class="badge-sudo">${T('modo sudo')}</span>` : ''}`;
      $('#ssh-who').classList.toggle('root', root);
      const cwd = st.cwd || '';
      $('#ssh-cwd').textContent = cwd.length > 60 ? '…' + cwd.slice(-59) : cwd; // pasta longa: mostra o fim
      $('#ssh-cwd').title = cwd;
      const closed = !!st.closed;
      const busy = !!st.busy;
      const ai = sh.mode === 'ai';
      $('#ssh-mode').innerHTML = ai ? `${icon('spark')}<span>${T('IA')}</span>` : '<span class="mono">$</span>';
      $('#ssh-mode').setAttribute('aria-pressed', ai);
      $('#ssh-mode').title = ai ? T('Perguntando à IA (toque para voltar aos comandos)') : T('Comandos (toque para perguntar à IA)');
      const showPw = sh.pw && busy && !ai;
      pw.hidden = !showPw;
      ta.hidden = showPw;
      ta.disabled = closed && !ai;
      ta.placeholder = closed ? T('Sessão encerrada') : ai ? T('Pergunte à IA… ex.: por que o disco encheu?') : busy ? T('Entrada para o programa (Enter manda a linha)') : root ? T('Comando como root…') : T('Comando… ex.: df -h');
      pw.placeholder = T('Senha (não aparece nem vai para o registro)');
      if (showPw && document.activeElement !== pw) setTimeout(() => pw.focus(), 0);
      const keys = (k) => $(`[data-k="${k}"]`);
      keys('ctrl-d').disabled = keys('esc').disabled = keys('pw').disabled = !busy || closed;
      keys('ctrl-c').disabled = closed;
      keys('tab').disabled = keys('sudo').disabled = busy || closed || ai;
      keys('sudo').textContent = root ? T('Sair do sudo') : 'sudo -i';
      $('#ssh-send').disabled = (closed && !ai) || (ai && sh.aiBusy);
      $('#ssh-chat').classList.toggle('busy', busy);
    },
    // --- envio ----------------------------------------------------------------------------
    async onSubmit(e) {
      const f = e.target;
      const sh = sshView.st();
      if (f.id === 'ssh-enable' || f.id === 'ssh-unlock') {
        e.preventDefault();
        const code = $('#ssh-code').value.replace(/\D/g, '');
        if (code.length !== 6) { toast(T('Digite os 6 números do app.')); return; }
        const btn = $('button[type=submit]', f);
        btn.disabled = true;
        try {
          await api(f.id === 'ssh-enable' ? '/api/ssh/enable' : '/api/ssh/unlock', { method: 'POST', body: JSON.stringify({ code }) });
          sh.sid = ''; sh.ver = -1; sh.lastId = 0;
          sshView.load();
        } catch (err) {
          if (err.message === 'login') return;
          btn.disabled = false;
          toast(err.message);
          $('#ssh-code').select();
        }
        return;
      }
      if (f.id === 'ssh-target') {
        e.preventDefault();
        try {
          await api('/api/ssh/target', { method: 'POST', body: JSON.stringify({ user: $('#ssh-t-user').value.trim(), host: $('#ssh-t-host').value.trim(), port: +$('#ssh-t-port').value }) });
          toast(T('Salvo. O comando do passo 1 mudou: rode de novo no servidor.'));
          sshView.load();
        } catch (err) { if (err.message !== 'login') toast(err.message); }
        return;
      }
      if (f.id !== 'ssh-form') return;
      e.preventDefault();
      const ta = $('#ssh-in'), pw = $('#ssh-pw');
      if (!pw.hidden) {
        const v = pw.value;
        pw.value = '';
        sh.pw = false;
        sh.pwDismiss = sshView.lastBlock() ? sshView.lastBlock().to : 0;
        sshView.inputs();
        sshView.send({ text: v, secret: true });
        $('#ssh-in').focus();
        return;
      }
      const text = ta.value;
      if (sh.mode === 'ai') {
        if (!text.trim() || sh.aiBusy) return;
        ta.value = '';
        sshView.grow();
        sshView.askAI(text.trim());
        return;
      }
      if (sshView.busy()) {
        ta.value = '';
        sshView.grow();
        sshView.send({ text });
        return;
      }
      if (!text.trim()) return;
      if (await sshView.run(text)) { ta.value = ''; sshView.grow(); sshView.hideSugg(); }
    },
    async run(cmd) {
      const sh = sshView.st();
      if (sshView.busy()) { toast(T('Ainda há um comando rodando: espere ou use Ctrl+C.')); return false; }
      const m = cmd.match(SSH_FULL);
      if (m) {
        const ok = await confirmDialog({
          title: `${T('{0} não funciona no chat', [m[2]])}`, ok: T('Rodar mesmo assim'),
          body: `<p>${T('Programas de tela cheia precisam de um terminal de verdade; aqui eles ficam esperando ou mostram lixo. Se travar, use <b>Ctrl+C</b>.')}</p>
            <p>${T('No lugar: <code>{0}</code>', [esc(SSH_ALT[m[2]] || '—')])}</p>`,
        });
        if (!ok) return false;
      }
      try {
        await api('/api/ssh/run', { method: 'POST', body: JSON.stringify({ cmd }) });
        const c = cmd.trim();
        sh.hist = sh.hist.filter((x) => x !== c);
        sh.hist.push(c);
        sh.hi = -1;
        return true;
      } catch (e) {
        sshView.err(e);
        return false;
      }
    },
    async send(body) {
      try { await api('/api/ssh/input', { method: 'POST', body: JSON.stringify(body) }); } catch (e) { sshView.err(e); }
    },
    err(e) {
      if (e.message === 'login') return;
      if (e.code === 'ssh_locked' || e.code === 'ssh_disabled' || e.code === 'ssh_2fa_required') { toast(e.message); sshView.load(); return; }
      toast(e.message);
    },
    // --- teclado e teclas da tela -----------------------------------------------------------------
    onKey(e) {
      const sh = sshView.st();
      const ta = e.target;
      const open = !$('#ssh-sugg').hidden && sh.sugg.length;
      if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
        e.preventDefault();
        if (open && sh.sel >= 0) { sshView.pick(sh.sel); return; }
        $('#ssh-form').requestSubmit();
      } else if (e.key === 'Tab' && !e.shiftKey && sh.mode !== 'ai') {
        e.preventDefault();
        if (!sshView.busy()) sshView.complete();
      } else if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
        const up = e.key === 'ArrowUp';
        if (open) {
          e.preventDefault();
          const n = sh.sugg.length;
          sh.sel = up ? (sh.sel <= 0 ? n - 1 : sh.sel - 1) : (sh.sel >= n - 1 ? 0 : sh.sel + 1);
          sshView.drawSugg();
          return;
        }
        const before = ta.value.slice(0, ta.selectionStart), after = ta.value.slice(ta.selectionEnd);
        if ((up && !before.includes('\n')) || (!up && !after.includes('\n'))) { e.preventDefault(); sshView.histMove(up); }
      } else if (e.key === 'Escape') {
        if (open) { e.preventDefault(); sshView.hideSugg(); }
      } else if (e.ctrlKey && !e.altKey && !e.metaKey && (e.key === 'c' || e.key === 'C') && ta.selectionStart === ta.selectionEnd) {
        e.preventDefault();
        if (sshView.busy()) sshView.send({ key: 'ctrl-c' });
        else { ta.value = ''; sshView.grow(); sshView.hideSugg(); }
      } else if (e.ctrlKey && (e.key === 'd' || e.key === 'D') && sshView.busy()) {
        e.preventDefault();
        sshView.send({ key: 'ctrl-d' });
      } else if (e.ctrlKey && (e.key === 'l' || e.key === 'L')) {
        e.preventDefault();
        sshView.clear();
      }
    },
    histMove(up) {
      const sh = sshView.st();
      const ta = $('#ssh-in');
      if (!sh.hist.length) return;
      if (up) {
        if (sh.hi === -1) { sh.draft = ta.value; sh.hi = sh.hist.length - 1; } else sh.hi = Math.max(0, sh.hi - 1);
        ta.value = sh.hist[sh.hi];
      } else {
        if (sh.hi === -1) return;
        sh.hi++;
        if (sh.hi >= sh.hist.length) { sh.hi = -1; ta.value = sh.draft; } else ta.value = sh.hist[sh.hi];
      }
      sshView.grow();
      sshView.hideSugg();
      ta.setSelectionRange(ta.value.length, ta.value.length);
    },
    clear() {
      const sh = sshView.st();
      sh.items = [];
      sshView.paint();
    },
    async key(k) {
      const sh = sshView.st();
      const ta = $('#ssh-in');
      switch (k) {
        case 'ctrl-c': if (sshView.busy()) sshView.send({ key: 'ctrl-c' }); else { ta.value = ''; sshView.grow(); } break;
        case 'ctrl-d': sshView.send({ key: 'ctrl-d' }); break;
        case 'esc': sshView.send({ key: 'esc' }); break;
        case 'tab': await sshView.complete(); break;
        case 'up': sshView.histMove(true); break;
        case 'down': sshView.histMove(false); break;
        case 'pw': sh.pw = !sh.pw; if (!sh.pw) sh.pwDismiss = sshView.lastBlock() ? sshView.lastBlock().to : 0; sshView.inputs(); break;
        case 'sudo': sshView.run(sshView.root() ? 'exit' : 'sudo -i'); break;
        case 'clear': sshView.clear(); break;
        case 'mode': sh.mode = sh.mode === 'ai' ? 'cmd' : 'ai'; sshView.hideSugg(); sshView.inputs(); break;
        default: break;
      }
      if (k !== 'pw' && ta && !ta.hidden) ta.focus();
    },
    // --- autocompletar --------------------------------------------------------------------------
    containers() {
      try { return [...new Set(appsOf().flatMap((a) => a.units).filter((u) => u.container).map((u) => u.container.name))].sort(); } catch { return []; }
    },
    suggest() {
      const sh = sshView.st();
      const q = $('#ssh-in').value;
      if (sh.mode === 'ai' || sshView.busy() || !q.trim() || q.includes('\n')) { sshView.hideSugg(); return; }
      const seen = new Set();
      const out = [];
      for (let i = sh.hist.length - 1; i >= 0 && out.length < 5; i--) {
        const h = sh.hist[i];
        if (h !== q && h.startsWith(q) && !seen.has(h)) { seen.add(h); out.push({ text: h, line: true, hint: T('já usado') }); }
      }
      for (const [c, d] of SSH_COMMON) {
        if (out.length >= 8) break;
        if (c !== q && c.startsWith(q) && !seen.has(c)) { seen.add(c); out.push({ text: c, line: true, hint: d }); }
      }
      sh.sugg = out;
      sh.sel = -1;
      sshView.drawSugg();
    },
    drawSugg() {
      const sh = sshView.st();
      const box = $('#ssh-sugg');
      if (!box) return;
      if (!sh.sugg.length) { box.hidden = true; return; }
      box.hidden = false;
      box.innerHTML = sh.sugg.map((s, i) => `<button type="button" role="option" class="sg${i === sh.sel ? ' sel' : ''}" aria-selected="${i === sh.sel}" data-sa="pick" data-i="${i}">
        <code>${esc(s.text)}</code>${s.hint ? `<small>${esc(s.hint)}</small>` : ''}</button>`).join('');
      const sel = $('.sg.sel', box);
      if (sel) sel.scrollIntoView({ block: 'nearest' });
    },
    hideSugg() {
      const sh = sshView.st();
      sh.sugg = [];
      sh.sel = -1;
      const box = $('#ssh-sugg');
      if (box) box.hidden = true;
    },
    pick(i) {
      const sh = sshView.st();
      const s = sh.sugg[i];
      const ta = $('#ssh-in');
      if (!s || !ta) return;
      if (s.line) ta.value = s.text;
      else {
        const pos = ta.selectionStart;
        ta.value = sh.wordHead + s.text + (s.text.endsWith('/') ? '' : ' ') + ta.value.slice(pos);
      }
      sshView.hideSugg();
      sshView.grow();
      ta.focus();
      ta.setSelectionRange(ta.value.length, ta.value.length);
    },
    async complete() {
      const sh = sshView.st();
      const ta = $('#ssh-in');
      const pos = ta.selectionStart;
      const before = ta.value.slice(0, pos);
      const word = before.match(/(\S*)$/)[1];
      const head = before.slice(0, before.length - word.length);
      const first = !head.trim() || /(\||&&|;|\$\(|\bsudo)\s*$/.test(head);
      let items = [];
      if (!first && !word.startsWith('-') && /\bdocker\s+(logs|restart|stop|start|exec|inspect|stats|top|kill|rm|pause|unpause|port|cp)\b/.test(head)) {
        items = sshView.containers().filter((n) => n.startsWith(word));
      }
      if (!items.length) {
        try { items = (await api(`/api/ssh/complete?m=${first ? 'cmd' : 'file'}&w=${encodeURIComponent(word)}`)).items; } catch (e) { sshView.err(e); return; }
      }
      if (!items.length) { toast(T('Nada para completar.')); return; }
      const apply = (rep) => {
        ta.value = head + rep + ta.value.slice(pos);
        const c = (head + rep).length;
        ta.setSelectionRange(c, c);
        sshView.grow();
      };
      if (items.length === 1) { apply(items[0] + (items[0].endsWith('/') ? '' : ' ')); sshView.hideSugg(); return; }
      let cp = items[0];
      for (const x of items) while (!x.startsWith(cp)) cp = cp.slice(0, -1);
      if (cp.length > word.length) apply(cp);
      sh.wordHead = head;
      sh.sugg = items.slice(0, 60).map((x) => ({ text: x }));
      sh.sel = -1;
      sshView.drawSugg();
    },
    // --- IA ---------------------------------------------------------------------------------------
    async askAI(q, explainKey) {
      const sh = sshView.st();
      if (sh.aiBusy) return;
      const history = sh.items.filter((x) => x.kind === 'a' && x.content && !x.error).slice(-5)
        .flatMap((x) => [{ role: 'user', content: x.q }, { role: 'assistant', content: x.content }]);
      history.push({ role: 'user', content: q });
      const m = { k: 'a' + sh.n++, kind: 'a', q, content: '', streaming: true, dirty: true };
      sh.items.push(m);
      sh.aiBusy = true;
      sh.abort = new AbortController();
      sshView.paint();
      sshView.inputs();
      let raf = 0;
      const repaint = () => { m.dirty = true; if (!raf) raf = requestAnimationFrame(() => { raf = 0; sshView.paint(); }); };
      const t0 = Date.now();
      const explain = explainKey ? +(explainKey.split(':')[1] || 0) : 0;
      try {
        const r = await fetch('/api/ssh/ai', {
          method: 'POST', credentials: 'same-origin', signal: sh.abort.signal,
          headers: { 'X-Requested-With': 'vpmon', 'X-VPMon-Lang': LANG, 'Content-Type': 'application/json' },
          body: JSON.stringify({ messages: history, explain }),
        });
        if (r.status === 401) { showLogin(); return; }
        if (!r.ok) {
          const j = await r.json().catch(() => ({}));
          const err = new Error((j.error && j.error.message) || `${T('Erro {0}', [r.status])}`);
          err.code = j.error && j.error.code;
          throw err;
        }
        await readSSE(r, (ev, d) => {
          if (ev === 'delta') m.content += d.text;
          else if (ev === 'error') m.error = d.message;
          else if (ev === 'done') {
            const u = d.usage || {};
            m.meta = `${T('{0} · {1} mil tokens · {2} s', [d.model, num(((u.prompt_tokens || 0) + (u.completion_tokens || 0)) / 1000, 1), num((Date.now() - t0) / 1000, 0)])}`;
          }
          repaint();
        });
      } catch (e) {
        if (e.name === 'AbortError') m.error = m.content ? '' : T('Pergunta cancelada.');
        else m.error = e.message;
      } finally {
        m.streaming = false;
        if (!m.content && !m.error) m.error = T('A IA não respondeu nada. Tente de novo.');
        sh.aiBusy = false;
        sh.abort = null;
        m.dirty = true;
        sshView.paint();
        sshView.inputs();
      }
    },
    // --- cliques ----------------------------------------------------------------------------------
    async onClick(e) {
      const sh = sshView.st();
      const k = e.target.closest('[data-k]');
      if (k && !k.disabled) { sshView.key(k.dataset.k); return; }
      const a = e.target.closest('[data-sa]');
      if (!a) return;
      const block = () => sh.blocks.get(a.dataset.key);
      switch (a.dataset.sa) {
        case 'probe': sshView.probe(true); break;
        case 'newkey': {
          const ok = await confirmDialog({ title: T('Gerar outra chave do painel?'), ok: T('Gerar'), danger: true,
            body: `<p>${T('A chave atual deixa de ser usada e o comando do passo 1 muda: rode o novo no servidor (ele troca a linha antiga do <code>authorized_keys</code>).')}</p>` });
          if (!ok) return;
          try { await api('/api/ssh/newkey', { method: 'POST', body: JSON.stringify({ confirm: true }) }); toast(T('Chave nova gerada.')); sshView.load(); } catch (err) { sshView.err(err); }
          break;
        }
        case 'disable': {
          const v = S.shInfo.ssh;
          const ok = await confirmDialog({ title: T('Desligar o SSH?'), ok: T('Desligar'), danger: true,
            body: `<ul class="cf-list"><li>${T('As sessões abertas fecham na hora (um comando rodando é interrompido).')}</li>
              <li>${T('Para religar basta o código do 2FA: a chave do painel continua autorizada no servidor.')}</li>
              <li>${T('Para cortar o acesso de vez, rode no servidor: <code>{0}</code>', [esc(v.revoke)])}</li></ul>` });
          if (!ok) return;
          try { await api('/api/ssh/disable', { method: 'POST', body: JSON.stringify({ confirm: true }) }); sshView.stop(); toast(T('SSH desligado.')); sshView.load(); } catch (err) { sshView.err(err); }
          break;
        }
        case 'end': {
          if (sshView.busy()) {
            const ok = await confirmDialog({ title: T('Encerrar a sessão?'), ok: T('Encerrar'), danger: true, body: `<p>${T('Há um comando rodando: ele é interrompido. Abrir de novo pede o código do app.')}</p>` });
            if (!ok) return;
          }
          try { await api('/api/ssh/lock', { method: 'POST', body: '{}' }); } catch (err) { if (err.message === 'login') return; }
          sshView.stop();
          sh.items = []; sh.sid = ''; sh.ver = -1; sh.lastId = 0; sh.blocks = new Map(); sh.state = null;
          sshView.load();
          break;
        }
        case 'reopen':
          try {
            await api('/api/ssh/open', { method: 'POST', body: '{}' });
            sh.state = null;
            sshView.poll();
            sshView.inputs();
          } catch (err) { sshView.err(err); }
          break;
        case 'log': sshView.showLog(); break;
        case 'wrap':
          sh.wrap = !sh.wrap;
          store.set('ssh-wrap', sh.wrap);
          a.setAttribute('aria-pressed', sh.wrap);
          $('#ssh-chat').classList.toggle('wrap', sh.wrap);
          break;
        case 'copy': {
          const b = block();
          if (b) navigator.clipboard.writeText(plainOut(b.out)).then(() => toast(T('Saída copiada.')), () => toast(T('Não consegui copiar.')));
          break;
        }
        case 'again': { const b = block(); if (b) sshView.run(b.cmd); break; }
        case 'explain': {
          const b = block();
          if (b) sshView.askAI(`${T('Explique a saída do comando `{0}`{1} e diga se há algo errado e o que fazer.', [b.cmd.slice(0, 300), b.code ? ` ${T('(saiu com código {0})', [b.code])}` : ''])}`, a.dataset.key);
          break;
        }
        case 'run-ai': sshView.run(a.parentElement.dataset.cmd); break;
        case 'edit-ai':
        case 'fill': {
          const ta = $('#ssh-in');
          sh.mode = 'cmd';
          sshView.inputs();
          ta.value = a.dataset.cmd || a.parentElement.dataset.cmd;
          sshView.grow();
          ta.focus();
          break;
        }
        case 'pick': sshView.pick(+a.dataset.i); break;
        default: break;
      }
    },
    async showLog() {
      let entries;
      try { entries = (await api('/api/ssh/log')).entries; } catch (e) { sshView.err(e); return; }
      const kind = { enable: ['ok', T('SSH ligado')], disable: ['off', T('SSH desligado')], open: ['info', T('sessão aberta')], close: ['off', T('sessão fechada')],
        fail: ['crit', T('código errado')], cmd: ['', T('comando')], input: ['', T('entrada')] };
      const rows = entries.map((x) => {
        const [lvl, label] = kind[x.kind] || ['', x.kind];
        const code = x.kind === 'cmd' ? (x.code == null ? '<span class="muted">—</span>' : x.code === 0 ? '<span class="sb-st ok">0</span>' : `<span class="sb-st bad">${x.code}</span>`) : '';
        return `<tr><td class="nowrap">${dt(x.t)}</td><td>${esc(x.by)}</td><td>${lvl ? badge(lvl, label) : `<span class="muted">${label}</span>`}</td>
          <td class="mono ssh-log-t">${x.uid === 0 ? '# ' : x.kind === 'cmd' ? '$ ' : ''}${esc(x.text || '')}</td><td>${code}</td><td class="muted nowrap">${esc(x.ip || '')}</td></tr>`;
      }).join('');
      const scrim = document.createElement('div');
      scrim.className = 'cf-scrim';
      const m = document.createElement('div');
      m.className = 'cf-modal';
      m.innerHTML = `<div class="card cf-card ssh-logcard" role="dialog" aria-modal="true" aria-labelledby="ssh-log-t">
        <div class="card-h"><h2 id="ssh-log-t">${icon('logs')}${T('Registro do SSH')}</h2><button class="icon-btn" type="button" data-close aria-label="${T('Fechar')}">${icon('x')}</button></div>
        <p class="muted">${T('Quem ligou, abriu sessão e cada comando com o código de saída (a saída não fica; senhas viram "(senha)"). Os {0} mais novos.', [entries.length])}</p>
        ${entries.length ? `<div class="table-wrap"><table><thead><tr><th>${T('Quando')}</th><th>${T('Quem')}</th><th>${T('O quê')}</th><th>${T('Comando')}</th><th>${T('Código de saída')}</th><th>IP</th></tr></thead><tbody>${rows}</tbody></table></div>` : `<div class="empty">${T('Nada registrado ainda.')}</div>`}</div>`;
      document.body.append(scrim, m);
      const close = () => { scrim.remove(); m.remove(); window.removeEventListener('keydown', onKey, true); };
      const onKey = (ev) => { if (ev.key === 'Escape') { ev.stopPropagation(); close(); } };
      window.addEventListener('keydown', onKey, true);
      scrim.addEventListener('click', close);
      m.addEventListener('click', (ev) => { if (ev.target === m || ev.target.closest('[data-close]')) close(); });
    },
  };

  const VIEWS = { overview, ssh: sshView, servers: serversView, cleanup: cleanupView, backups: backupsView, users: usersView, infos: infosView, ai: aiView, apps: appsView, traffic: trafficView, logs: logsView, system: systemView, limits: limitsView, notify: notifyView };

  // ------------------------------------------------------------------ eventos globais
  document.addEventListener('click', async (e) => {
    const eye = e.target.closest('[data-eye]');
    if (eye) { toggleEye(eye); return; }
    const cp = e.target.closest('[data-copy]');
    if (cp) {
      try { await navigator.clipboard.writeText(cp.dataset.copy); toast(T('Copiado.')); } catch { toast(T('Não deu para copiar: selecione e copie à mão.')); }
      return;
    }
    const dl = e.target.closest('[data-download]');
    if (dl) {
      const a = document.createElement('a');
      a.href = URL.createObjectURL(new Blob([`${T('VPServer: códigos de recuperação ({0})', [S.me ? S.me.user : ''])}\n\n${dl.dataset.download}\n`], { type: 'text/plain' }));
      a.download = T('vpserver-codigos-de-recuperacao.txt');
      a.click();
      setTimeout(() => URL.revokeObjectURL(a.href), 1000);
      return;
    }
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
      if (a === 'servers-menu') { if ($('.srv-menu')) closeDrawer(); else openServersMenu(act); return; }
      if (a === 'nav-menu') {
        const open = $('.nav-menu');
        const same = open && open.dataset.group === act.dataset.group;
        closeDrawer();
        if (!same) openNavMenu(act);
        return;
      }
      if (a === 'pick-server') { pickServer(v); return; }
      if (a === 'update') { location.reload(); return; }
      if (a === 'install') { closeDrawer(); closeInstallBar(); installApp(); return; }
      if (a === 'install-later') { closeInstallBar(); toast(T('Dá para instalar depois pelo menu Mais.')); return; }
      if (a === 'pause') { togglePause(v, act.dataset.pause === '1'); return; }
      if (a === 'lang') { setLang(v, act.dataset.save === '1'); return; }
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
    if (followAccountLang(S.me)) return;
    try { await loadFleet(); } catch { return; }
    renderShell();
    renderStrip();
    route();
    const t = S.me.twoFA || {};
    if (!t.enabled && !t.asked) setTimeout(recommend2FA, 1500);
    else setTimeout(offerInstall, 2500);
    await poll();
  }
  async function boot() {
    if (!window.uPlot) { await new Promise((r) => window.addEventListener('load', r, { once: true })); }
    let me;
    try { me = await api('/api/me'); } catch (e) { if (e.message !== 'login') showLogin(T('Não consegui falar com o painel.')); return; }
    if (followAccountLang(me)) return;
    if (me.mustChange) showSetup();
    else start();
  }
  boot();
})();
