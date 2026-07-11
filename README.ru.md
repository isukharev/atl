[English](README.md) · **Русский**

# atl

[![Go](https://img.shields.io/badge/go-1.26-blue?logo=go)](https://go.dev)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/isukharev/atl/ci.yml?branch=main&label=CI)](https://github.com/isukharev/atl/actions/workflows/ci.yml)

**Git-style CLI для Confluence и Jira — создан для кодинг-агентов.**

`atl` позволяет кодинг-агенту (например, Claude Code или Codex) работать с Confluence и Jira
так же, как с кодом: зеркалировать документы на диск, искать с помощью `ripgrep`,
редактировать **нативный формат хранения** (Confluence Storage Format, `.csf`),
работать с диффами и публиковать изменения под **оптимистичным version gate**, который
не позволяет молча затереть параллельные правки.

> **Отказ от аффилиации:** этот проект является независимым инструментом с открытым
> исходным кодом и НЕ связан с Atlassian Pty Ltd, не одобрен и не спонсирован ею.
> Подробнее — в разделе [Товарные знаки и отказ от ответственности](#товарные-знаки-и-отказ-от-ответственности).

---

## Возможности

- **Зеркало на диске** — скачивает страницы (вместе с ресурсами) в локальную директорию,
  воспроизводящую иерархию страниц Confluence; ищите любым текстовым инструментом.
- **Редактирование в нативном формате** — работа напрямую с байтами `.csf` (Confluence
  Storage Format); без потерь при Markdown-конвертации: макросы, панели, шаблоны и
  диаграммы не теряются молча.
- **Оптимистичный version gate** — `push` завершается с кодом 5 при расхождении версий
  и заранее показывает последствия через `--dry-run`; `--force` разрешает конфликт, когда
  вы отдаёте себе отчёт в происходящем.
- **Работа с диаграммами** — draw.io-макросы разрешаются в PNG нужной ревизии, чтобы
  агент с поддержкой vision мог их изучить.
- **Интеграция с Jira** — запросы, комментарии, переходы статусов; зеркалирование задач на диск
  как нативный `.wiki` + отрендеренный `.md`, затем правка `# Description` или явно разрешённых
  rich-text полей в `.md`-виде и подготовка через `jira apply` (или правка `.wiki` напрямую), и отправка через
  `jira status` / `jira push` (по умолчанию dry-run; защита от дрейфа отклоняет устаревшую запись,
  так как в Jira нет серверного version gate).
- **Bearer PAT, per-request** — токены отправляются только на настроенный хост и никогда
  не записываются в репозиторий или зеркало.
- **Самообновление с подписью** — бинарник обновляется из GitHub Releases не чаще одного
  раза в 6 часов, с проверкой SHA-256 и ed25519-подписи. Подробнее — в
  [docs/self-update.md](docs/self-update.md) и [SECURITY.md](SECURITY.md).
- **Удобен для скриптов** — JSON в stdout, логи и ошибки в stderr, без интерактивных
  запросов, чёткие коды выхода.
- **Один статический бинарник** — `CGO_ENABLED=0`, запускается везде, где работает Go 1.26.

---

## Установка

### Быстрая установка (Linux / macOS)

```sh
curl -fsSL https://github.com/isukharev/atl/releases/latest/download/install.sh | sh
```

Устанавливает в `~/.local/bin/atl` и проверяет SHA-256 контрольную сумму. С каждым
релизом публикуется SLSA build provenance для опциональной проверки out-of-band
(см. [docs/RELEASING.md](docs/RELEASING.md)); сам установщик не требует `gh`.

### go install

```sh
go install github.com/isukharev/atl/cmd/atl@latest
```

### Скачать бинарник

Скачайте готовый бинарник со страницы
[GitHub Releases](https://github.com/isukharev/atl/releases). Рядом с каждым релизом
публикуются контрольные суммы и подписи.

### Homebrew

```sh
brew install isukharev/tap/atl
```

> Формула (`atl.rb`, с привязкой к SHA-256 каждого бинарника) публикуется с каждым релизом. Если
> tap ещё недоступен, используйте быструю установку или `go install` выше.

**Требования:** Linux или macOS (amd64/arm64). Для сборки из исходников нужен Go 1.26+; у готового
бинарника нет зависимостей времени выполнения.

---

## Быстрый старт

От нуля до первого результата напрямую через CLI (для агентских плагинов — см. следующий раздел):

```sh
# 1. Установка (Linux/macOS) — затем добавьте ~/.local/bin в PATH, если установщик попросит
curl -fsSL https://github.com/isukharev/atl/releases/latest/download/install.sh | sh

# 2. Укажите atl на ваш(и) инстанс(ы) — Server/Data Center, обязателен https
atl config set --confluence-url https://confluence.example.com \
               --jira-url       https://jira.example.com

# 3. Добавьте Personal Access Token (скрытый ввод; никогда в argv)
atl auth login --service confluence

# 4. Проверьте и сделайте дешёвое чтение
atl auth status
atl conf search --cql 'type = page' --limit 1
```

Чистый JSON-результат на шаге 4 означает, что всё готово. Код выхода **7** — URL или PAT ещё не
настроены (завершите шаги 2–3); **3** — PAT передан, но сервер его отклонил. Автоматизируете в CI?
См. [docs/usage.md → Scripting & CI](docs/usage.md#scripting--ci).

---

## Использование с кодинг-агентами

`atl` поставляет устанавливаемые workflow для Claude Code и Codex, так что агент может сам
установить CLI и работать с ним за вас.

### Claude Code

Этот репозиторий одновременно является marketplace плагинов для
[Claude Code](https://claude.com/claude-code). Добавьте marketplace и установите плагин:

```
/plugin marketplace add isukharev/atl
/plugin install atl@atl
/atl:setup
```

`/atl:setup` устанавливает бинарник `atl`, если его нет, настраивает аутентификацию и базовые URL
Confluence/Jira и согласовывает локальную директорию зеркала. После этого Claude Code автоматически
использует общие скиллы ниже по мере необходимости. Версии плагина следуют релизам CLI —
включите автообновление atl-marketplace (`/plugin` → Marketplaces → Enable auto-update; для
сторонних marketplace оно по умолчанию выключено), чтобы каждый релиз обновлял скиллы вместе с
самообновляющимся бинарником.

### Codex

В репозитории также есть metadata Codex-плагина и repo-local marketplace. Добавьте marketplace и
установите тот же набор workflow:

```sh
codex plugin marketplace add isukharev/atl
codex plugin add atl@atl
```

Затем начните новую сессию Codex, вызовите skill `setup` через `/skills` или `$setup` и дайте ему
установить/настроить CLI `atl`. После этого можно отдельно вызвать `$onboarding`: он сформирует
проверяемый приватный профиль только по явно разрешённым примерам. После setup Codex сможет
использовать те же встроенные скиллы по мере необходимости.

Основные скиллы:

- **`atl`** — ориентация: когда использовать `atl` (а когда live Atlassian MCP), workflow
  «сначала поиск» и где живёт зеркало.
- **`confluence`** — pull, правка `.csf`, валидация и публикация страниц под version gate.
- **`jira`** — поиск/выгрузка задач, точное обнаружение папок и нормализованные срезы Structure и Kanban/Scrum-досок,
  а также create/update/transition/comment/link через guarded-команды.
- **`onboarding`** — опциональное consent-gated изучение workflow, явные командные defaults и
  проверяемый приватный профиль; дальнейшие наблюдения превращаются в deterministic
  review/apply/reject suggestions, schema facts ревалидируются явно, а сохранённые настройки
  рендера/зеркала синхронизируются с runtime только после отдельного подтверждения.

Поверх справочных скиллов плагин включает workflow-рецепты — сквозные процессы со встроенным
подтверждением перед созданием чего-либо:

- **`search-knowledge`** — ответы на вопросы по Confluence + Jira с цитированием источников.
- **`triage-issue`** — поиск дублей и прошлых фиксов перед заведением структурированного бага.
- **`status-report`** — статус-отчёт из Jira с опциональной публикацией в Confluence.
- **`spec-to-backlog`** — превращает спецификацию из Confluence в Epic со связанными задачами.
- **`sprint-dashboard`** — read-only визуальный срез текущего спринта.
- **`meeting-tasks`** — action items из заметок встречи в задачи Jira с исполнителями.

Обе платформы получают одни и те же скиллы, генерируемые из единого источника
[`skills-src/`](skills-src/) (устройство пайплайна: [docs/plugins.md](docs/plugins.md)). Упаковка
Claude Code находится в [`.claude-plugin/`](.claude-plugin/); упаковка Codex — в
[`plugins/atl`](plugins/atl), а repo marketplace — в
[`.agents/plugins/marketplace.json`](.agents/plugins/marketplace.json).

---

## Аутентификация

```sh
# 1. Укажите базовые URL ваших инстансов Confluence и Jira
atl config set \
  --confluence-url https://confluence.example.com \
  --jira-url       https://jira.example.com

# 2. Передайте Personal Access Token (PAT) — только Server/Data Center.
#    Токен читается из скрытого ввода, stdin или --from-file — никогда из argv.
atl auth login --service confluence   # запросит ввод без эха
atl auth login --service jira         # запросит ввод без эха

# Или используйте переменные окружения (рекомендуется для CI / агентских сессий):
export ATL_CONFLUENCE_PAT=<PAT>
export ATL_JIRA_PAT=<PAT>

# 3. Проверьте
atl auth status
atl config show
```

Токены хранятся в файле `0600` в директории `~/.config/atl` (или берутся из
переменных окружения выше). Они никогда не записываются в зеркало или репозиторий.

> **Только Server / Data Center.** `atl` аутентифицируется через **bearer Personal Access Token** —
> это модель токенов Confluence/Jira **Server & Data Center**. Atlassian **Cloud**
> (`*.atlassian.net`) использует Basic-аутентификацию email + API-token и **не поддерживается**.
>
> - **Базовый URL** — то, что вы вводите в браузере для доступа к инстансу, например
>   `https://confluence.example.com` (без `/wiki`, `/display/…` или пути к странице). Обязателен
>   `https` (для внутреннего http-инстанса нужен `ATL_ALLOW_INSECURE=1`).
> - **PAT** — создаётся в веб-интерфейсе: ваш профиль → **Personal Access Tokens** → *Create token*.
>   Используйте токен с минимальными правами под задачу; для Confluence и Jira нужны отдельные токены.

---

## Работа с Confluence

### 1. Скачать страницы на диск

```sh
atl conf pull \
  --cql 'space=DOCS and title~"Acme"' \
  --assets \
  --into mirror
```

### 2. Изучить зеркало

```
mirror/
  DOCS/                         # ключ пространства
    acme-adr/
      acme-adr.csf              # источник правды (нативный формат хранения)
      acme-adr.md               # производное staging-представление; изменения через conf apply
      acme-adr.meta.json        # id, версия, хэш содержимого, разрешённые фрагменты, comment_count
      acme-adr.comments.json    # [{id,author,created,body,body_storage?}] (при --comments)
      acme-adr.comments.md      # производное представление для чтения (при --comments)
      acme-adr.assets/*.png     # рендеры draw.io + изображения страницы (при --assets)
      child-page/…              # дерево папок воспроизводит иерархию страниц
  .atl/                         # sidecar: последние синхронизированные версии/хэши + база
  .gitignore
```

Ищите по зеркалу любым текстовым инструментом:

```sh
rg "decision" mirror/
```

### 3. Редактирование, валидация и публикация

```sh
# Проще всего: проверьте marker v2, правьте markdown-представление и слейте правки в .csf.
# Нетронутые блоки сохраняют байты в точности; неконвертируемые правки отклоняются.
$EDITOR mirror/DOCS/acme-adr/acme-adr.md
atl conf apply mirror/DOCS/acme-adr/acme-adr.md --dry-run
atl conf apply mirror/DOCS/acme-adr/acme-adr.md

# Или редактируйте нативный формат напрямую
$EDITOR mirror/DOCS/acme-adr/acme-adr.csf

# Валидация перед публикацией (блокирует при невалидном XML, предупреждает о проблемах)
atl conf validate mirror/DOCS/acme-adr/acme-adr.csf

# Dry-run — посмотрите, что сделает push
atl conf push mirror/DOCS/acme-adr/acme-adr.csf --dry-run

# Публикация (выходит с кодом 5 при расхождении версий; для восстановления: re-pull + reapply)
atl conf push mirror/DOCS/acme-adr/acme-adr.csf

# Статус синхронизации
atl conf status mirror --remote
```

### Другие команды Confluence

```sh
atl conf search --cql 'space=DOCS and label="adr"'
atl conf space tree --space DOCS
# Идентификаторы страниц берутся из вывода atl conf pull (meta.json → поле "id") или URL страницы.
atl conf page view 123456 -o text   # настроенный Markdown без артефактов mirror
atl conf page get     --id 123456
atl conf page get     --id 123456 --format csf
atl conf page meta    --id 123456  # если restricted отсутствует, состояние неизвестно
atl conf page history --id 123456
# Guarded-обновление title: значение берётся из файла/stdin, а не argv
atl conf page title set 123456 --from-file title.txt
# Затем --apply с --expected-version и --expected-proposal-hash из preview
# Типизированные read-only метаданные страницы (см. docs/usage.md)
atl config set render.confluence.include page_fields
atl config set render.confluence.page_fields '[{"id":"title"},{"id":"updated","format":"date"}]'
# View v2 явно разделяет # Metadata / # Content / # Comments; нативное
# форматирование комментариев и target ссылок на страницы сохраняются.
atl conf table extract --id 123456 --format json
atl conf table extract --id 123456 --table 2 --format csv # формулы нейтрализуются по умолчанию
atl conf table extract --id 123456 --table 2 --format csv --raw-csv # небезопасно открывать в таблицах
atl conf table extract --id 123456 --format xlsx --out tables.xlsx
atl conf page create  --space DOCS --parent 123456 --title "My Page" --from-file body.csf
atl conf page create  --space DOCS --title "From markdown" --from-md body.md
# Guarded-перенос: сначала preview, затем apply с полученными source-state gates
atl conf page move    123456 --parent 654321
atl conf page delete  --id 123456
atl conf comment list --id 123456
atl conf comment add  --id 123456 --from-file comment.csf
```

---

## Модель редактирования и защитные механизмы

Байты `.csf` являются **субстратом** — то, что вы записываете, то и публикуется.
Нет потерь при Markdown-конвертации: макросы, панели, шаблоны и диаграммы никогда не
теряются молча.

| Защита | Поведение |
|--------|-----------|
| `atl conf validate` | Блокирует при невалидном XML (с указанием строки/колонки); предупреждает о структурных проблемах |
| `atl conf push --dry-run` | Показывает все последствия без записи |
| Version gate | `push` завершается с кодом **5**, если удалённая версия опередила последнюю синхронизацию |
| `--force` | Обходит version gate; безопасное восстановление — re-pull + reapply |
| Диаграммы | draw.io-макросы разрешаются в PNG нужной ревизии для визуального осмотра |

---

## Jira

```sh
# Чтение
atl jira issue get  PROJ-1
atl jira issue view PROJ-1 -o text   # настроенный Markdown без записи файлов
atl jira issue search --jql 'project = PROJ AND status = "In Progress"' --columns key,summary,status,assignee
atl jira issue children PROJ-100 --columns key,summary,status,assignee
atl jira issue attachment list PROJ-1
atl jira issue attachment get PROJ-1 --id spec.xlsx --into ./attachments

# Зеркалировать набор задач на диск (добавьте --assets, чтобы также зеркалировать вложения-изображения)
atl jira pull --jql 'project = PROJ' --into mirror-jira
atl jira pull --jql 'project = PROJ AND status = Open' --assets
# Выберите объём .md-представления: minimal | default | full (см. docs/usage.md)
atl jira pull --jql 'project = PROJ' --render-profile full
atl jira render mirror-jira --render-profile default   # перерендер офлайн, без повторного pull
# Типизированные custom fields (читаемые metadata/date/list) и проверяемые задачи эпика
# настраиваются для зеркала; см. docs/usage.md

# Запись
atl jira issue attachment upload PROJ-1 --file ./spec.xlsx
atl jira issue assign PROJ-1 --me
atl jira issue comment add PROJ-1 --from-md note.md
atl jira issue edit PROJ-1 --old 'timeout = 300' --new 'timeout = 600'
atl jira issue field set PROJ-1 --from-md customfield_10001=notes.md --allow-fields customfield_10001   # dry-run
atl jira issue transition PROJ-1 --to Done
# До правки перерендерьте представление без актуальной версии в первой строке
atl jira render mirror-jira
# Правка поддерживаемых generated-разделов, подготовка через apply, затем push
atl jira apply mirror-jira/PROJ/PROJ-1.md --dry-run

# Метаданные
atl jira fields
atl jira transitions --key PROJ-1
atl jira link-types
atl jira field-options --project PROJ --field <field-id>
```

---

## Соглашения и коды выхода

- JSON в **stdout** по умолчанию; `-o text` для человекочитаемого вывода.
- Логи и ошибки в **stderr** — при сбое по умолчанию JSON `{"error": "...", "code": N}`
  (или строка `error: <msg>` при `-o text`).
- Тела запросов передаются через `--from-file <path>` или `--from-file -` (stdin, лимит 64 MiB;
  больший ввод отклоняется с ошибкой, а не усекается).
- Никаких интерактивных запросов.
- Confluence pull/render/apply/push и локальный `conf edit` сериализуются для каждого mirror; при
  конфликте дождитесь активной операции и не удаляйте постоянный lock в `.atl`.
- Если re-pull меняет путь отслеживаемой страницы Confluence, локальные правки
  или коллизия блокируют перенос; после записи нового пути удаляются только
  старые основные файлы страницы, без рекурсивного удаления дочерних каталогов.
  Если все три старых основных файла были удалены намеренно, pull исправляет
  устаревший путь; частичное удаление остаётся ошибкой согласования с кодом 8.
- Обновления общего `state.json` из Jira и Confluence объединяются под одним
  нейтральным lock; краткий конфликт повторяется в ограниченном окне, затем
  операция завершается безопасно, не теряя записи.

| Код | Значение |
|-----|----------|
| 0 | Успех |
| 1 | Общая ошибка |
| 2 | Неверное использование / неверные аргументы (в т.ч. небезопасный non-https URL) |
| 3 | Ошибка аутентификации — PAT **был** передан, но сервер его отклонил |
| 4 | Не найдено |
| 5 | Конфликт версий (оптимистичная блокировка) |
| 6 | Доступ запрещён (у токена нет прав) |
| 7 | Не настроено — базовый URL или PAT **ещё не заданы** |
| 8 | Проверка не пройдена — `jira issue check` нашёл пустые обязательные поля |

`7` против `3`: `7` означает «завершите настройку» (нет URL/токена); `3` — «замените токен» (он был
отклонён). Паттерны для скриптов и CI (конфигурация только через env, отключение самообновления,
изоляция учётных данных, обработка лимита страниц `--cql`) — в
[docs/usage.md → Scripting & CI](docs/usage.md#scripting--ci).

---

## Решение проблем

| Симптом | Вероятная причина и решение |
|---------|------------------------------|
| `command not found: atl` после установки | `~/.local/bin` (или `$(go env GOBIN)`) не в `PATH` — добавьте в профиль шелла и переоткройте терминал. |
| Код **7** / «URL not set» / «no PAT found» | Настройка не завершена — выполните `atl config set --confluence-url …` и `atl auth login --service …` (или задайте `ATL_*_URL` / `ATL_*_PAT`). |
| Код **3** на каждый вызов | PAT отклонён (истёк/отозван или принадлежит другому инстансу) — создайте новый токен и заново `auth login`. |
| «refusing to send the PAT over http…» | Базовый URL non-https на non-loopback хосте. Используйте `https` или `export ATL_ALLOW_INSECURE=1` для доверенного внутреннего http-инстанса. |
| Код **5** при push | Удалённая страница изменилась с момента последнего pull (ожидаемо) — сделайте re-pull, заново примените правку и снова push; `--force` — только после решения человека. |
| Pull по `--cql` будто теряет страницы | Лимит 1000 (`"truncated": true` + `warning:` в stderr). Сузьте CQL или используйте `--space`. |
| Для прямого REST-запроса нужен PAT | Не кладите токен в argv/логи; используйте env-переменные и передавайте curl header через stdin (см. `docs/usage.md`). |
| Structure API сообщает, что forest spec/body отсутствует | Проверьте, что request body реально отправлен как файл или stdin payload; избегайте shell-расширений, которые дают пустой body. |
| Cloud (`*.atlassian.net`) не аутентифицируется | Не поддерживается — `atl` использует bearer-PAT Server/Data Center, а не Cloud API-токены. |

---

## Безопасность и самообновление

Бинарник проверяет наличие новых релизов не чаще одного раза в 6 часов. Каждое
обновление верифицируется SHA-256 контрольной суммой **и** ed25519-подписью по
публичному ключу, скомпилированному в бинарник. Если релиз не подписан или подпись
не проходит проверку — обновление не применяется.

- Отключить автообновление: `ATL_NO_UPDATE=1`
- Dev-сборки никогда не обновляются автоматически.
- Полная модель доверия: [docs/self-update.md](docs/self-update.md)
- Политика безопасности: [SECURITY.md](SECURITY.md)

---

## Сборка и архитектура

```sh
make build   # собирает ./atl
make test    # go test ./...
make lint    # golangci-lint run
# или напрямую:
go build ./...
go test  ./...
```

Кодовая база следует **гексагональной архитектуре (ports & adapters)**:

| Пакет | Роль |
|-------|------|
| `internal/domain` | Порты (интерфейсы) + модель `Resource` |
| `internal/adapter/confluence` | REST-адаптер Confluence |
| `internal/adapter/jira` | REST-адаптер Jira |
| `internal/csf` | Парсер/сериализатор Confluence Storage Format |
| `internal/fragment` | Реестр фрагментов |
| `internal/mirror` | Структура зеркала, sidecar, синхронизация |
| `internal/app` | Transport-agnostic use-cases |
| `internal/cli` | Тонкий слой команд Cobra |

Дополнительно: [docs/architecture.md](docs/architecture.md) · [docs/usage.md](docs/usage.md) ·
[docs/self-update.md](docs/self-update.md)

---

## Участие в разработке

Руководство по участию, соглашения и инструкции по созданию pull request — в файле
[CONTRIBUTING.md](CONTRIBUTING.md).

---

## Лицензия

Apache License 2.0 — см. [LICENSE](LICENSE).  
Уведомления о сторонних компонентах: [NOTICE](NOTICE).

---

## Товарные знаки и отказ от ответственности

Этот проект является **независимым инструментом с открытым исходным кодом** и **НЕ**
связан с **Atlassian Pty Ltd**, не одобрен и не спонсирован ею.

«Atlassian», «Confluence» и «Jira» являются зарегистрированными товарными знаками
Atlassian Pty Ltd. Эти названия используются здесь исключительно в **номинативном,
описательном смысле** — для идентификации программных продуктов, с которыми
взаимодействует `atl`, — и не подразумевают никакой связи с Atlassian или её одобрения.

Использование данного программного обеспечения регулируется [лицензией Apache 2.0](LICENSE).
Авторы и участники проекта не предоставляют никаких гарантий. Уведомления о сторонних
компонентах — в файле [NOTICE](NOTICE).
