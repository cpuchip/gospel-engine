// Package signin lets people sign in to engine.ibeco.me with Google and
// create, see and revoke their own API keys there.
//
// It follows ibeco.me's sign-in (scripts/becoming internal/auth): the same
// Google OAuth client from the same env names, the authorization-code flow
// with a one-time state that expires in 5 minutes, a server-side session of
// 30 days in an HttpOnly, Secure, SameSite=Lax cookie, sign-out deleting the
// row. Deliberate differences, all narrower: the scope is "openid email" (no
// name, no picture); the cookies are host-only and, over HTTPS, "__Host-"
// prefixed, so no other ibeco.me host can set or read them; and the OAuth
// state is bound to the browser that started the sign-in (an HMAC-signed
// cookie checked at the callback), so a callback link cannot sign someone
// into another person's account, and no server-side state map exists to flood
// or to lose across instances. After sign-in the redirect is always /keys:
// there is no redirect parameter to abuse.
//
// Keys minted here are ordinary api_tokens (never admin), owned as
// external_user "google:<sub>", at most 10 live per person, 600 requests a
// minute each (enforced by the engine's rate limiter).
package signin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/cpuchip/gospel-engine/internal/db"
)

const (
	sessionLife    = 30 * 24 * time.Hour
	stateLife      = 5 * time.Minute
	maxLiveKeys    = 10
	keyRateLimit   = 600 // ruled 2026-10-10: aligned with ibeco.me's per-user keys
	ownerPrefix    = "google:"
	maxKeyNameRune = 60
)

//go:embed templates/*.html
var templateFS embed.FS

var pages = template.Must(template.ParseFS(templateFS, "templates/*.html"))

// Config holds the Google client and site settings. ClientID empty disables
// sign-in (the key routes answer 404; /privacy is still served).
type Config struct {
	ClientID     string // GOOGLE_CLIENT_ID
	ClientSecret string // GOOGLE_CLIENT_SECRET
	RedirectURL  string // GOOGLE_REDIRECT_URL, e.g. https://engine.ibeco.me/auth/google/callback
	CookieSecure bool   // false only for local http development
	Contact      string // the privacy contact address
}

// Handler serves the sign-in, key and privacy pages.
type Handler struct {
	DB  *db.DB
	Cfg Config

	// Google's endpoints; tests point them at a fake.
	AuthURL     string
	TokenURL    string
	UserInfoURL string
	HTTP        *http.Client
	now         func() time.Time
}

// New returns a handler with Google's real endpoints.
func New(database *db.DB, cfg Config) *Handler {
	return &Handler{
		DB:          database,
		Cfg:         cfg,
		AuthURL:     "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:    "https://oauth2.googleapis.com/token",
		UserInfoURL: "https://www.googleapis.com/oauth2/v3/userinfo",
		HTTP:        &http.Client{Timeout: 15 * time.Second},
		now:         time.Now,
	}
}

// Enabled reports whether a Google client is configured.
func (h *Handler) Enabled() bool {
	return h.Cfg.ClientID != "" && h.Cfg.ClientSecret != "" && h.Cfg.RedirectURL != ""
}

// Mount adds the routes to r.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/privacy", h.privacy)
	r.Get("/keys", h.keys)
	r.Post("/keys", h.createKey)
	r.Post("/keys/{id}/revoke", h.revokeKey)
	r.Post("/account/delete", h.deleteAccount)
	r.Get("/auth/google/login", h.login)
	r.Get("/auth/google/callback", h.callback)
	r.Post("/auth/logout", h.logout)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failing is not recoverable
	}
	return hex.EncodeToString(b)
}

// cookie names: "__Host-" over HTTPS (the browser then refuses the cookie
// unless Secure, Path=/ and no Domain), the bare name for local http.
func (h *Handler) cookie(name string) string {
	if h.Cfg.CookieSecure {
		return "__Host-" + name
	}
	return name
}

func (h *Handler) setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: h.cookie(name), Value: value, Path: "/", HttpOnly: true,
		Secure: h.Cfg.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}

// signState makes the browser-bound state value "state.expiry.mac", keyed by
// the client secret (stable across restarts and instances, never sent).
func (h *Handler) signState(state string, exp int64) string {
	m := hmac.New(sha256.New, []byte("engine-oauth-state:"+h.Cfg.ClientSecret))
	fmt.Fprintf(m, "%s.%d", state, exp)
	return fmt.Sprintf("%s.%d.%s", state, exp, hex.EncodeToString(m.Sum(nil)))
}

