import { api, ApiError } from '/shared/api.js';
import { syncServerTime, serverNow, startClock } from '/shared/clock.js';
import { setText, clearChildren, el } from '/shared/text.js';

const STATE_NAMES = ['미접속', '접속', '위치거부', '이동중', '응소', '이탈'];
const STATE_KEYS = ['notified', 'logged_in', 'loc_denied', 'moving', 'arrived', 'left'];

const els = {
  loginView: document.getElementById('loginView'),
  boardView: document.getElementById('boardView'),
  loginForm: document.getElementById('loginForm'),
  loginError: document.getElementById('loginError'),
  clockDate: document.getElementById('clockDate'),
  clockTime: document.getElementById('clockTime'),
  connStatus: document.getElementById('connStatus'),
  incidentType: document.getElementById('incidentType'),
  incidentMessage: document.getElementById('incidentMessage'),
  editBtn: document.getElementById('editBtn'),
  smsBtn: document.getElementById('smsBtn'),
  closeBtn: document.getElementById('closeBtn'),
  newIncidentBtn: document.getElementById('newIncidentBtn'),
  teamsGrid: document.getElementById('teamsGrid'),
  mapEl: document.getElementById('mapEl'),
  teamSheet: document.getElementById('teamSheet'),
  teamSheetTitle: document.getElementById('teamSheetTitle'),
  teamMissionInput: document.getElementById('teamMissionInput'),
  teamAreaSelect: document.getElementById('teamAreaSelect'),
  teamMissionApply: document.getElementById('teamMissionApply'),
  teamRosterList: document.getElementById('teamRosterList'),
  teamSheetClose: document.getElementById('teamSheetClose'),
  personCard: document.getElementById('personCard'),
  personCardBody: document.getElementById('personCardBody'),
  personCardClose: document.getElementById('personCardClose'),
  incidentSheet: document.getElementById('incidentSheet'),
  incidentSheetTitle: document.getElementById('incidentSheetTitle'),
  incidentSheetBody: document.getElementById('incidentSheetBody'),
  incidentSheetClose: document.getElementById('incidentSheetClose'),
};

const board = {
  epoch: null,
  seq: 0,
  incident: null,
  teams: [],
  areas: [],
  people: new Map(), // id -> [id,name,dept,team,officeTel,mobileMasked,note,missionOv,areaOv]
  m: new Map(), // id -> [id,stateCode,lat5,lng5,acc,elapsed,flags]
  cfg: null,
  activeTeam: null,
  markers: new Map(), // id -> L.CircleMarker
  pollTimer: null,
  connLostSince: null,
};

function showOnly(view) {
  for (const v of [els.loginView, els.boardView]) v.hidden = v !== view;
}

// ---------------------------------------------------------------- Login
els.loginForm.addEventListener('submit', async (e) => {
  e.preventDefault();
  setText(els.loginError, '');
  const id = document.getElementById('loginId').value.trim();
  const pw = document.getElementById('loginPw').value;
  try {
    const res = await api.post('/a/login', { id, pw });
    syncServerTime(res.t);
    await boot();
  } catch (err) {
    setText(els.loginError, err instanceof ApiError ? (err.message || '로그인 실패') : '오류가 발생했습니다.');
  }
});

// ---------------------------------------------------------------- Map
let map, layerGroup;
function initMap() {
  map = L.map(els.mapEl, { preferCanvas: true, zoomControl: true });
  L.tileLayer(board.cfg.tileUrl, { attribution: board.cfg.tileAttribution, maxZoom: 19 }).addTo(map);
  layerGroup = L.layerGroup().addTo(map);
  map.setView([36.5, 127.8], 7);
}

function areaLayerStyleFor() { return { color: '#111', weight: 2, fillColor: '#111', fillOpacity: 0.05 }; }

