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

## Деплой на Render

1. Render → **New → Blueprint** → вибрати цей репозиторій.
2. Render прочитає `render.yaml` і створить web-сервіс `tasks-manager` та Postgres `tasks-manager-db`.
   `DATABASE_URL` підставиться автоматично.
3. Кожен push у `main` деплоїться сам.

Обмеження безкоштовного плану Render: сервіс засинає після ~15 хв без запитів (перший запит після цього йде ~30–60 с),
а безкоштовна база видаляється через 30 днів, якщо не перейти на платний план.

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