// checkState reports whether the state cookie is ours, unexpired and names
// the state Google returned.
func (h *Handler) checkState(cookieVal, state string) bool {
	parts := strings.Split(cookieVal, ".")
	if len(parts) != 3 || state == "" || parts[0] != state {
		return false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || h.now().Unix() >= exp {
		return false
	}
	return hmac.Equal([]byte(h.signState(parts[0], exp)), []byte(cookieVal))
}

func hashToken(t string) string {
	s := sha256.Sum256([]byte(t))
	return hex.EncodeToString(s[:])
}

// --- pages ---

func (h *Handler) render(w http.ResponseWriter, status int, name string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["Contact"] = h.Cfg.Contact
	data["Enabled"] = h.Enabled()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.WriteHeader(status)
	if err := pages.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("signin: render %s: %v", name, err)
	}
}

func (h *Handler) privacy(w http.ResponseWriter, r *http.Request) {
	h.render(w, http.StatusOK, "privacy.html", nil)
}

type session struct {
	userID int64
	sub    string
	email  string
	csrf   string
	hash   string
}

// currentSession returns the signed-in session, or nil.
func (h *Handler) currentSession(ctx context.Context, r *http.Request) *session {
	c, err := r.Cookie(h.cookie("engine_session"))
	if err != nil || len(c.Value) != 64 {
		return nil
	}
	s := &session{hash: hashToken(c.Value)}
	err = h.DB.Pool.QueryRow(ctx, `
		SELECT u.id, u.google_sub, u.email, s.csrf
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > $2`, s.hash, h.now()).Scan(&s.userID, &s.sub, &s.email, &s.csrf)
	if err != nil {
		return nil
	}
	return s
}

func (h *Handler) keys(w http.ResponseWriter, r *http.Request) {
	if !h.Enabled() {
		h.render(w, http.StatusNotFound, "keys.html", map[string]any{"Error": "Sign-in is not configured on this server."})
		return
	}
	s := h.currentSession(r.Context(), r)
	if s == nil {
		h.render(w, http.StatusOK, "keys.html", nil)
		return
	}
	var extra map[string]any
	if c, err := r.Cookie(h.cookie("engine_newkey")); err == nil {
		// set by createKey, read once and cleared: a reload neither shows the
		// key again nor creates another
		h.setCookie(w, "engine_newkey", "", -1)
		if name, key, ok := strings.Cut(c.Value, "|"); ok && strings.HasPrefix(key, db.TokenPrefix) {
			if n, err := url.QueryUnescape(name); err == nil {
				extra = map[string]any{"NewKey": key, "NewKeyName": n}
			}
		}
	}
	h.renderKeys(w, r, s, http.StatusOK, extra)
}

func (h *Handler) renderKeys(w http.ResponseWriter, r *http.Request, s *session, status int, extra map[string]any) {
	toks, err := h.DB.ListAPITokensByOwner(r.Context(), ownerPrefix+s.sub)
	if err != nil {
		log.Printf("signin: list keys: %v", err)
		h.render(w, http.StatusInternalServerError, "keys.html", map[string]any{"Error": "Could not read your keys; try again."})
		return
	}
	now := h.now()
	type row struct {
		ID                              int64
		Name, Prefix, Created, LastUsed string
		Expires, Status                 string
		RateLimit                       int
		Live                            bool
	}
	var rows []row
	live := 0
	for _, t := range toks {
		rw := row{ID: t.ID, Name: t.Name, Prefix: t.Prefix, Created: t.CreatedAt.UTC().Format("2006-01-02"),
			RateLimit: t.RateLimit, LastUsed: "never", Expires: "never", Status: "live", Live: true}
		if t.LastUsed != nil {
			rw.LastUsed = t.LastUsed.UTC().Format("2006-01-02 15:04 UTC")
		}
		if t.ExpiresAt != nil {
			rw.Expires = t.ExpiresAt.UTC().Format("2006-01-02")
			if !t.ExpiresAt.After(now) {
				rw.Status, rw.Live = "expired", false
			}
		}
		if t.Revoked {
			rw.Status, rw.Live = "revoked", false
		}
		if rw.Live {
			live++
		}
		rows = append(rows, rw)
	}
	data := map[string]any{"Email": s.email, "CSRF": s.csrf, "Keys": rows, "Live": live, "Max": maxLiveKeys, "RateLimit": keyRateLimit}
	for k, v := range extra {
		data[k] = v
	}
	h.render(w, status, "keys.html", data)
}

