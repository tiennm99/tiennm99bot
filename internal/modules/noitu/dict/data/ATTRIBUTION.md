# Dictionary Data Attribution

> Copied from `tiennm99/noitu` (`data/dictionary.txt` and `data/LICENSE`
> unchanged, this file with one sentence changed: how the corpus ships in the
> bot binary) for the bot's `noitu` game. Paths below such as
> `data/dictionary.txt` and the `make` targets refer to that repository.

The Vietnamese dictionary data used by this game is **not** original work of this project.
It is derived from Wiktionary text licensed under **CC BY-SA 4.0**, and this file records
the attribution and the modifications required by that license.

## Source

| Field | Value |
|---|---|
| Original work | Entries of [Wiktionary tiếng Việt](https://vi.wiktionary.org/), written by its contributors |
| Original license | [CC BY-SA 4.0](https://creativecommons.org/licenses/by-sa/4.0/) (Wiktionary text is dual-licensed CC BY-SA / GFDL) — full text in [`LICENSE`](./LICENSE) |
| Asset | [`viwiktionary-latest-pages-articles.xml.bz2`](https://dumps.wikimedia.org/viwiktionary/latest/viwiktionary-latest-pages-articles.xml.bz2) — the Wikimedia Foundation's dump of every page of the Vietnamese Wiktionary edition with its current wikitext, ~61 MB compressed, ~43,000 pages with a Vietnamese section |
| Refresh | regenerated monthly by Wikimedia; `latest/` is repointed at each new run |

**The asset is not pinned.** Each refresh fetches whatever `latest/` currently points at,
roughly monthly. The exact bytes the committed `data/dictionary.txt` was derived from are
recorded in its `#@` header lines and carried into every `data/noitu.db` built from it, in its
`meta` table:
`source_sha256` (SHA-256 of the file as read), `source_pages` (pages with a Vietnamese
section, redirects excluded) and `source_fetched_at` (the dump's own modification time).
Two refreshes a month apart may differ by a few hundred words; the corpus's git history
shows exactly which words and definitions changed, and the hash says which dump a given
image shipped. Dated dumps under `dumps.wikimedia.org/viwiktionary/`
exist should a build ever need reproducing.

The attribution chain has one link before this project: Wiktionary tiếng Việt's
contributors, who wrote the entries. The dump is their text as the wiki stores it; this
project's builder reads the wikitext itself.

## Modifications made by this project

`server/cmd/build-dictionary` transforms the dump into `data/dictionary.txt`, a sorted text
corpus of the accepted words and their definition excerpts, and builds `data/noitu.db` from
that corpus. Both are a **modified version** of the source data. Changes:

1. **Section selection** — read only the Vietnamese section of each page, in either of the
   two markup dialects the wiki currently uses (`{{-vie-}}` or `== {{langname|vi}} ==`).
   Pages outside the main namespace, redirects, and pages with no Vietnamese section were
   skipped. Other languages' sections on the same page were not read.
2. **Length filter** — kept only words of **2 or more space-separated syllables**, as
   required by the nối từ game rules. Single-syllable entries were dropped.
3. **Content filter** — dropped entries containing digits or punctuation, entries using
   letters Vietnamese does not have (f, j, w, z), and entries whose syllables do not fit
   Vietnamese phonotactics (a closed inventory of onsets, nuclei and codas). Diacritic-free
   Vietnamese words ("con cua") are kept.
4. **Normalization** — all words Unicode NFC-normalized, lowercased, and
   whitespace-collapsed. Capitalized headwords (`Hà Nội`) become lowercase entries; nothing
   is removed on the basis of capitalization or part of speech. Two pages whose titles
   normalize to one word are merged into one entry.
5. **Spelling aliases** — added an `aliases` table mapping alternative Vietnamese spellings
   to canonical entries. Two kinds: competing tone placement in open oa/oe/uy syllables
   (`hoà` → `hòa`, `thuý` → `thúy`), and i/y alternation in Sino-Vietnamese syllables
   (`quí` → `quý`, `lí` → `lý`). The majority are the i/y kind. These aliases are generated
   by this project and are not present upstream.
6. **Definition text** — each entry carries an **excerpt and modification** of its
   definitions, in a `meanings` table: the text of each `#` definition line of the
   Vietnamese section, with wiki markup removed (links reduced to their display text,
   formatting and references dropped, a few context and link templates unwrapped, every
   other template removed whole), cut to at most **five senses of 200 characters** each,
   and labelled with the Vietnamese name of the part-of-speech heading it sat under
   (`danh từ`, `động từ`, …; empty when the heading was not one the builder knows). This
   is not the entry as written: senses past the fifth, text past 200 characters, and
   template-only definitions the builder does not understand are gone.
7. **Added columns and tables** — `first` and `last` syllable columns, a `syllables`
   count, an index on `first`, a `syllables` out-degree table, the `meanings` table above,
   and a `meta` table recording provenance (source URL, SHA-256, page count, fetch time,
   licence). All added for game lookups.
8. **Deduplication** — a generated spelling variant that is itself a real word, or that
   more than one word would claim, is discarded rather than recorded as an alias.
9. **Dropped fields** — everything else in an entry was discarded: example sentences,
   quotations, translations, pronunciations, etymologies, synonyms, derived terms,
   categories, images and references. The derived database carries word forms and the
   definition excerpts described in item 6, and nothing else of the entry.

## Share-alike obligation

CC BY-SA 4.0 is a **share-alike** license. The derived corpus `data/dictionary.txt`, the
database `data/noitu.db` built from it, and any distribution of either, remain licensed under
**CC BY-SA 4.0** — including this repository, which distributes the corpus, and a container
image or any other packaged build of this project. Because the database
now redistributes edited excerpts of the entries' text and not only their headwords, the
attribution and this record of modifications travel with it wherever it goes.

This obligation applies to the **data only**. The source code of this project is licensed
separately under Apache-2.0 (see the repository root `LICENSE` and `NOTICE`). In tiennm99bot the corpus is embedded in the bot binary with go:embed,
unmodified; CC BY-SA 4.0 applies to this data only, the bot's code remains Apache-2.0.

## How to reproduce the derived data

```sh
make fetch-dict     # downloads the current Wiktionary tiếng Việt dump (~61 MB) into data/
make refresh-dict   # derives data/dictionary.txt from it, recording the file's SHA-256 in its header
make dict           # builds data/noitu.db from data/dictionary.txt
```

The corpus is committed and the dump and database are not. Because `latest/` is repointed
monthly, a refresh in a later month may not be byte-identical to an earlier one; the corpus
as committed is what every build of a given revision uses.
