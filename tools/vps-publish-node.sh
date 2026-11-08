#!/usr/bin/env bash
# Публикация программы участника в релизный фид платформы.
# Запускается на VPS после того, как бинарники уже лежат в /tmp как *.new.
# Каталог /opt/compute-control/node раздаётся nginx по адресу /node/.
set -euo pipefail

VERSION="${VERSION:?нужна версия, например VERSION=0.3.21}"
DIR=/opt/compute-control/node
EXE=/tmp/compute-node.exe.new
LINUX=/tmp/compute-node-linux.new

test -f "$EXE"
test -f "$LINUX"

# Хеши считаются по загруженным файлам, а не берутся со слов сборки.
EXE_SHA=$(sha256sum "$EXE" | cut -d' ' -f1)
LINUX_SHA=$(sha256sum "$LINUX" | cut -d' ' -f1)
EXE_BYTES=$(stat -c %s "$EXE")
LINUX_BYTES=$(stat -c %s "$LINUX")

echo "версия $VERSION"
echo "windows $EXE_BYTES байт $EXE_SHA"
echo "linux   $LINUX_BYTES байт $LINUX_SHA"

# 1. Сначала версии с номером в имени: если публикация сорвётся,
#    фид останется прежним и участники ничего не заметят.
install -m 644 "$EXE" "$DIR/compute-node-$VERSION.exe"
install -m 644 "$LINUX" "$DIR/compute-node-linux-$VERSION"

# 2. Затем файлы, на которые указывает фид.
install -m 644 "$EXE" "$DIR/compute-node.exe"
install -m 644 "$LINUX" "$DIR/compute-node-linux"

# 3. Фиды пишутся последними и через временный файл: читатель видит
#    либо старую запись, либо новую, но никогда половину.
write_json() {
  local target=$1 content=$2
  printf '%s' "$content" > "$target.tmp"
  chmod 644 "$target.tmp"
  mv "$target.tmp" "$target"
}

write_json "$DIR/release.json" \
  "{\"version\":\"$VERSION\",\"url\":\"https://api.nshno.tech/node/compute-node.exe\",\"sha256\":\"$EXE_SHA\",\"bytes\":$EXE_BYTES}"

write_json "$DIR/release-linux.json" \
  "{\"version\":\"$VERSION\",\"url\":\"https://api.nshno.tech/node/compute-node-linux\",\"sha256\":\"$LINUX_SHA\",\"bytes\":$LINUX_BYTES}"

echo "--- release.json ---"
cat "$DIR/release.json"; echo
echo "--- release-linux.json ---"
cat "$DIR/release-linux.json"; echo

# 4. Проверка через тот же адрес, который видят участники.
echo "--- проверка фида по https ---"
curl -sS --max-time 20 https://api.nshno.tech/node/release.json; echo
curl -sS --max-time 20 https://api.nshno.tech/node/release-linux.json; echo

# 5. Сверка: скачанный по ссылке файл совпадает по размеру и хешу.
echo "--- сверка скачанного файла ---"
curl -sS --max-time 120 -o /tmp/check-node.exe https://api.nshno.tech/node/compute-node.exe
stat -c '%s байт' /tmp/check-node.exe
sha256sum /tmp/check-node.exe
rm -f /tmp/check-node.exe
