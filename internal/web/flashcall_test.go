package web

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
)

type fakeFlashCallProvider struct {
	session     FlashCallSession
	status      FlashCallStatus
	getErr      error
	statusErr   error
	getCalls    int
	statusCalls int
}

func (f *fakeFlashCallProvider) GetPhone(context.Context, string) (FlashCallSession, error) {
	f.getCalls++
	if f.getErr != nil {
		return FlashCallSession{}, f.getErr
	}
	return f.session, nil
}

func (f *fakeFlashCallProvider) Status(context.Context, string) (FlashCallStatus, error) {
	f.statusCalls++
	if f.statusErr != nil {
		return FlashCallStatus{}, f.statusErr
	}
	return f.status, nil
}

// fakeLoginCodeIssuer stands in for auth.Service: it holds the code the client
// already asked for, which is what the flash call reveals.
type fakeLoginCodeIssuer struct {
	code      string
	ttl       time.Duration
	err       error
	calls     int
	lastPhone string
}

func (f *fakeLoginCodeIssuer) FlashcallCode(_ context.Context, phone string) (string, time.Time, error) {
	f.calls++
	f.lastPhone = phone
	if f.err != nil {
		return "", time.Time{}, f.err
	}
	ttl := f.ttl
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	return f.code, time.Now().Add(ttl), nil
}

func newTestFlashCallService(provider FlashCallProvider, issuer LoginCodeIssuer) *FlashCallService {
	return NewFlashCallService(provider, issuer, "+7", "TestApp", zap.NewNop())
}

