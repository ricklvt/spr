package selector

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/ejoffe/spr/bl/internal"
)

// ErrInvalidSelector is returned by Evaluate if the selector is invalid
var ErrInvalidSelector = errors.New("invalid commit selector")

func splitAndClean(s string, sep string) []string {
	s = strings.TrimSpace(s)

	var result []string
	for _, str := range strings.Split(s, sep) {
		if str != "" {
			result = append(result, strings.TrimSpace(str))
		}
	}
	return result
}

func asInteger(s string) (int, bool) {
	s = strings.TrimSpace(s)

	number, err := strconv.Atoi(s)
	return number, err == nil
}

func asRange(r string) (int, int, bool) {
	r = strings.TrimSpace(r)

	parts := splitAndClean(r, "-")
	if len(parts) != 2 {
		return 0, 0, false
	}

	parts = []string{
		strings.TrimSpace(parts[0]),
		strings.TrimSpace(parts[1]),
	}

	start, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}

	end, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}

	if start > end {
		return 0, 0, false
	}

	return start, end, true
}

func asPRSet(s string) (int, bool) {
	s = strings.TrimSpace(s)

	if rest, found := strings.CutPrefix(s, "s"); found {
		if n, ok := asInteger(rest); ok {
			return n, true
		}
	}

	return 0, false
}

func asDestination(s string) (int, string, bool) {
	s = strings.TrimSpace(s)

	// Split by ":"
	parts := strings.Split(s, ":")
	if len(parts) == 2 {

		parts = []string{
			strings.TrimSpace(parts[0]),
			strings.TrimSpace(parts[1]),
		}

		if prIndex, ok := asPRSet(parts[0]); ok {
			return prIndex, parts[1], true
		}
	}

	// Split by "+"
	parts = strings.Split(s, "+")
	if len(parts) != 2 {
		return 0, "", false
	}

	parts = []string{
		strings.TrimSpace(parts[0]),
		strings.TrimSpace(parts[1]),
	}

	if prIndex, ok := asPRSet(parts[0]); ok {
		// treat the "s#+..." as "s#:s#,..."
		return prIndex, parts[0] + "," + parts[1], true
	}

	return 0, "", false
}

// Evaluate evaluates the selector string and existing pull request sets and returns an Indices
func Evaluate(commits []*internal.PRCommit, selector string) (internal.Indices, error) {
	var destinationPRIndex *int
	if destIndex, rest, ok := asDestination(selector); ok {
		destinationPRIndex = &destIndex
		selector = rest
	}

	indexes, err := evalateCommitIndexes(commits, selector)
	return internal.Indices{
		DestinationPRIndex: destinationPRIndex,
		CommitIndexes:      indexes,
	}, err
}

func evalateCommitIndexes(commits []*internal.PRCommit, selector string) (mapset.Set[int], error) {
	commitIndexes := mapset.NewSet[int]()

	selector = strings.TrimSpace(selector)

	list := splitAndClean(selector, ",")
	for _, l := range list {
		if n, ok := asInteger(l); ok {
			commitIndexes.Add(n)
			continue
		}
		if from, to, ok := asRange(l); ok {
			for n := from; n <= to; n++ {
				commitIndexes.Add(n)
			}
			continue
		}
		if prIndex, ok := asPRSet(l); ok {
			validPr := false
			for _, commit := range commits {
				if commit.PRIndex != nil && *commit.PRIndex == prIndex {
					commitIndexes.Add(commit.Index)
					validPr = true
				}
			}
			if !validPr {
				return commitIndexes, fmt.Errorf("invalid pull request set %s: %w", l, ErrInvalidSelector)
			}
			continue
		}

		return commitIndexes, ErrInvalidSelector
	}

	for commitIndex := range commitIndexes.Iter() {
		if commitIndex < 0 || commitIndex >= len(commits) {
			return commitIndexes, fmt.Errorf("commit index %d is not valid: %w", commitIndex, ErrInvalidSelector)
		}
	}

	return commitIndexes, nil
}
