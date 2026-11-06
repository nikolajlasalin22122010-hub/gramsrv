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
// production; it keeps the same contract so the gate can be tested without one.
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

func newGateService(t *testing.T, codes *memory.CodeStore, registry FlashcallCodeRegistry) *Service {
	t.Helper()
	return NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), codes, nil, nil, "12345",
		WithFlashcallRequiredPrefixes([]string{"+7"}),
		WithFlashcallCodeRegistry(registry),
	)
}

// The development code must never reach a flash-call-only number: the whole
// point of the gate is that 12345 stops working for +7.
func TestFlashcallSendCodeNeverUsesDevelopmentCode(t *testing.T) {
	ctx := context.Background()
	codes := memory.NewCodeStore()
	svc := newGateService(t, codes, newMemFlashcallRegistry())

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
	if rec.Channel != store.PhoneCodeChannelSMS {
		t.Fatalf("channel = %q, want sms", rec.Channel)
	}
}

// The gate is scoped: a number outside the configured prefixes keeps the old
// behaviour, so enabling it cannot break the rest of the world.
func TestFlashcallGateIsScopedToPrefixes(t *testing.T) {
	ctx := context.Background()
	codes := memory.NewCodeStore()
	svc := newGateService(t, codes, newMemFlashcallRegistry())

	hash, err := svc.SendCode(ctx, "+4915112345678")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	rec, _, err := codes.Get(ctx, hash)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.Code != "12345" {
		t.Fatalf("foreign number code = %q, want the development code", rec.Code)
	}
}

// Knowing the code is not enough before the call: the record already holds a
// real code, and it must be rejected until the number owner dials the number.
func TestFlashcallGateRejectsUnconfirmedCode(t *testing.T) {
	ctx := context.Background()
	codes := memory.NewCodeStore()
	svc := newGateService(t, codes, newMemFlashcallRegistry())

	hash, _, err := svc.EnsureFlashcallCode(ctx, "+79161234567")
	if err != nil {
		t.Fatalf("EnsureFlashcallCode: %v", err)
	}
	rec, found, err := codes.Get(ctx, hash)
	if err != nil || !found {
		t.Fatalf("code record missing: %v", err)
	}

	if _, _, _, err := svc.verifyLoginCode(ctx, normalizePhone("+79161234567"), hash, rec.Code); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("unconfirmed code error = %v, want ErrCodeInvalid", err)
	}
}

// Confirmation rotates the code, so the value that existed while the call was in
// flight is dead, and the rotated one authorizes the same phone_code_hash.
func TestFlashcallConfirmRotatesAndUnlocks(t *testing.T) {
	ctx := context.Background()
	codes := memory.NewCodeStore()
	svc := newGateService(t, codes, newMemFlashcallRegistry())

	hash, _, err := svc.EnsureFlashcallCode(ctx, "+79161234567")
	if err != nil {
		t.Fatalf("EnsureFlashcallCode: %v", err)
	}
	before, _, err := codes.Get(ctx, hash)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	code, err := svc.ConfirmFlashcall(ctx, "+79161234567")
	if err != nil {
		t.Fatalf("ConfirmFlashcall: %v", err)
	}
	if code == before.Code {
		t.Fatalf("code was not rotated on confirmation")
	}
	if _, _, _, err := svc.verifyLoginCode(ctx, normalizePhone("+79161234567"), hash, before.Code); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("pre-confirmation code still accepted: %v", err)
	}
	if _, _, _, err := svc.verifyLoginCode(ctx, normalizePhone("+79161234567"), hash, code); err != nil {
		t.Fatalf("confirmed code rejected: %v", err)
	}
}

// Ensure must reuse the pending hash so a page opened before the client still
// returns the hash the client will present at auth.signIn.
func TestFlashcallEnsureReusesPendingHash(t *testing.T) {
	ctx := context.Background()
	codes := memory.NewCodeStore()
	svc := newGateService(t, codes, newMemFlashcallRegistry())

	first, err := svc.SendCode(ctx, "+79161234567")
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	second, _, err := svc.EnsureFlashcallCode(ctx, "+79161234567")
	if err != nil {
		t.Fatalf("EnsureFlashcallCode: %v", err)
	}
	if first != second {
		t.Fatalf("hash changed between client and page: %q vs %q", first, second)
	}
}

// Without a registry the gate must fail closed rather than accept a code.
func TestFlashcallGateFailsClosedWithoutRegistry(t *testing.T) {
	ctx := context.Background()
	codes := memory.NewCodeStore()
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), codes, nil, nil, "12345",
		WithFlashcallRequiredPrefixes([]string{"+7"}))

	if _, _, err := svc.EnsureFlashcallCode(ctx, "+79161234567"); !errors.Is(err, ErrFlashcallUnavailable) {
		t.Fatalf("Ensure error = %v, want ErrFlashcallUnavailable", err)
	}
	confirmed, err := svc.flashcallConfirmed(ctx, "+79161234567", "hash")
	if err != nil {
		t.Fatalf("flashcallConfirmed: %v", err)
	}
	if confirmed {
		t.Fatalf("missing registry reported a confirmed number")
	}
}

func TestFlashcallRequiredPrefixMatching(t *testing.T) {
	svc := &Service{}
	// "+7" and "7" must be interchangeable: canonical phones carry no plus.
	WithFlashcallRequiredPrefixes([]string{" +7 ", "", "+375"})(svc)
	for _, phone := range []string{"+79161234567", "+375291234567"} {
		if !svc.flashcallRequiredFor(normalizePhone(phone)) {
			t.Errorf("%s should require flashcall", phone)
		}
	}
	if svc.flashcallRequiredFor(normalizePhone("+4915112345678")) {
		t.Errorf("+49 should not require flashcall")
	}
	if (&Service{}).flashcallRequiredFor(normalizePhone("+79161234567")) {
		t.Errorf("an unconfigured service must not require flashcall")
	}
}
