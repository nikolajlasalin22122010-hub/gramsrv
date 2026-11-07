package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"telesrv/internal/store"
)

// ErrFlashcallUnavailable reports a configuration where flash-call verification
// was requested but no registry was wired into the service.
var ErrFlashcallUnavailable = errors.New("flashcall verification is not configured")

// FlashcallCodeRegistry maps a phone number to the login-code hash that is
// currently pending for it.
//
// It exists because login codes are stored under an opaque hash with no reverse
// index: the flash-call API knows only the phone number, while auth.signIn knows
// only the hash. The registry is the bridge between the two.
type FlashcallCodeRegistry interface {
	Bind(ctx context.Context, phone, hash string, ttl time.Duration) error
	Lookup(ctx context.Context, phone string) (string, bool, error)
	Confirm(ctx context.Context, phone, hash string) error
	Confirmed(ctx context.Context, phone, hash string) (bool, error)
	Drop(ctx context.Context, phone, hash string) error
}

// WithFlashcallPrefixes makes the login code for the listed E.164 prefixes
// additionally retrievable through a flash call.
//
// This is a second delivery channel, not a gate: the code is still sent to the
// account's other devices as a 777000 service message, and the same code is
// revealed by the flash-call API once the caller proves they own the number.
// Numbers outside these prefixes are unaffected.
//
// Prefixes are compared against the digits-only canonical form produced by
// domain.NormalizePhone, so both "+7" and "7" work and the stored value never
// depends on how the operator wrote the country code.
func WithFlashcallPrefixes(prefixes []string) Option {
	return func(s *Service) {
		cleaned := make([]string, 0, len(prefixes))
		for _, prefix := range prefixes {
			var digits strings.Builder
			for _, r := range prefix {
				if r >= '0' && r <= '9' {
					digits.WriteRune(r)
				}
			}
			if digits.Len() > 0 {
				cleaned = append(cleaned, digits.String())
			}
		}
		s.flashcallPrefixes = cleaned
	}
}

// WithFlashcallCodeRegistry wires the phone->hash index that backs the channel.
func WithFlashcallCodeRegistry(registry FlashcallCodeRegistry) Option {
	return func(s *Service) { s.flashcallRegistry = registry }
}

// FlashCallSession is a pending call request: the owner of the phone has to
// dial Number before ExpiresIn seconds elapse.
type FlashCallSession struct {
	ID        string
	Number    string
	ExpiresIn int
}

// FlashCallStatus is the provider's view of a call request.
type FlashCallStatus struct {
	ID          string
	Status      string
	CallerPhone string
	ExpiresIn   int
}

// FlashCallProvider is the outbound boundary to a flash-call verification
// service. It never sees login codes: it only proves that the owner of a phone
// number dialed a number the provider controls.
type FlashCallProvider interface {
	GetPhone(ctx context.Context, phone string) (FlashCallSession, error)
	Status(ctx context.Context, id string) (FlashCallStatus, error)
}

// flashCallStatusConfirmed is the provider state that means the caller proved
// ownership by placing the call.
const flashCallStatusConfirmed = "CONFIRMED"

// flashCallSession remembers which provider request belongs to a login hash.
type flashCallSession struct {
	id        string
	expiresAt time.Time
}

// WithFlashCallProvider wires the outbound flash-call provider. Together with
// the prefixes it turns the flash call into a login channel of its own rather
// than just a second way to reveal the App code.
func WithFlashCallProvider(provider FlashCallProvider) Option {
	return func(s *Service) {
		s.flashCallProvider = provider
		if s.flashCallSessions == nil {
			s.flashCallSessions = make(map[string]flashCallSession)
		}
	}
}

