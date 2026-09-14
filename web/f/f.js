import { api, ApiError } from '/shared/api.js';
import { syncServerTime, startClock } from '/shared/clock.js';
import { setText, formatDistance, kakaoRouteUrl, naverRouteUrl, isAndroid } from '/shared/text.js';
import { LocationLoop } from '/shared/geo.js';
import * as fieldMap from '/shared/map.js';

const els = {
  loginView: document.getElementById('loginView'),
  pwChangeView: document.getElementById('pwChangeView'),
  mainView: document.getElementById('mainView'),
  idleView: document.getElementById('idleView'),
  idleText: document.getElementById('idleText'),
  loginForm: document.getElementById('loginForm'),
  loginError: document.getElementById('loginError'),
  pwChangeForm: document.getElementById('pwChangeForm'),
  pwChangeError: document.getElementById('pwChangeError'),
  clockTime: document.getElementById('clockTime'),
  incidentType: document.getElementById('incidentType'),
  board: document.getElementById('board'),
  r2: document.getElementById('r2'),
  teamName: document.getElementById('teamName'),
  missionText: document.getElementById('missionText'),
  areaName: document.getElementById('areaName'),
  areaDistance: document.getElementById('areaDistance'),
  mapEl: document.getElementById('mapEl'),
  mapFallback: document.getElementById('mapFallback'),
  kakaoBtn: document.getElementById('kakaoBtn'),
  naverBtn: document.getElementById('naverBtn'),
  meBtn: document.getElementById('meBtn'),
  destBtn: document.getElementById('destBtn'),
  wakeBtn: document.getElementById('wakeBtn'),
  consentSheet: document.getElementById('consentSheet'),
  consentAllowBtn: document.getElementById('consentAllowBtn'),
  ackModal: document.getElementById('ackModal'),
  ackBtn: document.getElementById('ackBtn'),
};

const state = {
  cfg: null,
  mustChangeShown: false,
  ackVer: 1,
  mv: 1,
  st: null,
  task: null,
  incident: null,
  loop: null,
  connLostSince: null,
  wakeOn: false,
  lastPos: null, // {lat, lng} — most recent raw GPS reading, for [내 위치]
  meFitShowsMe: false, // SPEC §8.1.3: [내 위치] toggles area-only vs area+me
};

function showOnly(view) {
  for (const v of [els.loginView, els.pwChangeView, els.mainView, els.idleView]) {
    v.hidden = v !== view;
  }
}

// ---- Board (R2) priority, SPEC §8.3 ----
function renderBoard() {
  els.r2.className = 'row r2';
  if (state.st === 'CLOSED') {
    setText(els.board, '상황종료 · 위치 수집을 중단했습니다');
    els.r2.classList.add('muted');
    return;
  }
  if (state.connLostSince) {
    setText(els.board, '서버 연결 끊김 · 재시도 중');
    els.r2.classList.add('warn');
    return;
  }
  if (state.st === 'LEFT') {
    setText(els.board, '임무지역 이탈');
    els.r2.classList.add('left');
    return;
  }
  if (state.mv > state.ackVer) {
    setText(els.board, '임무변경 확인 필요');
    els.r2.classList.add('warn');
    return;
  }
  if (state.st === 'LOC_DENIED') {
    setText(els.board, '위치 권한이 필요합니다');
    els.r2.classList.add('warn');
    return;
  }
  if (state.st === 'ARRIVED' && state.task) {
    setText(els.board, `${state.task.teamNameForBoard || ''} 임무 : ${state.task.teamMission}`);
    els.r2.classList.add('arrived');
    return;
  }
  if (state.st === 'MOVING' && state.lastDistance != null) {
    setText(els.board, `임무지역까지 ${formatDistance(state.lastDistance)}`);
    els.r2.classList.add('moving');
    return;
  }
  if (state.st === 'IDLE') {
    setText(els.board, '현재 발령이 없습니다');
    return;
  }
  if (state.st === 'NONE') {
    setText(els.board, '이번 발령 대상이 아닙니다');
    return;
  }
  setText(els.board, '위치 확인 중');
}

// ---- /f/me rendering ----
async function loadMe() {
  const me = await api.get('/f/me');
  syncServerTime(me.t);
  state.st = me.st;
  state.incident = me.incident;

  if (me.st === 'IDLE' || me.st === 'NONE') {
    showOnly(els.idleView);
    setText(els.idleText, me.st === 'IDLE' ? '현재 발령이 없습니다.' : '이번 발령 대상이 아닙니다.');
    return;
  }

  showOnly(els.mainView);
  state.task = me.task;
  state.ackVer = me.task.ackVer;
  state.mv = me.task.mv;
  setText(els.incidentType, me.incident.type);
  setText(els.teamName, `${me.member.teamName} · ${me.task.mission}`);
  els.teamName.dataset.teamName = me.member.teamName;
  state.task.teamNameForBoard = me.member.teamName;
  setText(els.missionText, '');
  setText(els.areaName, me.task.area.name);

  updateRouteLinks(me.task.area);
  ensureMap(me.task.area);

  maybeShowConsentSheet(me.incident.id, me.st);
  renderBoard();
}

