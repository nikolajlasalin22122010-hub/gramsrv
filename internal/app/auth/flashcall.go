package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"telesrv/internal/store"
)

// ErrFlashcallUnavailable reports a configuration where a phone must prove
// ownership with a flash call but no registry was wired into the service.
var ErrFlashcallUnavailable = errors.New("flashcall verification is not configured")

// FlashcallCodeRegistry maps a phone number to the login-code hash that is
// currently waiting for a flash-call confirmation.
//
// It exists because login codes are stored under an opaque hash with no reverse
// index: the public verification page knows only the phone number, while
// auth.signIn knows only the hash. The registry is the bridge between the two,
// and it is also the durable record of "this number already proved ownership
// for this exact hash".
type FlashcallCodeRegistry interface {
	Bind(ctx context.Context, phone, hash string, ttl time.Duration) error
	Lookup(ctx context.Context, phone string) (string, bool, error)
	Confirm(ctx context.Context, phone, hash string) error
	Confirmed(ctx context.Context, phone, hash string) (bool, error)
	Drop(ctx context.Context, phone, hash string) error
}

// WithFlashcallRequiredPrefixes makes login codes for the listed E.164 prefixes
// undeliverable: no SMS, no 777000 App message, no fixed development code. The
// only way to learn such a code is the flash-call verification page.
//
// Prefixes are compared against the digits-only canonical form produced by
// domain.NormalizePhone, so both "+7" and "7" work and the stored value never
// depends on how the operator wrote the country code.
func WithFlashcallRequiredPrefixes(prefixes []string) Option {
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

// WithFlashcallCodeRegistry wires the phone->hash index that backs the gate.
func WithFlashcallCodeRegistry(registry FlashcallCodeRegistry) Option {
	return func(s *Service) { s.flashcallRegistry = registry }
}

// flashcallRequiredFor reports whether phone belongs to a prefix whose codes may
// only be revealed by the flash-call page.
func (s *Service) flashcallRequiredFor(phone string) bool {
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

// flashcallConfirmed reports whether the number owner already completed a call
// for this hash. A missing registry fails closed: an unverifiable code must not
// authorize a login.
func (s *Service) flashcallConfirmed(ctx context.Context, phone, hash string) (bool, error) {
	if s.flashcallRegistry == nil {
		return false, nil
	}
	return s.flashcallRegistry.Confirmed(ctx, phone, hash)
}

// EnsureFlashcallCode returns the login-code hash for phone, creating a fresh
// record when none is pending.
//
// The code is deliberately never delivered anywhere. It only becomes usable
// after ConfirmFlashcall rotates it to a value the page can show, which keeps a
// guessed or intercepted value worthless.
func (s *Service) EnsureFlashcallCode(ctx context.Context, phone string) (string, time.Time, error) {
	phone = normalizePhone(phone)
	if !validPhone(phone) {
		return "", time.Time{}, ErrPhoneNumberInvalid
	}
	if systemLoginPhoneForbidden(phone) {
		return "", time.Time{}, ErrSystemUserLoginForbidden
	}
	if s.flashcallRegistry == nil {
		return "", time.Time{}, ErrFlashcallUnavailable
	}
	existing, found, err := s.currentPhoneOwner(ctx, phone)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("lookup flashcall code recipient: %w", err)
	}
	if found && systemUserLoginForbidden(existing) {
		return "", time.Time{}, ErrSystemUserLoginForbidden
	}
	issuedUserID := int64(0)
	if found {
		issuedUserID = existing.ID
	}

	ttl := s.codeTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	// Reuse a live record so a page opened before the client still ends up with
	// the same phone_code_hash the client will present at auth.signIn.
	if hash, ok, err := s.flashcallRegistry.Lookup(ctx, phone); err != nil {
		return "", time.Time{}, err
	} else if ok {
		rec, recFound, err := s.codes.Get(ctx, hash)
		if err != nil {
			return "", time.Time{}, err
		}
		if recFound && rec.Phone == phone && rec.IssuedUserID == issuedUserID && !rec.SignUpVerified {
			return hash, time.Now().Add(ttl), nil
		}
	}

	length := s.phoneCodeLength
	if length <= 0 {
		length = 5
	}
	hash, err := randomHex(8)
	if err != nil {
		return "", time.Time{}, err
	}
	code, err := randomDigits(length)
	if err != nil {
		return "", time.Time{}, err
	}
	rec := store.PhoneCode{
		Version:      store.PhoneCodeVersionCurrent,
		IssuedUserID: issuedUserID,
		Phone:        phone,
		Code:         code,
		Channel:      codeChannelSMS,
		MaxAttempts:  s.codeMaxAttempts,
	}
	if err := s.codes.Set(ctx, hash, rec, ttl); err != nil {
		return "", time.Time{}, fmt.Errorf("store flashcall code: %w", err)
	}
	// Keep the issued-owner binding in sync with the user row exactly like
	// createPhoneCode does, so a concurrent registration cannot turn this code
	// into a login for a different account.
	if err := s.ensureIssuedOwnerAfterSet(ctx, hash, rec); err != nil {
		return "", time.Time{}, err
	}
	if err := s.flashcallRegistry.Bind(ctx, phone, hash, ttl); err != nil {
		return "", time.Time{}, fmt.Errorf("bind flashcall code: %w", err)
	}
	return hash, time.Now().Add(ttl), nil
}

// ConfirmFlashcall rotates the pending code to a fresh value and marks the phone
// as confirmed for that hash.
//
// Rotation is what makes the scheme sound: before the call the record holds a
// code nobody has seen, and after the call it holds a code that is only ever
// shown to the caller. It also means the revealed code works with the
// phone_code_hash the client already holds, so the MTProto layer stays untouched.
func (s *Service) ConfirmFlashcall(ctx context.Context, phone string) (string, error) {
	phone = normalizePhone(phone)
	if s.flashcallRegistry == nil {
		return "", ErrFlashcallUnavailable
	}
	hash, ok, err := s.flashcallRegistry.Lookup(ctx, phone)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrCodeExpired
	}
	rec, found, err := s.codes.Get(ctx, hash)
	if err != nil {
		return "", err
	}
	if !found || rec.Phone != phone {
		return "", ErrCodeExpired
	}
	length := len(rec.Code)
	if length <= 0 || length > 12 {
		length = s.phoneCodeLength
		if length <= 0 {
			length = 5
		}
	}
	code, err := randomDigits(length)
	if err != nil {
		return "", err
	}
	ttl := s.codeTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	rec.Code = code
	if err := s.codes.Set(ctx, hash, rec, ttl); err != nil {
		return "", fmt.Errorf("rotate flashcall code: %w", err)
	}
	if err := s.flashcallRegistry.Confirm(ctx, phone, hash); err != nil {
		return "", fmt.Errorf("confirm flashcall code: %w", err)
	}
	return code, nil
}
