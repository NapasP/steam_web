package steam

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type TradeOfferState int

const (
	Invalid                  TradeOfferState = iota + 1
	Active                                   // This trade offer has been sent, neither party has acted on it yet.
	Accepted                                 // The trade offer was accepted by the recipient and items were exchanged.
	Countered                                // The recipient made a counter offer.
	Expired                                  // The trade offer was not accepted before the expiration date.
	Canceled                                 // The sender cancelled the offer.
	Declined                                 // The recipient declined the offer.
	InvalidItems                             // Some of the items in the offer are no longer available.
	CreatedNeedsConfirmation                 // The offer hasn't been sent yet and is awaiting further confirmation.
	CanceledBySecondFactor                   // Either party canceled the offer via email/mobile confirmation.
	InEscrow                                 // The trade has been placed on hold.
)

var ETradeOfferState = struct {
	Invalid                  TradeOfferState
	Active                   TradeOfferState
	Accepted                 TradeOfferState
	Countered                TradeOfferState
	Expired                  TradeOfferState
	Canceled                 TradeOfferState
	Declined                 TradeOfferState
	InvalidItems             TradeOfferState
	CreatedNeedsConfirmation TradeOfferState
	CanceledBySecondFactor   TradeOfferState
	InEscrow                 TradeOfferState
}{
	Invalid:                  Invalid,
	Active:                   Active,
	Accepted:                 Accepted,
	Countered:                Countered,
	Expired:                  Expired,
	Canceled:                 Canceled,
	Declined:                 Declined,
	InvalidItems:             InvalidItems,
	CreatedNeedsConfirmation: CreatedNeedsConfirmation,
	CanceledBySecondFactor:   CanceledBySecondFactor,
	InEscrow:                 InEscrow,
}

const (
	TradeConfirmationNone = iota
	TradeConfirmationEmail
	TradeConfirmationMobileApp
	TradeConfirmationMobile
)

const (
	TradeFilterNone             = iota
	TradeFilterSentOffers       = 1 << 0
	TradeFilterRecvOffers       = 1 << 1
	TradeFilterActiveOnly       = 1 << 3
	TradeFilterHistoricalOnly   = 1 << 4
	TradeFilterItemDescriptions = 1 << 5
)

var (
	// receiptExp matches JSON in the following form:
	//	oItem = {"id":"...",...}; (Javascript code)
	receiptExp    = regexp.MustCompile("oItem =\\s(.+?});")
	myEscrowExp   = regexp.MustCompile("var g_daysMyEscrow = (\\d+);")
	themEscrowExp = regexp.MustCompile("var g_daysTheirEscrow = (\\d+);")
	errorMsgExp   = regexp.MustCompile("<div id=\"error_msg\">\\s*([^<]+)\\s*</div>")
	offerInfoExp  = regexp.MustCompile("token=([a-zA-Z0-9-_]+)")

	apiGetTradeOffer     = APIBaseUrl + "/IEconService/GetTradeOffer/v1/?"
	apiGetTradeOffers    = APIBaseUrl + "/IEconService/GetTradeOffers/v1/?"
	apiDeclineTradeOffer = "https://steamcommunity.com/tradeoffer/%s/decline"
	apiCancelTradeOffer  = "https://steamcommunity.com/tradeoffer/%s/cancel"
	apiAcceptTradeOffer  = "https://steamcommunity.com/tradeoffer/%s/accept"

	apiMarketItemPrice = "https://steamcommunity.com/market/priceoverview/?currency=18&appid=730&market_hash_name=%s"

	ErrReceiptMatch        = errors.New("unable to match items in trade receipt")
	ErrCannotAcceptActive  = errors.New("unable to accept a non-active trade")
	ErrCannotFindOfferInfo = errors.New("unable to match data from trade offer url")
)

type ItemPrice struct {
	Success     bool   `json:"success"`
	LowestPrice string `json:"lowest_price"`
	Volume      string `json:"volume"`
	MedianPrice string `json:"median_price"`
}

type EconItem struct {
	AssetID    uint64 `json:"assetid,string,omitempty"`
	InstanceID uint64 `json:"instanceid,string,omitempty"`
	ClassID    uint64 `json:"classid,string,omitempty"`
	AppID      uint32 `json:"appid"`
	ContextID  uint64 `json:"contextid,string"`
	Amount     uint32 `json:"amount,string"`
	Missing    bool   `json:"missing,omitempty"`
	EstUSD     uint32 `json:"est_usd,string"`
}

