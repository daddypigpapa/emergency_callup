# 비상 인력동원 위치관제 시스템 — 설계지침 · 구현명세

- 문서 버전: 0.1 (2026-09-11)
- 대상 독자: 구현 담당 Claude Code 에이전트, 검토자
- 근거: `panic235-lab/testai`(인력동원상황실) 코드 검토 결과(치명 5 · 높음 10 · 중간 10)
- 설계 목표 한 줄: **서버 1대, 외부 의존 최소, 요청 1건 수백 바이트로 500명을 추적한다.**

---

## 0. 이 문서를 쓰는 규칙 (에이전트 필독)

1. 이 문서가 기준이다. 문서에 없는 기능은 만들지 않는다.
2. 모호하거나 문서끼리 충돌하면 구현을 멈추고 질문한다. 임의로 고르지 않는다.
3. 단계(§14)마다 수용 기준을 통과한 뒤 다음 단계로 간다.
4. 구현이 문서와 달라져야 하면 **문서를 먼저 고치고** 같은 커밋에 코드를 넣는다. (testai의 README·PRD 불일치 재발 방지)
5. 의존성을 추가하려면 §3.2의 허용 목록에 있어야 한다. 없으면 질문한다.
6. 16장 "미결 사항"은 사용자 결정 전까지 기본값으로 구현하고, 코드에 `// DECISION:` 주석으로 표시한다.

---

## 1. 목표와 범위

### 1.1 기능 요구사항 (사용자 원문 요약)

| 번호 | 요구사항 |
|---|---|
| F1 | 사용자는 관리자와 현장인력으로 나뉜다. 두 화면은 별개다. |
| F2 | 웹 기반. 추후 앱으로 확장할 수 있어야 한다. |
| F3 | 10개조 × 50명(500명)의 현위치를 실시간으로 받는다. 송수신 부담을 줄인다. 비상상황에서 작동한다. |
| F4 | 관리자 화면은 PC·모바일에서 같은 구성·같은 비율. 위에서부터 현재시간 → 발령종류 → 임무조별 임무부여현황 → 임무지역 지도. |
| F5 | 관리자는 사전 입력된 발령메시지·구성원별 임무내용·임무지역을 선택하거나 작성해 부여하고, 중간에 수정할 수 있다. |
| F6 | 현장인력 데이터: 소속(부서), 이름, 행정전화(4자리), 휴대전화(11자리), 편성조, 임무내용, 임무지역, 비고. |
| F7 | 문자로 비상발령 → 문자 속 링크 → 현장인력 페이지 → ID·비밀번호 로그인 → 위치권한 허용 팝업. |
| F8 | 현장 화면: 위에서부터 현재시간 → 발령종류 → 편성조·부여임무 → 임무지역 지도(임무지역이 한 화면에 들어오게 축척 조정). |
| F9 | 지도 기반 데이터는 가장 빠른 것으로 선정. 카카오맵·네이버지도 "가는길" 버튼 연동. |
| F10 | 임무지역 도착 시 자동 응소판정. |
| F11 | 임무 중 임무지역 이탈 시 팝업이 아닌 상단 전광판에 "임무지역 이탈". |
| F12 | 응소 확인된 인원의 전광판에 조별 임무 표시. 예: `3조 임무 : 차단선 구축` |
| F13 | 관리자가 조별 임무·임무지역을 바꾸면 해당 조원 전체에 '임무변경확인' 팝업 → 누르면 서버에서 임무를 다시 받아온다. |
| F14 | 모든 상황이 끝나면 '상황종료' 메시지를 발송한다. |
| F15 | 운영 환경은 기관 자체 서버 또는 클라우드. 문자는 기존 문자시스템 수동 발송과 문자 API 연동을 모두 지원. |

### 1.2 v1 범위 밖

- 네이티브 앱, 백그라운드 위치 추적, 푸시 알림 (→ v2, §12.5)
- 동시에 여러 사건 운영 (v1은 활성 사건 1건)
- 사진·파일·채팅 메시지
- 지도 안에서 경로 그리기 (길 안내는 카카오·네이버 앱에 맡김, §10)

### 1.3 전제와 한계 (사용자에게 숨기지 않을 것)

- **웹은 화면이 꺼지거나 브라우저가 뒤로 가면 위치 전송이 멈춘다.** 현장 화면은 이 사실을 안내하고 "화면 켜두기"를 제공한다. 근본 해결은 v2 앱.
- **웹에서는 위치 조작(가짜 GPS)을 막을 수 없다.** v1은 의심 표시(§6.4)까지만 한다.
- 문자 도달은 문자 시스템 책임이다. 이 시스템은 발송 기록과 미접속자 재발송만 맡는다.

---

## 2. 설계지침

### 2.1 경량화 원칙

| 원칙 | 구체 규칙 |
|---|---|
| 서버 1대로 끝낸다 | 단일 바이너리 + SQLite 파일 1개. Redis·MQ·별도 DB 서버 금지. |
| 요청을 작게 | 위치 보고 요청 본문 ≤ 80B, 응답 본문 ≤ 120B. 헤더를 줄이려고 쿠키는 1개만. |
| 요청을 적게 | 움직임이 없으면 보내지 않는다(거리·시간 조건, §6.1). 서버가 전송 주기를 내려준다. |
| 연결을 단순하게 | HTTP 요청·응답만 사용. WebSocket·SSE 금지. 기관 프록시·방화벽 통과 문제를 원천 차단. 서버 알림은 응답에 버전 번호를 실어 보낸다(piggyback). |
| 바뀐 것만 | 관리자 화면은 `since` 커서로 변경분만 받는다. |
| 외부 의존 0 | 모든 JS·CSS는 서버가 직접 제공. 외부 CDN·웹폰트 금지. 예외는 지도 타일뿐. |
| 빌드 단계 없음 | 프론트엔드는 바닐라 JS(ES 모듈). 번들러·프레임워크 금지. |
| 지도는 보조 | 지도가 안 떠도 임무·임무지역명·거리·가는길 버튼은 먼저 보인다. |

### 2.2 testai 교훈 → 설계 결정

| testai 문제 | 이번 설계의 결정 | 관련 절 |
|---|---|---|
| C1 알림이 실제로 안 감 | 문자 발송 모듈을 교체 가능하게 분리. 수동/HTTP 두 방식, 수신자별 발송 기록, 미접속자 재발송 | §9 |
| C2 무료 호스팅에서 데이터 휘발 | 영구 볼륨 필수, 잠드는 호스팅 금지, 세션도 DB 저장, 일일 백업 | §12 |
| C3 이름+전화번호로 로그인·위치 위조 | 개인 ID·비밀번호만 허용. 위치 이상치 의심 표시. 판정은 서버가 한다 | §6, §11 |
| C4 기본 암호 0000, 시도 제한 없음 | 기본 비밀번호 없음. 설정 누락 시 기동 거부. 로그인 지연 증가 방식 제한 | §11 |
| C5 innerHTML XSS, 업로드 파일 실행 | 화면 출력은 `textContent`만. `innerHTML` 사용 금지(정적 검사). 업로드는 명단 xlsx만, 저장·공개 안 함. CSP | §11 |
| H1 상황 종료 기능 없음 | 사건(incident) 테이블. 종료 시 서버가 위치 수집 중단 신호를 보냄 | §4, §7 |
| H2 전역 단일 상태 덮어쓰기 | 사건별 배정 테이블. 조 A 변경이 조 B 기록에 영향 없음 | §4, §5 |
| H3 발령 전 위치로 도착 판정 | 활성 사건의 배정 대상자만 판정 | §6.2 |
| H4 서버 시간대로 시각 9시간 오차 | 저장은 UTC 밀리초 정수. 표시는 항상 `Asia/Seoul` 명시 | §7.1 |
| H5·H6·H10 계정 생성·선점·삭제 문제 | 명단 등록 = 계정 발급. 초기 비밀번호는 무작위 1회 표시. 비활성화 즉시 세션 폐기 | §4, §11 |
| H7 전화번호 표기 차이 중복 | 숫자 11자리로 정규화 저장 + DB 제약 | §4 |
| H8 다각형 순서 오류 | 자기교차 다각형 저장 거부 | §6.2 |
| H9 판정 대체 경로가 죽어 있음 | 판정 경로는 하나(배정된 임무지역)만 | §6.2 |
| M1 동기 해시로 서버 정지 | 해시 동시 실행 수 제한(CPU 코어 수) | §11 |
| M2 위치 타이머 미해제 | 전송 루프는 객체 1개가 소유, 종료 시 모두 해제 | §8.1 |
| M6 감사 기록 없음 | 모든 변경을 event 테이블에 기록(누가·언제·무엇) | §4 |
| M8 외부 CDN·타일 의존 | 라이브러리 자체 호스팅. 타일은 어댑터로 교체 가능 | §10 |
| 문서–코드 불일치 | §0 규칙 4 | §0 |

---

## 3. 시스템 구조

### 3.1 구성도

```
[현장인력 휴대폰 브라우저]                         [관리자 PC/휴대폰 브라우저]
   │ POST /api/v1/f/fix (≈60B)                        │ GET /api/v1/a/delta?since=N (3초)
   │ ← {st, mv, iv, n, t} (≈100B)                     │ ← 변경된 인원 행만
   │                                                  │
   └──────────── HTTPS (리버스 프록시 또는 내장 TLS) ───┘
                          │
                 ┌────────▼─────────┐
                 │ 서버 단일 바이너리 │  Go, net/http
                 │  ├ 인증·세션       │
                 │  ├ 사건·임무 관리  │
                 │  ├ 위치 추적기     │  메모리 상태 + 2초 배치 쓰기
                 │  ├ 문자 모듈       │  manual(항상) + http(선택)
                 │  └ 정적 파일(embed)│
                 └────────┬─────────┘
                          │
                   SQLite (WAL) /data/app.db  + /data/backup/

[지도 타일 서버(VWorld 등)] ← 브라우저가 직접 요청 (서버 경유 안 함)
[카카오맵·네이버지도 앱]   ← 가는길 버튼이 URL로 호출
[기관 문자시스템 또는 문자 API]
```

### 3.2 기술 선택

