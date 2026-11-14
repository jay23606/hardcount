package paymentmethods

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gethardcount/hardcount/internal/auth"
	"github.com/gethardcount/hardcount/internal/platform/postgres"
)

// onceCooldown allows the first AllowKey call per key and refuses the rest,
// like a 60-second window that does not reopen during the test.
type onceCooldown struct{ taken map[string]bool }

func (c *onceCooldown) AllowKey(_ context.Context, key string) (int, time.Time, bool) {
	resetAt := time.Now().Add(time.Minute)
	if c.taken[key] {
		return 0, resetAt, false
	}
	c.taken[key] = true
	return 0, resetAt, true
}

// fillableLookup returns whatever email the test sets on it.
type fillableLookup struct{ email string }

func (l *fillableLookup) GetForSetupLink(context.Context, string, string) (string, string, error) {
	return l.email, "Acme", nil
}

// recordingEmailer counts the setup-link emails it is asked to queue.
type recordingEmailer struct{ sent int }

func (e *recordingEmailer) SendPaymentSetupLink(context.Context, string, string, string, string, string) error {
	e.sent++
	return nil
}

// TestSendSetupEmail_RefusedRequestDoesNotStartCooldown: a request refused
// with missing_email (422) sends nothing, so it must not use the 60-second
// slot. After the operator adds the email, the next send answers 202 and
// sends the email. That valid send does take the slot, so a third send
// answers 429 cooldown_active.
func TestSendSetupEmail_RefusedRequestDoesNotStartCooldown(t *testing.T) {
	stripe := &returnStripe{}
	svc := NewService(newMemStore(), stripe, nil)
	svc.SetPortalBaseURL("https://app.example.test/")
	lookup := &fillableLookup{}
	emailer := &recordingEmailer{}
	h := NewHandler(svc)
	h.SetCustomerLookup(lookup)
	h.SetEmailer(emailer)
	h.SetEmailConfigured(true)
	h.SetCooldown(&onceCooldown{taken: map[string]bool{}})

	r := chi.NewRouter()
	r.Mount("/customers/{customer_id}/payment-methods", h.OperatorRoutes())
	send := func() *httptest.ResponseRecorder {
		ctx := postgres.WithLivemode(auth.WithTenantID(context.Background(), "hc_ten_t1"), false)
		req := httptest.NewRequest(http.MethodPost, "/customers/hc_cus_1/payment-methods/send-setup-email", nil).
			WithContext(ctx)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	if rec := send(); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("first status = %d, want 422 missing_email; body=%s", rec.Code, rec.Body.String())
	}

	lookup.email = "ap@acme.test"
	if rec := send(); rec.Code != http.StatusAccepted {
		t.Fatalf("second status = %d, want 202 (not 429 cooldown_active); body=%s", rec.Code, rec.Body.String())
	}
	if emailer.sent != 1 {
		t.Fatalf("emails sent = %d, want 1", emailer.sent)
	}

	if rec := send(); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third status = %d, want 429 cooldown_active after a valid send; body=%s", rec.Code, rec.Body.String())
	}
}
