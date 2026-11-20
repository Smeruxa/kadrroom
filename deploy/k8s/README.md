# K3s на VPS

| Окружение    | Namespace             | Сайт и админка                    | Почта                    |
| ------------ | --------------------- | --------------------------------- | ------------------------ |
| `test`       | `kadrroom-test`       | Два отдельных HTTPS-домена стенда | Mailpit внутри namespace |
| `production` | `kadrroom-production` | Два отдельных HTTPS-домена релиза | Внешний SMTP с STARTTLS  |

В обоих окружениях Traefik направляет входящий трафик на зрительский и операторский фронтенды. Каждый слушает порт 8080 на своём домене. Зрительский фронтенд проксирует каталог на порт 8080, аккаунты на 8081 и комнаты на 8082. WebSocket проходит через зрительский HTTPS-домен. Операторский фронтенд проксирует запросы админки к `admin-api:8083`, авторизацию к аккаунтам, а обложки к каталогу. NetworkPolicy разрешает доступ к `admin-api` только от `admin-frontend` в том же namespace. Rooms API работает в одной реплике. При обновлении соединения закрываются, клиент переподключается и получает состояние из PostgreSQL. Воркер обработки медиа работает в одной реплике без HTTP-сервиса.

## Первый запуск

Нужен одноузловой K3s со штатными Traefik, контроллером NetworkPolicy, DNS-зоной кластера `cluster.local` и StorageClass `local-path`. Все четыре имени должны быть разными и вести на VPS. Открой входящий TCP 443. В манифесте Traefik принимает приложение на `websecure`. Если нужен перенаправляющий HTTP-адрес, администратор настраивает перенаправление `web` → `websecure` на уровне Traefik и открывает TCP 80.

На машине администратора нужен `kubectl` с доступом к кластеру. Из проверенного клона вначале выведи манифест на экран и проверь namespace и адрес. Выполни эти команды отдельно для каждого окружения с его значениями.

```bash
environment=test
public_hostname=test.example.org
admin_hostname=admin-test.example.org
namespace="kadrroom-$environment"
secret_dir="/etc/kadrroom/private/$environment"

bash deploy/k8s/render.sh "$environment" "$public_hostname" "$admin_hostname"
bash deploy/k8s/render.sh "$environment" "$public_hostname" "$admin_hostname" |
    kubectl apply --server-side -f -
```

Первичный манифест создаёт семь Deployment приложения с нулём реплик и значением `bootstrap` в аннотации `kadrroom.io/revision`. Workflow доставки выставит проверенные ссылки на образы с дайджестами и запустит по одной реплике. **Применяй полный первичный манифест только при создании окружения.** Для последующих правок конфигурации применяй нужные ресурсы отдельно, сохраняя у Deployment рабочий образ, ревизию и количество реплик. После изменения значений, подставляемых через переменные окружения, перезапусти затронутые Deployment.

До первой доставки создай по TLS Secret с цепочкой сертификатов и приватным ключом для каждого домена. Сохраняй имена `frontend-tls` и `admin-frontend-tls` при продлении. Можно поручить выпуск и продление DNS-01 контроллеру сертификатов, настроив его на те же имена Secret в этом namespace.

```bash
kubectl -n "$namespace" create secret tls frontend-tls \
    --cert="$secret_dir/tls/frontend/fullchain.pem" --key="$secret_dir/tls/frontend/privkey.pem"
kubectl -n "$namespace" create secret tls admin-frontend-tls \
    --cert="$secret_dir/tls/admin/fullchain.pem" --key="$secret_dir/tls/admin/privkey.pem"
```

При закрытых образах GHCR создай в каждом namespace Secret `registry-pull` типа `kubernetes.io/dockerconfigjson` из файла с правом скачивания образов и подключи к runtime ServiceAccount. Для публичных пакетов GHCR этот шаг можно пропустить.

```bash
kubectl -n "$namespace" create secret generic registry-pull \
    --type=kubernetes.io/dockerconfigjson \
    --from-file=.dockerconfigjson="$secret_dir/registry/config.json"
kubectl -n "$namespace" patch serviceaccount kadrroom-runtime --type=merge \
    -p '{"imagePullSecrets":[{"name":"registry-pull"}]}'
```

## Данные и почта

