import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

export const scriptDir = path.dirname(fileURLToPath(import.meta.url));
export const packageDir = path.resolve(scriptDir, "..");
export const contractsDir = path.resolve(packageDir, "..");
export const distDir = path.join(packageDir, "dist");
export const goldenDir = path.join(contractsDir, "golden");

export async function readJSON(file) {
  return JSON.parse(await fs.readFile(file, "utf8"));
}

export async function listContractJSONFiles() {
  const roots = ["intent", "runtime"];
  const files = [];
  for (const root of roots) {
    const rootDir = path.join(contractsDir, root);
    for (const file of await walkJSON(rootDir)) {
      files.push(path.relative(contractsDir, file));
    }
  }
  return files.sort();
}

export async function listSchemaFiles() {
  return (await listContractJSONFiles()).filter((file) => file.endsWith(".schema.json"));
}

export async function walkJSON(dir) {
  const entries = await fs.readdir(dir, { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const fullPath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      files.push(...await walkJSON(fullPath));
    } else if (entry.isFile() && entry.name.endsWith(".json")) {
      files.push(fullPath);
    }
  }
  return files.sort();
}

export async function ensureCleanDir(dir) {
  await fs.rm(dir, { recursive: true, force: true });
  await fs.mkdir(dir, { recursive: true });
}

export function pascalCase(input) {
  return input
    .replace(/\.schema\.json$/, "")
    .replace(/\.json$/, "")
    .split(/[^a-zA-Z0-9]+/)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join("");
}

export function camelCase(input) {
  const pascal = pascalCase(input);
  return pascal.charAt(0).toLowerCase() + pascal.slice(1);
}

export function typeNameFor(schema, relPath) {
  return schema.title || pascalCase(path.basename(relPath));
}

export function validatorNameFor(schema, relPath) {
  return `validate${typeNameFor(schema, relPath)}`;
}

export function schemaExportNameFor(relPath) {
  return camelCase(path.basename(relPath));
}

export function schemaAliasIDs(schema, relPath) {
  const aliases = new Set();
  aliases.add(`https://tokenplatform.local/contracts/${relPath}`);
  aliases.add(`https://schemas.tokenplatform.dev/publisher/${relPath}`);

  if (relPath.startsWith("runtime/") && relPath.endsWith(".schema.json")) {
    const name = path.basename(relPath, ".schema.json");
    aliases.add(`https://schemas.tokenplatform.dev/publisher/runtime/${name}.schema.json`);
  }

  if (schema.$id) {
    aliases.delete(schema.$id);
  }
  return [...aliases].sort();
}

export function cloneWithID(schema, id) {
  return {
    ...JSON.parse(JSON.stringify(schema)),
    $id: id
  };
}

export function goldenValidatorName(fileName) {
  const baseName = path.basename(fileName);
  const mappings = [
    ["runtime_intent.", "validateRuntimeIntent"],
    ["provider_info.", "validateProviderInfo"],
    ["key_meta.", "validateKeyMeta"],
    ["key_pool.", "validateKeyPool"],
    ["route_config.", "validateRouteConfig"],
    ["platform_config.", "validatePlatformConfig"],
    ["product_route_graph.", "validateProductRouteGraph"],
    ["product_runtime_policy.", "validateProductRuntimePolicy"]
  ];
  for (const [prefix, validator] of mappings) {
    if (baseName.startsWith(prefix)) {
      return validator;
    }
  }
  return null;
}
