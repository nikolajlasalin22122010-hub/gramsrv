package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Flash-call request states as reported by the verification provider.
const (
	FlashCallPending   = "PENDING"
	FlashCallConfirmed = "CONFIRMED"
	FlashCallExpired   = "EXPIRED"
)

var (
	// ErrFlashCallPhoneRejected means the provider refused the number format.
	ErrFlashCallPhoneRejected = errors.New("flashcall: phone number rejected")
	// ErrFlashCallUnauthorized means the provider rejected our token.
	ErrFlashCallUnauthorized = errors.New("flashcall: token rejected")
	// ErrFlashCallUpstream is any other provider-side failure.
	ErrFlashCallUpstream = errors.New("flashcall: upstream failure")
)

var (
	errFlashCallUnsupportedNumber = errors.New("flashcall: number outside allowed prefixes")
	errFlashCallRateLimited       = errors.New("flashcall: too many attempts")
	errFlashCallUnknownSession    = errors.New("flashcall: unknown verification session")
	errFlashCallBusy              = errors.New("flashcall: too many pending sessions")
	errFlashCallNoPendingCode     = errors.New("flashcall: no login code pending for this number")
)

// FlashCallSession is a pending call request: the owner of Phone must dial
// Number before ExpiresIn seconds elapse.
type FlashCallSession struct {
	ID        string
	Number    string
	ExpiresIn int
}

// FlashCallStatus is the current state of a call request.
type FlashCallStatus struct {
	ID          string
	Status      string
	CallerPhone string
	ExpiresIn   int
}

// FlashCallProvider is the outbound boundary to a flash-call verification
// service. It never sees login codes; it only proves that the owner of a phone
// number placed a call to a number the provider controls.
type FlashCallProvider interface {
	GetPhone(ctx context.Context, phone string) (FlashCallSession, error)
	Status(ctx context.Context, id string) (FlashCallStatus, error)
}

// LoginCodeIssuer exposes the login code already pending for a phone.
//
// The flash call is a second delivery channel to the same secret: the code the
// client asked for (and that the 777000 service message carries) is what the
// caller gets here, so the phone_code_hash the client already holds stays valid.
type LoginCodeIssuer interface {
	FlashcallCode(ctx context.Context, phone string) (code string, expiresAt time.Time, err error)
}

// HTTPFlashCallProvider talks to the flashcall.ru JSON API.
type HTTPFlashCallProvider struct {
	baseURL string
	token   string
	client  *http.Client
}

// NewHTTPFlashCallProvider builds a provider client. An empty token leaves the
// client unusable; callers are expected to skip wiring it in that case.
func NewHTTPFlashCallProvider(baseURL, token string, timeout time.Duration) *HTTPFlashCallProvider {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &HTTPFlashCallProvider{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   strings.TrimSpace(token),
		client:  &http.Client{Timeout: timeout},
	}
}

