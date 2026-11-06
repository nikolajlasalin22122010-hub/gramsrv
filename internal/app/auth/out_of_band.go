package auth

import (
	"context"
	"fmt"
	"time"

	"telesrv/internal/store"
)

// IssueOutOfBandCode issues an ordinary login code for phone without handing it
// to the configured delivery provider.
//
// The caller owns delivery: it must prove ownership of the number before
// revealing the code to anyone, and must never return it to an unauthenticated
// MTProto peer.
//
// The record is written with the SMS channel on purpose. That makes it pass the
// same LoginCodeChannelVerifiable check, owner binding, attempt counting and
// single-use consumption as a provider-delivered code, so auth.signIn accepts it
// with no changes to the MTProto layer and no new channel value in the store.
func (s *Service) IssueOutOfBandCode(ctx context.Context, phone string) (hash, code string, expiresAt time.Time, err error) {
	phone = normalizePhone(phone)
	if !validPhone(phone) {
		return "", "", time.Time{}, ErrPhoneNumberInvalid
	}
	if systemLoginPhoneForbidden(phone) {
		return "", "", time.Time{}, ErrSystemUserLoginForbidden
	}
	existing, found, err := s.currentPhoneOwner(ctx, phone)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("lookup out-of-band code recipient: %w", err)
	}
	if found && systemUserLoginForbidden(existing) {
		return "", "", time.Time{}, ErrSystemUserLoginForbidden
	}
	issuedUserID := int64(0)
	if found {
		issuedUserID = existing.ID
	}

	length := s.phoneCodeLength
	if length <= 0 {
		length = 5
	}
	ttl := s.codeTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if hash, err = randomHex(8); err != nil {
		return "", "", time.Time{}, err
	}
	if code, err = randomDigits(length); err != nil {
		return "", "", time.Time{}, err
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
		return "", "", time.Time{}, fmt.Errorf("store out-of-band code: %w", err)
	}
	// Keep the issued-owner binding in sync with the user row exactly like
	// createPhoneCode does, so a concurrent registration cannot turn this code
	// into a login for a different account.
	if err := s.ensureIssuedOwnerAfterSet(ctx, hash, rec); err != nil {
		return "", "", time.Time{}, err
	}
	return hash, code, time.Now().Add(ttl), nil
}
