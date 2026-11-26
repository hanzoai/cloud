// SPDX-License-Identifier: Apache-2.0

package preble

import (
	"container/list"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"unicode"
	"unicode/utf8"
)

const (
	// DefaultBlockSize is the default number of tokens per paged KV-cache block
	// (matching standard PagedAttention / RadixAttention 16-token blocks).
	DefaultBlockSize = 16

	// MaxPromptTokens caps a single request's prompt length to prevent integer overflow
	// and excessive memory allocation.
	MaxPromptTokens = 1 << 20 // 1,048,576 tokens

	// MaxKVBudgetTokens caps a single GPU replica's KV-cache budget.
	MaxKVBudgetTokens = 1 << 30 // ~1B tokens

	// MaxReplicas caps the cluster size tracked in one scheduler instance.
	MaxReplicas = 256

	// MaxTenants caps distinct (tenant, aclScope) namespaces to bound index overhead.
	MaxTenants = 4096

	// MaxTokenBlocks caps the number of blocks accepted per placement request.
	MaxTokenBlocks = MaxPromptTokens / 1

	kvDomainSeparator = "hanzo-preble-kv-block-v1"
	maxEvictionScan   = 512
)

var (
	// ErrInvalidConfig indicates invalid scheduler, replica, or prefix index configuration.
	ErrInvalidConfig = errors.New("preble: invalid configuration")
	// ErrInvalidRequest indicates a malformed or out-of-bounds placement request.
	ErrInvalidRequest = errors.New("preble: invalid request")
	// ErrNoReplicas indicates no eligible GPU replicas are registered.
	ErrNoReplicas = errors.New("preble: no gpu replicas available")
	// ErrReplicaExists indicates a replica ID is already registered.
	ErrReplicaExists = errors.New("preble: replica already exists")
	// ErrReplicaNotFound indicates the requested replica ID does not exist.
	ErrReplicaNotFound = errors.New("preble: replica not found")
	// ErrRequestNotFound indicates the request ID is unknown or belongs to another tenant.
	ErrRequestNotFound = errors.New("preble: request not found")
	// ErrTenantLimitExceeded indicates the configured namespace cap has been reached.
	ErrTenantLimitExceeded = errors.New("preble: tenant namespace limit exceeded")
)

// BlockHash is a domain-separated, tenant-scoped cumulative SHA-256 hash identifying
// a causal KV-cache prefix block.
type BlockHash [32]byte

// namespaceKey isolates every radix prefix tree root strictly by (tenant, aclScope).
// Tenant A can never traverse, match, or infer blocks belonging to Tenant B or a
// different ACL scope.
type namespaceKey struct {
	tenant   string
	aclScope string
}

// TokenBlock pairs a cumulative tenant-scoped block digest with its exact token count
// (blockSize for full blocks, or 1..blockSize-1 for a trailing partial block).
type TokenBlock struct {
	Hash   BlockHash
	Tokens int
}

func isWordRune(r rune) bool {
	return !unicode.IsSpace(r) && !unicode.IsPunct(r) && !unicode.IsSymbol(r)
}

