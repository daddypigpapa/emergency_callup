// Field-screen map wrapper over Leaflet. SPEC §10.1: if the background tile
// benchmark (Phase 0) ever swaps this screen to the Kakao Map SDK, that
// replacement module must expose exactly this same function set so f.js
// doesn't change. The admin situation board does NOT use this module (it
// stays on Leaflet for Canvas-rendered point clusters — see a.js) and so
// isn't affected by whichever choice wins here.
//
// Depends on the global `L` from vendor/leaflet/leaflet.js (a plain
// <script> tag, not an ES module — SPEC §2.1 "빌드 단계 없음").

let map = null;
let areaLayer = null;
let meMarker = null;
let meAccCircle = null;
let navMarker = null;
let tileErrorCount = 0;
let tileErrorCb = null;

export function init(container, tileUrl, tileAttribution) {
  map = L.map(container, { zoomControl: true, attributionControl: true });
  const layer = L.tileLayer(tileUrl, { attribution: tileAttribution || '', maxZoom: 19 });
  layer.on('tileerror', () => {
    tileErrorCount++;
    if (tileErrorCount >= 5 && tileErrorCb) tileErrorCb();
  });
  layer.addTo(map);
  areaLayer = L.layerGroup().addTo(map);
  return map;
}

/** bbox = [south, west, north, east] (SPEC §8.1.3). */
export function fitArea(bbox) {
  if (!map) return;
  const bounds = L.latLngBounds([bbox[0], bbox[1]], [bbox[2], bbox[3]]);
  map.fitBounds(bounds, { padding: [24, 24], maxZoom: 18 });
}

/** area = the `task.area` object from GET /f/me (SPEC §7.2). */
export function drawArea(area) {
  if (!map) return;
  areaLayer.clearLayers();
  if (area.kind === 'circle') {
    L.circle([area.lat, area.lng], { radius: area.r, color: '#2563eb', weight: 2, fillOpacity: 0.08 }).addTo(areaLayer);
  } else if (area.kind === 'polygon' && area.polygon) {
    L.polygon(area.polygon, { color: '#2563eb', weight: 2, fillOpacity: 0.08 }).addTo(areaLayer);
  }
  if (area.nav) {
    setNav(area.nav[0], area.nav[1]);
  }
}

export function setMe(lat, lng, acc) {
  if (!map) return;
  const latlng = [lat, lng];
  if (!meMarker) {
    meMarker = L.circleMarker(latlng, { radius: 7, color: '#fff', weight: 2, fillColor: '#2563eb', fillOpacity: 1 }).addTo(map);
    meAccCircle = L.circle(latlng, { radius: acc || 0, color: '#2563eb', weight: 1, opacity: 0.3, fillOpacity: 0.08 }).addTo(map);
  } else {
    meMarker.setLatLng(latlng);
    meAccCircle.setLatLng(latlng);
    meAccCircle.setRadius(acc || 0);
  }
}

export function setNav(lat, lng) {
  if (!map) return;
  const latlng = [lat, lng];
  if (!navMarker) {
    navMarker = L.marker(latlng).addTo(map);
  } else {
    navMarker.setLatLng(latlng);
  }
}

export function onTileError(cb) {
  tileErrorCb = cb;
}

/** Fit both the mission area and the current position (SPEC §8.1.3 [내 위치]). */
export function fitAreaAndMe(bbox, lat, lng) {
  if (!map) return;
  const bounds = L.latLngBounds([bbox[0], bbox[1]], [bbox[2], bbox[3]]);
  bounds.extend([lat, lng]);
  map.fitBounds(bounds, { padding: [32, 32], maxZoom: 18 });
}
