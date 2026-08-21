#!/usr/bin/env bash
set -euo pipefail

readonly expected_user=kreef
readonly image=ghcr.io/elhefe3/atlas-bridge:edge
readonly source_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly repo_root="$(cd -- "${source_dir}/.." && pwd)"
readonly app_root=/home/kreef/atlas-bridge
readonly secret_root=/home/kreef/.config/atlas-bridge/secrets
readonly quadlet_root=/home/kreef/.config/containers/systemd
readonly atlas_config=/home/kreef/kavita/config
readonly auth_file=/home/kreef/.config/containers/auth.json

[[ "$(id -un)" == "${expected_user}" ]] || { echo "Run as ${expected_user}." >&2; exit 1; }
test -d "${atlas_config}"
mkdir -p "${app_root}/data/torrents" "${app_root}/transmission" "${app_root}/deploy" "${secret_root}" "${quadlet_root}" "${atlas_config}/providers"
chmod 700 "${secret_root}"
mkdir -p "$(dirname -- "${auth_file}")"
if [[ ! -f "${auth_file}" ]]; then
    printf '{"auths":{}}\n' > "${auth_file}"
    chmod 600 "${auth_file}"
fi
if [[ ! -s "${secret_root}/bridge-token" ]]; then
    umask 077
    openssl rand -hex 32 > "${secret_root}/bridge-token"
fi
chmod 600 "${secret_root}/bridge-token"

install -m 0755 "${source_dir}/update-atlas-bridge.sh" "${app_root}/deploy/update-atlas-bridge.sh"
install -m 0644 "${source_dir}/atlas-bridge.network" "${quadlet_root}/atlas-bridge.network"
install -m 0644 "${source_dir}/atlas-bridge.container" "${quadlet_root}/atlas-bridge.container"
install -m 0644 "${source_dir}/transmission.container" "${quadlet_root}/transmission.container"
install -m 0644 "${source_dir}/atlas-bridge-update.service" /home/kreef/.config/systemd/user/atlas-bridge-update.service
install -m 0644 "${source_dir}/atlas-bridge-update.timer" /home/kreef/.config/systemd/user/atlas-bridge-update.timer
install -m 0600 "${repo_root}/manifests/anna.xml" "${atlas_config}/providers/atlas-bridge-anna.xml"
install -m 0600 "${repo_root}/manifests/libgen.xml" "${atlas_config}/providers/atlas-bridge-libgen.xml"

podman pull --authfile "${auth_file}" "${image}"
systemctl --user daemon-reload
systemctl --user enable --now transmission.service
systemctl --user start atlas-bridge.service
systemctl --user enable --now atlas-bridge-update.timer
echo 'Atlas Bridge installed. Configure its generated bridge token in both Atlas provider credential forms.'
echo 'The secret remains at /home/kreef/.config/atlas-bridge/secrets/bridge-token and was not printed.'
