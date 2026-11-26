import fs from "node:fs/promises";
import path from "node:path";
import { createRequire } from "node:module";
import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";
import standaloneCode from "ajv/dist/standalone/index.js";
import { compileFromFile } from "json-schema-to-typescript";
import {
  cloneWithID,
  contractsDir,
  distDir,
  ensureCleanDir,
  goldenDir,
  listContractJSONFiles,
  listSchemaFiles,
  packageDir,
  readJSON,
  schemaAliasIDs,
  schemaExportNameFor,
  typeNameFor,
  validatorNameFor,
  walkJSON
} from "./contracts-common.mjs";

const require = createRequire(import.meta.url);
const packageJSON = require(path.join(packageDir, "package.json"));
const contractsVersion = (await fs.readFile(path.join(contractsDir, "VERSION"), "utf8")).trim();

await ensureCleanDir(distDir);
await fs.mkdir(path.join(distDir, "schemas"), { recursive: true });
await fs.mkdir(path.join(distDir, "types"), { recursive: true });
await fs.mkdir(path.join(distDir, "validators"), { recursive: true });
await fs.mkdir(path.join(distDir, "golden"), { recursive: true });

const jsonFiles = await listContractJSONFiles();
const schemaFiles = await listSchemaFiles();
const goldenFiles = (await walkJSON(goldenDir))
  .map((file) => path.relative(goldenDir, file))
  .sort();
const protocolBindingsContract = await readJSON(path.join(contractsDir, "runtime", "protocol_bindings_contract.json"));
const enumEntries = Object.entries(protocolBindingsContract)
  .filter(([, value]) => Array.isArray(value) && value.every((item) => typeof item === "string"))
  .map(([fieldName, values]) => ({
    fieldName,
    constName: enumConstNameFor(fieldName),
    typeName: enumTypeNameFor(fieldName),
    values
  }));

for (const relPath of jsonFiles) {
  const source = path.join(contractsDir, relPath);
  const target = path.join(distDir, "schemas", relPath);
  await fs.mkdir(path.dirname(target), { recursive: true });
  await fs.copyFile(source, target);
}

for (const relPath of goldenFiles) {
  const source = path.join(goldenDir, relPath);
  const target = path.join(distDir, "golden", relPath);
  await fs.mkdir(path.dirname(target), { recursive: true });
  await fs.copyFile(source, target);
}

const typeExports = [];
for (const relPath of schemaFiles) {
  const source = path.join(contractsDir, relPath);
  const schema = await readJSON(source);
  const typeName = typeNameFor(schema, relPath);
  const output = await compileFromFile(source, {
    bannerComment: "",
    cwd: path.dirname(source),
    style: {
      singleQuote: false,
      semi: true
    },
    unreachableDefinitions: true
  });
  const targetRel = relPath.replace(/\.schema\.json$/, ".d.ts");
  const target = path.join(distDir, "types", targetRel);
  await fs.mkdir(path.dirname(target), { recursive: true });
  await fs.writeFile(target, output, "utf8");
  typeExports.push({ typeName, targetRel: targetRel.replace(/\.d\.ts$/, "") });
}

const ajv = new Ajv2020({
  allErrors: true,
  strict: false,
  code: {
    source: true,
    esm: false
  }
});
addFormats(ajv);

const validators = {};
for (const relPath of schemaFiles) {
  const schema = await readJSON(path.join(contractsDir, relPath));
  ajv.addSchema(schema);
  for (const aliasID of schemaAliasIDs(schema, relPath)) {
    ajv.addSchema(cloneWithID(schema, aliasID));
  }
}

for (const relPath of schemaFiles) {
  const schema = await readJSON(path.join(contractsDir, relPath));
  const validate = ajv.getSchema(schema.$id);
  if (!validate) {
    throw new Error(`AJV did not compile ${relPath} (${schema.$id})`);
  }
  validators[validatorNameFor(schema, relPath)] = schema.$id;
}

await fs.writeFile(
  path.join(distDir, "validators", "index.js"),
  standaloneCode(ajv, validators),
  "utf8"
);

const schemaEntries = jsonFiles.map((relPath) => {
  const exportName = schemaExportNameFor(relPath);
  const requirePath = `./schemas/${relPath}`;
  return `  ${JSON.stringify(exportName)}: require(${JSON.stringify(requirePath)})`;
});
const enumJSDecls = enumEntries.map(({ constName, values }) => {
  return `const ${constName} = Object.freeze(${JSON.stringify(values)});`;
});
const enumObjectEntries = enumEntries.map(({ constName }) => {
  return `  ${constName}: ${constName}`;
});
const goldenEntries = goldenFiles.map((relPath) => {
  return `  ${JSON.stringify(relPath)}: require(${JSON.stringify(`./golden/${relPath}`)})`;
});
const exportEnumNames = enumEntries.map(({ constName }) => `  ${constName},`);

