//go:build windows

package winrt

import (
	"context"
	"image"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/vision/ocr/engine"
)

// Text recognition through Windows.Media.Ocr.
//
// The engine is created from an ordered language preference and needs the OCR
// language pack for at least one of those languages, which Windows installs with
// a language's "Basic typing" feature. A machine without one gets
// CodeNotSupported naming that remedy, from Health and from Recognize, rather
// than a recognition that silently finds nothing.
//
// Everything below runs on one OS thread the process keeps in the WinRT
// multithreaded apartment for its lifetime, so the engine can be created once
// and reused, because RoUninitialize on the last thread in an apartment tears it
// down together with every object in it, and a caller recognizes far more often
// than it starts. Requests are serialized through that thread, which also
// serializes the engine, and Windows.Media.Ocr is not reentrant anyway.
//
// Privacy: the frame and the text read from it are screen content. Both reach
// the caller and nowhere else. Nothing here logs.

const (
	ocrEngineClass      = "Windows.Media.Ocr.OcrEngine"
	softwareBitmapClass = "Windows.Graphics.Imaging.SoftwareBitmap"
	bufferClass         = "Windows.Storage.Streams.Buffer"
	languageClass       = "Windows.Globalization.Language"

	// bitmapPixelFormatBgra8 is BitmapPixelFormat.Bgra8, the layout a DIB
	// capture already produces before it swaps into image.RGBA order.
	bitmapPixelFormatBgra8 = 87

	// Vtable slots past the IInspectable prefix, in the order the SDK's
	// generated headers declare the methods.
	ocrEngineStaticsMaxImageDimension     = inspectableSlots + 0
	ocrEngineStaticsTryCreateFromLanguage = inspectableSlots + 3
	ocrEngineStaticsTryCreateUserProfile  = inspectableSlots + 4
	ocrEngineRecognizeAsync               = inspectableSlots + 0
	ocrResultLines                        = inspectableSlots + 0
	ocrLineWords                          = inspectableSlots + 0
	ocrLineText                           = inspectableSlots + 1
	ocrWordBoundingRect                   = inspectableSlots + 0
	ocrWordText                           = inspectableSlots + 1
	languageFactoryCreateLanguage         = inspectableSlots + 0
	softwareBitmapFactoryCreate           = inspectableSlots + 0
	softwareBitmapCopyFromBuffer          = inspectableSlots + 11
	bufferFactoryCreate                   = inspectableSlots + 0
	bufferPutLength                       = inspectableSlots + 2
	asyncOperationGetResults              = inspectableSlots + 2
	asyncInfoStatus                       = inspectableSlots + 1
	asyncInfoErrorCode                    = inspectableSlots + 2
	asyncInfoCancel                       = inspectableSlots + 3
	asyncInfoClose                        = inspectableSlots + 4

	// AsyncStatus values (asyncinfo.h).
	asyncStatusCompleted = 1
	asyncStatusCanceled  = 2
	asyncStatusError     = 3

	// ocrPollInterval is how often a recognition in flight is checked. The
	// engine takes tens of milliseconds on a window, so a millisecond of
	// granularity costs nothing a user can feel and needs no COM completion
	// handler implemented in Go.
	ocrPollInterval = time.Millisecond

	// eInvalidArg is E_INVALIDARG, which Language.Create reports for a string
	// that is not a language tag. That is the caller's input, not a failure of
	// the machine, so it is reported as CodeInvalidInput.
	eInvalidArg = 0x80070057
)

// DefaultTimeout bounds one recognition that is given no deadline. A caller
// should pass a context with a deadline; this ceiling exists so that a caller
// that does not, and an engine that wedges, cannot occupy the one shared OCR
// thread for the rest of the process's life.
const DefaultTimeout = 30 * time.Second

// wordConfidence is the score every run carries. Windows.Media.Ocr reports no
// per-word confidence: the engine has already dropped what it could not read, so
// every run it does report is one it considers text, and neru's Windows backend
// reported that as 1. A caller's floor therefore keeps everything.
const wordConfidence = 1.0

