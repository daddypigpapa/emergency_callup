import { api, ApiError } from '/shared/api.js';
import { setText, clearChildren, el } from '/shared/text.js';
import { cellOf, cellBounds, cellCenter, cellKey, mergeRuns, runToBounds } from '/shared/grid.js';
import { pointInRings, boundsOfRings } from '/a/grid-edit.js';

// ---- Tabs ----
document.querySelectorAll('.setup-tabs .tab').forEach((btn) => {
  btn.addEventListener('click', () => {
    document.querySelectorAll('.setup-tabs .tab').forEach((b) => b.classList.remove('active'));
    document.querySelectorAll('.panel').forEach((p) => p.classList.remove('active'));
    btn.classList.add('active');
    document.getElementById(`panel-${btn.dataset.panel}`).classList.add('active');
    // The map is laid out while its panel is hidden (display:none), so
    // Leaflet measures it as zero-size until it's actually shown.
    if (btn.dataset.panel === 'areas' && editor.map) {
      setTimeout(() => { editor.map.invalidateSize(); redrawGridLines(); }, 0);
    }
    if (btn.dataset.panel === 'teamplan' && planMap) {
      setTimeout(() => { planMap.invalidateSize(); }, 0);
    }
  });
});

function guardAuth(err) {
  if (err instanceof ApiError && err.status === 401) {
    window.location.href = '/a/';
    return true;
  }
  return false;
}

// ---- Members / import ----
async function loadMembers() {
  try {
    const res = await api.get('/a/members');
    const wrap = document.getElementById('memberTableWrap');
    clearChildren(wrap);
    const table = el('table', {}, [
      el('thead', {}, [el('tr', {}, ['조', '이름', '소속', '휴대전화', '로그인ID', '활성', ''].map((h) => el('th', {}, [h])))]),
      el('tbody', {}, res.members.map((m) => el('tr', {}, [
        el('td', {}, [String(m.teamNo || '')]),
        el('td', {}, [m.name]),
        el('td', {}, [m.dept]),
        el('td', {}, [m.mobile]),
        el('td', {}, [m.loginId]),
        el('td', {}, [m.active ? '활성' : '비활성']),
        el('td', {}, [
          el('button', {
            class: 'action', type: 'button', onclick: async () => {
              await api.put(`/a/members/${m.id}`, { active: !m.active });
              await loadMembers();
            },
          }, [m.active ? '비활성화' : '활성화']),
          el('button', {
            class: 'action', type: 'button', onclick: async () => {
              const res2 = await api.post(`/a/members/${m.id}/password-reset`, {});
              alert(`임시 비밀번호: ${res2.password}\n(이 화면을 벗어나면 다시 볼 수 없습니다)`);
            },
          }, ['비밀번호 초기화']),
        ]),
      ]))),
    ]);
    wrap.appendChild(table);
  } catch (err) {
    if (!guardAuth(err)) throw err;
  }
}

document.getElementById('importPreviewBtn').addEventListener('click', async () => {
  const fileInput = document.getElementById('importFile');
  const resultEl = document.getElementById('importResult');
  clearChildren(resultEl);
  if (!fileInput.files[0]) return;
  const form = new FormData();
  form.append('file', fileInput.files[0]);
  const res = await api.raw('POST', '/a/members/import?mode=preview', { body: form });
  const data = await res.json();
  if (!res.ok) {
    resultEl.appendChild(el('p', { class: 'error-text' }, [data.msg || '가져오기 실패']));
    return;
  }
  const table = el('table', {}, [
    el('thead', {}, [el('tr', {}, ['행', '이름', '상태', '오류/경고'].map((h) => el('th', {}, [h])))]),
    el('tbody', {}, data.rows.map((r) => el('tr', {}, [
      el('td', {}, [String(r.rowNum)]),
      el('td', {}, [r.name]),
      el('td', {}, [r.status]),
      el('td', {}, [(r.errors || []).concat(r.warnings || []).join('; ')]),
    ]))),
  ]);
  resultEl.appendChild(table);
  resultEl.appendChild(el('button', {
    class: 'action', type: 'button', onclick: async () => {
      const commitRes = await api.post('/a/members/import?mode=commit', { token: data.token });
      let msg = `신규 ${commitRes.created}, 변경 ${commitRes.updated}, 동일 ${commitRes.unchanged}`;
      if (commitRes.newAccounts && commitRes.newAccounts.length) {
        msg += '\n\n신규 계정 초기 비밀번호:\n' + commitRes.newAccounts.map((a) => `${a.loginId}: ${a.password}`).join('\n');
      }
      alert(msg);
      clearChildren(resultEl);
      await loadMembers();
    },
  }, ['반영']));
});

