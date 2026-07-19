package cryptox

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	FormatVersion = 1
	FrameSize     = 4 * 1024 * 1024
	maxHeaderSize = 256 * 1024
	maxMetaSize   = 64 * 1024 * 1024
)

var magic = [8]byte{'W', 'C', 'B', 'L', 'O', 'C', 'K', '1'}

type KDFParams struct {
	Name        string `json:"name"`
	Time        uint32 `json:"time"`
	MemoryKiB   uint32 `json:"memoryKiB"`
	Parallelism uint8  `json:"parallelism"`
	KeyLength   uint32 `json:"keyLength"`
}

func DefaultKDFParams() KDFParams {
	return KDFParams{
		Name: "argon2id", Time: 3, MemoryKiB: 64 * 1024, Parallelism: 2,
		KeyLength: chacha20poly1305.KeySize,
	}
}

type PublicHeader struct {
	FormatVersion int       `json:"formatVersion"`
	TaskID        string    `json:"taskId"`
	ObjectID      string    `json:"objectId"`
	Kind          string    `json:"kind"`
	Salt          string    `json:"salt"`
	KDF           KDFParams `json:"kdf"`
	NoncePrefix   string    `json:"noncePrefix"`
	FrameSize     int       `json:"frameSize"`
}

func RandomSalt() ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}
	return salt, nil
}

func EncodeSalt(salt []byte) string {
	return base64.RawURLEncoding.EncodeToString(salt)
}

func DecodeSalt(encoded string) ([]byte, error) {
	salt, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode salt: %w", err)
	}
	if len(salt) < 16 {
		return nil, errors.New("salt is too short")
	}
	return salt, nil
}

func DeriveKey(password string, salt []byte, params KDFParams) ([]byte, error) {
	if password == "" {
		return nil, errors.New("task password is required")
	}
	if params.Name != "argon2id" || params.KeyLength != chacha20poly1305.KeySize {
		return nil, errors.New("unsupported key derivation parameters")
	}
	if params.Time == 0 || params.MemoryKiB < 8*1024 || params.Parallelism == 0 {
		return nil, errors.New("unsafe key derivation parameters")
	}
	return argon2.IDKey([]byte(password), salt, params.Time, params.MemoryKiB, params.Parallelism, params.KeyLength), nil
}

func NewHeader(taskID, objectID, kind, encodedSalt string) (PublicHeader, error) {
	noncePrefix := make([]byte, 16)
	if _, err := rand.Read(noncePrefix); err != nil {
		return PublicHeader{}, fmt.Errorf("generate nonce prefix: %w", err)
	}
	return PublicHeader{
		FormatVersion: FormatVersion,
		TaskID:        taskID,
		ObjectID:      objectID,
		Kind:          kind,
		Salt:          encodedSalt,
		KDF:           DefaultKDFParams(),
		NoncePrefix:   base64.RawURLEncoding.EncodeToString(noncePrefix),
		FrameSize:     FrameSize,
	}, nil
}

type EncryptResult struct {
	Size   int64
	SHA256 string
}

func EncryptObject(dst io.Writer, key []byte, header PublicHeader, metadata any, payload io.Reader) (EncryptResult, error) {
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return EncryptResult{}, fmt.Errorf("marshal public header: %w", err)
	}
	if len(headerJSON) > maxHeaderSize {
		return EncryptResult{}, errors.New("public header is too large")
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return EncryptResult{}, fmt.Errorf("marshal encrypted metadata: %w", err)
	}
	if len(metadataJSON) > maxMetaSize {
		return EncryptResult{}, errors.New("encrypted metadata is too large")
	}

	hasher := sha256.New()
	counter := &countingWriter{writer: io.MultiWriter(dst, hasher)}
	if _, err := counter.Write(magic[:]); err != nil {
		return EncryptResult{}, err
	}
	if err := binary.Write(counter, binary.BigEndian, uint32(len(headerJSON))); err != nil {
		return EncryptResult{}, fmt.Errorf("write public header length: %w", err)
	}
	if _, err := counter.Write(headerJSON); err != nil {
		return EncryptResult{}, fmt.Errorf("write public header: %w", err)
	}

	prefix := make([]byte, 8)
	binary.BigEndian.PutUint64(prefix, uint64(len(metadataJSON)))
	plain := io.MultiReader(bytesReader(prefix), bytesReader(metadataJSON), payload)
	if err := encryptFrames(counter, key, headerJSON, header, plain); err != nil {
		return EncryptResult{}, err
	}
	return EncryptResult{Size: counter.count, SHA256: hex.EncodeToString(hasher.Sum(nil))}, nil
}