type EconDesc struct {
	Type  string `json:"type"`
	Value string `json:"value"`
	Color string `json:"color"`
}

type EconTag struct {
	InternalName          string `json:"internal_name"`
	Category              string `json:"category"`
	LocalizedCategoryName string `json:"localized_category_name"`
	LocalizedTagName      string `json:"localized_tag_name"`
}

type EconAction struct {
	Link string `json:"link"`
	Name string `json:"name"`
}

type EconItemDesc struct {
	ClassID         uint64        `json:"classid,string"`    // for matching with EconItem
	InstanceID      uint64        `json:"instanceid,string"` // for matching with EconItem
	Tradable        bool          `json:"tradable"`
	BackgroundColor string        `json:"background_color"`
	IconURL         string        `json:"icon_url"`
	IconLargeURL    string        `json:"icon_url_large"`
	IconDragURL     string        `json:"icon_drag_url"`
	Name            string        `json:"name"`
	NameColor       string        `json:"name_color"`
	MarketName      string        `json:"market_name"`
	MarketHashName  string        `json:"market_hash_name"`
	MarketFeeApp    uint32        `json:"market_fee_app"`
	Comodity        bool          `json:"comodity"`
	Actions         []*EconAction `json:"actions"`
	Tags            []*EconTag    `json:"tags"`
	Descriptions    []*EconDesc   `json:"descriptions"`
}

type TradeOffer struct {
	ID                 uint64      `json:"tradeofferid,string"`
	Partner            uint32      `json:"accountid_other"`
	ReceiptID          uint64      `json:"tradeid,string"`
	RecvItems          []*EconItem `json:"items_to_receive"`
	SendItems          []*EconItem `json:"items_to_give"`
	Message            string      `json:"message"`
	State              uint8       `json:"trade_offer_state"`
	ConfirmationMethod uint8       `json:"confirmation_method"`
	Created            int64       `json:"time_created"`
	Updated            int64       `json:"time_updated"`
	Expires            int64       `json:"expiration_time"`
	EscrowEndDate      int64       `json:"escrow_end_date"`
	RealTime           bool        `json:"from_real_time_trade"`
	IsOurOffer         bool        `json:"is_our_offer"`
}

type TradeOfferResponse struct {
	Offer          *TradeOffer     `json:"offer"`                 // GetTradeOffer
	SentOffers     []*TradeOffer   `json:"trade_offers_sent"`     // GetTradeOffers
	ReceivedOffers []*TradeOffer   `json:"trade_offers_received"` // GetTradeOffers
	Descriptions   []*EconItemDesc `json:"descriptions"`          // GetTradeOffers
}

type APIResponse struct {
	Inner *TradeOfferResponse `json:"response"`
}

func (session *Session) GetTradeOffer(id uint64) (*TradeOffer, error) {
	resp, err := session.client.Get(apiGetTradeOffer + url.Values{
		"access_token": {session.accessToken},
		"tradeofferid": {strconv.FormatUint(id, 10)},
	}.Encode())
	if resp != nil {
		defer resp.Body.Close()
	}

	if err != nil {
		return nil, err
	}

	var response APIResponse
	if err = json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}

	return response.Inner.Offer, nil
}

// func testBit(bits uint32, bit uint32) bool {
// 	return (bits & bit) == bit
// }

func (session *Session) GetTradeOffers() (*TradeOfferResponse, error) {
	params := url.Values{
		"access_token": {session.accessToken},
		"language":     {"1"},
	}

	params.Set("get_sent_offers", "1")
	params.Set("get_received_offers", "1")
	params.Set("active_only", "1")
	params.Set("get_descriptions", "1")
	params.Set("historical_only", "0")
	params.Set("time_historical_cutoff", "")

	resp, err := session.client.Get(apiGetTradeOffers + params.Encode())
	if resp != nil {
		defer resp.Body.Close()
	}

	if err != nil {
		return nil, err
	}

	var response APIResponse
	if err = json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}

	return response.Inner, nil
}