// ---- Areas: map-based grid/circle editor (docs/SPEC_AREA_EDITOR.md §5.1) ----
const editor = {
  map: null,
  cellLayer: null,      // selected cells, merged into rectangles
  gridLineLayer: null,  // faint grid lines over the visible viewport
  dongLayer: null,      // administrative-dong boundary outlines
  otherAreasLayer: null, // ghost bbox of every other area, for reference
  size: 250,
  cells: new Map(),     // "i,j" -> {i,j}
  dongSelections: new Map(), // code -> Set of cell keys (for toggling a dong back off)
  tool: 'cell',         // 'cell' | 'drag' | 'dong'
  kind: 'grid',         // 'grid' | 'circle'
  editingId: null,      // null = creating a new area
  editingBBox: null,    // bbox of the area currently being edited, excluded from otherAreasLayer
};

const MAX_GRIDLINE_CELLS = 4000;

async function initAreaMap() {
  const cfg = await fetch('/api/v1/config').then((r) => r.json());
  editor.map = L.map('areaMapEl', { zoomControl: true });
  L.tileLayer(cfg.tileUrl, { attribution: cfg.tileAttribution, maxZoom: 19 }).addTo(editor.map);
  editor.map.setView([36.5, 127.8], 7);
  editor.cellLayer = L.layerGroup().addTo(editor.map);
  editor.gridLineLayer = L.layerGroup().addTo(editor.map);
  editor.dongLayer = L.layerGroup().addTo(editor.map);
  editor.otherAreasLayer = L.layerGroup().addTo(editor.map);
  editor.map.on('moveend zoomend', redrawGridLines);
  editor.map.on('click', onAreaMapClick);
  setupDragSelect();
}

function onAreaMapClick(e) {
  if (editor.kind === 'circle') {
    document.getElementById('areaLat').value = e.latlng.lat.toFixed(5);
    document.getElementById('areaLng').value = e.latlng.lng.toFixed(5);
    return;
  }
  if (editor.tool !== 'cell') return;
  const c = cellOf(editor.size, e.latlng.lat, e.latlng.lng);
  const key = cellKey(c);
  if (editor.cells.has(key)) editor.cells.delete(key);
  else editor.cells.set(key, c);
  redrawSelectedCells();
}

function redrawGridLines() {
  editor.gridLineLayer.clearLayers();
  if (editor.kind !== 'grid' || editor.map.getZoom() < 13) return;
  const b = editor.map.getBounds();
  const c0 = cellOf(editor.size, b.getSouth(), b.getWest());
  const c1 = cellOf(editor.size, b.getNorth(), b.getEast());
  const iCount = c1.i - c0.i + 2;
  const jCount = c1.j - c0.j + 2;
  if (iCount * jCount > MAX_GRIDLINE_CELLS) return; // too zoomed out to draw usefully
  const lines = [];
  for (let i = c0.i; i <= c1.i + 1; i++) {
    const south = cellBounds(editor.size, i, c0.j)[0];
    const north = cellBounds(editor.size, i, c1.j)[2];
    const lng = cellBounds(editor.size, i, c0.j)[1];
    lines.push([[south, lng], [north, lng]]);
  }
  for (let j = c0.j; j <= c1.j + 1; j++) {
    const west = cellBounds(editor.size, c0.i, j)[1];
    const east = cellBounds(editor.size, c1.i, j)[3];
    const lat = cellBounds(editor.size, c0.i, j)[0];
    lines.push([[lat, west], [lat, east]]);
  }
  L.polyline(lines, { color: '#111', weight: 1, opacity: 0.25, interactive: false }).addTo(editor.gridLineLayer);
}

function redrawSelectedCells() {
  editor.cellLayer.clearLayers();
  const cells = [...editor.cells.values()];
  const countEl = document.getElementById('gridCellCount');
  if (!cells.length) {
    setText(countEl, '선택된 칸: 0개');
    return;
  }
  for (const run of mergeRuns(cells)) {
    const b = runToBounds(editor.size, run);
    L.rectangle([[b[0], b[1]], [b[2], b[3]]], {
      color: '#111', weight: 1, fillColor: '#111', fillOpacity: 0.18, interactive: false,
    }).addTo(editor.cellLayer);
  }
  setText(countEl, `선택된 칸: ${cells.length}개` + (cells.length > 500 ? ' (많음 — 저장은 가능)' : ''));
}

function setTool(tool) {
  editor.tool = tool;
  document.querySelectorAll('.tool-btn').forEach((b) => b.classList.remove('active'));
  const idByTool = { cell: 'toolCellBtn', drag: 'toolDragBtn', dong: 'toolDongBtn' };
  document.getElementById(idByTool[tool]).classList.add('active');
  document.getElementById('dongSearch').hidden = tool !== 'dong';
  if (editor.map) editor.map.dragging.enable();
}
document.getElementById('toolCellBtn').addEventListener('click', () => setTool('cell'));
document.getElementById('toolDragBtn').addEventListener('click', () => setTool('drag'));
document.getElementById('toolDongBtn').addEventListener('click', () => setTool('dong'));
document.getElementById('gridClearBtn').addEventListener('click', () => {
  editor.cells.clear();
  editor.dongSelections.clear();
  editor.dongLayer.clearLayers();
  clearChildren(document.getElementById('dongResults'));
  redrawSelectedCells();
});

