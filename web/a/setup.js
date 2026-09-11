import { api, ApiError } from '/shared/api.js';
import { setText, clearChildren, el } from '/shared/text.js';

// ---- Tabs ----
document.querySelectorAll('.setup-tabs .tab').forEach((btn) => {
  btn.addEventListener('click', () => {
    document.querySelectorAll('.setup-tabs .tab').forEach((b) => b.classList.remove('active'));
    document.querySelectorAll('.panel').forEach((p) => p.classList.remove('active'));
    btn.classList.add('active');
    document.getElementById(`panel-${btn.dataset.panel}`).classList.add('active');
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

// ---- Areas ----
async function loadAreas() {
  try {
    const res = await api.get('/a/areas');
    const wrap = document.getElementById('areaListWrap');
    clearChildren(wrap);
    const table = el('table', {}, [
      el('thead', {}, [el('tr', {}, ['이름', '종류', '반경/점 수'].map((h) => el('th', {}, [h])))]),
      el('tbody', {}, res.areas.map((a) => el('tr', {}, [
        el('td', {}, [a.name]),
        el('td', {}, [a.kind]),
        el('td', {}, [a.kind === 'circle' ? `${a.r}m` : `${(a.polygon || []).length}점`]),
      ]))),
    ]);
    wrap.appendChild(table);
  } catch (err) {
    if (!guardAuth(err)) throw err;
  }
}

document.getElementById('areaCreateBtn').addEventListener('click', async () => {
  const name = document.getElementById('areaName').value.trim();
  const lat = Number(document.getElementById('areaLat').value);
  const lng = Number(document.getElementById('areaLng').value);
  const r = Number(document.getElementById('areaRadius').value);
  if (!name || !lat || !lng || !r) return;
  try {
    await api.post('/a/areas', { name, kind: 'circle', lat, lng, r });
    await loadAreas();
  } catch (err) {
    alert(err.message || '등록 실패');
  }
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
  await loadAreas();
  await loadPresets();
  await loadTileKey();
  await loadAdmins();
})();