| 영역 | 선택 | 이유 | 대안(채택 안 함) |
|---|---|---|---|
| 서버 언어 | **Go** (1.22 이상, `net/http` 라우팅 패턴 사용) | 단일 실행파일(윈도·리눅스), 런타임 설치 불필요, 메모리 수십 MB, npm 공급망·설치 차단 문제 없음 | Node.js: 생태계는 넓지만 기관 서버에서 패키지 설치·네이티브 모듈 문제 발생 가능 |
| DB | **SQLite** (`modernc.org/sqlite`, 순수 Go), WAL 모드 | 500명 규모에 충분, 파일 1개로 백업 | PostgreSQL: 운영 부담 대비 이득 없음 |
| 프론트엔드 | 바닐라 JS ES 모듈, 빌드 없음 | 첫 로딩 최소화, 유지보수 단순 | React 등: 번들 크기·빌드 필요 |
| 지도 렌더링 | **Leaflet 1.9.4** 자체 호스팅 | 약 40KB(gzip), Canvas로 점 500개 가벼움, 타일 공급자 교체 쉬움 | 카카오 지도 SDK: §10.2 |
| 배경 타일 | **VWorld Base**(기본), 벤치마크로 확정 | 공공기관 사용에 적합, 국내 지도 | §10.1 |
| 통신 | HTTP/1.1·HTTP/2 요청·응답만 | §2.1 | WebSocket, SSE |

**허용 의존성 목록 (서버)**

- `modernc.org/sqlite`
- `golang.org/x/crypto` (bcrypt)
- `github.com/xuri/excelize/v2` (명단 xlsx 가져오기 전용)

**허용 의존성 목록 (프론트엔드)**

- Leaflet 1.9.4 (`web/vendor/leaflet/`에 파일로 포함)

### 3.3 디렉터리 구조

```
/
├─ cmd/
│  ├─ server/main.go          # 기동, 설정 검증, 하위 명령(admin create 등)
│  └─ sim/main.go             # 가상 인원 500명 부하 시뮬레이터
├─ internal/
│  ├─ config/                 # 환경변수 읽기·검증 (누락 시 종료)
│  ├─ store/                  # SQLite 연결, 마이그레이션(embed), 쿼리
│  │  └─ migrations/001_init.sql
│  ├─ auth/                   # 비밀번호, 세션, 로그인 제한
│  ├─ roster/                 # 명단, xlsx 가져오기, 정규화
│  ├─ area/                   # 임무지역, 도형 검증, 거리 계산
│  ├─ incident/               # 사건·조 임무·개인 배정·버전
│  ├─ tracker/                # 메모리 상태, 판정 상태기계, 배치 기록, seq
│  ├─ sms/                    # Provider 인터페이스, manual, http
│  ├─ audit/                  # event 기록
│  └─ httpapi/                # 라우트, 미들웨어(CSP, 세션, 역할, gzip)
├─ web/                       # go:embed 로 바이너리에 포함
│  ├─ f/index.html, f.js, f.css          # 현장인력
│  ├─ a/index.html, a.js, a.css          # 관리자 상황판
│  ├─ a/setup.html, setup.js             # 관리자 설정(명단·지역·프리셋·계정)
│  ├─ shared/api.js, clock.js, geo.js, map.js, text.js
│  ├─ sw.js                               # 서비스워커(앱 셸 캐시)
│  └─ vendor/leaflet/
├─ deploy/
│  ├─ Dockerfile, docker-compose.yml, Caddyfile.example
│  └─ .env.example
├─ docs/SPEC.md               # 이 문서
└─ Makefile                   # build, test, lint(innerHTML 검사 포함), sim
```

---

## 4. 데이터 모델

모든 시각 컬럼은 **UTC 밀리초 정수(`INTEGER`)** 다. 문자열 시각 저장 금지.

SQLite 설정은 마이그레이션 파일이 아니라 **연결 문자열**에 둔다(WAL은 트랜잭션 안에서 바꿀 수 없고, `foreign_keys`는 연결마다 켜야 한다).

```
file:/data/app.db?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)
```

```sql
-- 편성조 (기본 1~10조 시딩, 이름만 수정 가능)
CREATE TABLE team (
  no    INTEGER PRIMARY KEY CHECK (no BETWEEN 1 AND 99),
  name  TEXT NOT NULL                       -- 예: '3조'
);

-- 임무지역. 사건에 한 번이라도 쓰인 지역은 수정하지 않는다.
-- 수정이 필요하면 새 행으로 복제하고 원본은 active=0 (진행 중 배정은 id로 원본을 계속 참조).
CREATE TABLE area (
  id        INTEGER PRIMARY KEY,
  name      TEXT NOT NULL,
  kind      TEXT NOT NULL CHECK (kind IN ('circle','polygon')),
  lat       REAL, lng REAL,                 -- circle 중심
  radius_m  INTEGER CHECK (radius_m BETWEEN 50 AND 5000),
  polygon   TEXT,                           -- JSON [[lat,lng],...] 3~20점, 자기교차 금지
  nav_lat   REAL NOT NULL,                  -- 가는길 도착점(진입지점). 기본값 = 중심
  nav_lng   REAL NOT NULL,
  bbox      TEXT NOT NULL,                  -- 계산값 JSON [south,west,north,east]
  active    INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX area_active_name ON area(name) WHERE active = 1;  -- 명단 가져오기의 이름 매칭용

-- 현장인력 명단 = 계정
CREATE TABLE member (
  id          INTEGER PRIMARY KEY,
  login_id    TEXT NOT NULL UNIQUE,         -- 휴대전화번호 사용 금지 (§16-2)
  pw_hash     TEXT NOT NULL,
  pw_must_change INTEGER NOT NULL DEFAULT 1,
  dept        TEXT NOT NULL,                -- 소속(부서)
  name        TEXT NOT NULL,                -- 이름
  office_tel  TEXT CHECK (office_tel IS NULL OR office_tel GLOB '[0-9][0-9][0-9][0-9]'),       -- 행정전화 4자리
  mobile      TEXT NOT NULL UNIQUE
              CHECK (mobile GLOB '01[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]'),            -- 휴대전화 숫자 11자리
  team_no     INTEGER REFERENCES team(no),  -- 편성조
  mission     TEXT,                         -- 임무내용(평시 사전 편성값)
  area_id     INTEGER REFERENCES area(id),  -- 임무지역(평시 사전 편성값)
  note        TEXT,                         -- 비고
  active      INTEGER NOT NULL DEFAULT 1,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);

-- 사전 입력 문구
CREATE TABLE preset (
  id     INTEGER PRIMARY KEY,
  kind   TEXT NOT NULL CHECK (kind IN ('incident_type','message','mission')),
  text   TEXT NOT NULL,
  sort   INTEGER NOT NULL DEFAULT 0,
  active INTEGER NOT NULL DEFAULT 1
);

-- 사건(비상발령). 활성 사건은 동시에 1건.
CREATE TABLE incident (
  id         INTEGER PRIMARY KEY,
  type_text  TEXT NOT NULL,                 -- 발령종류
  message    TEXT NOT NULL,                 -- 비상발령 메시지
  status     TEXT NOT NULL CHECK (status IN ('active','closed')),
  version    INTEGER NOT NULL DEFAULT 1,    -- 발령종류·메시지 수정 시 +1
  opened_by  TEXT NOT NULL,
  opened_at  INTEGER NOT NULL,
  closed_by  TEXT,
  closed_at  INTEGER
);
CREATE UNIQUE INDEX one_active_incident ON incident(status) WHERE status = 'active';

-- 사건별 조 임무
CREATE TABLE team_task (
  incident_id INTEGER NOT NULL REFERENCES incident(id),
  team_no     INTEGER NOT NULL REFERENCES team(no),
  mission     TEXT NOT NULL,
  area_id     INTEGER NOT NULL REFERENCES area(id),
  version     INTEGER NOT NULL DEFAULT 1,
  updated_by  TEXT NOT NULL,
  updated_at  INTEGER NOT NULL,
  PRIMARY KEY (incident_id, team_no)
);

-- 사건별 개인 배정 (발령 시점 명단을 복사해 고정)
CREATE TABLE assignment (
  incident_id      INTEGER NOT NULL REFERENCES incident(id),
  member_id        INTEGER NOT NULL REFERENCES member(id),
  team_no          INTEGER NOT NULL,
  mission_override TEXT,                    -- NULL이면 조 임무 사용
  area_override_id INTEGER REFERENCES area(id),
  eff_mission      TEXT NOT NULL,           -- 실제 적용 임무(계산값)
  eff_area_id      INTEGER NOT NULL,        -- 실제 적용 지역(계산값)
  mission_ver      INTEGER NOT NULL DEFAULT 1,  -- eff_* 가 바뀔 때마다 +1
  ack_ver          INTEGER NOT NULL DEFAULT 1,  -- 인원이 확인한 버전 (발령 직후는 확인 불필요 → 1)
  state            TEXT NOT NULL CHECK (state IN
                     ('NOTIFIED','LOGGED_IN','LOC_DENIED','MOVING','ARRIVED','LEFT')),
  first_login_at   INTEGER,
  arrived_at       INTEGER,                 -- 현재 임무지역 최초 도착
  last_left_at     INTEGER,
  left_count       INTEGER NOT NULL DEFAULT 0,
  last_lat5        INTEGER,                 -- 위도×1e5 정수
  last_lng5        INTEGER,
  last_acc         INTEGER,
  last_fix_at      INTEGER,
  suspect          INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (incident_id, member_id)
);
CREATE INDEX assignment_team ON assignment(incident_id, team_no);

-- 위치 이력: 인원당 60초에 1건 + 상태 전이 시점만. 보존기간 후 삭제.
CREATE TABLE fix (
  incident_id INTEGER NOT NULL,
  member_id   INTEGER NOT NULL,
  ts          INTEGER NOT NULL,
  lat5        INTEGER NOT NULL,
  lng5        INTEGER NOT NULL,
  acc         INTEGER NOT NULL
);
CREATE INDEX fix_lookup ON fix(incident_id, member_id, ts);

-- 감사·이벤트 기록 (추가만, 수정·삭제 금지)
CREATE TABLE event (
  id          INTEGER PRIMARY KEY,
  ts          INTEGER NOT NULL,
  incident_id INTEGER,
  actor       TEXT NOT NULL,                -- 'admin:<login_id>' | 'member:<id>' | 'system'
  type        TEXT NOT NULL,                -- §4.1 목록
  member_id   INTEGER,
  data        TEXT                          -- JSON
);
CREATE INDEX event_incident ON event(incident_id, ts);

CREATE TABLE admin_user (
  id         INTEGER PRIMARY KEY,
  login_id   TEXT NOT NULL UNIQUE,
  pw_hash    TEXT NOT NULL,
  name       TEXT NOT NULL,
  dept       TEXT,
  role       TEXT NOT NULL CHECK (role IN ('admin','operator')),
  active     INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL
);

CREATE TABLE session (
  token_hash   TEXT PRIMARY KEY,            -- SHA-256(토큰). 원문 저장 금지
  kind         TEXT NOT NULL CHECK (kind IN ('admin','member')),
  subject_id   INTEGER NOT NULL,
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL
);

-- 종료된 사건도 이 테이블에는 기록할 수 있다(종료 문자). §5.2 읽기 전용의 유일한 예외.
CREATE TABLE sms_log (
  id           INTEGER PRIMARY KEY,
  batch_id     TEXT NOT NULL,               -- 한 번의 발송 요청 단위
  incident_id  INTEGER NOT NULL,
  kind         TEXT NOT NULL CHECK (kind IN ('open','close','resend')),
  member_id    INTEGER NOT NULL,
  provider     TEXT NOT NULL,               -- 'manual' | 'http'
  status       TEXT NOT NULL CHECK (status IN ('prepared','sent','failed')),
  provider_ref TEXT,
  ts           INTEGER NOT NULL
);
```