// Drag-select: click-drag draws a rubber-band box; every cell it overlaps
// is added (Shift+drag removes them instead). Leaflet's own panning is
// disabled for the duration so the drag moves the selection box, not the map.
function setupDragSelect() {
  const container = editor.map.getContainer();
  let dragging = false;
  let startLatLng = null;
  let rectLayer = null;

  container.addEventListener('pointerdown', (e) => {
    if (editor.tool !== 'drag' || editor.kind !== 'grid') return;
    dragging = true;
    startLatLng = editor.map.mouseEventToLatLng(e);
    editor.map.dragging.disable();
  });
  container.addEventListener('pointermove', (e) => {
    if (!dragging) return;
    const cur = editor.map.mouseEventToLatLng(e);
    if (rectLayer) editor.map.removeLayer(rectLayer);
    rectLayer = L.rectangle(L.latLngBounds(startLatLng, cur), {
      color: '#111', weight: 1, dashArray: '4,3', fill: false, interactive: false,
    }).addTo(editor.map);
  });
  window.addEventListener('pointerup', (e) => {
    if (!dragging) return;
    dragging = false;
    editor.map.dragging.enable();
    if (rectLayer) { editor.map.removeLayer(rectLayer); rectLayer = null; }
    const cur = editor.map.mouseEventToLatLng(e);
    const bounds = L.latLngBounds(startLatLng, cur);
    const c0 = cellOf(editor.size, bounds.getSouth(), bounds.getWest());
    const c1 = cellOf(editor.size, bounds.getNorth(), bounds.getEast());
    const remove = e.shiftKey;
    let n = 0;
    for (let i = c0.i; i <= c1.i && n < 5000; i++) {
      for (let j = c0.j; j <= c1.j && n < 5000; j++, n++) {
        const c = { i, j };
        if (remove) editor.cells.delete(cellKey(c));
        else editor.cells.set(cellKey(c), c);
      }
    }
    redrawSelectedCells();
  });
}

document.getElementById('gridSize').addEventListener('change', (e) => {
  if (editor.cells.size > 0 && !confirm('격자 크기를 바꾸면 선택된 칸이 모두 초기화됩니다. 계속할까요?')) {
    e.target.value = String(editor.size);
    return;
  }
  editor.size = Number(e.target.value);
  editor.cells.clear();
  editor.dongSelections.clear();
  editor.dongLayer.clearLayers();
  clearChildren(document.getElementById('dongResults'));
  redrawSelectedCells();
  redrawGridLines();
});

document.getElementById('dongSearchBtn').addEventListener('click', async () => {
  const q = document.getElementById('dongQuery').value.trim();
  if (!q) return;
  const resultsEl = document.getElementById('dongResults');
  clearChildren(resultsEl);
  try {
    const res = await api.get(`/a/geo/admin?q=${encodeURIComponent(q)}`);
    if (!res.results.length) {
      resultsEl.appendChild(el('p', { class: 'hint' }, ['검색 결과가 없습니다.']));
      return;
    }
    for (const dong of res.results) {
      const item = el('div', { class: 'dong-item' }, [dong.full || dong.name]);
      if (editor.dongSelections.has(dong.code)) item.classList.add('selected');
      item.addEventListener('click', () => toggleDong(dong, item));
      resultsEl.appendChild(item);
    }
  } catch (err) {
    resultsEl.appendChild(el('p', { class: 'hint' }, [err.message || '검색 실패']));
  }
});

function toggleDong(dong, itemEl) {
  if (editor.dongSelections.has(dong.code)) {
    for (const key of editor.dongSelections.get(dong.code)) editor.cells.delete(key);
    editor.dongSelections.delete(dong.code);
    itemEl.classList.remove('selected');
  } else {
    const cellKeys = new Set();
    const [south, west, north, east] = boundsOfRings(dong.rings);
    const c0 = cellOf(editor.size, south, west);
    const c1 = cellOf(editor.size, north, east);
    for (let i = c0.i; i <= c1.i; i++) {
      for (let j = c0.j; j <= c1.j; j++) {
        const [lat, lng] = cellCenter(editor.size, i, j);
        if (pointInRings(lat, lng, dong.rings)) {
          const c = { i, j };
          editor.cells.set(cellKey(c), c);
          cellKeys.add(cellKey(c));
        }
      }
    }
    editor.dongSelections.set(dong.code, cellKeys);
    for (const ring of dong.rings) {
      L.polyline([...ring, ring[0]], { color: '#111', weight: 2, dashArray: '4,3', interactive: false }).addTo(editor.dongLayer);
    }
    itemEl.classList.add('selected');
  }
  redrawSelectedCells();
}

function onKindChange() {
  editor.kind = document.getElementById('areaKind').value;
  document.getElementById('gridFields').hidden = editor.kind !== 'grid';
  document.getElementById('circleFields').hidden = editor.kind !== 'circle';
  redrawGridLines();
}
document.getElementById('areaKind').addEventListener('change', onKindChange);

