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
