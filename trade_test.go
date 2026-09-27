package steam

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestParseTradeURL(t *testing.T) {
	ok := []struct {
		in    string
		acc   uint32
		token string
	}{
		{"https://steamcommunity.com/tradeoffer/new/?partner=39734273&token=AbCd_12-", 39734273, "AbCd_12-"},
		{" http://www.steamcommunity.com/tradeoffer/new?token=abcdef12&partner=1 ", 1, "abcdef12"},
	}
	for _, c := range ok {
		got, err := ParseTradeURL(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got.AccountID != c.acc || got.Token != c.token {
			t.Fatalf("%q: got %+v", c.in, got)
		}
	}
	bad := []string{
		"", "steamcommunity.com/tradeoffer/new/?partner=1&token=abcdef12",
		"https://steamcommunity.com.evil.io/tradeoffer/new/?partner=1&token=abcdef12",
		"https://steamcommunity.com/tradeoffer/new/?partner=abc&token=abcdef12",
		"https://steamcommunity.com/tradeoffer/new/?partner=1",
		"https://steamcommunity.com/tradeoffer/new/?partner=1&token=a<b>cdef",
		"https://steamcommunity.com/tradeoffer/new/?partner=4294967296&token=abcdef12",
		"https://steamcommunity.com/tradeoffer/123/?partner=1&token=abcdef12",
		"javascript:alert(1)",
	}
	for _, in := range bad {
		if _, err := ParseTradeURL(in); !errors.Is(err, ErrInvalidTradeURL) {
			t.Fatalf("%q: want ErrInvalidTradeURL, got %v", in, err)
		}
	}
	tu, _ := ParseTradeURL(ok[0].in)
	if tu.SteamID() != SteamID(76561197960265728+39734273) {
		t.Fatalf("steamid %d", tu.SteamID())
	}
	if tu.String() != "https://steamcommunity.com/tradeoffer/new/?partner=39734273&token=AbCd_12-" {
		t.Fatal(tu.String())
	}
}

func TestSendOfferPayload(t *testing.T) {
	var form url.Values
	var referer string
	s := newTestSession(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tradeoffer/new/send" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		b, _ := io.ReadAll(r.Body)
		form, _ = url.ParseQuery(string(b))
		referer = r.Header.Get("Referer")
		w.Write([]byte(`{"tradeofferid":"6543210987","needs_mobile_confirmation":false}`))
	}))
	partner := SteamID(76561198000000002)
	res, err := s.SendOffer(context.Background(), OfferRequest{
		Partner: partner, Token: "tok_EN12", Message: "Napas NP-ABC123",
		ItemsToReceive: []TradeItem{{AppID: 730, ContextID: 2, AssetID: 111}, {AppID: 730, ContextID: 2, AssetID: 222, Amount: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != 6543210987 || res.State != Active || res.NeedsMobileConfirmation {
		t.Fatalf("result %+v", res)
	}
	if form.Get("sessionid") != "test-session-id" || form.Get("partner") != "76561198000000002" || form.Get("tradeoffermessage") != "Napas NP-ABC123" {
		t.Fatalf("form %v", form)
	}
	var params map[string]string
	if err := json.Unmarshal([]byte(form.Get("trade_offer_create_params")), &params); err != nil || params["trade_offer_access_token"] != "tok_EN12" {
		t.Fatalf("create params %q", form.Get("trade_offer_create_params"))
	}
	var offer struct {
		NewVersion bool `json:"newversion"`
		Version    int  `json:"version"`
		Me         struct {
			Assets []map[string]any `json:"assets"`
		} `json:"me"`
		Them struct {
			Assets []map[string]any `json:"assets"`
		} `json:"them"`
	}
	if err := json.Unmarshal([]byte(form.Get("json_tradeoffer")), &offer); err != nil {
		t.Fatal(err)
	}
	if !offer.NewVersion || offer.Version != 3 || len(offer.Me.Assets) != 0 || len(offer.Them.Assets) != 2 {
		t.Fatalf("offer json %s", form.Get("json_tradeoffer"))
	}
	a := offer.Them.Assets[0]
	if a["assetid"] != "111" || a["contextid"] != "2" || a["appid"] != float64(730) || a["amount"] != float64(1) {
		t.Fatalf("asset %v", a)
	}
	if !strings.Contains(referer, "partner=39734274") || !strings.Contains(referer, "token=tok_EN12") {
		t.Fatalf("referer %q", referer)
	}
}

func TestSendOfferStrError(t *testing.T) {
	s := newTestSession(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"strError":"There was an error sending your trade offer.  Please try again later. (26)"}`))
	}))
	_, err := s.SendOffer(context.Background(), OfferRequest{Partner: 76561198000000002, ItemsToReceive: []TradeItem{{AppID: 730, ContextID: 2, AssetID: 1}}})
	var se *SteamError
	if !errors.As(err, &se) || se.EResult != EResultRevoked || se.HTTPStatus != 500 {
		t.Fatalf("got %#v", err)
	}

	s2 := newTestSession(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"strError":"You cannot trade with Foo because they have a trade ban."}`))
	}))
	_, err = s2.SendOffer(context.Background(), OfferRequest{Partner: 76561198000000002, ItemsToReceive: []TradeItem{{AppID: 730, ContextID: 2, AssetID: 1}}})
	if !errors.As(err, &se) || se.Cause != CauseTradeBan {
		t.Fatalf("got %#v", err)
	}

	s3 := newTestSession(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	_, err = s3.SendOffer(context.Background(), OfferRequest{Partner: 76561198000000002, ItemsToReceive: []TradeItem{{AppID: 730, ContextID: 2, AssetID: 1}}})
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("want ErrNotLoggedIn, got %v", err)
	}

	if _, err := s3.SendOffer(context.Background(), OfferRequest{Partner: 1}); err == nil {
		t.Fatal("empty offer must fail")
	}
}

