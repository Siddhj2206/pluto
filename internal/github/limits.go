package github

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// pollReserve is how many requests a token keeps back from job polling, so
// registering and removing runners for jobs already found still works.
const pollReserve = 100

// maxCached bounds the conditional-request cache. Each active workflow run
// adds an entry, and old runs stop being asked about.
const maxCached = 1000

// Shared is what clients for every token learn over time: answers to repeat
// for conditional requests, and each token's rate limit. A host keeps one for
// its whole life. A Client without one asks GitHub afresh every time.
type Shared struct {
	// Now is the clock, for tests. Nil means time.Now.
	Now func() time.Time

	mu     sync.Mutex
	cache  map[string]*cached
	limits map[string]*limit
}

type cached struct {
	etag string
	body []byte
	used time.Time
}

type limit struct {
	remaining int
	known     bool
	reset     time.Time
	blocked   time.Time // No requests before this.
}

// NewShared returns empty shared state.
func NewShared() *Shared {
	return &Shared{cache: map[string]*cached{}, limits: map[string]*limit{}}
}

func (s *Shared) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// RateLimitError means GitHub won't take, or Pluto is holding back, requests
// with this token until a time.
type RateLimitError struct {
	Until time.Time
	// Reserve means Pluto stopped polling to keep requests for runners.
	Reserve bool
}

func (e *RateLimitError) Error() string {
	at := e.Until.Local().Format("15:04")
	if e.Reserve {
		return fmt.Sprintf("this token has fewer than %d GitHub API requests left this hour, so Pluto stops looking for new jobs until %s and keeps the rest for jobs already starting", pollReserve, at)
	}
	return "GitHub's rate limit for this token is used up, so Pluto waits until " + at
}

func (s *Shared) limitFor(token string) *limit {
	l := s.limits[token]
	if l == nil {
		l = &limit{}
		s.limits[token] = l
	}
	return l
}

// check returns an error when the token mustn't be used now. Polling also
// stops once only the reserve is left.
func (s *Shared) check(token string, polling bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.limitFor(token)
	now := s.now()
	if now.Before(l.blocked) {
		return &RateLimitError{Until: l.blocked}
	}
	if polling && l.known && l.remaining < pollReserve && now.Before(l.reset) {
		return &RateLimitError{Until: l.reset, Reserve: true}
	}
	return nil
}

// observe records what a response says about the token's limit, and returns
// an error if GitHub refused the request for it.
func (s *Shared) observe(token string, resp *http.Response, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.limitFor(token)
	now := s.now()
	if v, err := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining")); err == nil {
		l.remaining, l.known = v, true
	}
	if v, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		l.reset = time.Unix(v, 0)
	}
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return nil
	}
	var until time.Time
	if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
		until = now.Add(time.Duration(secs) * time.Second)
	} else if resp.Header.Get("X-RateLimit-Remaining") == "0" && l.reset.After(now) {
		until = l.reset
	} else if m := strings.ToLower(message); strings.Contains(m, "rate limit") || strings.Contains(m, "abuse") {
		until = now.Add(time.Minute) // GitHub asks for at least a minute after a secondary limit.
	} else {
		return nil // A 403 for permissions, not limits.
	}
	l.blocked = until
	return &RateLimitError{Until: until}
}

// lookup returns the stored answer for a request, if any.
func (s *Shared) lookup(key string) (etag string, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cache[key]
	if c == nil {
		return "", nil
	}
	c.used = s.now()
	return c.etag, c.body
}

func (s *Shared) store(key, etag string, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache[key] = &cached{etag: etag, body: body, used: s.now()}
	if len(s.cache) <= maxCached {
		return
	}
	// Drop the half used longest ago.
	keys := make([]string, 0, len(s.cache))
	for k := range s.cache {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int { return s.cache[a].used.Compare(s.cache[b].used) })
	for _, k := range keys[:len(keys)/2] {
		delete(s.cache, k)
	}
}