async function loadAreas() {
  try {
    const res = await api.get('/a/areas');
    const wrap = document.getElementById('areaListWrap');
    clearChildren(wrap);
    const table = el('table', {}, [
      el('thead', {}, [el('tr', {}, ['이름', '종류', '크기'].map((h) => el('th', {}, [h])))]),
      el('tbody', {}, res.areas.map((a) => el('tr', {}, [
        el('td', {}, [a.name]),
        el('td', {}, [{ grid: '격자', circle: '원', polygon: '다각형' }[a.kind] || a.kind]),
        el('td', {}, [
          a.kind === 'grid' ? `${a.size}m · ${(a.cells || []).length}칸`
            : a.kind === 'circle' ? `${a.r}m`
              : `${(a.polygon || []).length}점`,
        ]),
        el('td', {}, [
          el('button', { class: 'action', type: 'button', onclick: () => loadAreaIntoForm(a) }, ['편집']),
          el('button', { class: 'action', type: 'button', onclick: () => deleteArea(a) }, ['삭제']),
        ]),
      ]))),
    ]);
    wrap.appendChild(table);

    editor.otherAreasLayer.clearLayers();
    for (const a of res.areas) {
      if (a.id === editor.editingId || !a.bbox) continue;
      L.rectangle([[a.bbox[0], a.bbox[1]], [a.bbox[2], a.bbox[3]]], {
        color: '#111', weight: 1, dashArray: '2,2', fill: false, interactive: false,
      }).addTo(editor.otherAreasLayer);
    }
  } catch (err) {
    if (!guardAuth(err)) throw err;
  }
}

function resetAreaForm() {
  editor.editingId = null;
  document.getElementById('areaFormTitle').textContent = '새 지역';
  document.getElementById('areaName').value = '';
  document.getElementById('areaCancelBtn').hidden = true;
  editor.cells.clear();
  editor.dongSelections.clear();
  editor.dongLayer.clearLayers();
  clearChildren(document.getElementById('dongResults'));
  redrawSelectedCells();
  loadAreas();
}
document.getElementById('areaCancelBtn').addEventListener('click', resetAreaForm);

function loadAreaIntoForm(a) {
  editor.editingId = a.id;
  document.getElementById('areaFormTitle').textContent = `지역 수정: ${a.name}`;
  document.getElementById('areaName').value = a.name;
  document.getElementById('areaCancelBtn').hidden = false;
  document.getElementById('areaKind').value = a.kind === 'polygon' ? 'grid' : a.kind;
  onKindChange();

  editor.cells.clear();
  editor.dongSelections.clear();
  editor.dongLayer.clearLayers();
  clearChildren(document.getElementById('dongResults'));

  if (a.kind === 'grid') {
    editor.size = a.size;
    document.getElementById('gridSize').value = String(a.size);
    for (const [i, j] of a.cells || []) editor.cells.set(cellKey({ i, j }), { i, j });
    redrawSelectedCells();
  } else if (a.kind === 'circle') {
    document.getElementById('areaLat').value = a.lat;
    document.getElementById('areaLng').value = a.lng;
    document.getElementById('areaRadius').value = a.r;
  }
  if (a.bbox) editor.map.fitBounds([[a.bbox[0], a.bbox[1]], [a.bbox[2], a.bbox[3]]], { padding: [20, 20] });
  loadAreas();
}

async function deleteArea(a) {
  if (!confirm(`"${a.name}" 지역을 삭제할까요?`)) return;
  try {
    await api.del(`/a/areas/${a.id}`);
    if (editor.editingId === a.id) resetAreaForm();
    else await loadAreas();
  } catch (err) {
    alert(err.message || '삭제 실패');
  }
}

document.getElementById('areaSaveBtn').addEventListener('click', async () => {
  const name = document.getElementById('areaName').value.trim();
  const statusEl = document.getElementById('areaFormStatus');
  setText(statusEl, '');
  if (!name) { setText(statusEl, '이름을 입력하세요.'); return; }

  let body;
  if (editor.kind === 'grid') {
    const cells = [...editor.cells.values()].map((c) => [c.i, c.j]);
    if (!cells.length) { setText(statusEl, '칸을 하나 이상 선택하세요.'); return; }
    body = { name, kind: 'grid', size: editor.size, cells };
  } else {
    const lat = Number(document.getElementById('areaLat').value);
    const lng = Number(document.getElementById('areaLng').value);
    const r = Number(document.getElementById('areaRadius').value);
    if (!lat || !lng || !r) { setText(statusEl, '위도·경도·반경을 입력하세요.'); return; }
    body = { name, kind: 'circle', lat, lng, r };
  }

  try {
    if (editor.editingId) await api.put(`/a/areas/${editor.editingId}`, body);
    else await api.post('/a/areas', body);
    setText(statusEl, '저장했습니다.');
    resetAreaForm();
  } catch (err) {
    setText(statusEl, err.message || '저장 실패');
  }
});

