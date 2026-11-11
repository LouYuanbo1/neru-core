// Package tesseract recognizes screen text with a local tesseract
// installation.
//
// It offers the same engine through two cgo-free paths:
//
//   - NewLibrary loads libtesseract with purego and calls its C API in process,
//     which is the closest match to the original application's backend and the
//     fastest path. A missing library reports errs.CodeNotSupported naming the
//     runtime package to install.
//   - New (also NewCLI) runs the operator's own `tesseract` executable and
//     parses its TSV output. It needs no shared library, only the binary, and is
//     a useful fallback when the runtime is absent.
//
// Either way the module stays pure Go and gains no link-time dependency. It is
// the default engine on Linux and the fallback on macOS, and it works on any
// platform where tesseract is installed.
//
// Text is screen content: the frame and the recognized strings reach the
// caller and nothing else. The temporary PNG the CLI path writes is removed
// before Recognize returns.
package tesseract

import (
	"bytes"
	"context"
	"encoding/csv"
	"image"
	"image/png"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/vision/ocr/engine"
)

// DefaultPageSegMode is tesseract's sparse-text mode, the one that behaves best
// on a screenshot of an interface rather than a scanned page. It is --psm 6
// ("assume a single uniform block of text") because that is what the original
// implementation used for screen frames.
const DefaultPageSegMode = 6

// Options configure a Recognizer.
type Options struct {
	// Binary is the tesseract executable to run. Empty means "tesseract" from
	// PATH.
	Binary string
	// PageSegMode is passed as --psm. Zero means DefaultPageSegMode.
	PageSegMode int
	// Languages overrides engine.Options.Languages when set. Entries are joined
	// with "+", tesseract's own syntax ("eng+chi_sim").
	Languages []string
}

// Recognizer is a tesseract-backed engine.Recognizer.
//
// It is safe for concurrent use: invocations are serialized, because spawning
// one tesseract per call is cheap and a shared process would not be.
type Recognizer struct {
	binary      string
	pageSegMode int
	languages   []string

	mu    sync.Mutex
	found string
	err   error
}

// NewCLI returns a recognizer that runs the tesseract executable. It is the
// same engine as New, named for symmetry with NewLibrary: NewCLI spawns a
// process, NewLibrary calls libtesseract in-process.
func NewCLI(opts Options) *Recognizer {
	return New(opts)
}

// New returns a recognizer. It performs no I/O: the binary is discovered on
// the first Recognize or Health call, so constructing a recognizer never fails
// for a missing installation.
func New(opts Options) *Recognizer {
	return &Recognizer{
		binary:      opts.Binary,
		pageSegMode: opts.PageSegMode,
		languages:   append([]string(nil), opts.Languages...),
	}
}

// Health reports whether a tesseract binary can be executed. It remembers the
// answer, so the pipeline's pre-flight probe costs one stat after the first
// call.
func (r *Recognizer) Health(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, err := r.locate()

	return err
}

// locate resolves the executable, caching the result.
func (r *Recognizer) locate() (string, error) {
	if r.found != "" || r.err != nil {
		return r.found, r.err
	}

	name := r.binary
	if name == "" {
		name = "tesseract"
	}

	path, err := exec.LookPath(name)
	if err != nil {
		r.err = errs.Newf(
			errs.CodeNotSupported,
			"text recognition needs tesseract: install it (for example the %q package) or pass Options.Binary",
			"tesseract-ocr",
		)

		return "", r.err
	}

	r.found = path

	return path, nil
}

// Recognize runs tesseract over img and returns its words or lines in the
// image's pixel space.
//
// A deadline in ctx is a hard budget: the process is killed when it expires
// and the call reports errs.CodeTimeout.
func (r *Recognizer) Recognize(
	ctx context.Context,
	img *image.RGBA,
	opts engine.Options,
) ([]engine.Word, error) {
	if img == nil || img.Rect.Dx() <= 0 || img.Rect.Dy() <= 0 {
		return nil, errs.New(errs.CodeInvalidInput, "tesseract needs a non-empty frame")
	}

	r.mu.Lock()
	binary, err := r.locate()
	r.mu.Unlock()

	if err != nil {
		return nil, err
	}

	path, err := writeTempPNG(img)
	if err != nil {
		return nil, err
	}

	defer os.Remove(path)

	args := []string{path, "stdout"}
	if language := r.languageOption(opts); language != "" {
		args = append(args, "-l", language)
	}

	args = append(args,
		"--psm", strconv.Itoa(r.pageMode()),
		"tsv",
	)

	cmd := exec.CommandContext(ctx, binary, args...)

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, errs.Wrap(ctx.Err(), errs.CodeTimeout, "tesseract did not finish within the deadline")
	}

	if runErr != nil {
		// tesseract names a missing language pack on stderr; keep it, but do
		// not wrap the whole output when it is simply empty.
		detail := strings.TrimSpace(stderr.String())

		return nil, errs.Newf(
			errs.CodeAccessibilityFailed,
			"tesseract failed: %v: %s",
			runErr,
			detail,
		)
	}

	words, err := ParseTSV(stdout.Bytes(), opts.WordLevel)
	if err != nil {
		return nil, err
	}

	if opts.MinConfidence > 0 {
		kept := words[:0]

		for _, word := range words {
			if word.Confidence >= opts.MinConfidence {
				kept = append(kept, word)
			}
		}

		words = kept
	}

	return words, nil
}

