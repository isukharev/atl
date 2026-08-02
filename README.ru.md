[English](README.md) · **Русский**

# atl

[![Go](https://img.shields.io/badge/go-1.26-blue?logo=go)](https://go.dev)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)
[![Main smoke](https://img.shields.io/github/actions/workflow/status/isukharev/atl/ci.yml?branch=main&label=main%20smoke)](https://github.com/isukharev/atl/actions/workflows/ci.yml)

[Документация](docs/README.md) · [Совместимость](docs/compatibility.md) ·
[Roadmap](ROADMAP.md) · [Участие](CONTRIBUTING.md) ·
[Безопасность](SECURITY.md)

**Lossless local-first workflows для Jira и Confluence Server/Data Center.**

`atl` позволяет людям и кодинг-агентам читать, зеркалировать, сравнивать и
обновлять Atlassian-контент обычными локальными инструментами. Байты
Confluence `.csf` и Jira `.wiki` остаются write-substrate; Markdown — только
производный staging-view. Удалённые изменения проходят явные version,
baseline или proposal gates и не затирают параллельные правки молча.

```sh
export ATL_READ_ONLY=1
atl conf search --cql 'type = page' --limit 1
atl conf pull --id 123456 --into "$HOME/.atl/example-workspace"
atl conf diff "$HOME/.atl/example-workspace" -o text
```

Pull создаёт локальные файлы, но не меняет Jira или Confluence. Снимайте
read-only policy только после проверки конкретного предложения записи.

> `atl` — независимый open-source проект. Он не связан с Atlassian Pty Ltd, не
> одобрен и не спонсирован ею.

## Начните с задачи

| Цель | Руководство | Результат |
|---|---|---|
| Установить и проверить один backend | [Первое чтение](#первое-чтение) | Первое ограниченное чтение и локальное зеркало |
| Дать кодинг-агенту безопасный доступ | [Кодинг-агенты](#кодинг-агенты) | Узкие skills и типизированный read-only MCP |
| Зеркалировать, править и публиковать | [Три основных workflow](#три-основных-workflow) | Native local diff и одна guarded-запись |
| Проверить подходящую среду | [Совместимость](#совместимость) | Supported, unverified и unsupported границы |
| Разобраться с ошибкой | [Быстрое восстановление](#быстрое-восстановление) | Действие по стабильному коду выхода |

Полный [справочник команд](docs/usage.md) и
[контракт вывода](docs/OUTPUT_CONTRACT.md) сохранены, но для первого успешного
workflow читать их целиком не нужно.

Для одной точной Jira-задачи `atl jira issue graph PROJ-123` возвращает
квалифицированный по источникам прямой schema-v2 граф рабочих
артефактов, не переходя к
найденным задачам, страницам или URL. `--depth 1..3` переходит
только по точным структурированным Jira-связям под жёсткими лимитами запросов и
вывода; `--resolve confluence` читает только метаданные id/title страницы.
Явный флаг `--include-development` добавляет минимизированные координаты GitLab:
проект, полный commit SHA, точную ветку и номер merge request — из
экспериментального Jira Development API. ATL при этом не обращается к GitLab и
не загружает найденные URL; неполный источник Development не возвращает ни
одного Development-факта.
Каждое возвращённое поле сверяется с метаданными до обхода: отсутствие или
некорректность метаданных делает именованный источник неполным, а свойства
задачи явно помечены как экспериментальный источник.
Типизированный MCP-инструмент `jira_issue_graph` возвращает тот же schema-v2
граф с обходом только Jira: он не принимает параметр разрешения Confluence,
оставляет страницы квалифицированными заглушками и отделяет фиксированный лимит
ответов backend от настраиваемого лимита закодированного результата. Если
`include_development` не указан или равен `false`, источник Development
по-прежнему отсутствует. Явное значение `true` добавляет только закрытый набор
экспериментальных SCM-координат без URL Development-узлов. ATL не обращается к GitLab;
последующее чтение допустимо, только если возвращённый lowercase hostname точно
совпадает с заранее одобренным hostname, и выполняется отдельно
аутентифицированным read-only клиентом без повторного использования Jira
credentials.

Для обсуждений Confluence команда `atl conf comment list --id 123456`
возвращает квалифицированный schema-v2 inventory footer-, inline- и resolved-
комментариев с доказанными связями треда, независимыми измерениями полноты и
точным сопоставлением anchors с native CSF. `conf comment thread` выбирает один
точный корневой тред и ограничивает diagnostics/completeness этим тредом;
недостающие ancestry или markers остаются явным partial evidence. Явный
backend-статус `reopened` нормализуется в семантический `open`, а неизвестные
статусы остаются partial. Каждый
qualified backend read, включая pagination, внутренне привязан к согласованной
ревизии страницы.
`conf pull --comments` сохраняет эту квалификацию в versioned sidecar зеркала;
основной `.md` страницы показывает детерминированное read-only дерево с явными
location/state, полнотой, безопасно квалифицированными anchors и непривязанными
записями. Schema-v2 `.comments.json` остаётся исходным evidence, включая
закрытые diagnostics, а `.comments.md` — плоским compatibility view; старые
плоские sidecar читаются, и comments не влияют на drift страницы.
Offline capability route `confluence/comments` разделяет list, точный thread,
preview и add. MCP предоставляет только первые два как ограниченные read-only
tools: body-free `confluence_comment_list` для discovery и
`confluence_comment_thread` для одного точного plain-text expansion. Partial
результат не доказывает отсутствие, а preview/add остаются guarded CLI-only.

## Установка

Статические release-бинарники для Linux и macOS доступны на amd64 и arm64.

```sh
curl -fsSL https://github.com/isukharev/atl/releases/latest/download/install.sh | sh
```

Установщик проверяет SHA-256. Релизы также публикуют checksums, подписи и SLSA
provenance.

Homebrew:

```sh
brew install isukharev/tap/atl
```

Из исходников (Go 1.26.5+):

```sh
go install github.com/isukharev/atl/cmd/atl@latest
```

Прямые загрузки находятся в [GitHub Releases](https://github.com/isukharev/atl/releases).
Windows пока не поддерживается; полное platform/backend evidence находится в
[compatibility.md](docs/compatibility.md).

## Первое чтение

Настройте только нужный сервис:

```sh
atl config set --confluence-url https://confluence.example.com
# или:
atl config set --jira-url https://jira.example.com

atl auth login --service confluence
# или:
atl auth login --service jira

atl auth status
atl doctor
```

`auth login` читает bearer PAT из скрытого prompt, stdin или файла — никогда из
argv. `auth status` показывает только источник credential. `doctor` проверяет
build, права config-файлов, URL policy, наличие credentials и необязательное
локальное зеркало, не выводя URL, hostname, пути, identity, token или content.

Затем выполните одно ограниченное чтение:

```sh
export ATL_READ_ONLY=1

atl doctor --remote
atl conf search --cql 'type = page' --limit 1
# или:
atl jira issue search --jql 'order by updated DESC' --limit 1
```

Без `--remote` команда полностью offline. Remote-режим делает по одному
single-attempt product/version GET на готовый backend; только при отсутствии
version route Confluence он может добавить один bodyless reachability HEAD. Он
не читает body страниц/задач, результаты поиска или identity. Доступность без
версии отмечается как unverified compatibility. При blocking findings `doctor`
всё равно выводит полный отчёт и завершает работу с кодом `8`. Продолжение —
[пятиминутное руководство](docs/getting-started.md).

Экспериментальные compatibility providers для Data Center включаются отдельно
и никогда не выбираются по диапазону версий:

```sh
atl compatibility status
atl compatibility pin confluence \
  --version "$ATL_CONFLUENCE_VERSION" \
  --build-number "$ATL_CONFLUENCE_BUILD_NUMBER"
atl compatibility status --remote
```

Owner-only pin хранится отдельно от обычного `config.json` и не позволяет
задавать произвольные endpoints, headers, payloads или fallback REST route.

## Три основных workflow

### 1. Узкое чтение

Начинайте с CQL/JQL discovery, затем читайте только выбранный объект или поля.
До утверждения об отсутствии проверяйте completeness и truncation.

```sh
export ATL_READ_ONLY=1
atl jira issue search \
  --jql 'assignee = currentUser() order by updated DESC' \
  --limit 20
atl conf search --cql 'type = page' --limit 20
```

### 2. Зеркало и diff

Храните зеркало вне source repository:

```sh
export ATL_READ_ONLY=1
export ATL_MIRROR_ROOT="$HOME/.atl/example-workspace"

atl conf pull --id 123456
atl conf status "$ATL_MIRROR_ROOT"
atl conf diff "$ATL_MIRROR_ROOT" -o text

# Маршрут Jira:
atl jira pull --jql 'project = EXAMPLE order by key' --limit 20
atl jira status "$ATL_MIRROR_ROOT"
```

Используйте `.md` для чтения и поддерживаемых staging-правок. Нативные `.csf` /
`.wiki` сохраняют конструкции, которые Markdown не может представить. В
Confluence-view v6 используются безопасные code fence, обратимое экранирование
параграфов, явные inline-разрывы и table merge с сохранением структуры;
непредставимая нативная форма отклоняется до изменения `.csf`.

Pull никогда молча не перезаписывает локальные правки нативного файла или
derived view. Он обновляет чистые соседние объекты, сообщает блокировки через
content-free `local_safety` и завершается с кодом `8`. Для проверки используйте
`pull --dry-run`; перед намеренным сбросом сохраните точные нативные байты через
`--stash-local` либо явно отбросьте их через `--overwrite-local`. Эти флаги не
обходят правки Markdown и нарушения baseline.

### 3. Проверяемая запись

Write-loop: свежее чтение → candidate → diff/preview → проверенные
version/baseline/hash → один apply → reconciliation.

```sh
atl conf apply "$ATL_MIRROR_ROOT/SPACE/page/page.md"
atl conf validate "$ATL_MIRROR_ROOT/SPACE/page/page.csf"
atl conf diff "$ATL_MIRROR_ROOT/SPACE/page/page.csf" -o text
atl conf push "$ATL_MIRROR_ROOT/SPACE/page/page.csf" --dry-run
```

После проверки повторите точную guarded-команду без `--dry-run`. Confluence
version conflict даёт код `5`: сохраните и повторно примените локальный
candidate (проверенный `pull --stash-local` сохранит точные нативные байты), не
включая `--force` автоматически. Для нового комментария Confluence сначала используйте
read-only команду `conf comment preview`, затем повторите точное native-CSF body
через `conf comment add --apply --expected-proposal-hash ...`. Команда `add` по
умолчанию выполняет dry-run, но остаётся mutating-classified; она создаёт только
footer root и не повторяет неоднозначный POST. Для существующих inline threads
есть exact-pinned цикл `conf comment mutation preview|apply` для reply,
resolve и reopen. Тот же цикл создаёт новый server-owned inline anchor по
точному тексту из файла и номеру вхождения; ATL получает геометрию из
версионированного серверного DOM и сам не меняет marker CSF.
Jira-команды записи используют
тот же принцип проверенного baseline. Подробнее —
[safe-write guide](docs/safe-writes.md).

## Кодинг-агенты

Репозиторий поставляет одинаковые focused skills для Claude Code и Codex, а
также типизированный read-only MCP server. CLI остаётся маршрутом для durable
mirrors, export, raw Structure data и любой записи.

Claude Code:

```text
/plugin marketplace add isukharev/atl
/plugin install atl@atl
/atl:setup
```

Codex:

```sh
codex plugin marketplace add isukharev/atl
codex plugin add atl@atl
```

После установки начните новую сессию агента и вызовите явный setup skill.
[Agent setup](docs/agent-setup.md) описывает version skew, размещение зеркала,
read-only policy и выбор CLI/MCP.

## Совместимость

`atl` поддерживает Jira и Confluence Server/Data Center с bearer PAT. Runtime
Atlassian Cloud, Cloud OAuth и email/API-token auth не поддерживаются; HTTPS
Cloud URL при сохранении конфигурации специально не распознаётся и не
блокируется, поэтому корректный deployment type нужно проверить до setup.

Release targets: Linux и macOS на amd64/arm64. Linux amd64 и один hosted macOS
runner проходят runtime CI; arm64 artifacts cross-compile-ятся, но отдельной
hosted arm64 certification пока нет. Windows artifact отсутствует. Полная
матрица evidence и ограничения находятся в
[docs/compatibility.md](docs/compatibility.md).

## Быстрое восстановление

| Код | Что означает | Первое действие |
|---:|---|---|
| `2` | Неверная команда/flag/input | Запустите точный parent route с `--help` |
| `3` | Backend отклонил PAT | Обновите или заново введите token |
| `4` | Объект не найден | Проверьте selector и permission |
| `5` | Remote version conflict | Re-pull и reapply; не включайте force автоматически |
| `6` | Пользователь аутентифицирован, но доступ запрещён | Запросите минимальный permission |
| `7` | URL/PAT/config отсутствует или некорректен | Завершите или исправьте setup |
| `8` | Safety/check gate отказал | Следуйте structured recovery, не обходите gate |

Подробное руководство пока доступно на английском:
[docs/troubleshooting.md](docs/troubleshooting.md).

## Почему `atl`

Проект объединяет четыре контракта:

- lossless native local storage вместо Markdown-only write path;
- обычные offline search, diff, status и review workflows;
- optimistic/baseline-bound записи без слепых retry;
- bounded JSON/typed MCP evidence для automation и agents.

`atl` намеренно Server/Data Center и local-first. Atlassian CLI и Rovo MCP
обслуживают Atlassian Cloud, а community MCP servers ориентированы на широкую
live tool inventory. Выбирайте `atl`, когда важны нативные локальные байты,
offline diff и явные write gates. Ссылки на источники и сравнение без рейтинга —
в [compatibility.md](docs/compatibility.md#choosing-a-different-tool).

## Безопасность и вывод

- `ATL_READ_ONLY=1` / `--read-only` блокирует мутации до credentials,
  body-файлов, self-update и сети.
- PAT host-scoped; cross-host и HTTPS-downgrade redirects запрещены, а
  mutating requests никогда не следуют redirects.
- JSON идёт в stdout по умолчанию; логи/ошибки — в stderr.
- Стабильные коды выхода классифицируют usage, auth, not-found, version
  conflict, forbidden, config и safety failures.
- Reads ограничены и квалифицируют incomplete/truncated результаты.
- Generic retry применяется только к replay-safe reads, никогда к writes.
- Подписанный self-update имеет пятисекундный remote startup budget и
  отключается через `ATL_NO_UPDATE=1`.

Подробнее: [контракт вывода](docs/OUTPUT_CONTRACT.md),
[network egress](docs/network-egress.md),
[self-update trust](docs/self-update.md) и [SECURITY.md](SECURITY.md).

## Документация

- [Task-first index](docs/README.md)
- [Готовые agent recipes](docs/agent-recipes.md)
- [Полный command reference](docs/usage.md)
- [Confluence storage и fragments](docs/csf-and-fragments.md)
- [Typed read-only MCP](docs/mcp.md)
- [Архитектура](docs/architecture.md)

Вопросы, compatibility reports и обезличенные дефекты отправляйте через
[GitHub Issues](https://github.com/isukharev/atl/issues/new/choose). Никогда не
публикуйте credentials, private hosts, object IDs, titles/content, user
identity, company data или private local paths. Security vulnerabilities
следуют [SECURITY.md](SECURITY.md).

## Сборка и участие

```sh
make build
make test
make lint
```

Код следует hexagonal ports-and-adapters architecture. См.
[architecture.md](docs/architecture.md) и
[CONTRIBUTING.md](CONTRIBUTING.md).

Apache License 2.0 — [LICENSE](LICENSE). Сторонние уведомления:
[NOTICE](NOTICE).

«Atlassian», «Confluence» и «Jira» — зарегистрированные товарные знаки
Atlassian Pty Ltd и используются только для обозначения продуктов, с которыми
работает `atl`. Проект не даёт гарантий; см. [NOTICE](NOTICE).