// FlashCallNumber opens a call request for a phone that just received a login
// code and returns the number the caller has to dial.
//
// ok is false whenever the number is outside the configured prefixes, no
// provider is wired, or the provider refuses the number: the caller then keeps
// the ordinary App-code delivery, so a provider outage never blocks a login.
func (s *Service) FlashCallNumber(ctx context.Context, phone, hash string) (string, bool) {
	phone = normalizePhone(phone)
	if s == nil || s.flashCallProvider == nil || !s.flashcallAppliesTo(phone) {
		return "", false
	}
	session, err := s.flashCallProvider.GetPhone(ctx, phone)
	if err != nil || strings.TrimSpace(session.Number) == "" {
		return "", false
	}
	ttl := s.codeTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	s.flashCallMu.Lock()
	if s.flashCallSessions == nil {
		s.flashCallSessions = make(map[string]flashCallSession)
	}
	s.flashCallSessions[hash] = flashCallSession{id: session.ID, expiresAt: time.Now().Add(ttl)}
	s.flashCallMu.Unlock()
	return session.Number, true
}

// flashCallApprovedCode returns the login code pending for hash once the owner
// of phone has proven ownership with a call.
//
// The provider is polled at most once per signIn: the client retries while the
// user is still on the call, so a signIn that lands right after the call still
// succeeds without waiting for a webhook.
func (s *Service) flashCallApprovedCode(ctx context.Context, phone, hash string) (string, bool) {
	if s == nil || s.flashCallProvider == nil || s.flashcallRegistry == nil || !s.flashcallAppliesTo(phone) {
		return "", false
	}
	rec, found, err := s.codes.Get(ctx, hash)
	if err != nil || !found || rec.Phone != phone {
		return "", false
	}
	if ok, err := s.flashcallRegistry.Confirmed(ctx, phone, hash); err == nil && ok {
		return rec.Code, true
	}
	s.flashCallMu.Lock()
	session, has := s.flashCallSessions[hash]
	s.flashCallMu.Unlock()
	if !has || time.Now().After(session.expiresAt) {
		return "", false
	}
	status, err := s.flashCallProvider.Status(ctx, session.id)
	if err != nil || status.Status != flashCallStatusConfirmed {
		return "", false
	}
	_ = s.flashcallRegistry.Confirm(ctx, phone, hash)
	return rec.Code, true
}

// flashcallAppliesTo reports whether phone belongs to a prefix that may also be
// verified by a flash call.
func (s *Service) flashcallAppliesTo(phone string) bool {
	if s == nil || len(s.flashcallPrefixes) == 0 {
		return false
	}
	for _, prefix := range s.flashcallPrefixes {
		if strings.HasPrefix(phone, prefix) {
			return true
		}
	}
	return false
}

// bindFlashcallCode records phone -> hash for a code that was just issued, so
// the flash-call API can find it from the number alone.
//
// It is best effort on purpose: the 777000 service message is the primary
// channel, and a failed bind only costs the caller the flash-call alternative
// for this one attempt.
func (s *Service) bindFlashcallCode(ctx context.Context, phone, hash string) {
	if s.flashcallRegistry == nil || !s.flashcallAppliesTo(phone) {
		return
	}
	ttl := s.codeTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	_ = s.flashcallRegistry.Bind(ctx, phone, hash, ttl)
}

// FlashcallCode returns the code currently pending for phone: the same value the
// 777000 message carries and the same hash the client holds as
// phone_code_hash, so a caller that proves ownership can sign in without the
// MTProto layer changing at all.
func (s *Service) FlashcallCode(ctx context.Context, phone string) (string, time.Time, error) {
	phone = normalizePhone(phone)
	if !validPhone(phone) {
		return "", time.Time{}, ErrPhoneNumberInvalid
	}
	if s.flashcallRegistry == nil {
		return "", time.Time{}, ErrFlashcallUnavailable
	}
	hash, ok, err := s.flashcallRegistry.Lookup(ctx, phone)
	if err != nil {
		return "", time.Time{}, err
	}
	if !ok {
		return "", time.Time{}, ErrCodeExpired
	}
	rec, found, err := s.codes.Get(ctx, hash)
	if err != nil {
		return "", time.Time{}, err
	}
	if !found || rec.Phone != phone || !store.LoginCodeChannelVerifiable(rec.Channel) {
		return "", time.Time{}, ErrCodeExpired
	}
	ttl := s.codeTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return rec.Code, time.Now().Add(ttl), nil
}
