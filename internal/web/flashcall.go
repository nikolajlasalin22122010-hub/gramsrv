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

// LoginCodeIssuer issues a login code without delivering it, so the Web layer
// can reveal it only after a flash call proves phone ownership.
type LoginCodeIssuer interface {
	// EnsureFlashcallCode returns the pending login-code hash for phone,
	// creating the record on first use. The code itself is never returned
	// here: it only becomes usable after ConfirmFlashcall rotates it.
	EnsureFlashcallCode(ctx context.Context, phone string) (hash string, expiresAt time.Time, err error)
	// ConfirmFlashcall rotates the pending code and marks the number proven.
	ConfirmFlashcall(ctx context.Context, phone string) (code string, err error)
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
	flashCallPagePath       = "/verify"
)

type flashCallPending struct {
	id          string
	phone       string
	flashcallID string
	code        string
	number      string
	createdAt   time.Time
	expiresAt   time.Time
	confirmed   bool
	// confirming guards against two concurrent polls rotating the code twice
	// and handing out a value that the second rotation already invalidated.
	confirming bool
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

// Start issues a login code for phone and opens a call request. The code stays
// server-side until Status reports a confirmed call.
func (s *FlashCallService) Start(ctx context.Context, rawPhone, ip string) (flashCallStartResult, error) {
	phone := normalizeFlashCallPhone(rawPhone)
	if phone == "" || !s.allowed(phone) {
		return flashCallStartResult{}, errFlashCallUnsupportedNumber
	}
	now := s.now()
	if err := s.reserve(phone, ip, now); err != nil {
		return flashCallStartResult{}, err
	}
	session, err := s.provider.GetPhone(ctx, phone)
	if err != nil {
		return flashCallStartResult{}, err
	}
	_, expiresAt, err := s.issuer.EnsureFlashcallCode(ctx, phone)
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
		code := record.code
		s.mu.Unlock()
		return flashCallStatusResult{Status: FlashCallConfirmed, Code: code}, nil
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
			code := current.code
			s.mu.Unlock()
			return flashCallStatusResult{Status: FlashCallConfirmed, Code: code}, nil
		}
		if current.confirming {
			s.mu.Unlock()
			return flashCallStatusResult{Status: FlashCallPending}, nil
		}
		current.confirming = true
		s.mu.Unlock()

		// Rotate only now: the code that existed while the call was in flight
		// was never shown to anyone and must not survive confirmation.
		code, err := s.issuer.ConfirmFlashcall(ctx, phone)
		if err != nil {
			s.mu.Lock()
			if cur := s.pending[id]; cur != nil {
				cur.confirming = false
			}
			s.mu.Unlock()
			return flashCallStatusResult{}, err
		}
		s.mu.Lock()
		if cur := s.pending[id]; cur != nil {
			cur.confirmed = true
			cur.confirming = false
			cur.code = code
		}
		s.mu.Unlock()
		s.logger.Info("flashcall verification confirmed", zap.String("verification_id", id))
		return flashCallStatusResult{Status: FlashCallConfirmed, Code: code}, nil
	case FlashCallExpired:
		s.forget(id)
		return flashCallStatusResult{Status: FlashCallExpired}, nil
	default:
		return flashCallStatusResult{Status: FlashCallPending}, nil
	}
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

// HandlePage serves the public verification page.
func (s *FlashCallService) HandlePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = io.WriteString(w, strings.ReplaceAll(flashCallPageHTML, "__APP_NAME__", s.appName))
}

func (s *FlashCallService) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errFlashCallUnsupportedNumber):
		flashCallWriteJSON(w, http.StatusBadRequest, map[string]string{"error": "Поддерживаются только российские номера (+7)"})
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

