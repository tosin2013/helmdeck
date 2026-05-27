// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/resend/resend-go/v3"

	"github.com/tosin2013/helmdeck/internal/packs"
)

type fakeEmailSender struct {
	sendResp   *resend.SendEmailResponse
	sendErr    error
	domainResp resend.ListDomainsResponse
	domainErr  error
}

func (f *fakeEmailSender) SendEmail(
	ctx context.Context,
	req *resend.SendEmailRequest,
) (*resend.SendEmailResponse, error) {
	return f.sendResp, f.sendErr
}

func (f *fakeEmailSender) ListDomains(
	ctx context.Context,
) (resend.ListDomainsResponse, error) {
	return f.domainResp, f.domainErr
}

type fakeEmailSendFactory struct {
	svc emailSender
}

func (f *fakeEmailSendFactory) New(apikey string) emailSender {
	return f.svc
}

func TestEmailSend_Success(t *testing.T) {
	_, _, key := validGhostKey()
	v := vaultWithGhostKey(t, "resend-api-key", "*", key)
	fakeSvc := &fakeEmailSender{
		sendResp: &resend.SendEmailResponse{
			Id: "msg_123",
		},
		domainResp: resend.ListDomainsResponse{
			Data: []resend.Domain{
				{
					Name:   "example.com",
					Status: resend.DomainStatusVerified,
				},
			},
		},
	}

	handler := emailSendHandler(v, &fakeEmailSendFactory{
		svc: fakeSvc,
	})

	input := emailSendInput{
		From:    "hello@example.com",
		To:      "user@test.com",
		Subject: "Hello, user",
		Html:    "<p>Hello</p>",
	}

	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}

	out, err := handler(context.Background(), &packs.ExecutionContext{
		Input: raw,
	})

	var resp resend.SendEmailResponse

	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("expected %s, got %s", fakeSvc.sendResp.Id, resp.Id)
	}
}

func TestExtractDomain(t *testing.T) {
	want := "example.com"
	got := extractDomain("hello@example.com")

	if got != want {
		t.Fatalf(" expected %s, got %s", want, got)
	}
}

func TestIsVerifiedSenderDomain(t *testing.T) {
	expected := "example.com"
	domainsData := []resend.Domain{
		{
			Name:   "example.com",
			Status: resend.DomainStatusVerified,
		},
		{
			Name:   "acme.io",
			Status: resend.DomainStatusPending,
		},
		{
			Name:   "mail.test.dev",
			Status: resend.DomainStatusVerified,
		},
	}

	domainVerified := isVerifiedSenderDomain(domainsData, expected)
	if !domainVerified {
		t.Fatalf("expected domain %s to be verified", expected)
	}
}
