package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

// memFlashcallRegistry is the in-memory twin of the Redis registry used in
// production; it keeps the same contract so the channel can be tested without
// one.
type memFlashcallRegistry struct {
	mu      sync.Mutex
	hash    map[string]string
	confirm map[string]bool
}

func newMemFlashcallRegistry() *memFlashcallRegistry {
	return &memFlashcallRegistry{hash: map[string]string{}, confirm: map[string]bool{}}
}

func (r *memFlashcallRegistry) Bind(_ context.Context, phone, hash string, _ time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hash[phone] = hash
	r.confirm[phone] = false
	return nil
}

func (r *memFlashcallRegistry) Lookup(_ context.Context, phone string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	hash, ok := r.hash[phone]
	return hash, ok, nil
}

func (r *memFlashcallRegistry) Confirm(_ context.Context, phone, hash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hash[phone] != hash {
		return errors.New("hash mismatch")
	}
	r.confirm[phone] = true
	return nil
}

func (r *memFlashcallRegistry) Confirmed(_ context.Context, phone, hash string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hash[phone] == hash && r.confirm[phone], nil
}

func (r *memFlashcallRegistry) Drop(_ context.Context, phone, hash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hash[phone] == hash {
		delete(r.hash, phone)
		delete(r.confirm, phone)
	}
	return nil
}

func newFlashcallService(t *testing.T, codes *memory.CodeStore, registry FlashcallCodeRegistry) *Service {
	t.Helper()
	return NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), codes, nil, nil, "12345",
		WithFlashcallPrefixes([]string{"+7"}),
		WithFlashcallCodeRegistry(registry),
		// Production behaviour: the fixed code is configured but never used for
		// a login, because both delivery channels carry a real code.
		WithRandomLoginCodes(),
	)
}

// WithRandomLoginCodes is what makes a delivered code a secret: outside the
// flash-call prefixes the configured development code must not survive either.
func TestRandomLoginCodesIgnoreFixedCode(t *testing.T) {
	ctx := context.Background()
	codes := memory.NewCodeStore()
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), codes, nil, nil, "12345",
		WithRandomLoginCodes())

	hash, err := svc.SendCode(ctx, "+4915112345678")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	rec, found, err := codes.Get(ctx, hash)
	if err != nil || !found {
		t.Fatalf("code record not stored: %v", err)
	}
	if rec.Code == "12345" {
		t.Fatalf("random login codes still produced the development code")
	}
	if len(rec.Code) != 5 {
		t.Fatalf("code length = %d, want 5", len(rec.Code))
	}
}

// A flash-call number still gets a real random code: the code is the secret both
// channels carry, and the development code would make the call pointless.
func TestFlashcallNumberGetsRandomCode(t *testing.T) {
	ctx := context.Background()
	codes := memory.NewCodeStore()
	svc := newFlashcallService(t, codes, newMemFlashcallRegistry())

	hash, err := svc.SendCode(ctx, "+79161234567")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	rec, found, err := codes.Get(ctx, hash)
	if err != nil || !found {
		t.Fatalf("code record not stored: %v", err)
	}
	if rec.Code == "12345" {
		t.Fatalf("flashcall number received the development code")
	}
	if rec.Channel != store.PhoneCodeChannelPhone {
		t.Fatalf("channel = %q, want phone (777000 delivery)", rec.Channel)
	}
}

// The flash call reveals the code the client already holds a hash for. It must
// be the same value, not a rotation: a rotation would invalidate the code the
// user may be reading in the 777000 message.
func TestFlashcallRevealsTheSameCode(t *testing.T) {
	ctx := context.Background()
	codes := memory.NewCodeStore()
	svc := newFlashcallService(t, codes, newMemFlashcallRegistry())

	hash, err := svc.SendCode(ctx, "+79161234567")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	issued, _, err := codes.Get(ctx, hash)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	revealed, _, err := svc.FlashcallCode(ctx, "+79161234567")
	if err != nil {
		t.Fatalf("FlashcallCode: %v", err)
	}
	if revealed != issued.Code {
		t.Fatalf("flash call revealed %q, the login code is %q", revealed, issued.Code)
	}
}

// No call is needed to sign in: the 777000 message is a legitimate delivery
// channel on its own, so the code must pass verification before any call.
func TestFlashcallCodeNeedsNoConfirmation(t *testing.T) {
	ctx := context.Background()
	codes := memory.NewCodeStore()
	svc := newFlashcallService(t, codes, newMemFlashcallRegistry())

	hash, err := svc.SendCode(ctx, "+79161234567")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	code, _, err := svc.FlashcallCode(ctx, "+79161234567")
	if err != nil {
		t.Fatalf("FlashcallCode: %v", err)
	}
	if _, _, _, err := svc.verifyLoginCode(ctx, normalizePhone("+79161234567"), hash, code); err != nil {
		t.Fatalf("code from the 777000 channel rejected: %v", err)
	}
}

// The channel is scoped: a number outside the prefixes has no phone->hash
// binding, so there is nothing for a flash call to reveal.
func TestFlashcallChannelIsScopedToPrefixes(t *testing.T) {
	ctx := context.Background()
	codes := memory.NewCodeStore()
	svc := newFlashcallService(t, codes, newMemFlashcallRegistry())

	if _, err := svc.SendCode(ctx, "+4915112345678"); err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	if _, _, err := svc.FlashcallCode(ctx, "+4915112345678"); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("foreign number flash call error = %v, want ErrCodeExpired", err)
	}
}

