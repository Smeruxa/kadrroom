# Доставка KadrRoom на VPS

Workflow **Deploy VPS** принимает имя окружения и ID успешного ручного прогона **CI** с ветки `main`. После одобрения он загружает семь архивов этого прогона, сверяет ревизию образов, публикует их в GHCR и передаёт на VPS SHA коммита и семь дайджестов. Для текущих CI-образов нужен VPS на Linux amd64. Установка кластера и настройка инфраструктуры выполняются администратором.

## Первичная настройка

1. Подготовь одноузловой K3s с Traefik, Bash, `kubectl`, `jq`, `curl` и `flock`. Ограничь снаружи доступ к Kubernetes API. Настрой отдельные DNS-имена и сертификаты для публичного сайта и админки в каждом окружении. Примени манифесты по [инструкции K3s](../../deploy/k8s/README.md). Для закрытых пакетов GHCR настрой отдельный `registry-pull` в каждом namespace с правом только чтения пакетов.
2. Установи скрипт из проверенной ревизии в каталог, доступный на запись администратору.

    ```sh
    sudo install -d -m 0755 /usr/local/libexec/kadrroom
    sudo install -m 0755 tools/deploy/release.sh /usr/local/libexec/kadrroom/release.sh
    ```

3. Заведи двух отдельных системных пользователей с собственной группой и оболочкой Bash, без доступа к sudo, Docker и общим группам администратора. Следующие команды выполняются администратором для каждого окружения. Задай соответствующий домен вместо примера.

    ```sh
    environment=test
    public_host=test.example.org
    admin_host=admin.test.example.org
    account="kadrroom-deploy-$environment"
    config_dir="/etc/kadrroom/$environment"
    state_dir="/var/lib/kadrroom-deploy/$environment"

    useradd --system --user-group --create-home --shell /bin/bash "$account"
    install -d -o root -g "$account" -m 0750 "$config_dir"
    install -d -o "$account" -g "$account" -m 0700 "$state_dir"
    printf '%s\n' "$public_host" > "$config_dir/host"
    printf '%s\n' "$admin_host" > "$config_dir/admin-host"
    install -m 0644 /var/lib/rancher/k3s/server/tls/server-ca.crt "$config_dir/k3s-ca.crt"
    ```

4. Создай отдельные учётные данные Kubernetes для `kadrroom-deployer` в каждом namespace. Эта учётная запись привязана к роли обновления семи конкретных Deployments. Храни токен на VPS с правами чтения только для соответствующего пользователя выкладки. После первого создания Secret дождись заполнения контроллером перед чтением ключа `token`. Выполняй команды без трассировки и сохраняй секретные данные в файле с ограниченным доступом.

    ```bash
    k3s kubectl -n "kadrroom-$environment" apply -f - <<'YAML'
    apiVersion: v1
    kind: Secret
    metadata:
        name: kadrroom-deployer-token
        annotations:
            kubernetes.io/service-account.name: kadrroom-deployer
    type: kubernetes.io/service-account-token
    YAML

    (
        set -euo pipefail
        token_file=$(mktemp "$config_dir/token.XXXXXXXX")
        trap 'rm -f -- "$token_file"' EXIT
        k3s kubectl -n "kadrroom-$environment" get secret kadrroom-deployer-token \
            -o jsonpath='{.data.token}' | base64 --decode > "$token_file"
        test -s "$token_file"
        install -o root -g "$account" -m 0640 "$token_file" "$config_dir/token"
    )

    cat > "$config_dir/kubeconfig" <<EOF
    apiVersion: v1
    kind: Config
    clusters:
        - name: k3s
          cluster:
              server: https://127.0.0.1:6443
              certificate-authority: $config_dir/k3s-ca.crt
    users:
        - name: kadrroom-deployer
          user:
              tokenFile: $config_dir/token
    contexts:
        - name: kadrroom
          context:
              cluster: k3s
              namespace: kadrroom-$environment
              user: kadrroom-deployer
    current-context: kadrroom
    EOF
    chown root:"$account" "$config_dir/kubeconfig"
    chmod 0640 "$config_dir/kubeconfig"
    ```

    Для отзыва удали Secret `kadrroom-deployer-token` в нужном namespace. Для замены создай Secret заново, дождись нового токена и обнови файл. Проверь kubeconfig от имени соответствующего Linux-пользователя. Команда `auth can-i get deployment/frontend` должна вернуть `yes`, а `auth can-i get secrets` и проверка доступа к Deployment в соседнем namespace должны вернуть `no`.