func TestSendOfferNeedsConfirmation(t *testing.T) {
	s := newTestSession(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tradeofferid":"77","needs_mobile_confirmation":true}`))
	}))
	res, err := s.SendOffer(context.Background(), OfferRequest{Partner: 76561198000000002, ItemsToGive: []TradeItem{{AppID: 730, ContextID: 2, AssetID: 1}}})
	if err != nil || res.State != CreatedNeedsConfirmation || !res.NeedsMobileConfirmation {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestGetOfferAndStates(t *testing.T) {
	s := newTestSession(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/IEconService/GetTradeOffer/v1/" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("access_token") != "test-access-token" {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte("<html>Access is denied</html>"))
			return
		}
		switch r.URL.Query().Get("tradeofferid") {
		case "5":
			w.Header().Set("X-Eresult", "1")
			w.Write([]byte(`{"response":{"offer":{"tradeofferid":"5","accountid_other":42,"trade_offer_state":3,"tradeid":"999","is_our_offer":true,"items_to_receive":[{"appid":730,"contextid":"2","assetid":"1","classid":"2","instanceid":"0","amount":"1","missing":true}]}}}`))
		case "6":
			w.Header().Set("X-Eresult", "1")
			w.Write([]byte(`{"response":{}}`))
		default:
			w.Header().Set("X-Eresult", "84")
			w.Write([]byte(`{"response":{}}`))
		}
	}))
	o, _, err := s.GetOffer(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if o.OfferState() != Accepted || o.ReceiptID != 999 || !o.IsOurOffer || len(o.RecvItems) != 1 || !o.RecvItems[0].Missing {
		t.Fatalf("offer %+v", o)
	}
	if !o.OfferState().IsFinal() || o.OfferState().IsOpen() || o.OfferState().String() != "Accepted" {
		t.Fatal("state helpers")
	}
	if _, _, err := s.GetOffer(context.Background(), 6); !errors.Is(err, ErrOfferNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
	_, _, err = s.GetOffer(context.Background(), 7)
	if !IsRateLimited(err) || !IsTemporary(err) {
		t.Fatalf("want rate limited, got %v", err)
	}
	// legacy API: an empty response is an error, not a nil offer
	if _, err := s.GetTradeOffer(6); !errors.Is(err, ErrOfferNotFound) {
		t.Fatalf("legacy GetTradeOffer: %v", err)
	}
	s.accessToken = "expired"
	if _, _, err := s.GetOffer(context.Background(), 5); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("want not logged in, got %v", err)
	}
}

