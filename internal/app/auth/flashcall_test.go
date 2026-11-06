package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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
	)
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
