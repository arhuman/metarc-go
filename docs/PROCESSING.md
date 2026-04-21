# Archiving a Directory: Processing Points in `metarc-go`

This document details the internal processing flow when archiving a directory using the `metarc-go` tool, focusing on when data is read, transformed, compressed, and written, as well as distinguishing between currently active features and potential, configurable ones.

## Overall Archiving Pipeline

The `metarc-go` archiving process is orchestrated by the `runtime` package, which integrates functionality from the `scan`, `plan`, and `store` packages. The pipeline can be broadly divided into three main stages:

1.  **Initial Scan and Entry Collection:** Discovering files and gathering metadata.
2.  **Parallel Analysis and Hashing:** Reading file content to compute cryptographic hashes in parallel.
3.  **Archive Writing and Transformation:** Applying transformations, compressing data, and writing to the `.marc` archive file.

## Detailed Processing Points

### 1. Initial Scan and Entry Collection (`internal/scan`)

This stage is handled by the `internal/scan` package.

*   **When a file/line is read (initial scan):**
    *   The process begins with `scan.Walk` (for a single source directory) or `scan.WalkMulti` (for multiple source directories).
    *   These functions traverse the specified source directory(ies) using `filepath.WalkDir`.
    *   For each file, directory, or symlink encountered, `scan` collects basic metadata such as the file path, relative path within the source, `os.FileInfo` (mode, size, modification time), and the symlink target (if applicable).
    *   A `marc.Entry` struct is created for each item, encapsulating this metadata.
    *   **Crucially, at this stage, the *content* of regular files is NOT read from disk.** Only filesystem metadata is gathered.
    *   These `marc.Entry` objects are then sent over a Go channel to the `runtime` package for further processing.

*   **Output:** A channel of `marc.Entry` structs.

### 2. Parallel Analysis and Hashing (`internal/runtime`)

This stage is managed by the `internal/runtime` package, specifically within the `runArchivePipeline` function.

*   **Fan-out and Sequencing:**
    *   `marc.Entry` objects received from the `scan` stage are assigned a unique sequence number.
    *   They are then dispatched to a pool of worker goroutines, ensuring that results can be re-ordered later to maintain the original filesystem traversal order.
*   **Worker Pool (Parallel Hashing):**
    *   Multiple goroutines (the number configurable via the `--workers` flag, defaulting to `runtime.NumCPU()`) operate in parallel.
    *   **When a file/line is read (content for hashing):** If an `marc.Entry` represents a regular file (not a directory or symlink) and has a size greater than zero, a worker calls `hashFile(entry.Path)`.
        *   `hashFile` opens the file using `os.Open`.
        *   It then `io.Copy`s the *entire content* of the file into a `blake3.New()` hasher. **This is the first point in the pipeline where the actual file content is read from disk.**
        *   The BLAKE3-256 hash of the file content is computed.
    *   Directories and empty files are assigned a zero hash.
    *   An `AnalyzedEntry` (containing the original `marc.Entry`, the computed BLAKE3 hash, and its sequence number) is sent to a result channel.
*   **Re-ordering:**
    *   Another goroutine buffers `AnalyzedEntry` objects from the parallel workers. It re-orders them based on their sequence numbers to ensure they are processed by the `store.Writer` in the original scan order. These ordered entries are then sent to a final channel (`orderedCh`).

*   **Output:** A channel of `AnalyzedEntry` structs (each with a BLAKE3 hash of its content).

### 3. Archive Writing and Transformation (`internal/store`)

This is the final and most complex stage, handled primarily by the `internal/store` package, using the `store.Writer`.

*   **Archive Initialization (`store.OpenWriter`):**
    *   A new `.marc` archive file is created at the specified `marcPath`.
    *   A temporary SQLite database file is created to serve as the in-memory *catalog* for the archive. This catalog will store all metadata about entries, blobs, and transformations.
    *   SQLite performance pragmas are applied.
    *   The `marc.SchemaDDL` (database schema) is applied to the catalog.
    *   Archive version metadata and a comma-separated list of *all registered transform IDs* (`plan.RegistryIDs()`) are inserted into the catalog's `meta` table. This list is crucial for forward compatibility and understanding which transformations *could* have been applied.
    *   A magic header (`marc.Magic`) is written to the beginning of the `.marc` file.
    *   If solid block compression is enabled (via `--solid-block-size`), a `solidAccumulator` is initialized to buffer blobs.

