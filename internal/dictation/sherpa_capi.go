package dictation

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// FindSherpaCAPILibrary locates the sherpa-onnx C API shared library next to
// the online binary (or in ../lib relative to it), returning "" when absent.
func FindSherpaCAPILibrary(binaryHint string) string {
	libName := sherpaCAPILibName()
	if libName == "" {
		return ""
	}
	bin := strings.TrimSpace(binaryHint)
	if bin == "" {
		bin = "sherpa-onnx"
	}
	var resolved string
	if strings.ContainsAny(bin, `/\`) {
		if info, err := os.Stat(bin); err == nil && !info.IsDir() {
			resolved = bin
		}
	} else if found, err := exec.LookPath(bin); err == nil {
		resolved = found
	}
	if resolved == "" {
		return ""
	}
	if target, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = target
	}
	binDir := filepath.Dir(resolved)
	candidates := []string{
		filepath.Join(binDir, libName),
		filepath.Join(filepath.Dir(binDir), "lib", libName),
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	return ""
}

// CanUseSherpaCAPI returns the C API shared library path when modelPath is an
// online/streaming model supported by SherpaOnnxOnlineRecognizer and the shared
// library exists alongside binaryHint; otherwise it returns "".
func CanUseSherpaCAPI(modelPath, binaryHint string) string {
	if !StreamingModelDir(modelPath) {
		return ""
	}
	// If the directory is readable and belongs to a CLI-only online family
	// (such as wenet), fall back to the rolling file runner. Unreadable or
	// missing directories keep the C API path so errors surface lazily at use
	// time, matching NewLocalTranscriber.
	if _, ok, err := detectOnlineModelSpec(modelPath); err == nil && !ok {
		return ""
	}
	return FindSherpaCAPILibrary(binaryHint)
}

type onlineModelSpec struct {
	encoder     string
	decoder     string
	joiner      string
	paraEncoder string
	paraDecoder string
	zip2CTC     string
	nemoCTC     string
	toneCTC     string
	tokens      string
	modelType   string
}

// detectOnlineModelSpec inspects dir for the online model families supported by
// SherpaOnnxOnlineModelConfig. Returns (spec, false, nil) when the directory is
// a valid model that the online C API does not configure (e.g. wenet).
func detectOnlineModelSpec(dir string) (onlineModelSpec, bool, error) {
	dirName, files, tokens, err := scanModelDir(dir)
	if err != nil {
		return onlineModelSpec{}, false, err
	}

	if set := consistentQuantizedSet(files, "encoder", "decoder", "joiner"); set != nil {
		spec := onlineModelSpec{
			encoder: set[0],
			decoder: set[1],
			joiner:  set[2],
			tokens:  tokens,
		}
		if strings.Contains(dirName, "streaming-zipformer-en-kroko") {
			spec.modelType = "zipformer2"
		}
		return spec, true, nil
	}

	if nameMatchesFamily(dirName, files, []string{"paraformer"}) {
		if set := consistentQuantizedSet(files, "encoder", "decoder"); set != nil {
			return onlineModelSpec{
				paraEncoder: set[0],
				paraDecoder: set[1],
				tokens:      tokens,
			}, true, nil
		}
	}

	if m := singleModelOnnx(files); m != "" {
		switch {
		case nameMatchesFamily(dirName, files, []string{"zipformer2-ctc", "zipformer-ctc"}):
			return onlineModelSpec{zip2CTC: m, tokens: tokens}, true, nil
		case nameMatchesFamily(dirName, files, []string{"nemo-ctc", "nemo_ctc"}):
			return onlineModelSpec{nemoCTC: m, tokens: tokens}, true, nil
		case nameMatchesFamily(dirName, files, []string{"t-one-ctc", "t_one_ctc"}):
			return onlineModelSpec{toneCTC: m, tokens: tokens}, true, nil
		}
	}

	return onlineModelSpec{}, false, nil
}

// C struct layouts mirroring SherpaOnnxOnlineRecognizerConfig in c-api.h.
// Verified by TestSherpaCAPIOnlineConfigLayout.
type (
	cOnlineTransducerModelConfig struct {
		encoder *byte
		decoder *byte
		joiner  *byte
	}
	cOnlineParaformerModelConfig struct {
		encoder *byte
		decoder *byte
	}
	cOnlineZipformer2CtcModelConfig struct {
		model *byte
	}
	cOnlineNemoCtcModelConfig struct {
		model *byte
	}
	cOnlineToneCtcModelConfig struct {
		model *byte
	}
	cOnlineModelConfig struct {
		transducer    cOnlineTransducerModelConfig
		paraformer    cOnlineParaformerModelConfig
		zipformer2Ctc cOnlineZipformer2CtcModelConfig
		tokens        *byte
		numThreads    int32
		provider      *byte
		_             int32 // debug
		modelType     *byte
		_             *byte // modeling_unit
		_             *byte // bpe_vocab
		_             *byte // tokens_buf
		_             int32 // tokens_buf_size
		nemoCtc       cOnlineNemoCtcModelConfig
		tOneCtc       cOnlineToneCtcModelConfig
	}
	cFeatureConfig struct {
		sampleRate int32
		featureDim int32
	}
	cOnlineCtcFstDecoderConfig struct {
		_ *byte // graph
		_ int32 // max_active
	}
	cHomophoneReplacerConfig struct {
		_ *byte // dict_dir
		_ *byte // lexicon
		_ *byte // rule_fsts
	}
	cOnlineRecognizerConfig struct {
		featConfig     cFeatureConfig
		modelConfig    cOnlineModelConfig
		decodingMethod *byte
		maxActivePaths int32
		_              int32   // enable_endpoint
		_              float32 // rule1_min_trailing_silence
		_              float32 // rule2_min_trailing_silence
		_              float32 // rule3_min_utterance_length
		_              *byte   // hotwords_file
		_              float32 // hotwords_score
		_              cOnlineCtcFstDecoderConfig
		_              *byte   // rule_fsts
		_              *byte   // rule_fars
		_              float32 // blank_penalty
		_              *byte   // hotwords_buf
		_              int32   // hotwords_buf_size
		_              cHomophoneReplacerConfig
	}
)

type sherpaCAPI struct {
	createOnlineRecognizer  func(cfg *cOnlineRecognizerConfig) uintptr
	destroyOnlineRecognizer func(rec uintptr)
	createOnlineStream      func(rec uintptr) uintptr
	destroyOnlineStream     func(stream uintptr)
	acceptWaveform          func(stream uintptr, sampleRate int32, samples *float32, n int32)
	inputFinished           func(stream uintptr)
	isStreamReady           func(rec, stream uintptr) int32
	decodeStream            func(rec, stream uintptr)
	getStreamResultAsJson   func(rec, stream uintptr) *byte
	destroyStreamResultJson func(s *byte)
}

var (
	sherpaLibsMu sync.Mutex
	sherpaLibs   = map[string]*sherpaCAPI{}
)

func loadSherpaCAPI(libPath string) (*sherpaCAPI, error) {
	sherpaLibsMu.Lock()
	defer sherpaLibsMu.Unlock()
	if api, ok := sherpaLibs[libPath]; ok {
		return api, nil
	}
	h, err := openSherpaCAPILib(libPath)
	if err != nil {
		return nil, err
	}
	api := &sherpaCAPI{}
	bindings := []struct {
		sym string
		fn  any
	}{
		{"SherpaOnnxCreateOnlineRecognizer", &api.createOnlineRecognizer},
		{"SherpaOnnxDestroyOnlineRecognizer", &api.destroyOnlineRecognizer},
		{"SherpaOnnxCreateOnlineStream", &api.createOnlineStream},
		{"SherpaOnnxDestroyOnlineStream", &api.destroyOnlineStream},
		{"SherpaOnnxOnlineStreamAcceptWaveform", &api.acceptWaveform},
		{"SherpaOnnxOnlineStreamInputFinished", &api.inputFinished},
		{"SherpaOnnxIsOnlineStreamReady", &api.isStreamReady},
		{"SherpaOnnxDecodeOnlineStream", &api.decodeStream},
		{"SherpaOnnxGetOnlineStreamResultAsJson", &api.getStreamResultAsJson},
		{"SherpaOnnxDestroyOnlineStreamResultJson", &api.destroyStreamResultJson},
	}
	for _, b := range bindings {
		addr, err := symSherpaCAPILib(h, b.sym)
		if err != nil {
			return nil, err
		}
		purego.RegisterFunc(b.fn, addr)
	}
	sherpaLibs[libPath] = api
	return api, nil
}

type onlineRecCacheKey struct {
	libPath    string
	spec       onlineModelSpec
	numThreads int
	sampleRate int
}

type onlineStream interface {
	AcceptWaveform(sampleRate int, samples []float32)
	InputFinished()
	IsReady() bool
	Decode()
	ResultText() string
	Close()
}

type onlineRecognizer interface {
	NewStream() onlineStream
	Release()
}

type cachedOnlineRecognizer struct {
	key     onlineRecCacheKey
	api     *sherpaCAPI
	handle  uintptr
	refs    int
	evicted bool
}

func (c *cachedOnlineRecognizer) NewStream() onlineStream {
	h := c.api.createOnlineStream(c.handle)
	return &cOnlineStream{api: c.api, rec: c.handle, handle: h}
}

func (c *cachedOnlineRecognizer) Release() {
	recCacheMu.Lock()
	c.refs--
	destroy := c.evicted && c.refs == 0
	recCacheMu.Unlock()
	if destroy {
		c.api.destroyOnlineRecognizer(c.handle)
	}
}

type cOnlineStream struct {
	api    *sherpaCAPI
	rec    uintptr
	handle uintptr
}

func (s *cOnlineStream) AcceptWaveform(sampleRate int, samples []float32) {
	if s.handle == 0 || len(samples) == 0 {
		return
	}
	var pin runtime.Pinner
	pin.Pin(&samples[0])
	defer pin.Unpin()
	s.api.acceptWaveform(s.handle, int32(sampleRate), &samples[0], int32(len(samples)))
}

func (s *cOnlineStream) InputFinished() {
	if s.handle != 0 {
		s.api.inputFinished(s.handle)
	}
}

func (s *cOnlineStream) IsReady() bool {
	return s.handle != 0 && s.api.isStreamReady(s.rec, s.handle) != 0
}

func (s *cOnlineStream) Decode() {
	if s.handle != 0 {
		s.api.decodeStream(s.rec, s.handle)
	}
}

func (s *cOnlineStream) ResultText() string {
	if s.handle == 0 {
		return ""
	}
	ptr := s.api.getStreamResultAsJson(s.rec, s.handle)
	if ptr == nil {
		return ""
	}
	raw := cStringToGo(ptr)
	s.api.destroyStreamResultJson(ptr)
	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Text)
}

func (s *cOnlineStream) Close() {
	if s.handle != 0 {
		s.api.destroyOnlineStream(s.handle)
		s.handle = 0
	}
}

type inflightRec struct {
	key     onlineRecCacheKey
	done    chan struct{}
	waiters int
	rec     *cachedOnlineRecognizer
	err     error
}

var (
	recCacheMu   sync.Mutex
	cachedRec    *cachedOnlineRecognizer
	inflightInit *inflightRec
)

func acquireCachedRecognizer(key onlineRecCacheKey) (onlineRecognizer, error) {
	for {
		recCacheMu.Lock()
		if cachedRec != nil && cachedRec.key == key && !cachedRec.evicted {
			cachedRec.refs++
			rec := cachedRec
			recCacheMu.Unlock()
			return rec, nil
		}
		if inflightInit != nil {
			if inflightInit.key == key {
				inf := inflightInit
				inf.waiters++
				recCacheMu.Unlock()
				<-inf.done
				if inf.err != nil {
					return nil, inf.err
				}
				return inf.rec, nil
			}
			wait := inflightInit.done
			recCacheMu.Unlock()
			<-wait
			continue
		}
		inf := &inflightRec{key: key, done: make(chan struct{})}
		inflightInit = inf
		recCacheMu.Unlock()

		rec, err := createOnlineRecognizer(key)

		recCacheMu.Lock()
		inflightInit = nil
		inf.err = err
		var toDestroy *cachedOnlineRecognizer
		if err == nil {
			if cachedRec != nil {
				cachedRec.evicted = true
				if cachedRec.refs == 0 {
					toDestroy = cachedRec
				}
			}
			rec.refs = 1 + inf.waiters
			cachedRec = rec
			inf.rec = rec
		}
		close(inf.done)
		recCacheMu.Unlock()

		if toDestroy != nil {
			toDestroy.api.destroyOnlineRecognizer(toDestroy.handle)
		}
		if err != nil {
			return nil, err
		}
		return rec, nil
	}
}

func createOnlineRecognizer(key onlineRecCacheKey) (*cachedOnlineRecognizer, error) {
	api, err := loadSherpaCAPI(key.libPath)
	if err != nil {
		return nil, err
	}
	var pin runtime.Pinner
	defer pin.Unpin()

	cstr := func(s string) *byte {
		if s == "" {
			return nil
		}
		b := make([]byte, len(s)+1)
		copy(b, s)
		pin.Pin(&b[0])
		return &b[0]
	}

	cfg := cOnlineRecognizerConfig{
		featConfig: cFeatureConfig{
			sampleRate: int32(key.sampleRate),
			featureDim: 80,
		},
		modelConfig: cOnlineModelConfig{
			transducer: cOnlineTransducerModelConfig{
				encoder: cstr(key.spec.encoder),
				decoder: cstr(key.spec.decoder),
				joiner:  cstr(key.spec.joiner),
			},
			paraformer: cOnlineParaformerModelConfig{
				encoder: cstr(key.spec.paraEncoder),
				decoder: cstr(key.spec.paraDecoder),
			},
			zipformer2Ctc: cOnlineZipformer2CtcModelConfig{
				model: cstr(key.spec.zip2CTC),
			},
			nemoCtc: cOnlineNemoCtcModelConfig{
				model: cstr(key.spec.nemoCTC),
			},
			tOneCtc: cOnlineToneCtcModelConfig{
				model: cstr(key.spec.toneCTC),
			},
			tokens:     cstr(key.spec.tokens),
			numThreads: int32(key.numThreads),
			provider:   cstr("cpu"),
			modelType:  cstr(key.spec.modelType),
		},
		decodingMethod: cstr("greedy_search"),
		maxActivePaths: 4,
	}
	pin.Pin(&cfg)

	h := api.createOnlineRecognizer(&cfg)
	if h == 0 {
		return nil, fmt.Errorf("sherpa-onnx C API failed to initialize recognizer for %q", filepath.Dir(key.spec.tokens))
	}
	return &cachedOnlineRecognizer{key: key, api: api, handle: h}, nil
}

func cStringToGo(p *byte) string {
	if p == nil {
		return ""
	}
	const maxLen = 1 << 20
	var out []byte
	for i := uintptr(0); i < maxLen; i++ {
		b := *(*byte)(unsafe.Add(unsafe.Pointer(p), i))
		if b == 0 {
			break
		}
		out = append(out, b)
	}
	return string(out)
}

func pcm16ToFloat32(pcm []byte) []float32 {
	n := len(pcm) / 2
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		sample := int16(binary.LittleEndian.Uint16(pcm[i*2:]))
		out[i] = float32(sample) / 32768.0
	}
	return out
}

type localStreamingTranscriber struct {
	cfg        LocalConfig
	libPath    string
	sampleRate int
	fallback   Transcriber
	acquire    func() (onlineRecognizer, error)
}

// NewLocalStreamingTranscriber builds an in-process streaming transcriber backed
// by sherpa-onnx's shared C API library (no network ports or per-partial
// subprocesses). Whole-clip Transcribe delegates to NewLocalTranscriber.
func NewLocalStreamingTranscriber(cfg LocalConfig, libPath string) (Transcriber, error) {
	cfg.Online = true
	fallback, err := NewLocalTranscriber(cfg)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(libPath) == "" {
		return nil, errors.New("sherpa-onnx C API library path is empty")
	}
	s := &localStreamingTranscriber{
		cfg:        cfg,
		libPath:    libPath,
		sampleRate: DefaultSampleRate,
		fallback:   fallback,
	}
	s.acquire = s.defaultAcquire
	return s, nil
}

func (s *localStreamingTranscriber) SampleRate() int {
	return s.sampleRate
}

func (s *localStreamingTranscriber) Transcribe(ctx context.Context, audio []byte) (string, error) {
	return s.fallback.Transcribe(ctx, audio)
}

func (s *localStreamingTranscriber) defaultAcquire() (onlineRecognizer, error) {
	spec, ok, err := detectOnlineModelSpec(s.cfg.ModelPath)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, &SetupError{
			Tool: "modelo de ditado local",
			Hint: fmt.Sprintf("não reconheci o modelo em %q (rode /stt-model para baixar um modelo suportado)", s.cfg.ModelPath),
		}
	}
	key := onlineRecCacheKey{
		libPath:    s.libPath,
		spec:       spec,
		numThreads: effectiveNumThreads(s.cfg.NumThreads),
		sampleRate: s.sampleRate,
	}
	return acquireCachedRecognizer(key)
}

func (s *localStreamingTranscriber) StreamTranscribe(ctx context.Context, chunks <-chan []byte, onPartial func(string, bool)) (string, error) {
	if onPartial == nil {
		onPartial = func(string, bool) {}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	type recResult struct {
		rec onlineRecognizer
		err error
	}
	recCh := make(chan recResult, 1)
	go func() {
		rec, err := s.acquire()
		recCh <- recResult{rec: rec, err: err}
	}()

	var (
		rec         onlineRecognizer
		stream      onlineStream
		recChDone   bool
		pending     []byte
		carry       byte
		hasCarry    bool
		hasAudio    bool
		lastPartial string
	)
	defer func() {
		if stream != nil {
			stream.Close()
		}
		if rec != nil {
			rec.Release()
		}
		if !recChDone {
			go func() {
				if r := <-recCh; r.rec != nil {
					r.rec.Release()
				}
			}()
		}
	}()

	feedPCM := func(pcm []byte) {
		if hasCarry {
			combined := make([]byte, 1+len(pcm))
			combined[0] = carry
			copy(combined[1:], pcm)
			pcm = combined
			hasCarry = false
		}
		if len(pcm)%2 != 0 {
			carry = pcm[len(pcm)-1]
			hasCarry = true
			pcm = pcm[:len(pcm)-1]
		}
		if len(pcm) == 0 {
			return
		}
		stream.AcceptWaveform(s.sampleRate, pcm16ToFloat32(pcm))
		for stream.IsReady() {
			stream.Decode()
		}
		if text := strings.TrimSpace(stream.ResultText()); text != "" && text != lastPartial {
			lastPartial = text
			onPartial(text, false)
		}
	}

	for chunks != nil || rec == nil {
		select {
		case <-ctx.Done():
			return lastPartial, ctx.Err()
		case r := <-recCh:
			recChDone = true
			recCh = nil
			if r.err != nil {
				return "", r.err
			}
			rec = r.rec
			if chunks == nil && !hasAudio {
				return "", nil
			}
			stream = rec.NewStream()
			if len(pending) > 0 {
				feedPCM(pending)
				pending = nil
			}
		case chunk, ok := <-chunks:
			if !ok {
				chunks = nil
				if !hasAudio {
					return "", nil
				}
				continue
			}
			if len(chunk) == 0 {
				continue
			}
			hasAudio = true
			if stream == nil {
				pending = append(pending, chunk...)
				continue
			}
			feedPCM(chunk)
		}
	}

	// Pad ~0.5s of trailing silence before InputFinished so streaming models
	// with chunk lookahead (e.g. Zipformer) flush the last spoken syllables
	// when the user stops immediately after speaking.
	if tailSamples := s.sampleRate / 2; tailSamples > 0 {
		stream.AcceptWaveform(s.sampleRate, make([]float32, tailSamples))
		for stream.IsReady() {
			stream.Decode()
		}
	}
	stream.InputFinished()
	for stream.IsReady() {
		stream.Decode()
	}
	return strings.TrimSpace(stream.ResultText()), nil
}
