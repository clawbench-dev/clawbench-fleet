#!/usr/bin/env bash
# End-to-end rehearsal of the npm release, no registry involved.
#
#   ./npm/dry-run.sh                # stage 0.0.0-local, pack, install, verify
#
# Stages every package into dist/npm, turns them into tarballs, installs them
# into a throwaway prefix the way a user would, then checks the launcher finds
# the platform binary and that the binary reports the version we asked for.
# The systemd installer is exercised here too, since it ships inside the same
# package.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-0.0.0-local}"
OUT="$(pwd)/dist/npm"
PREFIX="$(pwd)/dist/npm-smoke/prefix"

if [[ ! -f web/dist/index.html ]]; then
  echo "==> 前端没构建，先跑 ./build.sh"
  ./build.sh
fi

rm -rf dist/npm dist/npm-smoke
node npm/pack.mjs --version "${VERSION}" --out "${OUT}"

echo "==> 打 tarball"
for dir in "${OUT}"/*/; do
  (cd "${dir}" && npm pack --silent --pack-destination "${OUT}" >/dev/null)
done
ls -lh "${OUT}"/*.tgz | awk '{print "   ", $9, $5}'

# A local registry is required: npm only resolves optionalDependencies from a
# registry, never from sibling tarballs, and skipping this would install the
# wrapper alone and hide a broken dependency graph until publish time.
#
# The throwaway .npmrc matters: it keeps the machine's own ~/.npmrc (a real
# registry token) out of the rehearsal. npm refuses to publish without any
# credentials at all, so the token below is a dummy — the throwaway registry
# accepts anonymous publishing and never looks at it.
echo "==> 起本地临时源（npm 模拟）"
PUBLISHED="${OUT}/published"
mkdir -p "${PUBLISHED}"
VERDACCIO_PORT=4873
NPMRC="${OUT}/npmrc"
{
  printf 'registry=http://127.0.0.1:%s/\n' "${VERDACCIO_PORT}"
  printf '//127.0.0.1:%s/:_authToken=dry-run\n' "${VERDACCIO_PORT}"
} >"${NPMRC}"
cat >"${OUT}/verdaccio.yaml" <<'YAML'
storage: /tmp/verdaccio-fleet-storage
auth:
  htpasswd:
    file: /tmp/verdaccio-fleet-htpasswd
uplinks: {}
packages:
  '@*/*':
    access: $anonymous
    publish: $anonymous
  '**':
    access: $anonymous
    publish: $anonymous
log: { type: stdout, format: pretty, level: warn }
YAML
rm -rf /tmp/verdaccio-fleet-storage /tmp/verdaccio-fleet-htpasswd
npx --yes verdaccio@6 --listen "${VERDACCIO_PORT}" --config "${OUT}/verdaccio.yaml" >"${OUT}/verdaccio.log" 2>&1 &
REGISTRY_PID=$!
trap 'kill "${REGISTRY_PID}" 2>/dev/null || true' EXIT
for _ in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:${VERDACCIO_PORT}/-/ping" >/dev/null 2>&1; then break; fi
  sleep 1
done
curl -fsS "http://127.0.0.1:${VERDACCIO_PORT}/-/ping" >/dev/null || {
  echo "本地源没起来，见 ${OUT}/verdaccio.log"; exit 1;
}
echo "  本地源 http://127.0.0.1:${VERDACCIO_PORT}"

echo "==> 发到本地源（平台包先发，主包后发，和真实发布顺序一致）"
for tgz in "${OUT}"/xulongzhe-clawbench-fleet-linux-*.tgz; do
  npm publish "${tgz}" --registry "http://127.0.0.1:${VERDACCIO_PORT}" --tag latest --userconfig "${NPMRC}"
done
npm publish "${OUT}/xulongzhe-clawbench-fleet-${VERSION}.tgz" --registry "http://127.0.0.1:${VERDACCIO_PORT}" --tag latest --userconfig "${NPMRC}"

echo "==> 安装到临时目录（模拟用户 npm i -g）"
mkdir -p "${PREFIX}"
npm install -g --prefix "${PREFIX}" --no-audit --no-fund --loglevel=error \
  --registry "http://127.0.0.1:${VERDACCIO_PORT}" --userconfig "${NPMRC}" \
  "@xulongzhe/clawbench-fleet@${VERSION}"

BIN="${PREFIX}/bin/clawbench-fleet"
[[ -x "${BIN}" ]] || { echo "启动脚本不存在或不可执行：${BIN}"; exit 1; }

echo "==> 验证版本透传"
GOT="$("${BIN}" --version)"
[[ "${GOT}" == "${VERSION}" ]] || { echo "版本不对：期望 ${VERSION}，实际 ${GOT}"; exit 1; }
echo "  ${BIN} --version -> ${GOT}"

echo "==> 验证参数原样转交给真正的二进制"
PLATFORM_BIN="$(find "${PREFIX}/lib/node_modules/@xulongzhe" -name clawbench-fleet -type f -path '*/bin/*' ! -name '*.js' | head -1)"
[[ -n "${PLATFORM_BIN}" ]] || { echo "没找到平台包里的二进制"; exit 1; }
DIRECT="$("${PLATFORM_BIN}" --version)"
[[ "${DIRECT}" == "${GOT}" ]] || { echo "启动脚本与二进制版本不一致：${GOT} vs ${DIRECT}"; exit 1; }
echo "  ${PLATFORM_BIN} --version -> ${DIRECT}"

echo "==> 验证安装脚本随包发布"
INSTALLER="${PREFIX}/lib/node_modules/@xulongzhe/clawbench-fleet/deploy/install.sh"
[[ -x "${INSTALLER}" ]] || { echo "deploy/install.sh 没打进包里或不可执行"; exit 1; }
bash -n "${INSTALLER}"
echo "  ${INSTALLER} 语法检查通过"

echo
echo "预演通过。发布前记得清掉临时产物：rm -rf dist/"
