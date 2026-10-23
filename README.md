# Е2 Екологія · що на коли

Спільний календар групи: заняття, дедлайни/ДЗ і новини. Без реєстрації: додавати й редагувати може кожен, хто має посилання,
а кожна зміна пишеться в журнал, звідки її можна повернути.

Стек: Go (`net/http` + `pgx`) і PostgreSQL. UI — статичні HTML/CSS/JS, вбудовані в бінарник через `go:embed`.
На виході один процес.

## Локальний запуск

```sh
docker compose up --build
```

Відкрити http://localhost:8080. Під час першого старту міграції створюють таблиці й заливають осінній розклад
(`internal/db/migrations/002_seed_schedule.sql`).

Без Docker для застосунку (Postgres усе одно потрібен):

```sh
docker compose up -d db
DATABASE_URL=postgres://tasks:tasks@localhost:5432/tasks?sslmode=disable go run ./cmd/server
```

## Деплой: DigitalOcean App Platform + Neon

Застосунок працює на App Platform (`.do/app.yaml`: сервіс із `Dockerfile`, 1 vCPU / 512 МБ, $5/міс, Франкфурт,
автодеплой на push у `main`). База — безкоштовний Postgres у [Neon](https://neon.tech).

Чому не dev-база DigitalOcean: її користувач не має права створювати таблиці (`permission denied for schema public`)
і навіть власну схему (`permission denied for database`), тож міграції не проходять.

**База (Neon):**
1. [console.neon.tech](https://console.neon.tech) → Create project, регіон **AWS Europe Central 1 (Frankfurt)**.
2. Connect → **вимкнути Connection pooling** (пряме підключення) → скопіювати рядок
   `postgresql://…neon.tech/neondb?sslmode=require`.

**Застосунок (DigitalOcean):**
1. Apps → Create App → GitHub → `zh0glikk/tasks-manager`, гілка `main`.
2. Settings → App Spec → Edit → вставити `.do/app.yaml`, підставивши свій рядок Neon у `DATABASE_URL`
   (`type: SECRET`, тому DO зберігає його зашифрованим). Створення з GitHub не читає `.do/app.yaml` само.
3. Save → після деплою в Runtime Logs має бути `migrations applied`.

Міграції запускаються у фоні з повторами: сервер і `/healthz` піднімаються одразу, а `/api/*` відповідає 503, доки
міграції не застосуються. Якщо в базі немає права писати в `public`, сервер пробує створити й використовувати схему `app`.

Свій домен: App → Settings → Domains.

## Структура

```
cmd/server/            точка входу: конфіг, міграції, HTTP-сервер
internal/db/           підключення і вбудовані SQL-міграції
internal/store/        запити до БД, журнал змін, відкат
internal/api/          JSON API і валідація
web/static/            UI (index.html, style.css, app.js)
```

## API

| Метод | Шлях | Що робить |
|---|---|---|
| GET | `/api/courses` | предмети |
| GET | `/api/events?from=YYYY-MM-DD&to=YYYY-MM-DD` | заняття за період |
| GET | `/api/assignments?from=…&to=…` | дедлайни за період |
| GET | `/api/posts?limit=30` | новини (закріплені першими) |
| POST / PUT / DELETE | `/api/{courses,events,assignments,posts}[/{id}]` | створити / змінити / видалити |
| GET | `/api/changes?limit=50` | журнал змін |
| POST | `/api/changes/{id}/revert` | повернути зміну |

Усі запити на запис приймають JSON з полем `author`. `POST /api/events` з полем `repeat_until` створює щотижневу серію.
Видалення м'яке: рядок отримує `deleted_at`, і його можна відновити з журналу.