const flashCallPageHTML = `<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Подтверждение номера — __APP_NAME__</title>
<style>
:root{--bg:#0a0c11;--card:#131722;--line:#242c3d;--text:#eef1f7;--muted:#8b95a9;--accent:#4f7cff;--ok:#31c07b;--err:#ff5f5f}
*{box-sizing:border-box}
html,body{height:100%}
body{margin:0;display:flex;align-items:center;justify-content:center;padding:24px;color:var(--text);
background:radial-gradient(900px 500px at 50% -10%,#1b2336 0%,var(--bg) 62%);
font:16px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;
-webkit-font-smoothing:antialiased}
.card{width:100%;max-width:440px;background:var(--card);border:1px solid var(--line);border-radius:20px;
padding:30px;box-shadow:0 24px 70px rgba(0,0,0,.5)}
.brand{display:flex;align-items:center;gap:10px;margin-bottom:22px;font-weight:650;letter-spacing:.2px}
.dot{width:10px;height:10px;border-radius:50%;background:var(--accent);box-shadow:0 0 14px var(--accent)}
h1{font-size:21px;line-height:1.3;margin:0 0 6px;font-weight:650}
p.sub{margin:0 0 20px;color:var(--muted);font-size:14px}
label{display:block;font-size:13px;color:var(--muted);margin-bottom:8px}
input{width:100%;padding:14px 16px;border-radius:12px;border:1px solid var(--line);background:#0d1119;
color:var(--text);font-size:17px;outline:none;transition:border-color .15s,box-shadow .15s}
input:focus{border-color:var(--accent);box-shadow:0 0 0 3px rgba(79,124,255,.18)}
button{width:100%;margin-top:16px;padding:14px 16px;border-radius:12px;border:0;background:var(--accent);
color:#fff;font-size:16px;font-weight:600;cursor:pointer;transition:filter .15s,opacity .15s}
button:hover:not(:disabled){filter:brightness(1.08)}
button:disabled{opacity:.55;cursor:default}
button.ghost{background:transparent;border:1px solid var(--line);color:var(--text);font-weight:500}
.number{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:18px 20px;
border-radius:14px;background:#0d1119;border:1px solid var(--line);margin:6px 0 18px}
.number strong{font-size:24px;letter-spacing:.6px;font-variant-numeric:tabular-nums}
.copy{border:1px solid var(--line);background:transparent;color:var(--muted);border-radius:10px;
padding:8px 12px;font-size:13px;width:auto;margin:0;cursor:pointer}
.timer{display:flex;align-items:center;gap:8px;color:var(--muted);font-size:14px;margin-bottom:18px}
.timer b{color:var(--text);font-variant-numeric:tabular-nums}
.steps{color:var(--muted);font-size:14px;margin:0 0 18px;padding-left:20px}
.steps li{margin-bottom:6px}
.code{text-align:center;padding:22px;border-radius:14px;background:rgba(49,192,123,.08);
border:1px solid rgba(49,192,123,.35);margin:6px 0 18px}
.code span{display:block;font-size:13px;color:var(--muted);margin-bottom:8px}
.code strong{font-size:34px;letter-spacing:8px;font-variant-numeric:tabular-nums;color:var(--ok)}
.error{margin-top:14px;padding:12px 14px;border-radius:12px;background:rgba(255,95,95,.1);
border:1px solid rgba(255,95,95,.35);color:#ffb3b3;font-size:14px}
.hint{color:var(--muted);font-size:13px;margin-top:16px;text-align:center}
[hidden]{display:none !important}
</style>
</head>
<body>
<div class="card">
  <div class="brand"><span class="dot"></span>__APP_NAME__</div>

  <section id="step-phone">
    <h1>Подтвердите номер телефона</h1>
    <p class="sub">Мы не отправляем SMS. Вместо этого вы позвоните на короткий номер — звонок бесплатный и сбрасывается автоматически.</p>
    <label for="phone">Номер телефона</label>
    <input id="phone" type="tel" inputmode="tel" autocomplete="tel" placeholder="+7 916 123-45-67">
    <button id="start-btn" onclick="startFlow()">Продолжить</button>
  </section>

  <section id="step-call" hidden>
    <h1>Позвоните на номер</h1>
    <p class="sub">Наберите этот номер со своего телефона. Сбрасывать не нужно — звонок оборвётся сам.</p>
    <div class="number">
      <strong id="call-number">—</strong>
      <button class="copy" onclick="copyNumber(this)">Копировать</button>
    </div>
    <div class="timer">Осталось: <b id="countdown">60 с</b></div>
    <ol class="steps">
      <li>Нажмите «Позвонить» в приложении телефона</li>
      <li>Дождитесь одного гудка и сбросьте</li>
      <li>Код появится здесь автоматически</li>
    </ol>
    <button class="ghost" onclick="resetFlow()">Запросить другой номер</button>
  </section>

  <section id="step-code" hidden>
    <h1>Номер подтверждён</h1>
    <p class="sub">Введите этот код в приложении __APP_NAME__ на экране входа.</p>
    <div class="code">
      <span>Ваш код</span>
      <strong id="code-value">—</strong>
    </div>
    <button class="ghost" onclick="copyCode(this)">Скопировать код</button>
    <p class="hint">Код действует 5 минут и работает один раз.</p>
  </section>

  <div id="error" class="error" hidden></div>
</div>
<script>
(function(){
  var API = '/v1/flashcall';
  var currentId = null;
  var pollTimer = null;
  var tickTimer = null;
  var deadline = 0;

  function el(id){ return document.getElementById(id); }

  function showStep(id){
    var steps = ['step-phone','step-call','step-code'];
    for(var i=0;i<steps.length;i++){ el(steps[i]).hidden = steps[i] !== id; }
  }

  function setError(message){
    var box = el('error');
    if(!message){ box.hidden = true; box.textContent = ''; return; }
    box.hidden = false;
    box.textContent = message;
  }

  function stopTimers(){
    if(pollTimer){ clearInterval(pollTimer); pollTimer = null; }
    if(tickTimer){ clearInterval(tickTimer); tickTimer = null; }
  }

  function startTicking(){
    if(tickTimer){ clearInterval(tickTimer); }
    tickTimer = setInterval(function(){
      var left = Math.max(0, Math.ceil((deadline - Date.now()) / 1000));
      el('countdown').textContent = left + ' с';
      if(left === 0){ clearInterval(tickTimer); tickTimer = null; }
    }, 250);
  }

  function startPolling(){
    if(pollTimer){ clearInterval(pollTimer); }
    pollTimer = setInterval(check, 2000);
  }

  function check(){
    if(!currentId){ return; }
    fetch(API + '/status?id=' + encodeURIComponent(currentId), {cache:'no-store'})
      .then(function(res){
        return res.json().catch(function(){ return {}; }).then(function(data){
          return {ok: res.ok, status: res.status, data: data};
        });
      })
      .then(function(result){
        if(!result.ok){
          if(result.status === 404){ fail('Сессия истекла. Запросите номер заново.'); }
          return;
        }
        if(result.data.status === 'CONFIRMED' && result.data.code){
          stopTimers();
          el('code-value').textContent = result.data.code;
          setError('');
          showStep('step-code');
          return;
        }
        if(result.data.status === 'EXPIRED'){
          fail('Звонок не поступил. Запросите новый номер.');
        }
      })
      .catch(function(){ /* network hiccup: keep polling until the deadline */ });
  }

  function fail(message){
    stopTimers();
    currentId = null;
    showStep('step-phone');
    setError(message);
  }

  window.startFlow = function(){
    setError('');
    var phone = el('phone').value.trim();
    if(!phone){ setError('Введите номер телефона'); return; }
    var button = el('start-btn');
    button.disabled = true;
    button.textContent = 'Получаем номер…';
    fetch(API + '/start', {
      method: 'POST',
      headers: {'Content-Type':'application/json'},
      body: JSON.stringify({phone: phone})
    })
      .then(function(res){
        return res.json().catch(function(){ return {}; }).then(function(data){
          return {ok: res.ok, data: data};
        });
      })
      .then(function(result){
        if(!result.ok){
          setError(result.data.error || 'Не удалось получить номер');
          return;
        }
        currentId = result.data.id;
        el('call-number').textContent = result.data.number;
        deadline = Date.now() + (result.data.expires_in || 60) * 1000;
        showStep('step-call');
        startTicking();
        startPolling();
      })
      .catch(function(){
        setError('Сеть недоступна. Проверьте соединение и попробуйте снова.');
      })
      .then(function(){
        button.disabled = false;
        button.textContent = 'Продолжить';
      });
  };

  window.resetFlow = function(){
    stopTimers();
    currentId = null;
    setError('');
    showStep('step-phone');
  };

  function copyText(text, button){
    var done = function(){ 
      var original = button.textContent;
      button.textContent = 'Скопировано';
      setTimeout(function(){ button.textContent = original; }, 1500);
    };
    if(navigator.clipboard && navigator.clipboard.writeText){
      navigator.clipboard.writeText(text).then(done, function(){});
      return;
    }
    var area = document.createElement('textarea');
    area.value = text;
    document.body.appendChild(area);
    area.select();
    try { document.execCommand('copy'); done(); } catch(e) {}
    document.body.removeChild(area);
  }

  window.copyNumber = function(button){
    var value = el('call-number').textContent.replace(/\s+/g, '');
    copyText(value, button);
  };

  window.copyCode = function(button){
    copyText(el('code-value').textContent, button);
  };

  el('phone').addEventListener('keydown', function(e){
    if(e.key === 'Enter'){ window.startFlow(); }
  });
})();
</script>
</body>
</html>`
