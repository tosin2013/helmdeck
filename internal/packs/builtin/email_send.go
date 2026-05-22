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

// emailSender defines the minima email operations requied by the
// pack handler. The handler depends on this interface instead of
// the concrete Resend SDK client so the implementation can be swapped
// out during tests without making real network calls
type emailSender interface {
	SendEmail(ctx context.Context, req *resend.SendEmailRequest) (*resend.SendEmailResponse, error)
	ListDomains(ctx context.Context) (resend.ListDomainsResponse, error)
}

// resendService is the production implementation of emailSender backed
// by the Resend SDK client.
type resendService struct {
	client *resend.Client
}

func (r *resendService) SendEmail(ctx context.Context, req *resend.SendEmailRequest) (*resend.SendEmailResponse, error) {
	return r.client.Emails.SendWithContext(ctx, req)
}

func (r *resendService) ListDomains(ctx context.Context) (resend.ListDomainsResponse, error) {
	return r.client.Domains.ListWithContext(ctx)
}

// emailSenderFactory constructs emailSender implementation at runtime.
// A factory is used becasue the Resend API key is resolved dynamically
// from Vault per request, not during pack registration.
type emailSenderFactory interface {
	New(apiKey string) emailSender
}

// resendFactory creates Resend-backed emailSender instances using the
// Vault-resolved API key for the current request
type resendFactory struct{}

func (f *resendFactory) New(apiKey string) emailSender {
	return &resendService{
		client: resend.NewClient(apiKey),
	}
}

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
		Handler: emailSendHandler(v, &resendFactory{}),
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

// emailSendHandler handles email sending via Resend with Vault-supplied API key.
func emailSendHandler(v *vault.Store, factory emailSenderFactory) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		var in emailSendInput
		var credName = "resend-api-key"

		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if in.To == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "recipient is required"}
		}
		if in.Subject == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "subject is required"}
		}

		actor := vault.Actor{Subject: "*"}
		res, err := v.ResolveByName(ctx, actor, credName)
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: fmt.Sprintf("vault credential %q not found (Configure a valid Resend API key to send emails)", credName)}
		}

		svc := factory.New(string(res.Plaintext))

		// Verify that the sender's email is verified on Resend
		if in.From == "" {
			fromDomain := extractDomain(in.From)
			domains, err := svc.ListDomains(ctx)
			if err != nil {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: err.Error(), Cause: err}
			}
			if !isVerifiedSenderDomain(domains.Data, fromDomain) {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: fmt.Sprintf("cannot send email from %q (Sender domain is not verified on Resend)", in.From)}
			}
		}


		// nil check before dereferencing
		var cc []string
		if in.Cc != nil {
			cc = []string{*in.Cc}
		}

		var bcc []string
		if in.Bcc != nil {
			bcc = []string{*in.Bcc}
		}

		var replyTo string
		if in.ReplyTo != nil {
			replyTo = *in.ReplyTo

		}

		emailRequest := &resend.SendEmailRequest{
			From:    in.From,
			To:      []string{in.To}, // TODO: Expand to accept slice of recipients
			Subject: in.Subject,
			Cc:      cc,
			Bcc:     bcc,
			ReplyTo: replyTo,
		}

		sent, err := svc.SendEmail(ctx, emailRequest)
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: err.Error(), Cause: err}
		}
		return json.Marshal(sent)
	}
}

// extractDomain extracts domain portion after '@' from an email address string.
func extractDomain(from string) string {
	return strings.Split(from, "@")[1]
}

// isVerifiedSenderDomain checks if a given sender domain is verified by Resend.
func isVerifiedSenderDomain(domains []resend.Domain, fromDomain string) bool {
	for _, domain := range domains {
		// TODO:  Check domain.capabilities.sending too
		if domain.Name == fromDomain && domain.Status == resend.DomainStatusVerified {
			return true
		}
	}
	return false
}
