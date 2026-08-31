package jobs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/synrise25/rss-pod/internal/config"
)

// decodeGeneratedScript first applies the normal strict decoder. Repair is only
// attempted when that fails, and the repaired document must pass the same strict
// schema and business validation as an untouched response.
func decodeGeneratedScript(raw []byte, speakers []config.SpeakerConfig) (generatedScript, []byte, []string, error) {
	script, err := decodeGeneratedScriptStrict(raw)
	if err == nil {
		if err := validateScript(script, speakers); err != nil {
			return generatedScript{}, nil, nil, err
		}
		canonical, err := json.Marshal(script)
		return script, canonical, nil, err
	}

	repaired, repairs := repairGeneratedScriptJSON(raw)
	if len(repairs) == 0 {
		return generatedScript{}, nil, nil, err
	}
	repairedScript, repairedErr := decodeGeneratedScriptStrict(repaired)
	if repairedErr != nil {
		return generatedScript{}, nil, repairs, fmt.Errorf("original JSON: %v; repaired JSON: %w", err, repairedErr)
	}
	if repairedErr := validateScript(repairedScript, speakers); repairedErr != nil {
		return generatedScript{}, nil, repairs, fmt.Errorf("repaired JSON failed script validation: %w", repairedErr)
	}
	canonical, repairedErr := json.Marshal(repairedScript)
	if repairedErr != nil {
		return generatedScript{}, nil, repairs, repairedErr
	}
	return repairedScript, canonical, repairs, nil
}

