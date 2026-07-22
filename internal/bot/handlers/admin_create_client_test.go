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
			name:  "Single item numeric ID format",
			input: "john:123456789",
			expected: []CustomerDetail{
				{Name: "john", TelegramID: 123456789, Username: ""},
			},
			expectErr: false,
		},
		{
			name:  "Single item username tag format with @",
			input: "john:@john_doe",
			expected: []CustomerDetail{
				{Name: "john", TelegramID: 0, Username: "john_doe"},
			},
			expectErr: false,
		},
		{
			name:  "Shorthand tag without name prefix",
			input: "@john_doe",
			expected: []CustomerDetail{
				{Name: "john_doe", TelegramID: 0, Username: "john_doe"},
			},
			expectErr: false,
		},
		{
			name:  "Shorthand numeric ID without name prefix",
			input: "123456789",
			expected: []CustomerDetail{
				{Name: "user_123456789", TelegramID: 123456789, Username: ""},
			},
			expectErr: false,
		},
		{
			name:  "Multiple items mixed formats comma and newline separated",
			input: "john:123456789, alice:@alice_user\n @bob_user, 99887766",
			expected: []CustomerDetail{
				{Name: "john", TelegramID: 123456789, Username: ""},
				{Name: "alice", TelegramID: 0, Username: "alice_user"},
				{Name: "bob_user", TelegramID: 0, Username: "bob_user"},
				{Name: "user_99887766", TelegramID: 99887766, Username: ""},
			},
			expectErr: false,
		},
		{
			name:      "Empty input",
			input:     "   \n  ",
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
					if got[i].Name != tt.expected[i].Name || got[i].TelegramID != tt.expected[i].TelegramID || got[i].Username != tt.expected[i].Username {
						t.Errorf("item %d mismatch: expected %+v, got %+v", i, tt.expected[i], got[i])
					}
				}
			}
		})
	}
}