// GetPhone asks the provider for a number the user has to call.
func (p *HTTPFlashCallProvider) GetPhone(ctx context.Context, phone string) (FlashCallSession, error) {
	body, err := json.Marshal(map[string]string{"token": p.token, "phone": phone})
	if err != nil {
		return FlashCallSession{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/get-phone", bytes.NewReader(body))
	if err != nil {
		return FlashCallSession{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return FlashCallSession{}, fmt.Errorf("%w: %v", ErrFlashCallUpstream, err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return FlashCallSession{}, p.decodeError(resp)
	}
	defer resp.Body.Close()
	var payload struct {
		ID        string `json:"id"`
		Phone     string `json:"phone"`
		ExpiresIn int    `json:"expiresIn"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&payload); err != nil {
		return FlashCallSession{}, fmt.Errorf("%w: decode get-phone: %v", ErrFlashCallUpstream, err)
	}
	if strings.TrimSpace(payload.ID) == "" || strings.TrimSpace(payload.Phone) == "" {
		return FlashCallSession{}, fmt.Errorf("%w: incomplete get-phone response", ErrFlashCallUpstream)
	}
	return FlashCallSession{ID: payload.ID, Number: payload.Phone, ExpiresIn: payload.ExpiresIn}, nil
}

// Status polls the state of a call request.
func (p *HTTPFlashCallProvider) Status(ctx context.Context, id string) (FlashCallStatus, error) {
	query := url.Values{}
	query.Set("token", p.token)
	query.Set("id", id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/status?"+query.Encode(), nil)
	if err != nil {
		return FlashCallStatus{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return FlashCallStatus{}, fmt.Errorf("%w: %v", ErrFlashCallUpstream, err)
	}
	if resp.StatusCode != http.StatusOK {
		return FlashCallStatus{}, p.decodeError(resp)
	}
	defer resp.Body.Close()
	var payload struct {
		ID          string `json:"id"`
		Status      string `json:"status"`
		CallerPhone string `json:"callerPhone"`
		ExpiresIn   int    `json:"expiresIn"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&payload); err != nil {
		return FlashCallStatus{}, fmt.Errorf("%w: decode status: %v", ErrFlashCallUpstream, err)
	}
	return FlashCallStatus{
		ID:          payload.ID,
		Status:      strings.ToUpper(strings.TrimSpace(payload.Status)),
		CallerPhone: payload.CallerPhone,
		ExpiresIn:   payload.ExpiresIn,
	}, nil
}

func (p *HTTPFlashCallProvider) decodeError(resp *http.Response) error {
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var api struct {
		Message json.RawMessage `json:"message"`
		Error   string          `json:"error"`
	}
	_ = json.Unmarshal(raw, &api)
	detail := strings.TrimSpace(api.Error)
	if detail == "" {
		detail = strings.TrimSpace(string(raw))
	}
	if len(detail) > 200 {
		detail = detail[:200]
	}
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %s", ErrFlashCallPhoneRejected, detail)
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return fmt.Errorf("%w: status %d %s", ErrFlashCallUnauthorized, resp.StatusCode, detail)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w: rate limited", ErrFlashCallUpstream)
	}
	return fmt.Errorf("%w: status %d %s", ErrFlashCallUpstream, resp.StatusCode, detail)
}

const (
	flashCallStartWindow    = 10 * time.Minute
	flashCallStartsPerPhone = 3
	flashCallStartsPerIP    = 20
	flashCallMaxPending     = 5000
)

type flashCallPending struct {
	id          string
	phone       string
	flashcallID string
	number      string
	createdAt   time.Time
	expiresAt   time.Time
	confirmed   bool
}

// FlashCallService binds a login code to a phone-ownership proof: the code is
// issued first, but it is only revealed once the provider reports that the user
// dialed the number from the very phone that owns it.
type FlashCallService struct {
	provider FlashCallProvider
	issuer   LoginCodeIssuer
	logger   *zap.Logger
	prefixes []string
	appName  string
	now      func() time.Time

	mu       sync.Mutex
	pending  map[string]*flashCallPending
	byPhone  map[string]string
	perPhone map[string][]time.Time
	perIP    map[string][]time.Time
}

// NewFlashCallService builds the Web-side verification service. prefixes is a
// comma-separated list of allowed E.164 prefixes; an empty value defaults to
// "+7" because the upstream provider only covers Russian numbers.
func NewFlashCallService(provider FlashCallProvider, issuer LoginCodeIssuer, prefixes, appName string, logger *zap.Logger) *FlashCallService {
	if logger == nil {
		logger = zap.NewNop()
	}
	allowed := make([]string, 0, 4)
	for _, part := range strings.Split(prefixes, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			allowed = append(allowed, part)
		}
	}
	if len(allowed) == 0 {
		allowed = []string{"+7"}
	}
	if strings.TrimSpace(appName) == "" {
		appName = "DaskGram"
	}
	return &FlashCallService{
		provider: provider,
		issuer:   issuer,
		logger:   logger,
		prefixes: allowed,
		appName:  appName,
		now:      time.Now,
		pending:  make(map[string]*flashCallPending),
		byPhone:  make(map[string]string),
		perPhone: make(map[string][]time.Time),
		perIP:    make(map[string][]time.Time),
	}
}

type flashCallStartResult struct {
	ID        string `json:"id"`
	Number    string `json:"number"`
	ExpiresIn int    `json:"expires_in"`
}

type flashCallStatusResult struct {
	Status string `json:"status"`
	Code   string `json:"code,omitempty"`
}

// Start opens a call request for phone. The code it will reveal already exists:
// the client asked for it before the call, and without one there is nothing to
// reveal and no reason to spend a call.
func (s *FlashCallService) Start(ctx context.Context, rawPhone, ip string) (flashCallStartResult, error) {
	phone := normalizeFlashCallPhone(rawPhone)
	if phone == "" || !s.allowed(phone) {
		return flashCallStartResult{}, errFlashCallUnsupportedNumber
	}
	_, expiresAt, err := s.issuer.FlashcallCode(ctx, phone)
	if err != nil {
		return flashCallStartResult{}, errFlashCallNoPendingCode
	}
	now := s.now()
	if err := s.reserve(phone, ip, now); err != nil {
		return flashCallStartResult{}, err
	}
	session, err := s.provider.GetPhone(ctx, phone)
	if err != nil {
		return flashCallStartResult{}, err
	}
	id, err := newFlashCallID()
	if err != nil {
		return flashCallStartResult{}, err
	}
	record := &flashCallPending{
		id:          id,
		phone:       phone,
		flashcallID: session.ID,
		number:      session.Number,
		createdAt:   now,
		expiresAt:   expiresAt,
	}
	s.mu.Lock()
	if previous, ok := s.byPhone[phone]; ok {
		delete(s.pending, previous)
	}
	s.pending[id] = record
	s.byPhone[phone] = id
	s.mu.Unlock()

	expiresIn := session.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 60
	}
	s.logger.Info("flashcall verification started",
		zap.String("verification_id", id),
		zap.Int("expires_in", expiresIn))
	return flashCallStartResult{ID: id, Number: session.Number, ExpiresIn: expiresIn}, nil
}

// Status reports the verification state and, once the call is confirmed, the
// login code that belongs to the phone.
func (s *FlashCallService) Status(ctx context.Context, id string) (flashCallStatusResult, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return flashCallStatusResult{}, errFlashCallUnknownSession
	}
	now := s.now()
	s.mu.Lock()
	record := s.pending[id]
	if record == nil {
		s.mu.Unlock()
		return flashCallStatusResult{}, errFlashCallUnknownSession
	}
	if record.confirmed {
		phone := record.phone
		s.mu.Unlock()
		return s.confirmedResult(ctx, phone)
	}
	flashcallID := record.flashcallID
	phone := record.phone
	expiredLocally := !now.Before(record.expiresAt)
	s.mu.Unlock()

	if expiredLocally {
		s.forget(id)
		return flashCallStatusResult{Status: FlashCallExpired}, nil
	}
	status, err := s.provider.Status(ctx, flashcallID)
	if err != nil {
		return flashCallStatusResult{}, err
	}
	switch status.Status {
	case FlashCallConfirmed:
		// A confirmed request for a different caller must never unlock this code.
		if caller := normalizeFlashCallPhone(status.CallerPhone); caller != "" && caller != phone {
			s.logger.Warn("flashcall confirmed by unexpected caller",
				zap.String("verification_id", id))
			s.forget(id)
			return flashCallStatusResult{Status: FlashCallExpired}, nil
		}
		s.mu.Lock()
		current := s.pending[id]
		if current == nil {
			s.mu.Unlock()
			return flashCallStatusResult{}, errFlashCallUnknownSession
		}
		if current.confirmed {
			s.mu.Unlock()
			return s.confirmedResult(ctx, phone)
		}
		current.confirmed = true
		s.mu.Unlock()
		s.logger.Info("flashcall verification confirmed", zap.String("verification_id", id))
		return s.confirmedResult(ctx, phone)
	case FlashCallExpired:
		s.forget(id)
		return flashCallStatusResult{Status: FlashCallExpired}, nil
	default:
		return flashCallStatusResult{Status: FlashCallPending}, nil
	}
}