func (session *Session) GetMyTradeToken() (string, error) {
	resp, err := session.client.Get("https://steamcommunity.com/my/tradeoffers/privacy")
	if resp != nil {
		defer resp.Body.Close()
	}

	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http error: %d", resp.StatusCode)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	m := offerInfoExp.FindStringSubmatch(string(body))
	if m == nil || len(m) != 2 {
		return "", ErrCannotFindOfferInfo
	}

	return m[1], nil
}

type EscrowSteamGuardInfo struct {
	MyDays   int64
	ThemDays int64
	ErrorMsg string
}

func (session *Session) GetEscrowGuardInfo(sid SteamID, token string) (*EscrowSteamGuardInfo, error) {
	return session.GetEscrow("https://steamcommunity.com/tradeoffer/new/?" + url.Values{
		"partner": {strconv.FormatUint(uint64(sid.GetAccountID()), 10)},
		"token":   {token},
	}.Encode())
}

func (session *Session) GetEscrowGuardInfoForTrade(offerID uint64) (*EscrowSteamGuardInfo, error) {
	return session.GetEscrow("https://steamcommunity.com/tradeoffer/" + strconv.FormatUint(offerID, 10))
}

func (session *Session) GetEscrow(url string) (*EscrowSteamGuardInfo, error) {
	resp, err := session.client.Get(url)
	if resp != nil {
		defer resp.Body.Close()
	}

	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http error: %d", resp.StatusCode)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var my int64
	var them int64
	var errMsg string

	m := myEscrowExp.FindStringSubmatch(string(body))
	if len(m) == 2 {
		my, _ = strconv.ParseInt(m[1], 10, 32)
	}

	m = themEscrowExp.FindStringSubmatch(string(body))
	if len(m) == 2 {
		them, _ = strconv.ParseInt(m[1], 10, 32)
	}

	m = errorMsgExp.FindStringSubmatch(string(body))
	if len(m) == 2 {
		errMsg = m[1]
	}

	return &EscrowSteamGuardInfo{
		MyDays:   my,
		ThemDays: them,
		ErrorMsg: errMsg,
	}, nil
}

func (session *Session) SendTradeOffer(offer *TradeOffer, sid SteamID, token string) error {
	content := map[string]interface{}{
		"newversion": true,
		"version":    3,
		"me": map[string]interface{}{
			"assets":   offer.SendItems,
			"currency": make([]struct{}, 0),
			"ready":    false,
		},
		"them": map[string]interface{}{
			"assets":   offer.RecvItems,
			"currency": make([]struct{}, 0),
			"ready":    false,
		},
	}

	contentJSON, err := json.Marshal(content)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(
		http.MethodPost,
		"https://steamcommunity.com/tradeoffer/new/send",
		strings.NewReader(url.Values{
			"sessionid":                 {session.sessionID},
			"serverid":                  {"1"},
			"partner":                   {sid.ToString()},
			"tradeoffermessage":         {offer.Message},
			"json_tradeoffer":           {string(contentJSON)},
			"trade_offer_create_params": {"{\"trade_offer_access_token\":\"" + token + "\"}"},
		}.Encode()),
	)
	if err != nil {
		return err
	}
	req.Header.Add("Referer", "https://steamcommunity.com/tradeoffer/new/?"+url.Values{
		"partner": {strconv.FormatUint(uint64(sid.GetAccountID()), 10)},
		"token":   {token},
	}.Encode())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := session.client.Do(req)
	if resp != nil {
		defer resp.Body.Close()
	}

	if err != nil {
		return err
	}

	type Response struct {
		ErrorMessage               string `json:"strError"`
		ID                         uint64 `json:"tradeofferid,string"`
		MobileConfirmationRequired bool   `json:"needs_mobile_confirmation"`
		EmailConfirmationRequired  bool   `json:"needs_email_confirmation"`
		EmailDomain                string `json:"email_domain"`
	}

	var response Response
	if err = json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return err
	}

	if len(response.ErrorMessage) != 0 {
		return errors.New(response.ErrorMessage)
	}

	if response.ID == 0 {
		return errors.New("no OfferID included")
	}

	offer.ID = response.ID
	offer.Created = time.Now().Unix()
	offer.Updated = time.Now().Unix()
	offer.Expires = offer.Created + 14*24*60*60
	offer.RealTime = false
	offer.IsOurOffer = true

	// Just test mobile confirmation, email is deprecated
	if response.MobileConfirmationRequired {
		offer.ConfirmationMethod = TradeConfirmationMobileApp
		offer.State = uint8(ETradeOfferState.CreatedNeedsConfirmation)
	} else {
		// set state to active
		offer.State = uint8(ETradeOfferState.Active)
	}

	return nil
}

