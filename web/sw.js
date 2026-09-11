// App-shell-only service worker (SPEC §13.2): caches HTML/JS/CSS/Leaflet so
// a re-visit loads instantly, but NEVER caches API responses or map tiles
// (both must always be fresh / are already excluded from this origin's
// same-path caching by pattern below).
const CACHE_NAME = 'ecu-shell-v1';
const SHELL_FILES = [
  '/f/', '/f/index.html', '/f/f.css', '/f/f.js',
  '/a/', '/a/index.html', '/a/a.css', '/a/a.js', '/a/setup.html', '/a/setup.js',
  '/shared/api.js', '/shared/clock.js', '/shared/geo.js', '/shared/map.js', '/shared/text.js',
  '/vendor/leaflet/leaflet.js', '/vendor/leaflet/leaflet.css',
];

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE_NAME).then((cache) => cache.addAll(SHELL_FILES)).then(() => self.skipWaiting()),
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((keys) => Promise.all(keys.filter((k) => k !== CACHE_NAME).map((k) => caches.delete(k)))).then(() => self.clients.claim()),
  );
});

self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url);
  // Never cache the API or map tiles — only same-origin app-shell files.
  if (url.pathname.startsWith('/api/')) return;
  if (url.origin !== self.location.origin) return;
  if (!SHELL_FILES.includes(url.pathname)) return;

  event.respondWith(
    caches.match(event.request).then((cached) => cached || fetch(event.request)),
  );
});