function updateRouteLinks(area) {
  const [navLat, navLng] = area.nav;
  els.kakaoBtn.href = kakaoRouteUrl(area.name, navLat, navLng);
  const android = isAndroid();
  const url = naverRouteUrl(area.name, navLat, navLng, state.cfg.naverAppName, state.cfg.naverRouteType, android);
  els.naverBtn.href = url;
  els.naverBtn.addEventListener('click', (e) => {
    if (android) return; // intent:// navigates directly
    e.preventDefault();
    const before = Date.now();
    window.location.href = url;
    setTimeout(() => {
      if (document.visibilityState === 'visible' && Date.now() - before < 3000) {
        alert('네이버지도 앱이 설치되어 있지 않습니다.\n앱스토어에서 설치 후 다시 시도해 주세요.');
      }
    }, 1500);
  }, { once: true });
}

let mapReady = false;
function ensureMap(area) {
  if (!mapReady) {
    fieldMap.init(els.mapEl, state.cfg.tileUrl, state.cfg.tileAttribution);
    fieldMap.onTileError(() => { els.mapFallback.hidden = false; });
    mapReady = true;
  }
  fieldMap.drawArea(area);
  // A new area (first load, or after a mission/area change confirmed via
  // the ack modal) always reframes. If we already have a GPS reading by
  // then (e.g. an area change mid-mission), show both together right away
  // rather than resetting to an area-only view the user would have to
  // press [목적지]/[내 위치] again just to undo.
  if (state.lastPos) {
    fieldMap.fitAreaAndMe(area.bbox, state.lastPos.lat, state.lastPos.lng);
    state.meFitShowsMe = true;
  } else {
    fieldMap.fitArea(area.bbox);
    state.meFitShowsMe = false;
  }
}

// ---- Consent sheet (once per incident) ----
function maybeShowConsentSheet(incidentId, st) {
  if (st === 'LOC_DENIED') return; // already answered "deny" — show board message, no re-prompt loop
  const key = `consent_shown_${incidentId}`;
  if (localStorage.getItem(key)) {
    startLoopIfNeeded();
    return;
  }
  els.consentSheet.showModal();
  els.consentAllowBtn.onclick = async () => {
    localStorage.setItem(key, '1');
    els.consentSheet.close();
    // Trigger the browser permission prompt inside this click handler.
    navigator.geolocation.getCurrentPosition(
      async (pos) => {
        // Show the position immediately rather than waiting for the loop's
        // first watchPosition tick — the user just granted permission and
        // should see their dot appear right away.
        applyPosition(pos.coords.latitude, pos.coords.longitude, Math.round(pos.coords.accuracy));
        await api.post('/f/consent', { granted: true, textVer: '2026-09-v1' });
        startLoopIfNeeded();
      },
      async () => {
        await api.post('/f/consent', { granted: false, textVer: '2026-09-v1' });
        state.st = 'LOC_DENIED';
        renderBoard();
      },
      { enableHighAccuracy: true, timeout: 10000 },
    );
  };
}

// ---- Location loop wiring (SPEC §6.1, §6.3) ----
function startLoopIfNeeded() {
  if (state.loop) return;
  state.loop = new LocationLoop({
    onFix: (fix) => api.post('/f/fix', { la: fix.lat, lo: fix.lng, ac: fix.acc, ts: fix.ts }),
    onSync: () => api.get('/f/sync'),
    onPosition: (fix) => {
      // Every raw GPS reading updates the "내 위치" dot immediately, even
      // when SPEC §6.1's send-throttling means this particular reading
      // isn't posted to the server. Previously nothing ever called
      // fieldMap.setMe() at all, so the dot never appeared no matter how
      // long location tracking ran.
      applyPosition(fix.lat, fix.lng, fix.acc);
    },
    onStatus: (status, detail) => {
      if (status === 'offline') {
        if (!state.connLostSince) state.connLostSince = Date.now();
      } else if (status === 'ok') {
        state.connLostSince = null;
        applyPollResult(detail);
      }
      renderBoard();
    },
    onUnauthorized: () => {
      state.loop = null;
      showOnly(els.loginView);
    },
    onStop: () => {
      // SPEC §8.1.5: watch/timers/wake-lock already released by loop.stop();
      // the map and route buttons stay visible, only the position marker
      // and further sync calls stop.
      state.st = 'CLOSED';
      renderBoard();
    },
  });
  state.loop.start();
}

function applyPollResult(res) {
  if (!res) return;
  syncServerTime(res.t);
  state.st = res.st;
  state.mv = res.mv;
  state.lastDistance = res.d;
  if (typeof res.d === 'number') {
    setText(els.areaDistance, formatDistance(Math.abs(res.d)));
  }
  if (res.mv > state.ackVer) {
    showAckModal();
  }
  renderBoard();
}

