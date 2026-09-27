package steam

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// flexBool decodes Steam's mixed boolean encodings: true/false, 0/1, "0"/"1", "true"/"false", null.
// The /inventory endpoint sends ints ("tradable":1) while IEconService sends JSON booleans.
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(bytes.TrimSpace(data)), `"`)
	switch strings.ToLower(s) {
	case "", "null", "0", "false":
		*b = false
	default:
		*b = true
	}
	return nil
}

// flexInt decodes a number that may be quoted.
type flexInt int64

func (n *flexInt) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(bytes.TrimSpace(data)), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil {
			return err
		}
		v = int64(f)
	}
	*n = flexInt(v)
	return nil
}

// UnmarshalJSON accepts both the IEconService (booleans) and the /inventory (0/1 ints) description shapes.
func (d *EconItemDesc) UnmarshalJSON(data []byte) error {
	type alias EconItemDesc
	aux := struct {
		*alias
		AppID                       flexInt  `json:"appid"`
		Currency                    flexBool `json:"currency"`
		Tradable                    flexBool `json:"tradable"`
		Commodity                   flexBool `json:"commodity"`
		Marketable                  flexBool `json:"marketable"`
		Sealed                      flexBool `json:"sealed"`
		MarketFeeApp                flexInt  `json:"market_fee_app"`
		MarketTradableRestriction   flexInt  `json:"market_tradable_restriction"`
		MarketMarketableRestriction flexInt  `json:"market_marketable_restriction"`
		SealedType                  flexInt  `json:"sealed_type"`
	}{alias: (*alias)(d)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	d.AppID = uint32(aux.AppID)
	d.Currency = bool(aux.Currency)
	d.Tradable = bool(aux.Tradable)
	d.Commodity = bool(aux.Commodity)
	d.Marketable = bool(aux.Marketable)
	d.Sealed = bool(aux.Sealed)
	d.MarketFeeApp = uint32(aux.MarketFeeApp)
	d.MarketTradableRestriction = int(aux.MarketTradableRestriction)
	d.MarketMarketableRestriction = int(aux.MarketMarketableRestriction)
	d.SealedType = int(aux.SealedType)
	return nil
}

// Tag returns the first tag of the category (e.g. "Type", "Rarity", "Exterior", "Quality", "Weapon"), or nil.
func (d *EconItemDesc) Tag(category string) *EconTag {
	if d == nil {
		return nil
	}
	for _, t := range d.Tags {
		if t != nil && strings.EqualFold(t.Category, category) {
			return t
		}
	}
	return nil
}

// EconomyImageBaseURL is the Steam economy image CDN (icon_url hashes are appended to it).
const EconomyImageBaseURL = "https://community.fastly.steamstatic.com/economy/image/"

// EconomyImageURL builds a CDN URL for an icon hash; size like "256fx256f" or "" for the original.
func EconomyImageURL(iconHash, size string) string {
	iconHash = strings.TrimSpace(iconHash)
	if iconHash == "" {
		return ""
	}
	if strings.HasPrefix(iconHash, "http://") || strings.HasPrefix(iconHash, "https://") {
		return iconHash
	}
	u := EconomyImageBaseURL + iconHash
	if size != "" {
		u += "/" + size
	}
	return u
}
