#!/bin/sh
# Fixed mount initialization only. Runtime graph/supervisor always use uid7474.
set -eu
umask 077
[ "$(id -u)" = 0 ] || { echo 'volume_init_requires_root' >&2; exit 1; }
[ "$#" = 0 ] || { echo 'volume_init_invalid_arguments' >&2; exit 1; }
[ -d /data ] && [ ! -L /data ] || { echo 'volume_init_invalid_mount' >&2; exit 1; }
# Never recursively change or follow existing user-controlled entries. Fresh
# mounts gain one dedicated owner-only directory; existing data stays intact.
if [ -e /data/kb ] || [ -L /data/kb ]; then
 [ -d /data/kb ] && [ ! -L /data/kb ] || exit 1
 [ "$(stat -c %u:%g /data/kb)" = 7474:7474 ] || { echo 'volume_init_existing_owner_mismatch' >&2; exit 1; }
else
 mkdir -m 700 /data/kb
 chown 7474:7474 /data/kb
fi
chmod 700 /data/kb
# /data must be traversable by the runtime owner even on root-owned mounts.
chown 7474:7474 /data
chmod 700 /data
printf '%s\n' volume_initialized
