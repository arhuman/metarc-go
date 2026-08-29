package store

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// dictBlockSize is small enough that a modest fixture spans several blocks,
// which is the only shape an inter-block dictionary applies to.
const dictBlockSize = 64 << 10

// dictFixtureFile is one file written to a dictionary fixture, in order.
type dictFixtureFile struct {
	name    string
	content []byte
}

// eligibleDictExts returns the extensions whose fixture files total more than
// blockSize, mirroring what runtime.solidDictExtensions derives from a scan. The
// writer takes the set rather than computing it, so a fixture that wants a
// dictionary has to qualify for one on the same rule a real corpus does.
func eligibleDictExts(files []dictFixtureFile, blockSize int64) map[string]bool {
	totals := make(map[string]int64)
	for _, f := range files {
		totals[filepath.Ext(f.name)] += int64(len(f.content))
	}
	exts := make(map[string]bool)
	for ext, total := range totals {
		if total > blockSize {
			exts[ext] = true
		}
	}
	return exts
}

// writeOrderedSolidFixture archives files in the given order into a solid
// archive at blockSize. Order matters here in a way the map-based helpers cannot
// express: extension groups are formed from the order the writer sees.
func writeOrderedSolidFixture(t *testing.T, blockSize int64, files []dictFixtureFile) (marcPath, srcDir string) {
	t.Helper()
	return writeOrderedSolidFixtureWithExts(t, blockSize, files, eligibleDictExts(files, blockSize))
}

