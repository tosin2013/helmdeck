#!/bin/sh
# Garage bootstrap (ADR 031, T211a).
#
# Brings a fresh Garage daemon up to a working state for helmdeck:
#
#   1. Wait until the daemon answers `garage status`.
#   2. Assign + apply a single-node layout if no role is set yet.
#   3. Wait until the cluster reports Healthy.
#   4. Create the helmdeck-artifacts bucket if it does not exist.
#   5. Create a key named helmdeck-control-plane if it does not exist
#      and grant it read/write on the bucket.
#   6. Write the resulting access key + secret key to a shared volume
#      so the control-plane container can pick them up via
#      HELMDECK_S3_*_FILE env vars.
#
# Every step is idempotent — re-running this script against an already
# bootstrapped cluster is a no-op that just refreshes the credential
# files. Compose runs it as a one-shot service via
# `service_completed_successfully`, so the control plane only starts
# after credentials are on disk.

set -eu

GARAGE="garage -c /etc/garage.toml"
CRED_DIR="${CRED_DIR:-/credentials}"
BUCKET="${HELMDECK_S3_BUCKET:-helmdeck-artifacts}"
KEY_NAME="${HELMDECK_S3_KEY_NAME:-helmdeck-control-plane}"
ZONE="${GARAGE_ZONE:-dc1}"
CAPACITY="${GARAGE_CAPACITY:-1G}"

mkdir -p "${CRED_DIR}"

log() { echo "garage-init: $*"; }

# 1. Wait for the daemon.
log "waiting for garage daemon"
i=0
until $GARAGE status >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -gt 60 ]; then
    log "garage never answered; aborting"
    exit 1
  fi
  sleep 1
done

# 2. Layout assign + apply (only when NO ROLE is set).
if $GARAGE status 2>/dev/null | grep -q "NO ROLE"; then
  NODE_ID=$($GARAGE node id -q 2>/dev/null | cut -d@ -f1)
  if [ -z "$NODE_ID" ]; then
    log "could not read node id"
    exit 1
  fi
  log "assigning layout to node $NODE_ID"
  $GARAGE layout assign -z "$ZONE" -c "$CAPACITY" "$NODE_ID"
  $GARAGE layout apply --version 1
fi

# 3. Wait for Healthy.
log "waiting for cluster Healthy state"
i=0
until $GARAGE status 2>/dev/null | grep -qE "(HEALTHY|Healthy)"; do
  i=$((i + 1))
  if [ "$i" -gt 60 ]; then
    log "cluster never reported healthy; current status:"
    $GARAGE status || true
    exit 1
  fi
  sleep 1
done

# 4. Bucket.
if ! $GARAGE bucket info "$BUCKET" >/dev/null 2>&1; then
  log "creating bucket $BUCKET"
  $GARAGE bucket create "$BUCKET"
fi

# 5. Key + grant.
if ! $GARAGE key info "$KEY_NAME" >/dev/null 2>&1; then
  log "creating key $KEY_NAME"
  $GARAGE key create "$KEY_NAME"
  $GARAGE bucket allow --read --write --owner "$BUCKET" --key "$KEY_NAME"
fi

# 6. Export credentials. `garage key info --show-secret` is human-formatted;
# parse the two relevant lines. Tested against Garage v2.x output:
#
#     Key name: helmdeck-control-plane
#     Key ID: GK<24 hex>
#     Secret key: <64 hex>
#
INFO=$($GARAGE key info "$KEY_NAME" --show-secret)
ACCESS_KEY=$(echo "$INFO" | awk -F': *' '/^Key ID:/ {print $2; exit}')
SECRET_KEY=$(echo "$INFO" | awk -F': *' '/^Secret key:/ {print $2; exit}')

if [ -z "$ACCESS_KEY" ] || [ -z "$SECRET_KEY" ]; then
  log "could not parse access/secret from key info; output was:"
  echo "$INFO"
  exit 1
fi

printf '%s' "$ACCESS_KEY" > "${CRED_DIR}/access_key"
printf '%s' "$SECRET_KEY" > "${CRED_DIR}/secret_key"
printf '%s' "$BUCKET"     > "${CRED_DIR}/bucket"
printf '%s' "http://garage:3900" > "${CRED_DIR}/endpoint"

# Permissions: any non-root user in the same compose project must be
# able to read these. Compose volume defaults are usually fine, but
# tighten just enough that they're readable but not group-writable.
chmod 0644 "${CRED_DIR}"/*

log "bootstrap complete (bucket=$BUCKET key=$ACCESS_KEY)"
