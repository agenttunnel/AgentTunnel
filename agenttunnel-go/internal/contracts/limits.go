package contracts

import "time"

// LimitWindow is one subscription usage window a harness reports, such as the
// rolling 5-hour window or the weekly allowance.
type LimitWindow struct {
	ID          string    `json:"id"`
	Label       string    `json:"label"`
	UsedPercent float64   `json:"usedPercent"`
	ResetsAt    time.Time `json:"resetsAt,omitzero"`
}

// HarnessLimits is a harness's subscription usage left. When Available is
// false, Reason says why no windows are shown; it never carries credentials.
type HarnessLimits struct {
	Harness   Harness       `json:"harness"`
	Available bool          `json:"available"`
	Reason    string        `json:"reason,omitempty"`
	Windows   []LimitWindow `json:"windows,omitempty"`
	FetchedAt time.Time     `json:"fetchedAt"`
}
