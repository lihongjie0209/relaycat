package accesscode

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	relayv1 "github.com/lihongjie0209/relaycat/gen/relay/v1"
	"google.golang.org/protobuf/proto"
)

const (
	Prefix          = "rc1_"
	ProtocolVersion = 1
	RouteIDSize     = 16
	PSKSize         = 32
)

type Code struct {
	RelayURL string
	RouteID  []byte
	PSK      []byte
}

type stateFile struct {
	Version   uint32    `json:"version"`
	RelayURL  string    `json:"relay_url"`
	Target    string    `json:"target"`
	RouteID   string    `json:"route_id"`
	PSK       string    `json:"preshared_key"`
	CreatedAt time.Time `json:"created_at"`
}

func New(relayURL string) (Code, error) {
	c := Code{RelayURL: relayURL, RouteID: make([]byte, RouteIDSize), PSK: make([]byte, PSKSize)}
	if _, err := rand.Read(c.RouteID); err != nil {
		return Code{}, fmt.Errorf("generating route ID: %w", err)
	}
	if _, err := rand.Read(c.PSK); err != nil {
		return Code{}, fmt.Errorf("generating preshared key: %w", err)
	}
	return c, nil
}

func Encode(c Code) (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	b, err := proto.Marshal(&relayv1.ConnectionCode{
		ProtocolVersion: ProtocolVersion,
		RelayUrl:        c.RelayURL,
		RouteId:         c.RouteID,
		PresharedKey:    c.PSK,
	})
	if err != nil {
		return "", fmt.Errorf("encoding connection code: %w", err)
	}
	return Prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

func Decode(s string, allowInsecure bool) (Code, error) {
	if !strings.HasPrefix(s, Prefix) {
		return Code{}, errors.New("invalid connection code prefix")
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, Prefix))
	if err != nil {
		return Code{}, errors.New("invalid connection code encoding")
	}
	var msg relayv1.ConnectionCode
	if err := proto.Unmarshal(b, &msg); err != nil {
		return Code{}, errors.New("invalid connection code payload")
	}
	if msg.ProtocolVersion != ProtocolVersion {
		return Code{}, fmt.Errorf("unsupported connection code version %d", msg.ProtocolVersion)
	}
	c := Code{RelayURL: msg.RelayUrl, RouteID: msg.RouteId, PSK: msg.PresharedKey}
	if err := c.validate(); err != nil {
		return Code{}, err
	}
	if err := ValidateRelayURL(c.RelayURL, allowInsecure); err != nil {
		return Code{}, err
	}
	return c, nil
}

func LoadOrCreate(path, relayURL, target string) (Code, error) {
	if path == "" {
		return New(relayURL)
	}
	b, err := os.ReadFile(path) // #nosec G304 -- the path is explicitly selected by the local operator.
	if err == nil {
		if err := CheckPrivateFile(path); err != nil {
			return Code{}, err
		}
		var st stateFile
		if err := json.Unmarshal(b, &st); err != nil {
			return Code{}, fmt.Errorf("decoding state file: %w", err)
		}
		if st.Version != ProtocolVersion || st.RelayURL != relayURL || st.Target != target {
			return Code{}, errors.New("state file does not match protocol version, relay URL, and target")
		}
		route, err := base64.RawURLEncoding.DecodeString(st.RouteID)
		if err != nil {
			return Code{}, errors.New("invalid route ID in state file")
		}
		psk, err := base64.RawURLEncoding.DecodeString(st.PSK)
		if err != nil {
			return Code{}, errors.New("invalid preshared key in state file")
		}
		c := Code{RelayURL: relayURL, RouteID: route, PSK: psk}
		return c, c.validate()
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Code{}, fmt.Errorf("reading state file: %w", err)
	}
	c, err := New(relayURL)
	if err != nil {
		return Code{}, err
	}
	st := stateFile{
		Version: ProtocolVersion, RelayURL: relayURL, Target: target,
		RouteID: base64.RawURLEncoding.EncodeToString(c.RouteID),
		PSK:     base64.RawURLEncoding.EncodeToString(c.PSK), CreatedAt: time.Now().UTC(),
	}
	b, err = json.MarshalIndent(st, "", "  ")
	if err != nil {
		return Code{}, fmt.Errorf("encoding state file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Code{}, fmt.Errorf("creating state directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- explicit local state path.
	if err != nil {
		return Code{}, fmt.Errorf("writing state file: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return Code{}, fmt.Errorf("writing state file: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return Code{}, fmt.Errorf("syncing state file: %w", err)
	}
	if err := f.Close(); err != nil {
		return Code{}, fmt.Errorf("closing state file: %w", err)
	}
	return c, nil
}

func ValidateRelayURL(raw string, allowInsecure bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("relay URL must contain only scheme and host")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && allowInsecure {
		return nil
	}
	return errors.New("relay URL must use https; use --allow-insecure-relay for trusted development networks")
}

func (c Code) validate() error {
	if len(c.RouteID) != RouteIDSize {
		return fmt.Errorf("route ID must be %d bytes", RouteIDSize)
	}
	if len(c.PSK) != PSKSize {
		return fmt.Errorf("preshared key must be %d bytes", PSKSize)
	}
	return nil
}

func CheckPrivateFile(path string) error {
	if path == "" || runtime.GOOS == "windows" {
		return nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is accessible by group or other users", path)
	}
	return nil
}