### 4.1 event.type 목록

`incident_open`, `incident_update`, `team_task_update`, `assignment_add`, `assignment_update`, `incident_close`, `sms_prepare`, `sms_send`, `sms_mark_sent`, `login_ok`, `login_fail`, `loc_consent`, `loc_denied`, `arrived`, `left`, `returned`, `ack`, `suspect_fix`, `contact_view`, `member_import`, `member_update`, `area_create`, `area_copy`, `admin_user_update`, `password_reset`

### 4.2 정규화 규칙 (명단 입력 시 반드시 적용)

| 항목 | 규칙 | 실패 시 |
|---|---|---|
| 휴대전화 | 숫자만 남김. 10자리이고 `10`으로 시작하면 앞에 `0` 보충(엑셀 숫자 셀 대응) + 경고 | 11자리 `01…` 아니면 거부 |
| 행정전화 | 숫자만 남김. 4자리 문자열로 저장(앞자리 0 유지) | 4자리 아니면 거부(빈 값 허용) |
| 편성조 | `3`, `3조`, `03` → 3 | team 테이블에 없으면 거부 |
| 임무지역 | 활성 area.name과 정확히 일치 | 없으면 거부 |
| 이름·소속·임무·비고 | 앞뒤 공백 제거, 제어문자 제거, 최대 길이 이름 30·소속 50·임무 200·비고 200 | 초과 시 거부 |

---

## 5. 상태 모델

### 5.1 인원 상태 (assignment.state)

```
            발령(사건 열림)
                 │
            NOTIFIED ──이 사건 중 첫 인증 요청(로그인 또는 기존 세션으로 /f/me·/f/sync)──► LOGGED_IN
                 │                                                                     │
                 │                                                          위치 거부 동의 기록
                 │                                                                     ▼
                 │                                                                LOC_DENIED
                 │                                                                     │
                 └──────────────┬──────────── 첫 유효 위치 수신 ─────────────────────────┘
                                ▼            (NOTIFIED·LOGGED_IN·LOC_DENIED 어디서든)
                             MOVING ──도착 판정──► ARRIVED ◄──복귀 판정── LEFT
                                                     │                   ▲
                                                     └─────이탈 판정──────┘

   임무지역 변경(eff_area_id 바뀜) 시 ARRIVED/LEFT → MOVING
```

- NOTIFIED → LOGGED_IN은 **로그인 이벤트가 아니라 "이 사건 중 첫 인증 요청"** 으로 판단한다. 사전에 로그인해 둔 세션을 재사용해도 미접속으로 남지 않게 하기 위해서다. 이때 `first_login_at` 기록.

| 상태 | 화면 이름 | 관리자 지도 표시 |
|---|---|---|
| NOTIFIED | 미접속 | 표시 안 함(위치 없음), 조 타일 숫자만 |
| LOGGED_IN | 접속(위치 확인 중) | 마지막 위치 있으면 회색 속빈 점 |
| LOC_DENIED | 위치 거부 | 조 타일 경고 숫자 |
| MOVING | 이동중 | 파란 점 |
| ARRIVED | 응소 | 초록 점 |
| LEFT | 이탈 | 빨간 점 |

추가 표시(상태가 아닌 플래그, 조회 시 계산):

- `ack_pending` = `mission_ver > ack_ver` → 임무변경 미확인
- `stale` = 마지막 위치가 180초보다 오래됨 → 점을 흐리게 (**클라이언트가** 서버 시각 `t`와 마지막 위치 시각으로 계산)
- `suspect` = §6.4 의심 위치

현장 API 응답에만 쓰는 추가 값(배정 상태가 아님):

| 값 | 뜻 | 현장 화면 |
|---|---|---|
| `IDLE` | 활성 사건 없음 | "현재 발령이 없습니다". 위치 수집 안 함, `/f/sync` 60초 |
| `NONE` | 활성 사건은 있으나 이 인원은 대상 아님 | "이번 발령 대상이 아닙니다". 위치 수집 안 함, `/f/sync` 60초 |
| `CLOSED` | 이 인원이 배정됐던 사건이 종료됨(종료 후 12시간 이내이고 새 활성 사건 없음) | 상황종료 처리(§8.1.5) |

### 5.2 사건 상태

`active` → `closed` 한 방향만. 종료된 사건은 읽기 전용이다. 종료 후 위치 보고는 저장하지 않고 `stop` 응답을 준다.

### 5.3 임무 변경과 버전

1. 조 임무 변경, 개인 배정 변경, 편성조 이동이 일어나면 영향받는 인원마다 새 `eff_mission`, `eff_area_id`를 계산한다.
2. 값이 **실제로 바뀐 인원만** `mission_ver += 1`.
3. `eff_area_id`가 바뀐 인원은 `ARRIVED`·`LEFT` → `MOVING`, `arrived_at = NULL`. 이전 도착 기록은 `event`에 남는다.
4. 임무 문구만 바뀌면 상태는 그대로다.
5. 다른 조의 배정 행은 건드리지 않는다. (testai H2 회귀 테스트 R2)
6. 발령종류·메시지 수정은 `incident.version += 1`만 한다. 팝업 대상이 아니다.
7. 발령 후 인원 추가(`POST /a/incidents/current/members`)는 새 배정 행을 `NOTIFIED`로 만든다. 기존 행은 건드리지 않는다.

### 5.4 발령 시 조 임무 자동 채움

명단의 평시 임무·임무지역은 사람마다 다를 수 있다. 발령 화면은 다음 규칙으로 채운다.

1. 조별 기본 임무·지역 = 그 조 활성 인원의 평시값 중 **가장 많은 값**(동률이면 member.id가 가장 작은 인원의 값).
2. 평시값이 조 기본값과 다른 인원은 **개인 override로 자동 등록**하고, 발령 전 미리보기 표에 표시한다.
3. 평시값이 비어 있는 인원은 조 기본값을 따른다. 조 전원이 비어 있으면 그 조는 발령 전에 직접 입력해야 한다.
4. 관리자는 미리보기에서 조 기본값·개인 override를 모두 고칠 수 있다.

---

## 6. 위치 수신과 응소 판정

### 6.1 현장 브라우저 전송 정책

`navigator.geolocation.watchPosition`(`enableHighAccuracy: true`, `maximumAge: 5000`)으로 위치를 계속 받되, **아래 조건일 때만 서버로 보낸다.**

```
상수
  MIN_GAP      = 5 s       # 어떤 경우에도 이보다 자주 보내지 않음
  MOVE_TRIGGER = 25 m      # 마지막 전송 지점에서 이만큼 움직이면 전송
  ACC_DROP     = 500 m     # 정확도가 이보다 나쁘면 버림

변수
  n = 서버가 응답으로 내려준 다음 전송 주기(초). 기본 15

전송 조건 (now - lastSentAt ≥ MIN_GAP 이고, 아래 중 하나)
  a) 마지막 전송 지점과 거리 ≥ MOVE_TRIGGER
  b) now - lastSentAt ≥ n
  c) 직전 전송의 정확도 > 100 m 였고 이번 정확도 ≤ 100 m

심장박동 타이머 (정지 상태 대응)
  - 기기가 움직이지 않으면 watchPosition 콜백이 오지 않을 수 있다.
  - 그래서 n초 동안 전송이 없으면 getCurrentPosition(maximumAge 10000, timeout 10000)을 1회 호출해 조건 b로 보낸다.
  - 이것도 실패(권한 없음·시간 초과)하면 GET /f/sync 를 호출해 임무변경·종료 신호는 계속 받는다.

실패 시
  - 보낼 위치는 "가장 최신 1건"만 보관(대기열 쌓지 않음)
  - 재시도 간격 5, 10, 20, 40, 60초(+0~2초 무작위)
  - 401 → 로그인 화면
  - 응답 st = CLOSED → 종료 처리(§8.1.5), st = IDLE·NONE → 전송 중지하고 60초 sync만
```

위 모든 watch·타이머는 **전송 루프 객체 1개**가 소유하고 `stop()` 한 곳에서 해제한다(testai M2 재발 방지).

서버가 정하는 `n` — **위에서부터 처음 맞는 행 1개**를 쓰고, 마지막에 과부하 배수를 곱한다.

| 순서 | 조건 | n |
|---|---|---|
| 1 | ARRIVED이고 `sd ≤ −30 m`(경계 안쪽 30 m 이상) | 60 |
| 2 | `|sd| ≤ D_near`, `D_near = max(2 × R_eq, 200 m)` | 10 |
| 3 | 그 외 (MOVING, LEFT, 경계 근처 ARRIVED 아님) | 15 |
| – | 서버 과부하(요청 처리 p95 > 200 ms 또는 초당 요청 > 150)면 | 위 값 × 2 |

`R_eq`: 원은 반경, 다각형은 bbox 중심에서 가장 먼 꼭짓점까지 거리.

### 6.2 서버 판정 규칙

**경계까지 부호 있는 거리 `sd`** (음수 = 안쪽)

- 원: `sd = haversine(점, 중심) − 반경`
- 다각형: 중심 위도 기준 등장방형 투영(m) 후 점–변 최단거리. 내부면 음수. (반경 수 km 이내 오차 무시 가능)

**유효 위치**: `acc ≤ 100 m`, `ts`가 서버 시각 기준 −120초 ~ +30초, suspect 아님. 유효하지 않은 위치는 `last_*`만 갱신하고 상태 전이에 쓰지 않는다.

**판정 대상**: 활성 사건에 배정된 인원만. 사건이 없거나 배정이 없으면 위치를 저장하지 않는다.

