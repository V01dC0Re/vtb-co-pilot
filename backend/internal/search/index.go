// Package search implements an immutable in-memory lexical index without external services.
package search

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"vtb-copilot-backend/internal/domain"
)

const DefaultThreshold = 0.20

type entry struct {
	chunk  domain.Chunk
	vector map[string]float64
	norm   float64
}

// Index is read-only after Load and safe for concurrent Search calls.
type Index struct {
	entries   []entry
	idf       map[string]float64
	threshold float64
}

// Load reads all chunks once. Missing, malformed or empty data prevents startup.
func Load(path string, threshold float64) (*Index, error) {
	if math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold <= 0 || threshold > 1 {
		return nil, fmt.Errorf("search threshold must be in (0, 1]")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read rules: %w", err)
	}
	var payload struct {
		Chunks []domain.Chunk `json:"chunks"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("parse rules: %w", err)
	}
	if len(payload.Chunks) == 0 {
		return nil, fmt.Errorf("rules contain no chunks")
	}
	idx := &Index{threshold: threshold, idf: make(map[string]float64)}
	seen := make(map[string]bool)
	df := make(map[string]int)
	for _, chunk := range payload.Chunks {
		if chunk.ID == "" || chunk.Citation == "" || strings.TrimSpace(chunk.Text) == "" || seen[chunk.ID] {
			return nil, fmt.Errorf("invalid or duplicate chunk %q", chunk.ID)
		}
		seen[chunk.ID] = true
		vector := frequencies(chunk.Text + " " + chunk.Citation)
		for token := range vector {
			df[token]++
		}
		idx.entries = append(idx.entries, entry{chunk: chunk, vector: vector})
	}
	for token, count := range df {
		idx.idf[token] = 1 + math.Log(float64(len(idx.entries)+1)/float64(count+1))
	}
	for i := range idx.entries {
		idx.entries[i].norm = weight(idx.entries[i].vector, idx.idf)
	}
	return idx, nil
}

func (idx *Index) Len() int { return len(idx.entries) }

// Chunks returns all source chunks in file order without exposing the index storage.
func (idx *Index) Chunks() []domain.Chunk {
	chunks := make([]domain.Chunk, 0, len(idx.entries))
	for _, item := range idx.entries {
		chunks = append(chunks, item.chunk)
	}
	return chunks
}

func (idx *Index) ChunkByID(id string) (domain.Chunk, bool) {
	for _, item := range idx.entries {
		if item.chunk.ID == id {
			return item.chunk, true
		}
	}
	return domain.Chunk{}, false
}

// ReferenceChunks expands the compact reference notation used in training cards.
// Only existing citations are returned; no rules are inferred from the answer.
func (idx *Index) ReferenceChunks(references string) []domain.ChunkMatch {
	var result []domain.ChunkMatch
	seen := map[string]bool{}
	clausePattern := regexp.MustCompile(`п\.\s*([0-9]+(?:\.[0-9]+)*)`)
	for _, group := range strings.Split(references, ";") {
		document := strings.TrimSpace(strings.SplitN(group, ",", 2)[0])
		for _, match := range clausePattern.FindAllStringSubmatch(group, -1) {
			for _, item := range idx.entries {
				if item.chunk.Document == document && item.chunk.Clause == match[1] && !seen[item.chunk.ID] {
					seen[item.chunk.ID] = true
					result = append(result, domain.ChunkMatch{Chunk: item.chunk})
				}
			}
		}
	}
	return result
}

// Search returns up to three positive matches in descending score order.
// Empty or unrelated tickets return an empty slice and require escalation.
// Unknown query terms contribute to the norm and coverage denominator, so a
// single familiar word in otherwise unrelated text cannot imply a strong match.
func (idx *Index) Search(ticket string) domain.SearchResult {
	result := domain.SearchResult{Chunks: []domain.ChunkMatch{}, EscalationRequired: true}
	query := frequencies(ticket)
	if len(query) == 0 {
		return result
	}
	queryIDF := make(map[string]float64, len(query))
	totalWeight := 0.0
	queryTokens := sortedTokens(query)
	for _, token := range queryTokens {
		value, ok := idx.idf[token]
		if !ok {
			// Names, ticket numbers and dates are often absent from regulations.
			// Penalize unknown terms without assigning them the maximum IDF.
			value = 2
		}
		queryIDF[token] = value
		totalWeight += value
	}
	queryNorm := weight(query, queryIDF)
	for _, item := range idx.entries {
		dot, covered := 0.0, 0.0
		matched := 0
		for _, token := range queryTokens {
			value := query[token]
			if docValue, ok := item.vector[token]; ok {
				matched++
				dot += value * docValue
				covered += queryIDF[token]
			}
		}
		if dot == 0 || item.norm == 0 {
			continue
		}
		score := math.Min(1, 0.65*dot/(queryNorm*item.norm)+0.35*covered/totalWeight)
		// A single shared word in a multi-term ticket is weak evidence.
		if len(query) > 1 && matched == 1 {
			score *= 0.5
		}
		result.Chunks = append(result.Chunks, domain.ChunkMatch{Chunk: item.chunk, Score: score})
	}
	sort.Slice(result.Chunks, func(i, j int) bool {
		if result.Chunks[i].Score == result.Chunks[j].Score {
			return result.Chunks[i].ID < result.Chunks[j].ID
		}
		return result.Chunks[i].Score > result.Chunks[j].Score
	})
	if len(result.Chunks) > 3 {
		result.Chunks = result.Chunks[:3]
	}
	if len(result.Chunks) > 0 {
		result.BestScore = result.Chunks[0].Score
		result.EscalationRequired = result.BestScore < idx.threshold
	}
	return result
}

func weight(vector map[string]float64, idf map[string]float64) float64 {
	norm := 0.0
	for _, token := range sortedTokens(vector) {
		count := vector[token]
		value := (1 + math.Log(count)) * idf[token]
		vector[token] = value
		norm += value * value
	}
	return math.Sqrt(norm)
}

var (
	amountPattern = regexp.MustCompile(`\b[0-9]{1,3}(?:[ \x{00a0}\x{202f}][0-9]{3})+\b`)
	tokenPattern  = regexp.MustCompile(`[\p{L}\p{N}]+(?:-[\p{L}\p{N}]+)*`)
	spaces        = strings.NewReplacer(" ", "", "\u00a0", "", "\u202f", "")
	normalize     = strings.NewReplacer("ё", "е", "–", "-", "—", "-", "‑", "-", "−", "-")
	stopWords     = make(map[string]bool)
)

func init() {
	for _, word := range strings.Fields("и в во на по с со из за для от до к ко у о об а но или не ни без при через после перед это этот эта что как так ли же уже еще его ее их он она мы вы я мне мой прошу пожалуйста нужно необходимо требуется сегодня клиент клиента банк банка задача заявку заявка обращение") {
		stopWords[word] = true
	}
}

func frequencies(text string) map[string]float64 {
	text = normalize.Replace(strings.ToLower(text))
	text = amountPattern.ReplaceAllStringFunc(text, spaces.Replace)
	counts := make(map[string]float64)
	for _, token := range tokenPattern.FindAllString(text, -1) {
		if stopWords[token] || utf8.RuneCountInString(token) < 2 {
			continue
		}
		// Lightweight Russian prefix normalization covers inflected words such as
		// реквизиты/реквизитов. Codes, acronyms and amounts remain exact tokens.
		runes := []rune(token)
		if len(runes) > 6 && allCyrillic(runes) {
			token = string(runes[:6])
		}
		counts[token]++
	}
	return counts
}

func allCyrillic(runes []rune) bool {
	for _, r := range runes {
		if r < 'а' || r > 'я' {
			return false
		}
	}
	return true
}

func sortedTokens(vector map[string]float64) []string {
	keys := make([]string, 0, len(vector))
	for token := range vector {
		keys = append(keys, token)
	}
	sort.Strings(keys)
	return keys
}
