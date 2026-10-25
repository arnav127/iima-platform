package jobgen

// The subset of Nomad's JSON job format the platform uses. Field names match Nomad's API;
// durations are nanoseconds. See https://developer.hashicorp.com/nomad/api-docs/json-jobs.

type Job struct {
	ID          string            `json:"ID"`
	Name        string            `json:"Name"`
	Type        string            `json:"Type"`
	Datacenters []string          `json:"Datacenters"`
	Meta        map[string]string `json:"Meta,omitempty"`
	Update      *Update           `json:"Update,omitempty"`
	TaskGroups  []*TaskGroup      `json:"TaskGroups"`
}

type Update struct {
	MaxParallel      int    `json:"MaxParallel"`
	HealthCheck      string `json:"HealthCheck"`
	MinHealthyTime   int64  `json:"MinHealthyTime"`
	HealthyDeadline  int64  `json:"HealthyDeadline"`
	ProgressDeadline int64  `json:"ProgressDeadline"`
	AutoRevert       bool   `json:"AutoRevert"`
}

type TaskGroup struct {
	Name          string             `json:"Name"`
	Count         int                `json:"Count"`
	Networks      []*Network         `json:"Networks,omitempty"`
	Services      []*Service         `json:"Services,omitempty"`
	Volumes       map[string]*Volume `json:"Volumes,omitempty"`
	RestartPolicy *RestartPolicy     `json:"RestartPolicy,omitempty"`
	ShutdownDelay int64              `json:"ShutdownDelay,omitempty"`
	Tasks         []*Task            `json:"Tasks"`
}

type Network struct {
	DynamicPorts []Port `json:"DynamicPorts,omitempty"`
}

type Port struct {
	Label string `json:"Label"`
}

type Service struct {
	Name      string   `json:"Name"`
	Provider  string   `json:"Provider"`
	PortLabel string   `json:"PortLabel"`
	Tags      []string `json:"Tags,omitempty"`
	Checks    []*Check `json:"Checks,omitempty"`
}

type Check struct {
	Name     string `json:"Name"`
	Type     string `json:"Type"`
	Path     string `json:"Path"`
	Interval int64  `json:"Interval"`
	Timeout  int64  `json:"Timeout"`
}

type Volume struct {
	Name           string `json:"Name"`
	Type           string `json:"Type"`
	Source         string `json:"Source"`
	AccessMode     string `json:"AccessMode,omitempty"`     // required for dynamic host volumes
	AttachmentMode string `json:"AttachmentMode,omitempty"` // required for dynamic host volumes
}

type RestartPolicy struct {
	Attempts int    `json:"Attempts"`
	Interval int64  `json:"Interval"`
	Delay    int64  `json:"Delay"`
	Mode     string `json:"Mode"`
}

type Task struct {
	Name         string            `json:"Name"`
	Driver       string            `json:"Driver"`
	Config       map[string]any    `json:"Config"`
	Env          map[string]string `json:"Env,omitempty"`
	Artifacts    []*Artifact       `json:"Artifacts,omitempty"`
	Templates    []*Template       `json:"Templates,omitempty"`
	VolumeMounts []*VolumeMount    `json:"VolumeMounts,omitempty"`
	Resources    *Resources        `json:"Resources,omitempty"`
	KillTimeout  int64             `json:"KillTimeout,omitempty"`
}

type Artifact struct {
	GetterSource  string            `json:"GetterSource"`
	GetterOptions map[string]string `json:"GetterOptions,omitempty"`
	RelativeDest  string            `json:"RelativeDest"`
}

type Template struct {
	EmbeddedTmpl string `json:"EmbeddedTmpl"`
	DestPath     string `json:"DestPath"`
	Envvars      bool   `json:"Envvars"`
	ChangeMode   string `json:"ChangeMode"`
}

type VolumeMount struct {
	Volume      string `json:"Volume"`
	Destination string `json:"Destination"`
}

type Resources struct {
	CPU      int `json:"CPU"`
	MemoryMB int `json:"MemoryMB"`
}
