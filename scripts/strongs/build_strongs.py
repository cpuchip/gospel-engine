"""Build the engine's Strong's tables from their sources. Reads files, writes files; never connects to a database.
  python build_strongs.py --kjv-zip KJV.zip --lexicon strongs-lexicon.json.gz --library GOSPEL_LIBRARY --out OUT

Sources (none of their data is committed to this repository):
  --kjv-zip   CrossWire's SWORD KJV module (https://www.crosswire.org/sword/modules/ModInfo.jsp?modName=KJV, the rawzip
              package KJV.zip): KJV text with Strong's numbers; OT Strong's from The Bible Foundation, NT from the
              KJV2003 Project. DistributionLicense=GPL; CrossWire grants use "for any purpose". Ruled the engine's source
              of record on 2026-10-09; numbers kept as CrossWire stores them (lemma numbers), unpadded (H07225 -> H7225).
  --lexicon   strongs-lexicon.json.gz from cpuchip/strongs-concordance-mcp (Strong's 1890 via OpenScriptures, CC BY 4.0
              / CC BY-SA; STEPBible TBESH/TBESG glosses, CC BY 4.0).
  --library   a gospel-library checkout: every KJV verse must land on a verse the engine indexes (same rule as the
              indexer: a `**N.** text` line with non-empty cleaned text under eng/scriptures/{ot,nt}/{book}/{ch}.md).

Writes OUT/strongs_lexicon.tsv and OUT/kjv_words.tsv (Postgres COPY text format), OUT/summary.json and OUT/SHA256SUMS.
Exits non-zero, writing no TSV, if any check fails. Needs: pip install pysword==0.2.8 (pure Python)."""
import argparse, gzip, hashlib, json, os, re, sys, tempfile, zipfile

from pysword.modules import SwordModules

ap = argparse.ArgumentParser()
ap.add_argument("--kjv-zip", required=True); ap.add_argument("--lexicon", required=True)
ap.add_argument("--library", required=True); ap.add_argument("--out", required=True)
a = ap.parse_args()

OT = ("gen ex lev num deut josh judg ruth 1-sam 2-sam 1-kgs 2-kgs 1-chr 2-chr ezra neh esth job ps prov eccl song isa "
      "jer lam ezek dan hosea joel amos obad jonah micah nahum hab zeph hag zech mal").split()
NT = ("matt mark luke john acts rom 1-cor 2-cor gal eph philip col 1-thes 2-thes 1-tim 2-tim titus philem heb james "
      "1-pet 2-pet 1-jn 2-jn 3-jn jude rev").split()
canon = lambda n: re.sub(r"^([GH])0*(\d+).*$", r"\1\2", n)

# --- the engine's verse keys, by the indexer's rule
VERSE = re.compile(r"^\*\*(\d+)\.\*\*\s*(.+)")
FOOT = re.compile(r"<sup>\[[^\]]+\]\(#fn-[^)]+\)</sup>"); SUP = re.compile(r"<sup>[^<]+</sup>"); LINKC = re.compile(r"\[([^\]]+)\]\([^)]+\)")
clean_md = lambda s: " ".join(LINKC.sub(r"\1", SUP.sub("", FOOT.sub("", s))).split())
keys = set()
for vol, slugs in (("ot", OT), ("nt", NT)):
    for b in slugs:
        d = os.path.join(a.library, "eng", "scriptures", vol, b)
        for fn in os.listdir(d):
            if fn.endswith(".md") and fn[:-3].isdigit():
                for line in open(os.path.join(d, fn), encoding="utf-8"):
                    m = VERSE.match(line)
                    if m and clean_md(m.group(2)):
                        keys.add((vol, b, int(fn[:-3]), int(m.group(1))))

# --- CrossWire's module
tmp = tempfile.mkdtemp(); zipfile.ZipFile(a.kjv_zip).extractall(tmp)
mods = SwordModules(tmp); mods.parse_modules(); bible = mods.get_bible_from_module("KJV")
books = bible.get_structure().get_books()
cw_books = [("ot", b, s) for b, s in zip(books["ot"], OT)] + [("nt", b, s) for b, s in zip(books["nt"], NT)]
assert len(books["ot"]) == 39 and len(books["nt"]) == 27, "unexpected book count in the module"

STRIP = re.compile(r"(?s)<title[^>]*>.*?</title>|<note[^>]*>.*?</note>")  # Psalm titles and notes are not verse text
TOKEN = re.compile(r'(?s)<w\b([^>]*?)/>|<w\b([^>]*)>(.*?)</w>|<transChange[^>]*>(.*?)</transChange>')
TAG = re.compile(r"<[^>]+>")
LEMMA = re.compile(r'lemma="([^"]*)"')
def words(osis):
    """[(word, [numbers])] in order. A <w> with English gives its numbers; a self-closing <w/> is a Greek word the
    KJV leaves untranslated (usually the article) and is skipped; a translator's added word is kept, untagged."""
    out = []
    for m in TOKEN.finditer(STRIP.sub("", osis)):
        if m.group(1) is not None:
            continue
        if m.group(4) is not None:
            text, nums = TAG.sub("", m.group(4)), []
        else:
            text = TAG.sub("", m.group(3))
            lem = LEMMA.search(m.group(2))
            nums = [canon(t[7:]) for t in (lem.group(1).split() if lem else []) if t.startswith("strong:")]
        text = " ".join(text.split())
        if text:
            out.append((text, nums))
    return out

