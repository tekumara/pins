// Package pinboard is a minimal client for the Pinboard v1 API.
package pinboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the production Pinboard API.
const DefaultBaseURL = "https://api.pinboard.in/v1"

// ErrRateLimited means Pinboard refused the call as too frequent.
// The API allows posts/all once every five minutes.
var ErrRateLimited = errors.New("pinboard rate limit hit: posts/all allows one call every five minutes; try again later")

// Post is one bookmark as returned by posts/all with format=json.
type Post struct {
	Href        string `json:"href"`
	Description string `json:"description"`
	Extended    string `json:"extended"`
	Meta        string `json:"meta"`
	Hash        string `json:"hash"`
	Time        string `json:"time"`   // UTC, e.g. 2010-12-11T19:48:02Z
	Shared      string `json:"shared"` // "yes" or "no"
	ToRead      string `json:"toread"` // "yes" or "no"
	Tags        string `json:"tags"`   // space-separated
}

// APIError is an error reported by Pinboard, either as an HTTP status or a JSON result.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("pinboard API error (HTTP %d): %s", e.Status, e.Message)
}

// Client calls the Pinboard API with an API token ("username:TOKEN").
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewClient returns a Client for the production API.
func NewClient(token string) *Client {
	return &Client{
		BaseURL: DefaultBaseURL,
		Token:   token,
		// A full-account posts/all response can be large, so allow a long read.
		HTTP: &http.Client{Timeout: 10 * time.Minute},
	}
}

// AllPosts makes one posts/all request and calls fn for each post as it is decoded.
// fromdt, if non-empty, limits results to posts created after that UTC time
// (format 2006-01-02T15:04:05Z). The response is streamed, so the full list
// is never held in memory.
func (c *Client) AllPosts(ctx context.Context, fromdt string, fn func(Post) error) error {
	q := url.Values{}
	q.Set("format", "json")
	q.Set("auth_token", c.Token)
	if fromdt != "" {
		q.Set("fromdt", fromdt)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/posts/all?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return redact(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(body))}
	}

	dec := json.NewDecoder(resp.Body)
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}
	switch tok {
	case json.Delim('{'):
		// Pinboard reports some failures as a JSON object such as {"result_code": "..."}.
		return apiErrorFromObject(dec, resp.StatusCode)
	case json.Delim('['):
		// Expected: stream the array one post at a time.
	default:
		return fmt.Errorf("unexpected response: %v", tok)
	}

	for dec.More() {
		var p Post
		if err := dec.Decode(&p); err != nil {
			return fmt.Errorf("decoding post: %w", err)
		}
		if err := fn(p); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil { // closing ']'
		return fmt.Errorf("reading response end: %w", err)
	}
	return nil
}

// apiErrorFromObject reads the remainder of a JSON object and returns it as an APIError.
// The caller has already consumed the opening '{'.
func apiErrorFromObject(dec *json.Decoder, status int) error {
	fields := map[string]json.RawMessage{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("reading error response: %w", err)
		}
		key, _ := keyTok.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return fmt.Errorf("reading error response: %w", err)
		}
		fields[key] = v
	}

	msg := ""
	if rc, ok := fields["result_code"]; ok {
		var s string
		if json.Unmarshal(rc, &s) == nil {
			msg = s
		} else {
			msg = string(rc)
		}
	} else {
		raw, _ := json.Marshal(fields)
		msg = string(raw)
	}
	return &APIError{Status: status, Message: msg}
}

// redact drops the request URL from a transport error, because it contains the auth token.
func redact(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s request failed: %w", ue.Op, ue.Err)
	}
	return err
}

// FormatFromDT converts a user-supplied date or time to Pinboard's fromdt format (UTC).
// Accepted forms: 2006-01-02, 2006-01-02T15:04[:05], 2006-01-02 15:04[:05], and RFC 3339.
// Times without a zone are read as UTC, and times with an offset are converted to UTC.
func FormatFromDT(s string) (string, error) {
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339), nil
		}
	}
	return "", fmt.Errorf("unrecognised date %q: use YYYY-MM-DD or YYYY-MM-DDTHH:MM:SSZ", s)
}
