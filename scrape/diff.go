package scrape

import (
	"fmt"
	"strings"
)

// maxDiffWindowWords caps the LCS window: real snapshot diffs are a
// handful of words, and the common prefix/suffix trim below isolates them.
const maxDiffWindowWords = 2000

func DiffWords(prev, cur string) (string, error) {
	pw, cw := strings.Fields(prev), strings.Fields(cur)
	// Trim the common prefix/suffix so the O(n·m) LCS runs on the changed
	// window, not on two full pages that share 5k words.
	pre := 0
	for pre < len(pw) && pre < len(cw) && pw[pre] == cw[pre] {
		pre++
	}
	suf := 0
	for suf < len(pw)-pre && suf < len(cw)-pre && pw[len(pw)-1-suf] == cw[len(cw)-1-suf] {
		suf++
	}
	pw, cw = pw[pre:len(pw)-suf], cw[pre:len(cw)-suf]
	if len(pw) == 0 && len(cw) == 0 {
		return "", nil
	}
	// ponytail: O(n·m) time/memory on the window; Hirschberg is the
	// approved upgrade path if snapshots legitimately need wider diffs.
	if len(pw) > maxDiffWindowWords || len(cw) > maxDiffWindowWords {
		return "", fmt.Errorf("scrape: diff window too large (%d vs %d words, max %d): narrow the snapshot",
			len(pw), len(cw), maxDiffWindowWords)
	}
	// Classic word-LCS table over the window.
	n, m := len(pw), len(cw)
	width := m + 1
	table := make([]int32, (n+1)*width)
	at := func(i, j int) int32 { return table[i*width+j] }
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if pw[i] == cw[j] {
				table[i*width+j] = table[(i+1)*width+j+1] + 1
			} else if v1, v2 := at(i+1, j), at(i, j+1); v1 >= v2 {
				table[i*width+j] = v1
			} else {
				table[i*width+j] = v2
			}
		}
	}
	// Walk the script, emitting one line per maximal - / + run.
	var sb strings.Builder
	flush := func(op byte, words []string) {
		if len(words) == 0 {
			return
		}
		sb.WriteByte(op)
		for _, w := range words {
			sb.WriteByte(' ')
			sb.WriteString(w)
		}
		sb.WriteByte('\n')
	}
	var dels, adds []string
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case pw[i] == cw[j]:
			flush('-', dels)
			dels = nil
			flush('+', adds)
			adds = nil
			i++
			j++
		case at(i+1, j) >= at(i, j+1):
			dels = append(dels, pw[i])
			i++
		default:
			adds = append(adds, cw[j])
			j++
		}
	}
	for ; i < n; i++ {
		dels = append(dels, pw[i])
	}
	for ; j < m; j++ {
		adds = append(adds, cw[j])
	}
	flush('-', dels)
	flush('+', adds)
	return sb.String(), nil
}
