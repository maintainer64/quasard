# Собственный CA на Станции Лайт (micro)

[Основная настройка](../README.md) · [Keenetic: только DNS](KEENETIC.md) · [Диагностика](TRAFFIC_CAPTURE.md)

Это описание способа, применённого к **собственной Станции Лайт (micro) с уже доступным root SSH на порту 2222**. После установки своего CA и ручной перезагрузки владелец подтвердил голосовой ответ через локальный backend. Это не универсальная инструкция для всех моделей/прошивок и не способ получить root-доступ.

**Изменяется системное доверие устройства. Выполняйте команды только на своей колонке, после чтения всей инструкции и подготовки резервной копии. Ошибка при записи trust store может нарушить соединения. На другой разметке/прошивке остановитесь и сначала выясните устройство файловой системы.** Ни генерация CA, ни запуск Docker автоматически не выполняют эти действия.

## Что куда устанавливается

- `certs/ca.crt` — публичный CA, который добавляется на колонку.
- `certs/ca.key` — приватный ключ CA, остаётся у вас. **На колонку и в runtime-контейнер не передавать.**
- `certs/server.crt` и `certs/server.key` — только HTTPS-серверу quasard.
- На проверенном устройстве основной bundle: `/system/vendor/quasar/certs/ca-certificates.crt`. Могут существовать ссылки через другие пути, но работать следует с проверенным реальным файлом.

Сохраняются **все штатные CA**; ваш добавляется к ним. Не заменяйте bundle одним `ca.crt`. CA на Keenetic и macOS для этого подключения устанавливать не нужно.

## 1. Сгенерировать CA и проверить доступ

