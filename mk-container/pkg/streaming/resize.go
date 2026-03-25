package streaming

import "encoding/json"

type ResizeEvent struct {
	Width  uint32 `json:"width"`
	Height uint32 `json:"height"`
}

func parseResizeMessage(data []byte) (ResizeEvent, error) {
	var ev ResizeEvent
	err := json.Unmarshal(data, &ev)
	return ev, err
}
