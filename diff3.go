/*
Package diff3 implements a three-way merge algorithm
Original version in Javascript by Bryan Housel @bhousel: https://github.com/bhousel/node-diff3,
which in turn is based on project Synchrotron, created by Tony Garnock-Jones. For more detail please visit:
http://homepages.kcbbs.gen.nz/tonyg/projects/synchrotron.html
https://github.com/tonyg/synchrotron

Ported to go by Javier Peletier @jpeletier
*/
package diff3

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/devsisters/go-diff3/linereader"
)

type resultStruct struct {
	common []string
	file1  []string
	file2  []string
}

// We apply the LCS to build a 'comm'-style picture of the
// differences between file1 and file2.
func diffComm(file1, file2 []string) []*resultStruct {
	var result []*resultStruct
	var tail1 int
	for _, diff := range diffIndices(file1, file2) {
		if diff.file1[0] > tail1 {
			result = append(result, &resultStruct{
				common: file1[tail1:diff.file1[0]],
			})
		}
		tail1 = diff.file1[0] + diff.file1[1]

		if diff.file1[1] > 0 || diff.file2[1] > 0 {
			result = append(result, &resultStruct{
				file1: file1[diff.file1[0] : diff.file1[0]+diff.file1[1]],
				file2: file2[diff.file2[0] : diff.file2[0]+diff.file2[1]],
			})
		}
	}
	return result
}

type chunkDescription struct {
	offset int
	length int
	chunk  []string
}

type patch struct {
	file1 *chunkDescription
	file2 *chunkDescription
}

// We apply the LCD to build a JSON representation of a
// diff(1)-style patch.
func diffPatch(file1, file2 []string) []*patch {
	var result []*patch
	cd := func(file []string, diff [2]int) *chunkDescription {
		return &chunkDescription{
			offset: diff[0],
			length: diff[1],
			chunk:  file[diff[0] : diff[0]+diff[1]],
		}
	}
	for _, diff := range diffIndices(file1, file2) {
		result = append(result, &patch{
			file1: cd(file1, diff.file1),
			file2: cd(file2, diff.file2),
		})
	}
	return result
}

// Takes the output of diffPatch(), and removes
// information from it. It can still be used by patch(),
// below, but can no longer be inverted.
func stripPatch(p []*patch) []*patch {
	var newpatch []*patch
	for i := 0; i < len(p); i++ {
		chunk := p[i]
		newpatch = append(newpatch, &patch{
			file1: &chunkDescription{offset: chunk.file1.offset, length: chunk.file1.length},
			file2: &chunkDescription{chunk: chunk.file2.chunk},
		})
	}
	return newpatch
}

// Takes the output of diffPatch(), and inverts the
// sense of it, so that it can be applied to file2 to give
// file1 rather than the other way around.
func invertPatch(p []*patch) {
	for i := 0; i < len(p); i++ {
		chunk := p[i]
		tmp := chunk.file1
		chunk.file1 = chunk.file2
		chunk.file2 = tmp
	}
}

// Applies a applyPatch to a file.
//
// Given file1 and file2,
//
//	applyPatch(file1, diffPatch(file1, file2))
//
// should give file2.
func applyPatch(file []string, p []*patch) []string {
	var result []string
	commonOffset := 0

	copyCommon := func(targetOffset int) {
		for commonOffset < targetOffset {
			result = append(result, file[commonOffset])
			commonOffset++
		}
	}

	for chunkIndex := 0; chunkIndex < len(p); chunkIndex++ {
		chunk := p[chunkIndex]
		copyCommon(chunk.file1.offset)
		for lineIndex := 0; lineIndex < len(chunk.file2.chunk); lineIndex++ {
			result = append(result, chunk.file2.chunk[lineIndex])
		}
		commonOffset += chunk.file1.length
	}

	copyCommon(len(file))
	return result
}

type diffIndicesResult struct {
	file1 [2]int
	file2 [2]int
}

