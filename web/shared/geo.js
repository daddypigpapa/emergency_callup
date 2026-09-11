// The field-device location transmit loop (SPEC §6.1, §8.1.5, §8.1.6).
//
// A single LocationLoop instance owns every timer/watch/wake-lock it
// creates; stop() is the one place everything gets released
// (testai M2 regression, SPEC: "전송 루프 객체 1개가 소유하고 stop() 한
// 곳에서 해제").

const MIN_GAP_MS = 5000;
const MOVE_TRIGGER_M = 25;
const ACC_DROP_M = 500;
const RETRY_DELAYS_S = [5, 10, 20, 40, 60];

function haversineM(a, b) {
  const R = 6371000;
  const toRad = (d) => (d * Math.PI) / 180;
  const dLat = toRad(b.lat - a.lat);
  const dLng = toRad(b.lng - a.lng);
  const s =
    Math.sin(dLat / 2) ** 2 +
    Math.cos(toRad(a.lat)) * Math.cos(toRad(b.lat)) * Math.sin(dLng / 2) ** 2;
  return R * 2 * Math.atan2(Math.sqrt(s), Math.sqrt(1 - s));
}

function getCurrentPositionOnce(options) {
  return new Promise((resolve, reject) => {
    navigator.geolocation.getCurrentPosition(resolve, reject, options);
  });
}

/**
 * @param {object} opts
 * @param {(fix:{lat,lng,acc,ts}) => Promise<object>} opts.onFix - POST /f/fix, returns the parsed response.
 * @param {() => Promise<object>} opts.onSync - GET /f/sync, returns the parsed response.
 * @param {(status:string, detail?:any) => void} [opts.onStatus] - UI status callback.
 * @param {() => void} [opts.onUnauthorized] - called on a 401 (SPEC: "401 → 로그인 화면").
 * @param {(res:object) => void} [opts.onStop] - called when the server signals stop/CLOSED/IDLE/NONE.
 */
export class LocationLoop {
  constructor(opts) {
    this.onFix = opts.onFix;
    this.onSync = opts.onSync;
    this.onStatus = opts.onStatus || (() => {});
    this.onUnauthorized = opts.onUnauthorized || (() => {});
    this.onStop = opts.onStop || (() => {});

    this.watchId = null;
    this.heartbeatTimer = null;
    this.retryTimer = null;
    this.wakeLock = null;
    this.stopped = true;

    this.n = 15; // server-directed interval, seconds
    this.lastSentAt = 0;
    this.lastSentPos = null;
    this.lastSentAcc = null;
    this.retryAttempt = 0;
    this.collectingOnly = false; // true once server says IDLE/NONE: sync-only mode
  }

  start() {
    this.stopped = false;
    if (!('geolocation' in navigator)) {
      this.onStatus('no-geolocation');
      return;
    }
    this.watchId = navigator.geolocation.watchPosition(
      (pos) => this._onPosition(pos),
      (err) => this.onStatus('watch-error', err),
      { enableHighAccuracy: true, maximumAge: 5000 },
    );
    this._scheduleHeartbeat();
  }

  /** The one place every resource this loop owns gets released. */
  stop() {
    this.stopped = true;
    if (this.watchId != null) {
      navigator.geolocation.clearWatch(this.watchId);
      this.watchId = null;
    }
    if (this.heartbeatTimer) {
      clearTimeout(this.heartbeatTimer);
      this.heartbeatTimer = null;
    }
    if (this.retryTimer) {
      clearTimeout(this.retryTimer);
      this.retryTimer = null;
    }
    this._releaseWakeLock();
  }

  async setWakeLock(enabled) {
    if (!('wakeLock' in navigator)) return false;
    if (enabled) {
      try {
        this.wakeLock = await navigator.wakeLock.request('screen');
        return true;
      } catch {
        return false;
      }
    }
    this._releaseWakeLock();
    return true;
  }

