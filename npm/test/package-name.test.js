import assert from "node:assert/strict";
import test from "node:test";
import { npmTarballFilename } from "../scripts/package-name.js";

test("npm tarball filenames encode the scope without @ or slash", () => {
  assert.equal(
    npmTarballFilename("@krkarma777/ai-cli-gateway-linux-x64", "0.2.1"),
    "krkarma777-ai-cli-gateway-linux-x64-0.2.1.tgz",
  );
  assert.equal(
    npmTarballFilename("ai-cli-gateway", "0.2.1"),
    "ai-cli-gateway-0.2.1.tgz",
  );
});

for (const [name, version] of [
  ["@krkarma777", "0.2.1"],
  ["@krkarma777/AI-CLI-Gateway", "0.2.1"],
  ["@krkarma777/ai-cli-gateway/linux", "0.2.1"],
  ["@krkarma777/ai-cli-gateway", "v0.2.1"],
  ["../ai-cli-gateway", "0.2.1"],
]) {
  test(`rejects invalid npm identity ${name}@${version}`, () => {
    assert.throws(
      () => npmTarballFilename(name, version),
      new TypeError("invalid npm package identity"),
    );
  });
}