// Interface IDs, from the SDK headers named in winrt_windows.go.
var (
	iidOcrEngineStatics      = mustGUID("{5BFFA85A-3384-3540-9940-699120D428A8}")
	iidLanguageFactory       = mustGUID("{9B0252AC-0C27-44F8-B792-9793FB66C63E}")
	iidSoftwareBitmapFactory = mustGUID("{C99FEB69-2D62-4D47-A6B3-4FDB6A07FDF8}")
	iidBufferFactory         = mustGUID("{71AF914D-C10F-484B-BC50-14BC623B3A27}")
	iidBufferByteAccess      = mustGUID("{905A0FEF-BC53-11DF-8C49-001E4FC686DA}")
	iidAsyncInfo             = mustGUID("{00000036-0000-0000-C000-000000000046}")
)

// Recognizer is one handle on the process's OCR engine.
//
// It is safe for concurrent use. Every call is serialized through the one WinRT
// thread the process keeps, so two recognizers recognize one after the other
// rather than at once.
type Recognizer struct {
	worker  *ocrWorker
	options Options
	closed  atomic.Bool
}

var (
	_ engine.Recognizer    = (*Recognizer)(nil)
	_ engine.HealthChecker = (*Recognizer)(nil)
)

// New returns a recognizer for Windows.Media.Ocr.
//
// It takes zero or one Options value: New() asks the engine to use the account's
// language profile, and New(Options{Languages: []string{"en-US"}}) asks for that
// language first. The engine itself is created on the first Health or Recognize
// call, so New only joins the process's WinRT thread, and it is that step which
// can fail here.
func New(options ...Options) (*Recognizer, error) {
	if err := validateOptionCount(len(options)); err != nil {
		return nil, err
	}

	var opts Options
	if len(options) == 1 {
		opts = options[0]
	}

	bridge := sharedWorker()

	initErr := bridge.do(context.Background(), "text recognition setup", func() error {
		bridge.users++

		return nil
	})
	if initErr != nil {
		return nil, initErr
	}

	return &Recognizer{worker: bridge, options: opts.normalized()}, nil
}

// Recognize reads the text in img and returns each run with its box, in img's
// own pixel space.
//
// opts.WordLevel asks for one run per word. Otherwise a run is a line, and its
// box is the union of its words'. opts.Languages overrides the recognizer's own
// language order for this call; an empty list on both means the account's
// language profile. opts.MinConfidence is not applied: every run carries 1, so
// a floor is the caller's to compare against.
//
// ctx is a hard budget. A deadline that expires cancels the engine's operation
// and reports errs.CodeTimeout; a cancellation reports errs.CodeContextCanceled.
// A context with no deadline is bounded by DefaultTimeout.
func (r *Recognizer) Recognize(
	ctx context.Context,
	img *image.RGBA,
	opts engine.Options,
) ([]engine.Word, error) {
	if r == nil || r.closed.Load() {
		return nil, errClosed()
	}

	if img == nil || img.Bounds().Empty() {
		return nil, errEmptyImage()
	}

	if ctx == nil {
		ctx = context.Background()
	}

	// A caller with no deadline still gets a ceiling, so one wedged engine
	// cannot hold the shared thread forever. A caller with a deadline keeps it.
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	tags := effectiveLanguages(r.options.Languages, opts.Languages)
	wordLevel := opts.WordLevel

	var words []engine.Word

	err := r.worker.do(ctx, "text recognition", func() error {
		// Close may have been called while this request waited its turn. The
		// worker serves one request at a time, so a Close that ran before this
		// one has already released the engine, and rebuilding it here would
		// leave it cached for a recognizer nobody holds.
		if r.closed.Load() {
			return errClosed()
		}

		if engineErr := r.worker.ensureEngine(tags); engineErr != nil {
			return engineErr
		}

		var recognizeErr error

		words, recognizeErr = r.worker.recognize(ctx, img, wordLevel)

		return recognizeErr
	})
	if err != nil {
		return nil, err
	}

	return words, nil
}

// Health reports whether text recognition can run on this machine: the runtime
// is up and an OCR language pack is installed for one of the requested
// languages.
//
// Asking also warms the engine, and the handle is kept until the last recognizer
// is closed, so a caller that probes before capturing pays for the engine once.
// Like neru's OCRHealth, a machine with no pack reports CodeNotSupported with
// the install steps, which is a state a caller can degrade from rather than an
// error to retry.
func (r *Recognizer) Health(ctx context.Context) error {
	if r == nil || r.closed.Load() {
		return errClosed()
	}

	if ctx == nil {
		ctx = context.Background()
	}

	return r.worker.do(ctx, "text recognition health check", func() error {
		return r.worker.ensureEngine(r.options.Languages)
	})
}