// TokenizeText deterministically converts UTF-8 prompt text into uint64 token IDs
// using lossless whitespace- and punctuation-preserving subword segmentation (~4 chars
// per token on code/JSON). Every byte of text (including spaces, tabs, indentation, and
// newlines) is preserved in the emitted token sequence so distinct raw prompts never
// collide in KV prefix cache identity.
func TokenizeText(text string) []uint64 {
	if text == "" {
		return nil
	}
	var tokens []uint64
	emit := func(piece string) {
		h := fnv.New64a()
		_, _ = h.Write([]byte(piece))
		tokens = append(tokens, h.Sum64())
	}

	n := len(text)
	for i := 0; i < n; {
		r, sz := utf8.DecodeRuneInString(text[i:])

		// Attach a single leading ASCII space directly before a word rune (matching
		// BPE leading-space tokens such as " world") while preserving exact bytes.
		if r == ' ' && i+sz < n {
			nextR, _ := utf8.DecodeRuneInString(text[i+sz:])
			if isWordRune(nextR) {
				start := i
				i += sz
				runes := 0
				for i < n && runes < 4 {
					wr, wsz := utf8.DecodeRuneInString(text[i:])
					if !isWordRune(wr) {
						break
					}
					i += wsz
					runes++
				}
				emit(text[start:i])
				continue
			}
		}

		// Preserve all whitespace runs explicitly: emit CRLF/LF/CR separately and chunk
		// horizontal whitespace (spaces/tabs/indentation) in up to 4-rune pieces.
		if unicode.IsSpace(r) {
			if r == '\r' && i+sz < n && text[i+sz] == '\n' {
				emit(text[i : i+sz+1])
				i += sz + 1
				continue
			}
			if r == '\n' || r == '\r' {
				emit(text[i : i+sz])
				i += sz
				continue
			}
			start := i
			i += sz
			runes := 1
			for i < n && runes < 4 {
				sr, ssz := utf8.DecodeRuneInString(text[i:])
				if !unicode.IsSpace(sr) || sr == '\n' || sr == '\r' {
					break
				}
				i += ssz
				runes++
			}
			emit(text[start:i])
			continue
		}

		// Emit punctuation and symbol runes individually.
		if unicode.IsPunct(r) || unicode.IsSymbol(r) {
			emit(text[i : i+sz])
			i += sz
			continue
		}

		// Chunk contiguous word runes into <=4-rune subword pieces.
		start := i
		i += sz
		runes := 1
		for i < n && runes < 4 {
			wr, wsz := utf8.DecodeRuneInString(text[i:])
			if !isWordRune(wr) {
				break
			}
			i += wsz
			runes++
		}
		emit(text[start:i])
	}
	return tokens
}

// HashTokenBlocks converts a causal sequence of token IDs into tenant- and ACL-isolated
// cumulative KV block hashes. Each block hash chains the previous block's digest so two
// blocks with identical local tokens at different prefix histories never collide, and
// includes tenant + aclScope domain separation so distinct tenants/ACLs never collide.
func HashTokenBlocks(tenant, aclScope string, tokens []uint64, blockSize int) ([]TokenBlock, error) {
	if tenant == "" {
		return nil, fmt.Errorf("%w: empty tenant", ErrInvalidRequest)
	}
	if blockSize <= 0 {
		blockSize = DefaultBlockSize
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("%w: empty token sequence", ErrInvalidRequest)
	}
	if len(tokens) > MaxPromptTokens {
		return nil, fmt.Errorf("%w: prompt tokens %d exceed max %d", ErrInvalidRequest, len(tokens), MaxPromptTokens)
	}

	numBlocks := (len(tokens) + blockSize - 1) / blockSize
	blocks := make([]TokenBlock, numBlocks)
	var parent BlockHash
	var lenBuf [8]byte
	var tokBuf [8]byte

	for b := 0; b < numBlocks; b++ {
		start := b * blockSize
		end := start + blockSize
		if end > len(tokens) {
			end = len(tokens)
		}
		chunk := tokens[start:end]

		h := sha256.New()
		_, _ = h.Write([]byte(kvDomainSeparator))
		_, _ = h.Write([]byte{0})
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(tenant)))
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write([]byte(tenant))
		_, _ = h.Write([]byte{0})
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(aclScope)))
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write([]byte(aclScope))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(parent[:])
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(chunk)))
		_, _ = h.Write(lenBuf[:])
		for _, tok := range chunk {
			binary.BigEndian.PutUint64(tokBuf[:], tok)
			_, _ = h.Write(tokBuf[:])
		}

		var digest BlockHash
		h.Sum(digest[:0])
		blocks[b] = TokenBlock{
			Hash:   digest,
			Tokens: len(chunk),
		}
		parent = digest
	}
	return blocks, nil
}

// prefixNode is one node in a tenant-isolated radix/block prefix tree.
type prefixNode struct {
	hash      BlockHash
	ns        namespaceKey
	parent    *prefixNode
	children  map[BlockHash]*prefixNode
	depth     int
	cumTokens int
	replicas  map[string]struct{}
	hits      uint64
	activeReq int
}

// cacheEntry represents one KV prefix block stored in a GPU replica's LRU KV cache.
type cacheEntry struct {
	hash      BlockHash
	ns        namespaceKey
	node      *prefixNode
	tokens    int
	pinCount  int
	reuseHits uint64
}

