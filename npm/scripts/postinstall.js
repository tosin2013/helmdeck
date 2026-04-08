#!/usr/bin/env node
// postinstall: download the helmdeck-mcp binary matching the host's
// platform/arch from the GitHub release that corresponds to this
// package's version, verify its checksum against checksums.txt, and
// drop it into bin/ next to the launcher shim.
//
// Skipped automatically when HELMDECK_MCP_SKIP_DOWNLOAD=1 (so CI
// images that bake the binary in another way don't double-fetch).

"use strict";

const fs = require("fs");
const path = require("path");
const https = require("https");
const crypto = require("crypto");
const zlib = require("zlib");
const { execSync } = require("child_process");

if (process.env.HELMDECK_MCP_SKIP_DOWNLOAD === "1") {
  console.log("[helmdeck-mcp] HELMDECK_MCP_SKIP_DOWNLOAD=1, skipping binary download");
  process.exit(0);
}

const pkg = require("../package.json");
const VERSION = `v${pkg.version}`;
const REPO = "tosin2013/helmdeck";

const PLATFORM_MAP = {
  linux: "linux",
  darwin: "darwin",
  win32: "windows",
};
const ARCH_MAP = {
  x64: "amd64",
  arm64: "arm64",
};

const os = PLATFORM_MAP[process.platform];
const arch = ARCH_MAP[process.arch];
if (!os || !arch) {
  console.error(`[helmdeck-mcp] unsupported platform ${process.platform}/${process.arch}`);
  process.exit(1);
}

const ext = os === "windows" ? "zip" : "tar.gz";
const archiveName = `helmdeck-mcp_${pkg.version}_${os}_${arch}.${ext}`;
const baseURL = `https://github.com/${REPO}/releases/download/${VERSION}`;
const archiveURL = `${baseURL}/${archiveName}`;
const checksumsURL = `${baseURL}/checksums.txt`;

const binDir = path.join(__dirname, "..", "bin");
fs.mkdirSync(binDir, { recursive: true });

function get(url) {
  return new Promise((resolve, reject) => {
    https
      .get(url, { headers: { "User-Agent": "helmdeck-mcp-postinstall" } }, (res) => {
        if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
          resolve(get(res.headers.location));
          return;
        }
        if (res.statusCode !== 200) {
          reject(new Error(`GET ${url} -> ${res.statusCode}`));
          return;
        }
        const chunks = [];
        res.on("data", (c) => chunks.push(c));
        res.on("end", () => resolve(Buffer.concat(chunks)));
        res.on("error", reject);
      })
      .on("error", reject);
  });
}

(async () => {
  try {
    console.log(`[helmdeck-mcp] downloading ${archiveURL}`);
    const archive = await get(archiveURL);

    console.log(`[helmdeck-mcp] verifying checksum`);
    const checksums = (await get(checksumsURL)).toString("utf8");
    const wantLine = checksums.split("\n").find((l) => l.endsWith("  " + archiveName));
    if (!wantLine) {
      throw new Error(`checksum entry for ${archiveName} not found in checksums.txt`);
    }
    const want = wantLine.split(/\s+/)[0];
    const got = crypto.createHash("sha256").update(archive).digest("hex");
    if (got !== want) {
      throw new Error(`checksum mismatch: want ${want}, got ${got}`);
    }

    const archivePath = path.join(binDir, archiveName);
    fs.writeFileSync(archivePath, archive);

    console.log(`[helmdeck-mcp] extracting`);
    if (ext === "zip") {
      // Defer to a built-in unzip on each OS rather than ship a JS unzip dep.
      execSync(`powershell -Command "Expand-Archive -Force '${archivePath}' '${binDir}'"`, {
        stdio: "inherit",
      });
    } else {
      // Stream-decompress + untar inline so we don't shell out on Linux/Darwin.
      const tar = zlib.gunzipSync(archive);
      extractTar(tar, binDir);
    }
    fs.unlinkSync(archivePath);

    const binName = os === "windows" ? "helmdeck-mcp.exe" : "helmdeck-mcp";
    const binPath = path.join(binDir, binName);
    if (!fs.existsSync(binPath)) {
      throw new Error(`expected ${binPath} after extraction`);
    }
    if (os !== "windows") {
      fs.chmodSync(binPath, 0o755);
    }

    // Write the launcher shim referenced by package.json#bin so npm
    // can place it on $PATH regardless of the actual binary name.
    const shim =
      os === "windows"
        ? `#!/usr/bin/env node\nrequire("child_process").spawnSync(require("path").join(__dirname, "helmdeck-mcp.exe"), process.argv.slice(2), { stdio: "inherit" });\n`
        : `#!/usr/bin/env node\nrequire("child_process").spawnSync(require("path").join(__dirname, "helmdeck-mcp"), process.argv.slice(2), { stdio: "inherit" });\n`;
    fs.writeFileSync(path.join(binDir, "helmdeck-mcp.js"), shim);
    fs.chmodSync(path.join(binDir, "helmdeck-mcp.js"), 0o755);

    console.log(`[helmdeck-mcp] installed ${binPath}`);
  } catch (err) {
    console.error(`[helmdeck-mcp] postinstall failed: ${err.message}`);
    process.exit(1);
  }
})();

// Minimal POSIX-tar extractor: enough for goreleaser archives, which
// only contain regular files. Does not handle long-name extensions
// because goreleaser doesn't emit them for our binary names.
function extractTar(buf, dest) {
  let offset = 0;
  while (offset + 512 <= buf.length) {
    const header = buf.slice(offset, offset + 512);
    if (header[0] === 0) break;
    const name = header.slice(0, 100).toString("utf8").replace(/\0.*$/, "");
    const sizeStr = header.slice(124, 136).toString("utf8").replace(/\0.*$/, "").trim();
    const size = parseInt(sizeStr, 8);
    const type = header[156];
    offset += 512;
    if ((type === 0 || type === 0x30) && name) {
      const out = path.join(dest, path.basename(name));
      fs.writeFileSync(out, buf.slice(offset, offset + size));
    }
    offset += Math.ceil(size / 512) * 512;
  }
}