// ---- Team plans (docs/SPEC_AREA_EDITOR.md §5.2, §5.3) ----
let planMap = null;
let planLayer = null;
const plan = {
  teams: new Map(), // no -> {mission, areaId, rally:{lat,lng,addr}|null, checkpoints:[{name,lat,lng,addr,r}]}
  areas: [],
  expanded: null,
  dirty: false,
  mapClickHandler: null,
};

async function initPlanMap() {
  const cfg = await fetch('/api/v1/config').then((r) => r.json());
  planMap = L.map('planMapEl', { zoomControl: true });
  L.tileLayer(cfg.tileUrl, { attribution: cfg.tileAttribution, maxZoom: 19 }).addTo(planMap);
  planMap.setView([36.5, 127.8], 7);
  planLayer = L.layerGroup().addTo(planMap);
}

function armMapPicker(cb) {
  if (plan.mapClickHandler) planMap.off('click', plan.mapClickHandler);
  const handler = (e) => {
    plan.mapClickHandler = null;
    cb(e.latlng.lat, e.latlng.lng);
  };
  plan.mapClickHandler = handler;
  planMap.once('click', handler);
}

function rallyIcon() { return L.divIcon({ className: 'rally-icon', iconSize: [14, 12] }); }
function cpIcon(n) { return L.divIcon({ className: 'cp-icon', iconSize: [18, 18], html: String(n) }); }

// Draws the expanded team's area + rally + checkpoints, and fits the map
// to all of them (docs/SPEC_AREA_EDITOR.md §5.2 "지도에서").
function focusTeamOnMap(no) {
  planLayer.clearLayers();
  const t = plan.teams.get(no);
  if (!t || !planMap) return;
  const points = [];
  const area = plan.areas.find((a) => a.id === t.areaId);
  if (area) {
    if (area.kind === 'circle') {
      L.circle([area.lat, area.lng], { radius: area.r, color: '#111', weight: 2, fillColor: '#111', fillOpacity: 0.06, interactive: false }).addTo(planLayer);
    } else if (area.kind === 'grid' && area.cells && area.cells.length) {
      const cells = area.cells.map(([i, j]) => ({ i, j }));
      for (const run of mergeRuns(cells)) {
        const b = runToBounds(area.size, run);
        L.rectangle([[b[0], b[1]], [b[2], b[3]]], { color: '#111', weight: 1, fillColor: '#111', fillOpacity: 0.08, interactive: false }).addTo(planLayer);
      }
    } else if (area.kind === 'polygon' && area.polygon) {
      L.polygon(area.polygon, { color: '#111', weight: 2, fillColor: '#111', fillOpacity: 0.06, interactive: false }).addTo(planLayer);
    }
    if (area.bbox) points.push([area.bbox[0], area.bbox[1]], [area.bbox[2], area.bbox[3]]);
  }

  let rallyPt = t.rally;
  if (!rallyPt && area && area.nav) rallyPt = { lat: area.nav[0], lng: area.nav[1] };
  if (rallyPt) {
    L.marker([rallyPt.lat, rallyPt.lng], { icon: rallyIcon(), interactive: false }).addTo(planLayer);
    points.push([rallyPt.lat, rallyPt.lng]);
  }

  const cpPts = [];
  t.checkpoints.forEach((cp, idx) => {
    if (cp.lat == null) return;
    L.marker([cp.lat, cp.lng], { icon: cpIcon(idx + 1), interactive: false }).addTo(planLayer);
    points.push([cp.lat, cp.lng]);
    cpPts.push([cp.lat, cp.lng]);
  });
  if (rallyPt && cpPts.length) {
    L.polyline([[rallyPt.lat, rallyPt.lng], ...cpPts], { color: '#111', weight: 1, dashArray: '4,3', interactive: false }).addTo(planLayer);
  }

  if (points.length) planMap.fitBounds(points, { padding: [30, 30], maxZoom: 17 });
}

function markPlanDirty() { plan.dirty = true; }

