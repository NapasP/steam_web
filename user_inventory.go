package steam

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// InventoryOptions tunes GetUserInventoryContents. The zero value is valid.
type InventoryOptions struct {
	Language     string // "english" by default (item names / tags are localized by Steam)
	PageSize     int    // items per request, default 1000 (Steam's own clients use 1000–2000)
	TradableOnly bool   // drop items whose description says tradable=0
	MaxPages     int    // safety cap on pagination, default 25
}

// AssetProperty is one entry of the /inventory "asset_properties" block (CS2: 1 = paint seed, 2 = wear float, …).
type AssetProperty struct {
	PropertyID  int    `json:"propertyid"`
	IntValue    string `json:"int_value,omitempty"`
	FloatValue  string `json:"float_value,omitempty"`
	StringValue string `json:"string_value,omitempty"`
	Name        string `json:"name,omitempty"`
}

// UserInventoryItem is an asset merged with its description (Desc may be nil if Steam omitted it).
type UserInventoryItem struct {
	AppID      uint32
	ContextID  uint64
	AssetID    uint64
	ClassID    uint64
	InstanceID uint64
	Amount     uint64
	CurrencyID uint64
	Desc       *EconItemDesc
	Properties []AssetProperty
}

// CS2 asset property ids.
const (
	CS2PropertyPaintSeed = 1
	CS2PropertyWearFloat = 2
)

// Float returns the CS2 wear float when Steam included it.
func (it *UserInventoryItem) Float() (float64, bool) {
	for _, p := range it.Properties {
		if p.PropertyID == CS2PropertyWearFloat && p.FloatValue != "" {
			f, err := strconv.ParseFloat(p.FloatValue, 64)
			return f, err == nil
		}
	}
	return 0, false
}

// UserInventory is the full content of one app/context of a user's inventory.
type UserInventory struct {
	Items      []*UserInventoryItem
	Currencies []*UserInventoryItem
	TotalCount int // total_inventory_count reported by Steam (includes items hidden from this viewer)
}

type inventoryPage struct {
	Assets []struct {
		AppID      flexInt `json:"appid"`
		ContextID  string  `json:"contextid"`
		AssetID    string  `json:"assetid"`
		ClassID    string  `json:"classid"`
		InstanceID string  `json:"instanceid"`
		Amount     string  `json:"amount"`
		CurrencyID string  `json:"currencyid"`
	} `json:"assets"`
	Descriptions    []*EconItemDesc `json:"descriptions"`
	AssetProperties []struct {
		AssetID         string          `json:"assetid"`
		AssetProperties []AssetProperty `json:"asset_properties"`
	} `json:"asset_properties"`
	MoreItems           flexBool `json:"more_items"`
	LastAssetID         string   `json:"last_assetid"`
	TotalInventoryCount int      `json:"total_inventory_count"`
	Success             flexInt  `json:"success"`
	Error               string   `json:"error"`
	ErrorCap            string   `json:"Error"`
}

func parseU64(s string) uint64 {
	v, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	return v
}

