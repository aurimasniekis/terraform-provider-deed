#!/usr/bin/env bash
# Applies examples/homelab with the locally built provider (dev_overrides) and requires an empty second plan
# (terraform plan -detailed-exitcode), then destroys everything.
#
# Against a running Deed: export DEED_ENDPOINT and DEED_TOKEN (an installation admin token) before running it.
# Otherwise a throwaway development `deed serve` is started from bin/deed (make deed-bin): the script completes the
# bootstrap link as a local owner, enrolls TOTP and creates an admin API token.
#
# The provider is used through a dev_overrides CLI configuration in a temporary TF_CLI_CONFIG_FILE (~/.terraformrc is
# never touched); with dev_overrides `terraform init` is not needed.
# Needs: bin/terraform-provider-deed (make build), terraform, python3, curl.
# Adapted from terraform-provider-porch scripts/verify-families.sh (Apache-2.0).
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
deed="${DEED_ACC_BINARY:-$root/bin/deed}"
provider="$root/bin/terraform-provider-deed"
[[ -x "$provider" ]] || { echo "missing $provider (run make build)"; exit 1; }

work=$(mktemp -d)
chmod 700 "$work"
cleanup() {
  if [[ -n "${deed_pid:-}" ]]; then kill "$deed_pid" 2>/dev/null || true; wait "$deed_pid" 2>/dev/null || true; fi
  rm -rf "$work"
}
trap cleanup EXIT

json() { python3 -c 'import json,sys; print(json.load(sys.stdin)[sys.argv[1]])' "$1"; }
free_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'; }

if [[ -n "${DEED_ENDPOINT:-}" && -n "${DEED_TOKEN:-}" ]]; then
  endpoint="$DEED_ENDPOINT"
  token="$DEED_TOKEN"
  echo "Using the running Deed at $endpoint"
else
  [[ -x "$deed" ]] || { echo "missing $deed (run make deed-bin) or set DEED_ENDPOINT and DEED_TOKEN"; exit 1; }
  PUB=$(free_port); MGMT=$(free_port)
  origin="http://127.0.0.1:$PUB"
  cat > "$work/deed.yaml" <<YAML
mode: development
data_dir: $work/data
public_url: $origin
http:
  listen: 127.0.0.1:$PUB
management:
  listen: 127.0.0.1:$MGMT
log:
  format: text
  level: warn
auth:
  local:
    enabled: true
YAML
  # Deed rejects unknown DEED_* variables (the provider's DEED_ENDPOINT/DEED_TOKEN/DEED_SOURCE, CI's DEED_COMMIT, …).
  unset_deed=()
  while IFS= read -r v; do [[ "$v" == DEED_CONFIG ]] || unset_deed+=(-u "$v"); done < <(compgen -e | grep '^DEED_' || true)
  run_deed() { env ${unset_deed[@]+"${unset_deed[@]}"} "$deed" "$@"; }
  out=$(run_deed admin bootstrap -config "$work/deed.yaml" 2>&1) || { echo "deed admin bootstrap failed:"; echo "$out"; exit 1; }
  setup=$(grep -o 'deed_setup_[A-Za-z0-9_-]*' <<<"$out") || { echo "no setup token in output:"; echo "$out"; exit 1; }
  run_deed serve -config "$work/deed.yaml" >"$work/deed.log" 2>&1 &
  deed_pid=$!
  ready=false
  for _ in $(seq 1 100); do curl -sf "http://127.0.0.1:$MGMT/readyz" >/dev/null && { ready=true; break; }; sleep 0.1; done
  $ready || { echo "deed did not become ready:"; cat "$work/deed.log"; exit 1; }

  jar="$work/cookies"
  api() { # method path [json-body]: an owner-session request (Origin + CSRF)
    local args=(-sf -b "$jar" -c "$jar" -X "$1" "$origin/api/v1$2" -H "Origin: $origin" -H 'Content-Type: application/json')
    [[ -n "${csrf:-}" ]] && args+=(-H "X-Deed-CSRF: $csrf")
    [[ $# -ge 3 ]] && args+=(-d "$3")
    curl "${args[@]}"
  }
  csrf=""
  me=$(api POST /auth/setup/complete "{\"token\":\"$setup\",\"email\":\"owner@example.com\",\"display_name\":\"Owner\",\"password\":\"scenario-owner-password\"}")
  csrf=$(json csrf_token <<<"$me")
  secret=$(api POST /me/mfa/totp | json secret)
  code=$(python3 -c '
import base64, hashlib, hmac, struct, sys, time
s = sys.argv[1]; key = base64.b32decode(s + "=" * (-len(s) % 8))
d = hmac.new(key, struct.pack(">Q", int(time.time()) // 30), hashlib.sha1).digest(); o = d[-1] & 15
print("%06d" % ((struct.unpack(">I", d[o:o + 4])[0] & 0x7fffffff) % 1000000))' "$secret")
  api POST /me/mfa/totp/confirm "{\"code\":\"$code\"}" >/dev/null
  csrf=$(api GET /me | json csrf_token)
  token=$(api POST /me/tokens '{"name":"scenario","installation_role":"admin","expires_in_days":1}' | json token)
  endpoint="$origin"
  echo "Started a throwaway Deed at $endpoint"
fi

cat > "$work/dev.tfrc" <<HCL
provider_installation {
  dev_overrides {
    "aurimasniekis/deed" = "$root/bin"
  }
  direct {}
}
HCL
mkdir "$work/tf"
cp "$root"/examples/homelab/*.tf "$work/tf/"
cd "$work/tf"
export TF_CLI_CONFIG_FILE="$work/dev.tfrc" CHECKPOINT_DISABLE=1 TF_IN_AUTOMATION=1
export DEED_ENDPOINT="$endpoint" DEED_TOKEN="$token"
unset DEED_SOURCE
project="homelab-$(python3 -c 'import secrets; print(secrets.token_hex(4))')"
tfvars=(-var "project=$project")

terraform plan -input=false -no-color "${tfvars[@]}" -out=tfplan 2>&1 | grep -E '^(Plan:|Error)' || true
terraform apply -input=false -no-color tfplan | grep -E '^Apply complete'
set +e
terraform plan -input=false -no-color "${tfvars[@]}" -detailed-exitcode >"$work/replan.txt" 2>&1
code=$?
set -e
if [[ $code -ne 0 ]]; then
  echo "plan after apply is not empty (exit $code):"; cat "$work/replan.txt"; exit 1
fi
echo "Plan after apply: no changes."
terraform destroy -auto-approve -input=false -no-color "${tfvars[@]}" >"$work/destroy.txt" 2>&1 || { cat "$work/destroy.txt"; exit 1; }
grep -E '^(Destroy complete|Warning)' "$work/destroy.txt"
