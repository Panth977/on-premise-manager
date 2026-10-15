// Shared chrome (nav, theme, toc) + small diagram helpers used across pages.
const PAGES = [
  ['index.html', 'Overview'],
  ['data-model.html', 'Data model (schemas)'],
  ['expressions.html', 'Expressions'],
  ['cloud-frontend.html', 'Cloud frontend (routes)'],
  ['agent-gui.html', 'Agent GUI (routes)'],
  ['agent.html', 'The agent (internals)'],
  ['auth.html', 'Auth & access'],
  ['updates.html', 'Config, proposals & updates'],
  ['backups.html', 'Backups & rollback'],
  ['sync.html', 'Activities, status & transfers'],
  ['networking.html', 'Networking & ports'],
  ['api.html', 'Cloud API reference'],
  ['build.html', 'Prototype build plan'],
];

const SVGNS = 'http://www.w3.org/2000/svg';
function svgEl(tag, attrs = {}, text) {
  const e = document.createElementNS(SVGNS, tag);
  for (const [k, v] of Object.entries(attrs)) if (v !== undefined && v !== null) e.setAttribute(k, v);
  if (text !== undefined) e.textContent = text;
  return e;
}
function h(tag, attrs = {}, html) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
  if (html !== undefined) e.innerHTML = html;
  return e;
}

function initChrome() {
  const here = location.pathname.split('/').pop() || 'index.html';
  const side = document.querySelector('.side');
  if (side) {
    const nav = side.querySelector('nav');
    PAGES.forEach(([href, label], i) => {
      const a = h('a', { href }, `<span class="n">${String(i + 1).padStart(2, '0')}</span><span>${label}</span>`);
      if (href === here) a.classList.add('active');
      nav.appendChild(a);
    });
    const heads = document.querySelectorAll('main h2[id]');
    if (heads.length) {
      const toc = h('div', { class: 'toc' }, '<div>On this page</div>');
      heads.forEach((hd) => toc.appendChild(h('a', { href: '#' + hd.id }, hd.textContent)));
      side.appendChild(toc);
    }
    const tb = h('button', { class: 'theme-btn', type: 'button' }, 'Toggle theme');
    tb.onclick = () => {
      const root = document.documentElement;
      const dark = root.dataset.theme
        ? root.dataset.theme === 'dark'
        : matchMedia('(prefers-color-scheme: dark)').matches;
      root.dataset.theme = dark ? 'light' : 'dark';
      try { localStorage.setItem('opm2-theme', root.dataset.theme); } catch (e) {}
    };
    side.appendChild(tb);
    const mb = h('button', { class: 'btn menu-btn', type: 'button' }, 'Menu');
    mb.onclick = () => side.classList.toggle('open');
    document.body.appendChild(mb);
  }
  // prev / next
  const i = PAGES.findIndex(([p]) => p === here);
  const main = document.querySelector('main');
  if (main && i >= 0) {
    const nx = h('div', { class: 'next' });
    nx.appendChild(i > 0 ? h('a', { href: PAGES[i - 1][0] }, '← ' + PAGES[i - 1][1]) : h('span'));
    nx.appendChild(i < PAGES.length - 1 ? h('a', { href: PAGES[i + 1][0] }, PAGES[i + 1][1] + ' →') : h('span'));
    main.appendChild(nx);
  }
}

try {
  const t = localStorage.getItem('opm2-theme');
  if (t) document.documentElement.dataset.theme = t;
} catch (e) {}

// Segmented control: <div class="seg" data-seg="name"><button data-v="x">…</button></div>
function seg(el, onChange) {
  const btns = [...el.querySelectorAll('button')];
  const set = (v) => {
    btns.forEach((b) => b.classList.toggle('on', b.dataset.v === v));
    onChange(v);
  };
  btns.forEach((b) => (b.onclick = () => set(b.dataset.v)));
  const init = (btns.find((b) => b.classList.contains('on')) || btns[0]).dataset.v;
  set(init);
  return set;
}

