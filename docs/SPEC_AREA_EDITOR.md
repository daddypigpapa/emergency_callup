# 임무지역·조별 계획 사전 설정 화면 — 구현명세

본 문서는 `docs/SPEC.md`(이하 본 SPEC)의 하위 명세다. 본 SPEC §0 규칙(허용 의존성, 색 없는 UI, innerHTML 금지, 요청 크기 예산)은 그대로 적용된다. 충돌 시 본 문서가 우선하는 항목은 §9에 명시했다.

구현자는 각 작업 패키지(WP)를 순서대로 진행하고, WP마다 `make lint test`가 통과하는 상태로 커밋한다.

---

## 0. 한 줄 요약

관리자 설정 화면에서 **(a)** VWorld 지도 위 격자(칸)를 클릭·드래그·행정동 단위로 골라 임무지역을 만들고, **(b)** 조마다 임무·임무지역·집결지·체크포인트를 미리 정해 저장해 두면, **(c)** 발령 시 그 값이 자동으로 채워지고 현장 화면에 집결지·체크포인트가 표시된다.

## 1. 용어

| 용어 | 정의 |
|---|---|
| 격자 / 칸(cell) | 전국 공통 기준으로 잘라 둔 정사각형 구역. 크기는 100·250·500·1000 m 중 하나. 칸은 `(size, i, j)` 정수 세 개로 유일하게 식별된다 (§3.1) |
| 격자형 임무지역 | 같은 크기의 칸 집합으로 이루어진 임무지역. `area.kind = 'grid'` |
| 집결지(rally) | 조원이 모이는 지점. 조별 하나. 길안내 목적지로 쓰인다. 직접 지정하지 않으면 임무지역 중심(§3.4)이 자동 지정된다 |
| 체크포인트(checkpoint) | 집결지로 가는 길목 또는 임무 중 거쳐야 할 지점. 조별 0~10개, 순서 있음. **이 명세 범위에서는 표시만 하고 통과 판정은 하지 않는다** |
| 조별 계획(team plan) | 평시에 미리 정해 두는 조별 임무·임무지역·집결지·체크포인트. 발령 시 §5.4 자동 채움의 1순위 근거가 된다 |
| 국가기초번호 | 도로명주소법의 "도로명 + 기초번호"(예: `공평로 88`). 건물이 없는 곳도 번호판이 있다. 집결지·체크포인트 위치 입력의 기본 방식 (§4.3). **주의**: "국가지점번호"(가나다+숫자 8자리 격자 주소)와 다르다. 이 명세는 기초번호를 구현하고 지점번호는 §10 범위 밖으로 둔다 |

## 2. 결정 사항 (구현 전에 알아야 하는 것)

1. **외부 API는 전부 서버가 대신 호출한다.** 브라우저 CSP가 `connect-src 'self'`(`internal/httpapi/middleware.go:20`)라 프론트에서 VWorld를 직접 부를 수 없다. 서버는 이미 있는 VWorld 키(`effectiveTileKey`)를 그대로 쓴다. 새 Go 의존성은 추가하지 않는다(`net/http`, `encoding/json`으로 충분).
2. **`area.kind`에 `'grid'`를 추가하려면 `area` 테이블을 재생성해야 한다.** `001_init.sql`의 `CHECK (kind IN ('circle','polygon'))`은 ALTER로 못 바꾼다. 마이그레이션 러너에 FK-off 지시자를 추가한다 (§3.6).
3. **격자 판정은 다각형 변환 없이 칸 집합으로 직접 한다.** 칸 합집합 외곽선을 계산하지 않는다. "점이 어느 칸에 있나"는 O(1), 경계 거리는 칸별 사각형 거리의 최솟값이다 (§3.3).
4. **집결지는 기존 `nav`(길안내 목적지) 개념을 조 단위로 끌어올린 것이다.** 현장 화면은 이미 `area.nav`로 길안내·마커를 그리므로, 서버가 `/f/me` 응답의 `area.nav`를 조의 집결지로 덮어쓰면 현장 화면 변경이 최소화된다.
5. **조별 계획은 평시 데이터, 발령 시 `team_task`로 복사(스냅샷)된다.** 상황 중에 계획을 고쳐도 진행 중인 발령에는 영향이 없다 (본 SPEC의 "상황 중 지역 불변" 원칙).
6. **행정동 선택은 "그 동에 속한 칸을 한꺼번에 고르는 것"이다.** 동 경계 다각형 자체를 임무지역으로 저장하지 않는다. 격자가 기본 단위라는 요구(§0)에 맞추고, 저장 형식·판정 로직을 하나로 유지한다. 동 경계선은 편집 중 참고선으로만 점선 표시한다.
7. **기존 원(circle) 임무지역과 API는 그대로 둔다.** 설정 화면에서 원 만들기도 계속 가능하다. 다각형(`polygon`)은 API로만 만들 수 있는 현 상태를 유지한다(그리기 UI는 범위 밖).