*   **Dictionary Compression Setup (ZSTD specific):**
    *   **`DictPrescan` Mode (Configurable via `--dict-compress=prescan`):** If enabled and ZSTD compression is used, `store.TrainDictionary` is called *before* any files are written to the archive. This function performs a separate, upfront scan of a source directory (the first one for multi-root archives) to collect samples. These samples are then used to pre-train a ZSTD dictionary. This dictionary (`dictData`) is subsequently used by the `zstd.Encoder` for all blob compression, potentially yielding better compression ratios for highly redundant data.
    *   **`DictSimple` Mode (Configurable via `--dict-compress=simple`):** If enabled and ZSTD compression is used, the `store.Writer` enters an "online dictionary training" mode. It buffers raw content from the first few small blobs (`dictSamples`) as they are processed. Once a sufficient amount of sample data is collected (e.g., `minSamplesForTraining` entries or `32KB` of data), a ZSTD dictionary is trained mid-stream (`trainDictFromSamples`). This newly trained dictionary is then used for all *subsequent* blobs. If not enough samples are collected during the main archiving process, a dictionary might be trained during the `Close()` operation if `dictSamples` exist.
    *   **`DictNone` (Default if `--dict-compress` is not specified):** No dictionary compression is used. The compressor will still be ZSTD (by default), but without a pre-trained or dynamically trained dictionary.