// Close releases this recognizer's hold on the engine.
//
// The WinRT apartment and its thread stay for the process, as neru keeps them: a
// thread cannot leave the apartment without tearing down every object created in
// it, and another recognizer may be using it. The engine's reference is dropped
// when the last recognizer closes, so a program that is done with recognition
// does not keep the model loaded.
//
// Close is idempotent, and any call after it reports CodeInvalidInput.
func (r *Recognizer) Close() error {
	if r == nil {
		return nil
	}

	if r.closed.Swap(true) {
		return nil
	}

	return r.worker.do(context.Background(), "text recognition shutdown", func() error {
		r.worker.releaseUser()

		return nil
	})
}

// ocrWorker is the one thread the apartment and the engine live on.
type ocrWorker struct {
	requests chan func()

	// initErr is why the worker could not start, read after start has
	// happened. nil means it is serving.
	initErr error

	// The fields below are touched only from the worker goroutine, which is
	// what serializes them.
	engine    unsafe.Pointer
	engineKey string
	maxImage  int
	users     int
}

var (
	workerOnce   sync.Once
	workerShared *ocrWorker
)

// sharedWorker starts the OCR thread on first use and returns it. The thread is
// never stopped: its goroutine parks on the request channel for the life of the
// process, which is what keeps the apartment, and the engine in it, alive.
func sharedWorker() *ocrWorker {
	workerOnce.Do(func() {
		shared := &ocrWorker{requests: make(chan func())}
		started := make(chan struct{})

		go shared.run(started)

		<-started
		workerShared = shared
	})

	return workerShared
}

// run is the worker goroutine: locked to its OS thread, joined to the
// multithreaded apartment, serving one request at a time until the process ends.
func (w *ocrWorker) run(started chan<- struct{}) {
	runtime.LockOSThread()

	w.initErr = roInitialize()

	close(started)

	if w.initErr != nil {
		return
	}

	for request := range w.requests {
		request()
	}
}

// do runs request on the worker thread and waits for it, giving up when ctx
// ends first. An abandoned request still runs, because it has already been
// handed over; it writes its result into a buffered channel nobody reads, so it
// leaks nothing.
func (w *ocrWorker) do(ctx context.Context, operation string, request func() error) error {
	if w.initErr != nil {
		return errs.Wrap(
			w.initErr,
			errs.CodeAccessibilityFailed,
			"the Windows Runtime could not be initialized for text recognition",
		)
	}

	if ctx == nil {
		ctx = context.Background()
	}

	done := make(chan error, 1)

	select {
	case w.requests <- func() { done <- request() }:
	case <-ctx.Done():
		return contextError(ctx, operation)
	}

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// The request may have finished between the two selects; prefer its
		// answer to the context's, so a completed recognition is not reported
		// as a timeout.
		select {
		case err := <-done:
			return err
		default:
		}

		return contextError(ctx, operation)
	}
}

// ensureEngine creates the engine for tags on first use and again whenever the
// requested language order changes. Worker thread only.
//
// The engine is cached rather than created per call because creating it loads
// the recognition model; the key makes a caller alternating between two
// language orders pay for a reload, which is the price of not holding every
// model at once.
func (w *ocrWorker) ensureEngine(tags []string) error {
	key := languageKey(tags)
	if w.engine != nil && w.engineKey == key {
		return nil
	}

	w.releaseEngine()

	statics, err := activationFactory(ocrEngineClass, &iidOcrEngineStatics)
	if err != nil {
		// Every Windows build Neru ships for carries this class, so its
		// absence is a broken install rather than an unsupported platform.
		return errs.Wrap(
			err,
			errs.CodeAccessibilityFailed,
			"Windows.Media.Ocr could not be reached on this Windows install",
		)
	}
	defer releaseObject(statics)

	var maxImage uint32

	hresult := winrtCall(
		statics,
		ocrEngineStaticsMaxImageDimension,
		uintptr(unsafe.Pointer(&maxImage)),
	)
	if hresultFailed(hresult) || maxImage == 0 {
		return errs.Wrap(
			hresultError("OcrEngine.MaxImageDimension", hresult),
			errs.CodeAccessibilityFailed,
			"could not read the OCR engine's image size limit",
		)
	}

	comEngine, err := w.createEngine(statics, tags)
	if err != nil {
		return err
	}

	w.engine = comEngine
	w.engineKey = key
	w.maxImage = int(maxImage)

	return nil
}