Используй отдельные секреты, пароли, базы, ключи подписи и TLS для каждого окружения. Передай Secret из приватных файлов окружения с правами `0600`, хранящихся вне рабочего дерева. Формат файла для `--from-env-file` — одна строка `КЛЮЧ=значение` без кавычек.

| Secret                   | Ключи                                                                                                                                                                                                                      |
| ------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `catalog-config`         | `KADRROOM_CATALOG_DATABASE_URL` для роли `catalog_read`, `KADRROOM_CATALOG_SIGNING_KEY`                                                                                                                                    |
| `admin-config`           | `KADRROOM_ADMIN_DATABASE_URL` для роли `catalog`                                                                                                                                                                           |
| `media-worker-config`    | `KADRROOM_CATALOG_DATABASE_URL` для роли `catalog_worker`                                                                                                                                                                  |
| `catalog-rooms-contract` | `KADRROOM_CATALOG_INTERNAL_TOKEN` для внутреннего запроса комнат к каталогу                                                                                                                                                |
| `identity-config`        | `KADRROOM_IDENTITY_DATABASE_URL`, `KADRROOM_MAIL_TOKEN_KEY`, опционально `KADRROOM_OPERATOR_EMAIL`. В production также `KADRROOM_SMTP_ADDR`, `KADRROOM_SMTP_FROM`, `KADRROOM_SMTP_USERNAME`, `KADRROOM_SMTP_PASSWORD`      |
| `auth-proxy-config`      | `KADRROOM_AUTH_PROXY_TOKEN` для `identity-api` и обоих фронтендов, `KADRROOM_TRUSTED_PROXY_CIDR` для обоих Nginx                                                                                                           |
| `rooms-config`           | `KADRROOM_ROOMS_DATABASE_URL`                                                                                                                                                                                              |
| `postgres-bootstrap`     | Для встроенного PostgreSQL. `POSTGRES_PASSWORD`, `KADRROOM_CATALOG_DB_PASSWORD`, `KADRROOM_CATALOG_READ_DB_PASSWORD`, `KADRROOM_CATALOG_WORKER_DB_PASSWORD`, `KADRROOM_IDENTITY_DB_PASSWORD`, `KADRROOM_ROOMS_DB_PASSWORD` |
| `livekit-config`         | Для звонка задай вместе `KADRROOM_LIVEKIT_URL`, `KADRROOM_LIVEKIT_INTERNAL_URL`, `KADRROOM_LIVEKIT_API_KEY` и `KADRROOM_LIVEKIT_API_SECRET` от 32 байт                                                                     |

Сгенерируй независимые случайные значения длиной от 32 байт для ключа подписи ссылок и внутреннего токена. Для `KADRROOM_MAIL_TOKEN_KEY` задай постоянный 32-байтный ключ в виде 64 шестнадцатеричных символов. Сохраняй его при обновлениях `identity-api`. Для токена прокси также используй независимо сгенерированные 64 шестнадцатеричных символа.

Оба Nginx передают токен и адрес клиента в `identity-api` на маршрутах авторизации. Укажи в `KADRROOM_TRUSTED_PROXY_CIDR` диапазон адресов Traefik. Для штатного одноузлового K3s это Pod CIDR узла, который можно посмотреть через `kubectl get nodes -o custom-columns=NAME:.metadata.name,POD_CIDR:.spec.podCIDR`. NetworkPolicy ограничивает доступ к Nginx подами Traefik. Traefik должен принимать заголовки перенаправления только от доверенных входящих прокси.

Для production укажи SMTP-адрес `host:port` с STARTTLS, разрешённый адрес отправителя и отдельные учётные данные SMTP. Для входа оператора задай его подтверждённый адрес через `KADRROOM_OPERATOR_EMAIL`. Для стенда SMTP обслуживает локальный Mailpit. Чтобы прочитать тестовое письмо, используй перенаправление порта с машины администратора.

```bash
for secret in catalog-config admin-config media-worker-config catalog-rooms-contract identity-config auth-proxy-config rooms-config; do
    kubectl -n "$namespace" create secret generic "$secret" \
        --from-env-file="$secret_dir/$secret.env"
done

kubectl -n kadrroom-test port-forward --address=127.0.0.1 service/mailpit 8025:8025
```