*   **Processing Each `AnalyzedEntry` (`w.WriteEntryWithSHA`):**
    *   The `store.Writer` consumes `AnalyzedEntry` objects from the `orderedCh`.
    *   **Metadata Storage:** For each entry (file, directory, or symlink):
        *   The entry's base name and parent directory path are "interned" into the SQLite catalog's `names` table to optimize storage.
        *   Metadata (parent ID, name ID, file mode, modification time, size, UID, GID, symlink target) is inserted into the `entries` table of the SQLite catalog.
        *   Directories and symlinks are recorded directly in the catalog at this point.
    *   **File Content Handling (`w.writeFileWithSHA` for regular files):**
        *   The original file is `os.Open`ed *again* from disk (`f`).
        *   A `blobSink` helper is created. This `blobSink` is responsible for writing the processed content to the main `.marc` file, handling compression and solid blocks.
        *   **When a transformer is called:**
            *   `plan.Decide(ctx, e, facts)` is invoked to determine if any registered transformer should be applied to the current file. This decision considers the file's metadata (`e`) and pre-computed facts (`marc.Facts`).
            *   The `plan.Disabled` map (populated from the `--disable-transform` CLI flag) is checked. If a transformer's ID is in this map, it is skipped.
            *   If an enabled transformer (`t`) is selected, its `t.Apply(ctx, e, f, sink)` method is called. The transformer reads the file content from `f`, performs its specific transformation, and writes the *transformed content* to the `sink`.
            *   If `t.Apply` returns `marc.ErrNotApplicable` (meaning the transformer decided it couldn't process this specific file), the file is rewound (`f.Seek(0, io.SeekStart)`) and processed as a raw blob.
            *   The `plan.Decision` (indicating whether a transform was applied, its ID, estimated gain/CPU cost, and reason) is recorded.
        *   **When something is compressed with ZSTD:**
            *   The `blobSink` (or its underlying components) handles the actual compression. If the `compressor` is "zstd" (the default), the data written to the `sink` (either raw file content or transformed content) is compressed using a `zstd.Encoder`.
            *   If a dictionary (`dictData`) is available (from `DictPrescan` or `DictSimple` modes), the `zstd.Encoder` is configured to use it, which can significantly improve compression ratios for repetitive data.
            *   **Solid Block Compression (Enabled by default via `--solid-block-size`):** If `solidSize` is enabled, the `blobSink` writes to a `solidAccumulator`. There is only *one* `solidAccumulator` per archive writer, making it a global mechanism for the entire archive. It buffers multiple blobs (regardless of their original file type) and compresses them together into a single ZSTD frame once a certain uncompressed size threshold (`solidSize`, default 4MB) is reached. This "solid block" approach exploits cross-file redundancy, leading to better overall compression. The resulting solid block is then written to the archive.
        *   **When something is written in archive and where:**
            *   The `blobSink` writes the *compressed* (or raw, if no compression) data as "blobs" to the main `.marc` file.
            *   Each blob is prefixed with a chunk header (`marc.ChunkTypeBlob`, followed by its length).
            *   The `blobOff` (current write position in the `.marc` file) is updated.
            *   The BLAKE3 hash of the *uncompressed blob content* is computed (or reused from the pre-computed hash) and stored in the catalog's `blobs.sha` column for content-addressable deduplication.
            *   A `blobID` (an integer identifier for the blob) is returned by the `sink`.
            *   The `entry_blobs` table in the SQLite catalog links the `entry_id` to the `blob_id`(s) and their sequence within the entry (for entries that might consist of multiple blobs).
            *   The `plan_log` table records the `plan.Decision` for each entry, including the `transform_id`, `estimated_gain`, `estimated_cpu`, `applied` status, and `reason`. This table is optionally deleted at the end of the archiving process if `--keep-plan-log` is false.

*   **Finalization (`w.Close` and `w.finalize`):**
    *   **Flush Remaining Solid Blocks:** Any buffered data in the `solidAccumulator` is flushed and written to the archive.
    *   **Commit Catalog Transaction:** The final SQLite transaction for the catalog is committed.
    *   **Serialize Catalog:** The temporary SQLite database (the catalog) is `VACUUM INTO` a new temporary file, effectively serializing its entire content into a single file.
    *   **Compress Catalog:** The serialized catalog bytes are compressed using ZSTD (the catalog itself is *always* compressed, even if `--final-compressor=none` was used for blobs).
    *   **Write Catalog Chunk:** The compressed catalog is written as a `marc.ChunkTypeCatalog` chunk to the `.marc` file, positioned *after* all blob data.
    *   **Compute Final Checksum:** A BLAKE3 checksum of *all data written to the `.marc` file so far* (including the magic header, all blob chunks, and the catalog chunk) is computed.
    *   **Write Footer:** A `marc.Footer` struct (containing the catalog offset, blob region offset, and the final BLAKE3 checksum) is written to the very end of the `.marc` file. This footer itself is *not* included in the final checksum.
    *   **Cleanup:** All temporary SQLite files are removed.

### Side Structures Used

*   **`.marc` file:** The main archive file.
    *   **Layout:** `[Magic 8B][Blob chunks...][Catalog chunk][Footer 24B]`
    *   **Blob chunks:** Contain compressed (or raw) file content.
    *   **Catalog chunk:** Contains the compressed SQLite database.
    *   **Footer:** Contains metadata about the archive structure and a final checksum.
*   **SQLite Database (temporary file):** The in-memory catalog, serialized at the end.
    *   **`entries` table:** Stores metadata for each file, directory, symlink.
    *   **`names` table:** Stores unique file/directory names, referenced by `name_id`.
    *   **`entry_blobs` table:** Links entries to their corresponding blobs.
    *   **`blobs` table:** Stores metadata about each blob (offset, size, compressed size, BLAKE3 hash).
    *   **`meta` table:** Stores archive-level metadata (version, registered transforms, dictionary data).
    *   **`plan_log` table:** Records decisions made by the `plan` package for each entry (transform applied, reason, estimated gain, etc.).
*   **`marc.Entry`:** Struct holding basic file/directory metadata from the scan phase.
*   **`AnalyzedEntry`:** `marc.Entry` augmented with its BLAKE3 hash and sequence number.
*   **`plan.Decision`:** Struct holding the outcome of a transformer's decision.
*   **`plan.Disabled` map:** Global map used to track explicitly disabled transformers.
*   **ZSTD Dictionary (`dictData`):** Byte slice containing the trained ZSTD dictionary.
*   **`zstd.Encoder`:** Used for ZSTD compression.
*   **`blake3.Hasher`:** Used for computing BLAKE3 hashes of file contents and the overall archive.
*   **`blobSink`:** Internal helper for writing data to the archive, handling compression and solid blocks.
*   **`solidAccumulator`:** Buffers multiple blobs for solid block compression.
*   **Channels and `sync.WaitGroup`:** Standard Go concurrency primitives for managing parallel operations.

### Current Implementation vs. Potential Usage

#### Currently Used (Active Features)

The following features are actively implemented and enabled by default or through standard configuration:

*   **File Scanning:** `scan.Walk` and `scan.WalkMulti` are used to discover files and collect metadata.
*   **Parallel Hashing:** BLAKE3 hashing of file contents is a core part of the pipeline, enabling content-addressable deduplication.
*   **ZSTD Compression:** This is the default and primary compression method for blobs.
*   **Solid Block Compression:** Enabled by default (`--solid-block-size=4MB`) and actively used to improve compression by grouping multiple blobs into shared ZSTD frames, exploiting cross-file redundancy.
*   **ZSTD Dictionary Compression:** Both `prescan` and `simple` dictionary modes are implemented and can be activated via the `--dict-compress` flag.
*   **Transformation Pipeline:** The `plan.Decide` mechanism is active, and any *enabled* registered transformer will be considered and applied if applicable.
    *   **`go-line-subst/v1` (GoLineSubst):** This lossless transformer is enabled by default. It replaces frequently repeated lines in `.go` files with 2-byte tokens to enhance ZSTD compression. Its `Apply` method is called *per file* after `plan.Decide` determines it's applicable. Once called, its `Apply` method then reads the *entire content of that specific .go file line by line* to perform its token substitution. It is not called on each line during the initial file system scan.
    *   **`dedup/v1` (Dedup):** This lossless transformer is enabled by default. It leverages content-addressable storage to deduplicate identical file contents, storing them only once. Its `Apply` method is called *per file* when it's deemed applicable by `plan.Decide`. Its `Apply` method then streams the *entire content of that file* to the internal `BlobSink`. The actual deduplication logic (checking if the content's BLAKE3 hash already exists in the archive) happens within the `BlobSink`, which decides whether to write the content as a new blob or simply reference an existing one.