// replicaCache is the exact capacity-bounded LRU KV-cache model for one GPU replica.
type replicaCache struct {
	replicaID     string
	maxTokens     int
	maxBlocks     int
	usedTokens    int
	usedBlocks    int
	pinnedBlocks  int
	pinnedTokens  int
	lru           *list.List // front = MRU, back = LRU
	entries       map[BlockHash]*list.Element
	evictedBlocks uint64
	evictedTokens uint64
	cacheBypassed uint64
	blockHits     uint64
	blockMisses   uint64
}

func newReplicaCache(replicaID string, kvBudgetTokens, blockSize int) *replicaCache {
	maxBlocks := kvBudgetTokens / blockSize
	if maxBlocks < 1 {
		maxBlocks = 1
	}
	return &replicaCache{
		replicaID: replicaID,
		maxTokens: kvBudgetTokens,
		maxBlocks: maxBlocks,
		lru:       list.New(),
		entries:   make(map[BlockHash]*list.Element, min(maxBlocks, 1024)),
	}
}

// PrefixMatch describes the causal KV prefix cache overlap for a prompt across all
// replicas strictly within the caller's (tenant, aclScope) namespace.
type PrefixMatch struct {
	// MatchedBlocks maps replicaID -> longest contiguous cached prefix length in blocks.
	MatchedBlocks map[string]int
	// MatchedTokens maps replicaID -> longest contiguous cached prefix length in tokens.
	MatchedTokens map[string]int
	// MaxMatchedTokens is the maximum reusable prefix token count on any replica.
	MaxMatchedTokens int
	// DeepestHits is the historical hit count at the deepest shared prefix node.
	DeepestHits uint64
	// DeepestActive is the active in-flight request count sharing the deepest prefix node.
	DeepestActive int
	// WarmReplicas is the number of replicas holding the longest shared prefix (>= 50% of MaxMatchedTokens).
	WarmReplicas int
}

// EvictionEstimate predicts the KV eviction cost of inserting neededBlocks onto a replica.
type EvictionEstimate struct {
	BlocksToEvict    int
	PinnedPressure   float64 // fraction [0, 1] of replica KV budget currently pinned by active requests
	AvgEvictedReuse  float64 // average reuse hit count of LRU tail blocks that would be evicted
	WouldOverflowPin bool    // true if unpinned capacity cannot accommodate neededBlocks
}

// PrefixIndex maintains tenant-isolated causal prefix trees synchronized with exact
// per-replica LRU KV caches. Not safe for concurrent use without external locking
// (owned under Scheduler.mu).
type PrefixIndex struct {
	blockSize int
	roots     map[namespaceKey]*prefixNode
	caches    map[string]*replicaCache
	nodeCount int
}

// NewPrefixIndex creates an empty tenant-isolated prefix index with the given KV block size.
func NewPrefixIndex(blockSize int) *PrefixIndex {
	if blockSize <= 0 {
		blockSize = DefaultBlockSize
	}
	return &PrefixIndex{
		blockSize: blockSize,
		roots:     make(map[namespaceKey]*prefixNode),
		caches:    make(map[string]*replicaCache),
	}
}

// BlockSize returns the configured KV block granularity in tokens.
func (idx *PrefixIndex) BlockSize() int { return idx.blockSize }

// NodeCount returns the exact number of live non-root prefix nodes across all tenants.
// Because nodes are pruned as soon as no replica caches them, NodeCount is strictly
// bounded by the aggregate KV block capacity across registered replicas.
func (idx *PrefixIndex) NodeCount() int { return idx.nodeCount }

// TenantCount returns the number of active (tenant, aclScope) namespaces with cached blocks.
func (idx *PrefixIndex) TenantCount() int { return len(idx.roots) }

// AddReplica registers a GPU replica with exact KV-cache token budget.
func (idx *PrefixIndex) AddReplica(replicaID string, kvBudgetTokens int) error {
	if replicaID == "" {
		return fmt.Errorf("%w: empty replica id", ErrInvalidConfig)
	}
	if kvBudgetTokens < idx.blockSize || kvBudgetTokens > MaxKVBudgetTokens {
		return fmt.Errorf("%w: kv budget %d out of bounds [%d, %d]", ErrInvalidConfig, kvBudgetTokens, idx.blockSize, MaxKVBudgetTokens)
	}
	if _, exists := idx.caches[replicaID]; exists {
		return fmt.Errorf("%w: %s", ErrReplicaExists, replicaID)
	}
	idx.caches[replicaID] = newReplicaCache(replicaID, kvBudgetTokens, idx.blockSize)
	return nil
}