## 3. 데이터 모델·지오메트리

### 3.1 격자 정의 (Go·JS 공통, 상수·식 동일해야 함)

```
REF_LAT      = 36.0                       // 전국 공통 기준 위도 (도)
M_PER_DEG_LAT = 111320.0
dLat(size)   = size / M_PER_DEG_LAT                          // 도
dLng(size)   = size / (M_PER_DEG_LAT * cos(REF_LAT * π/180)) // 도

cellOf(size, lat, lng) = ( i = floor(lng / dLng(size)),  j = floor(lat / dLat(size)) )
cellBounds(size, i, j) = [ south = j*dLat, west = i*dLng, north = (j+1)*dLat, east = (i+1)*dLng ]
cellCenter(size, i, j) = ( (j+0.5)*dLat, (i+0.5)*dLng )
```

- 기준 위도를 고정했으므로 위도 33~38.6°에서 칸의 동서 폭은 명목 크기 대비 약 ±5% 오차가 있다. 허용한다 (이탈 여유값 30 m와 GPS 오차 범위 안).
- 칸 크기 허용값: `100, 250, 500, 1000`. 그 외는 400.
- 임무지역 하나의 칸 수: `1 ≤ n ≤ 2500`. 초과 시 400 + 메시지 "칸이 너무 많습니다. 더 큰 격자 크기를 쓰세요." 편집 화면은 500칸 초과 시 경고 문구(저장은 허용).
- 칸 목록은 `[[i,j], ...]` 정수 쌍 배열. 중복 금지(서버에서 정렬·중복 제거 후 저장). 모든 칸의 중심이 `area.InKorea`를 만족해야 한다.

### 3.2 스키마 변경 — `003_area_grid_team_plan.sql`

```sql
-- +fk_off   (러너 지시자, §3.6)

-- 1) area: kind에 'grid' 추가 + grid_size, cells 컬럼. 재생성.
CREATE TABLE area_new (
  id        INTEGER PRIMARY KEY,
  name      TEXT NOT NULL,
  kind      TEXT NOT NULL CHECK (kind IN ('circle','polygon','grid')),
  lat       REAL,
  lng       REAL,
  radius_m  INTEGER CHECK (radius_m BETWEEN 50 AND 5000),
  polygon   TEXT,
  grid_size INTEGER CHECK (grid_size IN (100,250,500,1000)),
  cells     TEXT,
  nav_lat   REAL NOT NULL,
  nav_lng   REAL NOT NULL,
  bbox      TEXT NOT NULL,
  active    INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL
);
INSERT INTO area_new (id,name,kind,lat,lng,radius_m,polygon,nav_lat,nav_lng,bbox,active,created_at)
  SELECT id,name,kind,lat,lng,radius_m,polygon,nav_lat,nav_lng,bbox,active,created_at FROM area;
DROP TABLE area;
ALTER TABLE area_new RENAME TO area;
CREATE UNIQUE INDEX area_active_name ON area(name) WHERE active = 1;

-- 2) 조별 계획 (평시)
CREATE TABLE team_plan (
  team_no    INTEGER PRIMARY KEY REFERENCES team(no),
  mission    TEXT NOT NULL DEFAULT '',
  area_id    INTEGER REFERENCES area(id),
  rally_lat  REAL,            -- NULL = 자동(임무지역 중심)
  rally_lng  REAL,
  rally_addr TEXT,            -- 입력했던 기초번호/주소 원문 (표시용)
  updated_by TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE checkpoint (
  id        INTEGER PRIMARY KEY,
  team_no   INTEGER NOT NULL REFERENCES team(no),
  seq       INTEGER NOT NULL,          -- 1부터, 조 안에서 유일
  name      TEXT NOT NULL,
  lat       REAL NOT NULL,
  lng       REAL NOT NULL,
  addr      TEXT,
  radius_m  INTEGER NOT NULL DEFAULT 50 CHECK (radius_m BETWEEN 20 AND 500),
  UNIQUE (team_no, seq)
);

-- 3) 발령 시 스냅샷
ALTER TABLE team_task ADD COLUMN rally_lat REAL;
ALTER TABLE team_task ADD COLUMN rally_lng REAL;
ALTER TABLE team_task ADD COLUMN checkpoints TEXT;   -- JSON [{seq,name,lat,lng,r}], NULL/'' = 없음
```