function showAckModal() {
  if (els.ackModal.open) return;
  els.ackModal.showModal();
  els.ackBtn.onclick = async () => {
    const me = await api.get('/f/me');
    syncServerTime(me.t);
    state.task = me.task;
    state.mv = me.task.mv;
    setText(els.teamName, `${me.member.teamName} · ${me.task.mission}`);
    state.task.teamNameForBoard = me.member.teamName;
    setText(els.areaName, me.task.area.name);
    updateRouteLinks(me.task.area);
    ensureMap(me.task.area);
    const ackToSend = me.task.mv;
    const ackRes = await api.post('/f/ack', { mv: ackToSend });
    state.ackVer = ackRes.mv;
    if (me.task.mv > state.ackVer) {
      // Changed again while we were syncing — reopen.
      renderBoard();
      showAckModal();
      return;
    }
    els.ackModal.close();
    renderBoard();
  };
}

// ---- Position handling / map framing ----
// applyPosition() is the single place a raw GPS reading turns into map
// state, called both from the initial consent-grant getCurrentPosition and
// from every LocationLoop tick. On the very first reading we know about for
// this mission, it auto-frames the view to show both the mission area and
// the user's position together (requested behavior: "최초화면은 내
// 현재위치와 임무지역을 한 화면 안에 모두 표시") instead of leaving the
// user staring at an area-only view with no indication their dot exists.
// Later readings only move the dot — the view doesn't keep re-centering
// itself as the user walks, which would be disorienting.
function applyPosition(lat, lng, acc) {
  const isFirst = !state.lastPos;
  state.lastPos = { lat, lng };
  fieldMap.setMe(lat, lng, acc);
  if (isFirst && state.task) {
    fieldMap.fitAreaAndMe(state.task.area.bbox, lat, lng);
    state.meFitShowsMe = true;
  }
}

// SPEC §8.1.3 / this feature request: [내 위치] and R3's [목적지] both
// toggle between "area + me together" and "area only, centered" — sharing
// one state so either button reflects what the other just did.
function toggleAreaMeFit() {
  if (!state.task) return;
  if (!state.lastPos) {
    // No GPS reading yet (e.g. still waiting on the permission prompt or
    // the first fix) — nothing to add to the view yet, but re-centering on
    // the area is still useful feedback that the button did something.
    fieldMap.fitArea(state.task.area.bbox);
    return;
  }
  if (state.meFitShowsMe) {
    fieldMap.fitArea(state.task.area.bbox);
    state.meFitShowsMe = false;
  } else {
    fieldMap.fitAreaAndMe(state.task.area.bbox, state.lastPos.lat, state.lastPos.lng);
    state.meFitShowsMe = true;
  }
}

// ---- Buttons ----
els.meBtn.addEventListener('click', toggleAreaMeFit);
els.destBtn.addEventListener('click', toggleAreaMeFit);

els.wakeBtn.addEventListener('click', async () => {
  state.wakeOn = !state.wakeOn;
  if (state.loop) await state.loop.setWakeLock(state.wakeOn);
  setText(els.wakeBtn, state.wakeOn ? '화면 켜두기 끄기' : '화면 켜두기');
});
if ('wakeLock' in navigator) els.wakeBtn.hidden = false;

// ---- Auth forms ----
els.loginForm.addEventListener('submit', async (e) => {
  e.preventDefault();
  setText(els.loginError, '');
  const id = document.getElementById('loginId').value.trim();
  const pw = document.getElementById('loginPw').value;
  try {
    const res = await api.post('/f/login', { id, pw });
    syncServerTime(res.t);
    if (res.mustChange) {
      showOnly(els.pwChangeView);
    } else {
      await boot();
    }
  } catch (err) {
    setText(els.loginError, describeError(err));
  }
});

els.pwChangeForm.addEventListener('submit', async (e) => {
  e.preventDefault();
  setText(els.pwChangeError, '');
  const oldPw = document.getElementById('pwOld').value;
  const newPw = document.getElementById('pwNew').value;
  try {
    await api.post('/f/password', { old: oldPw, new: newPw });
    await boot();
  } catch (err) {
    setText(els.pwChangeError, describeError(err));
  }
});

function describeError(err) {
  if (err instanceof ApiError) {
    if (err.code === 'rate') return `너무 많이 시도했습니다. ${err.retryAfter ? err.retryAfter + '초 후 다시 시도하세요.' : '잠시 후 다시 시도하세요.'}`;
    return err.message || '오류가 발생했습니다.';
  }
  return '오류가 발생했습니다.';
}

// ---- Boot ----
async function loadConfig() {
  const cfg = await fetch('/api/v1/config').then((r) => r.json());
  state.cfg = cfg;
}

async function boot() {
  try {
    await loadMe();
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      showOnly(els.loginView);
      return;
    }
    throw err;
  }
}

(async function main() {
  await loadConfig();
  startClock({ timeEl: els.clockTime });
  try {
    await boot();
  } catch (err) {
    // Surface boot failures instead of letting them fail silently (e.g. if
    // a required script like Leaflet failed to load).
    console.error('boot failed:', err);
  }
})();
