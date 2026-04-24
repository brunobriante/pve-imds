package imds

import "strings"

const userDataOpenTag = "<!--#user-data"
const networkConfigOpenTag = "<!--#network-config"
const blockPrefix = "<!--#"
const userDataCloseTag = "-->"

// findBlockEnd finds the position of the closing --> for the current block.
// It uses LastIndex to allow --> sequences inside the content, but bounds the
// search to content before any subsequent <!--# block marker so that multiple
// blocks can coexist in any order.
func findBlockEnd(content string) int {
	bound := len(content)
	if idx := strings.Index(content, blockPrefix); idx > 0 {
		bound = idx
	}
	return strings.LastIndex(content[:bound], userDataCloseTag)
}

// ParseNetworkConfig extracts embedded network configuration from a Proxmox VM
// description. The format follows the same HTML comment block convention as
// user-data, wrapping a standard cloud-init network-config document (v1 or v2):
//
//	<!--#network-config
//	network:
//	  version: 2
//	  ethernets:
//	    net0:
//	      addresses:
//	        - 192.168.1.10/24
//	      routes:
//	        - to: 0.0.0.0/0
//	          via: 192.168.1.1
//	-->
//
// Returns ("", false) when no opening tag is present, no closing "-->" follows
// the opening tag, or the trimmed content is empty.
func ParseNetworkConfig(description string) (string, bool) {
	_, after, ok := strings.Cut(description, networkConfigOpenTag)
	if !ok {
		return "", false
	}

	end := findBlockEnd(after)
	if end < 0 {
		return "", false
	}

	content := strings.TrimSpace(after[:end])
	if content == "" {
		return "", false
	}

	return content, true
}

// ParseUserData extracts embedded user-data from a Proxmox VM description.
//
// User-data is declared with an HTML comment block starting with the literal
// text "<!--#user-data" and ending at the last occurrence of "-->" before any
// subsequent <!--# block marker:
//
//	<!--#user-data
//	#cloud-config
//	users:
//	  - default
//	-->
//	other unparsed text
//
// The content between the opening tag and the closing "-->" is stripped of
// leading and trailing whitespace and returned. Using the last occurrence of
// "-->" (bounded by any subsequent block) rather than the first allows the
// user-data body to contain "-->" sequences without prematurely ending the
// block.
//
// Returns ("", false) when no opening tag is present, no closing "-->" follows
// the opening tag, or the trimmed content is empty.
func ParseUserData(description string) (string, bool) {
	_, after, ok := strings.Cut(description, userDataOpenTag)
	if !ok {
		return "", false
	}

	end := findBlockEnd(after)
	if end < 0 {
		return "", false
	}

	userData := strings.TrimSpace(after[:end])
	if userData == "" {
		return "", false
	}

	return userData, true
}