// pageMode returns the configured --psm value.
func (r *Recognizer) pageMode() int {
	if r.pageSegMode <= 0 {
		return DefaultPageSegMode
	}

	return r.pageSegMode
}

// languageOption returns the -l value for this call: the per-call languages
// win over the recognizer's configured ones, tesseract's syntax included.
func (r *Recognizer) languageOption(opts engine.Options) string {
	languages := opts.Languages
	if len(languages) == 0 {
		languages = r.languages
	}

	clean := make([]string, 0, len(languages))

	for _, language := range languages {
		if trimmed := strings.TrimSpace(language); trimmed != "" {
			clean = append(clean, trimmed)
		}
	}

	return strings.Join(clean, "+")
}

// writeTempPNG writes the frame to a temporary file and returns its path. The
// caller removes it.
func writeTempPNG(img *image.RGBA) (string, error) {
	file, err := os.CreateTemp("", "neru-core-ocr-*.png")
	if err != nil {
		return "", errs.Wrap(err, errs.CodeInternal, "could not create an OCR scratch file")
	}

	path := file.Name()

	encodeErr := png.Encode(file, img)
	closeErr := file.Close()

	if encodeErr != nil {
		os.Remove(path)

		return "", errs.Wrap(encodeErr, errs.CodeInternal, "could not encode the OCR frame")
	}

	if closeErr != nil {
		os.Remove(path)

		return "", errs.Wrap(closeErr, errs.CodeInternal, "could not write the OCR frame")
	}

	return path, nil
}

// ParseTSV turns tesseract's TSV output into words or lines.
//
// The format is one record per row with eleven tab-separated fields:
// level, page, block, par, line, word, left, top, width, height, conf, text.
// Level 5 rows are words; level 4 rows are lines. Coordinates are image
// pixels, which is the space engine.Word documents. Rows whose text is empty or
// whose confidence is negative are skipped.
//
// With wordLevel false, words are grouped by their line and joined with
// spaces; the line's box is the union of its words and its confidence the
// lowest word confidence, which is the honest summary of "some part of this
// line was uncertain".
func ParseTSV(data []byte, wordLevel bool) ([]engine.Word, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = '\t'
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	records, err := reader.ReadAll()
	if err != nil {
		return nil, errs.Wrap(err, errs.CodeInternal, "could not parse tesseract output")
	}

	var words []engine.Word

	type lineKey struct{ page, block, par, line int }

	type lineAccumulator struct {
		text    []string
		bounds  image.Rectangle
		minConf float64
	}

	lines := make(map[lineKey]*lineAccumulator)
	order := make([]lineKey, 0, len(records))

	for _, record := range records {
		if len(record) < 12 {
			continue
		}

		level, err := strconv.Atoi(strings.TrimSpace(record[0]))
		if err != nil || level != 5 {
			continue
		}

		text := strings.TrimSpace(record[11])
		if text == "" {
			continue
		}

		confidence, err := strconv.ParseFloat(strings.TrimSpace(record[10]), 64)
		if err != nil || confidence < 0 {
			continue
		}

		left, errLeft := strconv.Atoi(strings.TrimSpace(record[6]))
		top, errTop := strconv.Atoi(strings.TrimSpace(record[7]))
		width, errWidth := strconv.Atoi(strings.TrimSpace(record[8]))
		height, errHeight := strconv.Atoi(strings.TrimSpace(record[9]))

		if errLeft != nil || errTop != nil || errWidth != nil || errHeight != nil {
			continue
		}

		bounds := image.Rect(left, top, left+width, top+height)
		normalized := confidence / 100

		if wordLevel {
			words = append(words, engine.Word{Text: text, Bounds: bounds, Confidence: normalized})

			continue
		}

		page, _ := strconv.Atoi(strings.TrimSpace(record[1]))
		block, _ := strconv.Atoi(strings.TrimSpace(record[2]))
		par, _ := strconv.Atoi(strings.TrimSpace(record[3]))
		line, _ := strconv.Atoi(strings.TrimSpace(record[4]))

		key := lineKey{page: page, block: block, par: par, line: line}

		accumulator, seen := lines[key]
		if !seen {
			accumulator = &lineAccumulator{bounds: bounds, minConf: normalized}
			lines[key] = accumulator
			order = append(order, key)
		}

		accumulator.text = append(accumulator.text, text)
		accumulator.bounds = accumulator.bounds.Union(bounds)

		if normalized < accumulator.minConf {
			accumulator.minConf = normalized
		}
	}

	if wordLevel {
		return words, nil
	}

	for _, key := range order {
		accumulator := lines[key]

		words = append(words, engine.Word{
			Text:       strings.Join(accumulator.text, " "),
			Bounds:     accumulator.bounds,
			Confidence: accumulator.minConf,
		})
	}

	return words, nil
}

var _ engine.Recognizer = (*Recognizer)(nil)