- `area` 재생성 후 `PRAGMA foreign_key_check`가 0행이어야 한다 (러너가 검사).
- `area.lat/lng`는 grid에서도 채운다 = §3.4의 중심점. `radius_m`, `polygon`은 NULL.

### 3.3 Go `internal/area` 확장

```go
const KindGrid = "grid"
const (GridMinCells = 1; GridMaxCells = 2500)
var GridSizes = []int{100, 250, 500, 1000}

type Cell struct{ I, J int64 }

func CellOf(size int, p Point) Cell
func CellBounds(size int, c Cell) [4]float64      // [S,W,N,E]
func CellCenter(size int, c Cell) Point
func ValidateGrid(size int, cells []Cell) error   // 크기·개수·중복·InKorea
func GridBBox(size int, cells []Cell) [4]float64
func GridCenter(size int, cells []Cell) Point     // §3.4
func SignedDistanceGrid(size int, cells []Cell, p Point) float64
func REqGrid(size int, cells []Cell) float64      // sqrt(n*size²/π)
```

`SignedDistanceGrid` 규칙 (부호: 안쪽 음수, 바깥 양수 — 기존 `SignedDistanceCircle/Polygon`과 동일):
- `c := CellOf(size,p)`가 집합에 있으면: `p`에서 그 칸의 네 변까지 거리 중, **건너편 칸이 집합에 없는 변**만 골라 최솟값에 `-`를 붙인다. 네 이웃이 모두 집합에 있으면 `-(size/2)`를 반환한다(깊숙이 안쪽; 판정 로직은 부호와 30 m 여유만 보므로 충분).
- 집합에 없으면: 모든 칸에 대해 점→사각형 거리(투영은 기존 `projectMeters(refLat, p)` 사용)의 최솟값.
- 성능: 2500칸 × 초당 최대 100회 판정 = 25만 사각형 거리/초. 단순 루프로 충분하다. 최적화하지 않는다.

`Row`에 `GridSize int`, `Cells []Cell` 추가. `Row.SignedDistance`, `Row.REq`가 `KindGrid`를 분기한다. `scanRow`/`insert`/`selectCols`에 두 컬럼 추가. `Store.CreateGrid(ctx, name, size, cells, nav *Point, now)` 추가. `resolveNav`는 grid일 때 `GridCenter`를 기본값으로 쓴다(항상 칸 안이므로 `ErrNavOutside` 없음).

새로 추가: `Store.Update(ctx, id, ...)`와 `Store.Deactivate(ctx, id)`. 둘 다 **활성 사건의 `team_task.area_id` 또는 `assignment.eff_area_id`가 그 지역을 참조하면 `ErrAreaInUse`** 를 돌려준다(409). 이름 변경은 활성 유일 인덱스 충돌 시 409.

### 3.4 중심점 규칙 (집결지 자동값)

| kind | 중심 |
|---|---|
| circle | 중심 좌표 |
| polygon | 기존 `Centroid` (기존 규칙 유지: 다각형 밖이면 저장 거부) |
| grid | 모든 칸 중심의 산술평균을 구한 뒤, **그 평균에 가장 가까운 칸의 중심**. L자·ㄷ자 지역에서도 항상 임무지역 안에 놓이도록 |

### 3.5 조별 계획·스냅샷 규칙

- `team_plan.rally_lat IS NULL` ⇒ 자동. 표시·스냅샷 시점에 `area.nav`(= §3.4 중심 또는 지역에 직접 지정된 nav)를 쓴다.
- `POST /a/incidents`(발령) 시 각 조의 `team_task`에 복사한다:
  - `team_task.area_id == team_plan.area_id`이면 `rally_*`, `checkpoints`를 계획에서 복사. 자동 집결지는 이 시점에 좌표로 확정해 넣는다.
  - 다르면(발령 화면에서 관리자가 지역을 바꿨으면) `rally = 새 지역의 nav`, `checkpoints = NULL`.
- `PUT /a/incidents/current/teams/{no}`(상황 중 조 임무 변경)도 같은 규칙으로 `rally_*`, `checkpoints`를 다시 채운다.
- `/f/me` 응답 조립 시 `area.nav`를 `team_task.rally`로 덮어쓴다. 개인 지역 오버라이드(`area_override_id`)가 있는 대원은 덮어쓰지 않는다(그 지역의 nav 그대로).

### 3.6 마이그레이션 러너 변경 (`internal/store/store.go`)