| 전이 | 조건 |
|---|---|
| NOTIFIED/LOGGED_IN/LOC_DENIED → MOVING | 유효 위치 1건 수신 |
| MOVING/LEFT → ARRIVED | `sd ≤ 0`인 유효 위치가 **연속 2건**, 두 건 간격 ≥ 10초. LEFT에서 온 경우 event `returned` |
| ARRIVED → LEFT | `sd > max(30 m, acc)`인 유효 위치가 **연속 2건**, 첫 건과 마지막 건 간격 ≥ 30초 |
| 중간에 조건이 끊기면 | 연속 계수 초기화 |

`arrived_at`은 첫 도착 시각(연속 2건 중 **첫 건**의 ts)으로 한 번만 기록한다.

**근거 (몬테카를로 시뮬레이션, 반경 100 m, 15초 주기, 1시간 정지)**

| 실제 위치 | GPS 오차 σ | 단순 판정(경계 넘으면 즉시) 가짜 이탈 | 이 규칙 가짜 이탈 |
|---|---|---|---|
| 중심에서 80 m | 25 m | 시간당 44.8회 | 0회 |
| 중심에서 95 m | 25 m | 시간당 59.2회 | 0.05회 |

실제 이탈(경계 밖 50 m 이상)은 100% 탐지, 평균 30~40초 소요. → **임무지역 반경은 최소 50 m, 권장 100 m 이상**으로 관리자 화면에 안내한다.

**도형 검증 (저장 시)**

- 원: 반경 50~5000 m
- 다각형: 3~20점, 좌표가 대한민국 범위(위도 33~39, 경도 124~132) 안, **변끼리 교차 금지**(O(n²) 선분 교차 검사), 면적 ≥ 1,000 m²
- `nav_lat/lng` 미입력 시 원은 중심, 다각형은 꼭짓점 평균 → 이 점이 다각형 밖이면 저장 거부하고 입력 요구

### 6.3 서버 처리 흐름 (POST /fix)

```
1. 세션 확인 → member_id (요청 본문의 id는 절대 믿지 않음)
2. 활성 사건·배정 조회 (메모리)
     활성 사건 없음            → 최근 배정 사건이 12시간 내 종료면 {st:"CLOSED", stop:1}, 아니면 {st:"IDLE", n:60}
     활성 사건 있으나 배정 없음 → {st:"NONE", n:60}
     (세 경우 모두 위치 저장 안 함)
   배정 상태가 NOTIFIED면 → LOGGED_IN 전이(§5.1)
3. 값 검증 (§6.2 유효 위치, §6.4 의심)
4. 메모리 상태 갱신, 판정 상태기계 실행
5. 상태 전이가 있으면 → 즉시 DB 기록(동기) + event + seq 증가
   전이가 없으면     → dirty 표시(2초 배치 기록) + 위치가 10 m 이상 바뀐 경우만 seq 증가
6. 60초에 1건 규칙으로 fix 이력 적재 대상 표시
7. 응답 {t, st, iv, mv, d, n}
```

### 6.4 위치 이상 탐지 (의심 표시만, 차단 안 함)

- 직전 유효 위치와의 이동 속도 > 55 m/s(시속 약 200 km) → `suspect=1`, 이번 위치는 판정에 안 씀
- 좌표가 대한민국 범위 밖 → `suspect=1`
- 다음 정상 위치 2건이 연속으로 오면 `suspect=0`
- 관리자 화면: 점 테두리 점선 + 인원 상세에 "위치 의심"

### 6.5 메모리 상태와 기록

- 활성 사건의 배정 500건을 기동·발령 시 메모리에 올린다(`map[int]*Live`).
- 배치 기록 고루틴: 2초마다 dirty 행을 트랜잭션 1건으로 `UPDATE assignment`, 이력 `INSERT fix`.
- 서버 비정상 종료 시 잃는 것은 **최대 2초 치 위치**뿐. 상태 전이·임무 변경·ack는 동기 기록이라 잃지 않는다.
- `seq`는 메모리 전역 증가값. 재기동 시 `epoch`(기동 시각 난수)가 바뀌어 관리자 화면이 전체를 다시 받는다. seq는 저장하지 않는다.

---

## 7. API 명세

### 7.1 공통 규약

- 경로 접두어 `/api/v1`. v2 앱도 같은 API를 쓴다.
- 인증: 쿠키 `sid`(HttpOnly, Secure, SameSite=Lax) **또는** `Authorization: Bearer <token>`(앱 대비). 둘 다 같은 session 테이블.
- 상태 변경 요청은 `Content-Type: application/json` 필수(CSRF 완화). 아니면 415. **예외: `POST /a/members/import`만 `multipart/form-data`** 허용(이 경로는 SameSite 쿠키 + 역할 검사 + 파일 검증으로 보호).
- 모든 JSON 응답에 `t`(서버 UTC 밀리초) 포함. 클라이언트는 `t`로 시계 오차를 보정해 **현재시간을 표시**한다. 표시는 `Intl.DateTimeFormat('ko-KR', {timeZone:'Asia/Seoul', hour12:false})`.
- 오류 형식: `{"t":..., "err":"코드", "msg":"사람이 읽을 문장"}`. 코드와 HTTP 상태: `auth` 401, `forbidden` 403, `invalid` 400, `conflict` 409, `resync` 409, `rate` 429, `busy` 503, `closed` 409(종료된 사건 수정 시도), `server` 500.
- 현장 위치·동기화 응답의 `CLOSED`·`IDLE`·`NONE`은 오류가 아니라 **200 응답의 `st` 값**이다.
- 좌표는 API에서 소수점 5자리(약 1.1 m)로 반올림한다.
- 1KB 넘는 응답은 gzip.
- 잘못된 쿠키·토큰은 **401**(500 금지).

### 7.2 현장인력 API

| 메서드·경로 | 요청 | 응답 |
|---|---|---|
| `POST /f/login` | `{"id":"k1234","pw":"..."}` | 200 `{t, mustChange}` + 쿠키 / 401 / 429 `{retryAfter}` |
| `POST /f/password` | `{"old":"..","new":".."}` | 200 |
| `POST /f/logout` | – | 200 |
| `GET /f/me` | – | 아래 예시 |
| `POST /f/consent` | `{"granted":true,"textVer":"2026-09-v1"}` | `{t, st}` |
| `POST /f/fix` | `{"la":35.87140,"lo":128.60140,"ac":18,"ts":1757570000123}` | `{"t":…, "st":"MOVING", "iv":1, "mv":2, "d":1240, "n":15}` |
| `GET /f/sync` | – (위치 없이 상태만. 위치 거부자·지도 화면 대기용, 30초 주기) | fix 응답과 같음 |
| `POST /f/ack` | `{"mv":3}` | `{t, mv:3}` |

`st` 값: 배정 상태(§5.1) 또는 `IDLE`·`NONE`·`CLOSED`(§5.1 추가 값). 종료 시 `{"t":…, "st":"CLOSED", "stop":1}`.
`d`: 경계까지 거리(m, 안쪽이면 음수).

클라이언트 처리 규칙:

- `iv`가 보관값과 다르거나, `st`가 `IDLE`·`NONE`에서 배정 상태로 바뀌면 → 즉시 `GET /f/me` (발령종류·메시지 갱신, 새 사건이면 위치 안내 시트)
- `mv`가 보관값(`ackVer`)보다 크면 → **먼저 임무변경확인 팝업**(§8.1.4). `GET /f/me`는 버튼을 누른 뒤 호출한다.

`GET /f/me` 응답 예시:

```json
{
  "t": 1757570000123,
  "incident": {"id": 7, "type": "비상 2단계", "message": "신천 수위 상승, 담당 구역 즉시 출동", "iv": 1, "status": "active", "openedAt": 1757569000000},
  "member": {"name": "김도현", "dept": "안전정책과", "teamNo": 3, "teamName": "3조"},
  "task": {
    "mission": "차단선 구축",
    "teamMission": "차단선 구축",
    "mv": 2, "ackVer": 1,
    "area": {"name": "신천교 북단", "kind": "circle", "lat": 35.87140, "lng": 128.60140, "r": 150,
             "polygon": null, "nav": [35.87120, 128.60100], "bbox": [35.8700, 128.5997, 35.8728, 128.6031]}
  },
  "st": "MOVING",
  "n": 15
}
```

- `mission` = 이 인원에게 실제 적용되는 임무(개인 override 반영). 화면 R3에 표시.
- `teamMission` = 조 임무. 응소 후 전광판(§8.3 6순위)에 표시.
- `IDLE`·`NONE`이면 `incident`·`task`는 `null`.

### 7.3 관리자 API

역할: `operator` = 사건·임무·문자, `admin` = operator 권한 + 명단·지역·프리셋·관리자 계정.

**상황판**

| 메서드·경로 | 역할 | 설명 |
|---|---|---|
| `POST /a/login`, `POST /a/logout` | – | |
| `GET /a/snapshot` | operator | 전체 1회. 아래 형식 |
| `GET /a/delta?epoch=E&since=N` | operator | 변경분. epoch 다르면 409 `{"err":"resync"}` |
| `GET /a/incidents/draft` | operator | 발령 미리보기. §5.4 규칙으로 채운 조 기본값과 개인 override 목록 |
| `POST /a/incidents` | operator | 발령. `{type, message, teams:[{no, mission, areaId}], members:[{id, mission?, areaId?}]}` 대상 = 활성 명단 중 teams에 포함된 조 |
| `PATCH /a/incidents/current` | operator | `{type?, message?}` → incident.version+1 |
| `PUT /a/incidents/current/teams/{no}` | operator | `{mission, areaId}` → §5.3 |
| `POST /a/incidents/current/members` | operator | 발령 후 인원 추가 `{memberId, teamNo, mission?, areaId?}` |
| `PUT /a/incidents/current/members/{id}` | operator | `{mission:null\|"..", areaId:null\|n, teamNo?}` null = 조 임무 따름 |
| `GET /a/incidents/current/members/{id}/contact` | operator | 전체 휴대전화번호. 호출마다 event `contact_view` |
| `POST /a/incidents/current/close` | operator | 종료. 확인 문구 입력 필수 `{confirm:"상황종료"}` |
| `POST /a/incidents/{id}/sms` | operator | 발송 준비·실행 §9. `{kind:"open"\|"close"\|"resend", via:"manual"\|"http", target:"all"\|"not_logged_in"\|"failed"\|"team:3"}` → `{batchId, text, count, bytes, lms}` (http면 결과 수 포함). 종료된 사건은 `kind:"close"`만 허용 |
| `GET /a/incidents/{id}/sms/{batchId}/recipients.csv` | operator | manual용 수신자 `이름,휴대전화` |
| `POST /a/incidents/{id}/sms/{batchId}/mark-sent` | operator | manual 발송 완료 표시 |
| `GET /a/incidents/{id}/sms/{batchId}` | operator | 배치 결과(성공·실패·실패자 목록) |
| `GET /a/incidents/{id}/report.csv` | operator | 응소 기록(§8.2.5) |

