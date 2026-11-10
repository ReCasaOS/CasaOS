// Package share publishes apps of this box on the internet through the relay.
package share

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/utils/file"
)

// StateFile is kept in the fleet's state directory, under the root.
const StateFile = "share.json"

// App is what an app that is shared is reached on.
type App struct {
	Port   string `json:"port"`
	Scheme string `json:"scheme"`
}

// State is what this box shares, as saved in StateFile.
type State struct {
	Name          string         `json:"name"`
	Host          string         `json:"host"`
	Domain        string         `json:"domain"`
	TOSAcceptedAt time.Time      `json:"tos_accepted_at,omitzero"`
	Apps          map[string]App `json:"apps"`
}

// loadState reads the state; ok is false when there is none or it is unreadable.
func loadState(path string) (State, bool) {
	var s State
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &s) != nil {
		return State{}, false
	}
	return s, true
}

func saveState(path string, s State) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return file.WriteFileAtomic(path, data) // created 0600
}

func clearState(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

var (
	// ErrBadLabel is Label's error.
	ErrBadLabel = errors.New("the app's name cannot be made into a DNS label")
	// ErrBadName is ValidName's error.
	ErrBadName = errors.New("a box name is 3 to 32 lowercase letters, digits or dashes, not starting or ending with a dash")

	notLabel  = regexp.MustCompile(`[^a-z0-9-]+`)
	labelRule = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	nameRule  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,30}[a-z0-9]$`)
)

// Label turns an app's compose name into the DNS label of its host.
func Label(composeName string) (string, error) {
	label := strings.Trim(notLabel.ReplaceAllString(strings.ToLower(composeName), "-"), "-")
	if !labelRule.MatchString(label) {
		return "", ErrBadLabel
	}
	return label, nil
}

// ValidName reports whether name can be the name of a box.
func ValidName(name string) error {
	if !nameRule.MatchString(name) {
		return ErrBadName
	}
	return nil
}
