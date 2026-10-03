// Package engine is the internal engine layer. Upper layers (queue,
// schedule, APIs, events, import, preview) talk to an Engine, never to an
// engine implementation directly. URLs go to the built-in URL engine
// (engine/dl), torrents to libtorrent (engine/lt).
package engine

import "errors"

// Caps are the capability flags an engine declares. UI and APIs show or hide
// options by these flags, never by engine name.
type Caps struct {
	Socks5Peers          bool `json:"socks5_peers"`
	Socks5Trackers       bool `json:"socks5_trackers"`
	HTTPProxy            bool `json:"http_proxy"`
	UPnP                 bool `json:"upnp"`
	FilePriorityLevels   bool `json:"file_priority_levels"`
	GlobalConnLimit      bool `json:"global_conn_limit"`
	Sequential           bool `json:"sequential"`
	MoveWhileSeeding     bool `json:"move_while_seeding"`
	Webseeds             bool `json:"webseeds"`
	ResumeImportOfficial bool `json:"resume_import_official"`
	URLs                 bool `json:"urls"`
	Torrents             bool `json:"torrents"`
	FTP                  bool `json:"ftp"` // ftp:// and ftps:// URLs
	SFTP                 bool `json:"sftp"`
	SCP                  bool `json:"scp"`
	Socks5URLs           bool `json:"socks5_urls"` // URL downloads through a SOCKS5 proxy
}

// AddRequest describes a new engine task.
type AddRequest struct {
	URIs     []string // HTTP/FTP mirrors of one file
	Magnet   string
	Torrent  []byte
	Dir      string // download directory
	Out      string // file name for URL tasks (optional)
	Headers  []string
	User     string // HTTP/FTP credentials
	Pass     string
	Select   []int  // 0-based file indices to download (torrents); nil = all
	Root     string // torrents: name of the top folder (or single file) when it is not the torrent's
	Paused   bool
	Check    bool // verify existing data before downloading
	Trackers []string
	MaxDown  int64 // bytes/s, 0 = unlimited
	MaxUp    int64
	// Seeding targets: SeedTime minutes (-1 = do not seed, 0 = forever).
	SeedRatio    float64
	SeedTime     int
	MaxPeers     int
	Sequential   bool
	Proxy        string // per-task proxy URL ("" = direct)
	MetadataOnly bool   // magnet: fetch the metadata (.torrent) only
	Guard        bool   // URLs: refuse loopback and the NAS's own addresses
}

// State is the engine-level state of a task.
type State string

const (
	Active   State = "active"
	Waiting  State = "waiting"
	Paused   State = "paused"
	Complete State = "complete"
	Error    State = "error"
	Removed  State = "removed"
)

// File is one file of a task.
type File struct {
	Index     int    `json:"index"`
	Path      string `json:"path"` // path relative to the task directory
	Size      int64  `json:"size"`
	Completed int64  `json:"completed"`
	Selected  bool   `json:"selected"`
	Priority  int    `json:"priority"`
}

// Status is a polled snapshot of one engine task.
type Status struct {
	Ref         string
	State       State
	Name        string
	Dir         string
	Total       int64
	Completed   int64
	Uploaded    int64
	DownRate    int64
	UpRate      int64
	Connections int
	Seeders     int
	InfoHash    string
	FollowedBy  string // a magnet's metadata task was replaced by this ref
	IsMetadata  bool
	Seeding     bool // all selected data present, uploading
	Verifying   bool
	Moving      bool   // the data is being moved to another folder (StorageMover)
	MoveError   string // why the last move failed
	ErrorCode   string // one of the Err* codes below, or engine specific
	ErrorMsg    string
	NumPieces   int
	PieceLength int64
	Bitfield    string // hex, most significant bit first
	Files       []File
	Comment     string
}

// Peer is a connected peer.
type Peer struct {
	IP       string  `json:"ip"`
	Port     int     `json:"port"`
	Client   string  `json:"client"`
	DownRate int64   `json:"down_rate"`
	UpRate   int64   `json:"up_rate"`
	Seeder   bool    `json:"seeder"`
	Progress float64 `json:"progress"`
}

// Global holds the options applied to a running engine without restart.
type Global struct {
	MaxDown        int64 // bytes/s overall
	MaxUp          int64
	MaxActive      int
	PortFrom       int
	PortTo         int
	DHT            bool
	LSD            bool
	PEX            bool
	UPnP           bool
	Encrypt        bool
	MaxConn        int
	TorrentMaxConn int
	TorrentMaxUp   int64
	SeedRatio      float64
	SeedTime       int
	PeerID         string // peer id prefix, e.g. "-LT1218-"
	PeerAgent      string
	Proxy          Proxy
}

// Proxy is the proxy configuration for an engine.
type Proxy struct {
	Type          string // "", "http", "socks5"
	Host          string
	Port          int
	User          string
	Pass          string
	RemoteDNS     bool
	ApplyURL      bool
	ApplyTrackers bool
	ApplyPeers    bool
	Force         bool
	NoProxy       string
}

// Engine is implemented by every engine adapter.
type Engine interface {
	Name() string
	Caps() Caps
	Start() error
	Stop() error
	Health() error
	Version() string
	ApplyGlobal(g Global) error

	// Add creates a task. hash is the stable task id; engines that can
	// choose their reference derive it from hash so re-adding is idempotent.
	Add(hash string, r AddRequest) (ref string, err error)
	Forget(ref string)
	Restart(g Global) error
	Pause(ref string) error
	Resume(ref string) error
	Remove(ref string) error // engine side only; data is handled by the caller
	SetFiles(ref string, sel []int, prio map[int]int) error
	SetLimits(ref string, down, up int64) error
	SetSequential(ref string, on bool) error
	AddTrackers(ref string, trackers []string) error
	ReplaceURIs(ref string, uris []string) error
	SetSeeding(ref string, ratio float64, minutes int) error

	StatusAll() (map[string]*Status, error)
	Status(ref string) (*Status, error)
	Peers(ref string) ([]Peer, error)
	Trackers(ref string) ([]string, error)
	SaveState() error
}

// StorageMover is an engine that moves a task's data to another folder
// while the task keeps running (Caps.MoveWhileSeeding). The move happens in
// the background: Status.Dir changes once it is done, Status.Moving is set
// meanwhile and Status.MoveError when it failed. root renames the top folder
// (or single file) on the way; nothing at the destination is replaced.
type StorageMover interface {
	MoveStorage(ref, dir, root string) error
}

// Error codes reported in Status.ErrorCode. The numbers are aria2's exit
// codes, which the first version used: stored tasks, the automatic retries
// and the V4 error mapping key on them.
const (
	ErrUnknown     = "1"
	ErrTimeout     = "2"
	ErrNotFoundURL = "3"
	ErrNetwork     = "6"
	ErrNoResume    = "8"
	ErrDiskFull    = "9"
	ErrCreateFile  = "16"
	ErrFileIO      = "17"
	ErrCreateDir   = "18"
	ErrResolve     = "19"
	ErrFTPCommand  = "21"
	ErrBadResponse = "22"
	ErrRedirects   = "23"
	ErrAuth        = "24"
	ErrServerBusy  = "29"
	ErrNoTransport = "dcdl"    // the URL engine (dc-dl) is not available
	ErrHostKey     = "hostkey" // SFTP/SCP server key differs from the one seen before
)

var ErrNotFound = errors.New("engine: task not found")
var ErrUnsupported = errors.New("engine: not supported")
