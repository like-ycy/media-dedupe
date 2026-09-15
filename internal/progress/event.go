package progress

// Stage identifies a scan pipeline stage.
type Stage string

const (
	StageDiscover Stage = "discover"
	StageExact    Stage = "exact"
	StageImage    Stage = "image"
	StageVideo    Stage = "video"
	StageText     Stage = "text"
	StageMatch    Stage = "match"
	StageThumb    Stage = "thumb"
	StageDone     Stage = "done"
	StageCanceled Stage = "canceled"
	StageError    Stage = "error"
)

// Event is a structured progress payload for the UI.
type Event struct {
	Stage        Stage   `json:"stage"`
	Phase        string  `json:"phase"` // running|done|skipped|error|canceled
	Message      string  `json:"message"`
	Current      int     `json:"current"`
	Total        int     `json:"total"`
	Percent      float64 `json:"percent"`
	CurrentFile  string  `json:"currentFile"`
	FoundExact   int     `json:"foundExact"`
	FoundSimilar int     `json:"foundSimilar"`
	FilesSeen    int     `json:"filesSeen"`
	Errors       int     `json:"errors"`
	LastPercent  float64 `json:"-"`
	ProjectID    string  `json:"projectId"`
}
