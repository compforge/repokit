/** Explicit checkout selection must not inherit a caller's repository-local Git context.
 * @spec Temporary index/object overrides are applied after checkoutEnvironment.
 * @see {@link component://docs/git.md}
 */
const local = new Set(["GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_IMPLICIT_WORK_TREE", "GIT_GRAFT_FILE", "GIT_SHALLOW_FILE", "GIT_PREFIX", "GIT_NAMESPACE", "GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM", "GIT_SUPER_PREFIX"]);
export function checkoutEnvironment(source: NodeJS.ProcessEnv = process.env): NodeJS.ProcessEnv {
  return Object.fromEntries(Object.entries(source).filter(([key]) =>
    !local.has(key) && !key.startsWith("GIT_CONFIG_KEY_") && !key.startsWith("GIT_CONFIG_VALUE_")));
}
