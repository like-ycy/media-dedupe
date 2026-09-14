package model

type MediaType string

const (
	MediaImage MediaType = "image"
	MediaVideo MediaType = "video"
)

type FileStatus string

const (
	StatusActive  FileStatus = "active"
	StatusMissing FileStatus = "missing"
)

type GroupType string

const (
	GroupExact        GroupType = "exact"
	GroupSimilarImage GroupType = "similar_image"
	GroupSimilarVideo GroupType = "similar_video"
)

type Action string

const (
	ActionKeep    Action = "keep_recommended"
	ActionCleanup Action = "cleanup_candidate"
	ActionReview  Action = "review_required"
)

type DiscoveredFile struct {
	Path      string
	MediaType MediaType
	Extension string
	SizeBytes int64
	MTimeNs   int64
	Inode     uint64
	Device    uint64
}

type ImageMetadata struct {
	Width       int
	Height      int
	FormatName  string
	Orientation int
	IsReadable  bool
}

type VideoMetadata struct {
	DurationMs int64
	Width      int
	Height     int
	FrameRate  *float64
	BitRate    *int64
	Codec      string
	HasAudio   bool
	IsReadable bool
}

type SimilarityEdge struct {
	LeftFileID  int64
	RightFileID int64
	GroupType   GroupType
	Similarity  float64
	Reasons     []string
}

type ReportItem struct {
	FileID       int64
	Path         string
	Action       Action
	Similarity   float64
	QualityScore float64
	SizeBytes    int64
	ThumbPath    string
	Reasons      []string
}

type ReportGroup struct {
	GroupID           int64
	GroupType         GroupType
	Confidence        float64
	RecommendedFileID int64
	Items             []ReportItem
}

type ReportError struct {
	Path    string
	Stage   string
	Message string
}

type ScanMeta struct {
	Paths       []string `json:"paths"`
	StartedAt   string   `json:"started_at"`
	FinishedAt  string   `json:"finished_at"`
	FilesSeen   int      `json:"files_seen"`
	FilesFailed int      `json:"files_failed"`
	Threshold   float64  `json:"threshold"`
	CachePath   string   `json:"cache_path"`
}