*   **SQLite Catalog:** Actively used to store all archive metadata in a structured, queryable format.
*   **BLAKE3 Archive Checksum:** A final BLAKE3 checksum of the entire archive content (excluding the footer) is computed and stored in the footer for integrity verification.

#### Could Be Used (Inactive/Configurable Features)

The following features are either configurable options or exist in the codebase but are not enabled by default due to their nature (e.g., lossy transformations):

*   **No Compression:** The `--final-compressor=none` flag allows completely bypassing ZSTD compression for blobs, storing them raw.
*   **No Solid Block Compression:** The `--no-solid` flag disables solid block compression, reverting to per-blob compression.
*   **Different Dictionary Modes:** The choice between `prescan`, `simple`, or no dictionary is configurable via `--dict-compress`.
*   **Lossy Transformers:** The codebase contains references to other transformers that are currently *not* enabled by default because they are "lossy" (i.e., they discard original formatting and restore a canonical form, not the original bytes). These include:
    *   `license-canonical/v1`
    *   `json-canonical/v1`
    *   `log-template/v1`
    These transformers would need to be explicitly enabled (e.g., by modifying the `internal/plan/transforms.go` file or through a future CLI flag if implemented) to be used. Their purpose would be to normalize content for potentially better compression or easier analysis, but at the cost of not being able to perfectly reconstruct the original byte-for-byte content.
*   **`WriteEntry` vs. `WriteEntryWithSHA`:** The `store.Writer` has both methods. `WriteEntryWithSHA` is used by the `runtime` package because it pre-computes the SHA in parallel. `WriteEntry` would compute the SHA sequentially within the `store.Writer` if used directly, which is less efficient for archiving.

## Future Considerations and Potential Improvements

Based on user feedback and general archiving best practices, the following ideas have been evaluated for potential future improvements to the `metarc-go` archiving process. They are ranked by priority (impact vs. complexity).

### 1. Pre-Transform Dedup Check (High priority)

