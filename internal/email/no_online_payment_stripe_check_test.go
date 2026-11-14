package email

import (
	"context"
	"errors"
	"testing"

	"github.com/gethardcount/hardcount/internal/platform/postgres"
)

// A failed read is not "not connected" and not "connected": the send fails,
// nothing reaches the customer, and the outbox retries the row. So does a
// Sender with no check wired, and a ctx with no mode.
func TestSendInvoice_StripeCheckFailureSendsNothing(t *testing.T) {
	readErr := errors.New("decrypt stripe key: bad ciphertext")
	live := postgres.WithLivemode(context.Background(), false)
	for _, tc := range []struct {
		name  string
		check StripeReadiness
		ctx   context.Context
		want  error
	}{
		{"read error", &stripeCheck{err: readErr}, live, readErr},
		{"no check wired", nil, live, ErrNoStripeCheck},
		{"no mode on ctx", &stripeCheck{connected: true}, context.Background(), postgres.ErrLivemodeUnset},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newScriptedSMTP(t)
			s := newCCTestSender(t, srv)
			s.hostedInvoiceBaseURL = "https://pay.example.com"
			if tc.check != nil {
				s.SetStripeReadiness(tc.check)
			}
			err := s.SendInvoice(tc.ctx, "t1", "ap@acme.test", nil, "Ada", "INV-0042", 6000, "usd", nil, "tok_abc", false, false)
			if !errors.Is(err, tc.want) {
				t.Errorf("invoice send error %v, want %v", err, tc.want)
			}
			err = s.SendDunningEscalation(tc.ctx, "t1", "ap@acme.test", nil, "Ada", "INV-0042", noOutcome, "tok_abc")
			if !errors.Is(err, tc.want) {
				t.Errorf("escalation send error %v, want %v", err, tc.want)
			}
			if n := messagesSent(srv); n != 0 {
				t.Errorf("%d messages reached SMTP, want none", n)
			}
		})
	}
}

// The check is read only when the email would offer to pay online. A settled
// or written-off invoice, or one with no hosted page to link, is sent without
// asking, so a failed read never holds it up.
func TestSendInvoice_AsksTheStripeCheckOnlyWhenItWouldOfferToPay(t *testing.T) {
	ctx := postgres.WithLivemode(context.Background(), false)
	for _, tc := range []struct {
		name       string
		dueCents   int64
		writtenOff bool
		token      string
	}{
		{"settled", 0, false, "tok_abc"},
		{"written off", 6000, true, "tok_abc"},
		{"no hosted page", 6000, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newScriptedSMTP(t)
			s := newCCTestSender(t, srv)
			s.hostedInvoiceBaseURL = "https://pay.example.com"
			check := &stripeCheck{err: errors.New("db down")}
			s.SetStripeReadiness(check)
			if err := s.SendInvoice(ctx, "t1", "ap@acme.test", nil, "Ada", "INV-0042", tc.dueCents, "usd", nil, tc.token, tc.writtenOff, false); err != nil {
				t.Fatalf("send: %v", err)
			}
			if n := len(check.askedModes()); n != 0 {
				t.Errorf("Stripe check asked %d times, want 0", n)
			}
			html, text := sentParts(t, srv)
			for part, body := range map[string]string{"html": html, "text": text} {
				assertNoPayWording(t, part, body)
			}
		})
	}
}