// confirmedResult reads the code that was already pending for phone.
//
// Nothing is rotated here: the very same value is in the 777000 message, so
// rotating it would invalidate a code the user may already be looking at on
// another device.
func (s *FlashCallService) confirmedResult(ctx context.Context, phone string) (flashCallStatusResult, error) {
	code, _, err := s.issuer.FlashcallCode(ctx, phone)
	if err != nil {
		s.logger.Warn("flashcall confirmed but the login code is gone", zap.Error(err))
		return flashCallStatusResult{Status: FlashCallExpired}, nil
	}
	return flashCallStatusResult{Status: FlashCallConfirmed, Code: code}, nil
}

func (s *FlashCallService) allowed(phone string) bool {
	for _, prefix := range s.prefixes {
		if strings.HasPrefix(phone, prefix) {
			return true
		}
	}
	return false
}

// reserve enforces per-phone and per-IP start limits before any upstream call.
func (s *FlashCallService) reserve(phone, ip string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	if len(s.pending) >= flashCallMaxPending {
		return errFlashCallBusy
	}
	if !withinLimit(s.perPhone, phone, flashCallStartsPerPhone, now) {
		return errFlashCallRateLimited
	}
	if ip != "" && !withinLimit(s.perIP, ip, flashCallStartsPerIP, now) {
		return errFlashCallRateLimited
	}
	s.perPhone[phone] = append(s.perPhone[phone], now)
	if ip != "" {
		s.perIP[ip] = append(s.perIP[ip], now)
	}
	return nil
}

func withinLimit(buckets map[string][]time.Time, key string, limit int, now time.Time) bool {
	kept := buckets[key][:0]
	for _, at := range buckets[key] {
		if now.Sub(at) < flashCallStartWindow {
			kept = append(kept, at)
		}
	}
	buckets[key] = kept
	return len(kept) < limit
}

