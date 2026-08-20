#!/usr/bin/env bash
set -euo pipefail

readonly image=ghcr.io/elhefe3/atlas-bridge:edge
readonly service=atlas-bridge.service
readonly auth_file=/home/kreef/.config/containers/auth.json
readonly lock_file=/home/kreef/atlas-bridge/deploy/update.lock

exec 9>"${lock_file}"
flock --nonblock 9 || { echo 'Another Atlas Bridge update is running.'; exit 0; }

old_image_id="$(podman image inspect "${image}" --format '{{.Id}}')"
podman pull --authfile "${auth_file}" "${image}"
new_image_id="$(podman image inspect "${image}" --format '{{.Id}}')"
if [[ "${old_image_id}" == "${new_image_id}" ]]; then echo 'Atlas Bridge is current.'; exit 0; fi

if systemctl --user restart "${service}"; then
    for attempt in $(seq 1 30); do
        if [[ "$(podman inspect atlas-bridge --format '{{.State.Health.Status}}' 2>/dev/null)" == healthy ]]; then
            printf 'Updated Atlas Bridge from %s to %s.\n' "${old_image_id}" "${new_image_id}"
            exit 0
        fi
        sleep 2
    done
fi

echo 'Atlas Bridge update failed; restoring previous image.' >&2
systemctl --user stop "${service}" || true
podman untag "${image}" || true
podman tag "${old_image_id}" "${image}"
systemctl --user reset-failed "${service}" || true
systemctl --user start "${service}"
exit 1

