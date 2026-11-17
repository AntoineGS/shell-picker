package protocol

type Picker string

const (
	PickerCD     Picker = "cd"
	PickerCP     Picker = "cp"
	PickerSearch Picker = "search"
)

// Source identifies the candidate source independently of the interaction mode.
// The zero value intentionally keeps existing sessions in the browser.
type Source uint8

const (
	SourceBrowser Source = iota
	SourceIndexed
)

type Mode string

const (
	ModeInsert Mode = "insert"
	ModeNormal Mode = "normal"
	ModeAdd    Mode = "add"
)

type Kind string

const (
	KindLocal     Kind = "local"
	KindDirectory Kind = "directory"
	KindFile      Kind = "file"
	KindZoxide    Kind = "zoxide"
	KindDrive     Kind = "drive"
	KindVirtual   Kind = "virtual"
)

const VirtualDrivesTarget = "drives"

type CursorShape string

const (
	CursorLine  CursorShape = "line"
	CursorBlock CursorShape = "block"
)

type Opcode string

const (
	OpModeInsert   Opcode = "mi"
	OpModeAdd      Opcode = "ma"
	OpEscape       Opcode = "es"
	OpForward      Opcode = "fw"
	OpParent       Opcode = "up"
	OpSlash        Opcode = "sl"
	OpHome         Opcode = "hm"
	OpEnter        Opcode = "en"
	OpRestoreView  Opcode = "rs"
	OpSourceToggle Opcode = "sf"
	OpMarkToggle   Opcode = "mk"
)

type Event struct {
	Opcode          Opcode
	Key             string
	Query           []byte
	CurrentItem     []byte
	SelectedRecords [][]byte
}

type Effect struct {
	Mode              Mode        `json:"mode"`
	Prompt            string      `json:"prompt"`
	Header            string      `json:"header"`
	Search            string      `json:"search"`
	Query             string      `json:"query"`
	SetQuery          bool        `json:"set_query"`
	RestorePositions  string      `json:"restore_positions"`
	Rebind            Mode        `json:"rebind"`
	ClearQuery        bool        `json:"clear_query"`
	ClearMulti        bool        `json:"clear_multi"`
	ToggleSelection   bool        `json:"toggle_selection"`
	Accept            bool        `json:"accept"`
	Abort             bool        `json:"abort"`
	Ignore            bool        `json:"ignore"`
	Put               string      `json:"put"`
	InvalidPath       bool        `json:"invalid_path"`
	ReloadGeneration  uint64      `json:"reload_generation"`
	RestoreGeneration uint64      `json:"restore_generation"`
	Cursor            CursorShape `json:"cursor"`
	ErrorPrompt       bool        `json:"error_prompt"`
}

type OutputFormat string

const (
	OutputNUL  OutputFormat = "nul"
	OutputNUON OutputFormat = "nuon"
)

type Status string

const (
	StatusAccepted Status = "accepted"
	StatusAborted  Status = "aborted"
)

type Outcome struct {
	Status Status
	Paths  [][]byte
}

type ResolvedCandidate struct {
	Kind            Kind
	Path            []byte
	Size            int64
	ModTimeUnixNano int64
	Mode            uint32
}