func TestGetTradeStatus(t *testing.T) {
	status := 3
	s := newTestSession(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/IEconService/GetTradeStatus/v1/" || r.URL.Query().Get("tradeid") == "" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("tradeid") == "404" {
			w.Write([]byte(`{"response":{"trades":[]}}`))
			return
		}
		w.Header().Set("X-Eresult", "1")
		w.Write([]byte(`{"response":{"trades":[{"tradeid":"3812","steamid_other":"76561198000000002","time_init":1700000000,"status":` +
			itoa(status) + `,"assets_received":[{"appid":730,"contextid":"2","assetid":"111","amount":"1","classid":"5","instanceid":"0","new_assetid":"98765","new_contextid":"2"}]}]}}`))
	}))
	ts, err := s.GetTradeStatus(context.Background(), 3812)
	if err != nil {
		t.Fatal(err)
	}
	if !ts.Status.IsCompleted() || ts.Status.IsRolledBack() || len(ts.AssetsReceived) != 1 || ts.AssetsReceived[0].NewAssetID != 98765 || ts.SteamIDOther != 76561198000000002 {
		t.Fatalf("%+v", ts)
	}
	for _, st := range []int{4, 5, 6, 7, 8, 9, 11, 12, 99} {
		status = st
		ts, err = s.GetTradeStatus(context.Background(), 3812)
		if err != nil || !ts.Status.IsRolledBack() {
			t.Fatalf("status %d: rolled back expected (%v)", st, err)
		}
	}
	for _, st := range []int{0, 1, 2, 10} {
		status = st
		ts, _ = s.GetTradeStatus(context.Background(), 3812)
		if !ts.Status.IsPending() || ts.Status.IsRolledBack() {
			t.Fatalf("status %d: pending expected", st)
		}
	}
	if _, err := s.GetTradeStatus(context.Background(), 404); !errors.Is(err, ErrTradeNotFound) {
		t.Fatalf("want ErrTradeNotFound, got %v", err)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestTradeHoldAndPartnerDetails(t *testing.T) {
	s := newTestSession(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/IEconService/GetTradeHoldDurations/v1/":
			if r.URL.Query().Get("trade_offer_access_token") != "goodtoken" {
				w.Header().Set("X-Eresult", "15")
				w.Write([]byte(`{"response":{}}`))
				return
			}
			w.Write([]byte(`{"response":{"my_escrow":{"escrow_end_duration_seconds":0},"their_escrow":{"escrow_end_duration_seconds":1296000},"both_escrow":{"escrow_end_duration_seconds":1296000}}}`))
		case "/tradeoffer/new/":
			if r.URL.Query().Get("token") != "goodtoken" {
				w.Write([]byte(`<html><div id="error_msg">
					This Trade URL is no longer valid for sending a trade offer to Foo.
				</div></html>`))
				return
			}
			w.Write([]byte(`<script>var g_daysMyEscrow = 0;
				var g_daysTheirEscrow = 15;
				var g_bTradePartnerProbation = false;</script>`))
		default:
			http.NotFound(w, r)
		}
	}))
	h, err := s.GetTradeHoldDurations(context.Background(), 76561198000000002, "goodtoken")
	if err != nil || h.TheirEscrow != 15*24*time.Hour || h.MyEscrow != 0 {
		t.Fatalf("%+v %v", h, err)
	}
	var se *SteamError
	if _, err := s.GetTradeHoldDurations(context.Background(), 76561198000000002, "bad"); !errors.As(err, &se) || se.EResult != EResultAccessDenied {
		t.Fatalf("want AccessDenied, got %v", err)
	}
	d, err := s.GetPartnerDetails(context.Background(), &TradeURL{AccountID: 2, Token: "goodtoken"})
	if err != nil || d.TheirEscrowDays != 15 || d.MyEscrowDays != 0 || d.Probation {
		t.Fatalf("%+v %v", d, err)
	}
	_, err = s.GetPartnerDetails(context.Background(), &TradeURL{AccountID: 2, Token: "badtoken"})
	if !errors.As(err, &se) || se.Cause != CauseInvalidTradeURL || !strings.Contains(se.Message, "no longer valid") {
		t.Fatalf("want invalid trade url, got %v", err)
	}
}

