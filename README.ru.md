[English](README.md) · **Русский**

# atl

[![Go](https://img.shields.io/badge/go-1.26-blue?logo=go)](https://go.dev)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)
[![Main smoke](https://img.shields.io/github/actions/workflow/status/isukharev/atl/ci.yml?branch=main&label=main%20smoke)](https://github.com/isukharev/atl/actions/workflows/ci.yml)

[Документация](docs/README.md) · [Совместимость](docs/compatibility.md) ·
[Roadmap](ROADMAP.md) · [История версий](CHANGELOG.md) · [Участие](CONTRIBUTING.md) ·
[Безопасность](SECURITY.md)

**Локальные workflow без потерь для Jira и Confluence Server/Data Center.**

`atl` позволяет людям и кодинг-агентам просматривать, зеркалировать, сравнивать
и обновлять Atlassian-контент обычными локальными инструментами. Байты
Confluence `.csf` и Jira `.wiki` остаются основой записи; Markdown — удобное
представление для чтения и подготовки правок, а не замена с потерями. Удалённые
изменения проходят явные проверки версии, baseline или proposal и не затирают
параллельную работу молча.

```sh
export ATL_READ_ONLY=1
atl jira issue search --jql 'order by updated DESC' --limit 5
atl conf search --cql 'type = page' --limit 5
```

Pull записывает файлы локального зеркала, но не меняет Jira или Confluence.
Сохраняйте read-only policy, пока не проверено одно точное предложение записи.

> `atl` — независимый open-source проект. Он не связан с Atlassian Pty Ltd, не
> одобрен и не спонсирован ею.

## Начните со своей задачи

| Цель | Краткое руководство |
|---|---|
| Установить и настроить доверие к private PKI | [Настройка за пять минут](docs/getting-started.md) |
| Дать кодинг-агенту безопасный доступ | [Настройка агента](docs/agent-setup.md) |
| Безопасно зеркалировать, править и публиковать | [Безопасная запись](docs/safe-writes.md) |
| Обновить или восстановить существующее зеркало | [Зеркала и восстановление](docs/mirrors-and-recovery.md) |
| Сравнить квалифицированные поколения | [Запечатанные поколения корпуса](docs/corpus-generations.md) |
| Собрать приватный корпус в disposable-контейнере | [Corpus dev-container](docs/corpus-devcontainer.md) |
| Найти проекты, схему создания и [связи Jira](docs/jira-artifact-graph.md) | [Команды Jira](docs/reference/cli/README.md) |
| Прочитать или изменить обсуждения Confluence | [Квалифицированные комментарии](docs/confluence-comments.md) |
| Увидеть основные гарантии без credentials | [Воспроизводимые демо](docs/demos/README.md) |
| Разобраться с setup, доступом или конфликтами | [Устранение неполадок](docs/troubleshooting.md) |

[Индекс документации](docs/README.md) ведёт к отдельным workflow.
[Справочник команд](docs/reference/cli/README.md) и
[контракт вывода](docs/reference/output/README.md) описывают точные флаги и поля wire-формата.
Подробная документация поддерживается на английском; русский README остаётся
равноправной входной страницей и ведёт к тем же каноническим руководствам.

## Установка

Release-бинарники для Linux и macOS доступны на amd64 и arm64.

```sh
curl -fsSL https://github.com/isukharev/atl/releases/latest/download/install.sh | sh
```

Установщик проверяет SHA-256. Релизы также публикуют checksums, подписи и SLSA
provenance. Альтернативы:

```sh
brew install isukharev/tap/atl
```

Прямые загрузки находятся в [GitHub Releases](https://github.com/isukharev/atl/releases).
Разработчикам следует клонировать репозиторий и использовать `make install` —
он проставляет версию репозитория и build identity.
Windows и Atlassian Cloud пока не поддерживаются; перед развёртыванием проверьте
[матрицу совместимости](docs/compatibility.md).

## Первое чтение за пять минут

Настройте только нужный сервис. В примере используется Jira; для Confluence
замените флаги и имя сервиса.

```sh
atl config set --jira-url https://jira.example.com
atl auth login --service jira
atl auth status
atl doctor --service jira --remote

export ATL_READ_ONLY=1
atl jira issue search --jql 'order by updated DESC' --limit 5
```

`auth login` читает bearer PAT из скрытого prompt, stdin или файла — никогда из
argv. Без явного `--remote` команда `doctor` работает offline; `--service
jira|confluence` ограничивает проверку здоровья одним backend. Remote-режим
выполняет ограниченные проверки продукта и версии, не читая body страниц или
задач. Privacy-safe результат `safety` явно показывает настроенное и эффективное
read-only состояние, а также точный источник
`flag|environment|configuration|none`. По умолчанию результат выводится как
JSON; логи и ошибки остаются в stderr.

Для Confluence:

```sh
atl config set --confluence-url https://confluence.example.com
atl auth login --service confluence
atl doctor --service confluence --remote
export ATL_READ_ONLY=1
atl conf search --cql 'type = page' --limit 5
```

Считайте отсутствие доказанным только при `complete:true`; продолжайте
`next_cursor` и не доверяйте полной странице без квалификации.

## Три рабочих цикла

### 1. Читайте узко

Начинайте с поиска по CQL/JQL, затем читайте только выбранный объект или поля.
Используйте `atl jira issue graph KEY --depth 0` для структурированных связей,
иерархии, документации, вложений или Development identities; добавьте
`--projection compact` для квалифицированного URL/SCM JSON. Если исходная точка
— один GitLab-проект или страница Confluence, используйте CLI-only команду
`atl jira issue reference search` с явными JQL scope, набором источников,
режимом и лимитами; отсутствие доказывает только полный exhaustive-результат.
Перед раскрытием одного точного thread выполните `atl conf comment list --id
ID`. Эти поверхности явно квалифицируют неполные данные; в колонке `URL` text
output графа отображаются только безопасные идентификаторы URL-узлов.

Typed MCP предлагает агентам уменьшенные read-only представления. CLI остаётся
маршрутом для native body, долговременных зеркал, крупных ограниченных обходов,
экспорта и любой записи.

### 2. Зеркалируйте и проверяйте локально

Храните зеркало вне source repository и передавайте его root явно:

```sh
export ATL_READ_ONLY=1
export ATL_WORKSPACE_ROOT=/absolute/path/to/atl-workspace

atl conf pull --id 123456 --into "$ATL_WORKSPACE_ROOT"
atl conf status --into "$ATL_WORKSPACE_ROOT"
atl conf diff "$ATL_WORKSPACE_ROOT" -o text
```

Файл `.csf` содержит точный native body Confluence. Соседний `.md` — производное
представление для чтения и поддерживаемых staging-правок. После изменения
Markdown:

```sh
env -u ATL_READ_ONLY atl conf apply \
  "$ATL_WORKSPACE_ROOT/SPACE/page/page.md" --dry-run
env -u ATL_READ_ONLY atl conf apply \
  "$ATL_WORKSPACE_ROOT/SPACE/page/page.md"
atl conf validate "$ATL_WORKSPACE_ROOT/SPACE/page/page.csf"
atl conf diff "$ATL_WORKSPACE_ROOT/SPACE/page/page.csf" -o text
```

Нетронутые native-блоки остаются побайтно идентичными. Неподдерживаемые
Markdown-правки, потеря фрагментов, некорректный CSF или изменившийся baseline
отклоняются до публикации. `conf apply` меняет локальные native-байты, поэтому
классифицируется как mutation даже с `--dry-run`; scoped `env -u` сохраняет
policy в текущей shell-сессии. Pull не перезаписывает правки native-файла или
derived view: используйте его dry-run, stash или явное overwrite-восстановление,
чтобы не потерять работу. Долговременное зеркало также связано с
content-minimized identity backend, поэтому зеркало staging нельзя случайно
отправить в другой настроенный instance.

Jira использует нативные `.wiki`-файлы; обычный цикл и qualified resumable pull,
apply, reconcile и push описаны в разделе [Jira mirrors](docs/reference/cli/jira-mirrors.md).

### 3. Выполните preview, примените один раз и сверьте результат

Цикл записи: свежее чтение → candidate → diff/preview → проверенные
version/baseline/hash → один apply → reconciliation. Preview push тоже
классифицируется как mutation, поэтому снимайте read-only policy только для
этого процесса, сохраняя её в shell:

```sh
env -u ATL_READ_ONLY atl conf push \
  "$ATL_WORKSPACE_ROOT/SPACE/page/page.csf" --dry-run
```

Проверив полный результат, запустите ту же команду без `--dry-run`. Version
conflict Confluence завершается с кодом `5`: сохраните локальный candidate и
используйте `conf reconcile preview`, не включая force автоматически.
Proposal-bound workflow для комментариев, create/copy, корзины, полей Jira,
transition и удаления требуют выведенных expected-значений и никогда не
повторяют неоднозначную запись. Точные команды apply и восстановления приведены
в [руководстве по безопасной записи](docs/safe-writes.md).
Для перемещения страницы Confluence в корзину `--id` должен быть каноническим
положительным числом: alias, URL, знак, ведущие нули и окружающие пробелы
отклоняются до чтения конфигурации или обращения к backend.

## Кодинг-агенты

Плагины Claude Code и Codex включают typed read-only MCP.

Перед выбором маршрута проверьте статическую границу эффектов команды:

```sh
atl capabilities --effects
atl capabilities --effects --command "jira issue search"
```

Этот каталог работает offline и не читает credentials. Профили задают только
информационные верхние границы, а не разрешение или enforcement выполнения.

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

После установки начните новую сессию. ATL поддерживает MCP `2026-07-28` и
`2025-11-25`. [Руководство по настройке агента](docs/agent-setup.md) описывает
safety, зеркала, version skew, startup gates и modern opt-in Codex;
отдельный `atl mcp serve` поддерживается.

[`agent-eval`](docs/reference/agent-eval/README.md) находится в pre-release.

## Безопасность и совместимость

- `ATL_READ_ONLY=1` / `--read-only` блокирует remote mutations до чтения
  credentials, body-файлов, self-update или обращения к сети.
- Необязательная [scoped write policy](docs/reference/cli/policy.md) ограничивает
  записи Jira и Confluence по verb и канонической identity контента. Перед
  планированием записи запускайте `atl policy show`.
- PAT привязаны к host; cross-host redirect и downgrade с HTTPS запрещены.
  Mutating requests никогда не следуют redirect и не используют generic retry.
- Стабильные коды выхода различают usage, authentication, not-found, version
  conflict, forbidden, configuration и failed safety checks.
- Reads ограничены и явно отмечают incomplete или truncated evidence.
- Подписанный self-update проверяет manifest и бинарник до замены; его можно
  отключить через `ATL_NO_UPDATE=1`.

`atl` предназначен для Jira и Confluence Server/Data Center с bearer PAT. См.
[совместимость](docs/compatibility.md), [network egress](docs/network-egress.md),
[доверие к self-update](docs/self-update.md) и [SECURITY.md](SECURITY.md).

## Документация и участие

- [Индекс документации по задачам](docs/README.md)
- [Готовые рецепты для агентов](docs/agent-recipes.md)
- [Native storage и фрагменты Confluence](docs/csf-and-fragments.md)
- [Typed read-only MCP](docs/mcp.md)
- [Scoped write policy](docs/reference/cli/policy.md)
- [Архитектура](docs/architecture.md)

Вопросы и обезличенные отчёты о совместимости отправляйте через
[GitHub Issues](https://github.com/isukharev/atl/issues/new/choose). Никогда не
публикуйте credentials, private hosts, object identifiers, titles/content, user
identity, company data или private local paths. Уязвимости безопасности
отправляйте по правилам [SECURITY.md](SECURITY.md).

```sh
make build
make test
make lint
```

Код следует hexagonal ports-and-adapters architecture. См.
[CONTRIBUTING.md](CONTRIBUTING.md). Apache License 2.0 — [LICENSE](LICENSE).
Сторонние уведомления и оговорка о товарных знаках Atlassian находятся в
[NOTICE](NOTICE).