// We apply the LCS to give a simple representation of the
// offsets and lengths of mismatched chunks in the input
// files. This is used by diff3MergeIndices below.
func diffIndices[T comparable](file1, file2 []T) []*diffIndicesResult {
	var result []*diffIndicesResult
	var last *diffIndicesResult
	// Find shortest edit script, and merge adjacent chunks
	for diff := range shortestEditScript(file1, file2, 0, 0) {
		if last == nil {
			last = diff
		} else if last.file1[0]+last.file1[1] == diff.file1[0] && last.file2[0]+last.file2[1] == diff.file2[0] {
			last.file1[1] += diff.file1[1]
			last.file2[1] += diff.file2[1]
		} else {
			result = append(result, last)
			last = diff
		}
	}
	if last != nil {
		result = append(result, last)
	}
	return result
}

type hunk [5]int
type hunkList []*hunk

func (h hunkList) Len() int           { return len(h) }
func (h hunkList) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h hunkList) Less(i, j int) bool { return h[i][0] < h[j][0] }

// Given three files, A, O, and B, where both A and B are
// independently derived from O, returns a fairly complicated
// internal representation of merge decisions it's taken. The
// interested reader may wish to consult
//
// Sanjeev Khanna, Keshav Kunal, and Benjamin C. Pierce.
// 'A Formal Investigation of ' In Arvind and Prasad,
// editors, Foundations of Software Technology and Theoretical
// Computer Science (FSTTCS), December 2007.
//
// (http://www.cis.upenn.edu/~bcpierce/papers/diff3-short.pdf)
func diff3MergeIndices[T comparable](a, o, b []T) [][]int {
	m1Ch := make(chan []*diffIndicesResult, 1)
	go func() {
		m1Ch <- diffIndices(o, a)
	}()
	m2 := diffIndices(o, b)
	m1 := <-m1Ch

	var hunks []*hunk
	addHunk := func(h *diffIndicesResult, side int) {
		hunks = append(hunks, &hunk{h.file1[0], side, h.file1[1], h.file2[0], h.file2[1]})
	}
	for i := 0; i < len(m1); i++ {
		addHunk(m1[i], 0)
	}
	for i := 0; i < len(m2); i++ {
		addHunk(m2[i], 2)
	}
	sort.Sort(hunkList(hunks))

	var result [][]int
	var commonOffset = 0
	copyCommon := func(targetOffset int) {
		if targetOffset > commonOffset {
			result = append(result, []int{1, commonOffset, targetOffset - commonOffset})
			commonOffset = targetOffset
		}
	}

	for hunkIndex := 0; hunkIndex < len(hunks); hunkIndex++ {
		firstHunkIndex := hunkIndex
		hunk := hunks[hunkIndex]
		regionLhs := hunk[0]
		regionRhs := regionLhs + hunk[2]
		for hunkIndex < len(hunks)-1 {
			maybeOverlapping := hunks[hunkIndex+1]
			maybeLhs := maybeOverlapping[0]
			if maybeLhs > regionRhs {
				break
			}
			regionRhs = max(regionRhs, maybeLhs+maybeOverlapping[2])
			hunkIndex++
		}

		copyCommon(regionLhs)
		if firstHunkIndex == hunkIndex {
			// The 'overlap' was only one hunk long, meaning that
			// there's no conflict here. Either a and o were the
			// same, or b and o were the same.
			if hunk[4] > 0 {
				result = append(result, []int{hunk[1], hunk[3], hunk[4]})
			}
		} else {
			// A proper conflict. Determine the extents of the
			// regions involved from a, o and b. Effectively merge
			// all the hunks on the left into one giant hunk, and
			// do the same for the right; then, correct for skew
			// in the regions of o that each side changed, and
			// report appropriate spans for the three sides.
			regions := [][]int{{len(a), -1, len(o), -1}, nil, {len(b), -1, len(o), -1}}
			for i := firstHunkIndex; i <= hunkIndex; i++ {
				hunk = hunks[i]
				side := hunk[1]
				r := regions[side]
				oLhs := hunk[0]
				oRhs := oLhs + hunk[2]
				abLhs := hunk[3]
				abRhs := abLhs + hunk[4]
				r[0] = min(abLhs, r[0])
				r[1] = max(abRhs, r[1])
				r[2] = min(oLhs, r[2])
				r[3] = max(oRhs, r[3])
			}
			aLhs := regions[0][0] + (regionLhs - regions[0][2])
			aRhs := regions[0][1] + (regionRhs - regions[0][3])
			bLhs := regions[2][0] + (regionLhs - regions[2][2])
			bRhs := regions[2][1] + (regionRhs - regions[2][3])
			result = append(result, []int{-1,
				aLhs, aRhs - aLhs,
				regionLhs, regionRhs - regionLhs,
				bLhs, bRhs - bLhs})
		}
		commonOffset = regionRhs
	}

	copyCommon(len(o))
	return result
}