*   **Current Behavior:** `GoLineSubst` runs before `Dedup` in the Registry. Since `plan.Decide()` uses first-match-wins, `GoLineSubst` handles all `.go` files. The substituted content is then written through `BlobSink`, which performs dedup internally on the *substituted* content hash.
*   **Problem:** Running `GoLineSubst` on a file that is an exact duplicate of an already-archived file is wasted work — the file is read, processed line by line, buffered, all before the sink discovers it's a duplicate. The pre-computed BLAKE3 hash (from the parallel hashing phase) cannot be used for dedup because `GoLineSubst` transforms the content, producing a different hash. Dedup still works between identical `.go` files (both produce the same substituted content), but the I/O and CPU cost of substitution is paid for nothing on duplicates.
*   **Complication:** Simply swapping the Registry order (Dedup first) would disable `GoLineSubst` entirely — `plan.Decide()` is first-match-wins with no chaining, and `Dedup.Applicable()` returns true for all files > 0B.
*   **Correct fix:** Add a dedup check in `writeFileWithSHA` *before* calling `plan.Decide()`, using the pre-computed SHA via `sink.Reuse(sha)`. If the file is already in the archive, skip all transforms and reference the existing blob. Only if the file is NOT a duplicate, proceed to `plan.Decide()` for transform selection.
*   **Impact:** Eliminates wasted I/O and CPU on duplicate files. On corpora with high duplication (vendored deps, monorepos), this avoids reading and transforming thousands of files that could be resolved with a single hash lookup.
*   **Complexity:** Low — a few lines in `writeFileWithSHA`, before the existing `plan.Decide()` call.
*   **Recommendation:** Implement first. Highest value-to-effort ratio.

### 2. Sorting Files by Extension (High priority)

*   **Current Behavior:** Files are processed in filesystem walk order (alphabetical within a directory, but not globally sorted by type).
*   **Suggestion:** Sort files by extension before the write phase.
*   **Evaluation:** By feeding the compressor a stream of similar data (e.g., all `.go` files, then all `.md` files), the zstd model within each solid block becomes highly specialized for that data type. This is "compressor context locality." The primary trade-off is buffering all entries in memory before processing, but `marc.Entry` structs are lightweight metadata (no content).
*   **Constraint:** Parent directory entries must still precede their children for `parent_id` FK validity. The sort should process directories first (in walk order), then sort files by extension.
*   **Impact:** High — this exploits the same principle as type-grouped solid blocks but with zero architectural change to the accumulator.
*   **Complexity:** Low-medium — sort the `orderedCh` output buffer, respecting the directory-before-files constraint.
*   **Recommendation:** Implement after #1. Best compression improvement available without new architecture.

### 3. Multiple Solid Blocks by Type (Low priority)

*   **Current Behavior:** One global `solidAccumulator` buffers blobs regardless of type.
*   **Suggestion:** Separate solid blocks for binary vs. source code.
*   **Evaluation:** Different data types have different statistical properties. Grouping by type could improve compression within each block. However, if #2 (sorting by extension) is implemented, files are already grouped by type in the stream — the single accumulator naturally produces type-homogeneous blocks. Multiple accumulators would only help if the block size threshold is smaller than the total size of each type group, which is rare in practice.
*   **Impact:** Marginal — most of the benefit is captured by #2.
*   **Complexity:** High — requires file classification, multiple accumulators, routing logic.
*   **Recommendation:** Defer. Only revisit if benchmarks after #2 show significant cross-type mixing within solid blocks.

### 4. Default Dictionary Strategy: `DictSimple` (Not recommended)

*   **Current Behavior:** Default is `DictNone`. `DictSimple` is opt-in.
*   **Suggestion:** Make `DictSimple` the default.
*   **Evaluation:** Measured gains from dictionary compression are marginal (~0.4% on real-world repositories, per ADR-011). Solid blocks already exploit cross-file redundancy more effectively. The dictionary adds 32 KB of overhead that often negates the savings. `DictSimple` also adds CPU overhead for mid-stream training.
*   **Impact:** Negligible — measured at ~0.4%.
*   **Complexity:** Low (flag change), but introduces a default behavior that is demonstrably not worth its cost.
*   **Recommendation:** Do not change the default. Keep as opt-in for users who want to experiment. The feature is a candidate for removal (ADR-011).
