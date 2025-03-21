// Copyright 2018 Google Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package zoekt

import (
	"fmt"
	"log"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/filmil/zoekt/query"
)

// A docIterator iterates over documents in order.
type docIterator interface {
	// provide the next document where we can may find something
	// interesting.
	nextDoc() uint32

	// clears any per-document state of the docIterator, and
	// prepares for evaluating the given doc. The argument is
	// strictly increasing over time.
	prepare(nextDoc uint32)
}

const (
	costConst   = 0
	costMemory  = 1
	costContent = 2
	costRegexp  = 3
)

const (
	costMin = costConst
	costMax = costRegexp
)

// An expression tree coupled with matches. The matchtree has two
// functions:
//
// * it implements boolean combinations (and, or, not)
//
// * it implements shortcuts, where we skip documents (for example: if
// there are no trigram matches, we can be sure there are no substring
// matches). The matchtree iterates over the documents as they are
// ordered in the shard.
//
// The general process for a given (shard, query) is
//
// - construct matchTree for the query
//
// - find all different leaf matchTrees (substring, regexp, etc.)
//
// in a loop:
//
//   - find next doc to process using nextDoc
//
//   - evaluate atoms (leaf expressions that match text)
//
//   - evaluate the tree using matches(), storing the result in map.
//
//   - if the complete tree returns (matches() == true) for the document,
//     collect all text matches by looking at leaf matchTrees
//
type matchTree interface {
	docIterator

	// returns whether this matches, and if we are sure.
	matches(cp *contentProvider, cost int, known map[matchTree]bool) (match bool, sure bool)
}

// docMatchTree iterates over documents for which predicate(docID) returns true.
type docMatchTree struct {
	// the number of documents in a shard.
	numDocs uint32

	predicate func(docID uint32) bool

	// provides additional information about the reason why the docMatchTree was
	// created.
	reason string

	// mutable
	firstDone bool
	docID     uint32
}

type bruteForceMatchTree struct {
	// mutable
	firstDone bool
	docID     uint32
}

type andLineMatchTree struct {
	andMatchTree
}

type andMatchTree struct {
	children []matchTree
}

type orMatchTree struct {
	children []matchTree
}

type notMatchTree struct {
	child matchTree
}

// Don't visit this subtree for collecting matches.
type noVisitMatchTree struct {
	matchTree
}

type regexpMatchTree struct {
	regexp *regexp.Regexp

	fileName bool

	// mutable
	reEvaluated bool
	found       []*candidateMatch

	// nextDoc, prepare.
	bruteForceMatchTree
}

type substrMatchTree struct {
	matchIterator

	query         *query.Substring
	caseSensitive bool
	fileName      bool

	// mutable
	current       []*candidateMatch
	contEvaluated bool
}

type branchQueryMatchTree struct {
	fileMasks []uint64
	mask      uint64

	// mutable
	firstDone bool
	docID     uint32
}

// all prepare methods

func (t *bruteForceMatchTree) prepare(doc uint32) {
	t.docID = doc
	t.firstDone = true
}

func (t *docMatchTree) prepare(doc uint32) {
	t.docID = doc
	t.firstDone = true
}

func (t *andMatchTree) prepare(doc uint32) {
	for _, c := range t.children {
		c.prepare(doc)
	}
}

func (t *regexpMatchTree) prepare(doc uint32) {
	t.found = t.found[:0]
	t.reEvaluated = false
	t.bruteForceMatchTree.prepare(doc)
}

func (t *orMatchTree) prepare(doc uint32) {
	for _, c := range t.children {
		c.prepare(doc)
	}
}

func (t *notMatchTree) prepare(doc uint32) {
	t.child.prepare(doc)
}

func (t *substrMatchTree) prepare(nextDoc uint32) {
	t.matchIterator.prepare(nextDoc)
	t.current = t.matchIterator.candidates()
	t.contEvaluated = false
}

func (t *branchQueryMatchTree) prepare(doc uint32) {
	t.firstDone = true
	t.docID = doc
}

// nextDoc

func (t *docMatchTree) nextDoc() uint32 {
	var start uint32
	if t.firstDone {
		start = t.docID + 1
	}
	for i := start; i < t.numDocs; i++ {
		if t.predicate(i) {
			return i
		}
	}
	return maxUInt32
}

func (t *bruteForceMatchTree) nextDoc() uint32 {
	if !t.firstDone {
		return 0
	}
	return t.docID + 1
}

func (t *andMatchTree) nextDoc() uint32 {
	var max uint32
	for _, c := range t.children {
		m := c.nextDoc()
		if m > max {
			max = m
		}
	}
	return max
}