// RemoveReplica removes a GPU replica and prunes all prefix tree memberships owned by it.
func (idx *PrefixIndex) RemoveReplica(replicaID string) error {
	cache, ok := idx.caches[replicaID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrReplicaNotFound, replicaID)
	}
	for elem := cache.lru.Front(); elem != nil; elem = elem.Next() {
		entry := elem.Value.(*cacheEntry)
		delete(entry.node.replicas, replicaID)
		idx.pruneUpward(entry.node)
	}
	delete(idx.caches, replicaID)
	return nil
}

// Match walks strictly within the caller's (tenant, aclScope) prefix tree and returns
// the exact longest contiguous cached prefix length on every registered replica.
func (idx *PrefixIndex) Match(tenant, aclScope string, blocks []TokenBlock) PrefixMatch {
	res := PrefixMatch{
		MatchedBlocks: make(map[string]int, len(idx.caches)),
		MatchedTokens: make(map[string]int, len(idx.caches)),
	}
	if len(blocks) == 0 || len(idx.caches) == 0 {
		return res
	}
	ns := namespaceKey{tenant: tenant, aclScope: aclScope}
	root, ok := idx.roots[ns]
	if !ok {
		return res
	}

	// Track which replicas still have an unbroken contiguous prefix match from block 0.
	alive := make(map[string]struct{}, len(idx.caches))
	for id := range idx.caches {
		alive[id] = struct{}{}
	}

	cur := root
	var deepestNode *prefixNode
	for i, blk := range blocks {
		next, found := cur.children[blk.Hash]
		if !found {
			break
		}
		cur = next
		for id := range alive {
			if _, has := cur.replicas[id]; has {
				res.MatchedBlocks[id] = i + 1
				res.MatchedTokens[id] = cur.cumTokens
				if cur.cumTokens > res.MaxMatchedTokens {
					res.MaxMatchedTokens = cur.cumTokens
					deepestNode = cur
				}
			} else {
				delete(alive, id)
			}
		}
		if len(alive) == 0 {
			break
		}
	}

	if deepestNode != nil {
		res.DeepestHits = deepestNode.hits
		res.DeepestActive = deepestNode.activeReq
		threshold := res.MaxMatchedTokens / 2
		if threshold < idx.blockSize {
			threshold = idx.blockSize
		}
		for _, tok := range res.MatchedTokens {
			if tok >= threshold {
				res.WarmReplicas++
			}
		}
	}
	return res
}

// EstimateEviction predicts the KV cache eviction impact of placing a prompt requiring
// neededBlocks new blocks onto replicaID.
func (idx *PrefixIndex) EstimateEviction(replicaID string, neededBlocks int) EvictionEstimate {
	cache, ok := idx.caches[replicaID]
	if !ok {
		return EvictionEstimate{}
	}
	var est EvictionEstimate
	if cache.maxBlocks > 0 {
		est.PinnedPressure = float64(cache.pinnedBlocks) / float64(cache.maxBlocks)
	}
	freeBlocks := cache.maxBlocks - cache.usedBlocks
	if neededBlocks <= freeBlocks {
		return est
	}
	deficit := neededBlocks - freeBlocks
	est.BlocksToEvict = deficit

	unpinnedAvail := cache.maxBlocks - cache.pinnedBlocks
	if neededBlocks > unpinnedAvail {
		est.WouldOverflowPin = true
	}

	// Sample up to maxEvictionScan LRU tail entries to estimate reuse value lost on eviction.
	scanLimit := deficit
	if scanLimit > maxEvictionScan {
		scanLimit = maxEvictionScan
	}
	var sumReuse uint64
	var scanned int
	for elem := cache.lru.Back(); elem != nil && scanned < scanLimit; elem = elem.Prev() {
		entry := elem.Value.(*cacheEntry)
		if entry.pinCount > 0 {
			continue
		}
		sumReuse += entry.reuseHits
		scanned++
	}
	if scanned > 0 {
		est.AvgEvictedReuse = float64(sumReuse) / float64(scanned)
	}
	return est
}

