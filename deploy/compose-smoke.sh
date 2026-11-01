#!/bin/sh
# Creates only synthetic local state. Never load real capture or cloud config.
set -eu
umask 077
[ "$#" = 2 ] || { echo 'usage: compose-smoke.sh up|down ABSOLUTE_EMPTY_DIRECTORY' >&2; exit 2; }
action=$1
directory=$2
case "$directory" in /*) ;; *) echo fixture_requires_absolute_directory >&2; exit 2;; esac
repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
[ -d "$directory" ] && [ ! -L "$directory" ] || { echo fixture_requires_existing_directory >&2; exit 2; }
# Refuse shared/readable directories or forged cleanup metadata.
python3 - "$directory" <<'PY'
import os, stat, sys
s=os.lstat(sys.argv[1])
if s.st_uid != os.getuid() or stat.S_IMODE(s.st_mode) & 0o077:
    raise SystemExit('fixture_directory_not_private')
PY
if [ "$action" = up ]; then
 printf '%s\n' fixture_stage=configure
 started=$(date +%s)
 (cd "$repo/server" && go run ../deploy/fixture-config.go "$directory")
elif [ "$action" != down ]; then
 echo fixture_invalid_action >&2; exit 2
fi
project=$(python3 - "$directory/runtime.json" <<'PY'
import json,re,sys
p=json.load(open(sys.argv[1]))['compose_project']
if not re.fullmatch(r'kbq-[0-9a-f]{12}',p): raise SystemExit('invalid fixture identity')
print(p)
PY
)
compose() { docker compose --env-file "$directory/compose.env" -p "$project" -f "$repo/deploy/compose.test.yaml" "$@"; }
compose_bounded() {
 compose_limit=$1
 shift
 python3 -c 'import subprocess,sys
try: result=subprocess.run(sys.argv[2:],timeout=int(sys.argv[1]))
except subprocess.TimeoutExpired: raise SystemExit("fixture_command_timeout_unknown")
raise SystemExit(result.returncode)' "$compose_limit" docker compose --env-file "$directory/compose.env" -p "$project" -f "$repo/deploy/compose.test.yaml" "$@"
}
if [ "$action" = down ]; then
 compose down --volumes --remove-orphans
 printf '%s\n' fixture_stopped
 exit 0
fi
printf '%s\n' fixture_build_started
printf '%s\n' fixture_stage=build
compose build
built=$(date +%s)
printf 'fixture_build_seconds=%s\n' "$((built-started))"
printf '%s\n' fixture_stage=compose_up
compose up -d
printf '%s\n' fixture_stage=bindings
api_binding=$(compose port graph 8080)
object_binding=$(compose port graph 8333)
proxy_binding=$(compose port graph 8335)
python3 - "$directory/runtime.json" "$api_binding" "$object_binding" "$proxy_binding" <<'PYPORT'
import json,os,re,sys
path=sys.argv[1]
state=json.load(open(path))
for key,binding in [('api_url',sys.argv[2]),('object_url',sys.argv[3]),('proxy_url',sys.argv[4])]:
    if not re.fullmatch(r'127\.0\.0\.1:[1-9][0-9]{0,4}',binding):
        raise SystemExit('fixture_loopback_binding_invalid')
    state[key]='http://'+binding
with open(path,'w') as output: json.dump(state,output)
os.chmod(path,0o600)
PYPORT
# Bounded wait for anonymous live endpoint; readiness intentionally stays closed.
api=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["api_url"])' "$directory/runtime.json")
ready=0
printf '%s\n' fixture_stage=api_liveness
for attempt in $(seq 1 120); do
 if curl --silent --fail --max-time 2 "$api/livez" >/dev/null; then ready=1; break; fi
 sleep 2
done
[ "$ready" = 1 ] || { echo fixture_liveness_failed >&2; exit 1; }
# The proxy is a test-only boundary, with its control port exposed on loopback.
proxy_url=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["proxy_url"])' "$directory/runtime.json")
proxy_ready=0
printf '%s\n' fixture_stage=proxy_liveness
for probe in $(seq 1 120); do
 if curl --silent --fail --max-time 2 "$proxy_url/livez" >/dev/null; then proxy_ready=1; break; fi
 sleep 2
done
[ "$proxy_ready" = 1 ] || { echo fixture_proxy_readiness_failed >&2; exit 1; }
# A live API deliberately does not establish object or graph readiness.
object_url=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["object_url"])' "$directory/runtime.json")
object_ready=0
printf '%s\n' fixture_stage=object_readiness
for probe in $(seq 1 120); do
 object_status=$(curl --silent --max-time 2 --output /dev/null --write-out '%{http_code}' "$object_url") || object_status=000
 if [ "$object_status" = 403 ]; then object_ready=1; break; fi
 sleep 2
done
[ "$object_ready" = 1 ] || { echo fixture_object_readiness_failed >&2; exit 1; }
# Create only fixed fresh fixture buckets through the local operator shell.
# Timeout is an explicit unknown result, never an automatic mutation retry.
printf '%s\n' fixture_stage=bucket_create
python3 - "$directory" "$project" "$repo" <<'PYBUCKET'
import pathlib, subprocess, sys
root,project,repo=map(pathlib.Path,sys.argv[1:])
command=['docker','compose','--env-file',str(root/'compose.env'),'-p',str(project),'-f',str(repo/'deploy/compose.test.yaml'),'exec','-T','objects','weed','shell']
commands=''.join('s3.bucket.create -name '+name+'\n' for name in ['evidence','journal','backups','restored-evidence'])
with open(root/'bucket-init.log','wb') as output:
    try: result=subprocess.run(command,input=commands.encode(),stdout=output,stderr=subprocess.STDOUT,timeout=60)
    except subprocess.TimeoutExpired: raise SystemExit('fixture_bucket_init_unknown')
if result.returncode: raise SystemExit('fixture_bucket_init_failed')
PYBUCKET
printf '%s\n' fixture_stage=bucket_verify
(cd "$repo/server" && go run ../deploy/fixture-config.go buckets "$directory")
public_key=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["owner_public_key"])' "$directory/runtime.json")
bootstrap_deadline=$(( $(date +%s) + 600 ))
wait_host_ready() {
 for host_probe in $(seq 1 120); do
  [ "$(date +%s)" -lt "$bootstrap_deadline" ] || break
  if compose_bounded 10 exec -T graph /usr/local/bin/kb-runtime-entrypoint /usr/local/bin/kb-supervisor status > "$directory/host-status.json" 2> "$directory/host-status.stderr"; then
   if python3 - "$directory/host-status.json" <<'PYHOST'
import json,sys
try:
    response=json.load(open(sys.argv[1])); status=response['Status']; maintenance=status['Maintenance']
    ready=response.get('Version')==1 and response.get('Code')=='ok' and status.get('HostReady') is True and maintenance['active']==0 and maintenance['unknown']==0 and maintenance['phase'] in ('bootstrap','recovering','open')
except (ValueError,KeyError,TypeError): ready=False
raise SystemExit(0 if ready else 1)
PYHOST
   then return 0; fi
  fi
  sleep 2
 done
 echo fixture_host_bootstrap_wait_failed >&2
 return 1
}
run_host() {
 host_operation=$1
 host_input=$2
 shift 2
 for host_attempt in $(seq 1 120); do
  wait_host_ready || return 1
  # Reopen the exact input for every attempt: validation may consume it before
  # an admission refusal. Preserve private diagnostics without printing them.
  if "$@" < "$host_input" > "$directory/$host_operation.stdout" 2> "$directory/$host_operation.stderr"; then return 0; fi
  if [ -s "$directory/$host_operation.stdout" ]; then echo fixture_host_result_unknown >&2; return 1; fi
  if ! python3 - "$directory/$host_operation.stderr" "$host_operation" <<'PYRETRY'
import pathlib,sys
expected=sys.argv[2]+': maintenance: admission refused; retry after the current maintenance operation completes\n'
raise SystemExit(0 if pathlib.Path(sys.argv[1]).read_text()==expected else 1)
PYRETRY
  then echo fixture_host_operation_failed_no_retry >&2; return 1; fi
  sleep 2
 done
 echo fixture_host_admission_wait_failed >&2
 return 1
}
printf '%s\n' fixture_bootstrap_started
# Append to the protected sink. A known pre-admission refusal can retry safely;
# unknown/setup-delivery failures stop immediately without truncating its bytes.
printf '%s\n' fixture_stage=host_setup
run_host setup /dev/null compose_bounded 180 exec -T graph /usr/local/bin/kb-runtime-entrypoint /bin/sh -c 'umask 077; if [ ! -e /data/kb/bootstrap-credential ]; then (set -C; : > /data/kb/bootstrap-credential); fi; [ -f /data/kb/bootstrap-credential ] && [ ! -L /data/kb/bootstrap-credential ] || exit 1; exec 3>>/data/kb/bootstrap-credential; exec /usr/local/bin/kb-admin setup --owner-id fixture-owner --owner-public-key "$1" --project fixture-project --credential-fd 3' sh "$public_key"
printf '%s\n' fixture_stage=host_journal_init
run_host journal-init /dev/null compose_bounded 180 exec -T graph /usr/local/bin/kb-runtime-entrypoint /usr/local/bin/kb-admin journal-init
printf '%s\n' fixture_stage=host_policy_update
run_host policy-update "$directory/policy.json" compose_bounded 180 exec -T graph /usr/local/bin/kb-runtime-entrypoint /usr/local/bin/kb-admin policy-update
# Docker cp transfers the protected credential without stdout. A private
# directory prevents the temporary original UID file being exposed to others.
printf '%s\n' fixture_stage=credential_transfer
compose cp graph:/data/kb/bootstrap-credential "$directory/credential"
chmod 600 "$directory/credential"
compose exec -T graph /bin/sh -c 'rm -f /data/kb/bootstrap-credential'
ready=0
printf '%s\n' fixture_stage=api_readiness
for attempt in $(seq 1 120); do
 if curl --silent --fail --max-time 2 "$api/readyz" >/dev/null; then ready=1; break; fi
 sleep 2
done
[ "$ready" = 1 ] || { echo fixture_readiness_failed_inspect_private_status >&2; exit 1; }
finished=$(date +%s)
printf 'fixture_startup_seconds=%s\n' "$((finished-built))"
printf '%s\n' fixture_bootstrap_ready
printf '%s\n' fixture_stage=complete