파일 첫 줄이 `-- +fk_off`이면:
1. `PRAGMA foreign_keys=OFF` (트랜잭션 밖에서; `SetMaxOpenConns(1)`이므로 같은 연결이 보장된다)
2. 트랜잭션 안에서 SQL 실행 + `schema_migrations` 기록
3. 커밋 전에 `PRAGMA foreign_key_check` 실행, 결과 행이 있으면 롤백하고 에러
4. `PRAGMA foreign_keys=ON`

지시자가 없으면 기존과 동일. `store_test.go`에 "003 적용 후 기존 area 행·member.area_id 참조가 그대로 유지된다" 테스트를 추가한다.

## 4. 서버 API

모든 경로는 `/api/v1` 아래. 역할 게이트는 기존 관례를 따른다: 설정 성격의 쓰기·읽기는 `RoleAdmin`, 발령 관련은 `RoleOperator`.

### 4.1 임무지역

| 메서드·경로 | 역할 | 요청 | 응답 / 비고 |
|---|---|---|---|
| `GET /a/areas` | admin | – | 기존 + 각 항목에 `size`, `cells`(grid일 때) 추가 |
| `POST /a/areas` | admin | `{name, kind:'grid', size, cells:[[i,j],...], nav?:[lat,lng]}` | `{id}`. circle/polygon 요청 형식은 기존 유지 |
| `PUT /a/areas/{id}` | admin | POST와 동일 본문 (kind 변경 허용) | 활성 사건이 쓰는 지역이면 `409 in_use` |
| `DELETE /a/areas/{id}` | admin | – | `active=0`. 활성 사건이 쓰면 `409 in_use`. `member.area_id`가 참조 중이면 `409 referenced` + 참조 인원 수 |
| `GET /a/geo/cell?size=&lat=&lng=` | admin | – | `{i,j,bounds:[S,W,N,E]}`. 프론트 격자 계산이 서버와 같은지 검증용 |

### 4.2 행정동 조회 (VWorld 데이터 API 프록시)

`GET /a/geo/admin?q=<동 이름>` (admin)

- 서버 → `https://api.vworld.kr/req/data?service=data&request=GetFeature&data=LT_C_ADEMD_INFO&key={key}&domain={BASE_URL host}&attrFilter=emd_kor_nm:like:{q}&geometry=true&crs=EPSG:4326&size=20&page=1&format=json`
- 타임아웃 8초. 실패 시 `502 upstream` + 메시지 "행정동 정보를 가져오지 못했습니다. VWorld 키에 데이터 API 권한이 있는지 확인하세요."
- 응답 가공: feature마다 `{code: emd_cd, name: emd_kor_nm, full: full_nm, rings: [[[lat,lng],...], ...]}`. MultiPolygon은 외곽 링만 전부 포함(구멍 무시). 각 링은 Douglas-Peucker(허용오차 0.00005° ≈ 5 m)로 단순화, 링당 최대 2000점.
- 결과를 서버 메모리에 10분 캐시(키: q). 동시 요청 폭주 방지용, 필수 아님.
- 응답 최대 20건.

### 4.3 주소(기초번호) → 좌표 (VWorld 지오코더 프록시)

`GET /a/geo/geocode?q=<도로명 기초번호>` (admin)

- 서버 → `https://api.vworld.kr/req/address?service=address&request=getcoord&version=2.0&crs=epsg:4326&address={q}&refine=true&simple=false&format=json&type=road&key={key}`
- `type=road`로 없으면 `type=parcel`로 한 번 더 시도.
- 응답: `{lat, lng, matched: "대구광역시 중구 공평로 88", type: "road"|"parcel"}`. 없으면 `404 not_found` + "해당 기초번호를 찾지 못했습니다. 도로명과 번호를 확인하세요."
- 입력이 국가지점번호 패턴(`^[가-힣]{2}\s*\d{4}\s*\d{4}$`)이면 400 + "국가지점번호는 아직 지원하지 않습니다. 도로명+기초번호(예: 공평로 88)로 입력하세요."

`GET /a/geo/revgeocode?lat=&lng=` (admin, 선택 구현 — WP6)
- `request=getaddress&type=road` → `{addr}`. 지도 클릭으로 집결지를 찍었을 때 표시용 주소를 채운다. 실패해도 저장에는 지장 없음.

### 4.4 조별 계획

`GET /a/team-plans` (operator)
```json
{ "t": 0, "teams": [
  { "no": 1, "name": "1조", "mission": "...", "areaId": 2,
    "rally": { "lat": 35.87, "lng": 128.60, "addr": "공평로 88", "auto": false },
    "checkpoints": [ { "seq": 1, "name": "진입로", "lat": 0, "lng": 0, "addr": "...", "r": 50 } ] }
] }
```
- 계획이 없는 조도 10개 전부 돌려준다(빈 값). `rally.auto=true`이면 `lat/lng`는 지역 중심을 계산해 채워서 준다(지역이 없으면 null).