// writeOrderedSolidFixtureWithExts is writeOrderedSolidFixture with an explicit
// eligible-extension set, for tests that pin what the gate does to a set the
// size rule would not have produced.
func writeOrderedSolidFixtureWithExts(t *testing.T, blockSize int64, files []dictFixtureFile, exts map[string]bool) (marcPath, srcDir string) {
	t.Helper()
	tmp := t.TempDir()
	srcDir = filepath.Join(tmp, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	marcPath = filepath.Join(tmp, "dict.marc")
	w, err := OpenWriter(marcPath, WithSolidBlockSize(blockSize), WithSolidDictExtensions(exts))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, f := range files {
		e := createTestFile(t, srcDir, f.name, f.content)
		if err := w.WriteEntry(ctx, e); err != nil {
			t.Fatalf("write %s: %v", f.name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return marcPath, srcDir
}

// blockDict is one solid block's dictionary state as the catalog records it.
type blockDict struct {
	blockID int64
	dictID  sql.NullInt64
}

// queryBlockDicts returns every solid block in the archive with the dictionary
// block it claims, in block id order.
func queryBlockDicts(t *testing.T, r *Reader) []blockDict {
	t.Helper()
	rows, err := r.db.Query(
		`SELECT block_id, dict_block_id FROM blobs WHERE block_id IS NOT NULL GROUP BY block_id ORDER BY block_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()

	var out []blockDict
	for rows.Next() {
		var b blockDict
		if err := rows.Scan(&b.blockID, &b.dictID); err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// assertRoundTrip reads every entry of the archive back and compares it byte for
// byte with the file on disk that produced it. This is the gate on every
// dictionary test: a dictionary mismatch is a silent corruption in the worst
// case, so byte-exactness is checked rather than assumed from a clean error.
func assertRoundTrip(t *testing.T, marcPath, srcDir string) {
	t.Helper()
	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	rows, err := r.db.Query(`
		SELECT n.name, eb.blob_id
		FROM entries e
		JOIN names n ON n.id = e.name_id
		JOIN entry_blobs eb ON eb.entry_id = e.id
		WHERE eb.seq = 0
		ORDER BY e.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()

	seen := 0
	for rows.Next() {
		var name string
		var blobID int64
		if err := rows.Scan(&name, &blobID); err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join(srcDir, name))
		if err != nil {
			t.Fatal(err)
		}
		rc, err := r.OpenBlob(blobID)
		if err != nil {
			t.Fatalf("OpenBlob for %s: %v", name, err)
		}
		got, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s round-trip mismatch: got %d bytes, want %d", name, len(got), len(want))
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen == 0 {
		t.Fatal("no entries round-tripped")
	}
}

// TestSolidDict_singleGroupChainsToFirstBlock pins the core contract of the
// inter-block dictionary: inside one extension group, block 0 is plain and every
// later block is compressed against block 0, never against its predecessor.
func TestSolidDict_singleGroupChainsToFirstBlock(t *testing.T) {
	var files []dictFixtureFile
	for i := range 40 {
		files = append(files, dictFixtureFile{
			name:    fmt.Sprintf("f%03d.txt", i),
			content: compressibleBytes(24<<10, int64(i)),
		})
	}

	marcPath, srcDir := writeOrderedSolidFixture(t, dictBlockSize, files)

	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	blocks := queryBlockDicts(t, r)
	_ = r.Close()

	if len(blocks) < 3 {
		t.Fatalf("fixture produced %d blocks, want at least 3", len(blocks))
	}
	if blocks[0].dictID.Valid {
		t.Errorf("block %d is the group's first block, want NULL dict_block_id, got %d",
			blocks[0].blockID, blocks[0].dictID.Int64)
	}
	for _, b := range blocks[1:] {
		if !b.dictID.Valid {
			t.Errorf("block %d: want dict_block_id %d, got NULL", b.blockID, blocks[0].blockID)
			continue
		}
		if b.dictID.Int64 != blocks[0].blockID {
			t.Errorf("block %d: dict_block_id is %d, want the group's first block %d",
				b.blockID, b.dictID.Int64, blocks[0].blockID)
		}
	}

	assertRoundTrip(t, marcPath, srcDir)
}

// TestSolidDict_twoGroupsUseTheirOwnFirstBlock pins group isolation: a block
// must point at the first block of its own extension group. Pointing across
// groups would still decode, since any block is a legal raw dictionary, so only
// an explicit assertion catches a group boundary that failed to reset.
func TestSolidDict_twoGroupsUseTheirOwnFirstBlock(t *testing.T) {
	// Blocks of 4 MiB, so minBlockSize (1 MiB) is a genuine fraction of a block
	// and an extension change flushes instead of pooling the two extensions into
	// one block, which is what makes this two groups rather than one.
	const blockSize = 4 << 20
	var files []dictFixtureFile
	for i := range 6 {
		files = append(files, dictFixtureFile{
			name:    fmt.Sprintf("a%03d.aaa", i),
			content: compressibleBytes(2<<20, int64(i)),
		})
	}
	for i := range 6 {
		files = append(files, dictFixtureFile{
			name:    fmt.Sprintf("b%03d.bbb", i),
			content: compressibleBytes(2<<20, int64(1000+i)),
		})
	}

	marcPath, srcDir := writeOrderedSolidFixture(t, blockSize, files)

	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	blocks := queryBlockDicts(t, r)
	_ = r.Close()

	// Each distinct dictionary block starts a group; a block whose dict_block_id
	// is NULL is itself a group head.
	var heads []int64
	for _, b := range blocks {
		if !b.dictID.Valid {
			heads = append(heads, b.blockID)
		}
	}
	if len(heads) < 2 {
		t.Fatalf("fixture produced %d group heads across %d blocks, want at least 2 (one per extension)",
			len(heads), len(blocks))
	}

	// The head governing a block is the greatest head at or before it.
	for _, b := range blocks {
		if !b.dictID.Valid {
			continue
		}
		idx := sort.Search(len(heads), func(i int) bool { return heads[i] > b.blockID }) - 1
		if idx < 0 {
			t.Fatalf("block %d precedes every group head", b.blockID)
		}
		if b.dictID.Int64 != heads[idx] {
			t.Errorf("block %d: dict_block_id is %d, want its own group head %d",
				b.blockID, b.dictID.Int64, heads[idx])
		}
	}

	assertRoundTrip(t, marcPath, srcDir)
}

// TestSolidDict_oversizedBlockStaysPlain pins the correctness constraint behind
// the oversized exclusion: a blob larger than the block size becomes a
// sole-member block that the read path streams with no dictionary available, so
// it must be neither dictionary-compressed nor retained as a dictionary.
func TestSolidDict_oversizedBlockStaysPlain(t *testing.T) {
	var files []dictFixtureFile
	for i := range 12 {
		files = append(files, dictFixtureFile{
			name:    fmt.Sprintf("f%03d.txt", i),
			content: compressibleBytes(24<<10, int64(i)),
		})
	}
	// Above the block size on its own, so it becomes a standalone block.
	files = append(files, dictFixtureFile{
		name:    "big.txt",
		content: compressibleBytes(3*dictBlockSize, 99),
	})
	for i := 12; i < 24; i++ {
		files = append(files, dictFixtureFile{
			name:    fmt.Sprintf("f%03d.txt", i),
			content: compressibleBytes(24<<10, int64(i)),
		})
	}

	marcPath, srcDir := writeOrderedSolidFixture(t, dictBlockSize, files)

	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	blocks := queryBlockDicts(t, r)

	if len(r.soleBlocks) == 0 {
		_ = r.Close()
		t.Fatal("fixture produced no sole-member block; the oversized path did not trigger")
	}
	for _, b := range blocks {
		if !r.soleBlocks[b.blockID] {
			continue
		}
		if b.dictID.Valid {
			t.Errorf("sole-member block %d carries dict_block_id %d, want NULL", b.blockID, b.dictID.Int64)
		}
		// Nothing may name it as a dictionary either: the streaming read path
		// would have no bytes to decode it into.
		for _, other := range blocks {
			if other.dictID.Valid && other.dictID.Int64 == b.blockID {
				t.Errorf("block %d names sole-member block %d as its dictionary", other.blockID, b.blockID)
			}
		}
	}
	_ = r.Close()

	assertRoundTrip(t, marcPath, srcDir)
}

// TestSolidDict_rejectsDictionaryChain pins the bounded dependency depth. The
// write path only ever makes a group's plain first block a dictionary, so a
// dictionary block that itself claims one can only come from a forged catalog;
// the reader must say so rather than recurse or panic.
func TestSolidDict_rejectsDictionaryChain(t *testing.T) {
	var files []dictFixtureFile
	for i := range 40 {
		files = append(files, dictFixtureFile{
			name:    fmt.Sprintf("f%03d.txt", i),
			content: compressibleBytes(24<<10, int64(i)),
		})
	}
	marcPath, _ := writeOrderedSolidFixture(t, dictBlockSize, files)

	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	blocks := queryBlockDicts(t, r)
	if len(blocks) < 3 {
		_ = r.Close()
		t.Fatalf("fixture produced %d blocks, want at least 3", len(blocks))
	}
	head := blocks[0].blockID

	// Point the group head at a later block of its own group: a chain the write
	// path cannot produce.
	if _, err := r.db.Exec(
		`UPDATE blobs SET dict_block_id = ? WHERE block_id = ?`, blocks[1].blockID, head,
	); err != nil {
		_ = r.Close()
		t.Skipf("catalog opened read-only, cannot craft the chain: %v", err)
	}

	// A blob of a block that depends on the now-chained head.
	var blobID int64
	if err := r.db.QueryRow(
		`SELECT id FROM blobs WHERE block_id = ? ORDER BY id LIMIT 1`, blocks[2].blockID,
	).Scan(&blobID); err != nil {
		_ = r.Close()
		t.Fatal(err)
	}

	r.dictBlock, r.dictBlockID = nil, 0
	_, err = r.OpenBlob(blobID)
	_ = r.Close()
	if err == nil {
		t.Fatal("OpenBlob accepted a dictionary block that claims a dictionary of its own")
	}
	if !strings.Contains(err.Error(), "itself claims dictionary block") {
		t.Fatalf("error does not name the chain: %v", err)
	}
}

// TestSolidDict_readsArchiveWithoutTheColumn pins backward compatibility: an
// archive written before dict_block_id existed has no such column, and the
// column probe must keep it on the plain path rather than failing its queries.
func TestSolidDict_readsArchiveWithoutTheColumn(t *testing.T) {
	var files []dictFixtureFile
	for i := range 16 {
		files = append(files, dictFixtureFile{
			name:    fmt.Sprintf("f%03d.txt", i),
			content: compressibleBytes(24<<10, int64(i)),
		})
	}
	// One block only, so no block needs a dictionary and dropping the column
	// leaves an archive identical to what the previous format produced.
	marcPath, srcDir := writeOrderedSolidFixture(t, 4<<20, files)

	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	if !r.hasDictBlocks {
		_ = r.Close()
		t.Fatal("fresh archive has no dict_block_id column")
	}
	if _, err := r.db.Exec(`ALTER TABLE blobs DROP COLUMN dict_block_id`); err != nil {
		_ = r.Close()
		t.Skipf("cannot drop the column to simulate an older archive: %v", err)
	}
	// Re-probe the way an open would, then read through the plain path.
	r.hasDictBlocks = detectBlobColumn(r.db, "dict_block_id")
	if r.hasDictBlocks {
		_ = r.Close()
		t.Fatal("column probe still reports dict_block_id after the column was dropped")
	}

	rows, err := r.db.Query(`SELECT eb.blob_id, n.name FROM entries e
		JOIN names n ON n.id = e.name_id
		JOIN entry_blobs eb ON eb.entry_id = e.id WHERE eb.seq = 0 ORDER BY e.id`)
	if err != nil {
		_ = r.Close()
		t.Fatal(err)
	}
	checked := 0
	for rows.Next() {
		var blobID int64
		var name string
		if err := rows.Scan(&blobID, &name); err != nil {
			t.Fatal(err)
		}
		rc, err := r.OpenBlob(blobID)
		if err != nil {
			t.Fatalf("OpenBlob without the column: %v", err)
		}
		got, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join(srcDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s mismatch reading without the column", name)
		}
		checked++
	}
	_ = rows.Close()
	_ = r.Close()
	if checked == 0 {
		t.Fatal("no blob read without the column")
	}
}

// TestSolidDict_blockFilledByOneBlobStaysPlain pins the sole-member rule against
// the case that is not the oversized path: a blob no larger than the block size
// can still fill a block on its own, right after a small pooled block flushed.
// The reader streams any sole-member block with no dictionary available, so such
// a block must be plain however it came to hold one blob.
//
// This is a regression test: the first implementation excluded only the oversized
// path and produced an unreadable archive on the kubernetes corpus, where a
// 33 MB .idx file followed a 644 KB pooled block.
func TestSolidDict_blockFilledByOneBlobStaysPlain(t *testing.T) {
	const blockSize = 4 << 20
	var files []dictFixtureFile
	// A group large enough to establish a dictionary.
	for i := range 6 {
		files = append(files, dictFixtureFile{
			name:    fmt.Sprintf("a%03d.aaa", i),
			content: compressibleBytes(2<<20, int64(i)),
		})
	}
	// A pooled block under minBlockSize, so the extension change does not flush.
	files = append(files, dictFixtureFile{name: "small.bbb", content: compressibleBytes(64<<10, 500)})
	// Then a blob that fills a whole block on its own without exceeding it.
	files = append(files, dictFixtureFile{name: "solo.ccc", content: compressibleBytes(blockSize-4096, 501)})
	for i := range 4 {
		files = append(files, dictFixtureFile{
			name:    fmt.Sprintf("d%03d.ddd", i),
			content: compressibleBytes(2<<20, int64(600+i)),
		})
	}

	marcPath, srcDir := writeOrderedSolidFixture(t, blockSize, files)

	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	blocks := queryBlockDicts(t, r)
	soleSeen := 0
	for _, b := range blocks {
		if !r.soleBlocks[b.blockID] {
			continue
		}
		soleSeen++
		if b.dictID.Valid {
			t.Errorf("sole-member block %d carries dict_block_id %d, want NULL", b.blockID, b.dictID.Int64)
		}
		for _, other := range blocks {
			if other.dictID.Valid && other.dictID.Int64 == b.blockID {
				t.Errorf("block %d names sole-member block %d as its dictionary", other.blockID, b.blockID)
			}
		}
	}
	_ = r.Close()
	if soleSeen == 0 {
		t.Fatal("fixture produced no sole-member block; it does not cover the regression")
	}

	assertRoundTrip(t, marcPath, srcDir)
}

// TestSolidDictGate_groupBelowThresholdGetsNoDictionary pins the gate: an
// extension group whose files do not exceed the block size cannot produce a
// second block, so it must retain no dictionary and record no dict_block_id.
// Retaining one would cost a block copy and a second level-11 encoder for a gain
// that is arithmetically unavailable.
func TestSolidDictGate_groupBelowThresholdGetsNoDictionary(t *testing.T) {
	const blockSize = 4 << 20
	// One extension totalling under a block, so the size rule admits nothing.
	var files []dictFixtureFile
	for i := range 6 {
		files = append(files, dictFixtureFile{
			name:    fmt.Sprintf("f%03d.txt", i),
			content: compressibleBytes(256<<10, int64(i)),
		})
	}

	if exts := eligibleDictExts(files, blockSize); len(exts) != 0 {
		t.Fatalf("fixture is eligible (%v), so it does not cover the gate", exts)
	}

	marcPath, srcDir := writeOrderedSolidFixture(t, blockSize, files)

	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	blocks := queryBlockDicts(t, r)
	_ = r.Close()

	if len(blocks) == 0 {
		t.Fatal("fixture produced no solid block")
	}
	for _, b := range blocks {
		if b.dictID.Valid {
			t.Errorf("block %d carries dict_block_id %d, want NULL for an ineligible group",
				b.blockID, b.dictID.Int64)
		}
	}

	assertRoundTrip(t, marcPath, srcDir)
}

// TestSolidDictGate_groupAboveThresholdStillChains pins that the gate does not
// break the feature it guards: a group whose files exceed the block size keeps
// chaining every later block to its first.
func TestSolidDictGate_groupAboveThresholdStillChains(t *testing.T) {
	// 4 MiB blocks so minBlockSize (1 MiB) is a genuine fraction of a block; at
	// 64 KiB it would equal the block size and collapse every extension into one
	// group, which would not test a per-group gate.
	const blockSize = 4 << 20
	var files []dictFixtureFile
	for i := range 6 {
		files = append(files, dictFixtureFile{
			name:    fmt.Sprintf("a%03d.aaa", i),
			content: compressibleBytes(2<<20, int64(i)),
		})
	}

	exts := eligibleDictExts(files, blockSize)
	if !exts[".aaa"] {
		t.Fatalf("fixture does not qualify .aaa (%v), so it does not cover the chain", exts)
	}

	marcPath, srcDir := writeOrderedSolidFixture(t, blockSize, files)

	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	blocks := queryBlockDicts(t, r)
	_ = r.Close()

	if len(blocks) < 2 {
		t.Fatalf("fixture produced %d blocks, want at least 2", len(blocks))
	}
	if blocks[0].dictID.Valid {
		t.Errorf("block %d is the group head, want NULL dict_block_id, got %d",
			blocks[0].blockID, blocks[0].dictID.Int64)
	}
	for _, b := range blocks[1:] {
		if !b.dictID.Valid {
			t.Errorf("block %d: want dict_block_id %d, got NULL", b.blockID, blocks[0].blockID)
			continue
		}
		if b.dictID.Int64 != blocks[0].blockID {
			t.Errorf("block %d: dict_block_id is %d, want the group head %d",
				b.blockID, b.dictID.Int64, blocks[0].blockID)
		}
	}

	assertRoundTrip(t, marcPath, srcDir)
}

// TestSolidDictGate_gatesPerGroup pins that the gate is per group rather than per
// archive: in one tree, the extension that exceeds the block size chains while
// the one that does not stays plain.
func TestSolidDictGate_gatesPerGroup(t *testing.T) {
	const blockSize = 4 << 20
	var files []dictFixtureFile
	// Eligible: 12 MiB of .aaa spans three blocks.
	for i := range 6 {
		files = append(files, dictFixtureFile{
			name:    fmt.Sprintf("a%03d.aaa", i),
			content: compressibleBytes(2<<20, int64(i)),
		})
	}
	// Ineligible: 3 MiB of .bbb cannot fill a block, but is over minBlockSize so
	// it flushes as its own group rather than pooling into the .aaa tail.
	for i := range 2 {
		files = append(files, dictFixtureFile{
			name:    fmt.Sprintf("b%03d.bbb", i),
			content: compressibleBytes(1536<<10, int64(1000+i)),
		})
	}

	exts := eligibleDictExts(files, blockSize)
	if !exts[".aaa"] || exts[".bbb"] {
		t.Fatalf("fixture eligibility is %v, want .aaa only", exts)
	}

	marcPath, srcDir := writeOrderedSolidFixture(t, blockSize, files)

	r, err := OpenReader(marcPath)
	if err != nil {
		t.Fatal(err)
	}
	blocks := queryBlockDicts(t, r)
	_ = r.Close()

	// The eligible group chains, so at least one block names a dictionary; the
	// ineligible one adds blocks that name none.
	var chained, plain int
	for _, b := range blocks {
		if b.dictID.Valid {
			chained++
		} else {
			plain++
		}
	}
	if chained == 0 {
		t.Errorf("no block chained: the eligible .aaa group did not use a dictionary (%d blocks)", len(blocks))
	}
	// One head for .aaa plus every block of .bbb.
	if plain < 2 {
		t.Errorf("only %d plain block(s): the ineligible .bbb group appears to have chained", plain)
	}

	assertRoundTrip(t, marcPath, srcDir)
}
