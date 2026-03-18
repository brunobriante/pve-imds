package vmconfig

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// netKeyRe matches "netN" keys where N is a non-negative integer.
var netKeyRe = regexp.MustCompile(`^net(\d+)$`)

// ParseConfig parses the main section of a raw Proxmox QEMU config file.
//
// The digest field of the returned VMConfig covers the entire raw input,
// including any named sections beyond the main section.
//
// Parsing stops at the first line that begins with '[' (a named section
// header), so [PENDING], snapshot sections, and [special:*] sections are
// all silently ignored.
func ParseConfig(raw []byte) (*VMConfig, error) {
	cfg := &VMConfig{
		Digest:   sha256.Sum256(raw),
		Networks: make(map[int]NetworkDevice),
		Raw:      make(map[string]string),
	}

	var descLines []string
	scanner := bufio.NewScanner(bytes.NewReader(raw))

	for scanner.Scan() {
		line := scanner.Text()

		// Named section header: stop processing the main section.
		if strings.HasPrefix(line, "[") {
			break
		}

		// Comment line: accumulate into description.
		if strings.HasPrefix(line, "#") {
			// Strip the '#' and one optional space.
			rest := line[1:]
			if len(rest) > 0 && rest[0] == ' ' {
				rest = rest[1:]
			}
			descLines = append(descLines, rest)
			continue
		}

		// Blank line.
		if strings.TrimSpace(line) == "" {
			continue
		}

		// Key: value — split on the first colon only.
		rawKey, rawVal, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key := strings.TrimSpace(rawKey)
		val := strings.TrimSpace(rawVal)

		switch key {
		case "name":
			cfg.Name = val
		case "ostype":
			cfg.OSType = val
		case "tags":
			cfg.Tags = parseTags(val)
		case "description":
			// An explicit "description:" key overrides comment-accumulated lines.
			cfg.Description = val
		default:
			if m := netKeyRe.FindStringSubmatch(key); m != nil {
				idx, _ := strconv.Atoi(m[1])
				dev, err := parseNetworkDevice(val)
				if err != nil {
					return nil, fmt.Errorf("parse %s: %w", key, err)
				}
				cfg.Networks[idx] = dev
			} else {
				cfg.Raw[key] = val
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan config: %w", err)
	}

	// Only use comment-accumulated description if no explicit "description:" key
	// was present (the switch case above sets cfg.Description directly).
	if cfg.Description == "" && len(descLines) > 0 {
		cfg.Description = strings.Join(descLines, "\n")
	}

	return cfg, nil
}

// parseTags splits a semicolon-separated tag string into a []string.
// Tags are trimmed of surrounding whitespace. Returns nil for empty input.
func parseTags(val string) []string {
	if val == "" {
		return nil
	}
	parts := strings.Split(val, ";")
	tags := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			tags = append(tags, t)
		}
	}
	if len(tags) == 0 {
		return nil
	}
	return tags
}

// parseNetworkDevice parses the value of a "netN" key.
//
// The format is:
//
//	model=MAC[,option=value...]
//
// For example:
//
//	virtio=BC:24:11:2C:69:EC,bridge=vnet0,firewall=1
//	e1000=DE:AD:BE:EF:00:01,bridge=vmbr0,tag=10
func parseNetworkDevice(val string) (NetworkDevice, error) {
	var dev NetworkDevice

	parts := strings.Split(val, ",")
	for i, part := range parts {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			if i == 0 {
				return dev, fmt.Errorf("missing '=' in model=MAC pair: %q", part)
			}
			continue // ignore option tokens without '='
		}

		if i == 0 {
			// First token: model=MAC
			dev.Model = k
			mac, err := net.ParseMAC(v)
			if err != nil {
				return dev, fmt.Errorf("parse MAC %q: %w", v, err)
			}
			dev.MAC = mac
			continue
		}

		switch k {
		case "bridge":
			dev.Bridge = v
		case "firewall":
			dev.Firewall = v == "1"
		case "tag":
			if n, err := strconv.Atoi(v); err == nil {
				dev.Tag = n
			}
		case "mtu":
			if n, err := strconv.Atoi(v); err == nil {
				dev.MTU = n
			}
		}
	}

	return dev, nil
}
