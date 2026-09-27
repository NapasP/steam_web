package steam

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ---------- trade offer state ----------

var tradeOfferStateNames = map[TradeOfferState]string{
	Invalid: "Invalid", Active: "Active", Accepted: "Accepted", Countered: "Countered", Expired: "Expired",
	Canceled: "Canceled", Declined: "Declined", InvalidItems: "InvalidItems",
	CreatedNeedsConfirmation: "CreatedNeedsConfirmation", CanceledBySecondFactor: "CanceledBySecondFactor",
	InEscrow: "InEscrow",
}

func (s TradeOfferState) String() string {
	if n, ok := tradeOfferStateNames[s]; ok {
		return n
	}
	return "TradeOfferState(" + strconv.Itoa(int(s)) + ")"
}

// IsOpen reports states in which the offer can still change (Active, CreatedNeedsConfirmation, InEscrow).
func (s TradeOfferState) IsOpen() bool {
	return s == Active || s == CreatedNeedsConfirmation || s == InEscrow
}

// IsFinal reports states that will never change again.
func (s TradeOfferState) IsFinal() bool {
	switch s {
	case Accepted, Countered, Expired, Canceled, Declined, InvalidItems, CanceledBySecondFactor, Invalid:
		return true
	}
	return false
}

// OfferState returns the typed state of the offer.
func (offer *TradeOffer) OfferState() TradeOfferState { return TradeOfferState(offer.State) }

// ---------- trade status (IEconService/GetTradeStatus) ----------

// ETradeStatus is the status of a completed exchange (GetTradeStatus / GetTradeHistory).
type ETradeStatus int

const (
	TradeStatusInit                     ETradeStatus = 0  // accepted/confirmed, no work done yet
	TradeStatusPreCommitted             ETradeStatus = 1  // about to commit
	TradeStatusCommitted                ETradeStatus = 2  // items exchanged
	TradeStatusComplete                 ETradeStatus = 3  // all work finished
	TradeStatusFailed                   ETradeStatus = 4  // failed and rolled back
	TradeStatusPartialSupportRollback   ETradeStatus = 5  // support rolled back one side
	TradeStatusFullSupportRollback      ETradeStatus = 6  // support rolled back both sides
	TradeStatusSupportRollbackSelective ETradeStatus = 7  // support rolled back some items
	TradeStatusRollbackFailed           ETradeStatus = 8  // rollback in progress / incomplete
	TradeStatusRollbackAbandoned        ETradeStatus = 9  // rollback gave up
	TradeStatusInEscrow                 ETradeStatus = 10 // on hold
	TradeStatusEscrowRollback           ETradeStatus = 11 // on-hold trade rolled back
)

var tradeStatusNames = map[ETradeStatus]string{
	0: "Init", 1: "PreCommitted", 2: "Committed", 3: "Complete", 4: "Failed", 5: "PartialSupportRollback",
	6: "FullSupportRollback", 7: "SupportRollback_Selective", 8: "RollbackFailed", 9: "RollbackAbandoned",
	10: "InEscrow", 11: "EscrowRollback",
}

func (s ETradeStatus) String() string {
	if n, ok := tradeStatusNames[s]; ok {
		return n
	}
	return "ETradeStatus(" + strconv.Itoa(int(s)) + ")"
}

// IsCompleted: the items were exchanged and nothing has been reverted.
func (s ETradeStatus) IsCompleted() bool { return s == TradeStatusComplete }

// IsPending: the exchange is still in flight (Init / PreCommitted / Committed / InEscrow).
func (s ETradeStatus) IsPending() bool {
	return s == TradeStatusInit || s == TradeStatusPreCommitted || s == TradeStatusCommitted || s == TradeStatusInEscrow
}

// IsRolledBack: the exchange failed or was (partly) reverted — including any status newer than this
// table (Valve adds reversal states over time, e.g. for CS2 trade protection). Treat as "not received".
func (s ETradeStatus) IsRolledBack() bool {
	return !s.IsCompleted() && !s.IsPending()
}

// TradeAsset is an item of a GetTradeStatus exchange. NewAssetID is the id the item got in the receiver's inventory.
type TradeAsset struct {
	AppID        uint32 `json:"appid"`
	ContextID    uint64 `json:"contextid,string"`
	AssetID      uint64 `json:"assetid,string"`
	Amount       uint64 `json:"amount,string"`
	ClassID      uint64 `json:"classid,string"`
	InstanceID   uint64 `json:"instanceid,string"`
	NewAssetID   uint64 `json:"new_assetid,string"`
	NewContextID uint64 `json:"new_contextid,string"`
}

