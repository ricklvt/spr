package selector_test

import (
	"fmt"
	"strings"
	"testing"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/ejoffe/spr/bl/internal"
	"github.com/ejoffe/spr/bl/selector"
	"github.com/ejoffe/spr/git"
	"github.com/stretchr/testify/require"
)

func ptr(i int) *int {
	return &i
}

func testingCommits(count int, prMap map[int]int) []*internal.PRCommit {
	commits := []*internal.PRCommit{}
	for i := 0; i != count; i++ {
		var prIndex *int
		if prI, ok := prMap[i]; ok {
			prIndex = &prI
		}

		commit := internal.PRCommit{
			Commit: git.Commit{
				CommitID: strings.Repeat(fmt.Sprintf("%d", i), 8),
			},
			Index:   i,
			PRIndex: prIndex,
		}
		commits = append(commits, &commit)
	}
	return commits
}

type CommitToPr map[int]int

func TestEvaluate(t *testing.T) {
	tests := []struct {
		desc     string
		input    string
		commits  []*internal.PRCommit
		indicies internal.Indices
		err      error
	}{
		{
			desc:     "list",
			input:    "1,2,3",
			commits:  testingCommits(5, CommitToPr{}),
			indicies: internal.Indices{CommitIndexes: mapset.NewSet[int](1, 2, 3)},
		},
		{
			desc:     "range",
			input:    "1-3",
			commits:  testingCommits(5, CommitToPr{}),
			indicies: internal.Indices{CommitIndexes: mapset.NewSet[int](1, 2, 3)},
		},
		{
			desc:     "pr set",
			input:    "s0",
			commits:  testingCommits(5, CommitToPr{1: 0, 2: 0, 3: 0}),
			indicies: internal.Indices{CommitIndexes: mapset.NewSet[int](1, 2, 3)},
		},
		{
			desc:     "combined",
			input:    "s0,4-6,8",
			commits:  testingCommits(9, CommitToPr{1: 0, 2: 0, 3: 0}),
			indicies: internal.Indices{CommitIndexes: mapset.NewSet[int](1, 2, 3, 4, 5, 6, 8)},
		},
		{
			desc:     "with destination",
			input:    "s0:1-3",
			commits:  testingCommits(9, CommitToPr{}),
			indicies: internal.Indices{DestinationPRIndex: ptr(0), CommitIndexes: mapset.NewSet[int](1, 2, 3)},
		},
		{
			desc:     "with empty selector",
			input:    "s0:",
			indicies: internal.Indices{DestinationPRIndex: ptr(0), CommitIndexes: mapset.NewSet[int]()},
		},
		{
			desc:     "with additive destination",
			input:    "s0+5-7",
			commits:  testingCommits(9, CommitToPr{1: 0, 2: 0, 3: 0}),
			indicies: internal.Indices{DestinationPRIndex: ptr(0), CommitIndexes: mapset.NewSet[int](1, 2, 3, 5, 6, 7)},
		}, {
			desc:     "with duplicates",
			input:    "s0+5-7,4-6,8,9",
			commits:  testingCommits(10, CommitToPr{1: 0, 2: 0, 3: 0}),
			indicies: internal.Indices{DestinationPRIndex: ptr(0), CommitIndexes: mapset.NewSet[int](1, 2, 3, 4, 5, 6, 7, 8, 9)},
		},
		{
			desc:     "with whitespace",
			input:    "  s0  +  5 - 7 , 4 - 6 , 8 , 9 ",
			commits:  testingCommits(10, CommitToPr{1: 0, 2: 0, 3: 0}),
			indicies: internal.Indices{DestinationPRIndex: ptr(0), CommitIndexes: mapset.NewSet[int](1, 2, 3, 4, 5, 6, 7, 8, 9)},
		},
		{
			desc:    "error invalid pr set",
			input:   "s9",
			commits: testingCommits(9, CommitToPr{}),
			err:     selector.ErrInvalidSelector,
		},
		{
			desc:    "error index out of range",
			input:   "1-99",
			commits: testingCommits(9, CommitToPr{}),
			err:     selector.ErrInvalidSelector,
		},
		{
			desc:    "error reversed range",
			input:   "3-2",
			commits: testingCommits(9, CommitToPr{}),
			err:     selector.ErrInvalidSelector,
		},
		{
			desc:  "error invalid syntax - letters",
			input: "asdfse",
			err:   selector.ErrInvalidSelector,
		},
		{
			desc:  "error invalid syntax - bad range",
			input: "1-",
			err:   selector.ErrInvalidSelector,
		},
		{
			desc:  "error invalid syntax - bad destination",
			input: ":",
			err:   selector.ErrInvalidSelector,
		},
		{
			desc:  "error invalid syntax - bad additive destination",
			input: "+",
			err:   selector.ErrInvalidSelector,
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			indexes, err := selector.Evaluate(test.commits, test.input)
			require.ErrorIs(t, err, test.err)
			if err == nil {
				require.Equal(t, test.indicies, indexes)
			}
		})
	}
}