// Shared widget for both the rally point and each checkpoint (SPEC_AREA_EDITOR.md
// §5.3): road-name+basic-number lookup (confirmed explicitly, to avoid acting on
// a typo) or a single map click, either way reported back via onChange.
function createLocationPicker(initial, onChange) {
  let current = initial || null;
  let pending = null;
  const queryInput = el('input', { placeholder: '도로명 + 기초번호 (예: 공평로 88)' }, []);
  const lookupBtn = el('button', { class: 'action', type: 'button' }, ['조회']);
  const pickBtn = el('button', { class: 'action', type: 'button' }, ['지도에서 클릭']);
  const resultEl = el('p', { class: 'hint' }, []);
  const confirmBtn = el('button', { class: 'action', type: 'button', hidden: true }, ['이 위치로']);

  function render() {
    setText(resultEl, current ? `확정: ${current.addr || '(주소 없음)'} (${current.lat.toFixed(5)}, ${current.lng.toFixed(5)})` : '');
  }
  render();

  lookupBtn.addEventListener('click', async () => {
    const q = queryInput.value.trim();
    if (!q) return;
    try {
      const res = await api.get(`/a/geo/geocode?q=${encodeURIComponent(q)}`);
      pending = { lat: res.lat, lng: res.lng, addr: res.matched };
      setText(resultEl, `조회 결과: ${res.matched} (${res.lat.toFixed(5)}, ${res.lng.toFixed(5)})`);
      confirmBtn.hidden = false;
    } catch (err) {
      setText(resultEl, err.message || '조회 실패');
      confirmBtn.hidden = true;
    }
  });
  confirmBtn.addEventListener('click', () => {
    if (!pending) return;
    current = pending;
    pending = null;
    confirmBtn.hidden = true;
    render();
    onChange(current);
  });
  pickBtn.addEventListener('click', () => {
    setText(resultEl, '지도를 클릭하세요...');
    armMapPicker((lat, lng) => {
      current = { lat, lng, addr: '지도 지정' };
      render();
      onChange(current);
    });
  });

  return el('div', { class: 'loc-picker' }, [
    el('div', { class: 'field-row' }, [queryInput, lookupBtn, pickBtn]),
    resultEl, confirmBtn,
  ]);
}

function renderTeamPlanForm(no, t) {
  const missionInput = el('input', { type: 'text', value: t.mission || '', placeholder: '임무', list: 'missionPresetList' }, []);
  missionInput.addEventListener('input', () => { t.mission = missionInput.value; markPlanDirty(); });

  const areaSelect = el('select', {}, [
    el('option', { value: '' }, ['(없음)']),
    ...plan.areas.map((a) => {
      const opt = el('option', { value: a.id }, [a.name]);
      if (a.id === t.areaId) opt.selected = true;
      return opt;
    }),
  ]);
  areaSelect.addEventListener('change', () => {
    t.areaId = Number(areaSelect.value) || 0;
    markPlanDirty();
    focusTeamOnMap(no);
  });

  const rallyGroupName = `rally-${no}`;
  // Not passed via el()'s attrs: setAttribute('checked', false) still marks a
  // radio checked (HTML boolean attributes are presence-only), so this has
  // to be a property assignment after creation, matching how <option
  // selected> is handled elsewhere in this file.
  const autoRadio = el('input', { type: 'radio', name: rallyGroupName }, []);
  const manualRadio = el('input', { type: 'radio', name: rallyGroupName }, []);
  autoRadio.checked = t.rally == null;
  manualRadio.checked = t.rally != null;
  const rallyWidgetWrap = el('div', {}, []);
  function refreshRallyWidget() {
    clearChildren(rallyWidgetWrap);
    if (manualRadio.checked) {
      if (!t.rally) t.rally = null; // stays null until the picker confirms a value
      rallyWidgetWrap.appendChild(createLocationPicker(t.rally, (val) => {
        t.rally = val; markPlanDirty(); focusTeamOnMap(no);
      }));
    } else {
      t.rally = null;
      rallyWidgetWrap.appendChild(el('p', { class: 'hint' }, ['자동: 임무지역 중심']));
    }
  }
  autoRadio.addEventListener('change', () => { markPlanDirty(); refreshRallyWidget(); focusTeamOnMap(no); });
  manualRadio.addEventListener('change', () => { markPlanDirty(); refreshRallyWidget(); });
  refreshRallyWidget();

  const cpListWrap = el('div', {}, []);
  function refreshCpList() {
    clearChildren(cpListWrap);
    t.checkpoints.forEach((cp, idx) => {
      const nameInput = el('input', { type: 'text', value: cp.name || '', placeholder: '이름' }, []);
      nameInput.addEventListener('input', () => { cp.name = nameInput.value; markPlanDirty(); });
      const rInput = el('input', { type: 'number', value: String(cp.r || 50), placeholder: '반경(m)' }, []);
      rInput.addEventListener('input', () => { cp.r = Number(rInput.value) || 50; markPlanDirty(); });
      const upBtn = el('button', { class: 'action', type: 'button' }, ['↑']);
      upBtn.disabled = idx === 0;
      upBtn.addEventListener('click', () => {
        [t.checkpoints[idx - 1], t.checkpoints[idx]] = [t.checkpoints[idx], t.checkpoints[idx - 1]];
        markPlanDirty(); refreshCpList(); focusTeamOnMap(no);
      });
      const downBtn = el('button', { class: 'action', type: 'button' }, ['↓']);
      downBtn.disabled = idx === t.checkpoints.length - 1;
      downBtn.addEventListener('click', () => {
        [t.checkpoints[idx + 1], t.checkpoints[idx]] = [t.checkpoints[idx], t.checkpoints[idx + 1]];
        markPlanDirty(); refreshCpList(); focusTeamOnMap(no);
      });
      const delBtn = el('button', { class: 'action', type: 'button' }, ['삭제']);
      delBtn.addEventListener('click', () => {
        t.checkpoints.splice(idx, 1);
        markPlanDirty(); refreshCpList(); focusTeamOnMap(no);
      });

      cpListWrap.appendChild(el('div', { class: 'cp-item' }, [
        el('div', { class: 'field-row' }, [el('span', {}, [`${idx + 1}.`]), nameInput, rInput, upBtn, downBtn, delBtn]),
        createLocationPicker(cp.lat != null ? cp : null, (val) => {
          cp.lat = val.lat; cp.lng = val.lng; cp.addr = val.addr;
          markPlanDirty(); focusTeamOnMap(no);
        }),
      ]));
    });
  }
  refreshCpList();
  const addCpBtn = el('button', { class: 'action', type: 'button' }, ['체크포인트 추가']);
  addCpBtn.addEventListener('click', () => {
    if (t.checkpoints.length >= 10) { alert('체크포인트는 조당 최대 10개입니다.'); return; }
    t.checkpoints.push({ name: `지점${t.checkpoints.length + 1}`, lat: null, lng: null, addr: '', r: 50 });
    markPlanDirty();
    refreshCpList();
  });

  return el('div', { class: 'plan-form' }, [
    el('label', {}, ['임무']), missionInput,
    el('label', {}, ['임무지역']), areaSelect,
    el('label', {}, ['집결지']),
    el('div', { class: 'field-row' }, [
      autoRadio, el('span', {}, ['자동(임무지역 중심)']), manualRadio, el('span', {}, ['직접 지정']),
    ]),
    rallyWidgetWrap,
    el('label', {}, ['체크포인트 (최대 10개)']),
    cpListWrap, addCpBtn,
  ]);
}