func (t *orMatchTree) nextDoc() uint32 {
	min := uint32(maxUInt32)
	for _, c := range t.children {
		m := c.nextDoc()
		if m < min {
			min = m
		}
	}
	return min
}

func (t *notMatchTree) nextDoc() uint32 {
	return 0
}

func (t *branchQueryMatchTree) nextDoc() uint32 {
	var start uint32
	if t.firstDone {
		start = t.docID + 1
	}

	for i := start; i < uint32(len(t.fileMasks)); i++ {
		if (t.mask & t.fileMasks[i]) != 0 {
			return i
		}
	}
	return maxUInt32
}

// all String methods

func (t *bruteForceMatchTree) String() string {
	return "all"
}

func (t *docMatchTree) String() string {
	return fmt.Sprintf("doc(%s)", t.reason)
}

func (t *andMatchTree) String() string {
	return fmt.Sprintf("and%v", t.children)
}

func (t *regexpMatchTree) String() string {
	return fmt.Sprintf("re(%s)", t.regexp)
}

func (t *orMatchTree) String() string {
	return fmt.Sprintf("or%v", t.children)
}

func (t *notMatchTree) String() string {
	return fmt.Sprintf("not(%v)", t.child)
}

func (t *substrMatchTree) String() string {
	f := ""
	if t.fileName {
		f = "f"
	}

	return fmt.Sprintf("%ssubstr(%q, %v, %v)", f, t.query.Pattern, t.current, t.matchIterator)
}

func (t *branchQueryMatchTree) String() string {
	return fmt.Sprintf("branch(%x)", t.mask)
}

// visitMatches visits all atoms in matchTree. Note: This visits
// noVisitMatchTree. For collecting matches use visitMatches.
func visitMatchTree(t matchTree, f func(matchTree)) {
	switch s := t.(type) {
	case *andMatchTree:
		for _, ch := range s.children {
			visitMatchTree(ch, f)
		}
	case *orMatchTree:
		for _, ch := range s.children {
			visitMatchTree(ch, f)
		}
	case *andLineMatchTree:
		visitMatchTree(&s.andMatchTree, f)
	case *noVisitMatchTree:
		visitMatchTree(s.matchTree, f)
	case *notMatchTree:
		visitMatchTree(s.child, f)
	default:
		f(t)
	}
}

// visitMatches visits all atoms which can contribute matches. Note: This
// skips noVisitMatchTree.
func visitMatches(t matchTree, known map[matchTree]bool, f func(matchTree)) {
	switch s := t.(type) {
	case *andMatchTree:
		for _, ch := range s.children {
			if known[ch] {
				visitMatches(ch, known, f)
			}
		}
	case *andLineMatchTree:
		visitMatches(&s.andMatchTree, known, f)
	case *orMatchTree:
		for _, ch := range s.children {
			if known[ch] {
				visitMatches(ch, known, f)
			}
		}
	case *notMatchTree:
	case *noVisitMatchTree:
		// don't collect into negative trees.
	default:
		f(s)
	}
}

// all matches() methods.

func (t *docMatchTree) matches(cp *contentProvider, cost int, known map[matchTree]bool) (bool, bool) {
	return t.predicate(cp.idx), true
}

func (t *bruteForceMatchTree) matches(cp *contentProvider, cost int, known map[matchTree]bool) (bool, bool) {
	return true, true
}

// andLineMatchTree is a performance optimization of andMatchTree. For content
// searches we don't want to run the regex engine if there is no line that
// contains matches from all terms.
func (t *andLineMatchTree) matches(cp *contentProvider, cost int, known map[matchTree]bool) (bool, bool) {
	matches, sure := t.andMatchTree.matches(cp, cost, known)
	if !(sure && matches) {
		return matches, sure
	}

	// find child with fewest candidates
	min := maxUInt32
	fewestChildren := 0
	for ix, child := range t.children {
		v, ok := child.(*substrMatchTree)
		// make sure we are running a content search and that all candidates are a
		// substrMatchTree
		if !ok || v.fileName {
			return matches, sure
		}
		if len(v.current) < min {
			min = len(v.current)
			fewestChildren = ix
		}
	}

	type lineRange struct {
		start int
		end   int
	}
	lines := make([]lineRange, 0, len(t.children[fewestChildren].(*substrMatchTree).current))
	prev := -1
	for _, candidate := range t.children[fewestChildren].(*substrMatchTree).current {
		line, byteStart, byteEnd := candidate.line(cp.newlines(), cp.fileSize)
		if line == prev {
			continue
		}
		prev = line
		lines = append(lines, lineRange{byteStart, byteEnd})
	}

	// children keeps track of the children's candidates we have already seen.
	children := make([][]*candidateMatch, 0, len(t.children)-1)
	for j, child := range t.children {
		if j == fewestChildren {
			continue
		}
		children = append(children, child.(*substrMatchTree).current)
	}

nextLine:
	for i := 0; i < len(lines); i++ {
		hits := 1
	nextChild:
		for j := range children {
		nextCandidate:
			for len(children[j]) > 0 {
				candidate := children[j][0]
				bo := int(cp.findOffset(false, candidate.runeOffset))
				if bo < lines[i].start {
					children[j] = children[j][1:]
					continue nextCandidate
				}
				if bo <= lines[i].end {
					hits++
					continue nextChild
				}
				// move the `lines` iterator forward until bo <= line.end
				for i < len(lines) && bo > lines[i].end {
					i++
				}
				i--
				continue nextLine
			}
		}
		// return early once we found any line that contains matches from all children
		if hits == len(t.children) {
			return matches, true
		}
	}
	return false, true
}

