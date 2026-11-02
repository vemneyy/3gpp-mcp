# Обновление локального сервера до 0.2.0

Инструкция для Linux (Debian/Ubuntu) с systemd. Пути соответствуют файлам в
`examples/systemd`: бинарник `/usr/local/bin/3gpp-mcp`, база
`/var/lib/3gpp-mcp/3gpp.db`, пользователь и сервис `3gpp-mcp`.
Если у вас другие пути или имя сервиса, замените их в командах. Посмотреть
действующую конфигурацию: `systemctl cat 3gpp-mcp`.

## 1. Доставить код и собрать бинарник

Все изменения этой доработки, включая новые файлы, должны быть предварительно
доставлены на сервер. Команды Git ниже подходят, когда изменения уже
закоммичены и отправлены в `origin/work`. Изменения, оставшиеся только в рабочей
директории разработчика, через `git pull` на сервер не попадут.

В существующем чистом клоне проекта на сервере:

```bash
git switch work
git pull --ff-only origin work

# Нужен Go версии не ниже указанной в go.mod.
go version
make build
./bin/3gpp-mcp --version
# Ожидается: 3gpp-mcp 0.2.0
```

Обычная сборка, `go install` из этой ветки и Docker-сборка используют `0.2.0`.
В релизных сборках GoReleaser версия переопределяется Git-тегом через ldflags.
Команды `version`, `--version` и `-v` показывают фактическую версию бинарника.

## 2. Установить локальный анализатор дампов

```bash
sudo apt-get update
sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y tshark sqlite3
sudo -u 3gpp-mcp tshark --version
```

Разрешение на захват трафика обычным пользователям не требуется: сервер только
читает загруженные PCAP/PCAPNG. LibreOffice нужен, если вы используете
`--convert-doc` или `--convert-image`; для самого анализа дампов он не нужен.

## 3. Остановить сервис и сохранить резервную копию

Проверьте состояние таймера и сервиса обновления: если таймер был выключен,
оставьте его выключенным и после обновления.

```bash
systemctl is-active 3gpp-mcp-update.timer || true
sudo systemctl stop 3gpp-mcp-update.timer
sudo systemctl stop 3gpp-mcp-update.service
sudo systemctl stop 3gpp-mcp

sudo install -d -m 0700 /var/backups/3gpp-mcp/0.2.0
sudo cp -a /usr/local/bin/3gpp-mcp /var/backups/3gpp-mcp/0.2.0/3gpp-mcp.previous
sudo sqlite3 /var/lib/3gpp-mcp/3gpp.db ".backup '/var/backups/3gpp-mcp/0.2.0/3gpp.db'"
```

Если таймера/сервиса `3gpp-mcp-update` у вас нет, пропустите соответствующие
команды. Для повторной попытки обновления используйте другой каталог резервной
копии, чтобы сохранить первоначальную версию.

## 4. Установить бинарник и проверить systemd

```bash
sudo install -m 0755 ./bin/3gpp-mcp /usr/local/bin/3gpp-mcp.new
sudo mv /usr/local/bin/3gpp-mcp.new /usr/local/bin/3gpp-mcp
/usr/local/bin/3gpp-mcp --version
sudo install -d -o 3gpp-mcp -g 3gpp-mcp -m 0750 /var/lib/3gpp-mcp/cache
sudo systemctl edit 3gpp-mcp
```

В drop-in добавьте строки ниже. Сохраните ваши действующие параметры
`--addr`, `--web`, `--no-fetch` и остальные настройки, если они отличаются:

```ini
[Service]
ExecStart=
ExecStart=/usr/local/bin/3gpp-mcp serve --db /var/lib/3gpp-mcp/3gpp.db --transport http --addr :8080
Environment=XDG_CACHE_HOME=/var/lib/3gpp-mcp/cache
PrivateTmp=yes
```

`serve` обязателен. Bearer-токен из существующего `EnvironmentFile` сохранится:
не заменяйте файл `/etc/3gpp-mcp/env`. `PrivateTmp=yes` изолирует временные файлы
сервиса; анализатор удаляет свои файлы до возврата результата, в том числе при
ошибке или отмене. Для гарантированного отсутствия файлов после аварии хоста
используйте временную файловую систему для `TMPDIR`.

Если ваш update-unit не задаёт каталог кэша, также добавьте через
`sudo systemctl edit 3gpp-mcp-update.service`:

