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
	statusCalls int
}

func (f *fakeFlashCallProvider) GetPhone(context.Context, string) (FlashCallSession, error) {
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

type fakeLoginCodeIssuer struct {
	code      string
	ttl       time.Duration
	err       error
	calls     int
	lastPhone string
}

func (f *fakeLoginCodeIssuer) EnsureFlashcallCode(_ context.Context, phone string) (string, time.Time, error) {
	f.calls++
	f.lastPhone = phone
	if f.err != nil {
		return "", time.Time{}, f.err
	}
	ttl := f.ttl
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	return "hash", time.Now().Add(ttl), nil
}

func (f *fakeLoginCodeIssuer) ConfirmFlashcall(context.Context, string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.code, nil
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
	issuer := &fakeLoginCodeIssuer{code: "55555"}
	service := newTestFlashCallService(provider, issuer)

	if _, err := service.Start(context.Background(), "+79161234567", ""); !errors.Is(err, ErrFlashCallUnauthorized) {
		t.Fatalf("error = %v, want unauthorized", err)
	}
	if issuer.calls != 0 {
		t.Fatalf("issued %d codes despite provider failure", issuer.calls)
	}
}

func TestFlashCallPageUsesBrandName(t *testing.T) {
	service := newTestFlashCallService(&fakeFlashCallProvider{}, &fakeLoginCodeIssuer{})
	if service.appName != "TestApp" {
		t.Fatalf("app name = %q", service.appName)
	}
	if flashCallPagePath != "/verify" {
		t.Fatalf("page path = %q", flashCallPagePath)
	}
}
