package handlers

import (
	"testing"
)

func TestParseCustomerDetailsInput(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		expected  []CustomerDetail
		expectErr bool
	}{
		{
			name:  "Single item standard format",
			input: "john:123456789",
			expected: []CustomerDetail{
				{Name: "john", TelegramID: 123456789},
			},
			expectErr: false,
		},
		{
			name:  "Multiple items comma separated",
			input: "john:123456789, alice:987654321",
			expected: []CustomerDetail{
				{Name: "john", TelegramID: 123456789},
				{Name: "alice", TelegramID: 987654321},
			},
			expectErr: false,
		},
		{
			name:  "Multiple items newline separated",
			input: "john:123456789\nalice: 987654321\nbob: 11223344",
			expected: []CustomerDetail{
				{Name: "john", TelegramID: 123456789},
				{Name: "alice", TelegramID: 987654321},
				{Name: "bob", TelegramID: 11223344},
			},
			expectErr: false,
		},
		{
			name:  "Mixed whitespace, commas, newlines, equals delimiter",
			input: "  user_1 = 1001 ,  user_2: 1002 \n user_3 | 1003 ",
			expected: []CustomerDetail{
				{Name: "user_1", TelegramID: 1001},
				{Name: "user_2", TelegramID: 1002},
				{Name: "user_3", TelegramID: 1003},
			},
			expectErr: false,
		},
		{
			name:      "Empty input",
			input:     "   \n  ",
			expected:  nil,
			expectErr: true,
		},
		{
			name:      "Invalid telegram ID format",
			input:     "john:abc",
			expected:  nil,
			expectErr: true,
		},
		{
			name:      "Missing name or id",
			input:     ":123456789",
			expected:  nil,
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCustomerDetailsInput(tt.input)
			if (err != nil) != tt.expectErr {
				t.Fatalf("expected err: %v, got: %v", tt.expectErr, err)
			}
			if !tt.expectErr {
				if len(got) != len(tt.expected) {
					t.Fatalf("expected length %d, got %d", len(tt.expected), len(got))
				}
				for i := range got {
					if got[i].Name != tt.expected[i].Name || got[i].TelegramID != tt.expected[i].TelegramID {
						t.Errorf("item %d mismatch: expected %+v, got %+v", i, tt.expected[i], got[i])
					}
				}
			}
		})
	}
}
