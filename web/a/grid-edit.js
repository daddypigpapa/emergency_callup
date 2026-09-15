// Admin-only grid-editor helpers (docs/SPEC_AREA_EDITOR.md §5.1): point-in-
// polygon for administrative-dong cell selection. Split out of
// web/shared/grid.js, which the field screen also imports, to keep that
// bundle from carrying code it never uses.

/** Ray-casting point-in-ring test. ring: [[lat,lng], ...]. */
export function pointInRing(lat, lng, ring) {
  let inside = false;
  for (let i = 0, j = ring.length - 1; i < ring.length; j = i++) {
    const [latI, lngI] = ring[i];
    const [latJ, lngJ] = ring[j];
    if ((lngI > lng) !== (lngJ > lng)) {
      const latAt = latI + (lng - lngI) / (lngJ - lngI) * (latJ - latI);
      if (lat < latAt) inside = !inside;
    }
  }
  return inside;
}

/** True if (lat,lng) falls inside any of a dong's (possibly multiple) outer rings. */
export function pointInRings(lat, lng, rings) {
  for (const ring of rings) {
    if (pointInRing(lat, lng, ring)) return true;
  }
  return false;
}

/** [south, west, north, east] covering every ring's points. */
export function boundsOfRings(rings) {
  let south = Infinity, west = Infinity, north = -Infinity, east = -Infinity;
  for (const ring of rings) {
    for (const [lat, lng] of ring) {
      if (lat < south) south = lat;
      if (lat > north) north = lat;
      if (lng < west) west = lng;
      if (lng > east) east = lng;
    }
  }
  return [south, west, north, east];
}
