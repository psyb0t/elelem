package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// serverNamePattern is the set of server names that are safe inside a tool
// name.
var serverNamePattern = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// ToolName is the name the model sees for a server's tool:
// mcp__<server>__<tool>. A tool name with a character providers refuse, or
// one that makes the result too long, is cleaned and cut, and gets a short
// hash of the original so two such names never collide. The result depends
// only on the two names, so a saved transcript finds the same tool after a
// restart.
func ToolName(server, tool string) string {
	prefix := ToolNamePrefix + server + toolNameSeparator

	cleaned := strings.Map(func(character rune) rune {
		if isToolNameCharacter(character) {
			return character
		}

		return toolNameReplacement
	}, tool)

	if cleaned == tool && len(prefix)+len(tool) <= MaxToolNameLength {
		return prefix + tool
	}

	sum := sha256.Sum256([]byte(tool))
	suffix := toolNameHashMark +
		hex.EncodeToString(sum[:])[:toolNameHashLength]

	room := max(MaxToolNameLength-len(prefix)-len(suffix), 0)
	if len(cleaned) > room {
		cleaned = cleaned[:room]
	}

	return prefix + cleaned + suffix
}

// IsToolName reports whether name is an MCP tool name.
func IsToolName(name string) bool {
	return strings.HasPrefix(name, ToolNamePrefix)
}

// validServerName reports whether name fits in a tool name. An underscore
// is refused because the tool name uses "__" to separate the server.
func validServerName(name string) bool {
	return len(name) <= MaxServerNameLength &&
		serverNamePattern.MatchString(name)
}

func isToolNameCharacter(character rune) bool {
	return character >= 'a' && character <= 'z' ||
		character >= 'A' && character <= 'Z' ||
		character >= '0' && character <= '9' ||
		character == '_' || character == '-'
}