function redrawAreas() {
  layerGroup.eachLayer((l) => { if (l._isAreaShape) layerGroup.removeLayer(l); });
  const bounds = [];
  for (const a of board.areas) {
    let shape;
    if (a.kind === 'circle') {
      shape = L.circle([a.lat, a.lng], Object.assign({ radius: a.r }, areaLayerStyleFor()));
      bounds.push(...L.circle([a.lat, a.lng], { radius: a.r }).getBounds ? [] : []);
    } else if (a.polygon) {
      shape = L.polygon(a.polygon, areaLayerStyleFor());
    }
    if (shape) { shape._isAreaShape = true; shape.addTo(layerGroup); }
  }
  if (board.areas.length && board._areasBoundsDirty !== false) {
    const allBounds = board.areas.map((a) => a.bbox);
    const south = Math.min(...allBounds.map((b) => b[0]));
    const west = Math.min(...allBounds.map((b) => b[1]));
    const north = Math.max(...allBounds.map((b) => b[2]));
    const east = Math.max(...allBounds.map((b) => b[3]));
    map.fitBounds([[south, west], [north, east]], { padding: [16, 16] });
    board._areasBoundsDirty = false;
  }
}

// No color: every state below is black/white, told apart by fill vs
// hollow, line weight, and dash pattern only (matches the .dot legend
// classes in a.css). suspect/stale stay non-color cues too (dashed
// outline / reduced opacity), consistent with SPEC §8.2.3's "점 테두리
// 점선(suspect), 투명도 40%(stale)".
function styleForPerson(row) {
  const [, stateCode, , , , elapsed, flags] = row;
  const suspect = (flags & 2) !== 0;
  const stale = elapsed >= 0 && (serverNow() / 1000 - (board.incident.openedAt / 1000 + elapsed)) > 180;
  const base = { radius: 6, weight: 2, color: '#111' };
  switch (stateCode) {
    case 4: Object.assign(base, { fillColor: '#111', fillOpacity: 1 }); break; // arrived: filled
    case 3: Object.assign(base, { fillOpacity: 0, dashArray: '2,2' }); break; // moving: hollow + dashed
    case 5: Object.assign(base, { fillColor: '#111', fillOpacity: 1, weight: 4 }); break; // left: filled + thick
    default: Object.assign(base, { fillOpacity: 0 }); break; // notified/logged_in/loc_denied: hollow
  }
  if (suspect) base.dashArray = '3,3';
  if (stale) base.opacity = 0.4, base.fillOpacity = (base.fillOpacity || 0) * 0.4;
  return base;
}

function upsertMarker(row) {
  const [id, , lat5, lng5] = row;
  if (lat5 == null || lat5 === 0) return;
  const latlng = [lat5 / 1e5, lng5 / 1e5];
  let marker = board.markers.get(id);
  const style = styleForPerson(row);
  if (!marker) {
    marker = L.circleMarker(latlng, style);
    marker.on('click', () => showPersonCard(id));
    marker.addTo(layerGroup);
    board.markers.set(id, marker);
  } else {
    marker.setLatLng(latlng);
    marker.setStyle(style);
  }
}

// ---------------------------------------------------------------- Teams
function aggregateTeam(teamNo) {
  const agg = { arrived: 0, moving: 0, left: 0, notified: 0, other: 0, ackPending: 0, total: 0 };
  for (const [id, person] of board.people) {
    if (person[3] !== teamNo) continue;
    agg.total++;
    const m = board.m.get(id);
    if (!m) { agg.notified++; continue; }
    const [, stateCode, , , , , flags] = m;
    if (stateCode === 4) agg.arrived++;
    else if (stateCode === 3) agg.moving++;
    else if (stateCode === 5) agg.left++;
    else if (stateCode === 0) agg.notified++;
    else agg.other++;
    if (flags & 1) agg.ackPending++;
  }
  return agg;
}

function renderTeams() {
  clearChildren(els.teamsGrid);
  for (const t of board.teams) {
    const agg = aggregateTeam(t.no);
    const tile = el('div', { class: 'team' + (agg.left > 0 ? ' has-left' : ''), onclick: () => openTeamSheet(t) }, [
      el('div', { class: 'no' }, [`${t.no}조${t.name && t.name !== `${t.no}조` ? ' ' + t.name : ''}`]),
      el('div', { class: 'mission' }, [t.mission || '(미부여)']),
      el('div', { class: 'area' }, [areaName(t.areaId)]),
      el('div', { class: 'bar-wrap' }, [el('div', { class: 'bar-fill', style: { width: `${agg.total ? (agg.arrived / agg.total * 100) : 0}%` } }, [])]),
      el('div', { class: 'counts' }, [
        `${agg.arrived}/${agg.total}`,
        agg.left > 0 ? el('span', { class: 'left-count' }, [` 이탈 ${agg.left}`]) : '',
        agg.ackPending > 0 ? el('span', { class: 'ack-dot' }, []) : '',
      ]),
    ]);
    els.teamsGrid.appendChild(tile);
  }
  // pad to 10 tiles (2x5) even if fewer teams have tasks this incident
  for (let i = board.teams.length; i < 10; i++) {
    els.teamsGrid.appendChild(el('div', { class: 'team empty' }, [el('div', { class: 'no' }, [''])]));
  }
}

