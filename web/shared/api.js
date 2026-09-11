// Minimal fetch wrapper for the /api/v1 JSON API (SPEC §7.1).
// No build step, no frameworks — plain ES module (SPEC §2.1).

export const BASE = '/api/v1';

export class ApiError extends Error {
  constructor(code, msg, retryAfter, status) {
    super(msg || code || 'request failed');
    this.code = code;
    this.retryAfter = retryAfter;
    this.status = status;
  }
}

async function request(method, path, body) {
  const opts = { method, credentials: 'same-origin', headers: {} };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  let res;
  try {
    res = await fetch(BASE + path, opts);
  } catch (e) {
    throw new ApiError('network', '네트워크 오류가 발생했습니다.', 0, 0);
  }
  let data = null;
  try {
    data = await res.json();
  } catch {
    // some responses (csv, etc.) aren't JSON; caller uses api.raw for those.
  }
  if (!res.ok) {
    throw new ApiError(data && data.err, data && data.msg, data && data.retryAfter, res.status);
  }
  return data;
}

export const api = {
  get: (path) => request('GET', path),
  post: (path, body) => request('POST', path, body === undefined ? {} : body),
  put: (path, body) => request('PUT', path, body === undefined ? {} : body),
  patch: (path, body) => request('PATCH', path, body === undefined ? {} : body),
  del: (path) => request('DELETE', path),
  // raw: for endpoints that don't return JSON (CSV exports) or need the
  // Response object directly (multipart upload).
  raw: (method, path, opts) => fetch(BASE + path, Object.assign({ method, credentials: 'same-origin' }, opts)),
};