**PostgreSQL на том же VPS.** Применяй отдельный StatefulSet в каждом namespace. PostgreSQL на первом запуске создаёт базы и одноимённые роли `catalog`, `identity` и `rooms`. Для общей базы каталога `admin-api` использует роль владельца `catalog` и применяет миграции. Зрительский API читает опубликованные записи через роль `catalog_read`. Роль `catalog_worker` читает очередь и обновляет статус, длительность и поля захвата задания. URL сервиса имеет вид `postgres://identity:<пароль-из-файла-этого-окружения>@postgres:5432/identity?sslmode=disable`. Подставь роль и базу соответствующего сервиса, экранируй специальные символы пароля для URL.

```bash
kubectl -n "$namespace" create secret generic postgres-bootstrap \
    --from-env-file="$secret_dir/postgres-bootstrap.env"
kubectl apply --server-side -k "deploy/k8s/postgres/$environment"
kubectl -n "$namespace" rollout status statefulset/postgres --timeout=240s
```

Если начальное создание баз прервалось после инициализации диска, после диагностики администратор может повторить создание недостающих баз и ролей командой `kubectl -n "$namespace" exec postgres-0 -- sh /docker-entrypoint-initdb.d/init-databases.sh`. При смене пароля на существующем томе сначала измени его в PostgreSQL и согласованно обнови Secret сервиса. Начальная инициализация не меняет уже созданные пароли.

**Внешний PostgreSQL.** Используй отдельный кластер PostgreSQL для каждого окружения с тремя базами и пятью ролями. Имена ролей действуют на весь кластер. Для миграций право создавать объекты получают владельцы баз. Для `catalog_read` и `catalog_worker` выдай только CONNECT к базе `catalog` и USAGE на схему `public`. Миграция `admin-api` назначит табличные права этим двум ролям. Создай ConfigMap `postgres-ca` с ключом `ca.crt` из доверенной цепочки CA провайдера. В каждом URL укажи `sslmode=verify-full&sslrootcert=/etc/kadrroom/postgres-ca/ca.crt`.

```bash
kubectl -n "$namespace" create configmap postgres-ca \
    --from-file=ca.crt="$secret_dir/postgres-ca.pem"
```

Для внешней базы добавь в этом namespace отдельный NetworkPolicy с `podSelector.matchLabels.app: admin-api`, `policyTypes: [Egress]` и разрешением `egress.to.ipBlock.cidr` на диапазон IP базы и TCP-порт из URL. Штатная политика разрешает `admin-api` обращаться к CoreDNS, `identity-api` и встроенному PostgreSQL.

Манифесты запрашивают 10 ГиБ для PostgreSQL и 20 ГиБ для общего PVC `catalog-media` в каждом окружении. `admin-api` и `media-worker` пишут на том, а зрительский каталог монтирует его для чтения. Проверка готовности воркера подтверждает доступ на запись. Подключение к базе, захват задания и обработку файла проверь тестовой загрузкой. Сделай резервные копии баз и медиа вне VPS и проверь их восстановление. Откат образов через workflow использует существующие тома и базы.

## Звонок

Для голоса и камер подключи отдельный LiveKit с собственными учётными данными для каждого окружения. `KADRROOM_LIVEKIT_URL` задаёт публичный адрес `wss://`, который API возвращает браузеру. `KADRROOM_LIVEKIT_INTERNAL_URL` задаёт адрес для подписанных вызовов CreateRoom и DeleteRoom из `rooms-api`. Если LiveKit работает внутри кластера, укажи адрес его Service в том же namespace, например `http://livekit.kadrroom-test.svc.cluster.local:7880` для стенда. Для внешнего сервиса укажи доступный из подов HTTPS-адрес API. При выключенном звонке оставь все четыре переменные пустыми.

На LiveKit каждого окружения выключи автоматическое создание комнат. Тогда ранее выданный билет не сможет заново открыть удалённую комнату.

```yaml
room:
    auto_create: false
```

Настрой публичный WSS, медиапорты и TURN/TLS для браузеров. Если два LiveKit делят один VPS, выдели им разные TCP- и UDP-порты. После настройки создай ссылку на комнату, подключи двух участников к звонку, закрой комнату и проверь отключение участников и отказ при повторном входе со старым токеном.

```bash
kubectl -n "$namespace" create secret generic livekit-config \
    --from-env-file="$secret_dir/livekit-config.env"
```

Продолжение настройки SSH, GitHub Environments и ручной доставки описано в [инструкции доставки](../../tools/deploy/README.md).
