#!/usr/bin/env bash
set -euo pipefail
if [[ $# != 2 ]]; then
  printf 'usage: memory-restore.sh BACKUP_ID PAUSED_RESTORE_ID (fresh evidence bucket and rotated retrieval key required)\n' >&2
  exit 2
fi
exec kb-supervisor restore --backup-id "$1" --id "$2"