`PUT /a/team-plans` (admin) — **일괄 저장**
```json
{ "teams": [ { "no": 1, "mission": "...", "areaId": 2,
               "rally": null | { "lat":..., "lng":..., "addr":"..." },
               "checkpoints": [ { "name":"...", "lat":..., "lng":..., "addr":"...", "r": 50 } ] } ] }
```
- 한 트랜잭션에서 보낸 조는 전부 upsert, 보내지 않은 조는 그대로. `checkpoints`는 통째로 교체(삭제 후 순서대로 seq 재부여, 최대 10개).
- 검증: `areaId`는 활성 지역이어야 함(없으면 400). 좌표는 `InKorea`. `rally`가 있는데 지역도 있으면 **집결지가 지역 밖이어도 허용**(주차장 등이 밖일 수 있음)하되 응답에 `warnings: ["3조 집결지가 임무지역 밖입니다"]`를 붙인다.
- 감사 이벤트 `team_plan_update` 1건(변경된 조 번호 목록을 data에).

### 4.5 기존 API 변경

- `GET /a/incidents/draft`: `teams[]`에 `fromPlan: true|false`, `rally`, `checkpoints` 추가. `BuildDraft`는 **`team_plan`에 그 조의 `mission`(비어있지 않음) 또는 `area_id`가 있으면 그 값을 우선**하고, 없는 항목만 기존 §5.4 다수값 규칙으로 채운다. 개인 오버라이드 규칙(§5.4 rule 2)은 "조 기본값과 다른 개인 평시값" 기준 그대로.
- `POST /a/incidents`, `PUT /a/incidents/current/teams/{no}`: §3.5 스냅샷.
- `GET /a/snapshot`, `GET /a/delta`: `teams[]`에 `rally:[lat,lng]`, `cps:[[seq,lat,lng],...]` 추가. `areas[]`에 grid 필드 추가.
- `GET /f/me`: `task.area.nav` = 집결지(§3.5). `task.cps: [[seq,name,lat,lng], ...]` 추가(없으면 생략). `task.area`에 `size`, `cells` 포함(grid일 때; `polygon`은 null).

## 5. 관리자 설정 화면 (`web/a/setup.html`)

기존 "임무지역" 탭을 아래 두 탭으로 대체한다. 지도는 두 탭이 하나의 Leaflet 인스턴스를 공유해도 되고 각각 만들어도 된다(탭 전환 시 `invalidateSize()` 필수).

### 5.1 탭 "임무지역" — 격자 편집기

레이아웃(PC 기준, 900px 미만은 세로 쌓기): 좌측 지도(높이 70vh), 우측 320px 패널.

우측 패널
1. 지역 목록(표: 이름 · 종류 · 크기/칸 수 · [편집] [삭제]). [삭제]는 confirm 후 DELETE, 409면 이유 표시.
2. "새 지역" / 편집 폼
   - 이름 (필수)
   - 종류: `○ 격자  ○ 원`. 원 선택 시 기존 위도·경도·반경 입력 폼 + "지도 클릭으로 중심 지정" 버튼.
   - 격자 크기: `100 / 250 / 500 / 1000 m` (변경 시 선택 칸 전부 초기화 — confirm)
   - 도구(라디오처럼 하나만 활성): **[칸 선택]** **[드래그 선택]** **[행정동 선택]**, 그리고 **[모두 지우기]**
   - 선택 칸 수 표시. 500 초과 시 경고문(저장은 허용, 2500 초과 시 저장 버튼 비활성).
   - 행정동 선택 시: 검색어 입력 + [검색] → 후보 목록(전체 이름) → 클릭하면 **(a)** 동 경계선을 지도에 점선으로 그리고 **(b)** 그 동 안의 칸을 전부 선택에 **추가**한다. 같은 동을 다시 클릭하면 그 칸들을 제거(토글).
   - 집결지(선택): "지역 기본 길안내 지점" — 비우면 자동(§3.4). 입력 방식은 §5.3의 위치 입력 위젯.
   - [저장] [취소]