```ini
[Service]
Environment=XDG_CACHE_HOME=/var/lib/3gpp-mcp/cache
```

## 5. Обновить производные индексы и запустить сервис

Полностью скачивать спецификации заново для этой версии не требуется.
Общий поиск использует существующие таблицы текстов и OpenAPI. Пересборка
производных индексов добавляет недостающие индексы старой базе:

```bash
sudo -u 3gpp-mcp /usr/local/bin/3gpp-mcp build-openapi-index --db /var/lib/3gpp-mcp/3gpp.db
sudo -u 3gpp-mcp /usr/local/bin/3gpp-mcp build-asn1-index --db /var/lib/3gpp-mcp/3gpp.db
sudo systemctl daemon-reload
sudo systemctl start 3gpp-mcp
sudo systemctl status 3gpp-mcp --no-pager
curl --fail http://127.0.0.1:8080/health
sudo journalctl -u 3gpp-mcp -n 50 --no-pager
```

Проверить общий поиск без MCP-клиента:

```bash
sudo -u 3gpp-mcp /usr/local/bin/3gpp-mcp search-all --db /var/lib/3gpp-mcp/3gpp.db 'NAS OR NGAP'
```

Переподключите MCP-клиента, чтобы он получил новые инструменты `search_all` и
`analyze_dump`, а также обновлённую версию сервера. Старый параметр
`search.spec_id` заменён на `spec_ids: ["TS 23.501"]`.

Если таймер обновления был активен до обслуживания, включите его снова:

```bash
sudo systemctl start 3gpp-mcp-update.timer
```

## 6. Обновить сами спецификации при необходимости

Это отдельная операция; она обращается к архиву 3GPP. Запускайте её без
одновременного выполнения update-unit. Остановите таймер, дождитесь завершения
текущего `3gpp-mcp-update.service` и затем выполните:

```bash
sudo -u 3gpp-mcp env XDG_CACHE_HOME=/var/lib/3gpp-mcp/cache /usr/local/bin/3gpp-mcp update \
  --db /var/lib/3gpp-mcp/3gpp.db \
  --workers 8 --scrape-workers 8 --timeout 120s --convert-doc
sudo systemctl restart 3gpp-mcp
```

Если база собиралась с ограничением релиза, добавьте тот же
`--max-release`, например `--max-release 19`. После успешного обновления
возобновите ранее активный таймер. Операция использует рабочую копию БД;
перезапуск сервиса нужен, чтобы он открыл заменённый файл.

## Анализ дампов

MCP: `analyze_dump` принимает `content_base64`; HTTP:
`POST /analyze-dump` принимает сырые PCAP/PCAPNG под существующей bearer-авторизацией.
Сервис не сохраняет дампы в БД или кэше. Лимиты: 64 МиБ, 10 000 пакетов,
60 секунд, не более двух одновременно работающих анализаторов.

CLI: `3gpp-mcp analyze-dump --db /var/lib/3gpp-mcp/3gpp.db capture.pcapng`.
**Именованный входной файл CLI удаляется и при успехе, и при ошибке.**
HTTP/MCP не могут удалить исходное вложение на стороне клиента.

## Откат

Остановите таймер, update-unit и основной сервис, как в шаге 3. Затем:

```bash
sudo install -m 0755 /var/backups/3gpp-mcp/0.2.0/3gpp-mcp.previous /usr/local/bin/3gpp-mcp.rollback
sudo mv /usr/local/bin/3gpp-mcp.rollback /usr/local/bin/3gpp-mcp
```

Если после резервного копирования меняли индексы или спецификации, сначала
при остановленных сервисах выполните `sudo sqlite3 /var/lib/3gpp-mcp/3gpp.db
'PRAGMA wal_checkpoint(TRUNCATE); PRAGMA journal_mode=DELETE;'`, убедитесь в
успешном завершении, затем восстановите БД:

```bash
sudo install -o 3gpp-mcp -g 3gpp-mcp -m 0640 /var/backups/3gpp-mcp/0.2.0/3gpp.db /var/lib/3gpp-mcp/3gpp.db
sudo systemctl start 3gpp-mcp
```

Верните таймер в прежнее состояние. Конфигурацию сервиса сверяйте со старой
версией бинарника; автоматически сохранены бинарник и база, не systemd drop-in.
