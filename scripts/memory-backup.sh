#!/usr/bin/env bash
set -euo pipefail
if [[ $# != 1 ]]; then
  printf 'usage: memory-backup.sh BACKUP_ID (run as the graph host runtime UID)\n' >&2
  exit 2
fi
exec kb-supervisor backup --id "$1"
