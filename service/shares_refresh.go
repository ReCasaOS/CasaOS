package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// smbd refresh per change level:
//   - smb.conf changed (first write, guest mapping flipped): restart
//   - set of Time Machine shares changed: restart (smbd registers _adisk._tcp
//     over mDNS only at start)
//   - other share create/edit/delete: reload-config, then close-share for every
//     share whose section changed or is gone → its clients reconnect under the
//     new section (narrowed access applies at once); sessions on other shares
//     (a running backup) stay
//   - reload fails (smbd not running): restart, as before

// changedSambaShares: names of the shares in before whose section differs or is
// gone in after. Added shares are not listed: no sessions to close.
func changedSambaShares(before, after []byte) []string {
	previous := sambaShareSections(before)
	current := sambaShareSections(after)
	changed := []string{}
	for key, section := range previous {
		if next, ok := current[key]; !ok || next.text != section.text {
			changed = append(changed, section.name)
		}
	}
	sort.Strings(changed)
	return changed
}

// timeMachineSharesChanged: a Time Machine share added, removed, or its flag toggled.
func timeMachineSharesChanged(before, after []byte) bool {
	previous := sambaShareSections(before)
	current := sambaShareSections(after)
	for key, section := range previous {
		if section.timeMachine != current[key].timeMachine {
			return true
		}
	}
	for key, section := range current {
		if section.timeMachine && !previous[key].timeMachine {
			return true
		}
	}
	return false
}

type sambaShareSection struct {
	name        string // as written in the header: the close-share argument
	text        string // header + body, trimmed lines
	timeMachine bool
}

// sambaShareSections splits smb.casa.conf into its sections, keyed as smbd
// resolves share names (case-insensitive).
func sambaShareSections(data []byte) map[string]sambaShareSection {
	sections := map[string]sambaShareSection{}
	var name string
	var text strings.Builder
	flush := func() {
		if name != "" {
			body := text.String()
			sections[strings.ToLower(name)] = sambaShareSection{
				name:        name,
				text:        body,
				timeMachine: strings.Contains(body, "\nfruit:time machine = yes\n"),
			}
		}
		text.Reset()
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			flush()
			name = strings.TrimSpace(line[1 : len(line)-1])
		}
		if name != "" && line != "" {
			text.WriteString(line)
			text.WriteByte('\n')
		}
	}
	flush()
	return sections
}

// refreshSamba makes smbd take a new smb.casa.conf (before → after).
func (s *sharesStruct) refreshSamba(mainChanged bool, before, after []byte) error {
	if mainChanged || s.reloadSMBD == nil || timeMachineSharesChanged(before, after) {
		return s.restartSMBD()
	}
	reloadErr := s.reloadSMBD(changedSambaShares(before, after))
	if reloadErr == nil {
		return nil
	}
	if err := s.restartSMBD(); err != nil {
		return errors.Join(fmt.Errorf("reload samba: %w", reloadErr), err)
	}
	return nil
}

// reloadSambaService: smbd re-reads its configuration (the parent forwards it to
// every child), then drops the tree connects of the given shares.
func reloadSambaService(closeShares []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := runSmbcontrol(ctx, "reload-config"); err != nil {
		return err
	}
	for _, name := range closeShares {
		if err := runSmbcontrol(ctx, "close-share", name); err != nil {
			return fmt.Errorf("close share %q: %w", name, err)
		}
	}
	return nil
}

func runSmbcontrol(ctx context.Context, arguments ...string) error {
	output, err := exec.CommandContext(ctx, "smbcontrol", append([]string{"smbd"}, arguments...)...).CombinedOutput()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errors.New("smbcontrol timed out")
		}
		const limit = 1024
		if len(output) > limit {
			output = output[:limit]
		}
		return fmt.Errorf("smbcontrol %s: %w: %s", arguments[0], err, strings.TrimSpace(string(output)))
	}
	return nil
}
