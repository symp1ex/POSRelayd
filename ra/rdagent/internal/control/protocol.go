package control

type Message struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	TS        int64  `json:"ts,omitempty"`

	X      float64 `json:"x,omitempty"`
	Y      float64 `json:"y,omitempty"`
	Button string  `json:"button,omitempty"`
	DeltaX int32   `json:"delta_x,omitempty"`
	DeltaY int32   `json:"delta_y,omitempty"`

	Code   string `json:"code,omitempty"`
	Key    string `json:"key,omitempty"`
	Ctrl   bool   `json:"ctrl,omitempty"`
	Shift  bool   `json:"shift,omitempty"`
	Alt    bool   `json:"alt,omitempty"`
	Meta   bool   `json:"meta,omitempty"`
	Repeat bool   `json:"repeat,omitempty"`

	Text     string `json:"text,omitempty"`
	Origin   string `json:"origin,omitempty"`
	Seq      uint64 `json:"seq,omitempty"`
	Revision string `json:"revision,omitempty"`

	Focused bool `json:"focused,omitempty"`
}