**설정 (admin)**

| 메서드·경로 | 설명 |
|---|---|
| `GET/POST /a/members`, `PUT /a/members/{id}` | 명단 조회·등록·수정. 등록 응답에 초기 비밀번호 1회 포함 |
| `POST /a/members/import?mode=preview` | xlsx 업로드 → 신규·변경·오류 행 목록(저장 안 함) |
| `POST /a/members/import?mode=commit` | 미리보기 토큰으로 반영. 신규 인원 초기 비밀번호 목록은 이 응답에서만 1회 제공 |
| `POST /a/members/{id}/password-reset` | 임시 비밀번호 1회 표시, 해당 인원 세션 전부 삭제 |
| `PUT /a/members/{id}` `{active:false}` | 비활성화 = 세션 즉시 삭제 |
| `GET/POST /a/areas`, `POST /a/areas/{id}/copy` | 지역 등록·복제(사용된 지역은 수정 불가) |
| `GET/POST/PUT/DELETE /a/presets` | 발령종류·메시지·임무 문구 |
| `GET/POST/PUT /a/admins` | 관리자 계정. 마지막 활성 admin 비활성화 금지 |
| `GET /a/events?incident=7` | 감사 기록 |

### 7.4 관리자 스냅샷·델타 형식

```json
{
  "t": 1757570000123, "epoch": "b7f3", "seq": 18233,
  "incident": {"id": 7, "type": "비상 2단계", "message": "…", "iv": 1, "openedAt": 1757569000000},
  "teams": [{"no": 3, "name": "3조", "mission": "차단선 구축", "areaId": 12, "ver": 2}],
  "areas": [{"id": 12, "name": "신천교 북단", "kind": "circle", "lat": 35.8714, "lng": 128.6014, "r": 150, "polygon": null, "bbox": [..]}],
  "people": [[101, "김도현", "안전정책과", 3, "1234", "010****2222", "야간 대기조", null, null]],
  "m": [[101, 3, 3587140, 12860140, 18, 3002, 0]]
}
```

- `people`(배정 정보): `[id, 이름, 소속, 조, 행정전화, 휴대전화(가운데 4자리 가림), 비고, 개인임무 또는 null, 개인지역id 또는 null]`
  - 스냅샷에 전체 포함. 조 이동·개인 배정 변경·인원 추가가 있으면 **바뀐 행만 델타의 `people`에 포함**한다.
  - 전체 번호는 `GET /a/incidents/current/members/{id}/contact`로만(§7.3).
- `m`(위치·상태): `[id, 상태코드, 위도×1e5, 경도×1e5, 정확도m, 마지막위치시각, 플래그]`
  - 마지막위치시각 = 사건 시작(`openedAt`) 기준 **경과 초(절대 기준점)**. 위치 없으면 −1. stale은 클라이언트가 `t`와 비교해 계산하므로 서버가 행을 다시 보낼 필요가 없다.
  - 상태코드: 0 NOTIFIED, 1 LOGGED_IN, 2 LOC_DENIED, 3 MOVING, 4 ARRIVED, 5 LEFT
  - 플래그 비트: 1 임무변경 미확인, 2 suspect
- 델타는 `seq > since`인 `m`·`people` 행만, 그리고 바뀐 경우에만 `incident`·`teams`·`areas`를 포함한다.
- 조별 집계(응소 n/50 등)는 **클라이언트가 `m`으로 계산**한다. 서버가 따로 보내지 않는다.

---

## 8. 화면 명세

### 8.0 공통

- 시스템 글꼴만 사용(웹폰트 금지): `system-ui, "Apple SD Gothic Neo", "Malgun Gothic", sans-serif`
- 화면에 데이터 넣을 때 `textContent`·`setAttribute`만 사용. `innerHTML`, `insertAdjacentHTML`, `document.write` 금지. Leaflet 툴팁·팝업에는 DOM 노드를 만들어 넘긴다(문자열 금지).
- 한국어 줄바꿈 `word-break: keep-all`.
- 색만으로 상태를 구분하지 않는다(글자·모양 병기).
- 크기 예산은 §13.2.

### 8.1 현장인력 화면 (`/f/`)

#### 8.1.1 흐름

```
문자 링크 https://<BASE_URL>/f
  → 세션 없음: 로그인 폼 (ID, 비밀번호) → mustChange면 비밀번호 변경
  → 세션 있음: 바로 본 화면
  → GET /f/me 결과가 IDLE·NONE이면 안내 문구만 표시하고 60초마다 /f/sync (위치 안내 시트 없음)
  → 배정 상태면 위치 안내 시트(1회/사건): "임무지역 도착을 자동 확인하려고 위치를 수집합니다. 상황이 끝나면 수집을 멈춥니다."
       [위치 권한 허용]  ← 이 버튼 클릭 핸들러 안에서 getCurrentPosition 호출 → 브라우저 권한 팝업
       허용 → POST /f/consent {granted:true} → 전송 루프 시작
       거부 → POST /f/consent {granted:false} → 전광판 "위치 권한이 필요합니다" + 기종별 설정 방법 링크
```

- 위치 권한 팝업은 **사용자 버튼 클릭 안에서** 요청한다. 페이지가 열리자마자 요청하면 사용자가 이유를 모른 채 거부하기 쉽고, 동의 기록도 남길 수 없다.
- HTTPS 필수(iOS는 HTTP에서 위치 API가 동작하지 않음).

#### 8.1.2 레이아웃 (세로, 위에서 아래)

```
┌─────────────────────────────────────┐
│ 14:32:07            비상 2단계        │ R1 현재시간(실시간, 서버 보정) | 발령종류
├─────────────────────────────────────┤
│ ▶ 3조 임무 : 차단선 구축              │ R2 전광판 (§8.3)
├─────────────────────────────────────┤
│ 3조 · 차단선 구축                     │ R3 편성조 · 부여임무
│ 임무지역: 신천교 북단 · 1.2 km        │    임무지역명 · 경계까지 거리
├─────────────────────────────────────┤
│                                     │
│          [ 임무지역 지도 ]            │ R4 지도 (남은 높이 전부)
│                                     │
│ [카카오맵 가는길] [네이버지도 가는길]  │    지도 하단 고정 버튼
│                  [내 위치] [화면 켜두기]│
└─────────────────────────────────────┘
```

- CSS: `body{height:100dvh; display:grid; grid-template-rows:auto auto auto 1fr}`
- R1 시계는 1초마다 갱신. 매 응답의 `t`로 오차 보정.
- R3·가는길 버튼은 지도 로딩과 무관하게 **먼저 그린다.** 지도는 첫 화면 표시 후 불러온다.

#### 8.1.3 지도 동작

- 처음과 임무지역 변경 시: `const [s,w,n,e] = area.bbox; map.fitBounds([[s,w],[n,e]], {padding:[24,24], maxZoom:18})` → **임무지역 전체가 한 화면에 들어오게.** (Leaflet은 평면 배열 `[s,w,n,e]`를 받지 않는다)
- 표시: 임무지역 도형(원/다각형), 가는길 도착점 깃발, 내 위치 점(정확도 원은 반투명).
- 내 위치가 화면 밖이면 화면 가장자리에 방향 화살표 + 거리.
- [내 위치] 버튼: 임무지역+내 위치를 함께 맞춤. 다시 누르면 임무지역만.
- 타일 로딩 실패 5건 이상이면 지도 위에 "지도를 불러오지 못했습니다. 가는길 버튼을 이용하세요." (텍스트 정보는 유지)

#### 8.1.4 임무변경확인 팝업

- 트리거: 응답의 `mv` > 로컬 `ackVer` (페이지를 새로 열었을 때 `/f/me`의 `mv > ackVer`인 경우도 포함)
- 모달 내용: "관리자가 임무를 변경했습니다. 확인을 누르면 새 임무를 불러옵니다." / 버튼 1개 **[임무변경확인]**
  - 새 임무 내용은 버튼을 누르기 전에는 받아오지 않는다(요구사항 F13: 확인 → 서버에서 다시 받아오기).
- 버튼 클릭: `GET /f/me` → R3·지도 갱신(새 지역이면 fitBounds) → `POST /f/ack {mv: 받아온 mv}` → 모달 닫기
- 받아오는 사이 또 바뀌어 응답 `mv`가 더 크면 모달을 다시 띄운다.
- 모달이 열려 있어도 위치 전송은 계속한다.
- 확인 전 전광판: "임무변경 확인 필요"

#### 8.1.5 상황종료 처리

응답에 `stop:1` 또는 `st:"CLOSED"`를 받으면:

1. `clearWatch`, 모든 타이머 해제, Wake Lock 해제 (**전송 루프 객체의 `stop()` 한 곳에서**)
2. 전광판 "상황종료 · 위치 수집을 중단했습니다"
3. 가는길·지도는 그대로 두되 위치 점 제거
4. 이후 `GET /f/sync`도 호출하지 않는다.

#### 8.1.6 화면 켜두기

- [화면 켜두기] 토글 → Screen Wake Lock API 요청. 미지원 브라우저면 버튼 숨김.
- 로그인 직후 안내 1줄: "이동 중에는 이 화면을 켜 두세요. 화면이 꺼지면 위치가 전송되지 않습니다."

### 8.2 관리자 상황판 (`/a/`)

#### 8.2.1 레이아웃 원칙

**PC·태블릿·휴대폰에서 같은 4행 구성, 같은 높이 비율**을 쓴다. 기기별로 배치를 바꾸지 않고 글자 크기와 타일 안 정보 밀도만 바뀐다.

```css
body   { height:100dvh; margin:0; overflow:hidden; display:grid;
         grid-template-rows: minmax(0,6fr) minmax(0,8fr) minmax(0,34fr) minmax(0,52fr); }
body > * { min-height:0; overflow:hidden; }          /* 내용이 많아도 행 비율이 깨지지 않게 */
.teams { display:grid; grid-template-columns:repeat(5,minmax(0,1fr)); grid-template-rows:repeat(2,minmax(0,1fr)); }
.team  { container-type:size; min-width:0; min-height:0; overflow:hidden; }
:root  { font-size: clamp(10px, 1.6vmin, 18px); }
```

`fr`만 쓰면 행 최소 높이가 내용 크기(auto)가 되어 휴대폰에서 비율이 깨진다. 반드시 `minmax(0, …fr)`로 쓴다.

