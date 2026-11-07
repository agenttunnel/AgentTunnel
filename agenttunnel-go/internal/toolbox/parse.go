package toolbox

import (
	"strings"
	"unicode"

	"github.com/agenttunnel/agenttunnel-go/internal/contracts"
)

// ParseCommandLine recognizes a tool invocation printed by an agent when the
// harness did not execute it. Models sometimes answer with the command text
// instead of calling their shell tool; accepting that text keeps dispatch
// working across harnesses while still using the same inbox as a real
// `atd tool` call.
//
// The accepted shapes are deliberately narrow: a known tool binary or the bare
// `tool <kind>` prefix, followed by that kind. Prose that merely mentions the
// tool is ignored.
func ParseCommandLine(line string) (Call, bool) {
	tokens := shellFields(strings.TrimSpace(line))
	if len(tokens) < 2 {
		return Call{}, false
	}
	start := -1
	for i := 0; i+1 < len(tokens); i++ {
		if tokens[i] != "tool" {
			continue
		}
		if !knownKind(tokens[i+1]) {
			continue
		}
		if i == 0 || isToolBinary(tokens[i-1]) {
			start = i
			break
		}
	}
	if start < 0 {
		return Call{}, false
	}

	call := Call{Kind: Kind(tokens[start+1])}
	args := tokens[start+2:]
	for i := 0; i < len(args); i++ {
		flag := args[i]
		value := ""
		if j := strings.IndexByte(flag, '='); j > 0 {
			value = flag[j+1:]
			flag = flag[:j]
		} else if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			value = args[i+1]
			i++
		}
		switch flag {
		case "--role":
			call.Role = contracts.RoleID(value)
		case "--name":
			call.Name = value
		case "--task":
			call.Task = value
		case "--harness":
			call.Harness = value
		case "--model":
			call.Model = value
		case "--lane":
			call.Lane = value
		case "--text":
			call.Text = value
		case "--summary":
			call.Text = value
		case "--to":
			call.To = value
		case "--depends-on":
			for _, dep := range strings.Split(value, ",") {
				if dep = strings.TrimSpace(dep); dep != "" {
					call.DependsOn = append(call.DependsOn, dep)
				}
			}
		}
	}
	if call.Role == "" {
		call.Role = contracts.RoleExecutor
	}
	switch call.Kind {
	case KindDispatch:
		return call, strings.TrimSpace(call.Task) != ""
	case KindNote, KindProgress, KindDone, KindLearn:
		return call, strings.TrimSpace(call.Text) != ""
	case KindMessage:
		return call, strings.TrimSpace(call.Text) != "" && strings.TrimSpace(call.To) != ""
	default:
		return Call{}, false
	}
}

func knownKind(s string) bool {
	switch Kind(s) {
	case KindDispatch, KindNote, KindProgress, KindDone, KindLearn, KindMessage:
		return true
	default:
		return false
	}
}

// isToolBinary accepts the forms an agent may print for the tool executable.
func isToolBinary(token string) bool {
	token = strings.Trim(token, `"'`)
	if strings.Contains(token, "AT_TOOL") {
		return true
	}
	base := token
	if idx := strings.LastIndexByte(base, '/'); idx >= 0 {
		base = base[idx+1:]
	}
	return base == "atd" || base == "agenttunnel" || base == "tunnel" || base == "tool"
}

// shellFields splits a command line into arguments, honoring single and double
// quotes and backslash escapes. It is intentionally small: the agent is printing
// a command, not running a full shell parser.
func shellFields(s string) []string {
	var out []string
	var b strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for _, r := range s {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if unicode.IsSpace(r) {
			flush()
			continue
		}
		if r == '$' && b.Len() == 0 {
			// Keep "$AT_TOOL" intact so isToolBinary can recognize it.
			b.WriteRune(r)
			continue
		}
		b.WriteRune(r)
	}
	flush()
	return out
}
