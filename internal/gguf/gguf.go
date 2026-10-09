// Package gguf reads the metadata (key/value header) of GGUF model files.
package gguf

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
)

// Info is the subset of GGUF metadata llamira uses.
type Info struct {
	Architecture  string `json:"architecture"`
	Name          string `json:"name,omitempty"`
	SizeLabel     string `json:"size_label,omitempty"`
	FileType      string `json:"file_type,omitempty"`
	ContextLength uint64 `json:"context_length,omitempty"`
	BlockCount    uint64 `json:"block_count,omitempty"`
	EmbeddingLen  uint64 `json:"embedding_length,omitempty"`
	HeadCount     uint64 `json:"head_count,omitempty"`
	HeadCountKV   uint64 `json:"head_count_kv,omitempty"` // max over layers if per-layer
	// HeadCountKVLayers is set when head_count_kv is a per-layer array (0 = no KV cache,
	// e.g. recurrent layers of hybrid models).
	HeadCountKVLayers []uint64 `json:"head_count_kv_layers,omitempty"`
	VocabSize         uint64   `json:"vocab_size,omitempty"`
	// recurrent / state-space layers (Mamba, gated delta net, ...)
	SSMConvKernel uint64 `json:"ssm_conv_kernel,omitempty"`
	SSMInnerSize  uint64 `json:"ssm_inner_size,omitempty"`
	SSMStateSize  uint64 `json:"ssm_state_size,omitempty"`
	SSMGroupCount uint64 `json:"ssm_group_count,omitempty"`
	SSMTimeStep   uint64 `json:"ssm_time_step_rank,omitempty"`
	Alignment     uint64 `json:"-"`
	KeyLength     uint64 `json:"key_length,omitempty"`
	ValueLength   uint64 `json:"value_length,omitempty"`
	ExpertCount   uint64 `json:"expert_count,omitempty"`
	ExpertUsed    uint64 `json:"expert_used_count,omitempty"`
	SlidingWindow uint64 `json:"sliding_window,omitempty"`
	// SWAPattern marks sliding-window layers (1) per layer, when the model declares it.
	SWAPattern     []uint64 `json:"swa_pattern,omitempty"`
	KeyLengthSWA   uint64   `json:"key_length_swa,omitempty"`
	ValueLengthSWA uint64   `json:"value_length_swa,omitempty"`
	FullAttnEvery  uint64   `json:"full_attention_interval,omitempty"` // hybrid models: every Nth layer has a KV cache
	TensorCount    uint64   `json:"tensor_count"`
	Version        uint32   `json:"version"`
}

const (
	tUint8 = iota
	tInt8
	tUint16
	tInt16
	tUint32
	tInt32
	tFloat32
	tBool
	tString
	tArray
	tUint64
	tInt64
	tFloat64
)

// ReadFile reads GGUF metadata from a file (only the header is read).
func ReadFile(path string) (*Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Read(f)
}

type reader struct {
	r   *bufio.Reader
	err error
	n   int64 // bytes consumed
}

func (r *reader) full(b []byte) {
	if r.err == nil {
		var k int
		k, r.err = io.ReadFull(r.r, b)
		r.n += int64(k)
	}
}

func (r *reader) u32() uint32 {
	var b [4]byte
	r.full(b[:])
	return binary.LittleEndian.Uint32(b[:])
}

func (r *reader) u64() uint64 {
	var b [8]byte
	r.full(b[:])
	return binary.LittleEndian.Uint64(b[:])
}

func (r *reader) str() string {
	n := r.u64()
	if r.err != nil {
		return ""
	}
	if n > 1<<24 {
		r.err = fmt.Errorf("string too long (%d)", n)
		return ""
	}
	b := make([]byte, n)
	r.full(b)
	return string(b)
}

func (r *reader) skip(n uint64) {
	if r.err == nil {
		var k int
		k, r.err = r.r.Discard(int(n))
		r.n += int64(k)
	}
}