```
┌──────────────────────────────────────────────────┐
│ 2026-09-11(목) 14:32:07                           │ R1  6%  현재시간(실시간)
├──────────────────────────────────────────────────┤
│ 비상 2단계 │ 신천 수위 상승, 담당 구역 즉시 출동    │ R2  8%  발령종류 · 메시지
│                          [수정] [문자] [상황종료] │
├──────────────────────────────────────────────────┤
│ [1조][2조][3조][4조][5조]                          │ R3 34%  임무조별 임무부여현황
│ [6조][7조][8조][9조][10조]                         │         2행 × 5열 고정
├──────────────────────────────────────────────────┤
│                                                  │
│               [ 임무지역 지도 ]                    │ R4 52%  전체 임무지역 + 인원 점
│  범례: ●응소 ●이동중 ●이탈 ○접속                  │
└──────────────────────────────────────────────────┘
```

#### 8.2.2 조 타일

컨테이너 쿼리로 밀도만 바꾼다.

| 타일 너비 | 표시 |
|---|---|
| ≥ 180px (PC) | `3조` · 임무 문구 · 임무지역명 · 응소 막대 `42/50` · 이동 5 · 이탈 1 · 미접속 2 · 미확인 3 |
| 110~179px | `3조` · 임무 문구(1줄 말줄임) · 막대 `42/50` · 이탈·미확인 아이콘 숫자 |
| < 110px (휴대폰) | `3조` · `42/50` · 막대 · 이탈 있으면 빨간 숫자 |

- 이탈 > 0이면 타일 테두리 빨강. 미확인 > 0이면 주황 점.
- 임무 미부여 조(해당 조가 발령 대상 아님)는 흐리게.
- 타일 탭 → **하단 시트**(지도 위에 겹침, 레이아웃 비율은 그대로):
  - 조 임무 변경: 임무(프리셋 선택 또는 직접 입력), 임무지역(목록 선택) → [변경 적용] (확인 1회)
  - 조원 목록: 이름 · 소속 · 상태 · 마지막 위치 경과 · 행정전화 · 휴대전화(가림) · 비고
  - [전화] 버튼: `GET /a/incidents/current/members/{id}/contact`로 전체 번호를 받아 `tel:` 링크 실행(조회 기록 남음)
  - 조원 행 탭 → 개인 임무·지역 개별 부여 / 조 임무 따르기

#### 8.2.3 지도

- Leaflet `preferCanvas:true`. 인원 점은 `circleMarker`(Canvas) — DOM 마커 500개 금지.
- 전체 임무지역 도형 + 이름 라벨. 처음에는 전체 임무지역 bbox 합에 맞춤.
- 점 색·모양: 응소 초록 채움, 이동중 파랑 채움, 이탈 빨강 채움+굵은 테두리, 접속 회색 속빈 원, stale 투명도 40%, suspect 점선 테두리.
- 점 탭 → 인원 카드(이름·조·상태·경과초·정확도).
- 델타 적용 시 바뀐 점만 `setLatLng`/`setStyle`.

#### 8.2.4 갱신 주기

- `GET /a/delta` 3초. 탭이 숨겨지면 15초. 409 resync면 snapshot.
- 네트워크 실패 시 R1 우측에 "연결 끊김 00:12" 표시, 재시도 3→6→12초.

#### 8.2.5 발령·수정·종료 화면 (R2 버튼에서 여는 시트)

- **새 발령**: 발령종류(프리셋) → 메시지(프리셋 선택 후 편집 가능) → 조별 임무·지역(`GET /a/incidents/draft`, §5.4 규칙으로 자동 채움) → 개인 override 미리보기 표(수정 가능) → 대상 인원 수 확인 → [발령] → 문자 시트로 이동
- **인원 추가**: 발령 후 명단에서 인원을 골라 조·임무 지정 → `POST /a/incidents/current/members`
- **수정**: 발령종류·메시지 수정
- **문자**: §9
- **상황종료**: "상황종료" 입력 후 [종료] → 종료 문자 시트
- **응소 기록 CSV**: 조, 이름, 소속, 최초 접속, 응소 시각, 이탈 횟수, 마지막 상태. 시각은 `YYYY-MM-DD HH:mm:ss`(Asia/Seoul). 셀 값이 `= + - @`로 시작하면 앞에 `'` 추가. UTF-8 BOM.

#### 8.2.6 설정 화면 (`/a/setup.html`)

일반 반응형 페이지(상황판 비율 규칙 적용 안 함). 탭: 명단 · 임무지역 · 프리셋 · 관리자 계정 · 감사기록.

- 명단 가져오기: xlsx 업로드 → 미리보기 표(신규/변경/오류, 오류 사유) → [반영]. 열 머리글: `소속, 이름, 행정전화, 휴대전화, 편성조, 임무내용, 임무지역, 비고, 로그인ID`
- 임무지역: 지도에서 원(중심 클릭 + 반경 입력) 또는 다각형(점 클릭, 자기교차 시 즉시 빨간 표시·저장 불가), 가는길 도착점 지정.

### 8.3 전광판 문구 우선순위 (현장 화면 R2)

위에서부터 먼저 해당하는 1개만 표시한다.

| 순위 | 조건 | 문구 | 스타일 |
|---|---|---|---|
| 1 | 사건 종료 | `상황종료 · 위치 수집을 중단했습니다` | 회색 바탕 |
| 2 | 서버 응답 60초 이상 실패 | `서버 연결 끊김 · 재시도 중` | 주황 |
| 3 | 상태 LEFT | `임무지역 이탈` | 빨강 바탕, 흰 글자, 1초 점멸(동작 줄이기 설정 시 점멸 없음) |
| 4 | 임무변경 미확인 | `임무변경 확인 필요` | 주황 |
| 5 | 위치 권한 없음 | `위치 권한이 필요합니다` | 주황 |
| 6 | 상태 ARRIVED | `{조이름} 임무 : {teamMission}` 예: `3조 임무 : 차단선 구축` (개인 override가 있어도 **조 임무**를 표시, 개인 임무는 R3) | 초록 바탕 |
| 7 | 상태 MOVING | `임무지역까지 {거리}` (1 km 미만 m, 이상 소수 1자리 km) | 파랑 |
| 8 | `IDLE` | `현재 발령이 없습니다` | 기본 |
| 9 | `NONE` | `이번 발령 대상이 아닙니다` | 기본 |
| 10 | 그 외 | `위치 확인 중` | 기본 |

이탈·응소 알림은 **팝업을 쓰지 않는다.** 팝업은 위치 안내 시트와 임무변경확인 두 가지뿐이다.

---

## 9. 문자 발송

### 9.1 인터페이스

```go
type SMS struct { MemberID int; Mobile string; Text string }
type Result struct { MemberID int; OK bool; Ref string; Err string }

type Provider interface {
    Name() string                                   // "manual" | "http"
    Send(ctx context.Context, msgs []SMS) []Result
}
```

- **manual은 항상 켜져 있다.** http는 `SMS_HTTP_URL`이 설정된 경우에만 추가로 켜진다.
- 관리자는 발송할 때마다 방식(`via`)을 고른다. 문자 API가 장애여도 즉시 manual로 보낼 수 있다.
- 새 문자업체는 이 인터페이스 구현 1개만 추가한다.

### 9.2 문구 템플릿

```
발령:  [{발령종류}] {메시지}
       임무확인·위치보고 {BASE_URL}/f
종료:  [상황종료] {발령종류} 상황이 종료되었습니다. 위치 수집을 중단합니다.
재발송: [{발령종류}·재안내] 아직 접속하지 않으셨습니다. {BASE_URL}/f
```

- 링크는 모두 같은 공용 주소다(개인 식별 정보를 URL에 넣지 않는다).
- 90바이트(EUC-KR 기준) 초과 시 장문(LMS)임을 화면에 표시한다.

### 9.3 manual 방식 (기존 기관 문자시스템)

1. `POST /a/incidents/{id}/sms {via:"manual"}` → 배치 생성, 대상자별 `sms_log` status `prepared`
2. 문자 시트에 **문구 미리보기 + [문구 복사]** 버튼
3. **[수신자 CSV 받기]** → `GET …/sms/{batchId}/recipients.csv` : `이름,휴대전화` (UTF-8 BOM, §8.2.5 수식 문자 처리 동일)
4. 관리자가 기관 문자시스템에서 발송 후 **[발송 완료 표시]** → `POST …/sms/{batchId}/mark-sent` → 배치 전체 `sent`

### 9.4 http 방식 (문자 API)

- 환경변수: `SMS_HTTP_URL`, `SMS_HTTP_AUTH_HEADER`, `SMS_HTTP_BODY_TEMPLATE`(Go `text/template`, 필드 `.Mobile .Text .Sender`), `SMS_SENDER`
- 초당 요청 수 제한 `SMS_HTTP_RPS`(기본 10), 요청 타임아웃 10초, 실패 건 2회 재시도(30초 간격)
- 수신자별 결과를 `sms_log`에 기록, 문자 시트에 성공·실패 수와 실패자 목록 표시, [실패자 재발송]
- 발신번호는 사전 등록된 번호여야 한다(운영 전 확인 항목).

### 9.5 대상 필터

`all`(발령 대상 전체), `not_logged_in`(state = NOTIFIED), `failed`(같은 kind의 가장 최근 http 배치에서 실패한 인원), `team:N`

---

## 10. 지도와 가는길

### 10.1 배경 타일 선정 방법

"가장 빠른 것"은 실측으로 정한다. 추정으로 정하지 않는다.

- **기본 구현**: Leaflet + VWorld WMTS
  `TILE_URL=https://api.vworld.kr/req/wmts/1.0.0/{key}/Base/{z}/{y}/{x}.png` — 서버가 `{key}`를 `TILE_KEY` 값으로 바꿔 화면에 내려준다.
  (인증키는 서비스 도메인 등록 필요. 형식과 Referer 검증 방식은 Phase 0에서 실제 발급 키로 확인)
- **비교 후보**: 카카오 지도 JavaScript SDK
- **Phase 0 벤치마크** (`web/bench/map.html`, 확정 후 삭제)
  - 조건: 실제 휴대폰 3대(안드로이드 2, 아이폰 1), LTE(와이파이 끔), 캐시 비운 상태, 대구 시내 임무지역 1곳 bbox 화면
  - 측정: 지도 스크립트 로딩 시작 → 화면 안 타일 전부 표시(Leaflet `load` / 카카오 `tilesloaded`)까지 시간, 전송 바이트(`PerformanceResourceTiming`)
  - 각 5회, 중앙값 비교
  - 판정: 카카오가 **시간·바이트 둘 다 20% 이상** 빠르고 작으면 현장 화면만 카카오 SDK로 교체. 아니면 Leaflet + VWorld 유지.
