# 비상 인력동원 위치관제 시스템

설계와 구현 명세는 전부 [docs/SPEC.md](docs/SPEC.md)에 있습니다. 이 문서는 여기서
중복 서술하지 않습니다 (SPEC §14 Phase 7).

## 빠르게 시작하기

```bash
cp deploy/.env.example deploy/.env   # 값 채우기 (TILE_KEY 등)
export $(cat deploy/.env | xargs)    # 또는 서비스 매니저의 환경변수 기능 사용
go build -o bin/server ./cmd/server
./bin/server admin create --id admin --name "관리자" --role admin
./bin/server
```

또는 Docker:

```bash
cp deploy/.env.example deploy/.env
docker compose -f deploy/docker-compose.yml up -d
docker compose -f deploy/docker-compose.yml exec server /server admin create --id admin --name "관리자" --role admin
```

- 현장인력 화면: `/f/`
- 관리자 상황판: `/a/`
- 관리자 설정: `/a/setup.html`
- 헬스체크: `/healthz`

## 서비스 등록 (실행파일 직접 배포, SPEC §12.2)

**systemd** (`deploy/ecu.service`로 저장 후 `systemctl enable --now ecu`):

```ini
[Unit]
Description=Emergency Callup Location Tracking
After=network.target

[Service]
Type=simple
EnvironmentFile=/opt/ecu/.env
ExecStart=/opt/ecu/server
Restart=on-failure
RestartSec=3
User=ecu

[Install]
WantedBy=multi-user.target
```

**Windows (NSSM)**:

```
nssm install ECU "C:\ecu\server.exe"
nssm set ECU AppEnvironmentExtra BASE_URL=https://mob.example.go.kr DATA_DIR=C:\ecu\data ...
nssm set ECU AppDirectory C:\ecu
nssm start ECU
```

## 개발

```bash
make test    # go test ./...
make lint    # gofmt + go vet + innerHTML 등 금지 API 검사 (SPEC §11.3)
make sim     # 부하 시뮬레이터 (cmd/sim). SIM_ADMIN_ID/PW/BASE_URL 등 필요
```

## 백업 복원 (SPEC §12.4)

백업은 `/data/backup/app-YYYYMMDD-HHMMSS.db`에 매일 03:00(Asia/Seoul)과
상황종료 직후 생성되며 최근 14개를 보관합니다. 복원하려면 서버를 멈추고
원하는 백업 파일을 `/data/app.db`로 복사한 뒤 다시 시작하세요.

```bash
systemctl stop ecu   # 또는 docker compose stop server
cp /data/backup/app-20260911-030000.db /data/app.db
systemctl start ecu
```

## 미해결/수동 확인이 필요한 항목

- **§10.1 지도 벤치마크**: VWorld 실키 발급과 카카오 SDK와의 실기기 비교
  측정은 아직 수행되지 않았습니다. 기본값은 Leaflet + VWorld입니다.
- **§10.3 가는길 실기기 확인**: 카카오·네이버 링크의 안드로이드/아이폰,
  앱 설치/미설치 4가지 경우는 실기기에서 수동으로 확인해야 합니다.
- **§16 미결 사항 1·2·3**: 사용자 확인 전까지 기본값(세션 24시간/종료+1시간,
  로그인ID는 관리자 지정, 문자는 manual)으로 구현했고 코드에
  `// DECISION:` 주석으로 표시했습니다.
- **`cmd/sim`의 500명·1시간 정식 실행(R16)**: 소규모로 동작을 확인했지만,
  전체 규모·시간의 실행과 판정 정확도 교차검증은 배포 전 별도로 수행하세요.
