package integration

import "math/rand"

type firstFrameScheduledRun struct {
	pair int
	mode firstFrameMode
}

func firstFrameSchedule(pairs int, seed int64) []firstFrameScheduledRun {
	random := rand.New(rand.NewSource(seed))
	schedule := make([]firstFrameScheduledRun, 0, pairs*2)
	for pair := range pairs {
		first := firstFrameDisabled
		if random.Intn(2) == 1 {
			first = firstFrameEnabled
		}
		second := firstFrameEnabled
		if first == second {
			second = firstFrameDisabled
		}
		schedule = append(schedule, firstFrameScheduledRun{pair: pair, mode: first}, firstFrameScheduledRun{pair: pair, mode: second})
	}
	return schedule
}
