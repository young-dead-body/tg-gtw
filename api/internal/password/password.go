package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// Parameters follow the OWASP Argon2id recommendations.
const (
	// Version of the Argon2 algorithm (19 = Argon2id v19).
	version = 19
	// Memory is the memory cost in KiB (64 MiB).
	memory = 64 * 1024
	// Iterations is the time cost.
	iterations = 1
	// Parallelism is the number of threads.
	parallelism = 1
	// SaltLength is the random salt size in bytes.
	saltLength = 16
	// KeyLength is the derived key size in bytes.
	keyLength = 32
)

// Hasher hashes and verifies passwords using Argon2id.
type Hasher interface {
	// Hash derives an encoded Argon2id hash from the plain password.
	Hash(plain string) (string, error)
	// Verify reports whether the plain password matches the encoded hash.
	Verify(plain, encoded string) (bool, error)
}

// Argon2idHasher implements Hasher.
type Argon2idHasher struct{}

var ErrBusy = errors.New("password service busy")

// All instances share the memory budget. Thirty-two waiting requests and a two
// second timeout bound both the queue and latency on the smallest VPS.
var hashSlot = make(chan struct{}, 1)
var hashQueue = make(chan struct{}, 33)

func acquire() (func(), error) {
	select {
	case hashQueue <- struct{}{}:
	default:
		return nil, ErrBusy
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case hashSlot <- struct{}{}:
		return func() { <-hashSlot; <-hashQueue }, nil
	case <-timer.C:
		<-hashQueue
		return nil, ErrBusy
	}
}

// New creates an Argon2id hasher.
func New() *Argon2idHasher {
	return &Argon2idHasher{}
}

func (h *Argon2idHasher) Hash(plain string) (string, error) {
	release, err := acquire()
	if err != nil {
		return "", err
	}
	defer release()
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}

	key := argon2.IDKey([]byte(plain), salt, iterations, memory, parallelism, keyLength)

	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		version,
		memory,
		iterations,
		parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	)
	return encoded, nil
}

func (h *Argon2idHasher) Verify(plain, encoded string) (bool, error) {
	params, err := parse(encoded)
	if err != nil {
		return false, err
	}

	release, err := acquire()
	if err != nil {
		return false, err
	}
	defer release()
	derived := argon2.IDKey([]byte(plain), params.salt, params.iterations, params.memory, params.parallelism, params.keyLength)

	return subtle.ConstantTimeCompare(derived, params.hash) == 1, nil
}

type parsedHash struct {
	memory      uint32
	iterations  uint32
	parallelism uint8
	salt        []byte
	hash        []byte
	keyLength   uint32
}

func parse(encoded string) (*parsedHash, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return nil, fmt.Errorf("invalid argon2id hash format")
	}

	var algVersion int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &algVersion); err != nil || algVersion != version {
		return nil, fmt.Errorf("unsupported argon2id version %q", parts[2])
	}

	var memory, iterations, parallelism int
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return nil, fmt.Errorf("invalid argon2id parameters %q", parts[3])
	}

	if memory < 8 || memory > 64*1024 || iterations < 1 || iterations > 10 || parallelism < 1 || parallelism > 16 {
		return nil, fmt.Errorf("argon2id parameters exceed resource limits")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return nil, fmt.Errorf("decode salt: %w", err)
	}
	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return nil, fmt.Errorf("decode hash: %w", err)
	}

	if len(salt) < 8 || len(salt) > 64 || len(hash) < 16 || len(hash) > 64 {
		return nil, fmt.Errorf("invalid argon2id salt or hash length")
	}
	return &parsedHash{
		memory:      uint32(memory),
		iterations:  uint32(iterations),
		parallelism: uint8(parallelism),
		salt:        salt,
		hash:        hash,
		keyLength:   uint32(len(hash)),
	}, nil
}
