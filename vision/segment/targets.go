package segment

// Targets names what a call to Detect should hint. The strings are the
// package's own vocabulary; no domain package is involved.
const (
	// TargetsWords hints every word.
	TargetsWords = "words"
	// TargetsPhrases hints words joined into phrases.
	TargetsPhrases = "phrases"
	// TargetsControls hints controls only, dropping text.
	TargetsControls = "controls"
)