const indexJS = `"use strict";

const path = require("path");
const validators = require("./validators");

${enumJSDecls.join("\n")}

const schemas = {
${schemaEntries.join(",\n")}
};

const enums = Object.freeze({
${enumObjectEntries.join(",\n")}
});

const goldenFixtureNames = Object.freeze(${JSON.stringify(goldenFiles)});
const goldenFixtures = Object.freeze({
${goldenEntries.join(",\n")}
});
const goldenDir = path.join(__dirname, "golden");

function assertGoldenFixtureName(name) {
  if (!Object.prototype.hasOwnProperty.call(goldenFixtures, name)) {
    throw new Error("unknown golden fixture: " + name);
  }
}

function goldenFixturePath(name) {
  assertGoldenFixtureName(name);
  return path.join(goldenDir, name);
}

function loadGoldenFixture(name) {
  assertGoldenFixtureName(name);
  return JSON.parse(JSON.stringify(goldenFixtures[name]));
}

module.exports = {
  contractVersion: ${JSON.stringify(contractsVersion)},
${exportEnumNames.join("\n")}
  enums,
  schemas,
  goldenDir,
  goldenFixtureNames,
  goldenFixtures,
  goldenFixturePath,
  loadGoldenFixture,
  ...validators
};
`;
await fs.writeFile(path.join(distDir, "index.js"), indexJS, "utf8");

const enumDecls = enumEntries.map(({ constName, typeName, values }) => {
  return `export const ${constName}: readonly ${tupleType(values)};\nexport type ${typeName} = typeof ${constName}[number];`;
});
const enumObjectDecls = enumEntries.map(({ constName }) => {
  return `  readonly ${constName}: typeof ${constName};`;
});
const validatorDecls = Object.keys(validators)
  .sort()
  .map((name) => `export const ${name}: ValidateFunction;`);
const schemaDecls = jsonFiles
  .map((relPath) => `  readonly ${schemaExportNameFor(relPath)}: unknown;`);
const typeExportDecls = typeExports
  .map(({ targetRel }) => `export * from "./types/${targetRel}";`);
const indexDTS = `import type { ValidateFunction } from "ajv";

export const contractVersion: string;
${enumDecls.join("\n")}
export const enums: {
${enumObjectDecls.join("\n")}
};
export const schemas: {
${schemaDecls.join("\n")}
};
export const goldenDir: string;
export const goldenFixtureNames: readonly ${tupleType(goldenFiles)};
export type GoldenFixtureName = typeof goldenFixtureNames[number];
export const goldenFixtures: Readonly<Record<GoldenFixtureName, unknown>>;
export function goldenFixturePath(name: GoldenFixtureName): string;
export function loadGoldenFixture(name: GoldenFixtureName): unknown;

${validatorDecls.join("\n")}

${typeExportDecls.join("\n")}
`;
await fs.writeFile(path.join(distDir, "index.d.ts"), indexDTS, "utf8");

const manifest = {
  package: packageJSON.name,
  version: contractsVersion,
  schemas: jsonFiles,
  enums: enumEntries.map(({ fieldName }) => fieldName).sort(),
  golden: goldenFiles,
  validators: Object.keys(validators).sort(),
  types: typeExports.map(({ targetRel }) => `${targetRel}.d.ts`).sort()
};
await fs.writeFile(path.join(distDir, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`, "utf8");

console.log(`built ${packageJSON.name}@${contractsVersion}`);
console.log(`schemas=${jsonFiles.length} types=${typeExports.length} validators=${Object.keys(validators).length} enums=${enumEntries.length} golden=${goldenFiles.length}`);

function enumConstNameFor(fieldName) {
  return fieldName.toUpperCase();
}

function enumTypeNameFor(fieldName) {
  const overrides = {
    capabilities: "Capability",
    client_compatibilities: "ClientCompatibility",
    provider_profiles: "ProviderProfile",
    token_count_strategies: "TokenCountStrategy",
    compat_api_types: "CompatAPIType"
  };
  if (overrides[fieldName]) {
    return overrides[fieldName];
  }
  const words = fieldName.split("_");
  const last = words[words.length - 1];
  if (last.endsWith("ies")) {
    words[words.length - 1] = `${last.slice(0, -3)}y`;
  } else if (last.endsWith("s")) {
    words[words.length - 1] = last.slice(0, -1);
  }
  return words.map((word) => word.charAt(0).toUpperCase() + word.slice(1)).join("");
}

function tupleType(values) {
  return `[${values.map((value) => JSON.stringify(value)).join(", ")}]`;
}