5. Установи разные открытые SSH-ключи выкладки в `/etc/kadrroom/test/deploy.authorized_keys` и `/etc/kadrroom/production/deploy.authorized_keys`. Выставь владельца root, группу соответствующего пользователя и режим `0640`. Добавь префикс `restrict` к каждой строке ключа. В конфигурацию SSH добавь блоки для этих двух пользователей.

    ```text
    Match User kadrroom-deploy-test
        AuthenticationMethods publickey
        AuthorizedKeysFile /etc/kadrroom/test/deploy.authorized_keys
        DisableForwarding yes
        PermitTTY no
        PermitUserRC no
        ForceCommand PATH=/usr/local/bin:/usr/bin:/bin /usr/local/libexec/kadrroom/release.sh test /etc/kadrroom/test /var/lib/kadrroom-deploy/test

    Match User kadrroom-deploy-production
        AuthenticationMethods publickey
        AuthorizedKeysFile /etc/kadrroom/production/deploy.authorized_keys
        DisableForwarding yes
        PermitTTY no
        PermitUserRC no
        ForceCommand PATH=/usr/local/bin:/usr/bin:/bin /usr/local/libexec/kadrroom/release.sh production /etc/kadrroom/production /var/lib/kadrroom-deploy/production

    Match all
    ```

    Ограничь принимаемые от клиента переменные среды в `AcceptEnv` и исключи пользовательские файлы среды через `PermitUserEnvironment no`. Проверь итоговую конфигурацию через `sshd -t` и `sshd -T -C user=kadrroom-deploy-test,host=localhost,addr=127.0.0.1` перед перечитыванием конфигурации. Проверь аналогичный блок для production. SSH принимает только команду ниже, каждый дайджест передаётся в форме `sha256:…`.

    ```text
    deploy <commit> <frontend-digest> <catalog-api-digest> <identity-api-digest> <rooms-api-digest> <admin-frontend-digest> <admin-api-digest> <media-worker-digest>
    ```

6. Создай GitHub Environments `test` и `production`. Включи обязательное одобрение и разреши запуск только из `main`. Для каждого окружения укажи свои значения.

    | Тип        | Имя                   | Значение                                                    |
    | ---------- | --------------------- | ----------------------------------------------------------- |
    | Переменная | `VPS_SSH_HOST`        | DNS-имя или IP SSH-сервера                                  |
    | Переменная | `VPS_SSH_PORT`        | TCP-порт SSH, по умолчанию 22                               |
    | Переменная | `VPS_SSH_USER`        | `kadrroom-deploy-test` или `kadrroom-deploy-production`     |
    | Секрет     | `VPS_SSH_PRIVATE_KEY` | Закрытый ключ этого окружения для автоматизированного входа |
    | Секрет     | `VPS_SSH_KNOWN_HOSTS` | Запись known_hosts с проверенным ключом VPS                 |

    Возьми открытый ключ хоста и сверь его отпечаток через доверенный канал администратора. В записи known_hosts используй `host` на порту 22 и `[host]:port` на другом порту. Для существующих пакетов GHCR дай Actions этого репозитория право публикации. Каждое окружение должно иметь собственные полномочия чтения приватных пакетов.

## Выкладка и восстановление

Перед первым запуском пройди [preflight SSH, K3s и сервисов](PREFLIGHT.md).

1. Запусти полный **CI** на `main` и дождись успеха. Скопируй числовой ID запуска, пока доступны все семь архивов.
2. Запусти **Deploy VPS**, выбери `test`, укажи этот ID и одобри выкладку. На стенде проверь письмо и вход, загрузку и обработку разрешённого тестового медиа через админку, публикацию в каталог, гостевое приглашение и подключение двух клиентов к комнате по WSS. Автоматическая проверка WSS-маршрута ожидает отказ при отсутствующем билете. Полное подключение проверяется через живую комнату.
3. Для production выбери соответствующее окружение и тот же проверенный CI run. Скрипт ждёт готовности identity и admin API перед запуском каталога, комнат и обработчика медиа, затем обновляет оба фронтенда. Проверка открывает обе страницы по HTTPS, сверяет каталог, проверяет сессию без cookie на обоих доменах и ожидает `404` у административного пути на публичном домене. На домене админки тот же запрос должен получить `401`. VPS должен сам разрешать оба домена и достигать HTTPS.

На стенде проверь права публикации на уровне PostgreSQL. Роль `catalog_worker` должна захватывать задания и завершать обработку, а переход в `published` должен выполняться только через роль администратора каталога.

При ошибке скрипт возвращает семь предыдущих образов, число реплик и SHA. Если первая выкладка не удалась, он возвращает исходные нулевые реплики. Скрипт хранит журнал в отдельном каталоге каждого окружения и при следующем запуске завершает прерванный откат перед обновлением. Если откат не завершился, проверь состояние Deployments, доступность прежних дайджестов в GHCR, сертификаты, DNS и причины неготовности подов. Для старых выпусков сохрани образы и резервные копии БД. Вручную откатить рабочие образы позволяет повторный запуск workflow с прежним CI run, пока доступны его артефакты. Сводка **Deploy VPS** содержит SHA и опубликованные ссылки по дайджестам.

Обновляй SQL-схему совместимо с текущей и предыдущей версией приложений. Откат образов сохраняет изменения данных и миграций. Восстановление PostgreSQL из резервной копии проводится администратором отдельно. Изменения манифестов или конфигурации на действующем кластере выполняет администратор с сохранением полей образа, SHA и числа реплик семи Deployments.

`bash tools/deploy/release.test.sh` проверяет обработку команды, откат и повтор после прерывания. Тест использует временные файлы и подменяет вызовы Kubernetes и HTTPS.
