#!/usr/bin/env bash
# Shared library for source-matched install/update. Only source this file.
XUI_PAIRED_REPOSITORY='0x3f3f3f3f3f3f3f3f3f3f3/3x-ui'

paired_checksum() {
    local file="$1" sums="$2" expected actual
    [[ -s "$file" && -s "$sums" ]] || { echo 'paired package/checksum is missing' >&2; return 1; }
    expected=$(awk 'NR==1 {print $1} END {if (NR!=1) exit 1}' "$sums") || return 1
    [[ "$expected" =~ ^[0-9a-f]{64}$ ]] || { echo 'invalid paired package checksum' >&2; return 1; }
    actual=$(sha256sum -- "$file" | awk '{print $1}') || return 1
    [[ "$actual" == "$expected" ]] || { echo 'paired package checksum mismatch' >&2; return 1; }
}

paired_download() {
    local url="$1" target="$2"
    curl --fail --location --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 15 --max-time 600 --output "$target" "$url" || return 1
    [[ -s "$target" ]] || return 1
}

paired_prepare() {
    local requested="${1:-${XUI_UPDATE_TAG:-}}" parent archive tool source
    parent=$(dirname -- "$xui_folder")
    if [[ "${XUI_PAIRED_LIFECYCLE_LOCK:-}" == "$xui_folder" && -n "${XUI_PAIRED_WORK:-}" ]]; then
        PAIRED_WORK="$XUI_PAIRED_WORK"
        PAIRED_VERIFIER="$PAIRED_WORK/installer-verifier"
        PAIRED_CANDIDATE="$XUI_PAIRED_CANDIDATE"
        [[ "$PAIRED_WORK" == "$parent"/.x-ui-paired.* && -d "$PAIRED_WORK" && ! -L "$PAIRED_WORK" ]] || return 1
        [[ "$PAIRED_CANDIDATE" == "$PAIRED_WORK/x-ui" || "$PAIRED_CANDIDATE" == "$PAIRED_WORK/staged/x-ui" ]] || return 1
        "$PAIRED_VERIFIER" verify-incoming "$PAIRED_CANDIDATE" > "$PAIRED_WORK/locked-preflight.json" || return 1
        return 0
    fi
    mkdir -p -- "$parent" || return 1
    PAIRED_WORK=$(mktemp -d "$parent/.x-ui-paired.XXXXXXXX") || return 1
    chmod 700 "$PAIRED_WORK" || return 1
    if [[ -n "${XUI_LOCAL_PACKAGE:-}" ]]; then
        source="$XUI_LOCAL_PACKAGE"
        tool="${XUI_PACKAGE_VERIFIER:-$source/x-ui-package}"
        [[ -f "$tool" && -x "$tool" && ! -L "$tool" ]] || { echo 'local package requires its static verifier or XUI_PACKAGE_VERIFIER' >&2; return 1; }
        if [[ -d "$source" && ! -L "$source" ]]; then
            cp -a -- "$source" "$PAIRED_WORK/x-ui" || return 1
            "$tool" verify-incoming "$PAIRED_WORK/x-ui" > "$PAIRED_WORK/preflight.json" || return 1
            PAIRED_CANDIDATE="$PAIRED_WORK/x-ui"
        elif [[ -f "$source" && ! -L "$source" ]]; then
            "$tool" stage "$source" "$PAIRED_WORK/staged" > "$PAIRED_WORK/preflight.json" || return 1
            PAIRED_CANDIDATE="$PAIRED_WORK/staged/x-ui"
        else
            echo 'local package must be a directory or regular archive without a link' >&2; return 1
        fi
        cp -- "$tool" "$PAIRED_WORK/installer-verifier" || return 1
        chmod 700 "$PAIRED_WORK/installer-verifier" || return 1
        PAIRED_VERIFIER="$PAIRED_WORK/installer-verifier"
        return 0
    fi
    if [[ -z "$requested" ]]; then
        paired_download "https://api.github.com/repos/$XUI_PAIRED_REPOSITORY/releases/latest" "$PAIRED_WORK/latest-release.json" || return 1
        requested=$(sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' "$PAIRED_WORK/latest-release.json" | head -n1)
    fi
    [[ "$requested" == dev ]] && requested=dev-latest
    [[ "$requested" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || { echo 'a valid paired release tag is required' >&2; return 1; }
    local platform base
    platform=$(arch) || return 1
    base="https://github.com/$XUI_PAIRED_REPOSITORY/releases/download/$requested"
    archive="$PAIRED_WORK/x-ui-linux-$platform.tar.gz"
    tool="$PAIRED_WORK/x-ui-package-linux-$platform"
    paired_download "$base/x-ui-linux-$platform.tar.gz" "$archive" || return 1
    paired_download "$base/x-ui-linux-$platform.tar.gz.sha256" "$archive.sha256" || return 1
    paired_checksum "$archive" "$archive.sha256" || return 1
    paired_download "$base/x-ui-package-linux-$platform" "$tool" || return 1
    paired_download "$base/x-ui-package-linux-$platform.sha256" "$tool.sha256" || return 1
    paired_checksum "$tool" "$tool.sha256" || return 1
    chmod 700 "$tool" || return 1
    "$tool" stage "$archive" "$PAIRED_WORK/staged" > "$PAIRED_WORK/preflight.json" || return 1
    cp -- "$tool" "$PAIRED_WORK/installer-verifier" || return 1
    chmod 700 "$PAIRED_WORK/installer-verifier" || return 1
    PAIRED_VERIFIER="$PAIRED_WORK/installer-verifier"
    PAIRED_CANDIDATE="$PAIRED_WORK/staged/x-ui"
}

paired_service() {
    local action="$1"
    if [[ "$release" == alpine ]]; then
        rc-service x-ui "$action"
    elif [[ "$action" == status ]]; then
        systemctl is-active --quiet x-ui
    else
        systemctl "$action" x-ui
    fi
}

paired_atomic_copy() {
    local source="$1" destination="$2" temporary
    [[ -s "$source" && -f "$source" && ! -L "$source" ]] || return 1
    mkdir -p -- "$(dirname -- "$destination")" || return 1
    temporary=$(mktemp "${destination}.paired.XXXXXXXX") || return 1
    cp -- "$source" "$temporary" || return 1
    chmod --reference="$source" "$temporary" || return 1
    mv -f -- "$temporary" "$destination"
}

paired_control_paths() {
    PAIRED_MENU="${XUI_MENU_PATH:-/usr/bin/x-ui}"
    PAIRED_UNIT="$xui_service/x-ui.service"
    [[ "$release" != alpine ]] || PAIRED_UNIT="${XUI_OPENRC_PATH:-/etc/init.d/x-ui}"
}

paired_install_controls() {
    local unit
    # Service templates use this unquoted path. Reject unsupported spellings
    # before installing them rather than emitting an invalid service unit.
    [[ "$xui_folder" =~ ^/[A-Za-z0-9_./-]+$ ]] || { echo 'installation path contains unsupported service-template characters' >&2; return 1; }
    paired_control_paths
    if [[ "$release" == alpine ]]; then unit="$xui_folder/x-ui.rc"
    else
        case "$release" in
            ubuntu|debian|armbian) unit="$xui_folder/x-ui.service.debian" ;;
            arch|manjaro|parch) unit="$xui_folder/x-ui.service.arch" ;;
            *) unit="$xui_folder/x-ui.service.rhel" ;;
        esac
    fi
    [[ -s "$unit" && ! -L "$unit" ]] || return 1
    sed "s|/usr/local/x-ui|$xui_folder|g" "$unit" > "$PAIRED_WORK/new-unit" || return 1
    chmod 644 "$PAIRED_WORK/new-unit"
    [[ "$release" != alpine ]] || chmod 755 "$PAIRED_WORK/new-unit"
    paired_atomic_copy "$PAIRED_WORK/new-unit" "$PAIRED_UNIT" || return 1
    sed "s|/usr/local/x-ui|$xui_folder|g" "$xui_folder/x-ui.sh" > "$PAIRED_WORK/new-menu" || return 1
    chmod 755 "$PAIRED_WORK/new-menu"
    paired_atomic_copy "$PAIRED_WORK/new-menu" "$PAIRED_MENU" || return 1
    if [[ "$release" == alpine ]]; then rc-update add x-ui
    else systemctl daemon-reload && systemctl enable x-ui
    fi
}

