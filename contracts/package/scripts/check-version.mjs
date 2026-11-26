import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const packageDir = path.resolve(scriptDir, "..");
const contractsDir = path.resolve(packageDir, "..");

const packageJSON = JSON.parse(await fs.readFile(path.join(packageDir, "package.json"), "utf8"));
const contractsVersion = (await fs.readFile(path.join(contractsDir, "VERSION"), "utf8")).trim();

if (packageJSON.version !== contractsVersion) {
  throw new Error(`package.json version ${packageJSON.version} must match contracts/VERSION ${contractsVersion}`);
}

console.log(`ok package version ${packageJSON.version} matches contracts/VERSION`);
