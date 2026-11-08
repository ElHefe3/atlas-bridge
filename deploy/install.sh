#!/usr/bin/env bash
# Install or reconcile Atlas Bridge on the library host. Run as root.
#
#   sudo bash deploy/install.sh               # check: render and compare, change nothing
#   sudo bash deploy/install.sh --apply       # back up, install, verify, roll back on failure
#   sudo bash <backup>/install.sh --restore <backup>   # return to a backup
#
# Only Atlas Bridge's own files are managed: its Quadlets, update/ingest units,
# the updater and its compatibility entry point. Kavita's definitions, data,
# secrets and images are never modified. Live Image= lines are kept, so applying
# never changes which image runs.
set -Eeuo pipefail
umask 077

readonly source_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly repo_root="$(cd -- "${source_dir}/.." && pwd)"
# shellcheck source=library.env
. "${source_dir}/library.env"

readonly quadlets="${LIBRARY_HOME}/.config/containers/systemd"
readonly units="${LIBRARY_HOME}/.config/systemd/user"
readonly deploy_dir="${LIBRARY_ROOT}/atlas-bridge/deploy"
readonly lock_file="${deploy_dir}/update.lock"
readonly marker="${deploy_dir}/lab-update-recovery.json"
readonly backup_root="${LIBRARY_HOME}/deploy-backups"
readonly shim="${deploy_dir}/update-atlas-bridge.sh"
readonly remote_image=ghcr.io/elhefe3/atlas-bridge:edge

# destination|template|owner(account/root)|mode
managed() {
    printf '%s\n' \
        "${quadlets}/atlas-bridge.network|atlas-bridge.network|account|0600" \
        "${quadlets}/transmission.container|transmission.container|account|0600" \
        "${quadlets}/atlas-bridge.container|atlas-bridge.container|account|0600" \
        "${units}/atlas-bridge-update.service|atlas-bridge-update.service|account|0600" \
        "${units}/atlas-bridge-update.timer|atlas-bridge-update.timer|account|0600" \
        "${units}/lab-atlas-update-recover.service|lab-atlas-update-recover.service|account|0600" \
        "${units}/atlas-bridge-ingest-start.service|atlas-bridge-ingest-start.service|account|0600" \
        "${units}/atlas-bridge-ingest-start.timer|atlas-bridge-ingest-start.timer|account|0600" \
        "${units}/atlas-bridge-ingest-stop.service|atlas-bridge-ingest-stop.service|account|0600" \
        "${units}/atlas-bridge-ingest-stop.timer|atlas-bridge-ingest-stop.timer|account|0600" \
        "${LIBRARY_UPDATER}|atlas-bridge-updater.py|root|0644" \
        "${shim}|@shim|account|0700"
}
readonly state_units=(atlas-bridge-update.timer lab-atlas-update-recover.service atlas-bridge-ingest-start.timer atlas-bridge-ingest-stop.timer)

