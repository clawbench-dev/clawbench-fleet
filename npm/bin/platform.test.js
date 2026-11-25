import test from "node:test";
import assert from "node:assert/strict";
import {
  PLATFORM_MAP,
  BIN_NAME,
  resolvePlatformKey,
  resolvePlatformPackage,
} from "./platform.js";

test("linux x64 resolves to its platform package", () => {
  assert.deepEqual(resolvePlatformPackage("linux", "x64"), {
    key: "linux-x64",
    pkg: "@xulongzhe/clawbench-fleet-linux-x64",
  });
});

test("linux arm64 resolves to its platform package", () => {
  assert.deepEqual(resolvePlatformPackage("linux", "arm64"), {
    key: "linux-arm64",
    pkg: "@xulongzhe/clawbench-fleet-linux-arm64",
  });
});

test("unsupported platforms resolve to null", () => {
  for (const [platform, arch] of [
    ["darwin", "arm64"],
    ["win32", "x64"],
    ["linux", "ia32"],
    ["android", "arm64"],
  ]) {
    assert.equal(resolvePlatformPackage(platform, arch), null, `${platform}-${arch}`);
  }
});

test("every mapped package name carries the fleet prefix", () => {
  for (const pkg of Object.values(PLATFORM_MAP)) {
    assert.match(pkg, /^@xulongzhe\/clawbench-fleet-linux-/);
  }
});

test("binary name is clawbench-fleet", () => {
  assert.equal(BIN_NAME, "clawbench-fleet");
});

test("platform key joins platform and arch", () => {
  assert.equal(resolvePlatformKey("linux", "x64"), "linux-x64");
});
