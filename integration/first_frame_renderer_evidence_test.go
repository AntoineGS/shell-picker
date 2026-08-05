package integration

import "errors"

func firstFrameRendererCountsFromEvidence(events []traceEvent, records []descendantProcessRecord) (firstFrameProcessCounts, error) {
	counts, err := countFirstFrameProcesses(records)
	if err != nil {
		return firstFrameProcessCounts{}, err
	}
	for _, event := range events {
		if event.Event != "preview.finished" || event.Outcome != "ok" || event.Renderer == "" || event.ChildStarts != 1 {
			continue
		}
		if counts.renderers[event.Renderer] < 1 {
			counts.renderers[event.Renderer] = 1
			counts.total += event.ChildStarts
		}
	}
	if counts.renderers["eza"] == 0 {
		return firstFrameProcessCounts{}, errors.New("first-frame renderer evidence lacks eza")
	}
	return counts, nil
}