Генерация в готовом образе описана в [README, шаг 3](../README.md#3-создать-собственные-сертификаты). Если комплект уже создан, не перегенерируйте его без причины: CA переиспользуется, но leaf-файлы перезаписываются.

Команды ниже — POSIX shell, из корня проекта. `STATION_IP` замените на адрес своей Станции:

```sh
export STATION_IP=192.168.1.20 # пример, ЗАМЕНИТЬ
openssl x509 -in certs/ca.crt -noout -subject -dates -fingerprint -sha256
openssl verify -CAfile certs/ca.crt certs/server.crt
ssh -p 2222 "root@$STATION_IP"
```

При первом подключении сверяйте SSH host key по доверенному источнику; не используйте `StrictHostKeyChecking=no`. Если отпечаток изменился, сначала выясните причину. В интерактивной сессии **только осмотрите** устройство, затем выйдите:

```sh
ls -ld /system/vendor/quasar/certs
ls -l /system/vendor/quasar/certs/ca-certificates.crt
mount
command -v sha256sum
command -v mktemp
exit
```

Рецепт дальше предполагает обычный файл bundle (не symlink), родительский каталог на корневом разделе `/`, возможность remount этого раздела и утилиты `sha256sum`, `mktemp`, `dd`, `cp`, `mv`, `grep`. Если предположения не выполняются — **не продолжайте**. Не применяйте команды remount к другому разделу наугад.

## 2. Создать локальную и удалённую резервные копии

Локальные результаты размещаем в ignored `data/`, а не в Git. Каждый запуск получает новый каталог; старые backup не перезаписываются.

```sh
umask 077
mkdir -p data
BACKUP=$(mktemp -d "$PWD/data/station-ca-XXXXXXXX")
REMOTE=$(ssh -p 2222 -o StrictHostKeyChecking=yes "root@$STATION_IP" \
  'set -eu; umask 077; mktemp -d /data/quasard-ca-XXXXXXXX')
# Далее значение REMOTE используется в удалённых командах; принять только этот формат.
case "$REMOTE" in *[!A-Za-z0-9/-]*) exit 1 ;; esac
case "$REMOTE" in
  /data/quasard-ca-????????) ;;
  *) printf '%s\n' 'Неожиданный backup path; остановка.' >&2; exit 1 ;;
esac
printf '%s\n' "$REMOTE" > "$BACKUP/remote-path.txt"

ssh -p 2222 -o StrictHostKeyChecking=yes "root@$STATION_IP" \
  "set -eu; b=/system/vendor/quasar/certs/ca-certificates.crt; \
   test -f \"\$b\"; test ! -L \"\$b\"; \
   cp -p \"\$b\" '$REMOTE/original-ca-certificates.crt'; \
   dd if=\"\$b\" bs=65536 2>/dev/null" \
  > "$BACKUP/original-ca-certificates.crt"
test -s "$BACKUP/original-ca-certificates.crt" || exit 1
openssl crl2pkcs7 -nocrl -certfile "$BACKUP/original-ca-certificates.crt" \
  -outform DER > /dev/null || exit 1
ORIGINAL_SHA=$(openssl dgst -sha256 -r "$BACKUP/original-ca-certificates.crt" | cut -d ' ' -f 1)
printf '%s\n' "$ORIGINAL_SHA" > "$BACKUP/original.sha256"
ssh -p 2222 -o StrictHostKeyChecking=yes "root@$STATION_IP" \
  "sha256sum '$REMOTE/original-ca-certificates.crt'"
printf 'Локальный SHA-256: %s\nBackup: %s\n' "$ORIGINAL_SHA" "$BACKUP"
```

Сравните удалённый и локальный SHA-256: они должны совпасть. При ошибке SSH/копирования/проверки остановитесь. Сохраните путь `$BACKUP` и `$REMOTE`: они нужны для проверки и отката даже после закрытия shell. Bundle тоже может раскрывать настройки доверия устройства — не публикуйте его.

## 3. Подготовить новый bundle без изменения системного файла

Проверка ниже не даёт добавить тот же PEM повторно. Если сертификат уже установлен, не делайте повторную запись; сначала проверьте существующее доверие. Она не обнаруживает все эквивалентные формы PEM, поэтому не смешивайте разные кодировки одного сертификата.

```sh
# PEM-тело одной строки достаточно для обнаружения этого же сертификата в bundle.
CA_MARKER=$(grep -v '^-----' certs/ca.crt | grep -v '^$' | head -n 1)
test -n "$CA_MARKER" || exit 1
if grep -Fq "$CA_MARKER" "$BACKUP/original-ca-certificates.crt"; then
  printf '%s\n' 'Этот CA уже есть; установка не нужна.' >&2
  exit 1
fi

{
  dd if="$BACKUP/original-ca-certificates.crt" bs=65536 2>/dev/null
  printf '\n'
  dd if=certs/ca.crt bs=65536 2>/dev/null
} > "$BACKUP/combined-ca-certificates.crt"
openssl crl2pkcs7 -nocrl -certfile "$BACKUP/combined-ca-certificates.crt" \
  -outform DER > /dev/null || exit 1
openssl verify -CAfile "$BACKUP/combined-ca-certificates.crt" certs/server.crt || exit 1
INSTALLED_SHA=$(openssl dgst -sha256 -r "$BACKUP/combined-ca-certificates.crt" | cut -d ' ' -f 1)
printf '%s\n' "$INSTALLED_SHA" > "$BACKUP/installed.sha256"

# Передаём только публичные сертификаты, без scp/SFTP, которых может не быть в firmware.
ssh -p 2222 -o StrictHostKeyChecking=yes "root@$STATION_IP" \
  "set -eu; umask 077; test ! -e '$REMOTE/quasard-ca.crt'; \
   dd of='$REMOTE/quasard-ca.crt' bs=4096" < certs/ca.crt
ssh -p 2222 -o StrictHostKeyChecking=yes "root@$STATION_IP" \
  "set -eu; umask 077; test ! -e '$REMOTE/combined-ca-certificates.crt'; \
   dd of='$REMOTE/combined-ca-certificates.crt' bs=65536" \
  < "$BACKUP/combined-ca-certificates.crt"
```

Перед следующей записью сравните fingerprint `certs/ca.crt` с тем, который решили доверить. Не выполняйте установку, если подготовка или передача закончилась ошибкой.

## 4. Установить с проверкой хэшей и вернуть read-only

**Следующая команда записывает системный bundle.** Проверяются неизменность текущего файла и подготовленный SHA-256. При любом несоответствии запись не выполняется. На время операции никто другой не должен менять trust store.

```sh
ssh -p 2222 -o StrictHostKeyChecking=yes "root@$STATION_IP" \
  "sh -s -- '$REMOTE' '$ORIGINAL_SHA' '$INSTALLED_SHA'" <<'SH'
set -eu
d=$1
original=$2
installed=$3
b=/system/vendor/quasar/certs/ca-certificates.crt
check_hash() {
  actual=$(sha256sum "$1")
  actual=${actual%% *}
  test "$actual" = "$2"
}
test -f "$b"
test ! -L "$b"
check_hash "$b" "$original"
check_hash "$d/original-ca-certificates.crt" "$original"
check_hash "$d/combined-ca-certificates.crt" "$installed"
tmp=
cleanup() {
  rc=$?
  trap - EXIT
  if test -n "$tmp"; then rm -f "$tmp"; fi
  sync
  if ! mount -o remount,ro /; then
    printf '%s\n' 'ОШИБКА: не удалось вернуть / в read-only; проверьте устройство.' >&2
    exit 1
  fi
  exit "$rc"
}
# trap регистрируется до remount: ошибку возврата в RO нельзя потерять.
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
mount -o remount,rw /
tmp=$(mktemp "$b.quasard.XXXXXXXX")
cp "$d/combined-ca-certificates.crt" "$tmp"
chown 0:0 "$tmp"
chmod 0644 "$tmp"
check_hash "$tmp" "$installed"
check_hash "$b" "$original"
mv -f "$tmp" "$b"
tmp=
check_hash "$b" "$installed"
SH
```

Если SSH оборвался, не повторяйте установку вслепую: сначала проверьте bundle и режим монтирования. Trap помогает при штатном выходе/сигналах, но не гарантирует cleanup при сбое питания или принудительном завершении ядром.

## 5. Проверить результат и перезагрузить вручную

```sh
ssh -p 2222 -o StrictHostKeyChecking=yes "root@$STATION_IP" \
  'dd if=/system/vendor/quasar/certs/ca-certificates.crt bs=65536 2>/dev/null' \
  > "$BACKUP/verified-ca-certificates.crt"
VERIFIED_SHA=$(openssl dgst -sha256 -r "$BACKUP/verified-ca-certificates.crt" | cut -d ' ' -f 1)
test "$VERIFIED_SHA" = "$INSTALLED_SHA" || exit 1
openssl verify -CAfile "$BACKUP/verified-ca-certificates.crt" certs/server.crt || exit 1
ssh -p 2222 -o StrictHostKeyChecking=yes "root@$STATION_IP" 'mount'
```

Проверьте, что `/` снова `ro`. Затем **вручную перезагрузите свою Станцию**, чтобы процесс перечитал хранилище. На проверенном устройстве это сделал владелец; команды выше сами не перезагружают колонку и не перезапускают Quasar.

Теперь можно назначить DNS по [основной инструкции](../README.md#7-указать-dns-в-keenetic). Успешный `openssl verify` подтверждает сертификат локально, но не заменяет проверку реального соединения firmware. Обновление прошивки может вернуть штатное хранилище — тогда проверяйте состояние заново, не считайте CA установленным навсегда.

## Откат

Сначала верните прежний DNS Станции/профиля Keenetic. Не выключайте единственный DNS до этого шага. Удаление вашего CA требуется не для смены DNS как таковой, а для возврата исходного доверия устройства.

Укажите сохранённый локальный каталог, загрузите значения и проверьте их перед использованием:

```sh
BACKUP=/полный/путь/к/data/station-ca-XXXXXXXX # заменить
REMOTE=$(cat "$BACKUP/remote-path.txt")
ORIGINAL_SHA=$(cat "$BACKUP/original.sha256")
INSTALLED_SHA=$(cat "$BACKUP/installed.sha256")
case "$REMOTE" in *[!A-Za-z0-9/-]*) exit 1 ;; esac
case "$REMOTE" in /data/quasard-ca-????????) ;; *) exit 1 ;; esac
case "$ORIGINAL_SHA:$INSTALLED_SHA" in *[!0-9a-f:]*) exit 1 ;; esac
test "${#ORIGINAL_SHA}" -eq 64 && test "${#INSTALLED_SHA}" -eq 64 || exit 1
```

**Следующая команда восстанавливает системный bundle из backup.** Она откажется затирать более поздние изменения: текущий файл должен совпадать с установленным ранее SHA-256. Если firmware/другой администратор изменили bundle, нужен ручной разбор, а не удаление проверки.

```sh
ssh -p 2222 -o StrictHostKeyChecking=yes "root@$STATION_IP" \
  "sh -s -- '$REMOTE' '$ORIGINAL_SHA' '$INSTALLED_SHA'" <<'SH'
set -eu
d=$1
original=$2
installed=$3
b=/system/vendor/quasar/certs/ca-certificates.crt
check_hash() {
  actual=$(sha256sum "$1")
  actual=${actual%% *}
  test "$actual" = "$2"
}
test -f "$b"
test ! -L "$b"
check_hash "$b" "$installed"
check_hash "$d/original-ca-certificates.crt" "$original"
tmp=
cleanup() {
  rc=$?
  trap - EXIT
  if test -n "$tmp"; then rm -f "$tmp"; fi
  sync
  if ! mount -o remount,ro /; then
    printf '%s\n' 'ОШИБКА: / не вернулся в read-only.' >&2
    exit 1
  fi
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
mount -o remount,rw /
tmp=$(mktemp "$b.restore.XXXXXXXX")
cp "$d/original-ca-certificates.crt" "$tmp"
chown 0:0 "$tmp"
chmod 0644 "$tmp"
check_hash "$tmp" "$original"
check_hash "$b" "$installed"
mv -f "$tmp" "$b"
tmp=
check_hash "$b" "$original"
SH
```

После успешного отката скачайте bundle обратно, сравните с локальной исходной копией, проверьте `ro` и перезагрузите устройство вручную. Backup и приватные ключи не удаляйте автоматически. Для завершения локального теста затем остановите контейнер по [README](../README.md#управление-диагностика-и-откат).