func encryptFrames(dst io.Writer, key, headerJSON []byte, header PublicHeader, src io.Reader) error {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return fmt.Errorf("initialize XChaCha20-Poly1305: %w", err)
	}
	prefix, err := base64.RawURLEncoding.DecodeString(header.NoncePrefix)
	if err != nil || len(prefix) != 16 {
		return errors.New("invalid nonce prefix")
	}
	if header.FrameSize <= 0 || header.FrameSize > 16*1024*1024 {
		return errors.New("invalid frame size")
	}
	headerHash := sha256.Sum256(headerJSON)
	buffer := make([]byte, header.FrameSize)
	for index := uint64(0); ; index++ {
		n, readErr := io.ReadFull(src, buffer)
		if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("read plaintext frame: %w", readErr)
		}
		if n == 0 {
			if err := binary.Write(dst, binary.BigEndian, uint32(0)); err != nil {
				return fmt.Errorf("write end marker: %w", err)
			}
			return nil
		}
		nonce := makeNonce(prefix, index)
		sealed := aead.Seal(nil, nonce, buffer[:n], associatedData(headerHash, index))
		if err := binary.Write(dst, binary.BigEndian, uint32(len(sealed))); err != nil {
			return fmt.Errorf("write frame length: %w", err)
		}
		if _, err := dst.Write(sealed); err != nil {
			return fmt.Errorf("write encrypted frame: %w", err)
		}
		if errors.Is(readErr, io.ErrUnexpectedEOF) || errors.Is(readErr, io.EOF) {
			if err := binary.Write(dst, binary.BigEndian, uint32(0)); err != nil {
				return fmt.Errorf("write end marker: %w", err)
			}
			return nil
		}
	}
}

type ObjectReader struct {
	Header   PublicHeader
	Metadata json.RawMessage
	Payload  io.Reader
}

func OpenObject(src io.Reader, password string) (*ObjectReader, error) {
	header, headerJSON, err := readHeader(src)
	if err != nil {
		return nil, err
	}
	salt, err := DecodeSalt(header.Salt)
	if err != nil {
		return nil, err
	}
	key, err := DeriveKey(password, salt, header.KDF)
	if err != nil {
		return nil, err
	}
	return openObjectWithKey(src, key, header, headerJSON)
}

func OpenObjectWithKey(src io.Reader, key []byte) (*ObjectReader, error) {
	header, headerJSON, err := readHeader(src)
	if err != nil {
		return nil, err
	}
	return openObjectWithKey(src, key, header, headerJSON)
}

func openObjectWithKey(src io.Reader, key []byte, header PublicHeader, headerJSON []byte) (*ObjectReader, error) {
	frames, err := newFrameReader(src, key, headerJSON, header)
	if err != nil {
		return nil, err
	}
	lengthBytes := make([]byte, 8)
	if _, err := io.ReadFull(frames, lengthBytes); err != nil {
		return nil, fmt.Errorf("read encrypted metadata length: %w", err)
	}
	metadataLength := binary.BigEndian.Uint64(lengthBytes)
	if metadataLength > maxMetaSize {
		return nil, errors.New("encrypted metadata length is invalid")
	}
	metadata := make([]byte, int(metadataLength))
	if _, err := io.ReadFull(frames, metadata); err != nil {
		return nil, fmt.Errorf("read encrypted metadata: %w", err)
	}
	return &ObjectReader{Header: header, Metadata: metadata, Payload: frames}, nil
}