// createEngine asks the statics for an engine over tags, or over the account's
// language profile when tags is empty. Worker thread only.
func (w *ocrWorker) createEngine(statics unsafe.Pointer, tags []string) (unsafe.Pointer, error) {
	if len(tags) == 0 {
		var comEngine unsafe.Pointer

		hresult := winrtCall(
			statics,
			ocrEngineStaticsTryCreateUserProfile,
			uintptr(unsafe.Pointer(&comEngine)),
		)
		if hresultFailed(hresult) {
			return nil, errs.Wrap(
				hresultError("OcrEngine.TryCreateFromUserProfileLanguages", hresult),
				errs.CodeAccessibilityFailed,
				"could not create the OCR engine",
			)
		}

		// A null engine with S_OK is the documented answer for "none of the
		// profile languages has an OCR pack installed".
		if comEngine == nil {
			return nil, errNoLanguagePack()
		}

		return comEngine, nil
	}

	for _, tag := range tags {
		comEngine, err := w.createEngineForTag(statics, tag)
		if err != nil {
			return nil, err
		}

		if comEngine != nil {
			return comEngine, nil
		}
	}

	return nil, errNoLanguagePackFor(tags)
}

// createEngineForTag asks for an engine in one language. A nil engine and a nil
// error means the machine has no OCR pack for that tag, and the next preference
// is worth trying. Worker thread only.
func (w *ocrWorker) createEngineForTag(
	statics unsafe.Pointer,
	tag string,
) (unsafe.Pointer, error) {
	language, err := languageFromTag(tag)
	if err != nil {
		return nil, err
	}
	defer releaseObject(language)

	var comEngine unsafe.Pointer

	hresult := winrtCall(
		statics,
		ocrEngineStaticsTryCreateFromLanguage,
		uintptr(language),
		uintptr(unsafe.Pointer(&comEngine)),
	)
	if hresultFailed(hresult) {
		return nil, errs.Wrap(
			hresultError("OcrEngine.TryCreateFromLanguage", hresult),
			errs.CodeAccessibilityFailed,
			"could not create the OCR engine for the requested language",
		)
	}

	if comEngine == nil {
		return nil, nil
	}

	return comEngine, nil
}

// languageFromTag builds the Windows.Globalization.Language object the statics
// method takes. The caller releases it.
func languageFromTag(tag string) (unsafe.Pointer, error) {
	factory, err := activationFactory(languageClass, &iidLanguageFactory)
	if err != nil {
		return nil, errs.Wrap(
			err,
			errs.CodeAccessibilityFailed,
			"the WinRT language factory could not be reached",
		)
	}
	defer releaseObject(factory)

	name, err := newHString(tag)
	if err != nil {
		return nil, errs.Wrap(err, errs.CodeInvalidInput, "the language tag could not be encoded")
	}
	defer name.delete()

	var language unsafe.Pointer

	hresult := winrtCall(
		factory,
		languageFactoryCreateLanguage,
		uintptr(name),
		uintptr(unsafe.Pointer(&language)),
	)
	if hresultFailed(hresult) || language == nil {
		if uint32(hresult) == eInvalidArg {
			return nil, errs.Newf(
				errs.CodeInvalidInput,
				"%q is not a language tag Windows recognizes",
				tag,
			)
		}

		return nil, errs.Wrap(
			hresultError("Windows.Globalization.Language.Create", hresult),
			errs.CodeAccessibilityFailed,
			"could not create the requested language",
		)
	}

	return language, nil
}

// releaseEngine drops the cached engine and the image limit that came with it.
// Worker thread only.
func (w *ocrWorker) releaseEngine() {
	releaseObject(w.engine)

	w.engine = nil
	w.engineKey = ""
	w.maxImage = 0
}