func decodeGeneratedScriptStrict(raw []byte) (generatedScript, error) {
	if !utf8.Valid(raw) {
		return generatedScript{}, fmt.Errorf("JSON is not valid UTF-8")
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return generatedScript{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var script generatedScript
	if err := decoder.Decode(&script); err != nil {
		return generatedScript{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return generatedScript{}, fmt.Errorf("JSON contains more than one value")
		}
		return generatedScript{}, err
	}
	return script, nil
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var visitValue func() error
	visitValue = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate JSON key %q", key)
				}
				seen[key] = struct{}{}
				if err := visitValue(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := visitValue(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
		}
	}
	return visitValue()
}

func repairGeneratedScriptJSON(raw []byte) ([]byte, []string) {
	repairs := newRepairLog()
	value := repairJSONStringSyntax(raw, repairs)
	value = repairTurnObjects(value, repairs)
	value = removeTrailingJSONCommas(value, repairs)
	return value, repairs.values
}

type repairLog struct {
	seen   map[string]struct{}
	values []string
}

func newRepairLog() *repairLog {
	return &repairLog{seen: make(map[string]struct{})}
}

func (r *repairLog) add(value string) {
	if _, exists := r.seen[value]; exists {
		return
	}
	r.seen[value] = struct{}{}
	r.values = append(r.values, value)
}

// repairJSONStringSyntax only changes representations whose decoded character
// is unambiguous: a common escaped underscore, or a raw control character.
func repairJSONStringSyntax(raw []byte, repairs *repairLog) []byte {
	var output bytes.Buffer
	output.Grow(len(raw))
	inString := false
	for index := 0; index < len(raw); index++ {
		current := raw[index]
		if !inString {
			output.WriteByte(current)
			if current == '"' {
				inString = true
			}
			continue
		}

		switch current {
		case '"':
			output.WriteByte(current)
			inString = false
		case '\\':
			if index+1 < len(raw) && raw[index+1] == '_' {
				output.WriteByte('_')
				index++
				repairs.add("escaped underscore")
				continue
			}
			output.WriteByte(current)
			if index+1 < len(raw) {
				output.WriteByte(raw[index+1])
				index++
			}
		default:
			if current < 0x20 {
				fmt.Fprintf(&output, `\u%04x`, current)
				repairs.add("raw control character in string")
				continue
			}
			output.WriteByte(current)
		}
	}
	return output.Bytes()
}

type repairFrame struct {
	kind         byte
	turnsArray   bool
	turnObject   bool
	currentKey   string
	lastValueKey string
	hasSpeaker   bool
	hasText      bool
}

func repairTurnObjects(raw []byte, repairs *repairLog) []byte {
	var output bytes.Buffer
	output.Grow(len(raw))
	stack := make([]repairFrame, 0, 4)

	for index := 0; index < len(raw); {
		if raw[index] == '"' {
			end, ok := jsonStringEnd(raw, index)
			if !ok {
				output.Write(raw[index:])
				break
			}
			token := raw[index:end]
			var decoded string
			if err := json.Unmarshal(token, &decoded); err != nil {
				output.Write(token)
				index = end
				continue
			}
			isKey := nextNonSpaceByte(raw, end) == ':' && len(stack) > 0 && stack[len(stack)-1].kind == '{'
			if isKey {
				frame := &stack[len(stack)-1]
				if frame.turnObject && decoded == "speer_id" {
					decoded = "speaker_id"
					token = []byte(`"speaker_id"`)
					repairs.add("turn key speer_id -> speaker_id")
				}
				frame.currentKey = decoded
				if frame.turnObject && decoded == "speaker_id" {
					frame.hasSpeaker = true
				}
				if frame.turnObject && decoded == "text" {
					frame.hasText = true
				}
			} else if len(stack) > 0 && stack[len(stack)-1].kind == '{' {
				frame := &stack[len(stack)-1]
				frame.lastValueKey = frame.currentKey
				frame.currentKey = ""
			}
			output.Write(token)
			index = end
			continue
		}

		current := raw[index]
		switch current {
		case '{':
			if missingTurnBoundary(stack) && nextObjectLooksLikeTurn(raw[index:]) {
				output.WriteString("},")
				stack = stack[:len(stack)-1]
				repairs.add("missing delimiter between turns")
			}
			turnObject := len(stack) > 0 && stack[len(stack)-1].kind == '[' && stack[len(stack)-1].turnsArray
			stack = append(stack, repairFrame{kind: '{', turnObject: turnObject})
		case '[':
			turnsArray := len(stack) > 0 && stack[len(stack)-1].kind == '{' && stack[len(stack)-1].currentKey == "turns"
			if len(stack) > 0 && stack[len(stack)-1].kind == '{' {
				stack[len(stack)-1].currentKey = ""
			}
			stack = append(stack, repairFrame{kind: '[', turnsArray: turnsArray})
		case '}', ']':
			if len(stack) > 0 && ((current == '}' && stack[len(stack)-1].kind == '{') || (current == ']' && stack[len(stack)-1].kind == '[')) {
				stack = stack[:len(stack)-1]
			}
		}
		output.WriteByte(current)
		index++
	}
	return output.Bytes()
}

func missingTurnBoundary(stack []repairFrame) bool {
	if len(stack) < 2 {
		return false
	}
	turn := stack[len(stack)-1]
	parent := stack[len(stack)-2]
	return turn.kind == '{' && turn.turnObject && turn.hasSpeaker && turn.hasText && turn.lastValueKey == "text" &&
		parent.kind == '[' && parent.turnsArray
}

func nextObjectLooksLikeTurn(raw []byte) bool {
	index := 1
	for index < len(raw) && isJSONSpace(raw[index]) {
		index++
	}
	if index >= len(raw) || raw[index] != '"' {
		return false
	}
	end, ok := jsonStringEnd(raw, index)
	if !ok || nextNonSpaceByte(raw, end) != ':' {
		return false
	}
	var key string
	if err := json.Unmarshal(raw[index:end], &key); err != nil {
		return false
	}
	return key == "speaker_id" || key == "speer_id"
}

func jsonStringEnd(raw []byte, start int) (int, bool) {
	for index := start + 1; index < len(raw); index++ {
		if raw[index] == '\\' {
			index++
			continue
		}
		if raw[index] == '"' {
			return index + 1, true
		}
	}
	return len(raw), false
}

func nextNonSpaceByte(raw []byte, start int) byte {
	for index := start; index < len(raw); index++ {
		if !isJSONSpace(raw[index]) {
			return raw[index]
		}
	}
	return 0
}

func isJSONSpace(value byte) bool {
	return value == ' ' || value == '\n' || value == '\r' || value == '\t'
}

func removeTrailingJSONCommas(raw []byte, repairs *repairLog) []byte {
	var output bytes.Buffer
	output.Grow(len(raw))
	inString := false
	for index := 0; index < len(raw); index++ {
		current := raw[index]
		if inString {
			output.WriteByte(current)
			if current == '\\' && index+1 < len(raw) {
				output.WriteByte(raw[index+1])
				index++
			} else if current == '"' {
				inString = false
			}
			continue
		}
		if current == '"' {
			inString = true
			output.WriteByte(current)
			continue
		}
		if current == ',' {
			next := nextNonSpaceByte(raw, index+1)
			if next == '}' || next == ']' {
				repairs.add("trailing comma")
				continue
			}
		}
		output.WriteByte(current)
	}
	return output.Bytes()
}

func formatRepairs(repairs []string) string {
	return strings.Join(repairs, ", ")
}
