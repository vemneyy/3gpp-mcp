# Обновление до 0.2.0: индексатор на хосте, MCP в Docker

`3gpp-index` запускается на сервере и создаёт SQLite-базу из ETSI/GSM и RFC.
`3gpp-server` работает в Docker и открывает эту базу только для чтения.
Полная повторная индексация для обновления программы не нужна.

## Обновить код и установить индексатор

В существующем чистом клоне репозитория:

```bash
git switch work
git pull --ff-only origin work
sudo apt-get update
sudo apt-get install -y poppler-utils sqlite3
make build-indexer
./bin/3gpp-index --version
# 3gpp-index 0.2.0
```

Для сборки нужен Go не ниже версии в `go.mod`. Вместо сборки можно скачать
готовый статический бинарник из отдельной ветки `binaries-0.2.0`:

```bash
mkdir -p bin
curl --fail --location https://github.com/vemneyy/3gpp-mcp/raw/refs/heads/binaries-0.2.0/3gpp-index_0.2.0_linux_amd64 -o bin/3gpp-index_0.2.0_linux_amd64
curl --fail --location https://github.com/vemneyy/3gpp-mcp/raw/refs/heads/binaries-0.2.0/SHA256SUMS -o bin/SHA256SUMS
(cd bin && sha256sum --ignore-missing -c SHA256SUMS)
chmod +x bin/3gpp-index_0.2.0_linux_amd64
cp bin/3gpp-index_0.2.0_linux_amd64 bin/3gpp-index
```

Для ARM64 скачайте файл с суффиксом `linux_arm64`. Даже готовому бинарнику
нужен `pdftotext` из Poppler для ETSI PDF; для индексации только RFC он не нужен.
Бинарники опубликованы отдельно от исходников в ветке `binaries-0.2.0`;
в исходной ветке `work` нет отслеживаемых файлов в `bin/`.

## Обновить только MCP-сервер

Сначала соберите образ. Существующий контейнер продолжает работать во время сборки:

```bash
docker compose build mcp
docker compose up -d mcp
docker compose exec mcp /3gpp-server --version
curl --fail http://127.0.0.1:8080/health
docker compose logs --tail=50 mcp
```

Compose монтирует `./data` в `/data` только для чтения. Другой каталог задаётся
через `MCP_DATA_DIR`; порт и адрес — через `MCP_PORT` и `MCP_BIND_ADDRESS`.
Сохраните существующие значения этих переменных при обновлении.

В образ уже включён `tshark`, а `/tmp` монтируется как временная файловая система.
MCP-инструмент `analyze_dump` принимает base64, HTTP `POST /analyze-dump` — сырые
PCAP/PCAPNG. Дампы удаляются до возврата результата и не попадают в БД или кэш.
HTTP-ответ не сохраняется сервером. Лимиты анализа: 64 МиБ, 10 000 пакетов,
60 секунд и два параллельных процесса. Исходные вложения у клиента остаются
под управлением клиента.

Задайте `THREEGPP_MCP_BEARER_TOKEN` в окружении Compose или локальном `.env`,
чтобы защитить MCP и загрузку дампов одним токеном. По умолчанию порт доступен
только на `127.0.0.1`; `/health` доступен без токена. Не добавляйте `.env` в Git.
Переподключите MCP-клиента для обновления списка инструментов: общий поиск
`search_all` охватывает ETSI, 3GPP, GSM и RFC, уже находящиеся в базе.
Для `search` передавайте `spec_ids`, deprecated `spec_id` удалён.

## Обновить спецификации и RFC

Остановите MCP перед изменением БД; не запускайте несколько индексаторов
одновременно. Сделайте резервную копию в новый файл:

```bash
docker compose stop mcp
sqlite3 data/3gpp.db ".backup 'data/3gpp.before-0.2.0.db'"
./bin/3gpp-index --source all --db data/3gpp.db --resume \
  --workers 8 --scrape-workers 8
docker compose up -d mcp
```

При ненулевом коде завершения база частичная. Исправьте причину и повторите
команду с `--resume`; запускайте MCP после успешного завершения. Индексатор
сам выполняет checkpoint и переводит БД в режим, пригодный для read-only mount.
`--workers` задаёт число скачиваний/конвертаций, `--scrape-workers` — число
параллельных запросов списков версий. Обход каталогов и импорт теперь работают
одновременно. Для новой базы уберите `--resume`; существующую базу без него
индексатор ETSI не перезаписывает.

## Откат

Сохраните старый образ (его ID показывает `docker compose images -q mcp`)
перед пересборкой, например через `docker image tag IMAGE_ID 3gpp-mcp:previous`.
Для отката остановите MCP и индексатор. Если база менялась, при остановленных
процессах выполните SQLite checkpoint и убедитесь, что команда завершилась успешно,
затем восстановите резервную копию. Если есть незавершённый WAL, сначала устраните
его, а не заменяйте только основной файл БД. Укажите старый образ в Compose и
запустите контейнер с `--no-build`, чтобы не пересобрать новую версию.