lex = json.load(gzip.open(a.lexicon, "rt", encoding="utf-8"))
rows, verses, untagged, unmatched, missing_numbers, name_pairs = [], 0, [], [], {}, []
for vol, b, slug in cw_books:
    name_pairs.append((b.name, slug))
    for ch in range(1, b.num_chapters + 1):
        for vs in range(1, b.chapter_lengths[ch - 1] + 1):
            ws = words(bible.get(books=[b.name], chapters=[ch], verses=[vs], clean=False))
            if not ws:
                continue  # a verse the module leaves empty
            verses += 1
            if (vol, slug, ch, vs) not in keys:
                unmatched.append(f"{slug} {ch}:{vs}")
            if not any(n for _, n in ws):
                untagged.append(f"{slug} {ch}:{vs}")
            for pos, (w, nums) in enumerate(ws):
                for n in nums:
                    if n not in lex:
                        missing_numbers[n] = missing_numbers.get(n, 0) + 1
                rows.append((vol, slug, ch, vs, pos, w, nums))

kjv_keys = {k for k in keys}
covered = {(r[0], r[1], r[2], r[3]) for r in rows}
summary = {
    "verses": verses, "words": len(rows), "tagged_words": sum(1 for r in rows if r[6]),
    "verses_unmatched_in_library": len(unmatched), "unmatched_examples": unmatched[:10],
    "library_verses_without_tagging": len(kjv_keys - covered),
    "library_without_examples": sorted(f"{k[1]} {k[2]}:{k[3]}" for k in kjv_keys - covered)[:10],
    "verses_without_any_tag": len(untagged), "untagged_examples": untagged[:10],
    "distinct_numbers": len({n for r in rows for n in r[6]}),
    "numbers_not_in_lexicon": len(missing_numbers),
    "numbers_not_in_lexicon_top": sorted(missing_numbers.items(), key=lambda x: -x[1])[:15],
    "lexicon_entries": len(lex),
    "book_names_sample": name_pairs[:3] + name_pairs[38:41] + name_pairs[-2:],
}
print(json.dumps(summary, indent=1, ensure_ascii=False))
# The checks. Every KJV verse must land on an indexed verse and every indexed KJV verse must have tagging; a
# small number of numbers outside the lexicon is reported, not fatal (they still display, without a gloss).
fail = []
if verses != 31102: fail.append(f"expected 31,102 verses, got {verses}")
if unmatched: fail.append(f"{len(unmatched)} module verses have no indexed verse")
if kjv_keys - covered: fail.append(f"{len(kjv_keys - covered)} indexed KJV verses have no tagging")
if untagged: fail.append(f"{len(untagged)} verses carry no Strong's number")
if len(lex) != 19570: fail.append(f"expected 19,570 lexicon entries, got {len(lex)}")
if fail:
    sys.exit("CHECKS FAILED: " + "; ".join(fail))

os.makedirs(a.out, exist_ok=True)
esc = lambda s: (s or "").replace("\\", "\\\\").replace("\t", " ").replace("\n", " ").replace("\r", " ")
with open(os.path.join(a.out, "kjv_words.tsv"), "w", encoding="utf-8", newline="\n") as o:
    for vol, slug, ch, vs, pos, w, nums in rows:
        arr = "{" + ",".join(nums) + "}"
        o.write(f"{vol}\t{slug}\t{ch}\t{vs}\t{pos}\t{esc(w)}\t{arr}\n")
COLS = ("number", "lang", "lemma", "translit", "pron", "strongs_def", "kjv_def", "derivation", "step_gloss", "step_def")
with open(os.path.join(a.out, "strongs_lexicon.tsv"), "w", encoding="utf-8", newline="\n") as o:
    for n in sorted(lex, key=lambda x: (x[0], int(re.sub(r"\D", "", x) or 0), x)):
        e = lex[n]
        o.write("\t".join(esc(str(e.get(c) or "")) for c in COLS) + "\n")
json.dump(summary, open(os.path.join(a.out, "summary.json"), "w", encoding="utf-8"), indent=1, ensure_ascii=False)
with open(os.path.join(a.out, "SHA256SUMS"), "w", newline="\n") as o:
    for f in ("kjv_words.tsv", "strongs_lexicon.tsv", "summary.json"):
        o.write(hashlib.sha256(open(os.path.join(a.out, f), "rb").read()).hexdigest() + "  " + f + "\n")
print("wrote", a.out)
