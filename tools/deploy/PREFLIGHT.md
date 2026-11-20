# Проверка перед первой выкладкой на VPS

Понадобятся адрес VPS, SSH-порт, отпечаток ключа хоста из доверенного канала и четыре разных DNS-имени. Для каждого окружения нужны имя сайта и имя админки. Подготовь независимые namespace по [инструкции K3s](../../deploy/k8s/README.md) и отдельные учётные записи по [инструкции доставки](README.md). Начни проверку с `test`.

## SSH и сертификаты

На VPS администратор проверяет `sshd -t` и вывод `sshd -T -C user=...,host=...,addr=...` для обоих пользователей. Сверь `ForceCommand`, доступ только по ключу и запрет перенаправления портов. Получи отпечаток публичного хост-ключа командой `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`. Сверь его с заранее подтверждённым отпечатком и закрепи ключ в отдельном `known_hosts`.

С машины, с которой разрешено подключение, подставь путь к закреплённому `known_hosts` и ключу нужного окружения. Команда `preflight` специально выбрана вне разрешённого формата выкладки. Успех этой проверки означает, что сессия дошла до ограниченного скрипта и он отклонил команду до обращения к Kubernetes.

```bash
environment=test
ssh_host=vps.example.org
ssh_port=22
known_hosts=/path/to/verified-known-hosts
deploy_key=/path/to/test-deploy-key

ssh_status=0
ssh_reply=$(ssh -F /dev/null -nT -p "$ssh_port" -l "kadrroom-deploy-$environment" \
    -i "$deploy_key" -o "UserKnownHostsFile=$known_hosts" -o GlobalKnownHostsFile=/dev/null \
    -o StrictHostKeyChecking=yes -o UpdateHostKeys=no -o BatchMode=yes -o IdentitiesOnly=yes \
    -o IdentityAgent=none -o ClearAllForwardings=yes -o ConnectTimeout=10 \
    -- "$ssh_host" preflight 2>&1) || ssh_status=$?
[[ $ssh_status == 1 && $ssh_reply == *'Expected deploy, a commit SHA, and seven sha256 image digests.'* ]]
```

До переключения DNS можно проверить TLS через `curl --resolve "$host:443:$vps_ip" --head "https://$host/"` для каждого имени. Проверка цепочки и имени остаётся включённой. Если Ingress пока не обслуживает приложения с нулём реплик, проверь публичный сертификат. Запусти отдельно `openssl x509 -in "$fullchain" -noout -checkhost "$host"` для имени и `openssl x509 -in "$fullchain" -noout -checkend 604800` для срока. Повтори HTTPS-проверку после первого запуска. С VPS проверь, что обычный DNS возвращает ожидаемые A и AAAA и что доступен TCP 443 по каждому опубликованному адресу. `Deploy VPS` использует обычный DNS.

## K3s до и после запуска

На VPS из проверенной копии проекта выполни команду для каждого окружения с его доменами. Режим `bootstrap` проверяет нулевые реплики и исходные образы. После выкладки подставь полный SHA из сводки **Deploy VPS**. Скрипт сверяет оба namespace, Ingress, границы прав пользователя доставки, семь готовых Deployment и HTTPS для выбранного окружения. Используй административный kubeconfig локального K3s. Проверка читает состояние кластера и не обновляет ресурсы.

```bash
sudo bash tools/deploy/preflight.sh test bootstrap /etc/rancher/k3s/k3s.yaml \
    test.example.org admin-test.example.org
```

С реальным kubeconfig пользователя доставки отдельно выполни `kubectl auth can-i get deployment/frontend` и `kubectl auth can-i get secrets`. Ожидаются `yes` и `no`. В административном kubeconfig проверь состояние обоих хранилищ и отсутствие файла `/var/lib/kadrroom-deploy/<окружение>/pending` на первом запуске. Встроенный PostgreSQL должен пройти `kubectl -n "$namespace" rollout status statefulset/postgres --timeout=240s`. PVC `catalog-media` может перейти из `Pending` в `Bound` при запуске первого потребителя.

Сверь имена Secret из инструкции K3s через `kubectl -n "$namespace" get secret <имена> -o name`. Проверь наличие ключей и их формат локально по месту хранения. Сохраняй постоянный ключ почты между обновлениями. Оба фронтенда и `identity-api` должны ссылаться на один ключ `KADRROOM_AUTH_PROXY_TOKEN` внутри своего namespace. Фактический адрес пода Traefik должен входить в `KADRROOM_TRUSTED_PROXY_CIDR`. Принимай входящие forwarded headers только от доверенных промежуточных прокси. Для этой проверки достаточно ссылок на Secret и настроек маршрута. Полный вывод `nginx -T` содержит токен.