// GetUserInventoryContents loads another user's (or our own) inventory through the modern
// https://steamcommunity.com/inventory/{steamid64}/{appid}/{contextid} endpoint, following pagination and
// merging descriptions and asset_properties — the equivalent of steamcommunity's getUserInventoryContents.
//
// Errors: ErrInventoryPrivate (403 "null"), a *SteamError with HTTPStatus 429 when rate limited
// (see IsRateLimited / RetryAfterOf), or a *SteamError with the EResult parsed from Steam's error body.
func (session *Session) GetUserInventoryContents(ctx context.Context, sid SteamID, appID, contextID uint64, opts *InventoryOptions) (*UserInventory, error) {
	const op = "GetUserInventoryContents"
	o := InventoryOptions{}
	if opts != nil {
		o = *opts
	}
	if o.Language == "" {
		o.Language = "english"
	}
	if o.PageSize <= 0 {
		o.PageSize = 1000
	}
	if o.MaxPages <= 0 {
		o.MaxPages = 25
	}
	if uint64(sid) == 0 {
		return nil, &SteamError{Op: op, Message: "invalid SteamID"}
	}

	inv := &UserInventory{}
	start := ""
	for page := 0; page < o.MaxPages; page++ {
		params := url.Values{"l": {o.Language}, "count": {strconv.Itoa(o.PageSize)}}
		if start != "" {
			params.Set("start_assetid", start)
		}
		u := fmt.Sprintf("%s/inventory/%d/%d/%d?%s", CommunityBaseURL, uint64(sid), appID, contextID, params.Encode())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Referer", fmt.Sprintf("%s/profiles/%d/inventory", CommunityBaseURL, uint64(sid)))
		req.Header.Set("Accept", "application/json")
		resp, body, err := session.doRaw(req)
		if err != nil {
			return nil, wrapf(op, err)
		}
		trimmed := strings.TrimSpace(string(body))
		if resp.StatusCode == http.StatusForbidden && (trimmed == "null" || trimmed == "") {
			return nil, &SteamError{Op: op, HTTPStatus: http.StatusForbidden, Cause: CausePrivateInventory, Message: "This profile is private."}
		}
		var pg inventoryPage
		decodeErr := json.Unmarshal(body, &pg)
		if resp.StatusCode != http.StatusOK {
			se := httpError(op, resp, body)
			if decodeErr == nil {
				msg := pg.Error
				if msg == "" {
					msg = pg.ErrorCap
				}
				if msg != "" {
					se.Message = msg
					if m := strErrorEResultExp.FindStringSubmatch(msg); m != nil {
						n, _ := strconv.Atoi(m[1])
						se.EResult = EResult(n)
					}
				}
			}
			return nil, se
		}
		if decodeErr != nil {
			return nil, &SteamError{Op: op, Message: "malformed JSON: " + decodeErr.Error()}
		}
		if pg.Success != 1 {
			msg := pg.Error
			if msg == "" {
				msg = pg.ErrorCap
			}
			if msg == "" {
				msg = "unsuccessful response"
			}
			se := &SteamError{Op: op, Message: msg}
			if m := strErrorEResultExp.FindStringSubmatch(msg); m != nil {
				n, _ := strconv.Atoi(m[1])
				se.EResult = EResult(n)
			}
			return nil, se
		}
		inv.TotalCount = pg.TotalInventoryCount

		descs := make(map[[2]uint64]*EconItemDesc, len(pg.Descriptions))
		for _, d := range pg.Descriptions {
			if d != nil {
				descs[[2]uint64{d.ClassID, d.InstanceID}] = d
			}
		}
		props := make(map[string][]AssetProperty, len(pg.AssetProperties))
		for _, p := range pg.AssetProperties {
			props[p.AssetID] = p.AssetProperties
		}
		for _, a := range pg.Assets {
			it := &UserInventoryItem{
				AppID:      uint32(a.AppID),
				ContextID:  parseU64(a.ContextID),
				AssetID:    parseU64(a.AssetID),
				ClassID:    parseU64(a.ClassID),
				InstanceID: parseU64(a.InstanceID),
				Amount:     parseU64(a.Amount),
				CurrencyID: parseU64(a.CurrencyID),
				Properties: props[a.AssetID],
			}
			if it.ContextID == 0 {
				it.ContextID = contextID
			}
			if it.AppID == 0 {
				it.AppID = uint32(appID)
			}
			it.Desc = descs[[2]uint64{it.ClassID, it.InstanceID}]
			if o.TradableOnly && (it.Desc == nil || !it.Desc.Tradable) {
				continue
			}
			if it.CurrencyID != 0 {
				inv.Currencies = append(inv.Currencies, it)
			} else {
				inv.Items = append(inv.Items, it)
			}
		}

		if !bool(pg.MoreItems) {
			return inv, nil
		}
		if pg.LastAssetID == "" || pg.LastAssetID == start {
			return nil, &SteamError{Op: op, Message: "pagination did not advance"}
		}
		start = pg.LastAssetID
	}
	return nil, &SteamError{Op: op, Message: fmt.Sprintf("inventory exceeds %d pages", o.MaxPages)}
}