// checkPost returns the session for a form POST with a matching csrf field.
func (h *Handler) checkPost(w http.ResponseWriter, r *http.Request) *session {
	if !h.Enabled() {
		http.Error(w, "sign-in is not configured", http.StatusNotFound)
		return nil
	}
	s := h.currentSession(r.Context(), r)
	if s == nil {
		h.setCookie(w, "engine_session", "", -1)
		http.Redirect(w, r, "/keys", http.StatusSeeOther)
		return nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil || subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(s.csrf)) != 1 {
		http.Error(w, "the form expired; reload the page and try again", http.StatusForbidden)
		return nil
	}
	return s
}

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	s := h.checkPost(w, r)
	if s == nil {
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" || len([]rune(name)) > maxKeyNameRune {
		h.renderKeys(w, r, s, http.StatusBadRequest, map[string]any{"Error": fmt.Sprintf("Give the key a name of 1 to %d characters.", maxKeyNameRune)})
		return
	}
	var expires *time.Time
	if d := r.PostFormValue("expires_days"); d != "" && d != "0" {
		n, err := strconv.Atoi(d)
		if err != nil || n < 1 || n > 3650 {
			h.renderKeys(w, r, s, http.StatusBadRequest, map[string]any{"Error": "Expiry must be a number of days from 1 to 3650, or never."})
			return
		}
		t := h.now().Add(time.Duration(n) * 24 * time.Hour)
		expires = &t
	}
	_, raw, err := h.DB.CreateOwnedAPIToken(r.Context(), ownerPrefix+s.sub, name, expires, keyRateLimit, maxLiveKeys)
	if errors.Is(err, db.ErrTooManyTokens) {
		h.renderKeys(w, r, s, http.StatusConflict, map[string]any{"Error": fmt.Sprintf("You have %d live keys, the most allowed. Revoke one first.", maxLiveKeys)})
		return
	}
	if err != nil {
		log.Printf("signin: create key: %v", err)
		h.renderKeys(w, r, s, http.StatusInternalServerError, map[string]any{"Error": "Could not create the key; try again."})
		return
	}
	// Post-redirect-get: the key rides one short-lived cookie to the next page
	// view and is cleared there. It is never stored on the server.
	h.setCookie(w, "engine_newkey", url.QueryEscape(name)+"|"+raw, 120)
	http.Redirect(w, r, "/keys", http.StatusSeeOther)
}

func (h *Handler) revokeKey(w http.ResponseWriter, r *http.Request) {
	s := h.checkPost(w, r)
	if s == nil {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err == nil {
		err = h.DB.RevokeOwnedAPIToken(r.Context(), id, ownerPrefix+s.sub)
	}
	if err != nil {
		h.renderKeys(w, r, s, http.StatusNotFound, map[string]any{"Error": "That key is not one of yours."})
		return
	}
	http.Redirect(w, r, "/keys", http.StatusSeeOther)
}

// deleteAccount removes the person's keys, sessions and user row.
func (h *Handler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	s := h.checkPost(w, r)
	if s == nil {
		return
	}
	if r.PostFormValue("confirm") != "delete" {
		h.renderKeys(w, r, s, http.StatusBadRequest, map[string]any{"Error": "Type delete in the box to confirm."})
		return
	}
	if err := h.DB.DeleteOwnerAccount(r.Context(), ownerPrefix+s.sub, s.userID); err != nil { // sessions cascade
		log.Printf("signin: delete account: %v", err)
		h.renderKeys(w, r, s, http.StatusInternalServerError, map[string]any{"Error": "Could not delete the account; try again."})
		return
	}
	h.setCookie(w, "engine_session", "", -1)
	h.render(w, http.StatusOK, "keys.html", map[string]any{"Notice": "Your account and all its keys are deleted."})
}

// --- the OAuth flow ---

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if !h.Enabled() {
		http.Error(w, "sign-in is not configured", http.StatusNotFound)
		return
	}
	state := randomHex(16)
	h.setCookie(w, "engine_oauth", h.signState(state, h.now().Add(stateLife).Unix()), int(stateLife/time.Second))
	q := url.Values{
		"client_id":     {h.Cfg.ClientID},
		"redirect_uri":  {h.Cfg.RedirectURL},
		"response_type": {"code"},
		"scope":         {"openid email"},
		"state":         {state},
		"access_type":   {"online"},
		"prompt":        {"select_account"},
	}
	http.Redirect(w, r, h.AuthURL+"?"+q.Encode(), http.StatusFound)
}

