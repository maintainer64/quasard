// SPDX-License-Identifier: GPL-3.0-only
package session

// speechOnly follows the reference 512-sample VAD mask, 200 ms padding and
// 300 ms maximum silent gap. Input is unchanged when no speech was detected.
func speechOnly(pcm []int16, flags []bool) []int16 {
	const win, pad, maxGap = 512, 6, 9
	anySpeech := false
	for _, speech := range flags {
		anySpeech = anySpeech || speech
	}
	if !anySpeech || len(flags) == 0 {
		return pcm
	}
	keep := append([]bool(nil), flags...)
	for i, speech := range flags {
		if !speech {
			continue
		}
		for j := max(0, i-pad); j <= min(len(keep)-1, i+pad); j++ {
			keep[j] = true
		}
	}
	out := make([]int16, 0, len(pcm))
	gap := 0
	for i, k := range keep {
		if k {
			gap = 0
		} else {
			gap++
		}
		if k || gap <= maxGap {
			start := min(i*win, len(pcm))
			end := min((i+1)*win, len(pcm))
			out = append(out, pcm[start:end]...)
		}
	}
	if keep[len(keep)-1] && len(flags)*win < len(pcm) {
		out = append(out, pcm[len(flags)*win:]...)
	}
	if len(out) == 0 {
		return pcm
	}
	return out
}
