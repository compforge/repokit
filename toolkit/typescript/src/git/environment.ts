/** Explicit checkout selection must not inherit a caller's repository-local Git context.
 * @spec Temporary index/object overrides are applied after checkoutEnvironment.
 * @see {@link component://docs/git.md}
 */
const local = new Set(["GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_IMPLICIT_WORK_TREE", "GIT_GRAFT_FILE", "GIT_SHALLOW_FILE", "GIT_PREFIX", "GIT_NAMESPACE", "GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM", "GIT_SUPER_PREFIX"]);
export function checkoutEnvironment(source: NodeJS.ProcessEnv = process.env): NodeJS.ProcessEnv {
  const result = Object.fromEntries(Object.entries(source).filter(([key]) =>
    !local.has(key) && !key.startsWith("GIT_CONFIG_KEY_") && !key.startsWith("GIT_CONFIG_VALUE_")));
  const count = Number(source.GIT_CONFIG_COUNT ?? 0), pairs: [string,string][] = [];
  for (let i=0; Number.isInteger(count) && i<Math.min(count,Object.keys(source).length); i++) {
    const key=source[`GIT_CONFIG_KEY_${i}`], value=source[`GIT_CONFIG_VALUE_${i}`];
    if (key && value !== undefined && portableConfig(key)) pairs.push([key,value]);
  }
  if (pairs.length) {
    result.GIT_CONFIG_COUNT=String(pairs.length);
    pairs.forEach(([key,value],i)=>{result[`GIT_CONFIG_KEY_${i}`]=key;result[`GIT_CONFIG_VALUE_${i}`]=value;});
  }
  const parameters=splitParameters(source.GIT_CONFIG_PARAMETERS ?? "").filter(value=>portableConfig(value.split("=",1)[0]!));
  if(parameters.length) result.GIT_CONFIG_PARAMETERS=parameters.map(value=>"'"+value.replaceAll("'", "'\\''")+"'").join(" ");
  return result;
}
function portableConfig(key: string): boolean {
  key=key.toLowerCase();
  return /^(http|https|credential|url|user|author|committer)\./.test(key) || ["core.sshcommand","core.gitproxy","core.askpass","ssh.variant"].includes(key);
}
// Git encodes inherited -c settings as shell-quoted words (including adjacent quoted parts).
function splitParameters(input: string): string[] {
  const words: string[]=[]; let word="", quote="", escaped=false, started=false;
  for(const char of input) {
    if(escaped) {word+=char;escaped=false;started=true;}
    else if(char==="\\" && quote!=="'") escaped=true;
    else if(quote) {if(char===quote)quote="";else word+=char;}
    else if(char==="'" || char==='"') {quote=char;started=true;}
    else if(/\s/.test(char)) {if(started){words.push(word);word="";started=false;}}
    else {word+=char;started=true;}
  }
  if(quote || escaped)return [];
  if(started)words.push(word);
  return words;
}