- 선택 결과는 `docs/SPEC.md` §10.1에 수치와 함께 기록한다.
- **카카오 SDK로 교체할 경우에만 적용되는 예외** (교체 안 하면 무시)
  - §2.1 "외부 의존 0"의 예외로 `dapi.kakao.com` SDK 스크립트 1개 허용
  - §3.2 허용 의존성에 카카오 지도 JS SDK 추가
  - §11.2 CSP의 `script-src`·`img-src`·`connect-src`에 카카오 지도 도메인 추가(실제 요청 도메인을 Phase 0에서 확인해 목록화)
  - `web/shared/map.js`는 두 구현이 같은 함수만 노출: `init(el)`, `fitArea(bbox)`, `drawArea(area)`, `setMe(lat,lng,acc)`, `setNav(lat,lng)`, `onTileError(cb)`
  - 관리자 상황판은 교체 대상이 아니다(점 500개 Canvas 렌더 때문에 Leaflet 유지)

타일 URL은 설정값(`TILE_URL`, `TILE_ATTRIBUTION`)이라 코드 수정 없이 공급자를 바꿀 수 있다. OpenStreetMap 공개 타일 서버는 운영에 쓰지 않는다.

### 10.2 카카오맵·네이버지도 연계 검토 결론

| 방식 | 검토 | 결론 |
|---|---|---|
| 지도 SDK를 넣어 앱 안에서 길찾기 | SDK 로딩·도메인 키·사용량 정책(카카오는 2026-07-21부터 두 번째 앱부터 비즈월렛 연결 필요, 신규 도보·대중교통 길찾기 API는 일 1,000건 무료 후 건당 과금) 부담. 화면 무게 증가 | **채택 안 함** |
| URL로 카카오맵·네이버지도 앱을 호출 | SDK·키·비용 없음. 길 안내 품질은 각 앱이 제공. 출발지를 비우면 현재 위치(자택)에서 출발 | **채택** |

→ 지도 SDK 선택과 가는길 연동은 서로 독립이다. 어떤 배경 타일을 쓰든 가는길은 같다.

### 10.3 가는길 버튼 명세

도착점 = `area.nav_lat`, `area.nav_lng`, 이름 = `area.name`

**카카오맵** (웹 링크, 앱 설치 여부와 무관하게 동작)

```
https://map.kakao.com/link/to/{encodeURIComponent(이름에서 쉼표 제거)},{lat},{lng}
```

**네이버지도** (앱 URL 스킴. 출발지 slat/slng 생략 = 현재 위치. dlat·dlng·appname 필수)

```
안드로이드:
intent://route/car?dlat={lat}&dlng={lng}&dname={enc(이름)}&appname={NAVER_APPNAME}#Intent;scheme=nmap;action=android.intent.action.VIEW;category=android.intent.category.BROWSABLE;package=com.nhn.android.nmap;end

iOS·기타:
nmap://route/car?dlat={lat}&dlng={lng}&dname={enc(이름)}&appname={NAVER_APPNAME}
→ 1.5초 뒤에도 페이지가 보이면(document.visibilityState === 'visible') "네이버지도 앱이 설치되어 있지 않습니다" 안내 + 앱스토어 링크
```

- 경로 종류 기본 `car`. 설정 `NAVER_ROUTE_TYPE`(`car|public|walk|bicycle`).
- `NAVER_APPNAME`은 서비스 식별자(예: 서비스 도메인).
- **Phase 5 수용 기준에 실기기(안드로이드·아이폰, 앱 설치/미설치 4경우) 확인을 포함한다.** 네이버 웹 대체 주소는 공식 문서 확인 전까지 넣지 않는다.

---

## 11. 보안과 개인정보

### 11.1 인증

| 항목 | 규칙 |
|---|---|
| 기본 계정·비밀번호 | **없음.** 첫 관리자는 서버에서 `server admin create --id <id> --name <이름> --role admin` 명령으로 만든다(비밀번호 대화형 입력). |
| 필수 설정 누락 | `BASE_URL`, `DATA_DIR`, `TILE_URL`, `NAVER_APPNAME` 없으면 기동 실패. `TILE_URL`에 `{key}`가 있으면 `TILE_KEY` 필수. `SMS_HTTP_URL`이 있으면 `SMS_HTTP_AUTH_HEADER`·`SMS_HTTP_BODY_TEMPLATE`·`SMS_SENDER` 필수 |
| 현장인력 초기 비밀번호 | 무작위 10자(혼동 문자 제외), 등록·초기화 응답에서 1회만 표시, `pw_must_change=1` |
| 비밀번호 정책 | 8자 이상, 로그인ID·휴대전화 포함 금지 |
| 해시 | bcrypt cost 10. **동시 해시 계산 수 = CPU 코어 수**(세마포어). 대기 5초 넘으면 503 + `retryAfter` |
| 로그인 제한 | 로그인ID 기준 실패 5회부터 대기 2^(실패-4)초(최대 60초). 계정 잠금은 하지 않음(비상 중 잠김 방지). IP 기준은 분당 60회(통신사 공유 IP 고려해 느슨하게) |
| 세션 | 토큰 32바이트 난수, DB엔 SHA-256만. 관리자 12시간(무활동 2시간), 현장인력 24시간 또는 사건 종료 1시간 후 중 빠른 쪽 (§16-1) |
| 세션 폐기 | 로그아웃, 비밀번호 변경·초기화, 비활성화 시 해당 주체 세션 전부 삭제 |
| 권한 | 현장 API는 세션의 member_id로만 조회. 관리자 API는 역할 미들웨어 통과 필수 |

### 11.2 웹 보안 헤더

```
Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self';
  img-src 'self' data: {TILE_ORIGIN}; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'
X-Content-Type-Options: nosniff
Referrer-Policy: strict-origin-when-cross-origin   (타일 서버의 도메인 키 검증용. URL에 개인정보를 넣지 않으므로 출처만 전달돼도 무방. Phase 0에서 VWorld 동작 확인)
Permissions-Policy: geolocation=(self), camera=(), microphone=()
Strict-Transport-Security: max-age=31536000   (HTTPS 운영 시)
```

인라인 스크립트·스타일 금지(CSP로 강제).

### 11.3 출력·입력

- `make lint`: `web/`에서 **`web/vendor/`를 제외하고** `innerHTML|outerHTML|insertAdjacentHTML|document.write` 검색 결과 0건이어야 통과. (Leaflet 원본에는 이 코드가 있으므로 제외, vendor 파일은 수정 금지)
- 업로드는 `/a/members/import`의 xlsx만. 5MB 제한, 확장자와 파일 시그니처(ZIP `PK\x03\x04`) 모두 확인, 메모리에서만 처리, 디스크 저장·공개 없음.
- 모든 JSON 입력은 필드별 길이·형식 검증. 모르는 필드는 거부.

### 11.4 개인정보·위치정보

- 위치는 **활성 사건 중 배정 인원만** 수집한다. 사건 종료 시 서버가 수집 중단 신호를 준다.
- 위치 동의는 사건마다 받고 `event(loc_consent)`에 문구 버전과 함께 기록한다.
- `fix` 이력은 사건 종료 후 `FIX_RETENTION_DAYS`(기본 30일) 지나면 매일 새벽 삭제. `assignment.last_*`는 사건 기록으로 유지.
- 관리자 목록 화면의 휴대전화는 가운데 4자리 가림. 전체 번호 조회는 event에 기록.
- 로그에 비밀번호·토큰·전체 휴대전화번호를 남기지 않는다.
- 위치정보법·개인정보보호법 적용 범위는 운영 전 법무 검토 (§16-5).

---

## 12. 운영과 배포

### 12.1 서버 요구사항

- 최소 1 vCPU, 1 GB RAM, 10 GB 디스크(영구). **일정 시간 무접속 시 잠드는 무료·절전형 호스팅 금지.**
- 인스턴스 1대. 수평 확장 없음(§13 계산상 불필요).

### 12.2 배포 형태

1. **Docker**: 다단계 빌드 → 정적 바이너리 + distroless 이미지. `/data` 볼륨 필수.
2. **실행파일 직접**: 기관 윈도·리눅스 서버에 `server(.exe)` 1개 + `data` 폴더. 서비스 등록 예시(systemd, NSSM) 제공.
3. TLS: 기관 인증서가 있으면 리버스 프록시(nginx/Caddy) 뒤에 둔다. 없으면 내장 TLS(`TLS_CERT_FILE`, `TLS_KEY_FILE`).

### 12.3 설정 (`.env.example`)

```
BASE_URL=https://mob.example.go.kr
LISTEN_ADDR=:8080
DATA_DIR=/data
TILE_URL=https://api.vworld.kr/req/wmts/1.0.0/{key}/Base/{z}/{y}/{x}.png
TILE_KEY=
TILE_ATTRIBUTION=VWorld
NAVER_APPNAME=mob.example.go.kr
NAVER_ROUTE_TYPE=car
# 문자: manual은 항상 사용 가능. 아래 SMS_HTTP_URL을 채우면 http 방식이 추가로 켜짐
SMS_HTTP_URL=
SMS_HTTP_AUTH_HEADER=
SMS_HTTP_BODY_TEMPLATE=
SMS_HTTP_RPS=10
SMS_SENDER=
FIX_RETENTION_DAYS=30
TRUST_PROXY=false              # true면 X-Forwarded-For 사용
TLS_CERT_FILE=
TLS_KEY_FILE=
```

### 12.4 백업·점검

- 매일 03:00과 사건 종료 직후 `VACUUM INTO '/data/backup/app-YYYYMMDD-HHmm.db'`, 14개 보관.
- `GET /healthz` → `{"ok":true,"db":"ok","lastFlushAgoMs":800,"activeIncident":7}` (인증 없음, 개인정보 없음)
- 기동 로그에 설정 요약(비밀값 제외)과 마이그레이션 버전 출력.

### 12.5 v2 앱 확장 경로 (이번에 만들지 않음, API 호환만 보장)

- 같은 `/api/v1`, Bearer 토큰 사용
- 앱이 추가할 것: 백그라운드 위치, 푸시 알림(발령·임무변경·종료), 지오펜스
- 서버 판정 규칙(§6.2)은 앱에서도 그대로 사용

---

## 13. 성능 예산과 부하 계산

### 13.1 네트워크·서버 (500명 기준)

1회 위치 보고 교환 ≈ 630 B (요청 헤더 350 + 본문 60 + 응답 헤더 150 + 본문 70)