  _releaseWakeLock() {
    if (this.wakeLock) {
      this.wakeLock.release().catch(() => {});
      this.wakeLock = null;
    }
  }

  _onPosition(pos) {
    const acc = Math.round(pos.coords.accuracy);
    if (acc > ACC_DROP_M) return; // ACC_DROP: discard entirely
    const fix = { lat: pos.coords.latitude, lng: pos.coords.longitude, acc, ts: Date.now() };
    this._maybeSend(fix);
  }

  _maybeSend(fix, force) {
    if (this.stopped || this.collectingOnly) return;
    const now = Date.now();
    if (!force && now - this.lastSentAt < MIN_GAP_MS) return;

    let shouldSend = !!force || this.lastSentPos == null;
    if (!shouldSend && haversineM(this.lastSentPos, fix) >= MOVE_TRIGGER_M) shouldSend = true; // (a)
    if (!shouldSend && now - this.lastSentAt >= this.n * 1000) shouldSend = true; // (b)
    if (!shouldSend && this.lastSentAcc != null && this.lastSentAcc > 100 && fix.acc <= 100) shouldSend = true; // (c)

    if (shouldSend) this._send(fix);
  }

  async _send(fix) {
    this.lastSentAt = Date.now();
    this.lastSentPos = { lat: fix.lat, lng: fix.lng };
    this.lastSentAcc = fix.acc;
    try {
      const res = await this.onFix(fix);
      this.retryAttempt = 0;
      this._applyResponse(res);
    } catch (e) {
      if (e && e.status === 401) {
        this.stop();
        this.onUnauthorized();
        return;
      }
      this._scheduleRetry(fix);
    }
  }

  _scheduleRetry(fix) {
    if (this.stopped) return;
    const d = RETRY_DELAYS_S[Math.min(this.retryAttempt, RETRY_DELAYS_S.length - 1)];
    this.retryAttempt++;
    const jitterMs = Math.random() * 2000;
    this.retryTimer = setTimeout(() => this._send(fix), d * 1000 + jitterMs);
    this.onStatus('offline');
  }

  _applyResponse(res) {
    if (!res) return;
    if (typeof res.n === 'number' && res.n > 0) this.n = res.n;
    if (res.stop || res.st === 'CLOSED') {
      this.stop();
      this.onStop(res);
      return;
    }
    if (res.st === 'IDLE' || res.st === 'NONE') {
      this.collectingOnly = true;
    } else {
      this.collectingOnly = false;
    }
    this.onStatus('ok', res);
  }

  _scheduleHeartbeat() {
    if (this.stopped) return;
    this.heartbeatTimer = setTimeout(() => this._heartbeat(), this.n * 1000);
  }

  async _heartbeat() {
    if (this.stopped) return;
    const now = Date.now();
    const dueForSend = now - this.lastSentAt >= this.n * 1000;
    if (this.collectingOnly) {
      try {
        const res = await this.onSync();
        this._applyResponse(res);
      } catch (e) {
        if (e && e.status === 401) {
          this.stop();
          this.onUnauthorized();
          return;
        }
        this.onStatus('offline');
      }
    } else if (dueForSend) {
      try {
        const pos = await getCurrentPositionOnce({ maximumAge: 10000, timeout: 10000 });
        const acc = Math.round(pos.coords.accuracy);
        this._maybeSend({ lat: pos.coords.latitude, lng: pos.coords.longitude, acc, ts: Date.now() }, true);
      } catch {
        // No fresh reading available: fall back to a plain sync so
        // mission-change/close signals still arrive (SPEC §6.1, R18).
        try {
          const res = await this.onSync();
          this._applyResponse(res);
        } catch (e) {
          if (e && e.status === 401) {
            this.stop();
            this.onUnauthorized();
            return;
          }
          this.onStatus('offline');
        }
      }
    }
    this._scheduleHeartbeat();
  }
}
