// Grid-based mission areas (docs/SPEC_AREA_EDITOR.md §3.1). Must compute
// cell indices/bounds/centers with the exact same constants and formulas
// as the Go counterpart (internal/area/grid.go) — the server is the
// source of truth for which cell a coordinate belongs to, but every
// client (admin editor, field screen) needs to agree with it to draw and
// hit-test cells correctly.

export const GRID_SIZES = [100, 250, 500, 1000];
export const GRID_MIN_CELLS = 1;
export const GRID_MAX_CELLS = 2500;

const GRID_REF_LAT = 36.0;
const METERS_PER_DEG_LAT = 111320.0;

function dLat(size) { return size / METERS_PER_DEG_LAT; }
function dLng(size) { return size / (METERS_PER_DEG_LAT * Math.cos(GRID_REF_LAT * Math.PI / 180)); }

/** cellOf(size, lat, lng) -> {i, j} */
export function cellOf(size, lat, lng) {
  return { i: Math.floor(lng / dLng(size)), j: Math.floor(lat / dLat(size)) };
}

/** cellBounds(size, i, j) -> [south, west, north, east] */
export function cellBounds(size, i, j) {
  const dlat = dLat(size), dlng = dLng(size);
  const south = j * dlat, west = i * dlng;
  return [south, west, south + dlat, west + dlng];
}

/** cellCenter(size, i, j) -> [lat, lng] */
export function cellCenter(size, i, j) {
  return [(j + 0.5) * dLat(size), (i + 0.5) * dLng(size)];
}

/** cellKey({i,j}) -> string, for use as a Set/Map key. */
export function cellKey(c) { return `${c.i},${c.j}`; }

/** bboxOfCells(size, cells) -> [south, west, north, east]. cells: [{i,j},...] */
export function bboxOfCells(size, cells) {
  let [south, west, north, east] = cellBounds(size, cells[0].i, cells[0].j);
  for (let k = 1; k < cells.length; k++) {
    const b = cellBounds(size, cells[k].i, cells[k].j);
    south = Math.min(south, b[0]);
    west = Math.min(west, b[1]);
    north = Math.max(north, b[2]);
    east = Math.max(east, b[3]);
  }
  return [south, west, north, east];
}

// Run-merge adjacent cells sharing the same row (j) and consecutive i into
// a single rectangle, so drawing N cells in a row costs one L.rectangle
// instead of N (docs/SPEC_AREA_EDITOR.md §5.1). Input order doesn't matter.
// Returns [{i0, i1, j}, ...] — inclusive i range per row.
export function mergeRuns(cells) {
  const byRow = new Map();
  for (const c of cells) {
    if (!byRow.has(c.j)) byRow.set(c.j, []);
    byRow.get(c.j).push(c.i);
  }
  const runs = [];
  for (const [j, isRaw] of byRow) {
    const is = [...isRaw].sort((a, b) => a - b);
    let start = is[0], prev = is[0];
    for (let k = 1; k <= is.length; k++) {
      const cur = is[k];
      if (cur === prev + 1) { prev = cur; continue; }
      runs.push({ i0: start, i1: prev, j });
      if (k < is.length) { start = cur; prev = cur; }
    }
  }
  return runs;
}

/** runToBounds(size, run) -> [south, west, north, east] for a merged run. */
export function runToBounds(size, run) {
  const a = cellBounds(size, run.i0, run.j);
  const b = cellBounds(size, run.i1, run.j);
  return [a[0], a[1], b[2], b[3]];
}