지도 동작
- 타일: 기존 `/api/v1/config`의 `tileUrl`. 초기 시점: 저장된 지역이 있으면 전체 bbox, 없으면 `[36.5,127.8] z7`.
- **격자선**: `moveend`/`zoomend`마다 보이는 범위의 칸 경계선을 다시 그린다. 보이는 칸이 4000개를 넘으면 격자선을 그리지 않고 지도 위에 "격자를 보려면 확대하세요" 안내를 띄운다. 격자선은 세로선·가로선을 각각 하나의 `L.polyline`(다중 세그먼트)로 그린다.
- **선택 칸 표시**: `L.canvas()` 렌더러를 쓰는 별도 레이어그룹에 `L.rectangle`(interactive:false, 검은 테두리 1px, 검은 채움 불투명도 0.18). 같은 j행에서 i가 연속인 칸은 사각형 하나로 합쳐 그린다(런 병합) — 개수를 1/10 수준으로 줄이기 위함.
- **[칸 선택]**: 지도 `click` → `cellOf(size, latlng)` 토글. 지도 드래그(팬)는 그대로 동작.
- **[드래그 선택]**: `map.dragging.disable()`. 컨테이너에 `pointerdown → pointermove → pointerup` 처리. 누른 지점과 현재 지점으로 고무줄 사각형(`L.rectangle`, 점선)을 그리고, 놓으면 그 사각형과 겹치는 칸을 전부 **추가**. `Shift`를 누른 채면 **제거**. 도구를 바꾸면 `map.dragging.enable()`.
- **[행정동 선택]**: 후보 선택 시 서버에서 받은 링들에 대해, 동 bbox 안 칸 각각의 중심점을 ray-casting으로 판정(JS `pointInRings`). 링 점수 합이 커도 bbox 안 칸 수 × 점수 ≤ 수백만 연산이라 동기 처리로 충분.
- 편집 중인 지역 외의 다른 지역들은 옅은 점선 외곽(bbox 사각형)만 표시한다(참고용).

`web/shared/grid.js`(신규, ES 모듈 — 관리자·현장 양쪽이 import): §3.1 상수·함수, 런 병합. `web/a/grid-edit.js`(신규, 관리자만): `pointInRings`, 격자선 생성, 드래그 선택. **서버 `internal/area/grid.go`와 상수·식이 글자 단위로 같아야 하며, 파일 상단 주석에 상대 경로로 서로를 가리킨다.**

### 5.2 탭 "조별 계획"

레이아웃: 좌측 지도(공유), 우측 패널에 조 목록(1~10조, 세로).

각 조 행(펼침식, 한 번에 하나만 펼침):
- 임무: 텍스트 입력. `<datalist>`로 `preset(kind=mission)` 제안.
- 임무지역: `<select>` (활성 지역). 선택 시 지도에 그 지역을 그리고 fit.
- 집결지: `○ 자동(임무지역 중심)  ○ 직접 지정`. 직접 지정이면 §5.3 위젯. 자동이면 계산된 좌표를 회색으로 표시하고 지도에 ▲ 마커(interactive:false).
- 체크포인트: 목록(순번 · 이름 · 주소 · 반경 · [↑][↓][삭제]) + [추가]. 추가 시 이름·§5.3 위젯·반경(기본 50). 지도에 순번 붙은 □ 마커.
- 지도에서: 펼친 조의 지역 + 집결지 ▲ + 체크포인트 □(번호) + 집결지→체크포인트 순서대로 잇는 점선(참고용).

패널 하단: **[전체 저장]** 하나. 10개 조를 한 번에 `PUT /a/team-plans`. 성공 시 "저장됨 HH:MM:SS", `warnings`가 있으면 그 아래 나열. 페이지 이탈 시 미저장 변경이 있으면 `beforeunload` 경고.

### 5.3 위치 입력 위젯 (집결지·체크포인트 공통)

```
[ 도로명 + 기초번호 (예: 공평로 88)      ] [조회]   또는  [지도에서 클릭]
   ↳ 조회 결과: "대구광역시 중구 공평로 88"  (35.87140, 128.60140)   [이 위치로]
```
- [조회] → `GET /a/geo/geocode`. 결과를 확인 문구로 보여주고 [이 위치로]를 눌러야 확정된다(오탐 방지). 지도에는 임시 마커로 미리 보여준다.
- [지도에서 클릭] → 다음 지도 클릭 1회를 좌표로 받는다. WP6가 되면 역지오코딩으로 주소칸을 채운다; 아니면 주소칸에 "지도 지정"이라고 넣는다.
- 확정된 값은 `{lat, lng, addr}`로 상위 폼에 전달한다.

### 5.4 CSS

`web/a/setup.css`에 추가. 색 없음 원칙 유지: 선택 칸은 검은 채움 18%, 격자선은 `#111` 불투명도 0.25 두께 1, 동 경계선은 검은 점선 2px, 집결지는 검은 삼각형(`L.divIcon`, CSS로 삼각형), 체크포인트는 검은 테두리 사각형 안에 번호.

## 6. 현장 화면 변경 (`web/f`)