// releaseUser drops one recognizer's hold on the engine, releasing the engine
// with the last one. Worker thread only.
func (w *ocrWorker) releaseUser() {
	if w.users > 0 {
		w.users--
	}

	if w.users == 0 {
		w.releaseEngine()
	}
}

// recognize is Recognize's body on the worker thread, with the engine made.
func (w *ocrWorker) recognize(
	ctx context.Context,
	img *image.RGBA,
	wordLevel bool,
) ([]engine.Word, error) {
	factor := downsampleFactor(img.Bounds(), w.maxImage)

	frame := img
	if factor > 1 {
		frame = downsample(img, factor)

		// A frame thinner than the factor has nothing left to average: a
		// one-pixel-tall strip three thousand pixels wide is refused here
		// rather than handed to the engine as an empty bitmap.
		if frame.Bounds().Empty() {
			return nil, errTooThinToShrink()
		}
	}

	bitmap, err := softwareBitmapFrom(frame)
	if err != nil {
		return nil, err
	}
	defer releaseObject(bitmap)

	result, err := w.recognizeBitmap(ctx, bitmap)
	if err != nil {
		return nil, err
	}
	defer releaseObject(result)

	return collectWords(result, wordLevel, factor, img.Bounds())
}

// recognizeBitmap runs the engine over bitmap and waits for the result, polling
// the operation's status rather than registering a completion handler.
// Worker thread only.
func (w *ocrWorker) recognizeBitmap(
	ctx context.Context,
	bitmap unsafe.Pointer,
) (unsafe.Pointer, error) {
	var operation unsafe.Pointer

	hresult := winrtCall(
		w.engine,
		ocrEngineRecognizeAsync,
		uintptr(bitmap),
		uintptr(unsafe.Pointer(&operation)),
	)
	if hresultFailed(hresult) || operation == nil {
		return nil, errs.Wrap(
			hresultError("OcrEngine.RecognizeAsync", hresult),
			errs.CodeAccessibilityFailed,
			"the captured frame is not a shape the OCR engine can read",
		)
	}
	defer releaseObject(operation)

	info, err := winrtQuery(operation, &iidAsyncInfo)
	if err != nil {
		return nil, errs.Wrap(
			err,
			errs.CodeAccessibilityFailed,
			"the OCR operation does not expose its status",
		)
	}
	defer func() {
		winrtCall(info, asyncInfoClose)
		releaseObject(info)
	}()

	ticker := time.NewTicker(ocrPollInterval)
	defer ticker.Stop()

	for {
		var status int32

		hresult = winrtCall(info, asyncInfoStatus, uintptr(unsafe.Pointer(&status)))
		if hresultFailed(hresult) {
			return nil, errs.Wrap(
				hresultError("IAsyncInfo.get_Status", hresult),
				errs.CodeAccessibilityFailed,
				"could not read the OCR operation's status",
			)
		}

		switch status {
		case asyncStatusCompleted:
			var result unsafe.Pointer

			hresult = winrtCall(
				operation,
				asyncOperationGetResults,
				uintptr(unsafe.Pointer(&result)),
			)
			if hresultFailed(hresult) || result == nil {
				return nil, errs.Wrap(
					hresultError("IAsyncOperation.GetResults", hresult),
					errs.CodeAccessibilityFailed,
					"text recognition produced no result",
				)
			}

			return result, nil
		case asyncStatusError:
			var code uintptr

			winrtCall(info, asyncInfoErrorCode, uintptr(unsafe.Pointer(&code)))

			return nil, errs.Wrap(
				hresultError("OcrEngine.RecognizeAsync", code),
				errs.CodeAccessibilityFailed,
				"text recognition failed",
			)
		case asyncStatusCanceled:
			return nil, errs.New(
				errs.CodeAccessibilityFailed,
				"the OCR engine canceled the recognition",
			)
		}

		if ctxErr := ctx.Err(); ctxErr != nil {
			winrtCall(info, asyncInfoCancel)

			return nil, contextError(ctx, "text recognition")
		}

		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

// softwareBitmapFrom copies img into a Bgra8 SoftwareBitmap the engine reads.
// The pixels go through a Windows.Storage.Streams.Buffer because that is the one
// input SoftwareBitmap takes without locking its own planes. Worker thread only.
func softwareBitmapFrom(img *image.RGBA) (unsafe.Pointer, error) {
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	byteCount := width * height * bytesPerPixel

	bufferFactory, err := activationFactory(bufferClass, &iidBufferFactory)
	if err != nil {
		return nil, errs.Wrap(
			err,
			errs.CodeAccessibilityFailed,
			"the WinRT buffer factory could not be reached",
		)
	}
	defer releaseObject(bufferFactory)

	var buffer unsafe.Pointer

	hresult := winrtCall(
		bufferFactory,
		bufferFactoryCreate,
		uintptr(byteCount),
		uintptr(unsafe.Pointer(&buffer)),
	)
	if hresultFailed(hresult) || buffer == nil {
		return nil, errs.Wrap(
			hresultError("Buffer.Create", hresult),
			errs.CodeAccessibilityFailed,
			"could not allocate a buffer for the frame",
		)
	}
	defer releaseObject(buffer)

	byteAccess, err := winrtQuery(buffer, &iidBufferByteAccess)
	if err != nil {
		return nil, errs.Wrap(
			err,
			errs.CodeAccessibilityFailed,
			"the WinRT buffer exposes no bytes",
		)
	}
	defer releaseObject(byteAccess)

	var pixels unsafe.Pointer

	hresult = winrtCall(byteAccess, bufferByteAccessBuffer, uintptr(unsafe.Pointer(&pixels)))
	if hresultFailed(hresult) || pixels == nil {
		return nil, errs.Wrap(
			hresultError("IBufferByteAccess.Buffer", hresult),
			errs.CodeAccessibilityFailed,
			"could not map the frame buffer",
		)
	}

	dst := unsafe.Slice((*byte)(pixels), byteCount)

	// The buffer's bytes are screen content. The bitmap takes its own copy
	// below, and this one is wiped before the buffer is released, on every
	// path out of here.
	defer clear(dst)

	for y := range height {
		source := img.Pix[img.PixOffset(bounds.Min.X, bounds.Min.Y+y):]
		row := source[:width*bytesPerPixel]
		out := dst[y*width*bytesPerPixel:]

		for offset := 0; offset < len(row); offset += bytesPerPixel {
			out[offset] = row[offset+2]
			out[offset+1] = row[offset+1]
			out[offset+2] = row[offset]
			// The engine reads a fully opaque frame; a capture with a zero
			// alpha channel must not be read as transparent.
			out[offset+3] = 0xFF
		}
	}

	hresult = winrtCall(buffer, bufferPutLength, uintptr(byteCount))
	if hresultFailed(hresult) {
		return nil, errs.Wrap(
			hresultError("IBuffer.put_Length", hresult),
			errs.CodeAccessibilityFailed,
			"could not size the frame buffer",
		)
	}

	bitmapFactory, err := activationFactory(softwareBitmapClass, &iidSoftwareBitmapFactory)
	if err != nil {
		return nil, errs.Wrap(
			err,
			errs.CodeAccessibilityFailed,
			"the WinRT bitmap factory could not be reached",
		)
	}
	defer releaseObject(bitmapFactory)

	var bitmap unsafe.Pointer

	hresult = winrtCall(
		bitmapFactory,
		softwareBitmapFactoryCreate,
		bitmapPixelFormatBgra8,
		uintptr(width),
		uintptr(height),
		uintptr(unsafe.Pointer(&bitmap)),
	)
	if hresultFailed(hresult) || bitmap == nil {
		return nil, errs.Wrap(
			hresultError("SoftwareBitmap.Create", hresult),
			errs.CodeAccessibilityFailed,
			"could not create a bitmap for the frame",
		)
	}

	hresult = winrtCall(bitmap, softwareBitmapCopyFromBuffer, uintptr(buffer))
	if hresultFailed(hresult) {
		releaseObject(bitmap)

		return nil, errs.Wrap(
			hresultError("SoftwareBitmap.CopyFromBuffer", hresult),
			errs.CodeAccessibilityFailed,
			"could not copy the frame into its bitmap",
		)
	}

	return bitmap, nil
}

// collectWords walks the result's lines and words into engine.Words, scaling each
// box by factor to undo the downsample the frame went through.
func collectWords(
	result unsafe.Pointer,
	wordLevel bool,
	factor int,
	bounds image.Rectangle,
) ([]engine.Word, error) {
	var linesPtr unsafe.Pointer

	hresult := winrtCall(result, ocrResultLines, uintptr(unsafe.Pointer(&linesPtr)))
	if hresultFailed(hresult) || linesPtr == nil {
		return nil, errs.Wrap(
			hresultError("OcrResult.Lines", hresult),
			errs.CodeAccessibilityFailed,
			"could not read the recognized lines",
		)
	}
	defer releaseObject(linesPtr)

	lines := vectorView{ptr: linesPtr}

	count, err := lines.size()
	if err != nil {
		return nil, errs.Wrap(
			err,
			errs.CodeAccessibilityFailed,
			"could not count the recognized lines",
		)
	}

	words := make([]engine.Word, 0, count)

	for index := range count {
		line, lineErr := lines.at(index)
		if lineErr != nil {
			return nil, errs.Wrap(
				lineErr,
				errs.CodeAccessibilityFailed,
				"could not read a recognized line",
			)
		}

		words, err = appendLine(words, line, wordLevel, factor, bounds)

		releaseObject(line)

		if err != nil {
			return nil, err
		}
	}

	return words, nil
}

// appendLine appends line's words, or the line as one run, to words. Runs with
// no text, and boxes that clip to nothing, are dropped: a caller asking for a
// region's text has nothing to do with either.
func appendLine(
	words []engine.Word,
	line unsafe.Pointer,
	wordLevel bool,
	factor int,
	bounds image.Rectangle,
) ([]engine.Word, error) {
	var wordsPtr unsafe.Pointer

	hresult := winrtCall(line, ocrLineWords, uintptr(unsafe.Pointer(&wordsPtr)))
	if hresultFailed(hresult) || wordsPtr == nil {
		return nil, errs.Wrap(
			hresultError("OcrLine.Words", hresult),
			errs.CodeAccessibilityFailed,
			"could not read a line's words",
		)
	}
	defer releaseObject(wordsPtr)

	view := vectorView{ptr: wordsPtr}

	count, err := view.size()
	if err != nil {
		return nil, errs.Wrap(
			err,
			errs.CodeAccessibilityFailed,
			"could not count a line's words",
		)
	}

	var lineBounds image.Rectangle

	for index := range count {
		word, wordErr := view.at(index)
		if wordErr != nil {
			return nil, errs.Wrap(
				wordErr,
				errs.CodeAccessibilityFailed,
				"could not read a recognized word",
			)
		}

		var rect winrtRect

		hresult = winrtCall(word, ocrWordBoundingRect, uintptr(unsafe.Pointer(&rect)))
		if hresultFailed(hresult) {
			releaseObject(word)

			return nil, errs.Wrap(
				hresultError("OcrWord.BoundingRect", hresult),
				errs.CodeAccessibilityFailed,
				"could not read a word's box",
			)
		}

		box := wordBounds(rect, factor, bounds)

		if wordLevel {
			if text := inspectableText(word, ocrWordText); text != "" && !box.Empty() {
				words = append(words, engine.Word{
					Text:       text,
					Bounds:     box,
					Confidence: wordConfidence,
				})
			}
		} else {
			lineBounds = unionBounds(lineBounds, box)
		}

		releaseObject(word)
	}

	if !wordLevel && !lineBounds.Empty() {
		if text := inspectableText(line, ocrLineText); text != "" {
			words = append(words, engine.Word{
				Text:       text,
				Bounds:     lineBounds,
				Confidence: wordConfidence,
			})
		}
	}

	return words, nil
}

// inspectableText reads the HSTRING property at slot on this and returns a
// trimmed copy. A read that fails is an empty run, which the caller drops.
func inspectableText(this unsafe.Pointer, slot int) string {
	var text hstring

	hresult := winrtCall(this, slot, uintptr(unsafe.Pointer(&text)))
	if hresultFailed(hresult) {
		return ""
	}
	defer text.delete()

	return strings.TrimSpace(text.String())
}