say() { printf '%s\n' "$*"; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

preflight() {
    [[ ${EUID} -eq 0 ]] || die 'run as root (sudo).'
    [[ "$(uname -n)" == "${LIBRARY_HOST}" ]] || die "this host is $(uname -n), expected ${LIBRARY_HOST}."
    account_uid="$(id -u "${LIBRARY_ACCOUNT}")" || die "account ${LIBRARY_ACCOUNT} is missing."
    account_gid="$(id -g "${LIBRARY_ACCOUNT}")"
    [[ -d "${LIBRARY_ROOT}/" ]] || die "${LIBRARY_ROOT} is missing; is the secondary SSD mounted?"
    [[ -S "/run/user/${account_uid}/bus" ]] || die "the ${LIBRARY_ACCOUNT} user manager is not running."
    for tool in runuser flock curl sha256sum podman; do command -v "${tool}" > /dev/null || die "${tool} is required."; done
}

as_account() {
    runuser -u "${LIBRARY_ACCOUNT}" -- env --chdir="${LIBRARY_HOME}" HOME="${LIBRARY_HOME}" \
        XDG_RUNTIME_DIR="/run/user/${account_uid}" \
        DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/${account_uid}/bus" "$@"
}

render() { # template destination -> stdout
    local template=$1 dest=$2 live_image
    if [[ ${template} == @shim ]]; then
        printf '#!/bin/sh\nexec /usr/bin/python3 -I %s update\n' "${LIBRARY_UPDATER}"
        return
    fi
    sed -e "s|@LIBRARY_ROOT@|${LIBRARY_ROOT}|g" -e "s|@LIBRARY_HOME@|${LIBRARY_HOME}|g" \
        -e "s|@LIBRARY_UID@|${account_uid}|g" -e "s|@LIBRARY_HOST@|${LIBRARY_HOST}|g" \
        -e "s|@LIBRARY_UPDATER@|${LIBRARY_UPDATER}|g" "${source_dir}/${template}" | tr -d '\r' > "${work}/render"
    if [[ ${template} == *.container && -f ${dest} ]]; then
        live_image="$(grep -c '^Image=' "${dest}" || true)"
        [[ ${live_image} == 1 ]] || die "${dest} does not have exactly one Image= line."
        live_image="$(grep '^Image=' "${dest}" | tr -d '\r')"
        awk -v img="${live_image}" '/^Image=/{print img; next} {print}' "${work}/render"
    else
        cat "${work}/render"
    fi
    grep -q '@[A-Z_]*@' "${work}/render" && die "unrendered placeholder in ${template}."
    return 0
}

normal() { tr -d '\r' < "$1" | sed -e 's/[[:space:]]*$//' -e '/^[[:space:]]*[#;]/d' -e '/^$/d'; }

# Fills ${work}/new/<n>, and change[<n>] = same | cosmetic | changed | new
compare_all() {
    local n=0 dest template owner mode
    mkdir -p "${work}/new"
    while IFS='|' read -r dest template owner mode; do
        n=$((n + 1))
        render "${template}" "${dest}" > "${work}/new/${n}"
        if [[ ! -e ${dest} ]]; then change[n]=new
        elif cmp -s "${dest}" "${work}/new/${n}"; then change[n]=same
        elif cmp -s <(normal "${dest}") <(normal "${work}/new/${n}"); then change[n]=cosmetic
        else change[n]=changed; fi
    done < <(managed)
}

report() {
    local n=0 dest template owner mode
    while IFS='|' read -r dest template owner mode; do
        n=$((n + 1))
        printf '  %-9s %s\n' "${change[n]}" "${dest}"
        if [[ ${change[n]} == changed ]]; then diff -u "${dest}" "${work}/new/${n}" | sed 's/^/      /' || true; fi
    done < <(managed)
    for name in anna libgen; do
        local live="${LIBRARY_ROOT}/kavita/config/providers/atlas-bridge-${name}.xml"
        if [[ ! -f ${live} ]]; then say "  (manifest ${name}: not installed)"
        elif cmp -s <(tr -d '\r' < "${live}") <(tr -d '\r' < "${repo_root}/manifests/${name}.xml"); then say "  (manifest ${name}: matches repo)"
        else say "  (manifest ${name}: differs from repo; left unchanged)"; fi
    done
}

kind_changed() { # name of the managed file -> true when it changed in substance
    local n=0 dest template owner mode
    while IFS='|' read -r dest template owner mode; do
        n=$((n + 1))
        if [[ ${template} == "$1" && ${change[n]} =~ ^(changed|new)$ ]]; then return 0; fi
    done < <(managed)
    return 1
}

health() { # -> 0 when the bridge, Transmission and Kavita all answer
    local deadline=$((SECONDS + ${1:-150}))
    while (( SECONDS < deadline )); do
        if [[ "$(as_account podman inspect atlas-bridge --format '{{.State.Health.Status}}' 2>/dev/null)" == healthy &&
              "$(as_account podman inspect atlas-transmission --format '{{.State.Health.Status}}' 2>/dev/null)" == healthy ]] &&
           curl -fsS --noproxy '*' -o /dev/null --max-time 5 "http://127.0.0.1:${LIBRARY_KAVITA_PORT}/api/health"; then
            return 0
        fi
        sleep 3
    done
    return 1
}

take_lock() {
    [[ -f ${lock_file} ]] || die "${lock_file} is missing."
    exec 8<"${lock_file}"
    say 'Waiting for the update/backup lock...'
    flock -w 900 8 || die 'the update/backup lock is still held after 15 minutes.'
    [[ ! -e ${marker} ]] || die "an interrupted image update awaits recovery (${marker})."
}

install_file() { # source destination owner mode
    local tmp
    mkdir -p "$(dirname -- "$2")"
    tmp="$(mktemp "$(dirname -- "$2")/.atlas-install.XXXXXX")"
    cat "$1" > "${tmp}"
    if [[ $3 == root ]]; then chown root:root "${tmp}"; else chown "${account_uid}:${account_gid}" "${tmp}"; fi
    chmod "$4" "${tmp}"
    mv -f "${tmp}" "$2"
}

backup() {
    local stamp dest template owner mode
    stamp="$(date -u +%Y%m%dT%H%M%SZ)"
    backup_dir="${backup_root}/${stamp}"
    install -d -m 0700 -o root -g root "${backup_root}" "${backup_dir}" "${backup_dir}/files"
    : > "${backup_dir}/manifest"
    while IFS='|' read -r dest template owner mode; do
        if [[ -e ${dest} ]]; then
            cp -a --parents "${dest}" "${backup_dir}/files/"
            printf 'present|%s|%s\n' "${dest}" "$(sha256sum < "${dest}" | cut -d' ' -f1)" >> "${backup_dir}/manifest"
        else
            printf 'absent|%s|\n' "${dest}" >> "${backup_dir}/manifest"
        fi
    done < <(managed)
    for unit in "${state_units[@]}"; do
        printf '%s|%s\n' "${unit}" "$(as_account systemctl --user is-enabled "${unit}" 2>/dev/null || true)"
    done > "${backup_dir}/unit-states"
    { as_account podman inspect atlas-bridge --format 'atlas-bridge {{.ImageName}} {{.Image}}' || true
      as_account podman inspect atlas-transmission --format 'atlas-transmission {{.ImageName}} {{.Image}}' || true
    } > "${backup_dir}/images" 2>/dev/null
    tar -C "${LIBRARY_HOME}" -cf "${backup_dir}/full-config.tar" .config/containers/systemd .config/systemd/user
    cp "${source_dir}/install.sh" "${source_dir}/library.env" "${backup_dir}/"
    printf '#!/usr/bin/env bash\nexec bash %q --restore %q\n' "${backup_dir}/install.sh" "${backup_dir}" > "${backup_dir}/restore.sh"
    chmod 0700 "${backup_dir}/restore.sh"
    say "Backup: ${backup_dir}"
}

restore() { # backup directory
    local dir=$1 state dest sum unit enabled
    [[ -f ${dir}/manifest ]] || die "${dir} is not an Atlas deploy backup."
    while IFS='|' read -r state dest sum; do
        if [[ ${state} == present ]]; then
            cp -a "${dir}/files${dest}" "${dest}"
        else
            rm -f -- "${dest}"
        fi
    done < "${dir}/manifest"
    as_account systemctl --user daemon-reload
    while IFS='|' read -r unit enabled; do
        case ${enabled} in
            enabled) as_account systemctl --user enable "${unit}" >/dev/null 2>&1 || true ;;
            disabled) as_account systemctl --user disable "${unit}" >/dev/null 2>&1 || true ;;
        esac
    done < "${dir}/unit-states"
    as_account systemctl --user reset-failed transmission.service atlas-bridge.service 2>/dev/null || true
    as_account systemctl --user restart transmission.service
    as_account systemctl --user restart atlas-bridge.service
    as_account systemctl --user start atlas-bridge-update.timer 2>/dev/null || true
    if health; then say "Restored ${dir}; bridge, Transmission and Kavita are healthy."
    else die "restored ${dir} but health checks did not pass; see journalctl --user -u atlas-bridge.service as ${LIBRARY_ACCOUNT}."; fi
    say "Images at backup time (not changed by restore): $(tr '\n' ';' < "${dir}/images")"
}