// TradeStatus is one exchange returned by GetTradeStatus.
type TradeStatus struct {
	TradeID        uint64        `json:"tradeid,string"`
	SteamIDOther   SteamID       `json:"steamid_other,string"`
	TimeInit       int64         `json:"time_init"`
	TimeEscrowEnd  int64         `json:"time_escrow_end"`
	Status         ETradeStatus  `json:"status"`
	AssetsReceived []*TradeAsset `json:"assets_received"`
	AssetsGiven    []*TradeAsset `json:"assets_given"`
}

// GetTradeStatus loads the exchange details of an accepted offer (TradeOffer.ReceiptID = "tradeid"),
// like tradeoffer-manager's getExchangeDetails. Use it to detect rollbacks before crediting anything.
// Returns ErrTradeNotFound when Steam does not know the trade (yet).
func (session *Session) GetTradeStatus(ctx context.Context, tradeID uint64) (*TradeStatus, error) {
	const op = "GetTradeStatus"
	if tradeID == 0 {
		return nil, &SteamError{Op: op, Message: "trade id is 0"}
	}
	var out struct {
		Response struct {
			Trades []*TradeStatus `json:"trades"`
		} `json:"response"`
	}
	err := session.webAPIGet(ctx, op, "IEconService", "GetTradeStatus", 1, url.Values{
		"tradeid":          {strconv.FormatUint(tradeID, 10)},
		"get_descriptions": {"0"},
	}, &out)
	if err != nil {
		return nil, err
	}
	for _, t := range out.Response.Trades {
		if t != nil && t.TradeID == tradeID {
			return t, nil
		}
	}
	return nil, ErrTradeNotFound
}

// ---------- get offers (context-aware, error-checked) ----------

// GetOffer loads one offer by id (IEconService/GetTradeOffer). Returns ErrOfferNotFound for unknown ids.
// Unlike the legacy GetTradeOffer it checks HTTP status / x-eresult and never dereferences a missing body.
func (session *Session) GetOffer(ctx context.Context, id uint64) (*TradeOffer, []*EconItemDesc, error) {
	const op = "GetTradeOffer"
	var out struct {
		Response struct {
			Offer        *TradeOffer     `json:"offer"`
			Descriptions []*EconItemDesc `json:"descriptions"`
		} `json:"response"`
	}
	err := session.webAPIGet(ctx, op, "IEconService", "GetTradeOffer", 1, url.Values{
		"tradeofferid":     {strconv.FormatUint(id, 10)},
		"language":         {"english"},
		"get_descriptions": {"0"},
	}, &out)
	if err != nil {
		return nil, nil, err
	}
	if out.Response.Offer == nil || out.Response.Offer.ID == 0 {
		return nil, nil, ErrOfferNotFound
	}
	return out.Response.Offer, out.Response.Descriptions, nil
}

// GetOffersOptions selects what GetOffers returns.
type GetOffersOptions struct {
	Sent, Received   bool
	ActiveOnly       bool
	HistoricalOnly   bool
	Descriptions     bool
	HistoricalCutoff time.Time // with ActiveOnly: also return offers updated after this time
	Language         string
}

// GetOffers lists offers (IEconService/GetTradeOffers), following the cursor.
func (session *Session) GetOffers(ctx context.Context, o GetOffersOptions) (*TradeOfferResponse, error) {
	const op = "GetTradeOffers"
	b := func(v bool) string {
		if v {
			return "1"
		}
		return "0"
	}
	lang := o.Language
	if lang == "" {
		lang = "english"
	}
	all := &TradeOfferResponse{}
	cursor := 0
	for page := 0; page < 50; page++ {
		params := url.Values{
			"get_sent_offers":     {b(o.Sent)},
			"get_received_offers": {b(o.Received)},
			"active_only":         {b(o.ActiveOnly)},
			"historical_only":     {b(o.HistoricalOnly)},
			"get_descriptions":    {b(o.Descriptions)},
			"language":            {lang},
			"cursor":              {strconv.Itoa(cursor)},
		}
		if !o.HistoricalCutoff.IsZero() {
			params.Set("time_historical_cutoff", strconv.FormatInt(o.HistoricalCutoff.Unix(), 10))
		}
		var out APIResponse
		if err := session.webAPIGet(ctx, op, "IEconService", "GetTradeOffers", 1, params, &out); err != nil {
			return nil, err
		}
		if out.Inner == nil {
			return all, nil
		}
		all.SentOffers = append(all.SentOffers, out.Inner.SentOffers...)
		all.ReceivedOffers = append(all.ReceivedOffers, out.Inner.ReceivedOffers...)
		all.Descriptions = append(all.Descriptions, out.Inner.Descriptions...)
		if out.Inner.NextCursor == 0 || out.Inner.NextCursor == cursor {
			return all, nil
		}
		cursor = out.Inner.NextCursor
	}
	return all, nil
}

