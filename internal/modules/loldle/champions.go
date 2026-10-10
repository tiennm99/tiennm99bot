package loldle

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// Champion is one row of champions.json. The attribute tags match
// loldle.net's scraped schema verbatim so the embedded JSON file can be
// regenerated from the upstream scrape without a transform step. ID (the
// Data Dragon key, which names the champion's icon) and Title come from
// Riot's Data Dragon by name; see docs/loldle.md.
type Champion struct {
	ChampionName string   `json:"championName"`
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Gender       string   `json:"gender"`
	Positions    []string `json:"positions"`
	Species      []string `json:"species"`
	Resource     string   `json:"resource"`
	RangeType    []string `json:"range_type"`
	Regions      []string `json:"regions"`
	ReleaseDate  string   `json:"release_date"` // YYYY-MM-DD
}

// rawChampions holds the embedded JSON byte stream — the loldle.net champion
// dictionary scraped offline and checked into data/champions.json.
//
//go:embed data/champions.json
var rawChampions []byte

// tileIDs overrides the icon id of champions whose Data Dragon tile file is
// spelled differently from their key. The CDN is case-sensitive: the tile is
// FiddleSticks_0.jpg, while tiles/Fiddlesticks_0.jpg answers 403.
var tileIDs = map[string]string{"Fiddlesticks": "FiddleSticks"}

// loadChampions parses the embedded JSON and applies tileIDs, so every ID
// names a tile that loads. Panics on malformed data —
// a corrupt regen of champions.json is a build-time bug, not a runtime
// concern worth recovering from.
func loadChampions() []Champion {
	var cs []Champion
	if err := json.Unmarshal(rawChampions, &cs); err != nil {
		panic(fmt.Sprintf("loldle: cannot decode champions.json: %v", err))
	}
	if len(cs) == 0 {
		panic("loldle: champions.json contained no records")
	}
	for i := range cs {
		if id, ok := tileIDs[cs[i].ID]; ok {
			cs[i].ID = id
		}
	}
	return cs
}
