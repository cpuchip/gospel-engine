// Package citations answers "who has cited this verse" from the BYU Scripture
// Citation Index (scriptures.byu.edu), live, with links back. The data is
// BYU's: the engine passes a reference to the index, parses its answer, keeps
// it for a day so repeated lookups don't load their site, and links every
// result to the index and, for conference talks, to the talk itself.
//
// A light client by design: one upstream request per uncached lookup, a
// descriptive User-Agent with a contact address, no bulk crawling or
// pre-fetching, and the engine's per-token rate limit in front of it.
package citations

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Source is the attribution every response carries.
const (
	Source    = "BYU Scripture Citation Index"
	SourceURL = "https://scriptures.byu.edu/"
)

// Citation is one place the index says the verse was cited.
type Citation struct {
	Reference string `json:"reference"`          // the index's own locator: "1989-O:54", "JD 19:81b"
	Speaker   string `json:"speaker"`            //
	Title     string `json:"title"`              //
	IndexURL  string `json:"index_url"`          // the citing talk opened in the index, at the citation
	TalkURL   string `json:"talk_url,omitempty"` // the talk at churchofjesuschrist.org, when the index gives one
}

// Result is one lookup.
type Result struct {
	Reference string     `json:"reference"` // as asked, e.g. "Ether 12:27"
	Source    string     `json:"source"`
	SourceURL string     `json:"source_url"`
	Count     int        `json:"count"`
	Citations []Citation `json:"citations"`
	FetchedAt time.Time  `json:"fetched_at"`
	Cached    bool       `json:"cached"`
}

// Client queries the index with a day-long cache.
type Client struct {
	BaseURL   string        // https://scriptures.byu.edu
	UserAgent string        // names the engine and a contact address
	TTL       time.Duration // ruled 2026-10-09: one day
	MaxItems  int           // cache entries kept
	HTTP      *http.Client
	now       func() time.Time

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	res     Result
	expires time.Time
}

// New returns a client with the ruled cache life (24 h) and a 5,000-entry cap.
func New(contact, version string) *Client {
	ua := "gospel-engine/" + version + " (+https://engine.ibeco.me"
	if contact != "" {
		ua += "; contact " + contact
	}
	ua += ")"
	return &Client{
		BaseURL:   "https://scriptures.byu.edu",
		UserAgent: ua,
		TTL:       24 * time.Hour,
		MaxItems:  5000,
		HTTP:      &http.Client{Timeout: 20 * time.Second},
		now:       time.Now,
		cache:     map[string]cached{},
	}
}

// ErrUnknownBook is returned for a book the index does not cover.
var ErrUnknownBook = errors.New("book not in the citation index")

// Lookup returns the citations of book (an engine slug: "ether", "dc", "1-cor"),
// chapter, and verses ("27", "27-28"). display is the reference as the caller
// should see it.
func (c *Client) Lookup(ctx context.Context, book string, chapter int, verses, display string) (*Result, error) {
	id, ok := bookIDs[book]
	if !ok {
		return nil, ErrUnknownBook
	}
	key := fmt.Sprintf("%d/%d/%s", id, chapter, verses)
	now := c.now()
	c.mu.Lock()
	if e, ok := c.cache[key]; ok && now.Before(e.expires) {
		c.mu.Unlock()
		r := e.res
		r.Reference, r.Cached = display, true
		return &r, nil
	}
	c.mu.Unlock()

	// Through the current year, so citations from conferences after a
	// hardcoded year are not missed.
	u := fmt.Sprintf("%s/citation_index/citation_ajax/Any/1830/%d/all/s/f/%d/%d?verses=%s",
		c.BaseURL, now.Year(), id, chapter, verses)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("citation index unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("reading citation index: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("citation index answered %d", resp.StatusCode)
	}
	cites, err := Parse(string(body), c.BaseURL)
	if err != nil {
		return nil, err
	}
	res := Result{Source: Source, SourceURL: SourceURL, Count: len(cites), Citations: cites, FetchedAt: now}

	c.mu.Lock()
	if len(c.cache) >= c.MaxItems {
		c.evictLocked(now)
	}
	c.cache[key] = cached{res: res, expires: now.Add(c.TTL)}
	c.mu.Unlock()

	res.Reference = display
	return &res, nil
}

// evictLocked drops expired entries, then the oldest until there is room.
func (c *Client) evictLocked(now time.Time) {
	for k, e := range c.cache {
		if !now.Before(e.expires) {
			delete(c.cache, k)
		}
	}
	for len(c.cache) >= c.MaxItems {
		var oldest string
		var at time.Time
		for k, e := range c.cache {
			if oldest == "" || e.expires.Before(at) {
				oldest, at = k, e.expires
			}
		}
		delete(c.cache, oldest)
	}
}

var (
	itemRe    = regexp.MustCompile(`(?s)<li>(.*?)</li>`)
	talkRe    = regexp.MustCompile(`getTalk\('(\d+)',\s*'(\d+)'\)`)
	refRe     = regexp.MustCompile(`class="reference(?: [^"]*)?"[^>]*>([^<]*)<`)
	titleRe   = regexp.MustCompile(`class="talktitle(?: [^"]*)?"[^>]*>([^<]*)<`)
	watchRe   = regexp.MustCompile(`watchTalk\('\d+',\s*'(https://www\.churchofjesuschrist\.org/[^']+)'\)`)
	refSplitR = regexp.MustCompile(`^(.*?),\s*(.+)$`)
)

// Parse reads the index's answer: one <li> per citation, each holding the
// getTalk ids, the locator and speaker, the title, and for conference talks a
// watch link to churchofjesuschrist.org. Fields are read inside each item, so
// they cannot drift out of step with each other; an item missing its ids,
// locator or title is an error, not a guess.
func Parse(body, baseURL string) ([]Citation, error) {
	var out []Citation
	for _, m := range itemRe.FindAllStringSubmatch(body, -1) {
		item := m[1]
		t := talkRe.FindStringSubmatch(item)
		if t == nil {
			continue // not a citation item
		}
		r, ti := refRe.FindStringSubmatch(item), titleRe.FindStringSubmatch(item)
		if r == nil || ti == nil {
			return nil, fmt.Errorf("citation index answer changed shape (talk %s has no locator or title)", t[1])
		}
		c := Citation{Title: clean(ti[1]), IndexURL: talkLink(baseURL, t[1], t[2])}
		loc := clean(r[1])
		if s := refSplitR.FindStringSubmatch(loc); s != nil {
			c.Reference, c.Speaker = s[1], s[2]
		} else {
			c.Speaker = loc
		}
		if w := watchRe.FindStringSubmatch(item); w != nil {
			c.TalkURL = html.UnescapeString(w[1])
		}
		out = append(out, c)
	}
	return out, nil
}

func clean(s string) string { return strings.Join(strings.Fields(html.UnescapeString(s)), " ") }

// talkLink opens a citing talk in the index at the citation. The form is the
// index's own (base.js encodeCenter/encodeTalk, read 2026-10-09): the hash
// holds "<scripture>:<center>:<search>", the center is "t" + the talk id in
// hex, then "$" + the citation's id.
func talkLink(baseURL, talkID, refID string) string {
	id, err := strconv.ParseInt(talkID, 10, 64)
	if err != nil {
		return baseURL + "/"
	}
	return fmt.Sprintf("%s/#:t%x$%s:", baseURL, id, refID)
}