// Conflict describes a merge conflict
type Conflict[T any] struct {
	A      []T
	AIndex int
	O      []T
	OIndex int
	B      []T
	BIndex int
}

// Diff3MergeResult describes a merge result
type Diff3MergeResult[T any] struct {
	Ok       []T
	Conflict *Conflict[T]
}

// Diff3Merge applies the output of diff3MergeIndices to actually
// construct the merged file; the returned result alternates
// between 'Ok' and 'Conflict' blocks.
func Diff3Merge[T comparable](a, o, b []T, excludeFalseConflicts bool) []*Diff3MergeResult[T] {
	var result []*Diff3MergeResult[T]
	files := [][]T{a, o, b}
	indices := diff3MergeIndices(a, o, b)

	var okLines []T
	flushOk := func() {
		if len(okLines) != 0 {
			result = append(result, &Diff3MergeResult[T]{Ok: okLines})
		}
		okLines = nil
	}

	pushOk := func(xs []T) {
		for j := 0; j < len(xs); j++ {
			okLines = append(okLines, xs[j])
		}
	}

	isTrueConflict := func(rec []int) bool {
		if rec[2] != rec[6] {
			return true
		}
		var aoff = rec[1]
		var boff = rec[5]
		for j := 0; j < rec[2]; j++ {
			if a[j+aoff] != b[j+boff] {
				return true
			}
		}
		return false
	}

	for i := 0; i < len(indices); i++ {
		var x = indices[i]
		var side = x[0]
		if side == -1 {
			if excludeFalseConflicts && !isTrueConflict(x) {
				pushOk(files[0][x[1] : x[1]+x[2]])
			} else {
				flushOk()
				result = append(result, &Diff3MergeResult[T]{
					Conflict: &Conflict[T]{
						A:      a[x[1] : x[1]+x[2]],
						AIndex: x[1],
						O:      o[x[3] : x[3]+x[4]],
						OIndex: x[3],
						B:      b[x[5] : x[5]+x[6]],
						BIndex: x[5],
					},
				})
			}
		} else {
			pushOk(files[side][x[1] : x[1]+x[2]])
		}
	}

	flushOk()
	return result
}

// MergeResult describes a merge result
type MergeResult struct {
	Conflicts bool      // Conflict indicates if there is any merge conflict
	Result    io.Reader // returns a reader that contains the merge result
}

func addConflictMarkers(lines, conflictA, conflictB []string, labelA, labelB string) []string {
	lines = append(lines, fmt.Sprintf("<<<<<<<<< %s", labelA))
	lines = append(lines, conflictA...)
	lines = append(lines, "=========")
	lines = append(lines, conflictB...)
	lines = append(lines, fmt.Sprintf(">>>>>>>>> %s", labelB))
	return lines
}

// Merge takes three streams and returns the merged result
func Merge(a, o, b io.Reader, detailed bool, labelA string, labelB string) (*MergeResult, error) {
	al, err := linereader.GetLines(a)
	if err != nil {
		return nil, err
	}
	ol, err := linereader.GetLines(o)
	if err != nil {
		return nil, err
	}
	bl, err := linereader.GetLines(b)
	if err != nil {
		return nil, err
	}

	var merger = Diff3Merge(al, ol, bl, true)
	var conflicts = false
	var lines []string
	for i := 0; i < len(merger); i++ {
		var item = merger[i]
		if item.Ok != nil {
			lines = append(lines, item.Ok...)

		} else {
			if detailed {
				var c = diffComm(item.Conflict.A, item.Conflict.B)
				for j := 0; j < len(c); j++ {
					var inner = c[j]
					if inner.common != nil {
						lines = append(lines, inner.common...)
					} else {
						conflicts = true
						lines = addConflictMarkers(lines, inner.file1, inner.file2, labelA, labelB)
					}
				}
			} else {
				conflicts = true
				lines = addConflictMarkers(lines, item.Conflict.A, item.Conflict.B, labelA, labelB)
			}
		}
	}
	return &MergeResult{
		Conflicts: conflicts,
		Result:    strings.NewReader(strings.Join(lines, "\n")),
	}, nil
}