// ReserveAndPin inserts/refreshes blocks into replicaID's LRU KV cache strictly under
// (tenant, aclScope), pins them for the duration of an active request, and returns the
// list of actually pinned BlockHashes plus exact hit/miss/eviction counts.
func (idx *PrefixIndex) ReserveAndPin(tenant, aclScope, replicaID string, blocks []TokenBlock) (pinned []BlockHash, matchedTokens int, evictedBlocks int, err error) {
	cache, ok := idx.caches[replicaID]
	if !ok {
		return nil, 0, 0, fmt.Errorf("%w: %s", ErrReplicaNotFound, replicaID)
	}
	ns := namespaceKey{tenant: tenant, aclScope: aclScope}
	root, ok := idx.roots[ns]
	if !ok {
		if len(idx.roots) >= MaxTenants {
			return nil, 0, 0, ErrTenantLimitExceeded
		}
		root = &prefixNode{
			ns:       ns,
			children: make(map[BlockHash]*prefixNode),
			replicas: make(map[string]struct{}),
		}
		idx.roots[ns] = root
	}

	pinned = make([]BlockHash, 0, len(blocks))
	touchedElems := make([]*list.Element, 0, len(blocks))
	cur := root
	contiguous := true
	cumTokens := 0

	for _, blk := range blocks {
		cumTokens += blk.Tokens
		if elem, exists := cache.entries[blk.Hash]; exists {
			entry := elem.Value.(*cacheEntry)
			cache.blockHits++
			entry.reuseHits++
			if entry.pinCount == 0 {
				cache.pinnedBlocks++
				cache.pinnedTokens += entry.tokens
			}
			entry.pinCount++
			entry.node.hits++
			entry.node.activeReq++
			pinned = append(pinned, blk.Hash)
			touchedElems = append(touchedElems, elem)
			if contiguous {
				matchedTokens += blk.Tokens
			}
			cur = entry.node
			continue
		}

		contiguous = false
		cache.blockMisses++

		// Ensure capacity by evicting unpinned LRU leaf entries.
		if cache.usedBlocks >= cache.maxBlocks {
			if !idx.evictOneLRU(cache) {
				// All blocks in this replica are actively pinned; bypass caching this block
				// and all causally dependent tail blocks to preserve strict KV memory bounds
				// and causal prefix continuity.
				cache.cacheBypassed++
				break
			}
			evictedBlocks++
			// If eviction pruned the last cached block of this same (tenant, aclScope),
			// pruneUpward removed ns from idx.roots; re-register root so newly attached
			// blocks stay reachable by subsequent Match calls.
			if _, ok := idx.roots[ns]; !ok {
				idx.roots[ns] = root
			}
		}

		// Attach or fetch node in the tenant's prefix tree.
		next, exists := cur.children[blk.Hash]
		if !exists {
			next = &prefixNode{
				hash:      blk.Hash,
				ns:        ns,
				parent:    cur,
				children:  make(map[BlockHash]*prefixNode),
				depth:     cur.depth + 1,
				cumTokens: cumTokens,
				replicas:  make(map[string]struct{}),
			}
			cur.children[blk.Hash] = next
			idx.nodeCount++
		}
		next.replicas[replicaID] = struct{}{}
		next.activeReq++

		entry := &cacheEntry{
			hash:     blk.Hash,
			ns:       ns,
			node:     next,
			tokens:   blk.Tokens,
			pinCount: 1,
		}
		elem := cache.lru.PushFront(entry)
		cache.entries[blk.Hash] = elem
		cache.usedBlocks++
		cache.usedTokens += blk.Tokens
		cache.pinnedBlocks++
		cache.pinnedTokens += blk.Tokens
		pinned = append(pinned, blk.Hash)
		touchedElems = append(touchedElems, elem)
		cur = next
	}

	// RadixAttention leaf-first LRU invariant: move touched blocks to MRU front from deepest
	// child up to root (depth 1). This places root/shared prefix blocks strictly closer to
	// Front (MRU) than their causal descendants, guaranteeing lru.Back() always evicts
	// replica-leaves before parents so no orphaned causal KV blocks or dangling interior
	// prefix nodes can ever accumulate.
	for i := len(touchedElems) - 1; i >= 0; i-- {
		cache.lru.MoveToFront(touchedElems[i])
	}

	// Clean up empty root if nothing was cached (e.g., 100% pinned bypass), otherwise
	// ensure the live namespace root remains registered.
	if len(root.children) == 0 {
		delete(idx.roots, ns)
	} else {
		idx.roots[ns] = root
	}
	return pinned, matchedTokens, evictedBlocks, nil
}