var scalarSize = map[uint32]uint64{
	tUint8: 1, tInt8: 1, tBool: 1, tUint16: 2, tInt16: 2,
	tUint32: 4, tInt32: 4, tFloat32: 4, tUint64: 8, tInt64: 8, tFloat64: 8,
}

// value reads one value; numbers are returned as uint64 (or float64), strings as string,
// numeric arrays as []uint64 (only short ones), other arrays are skipped (nil).
func (r *reader) value(t uint32) any {
	switch t {
	case tString:
		return r.str()
	case tArray:
		et := r.u32()
		n := r.u64()
		if r.err != nil {
			return nil
		}
		if et == tString {
			for i := uint64(0); i < n && r.err == nil; i++ {
				r.skip(r.u64())
			}
			return stringArray(n)
		}
		sz, ok := scalarSize[et]
		if !ok {
			r.err = fmt.Errorf("unsupported array type %d", et)
			return nil
		}
		if n > 4096 || et == tFloat32 || et == tFloat64 {
			r.skip(n * sz)
			return nil
		}
		out := make([]uint64, n)
		for i := range out {
			out[i], _ = toU64(r.scalar(et))
		}
		return out
	default:
		return r.scalar(t)
	}
}

func (r *reader) scalar(t uint32) any {
	sz, ok := scalarSize[t]
	if !ok {
		r.err = fmt.Errorf("unsupported value type %d", t)
		return nil
	}
	var b [8]byte
	r.full(b[:sz])
	switch t {
	case tFloat32:
		return float64(math.Float32frombits(binary.LittleEndian.Uint32(b[:])))
	case tFloat64:
		return math.Float64frombits(binary.LittleEndian.Uint64(b[:]))
	case tInt8:
		return uint64(int8(b[0]))
	case tInt16:
		return uint64(int16(binary.LittleEndian.Uint16(b[:])))
	case tInt32:
		return uint64(int32(binary.LittleEndian.Uint32(b[:])))
	default:
		return binary.LittleEndian.Uint64(b[:])
	}
}

func toU64Or(v any, def uint64) uint64 {
	if n, ok := toU64(v); ok && n > 0 {
		return n
	}
	return def
}

func toU64(v any) (uint64, bool) {
	switch x := v.(type) {
	case uint64:
		return x, true
	case float64:
		return uint64(x), true
	case []uint64:
		var m uint64
		for _, e := range x {
			m = max(m, e)
		}
		return m, len(x) > 0
	}
	return 0, false
}

// stringArray stands for a skipped string array; only its length is kept.
type stringArray uint64

// Read parses GGUF metadata from r.
func Read(rd io.Reader) (*Info, error) {
	info, _, _, err := read(rd, false)
	return info, err
}

// Tensor is one tensor of a GGUF file with its size in bytes.
type Tensor struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// ReadTensors returns metadata and the tensors (with on-disk sizes) of a GGUF file.
// Sizes come from the data offsets, so no knowledge of quantization formats is needed.
func ReadTensors(path string) (*Info, []Tensor, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	return ReadTensorsFrom(f, st.Size())
}

// ReadTensorsFrom parses the header of a GGUF stream (e.g. the first megabytes of a remote
// file fetched with a range request); fileSize is the size of the complete file.
func ReadTensorsFrom(r io.Reader, fileSize int64) (*Info, []Tensor, error) {
	info, ts, headerLen, err := read(r, true)
	if err != nil {
		return nil, nil, err
	}
	// data section starts after the header, aligned
	align := int64(info.Alignment)
	if align == 0 {
		align = 32
	}
	dataStart := (headerLen + align - 1) / align * align
	dataLen := fileSize - dataStart
	order := make([]int, len(ts))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return ts[order[a]].offset < ts[order[b]].offset })
	out := make([]Tensor, len(ts))
	for k, i := range order {
		end := dataLen
		if k+1 < len(order) {
			end = int64(ts[order[k+1]].offset)
		}
		out[i] = Tensor{Name: ts[i].name, Size: max(0, end-int64(ts[i].offset))}
	}
	return info, out, nil
}