type userInfo struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
}

func (h *Handler) callback(w http.ResponseWriter, r *http.Request) {
	if !h.Enabled() {
		http.Error(w, "sign-in is not configured", http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	c, err := r.Cookie(h.cookie("engine_oauth"))
	h.setCookie(w, "engine_oauth", "", -1) // one use, whatever happens next
	if err != nil || !h.checkState(c.Value, q.Get("state")) {
		h.render(w, http.StatusBadRequest, "keys.html", map[string]any{"Error": "That sign-in link expired. Try again."})
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		h.render(w, http.StatusBadRequest, "keys.html", map[string]any{"Error": "Sign-in was cancelled."})
		return
	}
	info, err := h.exchange(r.Context(), q.Get("code"))
	if err != nil {
		log.Printf("signin: google: %v", err)
		h.render(w, http.StatusBadGateway, "keys.html", map[string]any{"Error": "Google sign-in failed. Try again."})
		return
	}
	if info.Sub == "" || info.Email == "" || !info.EmailVerified {
		h.render(w, http.StatusForbidden, "keys.html", map[string]any{"Error": "Google did not give a verified email for that account."})
		return
	}
	var userID int64
	err = h.DB.Pool.QueryRow(r.Context(), `
		INSERT INTO users (google_sub, email) VALUES ($1, $2)
		ON CONFLICT (google_sub) DO UPDATE SET email = EXCLUDED.email, last_login = NOW()
		RETURNING id`, info.Sub, info.Email).Scan(&userID)
	if err != nil {
		log.Printf("signin: upsert user: %v", err)
		h.render(w, http.StatusInternalServerError, "keys.html", map[string]any{"Error": "Could not sign you in; try again."})
		return
	}
	token := randomHex(32)
	// every sign-in prunes expired sessions (everyone's), so none outlives its 30 days by long
	if _, err := h.DB.Pool.Exec(r.Context(), `DELETE FROM sessions WHERE expires_at <= $1`, h.now()); err != nil {
		log.Printf("signin: prune sessions: %v", err)
	}
	if _, err := h.DB.Pool.Exec(r.Context(),
		`INSERT INTO sessions (token_hash, user_id, csrf, expires_at) VALUES ($1, $2, $3, $4)`,
		hashToken(token), userID, randomHex(16), h.now().Add(sessionLife)); err != nil {
		log.Printf("signin: create session: %v", err)
		h.render(w, http.StatusInternalServerError, "keys.html", map[string]any{"Error": "Could not sign you in; try again."})
		return
	}
	h.setCookie(w, "engine_session", token, int(sessionLife/time.Second))
	http.Redirect(w, r, "/keys", http.StatusFound)
}

func (h *Handler) exchange(ctx context.Context, code string) (*userInfo, error) {
	form := url.Values{
		"code":          {code},
		"client_id":     {h.Cfg.ClientID},
		"client_secret": {h.Cfg.ClientSecret},
		"redirect_uri":  {h.Cfg.RedirectURL},
		"grant_type":    {"authorization_code"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := h.doJSON(req, &tok); err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, errors.New("token exchange: no access token")
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, h.UserInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	var info userInfo
	if err := h.doJSON(req, &info); err != nil {
		return nil, fmt.Errorf("userinfo: %w", err)
	}
	return &info, nil
}

func (h *Handler) doJSON(req *http.Request, v any) error {
	resp, err := h.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode) // the body can echo request details; not logged
	}
	return json.Unmarshal(body, v)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	s := h.checkPost(w, r)
	if s == nil {
		return
	}
	if _, err := h.DB.Pool.Exec(r.Context(), `DELETE FROM sessions WHERE token_hash = $1`, s.hash); err != nil {
		log.Printf("signin: logout: %v", err)
	}
	h.setCookie(w, "engine_session", "", -1)
	http.Redirect(w, r, "/keys", http.StatusSeeOther)
}
