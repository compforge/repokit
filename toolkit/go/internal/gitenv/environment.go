// Package gitenv binds child Git operations to their explicit checkout.
package gitenv

import (
	"strconv"
	"strings"
	"unicode"
)

// ForCheckout drops inherited repository-local context, retaining transport and identity.
// +spec=`Explicit checkout paths win over inherited Git discovery, index and object overrides. Explicit per-command overrides are applied afterwards.`
// +link=component://docs/git.md
func ForCheckout(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_IMPLICIT_WORK_TREE", "GIT_GRAFT_FILE", "GIT_SHALLOW_FILE", "GIT_PREFIX", "GIT_NAMESPACE", "GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM", "GIT_SUPER_PREFIX":
			continue
		}
		if strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_") {
			continue
		}
		result = append(result, entry)
	}
	source := map[string]string{}
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		source[key] = value
	}
	count, _ := strconv.Atoi(source["GIT_CONFIG_COUNT"])
	index := 0
	for i := 0; i < count && i < len(source); i++ {
		key := source["GIT_CONFIG_KEY_"+strconv.Itoa(i)]
		value, ok := source["GIT_CONFIG_VALUE_"+strconv.Itoa(i)]
		if ok && portableConfig(key) {
			result = append(result, "GIT_CONFIG_KEY_"+strconv.Itoa(index)+"="+key, "GIT_CONFIG_VALUE_"+strconv.Itoa(index)+"="+value)
			index++
		}
	}
	if index > 0 {
		result = append(result, "GIT_CONFIG_COUNT="+strconv.Itoa(index))
	}
	parameters := []string{}
	for _, word := range splitParameters(source["GIT_CONFIG_PARAMETERS"]) {
		key, _, _ := strings.Cut(word, "=")
		if portableConfig(key) {
			parameters = append(parameters, "'"+strings.ReplaceAll(word, "'", "'\\''")+"'")
		}
	}
	if len(parameters) > 0 {
		result = append(result, "GIT_CONFIG_PARAMETERS="+strings.Join(parameters, " "))
	}
	return result
}
func portableConfig(key string) bool {
	key = strings.ToLower(key)
	for _, prefix := range []string{"http.", "https.", "credential.", "url.", "user.", "author.", "committer."} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	switch key {
	case "core.sshcommand", "core.gitproxy", "core.askpass", "ssh.variant":
		return true
	}
	return false
}
func splitParameters(input string) []string {
	words := []string{}
	var word strings.Builder
	var quote rune
	escaped, started := false, false
	for _, char := range input {
		switch {
		case escaped:
			word.WriteRune(char)
			escaped = false
			started = true
		case char == '\\' && quote != '\'':
			escaped = true
		case quote != 0:
			if char == quote {
				quote = 0
			} else {
				word.WriteRune(char)
			}
		case char == '\'' || char == '"':
			quote = char
			started = true
		case unicode.IsSpace(char):
			if started {
				words = append(words, word.String())
				word.Reset()
				started = false
			}
		default:
			word.WriteRune(char)
			started = true
		}
	}
	if quote != 0 || escaped {
		return nil
	}
	if started {
		words = append(words, word.String())
	}
	return words
}