- `task.area.kind === 'grid'`: `map.js`의 `drawArea`가 칸(런 병합된 사각형들)을 그린다. 스타일은 기존 다각형과 동일(검은 테두리 2px, 채움 0.06, interactive:false). `fitArea`는 bbox 그대로 사용(변경 없음).
- 집결지: 변경 없음(`area.nav`를 이미 씀). 마커 아이콘만 ▲ `divIcon`으로 통일(관리자 화면과 동일 기호).
- 체크포인트 `task.cps`: □ 번호 마커(interactive:false). 없으면 아무것도 안 그림.
- R3 "임무지역까지 ~km"는 그대로(지역 경계 기준). 추가 문구 없음.
- 크기 예산: `f.js`+`map.js`+`shared/grid.js` 증가분 ≤ 3 KB(gzip). 현장 화면은 `grid.js`에서 칸 경계·런 병합 함수만 import 한다(행정동 판정·격자선 생성 함수는 tree-shaking이 없으므로 별도 파일 `web/a/grid-edit.js`에 둔다).

## 7. 관리자 상황판 변경 (`web/a/a.js`)

- `redrawAreas`: grid 지역을 런 병합 사각형으로 그린다.
- `focusTeam` 시 그 조의 집결지 ▲와 체크포인트 □를 그린다. 포커스 해제 시 제거. 다른 조의 것은 그리지 않는다(지도 혼잡 방지).

## 8. 테스트 (회귀 목록 — 본 SPEC §15에 이어서 번호 부여)

| ID | 시험 | 기대 |
|---|---|---|
| R19 | `CellOf`/`CellBounds`: 위도 35.8714·경도 128.6014를 250 m 칸으로 → 다시 `CellCenter`·`CellBounds`로 복원했을 때 점이 그 bounds 안 | 통과. 같은 입력의 결과 표를 Go 테스트가 출력하고, `GET /a/geo/cell`로 프론트 `grid.js`와 수동 대조 |
| R20 | `SignedDistanceGrid`: 단일 칸 중심 → `-size/2` 근처 음수; 칸 밖 100 m 지점 → 약 +100; 3×3 블록 중앙 칸 → `-size/2` | 오차 ±3% |
| R21 | `ValidateGrid`: 크기 300 → 400, 2501칸 → 400, 중복 칸 → 정렬·중복 제거 후 저장 | |
| R22 | `GridCenter`: L자(3칸 가로 + 2칸 세로) → 반환점이 항상 어느 칸 중심과 일치 | |
| R23 | 마이그레이션 003: 001·002가 적용된 DB에 circle 지역 1 + 그것을 참조하는 member 1을 넣고 003 적용 → 지역·참조 유지, `foreign_key_check` 0행, `kind='grid'` INSERT 가능 | |
| R24 | `PUT /a/team-plans`: 조 2개 저장 후 GET → 저장한 값, 나머지 8개는 빈 값. 체크포인트 3개 → seq 1,2,3. 다시 2개로 저장 → seq 1,2만 남음 | |
| R25 | 발령 스냅샷: 계획(지역 A, 집결지 수동, cp 2개)이 있는 조를 지역 A로 발령 → `team_task.rally`=수동값, `checkpoints` 2개. 지역 B로 발령 → rally=B의 nav, checkpoints NULL | |
| R26 | 발령 후 계획을 바꿔도 `/f/me`의 nav·cps 불변 | |
| R27 | 활성 사건이 쓰는 지역 `PUT`/`DELETE` → 409 | |
| R28 | `BuildDraft`: 계획에 임무만 있고 지역이 없는 조 → 임무는 계획값, 지역은 §5.4 다수값 | |
| R29 | `/a/geo/geocode`에 국가지점번호 패턴 → 400. 지오코더 HTTP 테스트는 `httptest.Server`로 VWorld 응답을 흉내내어 road 실패→parcel 재시도를 확인 | |
| R30 | `/a/geo/admin`: 흉내낸 VWorld 응답(MultiPolygon 2링, 링당 3000점) → 링 2개, 각 ≤ 2000점, 단순화 후에도 첫 점==마지막 점 | |
| R31 | 개인 지역 오버라이드가 있는 대원의 `/f/me` `area.nav`는 조 집결지로 덮어쓰지 않는다 | |

