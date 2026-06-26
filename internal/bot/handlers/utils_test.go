package handlers

import (
	"testing"
)

func TestFormatMarkdown(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple text",
			input:    "Hello World",
			expected: "Hello World",
		},
		{
			name:     "bold conversion",
			input:    "**bold text**",
			expected: "*bold text*",
		},
		{
			name:     "bold with unescaped underscore",
			input:    "**bold** with _underscore_",
			expected: "*bold* with \\_underscore\\_",
		},
		{
			name:     "bold with unescaped asterisk",
			input:    "**bold** with *single*",
			expected: "*bold* with \\*single\\*",
		},
		{
			name:     "code span preservation",
			input:    "`code_span_with_underscore` and **bold**",
			expected: "`code_span_with_underscore` and *bold*",
		},
		{
			name:     "code block preservation",
			input:    "```\ncode_block_with_*\n``` and **bold**",
			expected: "```\ncode_block_with_*\n``` and *bold*",
		},
		{
			name:     "brackets escaping",
			input:    "some [brackets] here",
			expected: "some \\[brackets] here",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := FormatMarkdown(tc.input)
			if actual != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, actual)
			}
		})
	}
}
