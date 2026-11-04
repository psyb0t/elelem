package mcp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestToolName(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 80)

	testCases := []struct {
		name   string
		server string
		tool   string
		want   string
	}{
		{
			name: "plain", server: "github", tool: "create_issue",
			want: "mcp__github__create_issue",
		},
		{
			name: "refused characters", server: "docs", tool: "search.v2",
			want: "mcp__docs__search_v2_" + hashOf("search.v2"),
		},
		{
			name: "too long", server: "docs", tool: long,
			want: "mcp__docs__" + long[:44] + "_" + hashOf(long),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := ToolName(tc.server, tc.tool)
			assert.Equal(t, tc.want, got)
			assert.LessOrEqual(t, len(got), MaxToolNameLength)
			assert.True(t, IsToolName(got))
		})
	}
}

// Two names that clean to the same text stay apart.
func TestToolNameKeepsCleanedNamesApart(t *testing.T) {
	t.Parallel()

	assert.NotEqual(t, ToolName("docs", "a.b"), ToolName("docs", "a/b"))
	assert.NotEqual(t, ToolName("docs", "a_b"), ToolName("docs", "a.b"))
}

func TestIsToolName(t *testing.T) {
	t.Parallel()

	assert.False(t, IsToolName("readFile"))
}

func hashOf(tool string) string {
	name := ToolName("x", tool)

	return name[len(name)-toolNameHashLength:]
}
