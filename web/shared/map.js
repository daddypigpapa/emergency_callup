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
  // dragging/touchZoom explicit (they default to true, but this screen's
  // draggability is exactly the thing users have reported broken, so make
  // the intent unmissable rather than relying on Leaflet's defaults).
  map = L.map(container, { zoomControl: true, attributionControl: true, dragging: true, touchZoom: true });
  const layer = L.tileLayer(tileUrl, { attribution: tileAttribution || '', maxZoom: 19 });
  layer.on('tileerror', () => {
    tileErrorCount++;
    if (tileErrorCount >= 5 && tileErrorCb) tileErrorCb();
  });
  layer.addTo(map);
  areaLayer = L.layerGroup().addTo(map);

  // ensureMap() in f.js runs right after this screen is un-hidden, in the
  // same tick — the browser hasn't necessarily finished laying out the
  // now-visible container yet, so Leaflet can measure it as the wrong (or
  // zero) size and derive a broken internal pixel origin/pan boundary from
  // that. invalidateSize() once the layout has actually settled corrects
  // it; harmless to call if the size was already right.
  requestAnimationFrame(() => { if (map) map.invalidateSize(); });

  return map;
}

/** bbox = [south, west, north, east] (SPEC §8.1.3). */
export function fitArea(bbox) {
  if (!map) return;
  const bounds = L.latLngBounds([bbox[0], bbox[1]], [bbox[2], bbox[3]]);
  map.fitBounds(bounds, { padding: [24, 24], maxZoom: 18 });
}

/** [내 위치]: center tightly on the live position (not the mission area). */
export function centerOnMe(lat, lng) {
  if (!map) return;
  map.setView([lat, lng], 17);
}

/** area = the `task.area` object from GET /f/me (SPEC §7.2). */
export function drawArea(area) {
  if (!map) return;
  areaLayer.clearLayers();
  // No color (per design direction): the mission area is a black outline
  // with a faint black (not colored) fill so it still reads against tiles.
  // interactive:false so the shape never captures a touch/drag that starts
  // on top of it — without this, dragging to pan the map only works when
  // the gesture starts outside the circle, since Leaflet vector layers are
  // interactive (and so claim their own pointer events) by default.
  if (area.kind === 'circle') {
    L.circle([area.lat, area.lng], { radius: area.r, color: '#111', weight: 2, fillColor: '#111', fillOpacity: 0.06, interactive: false }).addTo(areaLayer);
  } else if (area.kind === 'polygon' && area.polygon) {
    L.polygon(area.polygon, { color: '#111', weight: 2, fillColor: '#111', fillOpacity: 0.06, interactive: false }).addTo(areaLayer);
  }
  if (area.nav) {
    setNav(area.nav[0], area.nav[1]);
  }
}

export function setMe(lat, lng, acc) {
  if (!map) return;
  const latlng = [lat, lng];
  if (!meMarker) {
    // interactive:false on every overlay below for the same reason as the
    // area shape: none of them have a click/tap behavior of their own on
    // this screen, so they should never intercept a pan gesture.
    meMarker = L.circleMarker(latlng, { radius: 7, color: '#111', weight: 2, fillColor: '#111', fillOpacity: 1, interactive: false }).addTo(map);
    meAccCircle = L.circle(latlng, { radius: acc || 0, color: '#111', weight: 1, opacity: 0.4, fillOpacity: 0.05, interactive: false }).addTo(map);
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
    navMarker = L.marker(latlng, { interactive: false }).addTo(map);
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
