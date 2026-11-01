package pinboard

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const twoPosts = `[
  {"href":"https://example.com/","description":"An Example","extended":"example description","meta":"m1","hash":"h1","time":"2019-11-06T22:50:31Z","shared":"yes","toread":"no","tags":"fish sheep goats"},
  {"href":"https://example.com/2","description":"Example 2","extended":"","meta":"m2","hash":"h2","time":"2018-11-06T22:50:31Z","shared":"no","toread":"yes","tags":"elephants goats lizards"}
]`

func serve(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, Token: "user:SECRET", HTTP: srv.Client()}
}

func collect(t *testing.T, c *Client, fromdt string) ([]Post, error) {
	t.Helper()
	var got []Post
	err := c.AllPosts(context.Background(), fromdt, func(p Post) error {
		got = append(got, p)
		return nil
	})
	return got, err
}

func TestAllPostsStreamsPosts(t *testing.T) {
	var gotQuery url.Values
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		if r.URL.Path != "/posts/all" {
			t.Errorf("path = %s, want /posts/all", r.URL.Path)
		}
		w.Write([]byte(twoPosts))
	})

	posts, err := collect(t, c, "2019-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 2 || posts[0].Hash != "h1" || posts[1].Tags != "elephants goats lizards" {
		t.Fatalf("unexpected posts: %+v", posts)
	}
	if gotQuery.Get("format") != "json" || gotQuery.Get("auth_token") != "user:SECRET" {
		t.Errorf("missing format/auth_token: %v", gotQuery)
	}
	if gotQuery.Get("fromdt") != "2019-01-01T00:00:00Z" {
		t.Errorf("fromdt = %q", gotQuery.Get("fromdt"))
	}
}

func TestAllPostsOmitsFromDTWhenEmpty(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["fromdt"]; ok {
			t.Error("fromdt sent when empty")
		}
		w.Write([]byte("[]"))
	})
	if _, err := collect(t, c, ""); err != nil {
		t.Fatal(err)
	}
}

func TestAllPostsResultCodeIsError(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result_code":"item not found"}`))
	})
	_, err := collect(t, c, "")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Message != "item not found" {
		t.Fatalf("err = %v, want APIError with message 'item not found'", err)
	}
}

func TestAllPostsHTTPErrorIsError(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	_, err := collect(t, c, "")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("err = %v, want APIError with status 403", err)
	}
}

func TestAllPostsRateLimited(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	if _, err := collect(t, c, ""); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}

func TestAllPostsCallbackErrorStopsStream(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(twoPosts))
	})
	stop := errors.New("stop")
	calls := 0
	err := c.AllPosts(context.Background(), "", func(Post) error {
		calls++
		return stop
	})
	if !errors.Is(err, stop) || calls != 1 {
		t.Fatalf("err = %v, calls = %d; want stop error after 1 call", err, calls)
	}
}

func TestTransportErrorDoesNotLeakToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // connections to a closed server fail

	c := &Client{BaseURL: url, Token: "user:SECRET", HTTP: &http.Client{}}
	_, err := collect(t, c, "")
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "auth_token") {
		t.Fatalf("error leaks credentials: %v", err)
	}
}

func TestFormatFromDT(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "2019-11-06", want: "2019-11-06T00:00:00Z"},
		{in: "2019-11-06T22:50:31Z", want: "2019-11-06T22:50:31Z"},
		{in: "2019-11-06 22:50:31", want: "2019-11-06T22:50:31Z"},
		{in: "2019-11-06T22:50", want: "2019-11-06T22:50:00Z"},
		{in: "2019-11-07T00:50:31+02:00", want: "2019-11-06T22:50:31Z"},
		{in: "last tuesday", wantErr: true},
		{in: "2019-13-01", wantErr: true},
	}
	for _, tt := range tests {
		got, err := FormatFromDT(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("FormatFromDT(%q) = %q, want error", tt.in, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("FormatFromDT(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}
