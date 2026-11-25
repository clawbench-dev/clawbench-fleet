#!/usr/bin/env node
// Launcher for @xulongzhe/clawbench-fleet. The binary itself lives in the
// per-platform package pulled in by optionalDependencies; this file only
// locates it and hands over argv untouched.
import { spawn } from "child_process";
import { resolve, dirname } from "path";
import { createRequire } from "module";
import os from "os";
import { resolvePlatformPackage, BIN_NAME } from "./platform.js";

const require = createRequire(import.meta.url);

const resolved = resolvePlatformPackage(process.platform, process.arch);
if (!resolved) {
  console.error(`clawbench-fleet: 不支持的平台 ${process.platform}-${process.arch}`);
  process.exit(1);
}

const { pkg } = resolved;

let binPath;
if (process.env.CLAWBENCH_FLEET_BINARY_PATH) {
  binPath = process.env.CLAWBENCH_FLEET_BINARY_PATH;
} else {
  try {
    binPath = resolve(dirname(require.resolve(`${pkg}/package.json`)), "bin", BIN_NAME);
  } catch {
    console.error(`clawbench-fleet: 平台包 "${pkg}" 未安装`);
    console.error(`  请运行: npm install ${pkg}`);
    console.error(`  或设置环境变量: CLAWBENCH_FLEET_BINARY_PATH=/path/to/clawbench-fleet`);
    process.exit(1);
  }
}

const child = spawn(binPath, process.argv.slice(2), { stdio: "inherit" });

// Signals are forwarded so Ctrl+C and `kill <launcher-pid>` stop the server.
// The launcher is not detached: this process runs in the foreground as a
// service, so it should die with its supervisor like the binary would.
process.on("SIGINT", () => child.kill("SIGINT"));
process.on("SIGTERM", () => child.kill("SIGTERM"));

child.on("exit", (code, signal) => {
  if (signal) {
    process.exit(128 + (os.constants.signals[signal] ?? 0));
  }
  process.exit(code ?? 1);
});

child.on("error", (err) => {
  console.error(`clawbench-fleet: 启动失败: ${err.message}`);
  process.exit(1);
});