paired_restore_controls() {
    local name destination
    for name in menu unit; do
        [[ "$name" != menu ]] || destination="$PAIRED_MENU"
        [[ "$name" != unit ]] || destination="$PAIRED_UNIT"
        if [[ -e "$PAIRED_WORK/previous-$name" ]]; then
            paired_atomic_copy "$PAIRED_WORK/previous-$name" "$destination" || return 1
        elif [[ -e "$destination" ]]; then
            mv -- "$destination" "$PAIRED_WORK/failed-$name" || return 1
        fi
    done
    [[ "$release" == alpine ]] || systemctl daemon-reload
}

paired_activation_failure() {
    # Stop the failed child before copying its latest state. A failed stop
    # leaves the trees and journal intact for a later explicit recovery.
    paired_service stop || return 1
    if [[ -d "$PAIRED_PREVIOUS" ]]; then
        local failed="$xui_folder.failed.$(date -u +%Y%m%dT%H%M%S).$$"
        "$PAIRED_VERIFIER" rollback "$xui_folder" "$failed" > "$PAIRED_WORK/rollback.json" || return 1
        paired_restore_controls || return 1
        paired_service start || return 1
        echo "Previous code restored with current business state; failed tree retained at $failed" >&2
    else
        echo "New installation failed; package and pending journal retained at $xui_folder" >&2
    fi
    return 1
}

