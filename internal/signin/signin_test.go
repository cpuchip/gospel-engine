package signin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/cpuchip/gospel-engine/internal/db"
	"github.com/cpuchip/gospel-engine/internal/testdb"
)

func TestPagesRenderWithoutADatabase(t *testing.T) {
	h := New(nil, Config{Contact: "someone@example.org"})
	r := chi.NewRouter()
	h.Mount(r)
	for path, want := range map[string]string{
		"/privacy": "someone@example.org",
		"/keys":    "Sign-in is not configured",
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: body lacks %q (status %d)", path, want, rec.Code)
		}
	}
	h.Cfg = Config{ClientID: "id", ClientSecret: "s", RedirectURL: "https://engine.example/auth/google/callback"}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/keys", nil))
	if !strings.Contains(rec.Body.String(), "Sign in with Google") {
		t.Errorf("signed-out key page lacks the sign-in button")
	}
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/google/login", nil))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	q := loc.Query()
	if rec.Code != http.StatusFound || q.Get("scope") != "openid email" || q.Get("state") == "" ||
		q.Get("redirect_uri") != "https://engine.example/auth/google/callback" {
		t.Errorf("login redirect %d %s", rec.Code, loc)
	}
}

// fakeGoogle answers the token and userinfo calls for one code.
func fakeGoogle(t *testing.T, sub, email string, verified bool) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("code") != "good-code" || r.PostForm.Get("client_secret") != "secret" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "at-" + sub})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-"+sub {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sub": sub, "email": email, "email_verified": verified})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestFlowAgainstPostgres signs in through a fake Google, creates, lists and
// revokes a key, checks the forms' CSRF guard and ownership, and deletes the
// account (GOSPEL_TEST_DATABASE_URL, local only; destructive; run with -p 1).
func TestFlowAgainstPostgres(t *testing.T) {
	dsn := testdb.URL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Pool.Exec(ctx, `TRUNCATE sessions, users RESTART IDENTITY CASCADE; DELETE FROM api_tokens WHERE external_user LIKE 'google:%'`); err != nil {
		t.Fatal(err)
	}

	newSite := func(sub, email string, verified bool) (*httptest.Server, *http.Client) {
		g := fakeGoogle(t, sub, email, verified)
		h := New(d, Config{ClientID: "id", ClientSecret: "secret", CookieSecure: false, Contact: "c@example.org"})
		h.TokenURL, h.UserInfoURL, h.AuthURL = g.URL+"/token", g.URL+"/userinfo", g.URL+"/auth"
		r := chi.NewRouter()
		h.Mount(r)
		site := httptest.NewServer(r)
		t.Cleanup(site.Close)
		h.Cfg.RedirectURL = site.URL + "/auth/google/callback"
		jar, _ := cookiejar.New(nil)
		c := &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if strings.HasPrefix(req.URL.String(), g.URL) {
				return http.ErrUseLastResponse // stop at Google's consent page
			}
			return nil
		}}
		return site, c
	}
	body := func(resp *http.Response) string {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(b)
	}
	signIn := func(site *httptest.Server, c *http.Client, code string) (int, string) {
		resp, err := c.Get(site.URL + "/auth/google/login")
		if err != nil {
			t.Fatal(err)
		}
		loc, _ := url.Parse(resp.Header.Get("Location"))
		resp.Body.Close()
		state := loc.Query().Get("state")
		resp, err = c.Get(site.URL + "/auth/google/callback?" + url.Values{"state": {state}, "code": {code}}.Encode())
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, body(resp)
	}
	csrfRe := regexp.MustCompile(`name="csrf" value="([0-9a-f]{32})"`)
	keyRe := regexp.MustCompile(`<code class="key">(stdy_[0-9a-f]{64})</code>`)
	revokeRe := regexp.MustCompile(`action="/keys/(\d+)/revoke"`)

	site, alice := newSite("sub-alice", "alice@example.org", true)
	status, page := signIn(site, alice, "good-code")
	if status != 200 || !strings.Contains(page, "alice@example.org") {
		t.Fatalf("sign-in: %d, page without the email", status)
	}
	csrf := csrfRe.FindStringSubmatch(page)[1]

	post := func(c *http.Client, path string, form url.Values) (int, string) {
		resp, err := c.PostForm(site.URL+path, form)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, body(resp)
	}
	if code, _ := post(alice, "/keys", url.Values{"csrf": {"0000"}, "name": {"x"}}); code != http.StatusForbidden {
		t.Errorf("create with a wrong csrf: %d, want 403", code)
	}
	code, page := post(alice, "/keys", url.Values{"csrf": {csrf}, "name": {"laptop"}, "expires_days": {"30"}})
	m := keyRe.FindStringSubmatch(page)
	if code != 200 || m == nil {
		t.Fatalf("create key: %d, no key shown", code)
	}
	tok, err := d.ValidateAPIToken(ctx, m[1])
	if err != nil || tok == nil || tok.ExternalUser != "google:sub-alice" || tok.IsAdmin || tok.RateLimit != 60 || tok.ExpiresAt == nil {
		t.Errorf("minted token: %+v %v", tok, err)
	}
	if strings.Count(page, m[1]) != 2 { // the key and the MCP URL, once each, on this page only
		t.Errorf("new key shown %d times", strings.Count(page, m[1]))
	}
	resp, _ := alice.Get(site.URL + "/keys")
	if strings.Contains(body(resp), m[1]) {
		t.Error("the raw key is shown again on reload")
	}

	// Another person cannot revoke Alice's key.
	// bob signs in on his own site instance sharing the database
	siteB, bob := newSite("sub-bob", "bob@example.org", true)
	_, pageB := signIn(siteB, bob, "good-code")
	csrfB := csrfRe.FindStringSubmatch(pageB)[1]
	if resp, err := bob.PostForm(siteB.URL+"/keys/"+itoa(tok.ID)+"/revoke", url.Values{"csrf": {csrfB}}); err == nil {
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("bob revoking alice's key: %d, want 404", resp.StatusCode)
		}
		resp.Body.Close()
	}

	// Alice revokes it.
	resp, _ = alice.Get(site.URL + "/keys")
	page = body(resp)
	id := revokeRe.FindStringSubmatch(page)[1]
	if code, _ := post(alice, "/keys/"+id+"/revoke", url.Values{"csrf": {csrf}}); code != 200 {
		t.Errorf("revoke: %d", code)
	}
	if tok, _ := d.ValidateAPIToken(ctx, m[1]); tok != nil {
		t.Error("revoked key still validates")
	}

	// The 11th live key is refused.
	for i := 0; i < 10; i++ {
		post(alice, "/keys", url.Values{"csrf": {csrf}, "name": {"k" + itoa(int64(i))}})
	}
	if code, _ := post(alice, "/keys", url.Values{"csrf": {csrf}, "name": {"one too many"}}); code != http.StatusConflict {
		t.Errorf("11th live key: %d, want 409", code)
	}

	// A sign-in link works once; an unverified email is refused.
	resp, _ = alice.Get(site.URL + "/auth/google/callback?state=never-issued&code=good-code")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown state: %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	siteC, carol := newSite("sub-carol", "carol@example.org", false)
	if status, _ := signIn(siteC, carol, "good-code"); status != http.StatusForbidden {
		t.Errorf("unverified email: %d, want 403", status)
	}

	// Alice deletes her account: keys, sessions and user row go.
	if code, _ := post(alice, "/account/delete", url.Values{"csrf": {csrf}, "confirm": {"nope"}}); code != http.StatusBadRequest {
		t.Errorf("delete without confirm: %d, want 400", code)
	}
	if code, _ := post(alice, "/account/delete", url.Values{"csrf": {csrf}, "confirm": {"delete"}}); code != 200 {
		t.Errorf("delete: %d", code)
	}
	var n int
	_ = d.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM api_tokens WHERE external_user = 'google:sub-alice') +
		(SELECT count(*) FROM users WHERE google_sub = 'sub-alice')`).Scan(&n)
	if n != 0 {
		t.Errorf("after deletion %d rows remain for alice", n)
	}
	resp, _ = alice.Get(site.URL + "/keys")
	if strings.Contains(body(resp), "alice@example.org") {
		t.Error("still signed in after deleting the account")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
