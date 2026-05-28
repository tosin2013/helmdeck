// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package pipelines

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateInputsFilled(t *testing.T) {
	cases := []struct {
		name      string
		inputs    string
		wantErr   bool
		wantInKey string // substring the error must mention (the offending input)
	}{
		{name: "empty inputs", inputs: ``, wantErr: false},
		{name: "all filled", inputs: `{"title":"Real Title","repo_url":"https://github.com/x/y"}`, wantErr: false},
		{name: "literal title placeholder", inputs: `{"title":"{{TITLE}}"}`, wantErr: true, wantInKey: "title"},
		{name: "placeholder with inner spaces", inputs: `{"repo_url":"{{ REPO_URL }}"}`, wantErr: true, wantInKey: "repo_url"},
		{name: "placeholder with surrounding whitespace", inputs: `{"query":"  {{QUERY}}\n"}`, wantErr: true, wantInKey: "query"},
		// A markdown body that merely *mentions* an UPPER_SNAKE token must
		// NOT trip the guard — only a value that is entirely a placeholder.
		{name: "placeholder as substring in content", inputs: `{"markdown":"Set the {{API_KEY}} env var before deploy."}`, wantErr: false},
		// Lowercase {{title}} is not the doc-template convention.
		{name: "lowercase token", inputs: `{"title":"{{title}}"}`, wantErr: false},
		// Non-string values are ignored.
		{name: "non-string value", inputs: `{"max_slides":18}`, wantErr: false},
		{name: "not an object", inputs: `"just a string"`, wantErr: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw json.RawMessage
			if tc.inputs != "" {
				raw = json.RawMessage(tc.inputs)
			}
			err := validateInputsFilled(raw)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %q, got nil", tc.inputs)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error for %q, got %v", tc.inputs, err)
			}
			if tc.wantErr && tc.wantInKey != "" && !strings.Contains(err.Error(), tc.wantInKey) {
				t.Fatalf("error %q should mention offending input %q", err.Error(), tc.wantInKey)
			}
		})
	}
}
