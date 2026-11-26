import fs from "node:fs/promises";
import path from "node:path";
import { createRequire } from "node:module";
import {
  goldenDir,
  goldenValidatorName,
  packageDir,
  walkJSON
} from "./contracts-common.mjs";

const require = createRequire(import.meta.url);
const contractsPackage = require(path.join(packageDir, "dist", "index.js"));

const files = (await walkJSON(goldenDir))
  .map((file) => path.relative(goldenDir, file).split(path.sep).join("/"))
  .sort();

if (files.length === 0) {
  throw new Error("contracts/golden has no JSON fixtures");
}

let validated = 0;
for (const fileName of files) {
  const isInvalidFixture = fileName.startsWith("invalid/");
  const validatorName = goldenValidatorName(fileName);
  if (!validatorName) {
    throw new Error(`no golden validator mapping for ${fileName}`);
  }
  const validate = contractsPackage[validatorName];
  if (typeof validate !== "function") {
    throw new Error(`generated package does not export ${validatorName}`);
  }
  const payload = JSON.parse(await fs.readFile(path.join(goldenDir, fileName), "utf8"));
  const ok = validate(payload);
  if (isInvalidFixture) {
    if (ok) {
      throw new Error(`${fileName} unexpectedly passed ${validatorName}`);
    }
    console.log(`ok ${fileName} rejected by ${validatorName}: ${formatErrors(validate.errors)}`);
    validated += 1;
    continue;
  }
  if (!ok) {
    const errors = JSON.stringify(validate.errors, null, 2);
    throw new Error(`${fileName} failed ${validatorName}: ${errors}`);
  }
  validated += 1;
  console.log(`ok ${fileName} -> ${validatorName}`);
}

console.log(`validated ${validated} golden fixtures with generated AJV validators`);

function formatErrors(errors) {
  if (!Array.isArray(errors) || errors.length === 0) {
    return "no errors";
  }
  return errors
    .map((error) => `${error.instancePath || "/"} ${error.keyword}`)
    .join("; ");
}
