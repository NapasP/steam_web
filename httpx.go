package steam

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	CommunityBaseURL = "https://steamcommunity.com"

	maxResponseBody = 32 << 20 // inventories with descriptions can be large; never read unbounded bodies
)

// httpClient returns the session client (a zero Session still works for public endpoints).
func (session *Session) httpClient() *http.Client {
	if session != nil && session.client != nil {
		return session.client
	}
	return http.DefaultClient
}

// doRaw sends req and returns the response with its body fully read (bounded). Non-2xx statuses are NOT
// turned into errors here, so callers can inspect special bodies (e.g. 403 "null" = private inventory).
func (session *Session) doRaw(req *http.Request) (*http.Response, []byte, error) {
	resp, err := session.httpClient().Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return resp, nil, err
	}
	return resp, body, nil
}

// webAPIGet calls https://api.steampowered.com/{iface}/{method}/v{version}/ with the session's access token
// (or the Web API key when no token is set) and decodes {"response": ...} into out.
// Mirrors tradeoffer-manager's _apiCall: x-eresult != 1 is an error, except the known fake "2" with a body.
func (session *Session) webAPIGet(ctx context.Context, op, iface, method string, version int, params url.Values, out any) error {
	if params == nil {
		params = url.Values{}
	}
	switch {
	case session.accessToken != "":
		params.Set("access_token", session.accessToken)
	case session.apiKey != "":
		params.Set("key", session.apiKey)
	default:
		return &SteamError{Op: op, Cause: CauseNotLoggedIn, Message: "no access token or API key"}
	}
	u := APIBaseUrl + "/" + iface + "/" + method + "/v" + strconv.Itoa(version) + "/?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, body, err := session.doRaw(req)
	if err != nil {
		return wrapf(op, err)
	}
	if resp.StatusCode != http.StatusOK {
		se := httpError(op, resp, body)
		if resp.StatusCode == http.StatusForbidden && strings.Contains(string(body), "Access is denied") {
			se.Cause = CauseNotLoggedIn
		}
		return se
	}
	if xe := resp.Header.Get("X-Eresult"); xe != "" && xe != "1" {
		n, _ := strconv.Atoi(xe)
		// Steam is known to send a fake Fail (2) with a perfectly good body.
		if !(n == int(EResultFail) && hasJSONContent(body)) {
			return &SteamError{Op: op, EResult: EResult(n), Message: resp.Header.Get("X-Error_message")}
		}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &SteamError{Op: op, Message: "malformed JSON: " + err.Error()}
	}
	return nil
}

// hasJSONContent reports a JSON object with more than one key, or a non-empty "response" object.
func hasJSONContent(body []byte) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return false
	}
	if len(m) > 1 {
		return true
	}
	if r, ok := m["response"]; ok {
		var inner map[string]json.RawMessage
		return json.Unmarshal(r, &inner) == nil && len(inner) > 0
	}
	return false
}

// RetryPolicy configures Retry: exponential backoff with full jitter, honouring Retry-After.
type RetryPolicy struct {
	Attempts  int           // total attempts (>= 1)
	BaseDelay time.Duration // delay before the 2nd attempt (doubles each time)
	MaxDelay  time.Duration // cap for a single wait (also caps Retry-After)
}

// DefaultRetryPolicy is a conservative policy for Steam web endpoints.
var DefaultRetryPolicy = RetryPolicy{Attempts: 3, BaseDelay: 2 * time.Second, MaxDelay: 30 * time.Second}

// Retry runs fn until it succeeds, returns a non-temporary error (see IsTemporary) or attempts run out.
// This package never retries on its own; callers opt in explicitly.
func Retry(ctx context.Context, p RetryPolicy, fn func(ctx context.Context) error) error {
	if p.Attempts < 1 {
		p.Attempts = 1
	}
	if p.BaseDelay <= 0 {
		p.BaseDelay = time.Second
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = 30 * time.Second
	}
	var err error
	for i := 0; i < p.Attempts; i++ {
		if err = fn(ctx); err == nil || !IsTemporary(err) || i == p.Attempts-1 {
			return err
		}
		d := BackoffDelay(p, i)
		if ra := RetryAfterOf(err); ra > d {
			d = ra
		}
		if d > p.MaxDelay {
			d = p.MaxDelay
		}
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return errors.Join(err, ctx.Err())
		case <-t.C:
		}
	}
	return err
}

// BackoffDelay is the jittered delay after the attempt-th failure (0-based): uniform in [d/2, d],
// d = BaseDelay * 2^attempt capped at MaxDelay.
func BackoffDelay(p RetryPolicy, attempt int) time.Duration {
	d := p.BaseDelay
	for i := 0; i < attempt && d < p.MaxDelay; i++ {
		d *= 2
	}
	if p.MaxDelay > 0 && d > p.MaxDelay {
		d = p.MaxDelay
	}
	if d <= 1 {
		return d
	}
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(half)+1))
}