function areaName(id) {
  const a = board.areas.find((a) => a.id === id);
  return a ? a.name : '';
}

function openTeamSheet(team) {
  board.activeTeam = team;
  setText(els.teamSheetTitle, `${team.no}조`);
  els.teamMissionInput.value = team.mission || '';
  clearChildren(els.teamAreaSelect);
  for (const a of board.areas) {
    const opt = el('option', { value: a.id }, [a.name]);
    if (a.id === team.areaId) opt.selected = true;
    els.teamAreaSelect.appendChild(opt);
  }
  renderTeamRoster(team.no);
  els.teamSheet.showModal();
}

function renderTeamRoster(teamNo) {
  clearChildren(els.teamRosterList);
  for (const [id, p] of board.people) {
    if (p[3] !== teamNo) continue;
    const m = board.m.get(id);
    const stateName = m ? STATE_NAMES[m[1]] : '미접속';
    const row = el('div', { class: 'roster-row', onclick: () => showPersonCard(id) }, [
      el('span', { class: 'name' }, [p[1]]),
      el('span', {}, [p[2]]),
      el('span', { class: 'state' }, [stateName]),
    ]);
    els.teamRosterList.appendChild(row);
  }
}

els.teamMissionApply.addEventListener('click', async () => {
  if (!board.activeTeam) return;
  const mission = els.teamMissionInput.value.trim();
  const areaId = Number(els.teamAreaSelect.value);
  if (!mission || !areaId) return;
  await api.put(`/a/incidents/current/teams/${board.activeTeam.no}`, { mission, areaId });
  els.teamSheet.close();
  await fetchSnapshot();
});

document.querySelectorAll('.tab-btn').forEach((btn) => {
  btn.addEventListener('click', () => {
    document.querySelectorAll('.tab-btn').forEach((b) => b.classList.remove('active'));
    btn.classList.add('active');
    document.getElementById('teamMissionTab').hidden = btn.dataset.tab !== 'mission';
    document.getElementById('teamRosterTab').hidden = btn.dataset.tab !== 'roster';
  });
});
els.teamSheetClose.addEventListener('click', () => els.teamSheet.close());

// ---------------------------------------------------------------- Person card
async function showPersonCard(id) {
  const p = board.people.get(id);
  const m = board.m.get(id);
  clearChildren(els.personCardBody);
  const stateName = m ? STATE_NAMES[m[1]] : '미접속';
  els.personCardBody.appendChild(el('h3', {}, [p[1]]));
  els.personCardBody.appendChild(el('p', {}, [`${p[2]} · ${p[3]}조 · ${stateName}`]));
  els.personCardBody.appendChild(el('p', {}, [`행정전화: ${p[4] || '-'}`]));
  const mobileLine = el('p', {}, [`휴대전화: ${p[5]} `]);
  const callBtn = el('button', {
    type: 'button', onclick: async () => {
      const res = await api.get(`/a/incidents/current/members/${id}/contact`);
      window.location.href = `tel:${res.mobile}`;
    },
  }, ['전화']);
  mobileLine.appendChild(callBtn);
  els.personCardBody.appendChild(mobileLine);
  if (p[6]) els.personCardBody.appendChild(el('p', {}, [`개인임무: ${p[6]}`]));
  els.personCard.showModal();
}
els.personCardClose.addEventListener('click', () => els.personCard.close());

// ---------------------------------------------------------------- Snapshot / delta
async function fetchSnapshot() {
  const data = await api.get('/a/snapshot');
  applyBoard(data, true);
}

async function fetchDelta() {
  try {
    const data = await api.get(`/a/delta?epoch=${encodeURIComponent(board.epoch)}&since=${board.seq}`);
    applyBoard(data, false);
    board.connLostSince = null;
  } catch (err) {
    if (err instanceof ApiError && err.code === 'resync') {
      await fetchSnapshot();
      return;
    }
    if (!board.connLostSince) board.connLostSince = Date.now();
  }
  renderConnStatus();
}

