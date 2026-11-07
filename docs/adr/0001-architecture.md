# ADR 0001 — Базовая архитектура платформы

- **Статус:** accepted (базовые решения), часть деталей — pending
- **Дата:** 2026-11-05
- **Контекст:** [`DISTRIBUTED_AI_PLATFORM_PLAN.md`](../../DISTRIBUTED_AI_PLATFORM_PLAN.md) — исходное ТЗ; [`../STATUS.md`](../STATUS.md) — состояние этапов; [`../operations/proxmox-inventory.md`](../operations/proxmox-inventory.md) — фактические ресурсы.

## 1. Решение: разделение Control Plane и Data Plane

Принято: Control Plane — отдельный управляющий узел на Proxmox (без GPU), Data Plane — Worker Agent на машинах участников.

Обоснование: аудит показал, что у Proxmox нет GPU, пригодного для инференса (GT 640, Kepler), и нет запаса RAM для тяжёлых процессов. Управляющий узел не должен быть прокси для трафика тензоров.

Следствия:

- на управляющем узле работают API Gateway, Auth, Scheduler, Model Registry, Deployment Manager, Ledger, Audit, PostgreSQL, NATS;
- веса и активации передаются Worker↔Worker или через S3, не через управляющий узел;
- управляющий узел можно потерять без потери весов моделей (они кэшируются на Worker).

## 2. Решение: единый OpenAI-совместимый API

Принято: внешний контракт — `/v1/models`, `/v1/chat/completions` (SSE), `/v1/health`, `/v1/usage`. Model ID — логический alias, физическая топология скрыта. Admin API — отдельный namespace `/admin/v1/*`, Worker API — `/worker/v1/*`.

Обоснование: DeepSeek Harness подключается как Custom model API, клиенту нужен только Base URL и ключ. Любая привязка клиента к внутренним адресам vLLM/Petals недопустима.

Следствия: Gateway отвечает за TLS, аутентификацию, стриминг и request ID; vLLM никогда не публикуется наружу.

## 3. Решение: адаптеры Runtime, а не один движок

Принято: vLLM — основной путь для LLM-реплик; Petals — изолированный PoC для WAN-шардинга; `torch.distributed.pipelining` — экспериментальный адаптер. Ни один движок не является фундаментом системы.

Принято также: по умолчанию для разнесённых по домам узлов используется **replica** и **independent compute**, а не tensor parallelism. Pipeline — только для моделей, которые физически не помещаются в одну GPU, с обязательными замерами TTFT и tokens/sec.

## 4. Решение: Worker Agent с исходящим соединением и собственным сертификатом

Принято:

- Worker не принимает входящие соединения из интернета; инициирует TLS-соединение к Control Plane (при необходимости — через WireGuard/Tailscale);
- enrollment по одноразовому короткоживущему токену, затем mTLS-сертификат устройства;
- задания выдаются как подписанные lease с TTL и heartbeat;
- runtime-контейнеры запускаются без privileged, без Docker socket, без монтирования host filesystem.

## 5. Решение: Model Registry с pinned revision и проверкой лицензии

Принято: для каждой модели хранится source, commit hash/revision, лицензия и её статус, runtime, quantization, список артефактов с SHA-256, требования к железу, режим размещения. Плавающая ветка `main` не используется.

Следствия: активация deployment невозможна при несовпадении контрольной суммы; удаление весов — только когда ни один deployment/Worker их не использует.

## 6. Решение: безопасность и приватность как часть MVP

Принято:

- три уровня разделяются явно: TLS/WireGuard (транспорт), шифрование дисков/полей (хранение), E2EE (Local Only-режим);
- приватные чаты обслуживает только доверенный пул (`Private Trusted Pool`), публичные Worker — только задачи, помеченные безопасными;
- KV Cache и prefix cache изолированы по tenant; межпользовательский prefix caching в MVP выключен;
- в БД хранится только hash API-ключей; секрет показывается один раз;
- аудит пишет действия, но не содержимое сообщений, файлов и промптов;
- логи редактируют Authorization, ключи, промпты, содержимое файлов.

## 7. Решение: CU — внутренний учёт, не деньги

Принято: две независимые величины — `contribution_cu` и `consumption_cu`; начисление только за принятые и прошедшие валидацию задания, идемпотентно по `task_attempt`; ledger append-only; формула и calibration-набор версионируются.

Следствия: нет подписки, нет месячных пакетов, нет токена; при перегрузке применяется weighted fair queueing с aging.

## 8. Решение: стек

| Область | Выбор | Статус |
|---|---|---|
| Control Plane / API | Go | accepted |
| БД | PostgreSQL | accepted |
| Очередь/события | NATS JetStream | accepted |
| Web | React + TypeScript | accepted |
| Worker Agent | Go | accepted |
| ML-адаптеры | Python (vLLM, Petals, Diffusers) | accepted |
| Объектное хранилище | S3-совместимое | accepted |
| Секреты | защищённый secret store; Vault/KMS позже | accepted |
| Метрики | Prometheus + Grafana, без пользовательских labels | accepted |
| Развёртывание управляющего узла | LXC vs KVM — **pending** | см. STATUS §3 |

## 9. Открытые вопросы, требующие отдельного ADR

1. 0002 — тип управляющего узла (LXC `compute-control` vs KVM) и параметры ресурсов.
2. 0003 — выбор канала Worker↔Control Plane: свой WireGuard-хаб на Proxmox vs Tailscale-координация.
3. 0004 — объектное хранилище: MinIO на Proxmox vs внешний S3 (влияет на 663 GB свободного места и на канал).
4. 0005 — схема шифрования MVP (LUKS + шифрование полей) и путь к envelope encryption.
5. 0006 — конкретная первая модель и её лицензия (после замера VRAM/диска на двух GPU).
