package types

// TuneArgument represents a single tuning argument
type TuneArgument struct {
	Key   string `json:"key"`   // The name of the argument (or argument itself if Bare)
	Value string `json:"value"` // The value of the argument
	Bare  bool   `json:"bare"`  // If the kernel argument is a bare argument (no value expected)
}
