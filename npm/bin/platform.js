// Platform → npm package resolution for the @xulongzhe/clawbench-fleet
// platform binaries. Kept as a pure module (no side effects) so it can be
// unit tested independently of the launcher's spawn logic.

export const PLATFORM_MAP = {
  "linux-x64": "@xulongzhe/clawbench-fleet-linux-x64",
  "linux-arm64": "@xulongzhe/clawbench-fleet-linux-arm64",
};

export function resolvePlatformKey(platform, arch) {
  return `${platform}-${arch}`;
}

// Returns { key, pkg } for a supported platform/arch, or null if unsupported.
export function resolvePlatformPackage(platform, arch) {
  const key = resolvePlatformKey(platform, arch);
  const pkg = PLATFORM_MAP[key];
  if (!pkg) return null;
  return { key, pkg };
}

// Linux only: the platform drives systemd, useradd and cgroups, so there is no
// Windows or macOS build to name a binary for.
export const BIN_NAME = "clawbench-fleet";