function applyBoard(data, full) {
  syncServerTime(data.t);
  board.epoch = data.epoch;
  board.seq = data.seq;
  if (data.incident !== undefined) board.incident = data.incident;
  if (data.teams && data.teams.length) board.teams = data.teams;
  if (data.areas && data.areas.length) { board.areas = data.areas; board._areasBoundsDirty = true; }
  if (full) { board.people = new Map(); board.m = new Map(); }
  for (const p of data.people || []) board.people.set(p[0], p);
  for (const row of data.m || []) board.m.set(row[0], row);

  renderIncidentHeader();
  renderTeams();
  if (map) {
    if (full || data.areas?.length) redrawAreas();
    for (const row of data.m || []) upsertMarker(row);
  }
}

function renderIncidentHeader() {
  if (!board.incident) {
    setText(els.incidentType, '발령 없음');
    setText(els.incidentMessage, '');
    els.newIncidentBtn.hidden = false;
    els.editBtn.hidden = els.smsBtn.hidden = els.closeBtn.hidden = true;
    return;
  }
  els.newIncidentBtn.hidden = true;
  els.editBtn.hidden = els.smsBtn.hidden = els.closeBtn.hidden = false;
  setText(els.incidentType, board.incident.type);
  setText(els.incidentMessage, board.incident.message);
}

function renderConnStatus() {
  if (!board.connLostSince) { setText(els.connStatus, ''); return; }
  const sec = Math.floor((Date.now() - board.connLostSince) / 1000);
  const mm = String(Math.floor(sec / 60)).padStart(2, '0');
  const ss = String(sec % 60).padStart(2, '0');
  setText(els.connStatus, `연결 끊김 ${mm}:${ss}`);
}

function schedulePolling() {
  if (board.pollTimer) clearTimeout(board.pollTimer);
  const interval = document.hidden ? 15000 : 3000;
  board.pollTimer = setTimeout(async () => {
    await fetchDelta();
    schedulePolling();
  }, interval);
}
document.addEventListener('visibilitychange', schedulePolling);

// ---------------------------------------------------------------- New incident / edit / sms / close
els.newIncidentBtn.addEventListener('click', openNewIncidentSheet);
els.editBtn.addEventListener('click', openEditSheet);
els.smsBtn.addEventListener('click', openSmsSheet);
els.closeBtn.addEventListener('click', openCloseSheet);
els.incidentSheetClose.addEventListener('click', () => els.incidentSheet.close());

async function openNewIncidentSheet() {
  setText(els.incidentSheetTitle, '새 발령');
  clearChildren(els.incidentSheetBody);
  const draft = await api.get('/a/incidents/draft');
  const typeInput = el('input', { type: 'text', placeholder: '발령종류 (예: 비상 2단계)' }, []);
  const msgInput = el('textarea', { rows: '3', placeholder: '메시지' }, []);
  const teamRows = draft.teams.map((t) => {
    const areaSelect = el('select', {}, board.areas.map((a) => {
      const opt = el('option', { value: a.id }, [a.name]);
      if (a.id === t.areaId) opt.selected = true;
      return opt;
    }));
    const missionInput = el('input', { type: 'text', value: t.mission || '' }, []);
    return el('div', { class: 'roster-row' }, [
      el('span', { class: 'name' }, [`${t.teamNo}조 (${t.memberCount}명)`]),
      missionInput, areaSelect,
    ]);
  });
  els.incidentSheetBody.appendChild(el('div', {}, [
    el('label', {}, ['발령종류']), typeInput,
    el('label', {}, ['메시지']), msgInput,
    el('label', {}, ['조별 임무·지역 (자동 채움, 필요 시 수정)']),
    ...teamRows,
    el('button', {
      class: 'primary', type: 'button', onclick: async () => {
        const teams = draft.teams.map((t, i) => ({
          no: t.teamNo,
          mission: teamRows[i].querySelectorAll('input,select')[0].value,
          areaId: Number(teamRows[i].querySelectorAll('input,select')[1].value),
        }));
        const members = draft.overrides.map((o) => ({ id: o.memberId, mission: o.mission, areaId: o.areaId }));
        await api.post('/a/incidents', { type: typeInput.value, message: msgInput.value, teams, members });
        els.incidentSheet.close();
        await fetchSnapshot();
      },
    }, ['발령']),
  ]));
  els.incidentSheet.showModal();
}