// ---------- trade URL ----------

// TradeURL is a parsed "Trade offer URL" (https://steamcommunity.com/tradeoffer/new/?partner=N&token=T).
type TradeURL struct {
	AccountID uint32
	Token     string
}

var tradeTokenExp = regexp.MustCompile(`^[A-Za-z0-9_-]{6,16}$`)

// ParseTradeURL validates and parses a trade offer URL. Only steamcommunity.com (http/https, optional www)
// with a numeric partner (32-bit account id) and a token of 6–16 [A-Za-z0-9_-] chars is accepted.
func ParseTradeURL(raw string) (*TradeURL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 300 {
		return nil, ErrInvalidTradeURL
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, ErrInvalidTradeURL
	}
	host := strings.ToLower(u.Hostname())
	if host != "steamcommunity.com" && host != "www.steamcommunity.com" {
		return nil, ErrInvalidTradeURL
	}
	if strings.TrimSuffix(u.Path, "/") != "/tradeoffer/new" {
		return nil, ErrInvalidTradeURL
	}
	q := u.Query()
	partner, err := strconv.ParseUint(q.Get("partner"), 10, 32)
	if err != nil || partner == 0 {
		return nil, ErrInvalidTradeURL
	}
	token := q.Get("token")
	if !tradeTokenExp.MatchString(token) {
		return nil, ErrInvalidTradeURL
	}
	return &TradeURL{AccountID: uint32(partner), Token: token}, nil
}

// SteamID returns the partner's SteamID64 (individual, public universe).
func (t *TradeURL) SteamID() SteamID {
	var sid SteamID
	sid.ParseDefaults(t.AccountID)
	return sid
}

// String renders the canonical URL.
func (t *TradeURL) String() string {
	return fmt.Sprintf("%s/tradeoffer/new/?partner=%d&token=%s", CommunityBaseURL, t.AccountID, t.Token)
}

// ---------- escrow / trade hold ----------

// TradeHoldDurations is IEconService/GetTradeHoldDurations: how long a trade between us and the target
// would be held (0 = instant). Their > 0 usually means no Steam Guard Mobile Authenticator (or < 7 days).
type TradeHoldDurations struct {
	MyEscrow    time.Duration
	TheirEscrow time.Duration
	BothEscrow  time.Duration
}

// GetTradeHoldDurations asks Steam for the trade hold with a partner (token = their trade URL token).
func (session *Session) GetTradeHoldDurations(ctx context.Context, sid SteamID, token string) (*TradeHoldDurations, error) {
	const op = "GetTradeHoldDurations"
	type dur struct {
		Seconds flexInt `json:"escrow_end_duration_seconds"`
	}
	var out struct {
		Response struct {
			My    *dur `json:"my_escrow"`
			Their *dur `json:"their_escrow"`
			Both  *dur `json:"both_escrow"`
		} `json:"response"`
	}
	params := url.Values{"steamid_target": {strconv.FormatUint(uint64(sid), 10)}}
	if token != "" {
		params.Set("trade_offer_access_token", token)
	}
	if err := session.webAPIGet(ctx, op, "IEconService", "GetTradeHoldDurations", 1, params, &out); err != nil {
		return nil, err
	}
	if out.Response.Their == nil && out.Response.Both == nil {
		return nil, &SteamError{Op: op, Message: "empty response"}
	}
	sec := func(d *dur) time.Duration {
		if d == nil {
			return 0
		}
		return time.Duration(d.Seconds) * time.Second
	}
	return &TradeHoldDurations{MyEscrow: sec(out.Response.My), TheirEscrow: sec(out.Response.Their), BothEscrow: sec(out.Response.Both)}, nil
}

// PartnerDetails is what the "new trade offer" page reveals about a partner (tradeoffer-manager getUserDetails).
type PartnerDetails struct {
	MyEscrowDays    int
	TheirEscrowDays int
	Probation       bool
}