수동 확인 체크리스트(브라우저, PC 1366×768 이상):
- [ ] 250 m 격자에서 [드래그 선택]으로 20칸 선택 → 저장 → 목록에 "격자 250 m · 20칸" → 상황판에 사각형들로 표시
- [ ] Shift+드래그로 일부 제거
- [ ] [행정동 선택] "삼덕동" 검색 → 선택 → 점선 경계 + 칸 채워짐 → 다시 클릭하면 제거
- [ ] 격자 크기 변경 시 confirm 후 초기화
- [ ] 조별 계획에서 집결지 "공평로 88" 조회 → 확인 문구 → [이 위치로] → ▲ 이동
- [ ] 집결지 자동 ↔ 직접 전환 시 ▲가 지역 중심 ↔ 지정 위치로 이동
- [ ] 체크포인트 3개 추가·순서 변경·삭제 → [전체 저장] → 새로고침 후 유지
- [ ] 발령 → 현장 화면(test 계정)에서 ▲와 □ 1,2,3 표시, 카카오/네이버 길안내가 집결지로 감
- [ ] 발령 중 지역 삭제 시도 → "사용 중" 메시지
- [ ] 360×800(휴대전화)에서 설정 화면이 세로로 쌓이고 격자 편집이 최소한 [칸 선택]으로는 동작

## 9. 본 SPEC과의 관계

- 본 SPEC §4 `area` 정의는 이 문서 §3.2로 갱신된다.
- 본 SPEC §5.4(발령 자동 채움)는 이 문서 §4.5의 "계획 우선" 규칙이 앞에 추가된다.
- 본 SPEC §6.2의 nav 기본값 규칙에 grid 항목(§3.4)이 추가된다.
- 본 SPEC §16-4 "임무지역 도형: 원 + 다각형"에 격자가 추가된다.
- 구현 완료 후 본 SPEC §17에 진행 상황을 기록하고, 위 항목들을 본 SPEC 본문에 반영한다(이 문서를 읽지 않아도 본 SPEC만으로 현재 상태를 알 수 있어야 한다).

## 10. 범위 밖 (하지 않는다)

- 국가지점번호 입력·변환 (패턴만 감지해 안내)
- 체크포인트 통과 판정·통과 시각 기록
- 다각형 그리기 UI (API는 유지)
- 동 경계 다각형을 임무지역으로 직접 저장
- 격자 칸 합집합 외곽선 계산
- 조 이름 편집
- VWorld 데이터 API 키 별도 관리 (타일 키를 그대로 사용)

## 11. 작업 패키지 (이 순서로, 각 WP 끝에 `make lint test` 통과 + 커밋)

| WP | 내용 | 산출물 | 예상 |
|---|---|---|---|
| **WP1** 지오메트리 | `internal/area/grid.go` + 테스트(R19~R22), `web/shared/grid.js`(같은 식) | Go·JS 격자 함수 | 0.5일 |
| **WP2** 스키마·저장소 | 러너 `-- +fk_off`, `003_*.sql`, `area.Row/Store` grid 지원·Update/Deactivate, `team_plan`/`checkpoint` 저장소(`internal/incident/plan.go`), R23·R27 | DB·스토어 | 1일 |
| **WP3** 지역 API | `POST/PUT/DELETE /a/areas` grid, `GET /a/geo/cell`, 스냅샷·`/f/me` 응답에 grid 필드 | API | 0.5일 |
| **WP4** 외부 프록시 | `internal/geo/vworld.go`(데이터 API·지오코더 클라이언트, 인터페이스로 분리해 테스트에서 httptest로 대체), `/a/geo/admin`, `/a/geo/geocode`, R29·R30 | API | 1일 |
| **WP5** 조별 계획 API·발령 연동 | `GET/PUT /a/team-plans`, `BuildDraft` 계획 우선, 발령·조 변경 스냅샷, `/f/me` nav 덮어쓰기·cps, R24~R26·R28·R31 | API | 1일 |
| **WP6** 설정 화면 — 격자 편집기 | §5.1 전부 (역지오코딩 제외) | UI | 1.5일 |
| **WP7** 설정 화면 — 조별 계획 | §5.2·§5.3, 선택: `/a/geo/revgeocode` | UI | 1일 |
| **WP8** 현장·상황판 표시 | §6·§7 | UI | 0.5일 |
| **WP9** 문서 | 본 SPEC §4·§5.4·§6.2·§16·§17 갱신, README 관리자 설명서에 "임무지역·조별 계획 미리 정하기" 절 추가(전문용어 없이) | 문서 | 0.5일 |

WP1~WP5는 UI 없이 `curl`·테스트로 검증 가능하므로 먼저 끝내고, WP6부터 브라우저로 확인한다. WP4는 실제 VWorld 키로 1회 호출해 데이터 API 권한이 열려 있는지 **가장 먼저** 확인한다(권한이 없으면 사용자에게 VWorld 콘솔에서 "데이터 API" 서비스를 추가하도록 안내하고 WP4·행정동 기능만 뒤로 미룬다).
