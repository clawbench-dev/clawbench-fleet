#!/usr/bin/env bash
# Install (or uninstall) clawbench-fleet on a Linux server.
#
#   sudo ./deploy/install.sh                # install and start
#   sudo ./deploy/install.sh --binary /path/to/clawbench-fleet
#   sudo ./deploy/install.sh --uninstall    # stop service, remove binary (data kept)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_SRC="${SCRIPT_DIR}/../clawbench-fleet"
BIN_DST="/usr/local/bin/clawbench-fleet"
CONF_DIR="/etc/clawbench-fleet"
DATA_DIR="/var/lib/clawbench-fleet"
INSTALL_DIR="/opt/clawbench-fleet"
UNIT="/etc/systemd/system/clawbench-fleet.service"

ACTION="install"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --binary) BIN_SRC="$2"; shift 2 ;;
    --uninstall) ACTION="uninstall"; shift ;;
    *) echo "unknown option: $1"; exit 1 ;;
  esac
done

if [[ "${EUID}" -ne 0 ]]; then
  echo "请用 root 运行（sudo ./deploy/install.sh）"
  exit 1
fi

if [[ "${ACTION}" == "uninstall" ]]; then
  systemctl disable --now clawbench-fleet.service 2>/dev/null || true
  rm -f "${BIN_DST}" "${UNIT}"
  systemctl daemon-reload || true
  echo "已停止并卸载服务；数据保留在 ${DATA_DIR}（租户账号和家目录不受影响）"
  exit 0
fi

[[ -x "${BIN_SRC}" ]] || { echo "找不到二进制 ${BIN_SRC}，先运行 ./build.sh"; exit 1; }
command -v systemctl >/dev/null || { echo "需要 systemd"; exit 1; }

install -m 0755 "${BIN_SRC}" "${BIN_DST}"
install -d -m 0755 "${CONF_DIR}"
install -d -m 0700 "${DATA_DIR}"
install -d -m 0755 "${INSTALL_DIR}/versions"

if [[ ! -f "${CONF_DIR}/config.yaml" ]]; then
  install -m 0600 "${SCRIPT_DIR}/config.example.yaml" "${CONF_DIR}/config.yaml"
  echo "已生成配置 ${CONF_DIR}/config.yaml"
fi

install -m 0644 "${SCRIPT_DIR}/clawbench-fleet.service" "${UNIT}"

systemctl daemon-reload
systemctl enable --now clawbench-fleet.service

for _ in $(seq 1 20); do
  [[ -f "${DATA_DIR}/initial-password" ]] && break
  sleep 0.5
done

echo
echo "安装完成。"
if [[ -f "${DATA_DIR}/initial-password" ]]; then
  echo "  地址： http://$(hostname -I 2>/dev/null | awk '{print $1}'):30000"
  echo "  账号： admin"
  echo "  密码： $(cat "${DATA_DIR}/initial-password")"
  echo
  echo "首次登录后请立即在“设置”里修改密码，并删除这个文件："
  echo "  rm ${DATA_DIR}/initial-password"
else
  echo "  已是升级安装，管理员密码不变。"
fi
echo
echo "下一步：打开“升级中心”上传一份 Clawbench 二进制，然后就可以建租户了。"
