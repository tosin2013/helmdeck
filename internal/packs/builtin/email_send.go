// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/resend/resend-go/v3"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/vault"
)

const (
	resendAPIBase = "https://api.resend.com"
)

func EmailSend(v *vault.Store) *packs.Pack {
	return &packs.Pack{
		Name:        "email.send",
		Version:     "v1", // where does version come from?
		Description: "Send a transactional email",
		InputSchema: packs.BasicSchema{
			Required: []string{"to"},
			Properties: map[string]string{
				"from":     "string", // this should be populated
				"to":       "string",
				"subject":  "string",
				"cc":       "string",
				"bcc":      "string",
				"reply_to": "string",
				"html":     "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"message_id"},
			Properties: map[string]string{
				"message_id": "string",
			},
		},
		Handler: emailSendHandler(v),
	}
}

type emailSendInput struct {
	From    string  `json:"from"`
	To      string  `json:"to"`
	Subject string  `json:"subject"`
	Cc      *string `json:"cc,omitempty"`
	Bcc     *string `json:"bcc,omitempty"`
	ReplyTo *string `json:"reply_to,omitempty"`
	Html    string  `json:"html"`
}

/// Handles email sending via Resend with Vault-supplied API key.
func emailSendHandler(v *vault.Store) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		var in emailSendInput
		var credName = "RESEND_API_KEY"

		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if in.To == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "recipient is required"}
		}
		if in.Subject == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "subject is required"}
		}
		actor := vault.Actor{Subject: "*"} // I still don't know what this means
		res, err := v.ResolveByName(ctx, actor, credName)
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: fmt.Sprintf("vault credential %q not found (Configure a valid Resend API key to send emails)", credName)}
		}
		resendClient := resend.NewClient(string(res.Plaintext))

		// Verify that the sender's email is verified on Resend
		if in.From == "" {
			fromDomain := extractDomain(in.From)
			domains, err := resendClient.Domains.ListWithContext(ctx)
			if err != nil {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: err.Error(), Cause: err}
			}
			if !isVerifiedSenderDomain(domains.Data, fromDomain) {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: fmt.Sprintf("cannot send email from %q (Sender domain is not verified on Resend)", in.From)}
			}
		}

		emailRequest := &resend.SendEmailRequest{
			From:    in.From,
			To:      []string{in.To}, // TODO: Expand to accept slice of recipients
			Subject: in.Subject,
			Cc:      []string{*in.Cc},
			Bcc:     []string{*in.Bcc},
			ReplyTo: *in.ReplyTo,
		}

		sent, err := resendClient.Emails.SendWithContext(ctx, emailRequest)
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: err.Error(), Cause: err}
		}
		return json.Marshal(sent)
	}
}

/// Extracts domain portion after '@' from an email address string.
func extractDomain(from string) string {
	return strings.Split(from, "@")[1]
}

/// Checks if a given sender domain is verified by Resend.
func isVerifiedSenderDomain(domains []resend.Domain, fromDomain string) bool {
	for _, domain := range domains {
		// TODO:  Check domain.capabilities.sending too
		if domain.Name == fromDomain && domain.Status == resend.DomainStatusVerified {
			return true
		}
	}
	return false
}