func (t *andMatchTree) matches(cp *contentProvider, cost int, known map[matchTree]bool) (bool, bool) {
	sure := true

	for _, ch := range t.children {
		v, ok := evalMatchTree(cp, cost, known, ch)
		if ok && !v {
			return false, true
		}
		if !ok {
			sure = false
		}
	}

	return true, sure
}

func (t *orMatchTree) matches(cp *contentProvider, cost int, known map[matchTree]bool) (bool, bool) {
	matches := false
	sure := true
	for _, ch := range t.children {
		v, ok := evalMatchTree(cp, cost, known, ch)
		if ok {
			// we could short-circuit, but we want to use
			// the other possibilities as a ranking
			// signal.
			matches = matches || v
		} else {
			sure = false
		}
	}
	return matches, sure
}

func (t *branchQueryMatchTree) matches(cp *contentProvider, cost int, known map[matchTree]bool) (bool, bool) {
	return t.fileMasks[t.docID]&t.mask != 0, true
}

func (t *regexpMatchTree) matches(cp *contentProvider, cost int, known map[matchTree]bool) (bool, bool) {
	if t.reEvaluated {
		return len(t.found) > 0, true
	}

	if cost < costRegexp {
		return false, false
	}

	cp.stats.RegexpsConsidered++
	idxs := t.regexp.FindAllIndex(cp.data(t.fileName), -1)
	found := t.found[:0]
	for _, idx := range idxs {
		cm := &candidateMatch{
			byteOffset:  uint32(idx[0]),
			byteMatchSz: uint32(idx[1] - idx[0]),
			fileName:    t.fileName,
		}

		found = append(found, cm)
	}
	t.found = found
	t.reEvaluated = true

	return len(t.found) > 0, true
}

// breakMatchesOnNewlines returns matches resulting from breaking each element
// of cms on newlines within text.
func breakMatchesOnNewlines(cms []*candidateMatch, text []byte) []*candidateMatch {
	var lineCMs []*candidateMatch
	for _, cm := range cms {
		lineCMs = append(lineCMs, breakOnNewlines(cm, text)...)
	}
	return lineCMs
}

// breakOnNewlines returns matches resulting from breaking cm on newlines
// within text.
func breakOnNewlines(cm *candidateMatch, text []byte) []*candidateMatch {
	var cms []*candidateMatch
	addMe := &candidateMatch{}
	*addMe = *cm
	for i := uint32(cm.byteOffset); i < cm.byteOffset+cm.byteMatchSz; i++ {
		if text[i] == '\n' {
			addMe.byteMatchSz = i - addMe.byteOffset
			if addMe.byteMatchSz != 0 {
				cms = append(cms, addMe)
			}

			addMe = &candidateMatch{}
			*addMe = *cm
			addMe.byteOffset = i + 1
		}
	}
	addMe.byteMatchSz = cm.byteOffset + cm.byteMatchSz - addMe.byteOffset
	if addMe.byteMatchSz != 0 {
		cms = append(cms, addMe)
	}
	return cms
}

func evalMatchTree(cp *contentProvider, cost int, known map[matchTree]bool, mt matchTree) (bool, bool) {
	if v, ok := known[mt]; ok {
		return v, true
	}

	v, ok := mt.matches(cp, cost, known)
	if ok {
		known[mt] = v
	}

	return v, ok
}

func (t *notMatchTree) matches(cp *contentProvider, cost int, known map[matchTree]bool) (bool, bool) {
	v, ok := evalMatchTree(cp, cost, known, t.child)
	return !v, ok
}