func (session *Session) GetTradeReceivedItems(receiptID uint64) ([]*InventoryItem, error) {
	resp, err := session.client.Get(fmt.Sprintf("https://steamcommunity.com/trade/%d/receipt", receiptID))
	if resp != nil {
		defer resp.Body.Close()
	}

	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http error: %d", resp.StatusCode)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	m := receiptExp.FindAllSubmatch(body, -1)
	if m == nil {
		return nil, ErrReceiptMatch
	}

	items := make([]*InventoryItem, len(m))
	for k := range m {
		item := &InventoryItem{}
		if err = json.Unmarshal(m[k][1], item); err != nil {
			return nil, err
		}

		items[k] = item
	}

	return items, nil
}

func (session *Session) DeclineTradeOffer(id uint64) error {
	tradeID := strconv.FormatUint(id, 10)

	data := url.Values{}
	data.Set("sessionid", session.sessionID)

	req, err := http.NewRequest("POST", fmt.Sprintf(apiDeclineTradeOffer, tradeID), bytes.NewBufferString(data.Encode()))
	if err != nil {
		return err
	}

	req.Header.Set("referer", fmt.Sprintf("https://steamcommunity.com/tradeoffer/%s", tradeID))
	req.Header.Set("origin", "https://steamcommunity.com")
	req.Header.Set("content-type", "application/x-www-form-urlencoded")

	resp, err := session.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP error: %d", resp.StatusCode)
	}

	return nil
}

func (session *Session) CancelTradeOffer(id uint64) error {
	tradeID := strconv.FormatUint(id, 10)

	data := url.Values{}
	data.Set("sessionid", session.sessionID)

	req, err := http.NewRequest("POST", fmt.Sprintf(apiCancelTradeOffer, tradeID), bytes.NewBufferString(data.Encode()))
	if err != nil {
		return err
	}

	req.Header.Set("referer", fmt.Sprintf("https://steamcommunity.com/tradeoffer/%s", tradeID))
	req.Header.Set("origin", "https://steamcommunity.com")
	req.Header.Set("content-type", "application/x-www-form-urlencoded")

	resp, err := session.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP error: %d", resp.StatusCode)
	}

	return nil
}

func (session *Session) AcceptTradeOffer(id uint64) error {
	tradeID := strconv.FormatUint(id, 10)

	data := url.Values{}
	data.Set("sessionid", session.sessionID)
	data.Set("serverid", "1")
	data.Set("tradeofferid", tradeID)
	data.Set("captcha", "")
	data.Set("partner", "4747")

	req, err := http.NewRequest("POST", fmt.Sprintf(apiAcceptTradeOffer, tradeID), bytes.NewBufferString(data.Encode()))
	if err != nil {
		return err
	}

	req.Header.Set("referer", fmt.Sprintf("https://steamcommunity.com/tradeoffer/%s", tradeID))
	req.Header.Set("origin", "https://steamcommunity.com")
	req.Header.Set("content-type", "application/x-www-form-urlencoded")

	resp, err := session.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP error: %d", resp.StatusCode)
	}

	return nil
}

func (session *Session) GetMarketPrice(marketName string) (dataPrice *ItemPrice, err error) {
	resp, err := session.client.Get(fmt.Sprintf(apiMarketItemPrice, url.QueryEscape(marketName)))
	if err != nil {
		return
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http error: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}

	err = json.Unmarshal(body, &dataPrice)
	if err != nil {
		return
	}

	return
}

func (offer *TradeOffer) Send(session *Session, sid SteamID, token string) error {
	return session.SendTradeOffer(offer, sid, token)
}

func (offer *TradeOffer) Accept(session *Session) error {
	return session.AcceptTradeOffer(offer.ID)
}

func (offer *TradeOffer) Cancel(session *Session) error {
	if offer.IsOurOffer {
		return session.CancelTradeOffer(offer.ID)
	}

	return session.DeclineTradeOffer(offer.ID)
}