func readHeader(src io.Reader) (PublicHeader, []byte, error) {
	var gotMagic [8]byte
	if _, err := io.ReadFull(src, gotMagic[:]); err != nil {
		return PublicHeader{}, nil, fmt.Errorf("read object magic: %w", err)
	}
	if gotMagic != magic {
		return PublicHeader{}, nil, errors.New("not a WebDAV Cold Backup object")
	}
	var length uint32
	if err := binary.Read(src, binary.BigEndian, &length); err != nil {
		return PublicHeader{}, nil, fmt.Errorf("read public header length: %w", err)
	}
	if length == 0 || length > maxHeaderSize {
		return PublicHeader{}, nil, errors.New("public header length is invalid")
	}
	headerJSON := make([]byte, int(length))
	if _, err := io.ReadFull(src, headerJSON); err != nil {
		return PublicHeader{}, nil, fmt.Errorf("read public header: %w", err)
	}
	var header PublicHeader
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return PublicHeader{}, nil, fmt.Errorf("decode public header: %w", err)
	}
	if header.FormatVersion != FormatVersion {
		return PublicHeader{}, nil, fmt.Errorf("unsupported object format version %d", header.FormatVersion)
	}
	return header, headerJSON, nil
}

type frameReader struct {
	src        io.Reader
	aead       cipherAEAD
	prefix     []byte
	headerHash [32]byte
	frameSize  int
	index      uint64
	buffer     []byte
	offset     int
	done       bool
}

type cipherAEAD interface {
	Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error)
	Overhead() int
}

func newFrameReader(src io.Reader, key, headerJSON []byte, header PublicHeader) (*frameReader, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("initialize XChaCha20-Poly1305: %w", err)
	}
	prefix, err := base64.RawURLEncoding.DecodeString(header.NoncePrefix)
	if err != nil || len(prefix) != 16 {
		return nil, errors.New("invalid nonce prefix")
	}
	if header.FrameSize <= 0 || header.FrameSize > 16*1024*1024 {
		return nil, errors.New("invalid frame size")
	}
	return &frameReader{
		src: src, aead: aead, prefix: prefix, headerHash: sha256.Sum256(headerJSON),
		frameSize: header.FrameSize,
	}, nil
}

func (r *frameReader) Read(dst []byte) (int, error) {
	for r.offset >= len(r.buffer) {
		if r.done {
			return 0, io.EOF
		}
		if err := r.readFrame(); err != nil {
			return 0, err
		}
	}
	n := copy(dst, r.buffer[r.offset:])
	r.offset += n
	return n, nil
}

func (r *frameReader) readFrame() error {
	var length uint32
	if err := binary.Read(r.src, binary.BigEndian, &length); err != nil {
		if errors.Is(err, io.EOF) {
			return io.ErrUnexpectedEOF
		}
		return fmt.Errorf("read encrypted frame length: %w", err)
	}
	if length == 0 {
		r.done = true
		r.buffer = nil
		r.offset = 0
		return nil
	}
	maxLength := r.frameSize + r.aead.Overhead()
	if int(length) > maxLength || int(length) <= r.aead.Overhead() {
		return errors.New("encrypted frame length is invalid")
	}
	sealed := make([]byte, int(length))
	if _, err := io.ReadFull(r.src, sealed); err != nil {
		return fmt.Errorf("read encrypted frame: %w", err)
	}
	opened, err := r.aead.Open(nil, makeNonce(r.prefix, r.index), sealed, associatedData(r.headerHash, r.index))
	if err != nil {
		return errors.New("object authentication failed: wrong password or damaged data")
	}
	r.index++
	r.buffer = opened
	r.offset = 0
	return nil
}

func makeNonce(prefix []byte, index uint64) []byte {
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	copy(nonce, prefix)
	binary.BigEndian.PutUint64(nonce[16:], index)
	return nonce
}

func associatedData(headerHash [32]byte, index uint64) []byte {
	data := make([]byte, 40)
	copy(data, headerHash[:])
	binary.BigEndian.PutUint64(data[32:], index)
	return data
}

type countingWriter struct {
	writer io.Writer
	count  int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	w.count += int64(n)
	return n, err
}

type immutableBytesReader struct {
	data   []byte
	offset int
}

func bytesReader(data []byte) *immutableBytesReader {
	return &immutableBytesReader{data: data}
}

func (r *immutableBytesReader) Read(p []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}

func SHA256Reader(src io.Reader) (string, error) {
	var hasher hash.Hash = sha256.New()
	if _, err := io.Copy(hasher, src); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
