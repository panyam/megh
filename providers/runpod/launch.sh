#!/usr/bin/env bash
# megh RunPod backend.
#
# Creates a CPU pod from a megh env image, attaches an existing network volume,
# and exposes the web shell / noVNC / SSH ports. This is the concrete backend
# behind `megh up --provider runpod`. The abstraction seam is the megh CLI, not
# this script; a second provider is a sibling file, not a rewrite.
#
# RunPod's own Terraform provider is currently too flaky to depend on, so this
# talks to the REST API directly:  POST https://rest.runpod.io/v1/pods
#
# Prerequisites you set once (see SETUP.md):
#   RUNPOD_API_KEY   - account API key
#   MEGH_IMAGE       - e.g. ghcr.io/<you>/megh-base:latest
#   MEGH_PUBKEY      - your SSH public key (for agent-forwarded git, no secrets on box)
#   MEGH_VOLUME_ID   - id of a network volume you created in the RunPod console
#   MEGH_DC          - data center id the volume lives in, e.g. US-KS-2 / us-east
set -euo pipefail

: "${RUNPOD_API_KEY:?set RUNPOD_API_KEY}"
: "${MEGH_IMAGE:?set MEGH_IMAGE (e.g. ghcr.io/you/megh-base:latest)}"
: "${MEGH_VOLUME_ID:?set MEGH_VOLUME_ID (create a network volume first)}"
: "${MEGH_DC:?set MEGH_DC (data center id of the volume)}"

NAME="${MEGH_NAME:-megh-$(id -un)-box}"
CPU_COUNT="${MEGH_VCPU:-4}"
MEM_GB="${MEGH_RAM:-16}"
DISK_GB="${MEGH_DISK:-100}"
PUBKEY="${MEGH_PUBKEY:-}"

# Ports: SSH over raw TCP, the two web surfaces over the HTTP proxy.
PORTS="22/tcp,7681/http,6080/http"

# Build the request body. WORK_MOUNT tells the entrypoint where the volume
# landed; RunPod mounts network volumes at /workspace.
read -r -d '' BODY <<JSON || true
{
  "name": "${NAME}",
  "imageName": "${MEGH_IMAGE}",
  "cpuCount": ${CPU_COUNT},
  "memoryInGb": ${MEM_GB},
  "containerDiskInGb": ${DISK_GB},
  "networkVolumeId": "${MEGH_VOLUME_ID}",
  "dataCenterId": "${MEGH_DC}",
  "ports": "${PORTS}",
  "env": [
    {"key": "PUBLIC_KEY", "value": "${PUBKEY}"},
    {"key": "WORK_MOUNT", "value": "/workspace"},
    {"key": "ARCH_TAG", "value": "x86_64"}
  ]
}
JSON

echo "[megh] creating RunPod pod '${NAME}' (${CPU_COUNT} vCPU / ${MEM_GB} GB) ..." >&2

RESP="$(curl -fsS -X POST "https://rest.runpod.io/v1/pods" \
  -H "Authorization: Bearer ${RUNPOD_API_KEY}" \
  -H "Content-Type: application/json" \
  -d "${BODY}")"

POD_ID="$(echo "${RESP}" | jq -r '.id // .pod.id // empty')"
if [ -z "${POD_ID}" ]; then
  echo "[megh] could not parse pod id from response:" >&2
  echo "${RESP}" | jq . >&2 || echo "${RESP}" >&2
  exit 1
fi

cat <<EOF
[megh] pod created: ${POD_ID}

  web shell : https://${POD_ID}-7681.proxy.runpod.net
  headed vnc: https://${POD_ID}-6080.proxy.runpod.net/vnc.html
  ssh       : read the TCP mapping from the RunPod console (Connect > TCP),
              then: ssh -A root@<ip> -p <mapped-port>

The box may take a minute to pull the image and start services. Re-open the web
shell URL if it 502s on the first try.
EOF