function openEditSheet() {
  setText(els.incidentSheetTitle, '발령 수정');
  clearChildren(els.incidentSheetBody);
  const typeInput = el('input', { type: 'text', value: board.incident.type }, []);
  const msgInput = el('textarea', { rows: '3' }, [board.incident.message]);
  els.incidentSheetBody.appendChild(el('div', {}, [
    el('label', {}, ['발령종류']), typeInput,
    el('label', {}, ['메시지']), msgInput,
    el('button', {
      class: 'primary', type: 'button', onclick: async () => {
        await api.patch('/a/incidents/current', { type: typeInput.value, message: msgInput.value });
        els.incidentSheet.close();
        await fetchSnapshot();
      },
    }, ['저장']),
  ]));
  els.incidentSheet.showModal();
}

function openSmsSheet() {
  setText(els.incidentSheetTitle, '문자 발송');
  clearChildren(els.incidentSheetBody);
  const kindSelect = el('select', {}, [
    el('option', { value: 'open' }, ['발령']),
    el('option', { value: 'resend' }, ['재발송']),
  ]);
  const viaSelect = el('select', {}, [
    el('option', { value: 'manual' }, ['수동(기존 문자시스템)']),
    el('option', { value: 'http' }, ['문자 API']),
  ]);
  const targetSelect = el('select', {}, [
    el('option', { value: 'all' }, ['전체']),
    el('option', { value: 'not_logged_in' }, ['미접속자']),
    el('option', { value: 'failed' }, ['실패자']),
  ]);
  const resultBox = el('div', {}, []);
  els.incidentSheetBody.appendChild(el('div', {}, [
    el('label', {}, ['종류']), kindSelect,
    el('label', {}, ['방식']), viaSelect,
    el('label', {}, ['대상']), targetSelect,
    el('button', {
      class: 'primary', type: 'button', onclick: async () => {
        const res = await api.post(`/a/incidents/${board.incident.id}/sms`, {
          kind: kindSelect.value, via: viaSelect.value, target: targetSelect.value,
        });
        clearChildren(resultBox);
        resultBox.appendChild(el('p', {}, [`문구: ${res.text}`]));
        resultBox.appendChild(el('p', {}, [`대상 ${res.count}명, ${res.bytes}바이트${res.lms ? ' (장문 LMS)' : ''}`]));
        if (viaSelect.value === 'manual') {
          resultBox.appendChild(el('a', { href: `/api/v1/a/incidents/${board.incident.id}/sms/${res.batchId}/recipients.csv` }, ['수신자 CSV 받기']));
          resultBox.appendChild(el('button', {
            type: 'button', onclick: async () => {
              await api.post(`/a/incidents/${board.incident.id}/sms/${res.batchId}/mark-sent`, {});
              alert('발송 완료로 표시했습니다.');
            },
          }, ['발송 완료 표시']));
        }
      },
    }, ['미리보기/발송']),
    resultBox,
  ]));
  els.incidentSheet.showModal();
}

function openCloseSheet() {
  setText(els.incidentSheetTitle, '상황종료');
  clearChildren(els.incidentSheetBody);
  const confirmInput = el('input', { type: 'text', placeholder: '"상황종료" 입력' }, []);
  els.incidentSheetBody.appendChild(el('div', {}, [
    el('p', {}, ['확인을 위해 "상황종료"를 입력하세요.']),
    confirmInput,
    el('button', {
      class: 'primary', type: 'button', onclick: async () => {
        await api.post('/a/incidents/current/close', { confirm: confirmInput.value });
        els.incidentSheet.close();
        await fetchSnapshot();
      },
    }, ['종료']),
  ]));
  els.incidentSheet.showModal();
}

// ---------------------------------------------------------------- Boot
async function boot() {
  try {
    const cfg = await fetch('/api/v1/config').then((r) => r.json());
    board.cfg = cfg;
    showOnly(els.boardView);
    if (!map) initMap();
    await fetchSnapshot();
    schedulePolling();
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      showOnly(els.loginView);
      return;
    }
    throw err;
  }
}

(async function main() {
  startClock({ dateEl: els.clockDate, timeEl: els.clockTime });
  setInterval(renderConnStatus, 1000);
  await boot();
})();
