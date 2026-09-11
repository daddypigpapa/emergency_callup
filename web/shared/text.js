// DOM-safe text helpers and outbound map-app link builders.
// SPEC §8.0: only textContent/setAttribute may put data on screen —
// innerHTML/outerHTML/insertAdjacentHTML/document.write are banned
// (enforced by `make lint`, R8).

/** Replace an element's children with a single text node. */
export function setText(el, text) {
  el.textContent = text == null ? '' : String(text);
}

/**
 * Build a DOM element tree without ever touching innerHTML.
 *
 * `attrs.style`, if given, MUST be an object of CSS properties (e.g.
 * `{ width: '40%' }`) set via the CSSOM (`node.style.prop = value`), never
 * a `style="..."` attribute string — CSP's `style-src 'self'` (no
 * `unsafe-inline`, SPEC §11.2) blocks the inline-style attribute but not
 * script-driven CSSOM property assignment.
 */
export function el(tag, attrs, children) {
  const node = document.createElement(tag);
  if (attrs) {
    for (const [k, v] of Object.entries(attrs)) {
      if (k === 'class') node.className = v;
      else if (k === 'style' && typeof v === 'object') Object.assign(node.style, v);
      else if (k.startsWith('on') && typeof v === 'function') node.addEventListener(k.slice(2), v);
      else node.setAttribute(k, v);
    }
  }
  for (const child of children || []) {
    node.appendChild(typeof child === 'string' ? document.createTextNode(child) : child);
  }
  return node;
}

export function clearChildren(el) {
  while (el.firstChild) el.removeChild(el.firstChild);
}

/** Distance formatting per SPEC §8.3: "1 km 미만 m, 이상 소수 1자리 km". */
export function formatDistance(meters) {
  const m = Math.max(0, Math.round(meters));
  if (m < 1000) return `${m}m`;
  return `${(m / 1000).toFixed(1)}km`;
}

/**
 * Kakao Map "가는길" link (SPEC §10.3) — a plain web link, works whether or
 * not the app is installed.
 */
export function kakaoRouteUrl(name, lat, lng) {
  const cleanName = String(name || '').replace(/,/g, '');
  return `https://map.kakao.com/link/to/${encodeURIComponent(cleanName)},${lat},${lng}`;
}

/**
 * Naver Map "가는길" deep link (SPEC §10.3). routeType defaults to "car"
 * (configurable server-side via NAVER_ROUTE_TYPE, passed in by the caller).
 * Returns {url, isAndroidIntent} — the caller navigates to `url`; for the
 * non-Android branch, the caller must also watch document.visibilityState
 * after ~1.5s to detect "app not installed" (done in f.js, not here, since
 * that needs a timer tied to the click).
 */
export function naverRouteUrl(name, lat, lng, appName, routeType, isAndroid) {
  const dname = encodeURIComponent(String(name || ''));
  const params = `dlat=${lat}&dlng=${lng}&dname=${dname}&appname=${encodeURIComponent(appName)}`;
  if (isAndroid) {
    return `intent://route/${routeType}?${params}#Intent;scheme=nmap;action=android.intent.action.VIEW;category=android.intent.category.BROWSABLE;package=com.nhn.android.nmap;end`;
  }
  return `nmap://route/${routeType}?${params}`;
}

export function isAndroid() {
  return /Android/i.test(navigator.userAgent);
}