func (t *substrMatchTree) matches(cp *contentProvider, cost int, known map[matchTree]bool) (bool, bool) {
	if t.contEvaluated {
		return len(t.current) > 0, true
	}

	if len(t.current) == 0 {
		return false, true
	}

	if t.fileName && cost < costMemory {
		return false, false
	}

	if !t.fileName && cost < costContent {
		return false, false
	}

	pruned := t.current[:0]
	for _, m := range t.current {
		if m.byteOffset == 0 && m.runeOffset > 0 {
			m.byteOffset = cp.findOffset(m.fileName, m.runeOffset)
		}
		if m.matchContent(cp.data(m.fileName)) {
			pruned = append(pruned, m)
		}
	}
	t.current = pruned
	t.contEvaluated = true

	return len(t.current) > 0, true
}

func (d *indexData) newMatchTree(q query.Q) (matchTree, error) {
	if q == nil {
		return nil, fmt.Errorf("got nil (sub)query")
	}
	switch s := q.(type) {
	case *query.Regexp:
		// RegexpToMatchTreeRecursive tries to distill a matchTree that matches a
		// superset of the regexp. If the returned matchTree is equivalent to the
		// original regexp, it returns true. An equivalent matchTree has the same
		// behaviour as the original regexp and can be used instead.
		//
		subMT, isEq, _, err := d.regexpToMatchTreeRecursive(s.Regexp, ngramSize, s.FileName, s.CaseSensitive)
		if err != nil {
			return nil, err
		}
		// if the query can be used in place of the regexp
		// return the subtree
		if isEq {
			return subMT, nil
		}

		prefix := ""
		if !s.CaseSensitive {
			prefix = "(?i)"
		}

		tr := &regexpMatchTree{
			regexp:   regexp.MustCompile(prefix + s.Regexp.String()),
			fileName: s.FileName,
		}

		return &andMatchTree{
			children: []matchTree{
				tr, &noVisitMatchTree{subMT},
			},
		}, nil
	case *query.And:
		var r []matchTree
		for _, ch := range s.Children {
			ct, err := d.newMatchTree(ch)
			if err != nil {
				return nil, err
			}
			r = append(r, ct)
		}
		return &andMatchTree{r}, nil
	case *query.Or:
		var r []matchTree
		for _, ch := range s.Children {
			ct, err := d.newMatchTree(ch)
			if err != nil {
				return nil, err
			}
			r = append(r, ct)
		}
		return &orMatchTree{r}, nil
	case *query.Not:
		ct, err := d.newMatchTree(s.Child)
		return &notMatchTree{
			child: ct,
		}, err

	case *query.Substring:
		return d.newSubstringMatchTree(s)

	case *query.Branch:
		mask := uint64(0)
		if s.Pattern == "HEAD" {
			mask = 1
		} else {
			for nm, m := range d.branchIDs {
				if strings.Contains(nm, s.Pattern) {
					mask |= uint64(m)
				}
			}
		}
		return &branchQueryMatchTree{
			mask:      mask,
			fileMasks: d.fileBranchMasks,
		}, nil
	case *query.Const:
		if s.Value {
			return &bruteForceMatchTree{}, nil
		} else {
			return &noMatchTree{"const"}, nil
		}
	case *query.Language:
		code, ok := d.metaData.LanguageMap[s.Language]
		if !ok {
			return &noMatchTree{"lang"}, nil
		}
		return &docMatchTree{
			reason:  "language",
			numDocs: uint32(len(d.languages)),
			predicate: func(docID uint32) bool {
				return d.languages[docID] == code
			},
		}, nil

	case *query.Symbol:
		mt, err := d.newSubstringMatchTree(s.Atom)
		if err != nil {
			return nil, err
		}

		if _, ok := mt.(*regexpMatchTree); ok {
			return nil, fmt.Errorf("regexps and short queries not implemented for symbol search")
		}
		subMT, ok := mt.(*substrMatchTree)
		if !ok {
			return nil, fmt.Errorf("found %T inside query.Symbol", mt)
		}

		subMT.matchIterator = d.newTrimByDocSectionIter(s.Atom, subMT.matchIterator)
		return subMT, nil
	}
	log.Panicf("type %T", q)
	return nil, nil
}

func (d *indexData) newSubstringMatchTree(s *query.Substring) (matchTree, error) {
	st := &substrMatchTree{
		query:         s,
		caseSensitive: s.CaseSensitive,
		fileName:      s.FileName,
	}

	if utf8.RuneCountInString(s.Pattern) < ngramSize {
		prefix := ""
		if !s.CaseSensitive {
			prefix = "(?i)"
		}
		t := &regexpMatchTree{
			regexp:   regexp.MustCompile(prefix + regexp.QuoteMeta(s.Pattern)),
			fileName: s.FileName,
		}
		return t, nil
	}

	result, err := d.iterateNgrams(s)
	if err != nil {
		return nil, err
	}
	st.matchIterator = result
	return st, nil
}