type tensorInfo struct {
	name   string
	offset uint64
}

// read parses the header (and optionally the tensor infos) and returns the number of
// bytes consumed, i.e. where the (unaligned) data section starts.
func read(rd io.Reader, tensors bool) (*Info, []tensorInfo, int64, error) {
	r := &reader{r: bufio.NewReaderSize(rd, 1<<20)}
	info, err := readHeader(r)
	if err != nil || !tensors {
		return info, nil, r.n, err
	}
	ts := make([]tensorInfo, 0, info.TensorCount)
	for i := uint64(0); i < info.TensorCount && r.err == nil; i++ {
		name := r.str()
		nd := r.u32()
		if nd > 8 {
			return nil, nil, 0, fmt.Errorf("tensor %s: %d dims", name, nd)
		}
		for d := uint32(0); d < nd; d++ {
			r.u64()
		}
		r.u32() // type
		ts = append(ts, tensorInfo{name, r.u64()})
	}
	if r.err != nil {
		return nil, nil, 0, fmt.Errorf("read tensor infos: %w", r.err)
	}
	return info, ts, r.n, nil
}

func readHeader(r *reader) (*Info, error) {
	if magic := r.u32(); r.err != nil || magic != 0x46554747 { // "GGUF"
		if r.err != nil {
			return nil, r.err
		}
		return nil, errors.New("not a GGUF file")
	}
	info := &Info{Version: r.u32()}
	if info.Version < 2 {
		return nil, fmt.Errorf("unsupported GGUF version %d", info.Version)
	}
	info.TensorCount = r.u64()
	nkv := r.u64()
	kv := map[string]any{}
	for i := uint64(0); i < nkv && r.err == nil; i++ {
		key := r.str()
		t := r.u32()
		v := r.value(t)
		if n, ok := v.(stringArray); ok {
			kv[key+".count"] = uint64(n) // e.g. tokenizer.ggml.tokens.count = vocab size
			continue
		}
		if r.err == nil && v != nil {
			kv[key] = v
		}
	}
	if r.err != nil {
		return nil, fmt.Errorf("read GGUF header: %w", r.err)
	}
	info.Alignment = toU64Or(kv["general.alignment"], 32)
	if toks, ok := kv["tokenizer.ggml.tokens.count"].(uint64); ok {
		info.VocabSize = toks
	}
	s := func(k string) string { v, _ := kv[k].(string); return v }
	u := func(k string) uint64 { v, _ := toU64(kv[k]); return v }
	info.Architecture = s("general.architecture")
	info.Name = s("general.name")
	info.SizeLabel = s("general.size_label")
	if ft, ok := toU64(kv["general.file_type"]); ok {
		info.FileType = FileTypeName(ft)
	}
	a := info.Architecture + "."
	info.ContextLength = u(a + "context_length")
	info.BlockCount = u(a + "block_count")
	info.EmbeddingLen = u(a + "embedding_length")
	info.HeadCount = u(a + "attention.head_count")
	info.HeadCountKV = u(a + "attention.head_count_kv")
	if arr, ok := kv[a+"attention.head_count_kv"].([]uint64); ok {
		info.HeadCountKVLayers = arr
	}
	if info.VocabSize == 0 {
		info.VocabSize = u(a + "vocab_size")
	}
	info.KeyLength = u(a + "attention.key_length")
	info.ValueLength = u(a + "attention.value_length")
	info.ExpertCount = u(a + "expert_count")
	info.ExpertUsed = u(a + "expert_used_count")
	info.SlidingWindow = u(a + "attention.sliding_window")
	if arr, ok := kv[a+"attention.sliding_window_pattern"].([]uint64); ok {
		info.SWAPattern = arr
	}
	info.KeyLengthSWA = u(a + "attention.key_length_swa")
	info.ValueLengthSWA = u(a + "attention.value_length_swa")
	info.FullAttnEvery = u(a + "full_attention_interval")
	info.SSMConvKernel = u(a + "ssm.conv_kernel")
	info.SSMInnerSize = u(a + "ssm.inner_size")
	info.SSMStateSize = u(a + "ssm.state_size")
	info.SSMGroupCount = u(a + "ssm.group_count")
	info.SSMTimeStep = u(a + "ssm.time_step_rank")
	return info, nil
}

