package tesseract

import (
	"context"
	"image"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/internal/native"
	"github.com/LouYuanbo1/neru-core/vision/ocr"
)

// LibraryOptions configure the in-process libtesseract recognizer.
//
// DataPath is passed to TessBaseAPIInit2 unchanged; empty means the
// installation's compiled-in tessdata directory or $TESSDATA_PREFIX. Language
// is tesseract's own language name ("eng", "chi_sim"), and several may be
// joined with "+".
type LibraryOptions struct {
	DataPath    string
	Language    string
	PageSegMode int
}

// LibraryRecognizer recognizes text by calling libtesseract through purego.
//
// It is the closest pure-Go equivalent of the original application's Linux
// backend: the same C API, the same sparse-text page mode, the same word/line
// iteration, but loaded at run time instead of linked, so the module still
// builds everywhere without cgo. One engine per (datapath, language) is cached
// for the process, because initialization loads an LSTM model from disk.
//
// Tesseract is not thread-safe; calls are serialized.
type LibraryRecognizer struct {
	options LibraryOptions

	mu      sync.Mutex
	engines map[string]uintptr
	closed  bool
}

// tesseractSymbols is the subset of tesseract's C API the recognizer calls.
type tesseractSymbols struct {
	create               func() uintptr
	deleteAPI            func(api uintptr)
	init2                func(api uintptr, datapath uintptr, language string, oem int32) int32
	setPageSegMode       func(api uintptr, mode int32)
	setImage             func(api uintptr, pixels unsafe.Pointer, width, height, bytesPerPixel, bytesPerLine int32)
	setSourceResolution  func(api uintptr, dpi int32)
	getIterator          func(api uintptr) uintptr
	resultIteratorDelete func(iterator uintptr)
	next                 func(iterator uintptr, level int32) int32
	boundingBox          func(iterator uintptr, level int32, left, top, right, bottom *int32) int32
	resultText           func(iterator uintptr, level int32) *byte
	resultConfidence     func(iterator uintptr, level int32) float32
	deleteText           func(text *byte)

	library *native.Library
}

const (
	tessOEMLSTMOnly          int32 = 1
	tessPSMSparseText        int32 = 11
	tessSourceDPI            int32 = 96
	tessBytesPerPixel        int32 = 4
	tessPageIteratorWord     int32 = 3
	tessPageIteratorTextLine int32 = 2
)

var (
	tessOnce     sync.Once
	tessSymbols  *tesseractSymbols
	tessLoadFail error
)

// loadTesseract binds libtesseract once. A missing library is reported as
// CodeNotSupported with the package to install, not as a crash.
func loadTesseract() (*tesseractSymbols, error) {
	tessOnce.Do(func() {
		tessSymbols, tessLoadFail = bindTesseract()
	})

	return tessSymbols, tessLoadFail
}

// bindTesseract opens the platform's libtesseract and binds the C API.
func bindTesseract() (*tesseractSymbols, error) {
	library, err := native.Open(tesseractLibraryNames()...)
	if err != nil {
		return nil, errs.Newf(
			errs.CodeNotSupported,
			"text recognition needs libtesseract: install the tesseract runtime (for example the %q package): %v",
			"libtesseract",
			err,
		)
	}

	symbols := &tesseractSymbols{library: library}

	bind := func(name string, target any) error {
		if err := library.Func(name, target); err != nil {
			return errs.Newf(
				errs.CodeNotSupported,
				"libtesseract is missing %s: install a tesseract 4 or newer runtime",
				name,
			)
		}

		return nil
	}

	for _, bindErr := range []error{
		bind("TessBaseAPICreate", &symbols.create),
		bind("TessBaseAPIDelete", &symbols.deleteAPI),
		bind("TessBaseAPIInit2", &symbols.init2),
		bind("TessBaseAPISetPageSegMode", &symbols.setPageSegMode),
		bind("TessBaseAPISetImage", &symbols.setImage),
		bind("TessBaseAPISetSourceResolution", &symbols.setSourceResolution),
		bind("TessBaseAPIGetIterator", &symbols.getIterator),
		bind("TessResultIteratorDelete", &symbols.resultIteratorDelete),
		bind("TessPageIteratorNext", &symbols.next),
		bind("TessPageIteratorBoundingBox", &symbols.boundingBox),
		bind("TessResultIteratorGetUTF8Text", &symbols.resultText),
		bind("TessResultIteratorConfidence", &symbols.resultConfidence),
		bind("TessDeleteText", &symbols.deleteText),
	} {
		if bindErr != nil {
			return nil, bindErr
		}
	}

	return symbols, nil
}

// tesseractLibraryNames lists the sonames to try, most specific first.
func tesseractLibraryNames() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/opt/homebrew/lib/libtesseract.5.dylib",
			"/usr/local/lib/libtesseract.5.dylib",
			"libtesseract.5.dylib",
			"libtesseract.dylib",
		}
	case "windows":
		return []string{"tesseract5.dll", "libtesseract-5.dll", "tesseract.dll"}
	default:
		return []string{"libtesseract.so.5", "libtesseract.so"}
	}
}

// NewLibrary returns an in-process tesseract recognizer. It binds libtesseract
// immediately, so a machine without it fails here with CodeNotSupported rather
// than at the first capture.
func NewLibrary(opts LibraryOptions) (*LibraryRecognizer, error) {
	if _, err := loadTesseract(); err != nil {
		return nil, err
	}

	return &LibraryRecognizer{
		options: opts,
		engines: make(map[string]uintptr, 1),
	}, nil
}

// Health reports whether libtesseract is available.
func (r *LibraryRecognizer) Health(context.Context) error {
	_, err := loadTesseract()

	return err
}

