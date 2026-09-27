package steam

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseMarketPrice(t *testing.T) {
	cases := map[string]float64{
		"$1,234.56":     1234.56,
		"$0.03":         0.03,
		"12,34₴":        12.34,
		"1 234,56₴":     1234.56,
		"1 234,56₴":     1234.56,
		"1.234,56€":     1234.56,
		"1.234,56 pуб.": 1234.56,
		"CDN$ 3.10":     3.10,
		"1'234.50 CHF":  1234.50,
		"12,5":          12.5,
		"1,000":         1000,
		"₴1,234.56":     1234.56,
		"205₴":          205,
	}
	for in, want := range cases {
		got, err := ParseMarketPrice(in)
		if err != nil || got < want-1e-9 || got > want+1e-9 {
			t.Errorf("%q: got %v %v, want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "₴", "--"} {
		if _, err := ParseMarketPrice(in); err == nil {
			t.Errorf("%q: want error", in)
		}
	}
}

func TestGetPriceOverview(t *testing.T) {
	s := newTestSession(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/market/priceoverview/" || q.Get("appid") != "730" {
			http.NotFound(w, r)
			return
		}
		switch q.Get("market_hash_name") {
		case "Revolution Case":
			if q.Get("currency") == "18" {
				w.Write([]byte(`{"success":true,"lowest_price":"17,05₴","volume":"12,345","median_price":"16,80₴"}`))
			} else {
				w.Write([]byte(`{"success":true,"lowest_price":"$0.41","volume":"12,345","median_price":"$0.40"}`))
			}
		case "Median Only":
			w.Write([]byte(`{"success":true,"median_price":"$5.10"}`))
		case "Unknown":
			w.WriteHeader(500)
			w.Write([]byte(`{"success":false}`))
		case "Throttle":
			w.WriteHeader(429)
		}
	}))
	ctx := context.Background()
	p, err := s.GetPriceOverview(ctx, 730, 18, "Revolution Case")
	if err != nil || p.Lowest != 17.05 || p.Median != 16.80 || p.Volume != 12345 || p.Best() != 17.05 {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = s.GetPriceOverview(ctx, 730, 1, "Median Only")
	if err != nil || p.Best() != 5.10 {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err = s.GetPriceOverview(ctx, 730, 1, "Unknown"); !errors.Is(err, ErrNoPrice) {
		t.Fatalf("unknown: %v", err)
	}
	if _, err = s.GetPriceOverview(ctx, 730, 1, "Throttle"); !IsRateLimited(err) {
		t.Fatalf("throttle: %v", err)
	}
}

func TestRetry(t *testing.T) {
	p := RetryPolicy{Attempts: 4, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}
	var n int32
	err := Retry(context.Background(), p, func(context.Context) error {
		if atomic.AddInt32(&n, 1) < 3 {
			return &SteamError{HTTPStatus: 429}
		}
		return nil
	})
	if err != nil || n != 3 {
		t.Fatalf("retry: %v n=%d", err, n)
	}
	n = 0
	perm := errors.New("permanent")
	if err := Retry(context.Background(), p, func(context.Context) error { atomic.AddInt32(&n, 1); return perm }); err != perm || n != 1 {
		t.Fatalf("permanent: %v n=%d", err, n)
	}
	n = 0
	err = Retry(context.Background(), p, func(context.Context) error { atomic.AddInt32(&n, 1); return &SteamError{HTTPStatus: 503} })
	if !IsTemporary(err) || n != 4 {
		t.Fatalf("exhausted: %v n=%d", err, n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = Retry(ctx, RetryPolicy{Attempts: 3, BaseDelay: time.Hour, MaxDelay: time.Hour}, func(context.Context) error { return &SteamError{HTTPStatus: 500} })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
	for i := 0; i < 10; i++ {
		d := BackoffDelay(RetryPolicy{BaseDelay: time.Second, MaxDelay: 8 * time.Second}, i)
		if d < 0 || d > 8*time.Second {
			t.Fatalf("delay %v", d)
		}
	}
}
