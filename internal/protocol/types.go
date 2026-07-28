package protocol

type Picker string

const (
	PickerCD Picker = "cd"
	PickerCP Picker = "cp"
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
	OpModeInsert Opcode = "mi"
	OpModeAdd    Opcode = "ma"
	OpEscape     Opcode = "es"
	OpForward    Opcode = "fw"
	OpParent     Opcode = "up"
	OpSlash      Opcode = "sl"
	OpHome       Opcode = "hm"
	OpEnter      Opcode = "en"
)

type Event struct {
	Opcode      Opcode
	Key         string
	Query       []byte
	CurrentItem []byte
}

type Effect struct {
	Mode             Mode        `json:"mode"`
	Prompt           string      `json:"prompt"`
	Search           string      `json:"search"`
	Rebind           Mode        `json:"rebind"`
	ClearQuery       bool        `json:"clear_query"`
	ClearMulti       bool        `json:"clear_multi"`
	Accept           bool        `json:"accept"`
	Ignore           bool        `json:"ignore"`
	Put              string      `json:"put"`
	ReloadGeneration uint64      `json:"reload_generation"`
	Cursor           CursorShape `json:"cursor"`
	ErrorPrompt      bool        `json:"error_prompt"`
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
