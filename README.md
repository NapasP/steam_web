# Steam [![Build Status](https://travis-ci.org/doctype/steam.svg?branch=master)](https://travis-ci.org/doctype/steam)

Steam is a library for interactions with [Steam](https://steamcommunity.com), it's written in Go.  
Steam tries to keep-it-simple and does not add extra non-sense.  There are absolutely no internal-polling or such,
      everything is up to you, all it does is wrap around Steam API.

## Why?

- You don't want a library to be "re-trying" automatically
- You don't want a library to be doing your homework
- You are an on-point person and just want stuff that works as-needed

## Installation

Make sure you have _at least_ Go 1.6 with a GOPATH set then run:

```
go get github.com/PuerkitoBio/goquery
go get github.com/NapasP/steam_web
```

## Example

```go
package main

import (
	"log"
	"os"

	"github.com/NapasP/steam_web"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	timeTip, err := steam.GetTimeTip()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Time tip: %#v\n", timeTip)
	timeDiff := time.Duration(timeTip.Time - time.Now().Unix())
	
	session := steam.NewSession(&http.Client{}, "")
	if err := session.Login(os.Getenv("steamAccount"), os.Getenv("steamPassword"), os.Getenv("steamSharedSecret"), timeDiff); err != nil {
		log.Fatal(err)
	}
	log.Print("Login successful")
}
```

Find more examples in the examples/ directory.  Even better is to read through the source code, it's simple and
straight-forward to understand.

## Trade offers, inventories and errors (context-aware API)

The functions below take a `context.Context`, check HTTP status / `x-eresult`, and return a `*steam.SteamError`
(`errors.As`) with the HTTP status, the `EResult`, Steam's message and a `Cause`
(`TradeBan`, `NewDevice`, `TargetCannotTrade`, `OfferLimitExceeded`, `ItemServerUnavailable`, `NotLoggedIn`,
`PrivateInventory`, `RateLimited`, `InvalidTradeURL`). Helpers: `IsRateLimited`, `IsTemporary`, `RetryAfterOf`,
`errors.Is(err, steam.ErrNotLoggedIn / ErrInventoryPrivate / ErrOfferNotFound / ErrTradeNotFound / ErrNoPrice)`.
The library never retries on its own; opt in with `steam.Retry(ctx, steam.DefaultRetryPolicy, fn)`
(exponential backoff with jitter, honours `Retry-After`).

| Function | What it does |
| --- | --- |
| `ParseTradeURL(raw)` | Validates `https://steamcommunity.com/tradeoffer/new/?partner=N&token=T` → `AccountID`, `Token`, `SteamID()` |
| `GetUserInventoryContents(ctx, sid, 730, 2, opts)` | Any user's inventory via `/inventory/{steamid}/{app}/{ctx}`: pagination, descriptions + `asset_properties` merged (`Float()`), private inventory → `ErrInventoryPrivate` |
| `GetPartnerDetails(ctx, tradeURL)` | Trade-hold days of both sides from the "new offer" page; invalid token / trade ban → `SteamError` with a cause |
| `GetTradeHoldDurations(ctx, sid, token)` | Same through `IEconService/GetTradeHoldDurations` |
| `SendOffer(ctx, OfferRequest)` | Creates an offer (`json_tradeoffer` exactly as the web client sends it, token in `trade_offer_create_params`); reports `NeedsMobileConfirmation` |
| `AcceptConfirmationForObject(identitySecret, offerID, now)` | Confirms the mobile confirmation of one offer |
| `GetOffer(ctx, id)` / `GetOffers(ctx, opts)` | `IEconService/GetTradeOffer(s)` with cursor paging; `TradeOfferState` has `String/IsOpen/IsFinal` |
| `CancelOffer` / `DeclineOffer` | `strError` bodies are parsed into `EResult` + cause |
| `GetTradeStatus(ctx, tradeID)` | Exchange details (`ETradeStatus`, `assets_received[].new_assetid`) to detect rollbacks: `IsCompleted / IsPending / IsRolledBack` |
| `GetPriceOverview(ctx, 730, currency, name)` / `ParseMarketPrice` | Market price in any currency (`"1 234,56₴"`, `"$1,234.56"` …) |
| `EconomyImageURL(icon, "256fx256f")`, `(*EconItemDesc).Tag("Rarity")` | Item helpers |

`EconItemDesc` now accepts both the IEconService (booleans) and the `/inventory` (0/1) encodings, which also fixes the
legacy `GetInventory`. The legacy `GetTradeOffer` returns `ErrOfferNotFound` instead of a nil offer.

```go
tu, err := steam.ParseTradeURL(userInput)
hold, err := session.GetPartnerDetails(ctx, tu) // hold.TheirEscrowDays > 0 → partner has no mobile authenticator
sent, err := session.SendOffer(ctx, steam.OfferRequest{
	Partner: tu.SteamID(), Token: tu.Token, Message: "order NP-123",
	ItemsToReceive: []steam.TradeItem{{AppID: 730, ContextID: 2, AssetID: 123456789}},
})
offer, _, err := session.GetOffer(ctx, sent.ID) // poll; offer.OfferState() == steam.Accepted → offer.ReceiptID is the trade id
ts, err := session.GetTradeStatus(ctx, offer.ReceiptID)
if ts.Status.IsRolledBack() { /* do not pay */ }
```

Tests run against `httptest` servers (`go test ./...`); the `examples/` programs are from the upstream fork and are
excluded from the build (`//go:build ignore`).

## Authors

- [Ahmed Samy](https://github.com/asamy) <asamy@doctype.se>
- [Mark Samman](https://github.com/marksamman) <mark@doctype.se>
- [Artemiy Ryabinkov](https://github.com/Furdarius) <getlag@ya.ru>

## License

LGPL 2.1