// Resend replaces the pending code, so the flash call must follow the newest
// hash instead of revealing a dead one.
func TestFlashcallBindingFollowsResend(t *testing.T) {
	ctx := context.Background()
	codes := memory.NewCodeStore()
	svc := newFlashcallService(t, codes, newMemFlashcallRegistry())

	first, err := svc.SendCode(ctx, "+79161234567")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	second, err := svc.ResendCode(ctx, "+79161234567", first)
	if err != nil {
		t.Fatalf("ResendCode: %v", err)
	}
	code, _, err := svc.FlashcallCode(ctx, "+79161234567")
	if err != nil {
		t.Fatalf("FlashcallCode after resend: %v", err)
	}
	fresh, found, err := codes.Get(ctx, second)
	if err != nil || !found {
		t.Fatalf("resent code missing: %v", err)
	}
	if code != fresh.Code {
		t.Fatalf("flash call revealed %q, the resent code is %q", code, fresh.Code)
	}
	if _, _, _, err := svc.verifyLoginCode(ctx, normalizePhone("+79161234567"), first, code); err == nil {
		t.Fatalf("the superseded hash still accepted the resent code")
	}
}

// Without a registry the channel is unavailable rather than silently useless.
func TestFlashcallCodeFailsWithoutRegistry(t *testing.T) {
	ctx := context.Background()
	codes := memory.NewCodeStore()
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), codes, nil, nil, "12345",
		WithFlashcallPrefixes([]string{"+7"}))

	if _, err := svc.SendCode(ctx, "+79161234567"); err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	if _, _, err := svc.FlashcallCode(ctx, "+79161234567"); !errors.Is(err, ErrFlashcallUnavailable) {
		t.Fatalf("FlashcallCode error = %v, want ErrFlashcallUnavailable", err)
	}
}

func TestFlashcallPrefixMatching(t *testing.T) {
	svc := &Service{}
	// "+7" and "7" must be interchangeable: canonical phones carry no plus.
	WithFlashcallPrefixes([]string{" +7 ", "", "+375"})(svc)
	for _, phone := range []string{"+79161234567", "+375291234567"} {
		if !svc.flashcallAppliesTo(normalizePhone(phone)) {
			t.Errorf("%s should have the flashcall channel", phone)
		}
	}
	if svc.flashcallAppliesTo(normalizePhone("+4915112345678")) {
		t.Errorf("+49 should not have the flashcall channel")
	}
	if (&Service{}).flashcallAppliesTo(normalizePhone("+79161234567")) {
		t.Errorf("an unconfigured service must not expose the channel")
	}
}

// fakeFlashCallProvider stands in for flashcall.ru: it hands out one number and
// reports whatever state the test sets.
type fakeFlashCallProvider struct {
	number string
	status string
}

func (p *fakeFlashCallProvider) GetPhone(context.Context, string) (FlashCallSession, error) {
	return FlashCallSession{ID: "sess-1", Number: p.number, ExpiresIn: 60}, nil
}

func (p *fakeFlashCallProvider) Status(context.Context, string) (FlashCallStatus, error) {
	return FlashCallStatus{ID: "sess-1", Status: p.status}, nil
}

// TestFlashCallNumberAndConfirmedSignIn covers variant A end to end: sendCode
// exposes the number to dial, an empty code is rejected until the provider sees
// the call, and the same empty code signs in once it does.
func TestFlashCallNumberAndConfirmedSignIn(t *testing.T) {
	ctx := context.Background()
	codes := memory.NewCodeStore()
	svc := newFlashcallService(t, codes, newMemFlashcallRegistry())
	provider := &fakeFlashCallProvider{number: "+79990001122", status: "PENDING"}
	svc.flashCallProvider = provider

	hash, err := svc.SendCode(ctx, "+79161234567")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}

	number, ok := svc.FlashCallNumber(ctx, "+79161234567", hash)
	if !ok || number != "+79990001122" {
		t.Fatalf("FlashCallNumber = %q, %v", number, ok)
	}

	if _, _, _, err := svc.SignIn(ctx, domain.Authorization{}, "+79161234567", hash, ""); err == nil {
		t.Fatal("signIn accepted an empty code before the call was confirmed")
	}

	provider.status = "CONFIRMED"
	_, _, needSignUp, err := svc.SignIn(ctx, domain.Authorization{}, "+79161234567", hash, "")
	if err != nil {
		t.Fatalf("signIn after confirmation: %v", err)
	}
	if !needSignUp {
		t.Fatal("an unregistered number must still require sign-up after the call")
	}
}

// TestFlashCallNumberSkipsForeignPrefix pins the fallback: outside the
// configured prefixes no call is requested and the App code stays the only
// channel.
func TestFlashCallNumberSkipsForeignPrefix(t *testing.T) {
	ctx := context.Background()
	codes := memory.NewCodeStore()
	svc := newFlashcallService(t, codes, newMemFlashcallRegistry())
	svc.flashCallProvider = &fakeFlashCallProvider{number: "+79990001122", status: "PENDING"}

	hash, err := svc.SendCode(ctx, "+4915112345678")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	if number, ok := svc.FlashCallNumber(ctx, "+4915112345678", hash); ok || number != "" {
		t.Fatalf("FlashCallNumber = %q, %v, want no call for a foreign prefix", number, ok)
	}
}
