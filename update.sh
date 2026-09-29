#!/bin/bash

red='\033[0;31m'
green='\033[0;32m'
blue='\033[0;34m'
yellow='\033[0;33m'
plain='\033[0m'

xui_folder="${XUI_MAIN_FOLDER:=/usr/local/x-ui}"
xui_service="${XUI_SERVICE:=/etc/systemd/system}"

# Don't edit this config
b_source="${BASH_SOURCE[0]}"
while [ -h "$b_source" ]; do
    b_dir="$(cd -P "$(dirname "$b_source")" > /dev/null 2>&1 && pwd || pwd -P)"
    b_source="$(readlink "$b_source")"
    [[ $b_source != /* ]] && b_source="$b_dir/$b_source"
done
cur_dir="$(cd -P "$(dirname "$b_source")" > /dev/null 2>&1 && pwd || pwd -P)"
script_name=$(basename "$0")

# Check command exist function
_command_exists() {
    type "$1" &> /dev/null
}

# Fail, log and exit script function
_fail() {
    local msg=${1}
    echo -e "${red}${msg}${plain}"
    exit 2
}

# Records this run's outcome for the panel's web updater to poll, since it
# launches this script detached and has no other way to learn whether it
# finished. Written to a fixed path outside XUI_MAIN_FOLDER so it survives
# the update regardless of what happens to that folder. The EXIT trap below
# covers every exit path in this file, including the bare `exit 1`/`exit 2`
# calls that don't go through _fail.
xui_update_work=""
xui_update_stage=""
xui_update_run_id="${XUI_UPDATE_RUN_ID:-0}"
[[ "${xui_update_run_id}" =~ ^[0-9]+$ ]] || xui_update_run_id="0"
xui_update_status_file="${XUI_UPDATE_STATUS_FILE:-/etc/x-ui/update-status.json}"

_write_update_status() {
    local state="$1"
    local exit_code="$2"
    local status_dir
    status_dir="$(dirname "${xui_update_status_file}")"
    mkdir -p "${status_dir}" > /dev/null 2>&1
    local tmp_file="${xui_update_status_file}.tmp.$$"
    printf '{"runId":"%s","state":"%s","exitCode":%s,"finishedAt":%s}\n' \
        "${xui_update_run_id}" "${state}" "${exit_code}" "$(date +%s)" > "${tmp_file}" 2> /dev/null
    mv -f "${tmp_file}" "${xui_update_status_file}" > /dev/null 2>&1
}

_report_update_exit() {
    local code=$?
    if [[ -n "$xui_update_work" ]]; then
        rm -rf -- "$xui_update_work"
    fi
    if [[ "${code}" -eq 0 ]]; then
        _write_update_status "success" "0"
    else
        _write_update_status "failed" "${code}"
    fi
}
trap _report_update_exit EXIT
trap 'exit 143' TERM
trap 'exit 130' INT

# check root
[[ $EUID -ne 0 ]] && _fail "FATAL ERROR: Please run this script with root privilege."

curl_bin=$(command -v curl || true)

# Check OS and set release variable
if [[ -f /etc/os-release ]]; then
    source /etc/os-release
    release=$ID
elif [[ -f /usr/lib/os-release ]]; then
    source /usr/lib/os-release
    release=$ID
else
    _fail "Failed to check the system OS, please contact the author!"
fi
echo "The OS release is: $release"

arch() {
    case "$(uname -m)" in
        x86_64 | x64 | amd64) echo 'amd64' ;;
        i*86 | x86) echo '386' ;;
        armv8* | armv8 | arm64 | aarch64) echo 'arm64' ;;
        armv7* | armv7 | arm) echo 'armv7' ;;
        armv6* | armv6) echo 'armv6' ;;
        armv5* | armv5) echo 'armv5' ;;
        s390x) echo 's390x' ;;
        *) echo -e "${red}Unsupported CPU architecture!${plain}" >&2; return 2 ;;
    esac
}

echo "Arch: $(arch)"

# Simple helpers
is_ipv4() {
    [[ "$1" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] && return 0 || return 1
}
is_ipv6() {
    [[ "$1" =~ : ]] && return 0 || return 1
}
is_ip() {
    is_ipv4 "$1" || is_ipv6 "$1"
}
is_domain() {
    [[ "$1" =~ ^([A-Za-z0-9](-*[A-Za-z0-9])*\.)+(xn--[a-z0-9]{2,}|[A-Za-z]{2,})$ ]] && return 0 || return 1
}

# acme.sh's standalone server binds IPv4 by default; --listen-v6 makes it
# v6-only, which breaks HTTP-01 validation when the domain's A record points
# at this host's IPv4 (#4994). Only force IPv6 when the host has no global
# IPv4 address at all.
acme_listen_flag() {
    if ip -4 addr show scope global 2> /dev/null | grep -q "inet "; then
        echo ""
    else
        echo "--listen-v6"
    fi
}

# Port helpers
is_port_in_use() {
    local port="$1"
    if command -v ss > /dev/null 2>&1; then
        ss -ltn 2> /dev/null | awk -v p=":${port}$" '$4 ~ p {exit 0} END {exit 1}'
        return
    fi
    if command -v netstat > /dev/null 2>&1; then
        netstat -lnt 2> /dev/null | awk -v p=":${port} " '$4 ~ p {exit 0} END {exit 1}'
        return
    fi
    if command -v lsof > /dev/null 2>&1; then
        lsof -nP -iTCP:${port} -sTCP:LISTEN > /dev/null 2>&1 && return 0
    fi
    return 1
}

gen_random_string() {
    local length="$1"
    openssl rand -base64 $((length * 2)) \
        | tr -dc 'a-zA-Z0-9' \
        | head -c "$length"
}

xui_env_file_path() {
    case "${release}" in
        ubuntu | debian | armbian)
            echo "/etc/default/x-ui"
            ;;
        arch | manjaro | parch | alpine)
            echo "/etc/conf.d/x-ui"
            ;;
        *)
            echo "/etc/sysconfig/x-ui"
            ;;
    esac
}

load_xui_env() {
    local env_file
    env_file="$(xui_env_file_path)"
    if [[ -r "$env_file" ]]; then
        set -a
        # shellcheck disable=SC1090
        source "$env_file"
        set +a
    fi
}

install_base() {
    echo -e "${green}Updating and install dependency packages...${plain}"
    case "${release}" in
        ubuntu | debian | armbian)
            apt-get update > /dev/null 2>&1 && apt-get install -y -q cron curl tar tzdata socat openssl > /dev/null 2>&1
            ;;
        fedora | amzn | virtuozzo | rhel | almalinux | rocky | ol)
            dnf makecache -y > /dev/null 2>&1 && dnf install -y -q cronie curl tar tzdata socat openssl > /dev/null 2>&1
            ;;
        centos)
            if [[ "${VERSION_ID}" =~ ^7 ]]; then
                yum makecache -y > /dev/null 2>&1 && yum install -y -q cronie curl tar tzdata socat openssl > /dev/null 2>&1
            else
                dnf makecache -y > /dev/null 2>&1 && dnf install -y -q cronie curl tar tzdata socat openssl > /dev/null 2>&1
            fi
            ;;
        arch | manjaro | parch)
            pacman -Sy --noconfirm cronie curl tar tzdata socat openssl > /dev/null 2>&1
            ;;
        opensuse-tumbleweed | opensuse-leap)
            zypper refresh > /dev/null 2>&1 && zypper -q install -y cron curl tar timezone socat openssl > /dev/null 2>&1
            ;;
        alpine)
            apk update > /dev/null 2>&1 && apk add dcron curl tar tzdata socat openssl > /dev/null 2>&1
            ;;
        *)
            apt-get update > /dev/null 2>&1 && apt install -y -q cron curl tar tzdata socat openssl > /dev/null 2>&1
            ;;
    esac
}

install_acme() {
    echo -e "${green}Installing acme.sh for SSL certificate management...${plain}"
    cd ~ || return 1
    curl -s https://get.acme.sh | sh > /dev/null 2>&1
    if [ $? -ne 0 ]; then
        echo -e "${red}Failed to install acme.sh${plain}"
        return 1
    else
        echo -e "${green}acme.sh installed successfully${plain}"
    fi
    return 0
}

setup_ssl_certificate() {
    local domain="$1"
    local server_ip="$2"
    local existing_port="$3"
    local existing_webBasePath="$4"

    echo -e "${green}Setting up SSL certificate...${plain}"

    # Check if acme.sh is installed
    if ! command -v ~/.acme.sh/acme.sh &> /dev/null; then
        install_acme
        if [ $? -ne 0 ]; then
            echo -e "${yellow}Failed to install acme.sh, skipping SSL setup${plain}"
            return 1
        fi
    fi

    # Create certificate directory
    local certPath="/root/cert/${domain}"
    mkdir -p "$certPath"

    # Issue certificate
    echo -e "${green}Issuing SSL certificate for ${domain}...${plain}"
    echo -e "${yellow}Note: Port 80 must be open and accessible from the internet${plain}"

    ~/.acme.sh/acme.sh --set-default-ca --server letsencrypt --force > /dev/null 2>&1
    ~/.acme.sh/acme.sh --issue -d ${domain} $(acme_listen_flag) --standalone --httpport 80 --force

    if [ $? -ne 0 ]; then
        echo -e "${yellow}Failed to issue certificate for ${domain}${plain}"
        echo -e "${yellow}Please ensure port 80 is open and try again later with: x-ui${plain}"
        rm -rf ~/.acme.sh/${domain} 2> /dev/null
        rm -rf "$certPath" 2> /dev/null
        return 1
    fi

    # Install certificate
    ~/.acme.sh/acme.sh --installcert --force -d ${domain} \
        --key-file /root/cert/${domain}/privkey.pem \
        --fullchain-file /root/cert/${domain}/fullchain.pem \
        --reloadcmd "systemctl restart x-ui" > /dev/null 2>&1

    if [ $? -ne 0 ]; then
        echo -e "${yellow}Failed to install certificate${plain}"
        return 1
    fi

    # Enable auto-renew
    ~/.acme.sh/acme.sh --upgrade --auto-upgrade > /dev/null 2>&1
    chmod 600 $certPath/privkey.pem 2> /dev/null
    chmod 644 $certPath/fullchain.pem 2> /dev/null

    # Set certificate for panel
    local webCertFile="/root/cert/${domain}/fullchain.pem"
    local webKeyFile="/root/cert/${domain}/privkey.pem"

    if [[ -f "$webCertFile" && -f "$webKeyFile" ]]; then
        ${xui_folder}/x-ui cert -webCert "$webCertFile" -webCertKey "$webKeyFile" > /dev/null 2>&1
        echo -e "${green}SSL certificate installed and configured successfully!${plain}"
        return 0
    else
        echo -e "${yellow}Certificate files not found${plain}"
        return 1
    fi
}

# Issue Let's Encrypt IP certificate with shortlived profile (~6 days validity)
# Requires acme.sh and port 80 open for HTTP-01 challenge
setup_ip_certificate() {
    local ipv4="$1"
    local ipv6="$2" # optional

    echo -e "${green}Setting up Let's Encrypt IP certificate (shortlived profile)...${plain}"
    echo -e "${yellow}Note: IP certificates are valid for ~6 days and will auto-renew.${plain}"
    echo -e "${yellow}Default listener is port 80. If you choose another port, ensure external port 80 forwards to it.${plain}"

    # Check for acme.sh
    if ! command -v ~/.acme.sh/acme.sh &> /dev/null; then
        install_acme
        if [ $? -ne 0 ]; then
            echo -e "${red}Failed to install acme.sh${plain}"
            return 1
        fi
    fi

    # Validate IP address
    if [[ -z "$ipv4" ]]; then
        echo -e "${red}IPv4 address is required${plain}"
        return 1
    fi

    if ! is_ipv4 "$ipv4"; then
        echo -e "${red}Invalid IPv4 address: $ipv4${plain}"
        return 1
    fi

    # Create certificate directory
    local certDir="/root/cert/ip"
    mkdir -p "$certDir"

    # Build domain arguments
    local domain_args="-d ${ipv4}"
    if [[ -n "$ipv6" ]] && is_ipv6 "$ipv6"; then
        domain_args="${domain_args} -d ${ipv6}"
        echo -e "${green}Including IPv6 address: ${ipv6}${plain}"
    fi

    # Set reload command for auto-renewal (add || true so it doesn't fail if service stopped)
    local reloadCmd="systemctl restart x-ui 2>/dev/null || rc-service x-ui restart 2>/dev/null || true"

    # Choose port for HTTP-01 listener (default 80, prompt override)
    local WebPort=""
    read -rp "Port to use for ACME HTTP-01 listener (default 80): " WebPort
    WebPort="${WebPort:-80}"
    if ! [[ "${WebPort}" =~ ^[0-9]+$ ]] || ((WebPort < 1 || WebPort > 65535)); then
        echo -e "${red}Invalid port provided. Falling back to 80.${plain}"
        WebPort=80
    fi
    echo -e "${green}Using port ${WebPort} for standalone validation.${plain}"
    if [[ "${WebPort}" -ne 80 ]]; then
        echo -e "${yellow}Reminder: Let's Encrypt still connects on port 80; forward external port 80 to ${WebPort}.${plain}"
    fi

    # Ensure chosen port is available
    while true; do
        if is_port_in_use "${WebPort}"; then
            echo -e "${yellow}Port ${WebPort} is currently in use.${plain}"

            local alt_port=""
            read -rp "Enter another port for acme.sh standalone listener (leave empty to abort): " alt_port
            alt_port="${alt_port// /}"
            if [[ -z "${alt_port}" ]]; then
                echo -e "${red}Port ${WebPort} is busy; cannot proceed.${plain}"
                return 1
            fi
            if ! [[ "${alt_port}" =~ ^[0-9]+$ ]] || ((alt_port < 1 || alt_port > 65535)); then
                echo -e "${red}Invalid port provided.${plain}"
                return 1
            fi
            WebPort="${alt_port}"
            continue
        else
            echo -e "${green}Port ${WebPort} is free and ready for standalone validation.${plain}"
            break
        fi
    done

    # Issue certificate with shortlived profile
    echo -e "${green}Issuing IP certificate for ${ipv4}...${plain}"
    ~/.acme.sh/acme.sh --set-default-ca --server letsencrypt --force > /dev/null 2>&1

    ~/.acme.sh/acme.sh --issue \
        ${domain_args} \
        --standalone \
        --server letsencrypt \
        --certificate-profile shortlived \
        --days 6 \
        --httpport ${WebPort} \
        --force

    if [ $? -ne 0 ]; then
        echo -e "${red}Failed to issue IP certificate${plain}"
        echo -e "${yellow}Please ensure port ${WebPort} is reachable (or forwarded from external port 80)${plain}"
        # Cleanup acme.sh data for both IPv4 and IPv6 if specified
        rm -rf ~/.acme.sh/${ipv4} 2> /dev/null
        [[ -n "$ipv6" ]] && rm -rf ~/.acme.sh/${ipv6} 2> /dev/null
        rm -rf ${certDir} 2> /dev/null
        return 1
    fi

    echo -e "${green}Certificate issued successfully, installing...${plain}"

    # Install certificate
    # Note: acme.sh may report "Reload error" and exit non-zero if reloadcmd fails,
    # but the cert files are still installed. We check for files instead of exit code.
    ~/.acme.sh/acme.sh --installcert --force -d ${ipv4} \
        --key-file "${certDir}/privkey.pem" \
        --fullchain-file "${certDir}/fullchain.pem" \
        --reloadcmd "${reloadCmd}" 2>&1 || true

    # Verify certificate files exist (don't rely on exit code - reloadcmd failure causes non-zero)
    if [[ ! -f "${certDir}/fullchain.pem" || ! -f "${certDir}/privkey.pem" ]]; then
        echo -e "${red}Certificate files not found after installation${plain}"
        # Cleanup acme.sh data for both IPv4 and IPv6 if specified
        rm -rf ~/.acme.sh/${ipv4} 2> /dev/null
        [[ -n "$ipv6" ]] && rm -rf ~/.acme.sh/${ipv6} 2> /dev/null
        rm -rf ${certDir} 2> /dev/null
        return 1
    fi

    echo -e "${green}Certificate files installed successfully${plain}"

    # Enable auto-upgrade for acme.sh (ensures cron job runs)
    ~/.acme.sh/acme.sh --upgrade --auto-upgrade > /dev/null 2>&1

    chmod 600 ${certDir}/privkey.pem 2> /dev/null
    chmod 644 ${certDir}/fullchain.pem 2> /dev/null

    # Configure panel to use the certificate
    echo -e "${green}Setting certificate paths for the panel...${plain}"
    ${xui_folder}/x-ui cert -webCert "${certDir}/fullchain.pem" -webCertKey "${certDir}/privkey.pem"
    if [ $? -ne 0 ]; then
        echo -e "${yellow}Warning: Could not set certificate paths automatically.${plain}"
        echo -e "${yellow}You may need to set them manually in the panel settings.${plain}"
        echo -e "${yellow}Cert path: ${certDir}/fullchain.pem${plain}"
        echo -e "${yellow}Key path: ${certDir}/privkey.pem${plain}"
    else
        echo -e "${green}Certificate paths set successfully!${plain}"
    fi

    echo -e "${green}IP certificate installed and configured successfully!${plain}"
    echo -e "${green}Certificate valid for ~6 days, auto-renews via acme.sh cron job.${plain}"
    echo -e "${yellow}Panel will automatically restart after each renewal.${plain}"
    return 0
}

# Comprehensive manual SSL certificate issuance via acme.sh
ssl_cert_issue() {
    local existing_webBasePath=$(${xui_folder}/x-ui setting -show true | grep 'webBasePath:' | awk -F': ' '{print $2}' | tr -d '[:space:]' | sed 's#^/##')
    local existing_port=$(${xui_folder}/x-ui setting -show true | grep 'port:' | awk -F': ' '{print $2}' | tr -d '[:space:]')

    # check for acme.sh first
    if ! command -v ~/.acme.sh/acme.sh &> /dev/null; then
        echo "acme.sh could not be found. Installing now..."
        cd ~ || return 1
        curl -s https://get.acme.sh | sh
        if [ $? -ne 0 ]; then
            echo -e "${red}Failed to install acme.sh${plain}"
            return 1
        else
            echo -e "${green}acme.sh installed successfully${plain}"
        fi
    fi

    # get the domain here, and we need to verify it
    local domain=""
    while true; do
        read -rp "Please enter your domain name: " domain
        domain="${domain// /}" # Trim whitespace

        if [[ -z "$domain" ]]; then
            echo -e "${red}Domain name cannot be empty. Please try again.${plain}"
            continue
        fi

        if ! is_domain "$domain"; then
            echo -e "${red}Invalid domain format: ${domain}. Please enter a valid domain name.${plain}"
            continue
        fi

        break
    done
    echo -e "${green}Your domain is: ${domain}, checking it...${plain}"
    SSL_ISSUED_DOMAIN="${domain}"

    # detect existing certificate and reuse it if present
    local cert_exists=0
    if ~/.acme.sh/acme.sh --list 2> /dev/null | awk '{print $1}' | grep -Fxq "${domain}"; then
        cert_exists=1
        local certInfo=$(~/.acme.sh/acme.sh --list 2> /dev/null | grep -F "${domain}")
        echo -e "${yellow}Existing certificate found for ${domain}, will reuse it.${plain}"
        [[ -n "${certInfo}" ]] && echo "$certInfo"
    else
        echo -e "${green}Your domain is ready for issuing certificates now...${plain}"
    fi

    # create a directory for the certificate
    certPath="/root/cert/${domain}"
    if [ ! -d "$certPath" ]; then
        mkdir -p "$certPath"
    else
        rm -rf "$certPath"
        mkdir -p "$certPath"
    fi

    # get the port number for the standalone server
    local WebPort=80
    read -rp "Please choose which port to use (default is 80): " WebPort
    if [[ -z ${WebPort} ]]; then
        WebPort=80
    elif [[ ! ${WebPort} =~ ^[1-9][0-9]*$ || ${WebPort} -gt 65535 ]]; then
        echo -e "${yellow}Your input ${WebPort} is invalid, will use default port 80.${plain}"
        WebPort=80
    fi
    echo -e "${green}Will use port: ${WebPort} to issue certificates. Please make sure this port is open.${plain}"

    # Stop panel temporarily
    echo -e "${yellow}Stopping panel temporarily...${plain}"
    systemctl stop x-ui 2> /dev/null || rc-service x-ui stop 2> /dev/null

    if [[ ${cert_exists} -eq 0 ]]; then
        # issue the certificate
        ~/.acme.sh/acme.sh --set-default-ca --server letsencrypt --force
        ~/.acme.sh/acme.sh --issue -d ${domain} $(acme_listen_flag) --standalone --httpport ${WebPort} --force
        if [ $? -ne 0 ]; then
            echo -e "${red}Issuing certificate failed, please check logs.${plain}"
            rm -rf ~/.acme.sh/${domain}
            systemctl start x-ui 2> /dev/null || rc-service x-ui start 2> /dev/null
            return 1
        else
            echo -e "${green}Issuing certificate succeeded, installing certificates...${plain}"
        fi
    else
        echo -e "${green}Using existing certificate, installing certificates...${plain}"
    fi

    # Setup reload command
    reloadCmd="systemctl restart x-ui || rc-service x-ui restart"
    echo -e "${green}Default --reloadcmd for ACME is: ${yellow}systemctl restart x-ui || rc-service x-ui restart${plain}"
    echo -e "${green}This command will run on every certificate issue and renew.${plain}"
    read -rp "Would you like to modify --reloadcmd for ACME? (y/n): " setReloadcmd
    if [[ "$setReloadcmd" == "y" || "$setReloadcmd" == "Y" ]]; then
        echo -e "\n${green}\t1.${plain} Preset: systemctl reload nginx ; systemctl restart x-ui"
        echo -e "${green}\t2.${plain} Input your own command"
        echo -e "${green}\t0.${plain} Keep default reloadcmd"
        read -rp "Choose an option: " choice
        case "$choice" in
            1)
                echo -e "${green}Reloadcmd is: systemctl reload nginx ; systemctl restart x-ui${plain}"
                reloadCmd="systemctl reload nginx ; systemctl restart x-ui"
                ;;
            2)
                echo -e "${yellow}It's recommended to put x-ui restart at the end${plain}"
                read -rp "Please enter your custom reloadcmd: " reloadCmd
                echo -e "${green}Reloadcmd is: ${reloadCmd}${plain}"
                ;;
            *)
                echo -e "${green}Keeping default reloadcmd${plain}"
                ;;
        esac
    fi

    # install the certificate
    local installOutput=""
    installOutput=$(~/.acme.sh/acme.sh --installcert --force -d ${domain} \
        --key-file /root/cert/${domain}/privkey.pem \
        --fullchain-file /root/cert/${domain}/fullchain.pem --reloadcmd "${reloadCmd}" 2>&1)
    local installRc=$?
    echo "${installOutput}"

    local installWroteFiles=0
    if echo "${installOutput}" | grep -q "Installing key to:" && echo "${installOutput}" | grep -q "Installing full chain to:"; then
        installWroteFiles=1
    fi

    if [[ -f "/root/cert/${domain}/privkey.pem" && -f "/root/cert/${domain}/fullchain.pem" && (${installRc} -eq 0 || ${installWroteFiles} -eq 1) ]]; then
        echo -e "${green}Installing certificate succeeded, enabling auto renew...${plain}"
    else
        echo -e "${red}Installing certificate failed, exiting.${plain}"
        if [[ ${cert_exists} -eq 0 ]]; then
            rm -rf ~/.acme.sh/${domain}
        fi
        systemctl start x-ui 2> /dev/null || rc-service x-ui start 2> /dev/null
        return 1
    fi

    # enable auto-renew
    ~/.acme.sh/acme.sh --upgrade --auto-upgrade
    if [ $? -ne 0 ]; then
        echo -e "${yellow}Auto renew setup had issues, certificate details:${plain}"
        ls -lah /root/cert/${domain}/
        chmod 600 $certPath/privkey.pem
        chmod 644 $certPath/fullchain.pem
    else
        echo -e "${green}Auto renew succeeded, certificate details:${plain}"
        ls -lah /root/cert/${domain}/
        chmod 600 $certPath/privkey.pem
        chmod 644 $certPath/fullchain.pem
    fi

    # Restart panel
    systemctl start x-ui 2> /dev/null || rc-service x-ui start 2> /dev/null

    # Prompt user to set panel paths after successful certificate installation
    read -rp "Would you like to set this certificate for the panel? (y/n): " setPanel
    if [[ "$setPanel" == "y" || "$setPanel" == "Y" ]]; then
        local webCertFile="/root/cert/${domain}/fullchain.pem"
        local webKeyFile="/root/cert/${domain}/privkey.pem"

        if [[ -f "$webCertFile" && -f "$webKeyFile" ]]; then
            ${xui_folder}/x-ui cert -webCert "$webCertFile" -webCertKey "$webKeyFile"
            echo -e "${green}Certificate paths set for the panel${plain}"
            echo -e "${green}Certificate File: $webCertFile${plain}"
            echo -e "${green}Private Key File: $webKeyFile${plain}"
            echo ""
            echo -e "${green}Access URL: https://${domain}:${existing_port}/${existing_webBasePath}${plain}"
            echo -e "${yellow}Panel will restart to apply SSL certificate...${plain}"
            systemctl restart x-ui 2> /dev/null || rc-service x-ui restart 2> /dev/null
        else
            echo -e "${red}Error: Certificate or private key file not found for domain: $domain.${plain}"
        fi
    else
        echo -e "${yellow}Skipping panel path setting.${plain}"
    fi

    return 0
}
# Unified interactive SSL setup (domain or IP)
# Sets global `SSL_HOST` to the chosen domain/IP
prompt_and_setup_ssl() {
    local panel_port="$1"
    local web_base_path="$2" # expected without leading slash
    local server_ip="$3"

    local ssl_choice=""

    echo -e "${yellow}Choose SSL certificate setup method:${plain}"
    echo -e "${green}1.${plain} Let's Encrypt for Domain (90-day validity, auto-renews)"
    echo -e "${green}2.${plain} Let's Encrypt for IP Address (6-day validity, auto-renews)"
    echo -e "${green}3.${plain} Custom SSL Certificate (Path to existing files)"
    echo -e "${green}4.${plain} Skip SSL (advanced — behind reverse proxy / SSH tunnel only)"
    echo -e "${blue}Note:${plain} Options 1 & 2 require port 80 open. Option 3 requires manual paths."
    echo -e "${blue}Note:${plain} Option 4 serves the panel over plain HTTP — only safe behind nginx/Caddy or an SSH tunnel."
    read -rp "Choose an option (default 2 for IP): " ssl_choice
    ssl_choice="${ssl_choice// /}" # Trim whitespace

    # Default to 2 (IP cert) if input is empty or invalid (not 1, 3 or 4)
    if [[ "$ssl_choice" != "1" && "$ssl_choice" != "3" && "$ssl_choice" != "4" ]]; then
        ssl_choice="2"
    fi

    case "$ssl_choice" in
        1)
            # User chose Let's Encrypt domain option
            echo -e "${green}Using Let's Encrypt for domain certificate...${plain}"
            if ssl_cert_issue; then
                local cert_domain="${SSL_ISSUED_DOMAIN}"
                if [[ -z "${cert_domain}" ]]; then
                    cert_domain=$(~/.acme.sh/acme.sh --list 2> /dev/null | tail -1 | awk '{print $1}')
                fi

                if [[ -n "${cert_domain}" ]]; then
                    SSL_HOST="${cert_domain}"
                    echo -e "${green}✓ SSL certificate configured successfully with domain: ${cert_domain}${plain}"
                else
                    echo -e "${yellow}SSL setup may have completed, but domain extraction failed${plain}"
                    SSL_HOST="${server_ip}"
                fi
            else
                echo -e "${red}SSL certificate setup failed for domain mode.${plain}"
                SSL_HOST="${server_ip}"
            fi
            ;;
        2)
            # User chose Let's Encrypt IP certificate option
            echo -e "${green}Using Let's Encrypt for IP certificate (shortlived profile)...${plain}"

            # Confirm the auto-detected IP before issuing for it: with asymmetric
            # routing / multi-WAN the echo services can return a transit address.
            local ip_confirm=""
            read -rp "Is ${server_ip} the correct incoming public IPv4 address for this server? [Default y]: " ip_confirm
            if [[ -n "$ip_confirm" && "$ip_confirm" != "y" && "$ip_confirm" != "Y" ]]; then
                server_ip=""
                while [[ -z "$server_ip" ]]; do
                    read -rp "Please enter your server's public IPv4 address: " server_ip
                    server_ip="${server_ip// /}"
                    if [[ ! "$server_ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
                        echo -e "${red}Invalid IPv4 address. Please try again.${plain}"
                        server_ip=""
                    fi
                done
            fi

            # Ask for optional IPv6
            local ipv6_addr=""
            read -rp "Do you have an IPv6 address to include? (leave empty to skip): " ipv6_addr
            ipv6_addr="${ipv6_addr// /}" # Trim whitespace

            # Stop panel if running (port 80 needed)
            if [[ $release == "alpine" ]]; then
                rc-service x-ui stop > /dev/null 2>&1
            else
                systemctl stop x-ui > /dev/null 2>&1
            fi

            setup_ip_certificate "${server_ip}" "${ipv6_addr}"
            if [ $? -eq 0 ]; then
                SSL_HOST="${server_ip}"
                echo -e "${green}✓ Let's Encrypt IP certificate configured successfully${plain}"
            else
                echo -e "${red}✗ IP certificate setup failed. Please check port 80 is open.${plain}"
                SSL_HOST="${server_ip}"
            fi

            # Restart panel after SSL is configured (restart applies new cert settings)
            if [[ $release == "alpine" ]]; then
                rc-service x-ui restart > /dev/null 2>&1
            else
                systemctl restart x-ui > /dev/null 2>&1
            fi

            ;;
        3)
            # User chose Custom Paths (User Provided) option
            echo -e "${green}Using custom existing certificate...${plain}"
            local custom_cert=""
            local custom_key=""
            local custom_domain=""

            # 3.1 Request Domain to compose Panel URL later
            read -rp "Please enter domain name certificate issued for: " custom_domain
            custom_domain="${custom_domain// /}" # Remove spaces

            # 3.2 Loop for Certificate Path
            while true; do
                read -rp "Input certificate path (keywords: .crt / fullchain): " custom_cert
                # Strip quotes if present
                custom_cert=$(echo "$custom_cert" | tr -d '"' | tr -d "'")

                if [[ -f "$custom_cert" && -r "$custom_cert" && -s "$custom_cert" ]]; then
                    break
                elif [[ ! -f "$custom_cert" ]]; then
                    echo -e "${red}Error: File does not exist! Try again.${plain}"
                elif [[ ! -r "$custom_cert" ]]; then
                    echo -e "${red}Error: File exists but is not readable (check permissions)!${plain}"
                else
                    echo -e "${red}Error: File is empty!${plain}"
                fi
            done

            # 3.3 Loop for Private Key Path
            while true; do
                read -rp "Input private key path (keywords: .key / privatekey): " custom_key
                # Strip quotes if present
                custom_key=$(echo "$custom_key" | tr -d '"' | tr -d "'")

                if [[ -f "$custom_key" && -r "$custom_key" && -s "$custom_key" ]]; then
                    break
                elif [[ ! -f "$custom_key" ]]; then
                    echo -e "${red}Error: File does not exist! Try again.${plain}"
                elif [[ ! -r "$custom_key" ]]; then
                    echo -e "${red}Error: File exists but is not readable (check permissions)!${plain}"
                else
                    echo -e "${red}Error: File is empty!${plain}"
                fi
            done

            # 3.4 Apply Settings via x-ui binary
            ${xui_folder}/x-ui cert -webCert "$custom_cert" -webCertKey "$custom_key" > /dev/null 2>&1

            # Set SSL_HOST for composing Panel URL
            if [[ -n "$custom_domain" ]]; then
                SSL_HOST="$custom_domain"
            else
                SSL_HOST="${server_ip}"
            fi

            echo -e "${green}✓ Custom certificate paths applied.${plain}"
            echo -e "${yellow}Note: You are responsible for renewing these files externally.${plain}"

            systemctl restart x-ui > /dev/null 2>&1 || rc-service x-ui restart > /dev/null 2>&1
            ;;
        4)
            echo ""
            echo -e "${red}⚠ Panel will be installed WITHOUT SSL/TLS.${plain}"
            echo -e "${yellow}Login credentials and cookies will travel as plain HTTP.${plain}"
            echo -e "${yellow}Only safe when:${plain}"
            echo -e "${yellow}  • A reverse proxy (nginx, Caddy, Traefik) terminates TLS for you, or${plain}"
            echo -e "${yellow}  • You access the panel exclusively via SSH tunnel${plain}"
            echo ""

            SSL_SCHEME="http"
            SSL_HOST="${server_ip}"

            local bind_local=""
            read -rp "Bind the panel to 127.0.0.1 only? (recommended — forces SSH tunnel / reverse-proxy access) [y/N]: " bind_local
            if [[ "$bind_local" == "y" || "$bind_local" == "Y" ]]; then
                ${xui_folder}/x-ui setting -listenIP "127.0.0.1" > /dev/null 2>&1
                SSL_HOST="127.0.0.1"
                echo -e "${green}✓ Panel bound to 127.0.0.1 only. It is now unreachable from the public internet.${plain}"
                echo ""
                echo -e "${green}SSH Port Forwarding — open the panel from your local machine via:${plain}"
                echo -e "  Standard SSH command:"
                echo -e "  ${yellow}ssh -L 2222:127.0.0.1:${panel_port} root@${server_ip}${plain}"
                echo -e "  If using an SSH key:"
                echo -e "  ${yellow}ssh -i <sshkeypath> -L 2222:127.0.0.1:${panel_port} root@${server_ip}${plain}"
                echo -e "  Then open in your browser:"
                echo -e "  ${yellow}http://localhost:2222/${web_base_path}${plain}"
                echo ""
                echo -e "${yellow}Alternative: point a reverse proxy (nginx/Caddy) at 127.0.0.1:${panel_port} and let it terminate TLS.${plain}"
            else
                echo -e "${yellow}Panel will listen on all interfaces over plain HTTP. Make sure something else is terminating TLS in front of it.${plain}"
            fi

            systemctl restart x-ui > /dev/null 2>&1 || rc-service x-ui restart > /dev/null 2>&1
            echo -e "${green}✓ SSL setup skipped.${plain}"
            ;;
        *)
            echo -e "${red}Invalid option. Skipping SSL setup.${plain}"
            SSL_HOST="${server_ip}"
            ;;
    esac
}

config_after_update() {
    local panel_needs_restart=0

    echo -e "${yellow}x-ui settings:${plain}"
    ${xui_folder}/x-ui setting -show true
    ${xui_folder}/x-ui migrate

    # Properly detect empty cert by checking if cert: line exists and has content after it
    local existing_cert=$(${xui_folder}/x-ui setting -getCert true 2> /dev/null | grep 'cert:' | awk -F': ' '{print $2}' | tr -d '[:space:]')
    local existing_port=$(${xui_folder}/x-ui setting -show true | grep -Eo 'port: .+' | awk '{print $2}')
    local existing_webBasePath=$(${xui_folder}/x-ui setting -show true | grep -Eo 'webBasePath: .+' | awk '{print $2}' | sed 's#^/##')

    # Get server IP
    local URL_lists=(
        "https://api4.ipify.org"
        "https://ipv4.icanhazip.com"
        "https://v4.api.ipinfo.io/ip"
        "https://ipv4.myexternalip.com/raw"
        "https://4.ident.me"
        "https://check-host.net/ip"
    )
    local server_ip=""
    for ip_address in "${URL_lists[@]}"; do
        local response=$(curl -s -w "\n%{http_code}" --max-time 3 "${ip_address}" 2> /dev/null)
        local http_code=$(echo "$response" | tail -n1)
        local ip_result=$(echo "$response" | head -n-1 | tr -d '[:space:]"')
        if [[ "${http_code}" == "200" && "${ip_result}" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
            server_ip="${ip_result}"
            break
        fi
    done

    if [[ -z "$server_ip" ]]; then
        echo -e "${yellow}Could not auto-detect server IP from any provider.${plain}"
        while [[ -z "$server_ip" ]]; do
            read -rp "Please enter your server's public IPv4 address: " server_ip
            server_ip="${server_ip// /}"
            if [[ ! "$server_ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
                echo -e "${red}Invalid IPv4 address. Please try again.${plain}"
                server_ip=""
            fi
        done
    fi

    # Handle missing/short webBasePath
    if [[ ${#existing_webBasePath} -lt 4 ]]; then
        echo -e "${yellow}WebBasePath is missing or too short. Generating a new one...${plain}"
        local config_webBasePath=$(gen_random_string 18)
        ${xui_folder}/x-ui setting -webBasePath "${config_webBasePath}"
        existing_webBasePath="${config_webBasePath}"
        panel_needs_restart=1
        echo -e "${green}New WebBasePath: ${config_webBasePath}${plain}"
    fi

    # Check and prompt for SSL if missing
    if [[ -z "$existing_cert" ]]; then
        echo ""
        echo -e "${red}═══════════════════════════════════════════${plain}"
        echo -e "${red}      ⚠ NO SSL CERTIFICATE DETECTED ⚠     ${plain}"
        echo -e "${red}═══════════════════════════════════════════${plain}"
        echo -e "${yellow}For security, SSL certificate is MANDATORY for all panels.${plain}"
        echo -e "${yellow}Let's Encrypt now supports both domains and IP addresses!${plain}"
        echo ""

        # Prompt and setup SSL (domain or IP)
        prompt_and_setup_ssl "${existing_port}" "${existing_webBasePath}" "${server_ip}"

        echo ""
        echo -e "${green}═══════════════════════════════════════════${plain}"
        echo -e "${green}     Panel Access Information              ${plain}"
        echo -e "${green}═══════════════════════════════════════════${plain}"
        echo -e "${green}Access URL: https://${SSL_HOST}:${existing_port}/${existing_webBasePath}${plain}"
        echo -e "${green}═══════════════════════════════════════════${plain}"
        echo -e "${yellow}⚠ SSL Certificate: Enabled and configured${plain}"
    else
        echo -e "${green}SSL certificate is already configured${plain}"
        # Show access URL with existing certificate
        local cert_domain=$(basename "$(dirname "$existing_cert")")
        echo ""
        echo -e "${green}═══════════════════════════════════════════${plain}"
        echo -e "${green}     Panel Access Information              ${plain}"
        echo -e "${green}═══════════════════════════════════════════${plain}"
        echo -e "${green}Access URL: https://${cert_domain}:${existing_port}/${existing_webBasePath}${plain}"
        echo -e "${green}═══════════════════════════════════════════${plain}"
    fi

    if [[ "$panel_needs_restart" -eq 1 ]]; then
        echo -e "${yellow}Restarting panel to apply the new web base path...${plain}"
        systemctl restart x-ui 2> /dev/null || rc-service x-ui restart 2> /dev/null
    fi
}

# setup_fail2ban auto-installs and configures fail2ban for the IP Limit feature
# by invoking the freshly downloaded x-ui CLI. IP Limit is load-bearing on
# fail2ban (without it the panel disables the limitIp field and zeroes existing
# limits), so updating an older install should make it work without a manual
# trip through the IP Limit menu. Non-fatal: a fail2ban failure must never abort
# the update. XUI_ENABLE_FAIL2BAN is honored (load_xui_env exports it from the
# persisted env file, so a deliberate opt-out survives updates).
setup_fail2ban() {
    if [[ -n "${XUI_ENABLE_FAIL2BAN+x}" && "${XUI_ENABLE_FAIL2BAN}" != "true" ]]; then
        echo -e "${yellow}XUI_ENABLE_FAIL2BAN=${XUI_ENABLE_FAIL2BAN}, skipping Fail2ban auto-setup.${plain}"
        return 0
    fi

    if [[ ! -x /usr/bin/x-ui ]]; then
        echo -e "${yellow}x-ui CLI not found; skipping Fail2ban auto-setup.${plain}"
        return 0
    fi

    # Scripts older than v3.4.0 have no setup-fail2ban and exit 0 from the
    # usage banner, which would read as success here.
    if ! grep -q '"setup-fail2ban")' /usr/bin/x-ui; then
        echo -e "${yellow}This x-ui.sh predates 'x-ui setup-fail2ban'; skipping Fail2ban auto-setup.${plain}"
        return 0
    fi

    echo -e "${green}Setting up Fail2ban for the IP Limit feature...${plain}"
    if /usr/bin/x-ui setup-fail2ban; then
        echo -e "${green}Fail2ban setup complete.${plain}"
    else
        echo -e "${yellow}Fail2ban setup did not finish; IP Limit stays disabled until you run 'x-ui' and open the IP Limit menu. Continuing.${plain}"
    fi
    return 0
}

# Bootstrap is fetched only from this fork over HTTPS. Its mandatory checksum
# protects download integrity; release provenance is checked by the helper.
_download_update_bootstrap() {
    local url="$1" dest="$2" limit="$3" bytes
    [[ -n "$curl_bin" ]] || _fail "ERROR: Command 'curl' is required for online updates."
    if ! (set -o pipefail; "${curl_bin}" --fail --silent --location \
        --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 120 \
        "$url" | head -c "$((limit + 1))" > "$dest"); then
        _fail "ERROR: Failed to download the managed fork update helper."
    fi
    bytes=$(wc -c < "$dest")
    [[ "$bytes" -gt 0 && "$bytes" -le "$limit" ]] || _fail "ERROR: Update helper download has invalid size."
}

_prepare_managed_update() {
    local platform="linux-$(arch)" helper_asset helper_url expected actual helper
    local parent
    parent=$(cd -- "$(dirname -- "${xui_folder}")" && pwd -P) || _fail "ERROR: Installation parent is unavailable."
    xui_update_work=$(mktemp -d "${parent}/.x-ui-update-XXXXXXXX") || _fail "ERROR: Cannot create a private update directory."
    helper="${xui_update_work}/update-stage"
    tag_version="${XUI_UPDATE_TAG:-}"
    [[ -z "$tag_version" || "$tag_version" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$ ]] || _fail "ERROR: Invalid release tag."
    local args=(--preflight --parent "${xui_update_work}" --release-platform "$platform")
    if [[ -n "${XUI_UPDATE_ARCHIVE:-}" ]]; then
        [[ -n "${XUI_UPDATE_HELPER:-}" && "${XUI_UPDATE_HELPER_SHA256:-}" =~ ^[0-9a-f]{64}$ ]] || _fail "ERROR: Offline update requires a helper and its SHA256."
        [[ -f "$XUI_UPDATE_HELPER" && ! -L "$XUI_UPDATE_HELPER" ]] || _fail "ERROR: Offline helper must be a regular file."
        cp -- "$XUI_UPDATE_HELPER" "$helper" || _fail "ERROR: Cannot copy offline update helper."
        expected="$XUI_UPDATE_HELPER_SHA256"
        [[ "${XUI_UPDATE_SHA256:-}" =~ ^[0-9a-f]{64}$ && "${XUI_UPDATE_COMMIT:-}" =~ ^[0-9a-f]{40}$ && -n "$tag_version" ]] || _fail "ERROR: Offline update requires archive SHA256, full commit and tag."
        args+=(--archive "$XUI_UPDATE_ARCHIVE" --sha256 "$XUI_UPDATE_SHA256" --release-commit "$XUI_UPDATE_COMMIT" --release-tag "$tag_version")
    else
        [[ -z "${XUI_UPDATE_SHA256:-}${XUI_UPDATE_COMMIT:-}${XUI_UPDATE_HELPER:-}${XUI_UPDATE_HELPER_SHA256:-}" ]] || _fail "ERROR: Offline inputs require XUI_UPDATE_ARCHIVE."
        helper_asset="update-stage-${platform}"
        helper_url="https://github.com/0x3f3f3f3f3f3f3f3f3f3f3/3x-ui/releases/latest/download/${helper_asset}"
        if [[ -n "$tag_version" ]]; then
            helper_url="https://github.com/0x3f3f3f3f3f3f3f3f3f3f3/3x-ui/releases/download/${tag_version}/${helper_asset}"
        fi
        _download_update_bootstrap "${helper_url}.sha256" "${helper}.sha256" 4096
        expected=$(awk -v name="$helper_asset" 'NF != 2 || $2 != name || NR != 1 {bad=1} END {if (bad || NR != 1) exit 1; print $1}' "${helper}.sha256") || _fail "ERROR: Invalid update helper checksum file."
        [[ "$expected" =~ ^[0-9a-f]{64}$ ]] || _fail "ERROR: Invalid update helper SHA256."
        _download_update_bootstrap "$helper_url" "$helper" 33554432
        args+=(--download)
        [[ -z "$tag_version" ]] || args+=(--release-tag "$tag_version")
    fi
    actual=$(sha256sum "$helper") || _fail "ERROR: Cannot hash update helper."
    actual="${actual%% *}"
    [[ "$expected" == "$actual" ]] || _fail "ERROR: Update helper checksum mismatch."
    chmod 700 "$helper" || _fail "ERROR: Cannot make update helper executable."
    if ! xui_update_stage=$("$helper" "${args[@]}"); then
        _fail "ERROR: Release validation or runtime preflight failed; the current installation is untouched."
    fi
    [[ "$xui_update_stage" == "${xui_update_work}/.x-ui-stage-"* && "$xui_update_stage" != *$'\n'* && -d "${xui_update_stage}/x-ui" ]] || _fail "ERROR: Invalid staged release path."
    echo -e "${green}Managed fork release validation and runtime preflight passed.${plain}"
}

_install_xui_service_unit() {
    local source="$1" dest="${xui_service}/x-ui.service" temp_file="${xui_service}/x-ui.service.tmp.$$"
    if ! cp -- "$source" "$temp_file" || ! chmod 644 "$temp_file" || ! chown root:root "$temp_file" || ! mv -f -- "$temp_file" "$dest"; then
        rm -f -- "$temp_file"
        return 1
    fi
}

update_x-ui() {
    load_xui_env
    [[ -d "$xui_folder" && ! -L "$xui_folder" && "$xui_folder" != / ]] || _fail "ERROR: Installation directory is unavailable."
    xui_folder=$(cd -- "$xui_folder" && pwd -P) || _fail "ERROR: Cannot resolve installation directory."
    [[ -f "${xui_folder}/x-ui" ]] || _fail "ERROR: Current x-ui version is unknown."
    local current_xui_version
    current_xui_version=$("${xui_folder}/x-ui" -v) || _fail "ERROR: Cannot read the installed panel version."
    echo -e "${green}Current x-ui version: ${current_xui_version}${plain}"
    if [[ "$release" == alpine ]]; then
        [[ -f /etc/init.d/x-ui ]] || _fail "ERROR: x-ui service unit is not installed."
    else
        [[ -f "${xui_service}/x-ui.service" ]] || _fail "ERROR: x-ui systemd unit is not installed."
    fi

    _prepare_managed_update
    install_base || _fail "ERROR: Cannot install update dependencies; the current installation is untouched."

    echo -e "${green}Stopping x-ui...${plain}"
    if [[ "$release" == alpine ]]; then
        rc-service x-ui stop || _fail "ERROR: Cannot stop the installed service; program files are unchanged."
    else
        systemctl stop x-ui || _fail "ERROR: Cannot stop the installed service; program files are unchanged."
    fi

    # Everything installed below comes from the preflighted release inventory.
    # Transactional activation, DB migration rollback and health recovery still
    # require implementation; a successful preflight alone is not that guarantee.
    echo -e "${green}Installing verified x-ui release...${plain}"
    cp -a -- "${xui_update_stage}/x-ui/." "${xui_folder}/" || _fail "ERROR: Cannot copy the verified release."
    cd -- "$xui_folder" || _fail "ERROR: Cannot enter the installation directory."
    local xui_script_temp="/usr/bin/x-ui-temp.$$"
    if ! cp -- "${xui_folder}/x-ui.sh" "$xui_script_temp" || ! chmod 755 "$xui_script_temp" || ! mv -f -- "$xui_script_temp" /usr/bin/x-ui; then
        rm -f -- "$xui_script_temp"
        _fail "ERROR: Cannot install the bundled control menu."
    fi
    mkdir -p /var/log/x-ui || _fail "ERROR: Cannot create the log directory."
    chown -R root:root "$xui_folder" || _fail "ERROR: Cannot set installation ownership."
    if [[ -f "${xui_folder}/bin/config.json" ]]; then
        chmod 640 "${xui_folder}/bin/config.json" || _fail "ERROR: Cannot set configuration permissions."
    fi
    if [[ "$release" == alpine ]]; then
        local xui_rc_temp="/etc/init.d/x-ui.tmp.$$"
        if ! cp -- "${xui_folder}/x-ui.rc" "$xui_rc_temp" || ! chmod 755 "$xui_rc_temp" || ! chown root:root "$xui_rc_temp" || ! mv -f -- "$xui_rc_temp" /etc/init.d/x-ui; then
            rm -f -- "$xui_rc_temp"
            _fail "ERROR: Cannot install the bundled OpenRC unit."
        fi
        rc-service x-ui start || _fail "ERROR: Updated service failed to start."
    else
        local unit
        case "$release" in
            ubuntu | debian | armbian) unit=x-ui.service.debian ;;
            arch | manjaro | parch) unit=x-ui.service.arch ;;
            *) unit=x-ui.service.rhel ;;
        esac
        _install_xui_service_unit "${xui_folder}/${unit}" || _fail "ERROR: Cannot install the bundled systemd unit."
        systemctl daemon-reload || _fail "ERROR: Cannot reload the service manager."
        systemctl start x-ui || _fail "ERROR: Updated service failed to start."
    fi
    config_after_update
    setup_fail2ban
    echo -e "${green}Managed fork x-ui update finished.${plain}"
}

echo -e "${green}Running...${plain}"
update_x-ui