func TestFlashCallNormalizesRussianNumbers(t *testing.T) {
	cases := map[string]string{
		"+7 916 123-45-67":  "+79161234567",
		"89161234567":       "+79161234567",
		"9161234567":        "+79161234567",
		"8 (916) 123 45 67": "+79161234567",
		"12345":             "",
		"":                  "",
	}
	for input, want := range cases {
		if got := normalizeFlashCallPhone(input); got != want {
			t.Errorf("normalizeFlashCallPhone(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestFlashCallHidesCodeUntilConfirmed(t *testing.T) {
	provider := &fakeFlashCallProvider{
		session: FlashCallSession{ID: "fc-1", Number: "+74996771908", ExpiresIn: 60},
		status:  FlashCallStatus{ID: "fc-1", Status: FlashCallPending},
	}
	issuer := &fakeLoginCodeIssuer{code: "54321"}
	service := newTestFlashCallService(provider, issuer)

	start, err := service.Start(context.Background(), "+79161234567", "10.0.0.1")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if start.Number != "+74996771908" || start.ExpiresIn != 60 || start.ID == "" {
		t.Fatalf("unexpected start result: %+v", start)
	}
	if issuer.lastPhone != "+79161234567" {
		t.Fatalf("issuer got phone %q", issuer.lastPhone)
	}

	pending, err := service.Status(context.Background(), start.ID)
	if err != nil {
		t.Fatalf("Status pending: %v", err)
	}
	if pending.Status != FlashCallPending {
		t.Fatalf("status = %q, want PENDING", pending.Status)
	}
	if pending.Code != "" {
		t.Fatalf("code leaked before confirmation: %q", pending.Code)
	}

	provider.status = FlashCallStatus{ID: "fc-1", Status: FlashCallConfirmed, CallerPhone: "+79161234567"}
	confirmed, err := service.Status(context.Background(), start.ID)
	if err != nil {
		t.Fatalf("Status confirmed: %v", err)
	}
	if confirmed.Status != FlashCallConfirmed || confirmed.Code != "54321" {
		t.Fatalf("unexpected confirmed result: %+v", confirmed)
	}
}

// The confirmed answer must carry the pending login code, not a new one: the
// client signs in with the phone_code_hash it already holds.
func TestFlashCallRevealsPendingCodeWithoutRotation(t *testing.T) {
	provider := &fakeFlashCallProvider{
		session: FlashCallSession{ID: "fc-1", Number: "+74996771908", ExpiresIn: 60},
		status:  FlashCallStatus{ID: "fc-1", Status: FlashCallConfirmed, CallerPhone: "+79161234567"},
	}
	issuer := &fakeLoginCodeIssuer{code: "77777"}
	service := newTestFlashCallService(provider, issuer)

	start, err := service.Start(context.Background(), "+79161234567", "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	first, err := service.Status(context.Background(), start.ID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	second, err := service.Status(context.Background(), start.ID)
	if err != nil {
		t.Fatalf("Status again: %v", err)
	}
	if first.Code != "77777" || second.Code != "77777" {
		t.Fatalf("code changed between polls: %q then %q", first.Code, second.Code)
	}
	// The code is re-read on every confirmed poll, so a resend that happened in
	// between is reflected instead of being answered from a stale copy.
	if issuer.calls < 2 {
		t.Fatalf("issuer consulted %d times, want the code re-read on each poll", issuer.calls)
	}
}

// A flash call for a number that never asked for a code has nothing to reveal,
// so it must not spend a paid call.
func TestFlashCallRequiresPendingCode(t *testing.T) {
	provider := &fakeFlashCallProvider{
		session: FlashCallSession{ID: "fc-1", Number: "+74996771908", ExpiresIn: 60},
	}
	service := newTestFlashCallService(provider, &fakeLoginCodeIssuer{err: errors.New("no code")})

	if _, err := service.Start(context.Background(), "+79161234567", ""); !errors.Is(err, errFlashCallNoPendingCode) {
		t.Fatalf("error = %v, want no pending code", err)
	}
	if provider.getCalls != 0 {
		t.Fatalf("provider asked for a number %d times without a pending code", provider.getCalls)
	}
}

func TestFlashCallRejectsCallerFromAnotherNumber(t *testing.T) {
	provider := &fakeFlashCallProvider{
		session: FlashCallSession{ID: "fc-1", Number: "+74996771908", ExpiresIn: 60},
		status:  FlashCallStatus{ID: "fc-1", Status: FlashCallConfirmed, CallerPhone: "+79990000000"},
	}
	service := newTestFlashCallService(provider, &fakeLoginCodeIssuer{code: "11111"})

	start, err := service.Start(context.Background(), "+79161234567", "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	result, err := service.Status(context.Background(), start.ID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if result.Status != FlashCallExpired {
		t.Fatalf("status = %q, want EXPIRED", result.Status)
	}
	if result.Code != "" {
		t.Fatalf("code leaked for a foreign caller: %q", result.Code)
	}
}

func TestFlashCallExpiredSessionDropsCode(t *testing.T) {
	provider := &fakeFlashCallProvider{
		session: FlashCallSession{ID: "fc-1", Number: "+74996771908", ExpiresIn: 60},
		status:  FlashCallStatus{ID: "fc-1", Status: FlashCallPending},
	}
	service := newTestFlashCallService(provider, &fakeLoginCodeIssuer{code: "22222", ttl: -time.Second})

	start, err := service.Start(context.Background(), "+79161234567", "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	result, err := service.Status(context.Background(), start.ID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if result.Status != FlashCallExpired {
		t.Fatalf("status = %q, want EXPIRED", result.Status)
	}
	if provider.statusCalls != 0 {
		t.Fatalf("provider polled %d times for a locally expired session", provider.statusCalls)
	}
	if _, err := service.Status(context.Background(), start.ID); !errors.Is(err, errFlashCallUnknownSession) {
		t.Fatalf("second Status error = %v, want unknown session", err)
	}
}

func TestFlashCallRejectsForeignNumbers(t *testing.T) {
	service := newTestFlashCallService(&fakeFlashCallProvider{}, &fakeLoginCodeIssuer{code: "33333"})
	if _, err := service.Start(context.Background(), "+4915112345678", ""); !errors.Is(err, errFlashCallUnsupportedNumber) {
		t.Fatalf("error = %v, want unsupported number", err)
	}
}

func TestFlashCallRateLimitsStartsPerPhone(t *testing.T) {
	provider := &fakeFlashCallProvider{
		session: FlashCallSession{ID: "fc-1", Number: "+74996771908", ExpiresIn: 60},
		status:  FlashCallStatus{ID: "fc-1", Status: FlashCallPending},
	}
	service := newTestFlashCallService(provider, &fakeLoginCodeIssuer{code: "44444"})

	for attempt := 0; attempt < flashCallStartsPerPhone; attempt++ {
		if _, err := service.Start(context.Background(), "+79161234567", ""); err != nil {
			t.Fatalf("Start %d: %v", attempt, err)
		}
	}
	if _, err := service.Start(context.Background(), "+79161234567", ""); !errors.Is(err, errFlashCallRateLimited) {
		t.Fatalf("error = %v, want rate limited", err)
	}
}

func TestFlashCallDoesNotStartWhenProviderFails(t *testing.T) {
	provider := &fakeFlashCallProvider{getErr: ErrFlashCallUnauthorized}
	service := newTestFlashCallService(provider, &fakeLoginCodeIssuer{code: "55555"})

	if _, err := service.Start(context.Background(), "+79161234567", ""); !errors.Is(err, ErrFlashCallUnauthorized) {
		t.Fatalf("error = %v, want unauthorized", err)
	}
	// The session must not exist: a failed provider call cannot leave a pending
	// verification that would later be polled against an unknown upstream id.
	if _, err := service.Status(context.Background(), "unknown"); !errors.Is(err, errFlashCallUnknownSession) {
		t.Fatalf("Status error = %v, want unknown session", err)
	}
}
