#!/bin/sh
# Fixed materialization from sealed runtime variables; never source/eval input.
set -eu
umask 077
[ "$(id -u)" = 7474 ] || { echo runtime_uid_invalid >&2; exit 1; }
[ -d /run/kb ] && [ ! -L /run/kb ] || exit 1
[ "$(stat -c %u:%a /run/kb)" = 7474:700 ] || { echo runtime_secret_directory_invalid >&2; exit 1; }
materialize() {
 variable=$1
 contents=$2
 path=$3
 configured=$4
 [ -n "$contents" ] || return 0
 [ -z "$configured" ] || { echo runtime_secret_configuration_conflict >&2; exit 1; }
 staged=$(mktemp /run/kb/.secret.XXXXXXXX)
 printf '%s' "$contents" > "$staged"
 chmod 600 "$staged"
 mv -f "$staged" "$path"
 export "$variable=$path"
}
materialize KB_GRAPH_SECRET_FILE "${KB_GRAPH_SECRET_CONTENTS:-}" /run/kb/graph-secret "${KB_GRAPH_SECRET_FILE:-}"
materialize KB_MAINTENANCE_SECRET_FILE "${KB_MAINTENANCE_SECRET_CONTENTS:-}" /run/kb/maintenance-secret "${KB_MAINTENANCE_SECRET_FILE:-}"
materialize KB_JOURNAL_KEYRING_FILE "${KB_JOURNAL_KEYRING_CONTENTS:-}" /run/kb/journal-keyring "${KB_JOURNAL_KEYRING_FILE:-}"
materialize KB_S3_ACCESS_KEY_FILE "${KB_S3_ACCESS_KEY_CONTENTS:-}" /run/kb/s3-access-key "${KB_S3_ACCESS_KEY_FILE:-}"
materialize KB_S3_SECRET_KEY_FILE "${KB_S3_SECRET_KEY_CONTENTS:-}" /run/kb/s3-secret-key "${KB_S3_SECRET_KEY_FILE:-}"
materialize KB_JOURNAL_ACCESS_KEY_FILE "${KB_JOURNAL_ACCESS_KEY_CONTENTS:-}" /run/kb/journal-access-key "${KB_JOURNAL_ACCESS_KEY_FILE:-}"
materialize KB_JOURNAL_SECRET_KEY_FILE "${KB_JOURNAL_SECRET_KEY_CONTENTS:-}" /run/kb/journal-secret-key "${KB_JOURNAL_SECRET_KEY_FILE:-}"
materialize KB_BACKUP_KEYRING_FILE "${KB_BACKUP_KEYRING_CONTENTS:-}" /run/kb/backup-keyring "${KB_BACKUP_KEYRING_FILE:-}"
materialize KB_BACKUP_ACCESS_KEY_FILE "${KB_BACKUP_ACCESS_KEY_CONTENTS:-}" /run/kb/backup-access-key "${KB_BACKUP_ACCESS_KEY_FILE:-}"
materialize KB_BACKUP_SECRET_KEY_FILE "${KB_BACKUP_SECRET_KEY_CONTENTS:-}" /run/kb/backup-secret-key "${KB_BACKUP_SECRET_KEY_FILE:-}"
materialize KB_RETRIEVAL_HMAC_KEY_FILE "${KB_RETRIEVAL_HMAC_KEY_CONTENTS:-}" /run/kb/retrieval-hmac "${KB_RETRIEVAL_HMAC_KEY_FILE:-}"
unset KB_RETRIEVAL_HMAC_KEY_CONTENTS
unset KB_BACKUP_KEYRING_CONTENTS KB_BACKUP_ACCESS_KEY_CONTENTS KB_BACKUP_SECRET_KEY_CONTENTS
unset KB_GRAPH_SECRET_CONTENTS KB_MAINTENANCE_SECRET_CONTENTS KB_JOURNAL_KEYRING_CONTENTS KB_S3_ACCESS_KEY_CONTENTS KB_S3_SECRET_KEY_CONTENTS KB_JOURNAL_ACCESS_KEY_CONTENTS KB_JOURNAL_SECRET_KEY_CONTENTS
unset contents configured
exec "$@"
