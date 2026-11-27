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
	Socks5URLs           bool `json:"socks5_urls"`  // URL downloads through a SOCKS5 proxy
	PeerRefresh          bool `json:"peer_refresh"` // torrents: PeerFinder (look for peers again, replace trackers)
	AdoptData            bool `json:"adopt_data"`   // torrents: AddRequest.AdoptFrom (take over data already in the destination)
}

// AddRequest describes a new engine task.
type AddRequest struct {
	URIs       []string // HTTP/FTP mirrors of one file
	Magnet     string
	Torrent    []byte
	Dir        string // download directory
	Out        string // file name for URL tasks (optional)
	Headers    []string
	User       string // HTTP/FTP credentials
	Pass       string
	Select     []int       // 0-based file indices to download (torrents); nil = all
	Priorities map[int]int // torrents: file index -> priority 1..7 (nil = engine default)
	Root       string      // torrents: name of the top folder (or single file) when it is not the torrent's
	Paused     bool
	Check      bool // verify existing data before downloading
	Trackers   []string
	MaxDown    int64 // bytes/s, 0 = unlimited
	MaxUp      int64
	// Seeding targets: SeedTime minutes (-1 = do not seed, 0 = forever).
	SeedRatio    float64
	SeedTime     int
	MaxPeers     int
	Sequential   bool
	Proxy        string // per-task proxy URL ("" = direct)
	MetadataOnly bool   // magnet: fetch the metadata (.torrent) only
	Guard        bool   // refuse loopback and the NAS's own addresses (a regular user's task, one added through a token, a torrent with the public tracker list)
	NoPages      bool   // URLs: a web page (text/html) in place of the file is an error
	// URLs: the server's certificate is not checked (HTTPS and FTPS, every
	// redirect; the task's own choice)
	SkipCertCheck bool
	// UntrustedDir: the torrent's destination is treated as a folder other
	// accounts can change (the core sends it for every torrent). The engine
	// then downloads and seeds only in Dir, which must be a folder only root
	// can enter, and hands the data over to the destination (MoveStorage)
	// without following links there.
	UntrustedDir bool
	// UntrustedDir: the account the handed-over files belong to (0: the
	// owner of the folder they are handed to).
	OwnerUID, OwnerGID int
	// UntrustedDir: the torrent's data may already sit in AdoptFrom, its top
	// folder (or single file) called AdoptRoot. What of it holds the
	// torrent's data, as a plain file of the owner, is taken over (Adopter)
	// and the torrent counts as handed over to AdoptFrom. With AdoptRequired
	// the add fails (ErrFolderNotPrivate) when nothing can be taken over.
	AdoptFrom     string
	AdoptRoot     string
	AdoptRequired bool
}

// Adopter is an engine that reports whether the last Add of a torrent took
// over the data in AddRequest.AdoptFrom.
type Adopter interface {
	Adopted(ref string) bool
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
	Pad       bool   `json:"pad,omitempty"` // BEP 47 padding file
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
	KnownPeers  int  // torrents: peers found, connected or not, without the NAS itself
	Private     bool // torrents: the metadata says private (only once it is there)
	// Torrents, for the source health: complete copies the connected peers
	// have together (-1 unknown: no metadata, or complete), when a complete
	// copy was last seen (Unix, 0 unknown), whether the torrent is being
	// announced to DHT, how many peers DHT found since the engine started
	// and when it last found one (Unix, 0 never; the NAS itself, which DHT
	// hands back, does not count; a lookup that finds nobody leaves no trace).
	Availability     float64
	LastSeenComplete int64
	DHTUp            bool
	DHTPeers         int
	DHTReplyAt       int64
	Seeders          int
	InfoHash         string
	FollowedBy       string // a magnet's metadata task was replaced by this ref
	IsMetadata       bool
	Seeding          bool // all selected data present, uploading
	Verifying        bool
	Moving           bool   // the data is being moved to another folder (StorageMover)
	MoveError        string // why the last move failed
	ErrorCode        string // one of the Err* codes below, or engine specific
	ErrorMsg         string
	NumPieces        int
	PieceLength      int64
	Bitfield         string // hex, most significant bit first
	Files            []File
	Comment          string
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
	PeerID         string // peer id prefix, e.g. "-TR2940-"; empty = the engine's own identity
	PeerAgent      string
	Proxy          Proxy
	// MemoryProfile: "low" on NAS models with little memory (lighter engine
	// settings), else "normal"
	MemoryProfile string
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

// OwnIdentity is implemented by engines with a client identity of their own,
// used when Global.PeerID is empty: the peer id prefix and User-Agent (empty
// while unknown).
type OwnIdentity interface {
	OwnIdentity() (peerID, agent string)
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

// ErrPending: the engine is still checking the data in AddRequest.AdoptFrom;
// the task is added again later (nothing failed).
var ErrPending = errors.New("engine: still checking the data in the destination folder")

// Errors of torrents added with UntrustedDir.
var (
	// The torrent engine is older than the one that keeps torrents away from
	// folders other accounts can change.
	ErrEngineTooOld = errors.New("The torrent engine is too old to download this torrent safely; update Download Center")
	// Dir is not a folder only root can change, and the torrent has no
	// finished data the engine could count as seeded.
	ErrFolderNotPrivate = errors.New("The torrent engine does not use folders that users can change; this torrent cannot continue from where its data is")
)

// PeerFinder is a torrent engine that can make a torrent look for peers
// again (Caps.PeerRefresh): RefreshPeers forgets the peers of a torrent with
// no connection and asks DHT and the trackers again (false when it has a
// connection and nothing was done); SetTrackers replaces its trackers;
// SetGuard turns the address guard of a torrent on or off (see
// AddRequest.Guard); Sources reports what the engine knows of each tracker
// from its announces (it contacts nobody).
type PeerFinder interface {
	RefreshPeers(ref string) (bool, error)
	SetTrackers(ref string, trackers []string) error
	SetGuard(ref string, on bool) error
	Sources(ref string) ([]TrackerState, error)
}

// TrackerState is one tracker of a torrent as its announces left it.
type TrackerState struct {
	URL       string `json:"url"`
	Tier      int    `json:"tier"`
	Contacted bool   `json:"contacted"` // an announce was answered or failed
	Fails     int    `json:"fails"`     // failures in a row; 0 answered the last time
	Message   string `json:"message"`   // the failure reason, or a working tracker's warning
	Seeds     int    `json:"seeds"`     // -1 unknown
	Leechers  int    `json:"leechers"`  // -1 unknown
	Updating  bool   `json:"updating"`  // an announce is under way
}