function renderTeamPlanList() {
  const wrap = document.getElementById('teamPlanList');
  clearChildren(wrap);
  for (const [no, t] of plan.teams) {
    const isOpen = plan.expanded === no;
    const header = el('div', { class: 'plan-row-header' + (isOpen ? ' open' : '') }, [`${no}조 · ${t.mission || '(미부여)'}`]);
    header.addEventListener('click', () => {
      plan.expanded = isOpen ? null : no;
      renderTeamPlanList();
      if (!isOpen) focusTeamOnMap(no);
    });
    const row = el('div', { class: 'plan-row' }, [header]);
    if (isOpen) row.appendChild(renderTeamPlanForm(no, t));
    wrap.appendChild(row);
  }
}

async function loadTeamPlans() {
  try {
    const [plansRes, areasRes, missionPresets] = await Promise.all([
      api.get('/a/team-plans'), api.get('/a/areas'), api.get('/a/presets?kind=mission'),
    ]);
    plan.areas = areasRes.areas;
    plan.teams.clear();
    for (const t of plansRes.teams) {
      plan.teams.set(t.no, {
        mission: t.mission || '',
        areaId: t.areaId || 0,
        rally: t.rally && t.rally.auto === false ? { lat: t.rally.lat, lng: t.rally.lng, addr: t.rally.addr || '' } : null,
        checkpoints: (t.checkpoints || []).map((cp) => ({ name: cp.name, lat: cp.lat, lng: cp.lng, addr: cp.addr || '', r: cp.r })),
      });
    }
    plan.dirty = false;
    renderTeamPlanList();

    const datalist = document.getElementById('missionPresetList');
    clearChildren(datalist);
    for (const p of missionPresets.presets) datalist.appendChild(el('option', { value: p.text }, []));
  } catch (err) {
    if (!guardAuth(err)) throw err;
  }
}

document.getElementById('teamPlanSaveAllBtn').addEventListener('click', async () => {
  const teams = [...plan.teams.entries()].map(([no, t]) => ({
    no,
    mission: t.mission || '',
    areaId: t.areaId || 0,
    rally: t.rally ? { lat: t.rally.lat, lng: t.rally.lng, addr: t.rally.addr || '' } : null,
    checkpoints: t.checkpoints
      .filter((cp) => cp.lat != null)
      .map((cp) => ({ name: cp.name, lat: cp.lat, lng: cp.lng, addr: cp.addr || '', r: cp.r || 50 })),
  }));
  const statusEl = document.getElementById('teamPlanStatus');
  clearChildren(statusEl);
  try {
    const res = await api.put('/a/team-plans', { teams });
    plan.dirty = false;
    const time = new Date(res.t).toLocaleTimeString('ko-KR', { timeZone: 'Asia/Seoul' });
    statusEl.appendChild(el('p', {}, [`저장됨 ${time}`]));
    for (const w of res.warnings || []) statusEl.appendChild(el('p', { class: 'hint' }, [w]));
  } catch (err) {
    statusEl.appendChild(el('p', { class: 'hint' }, [err.message || '저장 실패']));
  }
});

window.addEventListener('beforeunload', (e) => {
  if (!plan.dirty) return;
  e.preventDefault();
  e.returnValue = '';
});

