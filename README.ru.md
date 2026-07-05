[English](README.md) · **Русский**

# atl

[![Go](https://img.shields.io/badge/go-1.26-blue?logo=go)](https://go.dev)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/isukharev/atl/ci.yml?branch=main&label=CI)](https://github.com/isukharev/atl/actions/workflows/ci.yml)

**Git-style CLI для Confluence и Jira — создан для кодинг-агентов.**

`atl` позволяет кодинг-агенту (например, Claude Code) работать с Confluence и Jira
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
- **Интеграция с Jira** — запросы, комментарии, переходы статусов; зеркалирование задач на диск.
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

От нуля до первого результата напрямую через CLI (для Claude Code — см. следующий раздел):

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

## Использование с Claude Code

`atl` поставляет плагин для [Claude Code](https://claude.com/claude-code) (этот репозиторий
одновременно является и marketplace плагинов), так что агент может сам установить CLI и работать
с ним за вас. Добавьте marketplace и установите плагин:

```
/plugin marketplace add isukharev/atl
/plugin install atl@atl
/atl:setup
```

`/atl:setup` устанавливает бинарник `atl`, если его нет, настраивает аутентификацию и базовые URL
Confluence/Jira и согласовывает локальную директорию зеркала. После этого Claude Code автоматически
использует встроенные скиллы по мере необходимости:

- **`atl`** — ориентация: когда использовать `atl` (а когда live Atlassian MCP), workflow
  «сначала поиск» и где живёт зеркало.
- **`confluence`** — pull, правка `.csf`, валидация и публикация страниц под version gate.
- **`jira`** — поиск/выгрузка задач и create/update/transition/comment/link через команды.

Скиллы лежат в [`skills/`](skills/) и описаны в [`.claude-plugin/`](.claude-plugin/); их можно
проверить локально командой `claude plugin validate .`.

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
      acme-adr.md               # read-only представление: текст + ⟦фрагменты⟧ + ![](assets/…)
      acme-adr.meta.json        # id, версия, хэш содержимого, разрешённые фрагменты
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
# Проще всего: правьте markdown-представление и слейте правки в .csf поблочно.
# Нетронутые блоки сохраняют байты в точности; неконвертируемые правки отклоняются.
$EDITOR mirror/DOCS/acme-adr/acme-adr.md
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
atl conf page get     --id 123456
atl conf page get     --id 123456 --format csf
atl conf page meta    --id 123456
atl conf page history --id 123456
atl conf page create  --space DOCS --parent 123456 --title "My Page" --from-file body.csf
atl conf page create  --space DOCS --title "From markdown" --from-md body.md
atl conf page move    --id 123456 --parent 654321
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
atl jira issue search --jql 'project = PROJ AND status = "In Progress"'

# Зеркалировать набор задач на диск
atl jira pull --jql 'project = PROJ' --into mirror-jira

# Запись
atl jira issue assign PROJ-1 --me
atl jira issue comment add PROJ-1 --from-file note.txt
atl jira issue transition PROJ-1 --to Done

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
