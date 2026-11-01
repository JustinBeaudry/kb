#!/bin/sh
# Synthetic fixture only: preserve the host's 0600 config across different UIDs.
# Stage it in private tmpfs before the original entrypoint drops to seaweed.
set -eu
umask 077
source=/run/fixture-source/s3.json
runtime=/run/fixture
[ "$(id -u)" = 0 ] && [ -f "$source" ] && [ ! -L "$source" ] || {
 echo fixture_objects_config_unavailable >&2
 exit 1
}
mkdir -p "$runtime"
chmod 0700 "$runtime"
cp "$source" "$runtime/s3.json"
chmod 0600 "$runtime/s3.json"
chown seaweed:seaweed "$runtime" "$runtime/s3.json"
exec /entrypoint.sh "$@"
