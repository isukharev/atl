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
| Установить и проверить один backend | [Getting started](docs/getting-started.md) | Первое ограниченное чтение и локальное зеркало |
| Дать кодинг-агенту безопасный доступ | [Agent setup](docs/agent-setup.md) | Узкие skills и типизированный read-only MCP |
| Зеркалировать, править и публиковать | [Safe writes](docs/safe-writes.md) | Native local diff и одна guarded-запись |
| Проверить подходящую среду | [Compatibility](docs/compatibility.md) | Supported, unverified и unsupported границы |
| Разобраться с ошибкой | [Troubleshooting](docs/troubleshooting.md) | Восстановление от кода выхода |

Полный [справочник команд](docs/usage.md) и
[контракт вывода](docs/OUTPUT_CONTRACT.md) сохранены, но для первого успешного
workflow читать их целиком не нужно.

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
```

`auth login` читает bearer PAT из скрытого prompt, stdin или файла — никогда из
argv. `auth status` показывает только источник credential.

Затем выполните одно ограниченное чтение:

```sh
export ATL_READ_ONLY=1

atl conf search --cql 'type = page' --limit 1
# или:
atl jira issue search --jql 'order by updated DESC' --limit 1
```

JSON — формат по умолчанию. Код `7` означает незавершённую/некорректную
конфигурацию; код `3` — backend отклонил PAT. Продолжение —
[пятиминутное руководство](docs/getting-started.md).

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
`.wiki` сохраняют конструкции, которые Markdown не может представить.

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
version conflict даёт код `5`: сделайте re-pull и reapply, не включайте
`--force` автоматически. Jira-команды записи аналогично привязываются к свежим
baseline/proposal hash и не повторяют неоднозначную запись. Подробнее —
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
- PAT host-scoped; cross-host и HTTPS-downgrade redirects запрещены.
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