На стенде отправь один запрос входа с корректным `Origin`, некорректным JSON и фиктивным клиентским `X-Forwarded-For`. Ожидается `400 invalid_request`. Для сквозной проверки сравни счётчики `identity_rate_limits` до и после запроса. Ключ счётчика начинается с `auth-peer:`, затем идёт SHA-256 нормализованного IP в шестнадцатеричном виде. Должен измениться счётчик фактического IP клиента. Счётчики фиктивного IP и адресов Traefik и Nginx должны остаться прежними. Используй тестовый стенд без параллельного входа с того же IP. Один ответ `400` сам по себе показывает только доступность маршрута.

```bash
curl --silent --show-error --max-time 10 -o /dev/null -w '%{http_code}\n' \
    -H "Origin: https://$host" -H 'Content-Type: application/json' \
    -H 'X-Forwarded-For: 203.0.113.77' --data-binary '{' "https://$host/v1/auth/login"
```

## PostgreSQL и опубликованный каталог

Для встроенной PostgreSQL выполни следующий запрос администратором в каждом namespace до выкладки. Все три результата должны быть `t`. Для внешней PostgreSQL выполни этот же SQL в базе `postgres` соответствующего кластера через настроенное подключение с `sslmode=verify-full`. Серверная роль должна иметь возможность читать системные каталоги. У test и production должны быть отдельные экземпляры с указанными именами ролей. Пароли и строки подключения оставь в защищённом источнике.

```bash
namespace=kadrroom-test
kubectl -n "$namespace" exec -i statefulset/postgres -- \
    psql -XAtq -v ON_ERROR_STOP=1 -U kadrroom -d postgres <<'SQL'
BEGIN READ ONLY;
SELECT count(*) = 3 FROM pg_database
WHERE datname IN ('catalog', 'identity', 'rooms') AND pg_get_userbyid(datdba) = datname;
SELECT count(*) = 5 AND bool_and(rolcanlogin AND NOT rolsuper AND NOT rolbypassrls)
FROM pg_roles WHERE rolname IN ('catalog', 'catalog_read', 'catalog_worker', 'identity', 'rooms');
SELECT has_database_privilege('catalog_read', 'catalog', 'CONNECT')
   AND has_database_privilege('catalog_worker', 'catalog', 'CONNECT')
   AND NOT has_database_privilege('catalog_worker', 'identity', 'CONNECT')
   AND NOT has_database_privilege('catalog_worker', 'rooms', 'CONNECT')
   AND NOT pg_has_role('catalog_worker', 'catalog', 'MEMBER');
COMMIT;
SQL
```

После первого запуска проверь миграции. В базе `catalog` должны существовать `catalog_media`, опубликованное представление и политики строк для `catalog_worker`. Следующий запрос сверяет определения двух штатных политик PostgreSQL 16 и должен вернуть `t`.

```bash
kubectl -n "$namespace" exec -i statefulset/postgres -- \
    psql -XAtq -v ON_ERROR_STOP=1 -U kadrroom -d catalog <<'SQL'
BEGIN READ ONLY;
SELECT has_table_privilege('catalog_read', 'public.catalog_published_media', 'SELECT')
   AND NOT has_table_privilege('catalog_read', 'public.catalog_media', 'SELECT')
   AND has_column_privilege('catalog_worker', 'public.catalog_media', 'status', 'UPDATE')
   AND NOT has_column_privilege('catalog_worker', 'public.catalog_media', 'title', 'UPDATE')
   AND (SELECT relrowsecurity FROM pg_class WHERE oid = 'public.catalog_media'::regclass)
   AND (
       SELECT count(*) = 2 AND bool_and(COALESCE(
           permissive = 'PERMISSIVE' AND roles = ARRAY['catalog_worker']::name[] AND (
               (policyname = 'catalog_worker_read' AND cmd = 'SELECT' AND with_check IS NULL
                AND qual = $policy$(status = ANY (ARRAY['processing'::text, 'ready'::text, 'failed'::text]))$policy$)
               OR
               (policyname = 'catalog_worker_update' AND cmd = 'UPDATE'
                AND qual = $policy$(status = 'processing'::text)$policy$
                AND with_check = $policy$(status = ANY (ARRAY['processing'::text, 'ready'::text, 'failed'::text]))$policy$)
           ), false))
       FROM pg_policies
       WHERE schemaname = 'public' AND tablename = 'catalog_media'
   );
COMMIT;
SQL
```

