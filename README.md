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

## Деплой на DigitalOcean App Platform

Специфікація лежить у `.do/app.yaml`: сервіс із `Dockerfile` (1 vCPU / 512 МБ, $5/міс), dev-база PostgreSQL
($7/міс), регіон Франкфурт, автодеплой на push у `main`.

1. DigitalOcean → **Apps → Create App** → GitHub → дати доступ до `zh0glikk/tasks-manager`, гілка `main`.
2. Звичайне створення з GitHub **не** читає `.do/app.yaml` само. Тож або на кроці налаштувань, або вже після
   створення (App → Settings → App Spec → Edit) встав вміст `.do/app.yaml`. Це додасть dev-базу `db` і змінну
   `DATABASE_URL=${db.DATABASE_URL}`. Без неї сервер падає з `DATABASE_URL is required`.
3. Save / Create Resources → через кілька хвилин застосунок буде доступний на `https://tasks-manager-xxxxx.ondigitalocean.app`.

Альтернатива через CLI: `doctl apps create --spec .do/app.yaml`.

Міграції запускаються у фоні з повторами: сервер і `/healthz` піднімаються одразу, а `/api/*` відповідає 503, доки
міграції не застосуються. Так зроблено тому, що dev-база DigitalOcean видає права на схему `public` тільки після
першого успішного деплою.

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
