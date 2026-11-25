#!/usr/bin/env node
// Stage the @xulongzhe/clawbench-fleet npm packages.
//
//   node npm/pack.mjs --version 0.1.0 --out dist/npm [--platform linux-x64]...
//
// Produces one wrapper package (the one users install) plus one package per
// platform holding the real binary. The wrapper only switches dist-tags, so all
// platform tarballs are staged before publishing — an incomplete release would
// let a user install a version whose binary package is missing.
//
// Without --platform every supported target is staged. The release workflow
// runs the script once per target on its own runner and uploads the tarballs,
// which keeps the frontend build out of the cross-compilation path.
import { execFileSync } from "node:child_process";
import { cpSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const SCOPE = "@xulongzhe";
const BASE = "clawbench-fleet";
const BIN = "clawbench-fleet";

// Both targets are Linux: the platform is a root-run systemd manager, so
// there is nothing to ship for macOS or Windows.
const TARGETS = {
  "linux-x64": { goarch: "amd64", description: "Linux x64 binary" },
  "linux-arm64": { goarch: "arm64", description: "Linux arm64 binary" },
};

const REPO_URL = "git+https://github.com/clawbench-dev/clawbench-fleet.git";
const DESCRIPTION =
  "Clawbench Fleet — 企业 IT 批量管理 Clawbench 实例的平台（Linux + systemd）";

function parseArgs(argv) {
  const opts = {
    version: "",
    out: join(ROOT, "dist", "npm"),
    platforms: [],
    skipFrontendCheck: false,
    wrapperOnly: false,
  };
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    const value = argv[i + 1];
    switch (arg) {
      case "--version":
        opts.version = value;
        i += 1;
        break;
      case "--out":
        opts.out = resolve(value);
        i += 1;
        break;
      case "--platform":
        opts.platforms.push(value);
        i += 1;
        break;
      case "--skip-frontend-check":
        opts.skipFrontendCheck = true;
        break;
      case "--wrapper-only":
        // No compilation and no frontend needed: the release job runs this to
        // rebuild the wrapper from the platform tarballs the matrix produced.
        opts.wrapperOnly = true;
        break;
      default:
        throw new Error(`未知参数 ${arg}`);
    }
  }
  if (!opts.version) {
    throw new Error("缺少 --version，例如 --version 0.1.0");
  }
  if (!/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(opts.version)) {
    throw new Error(`版本号必须是不带 v 前缀的 semver，收到 ${opts.version}`);
  }
  // An empty list means "everything"; validate whatever was named.
  for (const p of opts.platforms) {
    if (!TARGETS[p]) {
      throw new Error(`不支持的平台 ${p}，可选：${Object.keys(TARGETS).join(", ")}`);
    }
  }
  if (opts.platforms.length === 0) {
    opts.platforms = Object.keys(TARGETS);
  }
  return opts;
}

// Fails when web/dist is missing: the binary embeds the frontend, and a
// release built without it would serve an empty page.
function ensureFrontendBuilt() {
  const dist = join(ROOT, "web", "dist", "index.html");
  try {
    readFileSync(dist);
  } catch {
    throw new Error("web/dist 不存在，先运行 ./build.sh 或 (cd web && npm run build)");
  }
}

function buildBinary(version, platform, outFile) {
  const target = TARGETS[platform];
  console.log(`==> 编译 ${platform}（GOARCH=${target.goarch}）`);
  execFileSync(
    "go",
    [
      "build",
      "-trimpath",
      "-ldflags",
      `-s -w -X clawbench-fleet/internal/version.Version=${version}`,
      "-o",
      outFile,
      "./cmd/fleet",
    ],
    {
      cwd: ROOT,
      stdio: "inherit",
      env: { ...process.env, CGO_ENABLED: "0", GOOS: "linux", GOARCH: target.goarch },
    },
  );
}

// go build already writes 0755, but chmod keeps the mode independent of the
// umask of whatever shell ran the script: npm records the mode in the tarball,
// so a binary staged as 0644 would install non-executable.
function applyExecutableBit(file) {
  execFileSync("chmod", ["0755", file]);
}

function writeJson(file, value) {
  writeFileSync(file, `${JSON.stringify(value, null, 2)}\n`);
}

function stagePlatform(opts, platform) {
  const target = TARGETS[platform];
  const pkgDir = join(opts.out, platform);
  rmSync(pkgDir, { recursive: true, force: true });
  mkdirSync(join(pkgDir, "bin"), { recursive: true });

  const binFile = join(pkgDir, "bin", BIN);
  buildBinary(opts.version, platform, binFile);
  applyExecutableBit(binFile);
  cpSync(join(ROOT, "LICENSE"), join(pkgDir, "LICENSE"));

  writeJson(join(pkgDir, "package.json"), {
    name: `${SCOPE}/${BASE}-${platform}`,
    version: opts.version,
    repository: { type: "git", url: REPO_URL },
    description: `Clawbench Fleet — ${target.description}`,
    os: ["linux"],
    cpu: [target.goarch === "amd64" ? "x64" : "arm64"],
    preferUnplugged: true,
    files: ["bin/", "LICENSE"],
    license: "MIT",
  });
  console.log(`==> 平台包就绪 ${pkgDir}`);
}

function stageWrapper(opts) {
  const pkgDir = join(opts.out, "wrapper");
  rmSync(pkgDir, { recursive: true, force: true });
  mkdirSync(join(pkgDir, "bin"), { recursive: true });

  for (const name of ["clawbench-fleet.js", "platform.js"]) {
    cpSync(join(ROOT, "npm", "bin", name), join(pkgDir, "bin", name));
  }
  cpSync(join(ROOT, "LICENSE"), join(pkgDir, "LICENSE"));
  // The systemd installer travels with the wrapper: "npm i -g" is the only
  // step a user takes, so deploy/ has to arrive without a git checkout.
  cpSync(join(ROOT, "deploy"), join(pkgDir, "deploy"), { recursive: true });

  const optionalDependencies = {};
  for (const platform of Object.keys(TARGETS)) {
    optionalDependencies[`${SCOPE}/${BASE}-${platform}`] = opts.version;
  }

  // The wrapper depends on every platform package at the same version. npm
  // skips the ones whose os/cpu do not match this machine, so one install
  // works everywhere.
  writeJson(join(pkgDir, "package.json"), {
    name: `${SCOPE}/${BASE}`,
    version: opts.version,
    repository: { type: "git", url: REPO_URL },
    description: DESCRIPTION,
    type: "module",
    bin: { "clawbench-fleet": "bin/clawbench-fleet.js" },
    files: ["bin/", "deploy/", "LICENSE"],
    optionalDependencies,
    engines: { node: ">=18" },
    license: "MIT",
  });
  console.log(`==> 主包就绪 ${pkgDir}`);
}

function main() {
  const opts = parseArgs(process.argv.slice(2));
  if (!opts.wrapperOnly && !opts.skipFrontendCheck) {
    ensureFrontendBuilt();
  }

  // Staging one platform for the release workflow must not wipe the wrapper
  // and the other platform's output, which live in the same directory.
  if (!opts.wrapperOnly && opts.platforms.length === Object.keys(TARGETS).length) {
    rmSync(opts.out, { recursive: true, force: true });
  }
  mkdirSync(opts.out, { recursive: true });

  if (!opts.wrapperOnly) {
    for (const platform of opts.platforms) {
      stagePlatform(opts, platform);
    }
  }
  stageWrapper(opts);
  console.log(`==> 完成：${opts.out}`);
}

try {
  main();
} catch (err) {
  console.error(`打包失败：${err.message}`);
  process.exit(1);
}