Отдельно проверь наличие `identity_accounts` в базе `identity` и `room_rtc_calls` в базе `rooms`. На стенде загрузи небольшой разрешённый файл через админку, дождись обработки и проверь появление в каталоге после публикации оператором. Это также проверяет запись в том и реальный доступ обработчика к очереди.

## Восстановление из резервной копии

1. На чистом целевом экземпляре PostgreSQL для нужного окружения создай роли `catalog`, `catalog_read` и `catalog_worker`, затем пустую базу `catalog` с владельцем `catalog`. Отзови доступ к базе у `PUBLIC` и выдай `CONNECT` ролям `catalog_read` и `catalog_worker`. Роли читателя и воркера должны оставаться самостоятельными, без членства в роли владельца и без права обхода RLS. Для восстановления баз `identity` и `rooms` также заранее создай их роли-владельцы и пустые базы с правами подключения из начальной настройки.
2. Восстанови полный архив каждой базы от имени администратора, который может назначать исходных владельцев объектов. Оставь стандартное восстановление владельцев и ACL. Полный архив `pg_dump --format=custom` включает и политики RLS. Пример для каталога использует профиль подключения `kadrroom-restore`, заранее настроенный на целевой экземпляр.

    ```bash
    pg_restore --dbname='service=kadrroom-restore dbname=catalog' \
        --exit-on-error --single-transaction "$catalog_archive"
    ```

3. До запуска приложений отдельно восстанови PVC `catalog-media` из копии, соответствующей точке восстановления БД, с сохранением прав доступа к файлам. В Secret `identity-config` верни точное значение `KADRROOM_MAIL_TOKEN_KEY` из защищённой копии того же окружения.

На двух чистых экземплярах PostgreSQL 16.15 бэкенд-проверка подтвердила результат восстановления. `catalog_read` по-прежнему читает только представление опубликованного каталога, а `catalog_worker` не может перевести запись в `published`.

## Звонок и откат

- На LiveKit проверь действующее `room.auto_create: false`. Из namespace комнат должен быть доступен внутренний HTTP(S)-адрес управления. Браузер должен получить свой публичный WSS-адрес. OpenSSL или запрос HTTPS проверят доступность, а два браузера подтвердят медиатранспорт.
- На тестовом звонке подключи ведущего и гостя, закрой комнату и проверь отключение обоих клиентов. Попробуй снова войти со старым токеном **до истечения его TTL**, чтобы проверить запрет пересоздания комнаты. Не выводи токен и не сохраняй его в общих отчётах.
- Сверь семь ссылок на образы в Deployment с дайджестами из сводки **Deploy VPS**. Для проверки отката на стенде используй два успешных CI run и повтори выкладку первого, пока доступны его архивы. Затем проверь все семь прежних образов, оба HTTPS-домена и сохранность тестовых данных. Автоматический откат после сбоя и восстановление после прерывания отдельно моделирует `bash tools/deploy/release.test.sh`. На production используй рабочий выпуск и проверенный резервный план для базы и тома.

## Что подтвердить на GitHub runner

В ручном полном **CI** на `main` сверь SHA проверяемой ревизии и результаты всех семи сборок BuildKit. Независимый `deploy-stack` должен загрузить именно семь `image-<app>/image.tar` из этого прогона в свежий Linux kind. Каждый архив должен содержать `kadrroom/<app>:ci` для `linux/amd64` с OCI-меткой `org.opencontainers.image.revision`, равной SHA прогона. Артефактам и тестам этого прогона достаточно синтетической конфигурации.

Подтверди запуск и готовность семи приложений, DNS сервисов через оба Nginx, Mailpit, обработку и публикацию файла, вход гостя, выдачу RTC-токена и закрытие комнаты. Отдельный `deploy-frontend` должен подтвердить доступность фронтенда во время остановки Catalog API и восстановление каталога после его запуска. Проверь также отчёт Playwright о реальном звонке через Compose. Для зачёта отказа повторного RTC-входа в kind проверь, что у экземпляра LiveKit включено `room.auto_create: false` и что попытка входа использует ещё действующий токен.

Локально kind дошёл до systemd после устранения отдельной ошибки маршрута на изолированном узле. Systemd завершился с `Too many open files` при создании inotify-объекта cgroup. Точный исчерпанный лимит не установлен, Kubernetes API не поднялся. Повтор с теми же предпосылками не проверит приложения. Для независимого замера нужен свежий runner с рабочими systemd и cgroup. Проверки через kind port-forward подтверждают приложение и DNS сервисов. Реальные Traefik, TLS, применение NetworkPolicy, SMTP production и звонок через публичный WSS проверяются на целевой инфраструктуре.
