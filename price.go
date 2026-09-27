package steam

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// ErrNoPrice: the market has neither a lowest nor a median price for the item.
var ErrNoPrice = errors.New("steam: no market price")

// ParseMarketPrice parses Steam's formatted prices: "$1,234.56", "1 234,56₴", "12,34€", "1.234,56 pуб.",
// "₴1,234.56", "CDN$ 3.10", "1'234.50 CHF". The last '.' or ',' followed by 1–2 digits is the decimal mark;
// every other separator (',', '.', ' ', NBSP, '\”) is a thousands separator.
func ParseMarketPrice(s string) (float64, error) {
	var digits []rune
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r == '.', r == ',':
			digits = append(digits, r)
		case unicode.IsSpace(r), r == ' ', r == '\'', r == ' ', r == ' ':
			// thousands separators: drop
		default:
			// currency symbols / letters: drop, but a letter between digits ends the number
		}
	}
	str := strings.Trim(string(digits), ".,")
	if str == "" {
		return 0, ErrNoPrice
	}
	dec := -1
	if i := strings.LastIndexAny(str, ".,"); i >= 0 {
		tail := len(str) - i - 1
		if tail >= 1 && tail <= 2 {
			dec = i
		}
	}
	var b strings.Builder
	for i, r := range str {
		if r == '.' || r == ',' {
			if i == dec {
				b.WriteByte('.')
			}
			continue
		}
		b.WriteRune(r)
	}
	v, err := strconv.ParseFloat(b.String(), 64)
	if err != nil {
		return 0, err
	}
	if v < 0 {
		return 0, ErrNoPrice
	}
	return v, nil
}

// PriceOverview is /market/priceoverview parsed.
type PriceOverview struct {
	Lowest float64 // lowest listing, 0 when absent
	Median float64 // median sale price, 0 when absent
	Volume int     // sold in the last 24h (0 when absent)
	Raw    ItemPrice
}

// Best returns the lowest listing price, falling back to the median sale price.
func (p *PriceOverview) Best() float64 {
	if p.Lowest > 0 {
		return p.Lowest
	}
	return p.Median
}

// GetPriceOverview fetches https://steamcommunity.com/market/priceoverview/ for one market_hash_name in the
// given currency (1 = USD, 18 = UAH, see Currency* constants). Steam rate-limits this endpoint hard
// (~20 req/min per IP): HTTP 429 comes back as a *SteamError (IsRateLimited). ErrNoPrice when unpriced.
func (session *Session) GetPriceOverview(ctx context.Context, appID uint64, currency int, marketHashName string) (*PriceOverview, error) {
	const op = "GetPriceOverview"
	u := CommunityBaseURL + "/market/priceoverview/?" + url.Values{
		"appid":            {strconv.FormatUint(appID, 10)},
		"currency":         {strconv.Itoa(currency)},
		"market_hash_name": {marketHashName},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, body, err := session.doRaw(req)
	if err != nil {
		return nil, wrapf(op, err)
	}
	if resp.StatusCode != http.StatusOK {
		// 500 {"success":false} is how Steam answers unknown / never-sold names.
		if resp.StatusCode == http.StatusInternalServerError && strings.Contains(string(body), `"success":false`) {
			return nil, ErrNoPrice
		}
		return nil, httpError(op, resp, body)
	}
	var raw ItemPrice
	if err := json.Unmarshal(body, &raw); err != nil {
		// Steam answers "null" when throttling softly.
		if strings.TrimSpace(string(body)) == "null" {
			return nil, &SteamError{Op: op, Cause: CauseRateLimited, Message: "null body"}
		}
		return nil, &SteamError{Op: op, Message: "malformed JSON: " + err.Error()}
	}
	if !raw.Success {
		return nil, ErrNoPrice
	}
	p := &PriceOverview{Raw: raw}
	if raw.LowestPrice != "" {
		p.Lowest, _ = ParseMarketPrice(raw.LowestPrice)
	}
	if raw.MedianPrice != "" {
		p.Median, _ = ParseMarketPrice(raw.MedianPrice)
	}
	if raw.Volume != "" {
		v, _ := ParseMarketPrice(raw.Volume)
		p.Volume = int(v)
	}
	if p.Best() <= 0 {
		return nil, ErrNoPrice
	}
	return p, nil
}
