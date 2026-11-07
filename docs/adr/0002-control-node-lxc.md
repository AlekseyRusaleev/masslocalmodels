# ADR 0002 — Управляющий узел: unprivileged LXC `compute-control`

- **Статус:** **superseded** (2026-11-05). Владелец вывел домашний Proxmox из плана платформы; `CT 108` остановлен, платформа живёт на арендованном VPS — см. [`0005`](0005-sites-and-environments.md).
- Ниже сохранено исходное решение и его проверка — как история и как готовая процедура, если понадобится стенд на Proxmox.
- **Контекст:** [`0001-architecture.md`](0001-architecture.md), [`../operations/proxmox-inventory.md`](../operations/proxmox-inventory.md)

## 1. Решение

Control Plane размещён в **unprivileged LXC** на Proxmox: `CT 108 / compute-control`.

| Параметр | Значение |
|---|---|
| ОС | Debian 13.6 (trixie), шаблон `debian-13-standard_13.6-1_amd64` |
| vCPU | 2 (`cpulimit: 2`) |
| RAM / swap | 2048 MB / 1024 MB |
| rootfs | 24 GB на `local-lvm` (thin), занято 1.7 GB |
| Сеть | `vmbr0`, `192.168.10.60/24`, gw `192.168.10.1`, DNS `1.1.1.1` |
| Привилегии | `unprivileged: 1`, `features: nesting=1,keyctl=1,fuse=1`, **без отключения AppArmor** |
| Автозапуск | `onboot: 1` |
| Часовой пояс | `Europe/Moscow` |
| Бэкап | входит в задачу `vzdump` (`101,108`, 03:30, `local`, zstd) — проверено, архив 649 MB |

Обоснование выбора LXC: при свободных ≈2.9 GiB RAM на хосте контейнер не создаёт второе ядро и второй набор системных служб. Фактическое потребление — **53 MB из 2048 MB**, диск — 1.7 GB из 24 GB.

## 2. Стек внутри: нативные службы, без Docker

| Компонент | Версия | Запуск |
|---|---|---|
| PostgreSQL | 17.11 (`postgresql` из Debian 13) | `postgresql.service`, БД `compute_control`, роль `control` |
| NATS Server | 2.10.27 (`nats-server`) | `nats-server.service`, `127.0.0.1:4222` |
| API Gateway | Go 1.24, сборка из `cmd/api-gateway` | `api-gateway.service`, `127.0.0.1:8080` |

Адреса прослушивания — только loopback. Наружу ничего не публикуется: внешний доступ появится на Этапе 4 через reverse-proxy (443) и, при необходимости, VPS-релей.

### 2.1. Почему от Docker отказались

Docker внутри unprivileged LXC потребовал:

```
lxc.mount.auto: proc:rw sys:rw
lxc.apparmor.profile: unconfined
```

Второй параметр снимает AppArmor-профиль со всего контейнера — Proxmox предупреждает об этом явно. Для управляющего узла, который хранит пользовательские данные и ключи, это неприемлемый размен, если того же результата можно достичь без Docker. Проверено: после удаления Docker и обеих строк конфигурации контейнер запускается снова, профиль AppArmor применяется, а PostgreSQL 17, NATS и Go-сервис работают штатно.

Плата за отказ: нет `docker compose`-воспроизводимости. Компенсация — `scripts/ct-stage1.sh` (идемпотентная установка), `scripts/migrate.sh` (версионированные миграции) и systemd-юниты в `deploy/control/`.

Docker остаётся допустимым для Worker-узлов (там нет пользовательских БД и ключей расшифровки) и для управляющего узла, если он когда-нибудь переедет в KVM с полноценным ядром.

## 3. Изоляция процесса API Gateway

Юнит `deploy/control/api-gateway.service` работает от системного пользователя `control` (shell `/usr/sbin/nologin`) и ограничен: `NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `PrivateDevices`, `ProtectKernel*`, `RestrictAddressFamilies`, `RestrictNamespaces`, `MemoryDenyWriteExecute`, `SystemCallFilter=@system-service`, пустые `CapabilityBoundingSet`/`AmbientCapabilities`, `UMask=0027`.

## 4. Что проверено

| Проверка | Результат |
|---|---|
| `systemctl is-active postgresql nats-server api-gateway` | `active active active` |
| Миграции | `0001_init` применена, `schema_migrations` заполнена |
| `curl http://127.0.0.1:8080/health` | `{"checks":{"database":"ok","nats":"ok"},"status":"ok","version":"0.1.0"}` |
| `curl http://127.0.0.1:8080/v1/models` | `{"data":[],"object":"list"}` |
| Логи | JSON, содержат метод/путь/статус/request_id, **без тел запросов** |
| `vzdump 108` | 649 MB, 58 с, архив и лог в `/var/lib/vz/dump/` |
| Негативный сценарий | остановлен NATS → `nats:"down"`; остановлен PostgreSQL → `database:"down"`; API при этом остаётся `active` и возвращает 200 с честным статусом зависимостей |

Изначально в юните стояло `Requires=postgresql.service`: при остановке PostgreSQL systemd гасил и API (`inactive`). После проверки зависимость ослаблена до `Wants=`, чтобы узел переживал кратковременный рестарт БД и сообщал о деградации, а не исчезал. Текущее состояние: `NRestarts=0`, все три службы `active`.

## 5. Последствия и путь миграции

- Данные: PostgreSQL в `/var/lib/postgresql` внутри контейнера, покрыты `vzdump` CT 108. На Этапе 5 добавится WAL-архивация и внешний бэкап ключей.
- Рост RAM — одна команда: `pct set 108 --memory 8192 --swap 2048` + рестарт. После переезда проекта владельца (освобождается ≈14 GB) узел можно поднять до 4–8 GB или перенести в KVM, сохранив тот же стек.
- Пароль root контейнера сгенерирован при создании; владельцу нужно сменить его и перейти на ключи.