// Sequence-diagram player. spec = {lanes:[{id,label,sub}], steps:[{from,to,label,title,detail}]}
function sequence(container, spec) {
  const laneW = 170, top = 56, rowH = 34;
  const W = Math.max(700, spec.lanes.length * laneW);
  const H = top + spec.steps.length * rowH + 24;
  const x = {};
  spec.lanes.forEach((l, i) => (x[l.id] = (W / spec.lanes.length) * (i + 0.5)));
  const svg = svgEl('svg', { viewBox: `0 0 ${W} ${H}`, role: 'img' });
  const uid = 'ah' + Math.random().toString(36).slice(2, 7);
  const defs = svgEl('defs');
  const mk = svgEl('marker', { id: uid, viewBox: '0 0 10 10', refX: 9, refY: 5, markerWidth: 7, markerHeight: 7, orient: 'auto-start-reverse' });
  mk.appendChild(svgEl('path', { d: 'M0,0 L10,5 L0,10 z', class: 'ah' }));
  defs.appendChild(mk);
  svg.appendChild(defs);
  spec.lanes.forEach((l) => {
    svg.appendChild(svgEl('rect', { x: x[l.id] - 74, y: 6, width: 148, height: 38, rx: 7, class: 'box ' + (l.cls || '') }));
    svg.appendChild(svgEl('text', { x: x[l.id], y: 22, 'text-anchor': 'middle', 'font-size': 12, 'font-weight': 600 }, l.label));
    if (l.sub) svg.appendChild(svgEl('text', { x: x[l.id], y: 37, 'text-anchor': 'middle', 'font-size': 10, class: 'muted' }, l.sub));
    svg.appendChild(svgEl('line', { x1: x[l.id], x2: x[l.id], y1: 44, y2: H - 8, class: 'lane-line' }));
  });
  const msgs = spec.steps.map((s, i) => {
    const y = top + i * rowH + 16;
    const g = svgEl('g', { class: 'msg' });
    if (s.from === s.to) {
      const x0 = x[s.from];
      g.appendChild(svgEl('path', { d: `M${x0},${y - 8} h34 v14 h-32`, 'marker-end': `url(#${uid})` }));
      g.appendChild(svgEl('text', { x: x0 + 40, y: y + 2, class: 'lbl' }, s.label));
    } else {
      const x1 = x[s.from], x2 = x[s.to];
      const dir = x2 > x1 ? 1 : -1;
      g.appendChild(svgEl('line', { x1: x1 + dir * 3, x2: x2 - dir * 3, y1: y, y2: y, 'marker-end': `url(#${uid})`, 'stroke-dasharray': s.reply ? '5 3' : null }));
      g.appendChild(svgEl('text', { x: (x1 + x2) / 2, y: y - 6, 'text-anchor': 'middle', class: 'lbl' }, s.label));
    }
    g.appendChild(svgEl('text', { x: 8, y: y + 4, 'font-size': 10, class: 'muted mono' }, String(i + 1)));
    g.style.cursor = 'pointer';
    g.onclick = () => go(i);
    svg.appendChild(g);
    return g;
  });
  const wrap = h('div', { class: 'diagram' });
  wrap.appendChild(svg);
  const ctl = h('div', { class: 'seq-ctl' });
  const prev = h('button', { class: 'btn', type: 'button' }, '← Prev');
  const play = h('button', { class: 'btn primary', type: 'button' }, '▶ Play');
  const next = h('button', { class: 'btn', type: 'button' }, 'Next →');
  const no = h('span', { class: 'stepno' });
  ctl.append(prev, play, next, no);
  const det = h('div', { class: 'seq-detail' });
  wrap.append(ctl, det);
  container.appendChild(wrap);
  let cur = 0, timer = null;
  function go(i) {
    cur = Math.max(0, Math.min(spec.steps.length - 1, i));
    msgs.forEach((m, j) => {
      m.classList.toggle('cur', j === cur);
      m.classList.toggle('past', j < cur);
    });
    const s = spec.steps[cur];
    det.innerHTML = `<b>${cur + 1}. ${s.title || s.label}</b>${s.detail || ''}`;
    no.textContent = `step ${cur + 1} / ${spec.steps.length}`;
    prev.disabled = cur === 0;
    next.disabled = cur === spec.steps.length - 1;
  }
  function stop() { clearInterval(timer); timer = null; play.textContent = '▶ Play'; }
  prev.onclick = () => { stop(); go(cur - 1); };
  next.onclick = () => { stop(); go(cur + 1); };
  play.onclick = () => {
    if (timer) return stop();
    if (cur === spec.steps.length - 1) go(0);
    play.textContent = '❚❚ Pause';
    timer = setInterval(() => (cur >= spec.steps.length - 1 ? stop() : go(cur + 1)), 2200);
  };
  go(0);
}

// Click-to-inspect diagrams: elements with data-k map to INFO[k] = {t, b}
function inspector(svgRoot, panel, info, initial) {
  const items = [...svgRoot.querySelectorAll('[data-k]')];
  function show(k) {
    items.forEach((it) => it.classList.toggle('sel', it.dataset.k === k));
    const d = info[k];
    panel.innerHTML = d ? `<h4>${d.t}</h4>${d.b}` : '<p class="empty">Click any box in the diagram.</p>';
  }
  items.forEach((it) => {
    it.classList.add('click');
    it.addEventListener('click', () => show(it.dataset.k));
  });
  show(initial);
}

document.addEventListener('DOMContentLoaded', initChrome);