paired_apply() {
    local mode="$1" had_previous=0
    [[ ! -e "$xui_folder/x-ui" ]] || had_previous=1
    paired_control_paths
    for name in menu unit; do
        local path="$PAIRED_MENU"
        [[ "$name" != unit ]] || path="$PAIRED_UNIT"
        if [[ -e "$path" ]]; then
            [[ -f "$path" && ! -L "$path" ]] || return 1
            cp -a -- "$path" "$PAIRED_WORK/previous-$name" || return 1
        fi
    done
    # Base packages may be needed by service/configuration helpers, but package
    # verification has already completed and the lifecycle lock is held.
    install_base || return 1
    if [[ "$had_previous" == 1 || -e "$xui_folder.pending.json" ]]; then
        paired_service stop || return 1
    fi
    "$PAIRED_VERIFIER" recover "$xui_folder" > "$PAIRED_WORK/recovery.json" || return 1
    # An interrupted promoted pair is now verified and quiescent. A new update
    # carries its latest state forward rather than restoring old usage.
    if [[ -e "$xui_folder.pending.json" ]]; then
        "$PAIRED_VERIFIER" complete "$xui_folder" > "$PAIRED_WORK/recovered-completion.json" || return 1
    fi
    [[ ! -e "$xui_folder/x-ui" ]] || had_previous=1
    paired_promote || { [[ "$had_previous" != 1 ]] || paired_service start; return 1; }
    if ! paired_install_controls; then paired_activation_failure; return 1; fi
    cd -- "$xui_folder" || return 1
    if [[ "$had_previous" == 0 && "$mode" == install ]]; then
        config_after_install || { paired_activation_failure; return 1; }
    else
        "$xui_folder/x-ui" migrate || { paired_activation_failure; return 1; }
    fi
    paired_service start || { paired_activation_failure; return 1; }
    # Require the service to remain up after its initial start returns.
    sleep 2
    paired_service status || { paired_activation_failure; return 1; }
    "$PAIRED_VERIFIER" complete "$xui_folder" > "$PAIRED_WORK/activation.json" || return 1
    setup_fail2ban
    echo "Source-matched panel and Custom Xray activated; installation receipts: $PAIRED_WORK"
}

paired_entrypoint() {
    local mode="$1" requested="${2:-}" entry
    paired_prepare "$requested" || return 1
    if [[ "${XUI_PACKAGE_ONLY:-0}" == 1 ]]; then
        "$PAIRED_VERIFIER" recover "$xui_folder" > "$PAIRED_WORK/recovery.json" || return 1
        [[ ! -e "$xui_folder.pending.json" ]] || "$PAIRED_VERIFIER" complete "$xui_folder" > "$PAIRED_WORK/recovered-completion.json" || return 1
        paired_promote || return 1
        "$PAIRED_VERIFIER" complete "$xui_folder" > "$PAIRED_WORK/activation.json" || return 1
        return 0
    fi
    if [[ "${XUI_PAIRED_LIFECYCLE_LOCK:-}" != "$xui_folder" ]]; then
        entry="$PAIRED_CANDIDATE/$mode.sh"
        [[ -s "$entry" && -f "$entry" && ! -L "$entry" ]] || { echo 'paired package does not contain its installer' >&2; return 1; }
        export XUI_PAIRED_WORK="$PAIRED_WORK" XUI_PAIRED_CANDIDATE="$PAIRED_CANDIDATE"
        "$PAIRED_VERIFIER" with-lock "$xui_folder" bash "$entry" "$requested"
        return $?
    fi
    paired_apply "$mode"
}

paired_promote() {
    # Run only after old service stop and accounting settlement. In package-only
    # mode the caller operates on an offline/private installation prefix.
    PAIRED_PREVIOUS="$xui_folder.previous.$(date -u +%Y%m%dT%H%M%S).$$"
    "$PAIRED_VERIFIER" promote "$PAIRED_CANDIDATE" "$xui_folder" "$PAIRED_PREVIOUS" > "$PAIRED_WORK/promotion.json" || return 1
    echo "Paired package installed; previous resources retained at $PAIRED_PREVIOUS"
}