bootstrap() { # creates only what is missing; never overwrites
    local secrets="${LIBRARY_ROOT}/.config/atlas-bridge/secrets" auth="${LIBRARY_HOME}/.config/containers/auth.json"
    install -d -m 0700 -o "${account_uid}" -g "${account_gid}" "${deploy_dir}" "${secrets}" \
        "${LIBRARY_ROOT}/atlas-bridge/data/torrents" "${LIBRARY_ROOT}/atlas-bridge/transmission"
    [[ -e ${lock_file} ]] || install -m 0600 -o "${account_uid}" -g "${account_gid}" /dev/null "${lock_file}"
    if [[ ! -s ${secrets}/bridge-token ]]; then
        openssl rand -hex 32 > "${secrets}/bridge-token"
        chown "${account_uid}:${account_gid}" "${secrets}/bridge-token"; chmod 0600 "${secrets}/bridge-token"
        say "Generated a bridge token at ${secrets}/bridge-token (not printed). Enter it in Kavita Atlas's provider credentials."
    fi
    if [[ ! -e ${auth} ]]; then
        install -d -m 0700 -o "${account_uid}" -g "${account_gid}" "$(dirname -- "${auth}")"
        printf '{"auths":{}}\n' > "${auth}"; chown "${account_uid}:${account_gid}" "${auth}"; chmod 0600 "${auth}"
    fi
    if [[ -d ${LIBRARY_ROOT}/kavita/config ]]; then
        for name in anna libgen; do
            local live="${LIBRARY_ROOT}/kavita/config/providers/atlas-bridge-${name}.xml"
            [[ -e ${live} ]] || install -D -m 0600 -o "${account_uid}" -g "${account_gid}" "${repo_root}/manifests/${name}.xml" "${live}"
        done
    fi
    if ! as_account podman image exists localhost/atlas-bridge:current 2>/dev/null &&
       ! [[ -f ${quadlets}/atlas-bridge.container ]]; then
        as_account podman pull --authfile "${auth}" "${remote_image}"
        as_account podman tag "${remote_image}" localhost/atlas-bridge:current
        as_account podman pull docker.io/linuxserver/transmission:4.0.6
    fi
}

