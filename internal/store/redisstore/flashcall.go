package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

const flashcallRegistryKeyPrefix = "telesrv:flashcall:pending:v1:"

// flashcallBinding is the value stored per phone number. Hash is the
// phone_code_hash the client is holding; Confirmed flips to true only after the
// caller proved ownership of the number.
type flashcallBinding struct {
	Hash      string `json:"hash"`
	Confirmed bool   `json:"confirmed"`
}

// FlashcallCodeRegistry stores which login-code hash is waiting for a flash-call
// confirmation, and whether that confirmation already happened.
type FlashcallCodeRegistry struct {
	c redis.UniversalClient
}

func NewFlashcallCodeRegistry(c redis.UniversalClient) *FlashcallCodeRegistry {
	return &FlashcallCodeRegistry{c: c}
}

func flashcallRegistryKey(phone string) string {
	return flashcallRegistryKeyPrefix + phone
}

func (r *FlashcallCodeRegistry) Bind(ctx context.Context, phone, hash string, ttl time.Duration) error {
	if r == nil || r.c == nil {
		return errors.New("redis flashcall registry is not configured")
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	raw, err := json.Marshal(flashcallBinding{Hash: hash})
	if err != nil {
		return err
	}
	return r.c.Set(ctx, flashcallRegistryKey(phone), raw, ttl).Err()
}

func (r *FlashcallCodeRegistry) Lookup(ctx context.Context, phone string) (string, bool, error) {
	if r == nil || r.c == nil {
		return "", false, errors.New("redis flashcall registry is not configured")
	}
	binding, found, err := r.read(ctx, phone)
	if err != nil || !found {
		return "", false, err
	}
	if binding.Hash == "" {
		return "", false, nil
	}
	return binding.Hash, true, nil
}

func (r *FlashcallCodeRegistry) read(ctx context.Context, phone string) (flashcallBinding, bool, error) {
	raw, err := r.c.Get(ctx, flashcallRegistryKey(phone)).Bytes()
	if errors.Is(err, redis.Nil) {
		return flashcallBinding{}, false, nil
	}
	if err != nil {
		return flashcallBinding{}, false, err
	}
	var binding flashcallBinding
	if err := json.Unmarshal(raw, &binding); err != nil {
		// A corrupt value must not be treated as a confirmation.
		return flashcallBinding{}, false, nil
	}
	return binding, true, nil
}

// confirmScript flips the flag only while the stored hash still matches, and
// keeps the remaining TTL so confirmation cannot extend the code's lifetime
// beyond what the caller was promised.
var confirmScript = redis.NewScript(`
local key = KEYS[1]
local want = ARGV[1]
local raw = redis.call('GET', key)
if not raw then
  return 0
end
local ok, binding = pcall(cjson.decode, raw)
if not ok or type(binding) ~= 'table' or binding['hash'] ~= want then
  return -1
end
binding['confirmed'] = true
redis.call('SET', key, cjson.encode(binding), 'KEEPTTL')
return 1
`)

func (r *FlashcallCodeRegistry) Confirm(ctx context.Context, phone, hash string) error {
	if r == nil || r.c == nil {
		return errors.New("redis flashcall registry is not configured")
	}
	result, err := confirmScript.Run(ctx, r.c, []string{flashcallRegistryKey(phone)}, hash).Int()
	if err != nil {
		return err
	}
	switch result {
	case 1:
		return nil
	case 0:
		return errors.New("flashcall registry entry expired")
	default:
		return errors.New("flashcall registry entry belongs to another hash")
	}
}

func (r *FlashcallCodeRegistry) Confirmed(ctx context.Context, phone, hash string) (bool, error) {
	if r == nil || r.c == nil {
		return false, errors.New("redis flashcall registry is not configured")
	}
	binding, found, err := r.read(ctx, phone)
	if err != nil || !found {
		return false, err
	}
	return binding.Confirmed && binding.Hash == hash, nil
}

func (r *FlashcallCodeRegistry) Drop(ctx context.Context, phone, hash string) error {
	if r == nil || r.c == nil {
		return errors.New("redis flashcall registry is not configured")
	}
	binding, found, err := r.read(ctx, phone)
	if err != nil || !found {
		return err
	}
	if binding.Hash != hash {
		return nil
	}
	return r.c.Del(ctx, flashcallRegistryKey(phone)).Err()
}