var fileTypes = map[uint64]string{
	0: "F32", 1: "F16", 2: "Q4_0", 3: "Q4_1", 7: "Q8_0", 8: "Q5_0", 9: "Q5_1",
	10: "Q2_K", 11: "Q3_K_S", 12: "Q3_K_M", 13: "Q3_K_L", 14: "Q4_K_S", 15: "Q4_K_M",
	16: "Q5_K_S", 17: "Q5_K_M", 18: "Q6_K", 19: "IQ2_XXS", 20: "IQ2_XS", 21: "Q2_K_S",
	22: "IQ3_XS", 23: "IQ3_XXS", 24: "IQ1_S", 25: "IQ4_NL", 26: "IQ3_S", 27: "IQ3_M",
	28: "IQ2_S", 29: "IQ2_M", 30: "IQ4_XS", 31: "IQ1_M", 32: "BF16", 36: "TQ1_0",
	37: "TQ2_0", 38: "MXFP4_MOE",
}

// FileTypeName maps general.file_type to its llama.cpp name.
func FileTypeName(ft uint64) string {
	if n, ok := fileTypes[ft]; ok {
		return n
	}
	return fmt.Sprintf("type %d", ft)
}

// SplitInfo mirrors llama.cpp's get_gguf_split_info: the quant tag is the last
// "[-.]WORD" of the file name (without .gguf and the -0000N-of-0000M split suffix).
type SplitInfo struct {
	Prefix string // path without .gguf and split suffix (tag included)
	Tag    string // upper-case quant tag, e.g. Q4_K_M
	Index  int
	Count  int
}

func ParseSplit(path string) (SplitInfo, bool) {
	prefix, ok := strings.CutSuffix(path, ".gguf")
	if !ok {
		if prefix, ok = strings.CutSuffix(path, ".GGUF"); !ok {
			return SplitInfo{}, false
		}
	}
	si := SplitInfo{Index: 1, Count: 1}
	// "-00001-of-00003"
	if n := len(prefix); n > 15 && prefix[n-15] == '-' && strings.EqualFold(prefix[n-9:n-5], "-of-") &&
		isDigits(prefix[n-14:n-9]) && isDigits(prefix[n-5:]) {
		si.Index = atoi(prefix[n-14 : n-9])
		si.Count = atoi(prefix[n-5:])
		prefix = prefix[:n-15]
	}
	si.Prefix = prefix
	i := len(prefix)
	for i > 0 && isTagChar(prefix[i-1]) {
		i--
	}
	if i > 0 && i < len(prefix) && (prefix[i-1] == '-' || prefix[i-1] == '.') {
		si.Tag = strings.ToUpper(prefix[i:])
	}
	return si, true
}

func isTagChar(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

func atoi(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}

// IsModelFile mirrors llama.cpp's gguf_filename_is_model: a .gguf that is not an mmproj,
// imatrix or a speculative-decoding draft head.
func IsModelFile(path string) bool {
	if !strings.HasSuffix(strings.ToLower(path), ".gguf") {
		return false
	}
	name := path
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	for _, k := range []string{"mmproj", "imatrix", "mtp-", "eagle3-", "dflash-", "dspark-"} {
		if strings.Contains(name, k) {
			return false
		}
	}
	return true
}
