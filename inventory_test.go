package steam

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

const invPage1 = `{"assets":[
 {"appid":730,"contextid":"2","assetid":"1001","classid":"10","instanceid":"0","amount":"1"},
 {"appid":730,"contextid":"2","assetid":"1002","classid":"20","instanceid":"188530139","amount":"1"}],
 "descriptions":[
 {"appid":730,"classid":"10","instanceid":"0","currency":0,"background_color":"","icon_url":"iconA","tradable":1,"marketable":1,"commodity":1,"market_tradable_restriction":7,
  "name":"Revolution Case","market_hash_name":"Revolution Case","type":"Base Grade Container",
  "tags":[{"category":"Type","internal_name":"CSGO_Type_WeaponCase","localized_category_name":"Type","localized_tag_name":"Container"}]},
 {"appid":730,"classid":"20","instanceid":"188530139","tradable":0,"marketable":1,"icon_url":"iconB","name":"AK-47 | Redline","market_hash_name":"AK-47 | Redline (Field-Tested)",
  "owner_descriptions":[{"type":"html","value":"Tradable/Marketable After Oct 01, 2026 (7:00:00) GMT"}],
  "tags":[{"category":"Exterior","internal_name":"WearCategory2","localized_category_name":"Exterior","localized_tag_name":"Field-Tested"}]}],
 "asset_properties":[{"appid":730,"contextid":"2","assetid":"1002","asset_properties":[{"propertyid":1,"int_value":"661"},{"propertyid":2,"float_value":"0.2512"}]}],
 "more_items":1,"last_assetid":"1002","total_inventory_count":3,"success":1,"rwgrsn":-2}`

const invPage2 = `{"assets":[{"appid":730,"contextid":"2","assetid":"1003","classid":"10","instanceid":"0","amount":"1"}],
 "descriptions":[{"appid":730,"classid":"10","instanceid":"0","tradable":1,"marketable":1,"name":"Revolution Case","market_hash_name":"Revolution Case"}],
 "total_inventory_count":3,"success":1}`

func TestGetUserInventoryContents(t *testing.T) {
	var pages []string
	s := newTestSession(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inventory/76561198000000002/730/2" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Referer") != "https://steamcommunity.com/profiles/76561198000000002/inventory" {
			t.Errorf("referer %q", r.Header.Get("Referer"))
		}
		start := r.URL.Query().Get("start_assetid")
		pages = append(pages, start)
		if start == "" {
			w.Write([]byte(invPage1))
		} else {
			w.Write([]byte(invPage2))
		}
	}))
	inv, err := s.GetUserInventoryContents(context.Background(), 76561198000000002, 730, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || pages[1] != "1002" {
		t.Fatalf("pages %v", pages)
	}
	if len(inv.Items) != 3 || inv.TotalCount != 3 {
		t.Fatalf("items %d total %d", len(inv.Items), inv.TotalCount)
	}
	c := inv.Items[0]
	if c.AssetID != 1001 || c.Desc == nil || !c.Desc.Tradable || !c.Desc.Marketable || !c.Desc.Commodity || c.Desc.MarketTradableRestriction != 7 {
		t.Fatalf("case %+v %+v", c, c.Desc)
	}
	if tag := c.Desc.Tag("type"); tag == nil || tag.InternalName != "CSGO_Type_WeaponCase" {
		t.Fatalf("tag %+v", tag)
	}
	ak := inv.Items[1]
	if ak.Desc.Tradable || len(ak.Desc.OwnerDescriptions) != 1 || ak.Desc.Tag("Exterior").LocalizedTagName != "Field-Tested" {
		t.Fatalf("ak %+v", ak.Desc)
	}
	if f, ok := ak.Float(); !ok || f != 0.2512 {
		t.Fatalf("float %v %v", f, ok)
	}
	if EconomyImageURL(c.Desc.IconURL, "256fx256f") != "https://community.fastly.steamstatic.com/economy/image/iconA/256fx256f" {
		t.Fatal("icon url")
	}

	pages = nil
	inv, err = s.GetUserInventoryContents(context.Background(), 76561198000000002, 730, 2, &InventoryOptions{TradableOnly: true})
	if err != nil || len(inv.Items) != 2 {
		t.Fatalf("tradable only: %v %d", err, len(inv.Items))
	}
}

func TestGetUserInventoryErrors(t *testing.T) {
	mode := ""
	s := newTestSession(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "private":
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte("null"))
		case "429":
			w.Header().Set("Retry-After", "42")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte("null"))
		case "500":
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"Service Unavailable (20)"}`))
		case "empty":
			w.Write([]byte(`{"total_inventory_count":0,"success":1,"rwgrsn":-2}`))
		case "loop":
			w.Write([]byte(`{"assets":[],"descriptions":[],"more_items":1,"last_assetid":"","success":1}`))
		}
	}))
	ctx := context.Background()

	mode = "private"
	_, err := s.GetUserInventoryContents(ctx, 76561198000000002, 730, 2, nil)
	if !errors.Is(err, ErrInventoryPrivate) {
		t.Fatalf("private: %v", err)
	}
	mode = "429"
	_, err = s.GetUserInventoryContents(ctx, 76561198000000002, 730, 2, nil)
	if !IsRateLimited(err) || RetryAfterOf(err) != 42*time.Second {
		t.Fatalf("429: %v", err)
	}
	mode = "500"
	_, err = s.GetUserInventoryContents(ctx, 76561198000000002, 730, 2, nil)
	var se *SteamError
	if !errors.As(err, &se) || se.EResult != EResultServiceUnavailable || !IsTemporary(err) {
		t.Fatalf("500: %v", err)
	}
	mode = "empty"
	inv, err := s.GetUserInventoryContents(ctx, 76561198000000002, 730, 2, nil)
	if err != nil || len(inv.Items) != 0 {
		t.Fatalf("empty: %v", err)
	}
	mode = "loop"
	if _, err = s.GetUserInventoryContents(ctx, 76561198000000002, 730, 2, nil); err == nil {
		t.Fatal("pagination loop must fail")
	}
	if _, err = s.GetUserInventoryContents(ctx, 0, 730, 2, nil); err == nil {
		t.Fatal("zero steamid must fail")
	}
}

// The legacy GetInventory decodes the same endpoint (0/1 ints used to break the bool fields).
func TestLegacyGetInventoryDecodesIntBools(t *testing.T) {
	s := newTestSession(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(invPage2))
	}))
	items, err := s.GetInventory(76561198000000002, 730, 2, true)
	if err != nil || len(items) != 1 || !items[0].Desc.Tradable {
		t.Fatalf("%v %+v", err, items)
	}
}