| 상황 | 초당 요청 | 대역폭 |
|---|---|---|
| 최악: 전원 차량 이동(5초마다) | 100 | 61.5 KB/s ≈ 0.50 Mbps |
| 평균 이동(15초) | 33 | 20.5 KB/s ≈ 0.17 Mbps |
| 도착 후(60초) | 8 | 5.1 KB/s ≈ 0.04 Mbps |

- 현장인력 1명 데이터 사용량: 4시간 15초 주기 ≈ **0.6 MB**(지도 타일 제외)
- 관리자 델타(3초): 변경 약 100행 ≈ 4.4 KB(gzip 전). 초기 스냅샷 ≈ 60 KB
- 위치 이력: 4시간 ≈ 120,000행 ≈ 10 MB
- 목표: 1 vCPU에서 초당 150요청 처리 시 `/f/fix` p95 < 100 ms, CPU < 50%, 메모리 < 100 MB

### 13.2 프론트엔드 크기 예산 (gzip, 지도 타일 제외)

| 화면 | 첫 로딩 | 재방문(서비스워커 캐시) |
|---|---|---|
| 현장인력 | ≤ 70 KB (Leaflet 약 40 KB 포함) | ≤ 2 KB (HTML 확인만) |
| 관리자 상황판 | ≤ 90 KB | ≤ 2 KB |

- 정적 파일은 `?v={BUILD_ID}`를 붙여 1년 캐시, HTML은 `no-cache`.
- 서비스워커는 앱 셸(HTML·JS·CSS·Leaflet)만 캐시한다. **API 응답과 지도 타일은 캐시하지 않는다.**
- 타일은 첫 화면에서 가장 무거운 부분이다. 그래서 §10.1 실측으로 공급자를 정한다.

---

## 14. 구현 단계와 수용 기준

각 단계 끝에 `make test lint`가 통과해야 한다.

### Phase 0 — 결정 확인 (코드 최소)
- [ ] VWorld 키 발급, 타일 URL 형식 실제 확인
- [ ] §10.1 지도 벤치마크 수행, 결과 기록
- [ ] §16 미결 사항 중 1·2·3을 사용자에게 확인

### Phase 1 — 서버 뼈대·인증
- [ ] 설정 검증: 필수값 누락 시 종료 코드 1 (테스트)
- [ ] 마이그레이션 001 적용, 재기동 시 데이터·세션 유지 (R13)
- [ ] `server admin create` 명령, 기본 계정 없음 (R6)
- [ ] 로그인 지연 증가 (R7), 잘못된 쿠키 401 (R11)
- [ ] 해시 세마포어: 동시 로그인 100건 중 `/healthz` p95 < 300 ms (R14)
- [ ] 보안 헤더 전부 적용 (응답 헤더 테스트)

### Phase 2 — 명단·임무지역·프리셋
- [ ] xlsx 미리보기/반영, 정규화 규칙 §4.2 전부 단위 테스트 (R4)
- [ ] 비활성화 시 세션 즉시 삭제 (R5)
- [ ] 다각형 자기교차 거부 (R9), 반경 범위, nav 점 검증
- [ ] 사용된 지역 수정 불가·복제만 가능

### Phase 3 — 사건·임무·문자
- [ ] 활성 사건 1건 제약 (DB 인덱스 + API 409)
- [ ] 조 B 임무 변경 시 조 A 배정 행 불변 (R2)
- [ ] mission_ver는 실제 바뀐 인원만 증가
- [ ] 종료 후 위치 보고 → `stop`, 저장 없음 (R3)
- [ ] SMS manual: 배치 생성·CSV·발송완료 표시 / http: 가짜 문자 서버로 성공·실패·재시도·`failed` 재발송 테스트 / http 미설정 시 manual만 노출
- [ ] 종료된 사건에 `kind:"close"` 문자 발송 가능, 그 외 수정은 409 `closed`
- [ ] 발령 미리보기 §5.4 규칙(최빈값, 동률, 개인 override 자동 등록) 테스트
- [ ] 발령 후 인원 추가 시 기존 배정 행 불변
- [ ] 응소 기록 CSV: Asia/Seoul 시각(서버 TZ=UTC에서 검증, R10), 수식 문자 이스케이프 (R12)

### Phase 4 — 위치 추적기
- [ ] §6.2 판정 규칙 표 기반 단위 테스트(연속 2건, 간격, 정확도, 끊김 초기화, 복귀)
- [ ] 사건 없음·비대상자 위치 → 상태·저장 없음, 응답 `IDLE`·`NONE` (R1)
- [ ] 기존 세션으로 `/f/me`만 호출해도 NOTIFIED → LOGGED_IN (R17)
- [ ] §6.1 `n` 표를 위에서부터 평가(원 안쪽 30 m 이상 ARRIVED → 60)
- [ ] 의심 위치 규칙 테스트
- [ ] 배치 기록: 강제 종료 후 재기동 시 상태 전이 손실 0
- [ ] `cmd/sim` 500명 × 1시간(이동→도착→일부 이탈·복귀, 중간 임무 변경 2회): §13.1 목표 충족, 판정 오류 0 (R16)

### Phase 5 — 현장인력 화면
- [ ] 크기 예산 §13.2
- [ ] 레이아웃 순서 R1~R4, 지도 없이도 텍스트·가는길 표시
- [ ] 지도 fitBounds로 임무지역 전체 표시
- [ ] 전광판 우선순위 §8.3 전 경우 (브라우저 테스트 또는 수동 체크리스트)
- [ ] 임무변경확인 팝업 → (버튼) → /f/me → /f/ack 순서, 팝업 전에는 새 임무를 받아오지 않음
- [ ] 휴대폰을 책상에 둔 정지 상태에서도 n초마다 전송 또는 sync가 일어남 (R18)
- [ ] `IDLE`·`NONE` 화면에서 위치 안내 시트가 뜨지 않음
- [ ] 상황종료 시 watch·타이머·Wake Lock 해제 확인 (R15)
- [ ] 가는길: 카카오 링크, 네이버 스킴 실기기 4경우 확인
- [ ] XSS 문자열이 든 이름·임무가 글자 그대로 보임 (R8)

### Phase 6 — 관리자 화면
- [ ] 1920×1080, 1366×768, 390×844, 360×800에서 4행 높이 비율 동일(스크린샷 첨부)
- [ ] 조 타일 3단계 밀도 전환
- [ ] 점 500개 Canvas 렌더, 델타 적용 시 프레임 드랍 없음(저사양 안드로이드 확인)
- [ ] 발령→수정→조 임무 변경→상황종료 전체 흐름
- [ ] 설정 화면 전 기능

### Phase 7 — 배포
- [ ] Docker 이미지, 실행파일 단독 실행 둘 다 기동 확인
- [ ] 백업 파일 생성·복원 절차 문서
- [ ] `/healthz`
- [ ] README는 이 문서로 링크만 두고 중복 서술하지 않음

---

## 15. 회귀 테스트 목록 (testai 검토에서 유래)

| ID | 시험 | 기대 결과 | 유래 |
|---|---|---|---|
| R1 | 사건 없음 / 비대상자가 위치 보고 | 상태·위치 저장 없음 | T1, H3 |
| R2 | 조 A 도착 후 조 B 임무·지역 변경 | 조 A 상태·도착시각 불변 | T2, T3, H2 |
| R3 | 상황종료 후 위치 보고 | `stop:1`, 저장 없음 | T13, H1 |
| R4 | `010-1111-2222`, `01011112222`, 숫자 `1011112222` 가져오기 | 같은 인원 1명, 마지막은 경고와 함께 보정 | T4, T5, H7 |
| R5 | 인원 비활성화 | 기존 세션 즉시 401, 로그인 불가 | T7, H10 |
| R6 | 빈 DB로 기동 | 로그인 가능한 계정 0개 | C4 |
| R7 | 같은 ID로 로그인 10회 실패 | 대기시간 증가, 성공 시 초기화, 잠금 없음 | C4 |
| R8 | 이름 `<img src=x onerror=alert(1)>` | 화면에 글자 그대로, 스크립트 실행 없음 + lint 0건 | C5 |
| R9 | 나비넥타이 순서 4점 다각형 저장 | 400 invalid | T9, H8 |
| R10 | 서버 TZ=UTC, 도착 00:30Z | CSV `09:30:00` | H4 |
| R11 | 쿠키 `sid=%E0%A4%A` | 401 | M4 |
| R12 | 이름 `=HYPERLINK(...)` CSV 내보내기 | `'=HYPERLINK(...)` | T10, M5 |
| R13 | 서버 재기동 | 데이터·세션 유지, 관리자 화면 resync | C2 |
| R14 | 동시 로그인 100건 | 다른 API p95 < 300 ms | M1 |
| R15 | 상황종료 수신 | 이후 위치 요청 0건(네트워크 로그) | M2 |
| R16 | 시뮬레이터 500명 1시간 | §13.1 목표, 판정 오류 0 | 신규 |
| R17 | 사전 로그인 세션으로 발령 링크 접속 | 미접속으로 남지 않고 LOGGED_IN | 설계 검토 |
| R18 | 정지한 기기(watchPosition 콜백 없음) | n초마다 위치 또는 sync 요청, 임무변경 팝업 수신 | 설계 검토 |

---

## 16. 미결 사항 (사용자 결정 필요 · 기본값으로 구현)

| 번호 | 항목 | 기본값 | 결정이 필요한 이유 |
|---|---|---|---|
| 1 | 현장인력 로그인 유지 | 24시간 또는 사건 종료 1시간 후 | 매 발령마다 로그인하면 안전하지만, 비상 시 비밀번호를 잊어 접속이 늦어질 수 있음. 사전 훈련 때 로그인해 두고 길게 유지하는 방식도 가능 |
| 2 | 로그인 ID 체계 | 관리자가 지정(예: 사번) | 휴대전화번호를 ID로 쓰면 testai H6처럼 선점 문제가 생김 |
| 3 | 문자 API 업체·기관 문자시스템 사양 | manual | http 방식 본문 템플릿을 확정해야 함 |
| 4 | 임무지역 도형 | 원 + 다각형 둘 다 | 원만 쓰면 더 단순함 |
| 5 | 위치 이력 보존기간·법적 근거 | 30일 | 위치정보법·개인정보보호법 검토 필요 |
| 6 | 네이버 길찾기 기본 수단 | 자동차 | 대중교통·도보가 맞을 수 있음 |
| 7 | 이탈 판정 여유값 | 30 m | 현장 특성(하천·도로변)에 따라 조정 |
| 8 | 서비스 도메인·인증서, VWorld 키 | – | 운영 전 준비 |