func (s *FlashCallService) sweepLocked(now time.Time) {
	for id, record := range s.pending {
		if now.Before(record.expiresAt) {
			continue
		}
		delete(s.pending, id)
		if s.byPhone[record.phone] == id {
			delete(s.byPhone, record.phone)
		}
	}
	for key, bucket := range s.perPhone {
		if len(bucket) == 0 || now.Sub(bucket[len(bucket)-1]) >= flashCallStartWindow {
			delete(s.perPhone, key)
		}
	}
	for key, bucket := range s.perIP {
		if len(bucket) == 0 || now.Sub(bucket[len(bucket)-1]) >= flashCallStartWindow {
			delete(s.perIP, key)
		}
	}
}

func (s *FlashCallService) forget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record := s.pending[id]; record != nil {
		if s.byPhone[record.phone] == id {
			delete(s.byPhone, record.phone)
		}
		delete(s.pending, id)
	}
}

// HandleStart serves POST /v1/flashcall/start.
func (s *FlashCallService) HandleStart(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Phone string `json:"phone"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&request); err != nil {
		flashCallWriteJSON(w, http.StatusBadRequest, map[string]string{"error": "Некорректный запрос"})
		return
	}
	result, err := s.Start(r.Context(), request.Phone, flashCallClientIP(r))
	if err != nil {
		s.writeError(w, err)
		return
	}
	flashCallWriteJSON(w, http.StatusOK, result)
}

// HandleStatus serves GET /v1/flashcall/status.
func (s *FlashCallService) HandleStatus(w http.ResponseWriter, r *http.Request) {
	result, err := s.Status(r.Context(), r.URL.Query().Get("id"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	flashCallWriteJSON(w, http.StatusOK, result)
}

// HandlePage used to serve the public verification page. The page is gone: the
// code arrives as a 777000 message in the client, and the flash call is an API
// channel the client drives itself.
func (s *FlashCallService) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errFlashCallUnsupportedNumber):
		flashCallWriteJSON(w, http.StatusBadRequest, map[string]string{"error": "Поддерживаются только российские номера (+7)"})
	case errors.Is(err, errFlashCallNoPendingCode):
		flashCallWriteJSON(w, http.StatusConflict, map[string]string{"error": "Сначала запросите код в приложении"})
	case errors.Is(err, ErrFlashCallPhoneRejected):
		flashCallWriteJSON(w, http.StatusBadRequest, map[string]string{"error": "Неверный формат номера телефона"})
	case errors.Is(err, errFlashCallRateLimited):
		flashCallWriteJSON(w, http.StatusTooManyRequests, map[string]string{"error": "Слишком много попыток. Попробуйте через 10 минут"})
	case errors.Is(err, errFlashCallBusy):
		flashCallWriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Сервис перегружен, попробуйте позже"})
	case errors.Is(err, errFlashCallUnknownSession):
		flashCallWriteJSON(w, http.StatusNotFound, map[string]string{"error": "Сессия не найдена или истекла"})
	case errors.Is(err, ErrFlashCallUnauthorized):
		s.logger.Error("flashcall token rejected", zap.Error(err))
		flashCallWriteJSON(w, http.StatusBadGateway, map[string]string{"error": "Проверка временно недоступна"})
	case errors.Is(err, ErrFlashCallUpstream):
		s.logger.Error("flashcall upstream failure", zap.Error(err))
		flashCallWriteJSON(w, http.StatusBadGateway, map[string]string{"error": "Проверка временно недоступна"})
	default:
		s.logger.Error("flashcall verification failed", zap.Error(err))
		flashCallWriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Внутренняя ошибка"})
	}
}

func flashCallWriteJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func flashCallClientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if first := strings.TrimSpace(strings.Split(forwarded, ",")[0]); first != "" {
			return first
		}
	}
	if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
		return real
	}
	host := r.RemoteAddr
	if index := strings.LastIndex(host, ":"); index > 0 {
		host = host[:index]
	}
	return strings.Trim(host, "[]")
}

func newFlashCallID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate flashcall verification id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// normalizeFlashCallPhone converts user input into E.164. Russian numbers are
// accepted as +7XXXXXXXXXX, 8XXXXXXXXXX and 9XXXXXXXXX.
func normalizeFlashCallPhone(raw string) string {
	var digits strings.Builder
	for _, symbol := range raw {
		if symbol >= '0' && symbol <= '9' {
			digits.WriteRune(symbol)
		}
	}
	value := digits.String()
	switch {
	case strings.HasPrefix(value, "8") && len(value) == 11:
		value = "7" + value[1:]
	case len(value) == 10 && strings.HasPrefix(value, "9"):
		value = "7" + value
	}
	if len(value) < 10 || len(value) > 15 {
		return ""
	}
	return "+" + value
}