// Unpin releases in-flight pins held by a completed request while leaving the KV blocks
// warm in replicaID's LRU cache for future prefix reuse.
func (idx *PrefixIndex) Unpin(replicaID string, pinned []BlockHash) {
	cache, ok := idx.caches[replicaID]
	if !ok {
		return
	}
	for _, h := range pinned {
		elem, exists := cache.entries[h]
		if !exists {
			continue
		}
		entry := elem.Value.(*cacheEntry)
		if entry.pinCount > 0 {
			entry.pinCount--
			if entry.pinCount == 0 {
				cache.pinnedBlocks--
				cache.pinnedTokens -= entry.tokens
			}
		}
		if entry.node.activeReq > 0 {
			entry.node.activeReq--
		}
	}
}

// evictOneLRU evicts the oldest unpinned block from cache and prunes dead radix nodes.
func (idx *PrefixIndex) evictOneLRU(cache *replicaCache) bool {
	for elem := cache.lru.Back(); elem != nil; elem = elem.Prev() {
		entry := elem.Value.(*cacheEntry)
		if entry.pinCount > 0 {
			continue
		}
		cache.lru.Remove(elem)
		delete(cache.entries, entry.hash)
		cache.usedBlocks--
		cache.usedTokens -= entry.tokens
		cache.evictedBlocks++
		cache.evictedTokens += uint64(entry.tokens)

		delete(entry.node.replicas, cache.replicaID)
		idx.pruneUpward(entry.node)
		return true
	}
	return false
}

// pruneUpward removes leaf prefix nodes that are no longer cached on any GPU replica,
// keeping radix tree memory strictly bounded by aggregate KV cache capacity.
func (idx *PrefixIndex) pruneUpward(node *prefixNode) {
	cur := node
	for cur != nil && cur.parent != nil {
		if len(cur.replicas) > 0 || len(cur.children) > 0 {
			return
		}
		parent := cur.parent
		delete(parent.children, cur.hash)
		idx.nodeCount--
		cur = parent
	}
	if cur != nil && cur.parent == nil && len(cur.children) == 0 {
		delete(idx.roots, cur.ns)
	}
}

// ReplicaCacheSnapshot captures aggregate non-tenant-specific KV metrics for one GPU replica.
type ReplicaCacheSnapshot struct {
	MaxTokens     int
	UsedTokens    int
	PinnedTokens  int
	FreeTokens    int
	MaxBlocks     int
	UsedBlocks    int
	EvictedBlocks uint64
	EvictedTokens uint64
	CacheBypassed uint64
	BlockHits     uint64
	BlockMisses   uint64
}

// ReplicaSnapshot returns aggregate KV metrics for replicaID (zero tenant metadata exposed).
func (idx *PrefixIndex) ReplicaSnapshot(replicaID string) (ReplicaCacheSnapshot, bool) {
	c, ok := idx.caches[replicaID]
	if !ok {
		return ReplicaCacheSnapshot{}, false
	}
	free := c.maxTokens - c.usedTokens
	if free < 0 {
		free = 0
	}
	return ReplicaCacheSnapshot{
		MaxTokens:     c.maxTokens,
		UsedTokens:    c.usedTokens,
		PinnedTokens:  c.pinnedTokens,
		FreeTokens:    free,
		MaxBlocks:     c.maxBlocks,
		UsedBlocks:    c.usedBlocks,
		EvictedBlocks: c.evictedBlocks,
		EvictedTokens: c.evictedTokens,
		CacheBypassed: c.cacheBypassed,
		BlockHits:     c.blockHits,
		BlockMisses:   c.blockMisses,
	}, true
}
