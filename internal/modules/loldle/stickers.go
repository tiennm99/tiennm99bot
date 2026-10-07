package loldle

import "math/rand"

// Sticker file_ids work across bot accounts (unlike photo and animation ids),
// so these survived the move from the previous bot. Adding/replacing requires
// resending the sticker to the bot and capturing the file_id via util's
// /stickerid private command. Empty pools are safe — pickSticker returns ""
// and handlers skip the SendSticker call.

// winStickers — cheerful pool used on a successful guess.
var winStickers = []string{
	"CAACAgIAAxkBAAECGBVp56vuLw5TSWy8TiFGkQYAAWxi0wUAAh9EAAJTD8BJ2wQ6vyMilxs7BA",
	"CAACAgIAAxkBAAECGBhp56wAATkzsBBcIxMSC_8mp2dBczsAAvBEAAL1vklJuCnu695nAvI7BA",
	"CAACAgIAAxkBAAECGBlp56wBZ4rmluL3KNlOuWsyctN0FQACNEQAAue5OEv37J_IfMnpljsE",
	"CAACAgIAAxkBAAECGCFp56w7WlD6vTIsHE2WUTs4C2IjXAAC9FsAAtgFMUu63usfH16ZpzsE",
}

// loseStickers — deflated pool used when the player runs out of guesses.
var loseStickers = []string{
	"CAACAgIAAxkBAAECGCBp56w3HUyYOHOeMwLfULT8p8SPtQACWkYAAiPesEjnptWk36YKZjsE",
}

// giveupStickers — resigned pool used on /loldle_giveup.
var giveupStickers = []string{
	"CAACAgIAAxkBAAECGCJp56xIk6B6McSfnYykLhgXVCSnmQACBlkAApSyWEo6G2rnqDvZxjsE",
}

// pickSticker returns one file_id from the pool, or "" when empty. The
// math/rand global is mutex-protected, so concurrent handlers can call this
// without their own synchronisation.
func pickSticker(pool []string) string {
	if len(pool) == 0 {
		return ""
	}
	return pool[rand.Intn(len(pool))]
}