apply() {
    local n dest template owner mode needs_reload=0
    if [[ ! -e ${lock_file} ]]; then
        install -d -m 0700 -o "${account_uid}" -g "${account_gid}" "${deploy_dir}"
        install -m 0600 -o "${account_uid}" -g "${account_gid}" /dev/null "${lock_file}"
    fi
    take_lock
    local fresh=0; [[ -f ${quadlets}/atlas-bridge.container ]] || fresh=1
    if (( ! fresh )); then health 20 || die 'production is not healthy before the change; nothing was modified.'; fi
    bootstrap
    compare_all
    report
    if ! grep -qvE '^(same)$' < <(printf '%s\n' "${change[@]}"); then say 'Already consolidated; nothing to do.'; return; fi
    backup
    trap 'say "Install failed; restoring ${backup_dir}."; restore "${backup_dir}"; exit 1' ERR
    n=0
    while IFS='|' read -r dest template owner mode; do
        n=$((n + 1))
        if [[ ${change[n]} != same ]]; then install_file "${work}/new/${n}" "${dest}" "${owner}" "${mode}"; needs_reload=1; fi
    done < <(managed)
    local generator=/usr/lib/systemd/user-generators/podman-user-generator
    if [[ -x ${generator} ]]; then as_account "${generator}" --user --dryrun > /dev/null; fi
    (( needs_reload )) && as_account systemctl --user daemon-reload
    if (( fresh )); then
        as_account systemctl --user enable lab-atlas-update-recover.service
        as_account systemctl --user start transmission.service atlas-bridge.service
        as_account systemctl --user enable --now atlas-bridge-update.timer
    else
        if kind_changed transmission.container; then as_account systemctl --user restart transmission.service; fi
        if kind_changed transmission.container || kind_changed atlas-bridge.container || kind_changed atlas-bridge.network; then
            as_account systemctl --user restart atlas-bridge.service
        fi
        if kind_changed atlas-bridge.network; then say 'NOTE: the network definition changed; restart kavita-atlas.service when convenient.'; fi
    fi
    health || false
    trap - ERR
    say 'Applied. Bridge, Transmission and Kavita are healthy.'
    say "Return to the previous state with: sudo bash ${backup_dir}/restore.sh"
}

main() {
    local mode=${1:---check}
    work="$(mktemp -d)"; trap 'rm -rf "${work}"' EXIT
    declare -ga change=()
    preflight
    case ${mode} in
        --check) compare_all; say 'Comparison with the live files (nothing changed):'; report ;;
        --apply) apply ;;
        --restore) [[ -n ${2:-} ]] || die 'usage: install.sh --restore <backup-dir>'; take_lock; restore "$2" ;;
        *) die 'usage: install.sh [--check | --apply | --restore <backup-dir>]' ;;
    esac
}

main "$@"
