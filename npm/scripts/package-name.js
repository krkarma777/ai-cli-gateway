const PACKAGE_PATTERN = /^(?:@[a-z0-9][a-z0-9._-]*\/)?[a-z0-9][a-z0-9._-]*$/u;
const VERSION_PATTERN = /^(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)$/u;

export function npmTarballFilename(name, version) {
  if (
    typeof name !== "string" ||
    typeof version !== "string" ||
    !PACKAGE_PATTERN.test(name) ||
    !VERSION_PATTERN.test(version)
  ) {
    throw new TypeError("invalid npm package identity");
  }
  const filenameName = name.startsWith("@")
    ? name.slice(1).replace("/", "-")
    : name;
  return `${filenameName}-${version}.tgz`;
}