var probationExp = regexp.MustCompile(`var g_bTradePartnerProbation = (true|false);`)

// GetPartnerDetails opens https://steamcommunity.com/tradeoffer/new/?partner=..&token=.. with the session.
// Steam renders an error page for invalid tokens, trade-banned or otherwise unavailable partners: that is
// returned as a *SteamError with Cause CauseInvalidTradeURL / CauseTargetCannotTrade / CauseTradeBan.
func (session *Session) GetPartnerDetails(ctx context.Context, t *TradeURL) (*PartnerDetails, error) {
	const op = "GetPartnerDetails"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CommunityBaseURL+"/tradeoffer/new/?"+url.Values{
		"partner": {strconv.FormatUint(uint64(t.AccountID), 10)},
		"token":   {t.Token},
	}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, body, err := session.doRaw(req)
	if err != nil {
		return nil, wrapf(op, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, httpError(op, resp, body)
	}
	s := string(body)
	if m := errorMsgExp.FindStringSubmatch(s); m != nil {
		msg := strings.TrimSpace(m[1])
		se := newStrError(op, 0, msg)
		if se.Cause == "" {
			se.Cause = CauseInvalidTradeURL
		}
		return nil, se
	}
	my := myEscrowExp.FindStringSubmatch(s)
	them := themEscrowExp.FindStringSubmatch(s)
	if my == nil || them == nil {
		if strings.Contains(s, "g_steamID = false") || strings.Contains(s, "login/home") {
			return nil, &SteamError{Op: op, Cause: CauseNotLoggedIn, Message: "login page returned"}
		}
		return nil, &SteamError{Op: op, Message: "malformed trade offer page"}
	}
	d := &PartnerDetails{}
	d.MyEscrowDays, _ = strconv.Atoi(my[1])
	d.TheirEscrowDays, _ = strconv.Atoi(them[1])
	if m := probationExp.FindStringSubmatch(s); m != nil {
		d.Probation = m[1] == "true"
	}
	return d, nil
}

// ---------- send / cancel ----------

// TradeItem identifies an asset in an offer.
type TradeItem struct {
	AppID     uint32
	ContextID uint64
	AssetID   uint64
	Amount    uint64 // 0 = 1
}

// OfferRequest describes an offer to send.
type OfferRequest struct {
	Partner        SteamID
	Token          string // partner's trade URL token (required unless you are friends)
	Message        string // shown to the partner (max ~128 chars are displayed)
	ItemsToGive    []TradeItem
	ItemsToReceive []TradeItem
}

// SentOffer is the result of SendOffer.
type SentOffer struct {
	ID                      uint64
	NeedsMobileConfirmation bool
	NeedsEmailConfirmation  bool
	EmailDomain             string
	State                   TradeOfferState // Active, or CreatedNeedsConfirmation
}

type offerAsset struct {
	AppID     uint32 `json:"appid"`
	ContextID string `json:"contextid"`
	Amount    uint64 `json:"amount"`
	AssetID   string `json:"assetid"`
}

func offerAssets(items []TradeItem) []offerAsset {
	out := make([]offerAsset, 0, len(items))
	for _, it := range items {
		amt := it.Amount
		if amt == 0 {
			amt = 1
		}
		out = append(out, offerAsset{AppID: it.AppID, ContextID: strconv.FormatUint(it.ContextID, 10), Amount: amt, AssetID: strconv.FormatUint(it.AssetID, 10)})
	}
	return out
}

// BuildOfferJSON returns the json_tradeoffer payload exactly as the Steam web client (and tradeoffer-manager) sends it.
func BuildOfferJSON(give, receive []TradeItem) (string, error) {
	payload := map[string]any{
		"newversion": true,
		"version":    len(give) + len(receive) + 1,
		"me":         map[string]any{"assets": offerAssets(give), "currency": []any{}, "ready": false},
		"them":       map[string]any{"assets": offerAssets(receive), "currency": []any{}, "ready": false},
	}
	b, err := json.Marshal(payload)
	return string(b), err
}

// SendOffer creates a trade offer. An offer where we only RECEIVE items does not need a mobile confirmation;
// when Steam asks for one anyway the result says so (NeedsMobileConfirmation) and the offer stays in
// CreatedNeedsConfirmation until AcceptConfirmationForObject(offer id) is called.
func (session *Session) SendOffer(ctx context.Context, r OfferRequest) (*SentOffer, error) {
	const op = "SendOffer"
	if len(r.ItemsToGive)+len(r.ItemsToReceive) == 0 {
		return nil, &SteamError{Op: op, Message: "cannot send an empty trade offer"}
	}
	if session.sessionID == "" {
		return nil, &SteamError{Op: op, Cause: CauseNotLoggedIn, Message: "no sessionid"}
	}
	offerJSON, err := BuildOfferJSON(r.ItemsToGive, r.ItemsToReceive)
	if err != nil {
		return nil, err
	}
	params := map[string]string{}
	if r.Token != "" {
		params["trade_offer_access_token"] = r.Token
	}
	paramsJSON, _ := json.Marshal(params)

	form := url.Values{
		"sessionid":                 {session.sessionID},
		"serverid":                  {"1"},
		"partner":                   {strconv.FormatUint(uint64(r.Partner), 10)},
		"tradeoffermessage":         {r.Message},
		"json_tradeoffer":           {offerJSON},
		"captcha":                   {""},
		"trade_offer_create_params": {string(paramsJSON)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CommunityBaseURL+"/tradeoffer/new/send", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	ref := url.Values{"partner": {strconv.FormatUint(uint64(r.Partner.GetAccountID()), 10)}}
	if r.Token != "" {
		ref.Set("token", r.Token)
	}
	req.Header.Set("Referer", CommunityBaseURL+"/tradeoffer/new/?"+ref.Encode())
	req.Header.Set("Origin", CommunityBaseURL)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")

	resp, body, err := session.doRaw(req)
	if err != nil {
		return nil, wrapf(op, err)
	}
	var out struct {
		StrError                string `json:"strError"`
		TradeOfferID            string `json:"tradeofferid"`
		NeedsMobileConfirmation bool   `json:"needs_mobile_confirmation"`
		NeedsEmailConfirmation  bool   `json:"needs_email_confirmation"`
		EmailDomain             string `json:"email_domain"`
	}
	jerr := json.Unmarshal(body, &out)
	// Steam answers some offer errors with HTTP 500 and a strError body: prefer the message.
	if jerr == nil && out.StrError != "" {
		return nil, newStrError(op, resp.StatusCode, out.StrError)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, httpError(op, resp, body)
	}
	if jerr != nil {
		return nil, &SteamError{Op: op, Message: "malformed JSON response"}
	}
	id, _ := strconv.ParseUint(out.TradeOfferID, 10, 64)
	if id == 0 {
		return nil, &SteamError{Op: op, Message: "no tradeofferid in response"}
	}
	res := &SentOffer{ID: id, NeedsMobileConfirmation: out.NeedsMobileConfirmation, NeedsEmailConfirmation: out.NeedsEmailConfirmation, EmailDomain: out.EmailDomain, State: Active}
	if res.NeedsMobileConfirmation || res.NeedsEmailConfirmation {
		res.State = CreatedNeedsConfirmation
	}
	return res, nil
}

// CancelOffer cancels an offer we sent (POST /tradeoffer/{id}/cancel).
func (session *Session) CancelOffer(ctx context.Context, id uint64) error {
	return session.offerAction(ctx, "CancelOffer", id, "cancel")
}

// DeclineOffer declines an offer we received (POST /tradeoffer/{id}/decline).
func (session *Session) DeclineOffer(ctx context.Context, id uint64) error {
	return session.offerAction(ctx, "DeclineOffer", id, "decline")
}

func (session *Session) offerAction(ctx context.Context, op string, id uint64, action string) error {
	sid := strconv.FormatUint(id, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CommunityBaseURL+"/tradeoffer/"+sid+"/"+action,
		strings.NewReader(url.Values{"sessionid": {session.sessionID}}.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Referer", CommunityBaseURL+"/tradeoffer/"+sid+"/")
	req.Header.Set("Origin", CommunityBaseURL)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	resp, body, err := session.doRaw(req)
	if err != nil {
		return wrapf(op, err)
	}
	var out struct {
		StrError     string `json:"strError"`
		TradeOfferID string `json:"tradeofferid"`
	}
	jerr := json.Unmarshal(body, &out)
	if jerr == nil && out.StrError != "" {
		return newStrError(op, resp.StatusCode, out.StrError)
	}
	if resp.StatusCode != http.StatusOK {
		return httpError(op, resp, body)
	}
	return nil
}