func TestCancelOffer(t *testing.T) {
	var path, sid string
	s := newTestSession(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		q, _ := url.ParseQuery(string(b))
		sid = q.Get("sessionid")
		if strings.HasSuffix(path, "/decline") {
			w.Write([]byte(`{"strError":"Offer is not active (11)"}`))
			return
		}
		w.Write([]byte(`{"tradeofferid":"55"}`))
	}))
	if err := s.CancelOffer(context.Background(), 55); err != nil || path != "/tradeoffer/55/cancel" || sid != "test-session-id" {
		t.Fatalf("%v %s %s", err, path, sid)
	}
	var se *SteamError
	if err := s.DeclineOffer(context.Background(), 56); !errors.As(err, &se) || se.EResult != EResultInvalidState {
		t.Fatalf("got %v", err)
	}
}

func TestGetOffersCursor(t *testing.T) {
	calls := 0
	s := newTestSession(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("cursor") == "0" {
			w.Write([]byte(`{"response":{"trade_offers_sent":[{"tradeofferid":"1","trade_offer_state":2}],"next_cursor":100}}`))
			return
		}
		w.Write([]byte(`{"response":{"trade_offers_sent":[{"tradeofferid":"2","trade_offer_state":7}],"trade_offers_received":[{"tradeofferid":"3","trade_offer_state":2}]}}`))
	}))
	res, err := s.GetOffers(context.Background(), GetOffersOptions{Sent: true, Received: true, ActiveOnly: true, HistoricalCutoff: time.Unix(1700000000, 0)})
	if err != nil || calls != 2 || len(res.SentOffers) != 2 || len(res.ReceivedOffers) != 1 {
		t.Fatalf("%v calls=%d %+v", err, calls, res)
	}
}

func TestAcceptConfirmationForObject(t *testing.T) {
	var answered url.Values
	s := newTestSession(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mobileconf/getlist":
			if r.URL.Query().Get("a") != "76561198000000001" || r.URL.Query().Get("k") == "" {
				w.Write([]byte(`{"success":false,"needauth":true}`))
				return
			}
			w.Write([]byte(`{"success":true,"conf":[{"type":2,"id":"11","nonce":"n11","creator_id":"900","creation_time":1},{"type":2,"id":"12","nonce":"n12","creator_id":"901","creation_time":1}]}`))
		case "/mobileconf/ajaxop":
			answered = r.URL.Query()
			w.Write([]byte(`{"success":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	secret := "aGVsbG8gd29ybGQ=" // dummy base64, not a real identity secret
	if err := s.AcceptConfirmationForObject(secret, 901, 1700000000); err != nil {
		t.Fatal(err)
	}
	if answered.Get("cid") != "12" || answered.Get("ck") != "n12" || answered.Get("op") != "allow" {
		t.Fatalf("answered %v", answered)
	}
	if err := s.AcceptConfirmationForObject(secret, 1234, 1700000000); !errors.Is(err, ErrConfirmationNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
	s.oauth.SteamID = 1
	if err := s.AcceptConfirmationForObject(secret, 901, 1700000000); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("want needauth -> not logged in, got %v", err)
	}
}
