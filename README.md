# Distributed AI Platform

Основной документ проекта: [`DISTRIBUTED_AI_PLATFORM_PLAN.md`](DISTRIBUTED_AI_PLATFORM_PLAN.md).

Частная распределённая AI-платформа: Control Plane с единым OpenAI-совместимым API, Worker Agent на машинах участников, логические имена моделей вместо физических GPU, изоляция пользовательских данных, учёт вклада в CU.

## Текущее состояние

Go API 0.4.0: PostgreSQL 16, NATS JetStream, OpenAI-совместимый инференс через очередь заданий, вход в панель по паролю и сессиям, метрики Prometheus, бэкап БД по расписанию. Детали — [`docs/STATUS.md`](docs/STATUS.md).

## Документы

- [`DISTRIBUTED_AI_PLATFORM_PLAN.md`](DISTRIBUTED_AI_PLATFORM_PLAN.md) — основное ТЗ. Актуальные отклонения и факт — **§27**; методика UI — §23.0.
- [`docs/ui-guidelines.md`](docs/ui-guidelines.md) — обязательные правила интерфейсов (методика unslop, CC BY 4.0, Damir Sokolovsky).
- [`docs/STATUS.md`](docs/STATUS.md) — состояние этапов, проверки, риски, следующие шаги.
- [`docs/adr/0001-architecture.md`](docs/adr/0001-architecture.md) — архитектурные решения (Control/Data Plane, API, Worker, Registry, безопасность, CU).
- [`docs/adr/0002-control-node-lxc.md`](docs/adr/0002-control-node-lxc.md) — история решения по Proxmox-узлу (superseded).
- [`docs/adr/0003-network-identity-and-relocation.md`](docs/adr/0003-network-identity-and-relocation.md) — домены вместо IP, VPN, переезд сервера.
- [`docs/adr/0004-storage-topology.md`](docs/adr/0004-storage-topology.md) — что хранится на сервере, а что на Worker (веса на сервере не хранятся).
- [`docs/adr/0005-sites-and-environments.md`](docs/adr/0005-sites-and-environments.md) — площадка: всё на арендованном VPS.
- [`docs/adr/0006-mvp-single-worker.md`](docs/adr/0006-mvp-single-worker.md) — MVP на одном Worker, распределённый режим отложен, готовность к набору участников.
- [`docs/operations/panel-access.md`](docs/operations/panel-access.md) — вход в панель: аккаунты, пароли, сессии, метрики.
- [`docs/operations/participant-requirements.md`](docs/operations/participant-requirements.md) — требования и опросник для будущих участников.
- [`docs/operations/vps-inventory.md`](docs/operations/vps-inventory.md) — инвентаризация VPS.
- [`docs/operations/proxmox-inventory.md`](docs/operations/proxmox-inventory.md) — инвентаризация домашнего Proxmox (вне платформы, обслуживает проект владельца).
- [`docs/operations/relocation-runbook.md`](docs/operations/relocation-runbook.md) — чек-лист переезда сервера и смены IP.

## Код и скрипты

- `cmd/api-gateway` — HTTP API (`serve`, `bootstrap-admin`, `set-password`, `issue-key`, `revoke-key`, `version`).
- `cmd/worker-agent` — Worker Agent (`enroll`, `run`, `status`, `discover`, `pause`, `resume`, `version`).
- `internal/auth` — выпуск и проверка API-ключей и токенов сессий панели (HMAC-SHA256 + pepper), юнит-тесты.
- `internal/httpapi` — маршруты, аутентификация (ключ или сессия), регистрация узлов, вход в панель, `/metrics`, JSON-логи без содержимого запросов.
- `internal/store` — доступ к PostgreSQL через pgx (пользователи, ключи, узлы, телеметрия).
- `internal/worker` — discovery (PowerShell/nvidia-smi/*proc*), клиент Control Plane, цикл heartbeat с backoff.
- `internal/workercfg` — локальная конфигурация агента (секрет устройства, права 0600).
- `migrations/` — версионированные миграции (`scripts/migrate.sh`).
- `deploy/control/` — systemd-юнит, nginx-vhost, пример окружения.
- `scripts/vps-control-plane.sh` — развёртывание Control Plane на VPS рядом с действующим проектом.
- `scripts/install-worker-agent.ps1` — установка агента как задачи планировщика (Windows, от администратора).
- `scripts/pg-backup.sh` — бэкап БД с проверкой целостности.
- `scripts/pve-audit.sh`, `scripts/ct-bootstrap.sh`, `scripts/ct-stage1.sh` — аудит и стенд на Proxmox (вне текущего плана).
- `tools/ssh_run.py` — запуск команд на серверах по SSH; пароли берутся из окружения и в репозитории не хранятся.

## Worker Agent: быстрый старт

```powershell
go build -o bin\worker-agent.exe .\cmd\worker-agent
.\bin\worker-agent.exe enroll --control https://control.nshno.tech --token dai_... --name owner-pc
.\bin\worker-agent.exe discover     # что видит агент: CPU, GPU, VRAM, диски
.\bin\worker-agent.exe run          # цикл heartbeat, Ctrl+C для остановки
```

Enrollment-токен выпускается администратором: `POST /admin/v1/enrollments` (одноразовый, 24 ч).
Секрет устройства хранится в `C:\ProgramData\ComputeNetwork\agent.json`, каталог закрыт до SYSTEM + Administrators — поэтому `status`/`run` требуют администраторской консоли, а автозапуск ставится через `scripts\install-worker-agent.ps1`.

## Быстрый старт локально

```bash
go test ./...
go build ./cmd/api-gateway
DATABASE_URL=... API_PEPPER=<32+ символов> ./api-gateway bootstrap-admin you@example.com
```

## Макеты интерфейсов

- `assets/01-user-workspace.png` — пользовательское рабочее пространство.
- `assets/02-contributor-dashboard.png` — кабинет участника/Worker.
- `assets/03-admin-console.png` — административная панель.
- `assets/04-model-registry.png` — каталог моделей и deployment wizard.
- `assets/05-worker-settings.png` — настройки Worker Agent.

Макеты концептуальные: точные подписи и поведение элементов описаны в основном Markdown-документе.

## Лицензия

Код распространяется по [Apache License 2.0](LICENSE).
