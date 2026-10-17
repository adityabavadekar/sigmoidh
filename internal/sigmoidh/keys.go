package sigmoidh

import "github.com/chzyer/readline"

// translateKey remaps terminal quirks onto readline's emacs bindings:
//   - Ctrl+Backspace (0x08 on most terminals) → delete previous word
//
// Alt+Backspace and Ctrl+W are already native word-deletes.
func translateKey(r rune) (rune, bool) {
	if r == 8 { // readline.CharCtrlH
		return readline.CharCtrlW, true
	}
	return r, true
}