// Recognize runs one tesseract pass over img.
func (r *LibraryRecognizer) Recognize(
	ctx context.Context,
	img *image.RGBA,
	opts ocr.Options,
) ([]ocr.Word, error) {
	if img == nil || img.Rect.Dx() <= 0 || img.Rect.Dy() <= 0 {
		return nil, errs.New(errs.CodeInvalidInput, "tesseract needs a non-empty frame")
	}

	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.CodeContextCanceled, "text recognition canceled")
	}

	symbols, err := loadTesseract()
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil, errs.New(errs.CodeInternal, "the tesseract recognizer is closed")
	}

	language := r.language(opts)

	engine, err := r.engine(symbols, language)
	if err != nil {
		return nil, err
	}

	width, height := int32(img.Rect.Dx()), int32(img.Rect.Dy())
	stride := int32(img.Stride)

	symbols.setImage(engine, unsafe.Pointer(&img.Pix[0]), width, height, tessBytesPerPixel, stride)
	symbols.setSourceResolution(engine, tessSourceDPI)

	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.CodeContextCanceled, "text recognition canceled")
	}

	iterator := symbols.getIterator(engine)
	if iterator == 0 {
		return nil, errs.New(errs.CodeAccessibilityFailed, "tesseract produced no result iterator")
	}

	defer symbols.resultIteratorDelete(iterator)

	level := tessPageIteratorTextLine
	if opts.WordLevel {
		level = tessPageIteratorWord
	}

	var words []ocr.Word

	for {
		text := symbols.resultText(iterator, level)
		if text != nil {
			value := strings.TrimSpace(goCString(text))

			var left, top, right, bottom int32

			boxed := symbols.boundingBox(iterator, level, &left, &top, &right, &bottom) != 0
			confidence := clampConfidence(symbols.resultConfidence(iterator, level) / 100)

			symbols.deleteText(text)

			if value != "" && boxed && right > left && bottom > top {
				words = append(words, ocr.Word{
					Text:       value,
					Bounds:     image.Rect(int(left), int(top), int(right), int(bottom)),
					Confidence: confidence,
				})
			}
		}

		if symbols.next(iterator, level) == 0 {
			break
		}
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

// language returns the language string for this call: per-call languages win,
// then the configured one, then "eng".
func (r *LibraryRecognizer) language(opts ocr.Options) string {
	languages := opts.Languages
	if len(languages) == 0 && r.options.Language != "" {
		languages = []string{r.options.Language}
	}

	clean := make([]string, 0, len(languages))

	for _, language := range languages {
		if trimmed := strings.TrimSpace(language); trimmed != "" {
			clean = append(clean, trimmed)
		}
	}

	if len(clean) == 0 {
		return "eng"
	}

	return strings.Join(clean, "+")
}

// engine returns the cached engine for a language, creating it on first use.
func (r *LibraryRecognizer) engine(symbols *tesseractSymbols, language string) (uintptr, error) {
	key := r.options.DataPath + "\x00" + language
	if cached, ok := r.engines[key]; ok {
		return cached, nil
	}

	api := symbols.create()
	if api == 0 {
		return 0, errs.New(errs.CodeInternal, "TessBaseAPICreate returned nil")
	}

	// TessBaseAPIInit2 treats a non-null datapath as authoritative, so the
	// empty option must be a real NULL rather than a pointer to "".
	var datapath unsafe.Pointer

	if r.options.DataPath != "" {
		pointer, err := syscall.BytePtrFromString(r.options.DataPath)
		if err != nil {
			symbols.deleteAPI(api)

			return 0, errs.Wrap(err, errs.CodeInvalidInput, "tesseract datapath is not a valid path")
		}

		datapath = unsafe.Pointer(pointer)
	}

	status := symbols.init2(api, uintptr(datapath), language, tessOEMLSTMOnly)

	runtime.KeepAlive(datapath)

	if status != 0 {
		symbols.deleteAPI(api)

		return 0, errs.Newf(
			errs.CodeNotSupported,
			"tesseract language %q is not installed: install its tessdata (for example tesseract-ocr-%s)",
			language,
			language,
		)
	}

	pageSegMode := int32(r.options.PageSegMode)
	if pageSegMode <= 0 {
		pageSegMode = tessPSMSparseText
	}

	symbols.setPageSegMode(api, pageSegMode)
	r.engines[key] = api

	return api, nil
}

// Close deletes every cached engine. It is idempotent.
func (r *LibraryRecognizer) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil
	}

	r.closed = true

	if symbols, err := loadTesseract(); err == nil {
		for _, api := range r.engines {
			symbols.deleteAPI(api)
		}
	}

	r.engines = nil

	return nil
}

// goCString copies a NUL-terminated C string into Go memory, with a cap so a
// malformed pointer cannot scan the address space.
func goCString(value *byte) string {
	if value == nil {
		return ""
	}

	const maxLength = 4096

	buffer := make([]byte, 0, 64)

	for index := 0; index < maxLength; index++ {
		char := *(*byte)(unsafe.Add(unsafe.Pointer(value), index))
		if char == 0 {
			break
		}

		buffer = append(buffer, char)
	}

	return string(buffer)
}

// clampConfidence keeps a tesseract confidence in 0..1. The C API returns -1
// for a word it could not score.
func clampConfidence(value float32) float64 {
	if value < 0 {
		return 0
	}

	if value > 1 {
		return 1
	}

	return float64(value)
}

var _ ocr.Recognizer = (*LibraryRecognizer)(nil)
var _ ocr.HealthChecker = (*LibraryRecognizer)(nil)