// ---- Presets ----
async function loadPresets() {
  try {
    const wrap = document.getElementById('presetListWrap');
    clearChildren(wrap);
    for (const kind of ['incident_type', 'message', 'mission']) {
      const res = await api.get(`/a/presets?kind=${kind}`);
      wrap.appendChild(el('h3', {}, [kind]));
      const table = el('table', {}, [
        el('tbody', {}, res.presets.map((p) => el('tr', {}, [
          el('td', {}, [p.text]),
          el('td', {}, [
            el('button', {
              class: 'action', type: 'button', onclick: async () => {
                await api.del(`/a/presets/${p.id}`);
                await loadPresets();
              },
            }, ['삭제']),
          ]),
        ]))),
      ]);
      wrap.appendChild(table);
    }
  } catch (err) {
    if (!guardAuth(err)) throw err;
  }
}

document.getElementById('presetCreateBtn').addEventListener('click', async () => {
  const kind = document.getElementById('presetKind').value;
  const text = document.getElementById('presetText').value.trim();
  if (!text) return;
  await api.post('/a/presets', { kind, text, sort: 0 });
  document.getElementById('presetText').value = '';
  await loadPresets();
});

// ---- Admins ----
async function loadAdmins() {
  try {
    const res = await api.get('/a/admins');
    const wrap = document.getElementById('adminListWrap');
    clearChildren(wrap);
    const table = el('table', {}, [
      el('thead', {}, [el('tr', {}, ['로그인ID', '이름', '역할', '활성', ''].map((h) => el('th', {}, [h])))]),
      el('tbody', {}, res.admins.map((a) => el('tr', {}, [
        el('td', {}, [a.loginId]),
        el('td', {}, [a.name]),
        el('td', {}, [a.role]),
        el('td', {}, [a.active ? '활성' : '비활성']),
        el('td', {}, [
          el('button', {
            class: 'action', type: 'button', onclick: async () => {
              try {
                await api.put(`/a/admins/${a.id}`, { active: !a.active });
                await loadAdmins();
              } catch (err) {
                alert(err.message || '실패');
              }
            },
          }, [a.active ? '비활성화' : '활성화']),
        ]),
      ]))),
    ]);
    wrap.appendChild(table);
  } catch (err) {
    if (!guardAuth(err)) throw err;
  }
}

document.getElementById('adminCreateBtn').addEventListener('click', async () => {
  const loginId = document.getElementById('adminLoginId').value.trim();
  const name = document.getElementById('adminName').value.trim();
  const password = document.getElementById('adminPw').value;
  const role = document.getElementById('adminRole').value;
  if (!loginId || !name || !password) return;
  try {
    await api.post('/a/admins', { loginId, name, password, role, dept: '' });
    await loadAdmins();
  } catch (err) {
    alert(err.message || '등록 실패');
  }
});

// ---- Map (VWorld tile key) ----
async function loadTileKey() {
  try {
    const res = await api.get('/a/settings/tile-key');
    document.getElementById('tileKeyInput').value = res.key || '';
    setText(
      document.getElementById('tileKeyStatus'),
      res.source === 'admin' ? '현재 관리자가 등록한 키를 쓰고 있습니다.' : '현재 서버 기본 설정값을 쓰고 있습니다.',
    );
  } catch (err) {
    if (!guardAuth(err)) throw err;
  }
}

document.getElementById('tileKeySaveBtn').addEventListener('click', async () => {
  const key = document.getElementById('tileKeyInput').value.trim();
  const statusEl = document.getElementById('tileKeyStatus');
  if (!key) return;
  try {
    await api.put('/a/settings/tile-key', { key });
    setText(statusEl, '저장했습니다. 지도 화면을 새로고침하면 바로 적용됩니다.');
  } catch (err) {
    setText(statusEl, err.message || '저장 실패');
  }
});

// ---- Events ----
document.getElementById('eventsLoadBtn').addEventListener('click', async () => {
  const id = document.getElementById('eventsIncidentId').value;
  if (!id) return;
  try {
    const res = await api.get(`/a/events?incident=${id}`);
    const wrap = document.getElementById('eventsListWrap');
    clearChildren(wrap);
    const table = el('table', {}, [
      el('thead', {}, [el('tr', {}, ['시각', '주체', '종류'].map((h) => el('th', {}, [h])))]),
      el('tbody', {}, (res.events || []).map((e) => el('tr', {}, [
        el('td', {}, [new Date(e.ts).toLocaleString('ko-KR', { timeZone: 'Asia/Seoul' })]),
        el('td', {}, [e.actor]),
        el('td', {}, [e.type]),
      ]))),
    ]);
    wrap.appendChild(table);
  } catch (err) {
    if (!guardAuth(err)) throw err;
  }
});

(async function main() {
  await loadMembers();
  await initAreaMap();
  await loadAreas();
  await initPlanMap();
  await loadTeamPlans();
  await loadPresets();
  await loadTileKey();
  await loadAdmins();
})();
