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

## Код групи і захист від спаму

- `EDIT_CODE`: якщо змінну задано, то додавати, змінювати, видаляти й повертати зміни можна тільки з правильним кодом
  (заголовок `X-Edit-Code`, регістр і пробіли по краях не важливі). Переглядати може кожен. UI питає код один раз і
  запам'ятовує його в браузері. Без змінної редагування відкрите (зручно локально).
  На DigitalOcean: App → Settings → web → Environment Variables → `EDIT_CODE`, тип **Encrypt**.
  Щоб змінити код, достатньо поміняти змінну: старий код одразу перестане працювати.
- Обмеження частоти: не більше 30 запитів на запис за хвилину з однієї IP-адреси, інакше відповідь 429.
  Спроби з неправильним кодом теж рахуються, тож підібрати код перебором не вийде. Читання не обмежене.

Локально з кодом: `EDIT_CODE=будь-який-код docker compose up --build`.

## Telegram-бот і MCP для Claude

**Бот** працює в чаті групи через long polling (вебхук не потрібен):
- зберігає текстові повідомлення чату `CHAT_RETENTION_DAYS` днів (за замовчуванням 14), потім видаляє;
- `/today`, `/week` у чаті, а о 20:00 (Київ) нагадує про завтрашні дедлайни;
- в особистих `/key` видає персональний MCP-ключ, але тільки учасникам чату (перевіряє через `getChatMember`).
  Вихід із групи автоматично відкликає ключ. Членство перевіряється ще й під час кожного звернення до MCP
  (кеш на 10 хв), тож ключ не працюватиме, навіть якщо бот пропустив подію виходу;
- для адмінів (`TELEGRAM_ADMIN_IDS`) є `/keys` і `/revokekey ID`.

**MCP** живе на `/mcp` (streamable HTTP, stateless), доступ за заголовком `Authorization: Bearer e2_…`.
- Інструменти: `chat_messages`, `calendar`, `upcoming_deadlines`, `news`, `courses`, `recent_changes`,
  `add_deadline`, `update_deadline`, `add_news`, `add_event`. Інструментів для видалення немає.
- У базі зберігається тільки SHA-256 ключа. Кожен виклик пишеться в `mcp_access`, а зміни потрапляють у журнал
  змін з автором «Ім'я (через Claude)».
- Ліміти: 120 викликів на хвилину на ключ і 300 на IP.

Інструкція для учасників групи: [docs/CLAUDE.md](docs/CLAUDE.md).

**Налаштування:**
1. @BotFather → `/newbot`, потім `/setprivacy` → Disable (інакше бот бачить лише команди). Додай бота в чат групи.
2. У чаті напиши `/chatid`, і бот відповість id групи. Свій user id дізнаєшся, написавши боту в особисті `/chatid`.
3. Змінні в DigitalOcean (App → Settings → web → Environment Variables):

| Змінна | Приклад | Примітка |
|---|---|---|
| `TELEGRAM_BOT_TOKEN` | `123:ABC…` | **Encrypt**. Без неї бот вимкнений, а MCP працює з ключами з CLI |
| `TELEGRAM_CHAT_ID` | `-1001234567890` | id чату групи. Поки не задано, бот відповідає лише на `/chatid` |
| `TELEGRAM_ADMIN_IDS` | `111,222` | хто бачить `/keys` |
| `PUBLIC_URL` | `https://clownfish-app-lpt6d.ondigitalocean.app` | для посилань і команди підключення |
| `CHAT_RETENTION_DAYS` | `14` | скільки днів зберігати повідомлення |

Ключ без Telegram (для себе чи для тестів): у DO відкрий App → Console і виконай
`/app keys create "Ім'я" [--read]`, а також `/app keys list` і `/app keys revoke ID`.

Тести (потрібен Postgres): `TEST_DATABASE_URL=postgres://… go test ./...`. Вони проганяють бота з фейковим
Telegram API і справжнього MCP-клієнта.

## Структура

```
cmd/server/            точка входу: конфіг, міграції, HTTP-сервер
internal/db/           підключення і вбудовані SQL-міграції
internal/store/        запити до БД, журнал змін, відкат
internal/api/          JSON API, код групи
internal/validate/     перевірка даних (спільна для API і MCP)
internal/telegram/     бот: збереження чату, ключі, /today, нагадування
internal/mcpserver/    MCP-сервер для Claude
internal/e2e/          інтеграційні тести бота і MCP
docs/CLAUDE.md         як учасникам підключити Claude
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
