// Server-corrected clock + Asia/Seoul display formatting (SPEC §7.1:
// "클라이언트는 t로 시계 오차를 보정해 현재시간을 표시").

let offsetMs = 0;

/** Feed every API response's `t` (server UTC ms) through this. */
export function syncServerTime(serverT) {
  offsetMs = serverT - Date.now();
}

/** Current time, corrected for the client/server clock difference. */
export function serverNow() {
  return Date.now() + offsetMs;
}

const dateFmt = new Intl.DateTimeFormat('ko-KR', {
  timeZone: 'Asia/Seoul', year: 'numeric', month: '2-digit', day: '2-digit', weekday: 'short',
});
const timeFmt = new Intl.DateTimeFormat('ko-KR', {
  timeZone: 'Asia/Seoul', hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit',
});

/** "2026-09-11(목)" */
export function formatDate(ms) {
  const parts = dateFmt.formatToParts(ms);
  const get = (t) => (parts.find((p) => p.type === t) || {}).value || '';
  return `${get('year')}-${get('month')}-${get('day')}(${get('weekday')})`;
}

/** "14:32:07" */
export function formatTime(ms) {
  return timeFmt.format(ms).replace(/^24/, '00');
}

/**
 * Starts a 1-second ticker that writes formatted date/time into the given
 * elements using textContent only (SPEC §8.0: innerHTML is forbidden).
 * Returns a stop() function.
 */
export function startClock({ dateEl, timeEl }) {
  function tick() {
    const now = serverNow();
    if (dateEl) dateEl.textContent = formatDate(now);
    if (timeEl) timeEl.textContent = formatTime(now);
  }
  tick();
  const id = setInterval(tick, 1000);
  return () => clearInterval(id);
}
